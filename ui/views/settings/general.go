package settings

import (
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// escapeBidi is text.EscapeBidi (a name from the daemon or the user shown in a sentence).
func escapeBidi(s string) string { return text.EscapeBidi(s) }

// newPage mounts a page's view.
func newPage(d *Dialog, id validate.SettingsPage) pageView {
	curSkin = d.skin
	switch id {
	case validate.PageDownloads:
		return newDownloadsPage(d)
	case validate.PageNetwork:
		return newNetworkPage(d)
	case validate.PageIntegration:
		return newIntegrationPage(d)
	case validate.PageVideo:
		return newVideoPage(d)
	case validate.PageAppearance:
		return newAppearancePage(d)
	case validate.PageUpdates:
		return newUpdatesPage(d)
	case validate.PageAbout:
		return newAboutPage(d)
	}
	return newGeneralPage(d)
}

// page is a page's .page box: its sections 24 px apart.
type page struct {
	kit.Element
	d *Dialog
}

func (p *page) init(d *Dialog) {
	p.d = d
	p.Style = kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(24), MinWidth: kit.Px(0)}
}

// toggleRow is one of General's switches.
type toggleRow struct {
	key        *bool
	label      string
	hint       string
	row        *widgets.SwitchRow
	rowSurface *surface
}

// generalPage is General: Start-up, Notifications and Progress windows, the dialog's own
// switches.
type generalPage struct {
	page
	rows []*toggleRow
}

func newGeneralPage(d *Dialog) *generalPage {
	g := &generalPage{}
	g.init(d)
	t := &d.toggles
	startup := []*toggleRow{
		{key: &t.launchAtLogin, label: "Start Downex in the background when I sign in", hint: "Downex then runs in the background, so scheduled queues start on time."},
		{key: &t.showTrayIcon, label: "Show the Downex icon in the notification area"},
	}
	notify := []*toggleRow{
		{key: &t.notifyOnComplete, label: "Notify me when a download completes"},
		{key: &t.notifyOnFailure, label: "Notify me when a download fails"},
	}
	progress := []*toggleRow{
		{key: &t.showProgressWindow, label: "Show a progress window when I start a download"},
		{key: &t.showCompleteDialog, label: "Show the download complete dialog for downloads I started"},
	}
	g.Add(
		sSection(sHead("Start-up"), g.card(startup)),
		sSection(sHead("Notifications"), g.card(notify)),
		sSection(sHead("Progress windows",
			sLead("A download’s progress window can always be opened from the list (double-click it, or choose Show progress).")),
			g.card(progress)),
	)
	g.sync()
	return g
}

func (g *generalPage) card(rows []*toggleRow) *surface {
	var ws []kit.Widget
	for _, r := range rows {
		r := r
		r.rowSurface, r.row = switchRow(r.label, r.hint, *r.key, func(on bool) {
			*r.key = on
			g.d.changed()
		})
		g.rows = append(g.rows, r)
		ws = append(ws, r.rowSurface)
	}
	return sCard(ws...)
}

func (g *generalPage) sync() {
	for _, r := range g.rows {
		r.row.SetDisabled(g.d.loading)
		if r.row.Switch.On != *r.key {
			r.row.Switch.SetOn(*r.key)
		}
	}
}
