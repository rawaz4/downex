package settings

import (
	"context"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/about"
	"downex/ui/core/display"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// aboutPage is Settings → About (DESIGN-SYSTEM.md §4.9.1): what this copy of Downex is (the
// window, downexd, the build), yt-dlp and Deno, whether updates can work, Downex's folders, the
// open-source notices, and diagnostics to copy into a problem report. Nothing here changes a
// setting; the IPC endpoint is never shown or copied.
type aboutPage struct {
	page
	root *kit.Element

	// downexd: nil while asking; down when it could not be asked.
	daemon *downexv1.StatusResponse
	asked  bool
	down   bool

	said      *saidPill
	saidTimer *kit.Timer

	copying      bool
	copyError    string
	folderError  string
	notices      *about.Notices
	noticesText  string
	noticesBusy  bool
	noticesError string
	showNotices  bool

	viewNotices *widgets.Button
	noticesHead *views.Text
	stop        func()
}

var (
	fontHeroName  = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypePage, Weight: 650, LineHeight: 1.2, LetterSpacing: -0.015, AutoOptical: true, Tabular: true}
	fontIntroHead = displayFont(tokens.TypeBody, 600, 1.3)
	fontIntro     = uiFont(tokens.TypeCell, 400, 1.5)
	fontToolName  = uiFont(tokens.TypeBody, 600, 1.4)
	fontFolder    = uiFont(tokens.TypeBody, 500, 1.4)
	// fontToolVersion is .tool-version.mono: .mono at --type-cell (.tool-version's
	// font-size wins over .mono's)
	fontToolVersion = func() kit.Font { f := views.FontMono; f.Size = tokens.TypeCell; return f }()
	fontCompTitle   = uiFont(tokens.TypeCell, 500, 1.4)
	fontLicence     = kit.Font{Family: tokens.FontMono, Size: tokens.TypeSmall, Weight: 400, LineHeight: 1.5, Tabular: true, AutoOptical: true}
	fontSaid        = uiFont(tokens.TypeSmall, 500, 1.4)
	fontLinkSmall   = uiFont(tokens.TypeCell, 600, 1)
)

func newAboutPage(d *Dialog) *aboutPage {
	p := &aboutPage{}
	p.init(d)
	p.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}
	p.root = el(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(24), MinWidth: kit.Px(0)})
	p.said = newSaidPill()
	p.Add(p.root, p.said)
	if t := p.tools(); t != nil {
		p.stop = d.app.Watch(&t.Changes, p.build)
		d.app.Go(t.Load)
	}
	p.build()
	p.askStatus()
	return p
}

func (p *aboutPage) dispose() {
	if p.stop != nil {
		p.stop()
	}
	if p.saidTimer != nil {
		p.saidTimer.Stop()
	}
}

func (p *aboutPage) sync() {}

func (p *aboutPage) tools() *state.ToolsStore {
	if p.d.app.Main == nil {
		return nil
	}
	return p.d.app.Main.Tools
}

func (p *aboutPage) askStatus() {
	d := p.d
	if d.app.Daemon == nil {
		p.asked, p.down = true, true
		p.build()
		return
	}
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		s, err := d.app.Daemon.Status(ctx)
		d.app.UI(func() {
			p.asked = true
			if err != nil {
				p.down = true
			} else {
				p.daemon = s
			}
			p.build()
		})
	})
}

// known is downexd's status when it answered.
func (p *aboutPage) known() *downexv1.StatusResponse {
	if p.asked && !p.down {
		return p.daemon
	}
	return nil
}

func (p *aboutPage) build() {
	d := p.d
	if p.d.gone {
		return
	}
	focus := d.app.Window.Focused()
	focusID := ""
	if focus != nil {
		focusID = focus.Semantics.AutomationID
	}
	p.root.RemoveAll()
	if p.showNotices && p.notices != nil {
		p.buildNotices()
	} else {
		p.buildAbout()
	}
	if focusID != "" {
		if e := findByID(p.root, focusID); e != nil {
			d.focus(e.Widget())
		}
	}
}

