package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/update"
)

// TestUpdateSettingsDefaultFeed is a release build (update.DefaultFeedURL set with -X): a
// new installation, a settings file without feed_url and a 0.9.x file with
// "feed_url":"" get the built-in feed, a user's own feed is kept, the file stores the
// built-in feed as "", and an invalid built-in feed is never handed out.
func TestUpdateSettingsDefaultFeed(t *testing.T) {
	old := update.DefaultFeedURL
	t.Cleanup(func() { update.DefaultFeedURL = old })
	const feed = "https://releases.example.test/downex/update.json"
	update.DefaultFeedURL = feed
	if d := DefaultUpdateSettings(); d != (UpdateSettings{FeedURL: feed, AutoCheck: true}) {
		t.Errorf("DefaultUpdateSettings = %+v", d)
	}
	if d := DefaultSettings().Update; d.FeedURL != feed || !d.autoChecks() {
		t.Errorf("DefaultSettings().Update = %+v", d)
	}
	for data, want := range map[string]UpdateSettings{
		`{}`:                                     {FeedURL: feed, AutoCheck: true},
		`{"auto_check":false}`:                   {FeedURL: feed},
		`{"feed_url":""}`:                        {FeedURL: feed, AutoCheck: true},
		`{"feed_url":"","auto_check":false}`:     {FeedURL: feed},
		`{"feed_url":"https://own.test/f.json"}`: {FeedURL: "https://own.test/f.json", AutoCheck: true},
	} {
		var u UpdateSettings
		if err := json.Unmarshal([]byte(data), &u); err != nil || u != want {
			t.Errorf("unmarshal %s = %+v, %v; want %+v", data, u, err, want)
		}
	}

	// A 0.9.x settings file saved with "feed_url":"" gets the built-in feed, and saving
	// it again keeps "" (the built-in feed of whichever build reads it next); a user's
	// own feed is kept on both ways.
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	old09 := `{"max_concurrent_downloads": 3, "update": {"feed_url": "", "auto_check": true}}`
	if err := os.WriteFile(path, []byte(old09), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Update != (UpdateSettings{FeedURL: feed, AutoCheck: true}) || !s.Update.autoChecks() {
		t.Errorf("0.9.x file: update = %+v, want the built-in feed", s.Update)
	}
	p := &downexv1.Settings{}
	s.Update.setProto(p)
	if p.GetUpdateFeedUrl() != feed {
		t.Errorf("0.9.x file: API update_feed_url = %q, want the built-in feed", p.GetUpdateFeedUrl())
	}
	if err := saveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || strings.Contains(string(data), feed) || !strings.Contains(string(data), `"feed_url": ""`) {
		t.Errorf("saved file keeps the built-in feed as \"\": %s, %v", data, err)
	}
	if again, err := loadSettings(path); err != nil || again.Update != s.Update {
		t.Errorf("reloaded update = %+v, %v; want %+v", again.Update, err, s.Update)
	}
	own := UpdateSettings{FeedURL: "https://own.test/f.json"}
	if out, err := json.Marshal(own); err != nil || string(out) != `{"feed_url":"https://own.test/f.json","auto_check":false}` {
		t.Errorf("marshal of a user's feed = %s, %v", out, err)
	}

	// A client that clears the address gets the built-in feed (updates are turned off
	// with auto_check), a user's own feed is kept, and an invalid one read from disk
	// is replaced by the built-in feed.
	if n, err := (UpdateSettings{AutoCheck: false}).normalize(); err != nil || n != (UpdateSettings{FeedURL: feed}) {
		t.Errorf("normalize of an empty feed = %+v, %v; want the built-in feed", n, err)
	}
	if n, err := own.normalize(); err != nil || n != own {
		t.Errorf("normalize of a user's feed = %+v, %v", n, err)
	}
	if got, problems := (UpdateSettings{FeedURL: "http://x.test/f", AutoCheck: true}).repair(); got != (UpdateSettings{FeedURL: feed, AutoCheck: true}) ||
		len(problems) != 1 || !strings.HasSuffix(problems[0], "; using the built-in update feed") {
		t.Errorf("repair = %+v, %q; want the built-in feed", got, problems)
	}
	for _, bad := range []string{"http://releases.example.test/f.json", "releases.example.test/f.json", "https://u:p@x.test/f"} {
		update.DefaultFeedURL = bad
		if d := DefaultUpdateSettings(); d.FeedURL != "" {
			t.Errorf("DefaultFeedURL %q: DefaultUpdateSettings = %+v, want no feed", bad, d)
		}
		if _, err := update.DefaultFeed(); err == nil {
			t.Errorf("update.DefaultFeed with %q: no error", bad)
		}
	}
}

func TestUpdateSettings(t *testing.T) {
	if d := DefaultUpdateSettings(); d.FeedURL != "" || !d.AutoCheck {
		t.Errorf("DefaultUpdateSettings = %+v", d)
	}
	// Fields missing from the settings file take their defaults.
	for data, want := range map[string]UpdateSettings{
		`{}`:                                   {AutoCheck: true},
		`{"feed_url":"https://x.test/f.json"}`: {FeedURL: "https://x.test/f.json", AutoCheck: true},
		`{"auto_check":false}`:                 {AutoCheck: false},
		`{"feed_url":"","auto_check":false,"z":1}`: {},
	} {
		var u UpdateSettings
		if err := json.Unmarshal([]byte(data), &u); err != nil || u != want {
			t.Errorf("unmarshal %s = %+v, %v; want %+v", data, u, err, want)
		}
	}
	out, err := json.Marshal(UpdateSettings{FeedURL: "https://x.test/f", AutoCheck: false})
	if err != nil || string(out) != `{"feed_url":"https://x.test/f","auto_check":false}` {
		t.Errorf("marshal = %s, %v", out, err)
	}
	// Without a built-in feed, "" stays "" (updates off) on every way.
	if out, err := json.Marshal(UpdateSettings{AutoCheck: true}); err != nil || string(out) != `{"feed_url":"","auto_check":true}` {
		t.Errorf("marshal without a feed = %s, %v", out, err)
	}
	if n, err := (UpdateSettings{AutoCheck: true}).normalize(); err != nil || n.FeedURL != "" || n.autoChecks() {
		t.Errorf("normalize without a built-in feed = %+v, %v; want updates off", n, err)
	}

	for _, ok := range []string{"", "https://updates.example.com/downex/feed.json", "https://x.test/f?channel=beta"} {
		if _, err := (UpdateSettings{FeedURL: ok}).normalize(); err != nil {
			t.Errorf("normalize(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"http://updates.example.com/feed.json", "file:///feed.json", "feed.json", "https://u:p@x.test/f", "https://x.test/f#a", " https://x.test/f"} {
		_, err := (UpdateSettings{FeedURL: bad}).normalize()
		if err == nil || !strings.HasPrefix(err.Error(), "update_feed_url: ") {
			t.Errorf("normalize(%q) = %v, want an update_feed_url error", bad, err)
		}
	}
	got, problems := UpdateSettings{FeedURL: "http://x.test/f", AutoCheck: true}.repair()
	if got != (UpdateSettings{AutoCheck: true}) || len(problems) != 1 {
		t.Errorf("repair = %+v, %q", got, problems)
	}
	if got, problems := (UpdateSettings{FeedURL: "https://x.test/f"}).repair(); got.FeedURL != "https://x.test/f" || problems != nil {
		t.Errorf("repair of valid settings = %+v, %q", got, problems)
	}

	// Proto round trip (fields 22–23).
	p := &downexv1.Settings{}
	UpdateSettings{FeedURL: "https://x.test/f", AutoCheck: true}.setProto(p)
	if p.GetUpdateFeedUrl() != "https://x.test/f" || !p.GetAutoCheckUpdates() {
		t.Errorf("setProto = %v", p)
	}
	if got := updateSettingsFromProto(p); got != (UpdateSettings{FeedURL: "https://x.test/f", AutoCheck: true}) {
		t.Errorf("updateSettingsFromProto = %+v", got)
	}
	if got := updateSettingsFromProto(nil); got != (UpdateSettings{}) {
		t.Errorf("updateSettingsFromProto(nil) = %+v", got)
	}
}

// updateFeed is an https update feed signed with a test key.
type updateFeed struct {
	srv      *httptest.Server
	pub      string // base64
	priv     ed25519.PrivateKey
	mu       sync.Mutex
	manifest []byte
	sig      []byte
	pkg      []byte
	hits     atomic.Int32 // manifest requests
}

func newUpdateFeed(t *testing.T) *updateFeed {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &updateFeed{pub: base64.StdEncoding.EncodeToString(pub), priv: priv, pkg: []byte("the new Downex Setup")}
	mux := http.NewServeMux()
	mux.HandleFunc("/feed.json", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write(f.manifest)
	})
	mux.HandleFunc("/feed.json.sig", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write(f.sig)
	})
	mux.HandleFunc("/pkg", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write(f.pkg)
	})
	f.srv = httptest.NewUnstartedServer(mux)
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes of the untrusted-certificate case
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	f.publish(t, "0.4.0", f.pkg)
	return f
}

