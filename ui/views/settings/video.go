package settings

import (
	"context"
	"math"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// disposer is a page that lets go of what it watches when it is unmounted.
type disposer interface{ dispose() }

// The licences of §4.8: fixed addresses, opened in the system's browser.
var licences = []struct{ label, url string }{
	{"yt-dlp licence (Unlicense)", "https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE"},
	{"Licences of the yt-dlp executables (GPLv3+)", "https://github.com/yt-dlp/yt-dlp/blob/master/THIRD_PARTY_LICENSES.txt"},
	{"Deno licence (MIT)", "https://github.com/denoland/deno/blob/main/LICENSE.md"},
}

// toolStateText is a tool's state as Video sites says it.
func toolStateText(t *downexv1.ToolInfo, siteMedia bool) string {
	switch t.GetState() {
	case downexv1.ToolState_TOOL_STATE_READY:
		return "Ready"
	case downexv1.ToolState_TOOL_STATE_INSTALLING:
		if t.GetVersion() != "" {
			return "Updating…"
		}
		return "Downloading…"
	case downexv1.ToolState_TOOL_STATE_MISSING:
		return "Not installed"
	case downexv1.ToolState_TOOL_STATE_FAILED:
		if t.GetVersion() != "" {
			return "Update failed"
		}
		return "Could not be set up"
	case downexv1.ToolState_TOOL_STATE_DISABLED:
		if siteMedia {
			return "Not available on this system"
		}
		return "Off"
	}
	return "Unknown"
}

// toolTone is the pill of a tool's state.
func toolTone(t *downexv1.ToolInfo) widgets.Tone {
	switch t.GetState() {
	case downexv1.ToolState_TOOL_STATE_READY:
		return widgets.Ok
	case downexv1.ToolState_TOOL_STATE_INSTALLING:
		return widgets.Info
	case downexv1.ToolState_TOOL_STATE_FAILED:
		return widgets.Fail
	}
	return widgets.Done
}

// verifiedText is what Downex checked of the copy in use.
func verifiedText(t *downexv1.ToolInfo) string {
	if !t.GetManaged() {
		if t.GetPath() != "" {
			return "Your own copy: Downex does not check it"
		}
		return ""
	}
	switch t.GetVerified() {
	case "openpgp+sha256":
		return "Signature (OpenPGP) and checksum checked"
	case "sha256+authenticode":
		return "Checksum and publisher signature checked"
	case "sha256":
		return "Checksum checked"
	case "":
		return ""
	}
	return escapeBidi(t.GetVerified())
}

// copyText is where the copy in use is.
func copyText(t *downexv1.ToolInfo) string {
	if t.GetPath() == "" {
		return ""
	}
	if t.GetManaged() {
		return "Downex copy"
	}
	return "Found on PATH: " + escapeBidi(t.GetPath())
}

// videoPage is Settings → Video sites (SITE-MEDIA-SPEC §5.4): whether videos of web pages are
// downloaded with yt-dlp, its updates and release channel, the state of yt-dlp and Deno with
// their install, and the licences. A wide pane has the settings and the licences at the start,
// the tools beside them.
type videoPage struct {
	page
	g         *grid
	siteMedia *widgets.SwitchRow
	autoUp    *widgets.SwitchRow
	channel   *widgets.Select
	chanLabel *views.Text
	tools     *kit.Element
	stop      func()
}

func newVideoPage(d *Dialog) *videoPage {
	p := &videoPage{}
	p.init(d)
	f := &d.video
	var smRow, auRow *surface
	smRow, p.siteMedia = switchRow("Download videos from web pages with yt-dlp", "", f.SiteMedia, func(on bool) {
		f.SiteMedia = on
		p.sync()
		p.buildTools()
		d.changed()
	})
	auRow, p.autoUp = switchRow("Update automatically", "Checks once a day; only Downex’s own copies.", f.ToolsAutoUpdate, func(on bool) {
		f.ToolsAutoUpdate = on
		d.changed()
	})
	p.chanLabel = para("yt-dlp releases", fontLabel, views.InkInk)
	chHint := sHint("Nightly releases follow site changes within a day. Saving another channel installs its latest release.")
	p.channel = widgets.NewSelect([]widgets.SelectOption{{Value: "nightly", Label: "Nightly (recommended by yt-dlp)"}, {Value: "stable", Label: "Stable"}},
		f.ToolsChannel, widgets.SelectOptions{Label: "yt-dlp releases", Size: widgets.SelectSm}, func(v string) {
			f.ToolsChannel = v
			d.changed()
		})
	p.channel.Semantics.Description = chHint.Text()
	ctl := sControl(p.channel)
	ctl.Style.Width = kit.Rem(16.5)
	chRow := sRow()
	chRow.Style.MinHeight = kit.Px(52)
	// .channel-row's row-gap: 4px loses to the dialog's .pane :where(.s-row) gap (equal
	// specificity, later in the cascade): today's rows are 6 px apart (the reference boxes)
	chRow.addAt(p.chanLabel, place{col: 0, span: 1, row: 0, rowSpan: 1})
	chRow.addAt(ctl, place{col: 1, span: 1, row: 0, rowSpan: 1})
	chRow.addAt(chHint, place{col: 0, span: 2, row: 1, rowSpan: 1})
	settings := sSection(sHead("Video sites", sLead("Downex saves the video of a web page (YouTube and the other sites yt-dlp supports) as one MP4 file, in the quality you choose. yt-dlp reads the page, with Deno for YouTube; Downex downloads both the first time you use it.")),
		sCard(smRow, auRow, chRow))

	p.tools = sSection(nil)
	p.buildTools()

	links := stack(kit.Px(8))
	links.Semantics.Role = kit.RoleList
	for _, l := range licences {
		url := l.url
		b := widgets.NewButton(l.label, "", widgets.Link, widgets.SizeMd, func() {
			if d.app.Platform == nil {
				return
			}
			d.app.Go(func() {
				ctx, cancel := d.app.CallContext()
				defer cancel()
				_ = d.app.Platform.OpenExternal(ctx, url) // nothing to do: the address is the link's tooltip
			})
		})
		b.Label().SetFont(linkFont)
		b.Style.ColumnGap = kit.Px(4)
		b.Add(icon("external", 12, views.InkAccent))
		b.Tooltip = url
		li := el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, b)
		b.Style.AlignSelf = kit.AlignStart
		li.Semantics.Role = kit.RoleListItem
		links.Add(li)
	}
	lic := sSection(sHead("Licences", sLead("yt-dlp (Unlicense; the Windows/macOS/Linux executables are GPLv3+) is downloaded from github.com/yt-dlp; Deno (MIT) from github.com/denoland/deno. Downex does not include either.")),
		links, sLead("Whether a site’s terms allow downloading its videos is your responsibility."))

	p.g = newGrid(repeatFr(1), kit.Px(28), kit.Px(24))
	p.g.alignItems = kit.AlignStart
	p.g.Add(settings, p.tools, lic)
	p.g.respond = func() {
		kids := p.g.Children()
		if d.paneW >= d.scale.Rem(50) {
			p.g.cols = repeatFr(2)
			p.g.setPlace(kids[0], place{col: 0, span: 1, row: 0, rowSpan: 1})
			p.g.setPlace(kids[1], place{col: 1, span: 1, row: 0, rowSpan: 2})
			p.g.setPlace(kids[2], place{col: 0, span: 1, row: 1, rowSpan: 1})
			return
		}
		p.g.cols = repeatFr(1)
		p.g.places = nil
	}
	p.Add(p.g)
	if t := p.toolsStore(); t != nil {
		p.stop = d.app.Watch(&t.Changes, func() { p.buildTools(); p.sync() })
		d.app.Go(t.Load)
	}
	p.sync()
	return p
}

