// Package windows holds the files that the Windows builds read besides the sources: the
// executables' and Setup's icon (icon.ico, drawn by internal/brand) and the native window's
// application manifest (downex-ui.exe.manifest), which ui/native/build.ps1 links in and
// installer/downex.nsi uses. They live here rather than in ui/build/windows, the Wails window's
// project folder, so that the release path does not depend on that tree.
//
// Its tests check those files and Setup's script without running Setup: the script compiles
// with NSIS 3 when makensis is installed (Windows only; skipped otherwise).
package windows

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repoFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestIcon: icon.ico is an icon file, and while ui/build/genicon.go still writes the Wails
// tree's copy, the two are the same drawing (run genicon, then copy it here).
func TestIcon(t *testing.T) {
	ico := repoFile(t, "packaging", "windows", "icon.ico")
	if len(ico) < 6 || !bytes.Equal(ico[:4], []byte{0, 0, 1, 0}) || ico[4] == 0 {
		t.Fatalf("icon.ico is not an .ico file (%d bytes)", len(ico))
	}
	wails, err := os.ReadFile(filepath.Join("..", "..", "ui", "build", "windows", "icon.ico"))
	if err != nil {
		t.Skipf("no ui/build/windows/icon.ico to compare with: %v", err)
	}
	if !bytes.Equal(ico, wails) {
		t.Error("packaging/windows/icon.ico differs from ui/build/windows/icon.ico (go run ./ui/build/genicon.go writes the latter): copy it here")
	}
}

// TestManifest: the window runs as the user who starts it, with per-monitor DPI, Common
// Controls 6 and long paths; the version is filled in by ui/native/winres.
func TestManifest(t *testing.T) {
	m := string(repoFile(t, "packaging", "windows", "downex-ui.exe.manifest"))
	for _, want := range []string{
		`level="asInvoker"`, `uiAccess="false"`, "Microsoft.Windows.Common-Controls", `version="6.0.0.0"`,
		"permonitorv2", "<longPathAware", "{{.Info.ProductVersion}}",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("downex-ui.exe.manifest lacks %s", want)
		}
	}
}

// TestReleasePathAvoidsWailsTree: Setup and the native window's build read the icon and the
// manifest from here.
func TestReleasePathAvoidsWailsTree(t *testing.T) {
	for _, f := range [][]string{{"installer", "downex.nsi"}, {"ui", "native", "build.ps1"}} {
		src := string(repoFile(t, f...))
		if strings.Contains(src, `ui\build\windows`) || strings.Contains(src, "ui/build/windows") {
			t.Errorf("%s reads ui/build/windows", filepath.Join(f...))
		}
		if !strings.Contains(src, `packaging\windows\icon.ico`) {
			t.Errorf("%s does not use packaging\\windows\\icon.ico", filepath.Join(f...))
		}
	}
}

