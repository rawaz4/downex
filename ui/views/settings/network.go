package settings

import (
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The proxy modes as the segmented control offers them ('unspecified' is shown as, and means,
// system).
var proxyModes = []struct {
	value       string
	mode        downexv1.ProxyMode
	label, hint string
	title       string
}{
	{"system", downexv1.ProxyMode_PROXY_MODE_SYSTEM, "System", "Uses the system proxy settings, including automatic detection and configuration scripts.", "Use the system proxy settings"},
	{"none", downexv1.ProxyMode_PROXY_MODE_NONE, "No proxy", "Connects directly, without a proxy.", "No proxy"},
	{"manual", downexv1.ProxyMode_PROXY_MODE_MANUAL, "Manual", "Uses the HTTP, HTTPS or SOCKS proxies you enter.", "Manual proxy"},
	{"pac", downexv1.ProxyMode_PROXY_MODE_PAC, "PAC script", "Asks a proxy auto-configuration (PAC) script, which Downex evaluates, for each address.", "Automatic configuration script"},
}

// shownMode is the mode shown as chosen.
func shownMode(m downexv1.ProxyMode) int {
	for i, x := range proxyModes {
		if x.mode == m {
			return i
		}
	}
	return 0
}

const (
	socksHint  = "When set, it is used instead of the HTTP proxies."
	bypassHint = "Separated by semicolons; * matches any part, <local> names without dots."
	savedPw    = "Saved (type to replace it)"
)

// networkPage is Settings → Network: the proxy (its mode as a segmented control, then the
// mode's fields), host rules and site logins. The dialog owns the form and the login editor.
type networkPage struct {
	page
	proxyCard *surface
	modeHint  *views.Text
	modeCtl   *widgets.SegmentedControl
	proxyErr  *errorText
	fields    map[string]*field
	clearPw   *checkSmall
	pwIn      *widgets.Input

	rulesSec *kit.Element
	rules    []*ruleRow
	addRule  *widgets.Button

	loginsSec *kit.Element
	logins    []*loginRow
	addLogin  *widgets.Button
	editor    *loginEditorView
}

func newNetworkPage(d *Dialog) *networkPage {
	n := &networkPage{fields: map[string]*field{}}
	n.init(d)
	n.proxyCard = sCard()
	n.buildProxy()
	n.rulesSec = sSection(nil)
	n.loginsSec = sSection(nil)
	n.buildRules()
	n.buildLogins()
	n.Add(sSection(sHead("Proxy"), n.proxyCard), n.rulesSec, n.loginsSec)
	n.sync()
	return n
}

// pair is .pair: two columns 12 px apart (one in a pane of 32rem or less).
func (n *networkPage) pair(a, b kit.Widget) *grid {
	g := newGrid(repeatFr(2), kit.Px(12), kit.Px(12), a, b)
	g.alignItems = kit.AlignStart
	g.respond = func() {
		if n.d.paneAtMost(32) {
			g.cols = repeatFr(1)
		} else {
			g.cols = repeatFr(2)
		}
	}
	return g
}

// textField makes a field of the proxy form bound to a string of the form.
func (n *networkPage) textField(key, label, hint, placeholder string, mono bool, value *string) *field {
	d := n.d
	in := input(*value, widgets.InputOptions{Name: label, Description: hint, Placeholder: placeholder, Mono: mono, Disabled: d.loading}, func(s string) {
		*value = s
		d.changed()
	})
	var f *field
	if hint != "" {
		e := newErrorText()
		f = &field{input: in, err: e}
		f.Field = views.NewField("", nil, views.NewLabelRow(label, views.NewHint(hint)), in, e)
	} else {
		f = newField(label, in, in, nil)
	}
	n.fields[key] = f
	return f
}

// buildProxy lays the proxy card out for the mode shown.
func (n *networkPage) buildProxy() {
	d := n.d
	nf := &d.network
	n.fields = map[string]*field{}
	mi := shownMode(nf.ProxyMode)
	n.modeHint = sHint(proxyModes[mi].hint)
	var opts []widgets.SegmentOption
	for _, m := range proxyModes {
		opts = append(opts, widgets.SegmentOption{Value: m.value, Label: m.label, Title: m.title})
	}
	n.modeCtl = widgets.NewSegmentedControl(opts, proxyModes[mi].value, widgets.SegmentedOptions{
		Legend: "How downloads connect", Fill: true, SelectWhenNarrow: true, Disabled: d.loading,
	}, func(v string) {
		for _, m := range proxyModes {
			if m.value == v {
				nf.ProxyMode = m.mode
			}
		}
		n.buildProxy()
		n.sync()
		d.changed()
	})
	n.modeCtl.Track().Style.MaxWidth = kit.Rem(30)
	n.proxyErr = newErrorText()
	rows := []kit.Widget{sStack(sText("How downloads connect", n.modeHint), n.modeCtl, n.proxyErr)}
	switch proxyModes[mi].value {
	case "manual":
		rows = append(rows, sStack(
			n.pair(n.textField("http", "HTTP proxy", "", "proxy.example.com:8080", true, &nf.HTTPProxy),
				n.textField("https", "HTTPS proxy", "", "proxy.example.com:8080", true, &nf.HTTPSProxy)),
			n.textField("socks", "SOCKS proxy", socksHint, "socks5://127.0.0.1:1080", true, &nf.SocksProxy),
			n.textField("bypass", "Connect directly to", bypassHint, "<local>;*.example.com", true, &nf.Bypass),
		))
	case "pac":
		rows = append(rows, sStack(n.textField("pac", "Script address", "", "https://wpad.example.com/proxy.pac", true, &nf.PacURL)))
	}
	n.clearPw, n.pwIn = nil, nil
	if proxyModes[mi].value != "none" {
		user := n.textField("user", "Proxy user name", "", "", false, &nf.ProxyUser)
		n.pwIn = input(d.proxyPassword, widgets.InputOptions{Name: "Proxy password", Password: true, Description: "Kept in the system's credential store, never shown.", Disabled: d.loading}, func(s string) {
			d.proxyPassword = s
			n.syncPassword()
			d.changed()
		})
		pw := newField("Proxy password", n.pwIn, n.pwIn, nil)
		shield := icon("shield", 12, views.InkFaint)
		hint := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MinWidth: kit.Px(0)},
			shield, views.NewHint("Kept in the system's credential store, never shown."))
		foot := el(kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignCenter, Justify: kit.JustifySpaceBetween,
			RowGap: kit.Px(6), ColumnGap: kit.Px(16), MinWidth: kit.Px(0)}, hint)
		if d.base.GetProxy().GetHasPassword() {
			n.clearPw = newCheckSmall("Remove the saved password", d.clearProxyPassword, func(on bool) {
				d.clearProxyPassword = on
				n.syncPassword()
				d.changed()
			})
			foot.Add(n.clearPw)
		}
		rows = append(rows, sStack(n.pair(user, pw), foot))
	}
	setRows(n.proxyCard, rows...)
	n.syncPassword()
}