func findByID(e *kit.Element, id string) *kit.Element {
	if e.Semantics.AutomationID == id {
		return e
	}
	for _, c := range e.Children() {
		if f := findByID(c, id); f != nil {
			return f
		}
	}
	return nil
}

func (p *aboutPage) build2() about.Build {
	b := p.d.app.Build
	return about.Build{Version: b.Version, OS: b.OS, Arch: b.Arch, GoVersion: b.GoVersion, WailsVersion: b.WailsVersion,
		Commit: b.Commit, CommitTime: b.CommitTime, Modified: b.Modified}
}

func (p *aboutPage) buildAbout() {
	d := p.d
	b := d.app.Build
	known := p.known()
	version := b.Version
	if version == "" && known != nil {
		version = known.GetVersion()
	}
	// the window's facts: its version and system (the native window has no WebView2)
	var facts []string
	if b.Version != "" {
		facts = append(facts, b.Version)
	}
	if b.OS != "" {
		facts = append(facts, about.OSLabel(b.OS, b.Arch))
	}
	windowFacts := strings.Join(facts, " · ")
	daemonFacts := "Asking…"
	switch {
	case p.down:
		daemonFacts = "Not reachable"
	case known != nil:
		parts := []string{known.GetVersion()}
		if ts := known.GetStartedAt(); ts != nil && ts.AsTime().UnixMilli() > 0 {
			parts = append(parts, "running since "+display.DayTimeText(float64(ts.AsTime().UnixMilli()), float64(d.app.Clock.Now().UnixMilli())))
		}
		if known.GetPid() > 0 {
			parts = append(parts, "process "+strconv.Itoa(int(known.GetPid())))
		}
		daemonFacts = strings.Join(parts, " · ")
	}
	var built []string
	if b.GoVersion != "" || b.Version != "" {
		built = about.BuildLines(p.build2())
	}
	mismatch := known != nil && about.VersionsDiffer(b.Version, known.GetVersion())
	status := known
	if !p.asked {
		status = &downexv1.StatusResponse{UpdatesSupported: true} // while downexd is asked, the saved address decides
	}
	updates := about.UpdatesState(status, d.base.GetUpdateFeedUrl(), d.base.GetAutoCheckUpdates())

	// the hero
	vt := "Version not known"
	if version != "" {
		vt = "Version " + version
	}
	name := heading("Downex", fontHeroName, 3)
	heroText := stack(kit.Px(1), name, para(vt, fontCell, views.InkMuted))
	diagHint := sHint("Diagnostics are versions, states and settings for a problem report: no addresses, file names, logins or proxy.")
	diagHint.Style.Margin = kit.Edges{Top: kit.Px(3)}
	heroText.Add(diagHint)
	hero := newGrid(tracks(auto(), frac(kit.Px(0), 1), auto()), kit.Px(14), kit.Px(14))
	hero.alignItems = kit.AlignCenter
	hero.Style.Padding = kit.EdgesOf(kit.Px(14), kit.Px(16))
	hero.Style.Border = kit.EdgesAll(kit.Px(1))
	hero.ground, hero.edge, hero.radius = inkSurface2, inkLineSoft, 10
	heroBar := &heroBar{}
	hero.Add(newBrandMark(44), heroText)
	var copyBtn *widgets.Button
	if d.app.Platform != nil {
		copyBtn = button("Copy diagnostics", "copy", widgets.Secondary, true, p.copyDiagnostics)
		copyBtn.SetDisabled(p.copying, false)
		copyBtn.Semantics.Description = diagHint.Text()
		copyBtn.Semantics.AutomationID = "about-copy-diagnostics"
		hero.Add(copyBtn)
	}
	hero.respond = func() {
		if d.paneAtMost(34) {
			hero.cols = tracks(auto(), frac(kit.Px(0), 1))
			if copyBtn != nil {
				hero.setPlace(copyBtn, place{col: 0, span: -1, row: -1, rowSpan: 1, justify: kit.AlignStart})
			}
			return
		}
		hero.cols = tracks(auto(), frac(kit.Px(0), 1), auto())
		hero.places = nil
	}
	hero.Semantics.Role = kit.RoleRegion
	hero.Semantics.Name = "Downex"
	p.root.Add(&heroBox{grid: hero, bar: heroBar})
	if p.copyError != "" {
		p.root.Add(p.note(views.NoteFail, p.copyError))
	}
	if mismatch {
		p.root.Add(p.note(views.NoteWarn, "This window is version "+escapeBidi(b.Version)+", but downexd is version "+escapeBidi(known.GetVersion())+
			". Quit Downex from its icon in the notification area and start it again, so that both are the same version."))
	}

	// This copy
	fact := func(label string, value kit.Widget) *grid {
		g := newGrid(tracks(fixed(kit.Rem(6)), frac(kit.Px(0), 1)), kit.Px(16), kit.Px(4))
		g.alignItems = kit.AlignStart
		g.Style.MinHeight = kit.Px(36)
		if curSkin == skinMac {
			g.Style.MinHeight, _ = rowBox(kit.Px(8)) // the Mac's rows are at least 44 px (§3.20)
		}
		g.Style.Padding = kit.EdgesOf(kit.Px(8), kit.Px(14))
		g.edge, g.rule = inkLineSoft, true
		dt := para(label, fontCell, views.InkMuted)
		dt.Semantics.Role = kit.RoleTerm
		g.Add(dt, value)
		g.respond = func() {
			if d.paneAtMost(34) {
				g.cols = repeatFr(1)
			} else {
				g.cols = tracks(fixed(kit.Rem(6)), frac(kit.Px(0), 1))
			}
		}
		return g
	}
	dd := func(s string, ink views.Ink) *views.Text {
		t := para(s, fontCell, ink)
		t.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
		t.Semantics.Role = kit.RoleDefinition
		return t
	}
	if windowFacts == "" {
		windowFacts = "Not known (an older window)"
	}
	daemonInk := views.InkInk
	if p.down {
		daemonInk = views.InkMuted
	}
	rows := []kit.Widget{fact("Window", dd(windowFacts, views.InkInk)), fact("downexd", dd(daemonFacts, daemonInk))}
	if len(built) > 0 {
		rows = append(rows, fact("Built with", dd(strings.Join(built, " · "), views.InkInk)))
	}
	updInk := views.InkInk
	if !updates.On {
		updInk = views.InkMuted
	}
	settingsLink := widgets.NewButton("Settings", "", widgets.Link, widgets.SizeSm, func() { d.showPage("updates", false) })
	settingsLink.Label().SetFont(fontLinkSmall)
	settingsLink.Semantics.Name = "Update settings"
	updText := para(updates.Text, fontCell, updInk)
	updText.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	with := el(kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignBaseline, Justify: kit.JustifySpaceBetween,
		RowGap: kit.Px(4), ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}, updText, settingsLink)
	with.Semantics.Role = kit.RoleDefinition
	rows = append(rows, fact("Updates", with))
	factsCard := sCard(rows...)
	factsCard.Semantics.Role = kit.RoleList
	copySec := sSection(sHead("This copy"), factsCard)

	// Video tools
	toolsSec := p.toolsSection()

	// Folders
	type folderRow struct {
		kind        host.FolderKind
		label, hint string
		path        string
	}
	var dataDir, logDir, toolsDir string
	if known != nil {
		dataDir, logDir, toolsDir = known.GetDataDir(), known.GetLogDir(), known.GetToolsDir()
	}
	folders := []folderRow{
		{host.FolderData, "Data", "The list of downloads and the settings", dataDir},
		{host.FolderLogs, "Logs", "What downexd did, for problem reports", logDir},
		{host.FolderTools, "Tools", "Downex’s copies of yt-dlp and Deno", toolsDir},
		{host.FolderDownloads, "Downloads", "The default download folder", d.base.GetDefaultDir()},
	}
	var folderWidgets []kit.Widget
	for _, f := range folders {
		f := f
		label := newInline(fontFolder, 1.4, span{text: f.label, font: fontFolder, ink: views.InkInk},
			span{text: " · " + f.hint, font: fontHint, ink: views.InkFaint})
		var path *views.Text
		if f.path != "" {
			path = para(f.path, kit.Font{Family: tokens.FontMono, Size: tokens.TypeSmall, Weight: tokens.MonoWeight, LineHeight: 1.4, LetterSpacing: -0.01, AutoOptical: true}, views.InkInk2)
			path.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
		} else {
			missing := "Not reported by this downexd"
			if !p.asked {
				missing = "Asking downexd…"
			}
			path = para(missing, kit.Font{Family: tokens.FontUI, Size: tokens.TypeSmall, LineHeight: 1.4, Italic: true, AutoOptical: true, Tabular: true}, views.InkFaint)
		}
		open := button("Open", "folder", widgets.Secondary, true, func() { p.openFolder(f.kind, f.label) })
		open.Semantics.Name = "Open the " + f.label + " folder"
		open.SetDisabled(d.app.Platform == nil, false)
		cp := widgets.NewIconButton("copy", 14, "Copy the path of the "+f.label+" folder", true, func() { p.copyPath(f.path, f.label) })
		cp.Tooltip = "Copy the path"
		cp.SetDisabled(d.app.Platform == nil || f.path == "", false)
		actions := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(4)}, open, cp)
		row := newGrid(tracks(frac(kit.Px(0), 1), auto()), kit.Px(12), kit.Px(12), stack(kit.Px(2), label, path), actions)
		row.alignItems = kit.AlignCenter
		row.Style.Padding = kit.EdgesOf(kit.Px(7), kit.Px(14))
		row.edge, row.rule = inkLineSoft, true
		row.Semantics.Role = kit.RoleListItem
		folderWidgets = append(folderWidgets, row)
	}
	folderCard := sCard(folderWidgets...)
	folderCard.Semantics.Role = kit.RoleList
	folderCard.Semantics.Name = "Folders"
	folderSec := sSection(sHead("Folders"), folderCard)
	if p.folderError != "" {
		folderSec.Add(p.note(views.NoteFail, p.folderError))
	}

	// Open-source notices
	label := "View open-source notices"
	p.viewNotices = button(label, "documents", widgets.Secondary, true, func() { p.openNotices() })
	p.viewNotices.Semantics.AutomationID = "about-view-notices"
	p.viewNotices.SetDisabled(d.app.Platform == nil || p.noticesBusy, false)
	videoLink := widgets.NewButton("Video sites", "", widgets.Link, widgets.SizeSm, func() { d.showPage("video", false) })
	videoLink.Label().SetFont(fontLinkSmall)
	noticesSec := sSection(sHead("Open-source notices", sLead("Downex is built with open-source software; its licences are listed here. yt-dlp and Deno are not part of Downex: their licences are on the Video sites page.")),
		sAfter(p.viewNotices, videoLink))
	if p.noticesError != "" {
		noticesSec.Add(p.note(views.NoteFail, p.noticesError))
	}

	col1 := stack(kit.Px(24), copySec, toolsSec)
	col2 := stack(kit.Px(24), folderSec, noticesSec)
	cols := newGrid(repeatFr(1), kit.Px(28), kit.Px(24), col1, col2)
	cols.alignItems = kit.AlignStart
	cols.respond = func() {
		if d.paneW >= d.scale.Rem(49) {
			cols.cols = repeatFr(2)
		} else {
			cols.cols = repeatFr(1)
		}
	}
	p.root.Add(cols)
}

