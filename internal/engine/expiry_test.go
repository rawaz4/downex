package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// TestIsExpiry is rule E1 of docs/ENGINE-SPEC.md §7.2 as a table.
func TestIsExpiry(t *testing.T) {
	const sent = "https://cdn.example/f.zip?sig=1"
	const moved = "https://login.example/expired"
	challenge := http.Header{"Www-Authenticate": {`Basic realm="x"`}}
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
		final  string
		ctype  string
		check  expiryCheck
		want   bool
	}{
		{"403", 403, nil, sent, "", expiryCheck{}, true},
		{"410", 410, nil, sent, "", expiryCheck{}, true},
		{"401 without a challenge", 401, nil, sent, "", expiryCheck{}, true},
		{"401 with a challenge", 401, challenge, sent, "", expiryCheck{}, false},
		{"plain 404", 404, nil, sent, "", expiryCheck{}, false},
		{"redirect to 404", 404, nil, moved, "", expiryCheck{}, true},
		{"500", 500, nil, sent, "", expiryCheck{}, false},
		{"429", 429, nil, sent, "", expiryCheck{}, false},
		{"200 page to a range", 200, nil, sent, "text/html", expiryCheck{rangedRequest: true}, true},
		{"200 xhtml to a range", 200, nil, sent, "application/xhtml+xml", expiryCheck{rangedRequest: true}, true},
		{"200 file to a range", 200, nil, sent, "application/zip", expiryCheck{rangedRequest: true}, false},
		{"206 labelled html", 206, nil, sent, "text/html", expiryCheck{rangedRequest: true}, false},
		{"200 page, not a ranged request", 200, nil, sent, "text/html", expiryCheck{}, false},
		{"probe redirected to a page", 200, nil, moved, "text/html", expiryCheck{probe: true}, true},
		{"probe redirected to a page, html file", 200, nil, moved, "text/html", expiryCheck{probe: true, htmlFile: true}, false},
		{"probe page without redirect", 200, nil, sent, "text/html", expiryCheck{probe: true}, false},
		{"probe redirected to a file", 200, nil, moved, "application/zip", expiryCheck{probe: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.check
			c.sentURL = sent
			resp := &transport.Response{StatusCode: tc.status, Header: tc.header, FinalURL: tc.final, ContentType: tc.ctype}
			got, why := isExpiry(resp, c)
			if got != tc.want {
				t.Fatalf("isExpiry = %v (%q), want %v", got, why, tc.want)
			}
			if got && (why == "" || strings.Contains(why, "example")) {
				t.Fatalf("reason %q must name the answer, never the URL", why)
			}
		})
	}
	if !isHTMLFile(&model.Download{}) || !isHTMLFile(&model.Download{Filename: "a.HTM"}) ||
		!isHTMLFile(&model.Download{Filename: "a.bin", MIME: "text/html"}) || isHTMLFile(&model.Download{Filename: "a.zip"}) {
		t.Fatal("isHTMLFile")
	}
	if isTransient(expiryError(0, 9, "HTTP 403 Forbidden")) {
		t.Fatal("an expiry must be permanent")
	}
	if isThrottle(expiryError(0, 9, "HTTP 403 Forbidden")) {
		t.Fatal("an expiry is no throttle")
	}
}

// expireMidway runs download id until the server has sent about held bytes, then
// changes the server's token, so that every request from then on is refused, and lets
// the responses already out go on. It returns Run's error.
func expireMidway(t *testing.T, h *harness, id model.DownloadID, held int64) error {
	t.Helper()
	h.srv.Hold(held)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := runContext(t)
		defer cancel()
		done <- h.eng.Run(ctx, id, nil, nil)
	}()
	waitFor(t, 30*time.Second, "the held bytes", func() bool { return h.srv.BytesSent() >= held })
	h.srv.SetToken("new")
	h.srv.Release()
	return awaitRun(t, done)
}

// TestExpiryMidDownload: once a request finds the link expired, no new request is sent,
// the responses already streaming finish their ranges, durable bytes are kept, and the
// download fails with ErrLinkExpired (E2, E3).
func TestExpiryMidDownload(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name string
		opts func(*testserver.Options)
		text string
	}{
		{"403", func(*testserver.Options) {}, "HTTP 403 Forbidden"},
		{"410", func(o *testserver.Options) { o.ExpiredStatus = http.StatusGone }, "HTTP 410 Gone"},
		{"401 without challenge", func(o *testserver.Options) { o.ExpiredNoChallenge401 = true }, "HTTP 401 Unauthorized"},
		{"redirect to a web page", func(o *testserver.Options) { o.ExpiredRedirect = true }, "a web page (text/html)"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			const size = 6 << 20
			so := testserver.Options{Size: size, Seed: 7, ETag: `"e1"`, Token: "old"}
			mode.opts(&so)
			opts := resumeOpts()
			const conns = 4
			opts.HostRules = &capRules{conns: conns, caps: map[string]int64{}}
			h := newHarness(t, so, opts)
			host := strings.TrimPrefix(h.srv.URL, "http://")
			opts.HostRules.(*capRules).caps[host] = 256 << 10 // every request ends after 256 KiB
			d := h.add("/f.bin?token=old", "", conns)

			err := expireMidway(t, h, d.ID, 1<<20)
			if !errors.Is(err, ErrLinkExpired) {
				t.Fatalf("Run = %v, want ErrLinkExpired", err)
			}
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error %q names the URL", err)
			}
			rec := h.st.get(t, d.ID)
			if rec.State != model.StateFailed || !strings.Contains(rec.Error, mode.text) ||
				!strings.Contains(rec.Error, "engine: link expired") {
				t.Fatalf("record = %v %q", rec.State, rec.Error)
			}
			if rec.RemoteName != "f.bin" || rec.Size != size || rec.Validators.ETag != `"e1"` {
				t.Fatalf("kept fields: name %q size %d validators %+v", rec.RemoteName, rec.Size, rec.Validators)
			}
			var durable int64
			for _, s := range h.st.segs(d.ID) {
				durable += s.Durable
			}
			if durable < 1<<20/2 || durable >= size {
				t.Fatalf("durable = %d", durable)
			}
			// Every request after the token changed was refused; at most one per connection
			// was out when the first one was (no new request after the expiry).
			refused := 0
			for _, r := range h.srv.Requests() {
				if r.Status != http.StatusPartialContent && r.Status != http.StatusOK {
					refused++
				}
			}
			if refused == 0 || refused > conns {
				t.Fatalf("%d refused requests, want 1..%d: %+v", refused, conns, h.srv.Requests())
			}
			if _, err := os.Stat(partPath(rec.Dir, rec.Filename)); err != nil {
				t.Fatalf("part file: %v", err)
			}
		})
	}
}

