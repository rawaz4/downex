package settings

import (
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/confirm"
	"downex/ui/core/format"
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// paneAtMost reports a pane whose content box is rem wide or less (@container settings-pane
// (max-width: …)).
func (d *Dialog) paneAtMost(rem float32) bool { return d.paneW <= d.scale.Rem(rem) }

// pickFolder shows the folder picker from initial and hands a chosen folder to f (on the UI
// thread); a cancelled picker does nothing.
func (d *Dialog) pickFolder(initial string, f func(string)) {
	if d.app.Platform == nil {
		return
	}
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		picked, err := d.app.Platform.PickFolder(ctx, initial)
		if err != nil || picked == "" {
			return
		}
		d.app.UI(func() {
			if !d.gone {
				f(picked)
			}
		})
	})
}

// field is a labelled field of a page: its label, control, hint and error (.field).
type field struct {
	*views.Field
	input *widgets.Input
	hint  *views.Text
	err   *errorText
}

// newField makes a .field: label, control, a hint (may be nil) and an error under it.
func newField(label string, control kit.Widget, in *widgets.Input, hint *views.Text) *field {
	f := &field{input: in, hint: hint, err: newErrorText()}
	extras := []kit.Widget{}
	if hint != nil {
		extras = append(extras, hint)
	}
	extras = append(extras, f.err)
	f.Field = views.NewField(label, control, extras...)
	return f
}

// setError shows an error on a field: the input invalid and described by it (else by the hint).
func setError(in *widgets.Input, e *errorText, hint string, msg string) {
	e.set(msg)
	if in == nil {
		return
	}
	in.SetInvalid(msg != "")
	desc := hint
	if msg != "" {
		desc = msg
	}
	if in.Semantics.Description != desc {
		in.Semantics.Description = desc
		in.SemanticsChanged()
	}
}

// setValue puts a value into an input when it differs (keeping the caret otherwise).
func setValue(in *widgets.Input, v string) {
	if in.Text() != v {
		in.SetText(v)
	}
}

// downloadsPage is Settings → Downloads: how many downloads run at once and with how many
// connections, the global speed limit (value and unit), the default folder, and the categories
// (their names, extensions and folders).
type downloadsPage struct {
	page
	limits      *grid
	concurrent  *field
	connections *field
	speed       *field
	speedIn     *widgets.Input
	unit        *widgets.Select
	dir         *field
	dirIn       *widgets.Input
	browse      *widgets.Button
	ownFolders  *widgets.SwitchRow
	ownBox      *kit.Element
	expired     *field
	renewSel    *widgets.Select
	polite      *widgets.SwitchRow // Polite mode (docs/POLITE-SPEC.md §8)
	politeBox   *kit.Element

	catHead  *kit.Element
	count    *views.Text
	addCat   *widgets.Button
	catCard  *surface
	catGrid  *kit.Element
	catHeads *grid
	rows     []*categoryRow
	empty    *views.Text
	listErr  *errorText
}

// categoryRow is one category's row.
type categoryRow struct {
	g           *grid
	key         int
	tile        *tile
	name, exts  *widgets.Input
	dir         *widgets.Input
	nameErr     *errorText
	extsErr     *errorText
	dirErr      *errorText
	pick, trash *widgets.IconButton
}

const (
	concurrentHint  = "Others wait in the queue ("
	connectionsHint = "Default for new downloads ("
	speedHint       = "Shared by all downloads. Empty = no limit."
	dirHint         = "Where new downloads are saved."
)

