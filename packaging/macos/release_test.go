package macos

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// releaseEnv is the environment of a release build without the update variables of the machine
// that runs the tests.
func releaseEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DOWNEX_UPDATE_PUBLIC_KEY=") && !strings.HasPrefix(kv, "DOWNEX_UPDATE_FEED_URL=") &&
			!strings.HasPrefix(kv, "DOWNEX_RELEASE_UPDATE_FILE=") {
			env = append(env, kv)
		}
	}
	// without packaging/release-update.txt, which would supply the key and the feed, so the key
	// and the feed come from the environment on purpose
	return append(append(env, "DOWNEX_RELEASE_UPDATE_FILE=none", "DOWNEX_RELEASE_USE_ENV_UPDATE=1"), extra...)
}

// TestScriptRelease: -r refuses, before anything is built, a version that is not semantic or
// starts with "v", a missing update key or feed, a release file that does not give both (none,
// missing) unless DOWNEX_RELEASE_USE_ENV_UPDATE=1, and an update key in the environment that
// differs from packaging/release-update.txt; a malformed key or feed is refused with or without -r.
func TestScriptRelease(t *testing.T) {
	bash := bashPath(t)
	const (
		key  = "DOWNEX_UPDATE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		feed = "DOWNEX_UPDATE_FEED_URL=https://releases.example.test/downex/update.json"
		// the release file decides, as on a build machine without the override
		noOverride = "DOWNEX_RELEASE_USE_ENV_UPDATE="
	)
	type tc struct {
		args []string
		env  []string
		want string
	}
	tests := []tc{
		{[]string{"-r"}, []string{key, feed}, "-r needs -v to be a semantic version such as 1.2.3 (not 'dev')"},
		{[]string{"-r", "-v", "1.2"}, []string{key, feed}, "not '1.2'"},
		{[]string{"-r", "-v", "01.2.3"}, []string{key, feed}, "not '01.2.3'"},
		{[]string{"-r", "-v", "1.2.3"}, []string{feed}, "-r needs DOWNEX_UPDATE_PUBLIC_KEY"},
		{[]string{"-r", "-v", "1.2.3-rc.1"}, []string{key}, "-r needs DOWNEX_UPDATE_FEED_URL"},
		{[]string{"-r", "-v", "v1.2.3"}, []string{key, feed}, "-r needs -v without a leading 'v'"},
		// a key left in the environment must not replace packaging/release-update.txt's
		{[]string{"-r", "-v", "1.2.3"}, []string{"DOWNEX_RELEASE_UPDATE_FILE=", noOverride, key}, "differs from public_key"},
		// a release file that is "none" or missing does not let the environment through unchecked
		{[]string{"-r", "-v", "1.2.3"}, []string{noOverride, key, feed}, "-r needs public_key and feed_url in none"},
		{[]string{"-r", "-v", "1.2.3"}, []string{"DOWNEX_RELEASE_UPDATE_FILE=/nonexistent/release-update.txt", noOverride, key, feed},
			"-r needs public_key and feed_url in /nonexistent/release-update.txt"},
		{[]string{"-v", "1.2.3"}, []string{"DOWNEX_UPDATE_FEED_URL=http://releases.example.test/f.json"}, "DOWNEX_UPDATE_FEED_URL is not an https URL"},
		{[]string{"-v", "1.2.3"}, []string{"DOWNEX_UPDATE_FEED_URL=https://x.test/a b"}, "DOWNEX_UPDATE_FEED_URL is not an https URL"},
		{[]string{"-v", "1.2.3"}, []string{"DOWNEX_UPDATE_PUBLIC_KEY=short"}, "DOWNEX_UPDATE_PUBLIC_KEY is not a base64 ed25519 public key"},
	}
	if runtime.GOOS != "darwin" { // on a Mac these would build
		tests = append(tests,
			tc{[]string{"-r", "-v", "1.2.3"}, []string{key, feed}, "runs on macOS only"},
			tc{[]string{"-r", "-v", "1.2.3+build.5"}, []string{key, feed}, "runs on macOS only"},
			// packaging/release-update.txt supplies both when the environment does not
			tc{[]string{"-r", "-v", "1.2.3"}, []string{"DOWNEX_RELEASE_UPDATE_FILE=", noOverride}, "runs on macOS only"})
	}
	for _, tc := range tests {
		cmd := exec.Command(bash, append([]string{scriptArg(t)}, tc.args...)...)
		cmd.Dir = t.TempDir()
		cmd.Env = releaseEnv(tc.env...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("build-macos.sh %q with %q succeeded:\n%s", tc.args, tc.env, out)
		}
		if !strings.Contains(string(out), tc.want) {
			t.Errorf("build-macos.sh %q with %q printed\n%s\nwant it to contain %q", tc.args, tc.env, out, tc.want)
		}
	}
}

