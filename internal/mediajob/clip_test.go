package mediajob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/remux"
	"downex/internal/testmedia"
)

// A part of an HLS stream fetches only the window's fragments (docs/MEDIA-SPEC.md §8
// C4), names its fragment files by their full-plan index (C11), checkpoints the window
// in full-plan terms and passes the assembler the part on the window's timeline.
func TestRunClipFetchesOnlyTheWindow(t *testing.T) {
	s := newServer(t)
	addFMP4Playlist(s, "/c/", 10, 7) // 10 segments of 4 s
	asm := &fakeAssembler{cut: func(w remux.Window) (remux.Result, error) {
		return remux.Result{Keyframe: w.Start - 500*time.Millisecond, Exact: true}, nil
	}}
	opts := testOptions(t, asm)
	var rec recorder
	sel := Selection{ManifestURL: s.url("/c/index.m3u8"), Clip: media.Clip{Start: 9 * time.Second, End: 15 * time.Second}}
	if _, err := Run(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		want := 0
		if i == 2 || i == 3 {
			want = 1
		}
		if got := s.hitCount(fmt.Sprintf("/c/s%d.m4s", i)); got != want {
			t.Errorf("segment %d fetched %d times, want %d", i, got, want)
		}
	}
	if s.hitCount("/c/init.mp4") != 1 {
		t.Error("the initialization segment was not fetched once")
	}
	if len(asm.windows) != 1 || asm.windows[0] != (remux.Window{Start: time.Second, End: 7 * time.Second, Origin: remux.OriginFirst}) {
		t.Errorf("windows %+v", asm.windows)
	}
	in := asm.inputs[0]
	if !strings.HasSuffix(in.Init, "0.frag") || len(in.Fragments) != 2 || !strings.HasSuffix(in.Fragments[0], "3.frag") || !strings.HasSuffix(in.Fragments[1], "4.frag") {
		t.Errorf("inputs %+v", in)
	}
	cp := rec.last()
	if cp == nil || len(cp.Tracks) != 1 || cp.Tracks[0].First != 0 || cp.Tracks[0].Total != 5 || cp.LeadIn != 0 {
		t.Fatalf("checkpoint %+v", cp)
	}
	for b, want := range []bool{true, false, false, true, true} {
		if bitSet(cp.Tracks[0].Done, b) != want {
			t.Errorf("checkpoint bit %d = %v", b, !want)
		}
	}
	last := rec.progress[len(rec.progress)-1]
	if last.Clip == nil || last.Clip.KeyframeMs != 8500 || last.Clip.FetchStartMs != 8000 || last.Clip.FetchEndMs != 16000 ||
		last.Clip.StartMs != 9000 || last.Clip.EndMs != 15000 || last.Clip.FetchedWhole {
		t.Errorf("last progress clip %+v", last.Clip)
	}
	// A Run that finds the recorded output reports the part again.
	var again recorder
	if _, err := Run(testContext(t), opts, sel, cp, again.onProgress, nil); err != nil {
		t.Fatal(err)
	}
	if len(again.progress) != 1 || again.progress[0].Clip == nil || again.progress[0].Clip.KeyframeMs != 8500 {
		t.Errorf("recorded completion: %+v", again.progress)
	}
}

