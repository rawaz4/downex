package dialogs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The Rescue partial file dialog's texts (docs/DESKTOP-SPEC.md §11).
const (
	RescueTitle       = "Rescue partial file"
	RescueDescription = "Downex continues a download a browser left unfinished, from the bytes already saved. " +
		"It first checks that the server still has the same file."
	RescuePickTitle    = "Choose the partial file"
	RescueFileLabel    = "Partial file"
	RescueChoose       = "Choose…"
	RescueNoFile       = "No file chosen"
	RescueAddressLabel = "Address"
	RescueAddressHint  = "The download's address. Chrome: right-click it on the downloads page, Copy link address. Firefox: right-click it in the downloads list, Copy Download Link."
	RescueSaveAsLabel  = "Save as"
	RescueServerNames  = "Named by the server"
	RescueFooterHint   = "Saved in the partial file's folder."
	RescueSubmit       = "Rescue"
	RescueChecking     = "Checking…"
	RescueNeedFile     = "Choose the partial file."
	RescueNeedAddress  = "Enter the download's address (http or https)."
	RescueUnreadable   = "Downex cannot read this file."
	RescueEmptyFile    = "This file is empty: there is nothing to finish."
)

// RescueCall sends a rescue (nil: the downloads store's Rescue).
type RescueCall func(req *downexv1.RescueRequest) (*downexv1.RescueResponse, error)

// RescueOptions replace the dialog's dependencies in tests.
type RescueOptions struct {
	// Call sends the rescue (nil: the downloads store's, within the window's CallTimeout).
	Call RescueCall
	// Size reads a chosen file's size off the UI thread (nil: os.Lstat of a regular file).
	Size func(path string) (int64, error)
}

// Rescue is the Rescue partial file dialog (docs/DESKTOP-SPEC.md §11): a partial file a
// browser left unfinished (picked with the system's file picker), the download's address
// and the name to save it as. downexd checks the file with the server first; refusals show
// under the field they concern.
type Rescue struct {
	*views.Dialog

	app  *views.App
	opts RescueOptions

	path              *rescuePath
	choose            *widgets.Button
	fileHint, fileErr *views.Text
	address           *widgets.Input
	addressErr        *views.Text
	saveAs            *widgets.Input

	cancel, submit *widgets.Button

	partial string
	size    int64 // -1 while unknown or unreadable
	url     string
	name    string
	busy    bool
	gone    bool
}

// rescuePath is the chosen file's path: mono on --surface-2, at most two lines with an
// ellipsis, the whole path as its tooltip (bidi controls shown escaped).
type rescuePath struct {
	kit.Element
	label *views.Text
}

func newRescuePath() *rescuePath {
	c := &rescuePath{}
	c.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesOf(kit.Px(7), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)),
		MinWidth: kit.Px(0), Flex: kit.Flex1}
	c.label = views.NewText(RescueNoFile, views.FontMono, views.InkFaint, kit.TextOptions{Wrap: kit.TextWrapAnywhere, MaxLines: 2, Truncate: kit.TruncateEnd})
	c.label.Style.MinWidth = kit.Px(0)
	c.Add(c.label)
	return c
}

func (c *rescuePath) set(path string) {
	shown := text.EscapeBidi(path)
	c.label.SetText(shown)
	c.label.Ink = views.InkInk2
	c.Tooltip = shown
}

// Paint is kit.Painter's.
func (c *rescuePath) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	r := views.SnapBox(p, 9)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	p.Canvas.FillRoundedRect(r, border)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 9-w), t.Surface2)
}

