package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary doubles as the stand-in tools: copied as yt-dlp(.exe) it prints the
// name of its folder as its version, copied as deno(.exe) "deno <folder name> (...)",
// and copied as standin(.exe) it runs the mode named by its first argument.
func TestMain(m *testing.M) {
	standIn()
	os.Exit(m.Run())
}

func standIn() {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	dir := filepath.Base(filepath.Dir(os.Args[0]))
	switch base {
	case "yt-dlp":
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println(dir)
			os.Exit(0)
		}
		// An onedir copy: print the marker its zip put into _internal/ next to it.
		if len(os.Args) == 2 && os.Args[1] == "--onedir-marker" {
			data, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), "_internal", "marker.txt"))
			if err != nil {
				os.Exit(3)
			}
			fmt.Println(string(data))
			os.Exit(0)
		}
		// A copy that fails every page: it prints the stderr.txt next to it and exits 1.
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), standInStderr)); err == nil {
			_, _ = os.Stderr.Write(data)
			os.Exit(1)
		}
		os.Exit(2)
	case "deno":
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Printf("deno %s (stable, release, test)\nv8 13.0\ntypescript 5.9\n", dir)
			os.Exit(0)
		}
		os.Exit(1)
	case "standin":
		os.Exit(standInMode(os.Args[1:]))
	}
}

func standInMode(args []string) int {
	if len(args) == 0 {
		return 3
	}
	switch args[0] {
	case "echo": // prints each argument on its own line
		for _, a := range args[1:] {
			fmt.Println(a)
		}
		return 0
	case "exit":
		n, _ := strconv.Atoi(args[1])
		fmt.Fprintln(os.Stderr, "exiting with", n)
		return n
	case "sleep":
		d, _ := time.ParseDuration(args[1])
		time.Sleep(d)
		return 0
	case "flood": // writes n bytes to stdout
		n, _ := strconv.Atoi(args[1])
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), n))
		return 0
	case "stderr": // writes n numbered lines to stderr
		n, _ := strconv.Atoi(args[1])
		for i := range n {
			fmt.Fprintf(os.Stderr, "line %06d\n", i)
		}
		return 1
	case "env":
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
		return 0
	case "spawn": // starts a sleeping grandchild, prints its pid, then sleeps (or exits: "spawn exit")
		exe, _ := os.Executable()
		c := exec.Command(exe, "sleep", "60s")
		c.Stdout = os.Stdout // the grandchild holds the pipe
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		fmt.Println(c.Process.Pid)
		if len(args) > 1 && args[1] == "exit" {
			return 0
		}
		time.Sleep(60 * time.Second)
		return 0
	}
	return 3
}

// standInStderr is the file next to a stand-in yt-dlp that makes it fail every page.
const standInStderr = "stderr.txt"

// writeStandInStderr makes the stand-in yt-dlp in dir print text and exit 1 for every
// page (its --version still answers).
func writeStandInStderr(t testing.TB, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, standInStderr), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// testExe is the running test binary.
func testExe(t testing.TB) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

var (
	exeOnce  sync.Once
	exeBytes []byte
	exeErr   error
)

// testExeBytes returns the test binary's contents (read once).
func testExeBytes(t testing.TB) []byte {
	t.Helper()
	exeOnce.Do(func() { exeBytes, exeErr = os.ReadFile(testExe(t)) })
	if exeErr != nil {
		t.Fatal(exeErr)
	}
	return exeBytes
}