func (p *aboutPage) note(tone views.NoteTone, s string) *views.Note {
	n := views.NewNote(tone, s)
	n.Semantics.Role = kit.RoleAlert
	return n
}

func (p *aboutPage) toolsSection() *kit.Element {
	d := p.d
	t := p.tools()
	title := sTitle("Video tools")
	title.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	titleRow := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifySpaceBetween, RowGap: kit.Px(8), ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}, title)
	head := stack(kit.Px(3), titleRow, sLead("yt-dlp reads video pages, with Deno for YouTube. They are separate programs that Downex downloads when you first use a video page."))
	sec := sSection(head)
	if t == nil {
		sec.Add(sLead("Their state is shown when Downex is connected to downexd."))
		return sec
	}
	if !t.Supported() {
		sec.Add(views.NewNote(views.NoteInfo, "This downexd cannot download videos from web pages."))
		return sec
	}
	list := t.Status().GetTools()
	siteMedia := d.base.GetSiteMedia()
	can := t.Busy() == state.NotBusy && siteMedia && len(list) > 0
	label := "Check for tool updates"
	if t.Busy() == state.Updating {
		label = "Checking…"
	}
	var hint string
	switch {
	case !siteMedia:
		hint = "Video downloads from web pages are off (Video sites)."
	case t.Busy() != state.NotBusy:
		hint = "This can take a few minutes."
	default:
		hint = "Only Downex’s own copies are updated, never ones found on PATH."
	}
	check := button(label, "refresh", widgets.Secondary, true, func() {
		d.app.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), host.ToolsInstallTimeout)
			defer cancel()
			t.Install(ctx, true)
		})
	})
	check.Semantics.AutomationID = "about-check-tools"
	check.Semantics.Description = hint
	check.SetDisabled(!can, false)
	titleRow.Add(check)
	head.Add(sHint(hint))
	var rows []kit.Widget
	for _, ti := range list {
		tone, text := aboutToolState(ti)
		ver := "—"
		if ti.GetVersion() != "" {
			ver = escapeBidi(ti.GetVersion())
		}
		where := ""
		if ti.GetPath() != "" {
			where = "Found on PATH"
			if ti.GetManaged() {
				where = "Downex copy"
			}
		}
		whereT := para(where, fontHint, views.InkFaint)
		g := newGrid(tracks(fixed(kit.Rem(5)), auto(), frac(kit.Px(0), 1), auto()), kit.Px(12), kit.Px(0),
			para(state.ToolName(ti.GetName()), fontToolName, views.InkInk), views.NewLine(ver, fontToolVersion, views.InkInk2), whereT, widgets.NewPill(text, tone))
		g.alignItems = kit.AlignCenter
		g.Style.MinHeight = kit.Px(40)
		g.Style.Padding = kit.EdgesInline(kit.Px(14))
		g.edge, g.rule = inkLineSoft, true
		g.Semantics.Role = kit.RoleListItem
		g.respond = func() {
			if d.paneAtMost(34) {
				g.cols = tracks(fixed(kit.Rem(4)), frac(kit.Px(0), 1), auto())
				whereT.Style.Hidden = true
			} else {
				g.cols = tracks(fixed(kit.Rem(5)), auto(), frac(kit.Px(0), 1), auto())
				whereT.Style.Hidden = false
			}
		}
		rows = append(rows, g)
	}
	card := sCard(rows...)
	card.Semantics.Role = kit.RoleList
	card.Semantics.Name = "Video tools"
	sec.Add(card)
	if e := t.InstallError(); e != "" {
		sec.Add(p.note(views.NoteFail, escapeBidi(e)))
	}
	return sec
}

