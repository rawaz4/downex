package transport

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Azure/go-ntlmssp"
)

// ErrAuthRequired reports a server or proxy that demands credentials which are missing
// or were rejected. Retrying will not help until a login is added or corrected.
var ErrAuthRequired = errors.New("authentication required")

// Login is a user name and password for server or proxy authentication. Its String
// and GoString never print the password.
type Login struct {
	Username string
	Password string
	// CurrentUser answers NTLM and Negotiate challenges as the signed-in user (Windows
	// SSPI) instead of with Username and Password. Ignored where unsupported.
	CurrentUser bool
}

func (l Login) String() string   { return "Login(" + l.Username + ")" }
func (l Login) GoString() string { return l.String() }

// hasPassword reports whether l can answer password schemes (Basic, Digest, NTLM).
func (l Login) hasPassword() bool { return l.Username != "" || l.Password != "" }

// Credentials supplies logins for authentication challenges. Implementations must be
// safe for concurrent use.
type Credentials interface {
	// ServerLogin returns the login for u, a URL whose server answered 401. It is only
	// asked for the URL that challenged; the transport decides whether the login may be
	// sent there (never to another origin reached over plain HTTP after a redirect).
	ServerLogin(u *url.URL) (Login, bool)
	// ProxyLogin returns the login for proxy p after it answered 407 (or before a SOCKS5
	// handshake). Static credentials of p take precedence.
	ProxyLogin(p Proxy) (Login, bool)
}

// maxAuthRounds bounds the extra requests one exchange may send for authentication
// (NTLM needs two; a stale Digest nonce one; a proxy and a server both challenging).
const maxAuthRounds = 6

// handshake answers the challenges of one scheme during one exchange.
type handshake interface {
	// next returns the credentials field value for the challenge ch of this scheme.
	// An error means the scheme cannot continue (credentials were rejected).
	next(ch challenge) (string, error)
	// connBased reports schemes that authenticate the connection (NTLM, Negotiate).
	connBased() bool
	close()
}

// authRequest is what a handshake needs to know about the request it authenticates.
type authRequest struct {
	method string
	uri    string // request-target: path?query, an absolute URI (forward proxy) or host:port (CONNECT)
	body   []byte // the request body (Digest qop=auth-int)
	host   string // server or proxy host name (Kerberos SPN)
}

// schemeOrder is the preference among offered schemes, strongest first.
var schemeOrder = []string{"negotiate", "ntlm", "digest", "basic"}

// authFlow answers the challenges of one server or proxy during one exchange.
type authFlow struct {
	field  string // "Www-Authenticate" or "Proxy-Authenticate"
	req    authRequest
	login  func() (Login, bool) // asked once, at the first challenge
	cache  *digestCache         // where a successful Digest challenge is kept (may be nil)
	origin string               // server flows: the origin (originString) that challenged

	asked    bool
	l        Login
	hasLogin bool
	hs       handshake
	scheme   string
	failed   bool
	next     string // credentials value prepared for the following request
}

// respond answers the challenges in values and, on success, stores the credentials
// value for the next request (read with pending). It returns the value and true, or
// false when it cannot answer (no usable login, or the credentials were rejected).
func (a *authFlow) respond(values []string) (string, bool) {
	a.next = ""
	if a.failed {
		return "", false
	}
	chs := parseChallenges(values)
	if a.hs != nil {
		if ch, ok := findChallenge(chs, a.scheme); ok {
			if v, err := a.hs.next(ch); err == nil {
				a.next = v
				return v, true
			}
		}
		// Rejected: never fall back to a weaker scheme with the same login.
		a.hs.close()
		a.hs, a.failed = nil, true
		return "", false
	}
	if !a.asked {
		a.asked = true
		a.l, a.hasLogin = a.login()
	}
	if !a.hasLogin {
		a.failed = true
		return "", false
	}
	for _, scheme := range schemeOrder {
		ch, ok := findChallenge(chs, scheme)
		if !ok {
			continue
		}
		hs := a.newHandshake(scheme)
		if hs == nil {
			continue
		}
		v, err := hs.next(ch)
		if err != nil {
			hs.close()
			continue
		}
		a.hs, a.scheme, a.next = hs, scheme, v
		return v, true
	}
	a.failed = true
	return "", false
}

// done is called once the exchange got a response that is not a challenge.
func (a *authFlow) done(success bool) {
	if a.hs == nil {
		return
	}
	if success && a.cache != nil {
		if d, ok := a.hs.(*digestHandshake); ok && d.last != nil {
			a.cache.store(d.last)
		}
	}
	a.hs.close()
	a.hs = nil
}

