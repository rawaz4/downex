package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(priv ed25519.PrivateKey, data []byte) []byte {
	return EncodeSignature(ed25519.Sign(priv, data))
}

func TestParsePublicKey(t *testing.T) {
	pub, _ := newKey(t)
	if _, err := ParsePublicKey(""); !errors.Is(err, ErrDisabled) {
		t.Errorf("empty key: %v, want ErrDisabled", err)
	}
	if _, err := ParsePublicKey("  "); !errors.Is(err, ErrDisabled) {
		t.Errorf("blank key: %v, want ErrDisabled", err)
	}
	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := ParsePublicKey(bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("ParsePublicKey(%q) = %v, want ErrBadKey", bad, err)
		}
	}
	got, err := ParsePublicKey(base64.StdEncoding.EncodeToString(pub) + "\n")
	if err != nil || !bytes.Equal(got, pub) {
		t.Errorf("ParsePublicKey = %x, %v", got, err)
	}
	// The compiled-in key of a test binary is empty: updates are disabled.
	if _, err := ParsePublicKey(PublicKey); !errors.Is(err, ErrDisabled) {
		t.Errorf("PublicKey of the test build: %v, want ErrDisabled", err)
	}
}

func TestVerify(t *testing.T) {
	pub, priv := newKey(t)
	otherPub, otherPriv := newKey(t)
	data := []byte(`{"version":"1.2.3","assets":[]}`)
	good := sign(priv, data)
	if err := Verify(pub, data, good); err != nil {
		t.Fatalf("valid signature: %v", err)
	}
	tampered := bytes.Replace(data, []byte("1.2.3"), []byte("1.2.4"), 1)
	cases := map[string]struct {
		key  ed25519.PublicKey
		data []byte
		sig  []byte
	}{
		"tampered manifest": {pub, tampered, good},
		"trailing byte":     {pub, append(bytes.Clone(data), ' '), good},
		"other key":         {otherPub, data, good},
		"other signer":      {pub, data, sign(otherPriv, data)},
		"not base64":        {pub, data, []byte("%%%")},
		"short signature":   {pub, data, []byte(base64.StdEncoding.EncodeToString([]byte("abc")))},
		"empty signature":   {pub, data, nil},
	}
	for name, c := range cases {
		if err := Verify(c.key, c.data, c.sig); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
		}
	}
	if err := Verify(nil, data, good); !errors.Is(err, ErrBadKey) {
		t.Errorf("no key: %v, want ErrBadKey", err)
	}
	// White space around the base64 is accepted (files end with a newline).
	if err := Verify(pub, data, []byte("\r\n  "+strings.TrimSpace(string(good))+"  \n")); err != nil {
		t.Errorf("signature with white space: %v", err)
	}
}