func aboutToolState(t *downexv1.ToolInfo) (widgets.Tone, string) {
	switch t.GetState() {
	case downexv1.ToolState_TOOL_STATE_READY:
		return widgets.Ok, "Ready"
	case downexv1.ToolState_TOOL_STATE_INSTALLING:
		if t.GetVersion() != "" {
			return widgets.Info, "Updating…"
		}
		return widgets.Info, "Downloading…"
	case downexv1.ToolState_TOOL_STATE_FAILED:
		if t.GetVersion() != "" {
			return widgets.Fail, "Update failed"
		}
		return widgets.Fail, "Not set up"
	case downexv1.ToolState_TOOL_STATE_MISSING:
		return widgets.Done, "Not installed"
	case downexv1.ToolState_TOOL_STATE_DISABLED:
		return widgets.Done, "Off"
	}
	return widgets.Done, "Unknown"
}

// say shows what the page did in its polite live region for 4 s.
func (p *aboutPage) say(s string) {
	p.said.set(s)
	if p.saidTimer != nil {
		p.saidTimer.Stop()
	}
	p.saidTimer = p.d.app.Window.After(4*time.Second, func() { p.said.set("") })
}

func (p *aboutPage) copyDiagnostics() {
	d := p.d
	if d.app.Platform == nil || p.copying {
		return
	}
	p.copying, p.copyError = true, ""
	p.build()
	endpoint := ""
	if k := p.known(); k != nil {
		endpoint = k.GetEndpoint()
	}
	feed := d.base.GetUpdateFeedUrl()
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		txt := d.app.Platform.Diagnostics(ctx)
		err := d.app.Platform.CopyText(ctx, about.ScrubDiagnostics(txt, []string{endpoint, feed}))
		d.app.UI(func() {
			p.copying = false
			if err != nil {
				p.copyError = "Could not copy the diagnostics: " + state.Message(err)
			} else {
				p.say("Diagnostics copied. They name no addresses, files or logins.")
			}
			p.build()
		})
	})
}

