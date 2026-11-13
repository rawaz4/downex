package transport

import "testing"

func TestParseChallenges(t *testing.T) {
	chs := parseChallenges([]string{
		`Digest realm="test", qop="auth,auth-int", nonce="abc", algorithm=SHA-256`,
		`Basic realm="simple"`,
		`Negotiate`,
		`NTLM TlRMTVNTUAABAAAA`,
	})
	if len(chs) != 4 {
		t.Fatalf("got %d challenges, want 4: %+v", len(chs), chs)
	}
	d, ok := findChallenge(chs, "digest")
	if !ok || d.param("realm") != "test" || d.param("nonce") != "abc" || d.param("algorithm") != "SHA-256" || d.param("qop") != "auth,auth-int" {
		t.Fatalf("digest parse wrong: %+v", d)
	}
	if b, ok := findChallenge(chs, "basic"); !ok || b.param("realm") != "simple" {
		t.Fatalf("basic parse wrong: %+v", b)
	}
	if n, ok := findChallenge(chs, "negotiate"); !ok || n.token != "" {
		t.Fatalf("negotiate parse wrong: %+v", n)
	}
	if n, ok := findChallenge(chs, "ntlm"); !ok || n.token != "TlRMTVNTUAABAAAA" {
		t.Fatalf("ntlm token parse wrong: %+v", n)
	}
}

func TestParseChallengesCommaSeparatedSchemes(t *testing.T) {
	// Multiple schemes in a single header line.
	chs := parseChallenges([]string{`Negotiate, NTLM, Basic realm="x"`})
	for _, want := range []string{"negotiate", "ntlm", "basic"} {
		if _, ok := findChallenge(chs, want); !ok {
			t.Errorf("missing scheme %q in %+v", want, chs)
		}
	}
}

func TestParseChallengesQuotedEscape(t *testing.T) {
	chs := parseChallenges([]string{`Digest realm="a\"b", domain="/path"`})
	d, ok := findChallenge(chs, "digest")
	if !ok || d.param("realm") != `a"b` {
		t.Fatalf("escaped quote not handled: %+v", d)
	}
}