// syncPassword shows the proxy password's placeholder and the removal box.
func (n *networkPage) syncPassword() {
	d := n.d
	if n.pwIn == nil {
		return
	}
	ph := "None"
	if d.base.GetProxy().GetHasPassword() && !d.clearProxyPassword {
		ph = savedPw
	}
	n.pwIn.SetPlaceholder(ph)
	if n.clearPw != nil {
		n.clearPw.SetDisabled(d.loading || d.proxyPassword != "")
	}
}

// ---- host rules ----

// ruleRow is a host rule's row of the table.
type ruleRow struct {
	key                            int
	pattern, conns, speed, request *widgets.Input
	unit                           *widgets.Select
	errs                           [4]*errorText
	remove                         *widgets.IconButton
}

// The tables' columns.
func ruleColumns() []track {
	return tracks(frac(kit.Px(0), 1), fixed(kit.Rem(6.5)), fixed(kit.Rem(12.5)), fixed(kit.Rem(7.5)), fixed(kit.Px(8+26+10)))
}

// tableCell is a td or th: its padding (8 px; 14 px at the row's start, 10 px at its end), a
// --line-soft rule above every body row after the first; the head on --surface-2 over a --line.
func tableCell(col, cols int, head, ruled bool, ground views.Ink, children ...kit.Widget) *surface {
	pad := kit.Edges{Top: kit.Px(7), Bottom: kit.Px(7), Start: kit.Px(8), End: kit.Px(8)}
	if col == 0 {
		pad.Start = kit.Px(14)
	}
	if col == cols-1 && !head {
		pad.End = kit.Px(10)
	}
	s := newSurface(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(4), MinWidth: kit.Px(0), Padding: pad}, ground, nil, 0, children...)
	if head {
		s.Style.Display = kit.DisplayFlex
		s.Style.AlignItems = kit.AlignCenter
		s.Style.Height = kit.Px(30)
		s.Style.Padding.Top, s.Style.Padding.Bottom = kit.Length{}, kit.Length{}
		s.Style.Border = kit.Edges{Bottom: kit.Px(1)}
		s.ground = inkSurface2
		s.edge = inkLine
		s.headRule = true
		return s
	}
	if ruled {
		s.Style.Border = kit.Edges{Top: kit.Px(1)}
		s.edge, s.rule, s.ruled = inkLineSoft, true, true
	}
	return s
}