func (p *aboutPage) openFolder(kind host.FolderKind, label string) {
	d := p.d
	if d.app.Platform == nil {
		return
	}
	p.folderError = ""
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		err := d.app.Platform.OpenAppFolder(ctx, kind)
		d.app.UI(func() {
			if err == nil {
				return
			}
			if ce := state.ErrorOf(err); ce != nil && ce.Code == connect.CodeFailedPrecondition {
				p.folderError = "The " + label + " folder does not exist yet."
			} else {
				p.folderError = "Could not open the " + label + " folder: " + state.Message(err)
			}
			p.build()
		})
	})
}

func (p *aboutPage) copyPath(path, label string) {
	d := p.d
	if d.app.Platform == nil || path == "" {
		return
	}
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		err := d.app.Platform.CopyText(ctx, path)
		d.app.UI(func() {
			if err != nil {
				p.folderError = "Could not copy: " + state.Message(err)
				p.build()
				return
			}
			p.say("The path of the " + label + " folder was copied.")
		})
	})
}

// openNotices shows the open-source notices (read once), at the pane's top with the keyboard on
// their heading.
func (p *aboutPage) openNotices() {
	d := p.d
	if d.app.Platform == nil || p.noticesBusy {
		return
	}
	p.noticesError = ""
	if p.notices == nil {
		p.noticesBusy = true
		p.build()
		d.app.Go(func() {
			txt := d.app.Platform.ThirdPartyNotices()
			d.app.UI(func() {
				p.noticesBusy = false
				p.noticesText = txt
				n := about.ParseNotices(txt)
				p.notices = &n
				p.showNoticesView()
			})
		})
		return
	}
	p.showNoticesView()
}

