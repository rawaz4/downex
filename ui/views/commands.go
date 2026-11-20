package views

import (
	"slices"
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/palette"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
)

// Commands is the registry the palette, the keyboard shortcuts and the download context menu
// share: App.svelte's commands (ui/core/palette has their list and wording), its
// onWindowKeydown and its menuItems and onMenuSelect. Each entry runs an Openers or Actions
// field of the App, or a store's setter, as App.svelte does; extra handlers (Handle) may add
// to or replace them. Its methods belong to the UI thread.
type Commands struct {
	a        *App
	handlers map[string]func()
}

func newCommands(a *App) *Commands { return &Commands{a: a, handlers: map[string]func(){}} }

// Handle sets what the command id runs, in place of the built-in entry (a lane's own command,
// or a test's).
func (c *Commands) Handle(id string, run func()) { c.handlers[id] = run }

// State is what the palette's command list reads of the window (App.svelte's derived values).
func (c *Commands) State() palette.CommandState {
	a := c.a
	ab := a.Abilities()
	s := palette.CommandState{
		Stale: ab.Stale, CanAdd: ab.CanAdd, CanPause: ab.CanPause, CanResume: ab.CanResume,
		CanPauseAll: ab.CanPauseAll, CanResumeAll: ab.CanResumeAll, CanRemove: ab.CanRemove,
		DetailsCollapsed: a.Details.Collapsed(),
		Density:          string(a.DensityChoice()),
		Theme:            string(a.ThemeChoice()),
		Category:         categories.All,
	}
	settings, loaded := a.Settings()
	s.SettingsLoaded = loaded
	s.GlobalSpeedLimit = settings.GetGlobalSpeedLimit()
	s.PoliteMode = loaded && settings.GetPoliteMode()
	if a.Main == nil {
		return s
	}
	s.Polite, _ = a.Main.Polite.Status()
	ds := a.Main.Downloads
	sel := ds.Selected()
	s.Selected = len(sel)
	for _, d := range sel {
		if d.GetState() == state.StateCompleted {
			s.CompletedSelected++
		}
	}
	if len(sel) == 1 {
		s.Renewable, s.PageKnown = display.Renewable(sel[0]), display.PageURL(sel[0]) != ""
	}
	s.Active = ds.Totals().Active
	s.Index, s.Category, s.Counts = ds.Index(), ds.Category(), ds.Counts()
	s.Queues = a.QueueChoices()
	s.QueueCounts = ds.QueueCounts()
	s.WhenDone = c.whenDoneKnown()
	return s
}

// whenDoneKnown reports that the main queue's post-queue action is known (App.svelte's
// whenDone !== null: the status bar's control and the palette's "When done…" exist).
func (c *Commands) whenDoneKnown() bool {
	q := c.a.Main.Queues
	if !q.Supported() || !q.Loaded() {
		return false
	}
	_, ok := q.Get(state.MainQueueID)
	return ok
}

// List is the palette's commands, in their order.
func (c *Commands) List() []palette.Command { return palette.Commands(c.State()) }

// Run runs a palette command by id, unless the list has it disabled; it reports whether it
// ran.
func (c *Commands) Run(id string) bool {
	for _, cmd := range c.List() {
		if cmd.ID == id {
			if cmd.Disabled {
				return false
			}
			return c.run(id)
		}
	}
	if h, ok := c.handlers[id]; ok { // a lane's own command
		h()
		return true
	}
	return false
}

// selected are the selected downloads' ids.
func (c *Commands) selected() []int64 {
	if c.a.Main == nil {
		return nil
	}
	return SelectedIDs(c.a.Main.Downloads.Selected())
}

