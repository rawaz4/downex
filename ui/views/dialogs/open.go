package dialogs

import (
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// Open is OpenDialog.svelte: the question before a completed download whose file can run
// code, whose name hides its type with bidi control characters, or whose file check found
// that it is not what its name says (DANGER, docs/DESKTOP-SPEC.md §9.7) is opened
// (App.svelte's openDownload). "Run this program?" (or "Open this program with another app?" for Open
// with…) with the danger tone and the alert, or "Open this file?" with the info tone and the
// open icon; the name in a monospaced box (bidi controls shown as ⟨U+…⟩) and, when the name
// hides controls, the warning in --danger. Cancel is focused first.
//
// It is the window's own question. The host's risk gate (host.Files, its ConfirmText in a
// native Yes/No box) still asks before a program runs, as today.
type Open struct {
	*views.Dialog

	app     *views.App
	id      int64
	withApp bool
	confirm func()

	name    *views.Text
	warning *views.Text
	cancel  *widgets.Button
	submit  *widgets.Button
}

// DisguisedTitle is the Open dialog's title for a file that is not what its name says
// (docs/DESKTOP-SPEC.md §9.7).
const DisguisedTitle = "This file is not what its name says"

// OpenTexts are OpenDialog's title, description and action for a file. A disguised file
// (its file check is DANGER) is asked about with its own title, the check's summary and
// detail as the description, and "… anyway" actions.
func OpenTexts(executable, withApp bool, disguise string) (title, description, action string) {
	if disguise != "" {
		action = "Open anyway"
		switch {
		case withApp:
			action = "Choose app anyway…"
		case executable:
			action = "Run anyway"
		}
		return DisguisedTitle, disguise, action
	}
	title = "Open this file?"
	description = "Check the file name before opening it."
	if executable {
		title = "Run this program?"
		description = "Opening it runs the program on this computer. Programs from the internet can harm your computer; open it only if you trust where it came from."
		if withApp {
			title = "Open this program with another app?"
			description = "It is a program or script, and the app you choose may run it on this computer. Programs from the internet can harm your computer; open it only if you trust where it came from."
		}
	}
	action = "Open"
	switch {
	case withApp:
		action = "Choose app…"
	case executable:
		action = "Run"
	}
	return title, description, action
}

// HiddenControlsWarning is OpenDialog's warning about a name with bidi control characters.
const HiddenControlsWarning = "The name contains hidden text-direction characters, shown here as ⟨U+…⟩. They can make a file look like a different type than it is."

// NewOpen makes the open confirmation for req. confirm runs when the user confirms, before
// the dialog goes ("confirm first: closing clears the caller's dialog state"); nil is
// App.svelte's openFile, a toast when it fails. Open with… (WithApp, the progress window's
// question) gives its own confirm: its openWithApp reports in the window, not in a toast.
func NewOpen(app *views.App, req views.OpenRequest, confirm func()) *Open {
	f := OpenFile{ID: req.ID, WithApp: req.WithApp}
	if app.Main != nil {
		f.Name = app.Main.Downloads.NameOf(req.ID)
		if d, ok := app.Main.Downloads.Get(req.ID); ok {
			raw := categories.RawName(d)
			f.Executable, f.HiddenControls = categories.IsExecutableName(raw), text.HasBidiControls(raw)
			f.Disguise = display.DisguiseText(d)
		}
	}
	return NewOpenFile(app, f, confirm)
}

// OpenFile is the file an Open dialog asks about, for a window without the main window's
// list (a progress window knows its download itself).
type OpenFile struct {
	ID int64
	// Name is the display name (bidi controls made visible).
	Name string
	// Executable: the file would run code when opened; HiddenControls: its real name has
	// bidi control characters; WithApp: Open with… (the user chooses the app next).
	Executable, HiddenControls, WithApp bool
	// Disguise is the description of a file that is not what its name says (display.DisguiseText;
	// "" for any other file): the dialog then asks with that title and "… anyway".
	Disguise string
}

// NewOpenFile is NewOpen for f: the same dialog, its facts given rather than read from
// App.Main (OpenDialog.svelte in the progress window).
func NewOpenFile(app *views.App, f OpenFile, confirm func()) *Open {
	o := &Open{app: app, id: f.ID, withApp: f.WithApp, confirm: confirm}
	if o.confirm == nil {
		o.confirm = o.open
	}
	display, executable := f.Name, f.Executable
	title, desc, action := OpenTexts(executable, f.WithApp, f.Disguise)
	o.Dialog = app.NewDialog(views.ModalOpen, widgets.DialogOptions{
		Title: title, Description: desc, Alert: true, Size: widgets.DialogSm, OnSubmit: o.Submit,
	})
	if executable || f.Disguise != "" {
		o.SetIcon(newTone(toneDanger, "alert"))
	} else {
		o.SetIcon(newTone(toneInfo, "open"))
	}

	o.name = views.NewText(display, views.FontMono, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapAnywhere, Antialias: kit.AntialiasGrayscale})
	box := &nameBox{}
	box.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Padding: kit.EdgesOf(kit.Px(8), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1))}
	box.Add(o.name)
	o.Body.Add(box)
	if f.HiddenControls {
		wf := views.FontCell
		wf.Weight = 600
		o.warning = views.NewParagraph(HiddenControlsWarning, wf, views.InkDanger)
		o.Body.Add(o.warning)
	}

	o.cancel = footButton("Cancel", widgets.Secondary, o.Cancel)
	o.cancel.Autofocus = true
	o.submit = footButton(action, widgets.Primary, o.Submit)
	o.SetFooter(nil, o.cancel, o.submit)
	return o
}

// NameText is the name shown.
func (o *Open) NameText() *views.Text { return o.name }

// Warning is the hidden-controls warning (nil when the name has none).
func (o *Open) Warning() *views.Text { return o.warning }

// Buttons are Cancel and the action (Run, Open, Choose app…).
func (o *Open) Buttons() (cancel, submit *widgets.Button) { return o.cancel, o.submit }

// Cancel is the Cancel button: the dialog goes at once.
func (o *Open) Cancel() { o.Dialog.Remove() }

// Submit confirms: confirm, then the dialog goes.
func (o *Open) Submit() {
	if !o.Shown() {
		return
	}
	o.confirm()
	o.Dialog.Remove()
}

// open is App.svelte's openFile: the file opened, a toast when it cannot be.
func (o *Open) open() {
	a, id := o.app, o.id
	if a.Main == nil {
		return
	}
	name := a.Main.Downloads.NameOf(id)
	a.Go(func() {
		ctx, cancel := a.CallContext()
		defer cancel()
		if err := a.Main.Downloads.OpenFile(ctx, id); err != nil {
			a.Error("Could not open " + text.Isolate(name) + ": " + text.Isolate(views.ErrorMessage(err)))
		}
	})
}

// nameBox is OpenDialog's .name: 8 px 11 px in a 1 px --line border with 9 px corners on
// --surface-2.
type nameBox struct{ kit.Element }

// Paint is kit.Painter's.
func (n *nameBox) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&n.Element)
	box := views.SnapBox(p, 9)
	p.Canvas.FillRoundedRect(box, t.Line)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(box.Rect.Inset(kit.UniformInsets(w)), 9-w), t.Surface2)
}

// BodyPane is the dialog's scrolling body.
func (o *Open) BodyPane() (*widgets.ScrollPane, bool) { return bodyPane(o.Dialog) }
