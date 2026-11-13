package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestOnedirAssetsFromSums: every platform with a yt-dlp build installs the onedir zip
// that upstream's SHA2-256SUMS (testdata, release 2026.08.19) lists for it, and falls
// back to the onefile executable only where the list names no zip.
func TestOnedirAssetsFromSums(t *testing.T) {
	sums, err := os.ReadFile(filepath.Join("testdata", "ytdlp-2026.08.19", ytdlpSums))
	if err != nil {
		t.Skipf("testdata of the real 2026.08.19 release missing (%v)", err)
	}
	cases := []struct {
		goos, goarch string
		musl         bool
		zip, zipExe  string
	}{
		{"windows", "amd64", false, "yt-dlp_win.zip", "yt-dlp.exe"},
		{"windows", "arm64", false, "yt-dlp_win_arm64.zip", "yt-dlp_arm64.exe"},
		{"windows", "386", false, "yt-dlp_win_x86.zip", "yt-dlp_x86.exe"},
		{"darwin", "amd64", false, "yt-dlp_macos.zip", "yt-dlp_macos"},
		{"darwin", "arm64", false, "yt-dlp_macos.zip", "yt-dlp_macos"},
		{"linux", "amd64", false, "yt-dlp_linux.zip", "yt-dlp_linux"},
		{"linux", "amd64", true, "yt-dlp_musllinux.zip", "yt-dlp_musllinux"},
		{"linux", "arm64", false, "yt-dlp_linux_aarch64.zip", "yt-dlp_linux_aarch64"},
		{"linux", "arm64", true, "yt-dlp_musllinux_aarch64.zip", "yt-dlp_musllinux_aarch64"},
		{"linux", "arm", false, "yt-dlp_linux_armv7l.zip", "yt-dlp_linux_armv7l"},
		{"linux", "arm", true, "", ""},
		{"linux", "riscv64", false, "", ""},
		{"freebsd", "amd64", false, "", ""},
	}
	for _, c := range cases {
		a := assetsFor(c.goos, c.goarch, c.musl)
		if a.YtDlpZip != c.zip || a.YtDlpZipExe != c.zipExe {
			t.Errorf("%s/%s musl=%v: zip %q with %q, want %q with %q", c.goos, c.goarch, c.musl, a.YtDlpZip, a.YtDlpZipExe, c.zip, c.zipExe)
		}
		m := &Manager{assets: a}
		got, err := m.ytdlpAssets(sums)
		if c.zip == "" {
			if a.hasYtDlp() || err == nil {
				t.Errorf("%s/%s musl=%v: a build without a release file: %v, %v", c.goos, c.goarch, c.musl, got, err)
			}
			continue
		}
		if err != nil || len(got) == 0 || got[0].name != c.zip || !got[0].onedir {
			t.Fatalf("%s/%s musl=%v: %+v, %v", c.goos, c.goarch, c.musl, got, err)
		}
		if want, _ := sumFor(sums, c.zip); got[0].want != want {
			t.Errorf("%s: SHA-256 %x", c.zip, got[0].want)
		}
		// The onefile executable follows where the release has it (not for armv7l).
		if wantOnefile := len(namedSums(sums, a.YtDlp)) == 1; wantOnefile != (len(got) == 2) {
			t.Errorf("%s/%s musl=%v: %d release files", c.goos, c.goarch, c.musl, len(got))
		}
	}
	// A release that lists no zip installs the onefile executable.
	m := &Manager{assets: assetsFor("windows", "amd64", false)}
	only := []byte(hexSum("x") + "  yt-dlp.exe\n")
	if got, err := m.ytdlpAssets(only); err != nil || len(got) != 1 || got[0].name != "yt-dlp.exe" || got[0].onedir {
		t.Errorf("onefile only: %+v, %v", got, err)
	}
	// A list naming the zip twice is refused, as is one naming neither file.
	twice := []byte(hexSum("a") + "  yt-dlp_win.zip\n" + hexSum("b") + "  yt-dlp_win.zip\n" + hexSum("x") + "  yt-dlp.exe\n")
	if _, err := m.ytdlpAssets(twice); codeOf(err) != CodeChecksum {
		t.Errorf("zip named twice: %v", err)
	}
	if _, err := m.ytdlpAssets([]byte(hexSum("x") + "  yt-dlp_linux\n")); codeOf(err) != CodeChecksum || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("neither named: %v", err)
	}
}

// onedirRelease publishes yt-dlp tag on the nightly channel as an onedir zip of the
// stand-in (with withOnefile also the onefile executable) and Deno 2.5.0.
func (e *testEnv) onedirRelease(t *testing.T, tag string, withOnefile bool) {
	t.Helper()
	files := map[string][]byte{e.m.assets.YtDlpZip: onedirZip(t, e.m.assets.YtDlpZipExe, testExeBytes(t))}
	if withOnefile {
		files[e.m.assets.YtDlp] = testExeBytes(t)
	}
	e.rs.publishYtDlpFiles(repoYtDlpNightly, tag, files, e.signer, e.clock.Now().Add(-time.Hour))
	e.rs.publishDeno(t, "v2.5.0", testExeBytes(t), false)
}

// assetHits is the number of requests for release file asset of tag (nightly).
func (e *testEnv) assetHits(tag, asset string) int {
	return e.rs.hit("/" + repoYtDlpNightly + "/releases/download/" + tag + "/" + asset)
}

