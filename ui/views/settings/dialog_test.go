package settings_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

// The ports of SettingsDialog.test.ts: each subtest is named after its vitest case.

// secret is a SetSecret or ClearSecret call.
type secret struct{ call, kind, id, password string }

// daemon is the test's downexd: statetest's fake with the save mock of SettingsDialog.test.ts
// (new site logins get the ids "new-1", "new-2"…), and the calls the vitest mocks recorded.
type daemon struct {
	*statetest.Daemon
	mu         sync.Mutex
	saves      []*downexv1.Settings
	loads      int
	secrets    []secret
	failSecret error
	nextID     int
	status     *downexv1.StatusResponse
	statusErr  error
	update     *downexv1.UpdateInfo
	checkErr   error // CheckForUpdate's answer when set
	installs   int
}

func newDaemon(initial *downexv1.Settings) *daemon {
	d := &daemon{Daemon: statetest.NewDaemon(), nextID: 1}
	d.SetSettings(proto.CloneOf(initial))
	return d
}

func (d *daemon) GetSettings(ctx context.Context) (*downexv1.GetSettingsResponse, error) {
	d.mu.Lock()
	d.loads++
	d.mu.Unlock()
	return d.Daemon.GetSettings(ctx)
}

func (d *daemon) UpdateSettings(_ context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sent := proto.CloneOf(req.GetSettings())
	d.saves = append(d.saves, sent)
	s := proto.CloneOf(sent)
	for _, l := range s.GetSiteLogins() {
		if l.GetId() == "" {
			l.Id = fmt.Sprintf("new-%d", d.nextID)
			d.nextID++
		}
	}
	d.SetSettings(s)
	return &downexv1.UpdateSettingsResponse{Settings: s}, nil
}

func (d *daemon) SetSecret(_ context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.secrets = append(d.secrets, secret{"set", req.GetKind(), req.GetId(), req.GetPassword()})
	if err := d.failSecret; err != nil {
		d.failSecret = nil
		return nil, err
	}
	return &downexv1.SetSecretResponse{}, nil
}

func (d *daemon) ClearSecret(_ context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.secrets = append(d.secrets, secret{"clear", req.GetKind(), req.GetId(), ""})
	return &downexv1.ClearSecretResponse{}, nil
}

func (d *daemon) Status(ctx context.Context) (*downexv1.StatusResponse, error) {
	d.mu.Lock()
	s, err := d.status, d.statusErr
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if s != nil {
		return s, nil
	}
	return d.Daemon.Status(ctx)
}

func (d *daemon) CheckForUpdate(ctx context.Context) (*downexv1.CheckForUpdateResponse, error) {
	if d.checkErr != nil {
		return nil, d.checkErr
	}
	if d.update == nil {
		return d.Daemon.CheckForUpdate(ctx)
	}
	return &downexv1.CheckForUpdateResponse{Info: d.update}, nil
}

func (d *daemon) InstallUpdate(ctx context.Context) (*downexv1.InstallUpdateResponse, error) {
	d.mu.Lock()
	d.installs++
	d.mu.Unlock()
	if d.update == nil {
		return d.Daemon.InstallUpdate(ctx)
	}
	return &downexv1.InstallUpdateResponse{Info: d.update}, nil
}

func (d *daemon) saved() []*downexv1.Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*downexv1.Settings(nil), d.saves...)
}

func (d *daemon) secretCalls() []secret {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]secret(nil), d.secrets...)
}

// loaded is the vitest file's `loaded`.
func loaded() *downexv1.Settings {
	s := state.DefaultSettings()
	s.MaxConcurrentDownloads = 4
	s.GlobalSpeedLimit = 2 * 1000 * 1000
	s.DefaultDir = "/home/you/Downloads"
	s.Categories = []*downexv1.Category{
		{Name: "Video", Extensions: []string{"mp4", "mkv"}},
		{Name: "Books", Extensions: []string{"epub"}, Dir: "/data/books"},
	}
	s.ConfirmBrowserDownloads = false
	s.LaunchAtLogin = true
	return s
}

// m6 is the vitest file's `m6`.
func m6() *downexv1.Settings {
	s := loaded()
	s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy.lan:8080", Bypass: "<local>", Username: "me", HasPassword: true}
	s.HostRules = []*downexv1.HostRule{{Pattern: "*.example.com", MaxConnections: 4, SpeedLimit: 1024 * 1024, MaxRequestBytes: 10 * 1024 * 1024}}
	s.SiteLogins = []*downexv1.SiteLogin{
		{Id: "a1", UrlPattern: "https://files.example.com/*", Username: "anna", HasPassword: true},
		{Id: "b2", UrlPattern: "*.example.org", Username: "bob"},
	}
	s.ClipboardMonitor = true
	s.MarkDownloadedFiles = false
	s.AvScanEnabled = true
	s.AvScanCommand = "/usr/bin/clamscan"
	s.AvScanArgs = "--no-summary [File]"
	s.UpdateFeedUrl = "https://updates.example.com/downex.json"
	s.AutoCheckUpdates = false
	return s
}

// rig is a Settings dialog over the test daemon.
type rig struct {
	t      *testing.T
	h      *viewstest.Harness
	dm     *daemon
	d      *settings.Dialog
	saved  []*downexv1.Settings
	closes int
	fp     *viewstest.FakePlatform
}

func setup(t *testing.T, initial *downexv1.Settings, req views.SettingsRequest, mutate ...func(*rig)) *rig {
	t.Helper()
	r := &rig{t: t, dm: newDaemon(initial)}
	r.h = viewstest.New(t, viewstest.Options{Daemon: r.dm.Daemon})
	r.h.App.Daemon = r.dm
	r.fp = r.h.Platform.(*viewstest.FakePlatform)
	r.fp.Picked = "/picked"
	for _, m := range mutate {
		m(r)
	}
	r.d = open(r.h, req, settings.Options{
		OnSaved: func(s *downexv1.Settings) { r.saved = append(r.saved, s) },
		OnClose: func() { r.closes++ },
	})
	return r
}

func (r *rig) root() *kit.Element { return r.d.Card() }

func (r *rig) tab(page string) *kit.Element { return tab(r.t, r.d, page) }

func (r *rig) click(page string) {
	r.h.Click(r.tab(page))
}

func (r *rig) save() {
	r.t.Helper()
	press(r.h, buttonNamed(r.t, r.root(), "Save"))
}

func (r *rig) press(name string) { r.t.Helper(); press(r.h, buttonNamed(r.t, r.root(), name)) }

func (r *rig) input(name string) *widgets.Input { r.t.Helper(); return inputNamed(r.t, r.root(), name) }

func (r *rig) typeIn(name, s string) { r.t.Helper(); typeInto(r.h, r.input(name), s) }

func (r *rig) toggle(sub string) {
	r.t.Helper()
	sw := switchNamed(r.t, r.root(), sub)
	if err := sw.PerformAction(kit.ActionRequest{Action: kit.ActionToggle}); err != nil {
		r.t.Fatal(err)
	}
	r.h.Settle()
}