// Without a keyframe at or before the part's start the window is extended back one
// fragment (C5), the lead-in is checkpointed before that fragment is fetched, the
// fragments already fetched are kept, and a resume computes the same window (C12).
func TestRunClipLeadIn(t *testing.T) {
	s := newServer(t)
	addFMP4Playlist(s, "/l/", 6, 8)
	calls := 0
	asm := &fakeAssembler{cut: func(w remux.Window) (remux.Result, error) {
		calls++
		if calls == 1 {
			if w.Late {
				t.Error("the first attempt allows a late start")
			}
			return remux.Result{}, fmt.Errorf("remux: %w", remux.ErrNoKeyframe)
		}
		return remux.Result{Keyframe: time.Second, Exact: true}, nil
	}}
	opts := testOptions(t, asm)
	var rec recorder
	sel := Selection{ManifestURL: s.url("/l/index.m3u8"), Clip: media.Clip{Start: 9 * time.Second}}
	if _, err := Run(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		want := 1
		if i < 1 {
			want = 0
		}
		if got := s.hitCount(fmt.Sprintf("/l/s%d.m4s", i)); got != want {
			t.Errorf("segment %d fetched %d times, want %d", i, got, want)
		}
	}
	if len(asm.windows) != 2 || asm.windows[1].Start != 5*time.Second || asm.windows[1].End != 0 {
		t.Errorf("windows %+v", asm.windows)
	}
	// The checkpoint with the lead-in came before the new fragment's bit.
	sawLeadIn := false
	for _, cp := range rec.checkpoints {
		if cp.LeadIn == 1 {
			if !sawLeadIn && bitSet(cp.Tracks[0].Done, 1) {
				t.Error("the lead-in fragment was recorded with the first lead-in checkpoint")
			}
			sawLeadIn = true
		}
	}
	cp := rec.last()
	if !sawLeadIn || cp.LeadIn != 1 || cp.Tracks[0].First != 0 || cp.Tracks[0].Total != 7 {
		t.Fatalf("checkpoint %+v", cp)
	}
	if last := rec.progress[len(rec.progress)-1].Clip; last == nil || last.KeyframeMs != 5000 || last.FetchStartMs != 4000 {
		t.Errorf("clip %+v", last)
	}

	// A paused run resumes the same window: nothing is fetched again.
	if err := os.Remove(opts.OutPath); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(opts.PartDir + "/output.json"); err != nil {
		t.Fatal(err)
	}
	before := s.hitCount("/l/s1.m4s") + s.hitCount("/l/s2.m4s") + s.hitCount("/l/init.mp4")
	if _, err := Run(testContext(t), opts, sel, cp, nil, nil); err != nil {
		t.Fatal(err)
	}
	if after := s.hitCount("/l/s1.m4s") + s.hitCount("/l/s2.m4s") + s.hitCount("/l/init.mp4"); after != before {
		t.Errorf("the resume fetched %d fragments again", after-before)
	}
	if w := asm.windows[len(asm.windows)-1]; w.Start != 5*time.Second {
		t.Errorf("the resumed window starts at %v", w.Start)
	}
}

// A LegacyTS record refuses a part.
func TestRunClipLegacyTS(t *testing.T) {
	s := newServer(t)
	addTSPlaylist(s, "/t/", 3, 9)
	opts := testOptions(t, &fakeAssembler{})
	opts.LegacyTS = true
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/t/index.m3u8"), Clip: media.Clip{Start: time.Second}}, nil, nil, nil)
	if !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("%v, want ErrUnsupported", err)
	}
}

// sidxTrack builds a fragmented MP4 video track with a real segment index: ftyp, moov,
// sidx, then one moof+mdat per second (30 samples of size bytes each, at 30 fps, the first
// of each a sync sample).
func sidxTrack(t *testing.T, seconds, size int) (data []byte, samples [][]byte) {
	t.Helper()
	return sidxTrackOf(t, seconds, 30, size)
}

// sidxTrackOf is sidxTrack with frames samples (at 30 fps) per subsegment.
func sidxTrackOf(t *testing.T, subsegments, frames, size int) (data []byte, samples [][]byte) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	trak := init.AddEmptyTrack(90000, "video", "und")
	if err := trak.SetAVCDescriptor("avc1", [][]byte{avcSPS()}, [][]byte{avcPPS()}, true); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(uint64(size), 5))
	var frags [][]byte
	dt := uint64(0)
	for k := range subsegments {
		frag, err := mp4.CreateFragment(uint32(k+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		for j := range frames {
			d := make([]byte, size)
			for i := range d {
				d[i] = byte(rng.Uint32())
			}
			samples = append(samples, d)
			flags := mp4.NonSyncSampleFlags
			if j == 0 {
				flags = mp4.SyncSampleFlags
			}
			frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Flags: flags, Dur: 3000, Size: uint32(size)}, DecodeTime: dt, Data: d})
			dt += 3000
		}
		frags = append(frags, encodeMP4(t, frag))
	}
	var refs []byte
	for _, f := range frags {
		refs = binary.BigEndian.AppendUint32(refs, uint32(len(f)))
		refs = binary.BigEndian.AppendUint32(refs, uint32(frames*3000))
		refs = binary.BigEndian.AppendUint32(refs, 1<<31|1<<28)
	}
	p := []byte{0, 0, 0, 0}
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, 90000)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, uint16(len(frags)))
	data = append(encodeMP4(t, init), box("sidx", p, refs)...)
	for _, f := range frags {
		data = append(data, f...)
	}
	return data, samples
}