// connBased reports whether the current handshake authenticates the connection.
func (a *authFlow) connBased() bool { return a.hs != nil && a.hs.connBased() }

func (a *authFlow) newHandshake(scheme string) handshake {
	l := a.l
	switch scheme {
	case "negotiate", "ntlm":
		if l.CurrentUser {
			if hs, err := newCurrentUserHandshake(scheme, a.req.host); err == nil {
				return hs
			}
		}
		if l.Username == "" {
			return nil
		}
		return &ntlmHandshake{scheme: scheme, login: l}
	case "digest":
		if !l.hasPassword() {
			return nil
		}
		return &digestHandshake{login: l, req: a.req}
	case "basic":
		if !l.hasPassword() {
			return nil
		}
		return &basicHandshake{login: l}
	}
	return nil
}

// basicHandshake implements RFC 7617.
type basicHandshake struct {
	login Login
	sent  bool
}

func basicValue(l Login) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(l.Username+":"+l.Password))
}

func (b *basicHandshake) next(challenge) (string, error) {
	if b.sent {
		return "", errors.New("basic credentials rejected")
	}
	b.sent = true
	return basicValue(b.login), nil
}
func (b *basicHandshake) connBased() bool { return false }
func (b *basicHandshake) close()          {}

// ntlmHandshake answers NTLM (or NTLM inside Negotiate) with a user name and password
// (MS-NLMP, NTLMv2 via go-ntlmssp).
type ntlmHandshake struct {
	scheme string // "ntlm" or "negotiate"
	login  Login
	leg    int
}

func (n *ntlmHandshake) name() string {
	if n.scheme == "negotiate" {
		return "Negotiate"
	}
	return "NTLM"
}

func (n *ntlmHandshake) next(ch challenge) (string, error) {
	n.leg++
	switch {
	case n.leg == 1 && ch.token == "":
		_, domain, _ := ntlmssp.GetDomain(n.login.Username)
		msg, err := ntlmssp.NewNegotiateMessage(domain, "")
		if err != nil {
			return "", err
		}
		return n.name() + " " + base64.StdEncoding.EncodeToString(msg), nil
	case n.leg == 2 && ch.token != "":
		chal, err := base64.StdEncoding.DecodeString(ch.token)
		if err != nil {
			return "", fmt.Errorf("ntlm: malformed challenge")
		}
		user, _, domainNeeded := ntlmssp.GetDomain(n.login.Username)
		if !domainNeeded {
			user = n.login.Username
		}
		msg, err := ntlmssp.ProcessChallenge(chal, n.login.Username, n.login.Password, domainNeeded)
		_ = user
		if err != nil {
			return "", fmt.Errorf("ntlm: %w", err)
		}
		return n.name() + " " + base64.StdEncoding.EncodeToString(msg), nil
	}
	return "", errors.New("ntlm credentials rejected")
}
func (n *ntlmHandshake) connBased() bool { return true }
func (n *ntlmHandshake) close()          {}

// digestChallenge is the state of a Digest protection space (RFC 7616).
type digestChallenge struct {
	realm, nonce, opaque, algorithm, qop string
	userhash                             bool
	nc                                   atomic.Uint32
}

// digestHandshake implements RFC 7616 (MD5, SHA-256 and SHA-512-256, with and without
// -sess; qop auth and auth-int; nonce counts; one retry on a stale nonce).
type digestHandshake struct {
	login Login
	req   authRequest
	tries int
	last  *digestChallenge
}

func (d *digestHandshake) next(ch challenge) (string, error) {
	d.tries++
	switch {
	case d.tries == 1:
	case d.tries == 2 && strings.EqualFold(ch.param("stale"), "true"):
	default:
		return "", errors.New("digest credentials rejected")
	}
	cur := &digestChallenge{
		realm:     ch.param("realm"),
		nonce:     ch.param("nonce"),
		opaque:    ch.param("opaque"),
		algorithm: ch.param("algorithm"),
		userhash:  strings.EqualFold(ch.param("userhash"), "true"),
	}
	if cur.nonce == "" {
		return "", errors.New("digest: challenge without a nonce")
	}
	newHash, sess, err := digestHashFor(cur.algorithm)
	if err != nil {
		return "", err
	}
	qop, err := pickQOP(ch.param("qop"))
	if err != nil {
		return "", err
	}
	cur.qop = qop
	d.last = cur
	v, err := d.answer(cur, newHash, sess, qop)
	if err != nil {
		return "", err
	}
	return v, nil
}

func (d *digestHandshake) connBased() bool { return false }
func (d *digestHandshake) close()          {}