func (p *aboutPage) showNoticesView() {
	p.showNotices = true
	p.build()
	p.d.shell.scrollTop()
	if p.noticesHead != nil {
		p.d.focus(p.noticesHead)
	}
}

func (p *aboutPage) backToAbout() {
	p.showNotices = false
	p.build()
	p.d.shell.scrollTop()
	if p.viewNotices != nil {
		p.d.focus(p.viewNotices)
	}
}

func (p *aboutPage) copyNotices() {
	d := p.d
	if d.app.Platform == nil || p.noticesText == "" {
		return
	}
	txt := p.noticesText
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		err := d.app.Platform.CopyText(ctx, txt)
		d.app.UI(func() {
			if err != nil {
				p.noticesError = "Could not copy: " + state.Message(err)
				p.build()
				return
			}
			p.say("The notices were copied.")
		})
	})
}

// buildNotices is the notices view: one component at a time.
func (p *aboutPage) buildNotices() {
	d := p.d
	n := p.notices
	back := widgets.NewButton("About", "chevron-left", widgets.Ghost, widgets.SizeSm, p.backToAbout)
	back.SetIconSize(15)
	back.Semantics.Name = "Back to About"
	back.Style.ColumnGap = kit.Px(2)
	back.Style.Padding.Start = kit.Px(4)
	p.noticesHead = sTitle("Open-source notices")
	p.noticesHead.Focus = kit.FocusClick
	p.noticesHead.Ring = kit.Ring{None: true}
	head := newGrid(tracks(auto(), frac(kit.Px(0), 1), auto()), kit.Px(10), kit.Px(0), back, p.noticesHead)
	head.alignItems = kit.AlignCenter
	if d.app.Platform != nil {
		head.Add(button("Copy all", "copy", widgets.Secondary, true, p.copyNotices))
	}
	sec := sSection(head)
	sec.Semantics.Role = kit.RoleRegion
	sec.Semantics.Name = "Open-source notices"
	if n.Intro != "" {
		intro := el(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(8), MaxWidth: kit.Rem(46), MinWidth: kit.Px(0)})
		for _, b := range about.IntroBlocks(n.Intro) {
			switch b.Kind {
			case about.Heading:
				h := heading(b.Text, fontIntroHead, 4)
				h.Style.Margin = kit.Edges{Top: kit.Px(4)}
				intro.Add(h)
			case about.Item:
				intro.Add(newBullet(b.Text))
			default:
				t := para(b.Text, fontIntro, views.InkInk2)
				t.SetOptions(kit.TextOptions{Wrap: kit.TextWrapWords})
				intro.Add(t)
			}
		}
		sec.Add(intro)
	}
	if k := len(n.Sections); k > 0 {
		word := "components"
		if k == 1 {
			word = "component"
		}
		sec.Add(sHint(strconv.Itoa(k) + " " + word + ": open one to read its licence."))
		var comps []kit.Widget
		for _, s := range n.Sections {
			comps = append(comps, newComponent(s))
		}
		card := sCard(comps...)
		card.Style.Overflow = kit.OverflowHidden
		card.Style.Radius = kit.CornersAll(kit.Px(10))
		sec.Add(card)
	}
	if p.noticesError != "" {
		sec.Add(p.note(views.NoteFail, p.noticesError))
	}
	p.root.Add(sec)
}

