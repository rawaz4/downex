package tools

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/sitemedia"
)

// TestFirstUseInstall: Ensure downloads, verifies and installs both tools, reporting
// progress meanwhile; tools.json records them and a second manager trusts them.
func TestFirstUseInstall(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	open := e.rs.gate(repoYtDlpNightly, "2026.10.20", ytdlpAsset())
	var notes atomic.Int32
	cancel := e.m.Subscribe(func() { notes.Add(1) })
	defer cancel()

	// The first answer comes at the caller's deadline while the install goes on.
	waitFor(t, "a partial download", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := e.m.Ensure(ctx)
		if err == nil {
			t.Fatal("Ensure succeeded before the download finished")
		}
		if !errors.Is(err, sitemedia.ErrToolsInstalling) || !regexp.MustCompile(`^downloading yt-dlp and Deno \(\d{1,2}%\)$`).MatchString(err.Error()) {
			t.Fatalf("Ensure during the install: %v", err)
		}
		in := e.info(ToolYtDlp)
		return in.State == StateInstalling && in.BytesDone > 0 && in.BytesTotal == int64(len(testExeBytes(t)))
	})
	open()
	if err := e.ensure(t); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if notes.Load() == 0 {
		t.Error("no status notifications")
	}
	s := e.m.Status()
	if !s.Ready {
		t.Errorf("not ready: %+v", s)
	}
	yt, dn := e.info(ToolYtDlp), e.info(ToolDeno)
	if yt.State != StateReady || !yt.Managed || yt.Version != "2026.10.20" || yt.Verified != "openpgp+sha256" ||
		yt.LatestVersion != "2026.10.20" || yt.CheckedAt.IsZero() || yt.Error != "" ||
		!samePath(yt.Path, filepath.Join(e.dir, "yt-dlp", "2026.10.20", exeName(ToolYtDlp, runtime.GOOS))) {
		t.Errorf("yt-dlp %+v", yt)
	}
	wantVerified := "sha256"
	if runtime.GOOS == "windows" {
		wantVerified = "sha256+authenticode"
	}
	if dn.State != StateReady || !dn.Managed || dn.Version != "2.5.0" || dn.Verified != wantVerified {
		t.Errorf("deno %+v", dn)
	}
	st := loadState(e.m.lay)
	if st.YtDlp.Version != "2026.10.20" || st.YtDlp.Channel != ChannelNightly || st.Deno.Version != "2.5.0" || st.YtDlp.SigCreated.IsZero() {
		t.Errorf("tools.json %+v", st)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "tmp")); len(entries) != 0 {
		t.Errorf("tmp not empty: %v", entries)
	}
	// Ready tools: no more requests.
	before := e.rs.total()
	if err := e.ensure(t); err != nil || e.rs.total() != before {
		t.Errorf("second Ensure: %v, %d requests", err, e.rs.total()-before)
	}
	l, err := e.m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !l.YtDlpManaged || l.YtDlpVersion != "2026.10.20" || !samePath(l.Deno, dn.Path) {
		t.Errorf("lease %+v", l)
	}
	l.Release()
	l.Release()

	// A new manager of the same folder checks the copies and uses them without requests.
	e.m.Close()
	m2, err := newManager(e.m.cfg, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	ctx, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	if err := m2.Ensure(ctx); err != nil || e.rs.total() != before {
		t.Errorf("restart: %v, %d requests", err, e.rs.total()-before)
	}
	// A managed copy that no longer matches its hash is installed again.
	m2.Close()
	exe := filepath.Join(e.dir, "yt-dlp", "2026.10.20", exeName(ToolYtDlp, runtime.GOOS))
	waitFor(t, "the copy to be writable", func() bool { return os.WriteFile(exe, []byte("tampered"), 0o755) == nil })
	m3, err := newManager(e.m.cfg, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	defer m3.Close()
	if err := m3.Ensure(ctx); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if e.rs.hit("/"+repoYtDlpNightly+"/releases/download/2026.10.20/"+ytdlpAsset()) != 2 {
		t.Error("the tampered copy was not downloaded again")
	}
	if sum, _, err := fileSum(exe); err != nil || hex.EncodeToString(sum[:]) != loadState(m3.lay).YtDlp.SHA256 {
		t.Errorf("reinstalled copy: %v", err)
	}
}

// TestInstallLeftoversCleaned: partial downloads, work folders, unfinished and unknown
// version folders of an earlier run are removed at start.
func TestInstallLeftoversCleaned(t *testing.T) {
	dir := t.TempDir()
	leftovers := []string{
		filepath.Join(dir, "tmp", "x.part"),
		filepath.Join(dir, "run", "0123456789abcdef", "tmp", "_MEI1", "python.dll"),
		filepath.Join(dir, "yt-dlp", "2026.10.20.new-00ff00ff00ff00ff", "yt-dlp.exe"),
		filepath.Join(dir, "yt-dlp", "2025.01.01", "yt-dlp.exe"),
		filepath.Join(dir, "deno", "2.1.0", "deno.exe"),
		filepath.Join(dir, ".tools-123.tmp"),
	}
	keep := filepath.Join(dir, "yt-dlp", "notes.txt")
	for _, p := range append(leftovers, keep) {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := newManager(Config{Dir: dir, LookPath: func(string) (string, error) { return "", os.ErrNotExist }}, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, p := range leftovers {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("an unrelated file was removed: %v", err)
	}
	for _, d := range []string{"tmp", "run", filepath.Join("cache", "yt-dlp"), filepath.Join("cache", "deno")} {
		if fi, err := os.Stat(filepath.Join(dir, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s missing: %v", d, err)
		}
	}
}

// TestInstallFailures: each check refuses its tool with its code, the other tool still
// installs, and nothing unverified is left.
func TestInstallFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(e *testEnv, t *testing.T)
		tool  string
		code  string
		text  string
	}{
		{"tampered SUMS", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			sums := e.rs.files["/"+repoYtDlpNightly+"/releases/download/2026.10.20/"+ytdlpSums]
			e.rs.put(repoYtDlpNightly, "2026.10.20", ytdlpSums, append(sums, '\n'))
		}, ToolYtDlp, CodeSignature, "yt-dlp failed its signature check"},
		{"other signer", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			sums := e.rs.files["/"+repoYtDlpNightly+"/releases/download/2026.10.20/"+ytdlpSums]
			e.rs.put(repoYtDlpNightly, "2026.10.20", ytdlpSumsSig, newTestSigner(t, "other").sign(sums, e.clock.Now(), sigOptions{}))
		}, ToolYtDlp, CodeSignature, "yt-dlp failed its signature check"},
		{"asset checksum", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			e.rs.put(repoYtDlpNightly, "2026.10.20", ytdlpAsset(), []byte("MZ something else"))
		}, ToolYtDlp, CodeChecksum, "yt-dlp failed its checksum check"},
		{"web page", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			e.rs.put(repoYtDlpNightly, "2026.10.20", ytdlpSums, []byte("\n  <!DOCTYPE html><html>rate limited</html>"))
		}, ToolYtDlp, CodeHTTPStatus, "could not download yt-dlp (a web page instead of the file)"},
		{"no release", func(e *testEnv, t *testing.T) {
			e.rs.publishDeno(t, "v2.5.0", testExeBytes(t), false)
		}, ToolYtDlp, CodeHTTPStatus, "could not download yt-dlp (HTTP 404)"},
		{"smoke test", func(e *testEnv, t *testing.T) {
			e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.20", []byte("not a program at all"), e.signer, e.clock.Now())
			e.rs.publishDeno(t, "v2.5.0", testExeBytes(t), false)
		}, ToolYtDlp, CodeSmokeTest, "the downloaded yt-dlp does not run"},
		{"deno zip checksum", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			_, asset := AssetNames(runtime.GOOS, runtime.GOARCH, false)
			e.rs.put(repoDeno, "v2.5.0", asset+".sha256sum", []byte(hexSum("other")+"  "+asset+"\n"))
		}, ToolDeno, CodeChecksum, "Deno failed its checksum check"},
		{"deno exe checksum", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			target := strings.TrimSuffix(strings.TrimPrefix(e.m.assets.Deno, "deno-"), ".zip")
			e.rs.put(repoDeno, "v2.5.0", denoExeSums(target), []byte(hexSum("other")+"  deno\n"))
		}, ToolDeno, CodeChecksum, "Deno failed its checksum check"},
		{"deno archive", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			z := writeZip(t, zipEntry{name: "../" + exeName(ToolDeno, runtime.GOOS), data: []byte("x")})
			data := readAll(t, z)
			_, asset := AssetNames(runtime.GOOS, runtime.GOARCH, false)
			e.rs.put(repoDeno, "v2.5.0", asset, data)
			e.rs.put(repoDeno, "v2.5.0", asset+".sha256sum", []byte(hex.EncodeToString(sumOf(data))+"  "+asset+"\n"))
		}, ToolDeno, CodeInternal, "could not install Deno: unexpected archive layout"},
		{"deno too old", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.2.0")
		}, ToolDeno, CodeInternal, "could not install Deno: the latest Deno is older than 2.3.0"},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			name  string
			setup func(e *testEnv, t *testing.T)
			tool  string
			code  string
			text  string
		}{"authenticode", func(e *testEnv, t *testing.T) {
			e.publishBoth(t, "2026.10.20", "2.5.0")
			e.m.h.authenticode = func(string) (string, error) { return "", errors.New("unsigned") }
		}, ToolDeno, CodeAuthenticode, "deno.exe is not signed by its publisher"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t)
			c.setup(e, t)
			err := e.ensure(t)
			if !errors.Is(err, sitemedia.ErrToolsUnavailable) || codeOf(err) != c.code {
				t.Fatalf("Ensure: %v (code %q), want tools_unavailable with %s", err, codeOf(err), c.code)
			}
			if !strings.HasSuffix(err.Error(), ": "+c.text) {
				t.Errorf("text %q, want it to end with %q", err, c.text)
			}
			in := e.info(c.tool)
			if in.State != StateFailed || in.ErrorCode != c.code || in.Error != c.text || in.Version != "" {
				t.Errorf("%s status %+v", c.tool, in)
			}
			other := ToolDeno
			if c.tool == ToolDeno {
				other = ToolYtDlp
			}
			if in := e.info(other); in.State != StateReady {
				t.Errorf("%s was not installed independently: %+v", other, in)
			}
			if entries, _ := os.ReadDir(filepath.Join(e.dir, c.tool)); len(entries) != 0 {
				t.Errorf("%s folder holds %v", c.tool, entries)
			}
			if entries, _ := os.ReadDir(filepath.Join(e.dir, "tmp")); len(entries) != 0 {
				t.Errorf("tmp holds %v", entries)
			}
		})
	}
}