// onedirAvailable skips tests on platforms without a yt-dlp zip.
func onedirAvailable(t *testing.T, e *testEnv) {
	t.Helper()
	if e.m.assets.YtDlpZip == "" {
		t.Skip("upstream publishes no onedir yt-dlp for this platform")
	}
}

// runOnedirMarker runs yt-dlp from lease l with --onedir-marker: the stand-in prints
// the marker its onedir zip unpacked into _internal/ next to it.
func runOnedirMarker(t *testing.T, e *testEnv, l *Lease) (string, error) {
	t.Helper()
	dir, err := NewRunDir(l.WorkRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	res, err := Exec(context.Background(), ExecSpec{
		Path: l.YtDlp, Args: []string{"--onedir-marker"}, Dir: dir, Timeout: 30 * time.Second,
		Env: e.m.Env(EnvSpec{TempDir: filepath.Join(dir, tmpName), DenoCache: l.DenoCache}),
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exit code %d", res.ExitCode)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// TestOnedirInstall: the first install takes the onedir zip (never the onefile
// executable), unpacks it into the version folder with its file list, records the
// layout, and runs yt-dlp from there with _internal/ next to it; a restart trusts the
// checked copy without requests.
func TestOnedirInstall(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	onedirAvailable(t, e)
	e.onedirRelease(t, "2026.10.20", true)
	if err := e.ensure(t); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if z, o := e.assetHits("2026.10.20", e.m.assets.YtDlpZip), e.assetHits("2026.10.20", e.m.assets.YtDlp); z != 1 || o != 0 {
		t.Errorf("zip fetched %d times, onefile executable %d times", z, o)
	}
	exe := exeName(ToolYtDlp, runtime.GOOS)
	vdir := filepath.Join(e.dir, ToolYtDlp, "2026.10.20")
	in := e.info(ToolYtDlp)
	if in.State != StateReady || !in.Managed || in.Version != "2026.10.20" || in.Verified != "openpgp+sha256" ||
		!samePath(in.Path, filepath.Join(vdir, exe)) {
		t.Errorf("yt-dlp %+v", in)
	}
	st := loadState(e.m.lay)
	sum := sha256.Sum256(testExeBytes(t))
	if st.YtDlp.Layout != layoutOnedir || !validSHA256(st.YtDlp.Manifest) || st.YtDlp.SHA256 != hex.EncodeToString(sum[:]) ||
		st.YtDlp.Size != int64(len(testExeBytes(t))) || st.YtDlp.File != "yt-dlp/2026.10.20/"+exe {
		t.Errorf("tools.json %+v", st.YtDlp.entry)
	}
	if got := readAll(t, filepath.Join(vdir, "_internal", "marker.txt")); string(got) != onedirMarker {
		t.Errorf("marker %q", got)
	}
	if fs := sha256.Sum256(readAll(t, filepath.Join(vdir, manifestName))); hex.EncodeToString(fs[:]) != st.YtDlp.Manifest {
		t.Error("the file list does not have the recorded SHA-256")
	}
	if e.m.assets.YtDlpZipExe != exe {
		if _, err := os.Stat(filepath.Join(vdir, e.m.assets.YtDlpZipExe)); err == nil {
			t.Error("the executable kept its archive name")
		}
	}
	names, _ := os.ReadDir(filepath.Join(e.dir, ToolYtDlp))
	if len(names) != 1 {
		t.Errorf("yt-dlp folder holds %v", names)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "tmp")); len(entries) != 0 {
		t.Errorf("tmp holds %v", entries)
	}
	// Exec runs the onedir yt-dlp from its version folder.
	l, err := e.m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !l.YtDlpManaged || !samePath(l.YtDlp, filepath.Join(vdir, exe)) {
		t.Errorf("lease %+v", l)
	}
	if got, err := runOnedirMarker(t, e, l); err != nil || got != onedirMarker {
		t.Errorf("yt-dlp --onedir-marker: %q, %v", got, err)
	}
	l.Release()
	// A restart checks every file and uses the copy without requests.
	before := e.rs.total()
	e.restart(t, nil)
	if err := e.ensure(t); err != nil || e.rs.total() != before {
		t.Errorf("restart: %v, %d requests", err, e.rs.total()-before)
	}
	if in := e.info(ToolYtDlp); in.State != StateReady || !in.Managed {
		t.Errorf("after the restart: %+v", in)
	}
	// An update check with nothing newer downloads nothing.
	if err := e.check(t); err != nil || e.assetHits("2026.10.20", ytdlpSums) != 1 {
		t.Errorf("up-to-date check: %v, %d checksum lists", err, e.assetHits("2026.10.20", ytdlpSums))
	}
}

// TestOnedirDiscoveryDetectsChange: a changed, missing or added-to file of the onedir
// copy's list, or a changed list, makes discovery drop the managed copy, and Ensure
// installs it again.
func TestOnedirDiscoveryDetectsChange(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, vdir string){
		"modified file": func(t *testing.T, vdir string) {
			p := filepath.Join(vdir, "_internal", "base_library.zip")
			data := readAll(t, p)
			data[0] ^= 0xff // same size, other content
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing file": func(t *testing.T, vdir string) {
			if err := os.Remove(filepath.Join(vdir, "_internal", "Cryptodome", "Cipher", "_raw_ecb.pyd")); err != nil {
				t.Fatal(err)
			}
		},
		"changed list": func(t *testing.T, vdir string) {
			p := filepath.Join(vdir, manifestName)
			data := bytes.Replace(readAll(t, p), []byte(`"files"`), []byte(`"files" `), 1)
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing list": func(t *testing.T, vdir string) {
			if err := os.Remove(filepath.Join(vdir, manifestName)); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t)
			onedirAvailable(t, e)
			e.onedirRelease(t, "2026.10.20", false)
			if err := e.ensure(t); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			vdir := filepath.Join(e.dir, ToolYtDlp, "2026.10.20")
			change(t, vdir)
			if err := e.m.rediscover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if in := e.info(ToolYtDlp); in.State == StateReady || in.Version != "" {
				t.Fatalf("the changed copy still serves: %+v", in)
			}
			if _, err := e.m.Acquire(context.Background()); err != nil {
				t.Fatalf("Acquire (installs again): %v", err)
			}
			if n := e.assetHits("2026.10.20", e.m.assets.YtDlpZip); n != 2 {
				t.Errorf("zip fetched %d times, want 2", n)
			}
			if !e.m.verifyEntry(ToolYtDlp, loadState(e.m.lay).YtDlp.entry) {
				t.Error("the installed copy does not verify")
			}
		})
	}
	// Files the list does not name are not looked at.
	e := newTestEnv(t)
	onedirAvailable(t, e)
	e.onedirRelease(t, "2026.10.20", false)
	if err := e.ensure(t); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, ToolYtDlp, "2026.10.20", "_internal", "cache.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.m.rediscover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if in := e.info(ToolYtDlp); in.State != StateReady {
		t.Errorf("an extra file dropped the copy: %+v", in)
	}
}

