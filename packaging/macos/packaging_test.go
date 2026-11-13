// Package macos holds the templates of the macOS bundle (README.md). Its tests check the macOS
// packaging (docs/MACOS-SPEC.md §1 and §6) on every OS, without a Mac: the property-list
// templates are well-formed and hold what the bundles need, and the functions of
// scripts/build-macos.sh render them, lay out the bundle and sign it in the order the spec gives.
// The script's functions run in bash with the macOS tools (ditto, plutil, codesign, xattr)
// replaced by shell functions; those tests are skipped when there is no bash (on Windows they use
// Git for Windows' bash, or $DOWNEX_TEST_BASH).
package macos

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const scriptPath = "../../scripts/build-macos.sh"

// placeholders are the @NAME@ placeholders that render_template fills.
var placeholders = map[string]bool{"@VERSION@": true, "@BUNDLE_VERSION@": true, "@EXTENSION_ID@": true}

// notTemplates are the files of this directory that the build script does not use.
var notTemplates = map[string]bool{".gitattributes": true, "README.md": true}

type bundleSpec struct {
	template   string
	dir        string // the bundle, relative to Downex.app ("" = Downex.app itself)
	identifier string
	executable string
	name       string
	uiElement  bool // LSUIElement: no Dock icon
}

var bundles = []bundleSpec{
	{"Downex-Info.plist", "", "com.downex.app", "Downex", "Downex", false},
	{"DownexProgress-Info.plist", "Contents/Helpers/Downex Progress.app", "com.downex.progress", "Downex", "Downex Progress", true},
	{"DownexAgent-Info.plist", "Contents/Library/LoginItems/Downex Agent.app", "com.downex.agent", "downexd-agent", "Downex Agent", true},
}

func TestBundleInfoPlists(t *testing.T) {
	for _, b := range bundles {
		t.Run(b.template, func(t *testing.T) {
			checkInfoPlist(t, readFile(t, b.template), b, "@BUNDLE_VERSION@")
		})
	}
}

// checkInfoPlist checks the Info.plist data of bundle b, whose versions must be version.
func checkInfoPlist(t *testing.T, data []byte, b bundleSpec, version string) {
	t.Helper()
	root, err := parsePlist(data)
	if err != nil {
		t.Fatalf("%s: %v", b.template, err)
	}
	if root.kind != "dict" {
		t.Fatalf("%s: the plist holds a <%s>, not a <dict>", b.template, root.kind)
	}
	want := map[string]string{
		"CFBundleDevelopmentRegion":     "en",
		"CFBundleDisplayName":           b.name,
		"CFBundleExecutable":            b.executable,
		"CFBundleIconFile":              "AppIcon",
		"CFBundleIdentifier":            b.identifier,
		"CFBundleInfoDictionaryVersion": "6.0",
		"CFBundleName":                  b.name,
		"CFBundlePackageType":           "APPL",
		"CFBundleShortVersionString":    version,
		"CFBundleVersion":               version,
		"LSMinimumSystemVersion":        "13.0",
		"NSPrincipalClass":              "NSApplication",
	}
	for key, value := range want {
		got, ok := root.dict[key]
		switch {
		case !ok:
			t.Errorf("%s: %s is missing", b.template, key)
		case got.kind != "string" || got.text != value:
			t.Errorf("%s: %s = %v, want <string>%s</string>", b.template, key, got, value)
		}
	}
	// Apple: CFBundleName "should contain no more than 15 characters".
	if name := root.dict["CFBundleName"].text; len([]rune(name)) > 15 {
		t.Errorf("%s: CFBundleName %q is longer than 15 characters", b.template, name)
	}
	checkBool(t, b.template, root, "NSHighResolutionCapable", true)
	if b.uiElement {
		checkBool(t, b.template, root, "LSUIElement", true)
	} else if v, ok := root.dict["LSUIElement"]; ok {
		t.Errorf("%s: LSUIElement = %v; the main app shows its Dock icon", b.template, v)
	}
}

