package mediajob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/remux"
)

// syncRecorder collects checkpoints from RunRanged's goroutine for readers on others.
type syncRecorder struct {
	mu  sync.Mutex
	cps []Checkpoint
}

func (r *syncRecorder) onCheckpoint(cp Checkpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cps = append(r.cps, cp)
	return nil
}

func (r *syncRecorder) all() []Checkpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.cps)
}

// A resumed track that starts over (its file no longer has the planned size) has its
// bits dropped from the stored checkpoint before its file is recreated: a crash while
// its first chunk is fetched again then never leaves old bits over a new, zero-filled
// file. The other track keeps its bits.
func TestRunRangedRestartedTrackCheckpointedFirst(t *testing.T) {
	s := newSiteServer(t)
	sel, _, _ := pairSelection(t, s, "/")
	opts := rangedOptions(t)
	cp := interrupted(t, s, opts, sel)
	if !bitSet(cp.Tracks[0].Done, 0) {
		t.Fatal("the interruption left no video chunk")
	}
	if err := os.Truncate(filepath.Join(opts.PartDir, "video-137", dataName), 100); err != nil {
		t.Fatal(err)
	}
	var rec syncRecorder
	var cleared, checked atomic.Bool
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if r.URL.Path == "/v.mp4" && !checked.Swap(true) {
			// The first request for the recreated video track: by now the stored
			// checkpoint must say that none of its chunks is durable.
			cps := rec.all()
			if len(cps) > 0 {
				last := cps[len(cps)-1]
				cleared.Store(!slices.ContainsFunc(last.Tracks[0].Done, func(b byte) bool { return b != 0 }) &&
					bytes.Equal(last.Tracks[1].Done, cp.Tracks[1].Done))
			}
		}
		return false
	})
	if _, err := RunRanged(testContext(t), opts, sel, cp, nil, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	if !checked.Load() || !cleared.Load() {
		t.Errorf("the video track was fetched again before a checkpoint without its old bits was stored (checkpoints %d)",
			len(rec.all()))
	}
}

// A failing checkpoint store before a track is recreated fails the run and leaves the
// track's file as it was.
func TestRunRangedRestartNeedsTheCheckpoint(t *testing.T) {
	s := newSiteServer(t)
	sel, _, _ := pairSelection(t, s, "/")
	opts := rangedOptions(t)
	cp := interrupted(t, s, opts, sel)
	video := filepath.Join(opts.PartDir, "video-137", dataName)
	if err := os.Truncate(video, 100); err != nil {
		t.Fatal(err)
	}
	storeErr := errors.New("disk gone")
	_, err := RunRanged(testContext(t), opts, sel, cp, nil, func(Checkpoint) error { return storeErr })
	if !errors.Is(err, storeErr) {
		t.Fatalf("RunRanged = %v, want the store's error", err)
	}
	if fi, err := os.Stat(video); err != nil || fi.Size() != 100 {
		t.Errorf("the video file was recreated before the checkpoint was stored (%v)", err)
	}
	if n := len(s.requests("")); n != 0 {
		t.Errorf("%d requests were made", n)
	}
}

// instantClock fires every timer at once: retry waits take no time.
type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }

func (instantClock) NewTimer(time.Duration) Timer {
	c := make(chan time.Time, 1)
	c <- time.Now()
	return &fakeTimer{c: c, stopped: true}
}

// A track file that exists but cannot be opened for writing fails the run with its
// progress kept rather than starting the track over; once it can be opened again, the
// run resumes from the same bits.
func TestRunRangedTrackFileThatCannotBeOpened(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	opts := rangedOptions(t)
	cp := interrupted(t, s, opts, sel)
	video := filepath.Join(opts.PartDir, "video-137", dataName)
	before, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(video, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(video, 0o600) })
	if f, err := os.OpenFile(video, os.O_RDWR, 0); err == nil {
		f.Close()
		t.Skip("a read-only file can still be opened for writing here (running as root?)")
	}
	failing := opts
	failing.Clock = instantClock{}
	var rec syncRecorder
	if _, err := RunRanged(testContext(t), failing, sel, cp, nil, rec.onCheckpoint); err == nil {
		t.Fatal("RunRanged succeeded with a track file it cannot open")
	}
	if got, err := os.ReadFile(video); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("the track file changed (%v)", err)
	}
	for _, c := range rec.all() {
		if !bytes.Equal(c.Tracks[0].Done, cp.Tracks[0].Done) {
			t.Fatalf("a checkpoint dropped the video's bits: %v", c.Tracks[0].Done)
		}
	}
	if err := os.Chmod(video, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
}

