package transport

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

// staticCreds is a test Credentials provider keyed by host.
type staticCreds struct {
	server   map[string]Login // host -> login
	proxy    Login
	hasProxy bool
}

func (c staticCreds) ServerLogin(u *url.URL) (Login, bool) {
	l, ok := c.server[strings.ToLower(u.Hostname())]
	return l, ok
}
func (c staticCreds) ProxyLogin(Proxy) (Login, bool) { return c.proxy, c.hasProxy }

// digestServer serves content behind HTTP Digest authentication (RFC 7616).
type digestServer struct {
	content    []byte
	user, pass string
	realm      string
	algorithm  string // "MD5" or "SHA-256"
	nonce      string

	mu        sync.Mutex
	issued    int  // 401s issued
	staleOnce bool // force one stale-nonce challenge after first success
	staleUsed bool
}

func (d *digestServer) newHash() func() hash.Hash {
	if d.algorithm == "SHA-256" {
		return sha256.New
	}
	return func() hash.Hash { return md5.New() }
}

func (d *digestServer) h(parts ...string) string {
	hsh := d.newHash()()
	hsh.Write([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(hsh.Sum(nil))
}

func (d *digestServer) challenge(w http.ResponseWriter, stale bool) {
	d.mu.Lock()
	d.issued++
	d.mu.Unlock()
	v := fmt.Sprintf(`Digest realm=%q, qop="auth", nonce=%q, algorithm=%s`, d.realm, d.nonce, d.algorithm)
	if stale {
		v += `, stale=true`
	}
	w.Header().Set("WWW-Authenticate", v)
	w.WriteHeader(http.StatusUnauthorized)
}

func (d *digestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Digest ") {
		d.challenge(w, false)
		return
	}
	chs := parseChallenges([]string{auth})
	if len(chs) == 0 {
		d.challenge(w, false)
		return
	}
	p := chs[0].params
	// Validate the response digest.
	ha1 := d.h(d.user, d.realm, d.pass)
	ha2 := d.h(r.Method, p["uri"])
	want := d.h(ha1, p["nonce"], p["nc"], p["cnonce"], p["qop"], ha2)
	if p["response"] != want || p["username"] != d.user {
		d.challenge(w, false)
		return
	}
	// Optionally force one stale challenge to exercise the retry path.
	d.mu.Lock()
	forceStale := d.staleOnce && !d.staleUsed
	if forceStale {
		d.staleUsed = true
		d.nonce = d.nonce + "2" // rotate the nonce
	}
	d.mu.Unlock()
	if forceStale {
		d.challenge(w, true)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(d.content))
}

func runDigest(t *testing.T, algorithm string, stale bool) {
	t.Helper()
	ds := &digestServer{
		content: testContent(8000), user: "neo", pass: "trinity",
		realm: "matrix", algorithm: algorithm, nonce: "nonce-1", staleOnce: stale,
	}
	ts := startServer(t, kindHTTP1, ds)
	host := mustURL(t, ts.URL).Hostname()
	opts := DefaultHTTPOptions()
	opts.Credentials = staticCreds{server: map[string]Login{host: {Username: "neo", Password: "trinity"}}}
	slot := newTestSlot(t, opts, ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: 0, End: 199})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := readBody(t, resp); !bytes.Equal(got, ds.content[0:200]) {
		t.Fatal("body mismatch after digest auth")
	}
}

func TestDigestAuthMD5(t *testing.T)    { runDigest(t, "MD5", false) }
func TestDigestAuthSHA256(t *testing.T) { runDigest(t, "SHA-256", false) }
func TestDigestAuthStaleNonce(t *testing.T) {
	runDigest(t, "MD5", true)
}

func TestBasicAuthSucceeds(t *testing.T) {
	const user, pass = "alice", "s3cret"
	body := testContent(1000)
	var saw string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = r.Header.Get("Authorization")
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	})
	ts := startServer(t, kindHTTP1, h)
	host := mustURL(t, ts.URL).Hostname()
	opts := DefaultHTTPOptions()
	opts.Credentials = staticCreds{server: map[string]Login{host: {Username: user, Password: pass}}}
	slot := newTestSlot(t, opts, ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	if !strings.HasPrefix(saw, "Basic ") {
		t.Fatalf("server did not receive Basic auth, saw %q", saw)
	}
}

func TestAuthRequiredWhenNoLogin(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	ts := startServer(t, kindHTTP1, h)
	opts := DefaultHTTPOptions()
	opts.Credentials = staticCreds{server: map[string]Login{}} // no match
	slot := newTestSlot(t, opts, ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 passed through for the engine to map to auth_required", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestBasicNotSentToOtherHostAfterRedirect checks that credentials are not sent over
// plain HTTP to a different origin reached by a redirect, even when a login matches.
func TestBasicNotSentToOtherHostAfterRedirect(t *testing.T) {
	body := testContent(500)
	var gotAuthOnB bool
	hB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			gotAuthOnB = true
		}
		// B challenges; the client must not answer it over plain http cross-origin.
		w.Header().Set("WWW-Authenticate", `Basic realm="b"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	tsB := httptest.NewServer(hB)
	t.Cleanup(tsB.Close)
	hostB := mustURL(t, tsB.URL).Hostname()

	hA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tsB.URL+"/file", http.StatusFound)
	})
	tsA := httptest.NewServer(hA)
	t.Cleanup(tsA.Close)

	opts := DefaultHTTPOptions()
	// A login exists for B, but it must not be used over plain http from A's redirect.
	opts.Credentials = staticCreds{server: map[string]Login{hostB: {Username: "x", Password: "y"}}}
	slot := newTestSlot(t, opts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: tsA.URL}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuthOnB {
		t.Fatal("Authorization was sent to a different host over plain HTTP after a redirect")
	}
	_ = body
}