func (r *rig) sw(sub string) *widgets.Switch { r.t.Helper(); return switchNamed(r.t, r.root(), sub) }

func (r *rig) focused() *kit.Element { return r.h.Window.Focused() }

func (r *rig) selected(page string) bool {
	return r.tab(page).Semantics.States&kit.NodeSelected != 0
}

// lastSave is what the last save sent.
func (r *rig) lastSave() *downexv1.Settings {
	r.t.Helper()
	s := r.dm.saved()
	if len(s) == 0 {
		r.t.Fatal("not saved")
	}
	return s[len(s)-1]
}

func (r *rig) noSave() {
	r.t.Helper()
	if n := len(r.dm.saved()); n != 0 {
		r.t.Fatalf("saved %d times", n)
	}
}

// selectNamed is the visible Select named name.
func (r *rig) selectNamed(name string) *widgets.Select {
	r.t.Helper()
	es := findAll(r.root(), func(e *kit.Element) bool {
		_, ok := e.Widget().(*widgets.Select)
		return ok && e.Semantics.Name == name
	})
	if len(es) == 0 {
		// a Select names its node itself (FillSemantics): find it by its label
		es = findAll(r.root(), func(e *kit.Element) bool {
			s, ok := e.Widget().(*widgets.Select)
			return ok && selectName(s) == name
		})
	}
	if len(es) == 0 {
		r.t.Fatalf("no select %q", name)
	}
	return es[0].Widget().(*widgets.Select)
}

func selectName(s *widgets.Select) string {
	var n kit.SemanticNode
	s.FillSemantics(&n)
	if n.Name != "" {
		return n.Name
	}
	return s.Semantics.Name
}

// choose picks an option of a Select as the user does.
func (r *rig) choose(name, value string) {
	r.t.Helper()
	if err := r.selectNamed(name).PerformAction(kit.ActionRequest{Action: kit.ActionSetValue, Text: value}); err != nil {
		r.t.Fatal(err)
	}
	r.h.Settle()
}

// radio picks a segment named label in the group named group.
func (r *rig) radio(group, label string) {
	r.t.Helper()
	g := find(r.root(), func(e *kit.Element) bool {
		return e.Semantics.Role == kit.RoleRadioGroup && e.Semantics.Name == group && !hiddenUnder(r.root(), e)
	})
	if g == nil {
		r.t.Fatalf("no group %q", group)
	}
	o := find(g, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleRadioButton && nameOf(e) == label })
	if o == nil {
		r.t.Fatalf("no %q in %q", label, group)
	}
	if err := o.Widget().(kit.ActionHandler).PerformAction(kit.ActionRequest{Action: kit.ActionSelect}); err != nil {
		r.t.Fatal(err)
	}
	r.h.Settle()
}

// nameOf is an element's accessible name (what its FillSemantics gives, else its own).
func nameOf(e *kit.Element) string {
	var n kit.SemanticNode
	if f, ok := e.Widget().(kit.SemanticFiller); ok {
		f.FillSemantics(&n)
	}
	if n.Name != "" {
		return n.Name
	}
	if e.Semantics.Name != "" {
		return e.Semantics.Name
	}
	// named from its contents (a radio button's label)
	if l := find(e, func(x *kit.Element) bool { _, ok := x.Widget().(*kit.Label); return ok }); l != nil {
		return l.Widget().(*kit.Label).Text()
	}
	return ""
}

func radios(root *kit.Element, group string) []*kit.Element {
	g := find(root, func(e *kit.Element) bool {
		return e.Semantics.Role == kit.RoleRadioGroup && e.Semantics.Name == group && !hiddenUnder(root, e)
	})
	if g == nil {
		return nil
	}
	return findAll(g, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleRadioButton })
}

func checked(root *kit.Element, e *kit.Element) bool {
	var n kit.SemanticNode
	if f, ok := e.Widget().(kit.SemanticFiller); ok {
		f.FillSemantics(&n)
	}
	return n.Toggle == kit.ToggleOn || n.States&kit.NodeSelected != 0 || e.Semantics.States&kit.NodeSelected != 0
}

func equal(a, b proto.Message) bool { return proto.Equal(a, b) }

func clone(s *downexv1.Settings, edit func(*downexv1.Settings)) *downexv1.Settings {
	c := proto.CloneOf(s)
	edit(c)
	return c
}

