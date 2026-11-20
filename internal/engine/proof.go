package engine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

// Proof constants (docs/ENGINE-SPEC.md §7.3, P2-P3).
const (
	// ProofWindowBytes is the largest window of a sampled proof.
	ProofWindowBytes = 65_536
	// FullProofMaxBytes and FullProofMaxRuns bound a full proof, which compares every
	// durable byte: at most this many bytes in at most this many runs.
	FullProofMaxBytes = 1 << 20
	FullProofMaxRuns  = 8
	// ProofWindowsVouched windows are compared when the new server's first answer carries
	// the stored strong entity tag; ProofWindowsUnvouched otherwise.
	ProofWindowsVouched   = 4
	ProofWindowsUnvouched = 8
	// ProofMinInformative is how many compared bytes of a sampled proof must lie in
	// windows that are not one repeated byte value (a zero-filled hole proves nothing).
	ProofMinInformative = 4_096
	// ProofRequestTimeout bounds one request of a proof.
	ProofRequestTimeout = 10 * time.Second
	// proofAlign aligns random windows.
	proofAlign = 4_096
	// proofRedraws is how often a random window that an earlier window contains is drawn
	// again.
	proofRedraws = 2
)

// byteRun is an inclusive byte range [start, end] of a download's durable bytes, or of a
// window compared by a proof.
type byteRun struct{ start, end int64 }

func (r byteRun) len() int64              { return r.end - r.start + 1 }
func (r byteRun) contains(o byteRun) bool { return r.start <= o.start && o.end <= r.end }
func (r byteRun) String() string          { return fmt.Sprintf("%d-%d", r.start, r.end) }
func runOf(start, n int64) byteRun        { return byteRun{start, start + n - 1} }
func (r byteRun) head(n int64) byteRun    { return byteRun{r.start, min(r.end, r.start+n-1)} }
func (r byteRun) tail(n int64) byteRun    { return byteRun{max(r.start, r.end-n+1), r.end} }
func (r byteRun) clip(start, n int64) byteRun {
	return byteRun{max(start, r.start), min(r.end, max(start, r.start)+n-1)}
}

// durableRuns returns the durable bytes of a stored layout as runs (P2): [Start,
// Start+Durable-1] of every segment with durable bytes, adjacent runs merged, ordered.
func durableRuns(segs []model.Segment) []byteRun {
	sorted := make([]model.Segment, 0, len(segs))
	for _, s := range segs {
		if s.Durable > 0 {
			sorted = append(sorted, s)
		}
	}
	slices.SortFunc(sorted, func(a, b model.Segment) int { return cmp.Compare(a.Start, b.Start) })
	var out []byteRun
	for _, s := range sorted {
		r := runOf(s.Start, s.Durable)
		if k := len(out); k > 0 && out[k-1].end+1 >= r.start {
			out[k-1].end = max(out[k-1].end, r.end)
			continue
		}
		out = append(out, r)
	}
	return out
}

// runsTotal is the number of bytes in runs.
func runsTotal(runs []byteRun) int64 {
	var t int64
	for _, r := range runs {
		t += r.len()
	}
	return t
}

// proofWindows returns the windows a sampled proof compares (P2), in request order: (1)
// the last ProofWindowBytes of the run that starts at 0, (2) the last ProofWindowBytes
// of the run with the largest end, (3) the first ProofWindowBytes of the byteRun that
// starts at 0, then random windows up to n in all: offsets drawn from rnd uniformly
// over the durable bytes, aligned down to 4,096 and clipped to their byteRun. A window that
// an earlier one contains is dropped; a random one is drawn again at most twice. runs
// must be ordered and disjoint.
func proofWindows(runs []byteRun, n int, rnd io.Reader) []byteRun {
	if len(runs) == 0 || n <= 0 {
		return nil
	}
	var out []byteRun
	add := func(w byteRun) bool {
		if w.end < w.start {
			return false
		}
		for _, o := range out {
			if o.contains(w) {
				return false
			}
		}
		out = append(out, w)
		return true
	}
	first, last := runs[0], runs[len(runs)-1]
	if first.start == 0 {
		add(first.tail(ProofWindowBytes))
	}
	add(last.tail(ProofWindowBytes))
	if first.start == 0 {
		add(first.head(ProofWindowBytes))
	}
	total := runsTotal(runs)
	for slot := len(out); slot < n; slot++ {
		for range 1 + proofRedraws {
			u, ok := uniform(rnd, total)
			if !ok {
				return out
			}
			if add(windowAt(runs, u)) {
				break
			}
		}
	}
	return out
}