var linkFont = uiFont(kit.Rem(0.78125), 600, 1)

func (p *videoPage) dispose() {
	if p.stop != nil {
		p.stop()
	}
}

func (p *videoPage) toolsStore() *state.ToolsStore {
	if p.d.app.Main == nil {
		return nil
	}
	return p.d.app.Main.Tools
}

// canInstall: an install or update can start (the saved settings must have video pages on).
func (p *videoPage) canInstall() bool {
	t := p.toolsStore()
	return t != nil && t.Supported() && t.Busy() == state.NotBusy && p.d.base.GetSiteMedia() && !p.d.loading
}

// buildTools shows yt-dlp and Deno.
func (p *videoPage) buildTools() {
	d := p.d
	p.tools.RemoveAll()
	p.tools.Add(sHead("yt-dlp and Deno"))
	t := p.toolsStore()
	switch {
	case t == nil:
		p.tools.Add(sLead("Their state is shown when Downex is connected to downexd."))
		return
	case !t.Supported():
		p.tools.Add(views.NewNote(views.NoteInfo, "This downexd cannot download videos from web pages. Update Downex to download videos."))
		return
	}
	status := t.Status()
	switch {
	case len(status.GetTools()) > 0:
		list := newGrid(repeatFr(1), kit.Px(10), kit.Px(10))
		list.Semantics.Role = kit.RoleList
		list.respond = func() {
			// repeat(auto-fit, minmax(15rem, 1fr))
			n := int(math.Floor(float64((d.paneW + 10) / (d.scale.Rem(15) + 10))))
			if d.paneW >= d.scale.Rem(50) {
				n = int(math.Floor(float64(((d.paneW-28)/2 + 10) / (d.scale.Rem(15) + 10))))
			}
			n = max(1, min(n, len(status.GetTools())))
			if len(list.cols) != n {
				list.cols = repeatFr(n)
			}
		}
		for _, ti := range status.GetTools() {
			list.Add(p.toolCard(ti))
		}
		p.tools.Add(list)
	case t.LoadError() != "":
		n := views.NewNote(views.NoteFail, "Could not read their state: "+escapeBidi(t.LoadError()))
		n.Semantics.Role = kit.RoleAlert
		p.tools.Add(n)
	default:
		sp := views.NewSpinner()
		sp.Style.Margin = kit.Edges{End: kit.Px(8)}
		r := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, MinWidth: kit.Px(0)}, sp, sLead("Reading their state…"))
		r.Semantics.Role = kit.RoleStatus
		p.tools.Add(r)
	}
	if t.Installing() {
		bar := widgets.NewProgressBar("Downloading yt-dlp and Deno", false)
		bar.Set(t.Fraction(), widgets.BarDownloading)
		prog := stack(kit.Px(6), bar, views.NewHint(state.InstallText(status, math.NaN(), true)))
		prog.Semantics.Live = kit.LivePolite
		p.tools.Add(prog)
	}
	var btn *widgets.Button
	if status.GetReady() {
		label := "Check for updates"
		if t.Busy() == state.Updating {
			label = "Checking for updates…"
		}
		btn = widgets.NewButton(label, "refresh", widgets.Secondary, widgets.SizeMd, func() { p.install(true) })
	} else {
		label := "Install now"
		if t.Busy() == state.Installing {
			label = "Installing…"
		}
		btn = widgets.NewButton(label, "arrow-down", widgets.Primary, widgets.SizeMd, func() { p.install(false) })
	}
	btn.SetDisabled(!p.canInstall(), false)
	var hint string
	switch {
	case !d.base.GetSiteMedia() && d.video.SiteMedia:
		hint = "Save the settings first: video pages are off in the saved settings."
	case !d.base.GetSiteMedia():
		hint = "Turn on video downloads from web pages first."
	case t.Busy() != state.NotBusy:
		hint = "This can take a few minutes; the window can be closed meanwhile."
	default:
		hint = "Only Downex’s own copies are installed or updated, never ones found on PATH."
	}
	h := sHint(hint)
	btn.Semantics.Description = hint
	p.tools.Add(sAfter(btn, h))
	if e := t.InstallError(); e != "" {
		n := views.NewNote(views.NoteFail, escapeBidi(e))
		n.Semantics.Role = kit.RoleAlert
		p.tools.Add(n)
	}
}