// headCells are a table's th cells.
func headCells(labels ...string) []kit.Widget {
	var out []kit.Widget
	for i, l := range labels {
		var c kit.Widget = el(kit.Style{})
		if l != "" {
			caps := views.NewCaps(l)
			caps.SetOptions(kit.TextOptions{})
			c = caps
		}
		cell := tableCell(i, len(labels), true, false, nil, c)
		cell.Semantics.Role = kit.RoleColumnHeader
		if l == "" {
			cell.Semantics.Name = "Actions"
		}
		out = append(out, cell)
	}
	return out
}

// buildRules lays the host rules out again (a rule added or removed).
func (n *networkPage) buildRules() {
	d := n.d
	nf := &d.network
	n.rulesSec.RemoveAll()
	n.rules = n.rules[:0]
	n.rulesSec.Add(sHead("Host rules", sLead("Limits for the downloads from some hosts. The most specific pattern wins.")))
	if len(nf.HostRules) > 0 {
		cols := ruleColumns()
		table := newGrid(cols, kit.Px(0), kit.Px(0))
		table.alignItems = kit.AlignStretch
		table.table = true
		table.Semantics.Role = kit.RoleTable
		table.Semantics.Name = "Host rules"
		for _, c := range headCells("Host", "Connections", "Speed limit (shared)", "Max request", "") {
			table.Add(c)
		}
		for i, r := range nf.HostRules {
			row := n.newRule(i, r)
			n.rules = append(n.rules, row)
			cells := []kit.Widget{
				stackOf(row.pattern, row.errs[0]),
				stackOf(row.conns, row.errs[1]),
				stackOf(el(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(6), MinWidth: kit.Px(0)}, row.speed, row.unit), row.errs[2]),
				stackOf(newOverlay(row.request, para("MB", fontHint, views.InkFaint), true), row.errs[3]),
				el(kit.Style{Display: kit.DisplayFlex, Justify: kit.JustifyEnd}, row.remove),
			}
			for ci, c := range cells {
				table.Add(tableCell(ci, len(cols), false, i > 0, nil, c))
			}
		}
		card := sCard(table)
		card.Style.Overflow = kit.OverflowHidden
		card.Style.Radius = kit.CornersAll(kit.Px(10))
		n.rulesSec.Add(card, sLead("Max request: the longest part ("+validate.RequestCapRangeMB.Min+"–"+validate.RequestCapRangeMB.Max+
			" MB) asked of the host in one request; empty = no cap. Without a rule of yours, YouTube’s video servers (*.googlevideo.com) get 10.5 MB and 4 connections."))
	}
	n.addRule = button("Add host rule", "add", widgets.Secondary, false, n.addHostRule)
	n.addRule.SetDisabled(d.loading, false)
	n.rulesSec.Add(sAfter(n.addRule))
}

