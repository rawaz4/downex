package dialogs

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// Restart is RestartDialog.svelte (DESIGN-SYSTEM.md §4.7): Restart and Download again.
// Restarting discards what was received and downloads from the beginning; a completed download
// is downloaded again, and while its file is still there the dialog asks what happens to it,
// as selectable rows like Move to queue's: keep both (the default; the new copy gets a
// numbered name) or replace it once the new copy is complete. The choice is remembered on this
// computer (the prefs' downex.existingFile, state.ExistingFileChoice); the dialog works
// without it.
type Restart struct {
	*views.Dialog

	app     *views.App
	ids     []int64
	reason  views.RestartReason
	again   []views.AgainFile
	present []views.AgainFile
	confirm func(existing downexv1.ExistingFile)

	tone    *tone
	list    *names
	choices *widgets.RadioRows
	gone    *views.Text
	cancel  *widgets.Button
	submit  *widgets.Button

	choice downexv1.ExistingFile
	busy   bool
}

// The values of the choice rows (ExistingFile's).
const (
	keepBothValue = "keepBoth"
	replaceValue  = "replace"
)

// NewRestart makes the Restart or Download again dialog for req (the composition has kept the
// restartable ids, filled Again with the completed ones and set RestartCompleted when every
// one is complete). confirm runs on App.Go with the choice for the completed ones (keep both
// when nothing was asked), and the dialog closes once it returns; nil is App.svelte's
// restartDownloads: store.restart, report(…, 'restart'), and the progress window of a single
// download that starts now when the setting shows it.
func NewRestart(app *views.App, req views.RestartRequest, confirm func(existing downexv1.ExistingFile)) *Restart {
	r := &Restart{app: app, ids: append([]int64(nil), req.IDs...), reason: req.Reason, again: append([]views.AgainFile(nil), req.Again...), confirm: confirm}
	if r.confirm == nil {
		r.confirm = r.restart
	}
	for _, a := range r.again {
		if a.Exists {
			r.present = append(r.present, a)
		}
	}
	r.choice = state.ExistingFileChoice(app.Storage)
	nameList := namesOf(app, r.ids)
	t := RestartTexts(nameList, r.reason, r.again)

	r.Dialog = app.NewDialog(views.ModalRestart, widgets.DialogOptions{
		Title: t.Title, Description: t.Description, Alert: true, Size: widgets.DialogSm, OnSubmit: r.Submit,
	})
	r.tone = newTone(toneInfo, "restart")
	r.SetIcon(r.tone)

	if len(nameList) > 1 {
		r.list = newNames(nameList, false)
		r.Body.Add(r.list)
	}
	choosing := r.choosing()
	if choosing {
		opts := make([]widgets.RadioOption, len(t.Options))
		for i, o := range t.Options {
			opts[i] = widgets.RadioOption{Value: o.Value, Label: o.Label, Note: o.Note, Icon: o.Icon}
		}
		r.choices = widgets.NewRadioRows(t.GroupLabel, opts, valueOf(r.choice), false, r.choose)
		fixRows(r.choices, nil)
		r.markAutofocus()
		r.Body.Add(r.choices)
		if t.GoneNote != "" {
			r.gone = views.NewParagraph(t.GoneNote, views.FontHint, views.InkMuted)
			r.Body.Add(r.gone)
		}
	}

	r.cancel = footButton(t.Cancel, widgets.Secondary, r.Cancel)
	r.cancel.Autofocus = !choosing // data-autofocus={choosing ? undefined : ''}
	r.submit = footButton(t.Submit, widgets.Primary, r.Submit)
	r.SetFooter(nil, r.cancel, r.submit)
	return r
}

// choosing reports that completed downloads whose files are still there get the choice.
func (r *Restart) choosing() bool { return len(r.present) > 0 }

