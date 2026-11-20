package mediajob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/remux"
	"downex/internal/transport"
)

// Parts of a site video (docs/SITE-MEDIA-SPEC.md §2.7, docs/MEDIA-SPEC.md §8). A
// RunRanged with RangedSelection.Clip first reads the head of every track of a known size
// whose server allows ranges: bytes from 0 (512 KiB, grown ×4 up to 8 MiB) until the
// 'ftyp' and 'moov' boxes, and for a fragmented file its top-level 'sidx' box, are
// complete; a progressive file whose 'moov' comes after its media is walked box by box
// with 16-byte range requests (at most 64) and its 'moov' (at most 64 MiB) fetched. The
// 'moov' is checked for protection (media.CheckInit) before any media byte is requested.
// The track then fetches one span of the resource: the subsegments the index lists for
// the part (fragmented), or the bytes that hold the part's samples (progressive,
// remux.SampleSpan). The span is stored in the track's file at its offset less
// rtrack.spanBase, and fetched in chunks as a whole track would be. A track without a known
// size or ranges, a fragmented file without a top-level 'sidx' (or with a hierarchical
// one), or a head that cannot be read within its bounds is fetched whole and the part is
// cut from it (C9).

// Bounds of the head step (docs/MEDIA-SPEC.md §8 C15).
const (
	clipHeadFirst   = 512 << 10
	clipHeadMax     = 8 << 20
	clipWalkMax     = 64
	clipHeadSuffix  = ".head"
	clipSidxLeadMax = 4
)

// Clip modes of a ranged track.
const (
	clipNone  = iota // no part, or a track fetched whole
	clipFrag         // a span of subsegments of a fragmented file; the head holds ftyp+moov+sidx
	clipProg         // a span of a progressive file's samples; the head holds the 'moov'
	clipWhole        // fetched whole, the part cut locally (C9)
)

// headPath is the file of the head of s: in PartDir, beside the track's directory,
// which a track that starts over removes.
func (r *rangedRunner) headPath(s *rtrack) string {
	return filepath.Join(r.o.PartDir, dirName(s.kind, s.id)+clipHeadSuffix)
}

// clipWindow is the part as the assembler cuts it: on the resource's media timeline.
func (r *rangedRunner) clipWindow() remux.Window {
	return remux.Window{Start: r.sel.Clip.Start, End: r.sel.Clip.End, Origin: remux.OriginMedia, Late: true}
}

// clipHead runs the head step of s (a track of a known size whose server allows ranges)
// and turns it into a span of its resource, or marks it to be fetched whole.
func (r *rangedRunner) clipHead(ctx context.Context, s *rtrack, prev *TrackCheckpoint) error {
	if err := os.MkdirAll(r.o.PartDir, 0o700); err != nil {
		return diskErr("creating the part directory", err)
	}
	if _, ok := r.audioFrom(s, 0); !ok {
		s.clip = clipWhole // the audio of a video fetched whole (audioFrom)
		return nil
	}
	path := r.headPath(s)
	if prev != nil && prev.Head > 0 {
		// A resumed track reuses its synced head when it is the size recorded (C12).
		if data, err := os.ReadFile(path); err == nil && int64(len(data)) == prev.Head {
			if ok, err := r.useHead(s, data); err != nil || ok {
				return err
			}
		}
	}
	head, err := r.readHead(ctx, s)
	if err != nil {
		return err
	}
	if head == nil {
		s.clip = clipWhole
		return nil
	}
	if err := writeSynced(path, head); err != nil {
		return diskErr("writing the head of a track", err)
	}
	ok, err := r.useHead(s, head)
	if err != nil {
		return err
	}
	if !ok {
		s.clip = clipWhole
		return nil
	}
	if prev == nil {
		// A fresh span is cut into chunks fitted to its own length (FitChunk), so that
		// a short part keeps every connection busy; a resumed one keeps its checkpoint's.
		s.chunkBytes = FitChunk(r.o.HostRules, r.source(s).url, s.chunkBytes, s.size, r.o.MaxConnections)
	}
	return nil
}

