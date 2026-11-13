package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Message size limits (docs/BROWSER-SPEC.md §2). Browsers refuse host messages larger
// than 1 MiB; the host refuses browser messages larger than 2 MiB.
const (
	maxRequestBytes  = 2 << 20
	maxResponseBytes = 1 << 20
)

var (
	errEmptyFrame    = errors.New("zero-length message")
	errFrameTooLarge = errors.New("message is larger than 2 MiB")
	errReplyTooLarge = errors.New("response is larger than 1 MiB")
)

// readFrame reads one message: a 32-bit length in native byte order, then that many
// bytes. It returns io.EOF only when r ends at a message boundary, and an error
// wrapping io.ErrUnexpectedEOF when r ends inside a message.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("read a message length: %w", err)
		}
		return nil, err // io.EOF at a boundary, or a read error
	}
	n := binary.NativeEndian.Uint32(hdr[:])
	switch {
	case n == 0:
		return nil, errEmptyFrame
	case n > maxRequestBytes:
		return nil, fmt.Errorf("%w (%d bytes)", errFrameTooLarge, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("read a %d-byte message: %w", n, err)
	}
	return buf, nil
}

// frameWriter writes length-prefixed messages; concurrent writes never interleave.
// After the first failed write every later one fails with the same error.
type frameWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func (fw *frameWriter) write(msg []byte) error {
	if len(msg) > maxResponseBytes {
		return fmt.Errorf("%w (%d bytes)", errReplyTooLarge, len(msg))
	}
	buf := make([]byte, 4+len(msg))
	binary.NativeEndian.PutUint32(buf, uint32(len(msg)))
	copy(buf[4:], msg)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.err != nil {
		return fw.err
	}
	if _, err := fw.w.Write(buf); err != nil {
		fw.err = fmt.Errorf("write a response: %w", err)
		return fw.err
	}
	return nil
}
