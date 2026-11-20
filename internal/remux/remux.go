// Package remux assembles downloaded media fragments into one playable file
// (docs/MEDIA-SPEC.md §5, §6 step 5; docs/VIDEO-DETECT-SPEC.md §4).
//
//   - MPEG-2 transport stream (ISO/IEC 13818-1) tracks — one track of any kind, or
//     a video track and a separate audio track — are converted to one MP4 without
//     re-encoding: H.264 and HEVC access units (Annex B) become length-prefixed
//     samples of avc1/hvc1 sample entries, AAC ADTS frames raw samples of an mp4a
//     sample entry, and PTS/DTS the MP4 timing, with discontinuity domains placed
//     on one timeline (internal/remux/mpegts demultiplexes). ConcatTS keeps the
//     byte-for-byte join of one transport stream track for downloads stored as .ts.
//   - Fragmented ISO-BMFF (ISO/IEC 14496-12, CMAF) tracks — one initialization
//     segment plus ordered media fragments each, a single track or a separate
//     video and audio track — are joined into one progressive MP4
//     (ISO/IEC 14496-14) with the 'moov' box before the 'mdat' box. The sample
//     tables (stts, ctts, stsc, stsz, stco/co64, stss) are built from the track
//     fragment headers (tfhd, tfdt, trun); sample data is copied byte for byte
//     and interleaved by decode time. Inputs too large for in-memory sample tables
//     are written as a multi-track fragmented MP4 instead.
//
// Everything is streamed: sample data is never held in memory, only per-sample
// table entries (about 16 bytes per sample, bounded by maxProgressiveSamples) and the
// samples of one movie fragment per input at a time (bounded by maxMoofSamples). The
// movie fragments of a file may describe at most the sample data its 'mdat' boxes hold,
// and at most maxFileEmptySamples samples without data, so the work per file follows
// its size. Transport stream conversion reads its fragments twice (tables, then data)
// and holds one PES unit (at most 32 MiB) per stream it reads, and one run (at most 16
// MiB) per track of fragmented output. All input is untrusted; malformed input yields
// an error, never a panic.
package remux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"downex/internal/remux/mpegts"
)

// Kind is the kind of a track input.
type Kind string

// Kinds.
const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindMuxed Kind = "muxed"
)

// Containers of inputs and outputs.
const (
	ContainerTS   = "ts"   // input and output: MPEG-2 transport stream
	ContainerFMP4 = "fmp4" // input: fragmented ISO-BMFF
	ContainerMP4  = "mp4"  // output: ISO-BMFF / MP4
)

// Input is one downloaded track.
type Input struct {
	Kind Kind
	// Container is "ts" or "fmp4" (the media.Container of the track's plan).
	Container string
	// Init is the path of the initialization segment file; "" when there is none
	// (TS without EXT-X-MAP, or fMP4 whose first fragment file carries the 'moov').
	// For TS, internal/mediajob never sets it: an EXT-X-MAP section is the first
	// entry of Fragments. When it is set it is read as a leading file, as ConcatTS
	// does. Breaks never count it.
	Init string
	// Fragments are the media fragment file paths in playback order. An fMP4
	// fragment file may repeat the initialization segment ('moov') as long as it
	// does not change. A TS file is read from its first packet (mpegts.FindStart):
	// what precedes it, such as an image a CDN put before every segment, is not media.
	Fragments []string
	// Breaks are the indexes into Fragments of the fragments preceded by a
	// discontinuity (media.Fragment.Discontinuity: EXT-X-DISCONTINUITY or a skipped
	// EXT-X-GAP, and every TS EXT-X-MAP entry after the first): sorted, unique, each
	// in 1..len(Fragments)-1. TS only (docs/VIDEO-DETECT-SPEC.md §4.5).
	Breaks []int
	// Language is the track's language (BCP 47 or ISO 639-2/T) for the 'mdhd' box;
	// "" = und. It is used only when the stream itself names none.
	Language string
}

