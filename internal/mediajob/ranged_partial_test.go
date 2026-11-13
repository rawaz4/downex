package mediajob

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/media"
)

// Chunk prefixes (docs/SITE-MEDIA-SPEC.md §2.4 "When a chunk fails the job", §2.5): what
// a chunk received before a pause, a failure or a retry is kept once it is synced, and the
// chunk continues after it.

// sendPart answers a range request of data with a 206 for the whole range that sends
// only its first n bytes; the caller then holds the response open.
func sendPart(t *testing.T, w http.ResponseWriter, r *http.Request, data []byte, n int64) {
	t.Helper()
	a, b := parseRange(t, r.Header.Get("Range"))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(a, 10)+"-"+strconv.FormatInt(b, 10)+"/"+strconv.Itoa(len(data)))
	w.Header().Set("Content-Length", strconv.FormatInt(b-a+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[a : a+n])
	w.(http.Flusher).Flush()
}

// progressLog collects progress events from RunRanged's goroutine for readers on others.
type progressLog struct {
	mu sync.Mutex
	ps []Progress
}

func (l *progressLog) onProgress(p Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ps = append(l.ps, p)
}

func (l *progressLog) all() []Progress {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ps)
}

func (l *progressLog) last() Progress {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ps) == 0 {
		return Progress{}
	}
	return l.ps[len(l.ps)-1]
}

// checkNeverBack fails when the bytes done of ps ever decrease.
func checkNeverBack(t *testing.T, name string, ps []Progress) {
	t.Helper()
	for i := 1; i < len(ps); i++ {
		if ps[i].BytesDone < ps[i-1].BytesDone {
			t.Errorf("%s: progress went back from %d to %d bytes (event %d of %d)", name, ps[i-1].BytesDone, ps[i].BytesDone, i, len(ps))
			return
		}
	}
}

// pauseSpec is a paused first run: hold answers the requests it takes on its own (the
// part of a chunk to send, or nothing), ready says when to pause, opts changes the run's
// options; none allows a run that stores no checkpoint (nothing became durable).
type pauseSpec struct {
	hold  func(t *testing.T, w http.ResponseWriter, r *http.Request) bool
	ready func(p Progress, reqs []siteReq) bool
	opts  func(o *Options)
	none  bool
}

// pausedRun runs sel against a server of its own with the files of s (so that s records
// only the requests of a later run, as in interrupted) until spec.ready, then cancels it
// and returns the last checkpoint and every progress event of the run.
func pausedRun(t *testing.T, s *siteServer, opts Options, sel RangedSelection, spec pauseSpec) (*Checkpoint, []Progress) {
	t.Helper()
	return pausedRunWith(t, s, opts, sel, spec, nil)
}

// halfOfVideoChunk1 is a pauseSpec for the fixture pair: video chunk 1 sends half its
// bytes and stops; the run pauses once every other chunk is written as well.
func halfOfVideoChunk1(vdata []byte, total int64) pauseSpec {
	const c = int64(media.MinChunkBytes)
	target := chunkRange(1, c, int64(len(vdata)))
	var sent atomic.Bool
	return pauseSpec{
		hold: func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasSuffix(r.URL.Path, "/v.mp4") || r.Header.Get("Range") != target {
				return false
			}
			sendPart(t, w, r, vdata, c/2)
			sent.Store(true)
			return true
		},
		ready: func(p Progress, _ []siteReq) bool { return sent.Load() && p.BytesDone >= total-c/2 },
	}
}