// run runs a command, enabled or not (the shortcuts' own rules decide).
func (c *Commands) run(id string) bool {
	if h, ok := c.handlers[id]; ok {
		h()
		return true
	}
	a := c.a
	o, act := &a.Open, &a.Act
	call := func(f func()) bool {
		if f == nil {
			return false
		}
		f()
		return true
	}
	switch id {
	case "add":
		return call(func() { o.Add(AddRequest{}) })
	case "add-video":
		return call(func() { o.Add(AddRequest{Mode: AddStream}) })
	case "rescue":
		return call(func() { o.Rescue(RescueRequest{}) })
	case "pause":
		return call(func() { act.Pause(nil) })
	case "resume":
		return call(func() { act.Resume(nil) })
	case "pause-all":
		return call(act.PauseAll)
	case "resume-all":
		return call(act.ResumeAll)
	case "download-again":
		var done []int64
		if a.Main != nil {
			for _, d := range a.Main.Downloads.Selected() {
				if d.GetState() == state.StateCompleted {
					done = append(done, d.GetId())
				}
			}
		}
		return call(func() { o.Restart(RestartRequest{IDs: done, Reason: RestartManual}) })
	case "new-link", "open-page":
		ids := c.selected()
		if len(ids) != 1 {
			return false
		}
		if id == "open-page" {
			return call(func() { act.OpenPage(ids[0]) })
		}
		return call(func() { o.Renew(RenewRequest{ID: ids[0]}) })
	case "remove":
		return call(func() { o.Remove(RemoveRequest{IDs: c.selected()}) })
	case "select-all":
		return call(act.SelectAll)
	case "limit":
		return call(act.OpenLimitMenu)
	case "polite":
		if act.SetPolite == nil {
			return false
		}
		settings, loaded := a.Settings()
		if !loaded {
			return false
		}
		on := !settings.GetPoliteMode()
		act.SetPolite(on)
		return true
	case "when-done":
		return call(act.OpenWhenDoneMenu)
	case "search":
		return call(act.FocusSearch)
	case "details":
		return call(act.ToggleDetails)
	case "density-comfortable", "density-compact":
		if a.Density == nil {
			return false
		}
		a.Density.Set(state.DensityChoice(strings.TrimPrefix(id, "density-")))
		return true
	case "theme-system", "theme-light", "theme-dark":
		if a.Theme == nil {
			return false
		}
		a.Theme.Set(host.ThemeChoice(strings.TrimPrefix(id, "theme-")))
		return true
	case "settings":
		return call(func() { o.Settings(SettingsRequest{}) })
	case "queues":
		var q int64
		if a.Main != nil {
			q = a.Main.Downloads.QueueFilter()
		}
		return call(func() { o.Queues(QueuesRequest{Queue: q}) })
	case "palette":
		return call(o.Palette)
	}
	if a.Main == nil {
		return false
	}
	ds := a.Main.Downloads
	switch {
	case strings.HasPrefix(id, "cat-"):
		ds.SetCategory(categories.Filter(strings.TrimPrefix(id, "cat-")))
		return true
	case strings.HasPrefix(id, "status-"):
		ds.SetStatusFilter(categories.Status(strings.TrimPrefix(id, "status-")))
		return true
	case strings.HasPrefix(id, "queue-"):
		q, err := strconv.ParseInt(strings.TrimPrefix(id, "queue-"), 10, 64)
		if err != nil {
			return false
		}
		ds.SetQueueFilter(q)
		return true
	}
	return false
}

// KeyContext is what the window's shortcuts need to know about the focused element
// (App.svelte's isEditable and onControl, and a menu of the page header holding the keys).
type KeyContext struct {
	// Editable: it takes text or is a form control (input, textarea, select,
	// contenteditable: fields, check boxes, switches, radios, Select buttons).
	Editable bool
	// OnControl: it is a button, a link, a summary or a menu item, whose Space is its own.
	OnControl bool
	// InMenu: a menu of the page header or the status bar has the keys.
	InMenu bool
}

// KeyContextOf reads the focused element of w by its role.
func KeyContextOf(w *kit.Window) KeyContext {
	var k KeyContext
	f := w.Focused()
	if f == nil {
		return k
	}
	switch f.Semantics.Role {
	case kit.RoleTextField, kit.RoleComboBox, kit.RoleCheckBox, kit.RoleRadioButton, kit.RoleSwitch, kit.RoleSpinButton, kit.RoleSlider:
		k.Editable = true
	case kit.RoleButton, kit.RoleLink, kit.RoleMenuItem, kit.RoleMenuItemCheckBox, kit.RoleMenuItemRadio:
		k.OnControl = true
	}
	for p := f; p != nil; p = p.Parent() {
		if p.Semantics.Role == kit.RoleMenu {
			k.InMenu = true
			break
		}
	}
	return k
}