// useHead computes the span of s from its head (ftyp+moov+sidx of a fragmented file, or
// a progressive file's 'moov'); false: no usable span, the track is fetched whole.
func (r *rangedRunner) useHead(s *rtrack, head []byte) (bool, error) {
	w := r.clipWindow()
	full := s.size
	var start, end int64
	mode := clipFrag
	if len(head) >= 8 && string(head[4:8]) == "moov" {
		if err := media.CheckInit(head); err != nil {
			return false, fmt.Errorf("mediajob: track %q: %w", s.id, err)
		}
		if r.sel.Output != OutputCopy || hasChildBox(head[8:], "mvex") {
			return false, nil // a progressive track is only cut when it is copied
		}
		a, b, err := remux.SampleSpan(head, w)
		if err != nil {
			if errors.Is(err, media.ErrEncrypted) {
				return false, err
			}
			return false, nil
		}
		start, end, mode = a, b, clipProg
		s.moov = head
	} else {
		boxes, _, err := topBoxes(head, full)
		if err != nil {
			return false, nil
		}
		var moov, sidx *isoBox
		for k := range boxes {
			switch boxes[k].typ {
			case "moov":
				moov = &boxes[k]
			case "sidx":
				if moov != nil && sidx == nil {
					sidx = &boxes[k]
				}
			}
		}
		if moov == nil || sidx == nil {
			return false, nil
		}
		if err := media.CheckInit(head[moov.off:moov.end()]); err != nil {
			return false, fmt.Errorf("mediajob: track %q: %w", s.id, err)
		}
		ix, err := media.ParseSidx(head[sidx.off:sidx.end()], sidx.end())
		if err != nil {
			return false, nil
		}
		from, ok := r.audioFrom(s, w.Start)
		if !ok {
			return false, nil
		}
		first, last, ok := ix.Span(from, w.End, 0)
		if !ok {
			return false, nil
		}
		// The part starts in a subsegment that starts with a stream access point (C5).
		for lead := 0; first > 0 && !ix.Refs[first].StartsWithSAP && lead < clipSidxLeadMax; lead++ {
			first--
		}
		start, end = ix.Refs[first].Offset, ix.Refs[last].End()
		if start < sidx.end() || end > full {
			return false, nil
		}
		s.spanStart, s.spanEnd = ix.Refs[first].Start, ix.Refs[last].Start+ix.Refs[last].Duration
	}
	if start < 0 || end <= start || end > full {
		return false, nil
	}
	s.clip, s.spanBase, s.headLen, s.fullSize = mode, start, int64(len(head)), full
	s.size = end - start
	return true, nil
}

// audioFrom is where the span of s starts in time: the part's start, or for the audio
// track of a video whose span is known the earlier start of the video's first
// subsegment, so that audio is there from the keyframe the video starts at and a player
// that ignores edit lists stays in sync (docs/MEDIA-SPEC.md §8 C6). false: the video is
// fetched whole, its keyframe unknown, so the audio is fetched whole too.
func (r *rangedRunner) audioFrom(s *rtrack, start time.Duration) (time.Duration, bool) {
	if s.kind != media.KindAudio || len(r.tracks) < 2 || r.tracks[0] == s {
		return start, true
	}
	v := r.tracks[0]
	if v.clip != clipFrag {
		return 0, false
	}
	return min(start, v.spanStart), true
}