// legacy rewrites tools.json as a Downex that did not record layouts wrote it, with
// the manager closed (a restart).
func (e *testEnv) legacy(t *testing.T) {
	t.Helper()
	e.restart(t, func() {
		st := loadState(e.m.lay)
		if st.YtDlp.Layout != layoutOnefile {
			t.Fatalf("layout %q, want a onefile copy", st.YtDlp.Layout)
		}
		st.YtDlp.Layout = ""
		if err := saveState(e.m.lay, st); err != nil {
			t.Fatal(err)
		}
	})
}

// TestOnedirMigration: a onefile copy installed before layouts were recorded keeps
// serving (no download at start or on Ensure) and the next update check replaces it by
// its own release's onedir build; a release without one marks it onefile for good.
func TestOnedirMigration(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	onedirAvailable(t, e)
	e.installed(t, "2026.10.01", "2.5.0")
	e.legacy(t)
	before := e.rs.total()
	if err := e.ensure(t); err != nil || e.rs.total() != before {
		t.Fatalf("Ensure with the onefile copy: %v, %d requests", err, e.rs.total()-before)
	}
	if in := e.info(ToolYtDlp); in.State != StateReady || in.Version != "2026.10.01" {
		t.Fatalf("onefile copy %+v", in)
	}
	// The release has no zip: the check looks once and marks the copy onefile.
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	if n := e.assetHits("2026.10.01", ytdlpSums); n != 2 {
		t.Errorf("checksum list fetched %d times, want 2", n)
	}
	if st := loadState(e.m.lay); st.YtDlp.Layout != layoutOnefile || st.YtDlp.Version != "2026.10.01" {
		t.Errorf("tools.json %+v", st.YtDlp.entry)
	}
	if err := e.check(t); err != nil || e.assetHits("2026.10.01", ytdlpSums) != 2 {
		t.Errorf("second check: %v, %d checksum lists", err, e.assetHits("2026.10.01", ytdlpSums))
	}

	// Now with a release that has a zip.
	e2 := newTestEnv(t)
	e2.installed(t, "2026.10.01", "2.5.0")
	e2.legacy(t)
	// The same release now lists its onedir zip too (as upstream's do).
	files := map[string][]byte{e2.m.assets.YtDlpZip: onedirZip(t, e2.m.assets.YtDlpZipExe, testExeBytes(t)), e2.m.assets.YtDlp: testExeBytes(t)}
	e2.rs.publishYtDlpFiles(repoYtDlpNightly, "2026.10.01", files, e2.signer, e2.clock.Now().Add(-time.Hour))
	if err := e2.ensure(t); err != nil || e2.assetHits("2026.10.01", e2.m.assets.YtDlpZip) != 0 {
		t.Fatalf("Ensure migrated: %v", err)
	}
	// A lease holds the onefile copy: the check leaves it.
	l, err := e2.m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sums := e2.assetHits("2026.10.01", ytdlpSums)
	if err := e2.check(t); err != nil || e2.assetHits("2026.10.01", e2.m.assets.YtDlpZip) != 0 || loadState(e2.m.lay).YtDlp.Layout != "" {
		t.Errorf("check with a lease: %v, %d zips, %+v", err, e2.assetHits("2026.10.01", e2.m.assets.YtDlpZip), loadState(e2.m.lay).YtDlp.entry)
	}
	if e2.assetHits("2026.10.01", ytdlpSums) != sums {
		t.Error("the checksum list was fetched while the copy was leased")
	}
	l.Release()
	if err := e2.check(t); err != nil {
		t.Fatalf("check: %v", err)
	}
	st := loadState(e2.m.lay)
	if st.YtDlp.Layout != layoutOnedir || st.YtDlp.Version != "2026.10.01" || st.YtDlp.Previous != nil {
		t.Fatalf("after the migration: %+v (previous %+v)", st.YtDlp.entry, st.YtDlp.Previous)
	}
	if n := e2.assetHits("2026.10.01", e2.m.assets.YtDlpZip); n != 1 {
		t.Errorf("zip fetched %d times", n)
	}
	in := e2.info(ToolYtDlp)
	if in.State != StateReady || in.Version != "2026.10.01" || in.Error != "" {
		t.Errorf("after the migration: %+v", in)
	}
	l, err = e2.m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runOnedirMarker(t, e2, l); err != nil || got != onedirMarker {
		t.Errorf("migrated copy: %q, %v", got, err)
	}
	l.Release()
	vparent := filepath.Join(e2.dir, ToolYtDlp)
	waitFor(t, "only the version folder to remain", func() bool {
		names, _ := os.ReadDir(vparent)
		return len(names) == 1 && names[0].Name() == "2026.10.01"
	})
	// Migrated once: later checks do not look again.
	sums = e2.assetHits("2026.10.01", ytdlpSums)
	if err := e2.check(t); err != nil || e2.assetHits("2026.10.01", ytdlpSums) != sums {
		t.Errorf("check after the migration: %v", err)
	}
	// A newer release replaces it as usual; the onedir copy becomes the previous one.
	e2.clock.Advance(24 * time.Hour)
	e2.rs.publishYtDlpFiles(repoYtDlpNightly, "2026.10.02", files, e2.signer, e2.clock.Now())
	if err := e2.check(t); err != nil || e2.info(ToolYtDlp).Version != "2026.10.02" {
		t.Fatalf("update: %v, %+v", err, e2.info(ToolYtDlp))
	}
	st = loadState(e2.m.lay)
	if st.YtDlp.Layout != layoutOnedir || st.YtDlp.Previous == nil || st.YtDlp.Previous.Layout != layoutOnedir ||
		!e2.m.verifyEntry(ToolYtDlp, *st.YtDlp.Previous) {
		t.Errorf("tools.json after the update %+v, previous %+v", st.YtDlp.entry, st.YtDlp.Previous)
	}
}

