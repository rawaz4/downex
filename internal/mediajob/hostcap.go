package mediajob

import (
	"context"
	"math"
	"net/url"
	"strings"
	"sync"

	"downex/internal/limiter"
	"downex/internal/media"
)

// HostRules supplies per-host limits (docs/M6-SPEC.md §2, docs/SITE-MEDIA-SPEC.md
// §2.4). engine.HostRules values satisfy it. A job calls it while it runs, so
// implementations must be fast and safe for concurrent use. Hosts are passed as a URL's
// lower-case host, with its port when the URL has one.
type HostRules interface {
	// HostMaxConnections caps the parallel requests of one job to host; 0 means no cap.
	HostMaxConnections(host string) int
	// HostLimiter returns a speed limiter shared by every download to host, or nil.
	HostLimiter(host string) limiter.Limiter
}

// requestCaps is the optional extension of HostRules that caps the length of every
// ranged request to a host (engine.RequestCaps), found by type assertion.
type requestCaps interface {
	HostMaxRequestBytes(host string) int64
}

// throttle is what the copier waits on for every slice it reads: a limiter.Limiter, or
// two of them combined.
type throttle interface {
	WaitN(ctx context.Context, n int) error
}

// bothThrottles waits on two throttles so that both limits hold.
type bothThrottles struct{ a, b throttle }

func (t bothThrottles) WaitN(ctx context.Context, n int) error {
	if err := t.a.WaitN(ctx, n); err != nil {
		return err
	}
	return t.b.WaitN(ctx, n)
}

// hostOf returns the lower-case host (with any port) of rawURL, "" when it does not
// parse.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// hostThrottle is what the bytes from host wait on: the job's limiter (the global →
// queue → download chain) and the host's shared limiter; nil for neither.
func (o *Options) hostThrottle(host string) throttle {
	var job, shared throttle
	if o.Limiter != nil {
		job = o.Limiter
	}
	if o.HostRules != nil {
		if lim := o.HostRules.HostLimiter(host); lim != nil {
			shared = lim
		}
	}
	switch {
	case job == nil:
		return shared
	case shared == nil:
		return job
	}
	return bothThrottles{job, shared}
}

// hostConnections returns the host's connection cap; 0 means none.
func (o *Options) hostConnections(host string) int {
	if o.HostRules == nil {
		return 0
	}
	return max(o.HostRules.HostMaxConnections(host), 0)
}

// hostRequestCap returns the host's cap on the length of a ranged request; 0 means none.
func (o *Options) hostRequestCap(host string) int64 {
	rc, ok := o.HostRules.(requestCaps)
	if !ok {
		return 0
	}
	return max(rc.HostMaxRequestBytes(host), 0)
}

// cappedEnd is the last byte a request starting at pos may ask for under limit (0 = no
// cap): end itself, or pos+limit-1 when that comes first (end -1 = to the end of the
// resource).
func cappedEnd(pos, end, limit int64) int64 {
	if limit <= 0 || pos > math.MaxInt64-limit {
		return end
	}
	if last := pos + limit - 1; end < 0 || last < end {
		return last
	}
	return end
}

// ChunkSize returns the chunk size of a ranged track (docs/SITE-MEDIA-SPEC.md §2.4):
// the smaller non-zero value of the format's own chunk size (formatChunk, yt-dlp's
// downloader_options.http_chunk_size) and the request cap rules give the host of
// rawURL; media.DefaultChunkBytes when both are zero; clamped to
// media.MinChunkBytes..media.MaxChunkBytes. rules may be nil.
func ChunkSize(rules HostRules, rawURL string, formatChunk int64) int64 {
	o := Options{HostRules: rules}
	size := max(formatChunk, 0)
	if c := o.hostRequestCap(hostOf(rawURL)); c > 0 && (size == 0 || c < size) {
		size = c
	}
	if size == 0 {
		size = media.DefaultChunkBytes
	}
	return min(max(size, media.MinChunkBytes), media.MaxChunkBytes)
}

// Chunk fitting (docs/SITE-MEDIA-SPEC.md §2.4).
const (
	// minFitChunk is the smallest chunk size FitChunk lowers a track to: below it, a
	// request's round trip costs more than the parallel connections gain.
	minFitChunk = 2 << 20
	// fitAlign is the multiple FitChunk rounds a lowered size up to.
	fitAlign = 64 << 10
)

// FitChunk returns the chunk size of a ranged track of size bytes (0 or less when
// unknown) fetched with at most maxConns parallel requests (0 or less selects
// DefaultMaxConnections; HardMaxConnections at most) from the host of rawURL
// (docs/SITE-MEDIA-SPEC.md §2.4). chunk, ChunkSize's result, is the ceiling: it is
// lowered to about two chunks per connection the job may use for that host (k, the
// smaller of maxConns and the host rule's HostMaxConnections), ceil(size/(2k)) rounded
// up to 64 KiB but at least 2 MiB, so that a short track keeps every connection busy
// instead of one or two large chunks. A track of unknown size, or one the host allows a
// single connection, keeps chunk. The result lies within media.MinChunkBytes..chunk for
// any valid chunk. rules may be nil.
func FitChunk(rules HostRules, rawURL string, chunk, size int64, maxConns int) int64 {
	k := maxConns
	if k <= 0 {
		k = DefaultMaxConnections
	}
	k = min(k, HardMaxConnections)
	o := Options{HostRules: rules}
	if h := o.hostConnections(hostOf(rawURL)); h > 0 {
		k = min(k, h)
	}
	if size <= 0 || k <= 1 {
		return chunk
	}
	per := int64(2 * k)
	c := size / per
	if size%per != 0 {
		c++
	}
	c = (c + fitAlign - 1) / fitAlign * fitAlign
	return min(chunk, max(c, minFitChunk))
}

// hostGate bounds the parallel requests of a Run per host (HostRules'
// HostMaxConnections): a semaphore per capped host, sized at its first request.
type hostGate struct {
	o    *Options
	mu   sync.Mutex
	sems map[string]chan struct{} // nil value: the host has no cap
}

func newHostGate(o *Options) *hostGate {
	return &hostGate{o: o, sems: make(map[string]chan struct{})}
}

// acquire waits until a request to host may start and returns its release.
func (g *hostGate) acquire(ctx context.Context, host string) (release func(), err error) {
	g.mu.Lock()
	sem, ok := g.sems[host]
	if !ok {
		if n := g.o.hostConnections(host); n > 0 {
			sem = make(chan struct{}, n)
		}
		g.sems[host] = sem
	}
	g.mu.Unlock()
	if sem == nil {
		return func() {}, nil
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// hostBudget is the number of requests the given hosts allow at once: the sum of
// their caps, or limit when a host has none (or the sum reaches it).
func (o *Options) hostBudget(hosts map[string]bool, limit int) int {
	if o.HostRules == nil {
		return limit
	}
	n := 0
	for h := range hosts {
		c := o.hostConnections(h)
		if c == 0 {
			return limit
		}
		n += c
		if n >= limit {
			return limit
		}
	}
	return max(n, 1)
}
