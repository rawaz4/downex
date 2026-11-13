package transport

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"downex/internal/model"
)

// chainHandler redirects /r/N to /r/N-1 (with status code) until /r/0, which records
// the request and serves content.
func chainHandler(rec *recorder, content []byte, code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/"))
		if err != nil || n < 0 {
			http.NotFound(w, r)
			return
		}
		if n > 0 {
			http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), code)
			return
		}
		rec.record(r)
		_, _ = w.Write(content)
	})
}

func TestRedirectLimit(t *testing.T) {
	var rec recorder
	content := []byte("payload")
	ts := startServer(t, kindHTTP1, chainHandler(&rec, content, http.StatusFound))
	tests := []struct {
		name         string
		maxRedirects int
		hops         int
		wantErr      bool
	}{
		{"within custom limit", 3, 3, false},
		{"over custom limit", 3, 4, true},
		{"within default limit", 0, DefaultMaxRedirects, false},
		{"over default limit", 0, DefaultMaxRedirects + 1, true},
		{"disabled", -1, 1, true},
		{"disabled without redirect", -1, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultHTTPOptions()
			opts.MaxRedirects = tc.maxRedirects
			slot := newTestSlot(t, opts, ts)
			resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL + "/r/" + strconv.Itoa(tc.hops)}, End: -1})
			if tc.wantErr {
				if !errors.Is(err, ErrRedirects) {
					t.Fatalf("err = %v, want ErrRedirects", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := readBody(t, resp); !bytes.Equal(got, content) {
				t.Fatalf("body = %q", got)
			}
			if want := ts.URL + "/r/0"; resp.FinalURL != want {
				t.Errorf("FinalURL = %q, want %q", resp.FinalURL, want)
			}
		})
	}
}

// TestRedirectHeaders checks which caller headers survive same- and cross-origin hops.
func TestRedirectHeaders(t *testing.T) {
	var recA, recB recorder
	content := []byte("data")
	var tsA, tsB *httptest.Server
	// Server A: /same → /final on A; /cross → B; /bounce → B → back to A.
	tsA = startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/cross":
			http.Redirect(w, r, tsB.URL+"/final", http.StatusFound)
		case "/bounce":
			http.Redirect(w, r, tsB.URL+"/back", http.StatusFound)
		case "/final":
			recA.record(r)
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	// Server B differs from A only by port, which net/http alone treats as the same host.
	tsB = startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/back":
			http.Redirect(w, r, tsA.URL+"/final", http.StatusFound)
		case "/final":
			recB.record(r)
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), tsA, tsB)
	headers := http.Header{
		"Cookie":        {"sid=secret"},
		"Authorization": {"Bearer token"},
		"Referer":       {"https://pages.example/"},
		"X-Custom":      {"kept"},
	}
	fetch := func(t *testing.T, path string, h http.Header) *Response {
		t.Helper()
		resp, err := slot.Fetch(testCtx(t), RangeRequest{
			Request: model.Request{URL: tsA.URL + path, Headers: h}, Start: 0, End: 3, IfRange: `"tag"`,
		})
		if err != nil {
			t.Fatal(err)
		}
		readBody(t, resp)
		return resp
	}
	checkCommon := func(t *testing.T, seen seenRequest) {
		t.Helper()
		for k, v := range map[string]string{"Range": "bytes=0-3", "If-Range": `"tag"`, "Accept-Encoding": "identity", "X-Custom": "kept"} {
			if got := seen.Header.Get(k); got != v {
				t.Errorf("after redirect %s = %q, want %q", k, got, v)
			}
		}
	}

	t.Run("same origin keeps credentials", func(t *testing.T) {
		resp := fetch(t, "/same", headers)
		seen := recA.last(t)
		checkCommon(t, seen)
		if seen.Header.Get("Cookie") != "sid=secret" || seen.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("credentials lost on a same-origin redirect: %v", seen.Header)
		}
		if seen.Header.Get("Referer") != "https://pages.example/" {
			t.Errorf("Referer = %q", seen.Header.Get("Referer"))
		}
		if resp.FinalURL != tsA.URL+"/final" {
			t.Errorf("FinalURL = %q", resp.FinalURL)
		}
	})
	t.Run("cross origin drops credentials", func(t *testing.T) {
		resp := fetch(t, "/cross", headers)
		seen := recB.last(t)
		checkCommon(t, seen)
		if v := seen.Header.Get("Cookie"); v != "" {
			t.Errorf("Cookie leaked across origins: %q", v)
		}
		if v := seen.Header.Get("Authorization"); v != "" {
			t.Errorf("Authorization leaked across origins: %q", v)
		}
		if seen.Header.Get("Referer") != "https://pages.example/" {
			t.Errorf("caller Referer = %q, want it kept", seen.Header.Get("Referer"))
		}
		if resp.FinalURL != tsB.URL+"/final" {
			t.Errorf("FinalURL = %q", resp.FinalURL)
		}
	})
	t.Run("credentials stay dropped after returning to the origin", func(t *testing.T) {
		fetch(t, "/bounce", headers)
		seen := recA.last(t)
		checkCommon(t, seen)
		if v := seen.Header.Get("Cookie") + seen.Header.Get("Authorization"); v != "" {
			t.Errorf("credentials restored after a cross-origin hop: %q", v)
		}
	})
	t.Run("no synthesised referer", func(t *testing.T) {
		h := http.Header{"X-Custom": {"kept"}}
		fetch(t, "/same", h)
		if v := recA.last(t).Header.Get("Referer"); v != "" {
			t.Errorf("Referer %q was synthesised from a redirect hop", v)
		}
	})
}