// TestOnedirMigrationToNewerRelease: a legacy onefile copy and a newer release with a
// zip: the update installs the onedir build and keeps the onefile copy as the previous
// one, which still verifies.
func TestOnedirMigrationToNewerRelease(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	onedirAvailable(t, e)
	e.installed(t, "2026.10.01", "2.5.0")
	e.legacy(t)
	e.clock.Advance(24 * time.Hour)
	files := map[string][]byte{e.m.assets.YtDlpZip: onedirZip(t, e.m.assets.YtDlpZipExe, testExeBytes(t)), e.m.assets.YtDlp: testExeBytes(t)}
	e.rs.publishYtDlpFiles(repoYtDlpNightly, "2026.10.02", files, e.signer, e.clock.Now())
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	st := loadState(e.m.lay)
	if st.YtDlp.Version != "2026.10.02" || st.YtDlp.Layout != layoutOnedir || st.YtDlp.Previous == nil ||
		st.YtDlp.Previous.Version != "2026.10.01" || st.YtDlp.Previous.Layout != "" || !e.m.verifyEntry(ToolYtDlp, *st.YtDlp.Previous) {
		t.Errorf("tools.json %+v, previous %+v", st.YtDlp.entry, st.YtDlp.Previous)
	}
	if e.assetHits("2026.10.02", e.m.assets.YtDlp) != 0 {
		t.Error("the onefile executable was fetched")
	}
}

// TestOnedirMigrationBrokenBuild: an onedir build that does not run leaves the onefile
// copy in place and serving, reports the failure, and is not tried again for that
// release.
func TestOnedirMigrationBrokenBuild(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	onedirAvailable(t, e)
	e.installed(t, "2026.10.01", "2.5.0")
	e.legacy(t)
	onefile := loadState(e.m.lay).YtDlp.entry
	files := map[string][]byte{
		e.m.assets.YtDlpZip: onedirZip(t, e.m.assets.YtDlpZipExe, []byte("not a program at all")),
		e.m.assets.YtDlp:    testExeBytes(t),
	}
	e.rs.publishYtDlpFiles(repoYtDlpNightly, "2026.10.01", files, e.signer, e.clock.Now().Add(-time.Hour))
	err := e.check(t)
	if codeOf(err) != CodeSmokeTest {
		t.Fatalf("check: %v, want a smoke test failure", err)
	}
	in := e.info(ToolYtDlp)
	if in.State != StateReady || in.Version != "2026.10.01" || in.ErrorCode != CodeSmokeTest {
		t.Errorf("after the failed migration: %+v", in)
	}
	st := loadState(e.m.lay)
	cur := st.YtDlp.entry
	cur.Layout = ""
	if st.YtDlp.Layout != layoutOnefile || !sameCopy(cur, onefile) || !e.m.verifyEntry(ToolYtDlp, st.YtDlp.entry) {
		t.Errorf("the onefile copy was not kept: %+v", st.YtDlp.entry)
	}
	if _, err := os.Stat(filepath.Join(e.dir, ToolYtDlp, "2026.10.01", "_internal")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the failed onedir build is still in place: %v", err)
	}
	if err := e.ensure(t); err != nil {
		t.Errorf("Ensure: %v", err)
	}
	if err := e.check(t); err != nil || e.assetHits("2026.10.01", e.m.assets.YtDlpZip) != 1 {
		t.Errorf("tried again: %v, %d zips", err, e.assetHits("2026.10.01", e.m.assets.YtDlpZip))
	}
	waitFor(t, "only the version folder to remain", func() bool {
		names, _ := os.ReadDir(filepath.Join(e.dir, ToolYtDlp))
		return len(names) == 1
	})
}