// A paused site video keeps what it received of a chunk in flight: the received half of
// video chunk 1 is synced and recorded at the pause; the progress never goes back (at
// the pause, nor on resume, which starts from the bytes the checkpoint records); the
// resumed run asks only for the rest of that chunk, and the output is byte-exact.
func TestRunRangedPauseKeepsPartialChunk(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	vsize, total := int64(len(v.data)), int64(len(v.data)+len(a.data))
	const c, half = int64(media.MinChunkBytes), int64(media.MinChunkBytes) / 2
	opts := rangedOptions(t)
	cp, first := pausedRun(t, s, opts, sel, halfOfVideoChunk1(v.data, total))

	checkNeverBack(t, "paused run", first)
	last := first[len(first)-1]
	if want := total - c + half; cp.DurableBytes != want || last.BytesDone != want || last.FragmentsDone != 4 ||
		last.BytesTotal != total {
		t.Errorf("at the pause: checkpoint %d durable bytes, progress %+v; want %d bytes, 4 of 5 parts, total %d",
			cp.DurableBytes, last, want, total)
	}
	if cp.BytesTotal != total {
		t.Errorf("checkpoint BytesTotal %d, want %d", cp.BytesTotal, total)
	}
	if vt := cp.Tracks[0]; !maps.Equal(vt.Partial, map[int]int64{1: half}) || !bitSet(vt.Done, 0) || bitSet(vt.Done, 1) || !bitSet(vt.Done, 2) {
		t.Errorf("video checkpoint %+v, want chunks 0 and 2 durable and %d bytes of chunk 1", vt, half)
	}
	if at := cp.Tracks[1]; len(at.Partial) != 0 {
		t.Errorf("audio checkpoint %+v has prefixes", at)
	}

	var resumed progressLog
	if _, err := RunRanged(testContext(t), opts, sel, cp, resumed.onProgress, nil); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	want := "/v.mp4 bytes=" + strconv.FormatInt(c+half, 10) + "-" + strconv.FormatInt(min(2*c, vsize)-1, 10)
	if got := rangesOf(s.requests("")); !slices.Equal(got, []string{want}) {
		t.Errorf("resumed requests %v, want only the rest of video chunk 1: %s", got, want)
	}
	ps := resumed.all()
	checkNeverBack(t, "resumed run", ps)
	if ps[0].BytesDone != cp.DurableBytes || ps[len(ps)-1].BytesDone != total {
		t.Errorf("resumed progress from %d to %d bytes, want from %d to %d", ps[0].BytesDone, ps[len(ps)-1].BytesDone, cp.DurableBytes, total)
	}
}

// Kill points: a crash after a chunk's bytes were written but before they were synced,
// after the sync but before the prefix was recorded, or after the checkpoint, leaves a
// checkpoint whose prefix is at most what is durable; whatever lies beyond it in the file
// (lost, torn or stale writes) is fetched again, and the output is byte-exact.
func TestRunRangedPartialKillPoints(t *testing.T) {
	const c, half = int64(media.MinChunkBytes), int64(media.MinChunkBytes) / 2
	for _, tc := range []struct {
		name    string
		prefix  int64 // the prefix the stored checkpoint records
		garbage bool  // the bytes after it are not the chunk's
	}{
		{"after the write, before the sync", 0, true},
		{"after the sync, before the prefix was recorded", 0, false},
		{"after the checkpoint", half, true},
		{"an older, shorter prefix", half / 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			sel, v, a := pairSelection(t, s, "/")
			vsize, total := int64(len(v.data)), int64(len(v.data)+len(a.data))
			opts := rangedOptions(t)
			cp, _ := pausedRun(t, s, opts, sel, halfOfVideoChunk1(v.data, total))
			if cp.Tracks[0].Partial[1] != half {
				t.Fatalf("checkpoint %+v, want %d bytes of video chunk 1", cp.Tracks[0], half)
			}
			cp.Tracks[0].Partial = map[int]int64{1: tc.prefix}
			if tc.prefix == 0 {
				cp.Tracks[0].Partial = nil
			}
			if tc.garbage {
				f, err := os.OpenFile(filepath.Join(opts.PartDir, "video-137", dataName), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteAt(bytes.Repeat([]byte{0xa5}, int(c-tc.prefix)), c+tc.prefix); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
				t.Fatal(err)
			}
			checkMP4(t, opts.OutPath, v.samples, a.samples)
			want := "/v.mp4 bytes=" + strconv.FormatInt(c+tc.prefix, 10) + "-" + strconv.FormatInt(min(2*c, vsize)-1, 10)
			if got := rangesOf(s.requests("")); !slices.Equal(got, []string{want}) {
				t.Errorf("resumed requests %v, want %s", got, want)
			}
		})
	}
}