// standInAt places the test binary at dir/name (".exe" added on Windows) and returns
// its path.
func standInAt(t testing.TB, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.Link(testExe(t), p); err != nil {
		if err := os.WriteFile(p, testExeBytes(t), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testSigner is an OpenPGP v4 RSA key that signs like yt-dlp's release key.
type testSigner struct {
	key     *rsa.PrivateKey
	created time.Time
	body    []byte // public key packet body
	fp      [20]byte
}

var (
	signerMu    sync.Mutex
	signerCache = map[string]*testSigner{}
)

// newTestSigner returns the test key named name (generated once per test binary).
func newTestSigner(t testing.TB, name string) *testSigner {
	t.Helper()
	signerMu.Lock()
	defer signerMu.Unlock()
	if s := signerCache[name]; s != nil {
		return s
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &testSigner{key: key, created: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	var b bytes.Buffer
	b.WriteByte(4)
	_ = binary.Write(&b, binary.BigEndian, uint32(s.created.Unix()))
	b.WriteByte(pgpAlgoRSA)
	b.Write(mpi(key.N))
	b.Write(mpi(big.NewInt(int64(key.E))))
	s.body = b.Bytes()
	h := crypto.SHA1.New()
	h.Write([]byte{0x99, byte(len(s.body) >> 8), byte(len(s.body))})
	h.Write(s.body)
	copy(s.fp[:], h.Sum(nil))
	signerCache[name] = s
	return s
}

func mpi(n *big.Int) []byte {
	b := n.Bytes()
	out := []byte{byte(n.BitLen() >> 8), byte(n.BitLen())}
	return append(out, b...)
}

// packet encodes a new-format packet.
func packet(tag byte, body []byte) []byte {
	out := []byte{0xc0 | tag}
	switch n := len(body); {
	case n < 192:
		out = append(out, byte(n))
	case n < 8384:
		n -= 192
		out = append(out, byte(n>>8)+192, byte(n))
	default:
		out = append(out, 255, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, body...)
}

// armor encodes data as an ASCII armor block of kind, with a CRC-24.
func armor(kind string, data []byte) []byte {
	var b strings.Builder
	b.WriteString("-----BEGIN " + kind + "-----\nComment: test key\n\n")
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 64 {
		b.WriteString(enc[:64] + "\n")
		enc = enc[64:]
	}
	b.WriteString(enc + "\n")
	c := crc24(data)
	b.WriteString("=" + base64.StdEncoding.EncodeToString([]byte{byte(c >> 16), byte(c >> 8), byte(c)}) + "\n")
	b.WriteString("-----END " + kind + "-----\n")
	return []byte(b.String())
}

// armoredPublic is the signer's public key as an armored key block (a user ID packet
// follows the key, as in real key files).
func (s *testSigner) armoredPublic() []byte {
	data := append(packet(pgpTagPublicKey, s.body), packet(13, []byte("Test Signer <test@example.invalid>"))...)
	return armor("PGP PUBLIC KEY BLOCK", data)
}

// sigOptions vary a test signature.
type sigOptions struct {
	sigType      byte
	hashAlgo     byte
	critical     byte // an extra hashed subpacket type marked critical (0 = none)
	noIssuerFP   bool
	expires      time.Duration
	creationCrit bool // the creation time marked critical, as GnuPG does
}

// sign returns a binary detached v4 signature over data made at created.
func (s *testSigner) sign(data []byte, created time.Time, o sigOptions) []byte {
	if o.hashAlgo == 0 {
		o.hashAlgo = pgpHashSHA512
	}
	var hashed bytes.Buffer
	ctype := byte(subCreationTime)
	if o.creationCrit {
		ctype |= 0x80
	}
	hashed.Write([]byte{5, ctype})
	_ = binary.Write(&hashed, binary.BigEndian, uint32(created.Unix()))
	if o.expires > 0 {
		hashed.Write([]byte{5, subExpiration})
		_ = binary.Write(&hashed, binary.BigEndian, uint32(o.expires/time.Second))
	}
	if !o.noIssuerFP {
		hashed.Write([]byte{22, subIssuerFprint, 4})
		hashed.Write(s.fp[:])
	}
	if o.critical != 0 {
		hashed.Write([]byte{2, 0x80 | o.critical, 0})
	}
	var body bytes.Buffer
	body.Write([]byte{4, o.sigType, pgpAlgoRSA, o.hashAlgo})
	_ = binary.Write(&body, binary.BigEndian, uint16(hashed.Len()))
	body.Write(hashed.Bytes())
	hashedPart := bytes.Clone(body.Bytes())
	unhashed := append([]byte{9, subIssuerKeyID}, s.fp[12:]...)
	_ = binary.Write(&body, binary.BigEndian, uint16(len(unhashed)))
	body.Write(unhashed)
	h := sha512.New()
	h.Write(data)
	h.Write(hashedPart)
	h.Write([]byte{4, 0xff})
	_ = binary.Write(h, binary.BigEndian, uint32(len(hashedPart)))
	digest := h.Sum(nil)
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA512, digest)
	if err != nil {
		panic(err)
	}
	body.Write(digest[:2])
	body.Write(mpi(new(big.Int).SetBytes(sig)))
	return packet(pgpTagSignature, body.Bytes())
}

// releaseServer is a TLS stand-in for GitHub releases: releases/latest redirects to the
// tag set with setLatest, and files are served from releases/download/<tag>/<asset>.
type releaseServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	latest map[string]string // repo → tag
	files  map[string][]byte // URL path → content
	hits   map[string]int    // URL path → requests
	gates  map[string]chan struct{}
}

func newReleaseServer(t testing.TB) *releaseServer {
	rs := &releaseServer{latest: map[string]string{}, files: map[string][]byte{}, hits: map[string]int{}, gates: map[string]chan struct{}{}}
	rs.srv = httptest.NewUnstartedServer(http.HandlerFunc(rs.serve))
	rs.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	rs.srv.StartTLS()
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *releaseServer) serve(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	rs.hits[r.URL.Path]++
	if repo, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/releases/latest"); ok {
		tag, found := rs.latest[repo]
		rs.mu.Unlock()
		if !found {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "https://"+r.Host+"/"+repo+"/releases/tag/"+tag, http.StatusFound)
		return
	}
	data, ok := rs.files[r.URL.Path]
	gate := rs.gates[r.URL.Path]
	rs.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if gate == nil {
		_, _ = w.Write(data)
		return
	}
	half := len(data) / 2
	_, _ = w.Write(data[:half])
	w.(http.Flusher).Flush()
	select {
	case <-gate:
	case <-r.Context().Done():
		return
	}
	_, _ = w.Write(data[half:])
}

func (rs *releaseServer) setLatest(repo, tag string) {
	rs.mu.Lock()
	rs.latest[repo] = tag
	rs.mu.Unlock()
}

func (rs *releaseServer) put(repo, tag, asset string, data []byte) {
	rs.mu.Lock()
	rs.files["/"+repo+"/releases/download/"+tag+"/"+asset] = data
	rs.mu.Unlock()
}

func (rs *releaseServer) hit(path string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.hits[path]
}

// total is the number of requests served.
func (rs *releaseServer) total() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, h := range rs.hits {
		n += h
	}
	return n
}

// gate makes the file of repo/tag/asset stop after its first half until the returned
// function is called.
func (rs *releaseServer) gate(repo, tag, asset string) (open func()) {
	ch := make(chan struct{})
	rs.mu.Lock()
	rs.gates["/"+repo+"/releases/download/"+tag+"/"+asset] = ch
	rs.mu.Unlock()
	return sync.OnceFunc(func() { close(ch) })
}

// ytdlpAsset is the yt-dlp asset of the running system.
func ytdlpAsset() string {
	a, _ := AssetNames(runtime.GOOS, runtime.GOARCH, false)
	return a
}

// publishYtDlp publishes a yt-dlp release of repo with the stand-in as its asset,
// SHA2-256SUMS signed by s at created, and makes it the latest.
func (rs *releaseServer) publishYtDlp(repo, tag string, asset []byte, s *testSigner, created time.Time) {
	sum := sha256.Sum256(asset)
	sums := fmt.Sprintf("%x  yt-dlp.tar.gz\n%s  %s\n%x  yt-dlp_other\n", sha256.Sum256([]byte("other")), hex.EncodeToString(sum[:]), ytdlpAsset(), sha256.Sum256([]byte("x")))
	rs.put(repo, tag, ytdlpSums, []byte(sums))
	rs.put(repo, tag, ytdlpSumsSig, s.sign([]byte(sums), created, sigOptions{creationCrit: true}))
	rs.put(repo, tag, ytdlpAsset(), asset)
	rs.setLatest(repo, tag)
}

// publishYtDlpFiles publishes a yt-dlp release of repo holding files (asset name →
// content), all named in SHA2-256SUMS signed by s at created, and makes it the latest.
func (rs *releaseServer) publishYtDlpFiles(repo, tag string, files map[string][]byte, s *testSigner, created time.Time) {
	var b strings.Builder
	fmt.Fprintf(&b, "%x  yt-dlp.tar.gz\n", sha256.Sum256([]byte("other")))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(files[name]), name)
		rs.put(repo, tag, name, files[name])
	}
	sums := []byte(b.String())
	rs.put(repo, tag, ytdlpSums, sums)
	rs.put(repo, tag, ytdlpSumsSig, s.sign(sums, created, sigOptions{creationCrit: true}))
	rs.setLatest(repo, tag)
}