// TestOnedirRefused: a zip that is not the one the signed list names is refused and
// nothing is installed; an onedir build that does not unpack or run falls back to the
// release's onefile executable, or fails where the release has none.
func TestOnedirRefused(t *testing.T) {
	t.Parallel()
	escape := func(e *testEnv) string { return filepath.Join(e.dir, ToolYtDlp, "escaped") }
	cases := []struct {
		name        string
		zip         func(t *testing.T, e *testEnv) []byte
		tamper      bool // serve other bytes than the list names
		withOnefile bool
		code        string // "" = the onefile executable is installed
		text        string
	}{
		{"tampered zip", func(t *testing.T, e *testEnv) []byte { return onedirZip(t, e.m.assets.YtDlpZipExe, testExeBytes(t)) },
			true, true, CodeChecksum, "yt-dlp failed its checksum check"},
		{"traversal", func(t *testing.T, e *testEnv) []byte {
			return buildZip(t, true, zipEntry{name: e.m.assets.YtDlpZipExe, data: testExeBytes(t)}, zipEntry{name: "../escaped", data: []byte("x")})
		}, false, false, CodeInternal, "could not install yt-dlp: unexpected archive layout"},
		{"traversal with a onefile build", func(t *testing.T, e *testEnv) []byte {
			return buildZip(t, true, zipEntry{name: e.m.assets.YtDlpZipExe, data: testExeBytes(t)}, zipEntry{name: "../escaped", data: []byte("x")})
		}, false, true, "", ""},
		{"symlink", func(t *testing.T, e *testEnv) []byte {
			return buildZip(t, true, zipEntry{name: e.m.assets.YtDlpZipExe, data: testExeBytes(t)},
				zipEntry{name: "_internal/python.dll", data: []byte("../../../escaped"), mode: fs.ModeSymlink | 0o777})
		}, false, false, CodeInternal, "could not install yt-dlp: unexpected archive layout"},
		{"no executable", func(t *testing.T, e *testEnv) []byte {
			return buildZip(t, true, zipEntry{name: "_internal/python.dll", data: []byte("x")})
		}, false, false, CodeInternal, "could not install yt-dlp: unexpected archive layout"},
		{"does not run", func(t *testing.T, e *testEnv) []byte {
			return onedirZip(t, e.m.assets.YtDlpZipExe, []byte("not a program at all"))
		}, false, false, CodeSmokeTest, "the downloaded yt-dlp does not run"},
		{"does not run with a onefile build", func(t *testing.T, e *testEnv) []byte {
			return onedirZip(t, e.m.assets.YtDlpZipExe, []byte("not a program at all"))
		}, false, true, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t)
			onedirAvailable(t, e)
			z := c.zip(t, e)
			files := map[string][]byte{e.m.assets.YtDlpZip: z}
			if c.withOnefile {
				files[e.m.assets.YtDlp] = testExeBytes(t)
			}
			e.rs.publishYtDlpFiles(repoYtDlpNightly, "2026.10.20", files, e.signer, e.clock.Now().Add(-time.Hour))
			e.rs.publishDeno(t, "v2.5.0", testExeBytes(t), false)
			if c.tamper {
				bad := bytes.Clone(z)
				bad[len(bad)/2] ^= 0xff
				e.rs.put(repoYtDlpNightly, "2026.10.20", e.m.assets.YtDlpZip, bad)
			}
			err := e.ensure(t)
			if _, serr := os.Stat(escape(e)); serr == nil {
				t.Error("an entry was written outside the version folder")
			}
			if entries, _ := os.ReadDir(filepath.Join(e.dir, "tmp")); len(entries) != 0 {
				t.Errorf("tmp holds %v", entries)
			}
			if c.code == "" {
				st := loadState(e.m.lay)
				if err != nil || st.YtDlp.Layout != layoutOnefile || e.assetHits("2026.10.20", e.m.assets.YtDlp) != 1 {
					t.Errorf("fallback to the onefile executable: %v, %+v", err, st.YtDlp.entry)
				}
				if _, err := os.Stat(filepath.Join(e.dir, ToolYtDlp, "2026.10.20", "_internal")); err == nil {
					t.Error("the onedir build was left in the version folder")
				}
				return
			}
			if codeOf(err) != c.code || !strings.HasSuffix(err.Error(), ": "+c.text) {
				t.Fatalf("Ensure: %v (code %q), want %s %q", err, codeOf(err), c.code, c.text)
			}
			if in := e.info(ToolYtDlp); in.State != StateFailed || in.Version != "" {
				t.Errorf("yt-dlp %+v", in)
			}
			if entries, _ := os.ReadDir(filepath.Join(e.dir, ToolYtDlp)); len(entries) != 0 {
				t.Errorf("yt-dlp folder holds %v", entries)
			}
			if e.assetHits("2026.10.20", e.m.assets.YtDlp) != 0 {
				t.Error("the onefile executable was fetched")
			}
		})
	}
}

