package sitejob

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"downex/internal/sitemedia"
)

// The key ignores the fragment and keeps no secret, and every other part of the request
// that changes what the page shows changes it.
func TestCacheKey(t *testing.T) {
	base := Request{PageURL: "https://www.youtube.com/watch?v=aaaaaaaaaaa", Cookie: "SID=1", Referer: "https://a.example/",
		Proxy: Proxy{URL: "http://proxy.lan:3128", Username: "me", Password: "pw"}}
	k := cacheKey(base)
	same := base
	same.PageURL += "#t=30"
	same.Fresh = true
	same.Prefetch = true // a look-ahead fills the entry a later probe, add and run use
	same.Proxy.Password = "other"
	if cacheKey(same) != k {
		t.Error("the fragment, Fresh, Prefetch or the proxy password changed the key")
	}
	for name, change := range map[string]func(*Request){
		"page":      func(r *Request) { r.PageURL += "&list=x" },
		"cookie":    func(r *Request) { r.Cookie = "SID=2" },
		"no cookie": func(r *Request) { r.Cookie = "" },
		"referer":   func(r *Request) { r.Referer = "" },
		"proxy":     func(r *Request) { r.Proxy.URL = "" },
	} {
		r := base
		change(&r)
		if cacheKey(r) == k {
			t.Errorf("changing the %s kept the key", name)
		}
	}
	if len(k) != 64 {
		t.Errorf("key %q", k)
	}
}

// Pages are kept for the TTL and failures for the failure TTL; the least recently used
// is dropped beyond the limit, and a later result replaces an older one.
func TestCache(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c := newCache(3, 15*time.Minute, 30*time.Second)
	page := &sitemedia.Info{ID: "p"}
	c.put(&result{key: "a", info: page, at: t0})
	c.put(&result{key: "f", err: errors.New("failed"), at: t0})
	if r, ok := c.get("a", t0.Add(14*time.Minute)); !ok || r.info != page {
		t.Error("a page was dropped before its TTL")
	}
	if _, ok := c.get("f", t0.Add(29*time.Second)); !ok {
		t.Error("a failure was dropped before its TTL")
	}
	if _, ok := c.get("f", t0.Add(30*time.Second)); ok {
		t.Error("a failure outlived its TTL")
	}
	if _, ok := c.get("a", t0.Add(15*time.Minute)); ok {
		t.Error("a page outlived its TTL")
	}
	if _, ok := c.get("a", t0.Add(-time.Second)); ok {
		t.Error("a result from the future (the clock went back) was used")
	}

	for i := range 3 {
		c.put(&result{key: fmt.Sprint(i), info: page, at: t0})
	}
	c.get("0", t0) // 1 is now the least recently used
	c.put(&result{key: "3", info: page, at: t0})
	if _, ok := c.get("1", t0); ok || c.len() != 3 {
		t.Errorf("the least recently used entry was kept (%d entries)", c.len())
	}
	newer := &sitemedia.Info{ID: "newer"}
	c.put(&result{key: "0", info: newer, at: t0.Add(time.Minute)})
	if r, ok := c.get("0", t0.Add(time.Minute)); !ok || r.info != newer {
		t.Error("a newer result did not replace the older one")
	}
	c.put(&result{key: "4", info: page, at: t0.Add(16 * time.Minute)})
	if c.len() != 1 {
		t.Errorf("stale entries kept: %d", c.len())
	}
}

// The daemon keeps at least as many pages as the extension keeps answers for
// (VIDEO_CACHE_SIZE of ext/src/lib/video-handoff.ts), so that look-aheads never push out
// a page whose qualities a browser still shows: its Add would resolve it again.
func TestCacheHoldsWhatTheExtensionShows(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "ext", "src", "lib", "video-handoff.ts"))
	if err != nil {
		t.Skipf("the extension's source is not here: %v", err)
	}
	m := regexp.MustCompile(`VIDEO_CACHE_SIZE = (\d+);`).FindSubmatch(src)
	if m == nil {
		t.Fatal("VIDEO_CACHE_SIZE not found in video-handoff.ts")
	}
	if n, _ := strconv.Atoi(string(m[1])); DefaultMaxEntries < n {
		t.Errorf("DefaultMaxEntries %d < the extension's VIDEO_CACHE_SIZE %d", DefaultMaxEntries, n)
	}
}
