package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/bits"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/transport"
)

// fakeMedia is a MediaJobs that serves a configured probe result and "downloads" a
// fixed number of fragments: each fragment i of the video track (or of the audio track
// when there is no video) is written to PartDir/<i>.frag with the content fragData(i),
// optionally waiting for a token on gate first, and the output is their concatenation
// in order. It honours resume checkpoints (only missing fragments are written), saves a
// checkpoint (with the bytes of its durable fragments) after every fragment and when
// cancelled, reports inflight more bytes than that in its progress, and records every
// call. Its runs
// return container, or when that is "" what the real job returns: "ts" for a LegacyTS
// run, else "mp4".
type fakeMedia struct {
	mu        sync.Mutex
	probe     MediaProbe
	probeErr  error
	fragments int
	container string
	gate      chan struct{}      // nil: no waiting
	workers   []model.WorkerView // reported with every progress event
	runErr    error              // returned after failAfter fragments of a run
	failAfter int
	inflight  int64 // bytes every progress event reports beyond the durable fragments (requests in flight)
	probes    []MediaSelection
	runs      []fakeMediaRun
	active    int // runs in progress
	maxActive int
}

type fakeMediaRun struct {
	opts   MediaRunOptions
	sel    MediaSelection
	resume *model.MediaCheckpoint
}

// sampleTracks are the tracks of fakeMedia's default probe: two video tracks, two audio
// tracks and a muxed one.
func sampleTracks() []media.Track {
	return []media.Track{
		{ID: "v0", Kind: media.KindVideo, Bandwidth: 800_000, Width: 640, Height: 360, FrameRate: 25, Codecs: "avc1.4d401e"},
		{ID: "v1", Kind: media.KindVideo, Bandwidth: 5_000_000, Width: 1920, Height: 1080, FrameRate: 29.97, Codecs: "avc1.640028"},
		{ID: "a0", Kind: media.KindAudio, Bandwidth: 64_000, Codecs: "mp4a.40.5", Language: "de", Name: "Deutsch"},
		{ID: "a1", Kind: media.KindAudio, Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en", Name: "English"},
		{ID: "m0", Kind: media.KindMuxed, Bandwidth: 1_000_000},
		{ID: "s0", Kind: media.KindSubtitles, Language: "en"},
	}
}

func newFakeMedia(fragments int) *fakeMedia {
	return &fakeMedia{
		probe: MediaProbe{
			Format: "hls", Tracks: sampleTracks(), DefaultVideo: "v1", DefaultAudio: "a1",
			Duration: 90 * time.Second, Container: "mp4", FragmentsTotal: fragments,
		},
		fragments: fragments,
	}
}

func fragData(i int) []byte { return []byte(fmt.Sprintf("<fragment %04d>", i)) }

// wantOutput is the assembled output of n fragments.
func wantOutput(n int) []byte {
	var b bytes.Buffer
	for i := range n {
		b.Write(fragData(i))
	}
	return b.Bytes()
}

func (f *fakeMedia) set(fn func(f *fakeMedia)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeMedia) calls() (probes []MediaSelection, runs []fakeMediaRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.probes), slices.Clone(f.runs)
}

func (f *fakeMedia) Probe(ctx context.Context, tf transport.Factory, sel MediaSelection) (*MediaProbe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sel.Headers = sel.Headers.Clone()
	f.probes = append(f.probes, sel)
	if tf == nil {
		return nil, errors.New("fake probe: no transport factory")
	}
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	p := f.probe
	p.Tracks = slices.Clone(p.Tracks)
	if sel.VideoTrack != "" {
		p.DefaultVideo = sel.VideoTrack
	}
	if sel.AudioTrack != "" {
		p.DefaultAudio = sel.AudioTrack
	}
	return &p, nil
}

