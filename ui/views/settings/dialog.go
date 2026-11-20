package settings

import (
	"context"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// ThemeStore is what Appearance changes for the theme: today's theme store (state.Theme in the
// window; tests give their own).
type ThemeStore interface {
	Choice() host.ThemeChoice
	Set(host.ThemeChoice)
}

// DensityStore is what Appearance changes for the list density (state.Density in the window).
type DensityStore interface {
	Choice() state.DensityChoice
	Set(state.DensityChoice)
}

// Options are what the composition gives the dialog beyond the App and the request
// (SettingsDialog.svelte's props that App.svelte fills).
type Options struct {
	// OnSaved hears the settings as saved (App.svelte's onsaved: applySettings and the
	// "Settings saved" toast); nil does exactly that through the App.
	OnSaved func(*downexv1.Settings)
	// OnClose hears the dialog closing (onclose: App.svelte's dialog = null); the dialog host
	// already forgets it.
	OnClose func()
	// Theme and Density are the stores Appearance changes (nil: the App's).
	Theme   ThemeStore
	Density DensityStore
}

// toggles are the settings' on/off rows the dialog itself keeps (SettingsDialog's toggles).
type toggles struct {
	useCategoryFolders, confirmBrowserDownloads, notifyOnComplete, notifyOnFailure bool
	launchAtLogin, showTrayIcon, showProgressWindow, showCompleteDialog            bool
	politeMode                                                                     bool
}

// Dialog is SettingsDialog.svelte (DESIGN-SYSTEM.md §3.20, §4.9): a dialog of 70 × 46 rem (less
// where the window has less room) that keeps its size between its pages, a side navigation (a
// vertical tablist; a row of tabs when the dialog is narrow) and a content pane that scrolls.
// Save writes the settings downexd keeps, edited on the message GetSettings returned (so the
// fields this build does not know are written back); passwords go only through SetSecret after
// the save. Appearance (theme, density) belongs to this computer and applies at once.
type Dialog struct {
	*views.Dialog
	app  *views.App
	opts Options
	req  views.SettingsRequest
	skin skin

	page      validate.SettingsPage
	loading   bool
	loadError string
	saveError string
	busy      bool
	attempted bool
	// base is the settings as loaded: fields this dialog does not show are written back from it.
	base               *downexv1.Settings
	maxConcurrent      string
	speedValue         string
	speedUnit          format.RateUnit
	defaultDir         string
	defaultConnections string
	categories         []validate.CategoryDraft
	toggles            toggles
	network            validate.NetworkForm
	integration        validate.IntegrationForm
	renewLinks         downexv1.RenewLinks // Downloads → Expired links (DESKTOP-SPEC §10)
	updates            validate.UpdatesForm
	video              validate.VideoForm
	proxyPassword      string
	clearProxyPassword bool
	// loadedLogins are the site logins as loaded: removing one also removes its password.
	loadedLogins []*downexv1.SiteLogin
	// loginEditor is Network's site login editor (kept across pages; Save applies it).
	loginEditor      *validate.SiteLoginEditor
	loginEditorError validate.SiteLoginEditorErrors
	// loginEditorBlocked: Save found the editor holding a login that cannot be added.
	loginEditorBlocked bool
	nextKey            int

	shell *shell
	view  pageView
	// paneW is the pane's content width (the settings-pane container queries), scale the
	// window's, as the last layout found them.
	paneW kit.DIP
	scale kit.Scale
	foot  *footMessages
	// cancelBtn and saveBtn are the footer's buttons.
	cancelBtn, saveBtn *widgets.Button
	// updates page state that outlives the page (none: Svelte's page state is per mount).
	gone bool
}

// pageView is a page's content: it shows the dialog's state and refreshes when it changes.
type pageView interface {
	kit.Widget
	// sync shows the dialog's state again (errors, loading).
	sync()
}

// focusTarget is a page that can put the focus on a field asked for.
type focusTarget interface {
	// focusField is the field to focus for the request, or nil.
	focusField(f validate.SettingsField) kit.Widget
}

// The pages of the side navigation.
var pages = []struct {
	id    validate.SettingsPage
	label string
	icon  string
}{
	{validate.PageGeneral, "General", "gear"},
	{validate.PageDownloads, "Downloads", "arrow-down"},
	{validate.PageNetwork, "Network", "globe"},
	{validate.PageIntegration, "Integration", "plug"},
	{validate.PageVideo, "Video sites", "stream"},
	{validate.PageAppearance, "Appearance", "density"},
	{validate.PageUpdates, "Updates", "refresh"},
	{validate.PageAbout, "About", "info"},
}

// New makes the Settings dialog for a request (App.svelte's openSettings): it starts reading the
// settings at once; show it with app.Dialogs.Show.
func New(app *views.App, req views.SettingsRequest, opts Options) *Dialog {
	d := &Dialog{app: app, opts: opts, req: req, loading: true, nextKey: 1}
	switch {
	case req.AddLogin != "":
		d.page = validate.PageNetwork
	case req.Page != "":
		d.page = req.Page
	case req.FocusField == validate.FieldGlobalSpeedLimit:
		d.page = validate.PageDownloads
	default:
		d.page = validate.PageGeneral
	}
	if d.opts.Theme == nil && app.Theme != nil {
		d.opts.Theme = app.Theme
	}
	if d.opts.Density == nil && app.Density != nil {
		d.opts.Density = app.Density
	}
	d.skin = skinFor(app.Window.Environment())
	curSkin = d.skin
	d.fillForms(state.DefaultSettings())
	dw, dh := d.skin.dialogSize()
	d.Dialog = app.NewDialog(views.ModalSettings, widgets.DialogOptions{
		Title: "Settings", Width: dw, FixedHeight: dh, Flush: true,
		OnClose: d.closed, OnSubmit: d.submit,
	})
	d.shell = newShell(d)
	d.Body.Add(d.shell)
	d.foot = newFootMessages()
	d.cancelBtn = widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, d.cancel)
	d.saveBtn = widgets.NewButton("Save", "", widgets.Primary, widgets.SizeMd, d.submit)
	d.SetFooter(d.foot, d.cancelBtn, d.saveBtn)
	d.showView()
	d.syncFooter()
	d.load()
	return d
}