func newDownloadsPage(d *Dialog) *downloadsPage {
	p := &downloadsPage{}
	p.init(d)
	dis := d.loading

	cHint := concurrentHint + strconv.Itoa(validate.MinConcurrent) + "–" + strconv.Itoa(validate.MaxConcurrent) + ")."
	cIn := input(d.maxConcurrent, widgets.InputOptions{Name: "Simultaneous downloads", Description: cHint, Align: kit.TextAlignEnd, Number: true, Disabled: dis}, func(s string) {
		d.maxConcurrent = s
		d.changed()
	})
	cIn.Style.Width = kit.Rem(5.5)
	p.concurrent = newField("Simultaneous downloads", cIn, cIn, p.hint(cHint))

	nHint := connectionsHint + strconv.Itoa(validate.MinConnections) + "–" + strconv.Itoa(validate.MaxConnections) + ")."
	nIn := input(d.defaultConnections, widgets.InputOptions{Name: "Connections per download", Description: nHint, Align: kit.TextAlignEnd, Number: true, Disabled: dis}, func(s string) {
		d.defaultConnections = s
		d.changed()
	})
	nIn.Style.Width = kit.Rem(5.5)
	p.connections = newField("Connections per download", nIn, nIn, p.hint(nHint))

	p.speedIn = input(d.speedValue, widgets.InputOptions{Name: "Global speed limit", Description: speedHint, Placeholder: "No limit", Align: kit.TextAlignEnd, Disabled: dis}, func(s string) {
		d.speedValue = s
		d.changed()
	})
	p.speedIn.Style.Width = kit.Rem(6.5)
	p.speedIn.Style.Flex = kit.FlexNone
	p.unit = widgets.NewSelect(unitOptions(), string(d.speedUnit), widgets.SelectOptions{AriaLabel: "Global speed limit unit", Size: widgets.SelectSm, Disabled: dis}, func(v string) {
		d.speedUnit = format.RateUnit(v)
		d.changed()
	})
	p.unit.Style.Width = kit.Rem(6.25)
	p.unit.Style.Flex = kit.FlexNone
	rate := el(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}, p.speedIn, p.unit)
	p.speed = newField("Global speed limit", rate, p.speedIn, p.hint(speedHint))

	p.dirIn = input(d.defaultDir, widgets.InputOptions{Name: "Default folder", Description: dirHint, Mono: true, Disabled: dis}, func(s string) {
		d.defaultDir = s
		p.dirIn.Tooltip = escapeBidi(s)
		p.syncPlaceholders()
		d.changed()
	})
	p.dirIn.Tooltip = escapeBidi(d.defaultDir)
	p.dirIn.Style.Padding.Start = kit.Px(34)
	p.browse = button("Browse…", "", widgets.Secondary, true, func() {
		d.pickFolder(d.defaultDir, func(dir string) {
			d.defaultDir = dir
			setValue(p.dirIn, dir)
			p.dirIn.Tooltip = escapeBidi(dir)
			p.syncPlaceholders()
			d.changed()
		})
	})
	p.browse.SetDisabled(dis, false)
	withIcon := newOverlay(p.dirIn, icon("folder", 16, views.InkFaint), false)
	withIcon.Style.Flex = kit.FlexAuto
	withButton := el(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}, withIcon, p.browse)
	dirErr := newErrorText()
	p.dir = &field{input: p.dirIn, err: dirErr}
	p.dir.Field = views.NewField("", nil, views.NewLabelRow("Default folder", views.NewHint(dirHint)), withButton, dirErr)

	p.ownFolders = widgets.NewSwitchRow("Save downloads into their category's folder", "Without it, every download goes to the default folder.",
		d.toggles.useCategoryFolders, func(on bool) {
			d.toggles.useCategoryFolders = on
			d.changed()
		})
	p.ownFolders.Switch.SetStyle(curSkin.switchStyle()) // the skin's switch, as switchRow gives the others
	p.ownBox = el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, p.ownFolders)
	p.polite = widgets.NewSwitchRow("Polite mode", politeDescription, d.toggles.politeMode, func(on bool) {
		d.toggles.politeMode = on
		d.changed()
	})
	p.polite.Switch.SetStyle(curSkin.switchStyle())
	p.politeBox = el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, p.polite)

	// Expired links (docs/DESKTOP-SPEC.md §10).
	p.renewSel = widgets.NewSelect(renewOptions(), renewValue(d.renewLinks),
		widgets.SelectOptions{AriaLabel: expiredLabel, Disabled: dis}, func(v string) {
			d.renewLinks = renewLinksOf(v)
			d.changed()
		})
	p.expired = newField(expiredLabel, p.renewSel, nil, p.hint(expiredHint))

	p.limits = newGrid(repeatFr(6), kit.Px(20), kit.Px(14))
	p.limits.alignItems = kit.AlignStart
	p.limits.Style.Padding = kit.Edges{Top: kit.Px(12), Start: kit.Px(14), End: kit.Px(14), Bottom: kit.Px(14)}
	p.limits.Add(p.concurrent, p.connections, p.speed, p.dir, p.ownBox, p.expired, p.politeBox)
	p.limits.respond = p.respondLimits
	limitsCard := sCard(p.limits)

	// the categories
	p.count = sHint("")
	p.addCat = button("Add category", "add", widgets.Secondary, true, p.addCategory)
	titles := stack(kit.Px(3), sTitle("Categories"), sLead("A new download joins the first category that lists its file extension."))
	titles.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	after := sAfter(p.count, p.addCat)
	after.Style.Flex = kit.FlexNone
	p.catHead = el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignEnd, Justify: kit.JustifySpaceBetween,
		RowGap: kit.Px(8), ColumnGap: kit.Px(16), MinWidth: kit.Px(0)}, titles, after)
	p.catCard = sCard()
	p.listErr = newErrorText()
	p.empty = para("No categories: every download is listed under Other and saved in the default folder.", fontCell, views.InkMuted)
	p.buildCategories()

	p.Add(
		sSection(sHead("Downloads"), limitsCard),
		sSection(p.catHead, p.catCard),
	)
	p.sync()
	return p
}

