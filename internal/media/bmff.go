package media

import (
	"encoding/binary"
	"errors"
)

// CheckInit walks the ISO-BMFF boxes (ISO/IEC 14496-12 §4.2) of an fMP4
// initialization segment and returns ErrEncrypted when it finds protection
// information: a 'pssh', 'tenc', 'sinf' or 'senc' box, or a protected sample entry
// ('encv', 'enca', 'enct', 'encs', 'encm', 'encf'). It descends into moov, trak,
// mdia, minf, stbl, stsd, sinf, schi, moof and traf, and into the child boxes of video
// and audio sample entries, to at most MaxBoxDepth levels.
//
// data may also be a prefix of a whole file: a trailing top-level box that is not
// descended into (such as 'mdat') may be cut short. Otherwise a box that does not fit
// its container, a size field smaller than its header, or data without a 'moov' box is
// malformed.
func CheckInit(data []byte) error {
	if len(data) == 0 {
		return errf(ErrMalformed, "empty initialization segment")
	}
	w := &boxWalker{}
	if err := w.walk(data, 0); err != nil {
		return err
	}
	if !w.sawMoov {
		return errf(ErrMalformed, "initialization segment has no 'moov' box")
	}
	return nil
}

// InitHasVideo reports whether the 'moov' of an initialization segment (or of a whole
// MP4 file's head) has a video track: a 'trak' whose handler is 'vide'. The data is
// walked as CheckInit walks it, and its errors are CheckInit's. An audio download
// (docs/SITE-MEDIA-SPEC.md §3.13) refuses such data, so it never saves a picture.
func InitHasVideo(data []byte) (bool, error) {
	if len(data) == 0 {
		return false, errf(ErrMalformed, "empty initialization segment")
	}
	w := &boxWalker{}
	if err := w.walk(data, 0); err != nil {
		return false, err
	}
	if !w.sawMoov {
		return false, errf(ErrMalformed, "initialization segment has no 'moov' box")
	}
	return w.video, nil
}

type boxWalker struct {
	count   int // boxes visited
	sawMoov bool
	// video reports a track whose handler is 'vide' (InitHasVideo).
	video bool
	// handler is the handler_type of the current track ('vide', 'soun', ...), which
	// decides the layout of its sample entries.
	handler string
}

func isProtectionBox(t string) bool {
	switch t {
	case "pssh", "tenc", "sinf", "senc", "encv", "enca", "enct", "encs", "encm", "encf":
		return true
	}
	return false
}

func isContainerBox(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "stsd", "sinf", "schi", "moof", "traf":
		return true
	}
	return false
}

// eachBox calls fn with the type and payload of each box in data, after refusing
// protection boxes. At the top level (depth 0) a trailing box that fn would not
// descend into may be truncated.
func (w *boxWalker) eachBox(data []byte, depth int, fn func(typ string, payload []byte) error) error {
	if depth > MaxBoxDepth {
		return errf(ErrTooLarge, "boxes nest deeper than %d", MaxBoxDepth)
	}
	complete := 0
	for off := 0; off < len(data); {
		if w.count++; w.count > MaxBoxes {
			return errf(ErrTooLarge, "more than %d boxes", MaxBoxes)
		}
		rem := data[off:]
		prefixEnd := depth == 0 && complete > 0
		if len(rem) < 8 {
			if prefixEnd {
				return nil
			}
			return errf(ErrMalformed, "truncated box header")
		}
		size := uint64(binary.BigEndian.Uint32(rem))
		typ := string(rem[4:8])
		hdr := uint64(8)
		switch size {
		case 0: // the box extends to the end of its container
			size = uint64(len(rem))
		case 1: // a 64-bit largesize follows the type
			if len(rem) < 16 {
				if prefixEnd {
					return nil
				}
				return errf(ErrMalformed, "truncated box header")
			}
			size, hdr = binary.BigEndian.Uint64(rem[8:16]), 16
		}
		if typ == "uuid" {
			hdr += 16 // extended type
		}
		if isProtectionBox(typ) {
			return errf(ErrEncrypted, "initialization segment has a %q box", typ)
		}
		if size < hdr {
			return errf(ErrMalformed, "box %q is smaller than its header", typ)
		}
		if size > uint64(len(rem)) {
			if prefixEnd && !isContainerBox(typ) {
				return nil
			}
			return errf(ErrMalformed, "box %q extends past the end of its container", typ)
		}
		if err := fn(typ, rem[hdr:size]); err != nil {
			return err
		}
		off += int(size)
		complete++
	}
	return nil
}

func (w *boxWalker) walk(data []byte, depth int) error {
	return w.eachBox(data, depth, func(typ string, payload []byte) error {
		switch typ {
		case "moov":
			w.sawMoov = true
		case "trak":
			w.handler = ""
		case "hdlr":
			// handler_type follows version/flags and pre_defined; a data handler
			// reference ('dhlr') describes storage, not media.
			if len(payload) >= 12 && string(payload[4:8]) != "dhlr" {
				w.handler = string(payload[8:12])
				w.video = w.video || w.handler == "vide"
			}
			return nil
		case "stsd":
			return w.stsd(payload, depth+1)
		}
		if isContainerBox(typ) {
			return w.walk(payload, depth+1)
		}
		return nil
	})
}

// stsd walks a SampleDescriptionBox: version/flags, entry_count, then sample entries.
func (w *boxWalker) stsd(payload []byte, depth int) error {
	if len(payload) < 8 {
		return errf(ErrMalformed, "'stsd' box is too short")
	}
	version := payload[0]
	return w.eachBox(payload[8:], depth, func(_ string, entry []byte) error {
		return w.sampleEntry(entry, version, depth)
	})
}

// sampleEntry looks for protection boxes among the child boxes of a video or audio
// sample entry, which follow fixed fields (ISO/IEC 14496-12 §12.1.3, §12.2.3). The
// layout of those fields varies, so structural errors inside them are ignored.
func (w *boxWalker) sampleEntry(entry []byte, stsdVersion byte, depth int) error {
	var skip int
	switch w.handler {
	case "vide":
		skip = 8 + 70 // SampleEntry + VisualSampleEntry fields
	case "soun":
		skip = 8 + 20 // SampleEntry + AudioSampleEntry fields
		if len(entry) >= 10 {
			switch binary.BigEndian.Uint16(entry[8:10]) {
			case 1:
				if stsdVersion == 0 {
					skip += 16 // sound sample description version 1
				}
			case 2:
				skip += 36 // sound sample description version 2
			}
		}
	default:
		return nil
	}
	if len(entry) <= skip {
		return nil
	}
	handler := w.handler
	err := w.walk(entry[skip:], depth+1)
	w.handler = handler
	if errors.Is(err, ErrEncrypted) {
		return err
	}
	return nil
}
