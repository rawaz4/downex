package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

// maxSecretBytes bounds a password read from the terminal or standard input.
const maxSecretBytes = 4096

var (
	errNoSecret      = errors.New("no password given (standard input is empty)")
	errEmptySecret   = errors.New("the password is empty")
	errSecretTooLong = fmt.Errorf("the password is longer than %d bytes", maxSecretBytes)
	errSecretUTF8    = errors.New("the password is not valid UTF-8 text")
)

// readPassword reads a secret. When in is a terminal, prompt is written to w and the
// typed characters are not echoed (the terminal mode is restored afterwards, also when
// ctx is canceled); otherwise (a pipe or a file: scripts) the first line of in is the
// secret and nothing is printed. The secret is never echoed, logged or kept.
func readPassword(ctx context.Context, in *os.File, w io.Writer, prompt string) (string, error) {
	restore, term, err := hideInput(in)
	if err != nil {
		return "", fmt.Errorf("hide the password input: %w", err)
	}
	if term {
		fmt.Fprint(w, prompt)
		defer func() {
			restore()
			fmt.Fprintln(w) // the Enter that ended the input was not echoed either
		}()
	}
	return readSecretLine(ctx, in)
}

// readSecretLine returns the first line of r without its line ending ("\n" or "\r\n").
// It fails for an empty input or line, a line longer than maxSecretBytes and text that
// is not UTF-8. A canceled ctx returns at once (the read itself may stay blocked).
func readSecretLine(ctx context.Context, r io.Reader) (string, error) {
	type result struct {
		s   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		var line []byte
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			line = append(line, buf[:n]...)
			if i := bytes.IndexByte(line, '\n'); i >= 0 {
				line = line[:i]
				break
			}
			if len(line) > maxSecretBytes+1 {
				done <- result{err: errSecretTooLong}
				return
			}
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					done <- result{err: errNoSecret}
					return
				}
				break
			}
			if err != nil {
				done <- result{err: fmt.Errorf("read the password: %w", err)}
				return
			}
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case len(line) == 0:
			done <- result{err: errEmptySecret}
		case len(line) > maxSecretBytes:
			done <- result{err: errSecretTooLong}
		case !utf8.Valid(line):
			done <- result{err: errSecretUTF8}
		default:
			done <- result{s: string(line)}
		}
	}()
	select {
	case res := <-done:
		return res.s, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
