package pac

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary double as the helper process of NewIsolated.
func TestMain(m *testing.M) {
	ServeIfHelper()
	os.Exit(m.Run())
}

func newIsolated(t *testing.T, script string, opts Options) *Isolated {
	t.Helper()
	iso, err := NewIsolated(script, opts)
	if err != nil {
		t.Fatalf("NewIsolated: %v", err)
	}
	t.Cleanup(iso.Close)
	return iso
}

func TestIsolatedEvaluates(t *testing.T) {
	iso := newIsolated(t, `function FindProxyForURL(u, h) {
		if (shExpMatch(h, "*.direct.test")) return "DIRECT";
		return "PROXY p.test:3128; SOCKS5 s.test:1080";
	}`, Options{})
	res, err := iso.FindProxy(mustURL(t, "http://x.test/a?b=c"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{{Kind: HTTP, Host: "p.test", Port: "3128"}, {Kind: SOCKS5, Host: "s.test", Port: "1080"}}
	if len(res) != len(want) || res[0] != want[0] || res[1] != want[1] {
		t.Fatalf("results = %+v, want %+v", res, want)
	}
	res, err = iso.FindProxy(mustURL(t, "https://a.direct.test/f.zip"))
	if err != nil || len(res) != 1 || res[0].Kind != Direct {
		t.Fatalf("results = %+v, %v; want DIRECT", res, err)
	}
}

func TestIsolatedReportsScriptErrors(t *testing.T) {
	for script, want := range map[string]string{
		`var x = 1;`:                    "does not define FindProxyForURL",
		`function FindProxyForURL(u, {`: "compile",
		`throw "no";`:                   "run script",
	} {
		_, err := NewIsolated(script, Options{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NewIsolated(%q) = %v, want an error mentioning %q", script, err, want)
		}
	}
	iso := newIsolated(t, `function FindProxyForURL(u, h) { throw "unwanted " + h; }`, Options{})
	if _, err := iso.FindProxy(mustURL(t, "http://x.test/")); err == nil || !strings.Contains(err.Error(), "unwanted x.test") {
		t.Errorf("FindProxy = %v, want the script's error", err)
	}
	// A script error is not a failure of the helper: the next call is evaluated again.
	if _, err := iso.FindProxy(mustURL(t, "http://y.test/")); err == nil || !strings.Contains(err.Error(), "unwanted y.test") {
		t.Errorf("second FindProxy = %v, want the script's error again", err)
	}
}

// TestIsolatedSurvivesHostileScripts: scripts whose native built-ins allocate without
// bound (in this process the Go runtime would end with a fatal "out of memory") or never
// return (the interrupt cannot stop them) only cost the helper: FindProxy fails in
// bounded time and this process goes on.
func TestIsolatedSurvivesHostileScripts(t *testing.T) {
	const timeout = 500 * time.Millisecond
	for name, body := range map[string]string{
		"huge repeat":         `return "x".repeat(Math.pow(2, 40));`,
		"huge padEnd":         `return "".padEnd(Math.pow(2, 40), "x");`,
		"huge argument list":  `return String.fromCharCode.apply(null, {length: Math.pow(2, 40)});`,
		"endless native loop": `return Array.prototype.indexOf.call({length: Math.pow(2, 53)}, 1) < 0 ? "DIRECT" : "DIRECT";`,
		"growing heap":        `var s = "x"; for (;;) { s = s + s; }`,
	} {
		t.Run(name, func(t *testing.T) {
			iso := newIsolated(t, "function FindProxyForURL(u, h) {"+body+"}", Options{EvalTimeout: timeout})
			start := time.Now()
			res, err := iso.FindProxy(mustURL(t, "http://x.test/"))
			elapsed := time.Since(start)
			if err == nil {
				t.Fatalf("FindProxy = %+v, want an error", res)
			}
			if limit := timeout + helperCallGrace + 3*time.Second; elapsed > limit {
				t.Errorf("FindProxy took %v (limit %v)", elapsed, limit)
			}
		})
	}
}

// TestIsolatedRestartsAfterFailure: after a helper failed, calls fail fast during the
// restart delay, then a new helper answers.
func TestIsolatedRestartsAfterFailure(t *testing.T) {
	iso := newIsolated(t, `function FindProxyForURL(u, h) {
		if (h == "boom.test") return "x".repeat(Math.pow(2, 40));
		return "PROXY ok.test:8080";
	}`, Options{})
	if _, err := iso.FindProxy(mustURL(t, "http://boom.test/")); err == nil {
		t.Fatal("the hostile call succeeded")
	}
	start := time.Now()
	if _, err := iso.FindProxy(mustURL(t, "http://fine.test/")); !errors.Is(err, errHelperFailed) {
		t.Fatalf("FindProxy during the restart delay = %v, want %v", err, errHelperFailed)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("a call during the restart delay took %v", elapsed)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := iso.FindProxy(mustURL(t, "http://fine.test/"))
		if err == nil {
			if len(res) != 1 || res[0].Host != "ok.test" {
				t.Fatalf("results = %+v", res)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no new helper answered: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestIsolatedClose(t *testing.T) {
	iso := newIsolated(t, `function FindProxyForURL(u, h) { return "DIRECT"; }`, Options{})
	if _, err := iso.FindProxy(mustURL(t, "http://x.test/")); err != nil {
		t.Fatal(err)
	}
	iso.mu.Lock()
	h := iso.h
	iso.mu.Unlock()
	iso.Close()
	if _, err := iso.FindProxy(mustURL(t, "http://other.test/")); !errors.Is(err, errEvaluatorClosed) {
		t.Errorf("FindProxy after Close = %v", err)
	}
	select {
	case <-h.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the helper did not end after Close")
	}
}

func TestIsolatedCachesPerHost(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	iso := newIsolated(t, `var n = 0; function FindProxyForURL(u, h) { n++; return "PROXY p" + n + ".test:1"; }`,
		Options{Now: func() time.Time { return now }})
	first, err := iso.FindProxy(mustURL(t, "http://x.test/a"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := iso.FindProxy(mustURL(t, "http://x.test/b"))
	if err != nil || again[0] != first[0] {
		t.Fatalf("cached result = %+v, %v; want %+v", again, err, first)
	}
	now = now.Add(DefaultCacheTTL + time.Second)
	later, err := iso.FindProxy(mustURL(t, "http://x.test/c"))
	if err != nil || later[0] == first[0] {
		t.Fatalf("after the TTL = %+v, %v; want a new evaluation", later, err)
	}
}

func TestValidResults(t *testing.T) {
	in := []Result{
		{Kind: Direct, Host: "ignored"},
		{Kind: HTTP, Host: "p.test", Port: "8080"},
		{Kind: HTTP, Host: "bad host", Port: "8080"},
		{Kind: SOCKS5, Host: strings.Repeat("a", 300), Port: "1080"},
		{Kind: SOCKS5, Host: "s.test", Port: "0"},
		{Kind: Kind(99), Host: "x.test", Port: "1"},
	}
	got := validResults(in)
	if len(got) != 2 || got[0] != (Result{Kind: Direct}) || got[1] != in[1] {
		t.Fatalf("validResults = %+v", got)
	}
}