func (f *fakeMedia) Run(ctx context.Context, opts MediaRunOptions, sel MediaSelection, resume *model.MediaCheckpoint,
	onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	f.mu.Lock()
	sel.Headers = sel.Headers.Clone()
	f.runs = append(f.runs, fakeMediaRun{opts: opts, sel: sel, resume: resume.Clone()})
	n, gate, runErr, failAfter, container, workers := f.fragments, f.gate, f.runErr, f.failAfter, f.container, slices.Clone(f.workers)
	inflight := f.inflight
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()
	defer f.set(func(f *fakeMedia) { f.active-- })

	if opts.Factory == nil || opts.Limiter == nil || opts.PartDir == "" || opts.OutPath == "" {
		return "", fmt.Errorf("fake run: incomplete options %+v", opts)
	}
	if container == "" {
		container = "mp4"
		if opts.LegacyTS {
			container = "ts"
		}
	}
	track := sel.VideoTrack
	if track == "" {
		track = sel.AudioTrack
	}
	done := make([]byte, (n+7)/8)
	if resume != nil && len(resume.Tracks) == 1 && resume.Tracks[0].TrackID == track && resume.Tracks[0].Total == n {
		copy(done, resume.Tracks[0].Done)
	}
	count := 0
	for _, b := range done {
		count += bits.OnesCount8(b)
	}
	var received int64
	for i := range n {
		if done[i/8]&(1<<(i%8)) != 0 {
			received += int64(len(fragData(i)))
		}
	}
	checkpoint := func() error {
		return onCheckpoint(model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
			{TrackID: track, Total: n, Done: slices.Clone(done)},
		}, DurableBytes: received})
	}
	if err := os.MkdirAll(opts.PartDir, 0o755); err != nil {
		return "", err
	}
	written := 0
	for i := range n {
		if done[i/8]&(1<<(i%8)) != 0 {
			continue
		}
		if runErr != nil && written == failAfter {
			_ = checkpoint()
			return "", runErr
		}
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				_ = checkpoint()
				return "", ctx.Err()
			}
		} else if ctx.Err() != nil {
			_ = checkpoint()
			return "", ctx.Err()
		}
		if err := os.WriteFile(filepath.Join(opts.PartDir, strconv.Itoa(i)+".frag"), fragData(i), 0o644); err != nil {
			return "", err
		}
		written++
		done[i/8] |= 1 << (i % 8)
		count++
		received += int64(len(fragData(i)))
		onProgress(MediaProgress{FragmentsTotal: n, FragmentsDone: count, BytesDone: received + inflight, SpeedBps: 1000, Connections: 2,
			Workers: workers})
		if err := checkpoint(); err != nil {
			return "", err
		}
	}
	if runErr != nil {
		return "", runErr
	}
	// Assemble from the fragment files: resumed fragments must still be on disk.
	var out bytes.Buffer
	for i := range n {
		b, err := os.ReadFile(filepath.Join(opts.PartDir, strconv.Itoa(i)+".frag"))
		if err != nil {
			return "", fmt.Errorf("fake assemble: %w", err)
		}
		out.Write(b)
	}
	// Like the real job: an existing file is replaced only with ReplaceExisting, and only
	// a regular one.
	if fi, err := os.Lstat(opts.OutPath); !errors.Is(err, fs.ErrNotExist) && !(opts.ReplaceExisting && err == nil && fi.Mode().IsRegular()) {
		return "", fmt.Errorf("fake assemble: %s exists", opts.OutPath)
	}
	part := opts.OutPath + ".dxpart"
	if err := os.WriteFile(part, out.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(part, opts.OutPath); err != nil {
		return "", err
	}
	// Like the real job, the part directory is left to the daemon.
	return container, nil
}

// feed releases n fragments of gated runs.
func (f *fakeMedia) feed(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case f.gate <- struct{}{}:
		case <-time.After(waitTimeout):
			t.Fatal("no media run took a fragment token")
		}
	}
}

// headerOf is a one-header http.Header.
func headerOf(k, v string) http.Header { return http.Header{k: {v}} }