// Page is the page shown.
func (d *Dialog) Page() validate.SettingsPage { return d.page }

// Pane is the content pane (the tab panel), which scrolls.
func (d *Dialog) Pane() *widgets.ScrollPane { return d.shell.pane }

// Nav is the side navigation (the tablist).
func (d *Dialog) Nav() *kit.Element { return &d.shell.nav.Element }

// closed is the dialog's OnClose (Escape, the close button, after the closing motion).
func (d *Dialog) closed() {
	d.gone = true
	if old, ok := d.view.(disposer); ok {
		old.dispose()
	}
	if d.opts.OnClose != nil {
		d.opts.OnClose()
	}
}

// cancel is the Cancel button: onclose at once.
func (d *Dialog) cancel() {
	if d.busy {
		return
	}
	d.Remove()
	d.closed()
}

// load reads the settings (onMount), then fills the pages, opens the editor asked for and puts
// the focus where the request asks.
func (d *Dialog) load() {
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		resp, err := d.app.Daemon.GetSettings(ctx)
		d.app.UI(func() {
			if d.gone {
				return
			}
			switch {
			case err != nil:
				d.loadError = state.Message(err)
			case resp.GetSettings() == nil:
				d.loadError = "downexd answered without its settings"
			default:
				d.fill(resp.GetSettings())
			}
			d.loading = false
			// A server asked for credentials (auth_required): the editor of a new login for
			// it, opened once (it then lives on until applied or cancelled, across pages).
			if d.req.AddLogin != "" {
				e := validate.NewLoginEditor(d.req.AddLogin)
				d.loginEditor = &e
			}
			d.showView()
			d.syncFooter()
			d.shell.syncTabs()
			d.focusAfterLoad()
		})
	})
}

