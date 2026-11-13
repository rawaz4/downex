package tools

import (
	"slices"
	"strings"
	"testing"
)

func TestEnvironmentWindows(t *testing.T) {
	parent := map[string]string{
		"SystemRoot": `C:\Windows`, "SystemDrive": "C:", "windir": `C:\Windows`, "PATHEXT": ".COM;.EXE",
		"NUMBER_OF_PROCESSORS": "8", "PROCESSOR_ARCHITECTURE": "AMD64", "USERPROFILE": `C:\Users\u`,
		"HOMEDRIVE": "C:", "HOMEPATH": `\Users\u`, "APPDATA": `C:\Users\u\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\u\AppData\Local`, "SSL_CERT_FILE": `C:\certs.pem`,
		"PATH": `C:\Python;C:\evil`, "PYTHONPATH": `C:\evil`, "HTTP_PROXY": "http://leak", "HOME": `C:\Users\u`,
		"DOWNEX_E2E_FAKE": "1", "YTDLP_OPTS": "--exec calc",
	}
	environ := []string{"DOWNEX_E2E_FAKE=1", "YTDLP_OPTS=--exec calc", "DOWNEX_E2E_CLOCK=x=y"}
	env := Environment("windows", func(k string) string { return parent[k] }, "DOWNEX_E2E_", environ, EnvSpec{
		DenoDir: `C:\tools\deno\2.5.0`, TempDir: `C:\tools\run\0123\tmp`, DenoCache: `C:\tools\cache\deno`,
		Set: []string{"HTTPS_PROXY=http://u:p@proxy:8080", "path=ignored-case-dup"},
	})
	want := []string{
		`SystemRoot=C:\Windows`, "SystemDrive=C:", `windir=C:\Windows`, "PATHEXT=.COM;.EXE", "NUMBER_OF_PROCESSORS=8",
		"PROCESSOR_ARCHITECTURE=AMD64", `USERPROFILE=C:\Users\u`, "HOMEDRIVE=C:", `HOMEPATH=\Users\u`,
		`APPDATA=C:\Users\u\AppData\Roaming`, `LOCALAPPDATA=C:\Users\u\AppData\Local`, `SSL_CERT_FILE=C:\certs.pem`,
		"DOWNEX_E2E_FAKE=1", "DOWNEX_E2E_CLOCK=x=y",
		"path=ignored-case-dup", // Set replaces PATH (names are case-insensitive on Windows)
		`TEMP=C:\tools\run\0123\tmp`, `TMP=C:\tools\run\0123\tmp`, `TMPDIR=C:\tools\run\0123\tmp`,
		`DENO_DIR=C:\tools\cache\deno`, "DENO_NO_UPDATE_CHECK=1", "NO_COLOR=1", "PYTHONUTF8=1",
		"HTTPS_PROXY=http://u:p@proxy:8080",
	}
	if !slices.Equal(env, want) {
		t.Errorf("environment:\n got %q\nwant %q", env, want)
	}
	// Without Set, PATH is the Deno folder then the system folders.
	env = Environment("windows", func(k string) string { return parent[k] }, "", environ, EnvSpec{DenoDir: `C:\d`, TempDir: `C:\t`})
	if !slices.Contains(env, `PATH=C:\d;C:\Windows\System32;C:\Windows;C:\Windows\System32\Wbem`) {
		t.Errorf("PATH missing or wrong in %q", env)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "DOWNEX_E2E_") || strings.HasPrefix(kv, "HTTP_PROXY") || strings.HasPrefix(kv, "PYTHONPATH") || strings.HasPrefix(kv, "HOME=") {
			t.Errorf("leaked %q", kv)
		}
	}
}

func TestEnvironmentUnix(t *testing.T) {
	parent := map[string]string{"HOME": "/home/u", "LANG": "en_US.UTF-8", "TZ": "UTC", "SSL_CERT_DIR": "/etc/ssl/certs",
		"PATH": "/opt/evil", "LD_PRELOAD": "/evil.so", "SystemRoot": "x"}
	env := Environment("linux", func(k string) string { return parent[k] }, "", nil, EnvSpec{TempDir: "/t/run/1/tmp"})
	want := []string{"HOME=/home/u", "LANG=en_US.UTF-8", "TZ=UTC", "SSL_CERT_DIR=/etc/ssl/certs",
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TEMP=/t/run/1/tmp", "TMP=/t/run/1/tmp", "TMPDIR=/t/run/1/tmp",
		"DENO_NO_UPDATE_CHECK=1", "NO_COLOR=1", "PYTHONUTF8=1"}
	if !slices.Equal(env, want) {
		t.Errorf("environment:\n got %q\nwant %q", env, want)
	}
}

// On macOS, yt-dlp (Python) cannot read the Keychain: with --compat-options no-certifi it
// trusts nothing and every https request fails ("unable to get local issuer
// certificate"). The child gets the system's root list, /etc/ssl/cert.pem, unless the
// user set a certificate file or folder of their own.
func TestEnvironmentDarwinRoots(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	env := Environment("darwin", get(map[string]string{"HOME": "/Users/u"}), "", nil, EnvSpec{})
	if !slices.Contains(env, "SSL_CERT_FILE=/etc/ssl/cert.pem") {
		t.Errorf("darwin without certificate settings: %q", env)
	}
	for _, own := range []map[string]string{{"SSL_CERT_FILE": "/corp/roots.pem"}, {"SSL_CERT_DIR": "/corp/certs"}} {
		env := Environment("darwin", get(own), "", nil, EnvSpec{})
		if slices.Contains(env, "SSL_CERT_FILE=/etc/ssl/cert.pem") {
			t.Errorf("darwin with %v: the system list replaced the user's: %q", own, env)
		}
	}
	if env := Environment("linux", get(nil), "", nil, EnvSpec{}); slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "SSL_CERT_") }) {
		t.Errorf("linux: %q", env)
	}
}