// bullet is the intro's .intro-item: a 5 px --accent dot, then the text 14 px in.
type bullet struct {
	kit.Element
	t *views.Text
}

func newBullet(s string) *bullet {
	b := &bullet{t: para(s, fontIntro, views.InkInk2)}
	b.t.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	b.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.Edges{Start: kit.Px(14)}, MinWidth: kit.Px(0)}
	b.Add(b.t)
	return b
}

// Paint is kit.Painter's: the dot at 0.6em.
func (b *bullet) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&b.Element)
	em := p.Scale.Rem(0.78125)
	x := kit.DIP(2)
	if w := b.Window(); w != nil && w.Direction() == kit.DirRTL {
		x = p.Size().W - 7
	}
	p.FillRoundedRect(kit.RoundRect(kit.R(x, 0.6*em, x+5, 0.6*em+5), 2.5), t.Accent)
}

// component is a <details> of the notices: its summary row (a chevron and the component) and,
// when open, its licence.
type component struct {
	*surface
	sum     *summaryRow
	licence *surface
	body    string
}

func newComponent(s about.NoticeSection) *component {
	c := &component{body: s.Body}
	c.sum = newSummaryRow(s.Title, c.toggle)
	c.surface = newSurface(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, nil, inkLineSoft, 0, c.sum)
	c.rule = true
	return c
}

func (c *component) toggle() {
	if c.licence != nil {
		c.Remove(c.licence)
		c.licence = nil
		c.sum.setOpen(false)
		return
	}
	t := para(c.body, fontLicence, views.InkInk2)
	t.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	t.Semantics.Role = kit.RoleDocument
	c.licence = newSurface(kit.Style{Display: kit.DisplayBlock, Border: kit.Edges{Top: kit.Px(1)}, MinWidth: kit.Px(0),
		Padding: kit.Edges{Top: kit.Px(10), End: kit.Px(14), Bottom: kit.Px(14), Start: kit.Px(31)}}, inkSurface2, inkLineSoft, 0, t)
	c.licence.rule, c.licence.ruled = true, true
	c.Add(c.licence)
	c.sum.setOpen(true)
}

// summaryRow is a component's summary: min 34 px, --hover under the pointer, its chevron
// --accent when open.
type summaryRow struct {
	kit.Pressable
	chev *chevron
}