// readHead fetches the head of s: ftyp+moov(+sidx) from byte 0, grown until complete, or
// the 'moov' of a progressive file found by a walk over its top-level boxes. nil: the
// head cannot be read within its bounds (the track is fetched whole).
func (r *rangedRunner) readHead(ctx context.Context, s *rtrack) ([]byte, error) {
	n := min(s.size, clipHeadFirst)
	for {
		data, whole, err := r.fetchRange(ctx, s, 0, n)
		if err != nil || whole {
			return nil, err
		}
		boxes, partial, err := topBoxes(data, s.size)
		if err != nil {
			return nil, nil
		}
		isMedia := func(typ string) bool { return typ == "mdat" || typ == "moof" }
		mi := -1
		for k := range boxes {
			if boxes[k].typ == "moov" {
				mi = k
				break
			}
		}
		switch {
		case mi >= 0 && !hasChildBox(data[boxes[mi].off+boxes[mi].hdr:boxes[mi].end()], "mvex"):
			// A progressive file: its 'moov' is all the head it needs.
			return append([]byte(nil), data[boxes[mi].off:boxes[mi].end()]...), nil
		case mi >= 0:
			// A fragmented file: its index must come after the 'moov', before the media.
			for _, b := range boxes[mi+1:] {
				switch {
				case b.typ == "sidx":
					return data[:b.end()], nil
				case isMedia(b.typ):
					return nil, nil
				}
			}
			if partial == nil || isMedia(partial.typ) {
				return nil, nil
			}
		default:
			for _, b := range boxes {
				if isMedia(b.typ) {
					return r.walkMoov(ctx, s, boxes, partial)
				}
			}
			if partial != nil && isMedia(partial.typ) {
				return r.walkMoov(ctx, s, boxes, partial)
			}
			if partial == nil {
				return nil, nil
			}
		}
		if n >= clipHeadMax || n >= s.size || partial.end() > clipHeadMax {
			return nil, nil
		}
		n = min(max(n*4, partial.end()), clipHeadMax, s.size)
	}
}

// walkMoov finds the 'moov' box of a progressive file after its media: it reads the
// header of each top-level box after the ones in the head (16-byte range requests, at
// most clipWalkMax) and fetches the 'moov' (at most remux.MaxProgressiveMoov).
func (r *rangedRunner) walkMoov(ctx context.Context, s *rtrack, boxes []isoBox, partial *isoBox) ([]byte, error) {
	off := int64(0)
	if len(boxes) > 0 {
		off = boxes[len(boxes)-1].end()
	}
	if partial != nil {
		off = partial.end()
	}
	for range clipWalkMax {
		if off+8 > s.size {
			return nil, nil
		}
		hdr, whole, err := r.fetchRange(ctx, s, off, min(16, s.size-off))
		if err != nil || whole {
			return nil, err
		}
		b, ok, err := parseBoxHeader(hdr, off, s.size)
		if err != nil || !ok || b.end() > s.size {
			return nil, nil
		}
		if b.typ == "moov" {
			if b.size > remux.MaxProgressiveMoov {
				return nil, nil
			}
			moov, whole, err := r.fetchRange(ctx, s, b.off, b.size)
			if err != nil || whole || int64(len(moov)) != b.size {
				return nil, err
			}
			return moov, nil
		}
		off = b.end()
	}
	return nil, nil
}

// fetchRange reads n bytes of the resource of s from off with one request (retried,
// renewed on 403/410). whole: the server answered with the whole resource (no ranges).
func (r *rangedRunner) fetchRange(ctx context.Context, s *rtrack, off, n int64) (data []byte, whole bool, err error) {
	err = r.retry(ctx, s, nil, func(src source) error {
		f := transport.WithNetwork(r.o.Factory, src.network).NewSlot()
		defer f.Close()
		resp, err := f.Fetch(ctx, transport.RangeRequest{Request: model.Request{URL: src.url, Headers: src.headers}, Start: off, End: off + n - 1})
		if err != nil {
			return redactURL(err)
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
			cr := resp.ContentRange
			if cr == nil || cr.Start != off || cr.End != off+n-1 {
				return fmt.Errorf("%w: asked for bytes %d-%d", ErrRangeMismatch, off, off+n-1)
			}
			// The bytes pass the job's throttle like those of the media (the speed
			// limits and Polite mode's meter, docs/POLITE-SPEC.md).
			cp := &copier{lim: r.o.hostThrottle(src.host), buf: make([]byte, min(n, sliceUncapped))}
			buf := bytes.NewBuffer(make([]byte, 0, n))
			if _, err := cp.copy(ctx, buf, resp.Body, n, false); err != nil {
				return err
			}
			data = buf.Bytes()
			return nil
		case http.StatusOK:
			whole = true
			return nil
		}
		return &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
	})
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, cerr
		}
		return nil, false, fmt.Errorf("mediajob: track %q: reading its head: %w", s.id, err)
	}
	return data, whole, nil
}

