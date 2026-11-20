package played

import (
	"encoding/binary"
	"errors"
	"time"

	"downex/internal/media"
)

// Desc is what a file's head says of it (ISO/IEC 14496-12).
type Desc struct {
	// Video and Audio report a track whose handler is 'vide' or 'soun'.
	Video, Audio bool
	// VideoCodec and AudioCodec are the first sample entry's type ("avc1", "hvc1", "av01",
	// "vp09", "mp4a", "Opus", ...); "" when not seen.
	VideoCodec, AudioCodec string
	// Width and Height are the video track's picture (tkhd, else the sample entry); 0 =
	// unknown.
	Width, Height int
	// Duration is the presentation's duration: the 'mvhd' duration, else the fragment
	// duration of 'mehd', else the longest track's 'mdhd' duration, else the sum of the
	// top-level 'sidx' subsegments; 0 = the head does not say.
	Duration time.Duration
	// Fragmented: the movie has an 'mvex' box (its samples are in movie fragments).
	Fragmented bool
	// Complete: the whole 'moov' box was in the head, so its track list is complete.
	Complete bool
	// Encrypted: a protection box or a protected sample entry was seen.
	Encrypted bool
	// Moov: a 'moov' box starts in the head (whole or cut). Lead is the head's first box
	// type: a head without a 'moov' that leads with 'moof', 'styp' or 'sidx' is a stream's
	// media segment, never a whole file.
	Moov bool
	Lead string
}

// ErrNotMP4 reports a head that does not start like an ISO-BMFF file.
var ErrNotMP4 = errors.New("played: the file is not an MP4")

// maxBoxes bounds the boxes Describe visits.
const maxBoxes = 20_000

type describer struct {
	d     Desc
	count int
	// movie and track timescales and durations.
	mvhdScale, mvhdDur uint64
	mehdDur            uint64
	trackSecs          float64
	handler            string
	trackW, trackH     int
}

// box is one box found in data: its type, payload, and whether it was cut short.
type box struct {
	typ     string
	payload []byte
	cut     bool
	start   int // offset of the box in its container
	size    uint64
	header  int
}

// boxes walks the boxes of data, calling fn for each; a box that extends past data is
// passed cut (its available payload) and ends the walk. It stops early when fn returns
// false.
func (w *describer) boxes(data []byte, fn func(b box) bool) {
	for off := 0; off+8 <= len(data); {
		if w.count++; w.count > maxBoxes {
			return
		}
		rem := data[off:]
		size := uint64(binary.BigEndian.Uint32(rem))
		typ := string(rem[4:8])
		hdr := 8
		switch size {
		case 0:
			size = uint64(len(rem))
		case 1:
			if len(rem) < 16 {
				return
			}
			size, hdr = binary.BigEndian.Uint64(rem[8:16]), 16
		}
		if typ == "uuid" {
			hdr += 16
		}
		if size < uint64(hdr) {
			return
		}
		b := box{typ: typ, start: off, size: size, header: hdr}
		if size > uint64(len(rem)) {
			if hdr <= len(rem) {
				b.payload, b.cut = rem[hdr:], true
				fn(b)
			}
			return
		}
		b.payload = rem[hdr:size]
		if !fn(b) {
			return
		}
		off += int(size)
	}
}

func protectionBox(t string) bool {
	switch t {
	case "pssh", "tenc", "sinf", "senc", "encv", "enca", "enct", "encs", "encm", "encf":
		return true
	}
	return false
}

// Describe reads a file's head: the first bytes of an MP4 (ISO/IEC 14496-12), as long as
// the browser read them. A head cut inside 'moov' still gives what precedes the cut (the
// 'mvhd' duration comes first). A head that does not start with a known top-level box is
// ErrNotMP4.
func Describe(head []byte) (Desc, error) {
	if len(head) < 8 {
		return Desc{}, ErrNotMP4
	}
	switch string(head[4:8]) {
	case "ftyp", "styp", "moov", "sidx", "free", "skip", "wide", "pdin", "moof":
	default:
		return Desc{}, ErrNotMP4
	}
	w := &describer{}
	w.d.Lead = string(head[4:8])
	var sidxSecs float64
	w.boxes(head, func(b box) bool {
		if protectionBox(b.typ) {
			w.d.Encrypted = true
		}
		switch b.typ {
		case "moov":
			w.d.Moov, w.d.Complete = true, !b.cut
			w.moov(b.payload)
		case "sidx":
			if !b.cut && sidxSecs == 0 {
				if idx, err := media.ParseSidx(head[b.start:b.start+int(b.size)], int64(b.start)+int64(b.size)); err == nil {
					var total time.Duration
					for _, r := range idx.Refs {
						total += r.Duration
					}
					sidxSecs = total.Seconds()
				}
			}
		case "moof":
			w.d.Fragmented = true
		case "mdat":
			return false
		}
		return !b.cut
	})
	switch {
	case w.mvhdScale > 0 && w.mvhdDur > 0 && w.mvhdDur != 0xFFFFFFFF && w.mvhdDur != 1<<64-1:
		w.d.Duration = seconds(float64(w.mvhdDur) / float64(w.mvhdScale))
	case w.mvhdScale > 0 && w.mehdDur > 0:
		w.d.Duration = seconds(float64(w.mehdDur) / float64(w.mvhdScale))
	case w.trackSecs > 0:
		w.d.Duration = seconds(w.trackSecs)
	case sidxSecs > 0:
		w.d.Duration = seconds(sidxSecs)
	}
	return w.d, nil
}

