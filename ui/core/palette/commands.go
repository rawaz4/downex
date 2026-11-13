package palette

import (
	"slices"
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/query"
	"downex/ui/core/text"
)

// The main window's command list (App.svelte): every action of the window, with what it does in
// plain words, found by typing; and the downloads it finds.

// Icon names the commands use (the kit's icon set).
const (
	IconAdd          = "add"
	IconStream       = "stream"
	IconPause        = "pause"
	IconResume       = "resume"
	IconPauseAll     = "pause-all"
	IconResumeAll    = "resume-all"
	IconRestart      = "restart"
	IconRemove       = "remove"
	IconCheck        = "check"
	IconGauge        = "gauge"
	IconPower        = "power"
	IconSearch       = "search"
	IconSidebarPanel = "sidebar-panel"
	IconDensity      = "density"
	IconSystem       = "system"
	IconSun          = "sun"
	IconMoon         = "moon"
	IconGear         = "gear"
	IconQueue        = "queue"
	IconFilter       = "filter"
)

// CommandState is what the command list reads of the window.
type CommandState struct {
	// Stale: downexd is not reachable.
	Stale bool
	// SettingsLoaded: the settings are read.
	SettingsLoaded bool
	// What the toolbar can do now (App.svelte canAdd, canPause, …).
	CanAdd, CanPause, CanResume, CanPauseAll, CanResumeAll, CanRemove bool
	// Selected is the number of selected downloads, CompletedSelected the completed ones among
	// them.
	Selected          int
	CompletedSelected int
	// Active is the number of active downloads (the store's totals).
	Active int
	// GlobalSpeedLimit is the settings' limit in bytes per second (0 = unlimited).
	GlobalSpeedLimit int64
	// WhenDone: the main queue's post-queue action is known (the "When done…" menu exists).
	WhenDone bool
	// DetailsCollapsed: the details pane is hidden.
	DetailsCollapsed bool
	// Density is "comfortable" or "compact"; Theme "system", "light" or "dark".
	Density, Theme string
	// Index and Category are the sidebar's categories and the one shown; Counts its faceted
	// counts.
	Index    *categories.Index
	Category categories.Filter
	Counts   query.Counts
	// Queues are the queues the window offers (none when downexd has no queues), QueueCounts
	// what each has left to do.
	Queues      []*downexv1.Queue
	QueueCounts map[int64]int
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func count(n int) string { return format.Count(float64(n)) }

// Commands is the main window's command list, in its order.
func Commands(s CommandState) []Command {
	// why a command is unavailable (read with it): unreachable daemon, empty selection, else none
	why := func(needsSelection bool, none string) string {
		switch {
		case s.Stale:
			return "downexd is not reachable"
		case needsSelection && s.Selected == 0:
			return "nothing selected"
		}
		return none
	}
	const inUse = "already in use"
	selected := "the selected download"
	if s.Selected != 1 {
		selected = "the " + count(s.Selected) + " selected downloads"
	}
	// "its" for one download, "their" for several (and for none: the command is disabled then)
	its, it := plural(s.Selected, "its", "their"), plural(s.Selected, "it", "they")

	pauseAll := "Holds the queued downloads, keeps their progress"
	switch {
	case s.Active == 1:
		pauseAll = "Stops the 1 active download, keeps its progress"
	case s.Active > 0:
		pauseAll = "Stops the " + count(s.Active) + " active downloads, keeps their progress"
	}
	paused := s.Counts.Statuses[categories.Paused]
	resumeAll := "Resumes every paused download"
	if paused > 0 {
		resumeAll = "Resumes the " + count(paused) + " paused download" + plural(paused, "", "s")
	}
	again := "Gets the selected completed download again; asks what happens to its file"
	if s.CompletedSelected > 1 {
		again = "Gets the " + count(s.CompletedSelected) + " selected completed downloads again; asks what happens to their files"
	}
	limit := "unlimited"
	if s.GlobalSpeedLimit > 0 {
		limit = format.Speed(float64(s.GlobalSpeedLimit))
	}
	limitWhy := "the settings are not read yet"
	if s.Stale {
		limitWhy = "downexd is not reachable"
	}

	list := []Command{
		{ID: "add", Group: "Commands", Label: "Add download…", Icon: IconAdd, Shortcut: "Ctrl+N", Keywords: "new url link", Description: "Paste or type the links of files to download", Disabled: !s.CanAdd, DisabledReason: why(false, "")},
		{ID: "add-video", Group: "Commands", Label: "Add video or stream…", Icon: IconStream, Keywords: "new youtube hls dash page", Description: "A video page or a stream address, saved as one MP4", Disabled: !s.CanAdd, DisabledReason: why(false, "")},
		{ID: "pause", Group: "Commands", Label: "Pause selected", Icon: IconPause, Shortcut: "Space", Description: "Pauses " + selected + ", keeps " + its + " progress", Disabled: !s.CanPause, DisabledReason: why(true, "nothing to pause in the selection")},
		{ID: "resume", Group: "Commands", Label: "Resume selected", Icon: IconResume, Shortcut: "Space", Description: "Continues " + selected + " where " + it + " stopped", Disabled: !s.CanResume, DisabledReason: why(true, "nothing to resume in the selection")},
		{ID: "pause-all", Group: "Commands", Label: "Pause all", Icon: IconPauseAll, Description: pauseAll, Disabled: !s.CanPauseAll, DisabledReason: why(false, "nothing is downloading or queued")},
		{ID: "resume-all", Group: "Commands", Label: "Resume all", Icon: IconResumeAll, Description: resumeAll, Disabled: !s.CanResumeAll, DisabledReason: why(false, "nothing is paused")},
		{ID: "download-again", Group: "Commands", Label: "Download again…", Icon: IconRestart, Keywords: "restart redownload refresh completed finished", Description: again, Disabled: s.Stale || s.CompletedSelected == 0, DisabledReason: why(true, "nothing completed in the selection")},
		{ID: "remove", Group: "Commands", Label: "Remove selected…", Icon: IconRemove, Shortcut: "Del", Keywords: "delete", Description: "Asks first; the files stay unless you choose to delete them", Disabled: !s.CanRemove, DisabledReason: why(true, "")},
		{ID: "select-all", Group: "Commands", Label: "Select all", Icon: IconCheck, Shortcut: "Ctrl+A", Description: "Every download the list shows"},
		{ID: "limit", Group: "Commands", Label: "Set global speed limit…", Icon: IconGauge, Keywords: "bandwidth throttle", Description: "Now " + limit + " · applies to every download", Disabled: s.Stale || !s.SettingsLoaded, DisabledReason: limitWhy},
	}
	if s.WhenDone {
		list = append(list, Command{ID: "when-done", Group: "Commands", Label: "When done…", Icon: IconPower, Keywords: "shut down sleep hibernate quit post queue", Description: "Keep running, sleep, shut down, or quit Downex", Disabled: s.Stale, DisabledReason: "downexd is not reachable"})
	}
	details := "Hide details pane"
	if s.DetailsCollapsed {
		details = "Show details pane"
	}
	list = append(list,
		Command{ID: "search", Group: "View", Label: "Search downloads", Icon: IconSearch, Shortcut: "Ctrl+F", Keywords: "find filter", Description: "Filters the list by name or address"},
		Command{ID: "details", Group: "View", Label: details, Icon: IconSidebarPanel, Keywords: "panel", Description: "Facts, connections and speed of the selected download"},
		Command{ID: "density-comfortable", Group: "View", Label: "Density: comfortable", Icon: IconDensity, Keywords: "rows list spacing", Description: "Two lines per download: name, then site and category", Disabled: s.Density == "comfortable", DisabledReason: inUse},
		Command{ID: "density-compact", Group: "View", Label: "Density: compact", Icon: IconDensity, Keywords: "rows list spacing dense", Description: "One line per download, about 18 rows", Disabled: s.Density == "compact", DisabledReason: inUse},
		Command{ID: "theme-system", Group: "View", Label: "Theme: follow the system", Icon: IconSystem, Keywords: "appearance dark light switch", Description: "Light or dark, as the system is set", Disabled: s.Theme == "system", DisabledReason: inUse},
		Command{ID: "theme-light", Group: "View", Label: "Theme: light", Icon: IconSun, Keywords: "appearance switch", Description: "Always light", Disabled: s.Theme == "light", DisabledReason: inUse},
		Command{ID: "theme-dark", Group: "View", Label: "Theme: dark", Icon: IconMoon, Keywords: "appearance switch", Description: "Always dark", Disabled: s.Theme == "dark", DisabledReason: inUse},
		Command{ID: "settings", Group: "View", Label: "Open settings", Icon: IconGear, Shortcut: "Ctrl+,", Keywords: "preferences options", Description: "Downloads, network, browser, video sites, appearance and updates"},
	)
	if len(s.Queues) > 0 {
		list = append(list, Command{ID: "queues", Group: "View", Label: "Manage queues…", Icon: IconQueue, Keywords: "schedule", Description: "Order, limits, schedules and what happens when done"})
	}
	type cat struct {
		key   categories.Filter
		label string
	}
	cats := []cat{{categories.All, categories.AllLabel}}
	if s.Index != nil {
		for _, e := range s.Index.Entries() {
			cats = append(cats, cat{e.Key, text.EscapeBidi(e.Name)})
		}
	}
	cats = append(cats, cat{categories.OtherFilter, categories.OtherLabel})
	for _, c := range cats {
		n := s.Counts.Categories[c.key]
		list = append(list, Command{ID: "cat-" + string(c.key), Group: "Go to", Label: "Category: " + c.label, Icon: IconFilter, Keywords: "filter show", Description: count(n) + " download" + plural(n, "", "s")})
	}
	categoryLabel := categories.AllLabel
	if s.Index != nil {
		categoryLabel = text.EscapeBidi(s.Index.LabelOf(s.Category))
	}
	for _, st := range categories.StatusFilters() {
		n := s.Counts.Statuses[st]
		list = append(list, Command{ID: "status-" + string(st), Group: "Go to", Label: "Status: " + categories.StatusLabel(st), Icon: IconFilter, Keywords: "filter show", Description: count(n) + " in " + categoryLabel})
	}
	for _, q := range s.Queues {
		n := s.QueueCounts[q.GetId()]
		state := "Stopped"
		if q.GetRunning() {
			state = "Running"
		}
		list = append(list, Command{ID: "queue-" + strconv.FormatInt(q.GetId(), 10), Group: "Go to", Label: "Queue: " + text.EscapeBidi(q.GetName()), Icon: IconQueue, Keywords: "filter show", Description: state + " · " + count(n) + " to do"})
	}
	return list
}

// millis is a timestamp in epoch milliseconds (0 when unset).
func millis(d *downexv1.Download) int64 {
	if ts := d.GetCreatedAt(); ts != nil {
		if ms := ts.AsTime().UnixMilli(); ms > 0 {
			return ms
		}
	}
	return 0
}

// Downloads are the downloads the palette finds: running ones first, then the newest. nameOf is
// a download's display name (bidi-escaped, as the list shows it); badge a stream's or site's
// badge ("HLS", "YouTube") for a media download.
func Downloads(list []*downexv1.Download, nameOf, badge func(*downexv1.Download) string) []Download {
	sorted := slices.Clone(list)
	running := func(d *downexv1.Download) int { return boolInt(IsRunning(d)) }
	slices.SortStableFunc(sorted, func(a, b *downexv1.Download) int {
		if c := running(b) - running(a); c != 0 {
			return c
		}
		if ca, cb := millis(a), millis(b); ca != cb {
			if cb > ca {
				return 1
			}
			return -1
		}
		switch {
		case b.GetId() > a.GetId():
			return 1
		case b.GetId() < a.GetId():
			return -1
		}
		return 0
	})
	out := make([]Download, len(sorted))
	for i, d := range sorted {
		address := d.GetFinalUrl()
		if address == "" {
			address = d.GetUrl()
		}
		host := format.HostOf(address)
		if len(host) >= 4 && strings.EqualFold(host[:4], "www.") {
			host = host[4:]
		}
		out[i] = Download{ID: d.GetId(), Name: nameOf(d), Host: text.EscapeBidi(host), Kind: string(categories.KindOf(d)), Download: d}
		if d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && badge != nil {
			out[i].Tag = text.EscapeBidi(badge(d))
		}
	}
	return out
}

// Summary is the palette's live count: "2 commands", "1 download · 1 command", or that nothing
// matches. haveDownloads: the palette can find downloads.
func Summary(entries []Entry, haveDownloads bool) string {
	if len(entries) == 0 {
		if haveDownloads {
			return "No matching commands or downloads"
		}
		return "No matching commands"
	}
	downloads := 0
	for _, e := range entries {
		if e.Type == EntryDownload {
			downloads++
		}
	}
	commands := len(entries) - downloads
	c := "1 command"
	if commands != 1 {
		c = count(commands) + " commands"
	}
	if downloads == 0 {
		return c
	}
	d := "1 download"
	if downloads != 1 {
		d = count(downloads) + " downloads"
	}
	if commands == 0 {
		return d
	}
	return d + " · " + c
}

// UnavailableText is what a disabled command's row reads with it: "Unavailable: nothing
// selected", "Unavailable" without a reason; "" for an available command.
func UnavailableText(c *Command) string {
	switch {
	case !c.Disabled:
		return ""
	case c.DisabledReason == "":
		return "Unavailable"
	}
	return "Unavailable: " + c.DisabledReason
}

// The palette's own texts.
const (
	DialogLabel = "Command palette"
	SearchLabel = "Search commands"
	Placeholder = "Type a command or a download’s name"
	// ListLabel names the list of rows (and is the scope beside the field).
	ListLabel = "Commands and downloads"
	HintMove  = "move"
	HintOpen  = "open"
	HintRun   = "run"
	// HintFolder is Ctrl+Enter's, shown when downloads are listed and can be shown in a folder.
	HintFolder = "show in folder"
	HintClose  = "close"
)

// NothingMatches is the line under an empty list: Nothing matches “query”.
func NothingMatches(query string) string { return "Nothing matches “" + jsTrim(query) + "”." }
