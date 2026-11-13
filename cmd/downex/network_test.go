package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

// secretCallsOf returns the secret requests of type T.
func secretCallsOf[T proto.Message](r m6Record) []T {
	var out []T
	for _, m := range r.secretCalls {
		if v, ok := m.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestProxyCommands(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()

	if r := h.run(ctx, "proxy"); r.code != exitOK || !strings.Contains(r.stdout, "Mode:  system") {
		t.Fatalf("proxy: %v", r)
	}
	r := h.run(ctx, "proxy", "set", "manual", "--http", "p.test:8080", "--socks", "SOCKS5://s.test:1080/",
		"--bypass", "<local>, *.corp.test;;10.0.0.1")
	if r.code != exitOK || !strings.Contains(r.stdout, "HTTP proxy:   p.test:8080") || !strings.Contains(r.stdout, "socks5://s.test:1080") {
		t.Fatalf("proxy set manual: %v", r)
	}
	u := f.rec().updates[0]
	want := &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "p.test:8080",
		SocksProxy: "socks5://s.test:1080", Bypass: "<local>;*.corp.test;10.0.0.1"}
	if !proto.Equal(u.GetProxy(), want) {
		t.Errorf("proxy %v, want %v", u.GetProxy(), want)
	}
	// Every other setting is written back as it was.
	if u.GetMaxConcurrentDownloads() != 3 || len(u.GetCategories()) != 2 || !u.GetConfirmBrowserDownloads() {
		t.Errorf("UpdateSettings lost fields: %v", u)
	}

	// Switching modes keeps the manual servers for later.
	if r = h.run(ctx, "proxy", "set", "none"); r.code != exitOK || !strings.Contains(r.stdout, "Mode:  none") {
		t.Errorf("proxy set none: %v", r)
	}
	if p := f.currentSettings().GetProxy(); p.GetMode() != downexv1.ProxyMode_PROXY_MODE_NONE || p.GetHttpProxy() != "p.test:8080" {
		t.Errorf("proxy after none: %v", p)
	}
	if r = h.run(ctx, "proxy", "set", "manual", "--http", ""); r.code != exitOK {
		t.Errorf("proxy set manual (stored servers): %v", r)
	}
	if p := f.currentSettings().GetProxy(); p.GetMode() != downexv1.ProxyMode_PROXY_MODE_MANUAL || p.GetHttpProxy() != "" ||
		p.GetSocksProxy() != "socks5://s.test:1080" {
		t.Errorf("proxy after manual: %v", p)
	}

	// pac with a user: the password is asked for and stored through SetSecret only.
	h.password = "pr0xy-pw"
	r = h.run(ctx, "proxy", "set", "pac", "https://wpad.test/proxy.pac", "--user", "alice", "--json")
	if r.code != exitOK {
		t.Fatalf("proxy set pac: %v", r)
	}
	var v proxyView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Mode != "pac" || v.PACURL != "https://wpad.test/proxy.pac" ||
		v.Username != "alice" || !v.HasPassword {
		t.Errorf("proxy set pac --json: %+v %v\n%s", v, err, r.stdout)
	}
	if p := h.promptsShown(); len(p) != 1 || p[0] != "Proxy password for alice: " {
		t.Errorf("prompts %q", p)
	}
	sets := secretCallsOf[*downexv1.SetSecretRequest](f.m6())
	if len(sets) != 1 || sets[0].GetKind() != "proxy" || sets[0].GetId() != "" || sets[0].GetPassword() != "pr0xy-pw" {
		t.Errorf("SetSecret calls %v", sets)
	}
	for _, s := range f.rec().updates {
		if strings.Contains(s.String(), "pr0xy-pw") {
			t.Errorf("the password reached the settings: %v", s)
		}
	}
	if r = h.run(ctx, "proxy", "show"); !strings.Contains(r.stdout, "User:                  alice (password stored)") ||
		!strings.Contains(r.stdout, "Configuration script:  https://wpad.test/proxy.pac") || strings.Contains(r.stdout, "pr0xy-pw") {
		t.Errorf("proxy show: %v", r)
	}
	// The stored script URL is kept by "pac" alone; --user "" removes the user and password.
	if r = h.run(ctx, "proxy", "set", "pac", "--user", ""); r.code != exitOK || strings.Contains(r.stdout, "User:") {
		t.Errorf("proxy set pac --user \"\": %v", r)
	}
	if clears := secretCallsOf[*downexv1.ClearSecretRequest](f.m6()); len(clears) != 1 || clears[0].GetKind() != "proxy" {
		t.Errorf("ClearSecret calls %v", clears)
	}
	if p := f.currentSettings().GetProxy(); p.GetUsername() != "" || p.GetHasPassword() || p.GetPacUrl() != "https://wpad.test/proxy.pac" {
		t.Errorf("proxy after clearing the user: %v", p)
	}
	if n := len(h.promptsShown()); n != 1 {
		t.Errorf("%d prompts, want none for --user \"\"", n-1)
	}

	// A failed prompt changes nothing.
	n := len(f.rec().updates)
	h.pwErr = errors.New("prompt failed")
	if r = h.run(ctx, "proxy", "set", "manual", "--http", "q.test:3128", "--user", "bob"); r.code != exitError || !strings.Contains(r.stderr, "prompt failed") {
		t.Errorf("failed prompt: %v", r)
	}
	h.pwErr = nil

	f.do(func() { f.settings.Proxy = nil })
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"proxy", "set"}, "set expects a mode"},
		{[]string{"proxy", "set", "bogus"}, `unknown proxy mode "bogus"`},
		{[]string{"proxy", "set", "none", "x"}, "too many arguments"},
		{[]string{"proxy", "set", "none", "--http", "h.test:1"}, `--http applies only to "proxy set manual"`},
		{[]string{"proxy", "set", "system", "--bypass", "x"}, `--bypass applies only to "proxy set manual"`},
		{[]string{"proxy", "set", "none", "--user", "u"}, `--user does not apply to "proxy set none"`},
		{[]string{"proxy", "set", "manual", "--http", "nohost"}, "want host:port"},
		{[]string{"proxy", "set", "manual", "--http", "h.test:99999"}, "want host:port"},
		{[]string{"proxy", "set", "manual", "--https", "http://h.test:8080"}, "want host:port"},
		{[]string{"proxy", "set", "manual", "--socks", "http://h.test:1"}, "want socks5://host:port"},
		{[]string{"proxy", "set", "manual", "--socks", "socks5://u:p@h.test:1"}, "put the user name in --user"},
		{[]string{"proxy", "set", "manual", "--bypass", "a b"}, `invalid host pattern "a b"`},
		{[]string{"proxy", "set", "manual"}, "manual needs --http, --https or --socks"},
		{[]string{"proxy", "set", "pac"}, "pac needs the URL"},
		{[]string{"proxy", "set", "pac", "ftp://h.test/p.pac"}, "want an http, https or file URL"},
		{[]string{"proxy", "show", "x"}, "show takes no arguments"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if got := len(f.rec().updates); got != n {
		t.Errorf("invalid proxy settings were sent: %d updates, want %d", got, n)
	}
	if r := h.run(ctx, "proxy", "set", "pac", "file:///etc/proxy.pac"); r.code != exitOK || !strings.Contains(r.stdout, "file:///etc/proxy.pac") {
		t.Errorf("file PAC URL: %v", r)
	}
}

