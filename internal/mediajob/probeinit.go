package mediajob

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
)

// What describeTracks reads of a fragmented MP4 track whose playlists declare neither
// its picture nor its codecs: the track's initialization section (at most
// describeHeadBytes), where a transport stream would carry them in its first segment,
// and, for the frame rate, the 'moof' boxes at the start of its first media segment,
// read only when that segment's size is asked for anyway (describeMoofBytes instead of
// one byte). Both are untrusted: bounded, walked by media.CheckInit first, and read in
// one pass over the boxes on the way to the few the description needs, each of which
// mp4ff decodes on its own, under recover. mp4ff never decodes a container with its
// children here: it computes their sizes again at every level, which takes time
// quadratic in their depth (a 'moov' of 'udta' boxes nested 60,000 deep, which
// media.CheckInit does not look into, took 18 s).
const (
	// describeMoofBytes bounds what is read of the first media segment of a fragmented
	// MP4 track: its 'moof' boxes (the sample durations), a few KiB in practice.
	describeMoofBytes = 64 << 10
	// maxInitCodecs bounds the codecs describeInit returns.
	maxInitCodecs = 128
)

// initPicture is what describeInit finds in the initialization section of a fragmented
// MP4 track: the picture and codecs of its first video track (pic, without a frame rate),
// and what the frame rate of a media segment is measured with: that track's ID, its mdhd
// timescale and its trex default_sample_duration (0 = none). encrypted reports protection
// information (media.CheckInit: a protected sample entry, 'sinf', 'tenc', 'pssh'), which
// Run refuses: the codecs are then left unknown, as describeTS leaves those of a stream
// the remuxer does not convert. The zero value describes nothing.
type initPicture struct {
	pic       streamPicture
	trackID   uint32
	timescale uint32
	trexDur   uint32
	encrypted bool
}

// describeInit reads an fMP4 initialization section (at most describeHeadBytes of it).
// media.CheckInit walks its boxes first: anything but protection that it refuses
// describes nothing, except that a section cut at describeHeadBytes is asked again with
// the boxes the cut leaves open closed (closeCut), so that protection in the part read
// still counts, as it does when Run checks the whole section. Then readMoov reads the
// 'moov' box. Of the first video track it takes the picture size of its sample entry
// (else of its 'tkhd') and the RFC 6381 codec of an avc1/avc3 ('avcC'), hvc1/hev1
// ('hvcC') or av01 ('av1C') entry, named with mp4ff's codec-string helpers as describeTS
// names those of a transport stream; with the first audio track's codec (initAudioCodec)
// after a comma. Codecs are left unknown when either is not named, or the section is
// encrypted. A section without a video track, cut short, one mp4ff cannot decode or one
// that Run's init parser refuses as malformed (readMoov) describes nothing, except that
// it is encrypted: that rests on media.CheckInit alone, as Run's refusal does, with
// what can be read of the video track.
func describeInit(b []byte) (in initPicture) {
	b = b[:min(len(b), describeHeadBytes)]
	err := media.CheckInit(b)
	if err != nil && !errors.Is(err, media.ErrEncrypted) && len(b) == describeHeadBytes {
		if c := closeCut(b); c != nil && errors.Is(media.CheckInit(c), media.ErrEncrypted) {
			b, err = c, media.ErrEncrypted
		}
	}
	encrypted := errors.Is(err, media.ErrEncrypted)
	if err != nil && !encrypted {
		return initPicture{}
	}
	none := initPicture{encrypted: encrypted} // what is known without mp4ff
	defer func() {
		if recover() != nil {
			in = none
		}
	}()
	m, ok := readMoov(topBox(b, "moov"))
	video, audio := m.first("vide"), m.first("soun")
	if video == nil {
		return none
	}
	ve, vok := readEntry(video.entry, "vide")
	ae, aok := sampleEntry{}, true
	if audio != nil {
		ae, aok = readEntry(audio.entry, "soun")
	}
	if !encrypted && !(ok && vok && aok) {
		return none
	}
	in = initPicture{trackID: video.tkhd.TrackID, encrypted: encrypted}
	if video.mdhd != nil {
		in.timescale = video.mdhd.Timescale
	}
	for _, trex := range m.trex {
		if trex.TrackID == in.trackID {
			in.trexDur = trex.DefaultSampleDuration
			break
		}
	}
	w, h := ve.width, ve.height
	if w == 0 || h == 0 { // the presentation size, 16.16 fixed point
		w, h = int(video.tkhd.Width>>16), int(video.tkhd.Height>>16)
	}
	if w > 0 && h > 0 && w <= 0xffff && h <= 0xffff {
		in.pic.Width, in.pic.Height = w, h
	}
	if encrypted {
		return in
	}
	codecs := initVideoCodec(ve)
	if codecs == "" {
		return in
	}
	if audio != nil {
		a := initAudioCodec(ae)
		if a == "" {
			return in // the audio is not named: the codecs stay unknown
		}
		codecs += "," + a
	}
	if len(codecs) <= maxInitCodecs {
		in.pic.Codecs = codecs
	}
	return in
}

