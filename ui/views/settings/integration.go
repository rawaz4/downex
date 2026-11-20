package settings

import (
	"errors"
	"time"

	"connectrpc.com/connect"

	"downex/ui/core/text"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

const argsHint = "[File] is replaced by the downloaded file; no shell runs it."

// integrationPage is Settings → Integration: the browser (whether its downloads wait for a
// confirmation, where the extension that came with Downex is, and registering Downex with the
// browsers again), the clipboard, and the safety of downloaded files (the file check,
// marking, virus scan). Its sections go in two balanced columns.
type integrationPage struct {
	page
	cols                *columns
	confirm, clip, mark *widgets.SwitchRow
	check, scan         *widgets.SwitchRow
	command, args       *field
	browse              *widgets.Button

	// register runs the relay's "register" again; registering while it runs.
	register    *widgets.Button
	registering bool
	// said is what the extension's steps last did (a polite status beside the button, gone
	// after 4 s); fail is why one failed (an alert under it, until the next try).
	said      *saidLine
	saidTimer *kit.Timer
	fail      *kit.Element
	// folderNote is why the extension folders cannot be used as they are ("": they can)
	folderNote string
}

// The Safety card's first row: the file check (docs/DESKTOP-SPEC.md §9.9).
const (
	checkFilesLabel = "Warn when a file is not what its name says"
	checkFilesHint  = "Downex compares the start of each downloaded file, and the list of files inside archives, with its name, and asks before opening one that pretends to be another type. It never changes or deletes files."
)

func (p *integrationPage) markHint() string {
	switch p.d.platform() {
	case "windows":
		return "Windows then warns before opening programs from the internet (Mark of the Web)."
	case "mac":
		return "macOS then checks downloaded apps before they first open (quarantine)."
	}
	return "The file records where it came from, so the system can warn before opening it."
}

// extensionDir is where the unpacked extension of kind is inside an installation: what the
// page shows when the host cannot say where this one is.
func (p *integrationPage) extensionDir(kind host.FolderKind) string {
	target := "chrome-mv3"
	if kind == host.FolderExtensionFirefox {
		target = "firefox-mv3"
	}
	switch p.d.platform() {
	case "windows":
		return `extension\` + target
	case "mac":
		return "Downex.app/Contents/Resources/extension/" + target
	}
	return "extension/" + target
}

// extensionFolder is the absolute folder of the extension of kind in this installation, and
// whether the host knows it.
func (p *integrationPage) extensionFolder(kind host.FolderKind) (string, bool) {
	if pl := p.d.app.Platform; pl != nil {
		dir, err := pl.ExtensionFolder(kind)
		if err == nil && dir != "" {
			return dir, true
		}
		// macOS runs a temporary copy of Downex.app (App Translocation): its folder goes away
		// when Downex quits, so say why the buttons are off and what to do
		if errors.Is(err, host.ErrTranslocated) {
			p.folderNote = host.ErrTranslocated.Error() + "."
		}
	}
	return p.extensionDir(kind), false
}

var (
	fontSteps = uiFont(tokens.TypeCell, 400, 1.5)
	fontStepN = uiFont(tokens.TypeMeta, 600, 1)
	// fontExtPath is an extension folder's path: About's folder paths.
	fontExtPath = kit.Font{Family: tokens.FontMono, Size: tokens.TypeSmall, Weight: tokens.MonoWeight, LineHeight: 1.4, LetterSpacing: -0.01, AutoOptical: true}
)

func newIntegrationPage(d *Dialog) *integrationPage {
	p := &integrationPage{}
	p.init(d)
	t := &d.toggles
	f := &d.integration
	var confirmRow, clipRow, checkRow, markRow, scanRow *surface
	confirmRow, p.confirm = switchRow("Ask before saving downloads from the browser",
		"Each download the extension hands over waits until you confirm its folder and name.", t.confirmBrowserDownloads,
		func(on bool) { t.confirmBrowserDownloads = on; d.changed() })

	tileBox := newSurface(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		Width: kit.Px(32), Height: kit.Px(32)}, inkAccentSoft, nil, 8, icon("plug", 18, views.InkAccent))
	tileBox.Semantics.Hidden = true
	words := sText("The Downex browser extension", sHint("Downloads you start in Chrome, Edge, Brave, Chromium or Firefox arrive here. Hold Alt while clicking a link to keep one in the browser."))
	words.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	extHead := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}, tileBox, words)
	plain := func(s string) span { return span{text: s, font: fontSteps, ink: views.InkInk2} }
	code := func(s string) span { return span{text: s, font: fontMono, ink: views.InkInk, code: true} }
	// step is a numbered step: its words, then what it shows below them (a folder, a button)
	step := func(n string, below []kit.Widget, spans ...span) kit.Widget {
		num := newSurface(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
			Width: kit.Px(18), Height: kit.Px(18), Margin: kit.Edges{Top: kit.Px(1)}, Border: kit.EdgesAll(kit.Px(1))}, nil, inkLineStrong, 9,
			para(n, fontStepN, views.InkMuted))
		num.Semantics.Hidden = true
		var body kit.Widget = newInline(fontSteps, 1.5, spans...)
		if len(below) > 0 {
			body = stack(kit.Px(8), append([]kit.Widget{body}, below...)...)
		}
		body.Base().Style.Flex = kit.FlexOf(1, 1, kit.Length{})
		body.Base().Style.MinWidth = kit.Px(0)
		li := el(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), MinWidth: kit.Px(0), AlignItems: kit.AlignStart}, num, body)
		li.Semantics.Role = kit.RoleListItem
		return li
	}
	p.said = newSaidLine()
	p.fail = el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Hidden: true})
	p.register = button("Register with browsers again", "refresh", widgets.Secondary, true, p.registerBrowsers)
	p.register.Semantics.AutomationID = "integration-register"
	p.register.SetDisabled(d.app.Platform == nil, false)
	chromeRow := p.folderRow(host.FolderExtensionChrome, "Chrome and Edge")
	step1 := []kit.Widget{chromeRow}
	if p.folderNote != "" {
		step1 = append(step1, views.NewNote(views.NoteInfo, escapeBidi(p.folderNote)))
	}
	steps := el(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(10), MinWidth: kit.Px(0), Padding: kit.Edges{Start: kit.Px(44)}},
		step("1", step1,
			plain("Chrome, Edge, Brave or Chromium: open "), code("chrome://extensions"), plain(" (Edge: "), code("edge://extensions"),
			plain(", Brave: "), code("brave://extensions"),
			plain("), turn on Developer mode and keep it on, choose Load unpacked and pick this folder. Reload it there after updating Downex.")),
		step("2", []kit.Widget{p.folderRow(host.FolderExtensionFirefox, "Firefox")},
			plain("Firefox: open "), code("about:debugging"),
			plain(", choose This Firefox, then Load Temporary Add-on… and pick the "),
			code("manifest.json"), plain(" file in this folder; Firefox removes it when it closes.")),
		step("3", []kit.Widget{sAfter(p.register, p.said), p.fail},
			plain("It reaches Downex through the relay "), code("downex-nmhost"),
			plain(", which the installer registers with your browsers. If the extension says Downex is not set up, register it again.")),
	)
	steps.Semantics.Role = kit.RoleList
	steps.Semantics.Name = "The Downex browser extension"
	browser := sCard(confirmRow, sStack(extHead, steps))

	clipRow, p.clip = switchRow("Offer download links that I copy",
		"When you copy the address of a file (a video, an archive, a program…), Downex asks whether to download it. Text that other apps mark as private, such as passwords, is ignored.",
		f.ClipboardMonitor, func(on bool) { f.ClipboardMonitor = on; d.changed() })

	checkRow, p.check = switchRow(checkFilesLabel, checkFilesHint, f.CheckDownloadedFiles,
		func(on bool) { f.CheckDownloadedFiles = on; d.changed() })
	markRow, p.mark = switchRow("Mark downloaded files as coming from the internet", p.markHint(), f.MarkDownloadedFiles,
		func(on bool) { f.MarkDownloadedFiles = on; d.changed() })
	scanRow, p.scan = switchRow("Scan each finished download with a virus scanner",
		"If the scanner reports a problem, Downex tells you and never opens the file by itself.", f.AVScanEnabled,
		func(on bool) { f.AVScanEnabled = on; d.changed() })

	cmdIn := input(f.AVScanCommand, widgets.InputOptions{Name: "Scanner program", Placeholder: "The scanner's command-line program", Mono: true, Disabled: d.loading}, func(s string) {
		f.AVScanCommand = s
		d.changed()
	})
	cmdIn.Style.Flex = kit.Flex1
	p.browse = button("Browse…", "", widgets.Secondary, false, func() {
		if d.app.Platform == nil {
			return
		}
		initial := text.TrimJS(f.AVScanCommand)
		d.app.Go(func() {
			ctx, cancel := d.app.CallContext()
			defer cancel()
			picked, err := d.app.Platform.PickFile(ctx, "Choose the virus scanner", initial)
			if err != nil || picked == "" {
				return
			}
			d.app.UI(func() {
				if d.gone {
					return
				}
				f.AVScanCommand = picked
				setValue(cmdIn, picked)
				d.changed()
			})
		})
	})
	withButton := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}, cmdIn, p.browse)
	p.command = newField("Scanner program", withButton, cmdIn, nil)
	argsIn := input(f.AVScanArgs, widgets.InputOptions{Name: "Arguments", Description: argsHint, Placeholder: "[File]", Mono: true, Disabled: d.loading}, func(s string) {
		f.AVScanArgs = s
		d.changed()
	})
	argsErr := newErrorText()
	p.args = &field{input: argsIn, err: argsErr}
	p.args.Field = views.NewField("", nil, views.NewLabelRow("Arguments", views.NewHint(argsHint)), argsIn, argsErr)

	p.cols = newColumns(
		sSection(sHead("Browser"), browser),
		sSection(sHead("Clipboard"), sCard(clipRow)),
		sSection(sHead("Safety"), sCard(checkRow, markRow, scanRow, sStack(p.command, p.args))),
	)
	p.Style.Display = kit.DisplayBlock
	p.Style.RowGap = kit.Length{}
	p.Add(p.cols)
	p.sync()
	return p
}

// firstInvalid is the first field with an error.
func (p *integrationPage) firstInvalid() kit.Widget {
	for _, f := range []*field{p.command, p.args} {
		if f.input.InputOptions().Invalid {
			return f.input
		}
	}
	return nil
}

func (p *integrationPage) sync() {
	d := p.d
	errs := d.errors()
	dis := d.loading
	rows := []*widgets.SwitchRow{p.confirm, p.clip, p.check, p.mark, p.scan}
	for _, s := range rows {
		s.SetDisabled(dis)
	}
	on := []bool{d.toggles.confirmBrowserDownloads, d.integration.ClipboardMonitor, d.integration.CheckDownloadedFiles,
		d.integration.MarkDownloadedFiles, d.integration.AVScanEnabled}
	for i, s := range rows {
		if s.Switch.On != on[i] {
			s.Switch.SetOn(on[i])
		}
	}
	setValue(p.command.input, d.integration.AVScanCommand)
	setError(p.command.input, p.command.err, "", errs.AVScanCommand)
	setError(p.args.input, p.args.err, argsHint, errs.AVScanArgs)
	p.command.input.SetDisabled(dis)
	p.args.input.SetDisabled(dis)
	p.browse.SetDisabled(dis, false)
}

// folderRow is an extension folder in step 1 or 2: its absolute path, Copy path and Open
// folder (both unavailable when the host cannot say where this installation is).
func (p *integrationPage) folderRow(kind host.FolderKind, browsers string) kit.Widget {
	d := p.d
	dir, known := p.extensionFolder(kind)
	path := para(escapeBidi(dir), fontExtPath, views.InkInk)
	path.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	path.Semantics.Name = "The extension folder for " + browsers + ": " + dir
	box := newSurface(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Padding: kit.EdgesOf(kit.Px(6), kit.Px(10)),
		Border: kit.EdgesAll(kit.Px(1))}, inkSurface, inkLineSoft, 6, path)
	cp := button("Copy path", "copy", widgets.Secondary, true, func() { p.copyPath(dir) })
	cp.Semantics.Name = "Copy the path of the extension folder for " + browsers
	cp.Semantics.AutomationID = "integration-copy-" + string(kind)
	cp.SetDisabled(!known, false)
	open := button("Open folder", "folder", widgets.Secondary, true, func() { p.openFolder(kind) })
	open.Semantics.Name = "Open the extension folder for " + browsers
	open.Semantics.AutomationID = "integration-open-" + string(kind)
	open.SetDisabled(!known, false)
	actions := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6)}, cp, open)
	g := newGrid(tracks(frac(kit.Px(0), 1), auto()), kit.Px(8), kit.Px(8), box, actions)
	g.alignItems = kit.AlignCenter
	g.respond = func() {
		// a narrow pane puts the buttons under the path
		if d.paneAtMost(40) {
			g.cols = repeatFr(1)
			g.setPlace(actions, place{col: 0, span: 1, row: -1, rowSpan: 1, justify: kit.AlignStart})
			return
		}
		g.cols = tracks(frac(kit.Px(0), 1), auto())
		g.places = nil
	}
	return g
}

func (p *integrationPage) dispose() {
	if p.saidTimer != nil {
		p.saidTimer.Stop()
	}
}

// say shows what a step did for 4 s (and takes away the last failure).
func (p *integrationPage) say(s string) {
	p.showFail("")
	p.said.set(s)
	if p.saidTimer != nil {
		p.saidTimer.Stop()
	}
	p.saidTimer = p.d.app.Window.After(4*time.Second, func() { p.said.set("") })
}

// showFail shows why a step failed ("": nothing failed).
func (p *integrationPage) showFail(s string) {
	p.fail.RemoveAll()
	p.fail.SetHidden(s == "")
	if s == "" {
		return
	}
	p.said.set("")
	n := views.NewNote(views.NoteFail, s)
	n.Semantics.Role = kit.RoleAlert
	p.fail.Add(n)
}

// failText is what failed and the host's reason.
func failText(what string, err error) string {
	if m := state.Message(err); m != "" {
		return what + ": " + escapeBidi(m)
	}
	return what + "."
}

// registerBrowsers runs the relay's "register" off the UI thread and says how it went.
func (p *integrationPage) registerBrowsers() {
	d := p.d
	if d.app.Platform == nil || p.registering {
		return
	}
	p.registering = true
	p.register.SetText("Registering…")
	p.register.SetDisabled(true, true)
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		err := d.app.Platform.RegisterBrowsers(ctx)
		d.app.UI(func() {
			if d.gone {
				return
			}
			p.registering = false
			p.register.SetText("Register with browsers again")
			p.register.SetDisabled(false, false)
			if err != nil {
				p.showFail(failText("Could not register Downex with the browsers", err))
				return
			}
			p.say("Downex is registered with your browsers again.")
		})
	})
}

// copyPath puts an extension folder's path on the clipboard.
func (p *integrationPage) copyPath(dir string) {
	d := p.d
	if d.app.Platform == nil || dir == "" {
		return
	}
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		err := d.app.Platform.CopyText(ctx, dir)
		d.app.UI(func() {
			if d.gone {
				return
			}
			if err != nil {
				p.showFail(failText("Could not copy the path", err))
				return
			}
			p.say("The path was copied.")
		})
	})
}

// openFolder shows an extension folder in the file manager.
func (p *integrationPage) openFolder(kind host.FolderKind) {
	d := p.d
	if d.app.Platform == nil {
		return
	}
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		err := d.app.Platform.OpenAppFolder(ctx, kind)
		d.app.UI(func() {
			if d.gone || err == nil {
				return
			}
			if ce := state.ErrorOf(err); ce != nil && ce.Code == connect.CodeFailedPrecondition {
				p.showFail("The extension folder is not there. Install Downex again to put it back.")
				return
			}
			p.showFail(failText("Could not open the extension folder", err))
		})
	})
}

// saidLine is the extension steps' polite live region: a check and what was done, in
// --accent-text, while it has something to say. The region itself stays (an empty one
// takes no room), so that a screen reader hears each new message.
type saidLine struct {
	kit.Element
	mark *widgets.Icon
	text *views.Text
}

func newSaidLine() *saidLine {
	s := &saidLine{}
	s.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MinWidth: kit.Px(0)}
	s.Semantics.Role = kit.RoleStatus
	s.Semantics.Live = kit.LivePolite
	s.mark = icon("check", 14, views.InkAccent)
	s.text = para("", fontSaid, views.InkAccent)
	s.text.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	s.mark.Style.Hidden, s.text.Style.Hidden = true, true
	s.Add(s.mark, s.text)
	return s
}

func (s *saidLine) set(t string) {
	s.text.SetText(t)
	s.mark.SetHidden(t == "")
	s.text.SetHidden(t == "")
	s.Semantics.Name = t
	s.SemanticsChanged()
}