// No prefix is recorded ahead of a sync: every prefix (and bit) a checkpoint records was
// in the track's file when a sync of it began, and with failing syncs nothing is recorded.
func TestRunRangedPartialNeedsSync(t *testing.T) {
	t.Run("recorded after a sync", func(t *testing.T) {
		s := newSiteServer(t)
		sel, v, a := pairSelection(t, s, "/")
		total := int64(len(v.data) + len(a.data))
		source := map[string][]byte{"video-137": v.data, "audio-140": a.data}
		var mu sync.Mutex
		synced := map[string][][]byte{} // what each track's file held when a sync of it began
		var cps []Checkpoint
		var seen []map[string]int // len(synced[track]) when each checkpoint was stored
		opts := rangedOptions(t)
		spec := halfOfVideoChunk1(v.data, total)
		spec.opts = func(o *Options) {
			o.syncFile = func(f *os.File) error {
				data, err := os.ReadFile(f.Name())
				if err != nil {
					return err
				}
				mu.Lock()
				dir := filepath.Base(filepath.Dir(f.Name()))
				synced[dir] = append(synced[dir], data)
				mu.Unlock()
				return f.Sync()
			}
		}
		_, _ = pausedRunWith(t, s, opts, sel, spec, func(cp Checkpoint) {
			mu.Lock()
			defer mu.Unlock()
			cps = append(cps, cp)
			n := map[string]int{}
			for k, d := range synced {
				n[k] = len(d)
			}
			seen = append(seen, n)
		})
		mu.Lock()
		defer mu.Unlock()
		const c = int64(media.MinChunkBytes)
		prefixes := 0
		for k, cp := range cps {
			for _, tc := range cp.Tracks {
				dir := map[string]string{"137": "video-137", "140": "audio-140"}[tc.TrackID]
				src := source[dir]
				covered := func(from, to int64) bool {
					for _, d := range synced[dir][:seen[k][dir]] {
						if bytes.Equal(d[from:to], src[from:to]) {
							return true
						}
					}
					return false
				}
				for i := range tc.Total {
					start := int64(i) * c
					if bitSet(tc.Done, i) && !covered(start, min(start+c, int64(len(src)))) {
						t.Errorf("checkpoint %d: %s chunk %d is durable before a sync saw its bytes", k, dir, i)
					}
				}
				for i, p := range tc.Partial {
					prefixes++
					if start := int64(i) * c; !covered(start, start+p) {
						t.Errorf("checkpoint %d: %s chunk %d has a prefix of %d bytes before a sync saw them", k, dir, i, p)
					}
				}
			}
		}
		if prefixes == 0 {
			t.Error("no checkpoint recorded a prefix")
		}
	})

	t.Run("failing syncs", func(t *testing.T) {
		s := newSiteServer(t)
		sel, v, _ := pairSelection(t, s, "/")
		const c = int64(media.MinChunkBytes)
		target := chunkRange(1, c, int64(len(v.data)))
		s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
			if r.URL.Path != "/v.mp4" || r.Header.Get("Range") != target {
				return false
			}
			sendPart(t, w, r, v.data, c/2)
			<-r.Context().Done()
			return true
		})
		broken := errors.New("the volume went away")
		opts := rangedOptions(t)
		opts.syncFile = func(*os.File) error { return broken }
		var rec syncRecorder
		_, err := RunRanged(testContext(t), opts, sel, nil, nil, rec.onCheckpoint)
		if !errors.Is(err, broken) {
			t.Fatalf("RunRanged = %v, want the sync's error", err)
		}
		for k, cp := range rec.all() {
			for _, tc := range cp.Tracks {
				if len(tc.Partial) > 0 || slices.ContainsFunc(tc.Done, func(b byte) bool { return b != 0 }) || cp.DurableBytes != 0 {
					t.Errorf("checkpoint %d records %+v (%d durable bytes) although no sync succeeded", k, tc, cp.DurableBytes)
				}
			}
		}
	})
}

