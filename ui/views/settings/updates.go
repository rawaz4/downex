package settings

import (
	"context"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/text"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

const urlHint = "An https address of a signed update description. Empty = the address Downex comes with, if it has one."

// updateBusy is what the Updates page waits for.
type updateBusy uint8

const (
	notBusy updateBusy = iota
	busyCheck
	busyInstall
)

// updatesPage is Settings → Updates: checking automatically, Check now / Install, and under a
// collapsed Advanced disclosure (with a summary of what it hides) the address of the signed
// update description; the disclosure opens by itself when that address is wrong.
type updatesPage struct {
	page
	auto       *widgets.SwitchRow
	checkHint  *views.Text
	checkBtn   *widgets.Button
	spinner    *views.Spinner
	result     *kit.Element
	disclosure *disclosure
	advCard    *surface
	urlField   *field

	info         *downexv1.UpdateInfo
	busy         updateBusy
	failure      string
	unpublished  bool // the failure is that no update information is published yet (not an error)
	confirming   bool
	installed    *downexv1.UpdateInfo
	advancedOpen bool
	installBtn   *widgets.Button
	installNow   *widgets.Button
}

func newUpdatesPage(d *Dialog) *updatesPage {
	p := &updatesPage{}
	p.init(d)
	f := &d.updates
	var autoRow *surface
	autoRow, p.auto = switchRow("Check for updates automatically", "Every day; Downex tells you when one is available.", f.AutoCheckUpdates, func(on bool) {
		f.AutoCheckUpdates = on
		d.changed()
	})
	p.checkHint = sHint("")
	p.checkBtn = widgets.NewButton("Check now", "refresh", widgets.Secondary, widgets.SizeMd, p.check)
	checkRow := sRow(sText("Check for an update now", p.checkHint), sControl(p.checkBtn))
	p.result = stack(kit.Px(10))
	p.result.Semantics.Live = kit.LivePolite
	sec := sSection(sHead("Updates", sLead("Updates are installed only when their signature is valid.")), sCard(autoRow, checkRow), p.result)

	urlIn := input(f.UpdateFeedURL, widgets.InputOptions{Name: "Update address", Description: urlHint, Placeholder: "https://downloads.example.com/downex/update.json", Mono: true, Disabled: d.loading}, func(s string) {
		f.UpdateFeedURL = s
		p.sync()
		d.changed()
	})
	p.urlField = newField("Update address", urlIn, urlIn, views.NewHint(urlHint))
	p.advCard = sCard(sStack(p.urlField))
	p.disclosure = newDisclosure("Advanced", func() {
		p.advancedOpen = !p.advancedShown()
		p.sync()
	})
	p.disclosure.Semantics.Controls = p.advCard
	adv := sSection(nil, p.disclosure, p.advCard)
	p.Add(sec, adv)
	p.sync()
	return p
}

func (p *updatesPage) url() string   { return text.TrimJS(p.d.updates.UpdateFeedURL) }
func (p *updatesPage) saved() string { return p.d.base.GetUpdateFeedUrl() }
func (p *updatesPage) unsaved() bool { return p.url() != p.saved() }
func (p *updatesPage) canCheck() bool {
	return p.saved() != "" && !p.unsaved() && !p.d.loading && p.busy == notBusy
}
func (p *updatesPage) advancedShown() bool {
	return p.advancedOpen || p.d.errors().UpdateFeedURL != ""
}

// hostOf is an address's host, else the address.
func hostOf(address string) string {
	u, err := text.ParseURL(address)
	if err != nil || u.Host() == "" {
		return address
	}
	return u.Host()
}

func msOfTS(t interface {
	GetSeconds() int64
	GetNanos() int32
}) float64 {
	return float64(t.GetSeconds())*1000 + float64(t.GetNanos())/1e6
}

// check is Check now.
func (p *updatesPage) check() {
	d := p.d
	if !p.canCheck() {
		return
	}
	p.busy, p.failure, p.unpublished, p.confirming = busyCheck, "", false, false
	p.sync()
	d.app.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), host.CheckTimeout)
		defer cancel()
		resp, err := d.app.Daemon.CheckForUpdate(ctx)
		d.app.UI(func() {
			p.busy = notBusy
			if err != nil {
				// downexd says "No update information is published yet" (not_found) for a feed
				// that has no update description yet: a fact, not a failure.
				p.failure, p.info = state.Message(err), nil
				p.unpublished = state.ErrorOf(err).Code == connect.CodeNotFound
			} else {
				p.info = resp.GetInfo()
			}
			p.sync()
		})
	})
}

// askInstall asks before installing, with the keyboard on Install and restart.
func (p *updatesPage) askInstall() {
	p.confirming = true
	p.sync()
	if p.installNow != nil {
		p.d.focus(p.installNow)
	}
}

