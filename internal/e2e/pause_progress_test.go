package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testmedia"
	"downex/internal/testserver"
)

// progressSample is what the API reported for a download at one poll.
type progressSample struct {
	state     downexv1.DownloadState
	received  int64
	fragments float64 // fragments done / total of a media download, else 0
}

// pollProgress polls download id, recording every report into log, until done reports
// true.
func (h *harness) pollProgress(id int64, log *[]progressSample, done func(*downexv1.Download) bool) *downexv1.Download {
	h.t.Helper()
	deadline := time.Now().Add(completeTimeout)
	for {
		d := h.get(id)
		s := progressSample{state: d.GetState(), received: d.GetReceived()}
		if m := d.GetMedia(); m.GetFragmentsTotal() > 0 {
			s.fragments = float64(m.GetFragmentsDone()) / float64(m.GetFragmentsTotal())
		}
		*log = append(*log, s)
		switch {
		case done(d):
			return d
		case d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED || time.Now().After(deadline):
			h.t.Fatalf("download %d: %v", id, d)
		}
		time.Sleep(pollInterval)
	}
}

func inState(s downexv1.DownloadState) func(*downexv1.Download) bool {
	return func(d *downexv1.Download) bool { return d.GetState() == s }
}

// Pausing and resuming never makes a download's progress go back (owner note 2026-10-29;
// a page's video: TestSitePauseMidChunk): an ordinary file keeps the bytes it received;
// an HLS stream keeps its fragments, which measure its progress (its size is an
// estimate), and once paused shows the bytes of its stored fragments, from which the
// resumed run counts on.
func TestPauseResumeNeverGoesBack(t *testing.T) {
	t.Parallel()
	const (
		paused    = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
		completed = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	)

	t.Run("file", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		const size, seed = 12 << 20, 404
		srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"n1"`, Throttle: 512 << 10})
		d := h.startDaemon()
		h.waitReady(d)
		id := h.add(srv.URL+"/file.bin", "-o", h.out, "-c", "4")
		var log []progressSample
		h.pollProgress(id, &log, func(d *downexv1.Download) bool { return d.GetReceived() >= 2<<20 })
		h.cli("pause", idArg(id)).ok(t)
		p := h.pollProgress(id, &log, inState(paused))
		srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
		h.cli("resume", idArg(id)).ok(t)
		h.pollProgress(id, &log, inState(completed))
		checkFile(t, filepath.Join(h.out, "file.bin"), seed, size)
		for i := 1; i < len(log); i++ {
			if log[i].received < log[i-1].received {
				t.Errorf("received went back from %d to %d (%v, then %v); paused at %d", log[i-1].received, log[i].received,
					log[i-1].state, log[i].state, p.GetReceived())
			}
		}
		h.stop(d)
	})

	t.Run("hls", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		s := newStreamServer(t)
		g := newGenTS(t, tsSpec{
			video:   &testmedia.VideoConfig{Width: 320, Height: 180, Frames: 300, Seed: 71},
			audio:   &testmedia.AudioConfig{Frames: 470, Seed: 72},
			seconds: 1,
		})
		segs := serveTS(s, "/hls/talk/", g)
		const kept = 4
		if len(segs) < 2*kept {
			t.Fatalf("%d segments, want more than %d", len(segs), 2*kept)
		}
		s.setGate(func(p string) bool { return segmentNumber(p) > kept })
		d := h.startDaemon()
		h.waitReady(d)
		id := h.cli("media", s.url("/hls/talk/index.m3u8"), "-o", h.out, "--name", "talk").ok(t).id(t)
		var log []progressSample
		h.pollProgress(id, &log, func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() >= kept })
		h.cli("pause", idArg(id)).ok(t)
		p := h.pollProgress(id, &log, inState(paused))
		var durable int64
		for _, seg := range segs[:kept] {
			durable += int64(len(seg))
		}
		if p.GetReceived() != durable || p.GetMedia().GetFragmentsDone() != kept {
			t.Errorf("paused: %d bytes and %d fragments received, want the %d bytes of the %d stored fragments",
				p.GetReceived(), p.GetMedia().GetFragmentsDone(), durable, kept)
		}
		at := len(log)
		s.setGate(nil)
		h.cli("resume", idArg(id)).ok(t)
		h.pollProgress(id, &log, inState(completed))
		checkMP4(t, filepath.Join(h.out, "talk.mp4"), tsTracks(g)...)
		for i := 1; i < len(log); i++ {
			if log[i].fragments < log[i-1].fragments && log[i].state != completed {
				t.Errorf("fragments went back from %.3f to %.3f (%v, then %v)", log[i-1].fragments, log[i].fragments,
					log[i-1].state, log[i].state)
			}
		}
		for _, l := range log[at:] {
			if l.received < p.GetReceived() {
				t.Errorf("resumed: %d bytes received (%v), less than the %d it was paused with", l.received, l.state, p.GetReceived())
				break
			}
		}
		for i := 1; i <= kept; i++ {
			if n := s.requests(fmt.Sprintf("/hls/talk/seg-%d.ts", i)); n != 1 {
				t.Errorf("segment %d, stored when paused, was requested %d times", i, n)
			}
		}
		noMediaLeftovers(t, h.out)
		h.stop(d)
	})
}