// TestVerifyFolder: an onedir folder verifies only with its recorded executable and
// file list, and only while every listed file matches; a list naming a path outside
// the folder never verifies.
func TestVerifyFolder(t *testing.T) {
	exe := []byte("the program")
	archive := writeZip(t, zipEntry{name: "yt-dlp.exe", data: exe}, zipEntry{name: "_internal/a.dll", data: []byte("a")},
		zipEntry{name: "_internal/b.dll", data: []byte("b")})
	fresh := func(t *testing.T) (string, entry) {
		dir := t.TempDir()
		u, err := unzipTree(archive, dir, "yt-dlp.exe", "yt-dlp.exe", treeLimits{bytes: 1 << 20, entries: 8})
		if err != nil {
			t.Fatal(err)
		}
		ms, err := writeManifest(dir, u.files)
		if err != nil {
			t.Fatal(err)
		}
		return dir, entry{SHA256: hex.EncodeToString(u.exeSum[:]), Size: u.exeSize, Layout: layoutOnedir, Manifest: hex.EncodeToString(ms[:])}
	}
	dir, e := fresh(t)
	if !verifyFolder(dir, "yt-dlp.exe", e) {
		t.Fatal("a fresh folder does not verify")
	}
	onefile := e
	onefile.Layout, onefile.Manifest = layoutOnefile, ""
	if !verifyFolder(dir, "yt-dlp.exe", onefile) {
		t.Error("the executable alone does not verify")
	}
	// rewrite replaces the file list and returns the entry recording it.
	rewrite := func(t *testing.T, dir string, e entry, files []manifestFile) entry {
		data, _ := json.Marshal(fileManifest{Version: manifestVersion, Files: files})
		if err := os.WriteFile(filepath.Join(dir, manifestName), data, 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(data)
		e.Manifest = hex.EncodeToString(s[:])
		return e
	}
	exeLine := manifestFile{Path: "yt-dlp.exe", Size: int64(len(exe)), SHA256: hexSum(string(exe))}
	cases := map[string]func(t *testing.T, dir string, e entry) entry{
		"other list hash": func(t *testing.T, dir string, e entry) entry { e.Manifest = hexSum("x"); return e },
		"changed file": func(t *testing.T, dir string, e entry) entry {
			_ = os.WriteFile(filepath.Join(dir, "_internal", "a.dll"), []byte("A"), 0o644)
			return e
		},
		"changed executable": func(t *testing.T, dir string, e entry) entry {
			_ = os.WriteFile(filepath.Join(dir, "yt-dlp.exe"), []byte("the progran"), 0o755)
			return e
		},
		"list without the executable": func(t *testing.T, dir string, e entry) entry {
			return rewrite(t, dir, e, []manifestFile{{Path: "_internal/a.dll", Size: 1, SHA256: hexSum("a")}})
		},
		"list outside the folder": func(t *testing.T, dir string, e entry) entry {
			outside := filepath.Join(filepath.Dir(dir), "outside")
			_ = os.WriteFile(outside, []byte("o"), 0o644)
			return rewrite(t, dir, e, []manifestFile{exeLine, {Path: "../outside", Size: 1, SHA256: hexSum("o")}})
		},
		"list of another version": func(t *testing.T, dir string, e entry) entry {
			data, _ := json.Marshal(fileManifest{Version: 2, Files: []manifestFile{exeLine}})
			_ = os.WriteFile(filepath.Join(dir, manifestName), data, 0o644)
			s := sha256.Sum256(data)
			e.Manifest = hex.EncodeToString(s[:])
			return e
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir, e := fresh(t)
			if e = change(t, dir, e); verifyFolder(dir, "yt-dlp.exe", e) {
				t.Error("verifies")
			}
		})
	}
	// A list that names only what is there, listed in another order, verifies.
	dir, e = fresh(t)
	e = rewrite(t, dir, e, []manifestFile{{Path: "_internal/b.dll", Size: 1, SHA256: hexSum("b")}, exeLine})
	if !verifyFolder(dir, "yt-dlp.exe", e) {
		t.Error("a valid rewritten list does not verify")
	}
}

// TestStateLayout: tools.json keeps an onedir entry with its file list's hash and drops
// entries whose layout fields do not fit.
func TestStateLayout(t *testing.T) {
	l := layout{dir: t.TempDir(), goos: "windows"}
	yt := entry{Version: "2026.10.10", File: l.rel(ToolYtDlp, "2026.10.10"), SHA256: hexSum("y"), Size: 10,
		Layout: layoutOnedir, Manifest: hexSum("m")}
	dn := entry{Version: "2.5.0", File: l.rel(ToolDeno, "2.5.0"), SHA256: hexSum("d"), Size: 10}
	if err := saveState(l, stateFile{YtDlp: toolRecord{entry: yt}, Deno: toolRecord{entry: dn}}); err != nil {
		t.Fatal(err)
	}
	if st := loadState(l); st.YtDlp.entry != yt || st.Deno.entry != dn {
		t.Fatalf("round trip %+v %+v", st.YtDlp.entry, st.Deno.entry)
	}
	if !strings.Contains(string(readAll(t, l.state())), `"layout": "onedir"`) {
		t.Error("tools.json does not record the layout")
	}
	bad := map[string]func(y, d *entry){
		"onedir without a list":     func(y, _ *entry) { y.Manifest = "" },
		"onedir with a short hash":  func(y, _ *entry) { y.Manifest = y.Manifest[:60] },
		"onefile with a list":       func(y, _ *entry) { y.Layout = layoutOnefile },
		"legacy with a list":        func(y, _ *entry) { y.Layout = "" },
		"unknown layout":            func(y, _ *entry) { y.Layout, y.Manifest = "flat", "" },
		"deno with a layout":        func(_, d *entry) { d.Layout = layoutOnedir; d.Manifest = hexSum("m") },
		"deno with a onefile label": func(_, d *entry) { d.Layout = layoutOnefile },
	}
	for name, change := range bad {
		y, d := yt, dn
		change(&y, &d)
		if err := saveState(l, stateFile{YtDlp: toolRecord{entry: y}, Deno: toolRecord{entry: d}}); err != nil {
			t.Fatal(err)
		}
		st := loadState(l)
		if st.YtDlp.entry == y && st.Deno.entry == d {
			t.Errorf("%s: kept", name)
		}
	}
	// Entries written before layouts were recorded still read.
	legacy := yt
	legacy.Layout, legacy.Manifest = "", ""
	if err := saveState(l, stateFile{YtDlp: toolRecord{entry: legacy}}); err != nil {
		t.Fatal(err)
	}
	if st := loadState(l); st.YtDlp.entry != legacy {
		t.Errorf("legacy entry %+v", st.YtDlp.entry)
	}
}

