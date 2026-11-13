package engine

import (
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/limiter"
	"downex/internal/testserver"
)

// capRules is a HostRules with RequestCaps: caps[host] limits the requests to host.
type capRules struct {
	conns int
	caps  map[string]int64

	mu   sync.Mutex
	seen map[string]bool // hosts asked for a cap
}

func (r *capRules) HostMaxConnections(string) int      { return r.conns }
func (r *capRules) HostLimiter(string) limiter.Limiter { return nil }

func (r *capRules) HostMaxRequestBytes(host string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	r.seen[host] = true
	return r.caps[host]
}

// connRules is a HostRules without RequestCaps.
type connRules struct{}

func (connRules) HostMaxConnections(string) int      { return 0 }
func (connRules) HostLimiter(string) limiter.Limiter { return nil }

// parseRangeHeader parses "bytes=a-b" (b -1 for "bytes=a-").
func parseRangeHeader(t *testing.T, v string) (int64, int64) {
	t.Helper()
	spec, ok := strings.CutPrefix(v, "bytes=")
	first, last, ok2 := strings.Cut(spec, "-")
	a, err := strconv.ParseInt(first, 10, 64)
	if !ok || !ok2 || err != nil {
		t.Fatalf("unexpected Range header %q", v)
	}
	if last == "" {
		return a, -1
	}
	b, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		t.Fatalf("unexpected Range header %q", v)
	}
	return a, b
}

func serverHost(t *testing.T, h *harness) string {
	t.Helper()
	u, err := url.Parse(h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(u.Host)
}

func TestDownloadUnderRequestCap(t *testing.T) {
	const capBytes = 64 << 10
	rules := &capRules{caps: map[string]int64{}}
	// Paced responses, so that connections ramp up while the download runs: one
	// connection would need about a second, beyond the ramp-up's worth-it guard (§3).
	h := newHarness(t, testserver.Options{Size: 9*capBytes + 1234, Seed: 7, Throttle: 512 << 10},
		Options{HostRules: rules, RampInterval: 5 * time.Millisecond})
	host := serverHost(t, h)
	rules.caps[host] = capBytes
	d := h.add("/file.bin", "file.bin", 4)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)

	reqs := h.srv.Requests()
	if len(reqs) < 10 {
		t.Fatalf("%d requests for %d bytes under a %d-byte cap", len(reqs), h.srv.Options().Size, capBytes)
	}
	if reqs[0].Range != "bytes=0-"+strconv.Itoa(capBytes-1) {
		t.Errorf("probe asked for %q, want bytes=0-%d", reqs[0].Range, capBytes-1)
	}
	covered := int64(0)
	for _, r := range reqs {
		a, b := parseRangeHeader(t, r.Range)
		if b < 0 || b-a+1 > capBytes {
			t.Errorf("request %q exceeds the %d-byte cap", r.Range, capBytes)
		}
		covered += b - a + 1
	}
	if covered < h.srv.Options().Size {
		t.Errorf("requests cover %d bytes of %d", covered, h.srv.Options().Size)
	}
	if peak := h.srv.MaxConcurrent(); peak < 2 || peak > 4 {
		t.Errorf("peak concurrent requests = %d, want 2..4: the cap must not serialise the download", peak)
	}
	rules.mu.Lock()
	defer rules.mu.Unlock()
	if !rules.seen[host] {
		t.Errorf("the cap was never asked for %q (asked: %v)", host, rules.seen)
	}
}

func TestRequestCapOtherHostOrNone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules HostRules
	}{
		{"other host", &capRules{caps: map[string]int64{"elsewhere.example": 1 << 10}}},
		{"no RequestCaps", connRules{}},
		{"no rules", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testserver.Options{Size: 300 << 10, Seed: 3}, Options{HostRules: tc.rules})
			d := h.add("/f", "f.bin", 2)
			if err := h.run(d.ID, nil, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			h.verify(d.ID)
			if got := h.srv.Requests()[0].Range; got != "bytes=0-" {
				t.Errorf("probe asked for %q, want bytes=0-", got)
			}
		})
	}
}

func TestRequestCapWithoutRanges(t *testing.T) {
	// A server that ignores Range answers the capped probe with the whole body: single
	// mode, one stream, no further requests.
	rules := &capRules{caps: map[string]int64{}}
	h := newHarness(t, testserver.Options{Size: 200 << 10, Seed: 5, NoRanges: true}, Options{HostRules: rules})
	rules.caps[serverHost(t, h)] = 16 << 10
	d := h.add("/whole", "whole.bin", 4)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	if n := len(h.srv.Requests()); n != 1 {
		t.Errorf("%d requests to a server without ranges, want 1", n)
	}
}

func TestCappedEnd(t *testing.T) {
	for _, tc := range []struct{ pos, end, limit, want int64 }{
		{0, -1, 0, -1},
		{0, -1, 100, 99},
		{50, 1000, 100, 149},
		{50, 120, 100, 120},
		{50, 149, 100, 149},
		{1<<63 - 10, -1, 100, -1}, // no overflow
	} {
		if got := cappedEnd(tc.pos, tc.end, tc.limit); got != tc.want {
			t.Errorf("cappedEnd(%d, %d, %d) = %d, want %d", tc.pos, tc.end, tc.limit, got, tc.want)
		}
	}
	rules := &capRules{caps: map[string]int64{"a.example": 300, "b.example": 200, "c.example": 0}}
	for _, tc := range []struct {
		host, url string
		want      int64
	}{
		{"", "https://a.example/x", 300},
		{"a.example", "https://b.example/x", 200}, // redirected: the smaller cap
		{"b.example", "https://a.example/x", 200},
		{"c.example", "https://a.example/x", 300}, // 0 is no cap
		{"a.example", "https://d.example/x", 300},
		{"", "https://d.example/x", 0},
	} {
		if got := requestCap(rules, tc.host, tc.url); got != tc.want {
			t.Errorf("requestCap(%q, %q) = %d, want %d", tc.host, tc.url, got, tc.want)
		}
	}
	if got := requestCap(nil, "a.example", "https://a.example/"); got != 0 {
		t.Errorf("requestCap without caps = %d", got)
	}
}
