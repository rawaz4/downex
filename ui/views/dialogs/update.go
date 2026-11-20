package dialogs

import (
	"context"
	"math"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/confirm"
	"downex/ui/core/text"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// Update is the pop-up that offers an update downexd's automatic check found
// (docs/M6-SPEC.md §4), in the look of the Restart dialog: the version, its notes when the
// update description has any, and Later or Update now. Update now runs the install that
// Settings → Updates runs (InstallUpdate: downexd downloads and verifies the package, pauses
// the downloads and installs it; Windows runs the new Setup silently and restarts Downex,
// macOS replaces Downex.app and starts it again); the dialog says what happens meanwhile and
// why it failed. Later, Escape and the close button put the offer off until the next
// automatic check (state.UpdateStore.Later). It opens by itself, so it takes no focus and
// ignores Update now right after it appeared (confirm.Guard), as the confirmation dialog does.
type Update struct {
	*views.Dialog

	app   *views.App
	store *state.UpdateStore
	info  *downexv1.UpdateInfo

	tone   *tone
	notes  *views.Note
	status *kit.Element
	// prog shows the running install: a bar and what it does (hidden while none runs).
	prog     *kit.Element
	progBar  *widgets.ProgressBar
	progLine *views.Text
	later    *widgets.Button
	submit   *widgets.Button
	close    *widgets.Button

	openedAt time.Time
	busy     bool
	done     bool
	// Guard is how long Update now is ignored after the dialog appeared (0: confirm.Guard;
	// negative: never, for tests).
	Guard time.Duration
}

// UpdateText is everything the pop-up says of an offer.
type UpdateText struct {
	Title, Description string
	// Notes are the update description's notes ("" for none).
	Notes string
	// Later and Submit are the footer's buttons; Busy is Submit's label while the update
	// downloads; Installing is the note once downexd started installing it; Failed starts
	// the note of a failure (downexd's reason follows).
	Later, Submit, Busy, Installing, Failed string
}

// UpdateTexts is the pop-up's texts for info (versions and notes with their bidi controls
// made visible, as Settings → Updates shows them).
func UpdateTexts(info *downexv1.UpdateInfo) UpdateText {
	latest := text.EscapeBidi(info.GetLatestVersion())
	t := UpdateText{
		Title:       "Downex " + latest + " is available",
		Description: "Downex pauses running downloads, installs the update and starts again.",
		Notes:       text.EscapeBidi(info.GetNotes()),
		Later:       "Later",
		Submit:      "Update now",
		Busy:        "Downloading the update…",
		Installing:  "Downex is installing version " + latest + " and closes for it. It starts again when the installation is done.",
		Failed:      "The update could not be installed",
	}
	if cur := info.GetCurrentVersion(); cur != "" {
		t.Description = "You have version " + text.EscapeBidi(cur) + ". " + t.Description
	}
	return t
}

// NewUpdate makes the pop-up for the offer info of store.
func NewUpdate(app *views.App, store *state.UpdateStore, info *downexv1.UpdateInfo) *Update {
	u := &Update{app: app, store: store, info: info}
	t := UpdateTexts(info)
	u.Dialog = app.NewDialog(views.ModalUpdate, widgets.DialogOptions{
		Title: t.Title, Description: t.Description, Size: widgets.DialogSm, FocusSelf: true,
		OnSubmit: u.Submit, OnClose: u.putOff,
	})
	u.tone = newTone(toneInfo, "arrow-down")
	u.SetIcon(u.tone)
	if t.Notes != "" {
		u.notes = views.NewNote(views.NotePlain, t.Notes)
		u.notes.Style.MaxHeight = kit.Rem(9)
		u.notes.Style.Overflow = kit.OverflowHidden
		u.Body.Add(u.notes)
	}
	u.status = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}}
	u.status.Semantics.Live = kit.LivePolite
	u.status.SetHidden(true)
	u.prog = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(6), MinWidth: kit.Px(0)}}
	u.prog.Semantics.Live = kit.LivePolite
	u.progBar = widgets.NewProgressBar("Update progress", false)
	u.progBar.Set(0, widgets.BarDownloading) // no hatch animation while it is hidden
	u.progLine = views.NewParagraph("", kit.Font{Family: tokens.FontUI, Size: kit.Rem(0.8125), Weight: 400, LineHeight: 1.4, Tabular: true}, views.InkMuted)
	u.prog.Add(u.progBar, u.progLine)
	u.prog.SetHidden(true)
	u.Body.Add(u.prog, u.status)

	u.later = footButton(t.Later, widgets.Secondary, u.Later)
	u.submit = footButton(t.Submit, widgets.Primary, u.Submit)
	u.SetFooter(nil, u.later, u.submit)
	u.openedAt = u.now()
	return u
}