// TestUnzipTree: an onedir archive is unpacked whole, its executable under its new
// name; backslash separators and "." elements are understood; the list of files is
// sorted and carries what was written.
func TestUnzipTree(t *testing.T) {
	exe := []byte("the program")
	archive := writeZip(t,
		zipEntry{name: "yt-dlp_linux", data: exe},
		zipEntry{name: "_internal/", mode: fs.ModeDir | 0o755},
		zipEntry{name: "_internal/python3.dll", data: []byte("python"), mode: 0o644},
		zipEntry{name: `_internal\lib\data.bin`, data: []byte("data"), mode: 0o644},
		zipEntry{name: "./_internal/helper", data: []byte("helper"), mode: 0o755},
		zipEntry{name: "_internal/empty/", mode: fs.ModeDir | 0o755},
	)
	dir := t.TempDir()
	u, err := unzipTree(archive, dir, "yt-dlp_linux", "yt-dlp", treeLimits{bytes: 1 << 20, entries: 16})
	if err != nil {
		t.Fatalf("unzipTree: %v", err)
	}
	if u.exeSum != sha256.Sum256(exe) || u.exeSize != int64(len(exe)) {
		t.Errorf("executable %x (%d bytes)", u.exeSum, u.exeSize)
	}
	want := map[string]string{
		"_internal/helper":       "helper",
		"_internal/lib/data.bin": "data",
		"_internal/python3.dll":  "python",
		"yt-dlp":                 string(exe),
	}
	var paths []string
	for _, f := range u.files {
		paths = append(paths, f.Path)
		got := readAll(t, filepath.Join(dir, filepath.FromSlash(f.Path)))
		if string(got) != want[f.Path] || f.Size != int64(len(got)) || f.SHA256 != hexSum(want[f.Path]) {
			t.Errorf("%s: %q, %+v", f.Path, got, f)
		}
	}
	if !slices.Equal(paths, []string{"_internal/helper", "_internal/lib/data.bin", "_internal/python3.dll", "yt-dlp"}) {
		t.Errorf("files %v", paths)
	}
	if _, err := os.Stat(filepath.Join(dir, "yt-dlp_linux")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the executable was also written under its archive name")
	}
	if fi, err := os.Stat(filepath.Join(dir, "_internal", "empty")); err != nil || !fi.IsDir() {
		t.Errorf("empty folder: %v", err)
	}
	if runtime.GOOS != "windows" {
		for name, mode := range map[string]fs.FileMode{"yt-dlp": 0o755, "_internal/helper": 0o755} {
			if fi, _ := os.Stat(filepath.Join(dir, name)); fi.Mode().Perm() != mode {
				t.Errorf("%s mode %v, want %v", name, fi.Mode().Perm(), mode)
			}
		}
		if fi, _ := os.Stat(filepath.Join(dir, "_internal", "python3.dll")); fi.Mode().Perm()&0o111 != 0 {
			t.Errorf("python3.dll made executable: %v", fi.Mode().Perm())
		}
	}
	// The executable under another root name is found when it is the only candidate.
	archive = writeZip(t, zipEntry{name: "yt-dlp_win.exe", data: exe}, zipEntry{name: "_internal/x", data: []byte("x")},
		zipEntry{name: "README.txt", data: []byte("r")})
	u, err = unzipTree(archive, t.TempDir(), "yt-dlp.exe", "yt-dlp.exe", treeLimits{bytes: 1 << 20, entries: 16})
	if err != nil || u.exeSum != sha256.Sum256(exe) {
		t.Errorf("renamed executable: %v", err)
	}
}