// focusAfterLoad: the fields were disabled while loading, so the focus goes to the field asked
// for, else to the chosen page's tab.
func (d *Dialog) focusAfterLoad() {
	var target kit.Widget
	if ft, ok := d.view.(focusTarget); ok {
		switch {
		case d.req.AddLogin != "":
			target = ft.focusField("login-user")
		case d.req.FocusField != "":
			target = ft.focusField(d.req.FocusField)
		}
	}
	if target == nil || target.Base().Disabled {
		target = d.shell.tab(d.page)
	}
	d.focus(target)
	if in, ok := target.(*widgets.Input); ok && d.req.FocusField != "" {
		in.SelectAll()
	}
}

// fill takes loaded settings into every page's form.
func (d *Dialog) fill(s *downexv1.Settings) {
	d.base = s
	d.fillForms(s)
	d.loadedLogins = s.GetSiteLogins()
}

func (d *Dialog) fillForms(s *downexv1.Settings) {
	if d.base == nil {
		d.base = s
	}
	d.maxConcurrent = format.Num(float64(s.GetMaxConcurrentDownloads()))
	d.speedValue, d.speedUnit = format.SplitRate(float64(s.GetGlobalSpeedLimit()))
	d.defaultDir = s.GetDefaultDir()
	d.defaultConnections = format.Num(float64(s.GetDefaultMaxConnections()))
	d.categories = validate.CategoryDrafts(s.GetCategories(), d.nextKey)
	d.nextKey += len(s.GetCategories())
	d.toggles = toggles{
		useCategoryFolders: s.GetUseCategoryFolders(), confirmBrowserDownloads: s.GetConfirmBrowserDownloads(),
		notifyOnComplete: s.GetNotifyOnComplete(), notifyOnFailure: s.GetNotifyOnFailure(),
		launchAtLogin: s.GetLaunchAtLogin(), showTrayIcon: s.GetShowTrayIcon(),
		showProgressWindow: s.GetShowProgressWindow(), showCompleteDialog: s.GetShowCompleteDialog(),
		politeMode: s.GetPoliteMode(),
	}
	d.network = validate.NewNetworkForm(s, d.nextKey)
	d.nextKey += len(s.GetHostRules()) + len(s.GetSiteLogins())
	d.integration = validate.NewIntegrationForm(s)
	d.updates = validate.NewUpdatesForm(s)
	d.video = validate.NewVideoForm(s)
	d.renewLinks = s.GetRenewLinks() // UNSPECIFIED (auto) is written back as it was
	d.proxyPassword = ""
	d.clearProxyPassword = false
}

func (d *Dialog) newKey() int {
	k := d.nextKey
	d.nextKey++
	return k
}

// form is the dialog's form for validation.
func (d *Dialog) form() validate.SettingsForm {
	t := d.toggles
	network, integration, updates, video := d.network, d.integration, d.updates, d.video
	return validate.SettingsForm{
		MaxConcurrent: d.maxConcurrent, SpeedValue: d.speedValue, SpeedUnit: d.speedUnit,
		SpeedLimitBytes: d.base.GetGlobalSpeedLimit(), DefaultDir: d.defaultDir, DefaultConnections: d.defaultConnections,
		Categories: d.categories, UseCategoryFolders: t.useCategoryFolders, ConfirmBrowserDownloads: t.confirmBrowserDownloads,
		NotifyOnComplete: t.notifyOnComplete, NotifyOnFailure: t.notifyOnFailure, LaunchAtLogin: t.launchAtLogin,
		ShowTrayIcon: t.showTrayIcon, ShowProgressWindow: t.showProgressWindow, ShowCompleteDialog: t.showCompleteDialog,
		PoliteMode: t.politeMode, Network: &network, Integration: &integration, Updates: &updates, Video: &video,
		RenewLinks: d.renewLinks,
	}
}

// result validates the form against the loaded settings.
func (d *Dialog) result() (*downexv1.Settings, validate.SettingsFormErrors, bool) {
	return validate.ValidateSettingsForm(d.form(), d.base)
}

// errors are the messages shown: after a save was attempted, while the form has some.
func (d *Dialog) errors() validate.SettingsFormErrors {
	if !d.attempted {
		return validate.SettingsFormErrors{}
	}
	_, errs, ok := d.result()
	if ok {
		return validate.SettingsFormErrors{}
	}
	return errs
}