// writeSynced writes data to path through a synced temporary file.
func writeSynced(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// readTrackMoov returns the 'moov' box of the track file of s (a whole progressive file).
func readTrackMoov(s *rtrack) ([]byte, error) {
	var hdr [16]byte
	for off, count := int64(0), 0; off < s.size && count < maxTopBoxes; count++ {
		k, err := s.file.ReadAt(hdr[:min(int64(len(hdr)), s.size-off)], off)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, diskErr("reading a track file", err)
		}
		b, ok, err := parseBoxHeader(hdr[:k], off, s.size)
		if err != nil || !ok || b.end() > s.size {
			break
		}
		if b.typ == "moov" {
			if b.size > remux.MaxProgressiveMoov {
				return nil, fmt.Errorf("mediajob: track %q: %w: its 'moov' box is larger than %d bytes", s.id, media.ErrUnsupported, remux.MaxProgressiveMoov)
			}
			moov := make([]byte, b.size)
			if _, err := s.file.ReadAt(moov, b.off); err != nil {
				return nil, diskErr("reading a track file", err)
			}
			return moov, nil
		}
		off = b.end()
	}
	return nil, fmt.Errorf("mediajob: track %q: %w: the file has no 'moov' box", s.id, media.ErrUnsupported)
}

// assembleClip cuts the part from the tracks into part (docs/MEDIA-SPEC.md §8 C9/C10).
func (r *rangedRunner) assembleClip(ctx context.Context, part string) (*model.MediaClip, error) {
	w := r.clipWindow()
	var res remux.Result
	var err error
	if r.sel.Output == OutputCopy && r.tracks[0].clip != clipFrag {
		s := r.tracks[0]
		moov := s.moov
		if moov == nil {
			if moov, err = readTrackMoov(s); err != nil {
				return nil, err
			}
		}
		if hasChildBox(moov[8:], "mvex") {
			// A fragmented file (muxed, or a silent page's video): joined from its own
			// fragments.
			res, err = r.o.Assemble(ctx, []remux.Input{{Kind: remux.Kind(string(s.kind)), Container: remux.ContainerFMP4, Fragments: []string{s.path}}}, part, w)
		} else {
			res, err = r.o.Trim(ctx, remux.ProgressiveSource{Moov: moov, Data: s.path, DataBase: s.spanBase}, part, w)
		}
	} else {
		inputs := make([]remux.Input, 0, len(r.tracks))
		for _, s := range r.tracks {
			in := remux.Input{Kind: remux.Kind(string(s.kind)), Container: remux.ContainerFMP4, Fragments: []string{s.path}}
			if s.clip == clipFrag {
				in.Init = r.headPath(s)
			}
			inputs = append(inputs, in)
		}
		res, err = r.o.Assemble(ctx, inputs, part, w)
	}
	if err != nil {
		return nil, err
	}
	out := &model.MediaClip{
		StartMs: r.sel.Clip.Start.Milliseconds(), EndMs: r.sel.Clip.End.Milliseconds(),
		KeyframeMs: res.Keyframe.Milliseconds(), FetchStartMs: res.Keyframe.Milliseconds(), FetchEndMs: r.sel.Clip.End.Milliseconds(),
	}
	for _, s := range r.tracks {
		switch s.clip {
		case clipWhole, clipNone:
			out.FetchedWhole = true
		case clipFrag:
			if s.kind != media.KindAudio {
				out.FetchStartMs, out.FetchEndMs = s.spanStart.Milliseconds(), s.spanEnd.Milliseconds()
			}
		}
	}
	if out.FetchedWhole {
		out.FetchStartMs, out.FetchEndMs = 0, 0
	}
	return out, nil
}