// buildZip returns a zip archive of entries, deflated or (store) stored; an entry's
// mode defaults to 0755.
func buildZip(t testing.TB, store bool, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if store {
			h.Method = zip.Store
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// onedirMarker is what the stand-in's --onedir-marker prints from an onedir copy.
const onedirMarker = "unpacked from the onedir zip"

// onedirFiles are the files of the test onedir zip besides its executable.
var onedirFiles = []zipEntry{
	{name: "_internal/", mode: fs.ModeDir | 0o755},
	{name: "_internal/marker.txt", data: []byte(onedirMarker), mode: 0o644},
	{name: "_internal/base_library.zip", data: bytes.Repeat([]byte("library "), 512), mode: 0o644},
	{name: "_internal/Cryptodome/Cipher/_raw_ecb.pyd", data: []byte("extension module"), mode: 0o755},
}

// onedirZip returns an onedir release zip: exe (the stand-in, unless given) at its root
// as exeName, and onedirFiles (cached per name and content).
func onedirZip(t testing.TB, exeName string, exe []byte) []byte {
	t.Helper()
	key := "onedir:" + exeName + ":" + hex.EncodeToString(func() []byte { s := sha256.Sum256(exe); return s[:] }())
	zipMu.Lock()
	defer zipMu.Unlock()
	if z := zipCache[key]; z != nil {
		return z
	}
	z := buildZip(t, true, append([]zipEntry{{name: exeName, data: exe}}, onedirFiles...)...)
	zipCache[key] = z
	return z
}

var (
	zipMu    sync.Mutex
	zipCache = map[string][]byte{}
)

// denoZip returns a zip holding exe as deno(.exe) (cached per name).
func denoZip(t testing.TB, exe []byte) []byte {
	t.Helper()
	name := exeName(ToolDeno, runtime.GOOS)
	key := name + strconv.Itoa(len(exe)) + hex.EncodeToString(func() []byte { s := sha256.Sum256(exe); return s[:8] }())
	zipMu.Lock()
	defer zipMu.Unlock()
	if z := zipCache[key]; z != nil {
		return z
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(exe); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipCache[key] = buf.Bytes()
	return buf.Bytes()
}

// publishDeno publishes a Deno release (tag "v<version>") holding exe and makes it the
// latest; withExeSums also publishes the unpacked executable's checksum file.
func (rs *releaseServer) publishDeno(t testing.TB, tag string, exe []byte, withExeSums bool) {
	t.Helper()
	_, asset := AssetNames(runtime.GOOS, runtime.GOARCH, false)
	if asset == "" {
		t.Skip("no Deno build for this platform")
	}
	z := denoZip(t, exe)
	zs := sha256.Sum256(z)
	rs.put(repoDeno, tag, asset, z)
	rs.put(repoDeno, tag, asset+".sha256sum", []byte(hex.EncodeToString(zs[:])+"  "+asset+"\n"))
	if withExeSums {
		es := sha256.Sum256(exe)
		target := strings.TrimSuffix(strings.TrimPrefix(asset, "deno-"), ".zip")
		rs.put(repoDeno, tag, denoExeSums(target), []byte(hex.EncodeToString(es[:])+"  "+exeName(ToolDeno, runtime.GOOS)+"\n"))
	}
	rs.setLatest(repoDeno, tag)
}

// testHooks are fast hooks without real Authenticode, musl or space checks.
func testHooks() hooks {
	h := defaultHooks()
	h.authenticode = func(string) (string, error) { return "Test Signer", nil }
	h.musl = func() bool { return false }
	h.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	h.headerTimeout = 10 * time.Second
	h.stallWindow = 10 * time.Second
	h.lockPoll = 10 * time.Millisecond
	return h
}

// testEnv is a manager wired to a release server, a fake clock and an empty PATH.
type testEnv struct {
	m      *Manager
	rs     *releaseServer
	clock  *fakeClock
	signer *testSigner
	dir    string
	path   map[string]string // what LookPath finds
	pathMu sync.Mutex
}

type envOption func(*Config, *hooks)

func newTestEnv(t *testing.T, opts ...envOption) *testEnv {
	t.Helper()
	e := &testEnv{rs: newReleaseServer(t), clock: newClock(), signer: newTestSigner(t, "release"), dir: t.TempDir(), path: map[string]string{}}
	cfg := Config{
		Dir:         e.dir,
		Client:      e.rs.srv.Client(),
		ReleaseBase: e.rs.srv.URL,
		YtDlpKeys:   [][]byte{e.signer.armoredPublic()},
		LookPath:    e.lookPath,
		Now:         e.clock.Now,
	}
	h := testHooks()
	for _, o := range opts {
		o(&cfg, &h)
	}
	m, err := newManager(cfg, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	e.m = m
	return e
}

// restart closes the manager and starts a new one of the same folder (a daemon
// restart); between runs in between, e.g. to edit tools.json.
func (e *testEnv) restart(t *testing.T, between func()) {
	t.Helper()
	_ = e.m.Close()
	if between != nil {
		between()
	}
	m, err := newManager(e.m.cfg, e.m.h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	e.m = m
}

func (e *testEnv) lookPath(name string) (string, error) {
	e.pathMu.Lock()
	defer e.pathMu.Unlock()
	if p, ok := e.path[name]; ok {
		return p, nil
	}
	return "", exec.ErrNotFound
}

func (e *testEnv) setPath(name, p string) {
	e.pathMu.Lock()
	e.path[name] = p
	e.pathMu.Unlock()
}

// publishBoth publishes yt-dlp (nightly) tag yt and Deno version deno, both the stand-in.
func (e *testEnv) publishBoth(t *testing.T, yt, deno string) {
	t.Helper()
	e.rs.publishYtDlp(repoYtDlpNightly, yt, testExeBytes(t), e.signer, e.clock.Now().Add(-time.Hour))
	e.rs.publishDeno(t, "v"+deno, testExeBytes(t), true)
}

// info returns the status of tool.
func (e *testEnv) info(tool string) Info {
	for _, in := range e.m.Status().Tools {
		if in.Name == tool {
			return in
		}
	}
	return Info{}
}

// waitFor polls cond for up to 20 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ensure calls Ensure with a generous deadline.
func (e *testEnv) ensure(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return e.m.Ensure(ctx)
}

// codeOf returns the ToolInfo error code of err's chain.
func codeOf(err error) string {
	var te *Error
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

// readAll reads a file or fails.
func readAll(t testing.TB, p string) []byte {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