// A part of a site's fragmented MP4 fetches the head, then only the span of the
// subsegments the index lists for the part (docs/SITE-MEDIA-SPEC.md §2.7), and cuts it.
func TestRunRangedClipSidxSpan(t *testing.T) {
	data, samples := sidxTrack(t, 12, 6000) // 12 s, ~2.2 MB
	s := newSiteServer(t)
	s.add("/v.mp4", data)
	sel := RangedSelection{Output: OutputRemux, Clip: media.Clip{Start: 4500 * time.Millisecond, End: 6500 * time.Millisecond},
		Tracks: []media.RangedTrack{rangedTrack("137", media.KindVideo, "mp4", s.url("/v.mp4"), int64(len(data)))}}
	opts := rangedOptions(t)
	var rec recorder
	if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	// Every request lies in the head or in the span of seconds 4..6.
	headEnd := int64(bytes.Index(data, []byte("sidx"))) - 4
	spanStart := int64(len(data)) // computed from the moofs below
	off := headEnd + int64(binary.BigEndian.Uint32(data[headEnd:]))
	var starts []int64
	for off < int64(len(data)) {
		starts = append(starts, off)
		off += int64(binary.BigEndian.Uint32(data[off:]))
		off += int64(binary.BigEndian.Uint32(data[off:]))
	}
	starts = append(starts, int64(len(data)))
	spanStart, spanEnd := starts[4], starts[7]
	var fetched int64
	for _, q := range s.requests("/v.mp4") {
		a, b := parseRange(t, q.rng)
		if a == 0 {
			continue // the head
		}
		if a < spanStart || b >= spanEnd {
			t.Errorf("request %s outside the span %d-%d", q.rng, spanStart, spanEnd-1)
		}
		fetched += b - a + 1
	}
	if fetched != spanEnd-spanStart {
		t.Errorf("fetched %d bytes of the span, want %d", fetched, spanEnd-spanStart)
	}
	cp := rec.last()
	if cp.Tracks[0].Base != spanStart || cp.Tracks[0].Head <= 0 {
		t.Errorf("checkpoint %+v", cp.Tracks[0])
	}
	if p := rec.progress[len(rec.progress)-1]; p.Clip == nil || p.Clip.KeyframeMs != 4000 || p.Clip.FetchStartMs != 4000 || p.Clip.FetchEndMs != 7000 || p.Clip.FetchedWhole {
		t.Errorf("clip %+v", p.Clip)
	}
	// The part: from the keyframe at 4 s to the last frame decoded before 6.5 s.
	checkMP4(t, opts.OutPath, samples[120:195])
}

// subsegmentOffsets returns the offsets of the subsegments (moof+mdat) of a file built
// by sidxTrackOf, and the file's size last.
func subsegmentOffsets(t *testing.T, data []byte) []int64 {
	t.Helper()
	sidx := int64(bytes.Index(data, []byte("sidx"))) - 4
	off := sidx + int64(binary.BigEndian.Uint32(data[sidx:]))
	var out []int64
	for off < int64(len(data)) {
		out = append(out, off)
		off += int64(binary.BigEndian.Uint32(data[off:]))
		off += int64(binary.BigEndian.Uint32(data[off:]))
	}
	return append(out, int64(len(data)))
}

