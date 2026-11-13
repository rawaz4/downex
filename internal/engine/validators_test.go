package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// TestResumeDetectsChangeWithoutStrongValidator: without a strong ETag no If-Range
// (or only a date) is sent, so the validators of every 206 must be compared with the
// recorded ones (RFC 9110 §8.8.3.2 weak comparison) before its bytes are used.
func TestResumeDetectsChangeWithoutStrongValidator(t *testing.T) {
	t.Parallel()
	lm := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for name, tc := range map[string]struct {
		so     testserver.Options
		change func(*testserver.Options)
	}{
		"weak etag": {testserver.Options{ETag: `W/"v1"`},
			func(o *testserver.Options) { o.ETag = `W/"v2"`; o.Seed = 99 }},
		"weak etag with unchanged last-modified": {testserver.Options{ETag: `W/"v1"`, LastModified: lm},
			func(o *testserver.Options) { o.ETag = `W/"v2"`; o.Seed = 99 }},
		"last-modified only": {testserver.Options{LastModified: lm},
			func(o *testserver.Options) { o.LastModified = lm.Add(time.Hour); o.Seed = 99 }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			so := tc.so
			so.Size, so.Seed, so.Throttle = 4<<20, 42, 1<<20
			h := newHarness(t, so, resumeOpts())
			d := h.add("/w.bin", "", 4)
			if err := pauseMidway(t, h, d.ID, 512<<10); !errors.Is(err, context.Canceled) {
				t.Fatalf("pause: %v", err)
			}
			h.srv.Update(tc.change)
			if err := h.run(d.ID, nil, nil); !errors.Is(err, ErrValidatorChanged) {
				t.Fatalf("resume = %v, want ErrValidatorChanged", err)
			}
			h.srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
			if err := h.eng.Restart(context.Background(), d.ID, RestartOptions{}); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			if err := h.run(d.ID, nil, nil); err != nil {
				t.Fatalf("Run after restart: %v", err)
			}
			h.verify(d.ID)
		})
	}
}

// TestNoIfRangeWithWeakETag: RFC 9110 §13.1.5 forbids a weak entity tag in If-Range
// and a date when an entity tag exists, so a weak ETag means no If-Range at all.
func TestNoIfRangeWithWeakETag(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 43, ETag: `W/"x"`, Throttle: 1 << 20,
		LastModified: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}, fastOpts())
	d := h.add("/weak.bin", "", 4)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	reqs := h.srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("only %d requests", len(reqs))
	}
	for _, r := range reqs[1:] {
		if r.IfRange != "" {
			t.Fatalf("request %+v carries If-Range although the only entity tag is weak", r)
		}
	}
}