// initMoov is what readMoov reads of a 'moov' box.
type initMoov struct {
	mvhd  bool // a movie header
	traks []initTrak
	trex  []*mp4.TrexBox // of 'mvex'
}

// initTrak is what readMoov reads of a 'trak' box: its track header, media header and
// handler (each decoded on its own; nil when missing), whether it has a 'mdia' box, and
// the sample entries of its 'stsd' (the first one whole, header included).
type initTrak struct {
	tkhd    *mp4.TkhdBox
	mdia    bool
	mdhd    *mp4.MdhdBox
	hdlr    *mp4.HdlrBox
	entries int
	entry   []byte
}

// first returns the first track of handler type h ("vide", "soun") with a track
// header, or nil.
func (m *initMoov) first(h string) *initTrak {
	for i := range m.traks {
		if t := &m.traks[i]; t.tkhd != nil && t.hdlr != nil && t.hdlr.HandlerType == h {
			return t
		}
	}
	return nil
}

// readMoov reads a 'moov' box (header included) in one pass over the boxes on the way:
// its 'mvhd'; of each 'trak' its 'tkhd', and in 'mdia' its 'mdhd', 'hdlr' and, through
// 'minf' and 'stbl', the sample entries of 'stsd'; the 'trex' boxes of 'mvex'. Each box it
// takes is decoded on its own with mp4ff; others are passed over unread. ok is false when
// a box on the way is malformed as mp4ff reads it (eachBox), one taken does not decode, or
// the 'moov' is one Run's init parser (remux) refuses as malformed: without a movie
// header; a track without its 'tkhd', 'mdia', 'mdhd' or 'hdlr', with track ID 0 or an ID
// used before; a video or audio track without a sample entry or with timescale 0. The
// tracks read until then stay in m.
func readMoov(moov []byte) (m initMoov, ok bool) {
	_, _, payload, _, ok := nextBox(moov)
	if !ok {
		return m, false
	}
	ids := make(map[uint32]bool)
	ok = eachBox(payload, false, func(typ string, whole, body []byte) bool {
		switch typ {
		case "mvhd":
			_, m.mvhd = decodeBox[*mp4.MvhdBox](whole)
			return m.mvhd
		case "trak":
			m.traks = append(m.traks, initTrak{})
			t := &m.traks[len(m.traks)-1]
			if !readTrak(t, body) || t.tkhd == nil || !t.mdia || t.mdhd == nil || t.hdlr == nil ||
				t.tkhd.TrackID == 0 || ids[t.tkhd.TrackID] {
				return false
			}
			ids[t.tkhd.TrackID] = true
			h := t.hdlr.HandlerType
			return h != "vide" && h != "soun" || t.entries > 0 && t.mdhd.Timescale > 0
		case "mvex":
			return eachBox(body, false, func(typ string, whole, _ []byte) bool {
				if typ != "trex" {
					return true
				}
				trex, ok := decodeBox[*mp4.TrexBox](whole)
				if ok {
					m.trex = append(m.trex, trex)
				}
				return ok
			})
		}
		return true
	})
	return m, ok && m.mvhd
}