// NewRescue makes the dialog; r.Partial, when set, is chosen already.
func NewRescue(app *views.App, r views.RescueRequest, o RescueOptions) *Rescue {
	n := &Rescue{app: app, opts: o, size: -1}
	if n.opts.Call == nil {
		n.opts.Call = func(req *downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
			ctx, cancel := app.CallContext()
			defer cancel()
			return app.Main.Downloads.Rescue(ctx, req)
		}
	}
	if n.opts.Size == nil {
		n.opts.Size = fileSize
	}
	n.Dialog = app.NewDialog(views.ModalRescue, widgets.DialogOptions{
		Title: RescueTitle, Description: RescueDescription, Width: kit.Rem(44), OnSubmit: n.Submit,
	})
	n.SetIcon(newTone(toneInfo, "restart"))

	n.path = newRescuePath()
	n.choose = widgets.NewButton(RescueChoose, "", widgets.Secondary, widgets.SizeMd, n.pick)
	n.choose.Style.Height, n.choose.Style.MinHeight = kit.Px(36), kit.Px(36)
	row := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: tokens.Sp4, MinWidth: kit.Px(0)}}
	row.Add(n.path, n.choose)
	n.fileHint = views.NewHint("")
	n.fileHint.SetHidden(true)
	n.fileErr = views.NewErrorText("")
	n.fileErr.SetHidden(true)
	n.fileErr.Semantics.Live = kit.LivePolite

	n.address = widgets.NewInput("", widgets.InputOptions{Placeholder: "https://…", Name: RescueAddressLabel, Mono: true,
		Description: RescueAddressHint}, n.onAddress)
	n.addressErr = views.NewErrorText("")
	n.addressErr.SetHidden(true)
	n.addressErr.Semantics.Live = kit.LivePolite

	n.saveAs = widgets.NewInput("", widgets.InputOptions{Placeholder: RescueServerNames, Name: RescueSaveAsLabel,
		AutoDirection: true}, func(s string) {
		if n.busy {
			n.saveAs.SetText(n.name)
			return
		}
		n.name = s
	})

	n.Body.Add(
		views.NewField(RescueFileLabel, row, n.fileHint, n.fileErr),
		views.NewField(RescueAddressLabel, n.address, views.NewHint(RescueAddressHint), n.addressErr),
		views.NewField(RescueSaveAsLabel, n.saveAs),
	)
	n.cancel = footButton("Cancel", widgets.Secondary, n.Cancel)
	n.submit = footButton(RescueSubmit, widgets.Primary, n.Submit)
	n.SetFooter(views.NewHint(RescueFooterHint), n.cancel, n.submit)
	if r.Partial != "" {
		n.choosePath(r.Partial)
	} else {
		n.choose.Autofocus = true
	}
	n.refresh()
	return n
}

// fileSize is the size of a regular file at path.
func fileSize(path string) (int64, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, errors.New("not a regular file")
	}
	return fi.Size(), nil
}

// ShowModal is views.Modal's.
func (n *Rescue) ShowModal(w *kit.Window, done func()) kit.Widget {
	return n.Dialog.ShowModal(w, func() {
		n.gone = true
		if done != nil {
			done()
		}
	})
}

// DismissModal is views.Modal's.
func (n *Rescue) DismissModal() {
	n.gone = true
	n.Dialog.DismissModal()
}

// Cancel is the Cancel button.
func (n *Rescue) Cancel() {
	if !n.busy {
		n.Dialog.Remove()
	}
}

// pick shows the system's file picker in the default folder.
func (n *Rescue) pick() {
	if n.busy || n.app.Platform == nil {
		return
	}
	start := filepath.Dir(n.partial)
	if n.partial == "" {
		s, _ := n.app.Settings()
		start = s.GetDefaultDir()
	}
	n.app.Go(func() {
		ctx, cancel := n.app.CallContext()
		picked, err := n.app.Platform.PickFile(ctx, RescuePickTitle, start)
		cancel()
		if err != nil || picked == "" {
			return
		}
		n.app.UI(func() {
			if !n.gone {
				n.choosePath(picked)
			}
		})
	})
}

// choosePath takes path as the partial file: its name prefills Save as, and its size is
// read off the UI thread for the hint.
func (n *Rescue) choosePath(path string) {
	n.partial, n.size = path, -1
	n.path.set(path)
	n.name = display.RescueSaveAs(filepath.Base(path))
	n.saveAs.SetText(n.name)
	n.setFileError("")
	n.fileHint.SetHidden(true)
	size := n.opts.Size
	n.app.Go(func() {
		sz, err := size(path)
		n.app.UI(func() {
			if n.gone || n.partial != path {
				return
			}
			switch {
			case err != nil:
				n.setFileError(RescueUnreadable)
			case sz == 0:
				n.size = 0
				n.setFileError(RescueEmptyFile)
			default:
				n.size = sz
				n.fileHint.SetText(format.Bytes(float64(sz)) + " saved · " + display.PartialKind(filepath.Base(path)))
				n.fileHint.SetHidden(false)
			}
			n.refresh()
		})
	})
	n.refresh()
}