func TestVersions(t *testing.T) {
	// Ascending precedence (semver §11 example and more).
	order := []string{
		"0.3.3", "0.3.4-alpha", "0.3.4-alpha.1", "0.3.4-alpha.beta", "0.3.4-beta", "0.3.4-beta.2",
		"0.3.4-beta.11", "0.3.4-rc.1", "0.3.4", "0.3.10", "0.4.0", "1.0.0", "1.0.1", "1.10.0", "2.0.0",
		"18446744073709551615.0.0",
	}
	for i := range order {
		for j := range order {
			a, err := ParseVersion(order[i])
			if err != nil {
				t.Fatal(err)
			}
			b, _ := ParseVersion(order[j])
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, eq := range [][2]string{{"1.2.3", "v1.2.3"}, {"1.2.3+build.5", "1.2.3"}, {"1.0.0-rc.1+x", "1.0.0-rc.1+y"}} {
		a, err1 := ParseVersion(eq[0])
		b, err2 := ParseVersion(eq[1])
		if err1 != nil || err2 != nil || a.Compare(b) != 0 {
			t.Errorf("%s vs %s: %v %v %d, want equal", eq[0], eq[1], err1, err2, a.Compare(b))
		}
	}
	v, err := ParseVersion("v1.2.3-rc.1+exp.sha.5114f85")
	if err != nil || v.String() != "1.2.3-rc.1+exp.sha.5114f85" || v.Major != 1 || v.Minor != 2 || v.Patch != 3 {
		t.Errorf("ParseVersion = %+v (%s), %v", v, v, err)
	}
	for _, bad := range []string{
		"", "v", "dev", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "1.2.3-", "1.2.3-01", "1.2.3-a..b",
		"1.2.3+", "1.2.3+a_b", "-1.2.3", "1.2.x", " 1.2.3", "1.2.3 ", "18446744073709551616.0.0", "V1.2.3",
		"test-version",
	} {
		if _, err := ParseVersion(bad); !errors.Is(err, ErrVersion) {
			t.Errorf("ParseVersion(%q) = %v, want ErrVersion", bad, err)
		}
	}
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func manifestJSON(t *testing.T, m Manifest) []byte {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseManifest(t *testing.T) {
	good := Asset{OS: "windows", Arch: "amd64", URL: "https://example.com/Setup.exe", SHA256: strings.Repeat("AB", 32), Size: 10}
	data := []byte(`{"version":"1.2.0","published_at":"2026-10-01T12:00:00Z","notes":"Fixes.","future":1,
		"assets":[{"os":"windows","arch":"amd64","url":"https://example.com/Setup.exe","sha256":"` + strings.Repeat("AB", 32) + `","size":10}]}`)
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.2.0" || m.Notes != "Fixes." || !m.PublishedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) ||
		len(m.Assets) != 1 || m.Assets[0].SHA256 != strings.Repeat("ab", 32) {
		t.Errorf("ParseManifest = %+v", m)
	}
	mod := func(f func(*Manifest)) []byte {
		m := Manifest{Version: "1.2.0", Assets: []Asset{good}}
		f(&m)
		return manifestJSON(t, m)
	}
	bad := map[string][]byte{
		"not json":         []byte("{"),
		"trailing data":    append(mod(func(*Manifest) {}), []byte("{}")...),
		"bad version":      mod(func(m *Manifest) { m.Version = "1.2" }),
		"no version":       mod(func(m *Manifest) { m.Version = "" }),
		"unknown os":       mod(func(m *Manifest) { m.Assets[0].OS = "plan9" }),
		"unknown arch":     mod(func(m *Manifest) { m.Assets[0].Arch = "mips" }),
		"universal win":    mod(func(m *Manifest) { m.Assets[0].Arch = "universal" }),
		"http url":         mod(func(m *Manifest) { m.Assets[0].URL = "http://example.com/Setup.exe" }),
		"userinfo url":     mod(func(m *Manifest) { m.Assets[0].URL = "https://u:p@example.com/Setup.exe" }),
		"short sha":        mod(func(m *Manifest) { m.Assets[0].SHA256 = "abcd" }),
		"non-hex sha":      mod(func(m *Manifest) { m.Assets[0].SHA256 = strings.Repeat("zz", 32) }),
		"zero size":        mod(func(m *Manifest) { m.Assets[0].Size = 0 }),
		"huge size":        mod(func(m *Manifest) { m.Assets[0].Size = MaxAssetSize + 1 }),
		"duplicate asset":  mod(func(m *Manifest) { m.Assets = append(m.Assets, good) }),
		"long notes":       mod(func(m *Manifest) { m.Notes = strings.Repeat("x", maxNotesLength+1) }),
		"bad published_at": []byte(`{"version":"1.2.0","published_at":"yesterday","assets":[]}`),
	}
	for name, data := range bad {
		if _, err := ParseManifest(data); !errors.Is(err, ErrManifest) {
			t.Errorf("%s: %v, want ErrManifest", name, err)
		}
	}
}

func TestAssetFor(t *testing.T) {
	a := func(os, arch string) Asset { return Asset{OS: os, Arch: arch, URL: "https://x/" + os + arch} }
	m := Manifest{Assets: []Asset{a("windows", "amd64"), a("windows", "arm64"), a("darwin", "arm64"), a("darwin", "universal"), a("linux", "amd64")}}
	cases := []struct {
		goos, goarch, want string
	}{
		{"windows", "amd64", "windowsamd64"},
		{"windows", "arm64", "windowsarm64"},
		{"windows", "386", ""},
		{"darwin", "amd64", "darwinuniversal"}, // universal first
		{"darwin", "arm64", "darwinuniversal"},
		{"linux", "amd64", "linuxamd64"},
		{"linux", "arm64", ""},
		{"freebsd", "amd64", ""},
	}
	for _, c := range cases {
		got, ok := m.AssetFor(c.goos, c.goarch)
		if want := c.want != ""; ok != want || (ok && got.URL != "https://x/"+c.want) {
			t.Errorf("AssetFor(%s, %s) = %v, %v; want %q", c.goos, c.goarch, got.URL, ok, c.want)
		}
	}
	// darwin without a universal asset: the one for the architecture.
	only := Manifest{Assets: []Asset{a("darwin", "arm64")}}
	if got, ok := only.AssetFor("darwin", "arm64"); !ok || got.Arch != "arm64" {
		t.Errorf("darwin/arm64 without universal: %v %v", got, ok)
	}
	if _, ok := only.AssetFor("darwin", "amd64"); ok {
		t.Error("darwin/amd64 got the arm64 package")
	}
}

func TestCheckFeedURL(t *testing.T) {
	for _, ok := range []string{"https://example.com/feed.json", "https://example.com:8443/a/b?channel=stable", "https://[::1]/f"} {
		if _, err := CheckFeedURL(ok); err != nil {
			t.Errorf("CheckFeedURL(%q) = %v", ok, err)
		}
	}
	for _, insecure := range []string{"http://example.com/feed.json", "ftp://example.com/f", "file:///C:/feed.json", "HTTP://example.com/f"} {
		if _, err := CheckFeedURL(insecure); !errors.Is(err, ErrInsecureURL) {
			t.Errorf("CheckFeedURL(%q) = %v, want ErrInsecureURL", insecure, err)
		}
	}
	for _, bad := range []string{
		"", "example.com/feed.json", "https://", "https:///feed.json", "https://u:p@example.com/f", "https://example.com/f#x",
		"https://example.com/a b", "https://example.com/\x00", "https:example.com", "https://example.com/" + strings.Repeat("a", maxURLLength),
	} {
		if _, err := CheckFeedURL(bad); err == nil {
			t.Errorf("CheckFeedURL(%q) accepted", bad)
		}
	}
	u, _ := url.Parse("https://example.com/releases/feed.json?channel=beta")
	if got := SignatureURL(u); got != "https://example.com/releases/feed.json.sig?channel=beta" {
		t.Errorf("SignatureURL = %s", got)
	}
}

// feedServer serves a signed manifest at /feed.json (+ .sig) and packages at /pkg/<name>.
type feedServer struct {
	*httptest.Server
	manifest atomic.Pointer[[]byte]
	sig      atomic.Pointer[[]byte]
	packages map[string][]byte
	hits     atomic.Int32
}

func newFeedServer(t *testing.T) *feedServer {
	t.Helper()
	fs := &feedServer{packages: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/feed.json", func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		_, _ = w.Write(*fs.manifest.Load())
	})
	mux.HandleFunc("/feed.json.sig", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(*fs.sig.Load())
	})
	mux.HandleFunc("/pkg/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := fs.packages[strings.TrimPrefix(r.URL.Path, "/pkg/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/redirect-http", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/feed.json", http.StatusFound)
	})
	mux.HandleFunc("/big.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte(" "), MaxManifestSize+1))
	})
	mux.HandleFunc("/big.json.sig", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(*fs.sig.Load())
	})
	mux.HandleFunc("/bigsig.json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(*fs.manifest.Load()) })
	mux.HandleFunc("/nosig.json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(*fs.manifest.Load()) })
	mux.HandleFunc("/gone.json", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusGone) })
	mux.HandleFunc("/broken.json", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/bigsig.json.sig", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("A"), MaxSignatureSize+1))
	})
	fs.Server = httptest.NewUnstartedServer(mux)
	fs.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes of the untrusted-certificate case
	fs.StartTLS()
	t.Cleanup(fs.Close)
	return fs
}