// The audio span of a part follows the video's (docs/SITE-MEDIA-SPEC.md §2.7,
// docs/MEDIA-SPEC.md §8 C6): it starts with the audio subsegment that holds the start of
// the video's first subsegment, so that audio is there from the keyframe the video starts
// at and a player that ignores edit lists stays in sync. Without a video span the audio is
// fetched whole.
func TestRunRangedClipAudioFollowsVideo(t *testing.T) {
	video, _ := sidxTrackOf(t, 4, 90, 2000) // subsegments (and keyframes) every 3 s
	audio, _ := sidxTrackOf(t, 12, 30, 500) // subsegments of 1 s
	s := newSiteServer(t)
	s.add("/v.mp4", video)
	s.add("/a.m4a", audio)
	run := func(t *testing.T, unranged bool) *Checkpoint {
		asm := &fakeAssembler{}
		opts := rangedOptions(t)
		opts.Assemble = asm.assemble
		vt := rangedTrack("137", media.KindVideo, "mp4", s.url("/v.mp4"), int64(len(video)))
		vt.Unranged = unranged
		sel := RangedSelection{Output: OutputRemux, Clip: media.Clip{Start: 4500 * time.Millisecond, End: 5500 * time.Millisecond},
			Tracks: []media.RangedTrack{vt, rangedTrack("140", media.KindAudio, "m4a", s.url("/a.m4a"), int64(len(audio)))}}
		var rec recorder
		if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
			t.Fatal(err)
		}
		cp := rec.last()
		if cp == nil || len(cp.Tracks) != 2 {
			t.Fatalf("checkpoint %+v", cp)
		}
		return cp
	}
	vs, as := subsegmentOffsets(t, video), subsegmentOffsets(t, audio)
	cp := run(t, false)
	if cp.Tracks[0].Base != vs[1] {
		t.Errorf("video span from byte %d, want %d (the subsegment from 3 s)", cp.Tracks[0].Base, vs[1])
	}
	if cp.Tracks[1].Base != as[3] {
		t.Errorf("audio span from byte %d, want %d (the subsegment from 3 s, where the video starts)", cp.Tracks[1].Base, as[3])
	}
	// A video whose server does not allow ranges is fetched whole: so is its audio, without
	// a request for its head.
	before := len(s.requests("/a.m4a"))
	cp = run(t, true)
	if got := s.requests("/a.m4a")[before:]; len(got) != 1 {
		t.Errorf("audio requests %+v, want one for the whole resource", got)
	}
	if cp.Tracks[0].Base != 0 || cp.Tracks[0].Head != 0 || cp.Tracks[1].Base != 0 || cp.Tracks[1].Head != 0 {
		t.Errorf("checkpoint %+v, want both tracks whole", cp.Tracks)
	}
}

