package sitejob

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"time"

	"downex/internal/sitemedia"
)

// cacheKey identifies the extractions of one page as one user sees it
// (docs/SITE-MEDIA-SPEC.md §2.6): the page URL without its fragment, the SHA-256 of
// the Cookie header, the Referer and the proxy route, hashed together so that the key
// holds none of them.
func cacheKey(r Request) string {
	page := r.PageURL
	if u, err := url.Parse(page); err == nil {
		u.Fragment, u.RawFragment = "", ""
		page = u.String()
	}
	cookie := sha256.Sum256([]byte(r.Cookie))
	h := sha256.New()
	for _, part := range []string{page, hex.EncodeToString(cookie[:]), r.Referer, r.Proxy.URL} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// result is a finished extraction: the page, or why yt-dlp could not give it.
type result struct {
	key  string
	info *sitemedia.Info
	err  error
	at   time.Time // when it finished
}

// cache keeps the latest results, least recently used dropped first: a page for ttl,
// a refusal or failure for failTTL (long enough for the caller that gave up waiting
// to collect it with its retry). It is not safe for concurrent use.
type cache struct {
	maxEntries   int
	ttl, failTTL time.Duration
	lru          *list.List // of *result, most recently used first
	byKey        map[string]*list.Element
}

func newCache(maxEntries int, ttl, failTTL time.Duration) *cache {
	return &cache{maxEntries: maxEntries, ttl: ttl, failTTL: failTTL, lru: list.New(), byKey: make(map[string]*list.Element)}
}

// get returns the result for key when it is still fresh at now.
func (c *cache) get(key string, now time.Time) (*result, bool) {
	el, ok := c.byKey[key]
	if !ok {
		return nil, false
	}
	r := el.Value.(*result)
	if !c.fresh(r, now) {
		c.lru.Remove(el)
		delete(c.byKey, key)
		return nil, false
	}
	c.lru.MoveToFront(el)
	return r, true
}

// put stores r, replacing an older result for its key, and drops what is stale or
// beyond maxEntries.
func (c *cache) put(r *result) {
	if el, ok := c.byKey[r.key]; ok {
		el.Value = r
		c.lru.MoveToFront(el)
	} else {
		c.byKey[r.key] = c.lru.PushFront(r)
	}
	for el := c.lru.Back(); el != nil; {
		prev := el.Prev()
		if old := el.Value.(*result); c.lru.Len() > c.maxEntries || !c.fresh(old, r.at) {
			c.lru.Remove(el)
			delete(c.byKey, old.key)
		}
		el = prev
	}
}

// fresh reports whether r may still answer at now.
func (c *cache) fresh(r *result, now time.Time) bool {
	ttl := c.ttl
	if r.err != nil {
		ttl = c.failTTL
	}
	age := now.Sub(r.at)
	return age >= 0 && age < ttl
}

// len returns the number of results kept.
func (c *cache) len() int { return c.lru.Len() }