func TestLoginCommands(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	const pw = "s3cret-pw"

	if r := h.run(ctx, "login"); r.code != exitOK || r.stdout != "No site logins.\n" {
		t.Fatalf("login: %v", r)
	}
	h.password = pw
	r := h.run(ctx, "login", "add", "https://files.test/*", "--user", "bob")
	if r.code != exitOK || r.stdout != "Added site login L1: https://files.test/* as bob.\n" {
		t.Fatalf("login add: %v", r)
	}
	if p := h.promptsShown(); len(p) != 1 || p[0] != "Password for bob at https://files.test/*: " {
		t.Errorf("prompts %q", p)
	}
	u := f.rec().updates
	if len(u) != 1 || len(u[0].GetSiteLogins()) != 1 || u[0].GetSiteLogins()[0].GetUrlPattern() != "https://files.test/*" ||
		u[0].GetSiteLogins()[0].GetUsername() != "bob" || u[0].GetSiteLogins()[0].GetId() != "" ||
		u[0].GetMaxConcurrentDownloads() != 3 || len(u[0].GetCategories()) != 2 {
		t.Fatalf("UpdateSettings %v", u)
	}
	sets := secretCallsOf[*downexv1.SetSecretRequest](f.m6())
	if len(sets) != 1 || sets[0].GetKind() != "site_login" || sets[0].GetId() != "L1" || sets[0].GetPassword() != pw {
		t.Errorf("SetSecret calls %v", sets)
	}

	r = h.run(ctx, "login", "ls")
	if r.code != exitOK || !strings.Contains(r.stdout, "L1  https://files.test/*  bob   stored") {
		t.Errorf("login ls: %v", r)
	}
	r = h.run(ctx, "login", "ls", "--json")
	var views []siteLoginView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 1 || views[0].ID != "L1" || !views[0].HasPassword {
		t.Errorf("login ls --json: %+v %v\n%s", views, err, r.stdout)
	}
	r = h.run(ctx, "settings", "--json")
	var sv settingsView
	if err := json.Unmarshal([]byte(r.stdout), &sv); err != nil || len(sv.SiteLogins) != 1 || sv.SiteLogins[0].Username != "bob" {
		t.Errorf("settings --json: %+v %v", sv, err)
	}

	// The same pattern and user again replaces the password only.
	h.password = "n3w-pw"
	if r = h.run(ctx, "login", "add", "https://files.test/*", "--user", "bob"); r.code != exitOK ||
		r.stdout != "Replaced the password of site login L1: https://files.test/* as bob.\n" {
		t.Errorf("login add again: %v", r)
	}
	if n := len(f.rec().updates); n != 1 {
		t.Errorf("%d settings updates, want none for a known login", n-1)
	}

	// A new login whose password cannot be stored is taken out again.
	f.do(func() { f.secretErr = connect.NewError(connect.CodeInternal, errors.New("credential store locked")) })
	r = h.run(ctx, "login", "add", "*.other.test", "--user", "eve")
	if r.code != exitError || !strings.Contains(r.stderr, "login add: credential store locked") {
		t.Errorf("login add with a failing store: %v", r)
	}
	if ls := f.currentSettings().GetSiteLogins(); len(ls) != 1 || ls[0].GetId() != "L1" {
		t.Errorf("site logins after the failed add: %v", ls)
	}
	f.do(func() { f.secretErr = nil })

	r = h.run(ctx, "login", "rm", "L1")
	if r.code != exitOK || r.stdout != "Removed site login L1 (https://files.test/* as bob).\n" {
		t.Errorf("login rm: %v", r)
	}
	clears := secretCallsOf[*downexv1.ClearSecretRequest](f.m6())
	if len(clears) != 1 || clears[0].GetKind() != "site_login" || clears[0].GetId() != "L1" {
		t.Errorf("ClearSecret calls %v", clears)
	}
	if ls := f.currentSettings().GetSiteLogins(); len(ls) != 0 {
		t.Errorf("site logins after rm: %v", ls)
	}

	prompts := len(h.promptsShown())
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"login", "add", "https://x.test/*"}, "add needs --user name"},
		{[]string{"login", "add", "--user", "u"}, "add expects exactly one URL pattern"},
		{[]string{"login", "add", "https://u@x.test/*", "--user", "u"}, "put the user name in --user"},
		{[]string{"login", "add", "a b", "--user", "u"}, `invalid URL pattern "a b"`},
		{[]string{"login", "add", "x.test", "--user", "u\x07"}, "control characters"},
		{[]string{"login", "rm"}, "rm expects exactly one site login id"},
		{[]string{"login", "rm", "L9"}, `no site login with id "L9"`},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if n := len(h.promptsShown()); n != prompts {
		t.Errorf("invalid arguments asked for a password %d times", n-prompts)
	}

	// No output ever carries a password.
	for _, args := range [][]string{{"login", "ls", "--json"}, {"settings"}, {"settings", "--json"}, {"proxy", "show", "--json"}} {
		if r := h.run(ctx, args...); strings.Contains(r.stdout+r.stderr, pw) || strings.Contains(r.stdout+r.stderr, "n3w-pw") {
			t.Errorf("downex %q printed a password: %v", args, r)
		}
	}
}

