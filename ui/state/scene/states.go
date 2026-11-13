package scene

// The 87 fixture states of scripts/measure-dialogs.mjs (FIT: 78 dialog states,
// FIT_PROGRESS: 9 progress-window views), the states the step-1 references were taken in
// (ref-0.9.2 in the research folder's native-ui, see ui/internal/research). A state is a
// scene (Fixture: the demo's ?fixture= tokens; Progress: the download of a progress window)
// and the steps that open its dialog, as the script plays them on today's page. The steps
// name today's DOM (roles, CSS selectors, texts) so that each view can map them to its own
// widgets; their texts are the window's own.

// StepKind is what a step does.
type StepKind uint8

// The steps of measure-dialogs.mjs.
const (
	// StepKey presses Key (with Ctrl when Ctrl).
	StepKey StepKind = iota + 1
	// StepClick clicks the element matching Selector whose text is Text (click()).
	StepClick
	// StepClickSelector clicks the element matching Selector (clickSel()).
	StepClickSelector
	// StepSelectRow clicks the row named Text.
	StepSelectRow
	// StepCtrlRow Ctrl-clicks the row named Text (adds it to the selection).
	StepCtrlRow
	// StepContextRow right-clicks the row named Text (its context menu).
	StepContextRow
	// StepDoubleRow double-clicks the row named Text.
	StepDoubleRow
	// StepSearch types Text into the list's search (the list is virtual: rows far down are
	// found this way).
	StepSearch
	// StepTypeIn types Text into the field matching Selector.
	StepTypeIn
	// StepFont sets the text size of the measurement (16 or 20 px) at this point.
	StepFont
	// StepMoreActions opens the title bar's "More actions" menu.
	StepMoreActions
	// StepClearFolder empties the dialog's folder field (the last monospaced input).
	StepClearFolder
	// StepWait waits Ms milliseconds (the Confirm dialog's guard delay).
	StepWait
	// StepFindMedia is Add → Video or stream: types Text into the address field, presses
	// "Find qualities" and waits for Selector inside the dialog.
	StepFindMedia
	// StepWaitFor waits for Selector inside the dialog (at most Ms).
	StepWaitFor
)

// Step is one step of a state.
type Step struct {
	Kind     StepKind
	Key      string
	Ctrl     bool
	Selector string
	Text     string
	Ms       int
}

// FixtureState is one of the 87 states.
type FixtureState struct {
	// Name is the reference's state name ("confirm-video-6-owner", "pw-status").
	Name string
	// Dialog is the dialog it shows ("Confirm", "Add", …, "Progress").
	Dialog string
	// State says what it shows, as the references' index does.
	State string
	// Fixture is the demo's ?fixture= value ("" = the Studio scene as it is).
	Fixture string
	// Confirm: the Confirm dialog opens by itself (the download awaiting confirmation).
	Confirm bool
	// Progress is the download of a progress window (0: the main window).
	Progress int64
	// Details: the progress window opens expanded (downex.progress.details = "1").
	Details bool
	// Selects: the state's selects are opened in turn by the script.
	Selects bool
	// Unbounded: content without a bound (a long list), which may scroll in one region.
	Unbounded bool
	// Panes: two columns that may scroll side by side in a small window.
	Panes bool
	Steps []Step
}

const youTube = "https://www.youtube.com/watch?v=Xb7mQ2kLp9w"

var several = []string{"novel-draft.epub", "podcast-episode-41.mp3", "drone-footage-coast.mp4", "viewer-8.x86_64.rpm", "project-assets-362.zip", "nature-documentary-part1.mp4"}

func key(k string, ctrl bool) Step    { return Step{Kind: StepKey, Key: k, Ctrl: ctrl} }
func click(sel, text string) Step     { return Step{Kind: StepClick, Selector: sel, Text: text} }
func clickSel(sel string) Step        { return Step{Kind: StepClickSelector, Selector: sel} }
func selectRow(name string) Step      { return Step{Kind: StepSelectRow, Text: name} }
func ctrlRow(name string) Step        { return Step{Kind: StepCtrlRow, Text: name} }
func ctxRow(name string) Step         { return Step{Kind: StepContextRow, Text: name} }
func dblRow(name string) Step         { return Step{Kind: StepDoubleRow, Text: name} }
func search(text string) Step         { return Step{Kind: StepSearch, Text: text} }
func typeIn(sel, text string) Step    { return Step{Kind: StepTypeIn, Selector: sel, Text: text} }
func waitFor(sel string, ms int) Step { return Step{Kind: StepWaitFor, Selector: sel, Ms: ms} }
func steps(s ...[]Step) []Step {
	var out []Step
	for _, x := range s {
		out = append(out, x...)
	}
	return out
}
func one(s ...Step) []Step { return s }

