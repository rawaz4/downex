package dialogs

import (
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// Remove is RemoveDialog.svelte: "Remove download?" or "Remove N downloads?" as an
// alertdialog (size sm) with the danger tone, the names (five, then "and N more") and "Also
// delete the file(s) from disk (including partial data)"; Cancel (focused first) and Remove,
// which reads "Remove and delete" while the box is checked (the tone's icon then turns to the
// alert). Both are disabled while the removal runs.
type Remove struct {
	*views.Dialog

	app     *views.App
	ids     []int64
	confirm func(deleteFile bool)

	tone   *tone
	list   *names
	check  *widgets.Checkbox
	cancel *widgets.Button
	submit *widgets.Button

	alsoDelete bool
	busy       bool
}

// NewRemove makes the Remove dialog for req (App.svelte's dialog of kind remove; the names are
// store.nameOf's). confirm runs on App.Go when the user confirms, with the box's state, and the
// dialog closes once it returns; nil is App.svelte's
// report(await store.remove(ids, deleteFile), 'remove').
func NewRemove(app *views.App, req views.RemoveRequest, confirm func(deleteFile bool)) *Remove {
	r := &Remove{app: app, ids: append([]int64(nil), req.IDs...), confirm: confirm, alsoDelete: req.DeleteFile}
	if r.confirm == nil {
		r.confirm = r.remove
	}
	nameList := namesOf(app, r.ids)
	count := len(nameList)
	title, desc, noun := "Remove download?", "The download is removed from the list.", "file"
	if count != 1 {
		title, desc, noun = "Remove "+itoa(count)+" downloads?", "The downloads are removed from the list.", "files"
	}
	r.Dialog = app.NewDialog(views.ModalRemove, widgets.DialogOptions{
		Title: title, Description: desc, Alert: true, Size: widgets.DialogSm, OnSubmit: r.Submit,
	})
	r.tone = newTone(toneDanger, r.toneIcon())
	r.SetIcon(r.tone)

	r.list = newNames(nameList, true)
	r.check = widgets.NewCheckbox("Also delete the "+noun+" from disk (including partial data)", r.alsoDelete, r.setAlsoDelete)
	r.check.Label.SetFont(views.FontCell) // .check { font-size: var(--type-cell) }
	r.check.Style.AlignSelf = kit.AlignStart
	r.Body.Add(r.list, r.check)

	r.cancel = footButton("Cancel", widgets.Secondary, r.Cancel)
	r.cancel.Autofocus = true // data-autofocus
	r.submit = footButton(r.submitText(), widgets.Danger, r.Submit)
	r.SetFooter(nil, r.cancel, r.submit)
	return r
}

// namesOf are the downloads' display names (store.nameOf).
func namesOf(app *views.App, ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if app.Main != nil {
			out[i] = app.Main.Downloads.NameOf(id)
		}
	}
	return out
}

func (r *Remove) toneIcon() string {
	if r.alsoDelete {
		return "alert"
	}
	return "remove"
}

func (r *Remove) submitText() string {
	if r.alsoDelete {
		return "Remove and delete"
	}
	return "Remove"
}

// AlsoDelete reports whether the files are deleted too.
func (r *Remove) AlsoDelete() bool { return r.alsoDelete }

// Checkbox is "Also delete …".
func (r *Remove) Checkbox() *widgets.Checkbox { return r.check }

// Buttons are Cancel and Remove.
func (r *Remove) Buttons() (cancel, submit *widgets.Button) { return r.cancel, r.submit }

func (r *Remove) setAlsoDelete(on bool) {
	r.alsoDelete = on
	r.tone.setIcon(r.toneIcon())
	r.submit.SetText(r.submitText())
}

// Cancel is the Cancel button: the dialog goes at once (onclick={onclose}).
func (r *Remove) Cancel() {
	if !r.busy {
		r.Dialog.Remove()
	}
}

// Submit removes the downloads (the form's submit): busy until confirm returns, then the
// dialog goes.
func (r *Remove) Submit() {
	if r.busy || !r.Shown() {
		return
	}
	r.setBusy(true)
	del := r.alsoDelete
	r.app.Go(func() {
		r.confirm(del)
		r.app.UI(func() {
			r.setBusy(false)
			r.Dialog.Remove()
		})
	})
}

func (r *Remove) setBusy(b bool) {
	r.busy = b
	r.SetBusy(b)
	r.cancel.SetDisabled(b, false)
	r.submit.SetDisabled(b, false)
}

// remove is App.svelte's onconfirm: the downloads removed, a toast for those that could not
// be.
func (r *Remove) remove(deleteFile bool) {
	a := r.app
	if a.Main == nil {
		return
	}
	ctx, cancel := a.CallContext()
	defer cancel()
	outcomes := a.Main.Downloads.Remove(ctx, r.ids, deleteFile)
	if msg := views.ReportText(outcomes, "remove", a.Main.Downloads.NameOf); msg != "" {
		a.Error(msg)
	}
}

// BodyPane is the dialog's scrolling body.
func (r *Remove) BodyPane() (*widgets.ScrollPane, bool) { return bodyPane(r.Dialog) }
