package settings_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// The ports of SettingsAbout.test.ts: each subtest is named after its vitest case.

// testBuild is the vitest BUILD.
func testBuild() host.BuildInfo {
	return host.BuildInfo{Version: "1.0.0-test", OS: "windows", Arch: "amd64", GoVersion: "go1.27.0", WailsVersion: "v2.17.0"}
}

// aboutPlatform is the FakePlatform with the vitest api's diagnostics and notices.
type aboutPlatform struct {
	*viewstest.FakePlatform
	diagnostics string
	notices     string
	diagCalls   int
}

func (p *aboutPlatform) Diagnostics(context.Context) string { p.diagCalls++; return p.diagnostics }
func (p *aboutPlatform) ThirdPartyNotices() string          { return p.notices }

// aboutRig is the vitest setup(): the About page over the test daemon (its tools ready).
type aboutRig struct {
	*rig
	p *aboutPlatform
}

func setupAbout(t *testing.T, edit func(*downexv1.Settings), mutate ...func(*rig)) *aboutRig {
	t.Helper()
	s := loaded()
	s.DefaultDir = "/home/you/Downloads"
	s.SiteMedia = true
	if edit != nil {
		edit(s)
	}
	ar := &aboutRig{}
	all := append([]func(*rig){func(r *rig) {
		r.h.App.Build = testBuild()
		r.dm.SetTools(statetest.Tools(func(*downexv1.ToolsStatus) {})) // api.tools = makeTools()
		ar.p = &aboutPlatform{FakePlatform: r.fp}
		r.h.App.Platform = ar.p
	}}, mutate...)
	ar.rig = setup(t, s, views.SettingsRequest{Page: validate.PageAbout}, all...)
	return ar
}

// fact is the value shown beside a label of the "This copy" card.
func fact(root *kit.Element, label string) string {
	dt := find(root, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleTerm && textOf(e) == label })
	if dt == nil {
		return ""
	}
	kids := dt.Parent().Children()
	for i, k := range kids {
		if k == dt && i+1 < len(kids) {
			return strings.Join(strings.Fields(strings.Join(texts(kids[i+1]), " ")), " ")
		}
	}
	return ""
}

// textOf is the text of a text element.
func textOf(e *kit.Element) string {
	if t := texts(e); len(t) > 0 {
		return t[0]
	}
	return ""
}

func headingNamed(root *kit.Element, name string) *kit.Element {
	return find(root, func(e *kit.Element) bool {
		return e.Semantics.Role == kit.RoleHeading && textOf(e) == name && !hiddenUnder(root, e)
	})
}

// listNamed is the list named name; listItems its items.
func listNamed(root *kit.Element, name string) *kit.Element {
	return find(root, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleList && e.Semantics.Name == name })
}

func listItems(list *kit.Element) []*kit.Element {
	return findAll(list, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleListItem })
}

func itemWith(t *testing.T, list *kit.Element, sub string) *kit.Element {
	t.Helper()
	for _, li := range listItems(list) {
		if hasText(li, sub) {
			return li
		}
	}
	t.Fatalf("no item with %q", sub)
	return nil
}