// readTrak reads the boxes of a 'trak' box's payload into t (readMoov).
func readTrak(t *initTrak, p []byte) bool {
	return eachBox(p, false, func(typ string, whole, body []byte) bool {
		var ok bool
		switch typ {
		case "tkhd":
			t.tkhd, ok = decodeBox[*mp4.TkhdBox](whole)
			return ok
		case "mdia":
			t.mdia = true
			return eachBox(body, false, func(typ string, whole, body []byte) bool {
				switch typ {
				case "mdhd":
					t.mdhd, ok = decodeBox[*mp4.MdhdBox](whole)
					return ok
				case "hdlr":
					t.hdlr, ok = decodeBox[*mp4.HdlrBox](whole)
					return ok
				case "minf":
					return eachBox(body, false, func(typ string, _, body []byte) bool {
						return typ != "stbl" || eachBox(body, false, func(typ string, _, body []byte) bool {
							return typ != "stsd" || readStsd(t, body)
						})
					})
				}
				return true
			})
		}
		return true
	})
}

// readStsd reads the payload of a SampleDescriptionBox into t: version and flags,
// entry_count, then the sample entries, as many as there are boxes (ISO/IEC 14496-12
// §8.5.2).
func readStsd(t *initTrak, p []byte) bool {
	if len(p) < 8 {
		return false
	}
	return eachBox(p[8:], false, func(_ string, whole, _ []byte) bool {
		if t.entries == 0 {
			t.entry = whole
		}
		t.entries++
		return true
	})
}

// sampleEntry is what readEntry reads of a sample entry: its type, the picture size of a
// visual entry (0 = not given), and the decoder configuration box that names the codec
// of a type the description names (configType), decoded on its own: *mp4.AvcCBox,
// *mp4.HvcCBox, *mp4.Av1CBox or *mp4.EsdsBox; nil when the entry has none.
type sampleEntry struct {
	typ           string
	width, height int
	config        mp4.Box
}

// readEntry reads a sample entry (header included) of a track of handler type h, laid
// out as mp4ff reads it: a visual entry ("vide") has 78 bytes of SampleEntry and
// VisualSampleEntry fields (ISO/IEC 14496-12 §12.1.3, width and height at 24) before its
// child boxes, an audio entry ("soun") those of audioChildren. ok is false when the entry
// is malformed so or its configuration box does not decode; the picture size is read
// before the child boxes are.
func readEntry(entry []byte, h string) (e sampleEntry, ok bool) {
	typ, _, p, _, ok := nextBox(entry)
	if !ok {
		return e, false
	}
	e.typ = typ
	var children []byte
	trailing := false
	switch h {
	case "vide":
		if len(p) < 78 {
			return e, false
		}
		e.width, e.height = int(binary.BigEndian.Uint16(p[24:])), int(binary.BigEndian.Uint16(p[26:]))
		// mp4ff keeps fewer than 8 bytes after the last child box of a visual entry.
		children, trailing = p[78:], true
	case "soun":
		if children, ok = audioChildren(p); !ok {
			return e, false
		}
	default:
		return e, true
	}
	config := configType(typ)
	ok = eachBox(children, trailing, func(typ string, whole, _ []byte) bool {
		if typ != config || e.config != nil {
			return true
		}
		var decoded bool
		e.config, decoded = decodeBox[mp4.Box](whole)
		return decoded
	})
	return e, ok
}

// audioChildren returns the child boxes of the payload p of an audio sample entry as
// mp4ff finds them: after its 28 bytes of SampleEntry and AudioSampleEntry fields
// (ISO/IEC 14496-12 §12.2.3) and, where the version at byte 8 says QuickTime's sound
// description of version 1 or 2 follows, its 16 or 36 more bytes (version 2 with its
// 0x7f000000 marker at byte 44); when those do not leave whole boxes and the version is
// not 0, after the 28 bytes. ok is false when neither does.
func audioChildren(p []byte) ([]byte, bool) {
	if len(p) < 28 {
		return nil, false
	}
	whole := func(p []byte) bool { return eachBox(p, false, func(string, []byte, []byte) bool { return true }) }
	version, at := binary.BigEndian.Uint16(p[8:]), 28
	switch version {
	case 1:
		at += 16
	case 2:
		at += 36
	}
	if len(p) >= at && (version != 2 || binary.BigEndian.Uint32(p[44:]) == 0x7f000000) && whole(p[at:]) {
		return p[at:], true
	}
	if version != 0 && whole(p[28:]) {
		return p[28:], true
	}
	return nil, false
}