func TestSettingsDialog(t *testing.T) {
	t.Run("writes every setting back, the ones it does not show included", func(t *testing.T) {
		newer := loaded()
		// a field of a newer downexd: the bytes this build does not know
		newer.ProtoReflect().SetUnknown([]byte{0xb8, 0x3e, 7}) // field 999, varint 7
		r := setup(t, newer, views.SettingsRequest{})
		if !r.sw("sign in").On {
			t.Error("Start at sign-in is off")
		}
		r.click("downloads")
		if v := r.input("Default folder").Text(); v != "/home/you/Downloads" {
			t.Errorf("default folder %q", v)
		}
		if !r.sw("category's folder").On {
			t.Error("category folders off")
		}
		// Asking before browser downloads moved to Integration.
		r.click("integration")
		if r.sw("Ask before saving downloads from the browser").On {
			t.Error("asks before browser downloads")
		}
		r.save()
		if r.closes != 1 {
			t.Fatalf("closed %d", r.closes)
		}
		if got := r.lastSave(); !equal(got, newer) || string(got.ProtoReflect().GetUnknown()) != string(newer.ProtoReflect().GetUnknown()) {
			t.Errorf("saved %v, want %v", got, newer)
		}
		if len(r.saved) != 1 || !equal(r.saved[0], newer) {
			t.Errorf("onsaved %v", r.saved)
		}
	})

	t.Run("edits categories and switches", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{Page: validate.PageDownloads})
		names := findAll(r.root(), func(e *kit.Element) bool {
			in, ok := e.Widget().(*widgets.Input)
			return ok && e.Semantics.Name == "Name" && in != nil
		})
		if len(names) != 2 {
			t.Fatalf("%d category rows", len(names))
		}
		typeInto(r.h, names[0].Widget().(*widgets.Input), "Movies")
		exts := inputsNamed(r.root(), "Extensions")
		typeInto(r.h, exts[0].Widget().(*widgets.Input), "MP4, .webm")
		// remove Books, add Comics with a folder from the picker
		r.press("Remove Books")
		r.press("Add category")
		names = findAll(r.root(), func(e *kit.Element) bool {
			_, ok := e.Widget().(*widgets.Input)
			return ok && e.Semantics.Name == "Name"
		})
		added := names[1].Widget().(*widgets.Input)
		if r.focused() != added.Base() {
			t.Errorf("focus on %v, not the new name", r.focused())
		}
		typeInto(r.h, added, "Comics")
		exts = inputsNamed(r.root(), "Extensions")
		typeInto(r.h, exts[1].Widget().(*widgets.Input), "cbz cbr")
		r.press("Choose the folder of Comics")
		folders := inputsNamed(r.root(), "Folder")
		if v := folders[1].Widget().(*widgets.Input).Text(); v != "/picked" {
			t.Errorf("folder %q", v)
		}
		if c := r.fp.Calls; len(c) == 0 || c[len(c)-1] != "PickFolder /home/you/Downloads/Comics" {
			t.Errorf("picker calls %q", c)
		}
		r.toggle("category's folder")
		r.click("general")
		r.toggle("notification area")
		r.click("integration")
		r.toggle("Ask before saving")
		r.save()
		want := clone(loaded(), func(s *downexv1.Settings) {
			s.Categories = []*downexv1.Category{{Name: "Movies", Extensions: []string{"mp4", "webm"}}, {Name: "Comics", Extensions: []string{"cbz", "cbr"}, Dir: "/picked"}}
			s.UseCategoryFolders = false
			s.ShowTrayIcon = false
			s.ConfirmBrowserDownloads = true
		})
		if got := r.lastSave(); !equal(got, want) {
			t.Errorf("saved\n%v\nwant\n%v", got, want)
		}
	})

	t.Run("shows and saves the progress window switches", func(t *testing.T) {
		r := setup(t, clone(loaded(), func(s *downexv1.Settings) { s.ShowProgressWindow, s.ShowCompleteDialog = true, false }), views.SettingsRequest{})
		if !r.sw("progress window when I start").On || r.sw("download complete dialog").On {
			t.Error("switches do not show the settings")
		}
		if !hasText(r.root(), "Progress windows") {
			t.Error("no Progress windows heading")
		}
		r.toggle("progress window when I start")
		r.toggle("download complete dialog")
		r.save()
		want := clone(loaded(), func(s *downexv1.Settings) { s.ShowProgressWindow, s.ShowCompleteDialog = false, true })
		if !equal(r.lastSave(), want) {
			t.Errorf("saved %v", r.lastSave())
		}
	})

	t.Run("shows what is wrong with a category and does not save", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{Page: validate.PageDownloads})
		names := inputsNamed(r.root(), "Name")
		books := names[1].Widget().(*widgets.Input)
		typeInto(r.h, books, "video")
		exts := inputsNamed(r.root(), "Extensions")
		typeInto(r.h, exts[1].Widget().(*widgets.Input), "ok no/pe")
		r.save()
		if !books.InputOptions().Invalid {
			t.Error("the name is not invalid")
		}
		if !hasText(r.root(), "Another category has this name") || !hasText(r.root(), `Not an extension: "no/pe"`) {
			t.Errorf("errors not shown: %q", texts(r.root()))
		}
		if r.focused() != books.Base() {
			t.Errorf("focus %v", r.focused())
		}
		r.noSave()
	})
}