func TestIfRangeValidator(t *testing.T) {
	const date = "Wed, 04 Mar 2026 05:06:07 GMT"
	for _, tc := range []struct {
		v    model.Validators
		want string
	}{
		{model.Validators{ETag: `"a"`, StrongETag: true, LastModified: date}, `"a"`},
		{model.Validators{ETag: `W/"a"`, LastModified: date}, ""},
		{model.Validators{ETag: `W/"a"`}, ""},
		{model.Validators{LastModified: date}, date},
		{model.Validators{}, ""},
	} {
		if got := ifRangeValidator(tc.v); got != tc.want {
			t.Errorf("ifRangeValidator(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// TestResumeAfterServerDropsRanges: a 200 on a resumed range means the remote file
// changed (§7), even when no If-Range could be sent; the caller can then restart,
// which falls back to one connection (F3).
func TestResumeAfterServerDropsRanges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 44, Throttle: 1 << 20}, resumeOpts())
	d := h.add("/nv.bin", "", 4)
	if err := pauseMidway(t, h, d.ID, 512<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	h.srv.Update(func(o *testserver.Options) { o.NoRanges = true; o.Seed = 45; o.Throttle = 0 })
	if err := h.run(d.ID, nil, nil); !errors.Is(err, ErrValidatorChanged) {
		t.Fatalf("resume = %v, want ErrValidatorChanged", err)
	}
	if err := h.eng.Restart(context.Background(), d.ID, RestartOptions{}); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run after restart: %v", err)
	}
	if got := h.verify(d.ID); got.Mode != model.ModeSingle {
		t.Fatalf("mode after restart %v", got.Mode)
	}
}

// TestSegmentsStayOnRedirectTarget: a redirector alternates between two mirrors with
// identical content but different strong ETags. Segment requests must go to the
// origin the probe's validators came from instead of re-following the redirect.
func TestSegmentsStayOnRedirectTarget(t *testing.T) {
	t.Parallel()
	const size = 4 << 20
	a := testserver.New(testserver.Options{Size: size, Seed: 46, ETag: `"mirror-a"`, Throttle: 4 << 20})
	t.Cleanup(a.Close)
	b := testserver.New(testserver.Options{Size: size, Seed: 46, ETag: `"mirror-b"`, Throttle: 4 << 20})
	t.Cleanup(b.Close)
	var hits atomic.Int64
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := a.URL
		if hits.Add(1)%2 == 0 {
			target = b.URL
		}
		http.Redirect(w, r, target+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	fac := &httpFactory{newTransport: func() *http.Transport { return &http.Transport{} }}
	st := newMemStore()
	eng := New(st, fac, fastOpts())
	d, err := eng.Add(context.Background(), model.Request{URL: redir.URL + "/m.bin"}, t.TempDir(), "", 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Mirror a, which the probe reaches, holds after half the file until a second request
	// came: at 4 MiB/s a loaded machine could otherwise let the probe finish before a
	// ramp-up step.
	a.Hold(size / 2)
	defer a.Release()
	ctx, cancel := runContext(t)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	waitFor(t, 30*time.Second, "a second request", func() bool { return len(a.Requests())+len(b.Requests()) >= 2 })
	a.Release()
	if err := awaitRun(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := st.get(t, d.ID)
	data, err := os.ReadFile(filepath.Join(r.Dir, r.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != model.StateCompleted || !bytes.Equal(data, testserver.Bytes(46, 0, size)) {
		t.Fatalf("state %v, %d bytes", r.State, len(data))
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("redirector saw %d requests; segments re-followed the redirect", n)
	}
	if n := len(a.Requests()); n < 2 {
		t.Fatalf("mirror a saw %d requests; expected a segmented download", n)
	}
}

// pinFactory serves a resource whose original URL redirects (FinalURL) to a signed
// CDN URL, at most 50,000 bytes per response. With expire set, the CDN refuses every
// request after the probe with 403, as an expired signature would; the original URL
// keeps working.
type pinFactory struct {
	size   int64
	expire bool
	mu     sync.Mutex
	urls   []string
	header []http.Header
}

const (
	pinOrigin = "http://origin.test/f.bin"
	pinCDN    = "http://cdn.test/f.bin?sig=abc"
)

func (f *pinFactory) NewSlot() transport.Fetcher { return pinFetcher{f} }

type pinFetcher struct{ f *pinFactory }

func (pinFetcher) Close() {}

func (p pinFetcher) Fetch(_ context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	f := p.f
	f.mu.Lock()
	first := len(f.urls) == 0
	f.urls = append(f.urls, rr.URL)
	f.header = append(f.header, rr.Headers.Clone())
	f.mu.Unlock()
	if rr.URL == pinCDN && f.expire && !first {
		return &transport.Response{StatusCode: 403, Body: emptyBody(), FinalURL: rr.URL}, nil
	}
	end := rr.End
	if end < 0 {
		end = f.size - 1
	}
	end = min(end, rr.Start+50_000-1) // short responses: every worker requests again
	return &transport.Response{
		StatusCode: 206, ContentLength: end - rr.Start + 1, FinalURL: pinCDN,
		ContentRange: &transport.ContentRange{Start: rr.Start, End: end, Total: f.size},
		Validators:   model.Validators{ETag: `"p"`, StrongETag: true},
		Body:         io.NopCloser(bytes.NewReader(testserver.Bytes(fakeSeed, rr.Start, end-rr.Start+1))),
	}, nil
}

func (f *pinFactory) requests() ([]string, []http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.urls...), append([]http.Header(nil), f.header...)
}

// TestPinnedTargetFallsBackToOriginalURL: segment requests go to the redirect target
// without the caller's credentials (it is another origin); when the target refuses
// them (an expired signature), workers use the original URL again.
func TestPinnedTargetFallsBackToOriginalURL(t *testing.T) {
	t.Parallel()
	for _, expire := range []bool{false, true} {
		fac := &pinFactory{size: 300_000, expire: expire}
		st := newMemStore()
		eng := New(st, fac, fastOpts())
		hdr := http.Header{"Cookie": {"sid=secret"}, "Authorization": {"Bearer t"}, "Referer": {"https://pages.test/"}}
		d, err := eng.Add(context.Background(), model.Request{URL: pinOrigin, Headers: hdr}, t.TempDir(), "", 4, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Run(context.Background(), d.ID, nil, nil); err != nil {
			t.Fatalf("expire=%v: Run: %v", expire, err)
		}
		r := st.get(t, d.ID)
		data, err := os.ReadFile(filepath.Join(r.Dir, r.Filename))
		if err != nil || !bytes.Equal(data, testserver.Bytes(fakeSeed, 0, fac.size)) {
			t.Fatalf("expire=%v: result %d bytes, %v", expire, len(data), err)
		}
		urls, headers := fac.requests()
		if len(urls) < 2 || urls[0] != pinOrigin {
			t.Fatalf("expire=%v: requests %v", expire, urls)
		}
		sawCDN, sawOrigin := false, false
		for i, u := range urls[1:] {
			h := headers[i+1]
			switch u {
			case pinCDN:
				sawCDN = true
				if h.Get("Cookie") != "" || h.Get("Authorization") != "" {
					t.Fatalf("credentials sent to the cross-origin target: %v", h)
				}
				if h.Get("Referer") != "https://pages.test/" {
					t.Fatalf("caller's Referer dropped: %v", h)
				}
			case pinOrigin:
				sawOrigin = true
				if h.Get("Cookie") != "sid=secret" {
					t.Fatalf("credentials lost on the original URL: %v", h)
				}
			default:
				t.Fatalf("request to %q", u)
			}
		}
		if !sawCDN || sawOrigin != expire {
			t.Fatalf("expire=%v: segment requests %v", expire, strings.Join(urls[1:], " "))
		}
	}
}

func TestPinnedRequest(t *testing.T) {
	base := model.Request{URL: "https://a.test/x", Method: http.MethodGet,
		Headers: http.Header{"Cookie": {"c"}, "Authorization": {"a"}, "X-Other": {"o"}}}
	for _, tc := range []struct {
		name      string
		req       model.Request
		final     string
		wantURL   string
		wantCreds bool
	}{
		{"no redirect", base, "https://a.test/x", "https://a.test/x", true},
		{"no final URL", base, "", "https://a.test/x", true},
		{"same origin", base, "https://a.test:443/y", "https://a.test:443/y", true},
		{"cross origin", base, "https://cdn.test/y?s=1", "https://cdn.test/y?s=1", false},
		{"never downgrade", base, "http://a.test/y", "https://a.test/x", true},
		{"POST is not pinned", model.Request{URL: base.URL, Method: http.MethodPost, Headers: base.Headers, Body: []byte("b")},
			"https://cdn.test/y", "https://a.test/x", true},
	} {
		got, _ := pinnedRequest(tc.req, tc.final)
		creds := got.Headers.Get("Cookie") != "" && got.Headers.Get("Authorization") != ""
		if got.URL != tc.wantURL || creds != tc.wantCreds || got.Headers.Get("X-Other") != "o" {
			t.Errorf("%s: got %q creds %v headers %v", tc.name, got.URL, creds, got.Headers)
		}
	}
	if base.Headers.Get("Cookie") != "c" {
		t.Fatal("caller's headers were modified")
	}
}
