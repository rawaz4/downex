package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/avscan"
	"downex/internal/engine"
	"downex/internal/filemark"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// The integration of the M6 areas into the Manager: settings conversion and
// persistence, the network runtime, post-processing of completed downloads and the
// auth_required code.

// TestM6SettingsRoundTrip: the network, desktop and update settings go through
// UpdateSettings, are stored, come back from GetSettings and are read again by the next
// daemon; new site logins get ids.
func TestM6SettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	scanner, err := os.Executable() // an existing program
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOptions{settingsPath: path})
	s := protoSettings(h.m.Settings())
	s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy.example.test:3128",
		Bypass: "<local>;*.corp.test", Username: "proxyuser"}
	s.HostRules = []*downexv1.HostRule{{Pattern: "*.Example.test", MaxConnections: 2, SpeedLimit: 1 << 20}}
	s.SiteLogins = []*downexv1.SiteLogin{{UrlPattern: "https://files.example.test/*", Username: "alice", HasPassword: true}}
	s.ClipboardMonitor, s.MarkDownloadedFiles = true, false
	s.AvScanEnabled, s.AvScanCommand, s.AvScanArgs = true, scanner, "--scan [File]"
	s.UpdateFeedUrl, s.AutoCheckUpdates = "https://updates.example.test/downex.json", false
	res, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if err != nil {
		t.Fatal(err)
	}
	got := res.Msg.GetSettings()
	login := got.GetSiteLogins()
	switch {
	case got.GetProxy().GetMode() != downexv1.ProxyMode_PROXY_MODE_MANUAL || got.GetProxy().GetHttpProxy() != "proxy.example.test:3128" ||
		got.GetProxy().GetUsername() != "proxyuser":
		t.Fatalf("proxy = %v", got.GetProxy())
	case len(got.GetHostRules()) != 1 || got.GetHostRules()[0].GetPattern() != "*.example.test" || got.GetHostRules()[0].GetMaxConnections() != 2:
		t.Fatalf("host rules = %v", got.GetHostRules())
	case len(login) != 1 || login[0].GetId() == "" || login[0].GetHasPassword():
		t.Fatalf("site logins = %v: want an assigned id and no password", login)
	case !got.GetClipboardMonitor() || got.GetMarkDownloadedFiles() || !got.GetAvScanEnabled() || got.GetAvScanArgs() != "--scan [File]":
		t.Fatalf("desktop settings = %v", got)
	case got.GetUpdateFeedUrl() != "https://updates.example.test/downex.json" || got.GetAutoCheckUpdates():
		t.Fatalf("update settings = %v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"network", "desktop", "update"} {
		if _, ok := file[k]; !ok {
			t.Errorf("the settings file has no %q: %s", k, data)
		}
	}
	if strings.Contains(string(data), "has_password") {
		t.Errorf("the settings file has password state: %s", data)
	}
	// A second update that keeps the login keeps its id.
	got.SiteLogins[0].Username = "alice2"
	res, err = h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: got}))
	if err != nil {
		t.Fatal(err)
	}
	if l := res.Msg.GetSettings().GetSiteLogins(); len(l) != 1 || l[0].GetId() != login[0].GetId() || l[0].GetUsername() != "alice2" {
		t.Fatalf("site logins after an edit = %v, want id %s", l, login[0].GetId())
	}
	want := h.m.Settings()

	h2 := newHarness(t, harnessOptions{settingsPath: path})
	if s := h2.m.Settings(); !s.Equal(want) {
		t.Fatalf("reloaded settings = %+v\nwant %+v", s, want)
	}

	// Invalid values are refused with the field's name.
	for _, c := range []struct {
		name string
		edit func(*downexv1.Settings)
		want string
	}{
		{"proxy", func(s *downexv1.Settings) {
			s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_PAC, PacUrl: "ftp://x"}
		}, "pac_url"},
		{"host rule", func(s *downexv1.Settings) { s.HostRules = []*downexv1.HostRule{{Pattern: "a b"}} }, "host rule"},
		{"login id", func(s *downexv1.Settings) {
			s.SiteLogins = []*downexv1.SiteLogin{{Id: "../x", UrlPattern: "*.example.test"}}
		}, "site login"},
		{"scanner", func(s *downexv1.Settings) { s.AvScanEnabled, s.AvScanCommand = true, "" }, "av_scan_command"},
		{"feed", func(s *downexv1.Settings) { s.UpdateFeedUrl = "http://updates.example.test/f.json" }, "update_feed_url"},
	} {
		s := protoSettings(h.m.Settings())
		c.edit(s)
		_, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: UpdateSettings = %v, want InvalidArgument naming %s", c.name, err, c.want)
		}
	}
}