// HandleKey is App.svelte's onWindowKeydown for a key no element used (kit.WindowOptions.OnKey
// hands them on): Escape closes the context menu; with no dialog, menu or palette open, Ctrl+K
// opens the palette, Ctrl+N Add, Ctrl+, Settings, Ctrl+F the search; outside text, Ctrl+A
// selects all, Delete removes (Shift+Delete with the files), and Space off a control pauses or
// resumes. It reports whether it used the key.
func (c *Commands) HandleKey(e kit.KeyEvent, k KeyContext) bool {
	if e.Action != kit.KeyDown {
		return false
	}
	a := c.a
	h := a.Dialogs
	if h.MenuOpen() && e.Key == kit.KeyEscape {
		if a.Act.CloseMenu != nil {
			a.Act.CloseMenu()
		}
		return true
	}
	if h.DialogOpen() || h.MenuOpen() || h.PaletteOpen() || k.InMenu {
		return false
	}
	mod := e.Mods&(kit.ModCtrl|kit.ModMeta) != 0
	alt := e.Mods&kit.ModAlt != 0
	shift := e.Mods&kit.ModShift != 0
	switch {
	case mod && !alt && !shift && e.Key == kit.KeyK:
		return c.run("palette")
	case mod && !alt && !shift && e.Key == kit.KeyN:
		return c.run("add")
	case mod && !alt && !shift && e.Key == kit.KeyComma: // Shift+, types '<', which is not ','
		return c.run("settings")
	case mod && !alt && e.Key == kit.KeyF:
		return c.run("search")
	}
	if k.Editable {
		return false
	}
	switch {
	case mod && !alt && e.Key == kit.KeyA:
		return c.run("select-all")
	// Delete removes; on the Mac also its delete key, ⌫, and ⌘⌫ (Finder's Move to Trash)
	case e.Key == kit.KeyDelete && !mod,
		kit.MacKeys && e.Key == kit.KeyBackspace && !alt && e.Mods&kit.ModCtrl == 0:
		if a.Open.Remove == nil {
			return false
		}
		a.Open.Remove(RemoveRequest{IDs: c.selected(), DeleteFile: shift})
		return true
	case e.Key == kit.KeySpace && !mod && !k.OnControl:
		if a.Act.TogglePauseResume == nil {
			return false
		}
		a.Act.TogglePauseResume()
		return true
	}
	return false
}

// MenuLabel is the download context menu's accessible name.
const MenuLabel = "Download actions"