func TestSettingsM6(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.settings.MarkDownloadedFiles, f.settings.AutoCheckUpdates = true, true
		f.settings.HostRules = []*downexv1.HostRule{{Pattern: "*.cdn.test", MaxConnections: 2, SpeedLimit: 1 << 20}}
		f.settings.SiteLogins = []*downexv1.SiteLogin{{Id: "L7", UrlPattern: "*.x.test", Username: "u"}}
		f.settings.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpsProxy: "p.test:3128"}
	})
	r := h.run(ctx, "settings")
	for _, want := range []string{"Clipboard monitor:          off", "Mark downloaded files:      on", "Virus scan:                 off",
		"Proxy:                      manual (https p.test:3128)", "Update feed:                none (updates off)",
		"Automatic update checks:    on", "Site logins:                1", "Host rules:", "*.cdn.test  2            1.0 MB/s"} {
		if r.code != exitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("settings: want %q in\n%s", want, r.stdout)
		}
	}

	scanner := filepath.Join(t.TempDir(), "scan")
	r = h.run(ctx, "settings", "--clipboard", "on", "--mark-files", "off", "--av-scan", "on", "--av-command", scanner,
		"--av-args", "--quiet [File]", "--update-feed", "https://updates.test/feed.json", "--auto-update", "off", "--json")
	if r.code != exitOK {
		t.Fatalf("settings with M6 flags: %v", r)
	}
	u := f.rec().updates[0]
	if !u.GetClipboardMonitor() || u.GetMarkDownloadedFiles() || !u.GetAvScanEnabled() || u.GetAvScanCommand() != scanner ||
		u.GetAvScanArgs() != "--quiet [File]" || u.GetUpdateFeedUrl() != "https://updates.test/feed.json" || u.GetAutoCheckUpdates() {
		t.Errorf("UpdateSettings %v", u)
	}
	// Read, modify, write: the network settings and the rest are written back unchanged.
	if u.GetProxy().GetHttpsProxy() != "p.test:3128" || len(u.GetHostRules()) != 1 || len(u.GetSiteLogins()) != 1 ||
		u.GetSiteLogins()[0].GetId() != "L7" || len(u.GetCategories()) != 2 || u.GetMaxConcurrentDownloads() != 3 {
		t.Errorf("UpdateSettings lost fields: %v", u)
	}
	var v settingsView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || !v.ClipboardMonitor || v.AVScanCommand != scanner ||
		v.UpdateFeedURL != "https://updates.test/feed.json" || v.Proxy.Mode != "manual" || len(v.HostRules) != 1 ||
		v.HostRules[0].MaxConnections != 2 || len(v.SiteLogins) != 1 {
		t.Errorf("settings --json: %+v %v\n%s", v, err, r.stdout)
	}
	if r = h.run(ctx, "settings"); !strings.Contains(r.stdout, "Virus scan:                 on: "+scanner+" --quiet [File]") {
		t.Errorf("settings after the scan was set:\n%s", r.stdout)
	}

	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"settings", "--av-command", "scan.exe"}, "--av-command must be an absolute path"},
		{[]string{"settings", "--av-args", "a\nb"}, "--av-args must not contain control characters"},
		{[]string{"settings", "--update-feed", "http://updates.test/feed.json"}, "want an https URL"},
		{[]string{"settings", "--update-feed", "https://u:p@updates.test/f"}, "want an https URL without credentials"},
		{[]string{"settings", "--clipboard", "maybe"}, "want on or off"},
		{[]string{"settings", "--av-command", ""}, "the virus scan needs the scanner"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if n := len(f.rec().updates); n != 1 {
		t.Errorf("invalid settings were sent: %d updates", n)
	}
	// Turning the scan off without a scanner is fine.
	if r := h.run(ctx, "settings", "--av-scan", "off", "--av-command", ""); r.code != exitOK {
		t.Errorf("scan off: %v", r)
	}
}

