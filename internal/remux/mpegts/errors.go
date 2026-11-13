package mpegts

import (
	"errors"
	"fmt"
	"strings"
)

// Errors. Every error of this package that is not a context error or an I/O error
// is an *Error whose Kind is one of these or media.ErrEncrypted; test with
// errors.Is.
var (
	// ErrMalformed reports a transport stream that violates ISO/IEC 13818-1 beyond
	// what is skipped: sync lost beyond the resync window, or a PES unit larger
	// than MaxPESBytes.
	ErrMalformed = errors.New("mpegts: malformed transport stream")
	// ErrUnsupported reports a valid stream this package's caller refused (see
	// WithProgramCheck). It wraps errors.ErrUnsupported.
	ErrUnsupported = fmt.Errorf("mpegts: %w", errors.ErrUnsupported)
)

// Error is an error found in the stream: its kind, where it was found and what.
// Messages never contain file paths or URLs.
type Error struct {
	Kind error // ErrMalformed, ErrUnsupported or media.ErrEncrypted
	File int   // index of the file in the demuxer's list; -1 = none
	PID  int   // the packet identifier; -1 = none
	Msg  string
}

func (e *Error) Error() string { return e.Kind.Error() + ": " + e.Detail() }

// Detail returns the message with its location, without the kind.
func (e *Error) Detail() string {
	var b strings.Builder
	if e.File >= 0 {
		fmt.Fprintf(&b, "file %d: ", e.File)
	}
	if e.PID >= 0 {
		fmt.Fprintf(&b, "PID %d: ", e.PID)
	}
	b.WriteString(e.Msg)
	return b.String()
}

func (e *Error) Unwrap() error { return e.Kind }

func newError(kind error, file, pid int, format string, args ...any) *Error {
	return &Error{Kind: kind, File: file, PID: pid, Msg: fmt.Sprintf(format, args...)}
}