// configType is the type of the decoder configuration box that names the codec of a
// sample entry of type entry ("avcC" of avc1), "" for the types the description does
// not name from such a box.
func configType(entry string) string {
	switch entry {
	case "avc1", "avc3":
		return "avcC"
	case "hvc1", "hev1":
		return "hvcC"
	case "av01":
		return "av1C"
	case "mp4a":
		return "esds"
	}
	return ""
}

// initVideoCodec names a video sample entry as RFC 6381 does ("avc1.640028",
// "hvc1.1.6.L93.90", "av01.0.08M.08") from its decoder configuration record; "" for
// other entries or one without its record.
func initVideoCodec(e sampleEntry) string {
	switch e.typ {
	case "avc1", "avc3":
		c, ok := e.config.(*mp4.AvcCBox)
		if !ok {
			return ""
		}
		r := &c.DecConfRec
		return avc.CodecString(e.typ, &avc.SPS{Profile: uint32(r.AVCProfileIndication),
			ProfileCompatibility: uint32(r.ProfileCompatibility), Level: uint32(r.AVCLevelIndication)})
	case "hvc1", "hev1":
		c, ok := e.config.(*mp4.HvcCBox)
		if !ok {
			return ""
		}
		r := &c.DecConfRec
		return hevc.CodecString(e.typ, &hevc.SPS{ProfileTierLevel: hevc.ProfileTierLevel{
			GeneralProfileSpace:              r.GeneralProfileSpace,
			GeneralTierFlag:                  r.GeneralTierFlag,
			GeneralProfileIDC:                r.GeneralProfileIDC,
			GeneralProfileCompatibilityFlags: r.GeneralProfileCompatibilityFlags,
			GeneralConstraintIndicatorFlags:  r.GeneralConstraintIndicatorFlags,
			GeneralLevelIDC:                  r.GeneralLevelIDC,
		}})
	case "av01":
		c, ok := e.config.(*mp4.Av1CBox)
		if !ok {
			return ""
		}
		return c.CodecString(e.typ)
	}
	return ""
}

// initAudioCodec names an audio sample entry as RFC 6381 does: "mp4a.40.<audio object
// type>" for MPEG-4 audio ('esds' with ObjectTypeIndication 0x40 and an
// AudioSpecificConfig, as adtsCodec names AAC), and the fixed codes of AC-3, E-AC-3,
// Opus and FLAC; "" for others.
func initAudioCodec(e sampleEntry) string {
	switch e.typ {
	case "mp4a":
		esds, ok := e.config.(*mp4.EsdsBox)
		if !ok || esds.DecConfigDescriptor == nil {
			return ""
		}
		dc := esds.DecConfigDescriptor
		if dc.ObjectType != 0x40 || dc.DecSpecificInfo == nil {
			return ""
		}
		h, err := aac.DecodeAudioSpecificConfigHeader(bytes.NewReader(dc.DecSpecificInfo.DecConfig))
		if err != nil || h.ObjectType == 0 {
			return ""
		}
		return fmt.Sprintf("mp4a.40.%d", h.ObjectType)
	case "ac-3", "ec-3":
		return e.typ
	case "Opus":
		return "opus"
	case "fLaC":
		return "fLaC"
	}
	return ""
}