func (fs *feedServer) publish(manifest, sig []byte) {
	fs.manifest.Store(&manifest)
	fs.sig.Store(&sig)
}

func TestFeedFetch(t *testing.T) {
	pub, priv := newKey(t)
	fs := newFeedServer(t)
	pkg := []byte("setup program")
	data := manifestJSON(t, Manifest{Version: "0.4.0", Notes: "New.", Assets: []Asset{
		{OS: "windows", Arch: "amd64", URL: fs.URL + "/pkg/setup.exe", SHA256: sum(pkg), Size: int64(len(pkg))},
		{OS: "darwin", Arch: "universal", URL: fs.URL + "/pkg/downex.dmg", SHA256: sum(pkg), Size: int64(len(pkg))},
	}})
	fs.publish(data, sign(priv, data))
	feed := Feed{URL: fs.URL + "/feed.json", Key: pub, Client: fs.Client(), UserAgent: "Downex/test"}
	ctx := context.Background()
	cur, _ := ParseVersion("0.3.3")

	r, err := feed.Check(ctx, cur, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Available || !r.Newer || r.Latest.String() != "0.4.0" || r.Asset.URL != fs.URL+"/pkg/setup.exe" || r.Manifest.Notes != "New." {
		t.Errorf("Check = %+v", r)
	}
	if r, _ := feed.Check(ctx, cur, "linux", "amd64"); r.Available || !r.Newer || r.HasAsset {
		t.Errorf("linux (no package): %+v", r)
	}
	same, _ := ParseVersion("0.4.0")
	if r, _ := feed.Check(ctx, same, "darwin", "arm64"); r.Available || r.Newer || !r.HasAsset {
		t.Errorf("same version: %+v", r)
	}
	newer, _ := ParseVersion("0.5.0-rc.1")
	if r, _ := feed.Check(ctx, newer, "windows", "amd64"); r.Available || r.Newer {
		t.Errorf("running a newer version: %+v", r)
	}

	// The signature is checked over the exact bytes before anything is parsed.
	fs.publish(append(bytes.Clone(data), '\n'), sign(priv, data))
	if _, err := feed.Fetch(ctx); !errors.Is(err, ErrSignature) {
		t.Errorf("tampered manifest: %v, want ErrSignature", err)
	}
	_, other := newKey(t)
	fs.publish(data, sign(other, data))
	if _, err := feed.Fetch(ctx); !errors.Is(err, ErrSignature) {
		t.Errorf("foreign signer: %v, want ErrSignature", err)
	}
	garbage := []byte("not json at all")
	fs.publish(garbage, sign(priv, garbage))
	if _, err := feed.Fetch(ctx); !errors.Is(err, ErrManifest) {
		t.Errorf("signed garbage: %v, want ErrManifest", err)
	}
	fs.publish(data, sign(priv, data))

	// Oversized manifest and signature are refused without reading them whole.
	big := feed
	big.URL = fs.URL + "/big.json"
	if _, err := big.Fetch(ctx); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized manifest: %v, want ErrTooLarge", err)
	}
	big.URL = fs.URL + "/bigsig.json"
	if _, err := big.Fetch(ctx); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized signature: %v, want ErrTooLarge", err)
	}

	// No key: disabled before anything is fetched.
	hits := fs.hits.Load()
	nokey := feed
	nokey.Key = nil
	if _, err := nokey.Fetch(ctx); !errors.Is(err, ErrDisabled) {
		t.Errorf("no key: %v, want ErrDisabled", err)
	}
	// Feeds must be https; redirects to http are refused.
	plain := feed
	plain.URL = "http" + strings.TrimPrefix(fs.URL, "https") + "/feed.json"
	if _, err := plain.Fetch(ctx); !errors.Is(err, ErrInsecureURL) {
		t.Errorf("http feed: %v, want ErrInsecureURL", err)
	}
	if fs.hits.Load() != hits {
		t.Error("a refused feed was fetched")
	}
	redir := feed
	redir.URL = fs.URL + "/redirect-http"
	if _, err := redir.Fetch(ctx); !errors.Is(err, ErrInsecureURL) {
		t.Errorf("redirect to http: %v, want ErrInsecureURL", err)
	}
	missing := feed
	missing.URL = fs.URL + "/nothing.json"
	_, err = missing.Fetch(ctx)
	if !errors.Is(err, ErrFetch) || strings.Contains(err.Error(), fs.URL) {
		t.Errorf("missing feed: %v, want ErrFetch without the URL", err)
	}
	// A feed that is not published (yet) says so; other failures do not.
	if !errors.Is(err, ErrNotPublished) {
		t.Errorf("missing feed (404): %v, want ErrNotPublished", err)
	}
	for path, published := range map[string]bool{"/gone.json": false, "/broken.json": true, "/nosig.json": true} {
		f := feed
		f.URL = fs.URL + path
		_, err := f.Fetch(ctx)
		if !errors.Is(err, ErrFetch) || errors.Is(err, ErrNotPublished) != !published {
			t.Errorf("%s: %v (ErrNotPublished %v), want ErrFetch and ErrNotPublished %v", path, err, errors.Is(err, ErrNotPublished), !published)
		}
	}
	// An untrusted certificate is a fetch failure (TLS is never relaxed).
	untrusted := feed
	untrusted.Client = NewHTTPClient(nil)
	if _, err := untrusted.Fetch(ctx); !errors.Is(err, ErrFetch) {
		t.Errorf("untrusted certificate: %v, want ErrFetch", err)
	}
}