// pageHasErrors reports a page whose fields need attention (marked in the side navigation).
func (d *Dialog) pageHasErrors(errs validate.SettingsFormErrors, p validate.SettingsPage) bool {
	on := validate.ErrorsOnPage(errs, p)
	return !on.Empty() || p == validate.PageNetwork && d.loginEditorBlocked && d.loginEditor != nil
}

// changed is every edit: the errors, marks and footer follow (Svelte's derived state).
func (d *Dialog) changed() {
	if d.loginEditor == nil {
		d.loginEditorBlocked = false
	}
	if d.attempted || d.loginEditorBlocked {
		d.view.sync()
		d.shell.syncTabs()
		d.syncFooter()
	}
}

// showView mounts the page shown ({#key page}): a new view of it.
func (d *Dialog) showView() {
	if old, ok := d.view.(disposer); ok {
		old.dispose()
	}
	d.view = newPage(d, d.page)
	d.shell.setPage(d.view)
}

// showPage shows a page (and focuses its tab with focusTab).
func (d *Dialog) showPage(id validate.SettingsPage, focusTab bool) {
	if d.page != id {
		d.page = id
		d.shell.scrollTop()
		d.showView()
		d.shell.syncTabs()
	}
	if focusTab {
		d.app.Window.Focus(d.shell.tab(id))
	}
}

// syncFooter shows the footer's messages and buttons.
func (d *Dialog) syncFooter() {
	_, _, ok := d.result()
	var msgs []string
	switch {
	case d.loginEditorBlocked && d.loginEditor != nil:
		msgs = append(msgs, "Add or cancel the site login you are editing (Network), then save.")
	case d.attempted && !ok:
		msgs = append(msgs, "Some settings need attention.")
	}
	if d.saveError != "" {
		msgs = append(msgs, d.saveError)
	}
	d.foot.set(msgs)
	d.cancelBtn.SetDisabled(d.busy, false)
	d.saveBtn.SetDisabled(d.busy || d.loading || d.loadError != "", false)
	label := "Save"
	if d.busy {
		label = "Saving…"
	}
	d.saveBtn.SetText(label)
	d.SetBusy(d.busy)
}

// takeLoginEditor runs at Save with the login editor open: a login entered there is added to
// the list (its password is stored with the others); one that cannot be added stops the save.
// An editor holding nothing new is closed. It reports whether saving may go on.
func (d *Dialog) takeLoginEditor() bool {
	e := d.loginEditor
	if e == nil {
		return true
	}
	if !validate.LoginEditorChanged(*e, d.network.SiteLogins) {
		d.loginEditor = nil
		d.loginEditorError = validate.SiteLoginEditorErrors{}
		return true
	}
	logins, _, errs, ok := validate.ApplyLoginEditor(d.network.SiteLogins, *e, d.newKey)
	if ok {
		d.network.SiteLogins = logins
		d.loginEditor = nil
		d.loginEditorError = validate.SiteLoginEditorErrors{}
		d.loginEditorBlocked = false
		return true
	}
	d.loginEditorError = errs
	d.loginEditorBlocked = true
	d.showPage(validate.PageNetwork, false)
	d.view.sync()
	if n, ok := d.view.(*networkPage); ok {
		if f := n.editorInvalid(); f != nil {
			d.focus(f)
		}
	}
	return false
}