// fullVolume is what a write to a full volume returns on this system.
func fullVolume() error {
	if runtime.GOOS == "windows" {
		return syscall.Errno(112) // ERROR_DISK_FULL
	}
	return syscall.ENOSPC
}

// A volume that fills while the tracks are joined fails the job with ErrDiskFull (the
// daemon's disk_full), for RunRanged and Run alike, and leaves no output.
func TestDiskFullWhileAssembling(t *testing.T) {
	full := func(ctx context.Context, in []remux.Input, out string) (string, error) {
		return "", fmt.Errorf("remux: write output: %w", &fs.PathError{Op: "write", Path: out, Err: fullVolume()})
	}
	s := newSiteServer(t)
	sel, _, _ := pairSelection(t, s, "/")
	opts := rangedOptions(t)
	opts.Assemble = full
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); !errors.Is(err, ErrDiskFull) {
		t.Errorf("RunRanged = %v, want ErrDiskFull", err)
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RunRanged left an output: %v", err)
	}

	m := newServer(t)
	addTSPlaylist(m, "/o/", 2, 5)
	mopts := testOptions(t, nil)
	mopts.Assemble = full
	if _, err := Run(testContext(t), mopts, Selection{ManifestURL: m.url("/o/index.m3u8")}, nil, nil, nil); !errors.Is(err, ErrDiskFull) {
		t.Errorf("Run = %v, want ErrDiskFull", err)
	}
}

// A size the site declared smaller than the resource is caught, never published cut: an
// unranged track whose 206 names the real total or whose 200 is longer, and a copied
// file whose boxes run past its end (a server that answers "bytes a-b/*").
func TestRunRangedDeclaredSizeTooSmall(t *testing.T) {
	mvhd := box("mvhd", make([]byte, 100))
	trak := box("trak", box("tkhd", make([]byte, 84)))
	data := slices.Concat(ftyp(), box("moov", mvhd, trak), box("mdat", filler(1500<<10, 6)))
	declared := int64(len(data) - 400<<10)
	for _, tc := range []struct {
		name     string
		unranged bool
		answer   func(w http.ResponseWriter, r *http.Request) bool
	}{
		{"unranged, real total sent", true, nil},
		{"unranged, longer 200", true, func(w http.ResponseWriter, r *http.Request) bool {
			r.Header.Del("Range")
			return false
		}},
		{"ranged, total *", false, func(w http.ResponseWriter, r *http.Request) bool {
			first, last := parseRange(t, r.Header.Get("Range"))
			last = min(last, int64(len(data)-1))
			w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(first, 10)+"-"+strconv.FormatInt(last, 10)+"/*")
			w.Header().Set("Content-Length", strconv.FormatInt(last-first+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[first : last+1])
			return true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			s.add("/18.mp4", data)
			if tc.answer != nil {
				s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool { return tc.answer(w, r) })
			}
			tr := rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), declared)
			tr.Unranged = tc.unranged
			opts := rangedOptions(t)
			opts.MaxRetries = 1
			_, err := RunRanged(testContext(t), opts, RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{tr}}, nil, nil, nil)
			if err == nil {
				t.Fatalf("a cut copy of %d of %d bytes was published as complete", declared, len(data))
			}
			if !errors.Is(err, ErrRangeMismatch) && !errors.Is(err, media.ErrMalformed) {
				t.Errorf("RunRanged = %v, want a range mismatch or a malformed file", err)
			}
			if _, err := os.Stat(opts.OutPath); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("an output was written: %v", err)
			}
		})
	}
}
