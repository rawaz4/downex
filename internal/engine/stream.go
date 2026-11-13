package engine

import (
	"io"

	"downex/internal/transport"
)

// stream is an open response body positioned at an absolute file offset. Bytes read
// but not reserved (because the segment ended) are pushed back so the worker can
// continue into an adjacent segment it takes over (docs/ENGINE-SPEC.md §5).
type stream struct {
	body io.ReadCloser
	off  int64  // absolute offset of the next byte read returns
	end  int64  // inclusive last byte the response carries; -1 if unknown
	left []byte // pushed-back bytes starting at off
	eof  bool   // the body is exhausted
}

func newStream(body io.ReadCloser, off, end int64) *stream {
	return &stream{body: body, off: off, end: end}
}

// read returns bytes starting at s.off and advances s.off past them. Pushed-back
// bytes are returned on their own and reported with fromLeft, so they are not
// charged to a speed limiter twice. The body is never read past end.
func (s *stream) read(p []byte) (n int, fromLeft bool, err error) {
	if len(s.left) > 0 {
		n = copy(p, s.left)
		s.left = s.left[n:]
		s.off += int64(n)
		return n, true, nil
	}
	if s.eof {
		return 0, false, io.EOF
	}
	if s.end >= 0 {
		rem := s.end - s.off + 1
		if rem <= 0 {
			s.eof = true
			return 0, false, io.EOF
		}
		if int64(len(p)) > rem {
			p = p[:rem]
		}
	}
	n, err = s.body.Read(p)
	s.off += int64(n)
	if err == io.EOF {
		s.eof = true
	}
	return n, false, err
}

// unread pushes back the last len(b) bytes returned by read.
func (s *stream) unread(b []byte) {
	if len(b) == 0 {
		return
	}
	left := make([]byte, 0, len(b)+len(s.left))
	s.left = append(append(left, b...), s.left...)
	s.off -= int64(len(b))
}

// more reports whether the stream may still deliver bytes.
func (s *stream) more() bool {
	if len(s.left) > 0 {
		return true
	}
	return !s.eof && (s.end < 0 || s.off <= s.end)
}

// close releases the body without reading it. Reusing the connection is the
// transport's concern (§5): it drains a short HTTP/1.1 remainder within a time limit
// and resets HTTP/2 streams. Reading here would block the worker, while it already
// owns its next segment, on a server that stalls in the abandoned tail.
func (s *stream) close() {
	_ = s.body.Close()
}

func closeBody(resp *transport.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
