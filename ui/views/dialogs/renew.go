package dialogs

import (
	"errors"
	"regexp"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// Renew is the New link dialog (docs/DESKTOP-SPEC.md §10, DESIGN-SYSTEM.md §4.x): a
// download whose link stopped working gets a new one. downexd compares the new link's
// bytes with those already downloaded first; the dialog shows its refusals under the field
// and offers "Continue anyway" for another name and "Download from the start" when too
// little can be compared. It closes when the download continues, also when that happens
// elsewhere (the browser handed the same file over and downexd matched it).
type Renew struct {
	*views.Dialog

	app   *views.App
	id    int64
	name  string
	call  RenewCall
	field *widgets.Input
	note  *views.Note
	page  *widgets.Button

	cancel, submit *widgets.Button

	link    string
	busy    bool
	retry   renewRetry // what the primary button sends again after a refusal
	stop    func()
	stopped bool
}

// RenewCall sends a renewal (nil: the downloads store's RenewLink).
type RenewCall func(req *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error)

// renewRetry is the primary button's meaning after a refusal.
type renewRetry uint8

const (
	retryNone       renewRetry = iota
	retryNameOK                // renew_name: "Continue anyway"
	retryFromStart             // renew_unproven: "Download from the start"
	renewFieldLabel = "New link"
)

// RenewTexts is what the dialog says of a download: its title, its description and the
// field.
type RenewTexts struct {
	Title, Description, Label, Placeholder, PageLink, Cancel, Submit string
}

// RenewTextsOf is the dialog's texts for d, named name (docs/DESKTOP-SPEC.md §10).
func RenewTextsOf(d *downexv1.Download, name string) RenewTexts {
	t := RenewTexts{Title: "New link", Label: renewFieldLabel, Placeholder: "https://…", PageLink: "Open the download page",
		Cancel: "Cancel", Submit: "Continue download"}
	if d.GetReceived() > 0 && d.GetSize() > 0 {
		t.Description = "Downex continues " + quote(name) + " from " + format.Bytes(float64(d.GetReceived())) + " of " +
			format.Bytes(float64(d.GetSize())) + " after checking that the new link serves the same file."
	} else {
		t.Description = "Downex downloads " + quote(name) + " from the new link."
	}
	return t
}

// NewRenew makes the New link dialog of download r.ID. call nil sends it through the
// downloads store.
func NewRenew(app *views.App, r views.RenewRequest, call RenewCall) *Renew {
	d, _ := app.Main.Downloads.Get(r.ID)
	n := &Renew{app: app, id: r.ID, name: app.Main.Downloads.NameOf(r.ID), call: call}
	if n.call == nil {
		n.call = func(req *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
			ctx, cancel := app.CallContext()
			defer cancel()
			return app.Main.Downloads.RenewLink(ctx, req)
		}
	}
	t := RenewTextsOf(d, n.name)
	n.Dialog = app.NewDialog(views.ModalRenew, widgets.DialogOptions{
		Title: t.Title, Description: t.Description, Size: widgets.DialogSm, OnSubmit: n.Submit,
	})
	n.SetIcon(newTone(toneInfo, "link"))
	n.link = clipboardLink(app)
	n.field = widgets.NewInput(n.link, widgets.InputOptions{Placeholder: t.Placeholder, Name: t.Label, Mono: true},
		n.onChange)
	n.field.Autofocus = true
	n.note = views.NewNote(views.NoteFail, " ")
	n.note.Semantics.Live = kit.LivePolite
	n.note.SetHidden(true)
	n.note.Style.Margin = kit.Edges{Top: tokens.Sp2}
	n.Body.Add(views.NewField(t.Label, n.field), n.note)

	var start kit.Widget
	if display.PageURL(d) != "" {
		n.page = widgets.NewButton(t.PageLink, "external", widgets.Link, widgets.SizeMd, n.openPage)
		start = n.page
	}
	n.cancel = footButton(t.Cancel, widgets.Secondary, n.Cancel)
	n.submit = footButton(t.Submit, widgets.Primary, n.Submit)
	n.SetFooter(start, n.cancel, n.submit)
	return n
}

// clipboardLink is the clipboard's text when it is exactly one http or https address (the
// Add dialog's paste rule), else "".
func clipboardLink(app *views.App) string {
	if app.Window == nil {
		return ""
	}
	h := app.Window.Host()
	if h == nil {
		return ""
	}
	s, err := h.ClipboardText()
	if err != nil {
		return ""
	}
	var links []string
	for _, u := range validate.SplitURLs(s) {
		if webLinkRE.MatchString(u) {
			links = append(links, u)
		}
	}
	if len(links) != 1 {
		return ""
	}
	return links[0]
}

var webLinkRE = regexp.MustCompile(`(?i)^https?://`)

// ShowModal is views.Modal's: the dialog, following its download (renewed elsewhere, it
// closes as if it had been renewed here).
func (n *Renew) ShowModal(w *kit.Window, done func()) kit.Widget {
	layer := n.Dialog.ShowModal(w, func() {
		n.unfollow()
		if done != nil {
			done()
		}
	})
	if n.app.Main != nil {
		n.stop = n.app.Watch(&n.app.Main.Downloads.Changes, n.follow)
	}
	return layer
}

// DismissModal is views.Modal's.
func (n *Renew) DismissModal() {
	n.unfollow()
	n.Dialog.DismissModal()
}

func (n *Renew) unfollow() {
	if n.stop != nil && !n.stopped {
		n.stopped = true
		n.stop()
	}
}

// follow closes the dialog when its download continues elsewhere (queued or downloading
// without a failure: downexd matched a new link the browser handed over) or is gone.
func (n *Renew) follow() {
	if n.busy || !n.Shown() {
		return
	}
	d, ok := n.app.Main.Downloads.Get(n.id)
	if !ok {
		n.Dialog.Remove()
		return
	}
	switch d.GetState() {
	case downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
		downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		if d.GetErrorCode() == "" {
			n.app.Success(RenewedText(n.name, false))
			n.Dialog.Remove()
		}
	}
}

// Field is the link field; NoteText the error under it ("" while hidden); Buttons the footer's.
func (n *Renew) Field() *widgets.Input { return n.field }

// NoteText is the refusal shown under the field ("" for none).
func (n *Renew) NoteText() string {
	if n.note.Style.Hidden || n.note.Text == nil {
		return ""
	}
	return n.note.Text.Text()
}

// Buttons are Cancel and the primary button.
func (n *Renew) Buttons() (cancel, submit *widgets.Button) { return n.cancel, n.submit }

// PageButton is "Open the download page" (nil when the page is not known).
func (n *Renew) PageButton() *widgets.Button { return n.page }

func (n *Renew) onChange(s string) {
	if n.busy {
		n.field.SetText(n.link) // read-only while checking
		return
	}
	if s != n.link {
		n.link = s
		n.setRefusal("", retryNone)
	}
}

// Cancel is the Cancel button: the dialog goes at once.
func (n *Renew) Cancel() {
	if !n.busy {
		n.Dialog.Remove()
	}
}

func (n *Renew) openPage() {
	d, ok := n.app.Main.Downloads.Get(n.id)
	if !ok || display.PageURL(d) == "" {
		return
	}
	n.app.Act.OpenPage(n.id)
}

// Submit sends the link (with what the last refusal's button stands for).
func (n *Renew) Submit() {
	if n.busy || !n.Shown() {
		return
	}
	link := n.link
	if !webLinkRE.MatchString(link) {
		n.setRefusal(RenewRefusalText(&renewRefusal{code: CodeInvalid}, n.name, ""), retryNone)
		return
	}
	req := &downexv1.RenewLinkRequest{Id: n.id, Link: &downexv1.RenewLinkRequest_Url{Url: link}, Start: true,
		AllowNameChange: n.retry == retryNameOK, RestartIfUnproven: n.retry == retryFromStart}
	n.setBusy(true)
	n.app.Go(func() {
		resp, err := n.call(req)
		n.app.UI(func() {
			n.setBusy(false)
			if err != nil {
				ref := refusalOf(err)
				n.setRefusal(RenewRefusalText(ref, n.name, ""), retryOf(ref.code))
				return
			}
			n.app.Success(RenewedText(n.name, resp.GetRestarted()))
			n.Dialog.Remove()
		})
	})
}

func (n *Renew) setBusy(b bool) {
	n.busy = b
	n.SetBusy(b)
	n.field.SetReadOnly(b)
	n.cancel.SetDisabled(b, false)
	n.submit.SetDisabled(b, false)
	n.submit.SetText(n.submitText())
}

func (n *Renew) submitText() string {
	switch {
	case n.busy:
		return "Checking…"
	case n.retry == retryNameOK:
		return "Continue anyway"
	case n.retry == retryFromStart:
		return "Download from the start"
	}
	return "Continue download"
}

func (n *Renew) setRefusal(text string, retry renewRetry) {
	n.retry = retry
	n.note.SetHidden(text == "")
	if text != "" {
		n.note.Text.SetText(text)
	}
	n.field.SetInvalid(text != "")
	n.submit.SetText(n.submitText())
}

// The refusal codes of RenewLink (docs/DAEMON-SPEC.md §6) the dialog words.
const (
	CodeRenewName     = "renew_name"
	CodeRenewUnproven = "renew_unproven"
	CodeRenewMismatch = "renew_mismatch"
	CodeRenewSize     = "renew_size"
	CodeRenewChanged  = "renew_changed"
	CodeRenewNoRanges = "renew_no_ranges"
	CodeAuthRequired  = "auth_required"
	// CodeInvalid is an InvalidArgument answer (the link is no http or https address).
	CodeInvalid = "invalid_request"
)

// renewRefusal is a refused renewal: its code ("" for another error) and message.
type renewRefusal struct {
	code, message string
}

var codedMessage = regexp.MustCompile(`(?s)^([a-z_]+): (.*)$`)

// refusalOf reads a refused RenewLink: the Downex-Error-Code metadata, else the message's
// code prefix; InvalidArgument without one is invalid_request.
func refusalOf(err error) *renewRefusal {
	r := &renewRefusal{message: views.ErrorMessage(err)}
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Meta().Get("Downex-Error-Code") != "" {
		r.code = ce.Meta().Get("Downex-Error-Code")
	}
	if m := codedMessage.FindStringSubmatch(r.message); m != nil {
		if r.code == "" || r.code == m[1] {
			r.code, r.message = m[1], m[2]
		}
	}
	if r.code == "" {
		if ce := host.ErrorOf(err); ce != nil && ce.Code == connect.CodeInvalidArgument {
			r.code = CodeInvalid
		}
	}
	return r
}

func retryOf(code string) renewRetry {
	switch code {
	case CodeRenewName:
		return retryNameOK
	case CodeRenewUnproven:
		return retryFromStart
	}
	return retryNone
}

var (
	quotedNames = regexp.MustCompile(`the new link names the file "(.*)"; download \d+ is "(.*)"`)
	sizes       = regexp.MustCompile(`^(\d+) bytes, expected (\d+)`)
)

// RenewRefusalText is the note under the field for a refusal (docs/DESKTOP-SPEC.md §10);
// name is the download's name.
func RenewRefusalText(r *renewRefusal, name, _ string) string {
	switch r.code {
	case CodeRenewName:
		newName, oldName := "", name
		if m := quotedNames.FindStringSubmatch(r.message); m != nil {
			newName, oldName = m[1], m[2]
		}
		return "The new link names the file " + quote(newName) + ". Downex keeps the name " + quote(oldName) + "."
	case CodeRenewUnproven:
		return "Too little of the file can be compared to be sure this is the same file."
	case CodeRenewMismatch:
		return "This link serves a different file: its bytes differ from what was downloaded."
	case CodeRenewSize:
		if m := sizes.FindStringSubmatch(r.message); m != nil {
			return "This link's file is " + format.Bytes(atof(m[1])) + "; this download is " + format.Bytes(atof(m[2])) + "."
		}
		return "This link's file has another size."
	case CodeRenewChanged:
		return "The server reports another version of the file. Restart the download to get the new version."
	case CodeRenewNoRanges:
		return "This link's server sends only whole files, so the download cannot continue where it stopped. Restart the download to use it."
	case display.CodeLinkExpired:
		return "This link does not work either: it expired or leads to a web page."
	case CodeAuthRequired:
		return "The server asks for a user name and password. Add a site login for it, then try again."
	case CodeInvalid:
		return "Enter an http or https link."
	}
	return r.message
}

func atof(s string) float64 {
	var v float64
	for _, c := range s {
		v = v*10 + float64(c-'0')
	}
	return v
}

// RenewedText is the toast after a renewal (display.RenewedToast).
func RenewedText(name string, restarted bool) string { return display.RenewedToast(name, restarted) }

// BodyPane is the dialog's scrolling body.
func (n *Renew) BodyPane() (*widgets.ScrollPane, bool) { return bodyPane(n.Dialog) }