// submit is Save (and Enter in a single-line field).
func (d *Dialog) submit() {
	if d.loading || d.loadError != "" || d.busy {
		return
	}
	d.attempted = true
	d.saveError = ""
	if !d.takeLoginEditor() {
		d.shell.syncTabs()
		d.syncFooter()
		return
	}
	settings, errs, ok := d.result()
	if !ok {
		// Stay on this page when it has an error, else show the first page that has one.
		target, found := validate.FirstErrorPage(validate.ErrorsOnPage(errs, d.page))
		if !found {
			target, found = validate.FirstErrorPage(errs)
		}
		if found {
			d.showPage(target, false)
		}
		d.view.sync()
		d.shell.syncTabs()
		d.syncFooter()
		if inv, ok := d.view.(interface{ firstInvalid() kit.Widget }); ok {
			if f := inv.firstInvalid(); f != nil {
				d.focus(f)
			}
		}
		return
	}
	// an unchanged view of the page: the errors are gone
	d.view.sync()
	d.shell.syncTabs()
	d.busy = true
	d.syncFooter()
	logins := d.network.SiteLogins
	proxyPassword, clearProxy := d.proxyPassword, d.clearProxyPassword
	loaded := d.loadedLogins
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		saved, err := host.SaveSettings(ctx, d.app.Daemon, settings)
		var failed []string
		fresh := saved
		if err == nil {
			failed = d.storeSecrets(saved, logins, loaded, proxyPassword, clearProxy)
			if len(failed) > 0 || hasSecretChanges(logins, loaded, proxyPassword, clearProxy) {
				ctx2, cancel2 := d.app.CallContext()
				if resp, err2 := d.app.Daemon.GetSettings(ctx2); err2 == nil && resp.GetSettings() != nil {
					fresh = resp.GetSettings()
				}
				cancel2()
			}
		}
		d.app.UI(func() { d.saveDone(fresh, err, failed) })
	})
}

// saveDone shows how the save went.
func (d *Dialog) saveDone(fresh *downexv1.Settings, err error, failed []string) {
	d.busy = false
	if d.gone {
		return
	}
	if err != nil {
		d.saveError = "Could not save: " + state.Message(err)
		d.syncFooter()
		return
	}
	if len(failed) > 0 {
		// The settings are saved: show what they are now, and what is missing.
		d.fill(fresh)
		d.attempted = false
		d.saveError = "Settings saved, but " + strings.Join(failed, "; ") + ". Enter the password again."
		d.showView()
		d.shell.syncTabs()
		d.syncFooter()
		d.saved(fresh)
		return
	}
	d.saved(fresh)
	d.Remove()
	d.closed()
}

// saved is onsaved.
func (d *Dialog) saved(s *downexv1.Settings) {
	if d.opts.OnSaved != nil {
		d.opts.OnSaved(s)
		return
	}
	if d.app.Main != nil {
		d.app.Main.SetSettings(s)
	}
	d.app.Success("Settings saved")
}

// hasSecretChanges reports a password set or removed in the dialog.
func hasSecretChanges(logins []validate.SiteLoginDraft, loaded []*downexv1.SiteLogin, proxyPassword string, clearProxy bool) bool {
	if proxyPassword != "" || clearProxy {
		return true
	}
	for _, l := range logins {
		if l.Password != "" || l.ClearPassword {
			return true
		}
	}
	for _, l := range loaded {
		if !l.GetHasPassword() {
			continue
		}
		gone := true
		for _, x := range logins {
			if x.ID == l.GetId() {
				gone = false
				break
			}
		}
		if gone {
			return true
		}
	}
	return false
}

// storeSecrets runs after the settings are saved: it stores new passwords, removes cleared ones
// and those of removed logins. A new login gets its id from downexd: it is found in the saved
// settings by its address and user name. It returns what could not be stored. It runs off the
// UI thread; it never logs or returns a password.
func (d *Dialog) storeSecrets(saved *downexv1.Settings, logins []validate.SiteLoginDraft, loaded []*downexv1.SiteLogin, proxyPassword string, clearProxy bool) []string {
	var failed []string
	call := func(f func(context.Context) error) error {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		return f(ctx)
	}
	attempt := func(what string, f func(context.Context) error) {
		if err := call(f); err != nil {
			failed = append(failed, what+" could not be stored ("+state.Message(err)+")")
		}
	}
	dm := d.app.Daemon
	setSecret := func(kind, id, pw string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := dm.SetSecret(ctx, &downexv1.SetSecretRequest{Kind: kind, Id: id, Password: pw})
			return err
		}
	}
	clearSecret := func(kind, id string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := dm.ClearSecret(ctx, &downexv1.ClearSecretRequest{Kind: kind, Id: id})
			return err
		}
	}
	if proxyPassword != "" {
		attempt("the proxy password", setSecret("proxy", "", proxyPassword))
	} else if clearProxy {
		attempt("the removal of the proxy password", clearSecret("proxy", ""))
	}
	known := map[string]bool{}
	for _, l := range loaded {
		known[l.GetId()] = true
	}
	claimed := map[string]bool{}
	for _, l := range logins {
		if l.Password == "" && !l.ClearPassword {
			continue
		}
		id := l.ID
		if id == "" {
			for _, s := range saved.GetSiteLogins() {
				if s.GetId() != "" && !known[s.GetId()] && !claimed[s.GetId()] &&
					s.GetUrlPattern() == text.TrimJS(l.URLPattern) && s.GetUsername() == text.TrimJS(l.Username) {
					id = s.GetId()
					break
				}
			}
		}
		what := "the password for " + text.EscapeBidi(text.TrimJS(l.URLPattern))
		if id == "" {
			if l.Password != "" {
				failed = append(failed, what+" could not be stored (downexd did not return the new login)")
			}
			continue
		}
		claimed[id] = true
		if l.Password != "" {
			attempt(what, setSecret("site_login", id, l.Password))
		} else if l.ClearPassword {
			attempt("the removal of "+what, clearSecret("site_login", id))
		}
	}
	// Passwords of removed logins are removed too (downexd may have done it already).
	for _, l := range loaded {
		if !l.GetHasPassword() {
			continue
		}
		still := false
		for _, s := range saved.GetSiteLogins() {
			if s.GetId() == l.GetId() {
				still = true
				break
			}
		}
		if !still {
			_ = call(clearSecret("site_login", l.GetId()))
		}
	}
	return failed
}