// Errors. Every error of this package that is not a context error, an I/O error
// or media.ErrEncrypted wraps one of these; test with errors.Is.
var (
	// ErrUnsupported reports a valid input combination or content this package
	// does not handle (for example WebM, transport streams mixed with fragmented
	// MP4, AC-3 in a transport stream, or an initialization segment that changes
	// mid-stream). It wraps errors.ErrUnsupported.
	ErrUnsupported = fmt.Errorf("remux: %w", errors.ErrUnsupported)
	// ErrMalformed reports input that violates its container format.
	ErrMalformed = errors.New("remux: malformed input")
	// ErrInvalid reports invalid arguments.
	ErrInvalid = errors.New("remux: invalid argument")
)

// remuxError is a sentinel kind plus a message.
type remuxError struct {
	kind error
	msg  string
}

func (e *remuxError) Error() string { return e.kind.Error() + ": " + e.msg }
func (e *remuxError) Unwrap() error { return e.kind }

func errf(kind error, format string, args ...any) error {
	return &remuxError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// withContext prefixes the message of err with where it happened, keeping its
// kind.
func withContext(err error, format string, args ...any) error {
	prefix := fmt.Sprintf(format, args...)
	if re, ok := err.(*remuxError); ok {
		return &remuxError{kind: re.kind, msg: prefix + ": " + re.msg}
	}
	return fmt.Errorf("remux: %s: %w", prefix, err)
}

// Assemble writes the tracks of inputs into outPath (the caller passes
// "<name>.dxpart" and renames it afterwards) and returns the output container,
// "mp4": transport stream inputs (one of any kind, or a video and an audio one)
// are converted, fragmented MP4 inputs joined. outPath is created or truncated; it
// is removed again when Assemble fails or ctx is cancelled. The written file is
// synced to stable storage before Assemble returns.
func Assemble(ctx context.Context, inputs []Input, outPath string) (container string, err error) {
	res, err := AssembleWindow(ctx, inputs, outPath, Window{})
	return res.Container, err
}

// AssembleWindow is Assemble cut to the part win of the presentation
// (docs/MEDIA-SPEC.md §8 C6–C8); the zero Window writes what Assemble writes. A video
// input without a sync sample at or before win.Start is ErrNoKeyframe unless win.Late;
// a part whose samples exceed the in-memory tables is ErrUnsupported.
func AssembleWindow(ctx context.Context, inputs []Input, outPath string, win Window) (res Result, err error) {
	if err := validate(inputs, outPath); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var nTS, nFMP4 int
	for _, in := range inputs {
		switch in.Container {
		case ContainerTS:
			nTS++
		case ContainerFMP4:
			nFMP4++
		}
	}
	ts := false
	switch {
	case nTS > 0 && nFMP4 > 0:
		return Result{}, errf(ErrUnsupported, "cannot join transport stream and fragmented MP4 tracks")
	case nTS == 1 && len(inputs) == 1:
		ts = true
	case nTS == 2 && len(inputs) == 2:
		if k0, k1 := inputs[0].Kind, inputs[1].Kind; !(k0 == KindVideo && k1 == KindAudio || k0 == KindAudio && k1 == KindVideo) {
			return Result{}, errf(ErrUnsupported, "cannot multiplex transport stream tracks of kinds %s and %s", k0, k1)
		}
		ts = true
	case nTS > 1:
		return Result{}, errf(ErrUnsupported, "cannot multiplex %d separate transport stream tracks", nTS)
	case nFMP4 == len(inputs):
	default:
		for _, in := range inputs {
			if in.Container != ContainerTS && in.Container != ContainerFMP4 {
				return Result{}, errf(ErrUnsupported, "container %q", in.Container)
			}
		}
		return Result{}, errf(ErrUnsupported, "input combination")
	}

	err = writeOutput(outPath, func(w io.Writer) error {
		if ts {
			return assembleTS(ctx, inputs, w, win, &res)
		}
		return assembleMP4(ctx, inputs, w, win, &res)
	})
	if err != nil {
		return Result{}, err
	}
	res.Container = ContainerMP4
	if win.IsZero() {
		res.Exact = true
	}
	return res, nil
}

// ConcatTS joins one TS input byte for byte into outPath: the initialization section
// (if any) and the fragments in order, each from its first packet, which must start
// within its first mpegts.MaxPrefix bytes (mpegts.FindStart; data before it, such as
// an image a CDN put there, is left out). outPath is created or truncated, synced to
// stable storage before ConcatTS returns, and removed again when ConcatTS fails or ctx
// is cancelled. It is what Assemble did with one TS input before 2026-10-23, kept for
// downloads stored as ".ts" then (docs/VIDEO-DETECT-SPEC.md §3.3).
func ConcatTS(ctx context.Context, in Input, outPath string) error {
	if err := validate([]Input{in}, outPath); err != nil {
		return err
	}
	if in.Container != ContainerTS {
		return errf(ErrInvalid, "container %q is not a transport stream", in.Container)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeOutput(outPath, func(w io.Writer) error { return concatTS(ctx, in, w) })
}

// writeOutput creates or truncates outPath, lets write fill it through a buffer,
// then flushes, syncs and closes it. The file is removed again when anything fails.
func writeOutput(outPath string, write func(w io.Writer) error) error {
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("remux: create output: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(outPath)
		}
	}()
	bw := bufio.NewWriterSize(f, 1<<20)
	if err := write(bw); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("remux: sync output: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("remux: close output: %w", err)
	}
	ok = true
	return nil
}

func validate(inputs []Input, outPath string) error {
	if len(inputs) == 0 {
		return errf(ErrInvalid, "no inputs")
	}
	if outPath == "" {
		return errf(ErrInvalid, "no output path")
	}
	out := cleanPath(outPath)
	for i, in := range inputs {
		switch in.Kind {
		case KindVideo, KindAudio, KindMuxed:
		default:
			return errf(ErrInvalid, "input %d: unknown kind %q", i, in.Kind)
		}
		if len(in.Fragments) == 0 {
			return errf(ErrInvalid, "input %d: no fragments", i)
		}
		if in.Init != "" && cleanPath(in.Init) == out {
			return errf(ErrInvalid, "input %d: initialization segment is the output file", i)
		}
		for j, p := range in.Fragments {
			if p == "" {
				return errf(ErrInvalid, "input %d: fragment %d has no path", i, j)
			}
			if cleanPath(p) == out {
				return errf(ErrInvalid, "input %d: fragment %d is the output file", i, j)
			}
		}
		if in.Container == ContainerTS {
			prev := 0
			for _, b := range in.Breaks {
				if b <= prev || b >= len(in.Fragments) {
					return errf(ErrInvalid, "input %d: discontinuity before fragment %d is not sorted, unique and in 1..%d", i, b, len(in.Fragments)-1)
				}
				prev = b
			}
		}
	}
	return nil
}

func cleanPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// concatTS copies the initialization section (if any) and the fragments of in to
// w in order, each from its first packet. A file without one, an error page saved in
// place of media for example, fails.
func concatTS(ctx context.Context, in Input, w io.Writer) error {
	files := in.Fragments
	if in.Init != "" {
		files = append([]string{in.Init}, in.Fragments...)
	}
	buf := make([]byte, 1<<20)
	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copyTSFile(ctx, p, w, buf); err != nil {
			if in.Init != "" {
				if i == 0 {
					return withContext(err, "initialization section")
				}
				i--
			}
			return withContext(err, "fragment %d", i)
		}
	}
	return nil
}

// copyTSFile copies the file at path to w from its first packet. buf holds at least
// mpegts.StartBytes.
func copyTSFile(ctx context.Context, path string, w io.Writer, buf []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.ReadFull(f, buf)
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		err = io.EOF
	case err != nil:
		return err
	}
	if n == 0 {
		return errf(ErrMalformed, "empty file")
	}
	k, ok := mpegts.FindStart(buf[:n])
	if !ok {
		return errf(ErrMalformed, "not an MPEG transport stream (first byte 0x%02x)", buf[0])
	}
	for chunk := buf[k:n]; ; {
		if _, werr := w.Write(chunk); werr != nil {
			return fmt.Errorf("remux: write output: %w", werr)
		}
		if err == io.EOF {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var m int
		m, err = f.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		chunk = buf[:m]
	}
}
