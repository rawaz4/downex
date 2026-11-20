package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxRedirects bounds the redirects of one request.
const maxRedirects = 10

// NewHTTPClient returns the client for update requests over rt (nil: a clone of
// http.DefaultTransport, which takes the proxy from the environment, with a 30-second
// response header timeout). Requests have no overall timeout: callers pass a context.
// Redirects are followed only to https URLs (secureClient adds that rule to any client).
func NewHTTPClient(rt http.RoundTripper) *http.Client {
	if rt == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 30 * time.Second
		rt = t
	}
	return secureClient(&http.Client{Transport: rt})
}

// secureClient returns a copy of c (nil: NewHTTPClient(nil)) that refuses redirects to
// URLs other than https and stops after maxRedirects; c's own CheckRedirect still runs.
func secureClient(c *http.Client) *http.Client {
	if c == nil {
		return NewHTTPClient(nil)
	}
	cp := *c
	next := c.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return ErrInsecureURL
		}
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
	return &cp
}

// fetchError is a failed request: it wraps ErrFetch and the cause, and its message names
// what was fetched but never the URL.
type fetchError struct {
	what string
	err  error
}

func (e *fetchError) Error() string   { return e.what + ": " + e.err.Error() }
func (e *fetchError) Unwrap() []error { return []error{ErrFetch, e.err} }

// newFetchError wraps err, dropping the URL that net/http puts into its errors.
func newFetchError(what string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &fetchError{what: what, err: err}
}

// statusError is an HTTP status other than 200.
type statusError int

func (s statusError) Error() string { return fmt.Sprintf("HTTP status %d", int(s)) }

// notPublished reports a manifest request that the server answered with "not found" (404)
// or "gone" (410): the feed is not published (yet).
func notPublished(err error) bool {
	var s statusError
	return errors.As(err, &s) && (s == http.StatusNotFound || s == http.StatusGone)
}

// get starts a GET of rawURL and checks the status (200) and the announced length.
func get(ctx context.Context, c *http.Client, rawURL, userAgent, what string, limit int64) (*http.Response, error) {
	if _, err := checkHTTPS(rawURL); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, ErrInsecureURL)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, ErrInsecureURL) {
			return nil, fmt.Errorf("%s: redirected: %w", what, ErrInsecureURL)
		}
		return nil, newFetchError(what, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, newFetchError(what, statusError(resp.StatusCode))
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %w (more than %d bytes)", what, ErrTooLarge, limit)
	}
	return resp, nil
}

// getBytes fetches rawURL into memory, at most limit bytes.
func getBytes(ctx context.Context, c *http.Client, rawURL, userAgent, what string, limit int64) ([]byte, error) {
	resp, err := get(ctx, c, rawURL, userAgent, what, limit)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, newFetchError(what, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w (more than %d bytes)", what, ErrTooLarge, limit)
	}
	return data, nil
}

// Feed is an update feed.
type Feed struct {
	URL       string            // https URL of the manifest (CheckFeedURL)
	Key       ed25519.PublicKey // ParsePublicKey(PublicKey)
	Client    *http.Client      // nil: NewHTTPClient(nil)
	UserAgent string
}

// Fetch downloads the manifest (at most MaxManifestSize bytes) and its signature (at
// <URL>.sig, at most MaxSignatureSize bytes), verifies the signature over the exact
// manifest bytes and only then parses the manifest. A manifest the server does not have
// (404, 410) is ErrNotPublished.
func (f Feed) Fetch(ctx context.Context) (Manifest, error) {
	if len(f.Key) == 0 {
		return Manifest{}, ErrDisabled
	}
	u, err := CheckFeedURL(f.URL)
	if err != nil {
		return Manifest{}, fmt.Errorf("update feed: %w", err)
	}
	c := secureClient(f.Client)
	data, err := getBytes(ctx, c, u.String(), f.UserAgent, "update manifest", MaxManifestSize)
	if notPublished(err) {
		return Manifest{}, fmt.Errorf("%w (%w)", ErrNotPublished, err)
	}
	if err != nil {
		return Manifest{}, err
	}
	sig, err := getBytes(ctx, c, SignatureURL(u), f.UserAgent, "update signature", MaxSignatureSize)
	if err != nil {
		return Manifest{}, err
	}
	if err := Verify(f.Key, data, sig); err != nil {
		return Manifest{}, err
	}
	return ParseManifest(data)
}