func stackOf(children ...kit.Widget) *kit.Element { return stack(kit.Px(4), children...) }

func (n *networkPage) ruleIndex(key int) int {
	for i, r := range n.d.network.HostRules {
		if r.Key == key {
			return i
		}
	}
	return -1
}

func (n *networkPage) newRule(i int, r validate.HostRuleDraft) *ruleRow {
	d := n.d
	key := r.Key
	row := &ruleRow{key: key}
	num := strconv.Itoa(i + 1)
	edit := func(f func(*validate.HostRuleDraft)) {
		if j := n.ruleIndex(key); j >= 0 {
			f(&d.network.HostRules[j])
			d.changed()
		}
	}
	dis := d.loading
	row.pattern = input(r.Pattern, widgets.InputOptions{Name: "Host of rule " + num, Placeholder: "*.example.com", Mono: true, Disabled: dis}, func(s string) {
		edit(func(h *validate.HostRuleDraft) { h.Pattern = s })
		n.syncRuleNames()
	})
	row.conns = input(r.Connections, widgets.InputOptions{Name: "Connections of rule " + num, Placeholder: "Default", Number: true, Disabled: dis}, func(s string) {
		edit(func(h *validate.HostRuleDraft) { h.Connections = s })
	})
	row.conns.Style.MinWidth = kit.Rem(4.5)
	row.speed = input(r.SpeedValue, widgets.InputOptions{Name: "Speed limit of rule " + num, Placeholder: "No limit", Disabled: dis}, func(s string) {
		edit(func(h *validate.HostRuleDraft) { h.SpeedValue = s })
	})
	row.speed.Style.Flex = kit.FlexAuto
	row.speed.Style.Width = kit.Length{}
	row.unit = widgets.NewSelect(unitOptions(), string(r.SpeedUnit), widgets.SelectOptions{AriaLabel: "Speed limit unit of rule " + num, Size: widgets.SelectSm, Disabled: dis}, func(v string) {
		edit(func(h *validate.HostRuleDraft) { h.SpeedUnit = format.RateUnit(v) })
	})
	row.unit.Style.Flex = kit.FlexOf(0, 0, kit.Rem(5.75))
	row.unit.Style.Width = kit.Rem(5.75)
	row.request = input(r.MaxRequest, widgets.InputOptions{Name: "Max request of rule " + num + " (MB)", Placeholder: "No cap", Disabled: dis}, func(s string) {
		edit(func(h *validate.HostRuleDraft) { h.MaxRequest = s })
	})
	row.request.Style.MinWidth = kit.Rem(4.5)
	row.request.Style.Padding.End = kit.Px(36)
	for j := range row.errs {
		row.errs[j] = newErrorText()
	}
	row.remove = iconButton("remove", "", "Remove rule", func() { n.removeRule(key) })
	row.remove.SetDisabled(dis, false)
	n.syncRuleName(row, i)
	return row
}

func (n *networkPage) syncRuleName(row *ruleRow, i int) {
	j := n.ruleIndex(row.key)
	if j < 0 {
		return
	}
	name := text.TrimJS(n.d.network.HostRules[j].Pattern)
	if name == "" {
		name = "row " + strconv.Itoa(i+1)
	}
	if s := "Remove the rule for " + name; row.remove.Semantics.Name != s {
		row.remove.Semantics.Name = s
		row.remove.SemanticsChanged()
	}
}

func (n *networkPage) syncRuleNames() {
	for i, r := range n.rules {
		n.syncRuleName(r, i)
	}
}