// toolCard is a tool's .tool card: its name, version and state, then what Downex knows of it.
func (p *videoPage) toolCard(t *downexv1.ToolInfo) kit.Widget {
	head := el(kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignCenter, RowGap: kit.Px(6), ColumnGap: kit.Px(8), MinWidth: kit.Px(0)},
		para(state.ToolName(t.GetName()), fontLeadTitle, views.InkInk))
	if v := t.GetVersion(); v != "" {
		head.Add(para("version "+escapeBidi(v), fontHint, views.InkMuted))
	}
	pill := widgets.NewPill(toolStateText(t, p.d.video.SiteMedia), toolTone(t))
	pill.Style.Margin = kit.Edges{Start: kit.Auto}
	head.Add(pill)
	facts := newGrid(tracks(auto(), frac(kit.Px(0), 1)), kit.Px(12), kit.Px(3))
	facts.Semantics.Role = kit.RoleList
	fact := func(label, value string, mono bool, ink views.Ink) {
		dt := para(label, fontHint, views.InkFaint)
		dt.Semantics.Role = kit.RoleTerm
		f := fontHint
		if mono {
			f = fontMono
		}
		dd := para(value, f, ink)
		dd.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
		dd.Semantics.Role = kit.RoleDefinition
		facts.Add(dt, dd)
	}
	if c := copyText(t); c != "" {
		fact("Copy", c, true, views.InkInk2)
	}
	if v := verifiedText(t); v != "" {
		fact("Checked", v, false, views.InkInk2)
	}
	last := "Never"
	if ts := t.GetCheckedAt(); ts != nil && ts.AsTime().UnixMilli() > 0 {
		last = format.DateTime(float64(ts.AsTime().UnixMilli()))
	}
	if l := t.GetLatestVersion(); l != "" && l != t.GetVersion() {
		last += " · latest " + escapeBidi(l)
	}
	fact("Last check", last, false, views.InkInk2)
	if e := t.GetError(); e != "" {
		fact("Problem", escapeBidi(e), false, views.InkDanger)
	}
	c := newSurface(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(8), Padding: kit.EdgesOf(kit.Px(12), kit.Px(14)), Border: kit.EdgesAll(kit.Px(1)), MinWidth: kit.Px(0)},
		inkSurface2, inkLineSoft, 10, head, facts)
	c.Semantics.Role = kit.RoleListItem
	return c
}

// install installs or updates the tools.
func (p *videoPage) install(update bool) {
	t := p.toolsStore()
	if t == nil || !p.canInstall() {
		return
	}
	p.d.app.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), host.ToolsInstallTimeout)
		defer cancel()
		t.Install(ctx, update)
	})
}

func (p *videoPage) sync() {
	d := p.d
	f := &d.video
	dis := d.loading
	p.siteMedia.SetDisabled(dis)
	p.autoUp.SetDisabled(dis || !f.SiteMedia)
	if p.siteMedia.Switch.On != f.SiteMedia {
		p.siteMedia.Switch.SetOn(f.SiteMedia)
	}
	if p.autoUp.Switch.On != f.ToolsAutoUpdate {
		p.autoUp.Switch.SetOn(f.ToolsAutoUpdate)
	}
	p.channel.SetDisabled(dis || !f.SiteMedia)
	p.channel.SetValue(f.ToolsChannel)
	ink := views.InkInk
	if dis || !f.SiteMedia {
		ink = views.InkFaint
	}
	p.chanLabel.Ink = ink
	p.chanLabel.Invalidate()
}