func (f *updateFeed) url() string { return f.srv.URL + "/feed.json" }

// publish signs a manifest of version v whose windows/amd64 and darwin/universal
// packages have the checksum of declared (served: f.pkg).
func (f *updateFeed) publish(t *testing.T, v string, declared []byte) {
	t.Helper()
	h := sha256.Sum256(declared)
	m := update.Manifest{Version: v, Notes: "Release " + v, PublishedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	for _, a := range [][2]string{{"windows", "amd64"}, {"darwin", "universal"}} {
		m.Assets = append(m.Assets, update.Asset{OS: a[0], Arch: a[1], URL: f.srv.URL + "/pkg", SHA256: hex.EncodeToString(h[:]), Size: int64(len(declared))})
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifest, f.sig = data, update.EncodeSignature(ed25519.Sign(f.priv, data))
}

// setUpdateSettings replaces the update settings of m directly (the settings file and
// API conversion are wired in settings.go and convert.go) and tells the subscribers.
func setUpdateSettings(m *Manager, u UpdateSettings) {
	m.mu.Lock()
	m.settings.Update = u
	s := m.settings.Clone()
	m.mu.Unlock()
	m.notify(Notice{Kind: NoticeSettings, Settings: s})
}

// fakeInstaller records what InstallUpdate does with the package.
type fakeInstaller struct {
	m          *Manager
	prepareErr error
	stageErr   error
	commitErr  error
	block      chan struct{} // Prepare waits for it when non-nil
	entered    chan struct{} // Prepare signals here when non-nil

	mu         sync.Mutex
	staged     []string
	stagedFor  []string // the versions Stage was told
	stagedData []byte
	commits    int
	discards   int
	closedAt   bool // the Manager was shut down when Commit ran
}

func (f *fakeInstaller) FileName(v update.Version) string {
	return "Downex-Setup-" + v.String() + ".exe"
}

func (f *fakeInstaller) Prepare(context.Context) error {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	return f.prepareErr
}

func (f *fakeInstaller) Stage(_ context.Context, path string, v update.Version) (update.Staged, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged = append(f.staged, path)
	f.stagedFor = append(f.stagedFor, v.String())
	f.stagedData, _ = os.ReadFile(path)
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	return fakeStaged{f}, nil
}

type fakeStaged struct{ f *fakeInstaller }

func (s fakeStaged) Commit() error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.commits++
	s.f.closedAt = s.f.m.isClosed()
	return s.f.commitErr
}

func (s fakeStaged) Discard() {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.discards++
}

func updateHarness(t *testing.T, feed *updateFeed, cfg UpdatesConfig) *harness {
	t.Helper()
	return updateHarnessWith(t, feed, cfg, harnessOptions{})
}

// updateHarnessWith is updateHarness with the harness options opts (a log to read, say).
func updateHarnessWith(t *testing.T, feed *updateFeed, cfg UpdatesConfig, opts harnessOptions) *harness {
	t.Helper()
	h := newHarness(t, opts)
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = feed.srv.Client()
	}
	if cfg.PublicKey == "" {
		cfg.PublicKey = feed.pub
	}
	if cfg.version == "" {
		cfg.version = "0.3.3"
	}
	if cfg.goos == "" {
		cfg.goos, cfg.goarch = "windows", "amd64"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	// The settings first: the automatic check then starts with them (one first timer).
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.url(), AutoCheck: true})
	stop := StartUpdates(h.m, cfg)
	t.Cleanup(stop)
	return h
}