func (p *downloadsPage) hint(s string) *views.Text { return views.NewHint(s) }

// The "Expired links" row (docs/DESKTOP-SPEC.md §10).
const (
	expiredLabel = "Expired links"
	expiredHint  = "Downex checks that the new link serves the same bytes before it continues a download."
)

// renewOptions are the choices of "Expired links".
func renewOptions() []widgets.SelectOption {
	return []widgets.SelectOption{
		{Value: "auto", Label: "Continue with a matching new link"},
		{Value: "ask", Label: "Ask before continuing"},
		{Value: "off", Label: "Don't look for new links"},
	}
}

func renewValue(v downexv1.RenewLinks) string {
	switch v {
	case downexv1.RenewLinks_RENEW_LINKS_ASK:
		return "ask"
	case downexv1.RenewLinks_RENEW_LINKS_OFF:
		return "off"
	}
	return "auto"
}

func renewLinksOf(v string) downexv1.RenewLinks {
	switch v {
	case "ask":
		return downexv1.RenewLinks_RENEW_LINKS_ASK
	case "off":
		return downexv1.RenewLinks_RENEW_LINKS_OFF
	}
	return downexv1.RenewLinks_RENEW_LINKS_AUTO
}

// politeDescription is Polite mode's switch's description (docs/POLITE-SPEC.md §8).
const politeDescription = "Slow down when downloads make calls, games or streaming lag; speed back up when the line is quiet."

// unitOptions are the rate units (RATE_UNITS).
func unitOptions() []widgets.SelectOption {
	var out []widgets.SelectOption
	for _, u := range format.RateUnits() {
		out = append(out, widgets.SelectOption{Value: string(u), Label: string(u)})
	}
	return out
}

// respondLimits is the limits' container queries: three to a row then two, two to a row at
// 44rem, one at 32rem.
func (p *downloadsPage) respondLimits() {
	d := p.d
	span, wide, own := 2, 3, 3
	if d.paneAtMost(44) {
		span, own = 3, -1
	}
	if d.paneAtMost(32) {
		span, wide = -1, -1
	}
	for _, f := range []*field{p.concurrent, p.connections, p.speed} {
		p.limits.setPlace(f, place{col: -1, span: span, row: -1, rowSpan: 1})
	}
	p.limits.setPlace(p.dir, place{col: -1, span: wide, row: -1, rowSpan: 1})
	p.limits.setPlace(p.ownBox, place{col: -1, span: own, row: -1, rowSpan: 1, align: kit.AlignEnd})
	p.limits.setPlace(p.expired, place{col: -1, span: wide, row: -1, rowSpan: 1})
	p.limits.setPlace(p.politeBox, place{col: -1, span: own, row: -1, rowSpan: 1, align: kit.AlignEnd})
	wrap := d.paneAtMost(44)
	if p.catHead.Style.Wrap != wrap {
		p.catHead.Style.Wrap = wrap
	}
}