// pausedRunWith is pausedRun with a checkpoint observer, called from RunRanged's
// goroutine (nil: none).
func pausedRunWith(t *testing.T, s *siteServer, opts Options, sel RangedSelection, spec pauseSpec,
	observe func(Checkpoint)) (*Checkpoint, []Progress) {
	t.Helper()
	first := newSiteServer(t)
	s.mu.Lock()
	for p, data := range s.files {
		first.add(p, data)
	}
	s.mu.Unlock()
	sel.Tracks = slices.Clone(sel.Tracks)
	for i := range sel.Tracks {
		sel.Tracks[i].URL = first.url(strings.TrimPrefix(sel.Tracks[i].URL, s.ts.URL))
	}
	first.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if spec.hold(t, w, r) {
			<-r.Context().Done()
			return true
		}
		return false
	})
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var log progressLog
	go func() {
		defer cancel()
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if spec.ready(log.last(), first.requests("")) {
				return
			}
		}
		t.Error("the first run never became ready to pause")
	}()
	o := opts
	o.CheckpointInterval = 10 * time.Millisecond
	if spec.opts != nil {
		spec.opts(&o)
	}
	var rec syncRecorder
	onCheckpoint := func(cp Checkpoint) error {
		if observe != nil {
			observe(cp)
		}
		return rec.onCheckpoint(cp)
	}
	if _, err := RunRanged(ctx, o, sel, nil, log.onProgress, onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("paused run = %v, want context.Canceled", err)
	}
	cps := rec.all()
	switch {
	case len(cps) > 0:
		return &cps[len(cps)-1], log.all()
	case !spec.none:
		t.Fatal("no checkpoint")
	}
	return nil, log.all()
}

// A prefix survives an address renewal: the resumed range answered 403 is asked again at
// the same offset from the renewed address.
func TestRunRangedPartialAcrossRefresh(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/old/")
	vsize, total := int64(len(v.data)), int64(len(v.data)+len(a.data))
	const c, half = int64(media.MinChunkBytes), int64(media.MinChunkBytes) / 2
	s.add("/new/v.mp4", v.data)
	s.add("/new/a.m4a", a.data)
	opts := rangedOptions(t)
	cp, _ := pausedRun(t, s, opts, sel, halfOfVideoChunk1(v.data, total))
	if cp.Tracks[0].Partial[1] != half {
		t.Fatalf("checkpoint %+v, want %d bytes of video chunk 1", cp.Tracks[0], half)
	}
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if strings.HasPrefix(r.URL.Path, "/old/") {
			w.WriteHeader(http.StatusForbidden) // the address expired during the pause
			return true
		}
		return false
	})
	var refreshes atomic.Int32
	sel.Refresh = func(ctx context.Context) ([]media.RangedTrack, error) {
		refreshes.Add(1)
		return withURLs(sel, s, "/new/"), nil
	}
	if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	rest := "bytes=" + strconv.FormatInt(c+half, 10) + "-" + strconv.FormatInt(min(2*c, vsize)-1, 10)
	if got := rangesOf(s.requests("")); !slices.Equal(got, []string{"/old/v.mp4 " + rest, "/new/v.mp4 " + rest}) {
		t.Errorf("resumed requests %v, want the rest of chunk 1 refused, then asked from the renewed address", got)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d renewals, want 1", n)
	}
}

// copiedTrack serves a progressive MP4 of 3 chunks (its 'moov' at the front) and selects
// it to be copied.
func copiedTrack(t *testing.T, s *siteServer, seed uint64) (RangedSelection, []byte) {
	t.Helper()
	data := progressiveFile(3<<20, seed)
	s.add("/18.mp4", data)
	return RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{
		rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), int64(len(data))),
	}}, data
}

// chunk0Prefix is a pauseSpec for copiedTrack: chunk 0 sends its first n bytes and stops,
// every other chunk is held without a byte; the run pauses once the n bytes are counted
// and, when others is set, the other chunks were asked for (the early pass).
func chunk0Prefix(data []byte, n int64, others bool) pauseSpec {
	first := chunkRange(0, media.MinChunkBytes, int64(len(data)))
	var sent atomic.Bool
	return pauseSpec{
		hold: func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
			if r.Header.Get("Range") == first {
				sendPart(t, w, r, data, n)
				sent.Store(true)
			}
			return true
		},
		ready: func(p Progress, reqs []siteReq) bool {
			return sent.Load() && p.BytesDone >= n && (!others || len(reqs) >= 3)
		},
	}
}