func TestSettingsAbout(t *testing.T) {
	t.Run("says which Downex this is: the window, downexd and the build (never the IPC endpoint)", func(t *testing.T) {
		r := setupAbout(t, nil)
		root := r.root()
		if headingNamed(root, "Downex") == nil {
			t.Error("no Downex heading")
		}
		if !hasText(root, "Version 1.0.0-test") {
			t.Error("no version")
		}
		// The native window has no WebView2, so its part is left out as when the user agent
		// names none (SettingsAbout.svelte drops an empty part).
		if f := fact(root, "Window"); f != "1.0.0-test · Windows x64" {
			t.Errorf("Window %q", f)
		}
		if f := fact(root, "Built with"); f != "Go 1.27.0 · Wails 2.17.0" {
			t.Errorf("Built with %q", f)
		}
		if f := fact(root, "downexd"); !regexp.MustCompile(`^1\.0\.0-test · running since .+ · process 1$`).MatchString(f) {
			t.Errorf("downexd %q", f)
		}
		if find(root, func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleAlert && !hiddenUnder(root, e) }) != nil {
			t.Error("an alert")
		}
		for _, s := range allStrings(root) {
			if strings.Contains(s, "127.0.0.1:47820") {
				t.Errorf("the endpoint is shown: %q", s)
			}
		}
	})

	t.Run("warns when the window and downexd are different versions", func(t *testing.T) {
		r := setupAbout(t, nil, func(r *rig) { b := testBuild(); b.Version = "1.1.0"; r.h.App.Build = b })
		a := find(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleAlert })
		if a == nil || !hasText(a, "This window is version 1.1.0, but downexd is version 1.0.0-test.") {
			t.Errorf("no warning: %q", texts(r.root()))
		}
	})

	t.Run("shows the commit only when the build has one", func(t *testing.T) {
		r := setupAbout(t, nil, func(r *rig) { b := testBuild(); b.Commit = "abcdef0123456789"; r.h.App.Build = b })
		if f := fact(r.root(), "Built with"); f != "Go 1.27.0 · Wails 2.17.0 · Commit abcdef012345" {
			t.Errorf("Built with %q", f)
		}
	})

	t.Run("says why updates are off, and opens the Updates page", func(t *testing.T) {
		r := setupAbout(t, func(s *downexv1.Settings) { s.UpdateFeedUrl = "https://u.example/update.json" })
		if !hasText(r.root(), "Off in this build (no signing key)") {
			t.Errorf("no reason: %q", texts(r.root()))
		}
		r.press("Update settings")
		if r.d.Page() != validate.PageUpdates {
			t.Errorf("page %q", r.d.Page())
		}
	})

	t.Run("shows yt-dlp and Deno and checks for their updates", func(t *testing.T) {
		r := setupAbout(t, nil)
		list := listNamed(r.root(), "Video tools")
		if list == nil {
			t.Fatal("no Video tools list")
		}
		if n := len(listItems(list)); n != 2 {
			t.Errorf("%d tools", n)
		}
		for _, s := range []string{"yt-dlp", "2026.10.10.233141", "Deno"} {
			if !hasText(list, s) {
				t.Errorf("no %q", s)
			}
		}
		r.press("Check for tool updates")
		if a := r.dm.Args("installTools"); len(a) != 1 || a[0] != true {
			t.Errorf("installTools(%v)", a)
		}
	})

	t.Run("opens and copies Downex’s folders; the host resolves the paths", func(t *testing.T) {
		r := setupAbout(t, nil)
		folders := listNamed(r.root(), "Folders")
		if folders == nil {
			t.Fatal("no Folders list")
		}
		if !hasText(itemWith(t, folders, "Logs"), "/home/you/.local/state/Downex") {
			t.Error("no logs path")
		}
		press(r.h, buttonNamed(t, itemWith(t, folders, "Logs"), "Open the Logs folder"))
		press(r.h, buttonNamed(t, itemWith(t, folders, "Downloads"), "Open the Downloads folder"))
		var opened []string
		for _, c := range r.fp.Calls {
			if strings.HasPrefix(c, "OpenAppFolder ") {
				opened = append(opened, strings.TrimPrefix(c, "OpenAppFolder "))
			}
		}
		if strings.Join(opened, ",") != "logs,downloads" {
			t.Errorf("folders %q", opened)
		}
		press(r.h, buttonNamed(t, itemWith(t, folders, "Data"), "Copy the path of the Data folder"))
		if c := r.fp.Copied; len(c) != 1 || c[0] != "/home/you/.config/Downex" {
			t.Errorf("copied %q", c)
		}
	})

	t.Run("separates each folder’s name from its hint with a spaced dot", func(t *testing.T) {
		r := setupAbout(t, nil)
		folders := listNamed(r.root(), "Folders")
		var labels []string
		for _, li := range listItems(folders) {
			labels = append(labels, textOf(li))
		}
		if len(labels) == 0 || labels[0] != "Data · The list of downloads and the settings" {
			t.Errorf("labels %q", labels)
		}
		for _, l := range labels {
			if !regexp.MustCompile(`^\S.* · \S`).MatchString(l) {
				t.Errorf("label %q", l)
			}
		}
	})

	t.Run("cannot copy a folder an older downexd does not report", func(t *testing.T) {
		r := setupAbout(t, nil, func(r *rig) {
			r.dm.status = &downexv1.StatusResponse{Version: "1.0.0-test", Pid: 1, DataDir: "/home/you/.config/Downex"}
		})
		logs := itemWith(t, listNamed(r.root(), "Folders"), "Logs")
		if !hasText(logs, "Not reported by this downexd") {
			t.Errorf("logs %q", texts(logs))
		}
		if b := buttonNamed(t, logs, "Copy the path of the Logs folder"); !disabled(b) {
			t.Error("copy enabled")
		}
	})

	t.Run("copies diagnostics with the WebView2 version, never the endpoint or the update address", func(t *testing.T) {
		r := setupAbout(t, func(s *downexv1.Settings) { s.UpdateFeedUrl = "https://u.example/update.json" })
		r.p.diagnostics = "Downex 1.0.0-test\nendpoint 127.0.0.1:47820\nfeed https://u.example/update.json\n"
		r.press("Copy diagnostics")
		c := r.fp.Copied
		if len(c) != 1 {
			t.Fatalf("copied %q", c)
		}
		// The native window has no WebView2 version to add: the host's diagnostics are asked
		// without one (vitest: { webview: … } from the WebView2 user agent).
		if r.p.diagCalls != 1 {
			t.Errorf("diagnostics asked %d times", r.p.diagCalls)
		}
		if !strings.Contains(c[0], "Downex 1.0.0-test") || strings.Contains(c[0], "127.0.0.1:47820") || strings.Contains(c[0], "u.example") {
			t.Errorf("copied %q", c[0])
		}
		st := find(r.root().Parent(), func(e *kit.Element) bool {
			return e.Semantics.Role == kit.RoleStatus && strings.Contains(e.Semantics.Name, "Diagnostics copied")
		})
		if st == nil {
			st = find(r.d.Card(), func(e *kit.Element) bool {
				return e.Semantics.Role == kit.RoleStatus && strings.Contains(e.Semantics.Name, "Diagnostics copied")
			})
		}
		if st == nil {
			t.Error("no Diagnostics copied status")
		}
	})

	t.Run("shows the open-source notices, one component at a time, and comes back", func(t *testing.T) {
		r := setupAbout(t, nil)
		r.p.notices = strings.Join([]string{"THIRD-PARTY NOTICES", "", "Intro.", "", strings.Repeat("=", 20), "github.com/wailsapp/wails/v2 v2.17.0", strings.Repeat("=", 20), "MIT License", ""}, "\n")
		r.press("View open-source notices")
		if headingNamed(r.root(), "Open-source notices") == nil {
			t.Fatal("no notices heading")
		}
		if !hasButton(r.root(), "github.com/wailsapp/wails/v2 v2.17.0") && !hasText(r.root(), "github.com/wailsapp/wails/v2 v2.17.0") {
			t.Error("no component")
		}
		if headingNamed(r.root(), "Downex") != nil {
			t.Error("the Downex heading is still shown")
		}
		r.press("Back to About")
		if headingNamed(r.root(), "Downex") == nil {
			t.Fatal("not back")
		}
		if f := r.focused(); f == nil || f != buttonNamed(t, r.root(), "View open-source notices") {
			t.Errorf("focus %v", f)
		}
	})
}