var (
	font          = Step{Kind: StepFont}
	openQueues    = one(Step{Kind: StepMoreActions}, click("[role=menuitem]", "Manage queues"))
	startDownload = one(Step{Kind: StepWait, Ms: 700}, click("[role=dialog] button", "Start download"))
	// invalidConfirm clears the folder and types a name the daemon refuses, then Start:
	// both fields show their error.
	invalidConfirm = steps(one(Step{Kind: StepClearFolder}, typeIn("[role=dialog] input.input:not(.mono)", "report<draft>?.mp4")), startDownload)
)

func queueRow(name string) Step { return click("[role=dialog] .queue", name) }

// findMedia is Add → Video or stream with address, waiting for until.
func findMedia(address, until string) []Step {
	if until == "" {
		until = ".found-mark"
	}
	return one(key("n", true), click("label", "Video or stream"), Step{Kind: StepFindMedia, Text: address, Selector: until})
}

func severalSteps(last ...Step) []Step {
	out := []Step{selectRow(several[0])}
	for _, n := range several[1:] {
		out = append(out, ctrlRow(n))
	}
	return append(append(out, font), last...)
}

// Fixtures is the 87 states, in the script's order (FIT, then FIT_PROGRESS).
func Fixtures() []FixtureState {
	settingsPage := func(page, tab string, selects bool) FixtureState {
		s := FixtureState{Name: "settings-" + page, Dialog: "Settings", State: page, Selects: selects, Steps: one(key(",", true))}
		if tab != "" {
			s.Steps = append(s.Steps, click("[role=tab]", tab))
		}
		return s
	}
	out := []FixtureState{
		// Confirm: a file
		{Name: "confirm-file", Dialog: "Confirm", State: "file budget-plan.xlsx, Queue choice", Fixture: "confirm:file", Confirm: true},
		{Name: "confirm-file-1q", Dialog: "Confirm", State: "file, one queue", Fixture: "confirm:file,queues:1", Confirm: true},
		{Name: "confirm-file-waiting", Dialog: "Confirm", State: "file, 2 more waiting, Queue choice", Fixture: "confirm:file,waiting:3", Confirm: true},
		{Name: "confirm-file-invalid", Dialog: "Confirm", State: "file, name and folder errors, Queue choice", Fixture: "confirm:file", Confirm: true, Steps: invalidConfirm},
		{Name: "confirm-file-fail", Dialog: "Confirm", State: "file, downexd refused (disk full), Queue choice", Fixture: "confirm:file,fail", Confirm: true, Steps: startDownload},
		{Name: "confirm-file-longurl", Dialog: "Confirm", State: "file, a signed storage address (600 characters), Queue choice", Fixture: "confirm:file,url:long", Confirm: true, Selects: true},
		{Name: "confirm-file-longurl-fail", Dialog: "Confirm", State: "file, a signed storage address, refused, Queue choice", Fixture: "confirm:file,url:long,fail", Confirm: true, Steps: startDownload},
		// Confirm: a web page's video
		{Name: "confirm-video-1", Dialog: "Confirm", State: "YouTube video, 1 quality", Fixture: "confirm:site,q:1,queues:1", Confirm: true},
		{Name: "confirm-video-3", Dialog: "Confirm", State: "YouTube video, 3 qualities", Fixture: "confirm:site,q:3,queues:1", Confirm: true},
		{Name: "confirm-video-6-owner", Dialog: "Confirm", State: "owner's case: 6 qualities 1080p50-144p, audio select, one queue", Fixture: "confirm:site,q:6,queues:1", Confirm: true, Selects: true},
		{Name: "confirm-video-6-queue", Dialog: "Confirm", State: "6 qualities, Queue choice", Fixture: "confirm:site,q:6", Confirm: true, Selects: true},
		{Name: "confirm-video-8", Dialog: "Confirm", State: "8 qualities 4K60 HDR-144p (AV1 note)", Fixture: "confirm:site,q:8,queues:1", Confirm: true},
		{Name: "confirm-video-8-long", Dialog: "Confirm", State: "8 qualities, a 2-line title", Fixture: "confirm:site,q:8,title:long,queues:1", Confirm: true},
		{Name: "confirm-video-8-queue", Dialog: "Confirm", State: "8 qualities, Queue choice", Fixture: "confirm:site,q:8", Confirm: true},
		{Name: "confirm-video-6-audio1", Dialog: "Confirm", State: "6 qualities, one audio track (no choice)", Fixture: "confirm:site,q:6,audio:1,queues:1", Confirm: true},
		{Name: "confirm-video-6-dub", Dialog: "Confirm", State: "6 qualities, 3 audio languages", Fixture: "confirm:site,q:6,audio:3,queues:1", Confirm: true, Selects: true},
		{Name: "confirm-video-6-long", Dialog: "Confirm", State: "6 qualities, long title (wraps)", Fixture: "confirm:site,q:6,title:long,queues:1", Confirm: true},
		{Name: "confirm-video-6-ku", Dialog: "Confirm", State: "6 qualities, Kurdish title (RTL)", Fixture: "confirm:site,q:6,title:ku,queues:1", Confirm: true},
		{Name: "confirm-video-6-ar", Dialog: "Confirm", State: "6 qualities, Arabic title (RTL)", Fixture: "confirm:site,q:6,title:ar,queues:1", Confirm: true},
		{Name: "confirm-video-6-notitle", Dialog: "Confirm", State: "6 qualities, no title", Fixture: "confirm:site,q:6,title:none,queues:1", Confirm: true},
		{Name: "confirm-video-6-longurl", Dialog: "Confirm", State: "6 qualities, playlist address with tracking parameters", Fixture: "confirm:site,q:6,url:long,queues:1", Confirm: true},
		{Name: "confirm-video-6-waiting", Dialog: "Confirm", State: "6 qualities, 1 more waiting", Fixture: "confirm:site,q:6,queues:1,waiting:2", Confirm: true},
		{Name: "confirm-video-6-fail", Dialog: "Confirm", State: "6 qualities, downexd refused (disk full)", Fixture: "confirm:site,q:6,queues:1,fail", Confirm: true, Steps: startDownload},
		{Name: "confirm-video-6-invalid", Dialog: "Confirm", State: "6 qualities, name and folder errors", Fixture: "confirm:site,q:6,queues:1", Confirm: true, Steps: invalidConfirm},
		{Name: "confirm-video-8-all", Dialog: "Confirm", State: "8 qualities, 3 audio, long title, long address, Queue choice, 1 more waiting, refused",
			Fixture: "confirm:site,q:8,audio:3,title:long,url:long,waiting:2,fail", Confirm: true, Steps: startDownload},
		// Confirm: streams
		{Name: "confirm-hls", Dialog: "Confirm", State: "HLS stream, 3 qualities (~ sizes), 3 audio", Fixture: "confirm:hls,queues:1", Confirm: true},
		{Name: "confirm-hls-queue", Dialog: "Confirm", State: "HLS stream, Queue choice", Fixture: "confirm:hls", Confirm: true},
		{Name: "confirm-dash", Dialog: "Confirm", State: "DASH stream, 3 qualities, 3 audio", Fixture: "confirm:dash,queues:1", Confirm: true},
		{Name: "confirm-muxed", Dialog: "Confirm", State: "HLS with muxed sound (TS to MP4)", Fixture: "confirm:muxed,queues:1", Confirm: true},
		{Name: "confirm-stream-audio", Dialog: "Confirm", State: "audio-only stream", Fixture: "confirm:audio,queues:1", Confirm: true},
		// Add
		{Name: "add-files", Dialog: "Add", State: "Files", Steps: one(key("n", true))},
		{Name: "add-files-more", Dialog: "Add", State: "Files, More options open, Queue choice", Selects: true, Steps: one(key("n", true), clickSel("[role=dialog] .more-toggle"))},
		{Name: "add-files-manifest", Dialog: "Add", State: "Files, a playlist address (suggestion note)",
			Steps: one(key("n", true), typeIn("[role=dialog] textarea", "https://media.example.org/talks/keynote-2026/master.m3u8"))},
		{Name: "add-stream", Dialog: "Add", State: "Video or stream, empty", Steps: one(key("n", true), click("label", "Video or stream"))},
		{Name: "add-video-installing", Dialog: "Add", State: "video page, yt-dlp and Deno downloading", Fixture: "tools:slow", Steps: findMedia(youTube, ".waiting")},
		{Name: "add-video-getting", Dialog: "Add", State: "video page, getting video info", Fixture: "tools:ready,resolve:slow", Steps: findMedia(youTube, ".getting")},
		{Name: "add-video-1", Dialog: "Add", State: "video found, 1 quality", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=1", "")},
		{Name: "add-video-3", Dialog: "Add", State: "video found, 3 qualities", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=3", "")},
		{Name: "add-video-6", Dialog: "Add", State: "video found, 6 qualities", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=6", "")},
		{Name: "add-video-8", Dialog: "Add", State: "video found, 8 qualities (AV1 note)", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=8", "")},
		{Name: "add-video-8-more", Dialog: "Add", State: "video found, 8 qualities, More options open, Queue choice", Fixture: "tools:ready", Selects: true,
			Steps: append(findMedia(youTube+"&qualities=8", ""), clickSel("[role=dialog] .more-toggle"))},
		{Name: "add-video-6-more", Dialog: "Add", State: "video found, 6 qualities, More options open, Queue choice", Fixture: "tools:ready",
			Steps: append(findMedia(youTube+"&qualities=6", ""), clickSel("[role=dialog] .more-toggle"))},
		// Add video, while downexd reads the page again (its 15-minute cache expired)
		{Name: "add-video-6-adding", Dialog: "Add", State: "video found, 6 qualities, being added (getting video info)", Fixture: "tools:ready,add:slow",
			Steps: append(findMedia(youTube+"&qualities=6", ""), click("[role=dialog] button", "Add video"), waitFor(".getting", 6000))},
		{Name: "add-video-6-long", Dialog: "Add", State: "video found, 6 qualities, long title", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=6&title=long", "")},
		{Name: "add-video-6-ar", Dialog: "Add", State: "video found, 6 qualities, Arabic title", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=6&title=ar", "")},
		{Name: "add-video-6-dub", Dialog: "Add", State: "video found, 6 qualities, 3 audio languages", Fixture: "tools:ready", Steps: findMedia(youTube+"&qualities=6&audio=3", "")},
		{Name: "add-hls", Dialog: "Add", State: "HLS stream found", Steps: findMedia("https://media.example.org/talks/keynote-2026/master.m3u8", "")},
		{Name: "add-video-error", Dialog: "Add", State: "video page refused (private video)", Fixture: "tools:ready", Steps: findMedia("https://www.youtube.com/watch?v=private0001", ".failures")},
		// Download again, Restart, Remove, Open
		{Name: "again-keep", Dialog: "Download again", State: "one completed, its file there (keep/replace)",
			Steps: one(search("novel-draft"), ctxRow("novel-draft.epub"), click("[role=menuitem]", "Download again"))},
		{Name: "again-missing", Dialog: "Download again", State: "one completed, its file gone", Fixture: "gone:novel-draft.epub",
			Steps: one(search("novel-draft"), ctxRow("novel-draft.epub"), click("[role=menuitem]", "Download again"))},
		// from the palette: the list's context menu does not open on a row this far down in the demo
		{Name: "again-several", Dialog: "Download again", State: "6 completed (5 names + 1 more), one file gone", Fixture: "gone:podcast-episode-41.mp3",
			Steps: severalSteps(key("k", true), typeIn("[role=dialog] input", "Download again"), click("[role=option]", "Download again"))},
		{Name: "restart-mixed", Dialog: "Restart", State: "a failed and a completed download",
			Steps: one(selectRow("photo-archive-277.7z"), ctrlRow("novel-draft.epub"), font, ctxRow("photo-archive-277.7z"), click("[role=menuitem]", "Restart"))},
		{Name: "restart", Dialog: "Restart", State: "one failed download", Steps: one(ctxRow("photo-archive-277.7z"), click("[role=menuitem]", "Restart"))},
		{Name: "remove", Dialog: "Remove", State: "one download", Steps: one(selectRow("podcast-episode-41.mp3"), key("Delete", false))},
		{Name: "remove-several", Dialog: "Remove", State: "6 downloads (5 names + 1 more)", Steps: severalSteps(key("Delete", false))},
		{Name: "open-program", Dialog: "Open", State: "a completed program (Run this program?)", Steps: one(search("editor-5"), dblRow("editor-5.AppImage"))},
		// Queues
		{Name: "move-queue", Dialog: "Move to queue", State: "2 queues", Steps: one(ctxRow("setup-x64-312.exe"), click("[role=menuitem]", "Move to queue"))},
		{Name: "move-queue-many", Dialog: "Move to queue", State: "14 queues", Fixture: "queues:14", Unbounded: true,
			Steps: one(ctxRow("setup-x64-312.exe"), click("[role=menuitem]", "Move to queue"))},
		{Name: "queues-main", Dialog: "Queues", State: "Main queue, 13 queued", Unbounded: true, Selects: true, Steps: openQueues},
		{Name: "queues-night", Dialog: "Queues", State: "Night: schedule every week, empty", Selects: true, Steps: append(openQueues[:len(openQueues):len(openQueues)], queueRow("Night"))},
		{Name: "queues-night-once", Dialog: "Queues", State: "Night: schedule once",
			Steps: append(openQueues[:len(openQueues):len(openQueues)], queueRow("Night"), click("[role=dialog] label", "Once"))},
		{Name: "queues-new", Dialog: "Queues", State: "New queue", Steps: append(openQueues[:len(openQueues):len(openQueues)], click("[role=dialog] button", "New queue"))},
		{Name: "queues-many-queues", Dialog: "Queues", State: "14 queues in the list (the main queue: 13 queued)", Fixture: "queues:14", Unbounded: true, Panes: true, Steps: openQueues},
		{Name: "queues-night-many", Dialog: "Queues", State: "Night: schedule and 24 queued", Fixture: "night:24", Unbounded: true,
			Steps: append(openQueues[:len(openQueues):len(openQueues)], queueRow("Night"))},
		{Name: "queues-main-many", Dialog: "Queues", State: "Main queue, 37 queued", Fixture: "main:30", Unbounded: true, Steps: openQueues},
		// Settings
		settingsPage("general", "", false),
		settingsPage("downloads", "Downloads", true),
		settingsPage("network", "Network", false),
		settingsPage("integration", "Integration", false),
		settingsPage("video", "Video sites", true),
		settingsPage("appearance", "Appearance", false),
		settingsPage("updates", "Updates", false),
		settingsPage("about", "About", false),
		{Name: "settings-network-full", Dialog: "Settings", State: "network: manual proxy, 3 host rules, 2 site logins", Fixture: "settings:full", Unbounded: true,
			Steps: one(key(",", true), click("[role=tab]", "Network"))},
		{Name: "settings-network-editor", Dialog: "Settings", State: "network: full, the site login editor open", Fixture: "settings:full", Unbounded: true,
			Steps: one(key(",", true), click("[role=tab]", "Network"), click("[role=dialog] button", "Add site login"))},
		{Name: "settings-video-ready", Dialog: "Settings", State: "video sites, yt-dlp and Deno installed", Fixture: "tools:ready",
			Steps: one(key(",", true), click("[role=tab]", "Video sites"))},
		{Name: "settings-about-notices", Dialog: "Settings", State: "about: open-source notices", Unbounded: true,
			Steps: one(key(",", true), click("[role=tab]", "About"), click("[role=dialog] button", "View open-source notices"))},
		// the command palette: every command (a list that scrolls by design)
		{Name: "palette", Dialog: "Palette", State: "every command", Unbounded: true, Steps: one(key("k", true))},
	}
	// The progress window's views (a window of its own: the host sizes it as the page asks).
	out = append(out,
		FixtureState{Name: "pw-status", Dialog: "Progress", State: "collapsed, Status", Progress: 1},
		FixtureState{Name: "pw-limits", Dialog: "Progress", State: "collapsed, Speed limit", Progress: 1, Steps: one(click("[role=tab]", "Speed limit"))},
		FixtureState{Name: "pw-site", Dialog: "Progress", State: "collapsed, a web page video", Progress: 2},
		// expanded: the connections (up to 32) are a list that scrolls in its own region
		FixtureState{Name: "pw-expanded", Dialog: "Progress", State: "expanded (details), 16 connections", Progress: 1, Details: true, Unbounded: true},
		FixtureState{Name: "pw-site-expanded", Dialog: "Progress", State: "expanded, a web page video", Progress: 2, Details: true, Unbounded: true},
		FixtureState{Name: "pw-paused", Dialog: "Progress", State: "paused", Progress: 5},
		FixtureState{Name: "pw-failed", Dialog: "Progress", State: "failed (HTTP 503)", Progress: 8},
		FixtureState{Name: "pw-complete", Dialog: "Progress", State: "complete", Progress: 11},
		FixtureState{Name: "pw-complete-media", Dialog: "Progress", State: "complete, a stream", Progress: 9},
	)
	return out
}

// Fixture is the state named name.
func Fixture(name string) (FixtureState, bool) {
	for _, s := range Fixtures() {
		if s.Name == name {
			return s, true
		}
	}
	return FixtureState{}, false
}

// Options are the demo's options for the state, as backend.ts makes them for the page
// (seed and clock aside): the fixture, a progress view when it has one, and a custom title
// bar for the main window.
func (s FixtureState) Options() Options {
	view := &Startup{OpenWith: true, Hibernate: true, WakeTimers: true, CustomTitleBar: true}
	if s.Progress > 0 {
		view = &Startup{Progress: s.Progress, OpenWith: true, Hibernate: true, WakeTimers: true}
	}
	return Options{Fixture: s.Fixture, View: view}
}
