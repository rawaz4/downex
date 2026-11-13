package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

func TestReadFrameShortReads(t *testing.T) {
	var stream bytes.Buffer
	msgs := []string{`{"id":"1","type":"ping"}`, `{"id":"2"}`, strings.Repeat("x", 70000)}
	for _, m := range msgs {
		stream.Write(frame([]byte(m)))
	}
	for name, r := range map[string]io.Reader{
		"one byte": iotest.OneByteReader(bytes.NewReader(stream.Bytes())),
		"half":     iotest.HalfReader(bytes.NewReader(stream.Bytes())),
		"data+EOF": iotest.DataErrReader(bytes.NewReader(stream.Bytes())),
	} {
		t.Run(name, func(t *testing.T) {
			for i, want := range msgs {
				got, err := readFrame(r)
				if err != nil || string(got) != want {
					t.Fatalf("message %d: %d bytes, %v", i, len(got), err)
				}
			}
			if _, err := readFrame(r); err != io.EOF {
				t.Fatalf("after the last message: %v, want io.EOF", err)
			}
		})
	}
}

func TestReadFrameLimits(t *testing.T) {
	hdr := func(n uint32) []byte {
		b := make([]byte, 4)
		binary.NativeEndian.PutUint32(b, n)
		return b
	}
	// The largest accepted message.
	big := bytes.Repeat([]byte("a"), maxRequestBytes)
	if got, err := readFrame(bytes.NewReader(frame(big))); err != nil || len(got) != maxRequestBytes {
		t.Fatalf("2 MiB message: %d bytes, %v", len(got), err)
	}
	cases := map[string]struct {
		in   []byte
		want error
	}{
		"zero length":     {hdr(0), errEmptyFrame},
		"one over 2 MiB":  {hdr(maxRequestBytes + 1), errFrameTooLarge},
		"4 GiB":           {hdr(0xFFFFFFFF), errFrameTooLarge},
		"partial length":  {[]byte{5, 0}, io.ErrUnexpectedEOF},
		"partial message": {append(hdr(10), "abc"...), io.ErrUnexpectedEOF},
		"no message":      {hdr(10), io.ErrUnexpectedEOF},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readFrame(bytes.NewReader(c.in)); !errors.Is(err, c.want) {
				t.Fatalf("readFrame = %v, want %v", err, c.want)
			}
		})
	}
	// A read error is returned as it is.
	boom := errors.New("boom")
	if _, err := readFrame(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("read error: %v", err)
	}
}

func TestFrameWriterSerialisesAndLimits(t *testing.T) {
	var buf bytes.Buffer
	fw := &frameWriter{w: &buf}
	if err := fw.write(make([]byte, maxResponseBytes+1)); !errors.Is(err, errReplyTooLarge) {
		t.Fatalf("oversized response: %v", err)
	}
	if err := fw.write(bytes.Repeat([]byte("r"), maxResponseBytes)); err != nil {
		t.Fatalf("1 MiB response: %v", err)
	}
	// Concurrent writers never interleave their messages.
	const n = 64
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fw.write(bytes.Repeat([]byte{byte('a' + i%26)}, 1000+i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r := bytes.NewReader(buf.Bytes())
	if m, err := readFrame(r); err != nil || len(m) != maxResponseBytes {
		t.Fatalf("first message: %d %v", len(m), err)
	}
	seen := map[int]bool{}
	for range n {
		m, err := readFrame(r)
		if err != nil {
			t.Fatal(err)
		}
		i := len(m) - 1000
		if i < 0 || i >= n || seen[i] || bytes.Count(m, []byte{byte('a' + i%26)}) != len(m) {
			t.Fatalf("interleaved or unexpected message of %d bytes", len(m))
		}
		seen[i] = true
	}

	// After a failed write, every write fails.
	fail := &frameWriter{w: errWriter{}}
	if err := fail.write([]byte("x")); err == nil {
		t.Fatal("write to a failing writer succeeded")
	}
	if err := fail.write([]byte("y")); err == nil || !strings.Contains(err.Error(), "write a response") {
		t.Fatalf("second write: %v", err)
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("pipe closed") }