// defaultCategoryDir is where a category's downloads go without a folder of its own.
func (p *downloadsPage) defaultCategoryDir(c validate.CategoryDraft) string {
	name := text.TrimJS(c.Name)
	if name == "" {
		name = "Category"
	}
	return confirm.JoinDir(text.TrimJS(p.d.defaultDir), name)
}

// kindOfDraft is a category's tile as it is being typed (its name and extensions decide).
func kindOfDraft(c validate.CategoryDraft) categories.FileKind {
	exts, _ := validate.ParseExtensions(c.Extensions)
	return categories.CategoryKind(text.TrimJS(c.Name), exts)
}

func (p *downloadsPage) draft(key int) (int, *validate.CategoryDraft) {
	for i := range p.d.categories {
		if p.d.categories[i].Key == key {
			return i, &p.d.categories[i]
		}
	}
	return -1, nil
}

// catColumns is --cat-cols.
func catColumns() []track {
	return tracks(fixed(kit.Px(20)), frac(kit.Rem(6), 0.8), frac(kit.Px(0), 1.2), frac(kit.Px(0), 1.5), fixed(kit.Px(54)))
}

// buildCategories lays the categories card out again (rows added or removed).
func (p *downloadsPage) buildCategories() {
	d := p.d
	p.rows = p.rows[:0]
	var parts []kit.Widget
	if len(d.categories) > 0 {
		p.catHeads = newGrid(catColumns(), kit.Px(8), kit.Px(0))
		p.catHeads.alignItems = kit.AlignEnd
		p.catHeads.Style.Height = kit.Px(26)
		p.catHeads.Style.Padding = kit.Edges{Start: kit.Px(14), End: kit.Px(10)}
		p.catHeads.Semantics.Hidden = true
		for _, s := range []string{"", "Name", "Extensions", "Folder", ""} {
			p.catHeads.Add(views.NewCaps(s))
		}
		list := el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)})
		list.Semantics.Role = kit.RoleList
		list.Semantics.Description = "A new download joins the first category that lists its file extension."
		for _, c := range d.categories {
			r := p.newRow(c)
			p.rows = append(p.rows, r)
			list.Add(r.g)
		}
		p.catGrid = el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Padding: kit.Edges{Top: kit.Px(4), Bottom: kit.Px(8)}}, p.catHeads, list)
		parts = append(parts, p.catGrid)
	} else if !d.loading {
		row := el(kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesOf(kit.Px(9), kit.Px(14))}, p.empty)
		parts = append(parts, row)
	}
	errRow := el(kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesOf(kit.Px(9), kit.Px(14))}, p.listErr)
	errRow.Style.Hidden = true
	parts = append(parts, errRow)
	setRows(p.catCard, parts...)
	p.syncCount()
}