func wantCode(t *testing.T, what string, err error, code connect.Code) {
	t.Helper()
	if connect.CodeOf(err) != code {
		t.Errorf("%s: %v (code %v), want %v", what, err, connect.CodeOf(err), code)
	}
}

func TestCheckForUpdate(t *testing.T) {
	feed := newUpdateFeed(t)
	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	h := updateHarness(t, feed, UpdatesConfig{now: func() time.Time { return clock }})
	ctx := context.Background()

	res, err := h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	info := res.Msg.GetInfo()
	if !info.GetAvailable() || info.GetCurrentVersion() != "0.3.3" || info.GetLatestVersion() != "0.4.0" ||
		info.GetNotes() != "Release 0.4.0" || !info.GetCheckedAt().AsTime().Equal(clock) ||
		!info.GetPublishedAt().AsTime().Equal(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("info = %v", info)
	}

	feed.publish(t, "0.3.3", feed.pkg)
	res, err = h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	if err != nil || res.Msg.GetInfo().GetAvailable() || res.Msg.GetInfo().GetLatestVersion() != "0.3.3" {
		t.Errorf("same version: %v, %v", res, err)
	}

	// A tampered manifest is refused.
	feed.mu.Lock()
	feed.manifest = []byte(strings.Replace(string(feed.manifest), "0.3.3", "9.9.9", 1))
	feed.mu.Unlock()
	_, err = h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "tampered manifest", err, connect.CodeDataLoss)

	// A feed that is not published yet (404) says so in plain words, not with the HTTP status.
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.srv.URL + "/missing.json", AutoCheck: true})
	_, err = h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "not published", err, connect.CodeNotFound)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Message() != "No update information is published yet" {
		t.Errorf("not published: %v, want the message %q", err, "No update information is published yet")
	}
	_, err = h.dm.InstallUpdate(ctx, connect.NewRequest(&downexv1.InstallUpdateRequest{}))
	if connect.CodeOf(err) == connect.CodeNotFound && (!errors.As(err, &ce) || ce.Message() != "No update information is published yet") {
		t.Errorf("install, not published: %v", err)
	}

	// Updates off.
	setUpdateSettings(h.m, UpdateSettings{AutoCheck: true})
	_, err = h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "no feed", err, connect.CodeFailedPrecondition)
}