func TestSettingsAboutInTheWindow(t *testing.T) {
	t.Run("is the last page of the side navigation", func(t *testing.T) {
		r := setupAbout(t, nil)
		r.d.Remove()
		r2 := setup(t, loaded(), views.SettingsRequest{}, func(r *rig) { r.h.App.Build = testBuild() })
		tabs := findAll(r2.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleTab })
		last := tabs[len(tabs)-1]
		if l := strings.TrimSpace(textOf(last)); l != "About" {
			t.Errorf("last tab %q", l)
		}
		r2.h.Click(last)
		if headingNamed(r2.root(), "Downex") == nil {
			t.Fatal("no Downex heading")
		}
		if f := fact(r2.root(), "downexd"); !strings.HasPrefix(f, "1.0.0-test") {
			t.Errorf("downexd %q", f)
		}
	})
}

// disabled reports a control that cannot be used.
func disabled(e *kit.Element) bool {
	if d, ok := e.Widget().(interface{ IsDisabled() bool }); ok {
		return d.IsDisabled()
	}
	var n kit.SemanticNode
	if f, ok := e.Widget().(kit.SemanticFiller); ok {
		f.FillSemantics(&n)
	}
	return e.Disabled || n.States&kit.NodeDisabled != 0 || e.Semantics.States&kit.NodeDisabled != 0
}

// allStrings is every text and accessible string under root: labels, names, values,
// descriptions and tooltips (what the screen and assistive technology are given).
func allStrings(root *kit.Element) []string {
	out := texts(root)
	walk(root, func(e *kit.Element) bool {
		var n kit.SemanticNode
		if f, ok := e.Widget().(kit.SemanticFiller); ok {
			f.FillSemantics(&n)
		}
		out = append(out, e.Semantics.Name, e.Semantics.Description, e.Semantics.Value, e.Tooltip, n.Name, n.Description, n.Value)
		if in, ok := e.Widget().(*widgets.Input); ok && in.InputOptions().Password {
			return true // the system's password field holds it and draws only its marks
		}
		if tx, ok := e.Widget().(interface{ Text() string }); ok {
			out = append(out, tx.Text())
		}
		return true
	})
	return out
}