func TestSettingsDialogM6(t *testing.T) {
	t.Run("writes every M6 setting back unchanged", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		for _, p := range []string{"downloads", "network", "integration", "video", "appearance", "updates", "general"} {
			r.click(p)
		}
		r.save()
		if !equal(r.lastSave(), m6()) {
			t.Errorf("saved\n%v\nwant\n%v", r.lastSave(), m6())
		}
		if len(r.dm.secretCalls()) != 0 {
			t.Errorf("secrets %v", r.dm.secretCalls())
		}
	})

	t.Run("edits the proxy, and stores its password only through setSecret after saving", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		r.click("network")
		if !r.selected("network") {
			t.Fatal("Network not selected")
		}
		pw := r.input("Proxy password")
		// A stored password is never shown: the field is empty and says so.
		if o := pw.InputOptions(); !o.Password || pw.Text() != "" || o.Placeholder != "Saved (type to replace it)" {
			t.Errorf("password field %+v %q", o, pw.Text())
		}
		r.typeIn("HTTP proxy", "proxy.lan")
		r.typeIn("SOCKS proxy", "socks5://127.0.0.1:1080")
		typeInto(r.h, pw, "s3cret")
		r.click("general")
		r.save()
		// The error is on Network: the dialog shows it there.
		if !r.selected("network") {
			t.Fatal("the error's page is not shown")
		}
		if !hasText(r.root(), "Enter host:port") {
			t.Errorf("no host:port error: %q", texts(r.root()))
		}
		if r.focused() != r.input("HTTP proxy").Base() {
			t.Errorf("focus %v", r.focused())
		}
		r.noSave()
		r.typeIn("HTTP proxy", "proxy.lan:3128")
		r.save()
		if r.closes != 1 {
			t.Fatal("not closed")
		}
		sent := r.lastSave()
		want := clone(m6(), func(s *downexv1.Settings) {
			s.Proxy.HttpProxy, s.Proxy.SocksProxy = "proxy.lan:3128", "socks5://127.0.0.1:1080"
		})
		if !equal(sent.GetProxy(), want.GetProxy()) {
			t.Errorf("proxy %v", sent.GetProxy())
		}
		if strings.Contains(sent.String(), "s3cret") {
			t.Error("the password went with the settings")
		}
		if c := r.dm.secretCalls(); len(c) != 1 || c[0] != (secret{"set", "proxy", "", "s3cret"}) {
			t.Errorf("secrets %v", c)
		}
		if len(r.saved) != 1 {
			t.Error("onsaved not called")
		}
	})

	t.Run("adds, edits and removes site logins; new passwords go to the ids downexd gives", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		r.click("network")
		table := find(r.root(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleTable && e.Semantics.Name == "Site logins"
		})
		if table == nil {
			t.Fatal("no Site logins table")
		}
		if !hasText(table, "Saved") || !hasText(table, "None") {
			t.Error("password states not shown")
		}
		r.press("Add site login")
		if r.focused() != r.input("Address").Base() {
			t.Errorf("focus %v", r.focused())
		}
		r.typeIn("Address", "https://private.example.net/*")
		r.typeIn("User name", "carol")
		r.typeIn("Password", "pw-carol")
		// Enter applies the login, not the whole dialog.
		r.h.Window.Focus(r.input("Password"))
		r.h.Key(kit.KeyEnter, 0)
		r.noSave()
		table = find(r.root(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleTable && e.Semantics.Name == "Site logins"
		})
		if !hasText(table, "New password (saved with the settings)") {
			t.Errorf("new login not listed: %q", texts(table))
		}
		// bob gets a password; anna is removed (with her stored password).
		r.press("Edit the login for *.example.org")
		r.typeIn("Password", "pw-bob")
		r.press("Apply")
		r.press("Remove the login for https://files.example.com/*")
		r.save()
		sent := r.lastSave()
		want := []*downexv1.SiteLogin{{Id: "b2", UrlPattern: "*.example.org", Username: "bob"}, {UrlPattern: "https://private.example.net/*", Username: "carol"}}
		if len(sent.GetSiteLogins()) != 2 || !equal(sent.GetSiteLogins()[0], want[0]) || !equal(sent.GetSiteLogins()[1], want[1]) {
			t.Errorf("logins %v", sent.GetSiteLogins())
		}
		c := r.dm.secretCalls()
		has := func(s secret) bool {
			for _, x := range c {
				if x == s {
					return true
				}
			}
			return false
		}
		if !has(secret{"set", "site_login", "b2", "pw-bob"}) || !has(secret{"set", "site_login", "new-1", "pw-carol"}) || !has(secret{"clear", "site_login", "a1", ""}) {
			t.Errorf("secrets %v", c)
		}
		// The stored state (which passwords exist) is read again after the secrets changed.
		if r.dm.loads != 3 { // the dialog's read, SaveSettings' read, the read after the secrets
			t.Errorf("settings read %d times", r.dm.loads)
		}
	})

	t.Run("keeps the dialog open when a password cannot be stored", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		r.dm.failSecret = connect.NewError(connect.CodeInternal, errors.New("the credential store is locked"))
		r.click("network")
		r.typeIn("Proxy password", "x")
		r.save()
		alerts := findAll(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleAlert })
		var msg string
		for _, a := range alerts {
			msg += a.Semantics.Name
		}
		if !strings.HasPrefix(msg, "Settings saved, but the proxy password could not be stored") || !strings.Contains(msg, "the credential store is locked") {
			t.Errorf("alert %q", msg)
		}
		if r.closes != 0 {
			t.Error("closed")
		}
		if v := r.input("Proxy password").Text(); v != "" {
			t.Errorf("password field holds %q", v)
		}
	})

	t.Run("opens the editor of a new site login for a server that asked (auth_required)", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{AddLogin: "https://private.example.net/*"})
		if v := r.input("Address").Text(); v != "https://private.example.net/*" {
			t.Errorf("address %q", v)
		}
		if !r.selected("network") {
			t.Error("not on Network")
		}
		if r.focused() != r.input("User name").Base() {
			t.Errorf("focus %v", r.focused())
		}
	})

	t.Run("saves the login still open in its editor with the settings (Save instead of Add login)", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{AddLogin: "https://private.example.net/*"})
		r.typeIn("User name", "carol")
		r.typeIn("Password", "pw-carol")
		r.save()
		if r.closes != 1 {
			t.Fatal("not closed")
		}
		want := append(proto.CloneOf(m6()).GetSiteLogins(), &downexv1.SiteLogin{UrlPattern: "https://private.example.net/*", Username: "carol"})
		got := r.lastSave().GetSiteLogins()
		if len(got) != len(want) {
			t.Fatalf("logins %v", got)
		}
		for i := range want {
			if !equal(got[i], want[i]) {
				t.Errorf("login %d %v", i, got[i])
			}
		}
		if c := r.dm.secretCalls(); len(c) != 1 || c[0] != (secret{"set", "site_login", "new-1", "pw-carol"}) {
			t.Errorf("secrets %v", c)
		}
	})

	t.Run("does not save while the login editor holds an incomplete login", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{AddLogin: "https://private.example.net/*"})
		r.typeIn("Password", "pw")
		r.click("general")
		r.save()
		if !r.selected("network") {
			t.Fatal("not back on Network")
		}
		if !hasText(r.root(), "Enter the user name") {
			t.Error("no user name error")
		}
		if r.focused() != r.input("User name").Base() {
			t.Errorf("focus %v", r.focused())
		}
		if !hasText(r.root(), "Add or cancel the site login you are editing (Network), then save.") {
			t.Error("no footer alert")
		}
		if n := r.tab("network").Semantics.Name; !strings.Contains(n, "needs attention") {
			t.Errorf("tab %q", n)
		}
		r.noSave()
		if r.closes != 0 {
			t.Fatal("closed")
		}
		// Cancelling the editor lets the settings be saved without it.
		ed := find(r.root(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleGroup && e.Semantics.Name == "New site login"
		})
		press(r.h, buttonNamed(t, ed, "Cancel"))
		r.save()
		if !equal(r.lastSave(), m6()) {
			t.Errorf("saved %v", r.lastSave())
		}
	})

	t.Run("keeps the login editor across pages, and opens the one for auth_required only once", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{AddLogin: "https://private.example.net/*"})
		r.typeIn("User name", "dave")
		r.typeIn("Password", "pw-dave")
		r.click("general")
		r.click("network")
		if r.input("Address").Text() != "https://private.example.net/*" || r.input("User name").Text() != "dave" || r.input("Password").Text() != "pw-dave" {
			t.Error("the editor lost its values")
		}
		// Once it is closed, coming back to the page does not open it again.
		ed := find(r.root(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleGroup && e.Semantics.Name == "New site login"
		})
		press(r.h, buttonNamed(t, ed, "Cancel"))
		r.click("general")
		r.click("network")
		if find(r.root(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleGroup && e.Semantics.Name == "New site login"
		}) != nil {
			t.Error("the editor opened again")
		}
		// An editor opened and left untouched is not a change.
		r.press("Add site login")
		r.save()
		if !equal(r.lastSave(), m6()) {
			t.Errorf("saved %v", r.lastSave())
		}
	})

	t.Run("edits host rules", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		r.click("network")
		r.press("Add host rule")
		host := r.input("Host of rule 2")
		if r.focused() != host.Base() {
			t.Errorf("focus %v", r.focused())
		}
		typeInto(r.h, host, "Mirror.Example.net")
		r.typeIn("Connections of rule 2", "2")
		r.typeIn("Speed limit of rule 2", "300")
		r.choose("Speed limit unit of rule 2", "KB/s")
		// The loaded 10 MiB shows as 10.486 MB and stays exact, as does rule 1's 1 MiB/s limit.
		if v := r.input("Max request of rule 1 (MB)").Text(); v != "10.486" {
			t.Errorf("max request %q", v)
		}
		if v := r.input("Speed limit of rule 1").Text(); v != "1.05" {
			t.Errorf("speed %q", v)
		}
		req := r.input("Max request of rule 2 (MB)")
		if req.InputOptions().Placeholder != "No cap" {
			t.Errorf("placeholder %q", req.InputOptions().Placeholder)
		}
		typeInto(r.h, req, "2048")
		r.save()
		if !req.InputOptions().Invalid || !hasText(r.root(), "Enter 1.05 to 1073 MB, or leave empty for no cap") {
			t.Error("no request error")
		}
		if !strings.Contains(r.tab("network").Semantics.Name, "needs attention") {
			t.Error("Network not marked")
		}
		r.noSave()
		typeInto(r.h, req, "1.5")
		r.save()
		got := r.lastSave().GetHostRules()
		want := []*downexv1.HostRule{
			{Pattern: "*.example.com", MaxConnections: 4, SpeedLimit: 1024 * 1024, MaxRequestBytes: 10 * 1024 * 1024},
			{Pattern: "mirror.example.net", MaxConnections: 2, SpeedLimit: 300 * 1000, MaxRequestBytes: 1500 * 1000},
		}
		if len(got) != 2 || !equal(got[0], want[0]) || !equal(got[1], want[1]) {
			t.Errorf("rules %v", got)
		}
	})

	t.Run("sets up the clipboard, file marking and the scanner (Integration)", func(t *testing.T) {
		r := setup(t, clone(m6(), func(s *downexv1.Settings) { s.AvScanEnabled, s.AvScanCommand, s.ClipboardMonitor = false, "", false }), views.SettingsRequest{})
		r.fp.Picked = "/opt/scanner/scan"
		r.click("integration")
		r.toggle("links that I copy")
		r.toggle("coming from the internet")
		r.toggle("virus scanner")
		r.save()
		if !hasText(r.root(), "Choose the scanner program") {
			t.Error("no scanner error")
		}
		r.noSave()
		r.press("Browse…")
		if v := r.input("Scanner program").Text(); v != "/opt/scanner/scan" {
			t.Errorf("scanner %q", v)
		}
		if c := r.fp.Calls; c[len(c)-1] != "PickFile Choose the virus scanner" { // title, then the empty initialDir
			t.Errorf("picker %q", c[len(c)-1])
		}
		r.save()
		s := r.lastSave()
		if !s.GetClipboardMonitor() || !s.GetMarkDownloadedFiles() || !s.GetAvScanEnabled() || s.GetAvScanCommand() != "/opt/scanner/scan" || s.GetAvScanArgs() != "--no-summary [File]" {
			t.Errorf("saved %v", s)
		}
	})

	t.Run("checks for updates with the saved address and installs after asking", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{}, func(r *rig) {
			r.dm.update = &downexv1.UpdateInfo{CurrentVersion: "0.3.3", LatestVersion: "0.4.0", Available: true, Notes: "Queues", CheckedAt: formattest.Millis(1)}
		})
		r.click("updates")
		// The address is under Advanced, closed with a summary of it.
		r.press("Advanced Update address: updates.example.com")
		url := r.input("Update address")
		if url.Text() != "https://updates.example.com/downex.json" {
			t.Errorf("address %q", url.Text())
		}
		// An edited address must be saved before checking.
		typeInto(r.h, url, "https://other.example.com/feed.json")
		if b := buttonNamed(t, r.root(), "Check now"); !b.Disabled {
			t.Error("Check now enabled")
		}
		if !hasText(r.root(), "Save the settings first") {
			t.Error("no save-first hint")
		}
		typeInto(r.h, url, "https://updates.example.com/downex.json")
		r.press("Check now")
		if !hasText(r.root(), "is available (you have") || !hasText(r.root(), "Queues") {
			t.Errorf("no update shown: %q", texts(r.root()))
		}
		r.press("Install update…")
		if r.dm.installs != 0 {
			t.Error("installed without asking")
		}
		if b := buttonNamed(t, r.root(), "Install and restart"); r.focused() != b {
			t.Errorf("focus %v", r.focused())
		}
		r.press("Install and restart")
		if r.dm.installs != 1 {
			t.Error("not installed")
		}
		if !hasText(r.root(), "is installing version") {
			t.Error("no installing note")
		}
	})

	t.Run("says when no update information is published yet", func(t *testing.T) {
		const unpublished = "No update information is published yet"
		r := setup(t, m6(), views.SettingsRequest{}, func(r *rig) {
			r.dm.checkErr = statetest.Err(connect.CodeNotFound, unpublished)
		})
		r.click("updates")
		r.press("Check now")
		if !hasText(r.root(), unpublished) || hasText(r.root(), "HTTP status") {
			t.Fatalf("texts %q", texts(r.root()))
		}
		// A fact, not a failure: an info note without the alert role.
		var note *views.Note
		walk(r.root(), func(e *kit.Element) bool {
			if n, ok := e.Widget().(*views.Note); ok && n.Text != nil && n.Text.Text() == unpublished {
				note = n
			}
			return true
		})
		if note == nil || note.Tone != views.NoteInfo || note.Semantics.Role == kit.RoleAlert {
			t.Errorf("note %+v", note)
		}
		// Another failure stays one.
		r.dm.checkErr = statetest.Err(connect.CodeUnavailable, "update manifest: HTTP status 503")
		r.press("Check now")
		if hasText(r.root(), unpublished) || !hasText(r.root(), "HTTP status 503") {
			t.Errorf("texts %q", texts(r.root()))
		}
	})

	t.Run("accepts only an https update address", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		r.click("updates")
		r.press("Advanced Update address: updates.example.com")
		r.typeIn("Update address", "http://updates.example.com/feed.json")
		r.click("general")
		r.save()
		if !r.selected("updates") {
			t.Fatal("not on Updates")
		}
		adv := buttonNamed(t, r.root(), "Advanced")
		// Advanced opens by itself to show the address that is wrong, and the keyboard is on it.
		if adv.Semantics.Expand != kit.ExpandOpen {
			t.Error("Advanced closed")
		}
		if r.focused() != r.input("Update address").Base() {
			t.Errorf("focus %v", r.focused())
		}
		if !hasText(r.root(), "must start with https://") {
			t.Error("no https error")
		}
		if !strings.Contains(r.tab("updates").Semantics.Name, "needs attention") {
			t.Error("Updates not marked")
		}
		r.noSave()
	})

	t.Run("moves between pages with the arrow keys", func(t *testing.T) {
		r := setup(t, m6(), views.SettingsRequest{})
		r.h.Window.Focus(r.tab("general").Widget())
		r.h.Key(kit.KeyArrowRight, 0)
		if r.focused() != r.tab("downloads") {
			t.Errorf("focus %v", r.focused())
		}
		r.h.Key(kit.KeyEnd, 0)
		if r.focused() != r.tab("about") {
			t.Errorf("focus %v", r.focused())
		}
		r.h.Key(kit.KeyArrowRight, 0)
		if r.focused() != r.tab("general") {
			t.Errorf("focus %v", r.focused())
		}
	})
}