// addHostRule adds an empty rule and puts the keyboard on its host.
func (n *networkPage) addHostRule() {
	d := n.d
	key := d.newKey()
	d.network.HostRules = append(d.network.HostRules, validate.HostRuleDraft{Key: key, SpeedUnit: format.MBps})
	n.buildRules()
	n.sync()
	d.changed()
	for _, r := range n.rules {
		if r.key == key {
			d.focus(r.pattern)
		}
	}
}

// removeRule removes a rule; the keyboard goes to the next rule's host, else the previous, else
// Add host rule.
func (n *networkPage) removeRule(key int) {
	d := n.d
	i := n.ruleIndex(key)
	if i < 0 {
		return
	}
	hr := d.network.HostRules
	d.network.HostRules = append(hr[:i:i], hr[i+1:]...)
	n.buildRules()
	n.sync()
	d.changed()
	var target kit.Widget = n.addRule
	switch {
	case i < len(n.rules):
		target = n.rules[i].pattern
	case i > 0 && i-1 < len(n.rules):
		target = n.rules[i-1].pattern
	}
	d.focus(target)
}

// ---- site logins ----

// loginRow is a site login's row.
type loginRow struct {
	key          int
	cells        []*surface
	addrErr      *errorText
	userErr      *errorText
	pw           *views.Text
	edit, remove *widgets.IconButton
}

// passwordState is what a login's Password cell says (never the password).
func passwordState(l validate.SiteLoginDraft) string {
	switch {
	case l.Password != "":
		return "New password (saved with the settings)"
	case l.ClearPassword:
		return "Removed when saved"
	case l.HasPassword:
		return "Saved"
	}
	return "None"
}

func (n *networkPage) buildLogins() {
	d := n.d
	nf := &d.network
	n.loginsSec.RemoveAll()
	n.logins = n.logins[:0]
	n.editor = nil
	n.loginsSec.Add(sHead("Site logins", sLead("User names and passwords for servers that ask for them. Passwords are kept in the system's credential store and are never shown.")))
	if len(nf.SiteLogins) > 0 {
		cols := tracks(tableAuto(), tableAuto(), tableAuto(), auto())
		table := newGrid(cols, kit.Px(0), kit.Px(0))
		table.alignItems = kit.AlignStretch
		table.Semantics.Role = kit.RoleTable
		table.Semantics.Name = "Site logins"
		for _, c := range headCells("Address", "User name", "Password", "") {
			table.Add(c)
		}
		for i, l := range nf.SiteLogins {
			row := n.newLogin(i, l)
			n.logins = append(n.logins, row)
			for _, c := range row.cells {
				table.Add(c)
			}
		}
		card := sCard(table)
		card.Style.Overflow = kit.OverflowHidden
		card.Style.Radius = kit.CornersAll(kit.Px(10))
		n.loginsSec.Add(card)
	}
	if d.loginEditor != nil {
		n.editor = newLoginEditorView(n)
		n.loginsSec.Add(n.editor)
	} else {
		n.addLogin = button("Add site login", "add", widgets.Secondary, false, func() { n.openEditor(-1) })
		n.addLogin.SetDisabled(d.loading, false)
		n.loginsSec.Add(sAfter(n.addLogin))
	}
}

func (n *networkPage) newLogin(i int, l validate.SiteLoginDraft) *loginRow {
	d := n.d
	key := l.Key
	row := &loginRow{key: key, addrErr: newErrorText(), userErr: newErrorText()}
	addr := para(escapeBidi(l.URLPattern), fontMono, views.InkInk)
	addr.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	user := para(escapeBidi(l.Username), fontBody, views.InkInk2)
	row.pw = para(passwordState(l), fontCell, views.InkMuted)
	if l.Password != "" {
		row.pw.Ink = views.InkAccent
	} else {
		row.pw.SetOptions(kit.TextOptions{})
	}
	name := escapeBidi(l.URLPattern)
	row.edit = iconButton("settings", "Edit the login for "+name, "Edit", func() { n.openEditor(key) })
	row.remove = iconButton("remove", "Remove the login for "+name, "Remove", func() { n.removeLogin(key) })
	row.edit.SetDisabled(d.loading, false)
	row.remove.SetDisabled(d.loading, false)
	parts := []kit.Widget{
		stackOf(addr, row.addrErr), stackOf(user, row.userErr), row.pw,
		el(kit.Style{Display: kit.DisplayFlex, Justify: kit.JustifyEnd}, row.edit, newSpaceGap(fontBody), row.remove),
	}
	for ci, p := range parts {
		c := tableCell(ci, len(parts), false, i > 0, nil, p)
		c.Style.Display = kit.DisplayFlex
		c.Style.Axis = kit.AxisColumn
		c.Style.Justify = kit.JustifyCenter
		c.Style.MinHeight = kit.Px(40)
		row.cells = append(row.cells, c)
	}
	return row
}