// TestM6SettingsFromOlderFile: a settings file of 0.3 (without the M6 objects) gets
// their defaults; a file with invalid entries loses just those.
func TestM6SettingsFromOlderFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFileName)
	if err := os.WriteFile(path, []byte(`{"max_concurrent_downloads": 2, "default_dir": ""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Network.Proxy.Mode != ProxyModeSystem || !s.Desktop.MarkDownloadedFiles || s.Desktop.AVScanArgs != avscan.DefaultArgs ||
		!s.Update.AutoCheck || s.Update.FeedURL != "" {
		t.Fatalf("defaults of an older file = %+v %+v %+v", s.Network, s.Desktop, s.Update)
	}

	bad := `{"network": {"proxy": {"mode": 2, "http_proxy": "ftp://nope"},
		"host_rules": [{"pattern": "ok.test", "max_connections": 2}, {"pattern": "bad pattern"}, {"pattern": "ok.test"}],
		"site_logins": [{"id": "", "url_pattern": "*.ok.test"}, {"id": "x y", "url_pattern": "*.b.test"}]},
		"desktop": {"av_scan_enabled": true}, "update": {"feed_url": "http://plain.test/f"}}`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	got, problems := raw.repair(func() (string, error) { return dir, nil })
	n := got.Network
	switch {
	case n.Proxy.Mode != ProxyModeSystem:
		t.Errorf("an invalid manual proxy = %+v, want the system proxy", n.Proxy)
	case len(n.HostRules) != 1 || n.HostRules[0].Pattern != "ok.test":
		t.Errorf("host rules = %+v, want only the valid one", n.HostRules)
	case len(n.SiteLogins) != 1 || n.SiteLogins[0].ID == "":
		t.Errorf("site logins = %+v, want the valid one with an id", n.SiteLogins)
	case got.Desktop.AVScanEnabled || got.Update.FeedURL != "":
		t.Errorf("desktop %+v, update %+v: want scanning and updates off", got.Desktop, got.Update)
	}
	if len(problems) < 6 {
		t.Errorf("problems = %q, want one per replaced value", problems)
	}
}

// TestNetworkRuntimeFollowsSettings: the Manager hands changed network settings to the
// runtime before UpdateSettings returns.
func TestNetworkRuntimeFollowsSettings(t *testing.T) {
	rt := NewNetworkRuntime(newMemKeyring(), nil)
	h := newHarness(t, harnessOptions{network: rt, settings: func(s *downexv1.Settings) {
		s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_NONE}
	}})
	target, _ := url.Parse("http://files.example.test/a.bin")
	if routes, err := rt.ResolveProxy(context.Background(), target); err != nil || len(routes) != 0 {
		t.Fatalf("NONE: routes %v, %v", routes, err)
	}
	s := protoSettings(h.m.Settings())
	s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy.example.test:3128"}
	s.HostRules = []*downexv1.HostRule{{Pattern: "files.example.test", MaxConnections: 1, SpeedLimit: 4096}}
	if _, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); err != nil {
		t.Fatal(err)
	}
	routes, err := rt.ResolveProxy(context.Background(), target)
	if err != nil || len(routes) != 1 || routes[0].Kind != transport.ProxyHTTP || routes[0].Addr != "proxy.example.test:3128" {
		t.Fatalf("MANUAL: routes %v, %v", routes, err)
	}
	// The engine passes a URL's host, with its port when the URL names one.
	for _, host := range []string{"files.example.test", "files.example.test:8443", "FILES.example.test"} {
		if n := rt.HostMaxConnections(host); n != 1 {
			t.Errorf("host rule cap for %s = %d", host, n)
		}
	}
	if n := rt.HostMaxConnections("other.example.test:8443"); n != 0 {
		t.Errorf("host rule cap for another host = %d", n)
	}
	if l := rt.HostLimiter("files.example.test:8443"); l == nil || l != rt.HostLimiter("files.example.test") {
		t.Errorf("the host limiter differs by port: %v", l)
	}
	if l := rt.HostLimiter("files.example.test"); l == nil || l.Limit() != 4096 {
		t.Errorf("host limiter = %v", l)
	}
	if u, err := rt.ProxyURL(mustRequest(t, target.String())); err != nil || u == nil || u.String() != "http://proxy.example.test:3128" {
		t.Errorf("ProxyURL = %v, %v", u, err)
	}
}

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// recordingPost is a PostProcessor that records what it was given.
type recordingPost struct {
	mu     sync.Mutex
	marked []string
	exit   int
}

func (p *recordingPost) processor() *PostProcessor {
	return &PostProcessor{
		Mark: func(path string, _ filemark.Origin) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.marked = append(p.marked, filepath.Base(path))
			return nil
		},
		Scan: func(context.Context, string, string, string) (avscan.Result, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			return avscan.Result{ExitCode: p.exit}, nil
		},
		Now: time.Now,
		Log: slog.New(slog.DiscardHandler),
	}
}

func (p *recordingPost) markedFiles() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.marked...)
}

// TestCompletedDownloadIsPostProcessed: a completed download is marked and scanned
// before the completion is announced; a scan that reports a problem leaves a note,
// the download completed, and a NoticeScanProblem.
func TestCompletedDownloadIsPostProcessed(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 6, ETag: `"m6"`})
	scanner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rp := &recordingPost{exit: 3}
	h := newHarness(t, harnessOptions{post: rp.processor(), settings: func(s *downexv1.Settings) {
		s.MarkDownloadedFiles, s.AvScanEnabled, s.AvScanCommand = true, true, scanner
	}})
	notices, cancel := h.m.Subscribe()
	defer cancel()

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/scanned.bin"})
	done := h.waitCompleted(d.GetId())
	if got := rp.markedFiles(); len(got) != 1 || got[0] != done.GetFilename() {
		t.Fatalf("marked %q, want %q", got, done.GetFilename())
	}
	if !strings.Contains(done.GetError(), "exit code 3") || done.GetErrorCode() != "" {
		t.Fatalf("completed download error %q code %q, want the scan's note", done.GetError(), done.GetErrorCode())
	}
	checkFile(t, done, 6, 64<<10)
	var completed, problem bool
	deadline := time.After(waitTimeout)
	for !problem {
		select {
		case n := <-notices:
			switch n.Kind {
			case NoticeCompleted:
				completed = true
			case NoticeScanProblem:
				if !completed {
					t.Fatal("the scan problem was announced before the completion")
				}
				if n.Text != "Scanner reported a problem with "+done.GetFilename() {
					t.Fatalf("scan notice = %q", n.Text)
				}
				problem = true
			}
		case <-deadline:
			t.Fatal("no NoticeScanProblem")
		}
	}
	// The note survives a restart (it is stored).
	if rec := storedState(t, h.st, d.GetId()); !strings.Contains(rec.Error, "exit code 3") {
		t.Fatalf("stored error = %q", rec.Error)
	}

	// With marking and scanning off nothing runs.
	s := protoSettings(h.m.Settings())
	s.MarkDownloadedFiles, s.AvScanEnabled = false, false
	if _, err := h.m.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	d2 := h.add(&downexv1.AddRequest{Url: ts.URL + "/plain.bin"})
	if got := h.waitCompleted(d2.GetId()); got.GetError() != "" {
		t.Fatalf("error %q without scanning", got.GetError())
	}
	if got := rp.markedFiles(); len(got) != 1 {
		t.Fatalf("marked %q with marking off", got)
	}
}

// TestAuthRequiredCode: a download refused with 401 fails with auth_required, from the
// live error and from the stored message alike.
func TestAuthRequiredCode(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("probe: HTTP 401: %w", engine.ErrAuthRequired),
		fmt.Errorf("bytes 0-9: HTTP 407: %w", transport.ErrAuthRequired),
	} {
		if c := ErrorCode(err); c != CodeAuthRequired {
			t.Errorf("ErrorCode(%v) = %q", err, c)
		}
		if c := codeFromMessage(err.Error()); c != CodeAuthRequired {
			t.Errorf("codeFromMessage(%q) = %q", err, c)
		}
	}
}