// toolsOf is the vitest toolsOf: a daemon whose tools are status (nil: unimplemented).
func missingTools() *downexv1.ToolsStatus {
	return statetest.Tools(func(s *downexv1.ToolsStatus) {
		s.Ready = false
		for _, t := range s.Tools {
			t.State, t.Version, t.Path, t.Verified, t.CheckedAt, t.LatestVersion = downexv1.ToolState_TOOL_STATE_MISSING, "", "", "", nil, ""
		}
	})
}

func TestSettingsDialogVideoSites(t *testing.T) {
	openVideo := func(t *testing.T, s *downexv1.Settings, tools *downexv1.ToolsStatus, mutate ...func(*rig)) *rig {
		return setup(t, s, views.SettingsRequest{Page: validate.PageVideo}, append([]func(*rig){func(r *rig) { r.dm.SetTools(tools) }}, mutate...)...)
	}

	t.Run("shows the switches, the channel and each tool, and saves them", func(t *testing.T) {
		tools := statetest.Tools(func(s *downexv1.ToolsStatus) {
			s.Tools[1].Managed, s.Tools[1].Path, s.Tools[1].Verified = false, "/usr/bin/deno", ""
		})
		r := openVideo(t, clone(m6(), func(s *downexv1.Settings) { s.SiteMedia, s.ToolsAutoUpdate, s.ToolsChannel = true, true, "nightly" }), tools)
		if !r.selected("video") {
			t.Fatal("not on Video sites")
		}
		if !r.sw("Download videos from web pages with yt-dlp").On || !r.sw("Update automatically").On {
			t.Error("switches off")
		}
		ch := r.selectNamed("yt-dlp releases")
		var labels []string
		for _, o := range ch.Options {
			labels = append(labels, o.Label)
		}
		if strings.Join(labels, "|") != "Nightly (recommended by yt-dlp)|Stable" {
			t.Errorf("channels %q", labels)
		}
		items := findAll(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleListItem && hasText(e, "Last check") })
		if len(items) != 2 {
			t.Fatalf("%d tools", len(items))
		}
		for _, want := range []string{"yt-dlp", "Ready", "version 2026.10.10.233141", "Downex copy", "Signature (OpenPGP) and checksum checked"} {
			if !hasText(items[0], want) {
				t.Errorf("yt-dlp lacks %q", want)
			}
		}
		for _, want := range []string{"Deno", "Found on PATH: /usr/bin/deno", "Your own copy: Downex does not check it"} {
			if !hasText(items[1], want) {
				t.Errorf("Deno lacks %q", want)
			}
		}
		if !hasButton(r.root(), "Check for updates") {
			t.Error("no Check for updates")
		}
		if !hasText(r.root(), "GPLv3+) is downloaded from github.com/yt-dlp; Deno (MIT) from github.com/denoland/deno") || !hasText(r.root(), "terms allow downloading its videos is your responsibility") {
			t.Error("licence texts missing")
		}
		r.choose("yt-dlp releases", "stable")
		r.toggle("Update automatically")
		r.save()
		want := clone(m6(), func(s *downexv1.Settings) { s.SiteMedia, s.ToolsAutoUpdate, s.ToolsChannel = true, false, "stable" })
		if !equal(r.lastSave(), want) {
			t.Errorf("saved %v", r.lastSave())
		}
	})

	t.Run("installs the tools, showing the progress of the tools events", func(t *testing.T) {
		r := openVideo(t, clone(m6(), func(s *downexv1.Settings) { s.SiteMedia = true }), missingTools())
		if n := len(findAll(r.root(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleListItem && hasText(e, "Not installed")
		})); n != 2 {
			t.Errorf("%d not installed", n)
		}
		// The harness runs App.Go on the caller, so the install call blocks the press: what the
		// window shows while downexd installs is checked inside the call (vitest's pending
		// promise), then the call returns the installed tools (finish(makeTools())).
		inCall := false
		r.dm.Before = func(name string, args ...any) error {
			if name != "installTools" {
				return nil
			}
			inCall = true
			r.h.Settle()
			if b := buttonNamed(t, r.root(), "Installing…"); !b.Widget().(*widgets.Button).Disabled {
				t.Error("Installing… enabled")
			}
			progress := missingTools()
			for _, ti := range progress.Tools {
				ti.State, ti.BytesDone, ti.BytesTotal = downexv1.ToolState_TOOL_STATE_INSTALLING, 30, 100
			}
			r.h.Main.Tools.Apply(state.Batch{{Kind: &downexv1.Event_Tools{Tools: progress}}})
			r.h.Settle()
			if !hasText(r.root(), "Downloading yt-dlp and Deno: 30%") {
				t.Errorf("no progress text: %q", texts(r.root()))
			}
			bar := find(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleProgressBar })
			if bar == nil {
				t.Fatal("no progress bar")
			}
			var n kit.SemanticNode
			bar.Widget().(kit.SemanticFiller).FillSemantics(&n) // role progressbar, aria-valuenow
			if n.Name != "Downloading yt-dlp and Deno" || n.Range.Value != 30 {
				t.Errorf("bar %q %+v", n.Name, n.Range)
			}
			return nil
		}
		r.press("Install now")
		if !inCall {
			t.Fatal("installTools not called")
		}
		if a := r.dm.Args("installTools"); len(a) != 1 || a[0] != false {
			t.Errorf("installTools(%v)", a)
		}
		r.dm.Before = nil
		r.h.Settle()
		if !hasButton(r.root(), "Check for updates") {
			t.Error("no Check for updates after the install")
		}
		if find(r.root(), func(e *kit.Element) bool {
			_, ok := e.Widget().(*widgets.ProgressBar)
			return ok && !hiddenUnder(r.root(), e)
		}) != nil {
			t.Error("progress bar left")
		}
		r.press("Check for updates")
		if a := r.dm.Args("installTools"); len(a) != 1 || a[0] != true {
			t.Errorf("installTools(%v)", a)
		}
	})

	t.Run("says why an install failed, and installs only with video pages on in the saved settings", func(t *testing.T) {
		r := openVideo(t, clone(m6(), func(s *downexv1.Settings) { s.SiteMedia = true }), missingTools())
		r.dm.Before = func(name string, _ ...any) error {
			if name == "installTools" {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("tools_unavailable: yt-dlp and Deno are not set up: deno.exe is not signed by its publisher"))
			}
			return nil
		}
		if b := buttonNamed(t, r.root(), "Install now"); b.Disabled {
			t.Fatal("Install now disabled")
		}
		r.press("Install now")
		alerts := findAll(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleAlert })
		found := false
		for _, a := range alerts {
			if hasText(a, "Downex could not set up yt-dlp/Deno: deno.exe is not signed by its publisher.") {
				found = true
			}
		}
		if !found {
			t.Errorf("no install error: %q", texts(r.root()))
		}
		r.toggle("Download videos from web pages")
		if !r.sw("Update automatically").Disabled {
			t.Error("Update automatically enabled")
		}
		if !r.selectNamed("yt-dlp releases").Disabled {
			t.Error("channel enabled")
		}
	})

	t.Run("tells that the saved settings must have video pages on first", func(t *testing.T) {
		r := openVideo(t, clone(m6(), func(s *downexv1.Settings) { s.SiteMedia = false }), missingTools())
		if !buttonNamed(t, r.root(), "Install now").Disabled {
			t.Error("Install now enabled")
		}
		if !hasText(r.root(), "Turn on video downloads from web pages first.") {
			t.Error("no turn-on hint")
		}
		r.toggle("Download videos from web pages")
		if !hasText(r.root(), "Save the settings first") {
			t.Error("no save-first hint")
		}
	})

	t.Run("opens the licences in the browser, and says when downexd has no video pages", func(t *testing.T) {
		r := openVideo(t, clone(m6(), func(s *downexv1.Settings) { s.SiteMedia = true }), nil)
		if !hasText(r.root(), "Update Downex to download videos") {
			t.Error("no update-Downex note")
		}
		if hasButton(r.root(), "Install now") {
			t.Error("Install now shown")
		}
		// where a link goes is its tooltip (the page's lead names the hosts)
		deno := buttonNamed(t, r.root(), "Deno licence (MIT)")
		if deno.Tooltip != "https://github.com/denoland/deno/blob/main/LICENSE.md" {
			t.Errorf("tooltip %q", deno.Tooltip)
		}
		press(r.h, deno)
		if c := r.fp.Calls; c[len(c)-1] != "OpenExternal https://github.com/denoland/deno/blob/main/LICENSE.md" {
			t.Errorf("calls %q", c)
		}
		r.press("yt-dlp licence (Unlicense)")
		if c := r.fp.Calls; c[len(c)-1] != "OpenExternal https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE" {
			t.Errorf("calls %q", c)
		}
	})
}