func sumOf(b []byte) []byte {
	s := hexSum(string(b))
	out, _ := hex.DecodeString(s)
	return out
}

// TestInstallBackoff: automatic installs back off after a failure (30 s, doubling);
// Install ignores the backoff.
func TestInstallBackoff(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	latest := "/" + repoYtDlpNightly + "/releases/latest"
	if err := e.ensure(t); codeOf(err) != CodeHTTPStatus {
		t.Fatalf("first Ensure: %v", err)
	}
	if e.rs.hit(latest) != 1 {
		t.Fatalf("%d requests for the latest release", e.rs.hit(latest))
	}
	// Within the backoff, Ensure fails at once with the last error.
	if err := e.ensure(t); !errors.Is(err, sitemedia.ErrToolsUnavailable) || codeOf(err) != CodeHTTPStatus || e.rs.hit(latest) != 1 {
		t.Errorf("during the backoff: %v, %d requests", err, e.rs.hit(latest))
	}
	e.clock.Advance(31 * time.Second)
	if err := e.ensure(t); codeOf(err) != CodeHTTPStatus || e.rs.hit(latest) != 2 {
		t.Errorf("after 31 s: %v, %d requests", err, e.rs.hit(latest))
	}
	// The second failure doubles the wait.
	e.clock.Advance(31 * time.Second)
	_ = e.ensure(t)
	if e.rs.hit(latest) != 2 {
		t.Errorf("31 s into a 60 s backoff: %d requests", e.rs.hit(latest))
	}
	// The user's Install ignores the backoff, and succeeds once the release exists.
	e.publishBoth(t, "2026.10.20", "2.5.0")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := e.m.Install(ctx, false)
	if err != nil || !s.Ready || e.rs.hit(latest) != 3 {
		t.Fatalf("Install: %v, %+v, %d requests", err, s, e.rs.hit(latest))
	}
	if err := e.ensure(t); err != nil {
		t.Errorf("Ensure after Install: %v", err)
	}
}