// seconds converts a duration in seconds, bounded to MaxDuration (more is unknown).
func seconds(s float64) time.Duration {
	if s <= 0 || s > MaxDuration.Seconds() {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

func (w *describer) moov(data []byte) {
	w.boxes(data, func(b box) bool {
		if protectionBox(b.typ) {
			w.d.Encrypted = true
		}
		switch b.typ {
		case "mvhd":
			w.mvhdScale, w.mvhdDur = fullBoxTimes(b.payload)
		case "mvex":
			w.d.Fragmented = true
			w.boxes(b.payload, func(c box) bool {
				if c.typ == "mehd" && len(c.payload) >= 8 {
					if c.payload[0] == 1 && len(c.payload) >= 12 {
						w.mehdDur = binary.BigEndian.Uint64(c.payload[4:12])
					} else {
						w.mehdDur = uint64(binary.BigEndian.Uint32(c.payload[4:8]))
					}
				}
				return true
			})
		case "trak":
			w.handler, w.trackW, w.trackH = "", 0, 0
			w.trak(b.payload)
		}
		return !b.cut
	})
}

// fullBoxTimes reads the timescale and duration of an 'mvhd' or 'mdhd' payload (versions 0
// and 1; 0, 0 when it is too short).
func fullBoxTimes(p []byte) (scale, dur uint64) {
	if len(p) < 4 {
		return 0, 0
	}
	if p[0] == 1 {
		if len(p) < 32 {
			return 0, 0
		}
		return uint64(binary.BigEndian.Uint32(p[20:24])), binary.BigEndian.Uint64(p[24:32])
	}
	if len(p) < 20 {
		return 0, 0
	}
	return uint64(binary.BigEndian.Uint32(p[12:16])), uint64(binary.BigEndian.Uint32(p[16:20]))
}

func (w *describer) trak(data []byte) {
	var mdhdScale, mdhdDur uint64
	var walk func(p []byte)
	walk = func(p []byte) {
		w.boxes(p, func(b box) bool {
			if protectionBox(b.typ) {
				w.d.Encrypted = true
			}
			switch b.typ {
			case "tkhd":
				// width and height are the last 8 bytes, 16.16 fixed point.
				if n := len(b.payload); !b.cut && n >= 84 {
					w.trackW = int(binary.BigEndian.Uint32(b.payload[n-8:]) >> 16)
					w.trackH = int(binary.BigEndian.Uint32(b.payload[n-4:]) >> 16)
				}
			case "mdhd":
				mdhdScale, mdhdDur = fullBoxTimes(b.payload)
			case "hdlr":
				if len(b.payload) >= 12 {
					w.handler = string(b.payload[8:12])
				}
			case "mdia", "minf", "stbl":
				walk(b.payload)
			case "stsd":
				w.stsd(b.payload)
			}
			return !b.cut
		})
	}
	walk(data)
	if mdhdScale > 0 && mdhdDur > 0 && mdhdDur != 0xFFFFFFFF && mdhdDur != 1<<64-1 {
		if s := float64(mdhdDur) / float64(mdhdScale); s > w.trackSecs {
			w.trackSecs = s
		}
	}
	switch w.handler {
	case "vide":
		w.d.Video = true
		if w.trackW > 0 && w.trackH > 0 {
			w.d.Width, w.d.Height = w.trackW, w.trackH
		}
	case "soun":
		w.d.Audio = true
	}
}

// stsd reads the first sample entry's type, and a visual entry's picture when the track
// header gave none.
func (w *describer) stsd(p []byte) {
	if len(p) < 16 {
		return
	}
	entry := p[8:]
	size := binary.BigEndian.Uint32(entry)
	typ := string(entry[4:8])
	if protectionBox(typ) {
		w.d.Encrypted = true
	}
	switch w.handler {
	case "vide":
		if w.d.VideoCodec == "" {
			w.d.VideoCodec = typ
		}
		if (w.trackW == 0 || w.trackH == 0) && size >= 36 && len(entry) >= 36 {
			w.trackW = int(binary.BigEndian.Uint16(entry[32:34]))
			w.trackH = int(binary.BigEndian.Uint16(entry[34:36]))
		}
		w.entryChildren(entry, size, 8+78)
	case "soun":
		if w.d.AudioCodec == "" {
			w.d.AudioCodec = typ
		}
		w.entryChildren(entry, size, 8+28)
	}
}

// entryChildren looks for protection boxes among a sample entry's child boxes.
func (w *describer) entryChildren(entry []byte, size uint32, skip int) {
	end := min(int(size), len(entry))
	if end <= skip {
		return
	}
	w.boxes(entry[skip:end], func(b box) bool {
		if protectionBox(b.typ) {
			w.d.Encrypted = true
		}
		return !b.cut
	})
}