// stored is the value kept under key ("" when none).
func stored(h *viewstest.Harness, key string) string {
	v, _, _ := h.Storage.Get(key)
	return v
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if f() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("timed out")
}

// themeStore and densityStore are the vitest file's own stores.
type themeStore struct {
	choice host.ThemeChoice
	sets   []host.ThemeChoice
}

func (s *themeStore) Choice() host.ThemeChoice { return s.choice }
func (s *themeStore) Set(c host.ThemeChoice)   { s.sets = append(s.sets, c) }

type densityStore struct {
	choice state.DensityChoice
	sets   []state.DensityChoice
}

func (s *densityStore) Choice() state.DensityChoice { return s.choice }
func (s *densityStore) Set(c state.DensityChoice)   { s.sets = append(s.sets, c) }

func TestSettingsDialogWindow(t *testing.T) {
	t.Run("lists eight pages in a vertical tablist that the arrows, Home and End move through", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{})
		list := find(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleTabList })
		if list == nil || list.Semantics.Name != "Settings pages" || list.Semantics.Orientation != kit.OrientationVertical {
			t.Fatalf("tablist %+v", list)
		}
		var names []string
		var tabbable []*kit.Element
		for _, e := range findAll(list, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleTab }) {
			names = append(names, e.Semantics.Name)
			if e.Focus == kit.FocusTab {
				tabbable = append(tabbable, e)
			}
		}
		if strings.Join(names, ",") != "General,Downloads,Network,Integration,Video sites,Appearance,Updates,About" {
			t.Errorf("tabs %q", names)
		}
		// The chosen page has the keyboard once the settings are loaded; the arrows reach the others.
		if r.focused() != r.tab("general") || len(tabbable) != 1 || tabbable[0] != r.tab("general") {
			t.Errorf("focus %v, tabbable %v", r.focused(), tabbable)
		}
		r.h.Key(kit.KeyArrowDown, 0)
		if r.focused() != r.tab("downloads") || !r.selected("downloads") {
			t.Error("ArrowDown did not move to Downloads")
		}
		if maybeInput(r.root(), "Default folder") == nil {
			t.Error("Downloads not shown")
		}
		r.h.Key(kit.KeyArrowUp, 0)
		if r.focused() != r.tab("general") {
			t.Error("ArrowUp")
		}
		r.h.Key(kit.KeyArrowUp, 0)
		if r.focused() != r.tab("about") {
			t.Error("ArrowUp wraps")
		}
		r.h.Key(kit.KeyHome, 0)
		if r.focused() != r.tab("general") {
			t.Error("Home")
		}
		r.h.Key(kit.KeyEnd, 0)
		if !r.selected("about") {
			t.Error("End")
		}
		// Other keys are left alone.
		r.h.Key(kit.KeyA, 0)
		if !r.selected("about") {
			t.Error("a moved")
		}
	})

	t.Run("shows an error on the page of its field, marks that page and saves once it is fixed", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{Page: validate.PageDownloads})
		r.typeIn("Global speed limit", "fast")
		r.click("appearance")
		r.save()
		if !r.selected("downloads") {
			t.Fatal("not on Downloads")
		}
		if r.focused() != r.input("Global speed limit").Base() {
			t.Errorf("focus %v", r.focused())
		}
		if !strings.Contains(r.tab("downloads").Semantics.Name, "needs attention") || strings.Contains(r.tab("general").Semantics.Name, "needs attention") {
			t.Error("marks")
		}
		alerts := findAll(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleAlert })
		if len(alerts) != 1 || alerts[0].Semantics.Name != "Some settings need attention." {
			t.Errorf("alerts %v", alerts)
		}
		r.noSave()
		// Fixed, it saves the limit in bytes per second.
		r.typeIn("Global speed limit", "512")
		r.choose("Global speed limit unit", "KB/s")
		r.save()
		if want := clone(loaded(), func(s *downexv1.Settings) { s.GlobalSpeedLimit = 512 * 1000 }); !equal(r.lastSave(), want) {
			t.Errorf("saved %v", r.lastSave())
		}
	})

	t.Run("opens on Downloads with the global speed limit focused (the Custom… limit of the status bar)", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{FocusField: validate.FieldGlobalSpeedLimit})
		if r.focused() != r.input("Global speed limit").Base() {
			t.Errorf("focus %v", r.focused())
		}
		if !r.selected("downloads") {
			t.Error("not on Downloads")
		}
		if v := r.input("Global speed limit").Text(); v != "2" {
			t.Errorf("value %q", v)
		}
		if v := r.selectNamed("Global speed limit unit").Value(); v != "MB/s" {
			t.Errorf("unit %q", v)
		}
	})

	t.Run("opens a page asked for by name: Integration explains the extension and its relay", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{Page: validate.PageIntegration})
		if r.sw("Ask before saving").Disabled || !r.selected("integration") {
			t.Fatal("Integration not loaded")
		}
		if !hasText(r.root(), "The Downex browser extension") || !hasText(r.root(), "downex-nmhost") || !hasText(r.root(), "Register with browsers again") {
			t.Error("extension texts missing")
		}
	})

	t.Run("applies the theme and the density at once, without Save, and keeps them on this computer", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{Page: validate.PageAppearance}, func(r *rig) { r.h.App.Theme.Set(host.ThemeSystem) })
		if buttonNamed(t, r.root(), "Save").Disabled {
			t.Error("Save disabled")
		}
		if !hasText(r.root(), "Saved on this computer right away.") {
			t.Error("no note")
		}
		th := radios(r.root(), "Theme")
		var on []bool
		for _, e := range th {
			on = append(on, checked(r.root(), e))
		}
		if fmt.Sprint(on) != "[true false false]" {
			t.Errorf("themes %v", on)
		}
		r.radio("Theme", "Dark")
		if r.h.App.Theme.Choice() != host.ThemeDark || stored(r.h, state.ThemeKey) != "dark" {
			t.Errorf("theme %q stored %q", r.h.App.Theme.Choice(), stored(r.h, state.ThemeKey))
		}
		de := radios(r.root(), "List density")
		if !checked(r.root(), de[0]) || !hasText(r.root(), "Two lines per download") {
			t.Error("comfortable not shown")
		}
		r.radio("List density", "Compact")
		if r.h.App.Density.Choice() != state.Compact || stored(r.h, state.DensityKey) != "compact" || !hasText(r.root(), "One line per download") {
			t.Error("compact not applied")
		}
		// Cancel keeps them: they were never part of the settings.
		r.press("Cancel")
		if r.closes != 1 {
			t.Error("not closed")
		}
		r.noSave()
		if r.h.App.Density.Choice() != state.Compact || r.h.App.Theme.Choice() != host.ThemeDark {
			t.Error("Cancel undid them")
		}
	})

	t.Run("changes the stores it is given, and shows their choice", func(t *testing.T) {
		ts := &themeStore{choice: host.ThemeLight}
		ds := &densityStore{choice: state.Compact}
		h := viewstest.New(t, viewstest.Options{Daemon: newDaemon(loaded()).Daemon})
		d := open(h, views.SettingsRequest{Page: validate.PageAppearance}, settings.Options{Theme: ts, Density: ds})
		root := d.Card()
		th := radios(root, "Theme")
		if !checked(root, th[1]) {
			t.Error("Light not chosen")
		}
		if de := radios(root, "List density"); !checked(root, de[1]) {
			t.Error("Compact not chosen")
		}
		rg := &rig{t: t, h: h, d: d}
		rg.radio("Theme", "System")
		rg.radio("List density", "Comfortable")
		if fmt.Sprint(ts.sets) != "[system]" || fmt.Sprint(ds.sets) != "[comfortable]" {
			t.Errorf("sets %v %v", ts.sets, ds.sets)
		}
	})

	t.Run("keeps the update address under Advanced, closed with a summary of what it hides", func(t *testing.T) {
		r := setup(t, clone(m6(), func(s *downexv1.Settings) { s.UpdateFeedUrl = "" }), views.SettingsRequest{Page: validate.PageUpdates})
		adv := buttonNamed(t, r.root(), "Advanced No update address: updates are off")
		if adv.Semantics.Expand != kit.ExpandClosed {
			t.Error("Advanced open")
		}
		if maybeInput(r.root(), "Update address") != nil {
			t.Error("the address is shown")
		}
		// Without an address, checking automatically cannot be on, and Check now says where to set one.
		if !hasText(r.root(), "Set an update address under Advanced first.") || !r.sw("Check for updates automatically").Disabled {
			t.Error("no address: hint or switch")
		}
		press(r.h, adv)
		if adv.Semantics.Expand != kit.ExpandOpen {
			t.Error("Advanced closed")
		}
		r.typeIn("Update address", "https://updates.example.com/feed.json")
		if r.sw("Check for updates automatically").Disabled {
			t.Error("switch disabled")
		}
		press(r.h, adv)
		if adv.Semantics.Expand != kit.ExpandClosed || !strings.Contains(adv.Semantics.Name, "Update address: updates.example.com") {
			t.Errorf("Advanced %q", adv.Semantics.Name)
		}
	})

	t.Run("closes only the list of an open select on Escape, not the window", func(t *testing.T) {
		r := setup(t, loaded(), views.SettingsRequest{Page: validate.PageDownloads})
		unit := r.selectNamed("Global speed limit unit")
		r.h.Window.Focus(unit)
		r.h.Key(kit.KeyArrowDown, 0)
		if !unit.IsOpen() {
			t.Fatal("the list did not open")
		}
		// the keyboard stays with the select while its list is open (aria-activedescendant)
		r.h.Key(kit.KeyEscape, 0)
		r.h.Settle()
		if unit.IsOpen() {
			t.Fatal("the list stayed open")
		}
		if r.closes != 0 || r.d.Leaving() {
			t.Error("the window closed")
		}
		if unit.Value() != "MB/s" {
			t.Errorf("unit %q", unit.Value())
		}
		// Closed, Escape closes the window as before.
		r.h.Key(kit.KeyEscape, 0)
		r.h.Advance(300e6)
		if r.closes != 1 {
			t.Error("Escape did not close the window")
		}
	})

	t.Run("chooses the proxy mode with a segmented control, showing only the fields of that mode", func(t *testing.T) {
		r := setup(t, clone(m6(), func(s *downexv1.Settings) { s.Proxy.Mode = downexv1.ProxyMode_PROXY_MODE_UNSPECIFIED }), views.SettingsRequest{Page: validate.PageNetwork})
		if buttonNamed(t, r.root(), "Add host rule").Disabled {
			t.Fatal("not loaded")
		}
		// 'unspecified' is shown as (and means) the system's settings.
		modes := radios(r.root(), "How downloads connect")
		var got []string
		for _, m := range modes {
			got = append(got, fmt.Sprintf("%s:%v", nameOf(m), checked(r.root(), m)))
		}
		if strings.Join(got, ",") != "System:true,No proxy:false,Manual:false,PAC script:false" {
			t.Errorf("modes %q", got)
		}
		if maybeInput(r.root(), "HTTP proxy") != nil {
			t.Error("HTTP proxy shown")
		}
		r.radio("How downloads connect", "PAC script")
		if maybeInput(r.root(), "Script address") == nil {
			t.Error("no script address")
		}
		r.radio("How downloads connect", "No proxy")
		if maybeInput(r.root(), "Proxy user name") != nil {
			t.Error("user name shown")
		}
		r.radio("How downloads connect", "Manual")
		if v := r.input("HTTP proxy").Text(); v != "proxy.lan:8080" {
			t.Errorf("http %q", v)
		}
		r.save()
		if m := r.lastSave().GetProxy().GetMode(); m != downexv1.ProxyMode_PROXY_MODE_MANUAL {
			t.Errorf("mode %v", m)
		}
	})
}

