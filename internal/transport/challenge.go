package transport

import (
	"strings"
)

// challenge is one authentication challenge of a WWW-Authenticate or
// Proxy-Authenticate field (RFC 9110 §11.6.1).
type challenge struct {
	scheme string            // lower-case, e.g. "basic", "digest", "ntlm", "negotiate"
	token  string            // token68 (NTLM/Negotiate legs), "" if none
	params map[string]string // auth-params with lower-case names, unquoted values
}

func (c challenge) param(name string) string { return c.params[name] }

// maxChallenges bounds the challenges taken from one response.
const maxChallenges = 16

// parseChallenges parses every challenge in the given field values. Malformed parts
// end the parsing of their field value; what was parsed before is kept.
func parseChallenges(values []string) []challenge {
	var out []challenge
	for _, v := range values {
		p := &chParser{s: v}
		for len(out) < maxChallenges {
			c, ok := p.next()
			if !ok {
				break
			}
			out = append(out, c)
		}
	}
	return out
}

// findChallenge returns the first challenge of scheme (lower-case).
func findChallenge(chs []challenge, scheme string) (challenge, bool) {
	for _, c := range chs {
		if c.scheme == scheme {
			return c, true
		}
	}
	return challenge{}, false
}

type chParser struct {
	s string
	i int
}

func (p *chParser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *chParser) skipSeparators() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == ',') {
		p.i++
	}
}

func (p *chParser) token() string {
	start := p.i
	for p.i < len(p.s) && isTokenChar(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i]
}

// next parses the next challenge: auth-scheme [ 1*SP ( token68 / #auth-param ) ].
func (p *chParser) next() (challenge, bool) {
	p.skipSeparators()
	scheme := p.token()
	if scheme == "" {
		return challenge{}, false
	}
	c := challenge{scheme: strings.ToLower(scheme), params: map[string]string{}}
	p.skipSpace()
	// token68: base64-ish characters ending with optional '=' padding, then end or comma.
	if t, ok := p.token68(); ok {
		c.token = t
		return c, true
	}
	for {
		save := p.i
		p.skipSpace()
		name := p.token()
		if name == "" {
			p.i = save
			return c, true
		}
		p.skipSpace()
		if p.i >= len(p.s) || p.s[p.i] != '=' {
			// A token not followed by '=' starts the next challenge.
			p.i = save
			return c, true
		}
		p.i++
		p.skipSpace()
		var val string
		if p.i < len(p.s) && p.s[p.i] == '"' {
			v, ok := p.quoted()
			if !ok {
				return c, true
			}
			val = v
		} else {
			val = p.token()
		}
		if len(c.params) < 32 {
			c.params[strings.ToLower(name)] = val
		}
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		return c, true
	}
}

// token68 consumes a token68 when the rest of this challenge is one.
func (p *chParser) token68() (string, bool) {
	start := p.i
	j := p.i
	for j < len(p.s) && (isAlnum(p.s[j]) || strings.IndexByte("-._~+/", p.s[j]) >= 0) {
		j++
	}
	if j == start {
		return "", false
	}
	k := j
	for k < len(p.s) && p.s[k] == '=' {
		k++
	}
	// After a token68 only white space, a comma or the end may follow; a single '='
	// followed by a value is an auth-param instead.
	m := k
	for m < len(p.s) && (p.s[m] == ' ' || p.s[m] == '\t') {
		m++
	}
	if m < len(p.s) && p.s[m] != ',' {
		return "", false
	}
	if k == j+1 && j < len(p.s) {
		// "name=" followed by end or comma: treat as token68 only when it looks like
		// base64 padding of a longer token.
		if j-start < 2 {
			return "", false
		}
	}
	p.i = k
	return p.s[start:k], true
}

func (p *chParser) quoted() (string, bool) {
	var b strings.Builder
	p.i++ // opening quote
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '\\':
			if p.i+1 < len(p.s) {
				b.WriteByte(p.s[p.i+1])
				p.i += 2
				continue
			}
			return "", false
		case '"':
			p.i++
			return b.String(), true
		}
		b.WriteByte(c)
		p.i++
	}
	return "", false
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// quoteParam returns s as a quoted-string.
func quoteParam(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}