func TestDownload(t *testing.T) {
	fs := newFeedServer(t)
	pkg := bytes.Repeat([]byte("downex"), 1000)
	fs.packages["good"] = pkg
	fs.packages["long"] = append(bytes.Clone(pkg), 'x')
	fs.packages["bad"] = bytes.ToUpper(pkg)
	dir := filepath.Join(t.TempDir(), "updates")
	ctx := context.Background()
	asset := func(name string) Asset {
		return Asset{OS: "windows", Arch: "amd64", URL: fs.URL + "/pkg/" + name, SHA256: sum(pkg), Size: int64(len(pkg))}
	}

	path, err := Download(ctx, fs.Client(), "Downex/test", asset("good"), dir, "Downex-Setup-1.0.0.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, pkg) || filepath.Base(path) != "Downex-Setup-1.0.0.exe" {
		t.Errorf("downloaded %s (%d bytes)", path, len(got))
	}

	if _, err := Download(ctx, fs.Client(), "", asset("bad"), dir, "Downex-Setup-1.0.1.exe"); !errors.Is(err, ErrChecksum) {
		t.Errorf("sha256 mismatch: %v, want ErrChecksum", err)
	}
	if _, err := Download(ctx, fs.Client(), "", asset("long"), dir, "Downex-Setup-1.0.2.exe"); !errors.Is(err, ErrSize) && !errors.Is(err, ErrTooLarge) {
		t.Errorf("larger than published: %v, want ErrSize or ErrTooLarge", err)
	}
	short := asset("good")
	short.Size++
	if _, err := Download(ctx, fs.Client(), "", short, dir, "Downex-Setup-1.0.3.exe"); !errors.Is(err, ErrSize) {
		t.Errorf("shorter than published: %v, want ErrSize", err)
	}
	insecure := asset("good")
	insecure.URL = "http://" + strings.TrimPrefix(fs.URL, "https://") + "/pkg/good"
	if _, err := Download(ctx, fs.Client(), "", insecure, dir, "Downex-Setup-1.0.4.exe"); !errors.Is(err, ErrInsecureURL) {
		t.Errorf("http package: %v, want ErrInsecureURL", err)
	}
	if _, err := Download(ctx, fs.Client(), "", asset("good"), dir, "../evil.exe"); err == nil {
		t.Error("a name with a path was accepted")
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if fmt.Sprint(names) != "[Downex-Setup-1.0.0.exe]" {
		t.Errorf("files left behind: %v (failed downloads must be deleted)", names)
	}

	// CleanDir removes packages and partial downloads only.
	for _, n := range []string{"Downex-1.0.0.dmg", "x.part", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	CleanDir(dir)
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Errorf("after CleanDir: %v", entries)
	}
	CleanDir(filepath.Join(dir, "missing")) // no panic, no error
}