// A resumed chunk 0 with a synced prefix: when the prefix holds a 'moov' that passes the
// early check, the other chunks start at once, before any byte of chunk 0's rest
// arrives; when it does not, they wait for chunk 0 as before (§2.4). Both outputs are
// byte-exact.
func TestRunRangedPartialChunkZeroEarlyPass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix int64
		open   bool
	}{
		{"moov in the prefix", 256 << 10, true},
		{"moov not in the prefix", 40, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			sel, data := copiedTrack(t, s, 41)
			opts := rangedOptions(t)
			cp, _ := pausedRun(t, s, opts, sel, chunk0Prefix(data, tc.prefix, tc.open))
			if got := cp.Tracks[0].Partial; len(got) != 1 || got[0] != tc.prefix {
				t.Fatalf("checkpoint %+v, want a prefix of %d bytes of chunk 0", cp.Tracks[0], tc.prefix)
			}
			rest := "bytes=" + strconv.Itoa(int(tc.prefix)) + "-" + strconv.Itoa(media.MinChunkBytes-1)
			var othersWhileHeld atomic.Bool
			s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
				if r.Header.Get("Range") != rest {
					return false
				}
				// Chunk 0's rest waits for another chunk's request (at most 1 s).
				for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					if len(s.requests("")) > 1 {
						othersWhileHeld.Store(true)
						break
					}
				}
				return false
			})
			if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(readFile(t, opts.OutPath), data) {
				t.Fatal("the output differs from the served file")
			}
			if got := othersWhileHeld.Load(); got != tc.open {
				t.Errorf("other chunks asked for while chunk 0's rest was held: %v, want %v", got, tc.open)
			}
			want := []string{"/18.mp4 " + rest, "/18.mp4 " + chunkRange(1, media.MinChunkBytes, int64(len(data))),
				"/18.mp4 " + chunkRange(2, media.MinChunkBytes, int64(len(data)))}
			got := rangesOf(s.requests(""))
			if !tc.open && (len(got) == 0 || got[0] != want[0]) {
				t.Errorf("resumed requests %v, want the rest of chunk 0 first", got)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("resumed requests %v, want the rest of chunk 0 and chunks 1 and 2", got)
			}
		})
	}
}

// 200 answers and whole tracks: a resumed chunk 0 answered with the whole track drops its
// prefix and becomes the whole track; a 200 to a later chunk keeps its prefix and is
// retried; a track fetched as one whole resource never records a prefix and restarts
// from byte 0.
func TestRunRangedPartialWholeTrack(t *testing.T) {
	const c, half = int64(media.MinChunkBytes), int64(media.MinChunkBytes) / 2

	t.Run("a 200 to a resumed chunk 0", func(t *testing.T) {
		s := newSiteServer(t)
		sel, data := copiedTrack(t, s, 42)
		opts := rangedOptions(t)
		cp, _ := pausedRun(t, s, opts, sel, chunk0Prefix(data, 40, false))
		if cp.Tracks[0].Partial[0] != 40 {
			t.Fatalf("checkpoint %+v, want 40 bytes of chunk 0", cp.Tracks[0])
		}
		s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
			r.Header.Del("Range") // the server no longer honours ranges
			return false
		})
		var rec recorder
		if _, err := RunRanged(testContext(t), opts, sel, cp, rec.onProgress, rec.onCheckpoint); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readFile(t, opts.OutPath), data) {
			t.Fatal("the output differs from the served file")
		}
		want := []string{"/18.mp4 bytes=40-" + strconv.FormatInt(c-1, 10), "/18.mp4 " + chunkRange(0, c, int64(len(data)))}
		if got := rangesOf(s.requests("")); !slices.Equal(got, want) {
			t.Errorf("requests %v, want %v", got, want)
		}
		if last := rec.last(); last == nil || last.Tracks[0].Total != 1 || last.Tracks[0].ChunkBytes != 0 || len(last.Tracks[0].Partial) != 0 {
			t.Errorf("last checkpoint %+v, want one whole-resource part without prefixes", last)
		}
	})

	t.Run("a 200 to a later chunk", func(t *testing.T) {
		s := newSiteServer(t)
		sel, data := copiedTrack(t, s, 43)
		size := int64(len(data))
		opts := rangedOptions(t)
		target := chunkRange(1, c, size)
		var sent atomic.Bool
		cp, _ := pausedRun(t, s, opts, sel, pauseSpec{
			hold: func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
				if r.Header.Get("Range") != target {
					return false
				}
				sendPart(t, w, r, data, half)
				sent.Store(true)
				return true
			},
			ready: func(p Progress, _ []siteReq) bool { return sent.Load() && p.BytesDone >= size-half },
		})
		if cp.Tracks[0].Partial[1] != half {
			t.Fatalf("checkpoint %+v, want %d bytes of chunk 1", cp.Tracks[0], half)
		}
		var once atomic.Bool
		s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
			if once.CompareAndSwap(false, true) {
				r.Header.Del("Range")
			}
			return false
		})
		if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readFile(t, opts.OutPath), data) {
			t.Fatal("the output differs from the served file")
		}
		rest := "/18.mp4 bytes=" + strconv.FormatInt(c+half, 10) + "-" + strconv.FormatInt(2*c-1, 10)
		if got := rangesOf(s.requests("")); !slices.Equal(got, []string{rest, rest}) {
			t.Errorf("requests %v, want the rest of chunk 1 twice", got)
		}
	})

	t.Run("an unranged track", func(t *testing.T) {
		s := newSiteServer(t)
		sel, data := copiedTrack(t, s, 44)
		size := int64(len(data))
		sel.Tracks[0].Unranged = true
		opts := rangedOptions(t)
		var sent atomic.Bool
		cp, _ := pausedRun(t, s, opts, sel, pauseSpec{
			hold: func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
				sendPart(t, w, r, data, size/2)
				sent.Store(true)
				return true
			},
			ready: func(p Progress, _ []siteReq) bool { return sent.Load() && p.BytesDone >= size/2 },
			none:  true,
		})
		if cp != nil {
			t.Fatalf("checkpoint %+v (%d durable bytes), want none: nothing is durable", cp.Tracks, cp.DurableBytes)
		}
		if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readFile(t, opts.OutPath), data) {
			t.Fatal("the output differs from the served file")
		}
		if got := rangesOf(s.requests("")); !slices.Equal(got, []string{"/18.mp4 bytes=0-" + strconv.FormatInt(size-1, 10)}) {
			t.Errorf("requests %v, want the whole track from byte 0", got)
		}
	})
}

