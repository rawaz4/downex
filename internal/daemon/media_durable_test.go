package daemon

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// A media download stores the bytes its checkpoint records as durable
// (MediaCheckpoint.DurableBytes), never those of requests in flight that the latest
// progress counted: paused, it shows those bytes, and a resumed run starts from them, so
// the bytes done never drop when it resumes (docs/SITE-MEDIA-SPEC.md §2.5).
func TestMediaStoresDurableBytes(t *testing.T) {
	const inflight = 4096
	fm := newFakeMedia(10)
	fm.gate = make(chan struct{})
	fm.inflight = inflight
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: t.TempDir(), Filename: "movie"})
	frag := int64(len(fragData(0)))

	fm.feed(t, 3)
	live := h.waitFor(d.GetId(), "3 fragments", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() >= 3 })
	if live.GetReceived() != 3*frag+inflight {
		t.Errorf("running: received %d, want the job's %d", live.GetReceived(), 3*frag+inflight)
	}
	p := h.pause(d.GetId())
	if p.GetState() != stPaused || p.GetReceived() != 3*frag {
		t.Errorf("paused: %v, want %d durable bytes received", p, 3*frag)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.Media.BytesDone != 3*frag || rec.Media.Checkpoint == nil ||
		rec.Media.Checkpoint.DurableBytes != 3*frag {
		t.Errorf("stored bytes done %d (checkpoint %+v), want %d", rec.Media.BytesDone, rec.Media.Checkpoint, 3*frag)
	}

	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	fm.feed(t, 1)
	r := h.waitFor(d.GetId(), "4 fragments", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() >= 4 })
	if r.GetReceived() < p.GetReceived() {
		t.Errorf("resumed: received %d, below the %d it was paused with", r.GetReceived(), p.GetReceived())
	}
	fm.feed(t, 6)
	h.waitCompleted(d.GetId())
}