// Info is the offer shown.
func (u *Update) Info() *downexv1.UpdateInfo { return u.info }

// Buttons are Later and Update now (Close alone once the update installs).
func (u *Update) Buttons() (later, submit *widgets.Button) { return u.later, u.submit }

// CloseButton is the footer's Close once the update installs (nil before).
func (u *Update) CloseButton() *widgets.Button { return u.close }

// Status is the note under the notes: why the install failed, or that it runs (nil while
// there is none).
func (u *Update) Status() *views.Note {
	for _, c := range u.status.Children() {
		if n, ok := c.Widget().(*views.Note); ok {
			return n
		}
	}
	return nil
}

// Refresh shows the running install's progress that downexd reports (the store's Progress):
// the bar, how much has arrived and what comes next.
func (u *Update) Refresh() {
	p := u.store.Progress()
	if p == nil {
		if !u.busy {
			u.prog.SetHidden(true)
		}
		return
	}
	u.prog.SetHidden(false)
	u.progBar.Set(state.UpdateFraction(p), widgets.BarDownloading)
	u.progLine.SetText(state.UpdateProgressText(p))
}

// Busy reports that the install runs.
func (u *Update) Busy() bool { return u.busy }

func (u *Update) now() time.Time {
	if w := u.app.Window; w != nil {
		return w.Now()
	}
	return time.Now()
}

// tooSoon: activation right after the pop-up appeared by itself is not a decision about it.
func (u *Update) tooSoon() bool {
	g := u.Guard
	if g == 0 {
		g = confirm.Guard
	}
	return g > 0 && u.now().Sub(u.openedAt) < g
}

// Later puts the offer off and closes the pop-up at once.
func (u *Update) Later() {
	if u.busy {
		return
	}
	u.putOff()
	u.Dialog.Remove()
}

// putOff is Later's record (also Escape's and the close button's, after the closing motion).
// An update that started installing is not put off.
func (u *Update) putOff() {
	if !u.done {
		u.store.Later(u.info)
	}
}

// Submit is Update now: the button reads "Downloading the update…" until downexd answers;
// then the dialog says that Downex is installing the update, or why it could not.
func (u *Update) Submit() {
	if u.busy || u.done || !u.Shown() || u.tooSoon() {
		return
	}
	u.setBusy(true)
	u.showStatus(nil)
	u.prog.SetHidden(false)
	u.progBar.Set(math.NaN(), widgets.BarDownloading)
	u.progLine.SetText("Getting the update ready…")
	u.Refresh()
	u.app.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), host.InstallTimeout)
		defer cancel()
		ok := u.store.Install(ctx)
		failure := u.store.Failure()
		u.app.UI(func() {
			u.setBusy(false)
			u.prog.SetHidden(true)
			if ok {
				u.installing()
				return
			}
			msg := UpdateTexts(u.info).Failed + "."
			if failure != "" {
				msg = UpdateTexts(u.info).Failed + ": " + text.EscapeBidi(failure)
			}
			n := views.NewNote(views.NoteFail, msg)
			n.Semantics.Role = kit.RoleAlert
			u.showStatus(n)
		})
	})
}

// installing shows that downexd installs the update (it closes Downex for it): Close alone.
func (u *Update) installing() {
	u.done = true
	u.showStatus(views.NewNote(views.NoteInfo, UpdateTexts(u.info).Installing))
	u.close = footButton("Close", widgets.Secondary, func() { u.Dialog.Remove() })
	u.SetFooter(nil, u.close)
	u.app.Window.Focus(u.close)
}

func (u *Update) showStatus(n *views.Note) {
	u.status.RemoveAll()
	if n != nil {
		u.status.Add(n)
	}
	u.status.SetHidden(n == nil)
}

func (u *Update) setBusy(b bool) {
	u.busy = b
	u.SetBusy(b)
	u.later.SetDisabled(b, false)
	u.submit.SetDisabled(b, false)
	label := UpdateTexts(u.info).Submit
	if b {
		label = UpdateTexts(u.info).Busy
	}
	u.submit.SetText(label)
}