// A checkpoint written before prefixes existed resumes as before (the chunks without
// bits in full), and prefixes that do not fit the plan are ignored, never an error.
func TestRunRangedOldCheckpointWithoutPartial(t *testing.T) {
	const c, half = int64(media.MinChunkBytes), int64(media.MinChunkBytes) / 2
	for _, tc := range []struct {
		name  string
		edit  func(cp *Checkpoint)
		chunk string // the request for video chunk 1 the resume makes
	}{
		{"no prefixes", func(cp *Checkpoint) {
			for i := range cp.Tracks {
				cp.Tracks[i].Partial = nil
			}
			cp.DurableBytes, cp.BytesTotal = 0, 0
		}, "full"},
		{"invalid prefixes", func(cp *Checkpoint) {
			// Out of range, on a durable chunk, empty, negative, as long as the chunk.
			cp.Tracks[0].Partial = map[int]int64{-1: 5, 0: 100, 1: c, 2: -7, 3: 9, 99: 5}
			cp.Tracks[1].Partial = map[int]int64{0: half, 1: 0}
		}, "full"},
		{"a valid prefix among invalid ones", func(cp *Checkpoint) {
			cp.Tracks[0].Partial = map[int]int64{-1: 5, 1: half, 7: 1}
		}, "rest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			sel, v, a := pairSelection(t, s, "/")
			vsize, total := int64(len(v.data)), int64(len(v.data)+len(a.data))
			opts := rangedOptions(t)
			cp, _ := pausedRun(t, s, opts, sel, halfOfVideoChunk1(v.data, total))
			tc.edit(cp)
			if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
				t.Fatal(err)
			}
			checkMP4(t, opts.OutPath, v.samples, a.samples)
			want := "/v.mp4 " + chunkRange(1, c, vsize)
			if tc.chunk == "rest" {
				want = "/v.mp4 bytes=" + strconv.FormatInt(c+half, 10) + "-" + strconv.FormatInt(min(2*c, vsize)-1, 10)
			}
			if got := rangesOf(s.requests("")); !slices.Equal(got, []string{want}) {
				t.Errorf("resumed requests %v, want %s", got, want)
			}
		})
	}
}