func TestCheckForUpdateDisabled(t *testing.T) {
	feed := newUpdateFeed(t)
	ctx := context.Background()

	// No StartUpdates: a default component with the compiled-in key, which a test build
	// lacks: updates are disabled with a clear error, and nothing is fetched.
	h := newHarness(t, harnessOptions{})
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.url(), AutoCheck: true})
	_, err := h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "no key", err, connect.CodeFailedPrecondition)
	if err == nil || !strings.Contains(err.Error(), "no update signing key") {
		t.Errorf("no key: %v", err)
	}
	_, err = h.dm.InstallUpdate(ctx, connect.NewRequest(&downexv1.InstallUpdateRequest{}))
	if err == nil {
		t.Error("InstallUpdate without a key succeeded")
	}
	if feed.hits.Load() != 0 {
		t.Errorf("%d manifest requests without a key", feed.hits.Load())
	}

	// A development build has no release version.
	h2 := updateHarness(t, feed, UpdatesConfig{version: "dev"})
	_, err = h2.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "dev build", err, connect.CodeFailedPrecondition)

	// A feed that is not reachable over trusted TLS.
	h3 := updateHarness(t, feed, UpdatesConfig{HTTPClient: update.NewHTTPClient(nil)})
	_, err = h3.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "untrusted certificate", err, connect.CodeUnavailable)
	if err != nil && strings.Contains(err.Error(), feed.srv.URL) {
		t.Errorf("the error names the URL: %v", err)
	}
}