// moovAtEnd moves the 'moov' box of a progressive file after its 'mdat'.
func moovAtEnd(t *testing.T, file []byte) []byte {
	t.Helper()
	f, err := mp4.DecodeFile(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	ftypB := encodeMP4(t, f.Ftyp)
	moovSize := int(f.Moov.Size())
	mdat := file[len(ftypB)+moovSize:]
	for _, trak := range f.Moov.Traks {
		for i := range trak.Mdia.Minf.Stbl.Stco.ChunkOffset {
			trak.Mdia.Minf.Stbl.Stco.ChunkOffset[i] -= uint32(moovSize)
		}
	}
	out := append(append([]byte(nil), ftypB...), mdat...)
	return append(out, encodeMP4(t, f.Moov)...)
}

// A part of a progressive file whose 'moov' comes last: the head finds an 'mdat', a walk
// of 16-byte requests finds the 'moov', and only the span of the part's samples is
// fetched, then cut without re-encoding.
func TestRunRangedClipProgressiveMoovAtEnd(t *testing.T) {
	vc := testmedia.VideoConfig{Frames: 150, SliceBytes: 9000, Seed: 61}
	ac := testmedia.AudioConfig{Frames: 235, Seed: 62}
	aus := testmedia.Video(vc)
	file, err := testmedia.ProgressiveMP4(vc, aus, ac, testmedia.AAC(ac))
	if err != nil {
		t.Fatal(err)
	}
	file = moovAtEnd(t, file)
	if len(file) < clipHeadFirst {
		t.Fatalf("the fixture has %d bytes, too few to test the walk", len(file))
	}
	s := newSiteServer(t)
	s.add("/m.mp4", file)
	sel := RangedSelection{Output: OutputCopy, Clip: media.Clip{Start: 2 * time.Second, End: 3 * time.Second},
		Tracks: []media.RangedTrack{rangedTrack("18", media.KindMuxed, "mp4", s.url("/m.mp4"), int64(len(file)))}}
	opts := rangedOptions(t)
	var rec recorder
	if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	reqs := s.requests("/m.mp4")
	var walk, total int64
	for _, q := range reqs {
		a, b := parseRange(t, q.rng)
		if b-a+1 == 16 {
			walk++
		}
		total += b - a + 1
	}
	if walk == 0 || walk > clipWalkMax {
		t.Errorf("%d walk requests", walk)
	}
	if total >= int64(len(file)) {
		t.Errorf("fetched %d of %d bytes: not a part", total, len(file))
	}
	f, err := mp4.DecodeFile(bytes.NewReader(readFile(t, opts.OutPath)))
	if err != nil {
		t.Fatal(err)
	}
	vt := f.Moov.Traks[0]
	if vt.Mdia.Minf.Stbl.Stss != nil && !vt.Mdia.Minf.Stbl.Stss.IsSyncSample(1) {
		t.Error("the part does not start with a keyframe")
	}
	if p := rec.progress[len(rec.progress)-1]; p.Clip == nil || p.Clip.KeyframeMs > 2000 || p.Clip.FetchedWhole {
		t.Errorf("clip %+v", p.Clip)
	}
}

// A copied file's part is written beside the downloaded bytes, not moved into place: the
// free space counts the output too (docs/MEDIA-SPEC.md §8 C18).
func TestRunRangedClipCopyFreeSpace(t *testing.T) {
	vc := testmedia.VideoConfig{Frames: 60, SliceBytes: 2000, Seed: 63}
	ac := testmedia.AudioConfig{Frames: 94, Seed: 64}
	file, err := testmedia.ProgressiveMP4(vc, testmedia.Video(vc), ac, testmedia.AAC(ac))
	if err != nil {
		t.Fatal(err)
	}
	s := newSiteServer(t)
	s.add("/m.mp4", file)
	tr := rangedTrack("18", media.KindMuxed, "mp4", s.url("/m.mp4"), int64(len(file)))
	tr.Unranged = true // fetched whole, the part cut from it
	sel := RangedSelection{Output: OutputCopy, Clip: media.Clip{Start: time.Second}, Tracks: []media.RangedTrack{tr}}
	opts := rangedOptions(t)
	size := int64(len(file))
	opts.FreeSpace = func(string) (int64, error) { return size + freeSpaceMargin, nil }
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); !errors.Is(err, ErrDiskFull) {
		t.Fatalf("room for the track alone: %v, want ErrDiskFull", err)
	}
	if n := len(s.requests("")); n != 0 {
		t.Fatalf("%d requests before the free space was known", n)
	}
	opts.FreeSpace = func(string) (int64, error) { return 2*size + freeSpaceMargin, nil }
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatalf("room for the track and its part: %v", err)
	}
}

// A server that ignores ranges: the track is fetched whole and the part cut from it
// (C9).
func TestRunRangedClipWholeFallback(t *testing.T) {
	data, samples := sidxTrack(t, 4, 2000)
	s := newSiteServer(t)
	s.add("/v.mp4", data)
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		r.Header.Del("Range")
		return false
	})
	sel := RangedSelection{Output: OutputRemux, Clip: media.Clip{Start: 2 * time.Second},
		Tracks: []media.RangedTrack{rangedTrack("137", media.KindVideo, "mp4", s.url("/v.mp4"), int64(len(data)))}}
	opts := rangedOptions(t)
	var rec recorder
	if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, nil); err != nil {
		t.Fatal(err)
	}
	if p := rec.progress[len(rec.progress)-1]; p.Clip == nil || !p.Clip.FetchedWhole || p.Clip.KeyframeMs != 2000 {
		t.Errorf("clip %+v", p.Clip)
	}
	checkMP4(t, opts.OutPath, samples[60:])
}