// install installs the update; the main window's update store is told when it started (the
// update pop-up then stays away, and the macOS window starts again from the new bundle).
func (p *updatesPage) install() {
	d := p.d
	if p.busy != notBusy {
		return
	}
	p.busy, p.failure, p.unpublished = busyInstall, "", false
	p.sync()
	d.app.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), host.InstallTimeout)
		defer cancel()
		resp, err := d.app.Daemon.InstallUpdate(ctx)
		if m := d.app.Main; err == nil && m != nil && m.Update != nil {
			m.Update.MarkInstalled(resp.GetInfo())
		}
		d.app.UI(func() {
			p.busy = notBusy
			if err != nil {
				p.failure = state.Message(err)
				p.unpublished = state.ErrorOf(err).Code == connect.CodeNotFound
			} else {
				p.installed, p.confirming = resp.GetInfo(), false
			}
			p.sync()
		})
	})
}

func (p *updatesPage) cancelInstall() {
	p.confirming = false
	p.sync()
	if p.installBtn != nil {
		p.d.focus(p.installBtn)
	}
}

func bold(s string) span {
	return span{text: s, font: uiFont(kit.Rem(0.8125), 700, 1.4), ink: views.InkInk}
}

// buildResult shows what the check or the install found.
func (p *updatesPage) buildResult() {
	d := p.d
	p.result.RemoveAll()
	p.installBtn, p.installNow = nil, nil
	switch {
	case p.installed != nil:
		p.result.Add(views.NewNote(views.NoteInfo, "Downex is installing version "+escapeBidi(p.installed.GetLatestVersion())+" and closes for it. It starts again when the installation is done."))
	case p.info != nil && p.info.GetAvailable():
		info := p.info
		published := ""
		if ts := info.GetPublishedAt(); ts != nil && msOfTS(ts) > 0 {
			published = ", published " + format.DateTime(msOfTS(ts))
		}
		plain := func(s string) span { return span{text: s, font: fontBody, ink: views.InkInk} }
		headText := newInline(fontBody, 1.4, plain("Version "), bold(escapeBidi(info.GetLatestVersion())),
			plain(" is available (you have "+escapeBidi(info.GetCurrentVersion())+")"+published+"."))
		headText.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		pill := widgets.NewPill("Update", widgets.Ok)
		pill.Style.Flex = kit.FlexNone
		head := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}, pill, headText)
		found := newSurface(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(10), Padding: kit.EdgesOf(kit.Px(12), kit.Px(14)), Border: kit.EdgesAll(kit.Px(1)), MinWidth: kit.Px(0)},
			inkAccentSoft, inkAccentLine, 10, head)
		if n := info.GetNotes(); n != "" {
			notes := para(escapeBidi(n), fontCell, views.InkInk2)
			notes.Style.MaxHeight = kit.Rem(8)
			notes.Style.Overflow = kit.OverflowHidden
			found.Add(notes)
		}
		buttons := el(kit.Style{Display: kit.DisplayFlex, Justify: kit.JustifyEnd, ColumnGap: kit.Px(8)})
		if p.confirming {
			label := "Install and restart"
			if p.busy == busyInstall {
				label = "Downloading the update…"
			}
			notNow := widgets.NewButton("Not now", "", widgets.Secondary, widgets.SizeMd, p.cancelInstall)
			p.installNow = widgets.NewButton(label, "", widgets.Primary, widgets.SizeMd, p.install)
			notNow.SetDisabled(p.busy != notBusy, false)
			p.installNow.SetDisabled(p.busy != notBusy, false)
			buttons.Add(notNow, p.installNow)
			q := "Install version " + escapeBidi(info.GetLatestVersion()) + " now? Running downloads are paused, and Downex closes while the update is installed."
			confirm := newSurface(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(10), Padding: kit.EdgesOf(kit.Px(10), kit.Px(12)), Border: kit.EdgesAll(kit.Px(1)), MinWidth: kit.Px(0)},
				inkWarnSoft, inkWarnLine, 8, para(q, fontCell, views.InkInk), buttons)
			confirm.Semantics.Role = kit.RoleGroup
			confirm.Semantics.Name = q
			found.Add(confirm)
		} else {
			p.installBtn = widgets.NewButton("Install update…", "", widgets.Primary, widgets.SizeMd, p.askInstall)
			p.installBtn.SetDisabled(d.loading, false)
			buttons.Add(p.installBtn)
			found.Add(buttons)
		}
		p.result.Add(found)
	case p.info != nil:
		checked := ""
		if ts := p.info.GetCheckedAt(); ts != nil && msOfTS(ts) > 0 {
			checked = ", checked " + format.DateTime(msOfTS(ts))
		}
		ic := icon("check", 14, inkAccentFill)
		p.result.Add(el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}, ic,
			para("Downex is up to date (version "+escapeBidi(p.info.GetCurrentVersion())+checked+").", fontCell, views.InkInk2)))
	}
	switch {
	case p.failure != "" && p.unpublished:
		p.result.Add(views.NewNote(views.NoteInfo, escapeBidi(p.failure)))
	case p.failure != "":
		n := views.NewNote(views.NoteFail, escapeBidi(p.failure))
		n.Semantics.Role = kit.RoleAlert
		p.result.Add(n)
	}
	// .result.empty stays in the section's grid (its 0 px row keeps the 12 px gap before it:
	// the -12 px margin cannot make a track smaller than nothing)
}