func TestInstallUpdate(t *testing.T) {
	feed := newUpdateFeed(t)
	ctx := context.Background()
	inst := &fakeInstaller{}
	dataDir := t.TempDir()
	h := updateHarness(t, feed, UpdatesConfig{Installer: inst, DataDir: dataDir})
	inst.m = h.m

	res, err := h.dm.InstallUpdate(ctx, connect.NewRequest(&downexv1.InstallUpdateRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetInfo().GetLatestVersion() != "0.4.0" || !res.Msg.GetInfo().GetAvailable() {
		t.Errorf("info = %v", res.Msg.GetInfo())
	}
	inst.mu.Lock()
	staged, stagedFor, data, commits, closedAt := inst.staged, inst.stagedFor, inst.stagedData, inst.commits, inst.closedAt
	inst.mu.Unlock()
	want := filepath.Join(dataDir, "updates", "Downex-Setup-0.4.0.exe")
	if len(staged) != 1 || staged[0] != want || string(data) != string(feed.pkg) {
		t.Errorf("staged %q (%q), want %s", staged, data, want)
	}
	// The installer is told the manifest's version (an ad-hoc signed Mac build checks it).
	if len(stagedFor) != 1 || stagedFor[0] != "0.4.0" {
		t.Errorf("staged for %q, want 0.4.0", stagedFor)
	}
	if commits != 1 || !closedAt {
		t.Errorf("commits %d, manager shut down at commit: %v", commits, closedAt)
	}
	select {
	case <-h.shutdowns:
	case <-time.After(waitTimeout):
		t.Fatal("the daemon was not asked to exit")
	}
	// The daemon is stopping: no second installation.
	_, err = h.dm.InstallUpdate(ctx, connect.NewRequest(&downexv1.InstallUpdateRequest{}))
	if err == nil {
		t.Error("a second InstallUpdate succeeded")
	}
}

func TestInstallUpdateRefusals(t *testing.T) {
	ctx := context.Background()
	install := func(t *testing.T, h *harness) error {
		t.Helper()
		_, err := h.dm.InstallUpdate(ctx, connect.NewRequest(&downexv1.InstallUpdateRequest{}))
		return err
	}
	// alive checks that a refused installation left the daemon running.
	alive := func(t *testing.T, h *harness, inst *fakeInstaller) {
		t.Helper()
		select {
		case <-h.shutdowns:
			t.Error("the daemon was asked to exit")
		default:
		}
		if h.m.isClosed() {
			t.Error("the manager was shut down")
		}
		if inst != nil {
			inst.mu.Lock()
			defer inst.mu.Unlock()
			if inst.commits != 0 {
				t.Error("the package was committed")
			}
		}
	}

	t.Run("sha256 mismatch", func(t *testing.T) {
		feed := newUpdateFeed(t)
		feed.publish(t, "0.4.0", []byte("something else, same length")[:len(feed.pkg)])
		inst := &fakeInstaller{}
		dataDir := t.TempDir()
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst, DataDir: dataDir})
		wantCode(t, "InstallUpdate", install(t, h), connect.CodeDataLoss)
		alive(t, h, inst)
		inst.mu.Lock()
		staged := len(inst.staged)
		inst.mu.Unlock()
		if staged != 0 {
			t.Error("a package with the wrong checksum was staged")
		}
		if entries, _ := os.ReadDir(filepath.Join(dataDir, "updates")); len(entries) != 0 {
			t.Errorf("left behind: %v", entries)
		}
	})
	t.Run("size mismatch", func(t *testing.T) {
		feed := newUpdateFeed(t)
		feed.publish(t, "0.4.0", append([]byte("x"), feed.pkg...))
		inst := &fakeInstaller{}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst})
		wantCode(t, "InstallUpdate", install(t, h), connect.CodeDataLoss)
		alive(t, h, inst)
	})
	t.Run("up to date", func(t *testing.T) {
		feed := newUpdateFeed(t)
		inst := &fakeInstaller{}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst, version: "0.4.0"})
		wantCode(t, "InstallUpdate", install(t, h), connect.CodeFailedPrecondition)
		alive(t, h, inst)
	})
	t.Run("no package for this system", func(t *testing.T) {
		feed := newUpdateFeed(t)
		inst := &fakeInstaller{}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst, goos: "windows", goarch: "arm64"})
		err := install(t, h)
		wantCode(t, "InstallUpdate", err, connect.CodeFailedPrecondition)
		if err == nil || !strings.Contains(err.Error(), "no package for this system") {
			t.Errorf("err = %v", err)
		}
		alive(t, h, inst)
	})
	t.Run("not updatable", func(t *testing.T) {
		feed := newUpdateFeed(t)
		inst := &fakeInstaller{prepareErr: update.ErrNotUpdatable}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst})
		wantCode(t, "InstallUpdate", install(t, h), connect.CodeFailedPrecondition)
		alive(t, h, inst)
		if feed.hits.Load() != 0 {
			t.Error("the feed was fetched although the installation cannot be updated")
		}
	})
	t.Run("code signature", func(t *testing.T) {
		feed := newUpdateFeed(t)
		dataDir := t.TempDir()
		inst := &fakeInstaller{stageErr: update.ErrCodeSignature}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst, DataDir: dataDir})
		wantCode(t, "InstallUpdate", install(t, h), connect.CodePermissionDenied)
		alive(t, h, inst)
		if entries, _ := os.ReadDir(filepath.Join(dataDir, "updates")); len(entries) != 0 {
			t.Errorf("a refused package was kept: %v", entries)
		}
	})
	t.Run("unsupported system", func(t *testing.T) {
		feed := newUpdateFeed(t)
		h := updateHarness(t, feed, UpdatesConfig{Installer: update.NewInstaller(update.System{GOOS: "linux"}), goos: "linux"})
		wantCode(t, "InstallUpdate", install(t, h), connect.CodeUnimplemented)
		alive(t, h, nil)
	})
	t.Run("one at a time: a second call joins the running install", func(t *testing.T) {
		feed := newUpdateFeed(t)
		inst := &fakeInstaller{prepareErr: errors.New("stop here"), block: make(chan struct{}), entered: make(chan struct{}, 2)}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst})
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { first <- install(t, h) }()
		<-inst.entered
		go func() { second <- install(t, h) }()
		time.Sleep(100 * time.Millisecond) // the second call joins before the first ends
		close(inst.block)
		for i, ch := range []chan error{first, second} {
			if err := <-ch; connect.CodeOf(err) != connect.CodeInternal {
				t.Errorf("InstallUpdate %d: %v", i+1, err)
			}
		}
		select {
		case <-inst.entered:
			t.Error("the second call started an install of its own")
		default:
		}
		alive(t, h, inst)
	})
	t.Run("commit fails", func(t *testing.T) {
		feed := newUpdateFeed(t)
		inst := &fakeInstaller{commitErr: errors.New("cannot start the Setup")}
		h := updateHarness(t, feed, UpdatesConfig{Installer: inst})
		inst.m = h.m
		wantCode(t, "InstallUpdate", install(t, h), connect.CodeInternal)
		// The downloads were paused for the installation: the daemon exits anyway.
		select {
		case <-h.shutdowns:
		case <-time.After(waitTimeout):
			t.Fatal("the daemon was not asked to exit")
		}
	})
}