// TestLdflags: the update key and feed go into downexd's -ldflags, and a release strips the
// symbol tables.
func TestLdflags(t *testing.T) {
	env := releaseEnv("VERSION=1.2.3", "DOWNEX_UPDATE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"DOWNEX_UPDATE_FEED_URL=https://releases.example.test/downex/update.json?channel=stable")
	for release, want := range map[string]string{
		"0": "-X main.version=1.2.3 -X downex/internal/update.PublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= " +
			"-X downex/internal/update.DefaultFeedURL=https://releases.example.test/downex/update.json?channel=stable",
		"1": "-s -w -X main.version=1.2.3 -X downex/internal/update.PublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= " +
			"-X downex/internal/update.DefaultFeedURL=https://releases.example.test/downex/update.json?channel=stable",
	} {
		out, err := shell(t, append(env, "RELEASE="+release), `daemon_ldflags`)
		if err != nil || out != want {
			t.Errorf("RELEASE=%s: daemon_ldflags = %q, %v; want %q", release, out, err, want)
		}
	}
	out, err := shell(t, releaseEnv("VERSION=dev"), `daemon_ldflags`)
	if err != nil || out != "-X main.version=dev" {
		t.Errorf("without key and feed: daemon_ldflags = %q, %v", out, err)
	}
}

// TestReadExtensionIDs: ext/EXTENSION_ID.txt holds one ID per line with # comments and blank
// lines; the first ID is the one EXTENSION.txt shows, all of them go to downex-nmhost.
func TestReadExtensionIDs(t *testing.T) {
	const (
		dev = "dlgpbdafcmciojddbbjikgeeefipejcn"
		cws = "abcdefghijklmnopabcdefghijklmnop"
	)
	write := func(t *testing.T, text string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "EXTENSION_ID.txt")
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(p)
	}
	read := `read_extension_ids "$1" && printf '%s|%s' "$EXTENSION_ID" "$EXTENSION_IDS"`
	for text, want := range map[string]string{
		dev + "\n": dev + "|" + dev,
		"# the unpacked build\r\n" + dev + "\r\n\r\n  " + cws + "  # Chrome Web Store\r\n" + dev: dev + "|" + dev + "," + cws,
		"# Chrome Web Store\n" + cws + "\n# dev\n" + dev:                                         cws + "|" + cws + "," + dev,
		dev: dev + "|" + dev, // no final line break
	} {
		out, err := shell(t, nil, read, write(t, text))
		if err != nil || out != want {
			t.Errorf("%q: %q, %v; want %q", text, out, err, want)
		}
	}
	for text, want := range map[string]string{
		"":                                 "holds no Chromium extension ID",
		"# only a comment\n\n":             "holds no Chromium extension ID",
		strings.ToUpper(dev):               "is not a Chromium extension ID",
		dev + " " + cws + "\n":             "is not a Chromium extension ID",
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz": "is not a Chromium extension ID",
	} {
		out, err := shell(t, nil, read, write(t, text))
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("%q: %q, %v; want an error with %q", text, out, err, want)
		}
	}
	// The repository's own file.
	if out, err := shell(t, nil, `read_extension_ids && printf '%s' "$EXTENSION_IDS"`); err != nil || out == "" {
		t.Errorf("ext/EXTENSION_ID.txt: %q, %v", out, err)
	}
}