// frameRate measures the frame rate of in's video track from the head of a media
// segment (at most describeMoofBytes of it): the first top-level 'moof' with a 'traf' of
// the track (after any 'styp', 'sidx', 'prft' or 'emsg' box, and the fragments of other
// tracks: a packager may write one 'moof' per track) gives the track's samples and their
// durations (trun's sample_duration, else tfhd's default_sample_duration, else trex's) in
// the mdhd timescale. 0 when in has no video track or timescale, no such 'moof' is
// complete in the head, its boxes do not decode (moofSamples), it holds no sample of the
// track or a sample without a duration, or the rate is outside 1-240 frames per second.
func (in initPicture) frameRate(b []byte) (fps float64) {
	if in.trackID == 0 || in.timescale == 0 {
		return 0
	}
	defer func() {
		if recover() != nil {
			fps = 0
		}
	}()
	for p := b[:min(len(b), describeMoofBytes)]; len(p) >= 8; {
		typ, _, payload, rest, ok := nextBox(p)
		if !ok {
			return 0 // the head ends within this box
		}
		p = rest
		if typ != "moof" {
			continue
		}
		if samples, total, found := in.moofSamples(payload); found {
			if samples == 0 || total == 0 {
				return 0
			}
			return roundRate(float64(samples) * float64(in.timescale) / float64(total))
		}
	}
	return 0
}

// moofSamples counts the samples of in's track in the payload of a 'moof' box, and
// their durations: of each 'traf' its 'tfhd' and 'trun' boxes, each decoded on its own
// with mp4ff. found is set when a 'traf' is of the track, or the 'moof' is malformed as
// mp4ff reads it (eachBox), a box taken does not decode or a sample of the track has no
// duration: those count nothing.
func (in initPicture) moofSamples(moof []byte) (samples, total uint64, found bool) {
	ok := eachBox(moof, false, func(typ string, _, traf []byte) bool {
		if typ != "traf" {
			return true
		}
		var tfhd *mp4.TfhdBox
		var truns []*mp4.TrunBox
		if !eachBox(traf, false, func(typ string, whole, _ []byte) bool {
			var ok bool
			switch {
			case typ == "tfhd" && tfhd == nil:
				tfhd, ok = decodeBox[*mp4.TfhdBox](whole)
				return ok
			case typ == "trun":
				var trun *mp4.TrunBox
				trun, ok = decodeBox[*mp4.TrunBox](whole)
				truns = append(truns, trun)
				return ok
			}
			return true
		}) {
			return false
		}
		if tfhd == nil || tfhd.TrackID != in.trackID {
			return true
		}
		found = true
		def := in.trexDur
		if tfhd.HasDefaultSampleDuration() {
			def = tfhd.DefaultSampleDuration
		}
		for _, trun := range truns {
			for _, s := range trun.Samples {
				d := def
				if trun.HasSampleDuration() {
					d = s.Dur
				}
				if d == 0 {
					return false
				}
				samples++
				total += uint64(d)
			}
		}
		return true
	})
	if !ok {
		return 0, 0, true
	}
	return samples, total, found
}

// decodeBox decodes one box (header included) on its own with mp4ff, which reads exactly
// its payload; ok is false when it does not decode, or not as a T. Callers pass only
// boxes without child boxes, or whose children mp4ff reads as descriptors or records
// (an 'esds', 'avcC', 'hvcC' or 'av1C'), in linear time.
func decodeBox[T mp4.Box](whole []byte) (T, bool) {
	box, err := mp4.DecodeBoxSR(0, bits.NewFixedSliceReader(whole))
	v, ok := box.(T)
	return v, err == nil && ok
}

// eachBox calls fn with the type, the whole box (header included) and the payload of
// each ISO-BMFF box that p holds, in order, as mp4ff reads the boxes inside a 'moov' or
// 'moof': a box that does not fit what is left of p, or with a size of 0 (to the end) or
// a 64-bit size, which mp4ff refuses there, is malformed. With trailing, fewer than 8
// bytes may follow the last box (mp4ff keeps them after those of a visual sample entry).
// It returns false at the first malformed box, or when fn does.
func eachBox(p []byte, trailing bool, fn func(typ string, whole, payload []byte) bool) bool {
	for len(p) > 0 {
		if trailing && len(p) < 8 {
			return true
		}
		if len(p) < 8 || binary.BigEndian.Uint32(p) <= 1 {
			return false
		}
		typ, whole, payload, rest, ok := nextBox(p)
		if !ok || !fn(typ, whole, payload) {
			return false
		}
		p = rest
	}
	return true
}