// Check fetches the feed and evaluates it for the running version and system.
func (f Feed) Check(ctx context.Context, current Version, goos, goarch string) (Result, error) {
	m, err := f.Fetch(ctx)
	if err != nil {
		return Result{}, err
	}
	return Evaluate(m, current, goos, goarch)
}

// partSuffix marks a package being downloaded.
const partSuffix = ".part"

// Download fetches the package a into dir/name (dir is created, mode 0700): over https
// only, never more than a.Size bytes, written to name+".part", flushed, compared with
// a.Size and a.SHA256 and only then renamed to name. A package that fails the checks is
// deleted. name is a plain file name. The package gets no Mark of the Web or quarantine
// attribute (unlike a user's download, internal/filemark): its manifest vouches for it, and
// a quarantined Downex.app would not start after an ad-hoc signed update.
func Download(ctx context.Context, c *http.Client, userAgent string, a Asset, dir, name string) (string, error) {
	return DownloadProgress(ctx, c, userAgent, a, dir, name, nil)
}

// DownloadProgress is Download that reports the bytes received so far and the package's
// size to progress (nil: none) as they arrive, on the downloading goroutine.
func DownloadProgress(ctx context.Context, c *http.Client, userAgent string, a Asset, dir, name string, progress func(done, total int64)) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\:`) {
		return "", fmt.Errorf("update: invalid package name %q", name)
	}
	want, err := hex.DecodeString(a.SHA256)
	if err != nil || len(want) != sha256.Size {
		return "", fmt.Errorf("%w: sha256 is not 64 hex digits", ErrManifest)
	}
	if a.Size <= 0 || a.Size > MaxAssetSize {
		return "", fmt.Errorf("%w: invalid size %d", ErrManifest, a.Size)
	}
	resp, err := get(ctx, secureClient(c), a.URL, userAgent, "update package", a.Size)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength >= 0 && resp.ContentLength != a.Size {
		return "", fmt.Errorf("%w: the server announces %d bytes, the manifest %d", ErrSize, resp.ContentLength, a.Size)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: create %s: %w", dir, err)
	}
	final := filepath.Join(dir, name)
	part := final + partSuffix
	_ = os.Remove(final)
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(part)
		}
	}()
	h := sha256.New()
	var w io.Writer = io.MultiWriter(f, h)
	if progress != nil {
		progress(0, a.Size)
		w = &progressWriter{w: w, total: a.Size, report: progress}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return "", fmt.Errorf("update: write the package: %w", err)
		}
		return "", newFetchError("update package", err)
	}
	if n != a.Size {
		return "", fmt.Errorf("%w: got %d bytes, the manifest says %d", ErrSize, n, a.Size)
	}
	if got := h.Sum(nil); !equalBytes(got, want) {
		return "", ErrChecksum
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("update: flush the package: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("update: write the package: %w", err)
	}
	ok = true
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("update: %w", err)
	}
	return final, nil
}

// equalBytes compares two digests (not secret: no constant time needed).
func equalBytes(a, b []byte) bool { return string(a) == string(b) }

// CleanDir removes the packages and partial downloads that Download left in dir (from an
// earlier update), ignoring files it cannot remove (a Setup that still runs). Other files
// are left alone.
func CleanDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if strings.HasSuffix(n, partSuffix) ||
			(strings.HasPrefix(n, "Downex-") && (strings.HasSuffix(n, ".exe") || strings.HasSuffix(n, ".dmg"))) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// progressWriter counts what passes through it for DownloadProgress.
type progressWriter struct {
	w      io.Writer
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	p.report(p.done, p.total)
	return n, err
}