func (p *downloadsPage) newRow(c validate.CategoryDraft) *categoryRow {
	d := p.d
	key := c.Key
	r := &categoryRow{key: key}
	dis := d.loading
	r.tile = newTile(tileKindOf(kindOfDraft(c)), 20)
	tileBox := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Height: kit.Px(30)}, r.tile)
	r.nameErr, r.extsErr, r.dirErr = newErrorText(), newErrorText(), newErrorText()
	r.name = input(c.Name, widgets.InputOptions{Name: "Name", Disabled: dis, AutoDirection: true}, func(s string) {
		if _, dr := p.draft(key); dr != nil {
			dr.Name = s
			p.syncRow(r)
		}
		d.changed()
	})
	r.exts = input(c.Extensions, widgets.InputOptions{Name: "Extensions", Placeholder: "mp4 m4v", Disabled: dis}, func(s string) {
		if _, dr := p.draft(key); dr != nil {
			dr.Extensions = s
			p.syncRow(r)
		}
		d.changed()
	})
	r.dir = input(c.Dir, widgets.InputOptions{Name: "Folder", Mono: true, Placeholder: p.defaultCategoryDir(c), Disabled: dis}, func(s string) {
		if _, dr := p.draft(key); dr != nil {
			dr.Dir = s
		}
		d.changed()
	})
	cell := func(in *widgets.Input, e *errorText) *kit.Element {
		return el(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(4), MinWidth: kit.Px(0)}, in, e)
	}
	r.pick = iconButton("folder", "", "Browse…", func() { p.browseCategory(key) })
	r.trash = iconButton("remove", "", "Remove category", func() { p.removeCategory(key) })
	r.pick.SetDisabled(dis, false)
	r.trash.SetDisabled(dis, false)
	actions := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(2), Height: kit.Px(30)}, r.pick, r.trash)
	// .category: gap 8px, between its rows too when a narrow pane stacks the cells
	r.g = newGrid(catColumns(), kit.Px(8), kit.Px(8), tileBox, cell(r.name, r.nameErr), cell(r.exts, r.extsErr), cell(r.dir, r.dirErr), actions)
	r.g.alignItems = kit.AlignStart
	r.g.Style.Padding = kit.Edges{Top: kit.Px(4), Bottom: kit.Px(4), Start: kit.Px(14), End: kit.Px(10)}
	r.g.Semantics.Role = kit.RoleListItem
	kids := r.g.Children()
	r.g.respond = func() {
		if p.d.paneAtMost(32) {
			r.g.cols = tracks(fixed(kit.Px(20)), frac(kit.Px(0), 1), auto())
			r.g.setPlace(kids[0], place{col: 0, span: 1, row: 0, rowSpan: 1})
			for i, k := range kids[1:4] {
				r.g.setPlace(k, place{col: 1, span: 1, row: i, rowSpan: 1})
			}
			r.g.setPlace(kids[4], place{col: 2, span: 1, row: 0, rowSpan: 1})
			if p.catHeads != nil {
				p.catHeads.Style.Hidden = true
			}
			return
		}
		r.g.cols = catColumns()
		r.g.places = nil
		if p.catHeads != nil {
			p.catHeads.Style.Hidden = false
		}
	}
	p.syncRow(r)
	return r
}

// syncRow shows a row's draft: its tile, the folder it defaults to, and its buttons' names.
func (p *downloadsPage) syncRow(r *categoryRow) {
	_, c := p.draft(r.key)
	if c == nil {
		return
	}
	r.tile.set(tileKindOf(kindOfDraft(*c)), 20)
	r.dir.SetPlaceholder(p.defaultCategoryDir(*c))
	name := text.TrimJS(c.Name)
	if name == "" {
		name = "this category"
	}
	if n := "Choose the folder of " + name; r.pick.Semantics.Name != n {
		r.pick.Semantics.Name = n
		r.pick.SemanticsChanged()
	}
	if n := "Remove " + name; r.trash.Semantics.Name != n {
		r.trash.Semantics.Name = n
		r.trash.SemanticsChanged()
	}
}

func (p *downloadsPage) syncPlaceholders() {
	for _, r := range p.rows {
		p.syncRow(r)
	}
}

func (p *downloadsPage) syncCount() {
	p.count.SetText(strconv.Itoa(len(p.d.categories)) + " of " + strconv.Itoa(validate.MaxCategories))
	p.addCat.SetDisabled(p.d.loading || len(p.d.categories) >= validate.MaxCategories, false)
}

func (p *downloadsPage) rowOf(key int) *categoryRow {
	for _, r := range p.rows {
		if r.key == key {
			return r
		}
	}
	return nil
}

// addCategory adds an empty category and puts the keyboard on its name.
func (p *downloadsPage) addCategory() {
	d := p.d
	key := d.newKey()
	d.categories = append(d.categories, validate.CategoryDraft{Key: key})
	p.buildCategories()
	p.sync()
	d.changed()
	if r := p.rowOf(key); r != nil {
		d.focus(r.name)
	}
}