// firstInvalid is the update address while it is wrong.
func (p *updatesPage) firstInvalid() kit.Widget {
	if p.urlField.input.InputOptions().Invalid {
		return p.urlField.input
	}
	return nil
}

func (p *updatesPage) sync() {
	d := p.d
	errs := d.errors()
	dis := d.loading
	url := p.url()
	p.auto.SetDisabled(dis || url == "")
	if p.auto.Switch.On != d.updates.AutoCheckUpdates {
		p.auto.Switch.SetOn(d.updates.AutoCheckUpdates)
	}
	hint := "Uses the saved address."
	switch {
	case p.saved() == "" && url == "":
		hint = "Set an update address under Advanced first."
	case p.unsaved():
		hint = "Save the settings first: checking uses the saved address."
	}
	p.checkHint.SetText(hint)
	p.checkBtn.Semantics.Description = hint
	label := "Check now"
	if p.busy == busyCheck {
		label = "Checking…"
	}
	p.checkBtn.SetText(label)
	p.checkBtn.SetDisabled(!p.canCheck(), false)
	p.buildResult()
	shown := p.advancedShown()
	summary := "No update address: updates are off"
	if url != "" {
		summary = "Update address: " + hostOf(url)
	}
	p.disclosure.set(shown, summary)
	p.advCard.SetHidden(!shown)
	setError(p.urlField.input, p.urlField.err, urlHint, errs.UpdateFeedURL)
	p.urlField.input.SetDisabled(dis)
}

// disclosure is Updates' .disclosure: a full-width button with a chevron (turned a quarter when
// open), the label (650 var(--type-lead) display) and, while closed, a summary of what it hides.
type disclosure struct {
	kit.Pressable
	chev    *chevron
	label   *views.Text
	summary *views.Text
	open    bool
}

func newDisclosure(label string, onPress func()) *disclosure {
	b := &disclosure{}
	b.OnPress = onPress
	b.Focus = kit.FocusTab
	b.Semantics.Role = kit.RoleButton
	b.Cursor = kit.CursorPointer
	b.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), Width: kit.Pct(100), MinHeight: kit.Px(32),
		Padding: kit.Edges{Start: kit.Px(4), End: kit.Px(8)}, Radius: kit.CornersAll(kit.Px(8))}
	b.chev = newChevron(14, views.InkMuted)
	b.label = para(label, fontLeadTitle, views.InkInk)
	b.label.Style.Flex = kit.FlexNone
	b.summary = views.NewLine("", fontHint, views.InkFaint)
	b.summary.Style.Margin = kit.Edges{Start: kit.Px(6)}
	b.Add(b.chev, b.label, b.summary)
	return b
}

func (b *disclosure) set(open bool, summary string) {
	b.open = open
	b.chev.setOpen(open)
	b.summary.SetHidden(open)
	b.summary.SetText(summary)
	exp := kit.ExpandClosed
	if open {
		exp = kit.ExpandOpen
	}
	name := b.label.Text()
	if !open {
		name += " " + summary
	}
	if b.Semantics.Expand != exp || b.Semantics.Name != name {
		b.Semantics.Expand = exp
		b.Semantics.Name = name
		b.SemanticsChanged()
	}
}

// HoverChanged is kit.HoverHandler's.
func (b *disclosure) HoverChanged(h bool) {
	b.Pressable.HoverChanged(h)
	b.Invalidate()
}

// Paint is kit.Painter's: --hover under the pointer.
func (b *disclosure) Paint(p *kit.PaintContext) {
	if b.Hovered() {
		t := views.TokensOf(b.Base())
		p.FillRoundedRect(kit.RoundRect(kit.RectAt(kit.Point{}, p.Size()), 8), t.Hover)
	}
}

// chevron is a chevron-right icon that turns a quarter when open (half in a right-to-left
// window while closed).
type chevron struct {
	kit.Element
	size kit.DIP
	ink  views.Ink
	open bool
}

func newChevron(size kit.DIP, ink views.Ink) *chevron {
	c := &chevron{size: size, ink: ink}
	c.Semantics.Hidden = true
	c.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	return c
}

func (c *chevron) setOpen(open bool) {
	if c.open != open {
		c.open = open
		c.Invalidate()
	}
}

// Paint is kit.Painter's.
func (c *chevron) Paint(p *kit.PaintContext) {
	w := c.Window()
	if w == nil || w.Host() == nil {
		return
	}
	deg := float32(0)
	switch {
	case c.open:
		deg = 90
	case w.Direction() == kit.DirRTL:
		deg = 180
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	set := iconsFor(w.Host().Resources())
	t := views.TokensOf(&c.Element)
	if deg == 0 {
		set.DrawIcon(p.Canvas, icons.ChevronRight, at, c.size, c.ink(t))
		return
	}
	cx, cy := at.X+c.size/2, at.Y+c.size/2
	p.Canvas.PushTransform(kit.Translation(-cx, -cy).Then(kit.Rotation(deg)).Then(kit.Translation(cx, cy)))
	set.DrawIcon(p.Canvas, icons.ChevronRight, at, c.size, c.ink(t))
	p.Canvas.Pop()
}
