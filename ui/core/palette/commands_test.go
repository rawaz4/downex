package palette

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/query"
)

// The command list of App.svelte, and the texts the component tests read (CommandPalette.test.ts
// "names its groups…", "shows running downloads…").

func state() CommandState {
	ix := categories.NewIndex([]*downexv1.Category{{Name: "Video", Extensions: []string{"mp4"}}})
	return CommandState{
		SettingsLoaded: true,
		CanAdd:         true, CanPause: true, CanPauseAll: true, CanRemove: true,
		Selected: 1, Active: 3,
		WhenDone: true,
		Density:  "comfortable", Theme: "system",
		Index: ix, Category: categories.All,
		Counts: query.Counts{
			Categories: map[categories.Filter]int{categories.All: 4, categories.Key("Video"): 1, categories.OtherFilter: 3},
			Statuses:   map[categories.Status]int{categories.AnyStatus: 4, categories.Active: 3, categories.Paused: 1},
		},
		Queues:      []*downexv1.Queue{{Id: 1, Name: "Main queue", Running: true}, {Id: 2, Name: "Night"}},
		QueueCounts: map[int64]int{1: 3, 2: 1},
	}
}

func find(t *testing.T, list []Command, id string) Command {
	t.Helper()
	i := slices.IndexFunc(list, func(c Command) bool { return c.ID == id })
	if i < 0 {
		t.Fatalf("no command %q", id)
	}
	return list[i]
}

func TestCommands(t *testing.T) {
	list := Commands(state())
	var got []string
	for _, c := range list {
		got = append(got, c.ID)
	}
	want := []string{"add", "add-video", "pause", "resume", "pause-all", "resume-all", "download-again", "remove", "select-all", "limit", "when-done",
		"search", "details", "density-comfortable", "density-compact", "theme-system", "theme-light", "theme-dark", "settings", "queues",
		"cat-all", "cat-cat:Video", "cat-other",
		"status-awaiting", "status-active", "status-queued", "status-paused", "status-completed", "status-failed",
		"queue-1", "queue-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("ids = %q", got)
	}
	for _, c := range []struct {
		id, label, desc string
		disabled        bool
		why             string
	}{
		{"pause", "Pause selected", "Pauses the selected download, keeps its progress", false, "nothing to pause in the selection"},
		{"resume", "Resume selected", "Continues the selected download where it stopped", true, "nothing to resume in the selection"},
		{"pause-all", "Pause all", "Stops the 3 active downloads, keeps their progress", false, "nothing is downloading or queued"},
		{"resume-all", "Resume all", "Resumes the 1 paused download", true, "nothing is paused"},
		{"download-again", "Download again…", "Gets the selected completed download again; asks what happens to its file", true, "nothing completed in the selection"},
		{"limit", "Set global speed limit…", "Now unlimited · applies to every download", false, "the settings are not read yet"},
		{"details", "Hide details pane", "Facts, connections and speed of the selected download", false, ""},
		{"density-comfortable", "Density: comfortable", "Two lines per download: name, then site and category", true, "already in use"},
		{"cat-all", "Category: All downloads", "4 downloads", false, ""},
		{"cat-cat:Video", "Category: Video", "1 download", false, ""},
		{"status-paused", "Status: Paused", "1 in All downloads", false, ""},
		{"status-awaiting", "Status: To confirm", "0 in All downloads", false, ""},
		{"queue-1", "Queue: Main queue", "Running · 3 to do", false, ""},
		{"queue-2", "Queue: Night", "Stopped · 1 to do", false, ""},
	} {
		got := find(t, list, c.id)
		if got.Label != c.label || got.Description != c.desc || got.Disabled != c.disabled || got.DisabledReason != c.why {
			t.Errorf("%s = %+v", c.id, got)
		}
	}

	s := state()
	s.Stale, s.Selected, s.Active, s.CompletedSelected, s.GlobalSpeedLimit = true, 0, 1, 2, 2_500_000
	s.Counts.Statuses[categories.Paused] = 0
	s.WhenDone, s.Queues, s.DetailsCollapsed = false, nil, true
	list = Commands(s)
	for _, c := range []struct{ id, desc, why string }{
		{"pause", "Pauses the 0 selected downloads, keeps their progress", "downexd is not reachable"},
		{"pause-all", "Stops the 1 active download, keeps its progress", "downexd is not reachable"},
		{"resume-all", "Resumes every paused download", "downexd is not reachable"},
		{"download-again", "Gets the 2 selected completed downloads again; asks what happens to their files", "downexd is not reachable"},
		{"limit", "Now 2.5 MB/s · applies to every download", "downexd is not reachable"},
	} {
		if got := find(t, list, c.id); got.Description != c.desc || got.DisabledReason != c.why {
			t.Errorf("stale %s = %+v", c.id, got)
		}
	}
	if find(t, list, "details").Label != "Show details pane" {
		t.Error("details label")
	}
	if slices.ContainsFunc(list, func(c Command) bool { return c.ID == "when-done" || c.ID == "queues" }) {
		t.Error("when-done or queues without the menu or queues")
	}
	// nothing selected
	s = state()
	s.Selected = 0
	if got := find(t, Commands(s), "remove").DisabledReason; got != "nothing selected" {
		t.Errorf("remove why = %q", got)
	}
}