// fakeTimers is the automatic check's clock and timers.
type fakeTimers struct {
	mu    sync.Mutex
	now   time.Time
	fns   []func()
	armed chan time.Duration
}

func newFakeTimers() *fakeTimers {
	return &fakeTimers{now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.Local), armed: make(chan time.Duration, 16)}
}

func (c *fakeTimers) clock() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeTimers) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeTimers) afterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	c.fns = append(c.fns, f)
	c.mu.Unlock()
	c.armed <- d
	return func() bool { return true }
}

// fire runs the latest timer's function.
func (c *fakeTimers) fire() {
	c.mu.Lock()
	f := c.fns[len(c.fns)-1]
	c.mu.Unlock()
	f()
}

func (c *fakeTimers) wantArmed(t *testing.T, want time.Duration) {
	t.Helper()
	select {
	case d := <-c.armed:
		if d != want {
			t.Fatalf("timer armed for %v, want %v", d, want)
		}
	case <-time.After(waitTimeout):
		t.Fatalf("no timer armed (want %v)", want)
	}
}

func TestUpdateAutoCheck(t *testing.T) {
	feed := newUpdateFeed(t)
	timers := newFakeTimers()
	notified := make(chan *downexv1.UpdateInfo, 4)
	var logs logBuffer
	h := updateHarnessWith(t, feed, UpdatesConfig{
		AutoCheck: true, Notify: func(i *downexv1.UpdateInfo) { notified <- i },
		now: timers.clock, afterFunc: timers.afterFunc,
	}, harnessOptions{log: slog.New(slog.NewJSONHandler(&logs, nil))})
	noNotice := func() {
		t.Helper()
		select {
		case i := <-notified:
			t.Fatalf("unexpected notification %v", i)
		default:
		}
	}

	// First check soon after the start.
	timers.wantArmed(t, updateFirstDelay)
	if feed.hits.Load() != 0 {
		t.Fatal("checked before the first delay")
	}
	timers.advance(updateFirstDelay)
	timers.fire()
	select {
	case i := <-notified:
		if i.GetLatestVersion() != "0.4.0" || !i.GetAvailable() {
			t.Errorf("notified %v", i)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no notification")
	}
	timers.wantArmed(t, time.Hour) // the interval, in timers of at most an hour

	// An early timer (the wall clock says not yet) only re-arms.
	timers.advance(time.Hour)
	timers.fire()
	timers.wantArmed(t, time.Hour)
	if n := feed.hits.Load(); n != 1 {
		t.Errorf("%d checks after an early timer, want 1", n)
	}

	wantNotice := func(version string) {
		t.Helper()
		select {
		case i := <-notified:
			if i.GetLatestVersion() != version || !i.GetAvailable() {
				t.Errorf("notified %v, want %s", i, version)
			}
		case <-time.After(waitTimeout):
			t.Fatalf("no notification for %s", version)
		}
	}

	// An interval later the same version is offered again (the user may have chosen Later).
	timers.advance(updateInterval - time.Hour)
	timers.fire()
	wantNotice("0.4.0")
	timers.wantArmed(t, time.Hour)
	if n := feed.hits.Load(); n != 2 {
		t.Errorf("%d checks, want 2", n)
	}

	// A version that a client saw with CheckForUpdate (Settings → Check now) is offered by
	// the next automatic check all the same; CheckForUpdate itself notifies nothing.
	feed.publish(t, "0.5.0", feed.pkg)
	if _, err := h.dm.CheckForUpdate(context.Background(), connect.NewRequest(&downexv1.CheckForUpdateRequest{})); err != nil {
		t.Fatal(err)
	}
	noNotice()
	timers.advance(updateInterval)
	timers.fire()
	wantNotice("0.5.0")
	timers.wantArmed(t, time.Hour)

	// Up to date: nothing is notified.
	feed.publish(t, "0.3.3", feed.pkg)
	timers.advance(updateInterval)
	timers.fire()
	timers.wantArmed(t, time.Hour)
	noNotice()

	// A feed that is not published yet (404) or cannot be reached is only logged.
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.srv.URL + "/missing.json", AutoCheck: true})
	timers.wantArmed(t, updateFirstDelay) // a new feed: checked soon after
	timers.advance(updateFirstDelay)
	timers.fire()
	timers.wantArmed(t, time.Hour)
	noNotice()
	// at Info, as an expected state until the feed is published, and never as a failure
	if recs := logs.records("automatic update check: no update information is published yet"); len(recs) != 1 ||
		recs[0]["level"] != "INFO" {
		t.Errorf("not-published records = %v", recs)
	}
	if recs := logs.records("automatic update check failed"); len(recs) != 0 {
		t.Errorf("a feed that is not published is logged as a failure: %v", recs)
	}
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.url(), AutoCheck: true})
	timers.wantArmed(t, updateFirstDelay)

	// Automatic checks off: the timer keeps running but nothing is fetched.
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.url(), AutoCheck: false})
	hits := feed.hits.Load()
	timers.advance(updateInterval)
	timers.fire()
	timers.wantArmed(t, time.Hour)
	if feed.hits.Load() != hits {
		t.Error("checked with auto_check_updates off")
	}
	// Turned on again: the next check is soon.
	setUpdateSettings(h.m, UpdateSettings{FeedURL: feed.url(), AutoCheck: true})
	timers.wantArmed(t, updateFirstDelay)
}