func (n *networkPage) loginIndex(key int) int {
	for i, l := range n.d.network.SiteLogins {
		if l.Key == key {
			return i
		}
	}
	return -1
}

// openEditor opens the editor of a login of the list (key) or of a new one (-1), and puts the
// keyboard on its address.
func (n *networkPage) openEditor(key int) {
	d := n.d
	var e validate.SiteLoginEditor
	if i := n.loginIndex(key); key >= 0 && i >= 0 {
		e = validate.LoginEditorOf(d.network.SiteLogins[i])
	} else {
		e = validate.NewLoginEditor("")
	}
	d.loginEditor = &e
	d.loginEditorError = validate.SiteLoginEditorErrors{}
	n.buildLogins()
	n.sync()
	d.changed()
	if n.editor != nil {
		d.focus(n.editor.pattern)
	}
}

// focusAfterEditor puts the keyboard on the login's Edit button, else Add site login.
func (n *networkPage) focusAfterEditor(key int, isNew bool) {

	if !isNew {
		for _, r := range n.logins {
			if r.key == key {
				n.d.focus(r.edit)
				return
			}
		}
	}
	if n.addLogin != nil {
		n.d.focus(n.addLogin)
	}
}

// applyEditor applies the editor to the list (Add login, Apply, Enter in its fields).
func (n *networkPage) applyEditor() {
	d := n.d
	e := d.loginEditor
	if e == nil {
		return
	}
	logins, _, errs, ok := validate.ApplyLoginEditor(d.network.SiteLogins, *e, d.newKey)
	if !ok {
		d.loginEditorError = errs
		n.sync()
		if f := n.editorInvalid(); f != nil {
			d.focus(f)
		}
		return
	}
	d.network.SiteLogins = logins
	key, isNew := e.Key, e.IsNew
	d.loginEditor = nil
	d.loginEditorError = validate.SiteLoginEditorErrors{}
	n.buildLogins()
	n.sync()
	d.changed()
	n.focusAfterEditor(key, isNew)
}

// cancelEditor closes the editor.
func (n *networkPage) cancelEditor() {
	d := n.d
	key, isNew := -1, true
	if e := d.loginEditor; e != nil {
		key, isNew = e.Key, e.IsNew
	}
	d.loginEditor = nil
	d.loginEditorError = validate.SiteLoginEditorErrors{}
	n.buildLogins()
	n.sync()
	d.changed()
	n.focusAfterEditor(key, isNew)
}

// removeLogin removes a login (and closes its editor).
func (n *networkPage) removeLogin(key int) {
	d := n.d
	i := n.loginIndex(key)
	if i < 0 {
		return
	}
	sl := d.network.SiteLogins
	d.network.SiteLogins = append(sl[:i:i], sl[i+1:]...)
	if e := d.loginEditor; e != nil && !e.IsNew && e.Key == key {
		d.loginEditor = nil
		d.loginEditorError = validate.SiteLoginEditorErrors{}
	}
	n.buildLogins()
	n.sync()
	d.changed()
	switch {
	case i < len(n.logins):
		n.d.focus(n.logins[i].edit)
	case i > 0:
		n.d.focus(n.logins[i-1].edit)
	case n.addLogin != nil:
		n.d.focus(n.addLogin)
	}
}