// removeCategory removes a category and keeps the keyboard in the list: the next row's name,
// else the previous, else Add category.
func (p *downloadsPage) removeCategory(key int) {
	d := p.d
	i, _ := p.draft(key)
	if i < 0 {
		return
	}
	d.categories = append(d.categories[:i:i], d.categories[i+1:]...)
	p.buildCategories()
	p.sync()
	d.changed()
	var target kit.Widget = p.addCat
	switch {
	case i < len(d.categories):
		target = p.rowOf(d.categories[i].Key).name
	case i > 0:
		target = p.rowOf(d.categories[i-1].Key).name
	}
	d.focus(target)
}

// browseCategory picks a category's folder.
func (p *downloadsPage) browseCategory(key int) {
	_, c := p.draft(key)
	if c == nil {
		return
	}
	initial := c.Dir
	if initial == "" {
		initial = p.defaultCategoryDir(*c)
	}
	p.d.pickFolder(initial, func(dir string) {
		if _, c := p.draft(key); c != nil {
			c.Dir = dir
			if r := p.rowOf(key); r != nil {
				setValue(r.dir, dir)
			}
			p.d.changed()
		}
	})
}

// focusField is focusTarget's: the global speed limit (the status bar's "Custom…").
func (p *downloadsPage) focusField(f validate.SettingsField) kit.Widget {
	if f == validate.FieldGlobalSpeedLimit {
		return p.speedIn
	}
	return nil
}

// firstInvalid is the first field with an error, in the page's order.
func (p *downloadsPage) firstInvalid() kit.Widget {
	for _, in := range []*widgets.Input{p.concurrent.input, p.connections.input, p.speedIn, p.dirIn} {
		if in.InputOptions().Invalid {
			return in
		}
	}
	for _, r := range p.rows {
		for _, in := range []*widgets.Input{r.name, r.exts, r.dir} {
			if in.InputOptions().Invalid {
				return in
			}
		}
	}
	return nil
}

func (p *downloadsPage) sync() {
	d := p.d
	errs := d.errors()
	dis := d.loading
	setValue(p.concurrent.input, d.maxConcurrent)
	setValue(p.connections.input, d.defaultConnections)
	setValue(p.speedIn, d.speedValue)
	p.unit.SetValue(string(d.speedUnit))
	setValue(p.dirIn, d.defaultDir)
	for _, in := range []*widgets.Input{p.concurrent.input, p.connections.input, p.speedIn, p.dirIn} {
		in.SetDisabled(dis)
	}
	p.unit.SetDisabled(dis)
	p.browse.SetDisabled(dis, false)
	p.ownFolders.SetDisabled(dis)
	if p.ownFolders.Switch.On != d.toggles.useCategoryFolders {
		p.ownFolders.Switch.SetOn(d.toggles.useCategoryFolders)
	}
	p.polite.SetDisabled(dis)
	if p.polite.Switch.On != d.toggles.politeMode {
		p.polite.Switch.SetOn(d.toggles.politeMode)
	}
	setError(p.concurrent.input, p.concurrent.err, p.concurrent.hint.Text(), errs.MaxConcurrent)
	setError(p.connections.input, p.connections.err, p.connections.hint.Text(), errs.DefaultConnections)
	setError(p.speedIn, p.speed.err, speedHint, errs.Speed)
	setError(p.dirIn, p.dir.err, dirHint, errs.DefaultDir)
	for _, r := range p.rows {
		re := errs.CategoryRows[r.key]
		setError(r.name, r.nameErr, "", re.Name)
		setError(r.exts, r.extsErr, "", re.Extensions)
		setError(r.dir, r.dirErr, "", re.Dir)
		for _, in := range []*widgets.Input{r.name, r.exts, r.dir} {
			in.SetDisabled(dis)
		}
		r.pick.SetDisabled(dis, false)
		r.trash.SetDisabled(dis, false)
	}
	p.listErr.set(errs.Categories)
	if row := p.listErr.Parent(); row != nil {
		row.SetHidden(errs.Categories == "")
	}
	p.syncCount()
}