// markAutofocus gives the chosen row the first focus (data-autofocus on the checked radio).
func (r *Restart) markAutofocus() {
	v := valueOf(r.choice)
	for i, row := range r.choices.Children() {
		row.Autofocus = i < len(r.choices.Options) && r.choices.Options[i].Value == v
	}
}

func valueOf(c downexv1.ExistingFile) string {
	if c == state.Replace {
		return replaceValue
	}
	return keepBothValue
}

func (r *Restart) choose(v string) {
	r.choice = state.KeepBoth
	if v == replaceValue {
		r.choice = state.Replace
	}
	r.markAutofocus()
}

// Choice is the existing-file choice shown (keep both by default).
func (r *Restart) Choice() downexv1.ExistingFile { return r.choice }

// Choices are the choice rows (nil when nothing is asked).
func (r *Restart) Choices() *widgets.RadioRows { return r.choices }

// Names is the list of names (nil for one download).
func (r *Restart) Names() *kit.Element {
	if r.list == nil {
		return nil
	}
	return &r.list.Element
}

// GoneNote is the line about files no longer in their folders (nil for none).
func (r *Restart) GoneNote() *views.Text { return r.gone }

// Buttons are Cancel (or Not now) and Restart (or Download again).
func (r *Restart) Buttons() (cancel, submit *widgets.Button) { return r.cancel, r.submit }

// Cancel is the Cancel / Not now button: the dialog goes at once.
func (r *Restart) Cancel() {
	if !r.busy {
		r.Dialog.Remove()
	}
}

// Submit starts the downloads: the choice is remembered when one was made, the button reads
// "Starting…" or "Restarting…" until confirm returns, then the dialog goes.
func (r *Restart) Submit() {
	if r.busy || !r.Shown() {
		return
	}
	used := state.KeepBoth
	if r.choosing() {
		used = r.choice
		state.SaveExistingFileChoice(r.app.Storage, used)
	}
	r.setBusy(true)
	r.app.Go(func() {
		r.confirm(used)
		r.app.UI(func() {
			r.setBusy(false)
			r.Dialog.Remove()
		})
	})
}

func (r *Restart) setBusy(b bool) {
	r.busy = b
	r.SetBusy(b)
	r.cancel.SetDisabled(b, false)
	r.submit.SetDisabled(b, false)
	r.submit.SetText(submitText(r.reason, b))
}

func submitText(reason views.RestartReason, busy bool) string {
	switch {
	case reason == views.RestartCompleted && busy:
		return "Starting…"
	case reason == views.RestartCompleted:
		return "Download again"
	case busy:
		return "Restarting…"
	}
	return "Restart"
}

// restart is App.svelte's restartDownloads.
func (r *Restart) restart(existing downexv1.ExistingFile) {
	a := r.app
	if a.Main == nil {
		return
	}
	ctx, cancel := a.CallContext()
	defer cancel()
	outcomes := a.Main.Downloads.Restart(ctx, r.ids, existing)
	if msg := views.ReportText(outcomes, "restart", a.Main.Downloads.NameOf); msg != "" {
		a.Error(msg)
	}
	if len(r.ids) == 1 && len(outcomes) == 1 && outcomes[0].OK() {
		id := outcomes[0].ID
		a.UI(func() { showProgressIfStarted(a, id) })
	}
}

// showProgressIfStarted is App.svelte's: the progress window of a download that starts now,
// when the setting shows it.
func showProgressIfStarted(a *views.App, id int64) {
	d, ok := a.Main.Downloads.Get(id)
	s, _ := a.Settings()
	if ok && s.GetShowProgressWindow() && views.StartsNow(d) && a.Act.OpenProgress != nil {
		a.Act.OpenProgress(id)
	}
}

// RestartChoice is a choice row's text (RestartDialog's Choice).
type RestartChoice struct {
	Value, Label, Note, Icon string
}

// RestartText is everything RestartDialog says for a set of downloads.
type RestartText struct {
	Title, Description string
	// GroupLabel names the choice group; Options are its rows; GoneNote is the line about
	// the files no longer there ("" for none).
	GroupLabel string
	Options    []RestartChoice
	GoneNote   string
	// Cancel and Submit are the footer's buttons (not busy).
	Cancel, Submit string
}