func checkBool(t *testing.T, file string, root plistValue, key string, want bool) {
	t.Helper()
	v, ok := root.dict[key]
	switch {
	case !ok:
		t.Errorf("%s: %s is missing", file, key)
	case v.kind != strconv.FormatBool(want):
		t.Errorf("%s: %s = %v, want <%t/>", file, key, v, want)
	}
}

func TestEntitlements(t *testing.T) {
	data := readFile(t, "entitlements.plist")
	// The kernel reads entitlements with its own XML parser, which ends a "<!" declaration at
	// the first ">": a comment could change what it reads.
	if bytes.Contains(data, []byte("<!--")) {
		t.Error("entitlements.plist has an XML comment")
	}
	root, err := parsePlist(data)
	if err != nil {
		t.Fatal(err)
	}
	if root.kind != "dict" {
		t.Fatalf("the plist holds a <%s>, not a <dict>", root.kind)
	}
	for key, v := range root.dict {
		if v.kind != "true" && v.kind != "false" {
			t.Errorf("%s = %v, want a boolean", key, v)
		}
		// Hardened runtime without exceptions: Go, AppKit and WKWebView (whose JIT runs in
		// WebKit's own processes) need none.
		if strings.HasPrefix(key, "com.apple.security.cs.") {
			t.Errorf("%s is a hardened-runtime exception", key)
		}
	}
	checkBool(t, "entitlements.plist", root, "com.apple.security.app-sandbox", false)
	checkBool(t, "entitlements.plist", root, "com.apple.security.network.client", true)
	// The notary service rejects code signed with get-task-allow (a debug entitlement).
	if _, ok := root.dict["com.apple.security.get-task-allow"]; ok {
		t.Error("com.apple.security.get-task-allow is set")
	}
}