// TestInstallDiskFull: an install needs 256 MiB free.
func TestInstallDiskFull(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(_ *Config, h *hooks) { h.freeSpace = func(string) (uint64, error) { return 100 << 20, nil } })
	e.publishBoth(t, "2026.10.20", "2.5.0")
	err := e.ensure(t)
	if codeOf(err) != CodeDiskFull || !strings.HasSuffix(err.Error(), "not enough free disk space to install yt-dlp") {
		t.Fatalf("Ensure: %v", err)
	}
	if e.info(ToolDeno).ErrorCode != CodeDiskFull || e.rs.total() != 0 {
		t.Errorf("deno %+v, %d requests", e.info(ToolDeno), e.rs.total())
	}
}

// TestSiteMediaOff: no tool is looked for or installed while site media is off.
func TestSiteMediaOff(t *testing.T) {
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	e.m.Configure(false, true, "")
	if err := e.ensure(t); !errors.Is(err, sitemedia.ErrSiteMediaOff) {
		t.Errorf("Ensure: %v", err)
	}
	if _, err := e.m.Install(context.Background(), true); !errors.Is(err, sitemedia.ErrSiteMediaOff) {
		t.Errorf("Install: %v", err)
	}
	if _, err := e.m.Acquire(context.Background()); !errors.Is(err, sitemedia.ErrSiteMediaOff) {
		t.Errorf("Acquire: %v", err)
	}
	if err := e.m.CheckUpdate(context.Background()); err != nil {
		t.Errorf("CheckUpdate: %v", err)
	}
	s := e.m.Status()
	if s.Enabled || s.Ready || s.Tools[0].State != StateDisabled || s.Tools[1].State != StateDisabled || e.rs.total() != 0 {
		t.Errorf("status %+v, %d requests", s, e.rs.total())
	}
	e.m.Configure(true, true, "")
	if err := e.ensure(t); err != nil {
		t.Errorf("Ensure after turning it on: %v", err)
	}
}