// answer builds the Authorization/Proxy-Authorization value for cur.
func (d *digestHandshake) answer(cur *digestChallenge, newHash func() hash.Hash, sess bool, qop string) (string, error) {
	user := d.login.Username
	h := func(parts ...string) string {
		hsh := newHash()
		for i, p := range parts {
			if i > 0 {
				hsh.Write([]byte{':'})
			}
			hsh.Write([]byte(p))
		}
		return hex.EncodeToString(hsh.Sum(nil))
	}
	ha1 := h(user, cur.realm, d.login.Password)
	cnonce := ""
	if sess || qop != "" {
		cnonce = randomCnonce()
	}
	if sess {
		ha1 = h(ha1, cur.nonce, cnonce)
	}
	var ha2 string
	switch qop {
	case "auth-int":
		ha2 = h(d.req.method, d.req.uri, h2body(newHash, d.req.body))
	default:
		ha2 = h(d.req.method, d.req.uri)
	}
	ncValue := ""
	var response string
	if qop == "" {
		response = h(ha1, cur.nonce, ha2)
	} else {
		ncValue = fmt.Sprintf("%08x", cur.nc.Add(1))
		response = h(ha1, cur.nonce, ncValue, cnonce, qop, ha2)
	}
	username := user
	userhash := false
	if cur.userhash {
		username = h(user, cur.realm)
		userhash = true
	}
	var b strings.Builder
	b.WriteString("Digest ")
	writeDigestParam(&b, "username", username, true, false)
	writeDigestParam(&b, "realm", cur.realm, true, true)
	writeDigestParam(&b, "nonce", cur.nonce, true, true)
	writeDigestParam(&b, "uri", d.req.uri, true, true)
	if qop != "" {
		writeDigestParam(&b, "qop", qop, false, true)
		writeDigestParam(&b, "nc", ncValue, false, true)
		writeDigestParam(&b, "cnonce", cnonce, true, true)
	}
	writeDigestParam(&b, "response", response, true, true)
	if cur.algorithm != "" {
		writeDigestParam(&b, "algorithm", cur.algorithm, false, true)
	}
	if cur.opaque != "" {
		writeDigestParam(&b, "opaque", cur.opaque, true, true)
	}
	if userhash {
		writeDigestParam(&b, "userhash", "true", false, true)
	}
	return b.String(), nil
}

func writeDigestParam(b *strings.Builder, name, value string, quoted, comma bool) {
	if comma {
		b.WriteString(", ")
	}
	b.WriteString(name)
	b.WriteByte('=')
	if quoted {
		b.WriteString(quoteParam(value))
	} else {
		b.WriteString(value)
	}
}

// digestHashFor returns the hash constructor for a Digest algorithm (RFC 7616 §6.1)
// and whether it is a -sess variant. An empty or MD5 algorithm selects MD5.
func digestHashFor(algorithm string) (func() hash.Hash, bool, error) {
	a := strings.ToLower(strings.TrimSpace(algorithm))
	sess := strings.HasSuffix(a, "-sess")
	base := strings.TrimSuffix(a, "-sess")
	switch base {
	case "", "md5":
		return func() hash.Hash { return md5.New() }, sess, nil
	case "sha-256":
		return sha256.New, sess, nil
	case "sha-512-256":
		return sha512.New512_256, sess, nil
	}
	return nil, false, fmt.Errorf("digest: unsupported algorithm %q", algorithm)
}

// pickQOP selects the strongest offered quality of protection (auth is preferred over
// auth-int because auth-int needs the whole body); "" means legacy RFC 2069 (no qop).
func pickQOP(offered string) (string, error) {
	if strings.TrimSpace(offered) == "" {
		return "", nil
	}
	var auth, authInt bool
	for _, tok := range strings.Split(offered, ",") {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "auth":
			auth = true
		case "auth-int":
			authInt = true
		}
	}
	switch {
	case auth:
		return "auth", nil
	case authInt:
		return "auth-int", nil
	}
	return "", errors.New("digest: no supported qop offered")
}

func h2body(newHash func() hash.Hash, body []byte) string {
	hsh := newHash()
	hsh.Write(body)
	return hex.EncodeToString(hsh.Sum(nil))
}

func randomCnonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// digestCache remembers the last accepted Digest challenge of a protection space so
// that the next request authenticates preemptively instead of paying a 401 round trip.
type digestCache struct {
	mu   sync.Mutex
	last *digestChallenge
}

func (c *digestCache) store(ch *digestChallenge) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.last = ch
	c.mu.Unlock()
}