// TestTemplateFiles checks every template for Unix line endings and for placeholders that
// render_template does not fill.
func TestTemplateFiles(t *testing.T) {
	re := regexp.MustCompile(`@[A-Z_]+@`)
	for _, name := range templateNames(t) {
		data := readFile(t, name)
		if bytes.ContainsRune(data, '\r') {
			t.Errorf("%s has CR line endings", name)
		}
		if !bytes.HasSuffix(data, []byte("\n")) {
			t.Errorf("%s does not end with a newline", name)
		}
		for _, p := range re.FindAll(data, -1) {
			if !placeholders[string(p)] {
				t.Errorf("%s has the placeholder %s, which render_template does not fill", name, p)
			}
		}
		if strings.HasSuffix(name, ".plist") {
			if _, err := parsePlist(data); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}

// TestScriptUsesTemplates checks that every template the script names exists and that every
// template is used.
func TestScriptUsesTemplates(t *testing.T) {
	src := readScript(t)
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$PKG/([A-Za-z0-9._-]+)`).FindAllStringSubmatch(src, -1) {
		used[m[1]] = true
		if _, err := os.Stat(m[1]); err != nil {
			t.Errorf("the script uses $PKG/%s: %v", m[1], err)
		}
	}
	for _, name := range templateNames(t) {
		if !used[name] {
			t.Errorf("packaging/macos/%s is not used by scripts/build-macos.sh", name)
		}
	}
}

// TestScriptPathsIgnoreCase checks that no two paths the script writes under $WORK differ only
// in case: macOS formats its volumes case-insensitive by default, where they are one file (as
// "$WORK/universal/Downex" and "$WORK/universal/downex" were).
func TestScriptPathsIgnoreCase(t *testing.T) {
	seen := map[string]string{}
	for _, p := range regexp.MustCompile(`\$WORK/[A-Za-z0-9._/@-]+`).FindAllString(readScript(t), -1) {
		key := strings.ToLower(p)
		if other, ok := seen[key]; ok && other != p {
			t.Errorf("the script uses both %s and %s", other, p)
		}
		seen[key] = p
	}
	if len(seen) == 0 {
		t.Fatal("no $WORK paths found")
	}
}

func TestScriptFile(t *testing.T) {
	src := readScript(t)
	if strings.Contains(src, "\r") {
		t.Error("scripts/build-macos.sh has CR line endings, which bash on macOS cannot run")
	}
	if !strings.HasPrefix(src, "#!/usr/bin/env bash\n") {
		t.Error("scripts/build-macos.sh does not start with #!/usr/bin/env bash")
	}
	if !strings.Contains(src, "\nset -euo pipefail\n") {
		t.Error("scripts/build-macos.sh does not set -euo pipefail")
	}
	// bash 3.2 (/bin/bash on macOS) has none of these.
	for i, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, bash4 := range []string{"declare -A", "local -A", "mapfile", "readarray", ",,}", "^^}", ";&", "|&", "declare -g", "wait -n"} {
			if strings.Contains(line, bash4) {
				t.Errorf("scripts/build-macos.sh:%d uses %q, which bash 3.2 does not have", i+1, bash4)
			}
		}
	}
}

func TestScriptSyntax(t *testing.T) {
	bash := bashPath(t)
	if out, err := exec.Command(bash, "-n", scriptArg(t)).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestScriptArguments(t *testing.T) {
	bash := bashPath(t)
	type argTest struct {
		args []string
		ok   bool
		want string
	}
	tests := []argTest{
		{[]string{"-h"}, true, "Usage: scripts/build-macos.sh [-v VERSION] [-o DIR]"},
		{[]string{"--help"}, true, "DEVELOPER_ID"},
		{[]string{"-v", "0.4 beta"}, false, "invalid version '0.4 beta'"},
		{[]string{"-v", "-1"}, false, "invalid version '-1'"},
		{[]string{"-v", "1.0;rm"}, false, "invalid version"},
		{[]string{"-x"}, false, "unknown option -x"},
		{[]string{"-v"}, false, "option -v needs a value"},
		{[]string{"-o", ""}, false, "the output directory (-o) is empty"},
		{[]string{"-v", "1.0.0", "extra"}, false, "unexpected argument 'extra'"},
	}
	if runtime.GOOS != "darwin" { // on a Mac this would build
		tests = append(tests,
			argTest{nil, false, "runs on macOS only"},
			argTest{[]string{"-v", "1.0.0", "-o", "out"}, false, "runs on macOS only"})
	}
	for _, tc := range tests {
		cmd := exec.Command(bash, append([]string{scriptArg(t)}, tc.args...)...)
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.ok {
			t.Errorf("build-macos.sh %q: error %v, want success %t\n%s", tc.args, err, tc.ok, out)
		}
		if !strings.Contains(string(out), tc.want) {
			t.Errorf("build-macos.sh %q printed\n%s\nwant it to contain %q", tc.args, out, tc.want)
		}
	}
}

func TestBundleVersion(t *testing.T) {
	tests := []struct{ version, want string }{
		{"dev", "0.0.0"},
		{"0.3.2", "0.3.2"},
		{"0.4.0-beta.1", "0.4.0"},
		{"1.2", "1.2.0"},
		{"7", "7.0.0"},
		{"1.2.3.4", "1.2.3"},
		{"007.08.0900", "7.8.900"},
		{"0.00.0", "0.0.0"},
		{"2026.10.20+abc", "2026.10.20"},
		{"1..2", "1.0.0"},
		{"v1.2.3", "0.0.0"},
	}
	args := make([]string, len(tests))
	for i, tc := range tests {
		args[i] = tc.version
	}
	out, err := shell(t, nil, `for v in "$@"; do bundle_version "$v"; done`, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(got) != len(tests) {
		t.Fatalf("bundle_version printed %q", out)
	}
	for i, tc := range tests {
		if got[i] != tc.want {
			t.Errorf("bundle_version %q = %q, want %q", tc.version, got[i], tc.want)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	const (
		version = "0.4.0-beta.1"
		id      = "abcdefghijklmnopabcdefghijklmnop"
	)
	dir := t.TempDir()
	env := []string{"VERSION=" + version, "BUNDLE_VERSION=0.4.0", "EXTENSION_ID=" + id}
	render := func(t *testing.T, name string) []byte {
		t.Helper()
		dest := filepath.Join(dir, name)
		if out, err := shell(t, env, `render_template "$1" "$2"`, slash(t, name), filepath.ToSlash(dest)); err != nil {
			t.Fatalf("render_template %s: %v\n%s", name, err, out)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if m := regexp.MustCompile(`@[A-Z_]+@`).Find(data); m != nil {
			t.Errorf("%s: %s is left", name, m)
		}
		return data
	}
	for _, b := range bundles {
		t.Run(b.template, func(t *testing.T) {
			checkInfoPlist(t, render(t, b.template), b, "0.4.0")
		})
	}
	if data := render(t, "EXTENSION.txt"); !bytes.Contains(data, []byte("\n       "+id+"\n")) {
		t.Errorf("EXTENSION.txt does not show the extension ID:\n%s", data)
	}
	if data := render(t, "README-DMG.txt"); !bytes.HasPrefix(data, []byte("Downex "+version+" for macOS\n")) {
		t.Errorf("README-DMG.txt does not start with the version:\n%s", data)
	}
	if data := render(t, "entitlements.plist"); !bytes.Equal(data, readFile(t, "entitlements.plist")) {
		t.Error("render_template changed entitlements.plist, which has no placeholders")
	}

	// A placeholder the script does not know stops it, and leaves no half-rendered file.
	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("@VERSION@ @SIGNER@\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "bad.out")
	out, err := shell(t, env, `render_template "$1" "$2"`, filepath.ToSlash(bad), filepath.ToSlash(dest))
	if err == nil || !strings.Contains(out, "@SIGNER@") {
		t.Errorf("render_template with @SIGNER@: error %v, output %q", err, out)
	}
	if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("render_template left %s: %v", dest, err)
	}
}

// TestAssembleLayout runs assemble with stand-ins for the built binaries and checks the bundle
// against docs/MACOS-SPEC.md §1, file by file.
func TestAssembleLayout(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	ext := filepath.Join(base, "ext")
	app := filepath.Join(base, "out", "Downex.app")
	inputs := map[string]string{
		"work/window/Downex":                 "window",
		"work/universal/downexd":             "daemon",
		"work/universal/downex":              "cli",
		"work/universal/downex-nmhost":       "relay",
		"work/universal/downexd-agent":       "agent",
		"work/AppIcon.icns":                  "icon",
		"ext/chrome-mv3/manifest.json":       "chrome manifest",
		"ext/chrome-mv3/background.js":       "chrome script",
		"ext/firefox-mv3/manifest.json":      "firefox manifest",
		"out/Downex.app/Contents/stale-file": "from an earlier build",
	}
	for name, content := range inputs {
		writeFile(t, filepath.Join(base, filepath.FromSlash(name)), content)
	}
	env := []string{
		"VERSION=0.4.0", "BUNDLE_VERSION=0.4.0", "EXTENSION_ID=abcdefghijklmnopabcdefghijklmnop",
		"WORK=" + filepath.ToSlash(work), "EXT_OUT=" + filepath.ToSlash(ext),
	}
	const stubs = `
ditto() { mkdir -p "$(dirname "$2")"; cp -R "$1" "$2"; }
plutil() { :; }
step() { :; }
`
	if out, err := shell(t, env, stubs+`assemble "$1"`, filepath.ToSlash(app)); err != nil {
		t.Fatalf("assemble: %v\n%s", err, out)
	}

	const (
		progress = "Contents/Helpers/Downex Progress.app/"
		agent    = "Contents/Library/LoginItems/Downex Agent.app/"
	)
	want := map[string]string{ // file → content ("" = rendered from a template)
		"Contents/Info.plist":                                    "",
		"Contents/MacOS/Downex":                                  "window",
		"Contents/Helpers/downexd":                               "daemon",
		"Contents/Helpers/downex":                                "cli",
		"Contents/Helpers/downex-nmhost":                         "relay",
		progress + "Contents/Info.plist":                         "",
		progress + "Contents/MacOS/Downex":                       "window",
		progress + "Contents/Resources/AppIcon.icns":             "icon",
		agent + "Contents/Info.plist":                            "",
		agent + "Contents/MacOS/downexd-agent":                   "agent",
		agent + "Contents/Resources/AppIcon.icns":                "icon",
		"Contents/Resources/AppIcon.icns":                        "icon",
		"Contents/Resources/EXTENSION.txt":                       "",
		"Contents/Resources/LICENSE.txt":                         "",
		"Contents/Resources/THIRD-PARTY-NOTICES.txt":             "",
		"Contents/Resources/extension/chrome-mv3/manifest.json":  "chrome manifest",
		"Contents/Resources/extension/chrome-mv3/background.js":  "chrome script",
		"Contents/Resources/extension/firefox-mv3/manifest.json": "firefox manifest",
	}
	got := map[string]string{}
	err := filepath.WalkDir(app, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(app, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		got[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sortedKeys(want) {
		content, ok := got[name]
		switch {
		case !ok:
			t.Errorf("Downex.app/%s is missing", name)
		case want[name] != "" && content != want[name]:
			t.Errorf("Downex.app/%s holds %q, want %q", name, content, want[name])
		}
	}
	for _, name := range sortedKeys(got) {
		if _, ok := want[name]; !ok {
			t.Errorf("Downex.app/%s should not be there", name)
		}
	}
	for _, b := range bundles {
		dir := filepath.Join(app, filepath.FromSlash(b.dir))
		checkInfoPlist(t, []byte(got[filepath.ToSlash(filepath.Join(b.dir, "Contents/Info.plist"))]), b, "0.4.0")
		for _, f := range []string{"Contents/MacOS/" + b.executable, "Contents/Resources/AppIcon.icns"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
				t.Errorf("%s: %v", b.template, err)
			}
		}
	}
}

// TestSignOrder runs sign_app with a stand-in for codesign and checks that every binary and
// bundle is signed, inner code first, with the hardened runtime and the entitlements.
func TestSignOrder(t *testing.T) {
	// The script's own path of packaging/macos (on Windows an MSYS path such as /d/...).
	pkg, err := shell(t, nil, `printf '%s' "$PKG"`)
	if err != nil {
		t.Fatalf("%v\n%s", err, pkg)
	}
	for _, identity := range []string{"-", "Developer ID Application: Example (ABCDE12345)"} {
		t.Run(identity, func(t *testing.T) {
			base := t.TempDir()
			log := filepath.Join(base, "log")
			app := filepath.ToSlash(filepath.Join(base, "Downex.app"))
			const stubs = `
codesign() { printf 'codesign %s\n' "$*" >>"$LOG"; }
xattr() { printf 'xattr %s\n' "$*" >>"$LOG"; }
step() { :; }
`
			env := []string{"IDENTITY=" + identity, "LOG=" + filepath.ToSlash(log)}
			if out, err := shell(t, env, stubs+`sign_app "$1"`, app); err != nil {
				t.Fatalf("sign_app: %v\n%s", err, out)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			targets := []string{
				app + "/Contents/Helpers/downexd",
				app + "/Contents/Helpers/downex",
				app + "/Contents/Helpers/downex-nmhost",
				app + "/Contents/Helpers/Downex Progress.app",
				app + "/Contents/Library/LoginItems/Downex Agent.app",
				app,
			}
			want := []string{"xattr -cr " + app}
			timestamp := "--timestamp"
			if identity == "-" {
				timestamp = "--timestamp=none"
			}
			for i, target := range targets {
				line := "codesign --force --sign " + identity + " --options runtime --entitlements " +
					pkg + "/entitlements.plist " + timestamp + " "
				if i < 3 {
					line += "--identifier com.downex." + filepath.Base(target) + " "
				}
				want = append(want, line+target)
			}
			want = append(want, "codesign --verify --deep --strict --verbose=2 "+app)
			if strings.Join(lines, "\n") != strings.Join(want, "\n") {
				t.Errorf("sign_app ran\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// --- property lists ---

// plistValue is one value of an XML property list.
type plistValue struct {
	kind  string                // its element: dict, array, string, integer, real, date, data, true, false
	text  string                // string, integer, real, date, data
	items []plistValue          // array
	dict  map[string]plistValue // dict
}

func (v plistValue) String() string {
	switch v.kind {
	case "true", "false":
		return "<" + v.kind + "/>"
	case "dict", "array":
		return "<" + v.kind + ">…</" + v.kind + ">"
	}
	return "<" + v.kind + ">" + v.text + "</" + v.kind + ">"
}

// parsePlist parses an XML property list strictly: the XML declaration, one <plist
// version="1.0"> holding one value, and nothing but white space and comments between elements.
func parsePlist(data []byte) (plistValue, error) {
	const header = `<?xml version="1.0" encoding="UTF-8"?>`
	if !bytes.HasPrefix(data, []byte(header)) {
		return plistValue{}, errors.New("does not start with " + header)
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	root, ok, err := nextElement(d)
	if err != nil {
		return plistValue{}, err
	}
	if !ok || root.Name.Local != "plist" {
		return plistValue{}, errors.New("the root element is not <plist>")
	}
	if version := attr(root, "version"); version != "1.0" {
		return plistValue{}, fmt.Errorf("<plist version=%q>, want 1.0", version)
	}
	start, ok, err := nextElement(d)
	if err != nil {
		return plistValue{}, err
	}
	if !ok {
		return plistValue{}, errors.New("<plist> is empty")
	}
	v, err := readValue(d, start)
	if err != nil {
		return plistValue{}, err
	}
	if _, ok, err := nextElement(d); err != nil || ok {
		if err == nil {
			err = errors.New("<plist> holds more than one value")
		}
		return plistValue{}, err
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return v, nil
		}
		if err != nil {
			return plistValue{}, err
		}
		if c, ok := tok.(xml.CharData); ok && len(bytes.TrimSpace(c)) == 0 {
			continue
		}
		if _, ok := tok.(xml.Comment); ok {
			continue
		}
		return plistValue{}, fmt.Errorf("content after </plist>: %T", tok)
	}
}

// nextElement skips white space, comments and declarations up to the next start tag (ok) or
// the end tag of the enclosing element (!ok). Other text is an error.
func nextElement(d *xml.Decoder) (xml.StartElement, bool, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return xml.StartElement{}, false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, true, nil
		case xml.EndElement:
			return xml.StartElement{}, false, nil
		case xml.CharData:
			if text := bytes.TrimSpace(t); len(text) > 0 {
				return xml.StartElement{}, false, fmt.Errorf("unexpected text %q", text)
			}
		}
	}
}

func readValue(d *xml.Decoder, start xml.StartElement) (plistValue, error) {
	v := plistValue{kind: start.Name.Local}
	switch v.kind {
	case "string", "integer", "real", "date", "data":
		text, err := readText(d)
		if err != nil {
			return v, err
		}
		v.text = text
		if v.kind == "integer" {
			if _, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err != nil {
				return v, fmt.Errorf("%v: %w", v, err)
			}
		}
		if v.kind == "real" {
			if _, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err != nil {
				return v, fmt.Errorf("%v: %w", v, err)
			}
		}
	case "true", "false":
		text, err := readText(d)
		if err != nil {
			return v, err
		}
		if strings.TrimSpace(text) != "" {
			return v, fmt.Errorf("<%s> holds text", v.kind)
		}
	case "array":
		for {
			s, ok, err := nextElement(d)
			if err != nil {
				return v, err
			}
			if !ok {
				break
			}
			item, err := readValue(d, s)
			if err != nil {
				return v, err
			}
			v.items = append(v.items, item)
		}
	case "dict":
		v.dict = map[string]plistValue{}
		for {
			k, ok, err := nextElement(d)
			if err != nil {
				return v, err
			}
			if !ok {
				break
			}
			if k.Name.Local != "key" {
				return v, fmt.Errorf("<%s> where a <dict> needs a <key>", k.Name.Local)
			}
			key, err := readText(d)
			if err != nil {
				return v, err
			}
			s, ok, err := nextElement(d)
			if err != nil {
				return v, err
			}
			if !ok {
				return v, fmt.Errorf("key %s has no value", key)
			}
			item, err := readValue(d, s)
			if err != nil {
				return v, fmt.Errorf("%s: %w", key, err)
			}
			if _, dup := v.dict[key]; dup {
				return v, fmt.Errorf("key %s appears twice", key)
			}
			v.dict[key] = item
		}
	default:
		return v, fmt.Errorf("<%s> is not a property-list element", v.kind)
	}
	return v, nil
}

// readText reads the text of an element up to its end tag.
func readText(d *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("<%s> inside text", t.Name.Local)
		}
	}
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func TestParsePlistRejects(t *testing.T) {
	const head = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	bad := map[string]string{
		"no declaration":    `<plist version="1.0"><dict/></plist>`,
		"unclosed":          head + `<plist version="1.0"><dict><key>a</key><string>b</dict></plist>`,
		"key without value": head + `<plist version="1.0"><dict><key>a</key></dict></plist>`,
		"duplicate key":     head + `<plist version="1.0"><dict><key>a</key><true/><key>a</key><false/></dict></plist>`,
		"value without key": head + `<plist version="1.0"><dict><string>b</string></dict></plist>`,
		"text in dict":      head + `<plist version="1.0"><dict>x<key>a</key><true/></dict></plist>`,
		"two values":        head + `<plist version="1.0"><dict/><dict/></plist>`,
		"wrong version":     head + `<plist version="2.0"><dict/></plist>`,
		"not a plist":       head + `<html/>`,
		"bad integer":       head + `<plist version="1.0"><integer>1x</integer></plist>`,
		"unknown element":   head + `<plist version="1.0"><dict><key>a</key><yes/></dict></plist>`,
		"text in true":      head + `<plist version="1.0"><true>1</true></plist>`,
		"after plist":       head + `<plist version="1.0"><dict/></plist><dict/>`,
	}
	for name, data := range bad {
		if _, err := parsePlist([]byte(data)); err == nil {
			t.Errorf("%s: parsePlist accepted %s", name, data)
		}
	}
	v, err := parsePlist([]byte(head + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>a</key><array><string>x &amp; y</string><integer>-3</integer></array></dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if a := v.dict["a"]; a.kind != "array" || len(a.items) != 2 || a.items[0].text != "x & y" || a.items[1].text != "-3" {
		t.Errorf("parsed %+v", v)
	}
}

// --- helpers ---

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readScript(t *testing.T) string {
	t.Helper()
	return string(readFile(t, scriptPath))
}

// templateNames lists the templates in this directory: every file but the tests and notTemplates.
func templateNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || notTemplates[e.Name()] || strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		t.Fatal("no templates")
	}
	return names
}

func templateAbs(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bashPath returns the bash to run the script's functions with, or skips the test.
func bashPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("DOWNEX_TEST_BASH"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		// Git for Windows' bash, found from git. The bash.exe in System32 is WSL's launcher,
		// which does not take Windows paths, so PATH is not searched for bash.
		if git, err := exec.LookPath("git"); err == nil {
			dir := filepath.Dir(git)
			for _, p := range []string{
				filepath.Join(dir, "..", "bin", "bash.exe"),       // <Git>\cmd\git.exe
				filepath.Join(dir, "bash.exe"),                    // <Git>\bin\git.exe
				filepath.Join(dir, "..", "..", "bin", "bash.exe"), // <Git>\mingw64\bin\git.exe
			} {
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
					return filepath.Clean(p)
				}
			}
		}
		t.Skip("no Git for Windows bash found; set DOWNEX_TEST_BASH to a bash to run this test")
	}
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	return p
}

// slash returns the absolute path of a file in this directory with forward slashes, which bash
// on Windows (MSYS) takes as well.
func slash(t *testing.T, name string) string {
	t.Helper()
	return filepath.ToSlash(templateAbs(t, name))
}

func scriptArg(t *testing.T) string {
	t.Helper()
	return slash(t, scriptPath)
}

// shell sources scripts/build-macos.sh in bash (which only defines its functions then) and runs
// code with args as "$@" and env added to the environment. It returns stdout and stderr.
func shell(t *testing.T, env []string, code string, args ...string) (string, error) {
	t.Helper()
	bash := bashPath(t)
	cmd := exec.Command(bash, append([]string{"-c", "source \"$1\"\nshift\n" + code, "bash", scriptArg(t)}, args...)...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