// MenuItems are the download context menu's items for the selection (App.svelte's menuItems).
func (c *Commands) MenuItems() []widgets.MenuItem {
	a := c.a
	if a.Main == nil {
		return nil
	}
	ab := a.Abilities()
	stale := ab.Stale
	sel := a.Main.Downloads.Selected()
	var one *downexv1.Download
	if len(sel) == 1 {
		one = sel[0]
	}
	var items []widgets.MenuItem
	special := false
	if one != nil && IsAwaiting(one.GetState()) {
		items = append(items, widgets.MenuItem{ID: "confirm", Label: "Confirm download…", Icon: "confirm", Shortcut: "Enter", Disabled: stale})
		special = true
	}
	if one != nil && HasProgress(one) {
		items = append(items, widgets.MenuItem{ID: "progress", Label: "Show progress", Icon: "progress", Shortcut: "Enter", Disabled: stale})
		special = true
	}
	if !special {
		items = append(items, widgets.MenuItem{ID: "open", Label: "Open", Icon: "open", Shortcut: "Enter", Disabled: one == nil || one.GetState() != state.StateCompleted})
	}
	items = append(items, widgets.MenuItem{ID: "folder", Label: "Open folder", Icon: "folder", Disabled: one == nil})
	toggle := widgets.MenuItem{ID: "toggle", Label: "Resume", Icon: "resume", Shortcut: "Space", Separated: true, Disabled: !ab.CanPause && !ab.CanResume}
	if ab.CanPause {
		toggle.Label, toggle.Icon = "Pause", "pause"
	}
	items = append(items, toggle)
	allDone := len(sel) > 0 && !slices.ContainsFunc(sel, func(d *downexv1.Download) bool { return d.GetState() != state.StateCompleted })
	restart := widgets.MenuItem{ID: "restart", Label: "Restart…", Icon: "restart", Disabled: stale || !slices.ContainsFunc(sel, Restartable)}
	if allDone {
		restart.Label = "Download again…"
	}
	items = append(items, restart)
	if one != nil && display.Renewable(one) { // docs/DESKTOP-SPEC.md §10
		items = append(items, widgets.MenuItem{ID: "renew", Label: "New link…", Icon: "link", Disabled: stale})
		if display.PageURL(one) != "" {
			items = append(items, widgets.MenuItem{ID: "open-page", Label: "Open page", Icon: "external"})
		}
	}
	if one != nil && one.GetErrorCode() == "auth_required" {
		items = append(items, widgets.MenuItem{ID: "add-login", Label: "Add site login…", Icon: "add", Disabled: stale})
	}
	if len(a.QueueChoices()) > 1 {
		items = append(items, widgets.MenuItem{ID: "move-queue", Label: "Move to queue…", Icon: "queue", Disabled: stale || len(sel) == 0})
	}
	items = append(items,
		widgets.MenuItem{ID: "resume-all", Label: "Resume all", Icon: "resume-all", Disabled: !ab.CanResumeAll},
		widgets.MenuItem{ID: "pause-all", Label: "Pause all", Icon: "pause-all", Disabled: !ab.CanPauseAll},
	)
	copyItem := widgets.MenuItem{ID: "copy", Label: "Copy URL", Icon: "copy", Separated: true, Disabled: len(sel) == 0}
	if len(sel) > 1 {
		copyItem.Label = "Copy URLs"
	}
	items = append(items, copyItem,
		widgets.MenuItem{ID: "remove", Label: "Remove…", Icon: "remove", Shortcut: "Del", Separated: true, Disabled: !ab.CanRemove},
		widgets.MenuItem{ID: "remove-delete", Label: "Remove and delete file…", Icon: "remove", Danger: true, Shortcut: "Shift+Del", Disabled: !ab.CanRemove},
	)
	return items
}

// RunMenuItem is App.svelte's onMenuSelect: it closes the menu (focusing the list), then does
// what the item says for the selection as it was.
func (c *Commands) RunMenuItem(id string) {
	a := c.a
	if a.Main == nil {
		return
	}
	sel := a.Main.Downloads.Selected()
	ids := SelectedIDs(sel)
	var one *downexv1.Download
	if len(sel) == 1 {
		one = sel[0]
	}
	if a.Act.CloseMenu != nil {
		a.Act.CloseMenu()
	}
	o, act := &a.Open, &a.Act
	do := func(f func()) {
		if f != nil {
			f()
		}
	}
	switch id {
	case "confirm", "open":
		if one != nil && act.OpenDownload != nil {
			act.OpenDownload(one.GetId())
		}
	case "progress":
		if one != nil && !a.Stale() && act.OpenProgress != nil {
			act.OpenProgress(one.GetId())
		}
	case "pause-all":
		do(act.PauseAll)
	case "resume-all":
		do(act.ResumeAll)
	case "folder":
		if one != nil && act.OpenFolder != nil {
			act.OpenFolder(one.GetId())
		}
	case "toggle":
		do(act.TogglePauseResume)
	case "restart":
		if o.Restart != nil {
			o.Restart(RestartRequest{IDs: ids, Reason: RestartManual})
		}
	case "renew":
		if one != nil && o.Renew != nil {
			o.Renew(RenewRequest{ID: one.GetId()})
		}
	case "open-page":
		if one != nil && act.OpenPage != nil {
			act.OpenPage(one.GetId())
		}
	case "add-login":
		if one != nil && act.AddSiteLogin != nil {
			act.AddSiteLogin(one.GetId())
		}
	case "move-queue":
		switch {
		case a.Stale():
			a.Unavailable()
		case len(ids) > 0 && o.MoveToQueue != nil:
			o.MoveToQueue(MoveRequest{IDs: ids})
		}
	case "copy":
		if act.CopyURLs != nil {
			act.CopyURLs(ids)
		}
	case "remove", "remove-delete":
		if o.Remove != nil {
			o.Remove(RemoveRequest{IDs: ids, DeleteFile: id == "remove-delete"})
		}
	}
}