// TestEmbeddedKeyRequired: without the real embedded key, yt-dlp is never installed.
func TestEmbeddedKeyRequired(t *testing.T) {
	e := newTestEnv(t, func(c *Config, _ *hooks) { c.YtDlpKeys = nil })
	if e.m.keyErr == nil {
		t.Skip("the embedded yt-dlp key is in place")
	}
	e.publishBoth(t, "2026.10.20", "2.5.0")
	if err := e.ensure(t); codeOf(err) != CodeSignature {
		t.Errorf("Ensure: %v, want a signature failure", err)
	}
	if e.rs.hit("/"+repoYtDlpNightly+"/releases/latest") != 0 {
		t.Error("yt-dlp was looked for without a key to check it")
	}
}

func TestErrorTexts(t *testing.T) {
	cases := map[string]*Error{
		"could not download yt-dlp (network)":                            {Tool: ToolYtDlp, Code: CodeNetwork},
		"could not download Deno (TLS)":                                  {Tool: ToolDeno, Code: CodeTLS},
		"yt-dlp failed its signature check":                              {Tool: ToolYtDlp, Code: CodeSignature},
		"deno.exe is not signed by its publisher":                        {Tool: ToolDeno, Code: CodeAuthenticode, GOOS: "windows"},
		"no yt-dlp build for this platform":                              {Tool: ToolYtDlp, Code: CodeUnsupportedPlatform},
		"no Deno build for this platform; YouTube offers fewer formats":  {Tool: ToolDeno, Code: CodeUnsupportedPlatform},
		"could not install yt-dlp: " + strings.Repeat("x", 200)[:200-26]: {Tool: ToolYtDlp, Code: CodeInternal, Detail: strings.Repeat("x", 300)},
		"could not install Deno: line one line two":                      {Tool: ToolDeno, Code: CodeInternal, Detail: "line one\nline two"},
		"not enough free disk space to install Deno":                     {Tool: ToolDeno, Code: CodeDiskFull},
		"could not download yt-dlp (HTTP 503)":                           {Tool: ToolYtDlp, Code: CodeHTTPStatus, Detail: "HTTP 503"},
		"the downloaded Deno does not run":                               {Tool: ToolDeno, Code: CodeSmokeTest},
		"Deno failed its checksum check":                                 {Tool: ToolDeno, Code: CodeChecksum},
	}
	for want, e := range cases {
		if got := e.Error(); got != want {
			t.Errorf("%+v: %q, want %q", e, got, want)
		}
		if len(e.Error()) > maxErrorText {
			t.Errorf("%q longer than %d bytes", e.Error(), maxErrorText)
		}
	}
	if got := installing(34).Error(); got != "downloading yt-dlp and Deno (34%)" {
		t.Errorf("installing text %q", got)
	}
	if ErrorCode(nil) != "" || ErrorCode(errors.New("x")) != CodeInternal || ErrorCode(unavailable(&Error{Code: CodeTLS})) != CodeTLS {
		t.Error("ErrorCode")
	}
}
