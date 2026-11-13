package tools

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"downex/internal/update"
)

// The tests of this file reach GitHub and run only when asked for
// (docs/SITE-MEDIA-SPEC.md §8.3); they are never part of the default test run.

// TestRealRelease (DOWNEX_REAL_TOOLS=1) fetches the latest SHA2-256SUMS and signature
// of both yt-dlp channels and verifies them with the embedded key, and parses the Deno
// .sha256sum files of every platform.
func TestRealRelease(t *testing.T) {
	if os.Getenv("DOWNEX_REAL_TOOLS") != "1" {
		t.Skip("set DOWNEX_REAL_TOOLS=1 to check the real releases on GitHub")
	}
	keys, err := pinnedKeys(nil)
	if err != nil {
		t.Fatalf("embedded yt-dlp key: %v", err)
	}
	f := &fetcher{client: secureClient(update.NewHTTPClient(nil)), headerTimeout: headerTimeout, stallWindow: stallWindow, stallMinBytes: stallMinRate * 60}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const base = "https://github.com"
	for _, ch := range []string{ChannelStable, ChannelNightly} {
		repo := ytdlpRepo(ch)
		tag, err := f.latestTag(ctx, base, repo)
		if err != nil {
			t.Fatalf("%s: latest tag: %v", ch, err)
		}
		if _, ok := versionOfTag(ToolYtDlp, tag); !ok {
			t.Fatalf("%s: unexpected tag %q", ch, tag)
		}
		sums, err := f.bytes(ctx, releaseURL(base, repo, tag, ytdlpSums), maxSumsFile)
		if err != nil {
			t.Fatalf("%s %s: SUMS: %v", ch, tag, err)
		}
		sig, err := f.bytes(ctx, releaseURL(base, repo, tag, ytdlpSumsSig), maxSigFile)
		if err != nil {
			t.Fatalf("%s %s: signature: %v", ch, tag, err)
		}
		created, err := verifyDetached(keys, sums, sig, time.Now())
		if err != nil {
			t.Fatalf("%s %s: %v", ch, tag, err)
		}
		for _, p := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
			a, _ := AssetNames(p[0], p[1], false)
			if _, err := sumFor(sums, a); err != nil {
				t.Errorf("%s %s: %v", ch, tag, err)
			}
			// The onedir zip is what the manager installs.
			m := &Manager{assets: assetsFor(p[0], p[1], false)}
			if got, err := m.ytdlpAssets(sums); err != nil || len(got) == 0 || !got[0].onedir {
				t.Errorf("%s %s %s/%s: no onedir zip (%+v, %v)", ch, tag, p[0], p[1], got, err)
			}
		}
		t.Logf("yt-dlp %s %s: signed %v", ch, tag, created)
	}
	tag, err := f.latestTag(ctx, base, repoDeno)
	if err != nil {
		t.Fatalf("deno: latest tag: %v", err)
	}
	for _, p := range [][2]string{{"windows", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}} {
		a := assetsFor(p[0], p[1], false)
		data, err := f.bytes(ctx, releaseURL(base, repoDeno, tag, a.Deno+".sha256sum"), maxSumsFile)
		if err != nil {
			t.Fatalf("deno %s %s: %v", tag, a.Deno, err)
		}
		if _, err := parseSHA256File(data, a.Deno); err != nil {
			t.Errorf("deno %s %s: %v\n%q", tag, a.Deno, err, data)
		}
		if data, err := f.bytes(ctx, releaseURL(base, repoDeno, tag, denoExeSums(a.DenoTarget)), maxSumsFile); err == nil {
			t.Logf("deno %s publishes %s: %q", tag, denoExeSums(a.DenoTarget), data)
		}
	}
}

// TestRealInstall (DOWNEX_REAL_TOOLS_INSTALL=1) installs both tools from GitHub into a
// temporary folder, which runs their smoke tests.
func TestRealInstall(t *testing.T) {
	if os.Getenv("DOWNEX_REAL_TOOLS_INSTALL") != "1" {
		t.Skip("set DOWNEX_REAL_TOOLS_INSTALL=1 to install the real tools into a temporary folder")
	}
	m, err := New(Config{Dir: t.TempDir(), Client: update.NewHTTPClient(nil),
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	if err := m.Ensure(ctx); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, in := range m.Status().Tools {
		t.Logf("%s %s at %s, verified %q, signer %q", in.Name, in.Version, in.Path, in.Verified, in.Signer)
	}
	// Where the release has an onedir zip it was unpacked (its root executable found and
	// renamed, _internal/ next to it) and ran its smoke test from there.
	st := loadState(m.lay)
	t.Logf("yt-dlp layout %q", st.YtDlp.Layout)
	if m.assets.YtDlpZip != "" && st.YtDlp.Layout != layoutOnedir {
		t.Errorf("yt-dlp installed as %q, want %q", st.YtDlp.Layout, layoutOnedir)
	}
}