func TestRedirectReplaysBody(t *testing.T) {
	var rec recorder
	ts := startServer(t, kindHTTP2TLS, chainHandler(&rec, []byte("ok"), http.StatusTemporaryRedirect))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	body := []byte(`{"id":42}`)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{
		Request: model.Request{URL: ts.URL + "/r/2", Method: http.MethodPost, Body: body}, End: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	seen := rec.last(t)
	if seen.Method != http.MethodPost || !bytes.Equal(seen.Body, body) {
		t.Fatalf("after 307s server saw %s %q", seen.Method, seen.Body)
	}
}

func TestRedirectToUnsupportedScheme(t *testing.T) {
	ts := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "ftp://files.example/x.bin")
		w.WriteHeader(http.StatusFound)
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	if _, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1}); err == nil {
		t.Fatal("redirect to ftp:// was followed")
	}
}

// TestRedirectRefusesTLSDowngrade: an https request is never continued over plain
// http (§10, N5), not even after an intermediate https hop; upgrades are allowed.
func TestRedirectRefusesTLSDowngrade(t *testing.T) {
	var plainHits atomic.Int32
	var secure *httptest.Server
	plain := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			http.Redirect(w, r, secure.URL+"/f.bin", http.StatusFound)
			return
		}
		plainHits.Add(1)
		_, _ = w.Write([]byte("evil"))
	}))
	secure = startServer(t, kindHTTP1TLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/down":
			http.Redirect(w, r, plain.URL+"/f.bin", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/down", http.StatusFound)
		default:
			_, _ = w.Write([]byte("good"))
		}
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), secure, plain)
	headers := http.Header{"Referer": {"https://intranet.example/secret?token=abc"}, "X-Api-Key": {"k"}}
	for _, path := range []string{"/down", "/hop"} {
		resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: secure.URL + path, Headers: headers}, End: -1})
		if err == nil {
			body := readBody(t, resp)
			t.Fatalf("%s: https request was redirected to http and got %q", path, body)
		}
		if !errors.Is(err, ErrInsecureRedirect) {
			t.Fatalf("%s: err = %v, want ErrInsecureRedirect", path, err)
		}
	}
	if n := plainHits.Load(); n != 0 {
		t.Fatalf("the plain http server received %d requests", n)
	}
	// http → https is an upgrade and is followed.
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: plain.URL + "/up"}, End: -1})
	if err != nil {
		t.Fatalf("upgrade redirect: %v", err)
	}
	if got := readBody(t, resp); string(got) != "good" {
		t.Fatalf("upgrade body %q", got)
	}
}

func TestOrigin(t *testing.T) {
	tests := []struct{ a, b string }{
		{"http://Example.com/x", "http://example.com:80/y"},
		{"https://example.com/", "https://EXAMPLE.com:443/z?q"},
	}
	for _, tc := range tests {
		ua, _ := url.Parse(tc.a)
		ub, _ := url.Parse(tc.b)
		if origin(ua) != origin(ub) {
			t.Errorf("origin(%q) = %q != origin(%q) = %q", tc.a, origin(ua), tc.b, origin(ub))
		}
	}
	diff := [][2]string{
		{"http://example.com/", "https://example.com/"},
		{"http://example.com/", "http://example.com:8080/"},
		{"http://example.com/", "http://sub.example.com/"},
	}
	for _, d := range diff {
		ua, _ := url.Parse(d[0])
		ub, _ := url.Parse(d[1])
		if origin(ua) == origin(ub) {
			t.Errorf("origin(%q) == origin(%q)", d[0], d[1])
		}
	}
}