func (n *Rescue) onAddress(s string) {
	if n.busy {
		n.address.SetText(n.url)
		return
	}
	if s != n.url {
		n.url = s
		n.setAddressError("")
		n.refresh()
	}
}

// ready reports a usable file and an http(s) address.
func (n *Rescue) ready() bool {
	return n.partial != "" && n.size > 0 && webLinkRE.MatchString(n.url)
}

func (n *Rescue) refresh() {
	n.submit.SetDisabled(n.busy || !n.ready(), false)
	n.submit.SetText(n.submitText())
}

func (n *Rescue) submitText() string {
	if n.busy {
		return RescueChecking
	}
	return RescueSubmit
}

func (n *Rescue) setFileError(s string) {
	n.fileErr.SetText(s)
	n.fileErr.SetHidden(s == "")
}

func (n *Rescue) setAddressError(s string) {
	n.addressErr.SetText(s)
	n.addressErr.SetHidden(s == "")
	n.address.SetInvalid(s != "")
}

// Submit sends the rescue.
func (n *Rescue) Submit() {
	if n.busy || !n.Shown() {
		return
	}
	switch {
	case n.partial == "":
		n.setFileError(RescueNeedFile)
		return
	case !webLinkRE.MatchString(n.url):
		n.setAddressError(RescueNeedAddress)
		return
	case n.size == 0:
		n.setFileError(RescueEmptyFile)
		return
	case n.size < 0:
		return // still reading it, or unreadable (its error shows)
	}
	req := &downexv1.RescueRequest{Url: n.url, PartialPath: n.partial, Filename: text.TrimJS(n.name),
		Source: downexv1.AddSource_ADD_SOURCE_UI}
	n.setBusy(true)
	call := n.opts.Call
	n.app.Go(func() {
		resp, err := call(req)
		n.app.UI(func() {
			n.setBusy(false)
			if err != nil {
				n.refused(err)
				return
			}
			d := resp.GetDownload()
			n.app.Success(display.RescuedToast(d.GetFilename(), resp.GetRescuedBytes()))
			if n.app.Main != nil {
				n.app.Main.Downloads.SelectOnly(d.GetId())
			}
			n.Dialog.Remove()
			showProgressIfStarted(n.app, d.GetId())
		})
	})
}

// refused shows a refusal under the field it concerns: the shared texts for the rescue
// codes, else downexd's message.
func (n *Rescue) refused(err error) {
	r := refusalOf(err)
	msg := display.RescueErrorText(r.code, runtime.GOOS)
	if msg == "" {
		msg = r.message
	}
	if display.RescueFileError(r.code) {
		n.setFileError(msg)
		return
	}
	n.setAddressError(msg)
}

func (n *Rescue) setBusy(b bool) {
	n.busy = b
	n.SetBusy(b)
	n.address.SetReadOnly(b)
	n.saveAs.SetReadOnly(b)
	n.choose.SetDisabled(b, false)
	n.cancel.SetDisabled(b, false)
	n.refresh()
}

// The dialog's parts, for tests.

// Path is the chosen file's path as shown ("" for none).
func (n *Rescue) Path() string {
	if n.partial == "" {
		return ""
	}
	return n.path.label.Text()
}

// FileHint is the size and kind line under the file ("" while hidden).
func (n *Rescue) FileHint() string { return shownText(n.fileHint) }

// FileError is the error under the file ("" for none).
func (n *Rescue) FileError() string { return shownText(n.fileErr) }

// AddressError is the error under the address ("" for none).
func (n *Rescue) AddressError() string { return shownText(n.addressErr) }

// Address and SaveAs are the fields; Choose the picker's button; Buttons the footer's.
func (n *Rescue) Address() *widgets.Input { return n.address }

// SaveAs is the Save as field.
func (n *Rescue) SaveAs() *widgets.Input { return n.saveAs }

// Choose is the "Choose…" button.
func (n *Rescue) Choose() *widgets.Button { return n.choose }

// Buttons are Cancel and Rescue.
func (n *Rescue) Buttons() (cancel, submit *widgets.Button) { return n.cancel, n.submit }

// BodyPane is the dialog's scrolling body.
func (n *Rescue) BodyPane() (*widgets.ScrollPane, bool) { return bodyPane(n.Dialog) }

func shownText(t *views.Text) string {
	if t.Style.Hidden {
		return ""
	}
	return t.Text()
}