func TestSummary(t *testing.T) {
	a := Command{ID: "a", Label: "Pause selected", Group: "Downloads", Disabled: true, DisabledReason: "nothing selected"}
	b := Command{ID: "b", Label: "Open settings", Group: "View"}
	cmds := []Command{a, b}
	if got := Summary(Entries(cmds, nil, ""), false); got != "2 commands" {
		t.Errorf("two = %q", got)
	}
	if got := Summary(Entries(cmds, nil, "settings"), false); got != "1 command" {
		t.Errorf("one = %q", got)
	}
	if got := Summary(Entries([]Command{{ID: "a", Label: "Pause all"}}, nil, "zzz"), false); got != "No matching commands" {
		t.Errorf("none = %q", got)
	}
	live := item(5, "conference-keynote.mp4", downloading, "youtube.com")
	done := item(6, "keynote-2026.mp4", completed, "media.example.org")
	if got := Summary(Entries([]Command{{ID: "add", Label: "Add download…"}}, []Download{live, done}, ""), true); got != "1 download · 1 command" {
		t.Errorf("mixed = %q", got)
	}
	if got := Summary(nil, true); got != "No matching commands or downloads" {
		t.Errorf("none with downloads = %q", got)
	}
	if got := Summary(Entries(nil, []Download{live, done}, "keynote"), true); got != "2 downloads" {
		t.Errorf("downloads = %q", got)
	}
	if got := UnavailableText(&a); got != "Unavailable: nothing selected" {
		t.Errorf("why = %q", got)
	}
	if got := UnavailableText(&Command{Disabled: true}); got != "Unavailable" {
		t.Errorf("no reason = %q", got)
	}
	if got := UnavailableText(&b); got != "" {
		t.Errorf("available = %q", got)
	}
	if got := NothingMatches(" zzz "); got != "Nothing matches “zzz”." {
		t.Errorf("nothing = %q", got)
	}
	// keynote 2026 finds only the completed one (App's search through the palette)
	found := Entries(nil, []Download{live, done}, "keynote 2026")
	if len(found) != 1 || found[0].Item.ID != 6 {
		t.Errorf("keynote 2026 = %v", keys(found))
	}
}

func TestPaletteDownloads(t *testing.T) {
	at := func(id int64, state downexv1.DownloadState, created int64, url, final string) *downexv1.Download {
		return &downexv1.Download{Id: id, State: state, CreatedAt: timestamppb.New(time.UnixMilli(created)), Url: url, FinalUrl: final, Filename: "f.zip"}
	}
	list := []*downexv1.Download{
		at(1, completed, 100, "https://www.example.com/a", ""),
		at(2, downloading, 50, "https://a.example/x", "https://WWW.cdn.example:8443/y"),
		at(3, queued, 300, "https://b.example/z", ""),
		at(4, completed, 300, "not a url", ""),
		{Id: 5, State: downloading, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Url: "https://www.youtube.com/watch?v=1", Filename: "v.mp4"},
	}
	got := Downloads(list, func(d *downexv1.Download) string { return d.GetFilename() }, func(*downexv1.Download) string { return "YouTube" })
	var order []int64
	for _, d := range got {
		order = append(order, d.ID)
	}
	// running first, then the newest, ties by the larger id
	if !slices.Equal(order, []int64{2, 5, 4, 3, 1}) {
		t.Errorf("order = %v", order)
	}
	hosts := map[int64]string{}
	for _, d := range got {
		hosts[d.ID] = d.Host
	}
	if hosts[1] != "example.com" || hosts[2] != "cdn.example:8443" || hosts[4] != "not a url" || hosts[5] != "youtube.com" {
		t.Errorf("hosts = %v", hosts)
	}
	if got[1].Tag != "YouTube" || got[0].Tag != "" || got[0].Kind != "archives" {
		t.Errorf("tags and kinds = %+v, %+v", got[0], got[1])
	}
}