// nextBox splits the ISO-BMFF box at the start of p (ISO/IEC 14496-12 §4.2) from what
// follows it: its type, the whole box and its payload. A size of 0 extends the box to
// the end of p; a size of 1 is followed by a 64-bit size. ok is false when p ends before
// the box does, or its size is smaller than its header.
func nextBox(p []byte) (typ string, whole, payload, rest []byte, ok bool) {
	if len(p) < 8 {
		return "", nil, nil, nil, false
	}
	size, hdr := uint64(binary.BigEndian.Uint32(p)), uint64(8)
	switch size {
	case 0:
		size = uint64(len(p))
	case 1:
		if len(p) < 16 {
			return "", nil, nil, nil, false
		}
		size, hdr = binary.BigEndian.Uint64(p[8:]), 16
	}
	if size < hdr || size > uint64(len(p)) {
		return "", nil, nil, nil, false
	}
	return string(p[4:8]), p[:size], p[hdr:size], p[size:], true
}

// topBox returns the first top-level ISO-BMFF box of type typ in b (header included)
// when the boxes before it and the box itself lie wholly within b; nil when b ends
// before, or a box size is malformed.
func topBox(b []byte, typ string) []byte {
	for p := b; len(p) >= 8; {
		t, whole, _, rest, ok := nextBox(p)
		if !ok {
			return nil
		}
		if t == typ {
			return whole
		}
		p = rest
	}
	return nil
}

// closeCut returns a copy of b, the start of an initialization section cut at the end of
// b, in which every box the cut leaves open ends where b ends: the last box of the top
// level that extends past the end gets the size of what is left of it, and so does the
// last one inside it when it is a container whose boxes media.CheckInit walks from a
// fixed offset ('moov', 'trak', 'mdia', 'minf', 'stbl', 'moof', 'traf' from 0, the
// sample entries of 'stsd' after 8 bytes), and so on down. Where the cut splits a box
// header, or the fields before a container's boxes, that box is left out. Only sizes
// change, so media.CheckInit sees every box of the part read where it is. nil when the
// boxes are malformed apart from the cut.
func closeCut(b []byte) []byte {
	c := bytes.Clone(b)
	for range 2 { // again once the box whose header the cut splits is left out
		end := closeOpen(c)
		switch {
		case end == len(c):
			return c
		case end < 0:
			return nil
		}
		c = c[:end]
	}
	return nil
}

// closeOpen gives each box of c that extends past its end the size of what is left of it
// (closeCut), from the top level down. It returns len(c) when that closes them all, the
// offset of a box whose header (or fields before its boxes) the end splits, or -1 when a
// box is malformed.
func closeOpen(c []byte) int {
	for at := 0; at < len(c); {
		rem := c[at:]
		if len(rem) < 8 {
			return at
		}
		typ := string(rem[4:8])
		size, hdr := uint64(binary.BigEndian.Uint32(rem)), 8
		switch size {
		case 0:
			size = uint64(len(rem))
		case 1:
			if len(rem) < 16 {
				return at
			}
			size, hdr = binary.BigEndian.Uint64(rem[8:]), 16
		}
		if typ == "uuid" {
			hdr += 16 // the extended type
		}
		if len(rem) < hdr {
			return at
		}
		if size < uint64(hdr) {
			return -1
		}
		if size <= uint64(len(rem)) {
			at += int(size) // a whole box: the next one is on the same level
			continue
		}
		if binary.BigEndian.Uint32(rem) == 1 { // the 64-bit size
			binary.BigEndian.PutUint64(rem[8:], uint64(len(rem)))
		} else { // at most describeHeadBytes
			binary.BigEndian.PutUint32(rem, uint32(len(rem)))
		}
		skip := -1 // the fields before the boxes inside it
		switch typ {
		case "moov", "trak", "mdia", "minf", "stbl", "moof", "traf":
			skip = 0
		case "stsd":
			skip = 8
		}
		if skip < 0 {
			return len(c)
		}
		if len(rem) < hdr+skip {
			return at
		}
		at += hdr + skip // the boxes inside it, which now end where c ends
	}
	return len(c)
}