// A span download stopped midway resumes from its checkpoint: the synced head is reused (no
// request from byte 0), the same span is computed again, and only the missing chunks are
// fetched (docs/SITE-MEDIA-SPEC.md §2.7, MEDIA-SPEC §8 C12).
func TestRunRangedClipResume(t *testing.T) {
	data, samples := sidxTrack(t, 12, 60000) // about 1.8 MB a second
	s := newSiteServer(t)
	s.add("/v.mp4", data)
	sel := RangedSelection{Output: OutputRemux, Clip: media.Clip{Start: 4500 * time.Millisecond, End: 6500 * time.Millisecond},
		Tracks: []media.RangedTrack{rangedTrack("137", media.KindVideo, "mp4", s.url("/v.mp4"), int64(len(data)))}}
	opts := rangedOptions(t)
	opts.MaxConnections = 1
	ctx, cancel := context.WithCancel(testContext(t))
	blocked := make(chan struct{}, 1)
	spans := 0
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
			return false
		}
		spans++
		if spans == 3 {
			blocked <- struct{}{}
			<-r.Context().Done()
			return true
		}
		return false
	})
	go func() {
		<-blocked
		cancel()
	}()
	var first recorder
	if _, err := RunRanged(ctx, opts, sel, nil, nil, first.onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run: %v", err)
	}
	cp := first.last()
	if cp == nil || cp.Tracks[0].Base == 0 || cp.Tracks[0].Head == 0 {
		t.Fatalf("checkpoint %+v", cp)
	}
	done := 0
	for i := range cp.Tracks[0].Total {
		if bitSet(cp.Tracks[0].Done, i) {
			done++
		}
	}
	if done == 0 || done == cp.Tracks[0].Total {
		t.Fatalf("%d of %d chunks done", done, cp.Tracks[0].Total)
	}
	s.setIntercept(nil)
	before := len(s.requests("/v.mp4"))
	if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range s.requests("/v.mp4")[before:] {
		if strings.HasPrefix(q.rng, "bytes=0-") {
			t.Errorf("the resumed run read the head again: %s", q.rng)
		}
	}
	if n := len(s.requests("/v.mp4")) - before; n > cp.Tracks[0].Total-done+1 {
		t.Errorf("the resumed run sent %d requests for %d missing chunks", n, cp.Tracks[0].Total-done)
	}
	checkMP4(t, opts.OutPath, samples[120:195])
}

// Every byte of a part passes the job's limiter, also those of its head, of the walk's
// box headers and of a progressive 'moov' (Polite mode and the speed limits see them all).
func TestRunRangedClipHeadPassesTheLimiter(t *testing.T) {
	vc := testmedia.VideoConfig{Frames: 150, SliceBytes: 9000, Seed: 61}
	ac := testmedia.AudioConfig{Frames: 235, Seed: 62}
	file, err := testmedia.ProgressiveMP4(vc, testmedia.Video(vc), ac, testmedia.AAC(ac))
	if err != nil {
		t.Fatal(err)
	}
	file = moovAtEnd(t, file)
	s := newSiteServer(t)
	s.add("/m.mp4", file)
	sel := RangedSelection{Output: OutputCopy, Clip: media.Clip{Start: 2 * time.Second, End: 3 * time.Second},
		Tracks: []media.RangedTrack{rangedTrack("18", media.KindMuxed, "mp4", s.url("/m.mp4"), int64(len(file)))}}
	opts := rangedOptions(t)
	var rec recorder
	lim := &countLimiter{}
	opts.Limiter = lim
	if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, q := range s.requests("/m.mp4") {
		a, b := parseRange(t, q.rng)
		total += b - a + 1
	}
	if got := lim.n.Load(); got != total {
		t.Errorf("the limiter saw %d of the %d bytes fetched", got, total)
	}
}
