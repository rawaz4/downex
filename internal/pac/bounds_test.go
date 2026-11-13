package pac

import (
	"errors"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestScriptSeesOnlyTheOriginOfEncryptedTargets: for https and wss targets the script
// gets scheme://host[:port]/ only (path and query, where signed links carry tokens, are
// encrypted on the wire and a hostile script could send them out through DNS); http
// targets keep path and query. User information and fragments never reach the script.
func TestScriptSeesOnlyTheOriginOfEncryptedTargets(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	e := newEval(t, `function FindProxyForURL(url, host) { isResolvable(url); return "DIRECT"; }`, Options{
		CacheTTL: -1,
		LookupIP: func(h string) ([]net.IP, error) {
			mu.Lock()
			seen = append(seen, h)
			mu.Unlock()
			return nil, errors.New("not resolvable")
		},
	})
	for _, tc := range []struct{ in, want string }{
		{"https://user:pw@bank.test:8443/dl/file.zip?sig=SECRETTOKEN#frag", "https://bank.test:8443/"},
		{"HTTPS://Bank.test/dl/file.zip?sig=SECRETTOKEN", "https://Bank.test/"},
		{"wss://chat.test/socket?token=SECRETTOKEN", "wss://chat.test/"},
		{"http://user:pw@plain.test/a/b.zip?x=1#frag", "http://plain.test/a/b.zip?x=1"},
	} {
		mu.Lock()
		seen = nil
		mu.Unlock()
		if _, err := e.FindProxy(mustURL(t, tc.in)); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		mu.Lock()
		got := append([]string(nil), seen...)
		mu.Unlock()
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("FindProxy(%s): the script saw %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestShExpMatchIsBounded: shExpMatch runs as native code that the evaluation timeout
// cannot interrupt, so one call must stay short whatever the script passes: oversized
// arguments never match, and patterns that make a backtracking matcher quadratic are
// matched in linear time.
func TestShExpMatchIsBounded(t *testing.T) {
	for name, script := range map[string]string{
		"oversized arguments": `function FindProxyForURL(u, h) {
			var s = "a".repeat(300000), p = "*" + "a".repeat(30000) + "b";
			return shExpMatch(s, p) ? "PROXY yes:1" : "DIRECT";
		}`,
		"backtracking pattern within the limits": `function FindProxyForURL(u, h) {
			var s = "a".repeat(16000), p = "*" + "a".repeat(4000) + "b", n = 0;
			for (var i = 0; i < 50; i++) { if (shExpMatch(s, p)) n++; }
			return n ? "PROXY yes:1" : "DIRECT";
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEval(t, script, Options{EvalTimeout: time.Second})
			start := time.Now()
			res, err := e.FindProxy(mustURL(t, "http://x.test/"))
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("FindProxy: %v (after %v)", err, elapsed)
			}
			if len(res) != 1 || res[0].Kind != Direct {
				t.Errorf("results = %+v, want DIRECT (no match)", res)
			}
			if elapsed > 900*time.Millisecond {
				t.Errorf("shExpMatch took %v", elapsed)
			}
		})
	}
}

// globReference is a plain dynamic-programming shell-expression matcher ('*' any run,
// '?' one byte) used to check shGlob.
func globReference(pattern, s string) bool {
	// m[j] reports whether pattern[:i] matches s[:j].
	m := make([]bool, len(s)+1)
	m[0] = true
	for i := 0; i < len(pattern); i++ {
		next := make([]bool, len(s)+1)
		switch c := pattern[i]; c {
		case '*':
			any := false
			for j := 0; j <= len(s); j++ {
				any = any || m[j]
				next[j] = any
			}
		default:
			for j := 1; j <= len(s); j++ {
				next[j] = m[j-1] && (c == '?' || s[j-1] == c)
			}
		}
		m = next
	}
	return m[len(s)]
}

func TestShGlob(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"", "", true},
		{"", "a", false},
		{"*", "", true},
		{"*", "anything", true},
		{"**", "", true},
		{"?", "", false},
		{"?", "a", true},
		{"a*b", "ab", true},
		{"a*b", "a", false},
		{"a*a", "a", false},
		{"*.example.com", "www.example.com", true},
		{"*.example.com", "example.com", false},
		{"http://*/*.zip", "http://h.test/a/b.zip", true},
		{"*a?c*", "xxabcxx", true},
		{"*a?c*", "xxacxx", false},
		{"a*b*c", "abbbbc", true},
		{"a*b*c", "acb", false},
		{"*ab*ab", "abab", true},
		{"*ab*ab", "aab", false},
	} {
		if got := shGlob(tc.pattern, tc.s); got != tc.want {
			t.Errorf("shGlob(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	gen := func(alphabet string, n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteByte(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for i := 0; i < 20000; i++ {
		p, s := gen("ab*?", rng.IntN(8)), gen("ab", rng.IntN(10))
		if got, want := shGlob(p, s), globReference(p, s); got != want {
			t.Fatalf("shGlob(%q, %q) = %v, want %v", p, s, got, want)
		}
	}
}
