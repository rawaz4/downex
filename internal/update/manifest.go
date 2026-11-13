package update

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Manifest describes one release (the JSON document at the feed URL). Unknown fields are
// ignored, so that later releases can add some.
type Manifest struct {
	Version     string    `json:"version"`               // semantic version of the release
	PublishedAt time.Time `json:"published_at,omitzero"` // RFC 3339; optional
	Notes       string    `json:"notes,omitempty"`       // release notes, plain text
	Assets      []Asset   `json:"assets"`
}

// Asset is the package of a release for one system.
type Asset struct {
	OS     string `json:"os"`     // GOOS: "windows", "darwin" or "linux"
	Arch   string `json:"arch"`   // GOARCH ("amd64", "arm64", "386"), or "universal" (darwin)
	URL    string `json:"url"`    // https
	SHA256 string `json:"sha256"` // hex, 64 digits
	Size   int64  `json:"size"`   // bytes, 1..MaxAssetSize
}

// Limits of a manifest's fields.
const (
	maxURLLength   = 4096
	maxNotesLength = 64 << 10
	maxAssets      = 64
)

var (
	knownOS   = map[string]bool{"windows": true, "darwin": true, "linux": true}
	knownArch = map[string]bool{"amd64": true, "arm64": true, "386": true, "universal": true}
)

// ParseManifest parses and validates a manifest whose signature has been verified: a
// semantic version, notes of at most 64 KiB, and up to 64 assets, each for a known
// system (one per os/arch), with an https URL, a 64-digit hex SHA-256 (returned in lower
// case) and a size of 1..MaxAssetSize bytes. Errors wrap ErrManifest.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return Manifest{}, fmt.Errorf("%w: version: %v", ErrManifest, err)
	}
	if len(m.Notes) > maxNotesLength {
		return Manifest{}, fmt.Errorf("%w: notes are longer than %d bytes", ErrManifest, maxNotesLength)
	}
	if len(m.Assets) > maxAssets {
		return Manifest{}, fmt.Errorf("%w: more than %d assets", ErrManifest, maxAssets)
	}
	seen := make(map[string]bool, len(m.Assets))
	for i := range m.Assets {
		a := &m.Assets[i]
		bad := func(format string, args ...any) (Manifest, error) {
			return Manifest{}, fmt.Errorf("%w: asset %d: %s", ErrManifest, i+1, fmt.Sprintf(format, args...))
		}
		switch {
		case !knownOS[a.OS]:
			return bad("unknown os %q", truncate(a.OS, 32))
		case !knownArch[a.Arch]:
			return bad("unknown arch %q", truncate(a.Arch, 32))
		case a.Arch == "universal" && a.OS != "darwin":
			return bad("arch universal is for darwin only")
		case seen[a.OS+"/"+a.Arch]:
			return bad("a second asset for %s/%s", a.OS, a.Arch)
		case a.Size <= 0 || a.Size > MaxAssetSize:
			return bad("size %d is not between 1 and %d", a.Size, int64(MaxAssetSize))
		}
		seen[a.OS+"/"+a.Arch] = true
		if _, err := checkHTTPS(a.URL); err != nil {
			return bad("url: %v", err)
		}
		sum, err := hex.DecodeString(a.SHA256)
		if err != nil || len(sum) != 32 {
			return bad("sha256 is not 64 hex digits")
		}
		a.SHA256 = hex.EncodeToString(sum)
	}
	return m, nil
}

// AssetFor returns the package for goos/goarch: on darwin the "universal" one, else the
// one for goarch; elsewhere the one for goarch.
func (m Manifest) AssetFor(goos, goarch string) (Asset, bool) {
	if goos == "darwin" {
		for _, a := range m.Assets {
			if a.OS == goos && a.Arch == "universal" {
				return a, true
			}
		}
	}
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Asset{}, false
}

// CheckFeedURL validates an update feed URL (Settings.update_feed_url): an absolute https
// URL with a host, without user information, fragment, white space or control
// characters, of at most 4096 bytes. Errors wrap ErrInsecureURL or describe the problem.
func CheckFeedURL(s string) (*url.URL, error) {
	return checkHTTPS(s)
}

// checkHTTPS is CheckFeedURL for any URL of the update process.
func checkHTTPS(s string) (*url.URL, error) {
	if len(s) > maxURLLength {
		return nil, fmt.Errorf("the URL is longer than %d bytes", maxURLLength)
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, fmt.Errorf("the URL contains white space or control characters")
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("not a valid URL")
	}
	switch {
	case u.Scheme != "https":
		return nil, ErrInsecureURL
	case u.Host == "" || u.Hostname() == "":
		return nil, fmt.Errorf("the URL has no host")
	case u.User != nil:
		return nil, fmt.Errorf("the URL must not contain user information")
	case u.Fragment != "" || strings.Contains(s, "#"):
		return nil, fmt.Errorf("the URL must not contain a fragment")
	case u.Opaque != "":
		return nil, fmt.Errorf("not a valid URL")
	}
	return u, nil
}

// SignatureURL is the URL of feed's detached signature: feed with ".sig" appended to its
// path (the query, if any, is kept).
func SignatureURL(feed *url.URL) string {
	u := *feed
	u.Path += ".sig"
	if u.RawPath != "" {
		u.RawPath += ".sig"
	}
	return u.String()
}

// Result is what a check found.
type Result struct {
	Manifest  Manifest
	Current   Version
	Latest    Version
	Newer     bool  // Latest has higher precedence than Current
	Asset     Asset // the package for this system (HasAsset)
	HasAsset  bool
	Available bool // Newer and HasAsset: an update can be installed
}

// Evaluate compares a verified manifest with the running version and picks the package
// for goos/goarch.
func Evaluate(m Manifest, current Version, goos, goarch string) (Result, error) {
	latest, err := ParseVersion(m.Version)
	if err != nil {
		return Result{}, fmt.Errorf("%w: version: %v", ErrManifest, err)
	}
	r := Result{Manifest: m, Current: current, Latest: latest, Newer: latest.Compare(current) > 0}
	r.Asset, r.HasAsset = m.AssetFor(goos, goarch)
	r.Available = r.Newer && r.HasAsset
	return r, nil
}