func TestUpdateCommands(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()

	if r := h.run(ctx, "update", "check"); r.code != exitOK || r.stdout != "Downex 0.3.3 is up to date.\n" {
		t.Errorf("update check: %v", r)
	}
	published := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	f.do(func() {
		f.update = &downexv1.UpdateInfo{CurrentVersion: "0.3.3", LatestVersion: "0.4.0", Available: true,
			Notes: "Queues.\r\nProxies \x1b[31m", PublishedAt: timestamppb.New(published)}
	})
	r := h.run(ctx, "update", "check")
	for _, want := range []string{"Downex 0.4.0 is available (this is 0.3.3), published 2026-10-01.", "  Queues.\n",
		`  Proxies \x1b[31m`, "Install it with: downex update install"} {
		if r.code != exitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("update check: want %q in %v", want, r)
		}
	}
	r = h.run(ctx, "update", "check", "--json")
	var v updateInfoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || !v.Available || v.LatestVersion != "0.4.0" || v.PublishedAt == nil {
		t.Errorf("update check --json: %+v %v\n%s", v, err, r.stdout)
	}
	if r = h.run(ctx, "update", "install"); r.code != exitOK || r.stdout != "Installing Downex 0.4.0; downexd stops for the installer.\n" {
		t.Errorf("update install: %v", r)
	}
	if n := f.m6().installs; n != 1 {
		t.Errorf("%d InstallUpdate calls", n)
	}
	f.do(func() {
		f.updateErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("updates are off: no update feed is set"))
	})
	if r = h.run(ctx, "update", "check"); r.code != exitError || !strings.Contains(r.stderr, "update check: updates are off") {
		t.Errorf("update check without a feed: %v", r)
	}
	if r = h.run(ctx, "update", "install", "now"); r.code != exitError || !strings.Contains(r.stderr, "install takes no arguments") {
		t.Errorf("update install now: %v", r)
	}
}

