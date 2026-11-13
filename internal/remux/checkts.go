package remux

import (
	"context"
	"errors"
	"io"

	"downex/internal/media"
	"downex/internal/remux/mpegts"
)

// MaxCheckTSBytes bounds the prefix CheckTS reads: the EXT-X-MAP section (if any)
// followed by the first media fragment of a track (docs/VIDEO-DETECT-SPEC.md §4.8).
const MaxCheckTSBytes = 1 << 20

// CheckTS inspects the start of a track's transport stream: the EXT-X-MAP section (if
// any) followed by the first media fragment, each from its first packet
// (mpegts.FindStart: the caller leaves out what precedes it, as Assemble does; data
// before the first packet of prefix is skipped too), at most MaxCheckTSBytes (bytes
// beyond that are not read). It returns nil when Assemble can convert a track of kind
// k, and otherwise:
//   - an error wrapping media.ErrEncrypted for a scrambled packet or PES of a stream it
//     would keep, or a SAMPLE-AES stream type (0xDB, 0xCF, 0xC1) for its role
//   - an error wrapping ErrUnsupported when the first program lacks what k needs (video
//     and muxed: H.264 or HEVC; audio: AAC ADTS; muxed audio present but not AAC ADTS)
//     or uses a limit of docs/VIDEO-DETECT-SPEC.md §4.7
//   - an error wrapping ErrMalformed when no PAT and PMT are found in the bytes, or when
//     sync is lost beyond the resync window
//
// The units of the kept streams go through the same conversion as in Assemble, so
// that unsupported content (several access units per PES, a sample rate above
// 65,535 Hz, a program config element, ...) is found here; what only a cut prefix
// would make look malformed (a truncated last unit, missing parameter sets) is not
// reported.
func CheckTS(prefix []byte, k Kind) error {
	switch k {
	case KindVideo, KindAudio, KindMuxed:
	default:
		return errf(ErrInvalid, "unknown kind %q", k)
	}
	if len(prefix) > MaxCheckTSBytes {
		prefix = prefix[:MaxCheckTSBytes]
	}
	ti := &tsInput{idx: 0, files: []string{""}, domains: []int{0}}
	r := newRoles(k)
	d := mpegts.NewBytesDemuxer([][]byte{prefix}, r.keep, mpegts.WithProgramCheck(r.check))
	defer d.Close()
	var convs [2]tsConverter
	var failed [2]bool
	var uw unwrapper
	discard := func(*tsSample) error { return nil }
	for {
		u, err := d.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			return checkError(ti.wrap(err))
		}
		ro := roleOfType(u.Type)
		if failed[ro] {
			continue
		}
		if convs[ro] == nil {
			convs[ro] = newConverter(ro, u.Type)
		}
		pts, dts, has := uw.unit(u.PTS, u.DTS)
		if err := convs[ro].push(&u, pts, dts, has, 0, discard); err != nil {
			if errors.Is(err, ErrUnsupported) || errors.Is(err, media.ErrEncrypted) {
				return checkError(ti.unitError(err, &u))
			}
			failed[ro] = true // malformed so far: perhaps only because the prefix is cut
		}
	}
	if _, ok := d.Program(); !ok {
		return errf(ErrMalformed, "no program association and program map table found")
	}
	// The unit a converter holds back (a following one could continue it).
	for ro, c := range convs {
		if c == nil || failed[ro] {
			continue
		}
		if err := c.flush(discard); errors.Is(err, ErrUnsupported) || errors.Is(err, media.ErrEncrypted) {
			return checkError(withContext(err, "PID %d", r.pid[ro]))
		}
	}
	return nil
}

// checkError drops the input and file prefix of a message: CheckTS sees one
// prefix of one track.
func checkError(err error) error {
	var re *remuxError
	if errors.As(err, &re) {
		msg := re.msg
		for _, p := range []string{"input 0: ", "fragment 0: "} {
			if len(msg) >= len(p) && msg[:len(p)] == p {
				msg = msg[len(p):]
			}
		}
		return &remuxError{kind: re.kind, msg: msg}
	}
	return err
}
