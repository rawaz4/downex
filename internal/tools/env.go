package tools

import (
	"os"
	"runtime"
	"strings"
)

// EnvSpec is what Downex sets in a child's environment (docs/SITE-MEDIA-SPEC.md §4.7).
type EnvSpec struct {
	// DenoDir is the folder of the Deno executable, put first on PATH ("" = none).
	DenoDir string
	// TempDir becomes TEMP, TMP and TMPDIR: a private folder of this run.
	TempDir string
	// DenoCache becomes DENO_DIR ("" = not set).
	DenoCache string
	// Set holds more NAME=value entries, set last (yt-dlp's proxy variables with
	// credentials). A name given here replaces any value of that name.
	Set []string
}

// Variables passed on from the daemon's environment (§4.7); every other one is dropped.
var (
	passWindows = []string{"SystemRoot", "SystemDrive", "windir", "PATHEXT", "NUMBER_OF_PROCESSORS",
		"PROCESSOR_ARCHITECTURE", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA"}
	passUnix = []string{"HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ"}
	passBoth = []string{"SSL_CERT_FILE", "SSL_CERT_DIR"}
)

// darwinRoots is macOS's list of the system's trusted roots, which every macOS since 10.x
// ships for programs that cannot use the Keychain.
const darwinRoots = "/etc/ssl/cert.pem"

// Env returns the environment of a child of this daemon: Environment for the running
// system with os.Getenv and the manager's pass-through prefix.
func (m *Manager) Env(s EnvSpec) []string {
	return Environment(runtime.GOOS, os.Getenv, m.cfg.PassEnvPrefix, os.Environ(), s)
}

// Environment builds a child's environment from an allow-list, never by inheritance
// (docs/SITE-MEDIA-SPEC.md §4.7). From the parent's environment (getenv) it passes on,
// when set: on Windows SystemRoot, SystemDrive, windir, PATHEXT, NUMBER_OF_PROCESSORS,
// PROCESSOR_ARCHITECTURE, USERPROFILE, HOMEDRIVE, HOMEPATH, APPDATA and LOCALAPPDATA;
// elsewhere HOME, LANG, LC_ALL, LC_CTYPE and TZ; everywhere SSL_CERT_FILE and
// SSL_CERT_DIR (on macOS SSL_CERT_FILE=/etc/ssl/cert.pem when neither is set); and every variable of environ whose name starts with passPrefix (e2etest
// builds: "DOWNEX_E2E_"; "" passes none). It then sets PATH (s.DenoDir, then the
// system's folders), TEMP/TMP/TMPDIR, DENO_DIR, DENO_NO_UPDATE_CHECK=1, NO_COLOR=1 and
// PYTHONUTF8=1, and finally s.Set. Each name appears once.
func Environment(goos string, getenv func(string) string, passPrefix string, environ []string, s EnvSpec) []string {
	var env []string
	seen := map[string]int{}
	set := func(name, value string) {
		key := name
		if goos == "windows" {
			key = strings.ToUpper(name)
		}
		if i, ok := seen[key]; ok {
			env[i] = name + "=" + value
			return
		}
		seen[key] = len(env)
		env = append(env, name+"="+value)
	}
	pass := passUnix
	if goos == "windows" {
		pass = passWindows
	}
	for _, name := range append(append([]string(nil), pass...), passBoth...) {
		if v := getenv(name); v != "" {
			set(name, v)
		}
	}
	if goos == "darwin" && getenv("SSL_CERT_FILE") == "" && getenv("SSL_CERT_DIR") == "" {
		// yt-dlp's Python cannot read the Keychain: with no-certifi it would trust no root
		// at all. /etc/ssl/cert.pem is the system's root list (§4.7).
		set("SSL_CERT_FILE", darwinRoots)
	}
	if passPrefix != "" {
		for _, kv := range environ {
			name, value, ok := strings.Cut(kv, "=")
			if ok && name != "" && strings.HasPrefix(name, passPrefix) {
				set(name, value)
			}
		}
	}
	set("PATH", strings.Join(append(nonEmpty(s.DenoDir), systemPath(goos, getenv)...), pathListSeparator(goos)))
	if s.TempDir != "" {
		set("TEMP", s.TempDir)
		set("TMP", s.TempDir)
		set("TMPDIR", s.TempDir)
	}
	if s.DenoCache != "" {
		set("DENO_DIR", s.DenoCache)
	}
	set("DENO_NO_UPDATE_CHECK", "1")
	set("NO_COLOR", "1")
	set("PYTHONUTF8", "1")
	for _, kv := range s.Set {
		if name, value, ok := strings.Cut(kv, "="); ok && name != "" {
			set(name, value)
		}
	}
	return env
}

// systemPath is the system's own program folders, which children get on PATH after
// the Deno folder.
func systemPath(goos string, getenv func(string) string) []string {
	if goos == "windows" {
		root := getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		root = strings.TrimRight(root, `\`)
		return []string{root + `\System32`, root, root + `\System32\Wbem`}
	}
	return []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
}

func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return ":"
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