// windowAt is the random window of the u-th durable byte: its offset aligned down to
// proofAlign (not below its byteRun's start), ProofWindowBytes long, clipped to its byteRun.
func windowAt(runs []byteRun, u int64) byteRun {
	for _, r := range runs {
		if u < r.len() {
			off := r.start + u
			off -= off % proofAlign
			return r.clip(off, ProofWindowBytes)
		}
		u -= r.len()
	}
	return byteRun{0, -1}
}

// uniform draws an integer in [0, n) from rnd (a negligible modulo bias for n ≪ 2⁶⁴).
func uniform(rnd io.Reader, n int64) (int64, bool) {
	if n <= 0 {
		return 0, false
	}
	var b [8]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(b[:]) % uint64(n)), true
}

// proofAnswer is one window a new link served.
type proofAnswer struct {
	resp *transport.Response // headers only: its body was read into data
	data []byte
}

// fetchWindow asks req for bytes w (no If-Range) over f and returns them (P3). The
// answer must be 206 with Content-Range exactly w/size and exactly w.len() body bytes:
// an expiry (§7.2 E1) is ErrLinkExpired, a 401 with a challenge or a 407
// ErrAuthRequired, a 200 or another range ErrLinkNoRanges (an unknown total "*" also
// ErrSizeUnknown), another total or one too short to hold w ErrLinkSize, any other
// status a *StatusError. size < 0 accepts any total that holds w.
func fetchWindow(ctx context.Context, f transport.Fetcher, req model.Request, w byteRun, size int64) (proofAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, ProofRequestTimeout)
	defer cancel()
	resp, err := f.Fetch(ctx, transport.RangeRequest{Request: req, Start: w.start, End: w.end})
	if err != nil {
		return proofAnswer{}, fmt.Errorf("bytes %s: %w", w, err)
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	defer closeBody(resp)
	if expired, why := isExpiry(resp, expiryCheck{sentURL: req.URL, rangedRequest: true}); expired {
		return proofAnswer{}, expiryError(w.start, w.end, why)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		return proofAnswer{}, fmt.Errorf("bytes %s: the whole file was sent: %w", w, ErrLinkNoRanges)
	case http.StatusUnauthorized, http.StatusProxyAuthRequired:
		return proofAnswer{}, fmt.Errorf("bytes %s: HTTP %d: %w", w, resp.StatusCode, ErrAuthRequired)
	default:
		return proofAnswer{}, fmt.Errorf("bytes %s: %w", w, &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter})
	}
	cr := resp.ContentRange
	switch {
	case cr == nil:
		return proofAnswer{}, fmt.Errorf("bytes %s: no usable Content-Range: %w", w, ErrLinkNoRanges)
	case cr.Total < 0:
		return proofAnswer{}, fmt.Errorf("bytes %s: answered without the file's size: %w: %w", w, ErrSizeUnknown, ErrLinkNoRanges)
	case size >= 0 && cr.Total != size:
		return proofAnswer{}, fmt.Errorf("%d bytes, expected %d: %w", cr.Total, size, ErrLinkSize)
	case cr.Total <= w.end:
		return proofAnswer{}, fmt.Errorf("%d bytes, fewer than bytes %s need: %w", cr.Total, w, ErrLinkSize)
	case cr.Start != w.start || cr.End != w.end:
		return proofAnswer{}, fmt.Errorf("bytes %s answered with %d-%d: %w", w, cr.Start, cr.End, ErrLinkNoRanges)
	}
	data := make([]byte, w.len())
	if _, err := io.ReadFull(io.LimitReader(resp.Body, w.len()), data); err != nil {
		return proofAnswer{}, fmt.Errorf("bytes %s: read body: %w", w, err)
	}
	return proofAnswer{resp: resp, data: data}, nil
}

// compareWindow compares the bytes a new link served for window w with the part file
// (P6): any difference is ErrLinkMismatch.
func compareWindow(part io.ReaderAt, w byteRun, got []byte) error {
	want := make([]byte, len(got))
	if _, err := part.ReadAt(want, w.start); err != nil {
		return fmt.Errorf("read the part file at %d: %w", w.start, err)
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("bytes %s differ: %w", w, ErrLinkMismatch)
	}
	return nil
}

// flat reports whether b is one repeated byte value (it proves nothing about a file).
func flat(b []byte) bool {
	return len(b) == 0 || bytes.Count(b, b[:1]) == len(b)
}
