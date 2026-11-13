package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

func TestMediaSettings(t *testing.T) {
	if d := DefaultMediaSettings(); d != (MediaSettings{SiteMedia: true, ToolsAutoUpdate: true, ToolsChannel: "nightly"}) {
		t.Errorf("DefaultMediaSettings = %+v", d)
	}
	// Fields missing from the settings file take their defaults.
	for data, want := range map[string]MediaSettings{
		`{}`:                   DefaultMediaSettings(),
		`{"site_media":false}`: {SiteMedia: false, ToolsAutoUpdate: true, ToolsChannel: "nightly"},
		`{"tools_channel":"stable","tools_auto_update":false,"z":1}`: {SiteMedia: true, ToolsChannel: "stable"},
	} {
		var s MediaSettings
		if err := json.Unmarshal([]byte(data), &s); err != nil || s != want {
			t.Errorf("unmarshal %s = %+v, %v; want %+v", data, s, err, want)
		}
	}
	out, err := json.Marshal(MediaSettings{SiteMedia: true, ToolsChannel: "stable"})
	if err != nil || string(out) != `{"site_media":true,"tools_auto_update":false,"tools_channel":"stable"}` {
		t.Errorf("marshal = %s, %v", out, err)
	}

	for _, ok := range []string{"", "stable", "nightly"} {
		if n, err := (MediaSettings{ToolsChannel: ok}).normalize(); err != nil || n.ToolsChannel != ok {
			t.Errorf("normalize(%q) = %+v, %v", ok, n, err)
		}
	}
	for _, bad := range []string{"beta", "Stable", " nightly", "master"} {
		if _, err := (MediaSettings{ToolsChannel: bad}).normalize(); err == nil || !strings.HasPrefix(err.Error(), "tools_channel ") {
			t.Errorf("normalize(%q) = %v, want a tools_channel error", bad, err)
		}
	}
	if got, problems := (MediaSettings{SiteMedia: true, ToolsChannel: "beta"}).repair(); got.ToolsChannel != "nightly" ||
		!got.SiteMedia || len(problems) != 1 {
		t.Errorf("repair = %+v, %q", got, problems)
	}
	if got, problems := (MediaSettings{ToolsChannel: "stable"}).repair(); got.ToolsChannel != "stable" || problems != nil {
		t.Errorf("repair of valid settings = %+v, %q", got, problems)
	}
	// "" (a client that sent none) reads as the default channel.
	if c := (MediaSettings{}).channel(); c != "nightly" {
		t.Errorf("channel of \"\" = %q", c)
	}

	// Proto round trip (fields 24–26).
	p := &downexv1.Settings{}
	MediaSettings{SiteMedia: true, ToolsAutoUpdate: false, ToolsChannel: "stable"}.toProto(p)
	if !p.GetSiteMedia() || p.GetToolsAutoUpdate() || p.GetToolsChannel() != "stable" {
		t.Errorf("toProto = %v", p)
	}
	if got := mediaSettingsFromProto(p); got != (MediaSettings{SiteMedia: true, ToolsChannel: "stable"}) {
		t.Errorf("mediaSettingsFromProto = %+v", got)
	}
	if got := mediaSettingsFromProto(nil); got != (MediaSettings{}) {
		t.Errorf("mediaSettingsFromProto(nil) = %+v", got)
	}
}

// TestMediaSettingsFile: the media settings go through UpdateSettings into the "media"
// object of the settings file and back; an older file gets the defaults, an unknown
// channel on disk is repaired and refused from clients.
func TestMediaSettingsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFileName)
	if err := os.WriteFile(path, []byte(`{"max_concurrent_downloads": 2, "update": {"auto_check": false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOptions{settingsPath: path})
	if s := h.m.Settings(); s.Media != DefaultMediaSettings() || s.MaxConcurrentDownloads != 2 {
		t.Fatalf("settings of an older file = %+v", s)
	}
	got, err := h.dm.GetSettings(ctxT(t), connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Msg.GetSettings()
	if !s.GetSiteMedia() || !s.GetToolsAutoUpdate() || s.GetToolsChannel() != "nightly" {
		t.Fatalf("GetSettings = %v", s)
	}
	s.SiteMedia, s.ToolsAutoUpdate, s.ToolsChannel = false, false, "stable"
	res, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Msg.GetSettings(); r.GetSiteMedia() || r.GetToolsAutoUpdate() || r.GetToolsChannel() != "stable" {
		t.Fatalf("updated = %v", r)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if string(file["media"]) == "" || !strings.Contains(string(file["media"]), `"tools_channel": "stable"`) {
		t.Errorf("the settings file's media object: %s", data)
	}
	want := h.m.Settings()
	if h2 := newHarness(t, harnessOptions{settingsPath: path}); !h2.m.Settings().Equal(want) {
		t.Errorf("reloaded = %+v, want %+v", h2.m.Settings(), want)
	}

	s.ToolsChannel = "beta"
	_, err = h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "tools_channel") {
		t.Errorf("unknown channel: %v", err)
	}

	// An unknown channel on disk is repaired; the rest of the object is kept.
	if err := os.WriteFile(path, []byte(`{"media": {"site_media": false, "tools_channel": "beta"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	fixed, problems := raw.repair(func() (string, error) { return dir, nil })
	if fixed.Media != (MediaSettings{SiteMedia: false, ToolsAutoUpdate: true, ToolsChannel: "nightly"}) || len(problems) != 1 {
		t.Errorf("repaired = %+v, %q", fixed.Media, problems)
	}
}

// TestToolsFollowSettings: Config.Tools is configured at start and after every change of
// the media settings (not after other changes).
func TestToolsFollowSettings(t *testing.T) {
	tools := newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: newFakeSite(1), tools: tools})
	if configs, _, _ := tools.recorded(); len(configs) != 1 || configs[0] != (toolsConfig{true, true, "nightly"}) {
		t.Fatalf("configured at start: %+v", configs)
	}
	update := func(edit func(s *downexv1.Settings)) {
		t.Helper()
		s := protoSettings(h.m.Settings())
		edit(s)
		if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
			t.Fatal(err)
		}
	}
	update(func(s *downexv1.Settings) { s.SiteMedia, s.ToolsChannel = false, "" })
	waitUntil(t, "the tools configured", func() bool { c, _, _ := tools.recorded(); return len(c) == 2 })
	if c, _, _ := tools.recorded(); c[1] != (toolsConfig{false, true, "nightly"}) {
		t.Errorf("configured = %+v", c[1])
	}
	update(func(s *downexv1.Settings) { s.NotifyOnComplete = !s.NotifyOnComplete })
	update(func(s *downexv1.Settings) { s.SiteMedia, s.ToolsAutoUpdate, s.ToolsChannel = true, false, "stable" })
	waitUntil(t, "the tools configured", func() bool { c, _, _ := tools.recorded(); return len(c) >= 3 })
	if c, _, _ := tools.recorded(); len(c) != 3 || c[2] != (toolsConfig{true, false, "stable"}) {
		t.Errorf("configured = %+v", c)
	}
}