func TestSettingsDialogSizing(t *testing.T) {
	t.Run("is sized in rem (it grows with the text) up to the room the window has, without a fixed pixel height", func(t *testing.T) {
		for _, c := range []struct {
			w, h, font int
			wantW      kit.DIP
			wantH      kit.DIP
		}{
			{1200, 760, 16, 960, 760 - 44 - 32},     // 60 × 46 rem (960 × 736), at most the room
			{1920, 1080, 16, 960, 736},              // 60 × 46 rem
			{1920, 1080, 20, 1200, 920},             // 60 × 46 rem at 20 px
			{900, 600, 16, 900 - 32, 600 - 44 - 16}, // the room the window has
		} {
			h := viewstest.New(t, viewstest.Options{Width: c.w, Height: c.h, Font: c.font, Daemon: newDaemon(loaded()).Daemon})
			d := open(h, views.SettingsRequest{}, settings.Options{})
			card := d.Card().Size()
			if card.W != c.wantW || card.H != c.wantH {
				t.Errorf("%dx%d at %d px: %v, want %vx%v", c.w, c.h, c.font, card, c.wantW, c.wantH)
			}
			// the same on every page
			for _, p := range []string{"downloads", "network", "about"} {
				h.Click(tab(t, d, p))
				if s := d.Card().Size(); s != card {
					t.Errorf("%s: %v, want %v", p, s, card)
				}
			}
		}
	})
}

var _ = timestamppb.Now