// editorInvalid is the editor's first field with an error.
func (n *networkPage) editorInvalid() kit.Widget {
	if n.editor == nil {
		return nil
	}
	for _, in := range []*widgets.Input{n.editor.pattern, n.editor.user} {
		if in.InputOptions().Invalid {
			return in
		}
	}
	return nil
}

// loginEditorView is the editor of a site login: a group on --surface-2 in an --accent-line
// border, named by its heading.
type loginEditorView struct {
	*surface
	n                       *networkPage
	pattern, user, password *widgets.Input
	patternErr, userErr     *errorText
	clear                   *checkSmall
	saved                   bool
}

const patternHint = "An address with * for any part, or a host such as *.example.com."

func newLoginEditorView(n *networkPage) *loginEditorView {
	d := n.d
	e := d.loginEditor
	v := &loginEditorView{n: n, patternErr: newErrorText(), userErr: newErrorText()}
	title := "New site login"
	applyLabel := "Add login"
	if !e.IsNew {
		title, applyLabel = "Edit site login", "Apply"
		if i := n.loginIndex(e.Key); i >= 0 {
			v.saved = d.network.SiteLogins[i].HasPassword
		}
	}
	enter := func() bool {
		n.applyEditor()
		return true
	}
	v.pattern = input(e.URLPattern, widgets.InputOptions{Name: "Address", Description: patternHint, Placeholder: "https://example.com/*", Mono: true}, func(s string) {
		if d.loginEditor != nil {
			d.loginEditor.URLPattern = s
		}
	})
	v.pattern.OnEnter = enter
	v.user = input(e.Username, widgets.InputOptions{Name: "User name"}, func(s string) {
		if d.loginEditor != nil {
			d.loginEditor.Username = s
		}
	})
	v.user.OnEnter = enter
	ph := ""
	if v.saved {
		ph = savedPw
	}
	v.password = input(e.Password, widgets.InputOptions{Name: "Password", Password: true, Placeholder: ph}, func(s string) {
		if d.loginEditor != nil {
			d.loginEditor.Password = s
			if v.clear != nil {
				v.clear.SetDisabled(s != "")
			}
		}
	})
	v.password.OnEnter = enter
	addr := views.NewField("", nil, views.NewLabelRow("Address", views.NewHint(patternHint)), v.pattern, v.patternErr)
	user := views.NewField("User name", v.user, v.userErr)
	pwParts := []kit.Widget{}
	if v.saved {
		v.clear = newCheckSmall("Remove the saved password", e.ClearPassword, func(on bool) {
			if d.loginEditor != nil {
				d.loginEditor.ClearPassword = on
			}
		})
		v.clear.SetDisabled(e.Password != "")
		pwParts = append(pwParts, v.clear)
	}
	pw := views.NewField("Password", v.password, pwParts...)
	hint := views.NewHint("The login is stored when you save the settings.")
	hint.Style.Margin = kit.Edges{End: kit.Auto}
	hint.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	actions := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyEnd, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)},
		hint, button("Cancel", "", widgets.Secondary, false, n.cancelEditor), button(applyLabel, "", widgets.Primary, false, n.applyEditor))
	v.surface = newSurface(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(12), Padding: kit.EdgesAll(kit.Px(14)), Border: kit.EdgesAll(kit.Px(1)), MinWidth: kit.Px(0)},
		inkSurface2, inkAccentLine, 10, heading(title, fontLeadTitle, 4), addr, n.pair(user, pw), actions)
	v.Semantics.Role = kit.RoleGroup
	v.Semantics.Name = title
	return v
}

func (v *loginEditorView) sync() {
	errs := v.n.d.loginEditorError
	setError(v.pattern, v.patternErr, patternHint, errs.URLPattern)
	setError(v.user, v.userErr, "", errs.Username)
}

// focusField is focusTarget's: the editor's user name (auth_required).
func (n *networkPage) focusField(f validate.SettingsField) kit.Widget {
	if f == "login-user" && n.editor != nil {
		return n.editor.user
	}
	return nil
}