// TestSetupScript: what Setup's script promises the rest of Downex, checked in its text.
func TestSetupScript(t *testing.T) {
	src := string(repoFile(t, "installer", "downex.nsi"))
	uninstall := strings.Index(src, `Section "Uninstall"`)
	if uninstall < 0 {
		t.Fatal("downex.nsi has no Uninstall section")
	}
	setup := src[:uninstall]
	for _, want := range []string{
		// The notices are installed and removed with the programs.
		`File "/oname=THIRD-PARTY-NOTICES.txt" "${__FILEDIR__}\..\ui\THIRD-PARTY-NOTICES.txt"`,
		// An in-app update (internal/update/setup.go: /S /RELAUNCH) opens the window again.
		`${GetOptions} $CMDLINE "/RELAUNCH" $0`,
		"Function .onInstSuccess",
		// A failed Setup puts the earlier programs back and starts the daemon.
		"Function .onInstFailed",
		"Call RestoreAside",
		// The desktop-shortcut choice is kept.
		"!insertmacro UnselectSection ${SecDesktop}",
		// ARM64 needs Windows 11 for the x64 window.
		"${AtLeastWin11}",
	} {
		if !strings.Contains(setup, want) {
			t.Errorf("Setup's part of downex.nsi lacks %s", want)
		}
	}
	// The extension folders are overwritten in place, never removed first.
	if regexp.MustCompile(`(?i)RMDir\s+/r\s+"\$INSTDIR\\extension`).MatchString(setup) {
		t.Error("Setup removes an extension folder (RMDir /r) instead of overwriting it in place")
	}
	// Setup renames programs aside; only the uninstaller deletes them in place.
	if strings.Contains(setup, "Call RemovePrograms") {
		t.Error("Setup deletes the programs instead of setting them aside")
	}
	if !strings.Contains(src[uninstall:], `Delete "$INSTDIR\THIRD-PARTY-NOTICES.txt"`) || !strings.Contains(src[uninstall:], `!insertmacro DELETE_ASIDE_COPIES "$INSTDIR"`) {
		t.Error("the uninstaller leaves THIRD-PARTY-NOTICES.txt or set-aside programs behind")
	}
	// Only the programs' own aside copies go: the folder may hold other people's *.old files.
	if regexp.MustCompile(`(?i)Delete\s+"[^"]*\\\*\.old"`).MatchString(src) {
		t.Error(`downex.nsi deletes "*.old": only the four programs' aside copies may go`)
	}
	setupGo := string(repoFile(t, "internal", "update", "setup.go"))
	if !strings.Contains(setupGo, `StartDetached(s.path, "/S", "/RELAUNCH")`) {
		t.Error(`internal/update/setup.go does not start Setup with /S /RELAUNCH`)
	}
}

// TestSetupCompiles compiles installer/downex.nsi for amd64 and arm64 with stand-ins for the
// programs and the extension (makensis also runs installer/extension-files.ps1 on them). It
// does not run the Setup it makes.
func TestSetupCompiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("makensis compiles Setup on Windows (scripts/build-windows.ps1 -Installer)")
	}
	if testing.Short() {
		t.Skip("compiling Setup takes a few seconds")
	}
	makensis, err := exec.LookPath("makensis")
	if err != nil {
		for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
			p := filepath.Join(base, "NSIS", "makensis.exe")
			if base != "" {
				if _, err := os.Stat(p); err == nil {
					makensis = p
					break
				}
			}
		}
	}
	if makensis == "" {
		t.Skip("NSIS 3 (makensis) is not installed")
	}
	nsi, err := filepath.Abs(filepath.Join("..", "..", "installer", "downex.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	files := map[string]string{
		"downexd.exe": "MZ", "downex.exe": "MZ", "downex-nmhost.exe": "MZ", "downex-ui.exe": "MZ",
		filepath.Join("extension", "chrome-mv3", "manifest.json"):          "{}",
		filepath.Join("extension", "chrome-mv3", "assets", "$popup-1.js"):  "x",
		filepath.Join("extension", "firefox-mv3", "manifest.json"):         "{}",
		filepath.Join("extension", "firefox-mv3", "icon", "icon 16.png"):   "x",
		filepath.Join("extension", "firefox-mv3", "_locales", "en", "m.j"): "x",
	}
	for name, data := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		out := filepath.Join(t.TempDir(), "Downex-Setup-1.2.3-"+arch+".exe")
		cmd := exec.Command(makensis, "/V2", "/INPUTCHARSET", "UTF8", "/DVERSION=1.2.3", "/DVIVERSION=1.2.3.0",
			"/DARCH="+arch, "/DSRCDIR="+src, "/DOUTFILE="+out, nsi)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("makensis (%s): %v\n%s", arch, err, b)
		}
		if strings.Contains(strings.ToLower(string(b)), "warning") {
			t.Errorf("makensis (%s) warned:\n%s", arch, b)
		}
		if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
			t.Errorf("makensis (%s) wrote no Setup: %v", arch, err)
		}
	}
}