func newSummaryRow(title string, onPress func()) *summaryRow {
	r := &summaryRow{}
	r.OnPress = onPress
	r.Focus = kit.FocusTab
	r.Semantics.Role = kit.RoleButton
	r.Semantics.Name = title
	r.Semantics.Expand = kit.ExpandClosed
	r.Cursor = kit.CursorPointer
	r.Ring = kit.Ring{Offset: kit.Px(-2)}
	r.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MinHeight: kit.Px(34),
		Padding: kit.Edges{Start: kit.Px(10), End: kit.Px(14)}, MinWidth: kit.Px(0)}
	r.chev = newChevron(13, views.InkMuted)
	r.Add(r.chev, views.NewLine(title, fontCompTitle, views.InkInk))
	return r
}

func (r *summaryRow) setOpen(open bool) {
	r.chev.setOpen(open)
	r.chev.ink = views.InkMuted
	r.Semantics.Expand = kit.ExpandClosed
	if open {
		r.chev.ink = inkAccentFill
		r.Semantics.Expand = kit.ExpandOpen
	}
	r.SemanticsChanged()
}

// HoverChanged is kit.HoverHandler's.
func (r *summaryRow) HoverChanged(h bool) {
	r.Pressable.HoverChanged(h)
	r.Invalidate()
}

// Paint is kit.Painter's.
func (r *summaryRow) Paint(p *kit.PaintContext) {
	if r.Hovered() {
		p.FillRect(kit.RectAt(kit.Point{}, p.Size()), views.TokensOf(r.Base()).Hover)
	}
}

// heroBox is the hero card with its 3 px --accent-soft bar at the start
// (linear-gradient(var(--accent-soft), var(--accent-soft)) no-repeat 0 0 / 3px 100%).
type heroBox struct {
	*grid
	bar *heroBar
}

type heroBar struct{}

// Paint is kit.Painter's: the ground, the bar, the border.
func (h *heroBox) Paint(p *kit.PaintContext) {
	t := views.TokensOf(h.Base())
	sz := p.Size()
	r := kit.RoundRect(kit.RectAt(kit.Point{}, sz), 10)
	p.FillRoundedRect(r, t.Surface)
	p.Canvas.PushRoundedClip(kit.RRect{Rect: p.Scale.SnapRect(r.Rect.Add(p.Origin)), Radii: kit.UniformRadii(10)})
	p.FillRect(kit.R(0, 0, 3, sz.H), t.AccentSoft)
	p.Canvas.Pop()
	edge := t.Line
	if t.IsForced() {
		edge = t.Ink
	}
	p.Border(r, 1, edge)
}

// saidPill is the page's polite live region ("Diagnostics copied …"): it takes no room; its
// pill sits at the end, 4 px above the bottom, while it has something to say.
type saidPill struct {
	kit.Element
	pill *surface
	text *views.Text
}

func newSaidPill() *saidPill {
	s := &saidPill{}
	s.Semantics.Role = kit.RoleStatus
	s.Semantics.Live = kit.LivePolite
	s.text = views.NewLine("", fontSaid, views.InkAccent)
	s.pill = newSurface(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), Padding: kit.EdgesOf(kit.Px(5), kit.Px(12)),
		Border: kit.EdgesAll(kit.Px(1))}, inkRaised, inkAccentLine, 999, icon("check", 14, views.InkAccent), s.text)
	s.pill.PassThrough = true
	s.pill.Style.Hidden = true
	s.Add(s.pill)
	return s
}

func (s *saidPill) set(t string) {
	s.text.SetText(t)
	s.pill.SetHidden(t == "")
	s.Semantics.Name = t
	s.SemanticsChanged()
}

// ContentWidths is kit.Layouter's.
func (s *saidPill) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's: no room.
func (s *saidPill) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's.
func (s *saidPill) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if s.pill.Style.Hidden {
		return
	}
	_, w := ctx.IntrinsicWidths(s.pill)
	w = min(w, box.Width())
	h := ctx.HeightFor(s.pill, w, w)
	x := box.Max.X - w
	if ctx.RTL {
		x = box.Min.X
	}
	ctx.Place(s.pill, kit.RectAt(kit.Pt(x, box.Min.Y-4-h), kit.Size{W: w, H: h}), w)
}