// platform is the host's system as the pages word it (SettingsDialog's platform).
func (d *Dialog) platform() string {
	switch {
	case d.app.Caps.WakeTimers:
		return "windows"
	case !d.app.Caps.Hibernate:
		return "mac"
	}
	return "other"
}

// footMessages is the footer's start: its alerts (.foot-msg: an alert icon and the text in
// --danger, var(--type-cell)/1.35, 7 px apart).
type footMessages struct {
	kit.Element
	rows []*kit.Element
}

func newFootMessages() *footMessages {
	f := &footMessages{}
	f.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}
	return f
}

func (f *footMessages) set(msgs []string) {
	same := len(msgs) == len(f.rows)
	if same {
		for i, m := range msgs {
			if f.rows[i].Semantics.Name != m {
				same = false
			}
		}
	}
	if same {
		return
	}
	f.RemoveAll()
	f.rows = f.rows[:0]
	for _, m := range msgs {
		ic := icon("alert", 14, views.InkDanger)
		ic.Style.Margin = kit.Edges{Top: kit.Px(1)}
		ic.Style.Flex = kit.FlexNone
		t := para(m, fontFoot, views.InkDanger)
		t.Style.MinWidth = kit.Px(0)
		t.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		r := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: kit.Px(7), MinWidth: kit.Px(0)}, ic, t)
		r.Semantics.Role = kit.RoleAlert
		r.Semantics.Name = m
		r.Semantics.Live = kit.LiveAssertive
		f.rows = append(f.rows, r)
		f.Add(r)
	}
}

var _ = tokens.TypeBody

// DismissModal is views.Modal's: a newer modal replaces the dialog (it goes at once, without
// OnClose).
func (d *Dialog) DismissModal() {
	d.Dialog.DismissModal()
	d.gone = true
	if old, ok := d.view.(disposer); ok {
		old.dispose()
	}
}

// focus puts the keyboard on w and, when it is in the pane out of view, scrolls the pane to
// centre it, as focus() scrolls a scroll container in Chromium (clamped to the pane's ends).
func (d *Dialog) focus(w kit.Widget) {
	if w == nil {
		return
	}
	win := d.app.Window
	win.Focus(w)
	pane := d.shell.pane
	if !pane.Content.Contains(w.Base()) {
		return
	}
	win.AfterLayout(func() {
		e := w.Base()
		if e.Window() == nil {
			return
		}
		r := e.WindowBounds()
		view := pane.WindowBounds()
		if r.Min.Y >= view.Min.Y && r.Max.Y <= view.Max.Y {
			return
		}
		top := r.Min.Y - pane.Content.WindowBounds().Min.Y
		pane.ScrollTo(top + r.Height()/2 - view.Height()/2)
	})
}