// firstInvalid is the first field with an error, in the page's order.
func (n *networkPage) firstInvalid() kit.Widget {
	for _, k := range []string{"http", "https", "socks", "bypass", "pac", "user"} {
		if f := n.fields[k]; f != nil && f.input.InputOptions().Invalid {
			return f.input
		}
	}
	for _, r := range n.rules {
		for _, in := range []*widgets.Input{r.pattern, r.conns, r.speed, r.request} {
			if in.InputOptions().Invalid {
				return in
			}
		}
	}
	errs := n.d.errors()
	for _, r := range n.logins {
		if e := errs.SiteLoginRows[r.key]; !e.Empty() {
			return r.edit
		}
	}
	return n.editorInvalid()
}

func (n *networkPage) sync() {
	d := n.d
	errs := d.errors()
	dis := d.loading
	n.modeHint.SetText(proxyModes[shownMode(d.network.ProxyMode)].hint)
	n.proxyErr.set(errs.Proxy)
	set := func(k, msg, hint string) {
		if f := n.fields[k]; f != nil {
			setError(f.input, f.err, hint, msg)
			f.input.SetDisabled(dis)
		}
	}
	set("http", errs.HTTPProxy, "")
	set("https", errs.HTTPSProxy, "")
	set("socks", errs.SocksProxy, socksHint)
	set("bypass", errs.Bypass, bypassHint)
	set("pac", errs.PacURL, "")
	set("user", errs.ProxyUser, "")
	if n.pwIn != nil {
		n.pwIn.SetDisabled(dis)
	}
	n.syncPassword()
	for _, r := range n.rules {
		re := errs.HostRuleRows[r.key]
		setError(r.pattern, r.errs[0], "", re.Pattern)
		setError(r.conns, r.errs[1], "", re.Connections)
		setError(r.speed, r.errs[2], "", re.Speed)
		setError(r.request, r.errs[3], "", re.MaxRequest)
		for _, in := range []*widgets.Input{r.pattern, r.conns, r.speed, r.request} {
			in.SetDisabled(dis)
		}
		r.unit.SetDisabled(dis)
		r.remove.SetDisabled(dis, false)
	}
	for _, r := range n.logins {
		le := errs.SiteLoginRows[r.key]
		r.addrErr.set(le.URLPattern)
		r.userErr.set(le.Username)
		var ground views.Ink
		if !le.Empty() {
			ground = inkDangerSoft
		}
		for _, c := range r.cells {
			c.ground = ground
			c.Invalidate()
		}
	}
	if n.editor != nil {
		n.editor.sync()
	}
	if n.addRule != nil {
		n.addRule.SetDisabled(dis, false)
	}
	if n.addLogin != nil {
		n.addLogin.SetDisabled(dis, false)
	}
}

// spaceGap is the space between two inline buttons of a cell (td.actions: the markup's
// whitespace between them, one space of the cell's font wide).
type spaceGap struct {
	kit.Element
	font  kit.Font
	scale kit.Scale
	w     kit.DIP
	made  bool
}

func newSpaceGap(f kit.Font) *spaceGap {
	g := &spaceGap{font: f}
	g.Style.Flex = kit.FlexOf(0, 0, kit.Length{})
	return g
}

func (g *spaceGap) measure(ctx *kit.LayoutContext) {
	if (g.made && g.scale == ctx.Scale) || ctx.Text == nil {
		return
	}
	g.scale, g.made = ctx.Scale, true
	if l, err := ctx.Text.LayoutText(" ", g.font.Style(ctx.Scale), kit.TextOptions{}); err == nil {
		g.w = l.Metrics().Advance
		l.Release()
	}
}

// ContentWidths is kit.Layouter's.
func (g *spaceGap) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	g.measure(ctx)
	return g.w, g.w
}

// ContentHeight is kit.Layouter's.
func (g *spaceGap) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's.
func (g *spaceGap) LayoutContent(*kit.LayoutContext, kit.Rect) {}