// RestartTexts is RestartDialog's derived texts for the names of every download, the reason
// and the completed ones among them.
func RestartTexts(names []string, reason views.RestartReason, again []views.AgainFile) RestartText {
	count := len(names)
	subject := itoa(count) + " downloads"
	if count == 1 {
		subject = quote(names[0])
	}
	var present []views.AgainFile
	allSites := len(again) > 0
	for _, a := range again {
		if a.Exists {
			present = append(present, a)
		}
		allSites = allSites && a.Site
	}
	gone := len(again) - len(present)

	var t RestartText
	switch reason {
	case views.RestartValidatorChanged:
		t.Description = subject + " cannot be resumed: the file on the server changed, or the saved progress no longer matches the partial file. Restarting discards the data received so far and downloads the file again from the beginning."
	case views.RestartCompleted:
		from := "its address"
		if allSites {
			from = "its video page"
		}
		switch {
		case len(again) == 1 && !again[0].Exists:
			t.Description = quote(again[0].Name) + " is no longer in its folder. Downex downloads it again from " + from + " and saves it under the same name."
		case len(again) == 1:
			verb := "downloads"
			if again[0].Site {
				verb = "gets"
			}
			t.Description = "Downex " + verb + " " + quote(again[0].Name) + " again from " + from + "."
		default:
			their := "addresses"
			if allSites {
				their = "video pages"
			}
			t.Description = "Downex downloads " + itoa(len(again)) + " completed downloads again from their " + their + "."
		}
	default:
		restart := "Restarting " + subject + " discards the data received so far and downloads from the beginning."
		switch {
		case len(again) == 0:
			t.Description = restart
		case len(again) == 1:
			t.Description = restart + " " + quote(again[0].Name) + " is complete: Downex downloads it again."
		default:
			t.Description = restart + " " + itoa(len(again)) + " of them are complete: Downex downloads them again."
		}
	}

	if len(present) == 1 {
		t.GroupLabel = "The current file of " + quote(present[0].Name)
	} else {
		t.GroupLabel = "The current files of the " + itoa(len(present)) + " completed downloads"
	}
	first := ""
	if len(present) > 0 {
		first = present[0].Name
	}
	keep := RestartChoice{Value: keepBothValue, Label: "Keep both", Icon: "copy"}
	replace := RestartChoice{Value: replaceValue, Label: "Replace the current files", Icon: "refresh",
		Note: "The current file stays until the new copy is complete. If downloading fails, it stays as it is."}
	if len(present) == 1 {
		keep.Note = "The new copy is saved as " + quote(state.NumberedName(first, 1)) + " (or the next free number)."
		replace.Label = "Replace the current file"
	} else {
		keep.Note = "Each new copy gets a numbered name, such as " + quote(state.NumberedName(first, 1)) + "."
	}
	t.Options = []RestartChoice{keep, replace}

	switch {
	case gone == 0 || len(again) == 1:
	case gone == 1:
		t.GoneNote = "1 of them is no longer in its folder: it comes back under its own name."
	default:
		t.GoneNote = itoa(gone) + " of them are no longer in their folders: they come back under their own names."
	}

	switch {
	case reason == views.RestartValidatorChanged:
		t.Title = "Restart from the beginning?"
	case reason == views.RestartCompleted:
		t.Title = "Download again?"
	case count == 1:
		t.Title = "Restart download?"
	default:
		t.Title = "Restart downloads?"
	}
	t.Cancel = "Cancel"
	if reason == views.RestartValidatorChanged {
		t.Cancel = "Not now"
	}
	t.Submit = submitText(reason, false)
	return t
}

// BodyPane is the dialog's scrolling body.
func (r *Restart) BodyPane() (*widgets.ScrollPane, bool) { return bodyPane(r.Dialog) }
