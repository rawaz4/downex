package remux

import (
	"bytes"
	"errors"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
)

// initTrack is one audio or video track of an initialization segment.
type initTrack struct {
	trak      *mp4.TrakBox
	trackID   uint32
	handler   string // "vide" or "soun"
	timescale uint32
	stsd      *mp4.StsdBox
	nEntries  uint32 // sample descriptions in stsd
	stsdBytes []byte // encoded stsd, to detect changes
	// The first non-empty edit of the track's edit list (§8.6.6), if any:
	// leading empty edits in movie timescale units and the media time it starts at.
	emptyEdit uint64
	mediaTime int64
}

// initInfo is a parsed initialization segment ('moov' box).
type initInfo struct {
	movieTimescale uint32
	tracks         []*initTrack            // audio and video tracks in trak order
	trex           map[uint32]trexDefaults // every declared track
}

// parseInit parses the bytes of a 'moov' box (header included).
func parseInit(moov []byte) (info *initInfo, err error) {
	if err := media.CheckInit(moov); err != nil {
		if errors.Is(err, media.ErrEncrypted) {
			return nil, err
		}
		return nil, errf(ErrMalformed, "initialization segment: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			info, err = nil, errf(ErrMalformed, "initialization segment: undecodable 'moov' box")
		}
	}()
	// The decoded boxes may keep slices of their input, which the caller reuses.
	moov = bytes.Clone(moov)
	box, err := mp4.DecodeBoxSR(0, bits.NewFixedSliceReader(moov))
	if err != nil {
		return nil, errf(ErrMalformed, "initialization segment: %v", err)
	}
	mv, ok := box.(*mp4.MoovBox)
	if !ok || mv.Mvhd == nil {
		return nil, errf(ErrMalformed, "initialization segment has no movie header")
	}
	info = &initInfo{movieTimescale: mv.Mvhd.Timescale, trex: map[uint32]trexDefaults{}}
	for _, trak := range mv.Traks {
		if trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Mdhd == nil || trak.Mdia.Hdlr == nil {
			return nil, errf(ErrMalformed, "initialization segment has an incomplete track")
		}
		id := trak.Tkhd.TrackID
		if id == 0 {
			return nil, errf(ErrMalformed, "initialization segment has a track with ID 0")
		}
		if _, dup := info.trex[id]; dup {
			return nil, errf(ErrMalformed, "initialization segment declares track %d twice", id)
		}
		info.trex[id] = trexDefaults{sdi: 1}
		h := trak.Mdia.Hdlr.HandlerType
		if h != "vide" && h != "soun" {
			continue // text, metadata, hint and other tracks are not kept
		}
		mi := trak.Mdia.Minf
		if mi == nil || mi.Stbl == nil || mi.Stbl.Stsd == nil || len(mi.Stbl.Stsd.Children) == 0 {
			return nil, errf(ErrMalformed, "track %d has no sample description", id)
		}
		ts := trak.Mdia.Mdhd.Timescale
		if ts == 0 {
			return nil, errf(ErrMalformed, "track %d has timescale 0", id)
		}
		var buf bytes.Buffer
		if err := mi.Stbl.Stsd.Encode(&buf); err != nil {
			return nil, errf(ErrMalformed, "track %d: sample description: %v", id, err)
		}
		t := &initTrack{
			trak: trak, trackID: id, handler: h, timescale: ts,
			stsd: mi.Stbl.Stsd, nEntries: uint32(len(mi.Stbl.Stsd.Children)), stsdBytes: buf.Bytes(),
		}
		if trak.Edts != nil {
			for _, elst := range trak.Edts.Elst {
				for _, e := range elst.Entries {
					if e.MediaTime == -1 {
						t.emptyEdit += e.SegmentDuration
						continue
					}
					if e.MediaTime > 0 {
						t.mediaTime = e.MediaTime
					}
					break
				}
				break
			}
		}
		info.tracks = append(info.tracks, t)
	}
	if mv.Mvex != nil {
		for _, tr := range mv.Mvex.Trexs {
			if _, ok := info.trex[tr.TrackID]; ok {
				sdi := tr.DefaultSampleDescriptionIndex
				info.trex[tr.TrackID] = trexDefaults{sdi: sdi, dur: tr.DefaultSampleDuration, size: tr.DefaultSampleSize, flags: tr.DefaultSampleFlags}
			}
		}
	}
	if len(info.tracks) == 0 {
		return nil, errf(ErrUnsupported, "initialization segment has no audio or video track")
	}
	return info, nil
}

// sameTracks reports whether b describes the kept tracks of a identically
// (a repeated initialization segment).
func sameTracks(a, b *initInfo) bool {
	if len(a.tracks) != len(b.tracks) {
		return false
	}
	for i, t := range a.tracks {
		u := b.tracks[i]
		if t.trackID != u.trackID || t.handler != u.handler || t.timescale != u.timescale || !bytes.Equal(t.stsdBytes, u.stsdBytes) {
			return false
		}
	}
	for id, d := range a.trex {
		if e, ok := b.trex[id]; !ok || e != d {
			return false
		}
	}
	return len(a.trex) == len(b.trex)
}