// TestUnzipTreeRefuses: entries that would leave the folder, links, bombs, duplicates
// and archives without a single executable are refused, and nothing is written outside
// the folder.
func TestUnzipTreeRefuses(t *testing.T) {
	small := []byte("small")
	exe := zipEntry{name: "yt-dlp.exe", data: small}
	lim := treeLimits{bytes: 1 << 10, entries: 8}
	many := []zipEntry{exe}
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "7", "8"} {
		many = append(many, zipEntry{name: n, data: small})
	}
	cases := map[string][]zipEntry{
		"traversal":           {exe, {name: "../escaped", data: small}},
		"nested traversal":    {exe, {name: "_internal/../../escaped", data: small}},
		"backslash traversal": {exe, {name: `..\escaped`, data: small}},
		"absolute":            {exe, {name: "/escaped", data: small}},
		"drive letter":        {exe, {name: "C:/escaped", data: small}},
		"drive relative":      {exe, {name: "C:escaped", data: small}},
		"UNC path":            {exe, {name: `\\server\share\escaped`, data: small}},
		"alternate stream":    {exe, {name: "_internal/x:stream", data: small}},
		"device name":         {exe, {name: "_internal/NUL.txt", data: small}},
		"trailing dot":        {exe, {name: "_internal/x.", data: small}},
		"control character":   {exe, {name: "_internal/a\x01b", data: small}},
		"empty element":       {exe, {name: "_internal//x", data: small}},
		"long name":           {exe, {name: "_internal/" + strings.Repeat("x", 300), data: small}},
		"symlink":             {exe, {name: "_internal/link", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}},
		"declared bomb":       {exe, {name: "_internal/big", data: make([]byte, 2<<10)}},
		"total bomb":          {exe, {name: "_internal/a", data: make([]byte, 600)}, {name: "_internal/b", data: make([]byte, 600)}},
		"too many entries":    many,
		"duplicate":           {exe, {name: "_internal/x", data: small}, {name: "_internal/x", data: small}},
		"case duplicate":      {exe, {name: "_internal/A.dll", data: small}, {name: "_internal/a.dll", data: small}},
		"file then folder":    {exe, {name: "_internal", data: small}, {name: "_internal/x", data: small}},
		"folder then file":    {exe, {name: "_internal/x", data: small}, {name: "_internal", data: small}},
		"folder over file":    {exe, {name: "_internal", data: small}, {name: "_internal/", mode: fs.ModeDir | 0o755}},
		"no executable":       {{name: "_internal/x", data: small}},
		"nested executable":   {{name: "_internal/yt-dlp.exe", data: small}},
		"executable folder":   {{name: "yt-dlp.exe/", mode: fs.ModeDir | 0o755}, {name: "yt-dlp.exe/x", data: small}},
		"two candidates":      {{name: "yt-dlp_a.exe", data: small}, {name: "yt-dlp_b.exe", data: small}},
		"file list name":      {exe, {name: manifestName, data: small}},
		"empty archive":       nil,
	}
	run := func(t *testing.T, entries []zipEntry, want string) {
		t.Helper()
		archive := writeZip(t, entries...)
		root := t.TempDir()
		dir := filepath.Join(root, "out")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := unzipTree(archive, dir, want, "yt-dlp.exe", lim); !errors.Is(err, ErrArchive) {
			t.Errorf("%v, want ErrArchive", err)
		}
		if got, _ := os.ReadDir(root); len(got) != 1 {
			t.Errorf("written next to the folder: %v", got)
		}
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) { run(t, entries, "yt-dlp.exe") })
	}
	// Another root file already has the name the executable is written as.
	t.Run("name of the executable taken", func(t *testing.T) {
		run(t, []zipEntry{{name: "yt-dlp_x86.exe", data: small}, {name: "YT-DLP.exe", data: small}}, "yt-dlp_x86.exe")
	})
	// Content larger than its header says is caught while unpacking.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("yt-dlp.exe"); err == nil {
		_, _ = w.Write(small)
	}
	data := make([]byte, 64<<10)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "_internal/lying", Method: zip.Store,
		CompressedSize64: uint64(len(data)), UncompressedSize64: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(data)
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "lying.zip")
	_ = os.WriteFile(p, buf.Bytes(), 0o600)
	for _, l := range []treeLimits{{bytes: 1 << 20, entries: 8}, lim} {
		if _, err := unzipTree(p, t.TempDir(), "yt-dlp.exe", "yt-dlp.exe", l); !errors.Is(err, ErrArchive) {
			t.Errorf("lying size (limit %d): %v, want ErrArchive", l.bytes, err)
		}
	}
	// Not a zip at all.
	p = filepath.Join(t.TempDir(), "x.zip")
	_ = os.WriteFile(p, []byte("<!DOCTYPE html>"), 0o600)
	if _, err := unzipTree(p, t.TempDir(), "yt-dlp.exe", "yt-dlp.exe", lim); !errors.Is(err, ErrArchive) {
		t.Errorf("not a zip: %v", err)
	}
}

func TestArchivePath(t *testing.T) {
	for name, want := range map[string]string{
		"yt-dlp.exe":           "yt-dlp.exe",
		"_internal/x.pyd":      "_internal/x.pyd",
		`_internal\lib\x.pyd`:  "_internal/lib/x.pyd",
		"./_internal/./x":      "_internal/x",
		"_internal/":           "_internal",
		"./":                   "",
		"_internal/cõnsole.py": "_internal/cõnsole.py",
		"_internal/conx.py":    "_internal/conx.py",
		"_internal/COM10":      "_internal/COM10",
	} {
		if got, _, err := archivePath(name); err != nil || got != want {
			t.Errorf("archivePath(%q) = %q, %v, want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"", "..", "a/..", "/", "a//b", "con", "LPT1.txt", "a ", "a\x00b", "a\x7fb", "a|b", "\xff"} {
		if _, _, err := archivePath(name); !errors.Is(err, ErrArchive) {
			t.Errorf("archivePath(%q): %v, want ErrArchive", name, err)
		}
	}
}