func TestReadSecretLine(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		in, want string
		err      error
	}{
		{"pw\r\nnext line", "pw", nil},
		{"p w \n", "p w ", nil},
		{"no newline", "no newline", nil},
		{"", "", errNoSecret},
		{"\r\n", "", errEmptySecret},
		{strings.Repeat("x", maxSecretBytes) + "\n", strings.Repeat("x", maxSecretBytes), nil},
		{strings.Repeat("x", maxSecretBytes+1) + "\n", "", errSecretTooLong},
		{strings.Repeat("x", 3*maxSecretBytes), "", errSecretTooLong},
		{"\xff\xfe\n", "", errSecretUTF8},
	} {
		got, err := readSecretLine(ctx, strings.NewReader(tc.in))
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("readSecretLine(%.20q) = %.20q, %v; want %.20q, %v", tc.in, got, err, tc.want, tc.err)
		}
	}

	// A canceled prompt returns at once, even though nothing was typed.
	pr, pw := io.Pipe()
	defer pw.Close()
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := readSecretLine(cctx, pr); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled read: %v", err)
	}
}

// TestReadPasswordFromPipe: a password piped in is read without a prompt (scripts).
func TestReadPasswordFromPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.WriteString(w, "piped-pw\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var prompt strings.Builder
	got, err := readPassword(context.Background(), r, &prompt, "Password: ")
	if err != nil || got != "piped-pw" || prompt.Len() != 0 {
		t.Errorf("readPassword = %q, %v; prompt %q", got, err, prompt.String())
	}
}