// TestUpdateWatchEvent: the window learns each automatic check's result through Watch (its
// pop-up): a stream of every download gets it after each automatic check and a new stream
// in its first batch; streams of some downloads and CheckForUpdate's results get nothing.
func TestUpdateWatchEvent(t *testing.T) {
	feed := newUpdateFeed(t)
	timers := newFakeTimers()
	h := updateHarness(t, feed, UpdatesConfig{AutoCheck: true, now: timers.clock, afterFunc: timers.afterFunc})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// watch streams the update events of a Watch of every download (one value per batch: the
	// batch's update event, or nil for a batch without one).
	watch := func() <-chan *downexv1.UpdateInfo {
		ch := make(chan *downexv1.UpdateInfo, 16)
		go func() {
			_ = h.m.Watch(ctx, nil, func(r *downexv1.WatchResponse) error {
				var info *downexv1.UpdateInfo
				for _, ev := range r.GetEvents() {
					if u := ev.GetUpdate(); u != nil {
						info = u
					}
				}
				ch <- info
				return nil
			})
		}()
		return ch
	}
	next := func(ch <-chan *downexv1.UpdateInfo) *downexv1.UpdateInfo {
		t.Helper()
		select {
		case i := <-ch:
			return i
		case <-time.After(waitTimeout):
			t.Fatal("no batch")
			return nil
		}
	}

	first := watch()
	if i := next(first); i != nil {
		t.Errorf("first batch before any check: update %v", i)
	}
	// CheckForUpdate's result is the caller's only.
	if _, err := h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{})); err != nil {
		t.Fatal(err)
	}
	if h.m.updateEvent() != nil {
		t.Error("CheckForUpdate's result reached the Watch streams")
	}

	timers.wantArmed(t, updateFirstDelay)
	timers.advance(updateFirstDelay)
	timers.fire()
	i := next(first)
	if i == nil || !i.GetAvailable() || i.GetLatestVersion() != "0.4.0" || i.GetNotes() != "Release 0.4.0" ||
		!i.GetCheckedAt().AsTime().Equal(timers.clock()) {
		t.Errorf("after the automatic check: %v", i)
	}
	timers.wantArmed(t, time.Hour)

	// A window opened later (the tray's notification) gets it in its first batch.
	if j := next(watch()); j == nil || j.GetLatestVersion() != "0.4.0" || !j.GetAvailable() {
		t.Errorf("a later stream's first batch: %v", j)
	}

	// The next day's check sends its own result (a new checked_at), also an up-to-date one.
	feed.publish(t, "0.3.3", feed.pkg)
	timers.advance(updateInterval)
	timers.fire()
	i = next(first)
	if i == nil || i.GetAvailable() || i.GetLatestVersion() != "0.3.3" || !i.GetCheckedAt().AsTime().Equal(timers.clock()) {
		t.Errorf("an up-to-date check: %v", i)
	}

	// A Watch of some downloads gets no update events.
	d, err := h.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{Url: "https://example.invalid/a.bin", StartPaused: true}))
	if err != nil {
		t.Fatal(err)
	}
	some := make(chan bool, 16)
	go func() {
		_ = h.m.Watch(ctx, []int64{d.Msg.GetDownload().GetId()}, func(r *downexv1.WatchResponse) error {
			has := false
			for _, ev := range r.GetEvents() {
				has = has || ev.GetUpdate() != nil
			}
			some <- has
			return nil
		})
	}()
	select {
	case has := <-some:
		if has {
			t.Error("a Watch of some downloads got an update event")
		}
	case <-time.After(waitTimeout):
		t.Fatal("no first batch")
	}
}

func TestUpdateComponentEndsWithManager(t *testing.T) {
	feed := newUpdateFeed(t)
	h := updateHarness(t, feed, UpdatesConfig{})
	updateServices.mu.Lock()
	u := updateServices.m[h.m]
	updateServices.mu.Unlock()
	if u == nil {
		t.Fatal("StartUpdates registered nothing")
	}
	if err := h.m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.done:
	case <-time.After(waitTimeout):
		t.Fatal("the update component did not end with the manager")
	}
	updateServices.mu.Lock()
	_, ok := updateServices.m[h.m]
	updateServices.mu.Unlock()
	if ok {
		t.Error("the component is still registered")
	}
	_, err := h.dm.CheckForUpdate(context.Background(), connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
	wantCode(t, "after shutdown", err, connect.CodeUnavailable)
}