// TestExpiryLiveStreamsFinish: the connections receiving when the link expires finish
// their ranges, so a download whose last ranges were all out completes (E3).
func TestExpiryLiveStreamsFinish(t *testing.T) {
	t.Parallel()
	const size = 4 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 8, Token: "old"}, resumeOpts())
	d := h.add("/whole.bin?token=old", "", 1) // one connection: the probe's response is the whole file
	if err := expireMidway(t, h, d.ID, 1<<20); err != nil {
		t.Fatalf("Run = %v, want completion", err)
	}
	h.verify(d.ID)
}

// TestExpiredProbe: a fresh download whose link is already expired fails at once with
// ErrLinkExpired: one request, no retry, nothing kept (E4).
func TestExpiredProbe(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 9, Token: "new", ExpiredStatus: http.StatusGone}, fastOpts())
	d := h.add("/f.zip?token=old", "", 4)
	if err := h.run(d.ID, nil, nil); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("Run = %v, want ErrLinkExpired", err)
	}
	if n := len(h.srv.Requests()); n != 1 {
		t.Fatalf("%d requests, want the probe only", n)
	}
	if rec := h.st.get(t, d.ID); rec.State != model.StateFailed || !strings.Contains(rec.Error, "probe: HTTP 410 Gone") {
		t.Fatalf("record %v %q", rec.State, rec.Error)
	}
}

// TestExpiredProbeRedirectedToPage: a fresh download whose link now redirects to a web
// page fails as expired, unless the download is a web page itself (E1 e).
func TestExpiredProbeRedirectedToPage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 10, Token: "new", ExpiredRedirect: true}, fastOpts())
	zip := h.add("/a.zip?token=old", "a.zip", 4)
	if err := h.run(zip.ID, nil, nil); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("Run(a.zip) = %v, want ErrLinkExpired", err)
	}
	page := h.add("/a.html?token=old", "a.html", 4)
	if err := h.run(page.ID, nil, nil); err != nil {
		t.Fatalf("Run(a.html) = %v, want the page downloaded", err)
	}
	rec := h.st.get(t, page.ID)
	got, err := os.ReadFile(rec.Dir + string(os.PathSeparator) + rec.Filename)
	if err != nil || string(got) != testserver.ExpiredPage {
		t.Fatalf("page = %q, %v", got, err)
	}
}

// TestRedirectTo404 is an expiry, a plain 404 is not (E1 c).
func TestRedirectTo404(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/signed", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/gone", http.StatusFound)
	})
	srv := httptest.NewServer(mux) // "/gone" and "/plain" are 404
	t.Cleanup(srv.Close)
	st := newMemStore()
	eng := New(st, &httpFactory{newTransport: func() *http.Transport {
		return srv.Client().Transport.(*http.Transport).Clone()
	}}, fastOpts())
	dir := t.TempDir()
	for path, wantExpired := range map[string]bool{"/signed": true, "/plain": false} {
		d, err := eng.Add(context.Background(), model.Request{URL: srv.URL + path}, dir, "f.bin", 2, 0)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := runContext(t)
		err = eng.Run(ctx, d.ID, nil, nil)
		cancel()
		var se *StatusError
		switch {
		case wantExpired && !errors.Is(err, ErrLinkExpired):
			t.Errorf("%s: Run = %v, want ErrLinkExpired", path, err)
		case !wantExpired && (!errors.As(err, &se) || se.Code != 404 || errors.Is(err, ErrLinkExpired)):
			t.Errorf("%s: Run = %v, want HTTP 404", path, err)
		}
	}
}

// TestRedirected compares parsed URLs.
func TestRedirected(t *testing.T) {
	for _, tc := range []struct {
		sent, final string
		want        bool
	}{
		{"https://a.example/x", "https://a.example/x", false},
		{"https://a.example/x", "", false},
		{"https://a.example/x", "https://b.example/x", true},
		{"https://a.example/x?s=1", "https://a.example/x?s=2", true},
	} {
		if got := redirected(tc.sent, tc.final); got != tc.want {
			t.Errorf("redirected(%q, %q) = %v", tc.sent, tc.final, got)
		}
	}
	if _, err := url.Parse("https://a.example/x"); err != nil {
		t.Fatal(err)
	}
}
