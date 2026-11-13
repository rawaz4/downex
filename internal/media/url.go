package media

import (
	"net/url"
	"strings"
)

// urlBudget charges every URL string a parse or plan allocates against
// MaxURLBytesTotal.
type urlBudget struct {
	used int
}

func (b *urlBudget) charge(n int) error {
	if n > MaxURLBytesTotal-b.used {
		return errf(ErrTooLarge, "URLs exceed %d bytes in total", MaxURLBytesTotal)
	}
	b.used += n
	return nil
}

// checkBase validates a caller-supplied base URL.
func checkBase(base *url.URL) error {
	if base != nil && len(base.String()) > MaxURIBytes {
		return errf(ErrTooLarge, "base URL is longer than %d bytes", MaxURIBytes)
	}
	return nil
}

// resolveRef parses raw and resolves it against base (nil leaves relative references
// relative, and so does a base that is itself a relative-path reference). The result
// must use http or https, or the scheme of base: a remote manifest cannot point the
// downloader at local files or other schemes.
func resolveRef(base *url.URL, raw string) (*url.URL, error) {
	if len(raw) > MaxURIBytes {
		return nil, errf(ErrTooLarge, "URI is longer than %d bytes", MaxURIBytes)
	}
	if raw == "" {
		return nil, errf(ErrMalformed, "URI is empty")
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, errf(ErrMalformed, "invalid URI")
	}
	u := ref
	switch {
	case base == nil:
	case base.Scheme == "" && base.Host == "" && base.User == nil && !strings.HasPrefix(base.EscapedPath(), "/"):
		u = resolveRelative(base, ref)
	default:
		u = base.ResolveReference(ref)
	}
	switch {
	case u.Scheme == "", u.Scheme == "http", u.Scheme == "https":
	case base != nil && u.Scheme == base.Scheme:
	case len(u.Scheme) > 32:
		return nil, errf(ErrUnsupported, "URI scheme is not allowed")
	default:
		return nil, errf(ErrUnsupported, "URI scheme %q is not allowed", u.Scheme)
	}
	return u, nil
}

// resolveRelative resolves ref against a base that is a relative-path reference, as
// nested BaseURLs of a manifest parsed without a base URL are. It follows RFC 3986
// §5.2.2 with the merge of §5.2.3 and the dot-segment removal of §5.2.4, except that
// ".." segments climbing above the base are kept, so the result is still a relative
// reference to resolve later against the manifest's location. (url.URL.ResolveReference
// would root it at "/" and drop those segments.)
func resolveRelative(base, ref *url.URL) *url.URL {
	if ref.Scheme != "" || ref.Host != "" || ref.User != nil || strings.HasPrefix(ref.EscapedPath(), "/") {
		return ref
	}
	u := *ref
	rp := ref.EscapedPath()
	if rp == "" {
		u.Path, u.RawPath = base.Path, base.RawPath
		if ref.RawQuery == "" && !ref.ForceQuery {
			u.RawQuery, u.ForceQuery = base.RawQuery, base.ForceQuery
		}
		return &u
	}
	bp := base.EscapedPath()
	p := removeDotSegments(bp[:strings.LastIndexByte(bp, '/')+1] + rp)
	if unescaped, err := url.PathUnescape(p); err == nil {
		u.Path, u.RawPath = unescaped, p
	} else {
		u.Path, u.RawPath = p, "" // not reached: p joins two escaped paths
	}
	return &u
}

// removeDotSegments removes "." and ".." segments from a relative path (RFC 3986
// §5.2.4), keeping the ".." segments that have nothing left to remove. A path that
// ends in a dot segment names a directory and keeps its final "/".
func removeDotSegments(p string) string {
	segs := strings.Split(p, "/")
	out := segs[:0]
	for i, s := range segs {
		last := i == len(segs)-1
		switch s {
		case ".":
		case "..":
			if n := len(out); n > 0 && out[n-1] != ".." {
				out = out[:n-1]
			} else {
				out = append(out, s)
			}
		default:
			out = append(out, s)
			continue
		}
		if last {
			out = append(out, "")
		}
	}
	if len(out) == 0 || len(out) == 1 && out[0] == "" {
		return "./"
	}
	j := strings.Join(out, "/")
	if j[0] == '/' {
		// An empty first segment ("a/..//b"): the path must not become rooted.
		j = "./" + j
	}
	return j
}

// urlString formats u, enforcing MaxURIBytes and charging the budget.
func (b *urlBudget) urlString(u *url.URL) (string, error) {
	s := u.String()
	if s == "" {
		return "", errf(ErrMalformed, "URI is empty")
	}
	if len(s) > MaxURIBytes {
		return "", errf(ErrTooLarge, "resolved URI is longer than %d bytes", MaxURIBytes)
	}
	if err := b.charge(len(s)); err != nil {
		return "", err
	}
	return s, nil
}

// resolver resolves URI references against one base, reusing the previous result for a
// repeated reference (byte-range playlists name one resource many times).
type resolver struct {
	base    *url.URL
	budget  *urlBudget
	lastRaw string
	lastOut string
	hasLast bool
}

func (r *resolver) resolve(raw string) (string, error) {
	if r.hasLast && raw == r.lastRaw {
		return r.lastOut, nil
	}
	u, err := resolveRef(r.base, raw)
	if err != nil {
		return "", err
	}
	s, err := r.budget.urlString(u)
	if err != nil {
		return "", err
	}
	r.lastRaw, r.lastOut, r.hasLast = raw, s, true
	return s, nil
}
