package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// capConfig describes the resource a capFactory serves.
type capConfig struct {
	size         int64
	limit        int64 // at most this many bytes per response
	unknownTotal bool  // Content-Range "bytes a-b/*"
	noRange      bool  // 206 without Content-Range
	acceptNone   bool  // "Accept-Ranges: none"
	ignoreLater  bool  // requests after the probe get 200 with the whole body
}

// capFactory serves size bytes of deterministic content, but at most limit bytes per
// response, with configurable deviations from RFC 9110 in its 206 answers.
type capFactory struct {
	capConfig
	fetches atomic.Int64
}

func (f *capFactory) NewSlot() transport.Fetcher { return capFetcher{f} }

type capFetcher struct{ f *capFactory }

func (capFetcher) Close() {}

func (c capFetcher) Fetch(_ context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	f := c.f
	n := f.fetches.Add(1)
	total := f.size
	if f.unknownTotal {
		total = -1
	}
	if n > 1 && f.ignoreLater {
		return &transport.Response{StatusCode: 200, ContentLength: f.size, FinalURL: rr.URL,
			Body: io.NopCloser(bytes.NewReader(testserver.Bytes(fakeSeed, 0, f.size)))}, nil
	}
	if rr.Start >= f.size {
		return &transport.Response{StatusCode: 416, Body: emptyBody(), FinalURL: rr.URL,
			ContentRange: &transport.ContentRange{Start: -1, End: -1, Total: total}}, nil
	}
	end := rr.End
	if end < 0 || end >= f.size {
		end = f.size - 1
	}
	end = min(end, rr.Start+f.limit-1)
	resp := &transport.Response{
		StatusCode: 206, ContentLength: end - rr.Start + 1, AcceptRangesNone: f.acceptNone, FinalURL: rr.URL,
		Body: io.NopCloser(bytes.NewReader(testserver.Bytes(fakeSeed, rr.Start, end-rr.Start+1))),
	}
	if !f.noRange {
		resp.ContentRange = &transport.ContentRange{Start: rr.Start, End: end, Total: total}
	}
	return resp, nil
}

func emptyBody() io.ReadCloser { return io.NopCloser(bytes.NewReader(nil)) }

// TestProbeNeverTruncates: a 206 probe that does not prove it carries the whole file
// must never complete a shorter file (§2.2–2.3).
func TestProbeNeverTruncates(t *testing.T) {
	t.Parallel()
	const size = 5000
	for _, tc := range []struct {
		name     string
		cfg      capConfig
		wantErr  error // nil: the download completes with all size bytes
		wantMode model.Mode
	}{
		{"partial with total and accept-ranges none", capConfig{limit: 1000, acceptNone: true}, nil, model.ModeRanged},
		{"partial with total, ranges really ignored", capConfig{limit: 1000, acceptNone: true, ignoreLater: true}, ErrRangeIgnored, 0},
		{"unknown total", capConfig{limit: 1000, unknownTotal: true}, nil, model.ModeSingle},
		{"unknown total, ranges really ignored", capConfig{limit: 1000, unknownTotal: true, ignoreLater: true}, ErrRangeIgnored, 0},
		{"no content-range", capConfig{limit: 1000, noRange: true}, ErrRangeIgnored, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fac := &capFactory{capConfig: tc.cfg}
			fac.size = size
			st := newMemStore()
			eng := New(st, fac, fastOpts())
			d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/p.bin"}, t.TempDir(), "", 4, 0)
			if err != nil {
				t.Fatal(err)
			}
			err = eng.Run(context.Background(), d.ID, nil, nil)
			r := st.get(t, d.ID)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || r.State != model.StateFailed {
					t.Fatalf("Run = %v, state %v (size %d); want %v and failed", err, r.State, r.Size, tc.wantErr)
				}
				if r.Filename != "" {
					if _, err := os.Stat(filepath.Join(r.Dir, r.Filename)); !os.IsNotExist(err) {
						t.Fatalf("a final file was created: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(r.Dir, r.Filename))
			if err != nil {
				t.Fatal(err)
			}
			if r.State != model.StateCompleted || r.Size != size || r.Mode != tc.wantMode ||
				!bytes.Equal(data, testserver.Bytes(fakeSeed, 0, size)) {
				t.Fatalf("state %v mode %v size %d, %d bytes on disk", r.State, r.Mode, r.Size, len(data))
			}
		})
	}
}
