package add

import (
	"context"
	"strconv"
	"strings"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/confirm"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/media"
	"downex/ui/core/query"
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// ConfirmOptions are what the Confirm dialog is given beside its download (ConfirmDialog.svelte's
// props). Functions run off the UI thread (App.Go) and may block; a nil one takes the App's.
type ConfirmOptions struct {
	// Settings gives the categories, the default folder and useCategoryFolders; nil is
	// App.Settings()'s.
	Settings *downexv1.Settings
	// Waiting is how many downloads await confirmation, this one included (0: 1).
	Waiting int
	// Requested: the user asked for it (the list, the details pane). Otherwise it opened by
	// itself: the focus stays off its form, and its buttons and Enter do nothing for Guard.
	Requested bool
	// Guard is that time (0: confirm.Guard, 600 ms).
	Guard time.Duration
	// PickFolder is the folder picker; nil is the platform's.
	PickFolder func(ctx context.Context, initial string) (string, error)
	// Confirm is Start download / Download later (App.svelte's confirmDownload); it returns
	// the daemon's error. nil is DownloadsStore.Confirm.
	Confirm func(ctx context.Context, req *downexv1.ConfirmRequest) error
	// Cancel removes the download (cancelDownload); nil is DownloadsStore.Remove.
	Cancel func(ctx context.Context) error
	// Move moves it (still awaiting confirmation) to the end of a queue; nil is
	// QueuesStore.Move and DownloadsStore.AssignQueue. NoMove offers no queue.
	Move   func(ctx context.Context, queueID int64) error
	NoMove bool
	// Queues to choose from (fewer than two: none offered); nil is App.QueueChoices().
	Queues []*downexv1.Queue
	// OnClose runs once the dialog has gone; closing leaves the download awaiting
	// confirmation (closeConfirm).
	OnClose func()
	// Go runs background work (nil: App.Go). Now is the guard's clock (nil: the window's).
	Go  func(func())
	Now func() time.Time
}

// Confirm is ConfirmDialog.svelte (DESIGN-SYSTEM.md §4.7): a download awaiting confirmation
// (DESKTOP-SPEC §1). A file's address, a stream's or a web page's video card and qualities, the
// file name with its size, the category (which proposes its folder), the queue and the folder;
// Cancel download, Download later, Start download. Its proposal is captured once: later updates
// of the download do not overwrite what is being edited. It is a views.Modal of kind
// ModalConfirm.
type Confirm struct {
	*views.Dialog
	app *views.App
	o   ConfirmOptions

	download  *downexv1.Download
	settings  *downexv1.Settings
	proposed  struct{ dir, filename, category string }
	info      *downexv1.MediaInfo
	site      bool
	qualities bool

	filename, dir, category string
	attempted               bool
	busy                    string // "", "start", "later", "cancel"
	failure                 string
	queueID, proposedQueue  int64
	queues                  []*downexv1.Queue
	openedAt                time.Time
	gone                    bool
	container               kit.DIP // the dialog's content width (@container dialog), from the form's

	form           *grid
	cardArea       *kit.Element
	Video          *VideoCard
	address        *views.Field
	url            *clampText
	qualityArea    *kit.Element
	Picker         *Picker
	fields         *kit.Element
	nameHint       *views.Text
	Name           *widgets.Input
	nameErr        *views.Text
	where          *grid
	CategorySelect *widgets.Select
	QueueSelect    *widgets.Select
	queueField     *views.Field
	queueHint      *views.Text
	dirField       *views.Field
	categoryHint   *views.Text
	Folder         *widgets.Input
	browse         *widgets.Button
	dirErr         *views.Text
	failBox        *views.Note
	failText       *views.Text

	cancel, later, start *widgets.Button
	chip                 *waitingChip
}

// NewConfirm makes the Confirm dialog of d (App.svelte's {#if dialog?.kind === 'confirm'}).
func NewConfirm(app *views.App, d *downexv1.Download, o ConfirmOptions) *Confirm {
	c := &Confirm{app: app, o: o, download: d}
	c.settings = o.Settings
	if c.settings == nil {
		c.settings, _ = app.Settings()
	}
	c.proposed.dir, c.proposed.filename, c.proposed.category = d.GetDir(), d.GetFilename(), d.GetCategory()
	known := false
	for _, cat := range c.settings.GetCategories() {
		if cat.GetName() == c.proposed.category {
			known = true
		}
	}
	if media.IsMedia(d) {
		c.info = d.GetMedia()
	}
	c.site = c.info != nil && media.IsSite(c.info)
	c.filename, c.dir = c.proposed.filename, c.proposed.dir
	if known {
		c.category = c.proposed.category
	}
	c.proposedQueue = query.QueueOf(d)
	c.queueID = c.proposedQueue
	c.queues = o.Queues
	if c.queues == nil {
		c.queues = app.QueueChoices()
	}
	c.qualities = c.info != nil && len(media.VideoTracks(c.info.GetTracks(), c.site, c.info.GetSelectedVideo())) > 0
	c.build()
	c.refresh()
	return c
}

func (c *Confirm) waiting() int {
	if c.o.Waiting <= 0 {
		return 1
	}
	return c.o.Waiting
}

// WaitingText is "2 more downloads are waiting for confirmation.".
func (c *Confirm) WaitingText() string {
	n := c.waiting()
	verb := "downloads are"
	if n == 2 {
		verb = "download is"
	}
	return strconv.Itoa(n-1) + " more " + verb + " waiting for confirmation."
}

func (c *Confirm) offerQueues() bool { return len(c.queues) > 1 && !c.o.NoMove }

func (c *Confirm) chosenQueue() *downexv1.Queue {
	for _, q := range c.queues {
		if q.GetId() == c.queueID {
			return q
		}
	}
	return nil
}

// laterQueued: Download later leaves it queued in the chosen queue, to start when the queue
// runs.
func (c *Confirm) laterQueued() bool { q := c.chosenQueue(); return q != nil && !q.GetRunning() }

func (c *Confirm) now() time.Time {
	if c.o.Now != nil {
		return c.o.Now()
	}
	if w := c.app.Window; w != nil {
		return w.Now()
	}
	return time.Now()
}

// tooSoon: activation right after the dialog appeared by itself is not a decision about it.
func (c *Confirm) tooSoon() bool {
	g := c.o.Guard
	if g <= 0 {
		g = confirm.Guard
	}
	return !c.o.Requested && c.now().Sub(c.openedAt) < g
}

func (c *Confirm) pick() media.TrackPick {
	if c.Picker != nil {
		return c.Picker.Pick()
	}
	return media.DefaultPick(c.info)
}

// SizeLabel is the size after "Size: ".
func (c *Confirm) SizeLabel() string {
	if c.info != nil {
		if s := media.PickSizeText(media.PickSizeOf(c.info, c.pick())); s != "" {
			return s
		}
		return "unknown"
	}
	if h := display.SizeHint(c.download); h > 0 {
		return format.Bytes(float64(h))
	}
	return "unknown"
}

type confirmErrors struct{ filename, dir string }

func (c *Confirm) errors() confirmErrors {
	if !c.attempted {
		return confirmErrors{}
	}
	var e confirmErrors
	if name := text.TrimJS(c.filename); name != "" {
		e.filename = validate.CheckFilename(name)
	}
	if folder := text.TrimJS(c.dir); folder == "" {
		e.dir = "Choose a folder"
	} else {
		e.dir = validate.CheckDir(folder)
	}
	return e
}

func (c *Confirm) description() string {
	switch {
	case c.site:
		return "A video from the browser is waiting. Choose its quality and where it is saved, then start it."
	case c.info != nil:
		return "A stream from the browser is waiting. Choose its quality and where it is saved, then start it."
	}
	return "A download from the browser is waiting. Check where it is saved, then start it."
}

func (c *Confirm) build() {
	width := kit.Rem(44)
	if c.qualities {
		width = kit.Rem(56)
	}
	c.Dialog = c.app.NewDialog(views.ModalConfirm, widgets.DialogOptions{
		Title: "Confirm download", Description: c.description(), Size: widgets.DialogMd, Width: width,
		FocusSelf: !c.o.Requested, OnSubmit: func() { c.confirm(true) }, OnClose: c.closed,
	})
	c.SetIcon(newFileTile(categories.KindOf(c.download), 32))
	if c.waiting() > 1 {
		c.chip = newWaitingChip(strconv.Itoa(c.waiting()-1)+" more waiting", c.WaitingText())
		c.SetAside(c.chip)
	}

	split := c.qualities
	c.form = newGrid(func(w kit.DIP, s kit.Scale) []float32 {
		if split && c.containerWide(w, s) {
			return []float32{1, 1.25}
		}
		return []float32{1}
	}, 12, 20)
	c.form.start = true
	c.form.onArrange = func(w kit.DIP) {
		if w >= 0 {
			c.container = w + 40 // the dialog's content box: the form and the body's padding
		}
	}
	c.form.flexRow = func(w kit.DIP, s kit.Scale) int {
		if split && c.failure != "" && c.containerWide(w, s) {
			return 3
		}
		return 0
	}
	c.form.place = func(e *kit.Element, w kit.DIP, s kit.Scale) (area, bool) {
		if !split || !c.containerWide(w, s) {
			return area{}, false
		}
		failed := c.failure != ""
		switch e {
		case c.cardArea.Base():
			return area{col: 1, colSpan: -1, row: 1}, true
		case c.qualityArea.Base():
			return area{col: 1, row: 2}, true
		case c.fields:
			if failed {
				return area{col: 2, row: 2, rowSpan: 2}, true
			}
			return area{col: 2, row: 2}, true
		case c.failBox.Base():
			return area{col: 1, row: 3}, true
		}
		return area{}, false
	}
	c.Body.Add(c.form)

	if c.info != nil {
		c.cardArea = &kit.Element{Style: kit.Style{MinWidth: kit.Px(0)}}
		label := "Address"
		if c.site {
			label = "Page"
		}
		c.Video = NewVideoCard(c.info, CardOptions{FallbackTitle: c.proposed.filename, URL: c.download.GetUrl(), ShowAddress: true, AddressLabel: label})
		c.cardArea.Add(c.Video)
		c.form.Add(c.cardArea)
		c.Picker = NewPicker(c.info, media.DefaultPick(c.info))
		c.Picker.OnChange = func(media.TrackPick) { c.refresh() }
		c.Picker.OnSubmit = func() { c.confirm(true) }
		if c.qualities {
			c.qualityArea = &kit.Element{Style: kit.Style{MinWidth: kit.Px(0)}}
			if w := c.Picker.Part(PartVideo); w != nil {
				c.qualityArea.Add(w)
			}
			c.form.Add(c.qualityArea)
		}
	} else {
		c.cardArea = &kit.Element{}
		c.url = newClampText(text.EscapeBidi(c.download.GetUrl()))
		lab := views.NewText("Address", views.FontLabel, views.InkInk, kit.TextOptions{})
		c.address = views.NewField("", lab, c.url)
		c.form.Add(c.address)
	}
	if c.qualityArea == nil {
		c.qualityArea = &kit.Element{}
	}

	c.fields = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp6, MinWidth: kit.Px(0)}}
	if c.Picker != nil {
		part := PartAll
		if c.qualities {
			part = PartAudio
		}
		if w := c.Picker.Part(part); w != nil {
			c.fields.Add(w)
		}
	}
	c.nameHint = views.NewHint("")
	ph := c.proposed.filename
	if ph == "" {
		ph = "Named by the server"
	}
	c.Name = widgets.NewInput(c.filename, widgets.InputOptions{Name: "File name", Placeholder: ph, AutoDirection: true}, func(s string) {
		c.filename = s
		c.refresh()
	})
	c.Name.Autofocus = c.o.Requested
	c.nameErr = views.NewErrorText("")
	c.fields.Add(views.NewField("", views.NewLabelRow("File name", c.nameHint), c.Name, c.nameErr))
	c.buildWhere()
	c.fields.Add(c.where)
	c.form.Add(c.fields)

	c.failText = views.NewParagraph("", views.FontCell, views.InkInk2)
	c.failBox = views.NewNote(views.NoteFail, "", c.failText)
	c.failBox.Semantics.Role = kit.RoleAlert
	c.failBox.Semantics.Live = kit.LiveAssertive
	c.form.Add(c.failBox)

	c.cancel = widgets.NewButton("Cancel download", "", widgets.DangerText, widgets.SizeMd, c.onCancel)
	c.later = widgets.NewButton("Download later", "", widgets.Secondary, widgets.SizeMd, func() { c.confirm(false) })
	c.start = widgets.NewButton("Start download", "", widgets.Primary, widgets.SizeMd, func() { c.confirm(true) })
	c.start.Semantics.States |= kit.NodeDefault
	c.SetFooter(nil, c.cancel, c.later, c.start)
	c.cancel.Style.Margin = kit.Edges{End: kit.Auto} // .cancel { margin-inline-end: auto } (after SetFooter sizes it)
}

func (c *Confirm) containerWide(w kit.DIP, s kit.Scale) bool { return w < 0 || w+40 >= s.Rem(42) }

func (c *Confirm) buildWhere() {
	opts := make([]widgets.SelectOption, 0, len(c.settings.GetCategories())+1)
	for _, cat := range c.settings.GetCategories() {
		opts = append(opts, widgets.SelectOption{Value: cat.GetName(), Label: text.EscapeBidi(cat.GetName())})
	}
	opts = append(opts, widgets.SelectOption{Value: "", Label: "None"})
	c.categoryHint = views.NewHint("Choosing a category proposes its folder.")
	c.CategorySelect = widgets.NewSelect(opts, c.category, widgets.SelectOptions{Label: "Category"}, c.chooseCategory)
	c.CategorySelect.Semantics.Description = "Choosing a category proposes its folder."
	catField := views.NewField("Category", c.CategorySelect)

	withQueue := c.offerQueues()
	split := c.qualities
	c.where = newGrid(func(w kit.DIP, s kit.Scale) []float32 {
		// @container dialog (max-width: 32rem): one column
		if c.container > 0 && c.container <= s.Rem(32) {
			return []float32{1}
		}
		switch {
		case withQueue:
			return []float32{1, 1}
		case split:
			return []float32{1}
		}
		return []float32{1, 1.9}
	}, 12, 16)
	c.where.start = true
	c.where.Add(catField)

	if withQueue {
		qopts := make([]widgets.SelectOption, 0, len(c.queues))
		for _, q := range c.queues {
			l := text.EscapeBidi(q.GetName())
			if !q.GetRunning() {
				l += " (stopped)"
			}
			qopts = append(qopts, widgets.SelectOption{Value: strconv.FormatInt(q.GetId(), 10), Label: l})
		}
		c.QueueSelect = widgets.NewSelect(qopts, strconv.FormatInt(c.queueID, 10), widgets.SelectOptions{Label: "Queue"}, func(v string) {
			c.queueID, _ = strconv.ParseInt(v, 10, 64)
			c.refresh()
		})
		c.queueField = views.NewField("Queue", c.QueueSelect)
		c.queueHint = views.NewHint("")
		c.queueHint.Style.Margin = kit.Edges{Top: kit.Px(-6)} // margin-block-start: calc(-1 * var(--sp-3))
		c.where.Add(c.queueField, c.queueHint)
	}

	c.Folder = widgets.NewInput(c.dir, widgets.InputOptions{Name: "Save to folder", Mono: true}, func(s string) {
		c.dir = s
		c.refresh()
	})
	c.Folder.Style.Padding.Start = kit.Px(34)
	box := newOverlay(c.Folder)
	box.at(icon("folder", 16, faint), false, 11)
	box.Style.Flex = kit.Flex1
	c.browse = widgets.NewButton("Browse…", "", widgets.Secondary, widgets.SizeMd, c.onBrowse)
	c.browse.Style.Height, c.browse.Style.MinHeight = kit.Px(36), kit.Px(36)
	withButton := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: tokens.Sp4, MinWidth: kit.Px(0)}}
	withButton.Add(box, c.browse)
	c.dirErr = views.NewErrorText("")
	c.dirField = views.NewField("", views.NewLabelRow("Save to", c.categoryHint), withButton, c.dirErr)
	c.where.Add(c.dirField)
	c.where.place = func(e *kit.Element, w kit.DIP, s kit.Scale) (area, bool) {
		if withQueue && (e == c.queueHint.Base() || e == c.dirField.Base()) {
			return area{colSpan: -1}, false
		}
		return area{}, false
	}
}

// refresh shows the dialog's state.
func (c *Confirm) refresh() {
	errs := c.errors()
	saved := ""
	if c.site {
		saved = " · saved as MP4"
	}
	c.nameHint.SetText("Size: " + c.SizeLabel() + saved)
	c.Name.Semantics.Description = describe(c.nameHint.Text(), errs.filename)
	setError(c.nameErr, errs.filename)
	c.Name.SetInvalid(errs.filename != "")
	setError(c.dirErr, errs.dir)
	c.Folder.SetInvalid(errs.dir != "")
	c.Folder.Semantics.Description = errs.dir
	c.Folder.Tooltip = text.EscapeBidi(c.dir)
	busy := c.busy != ""
	c.SetBusy(busy)
	c.CategorySelect.SetDisabled(busy)
	if c.CategorySelect.Value() != c.category {
		c.CategorySelect.SetValue(c.category)
	}
	if c.QueueSelect != nil {
		c.QueueSelect.SetDisabled(busy)
		h := "Start download queues it here; Download later keeps it paused in this queue."
		if c.laterQueued() {
			h = "Download later puts it in this queue: it starts when the queue runs."
		}
		c.queueHint.SetText(h)
		c.QueueSelect.Semantics.Description = h
	}
	if c.Picker != nil {
		c.Picker.SetDisabled(busy)
		if c.Video != nil {
			pic := ""
			if v := media.TrackByID(c.info, c.Picker.Pick().Video); v != nil {
				pic = media.VideoPictureText(v, c.site)
			}
			c.Video.SetPicture(pic)
		}
	}
	setDisabled(c.browse, busy)
	c.failText.SetText(text.EscapeBidi(c.failure))
	c.failBox.SetHidden(c.failure == "")
	c.form.Relayout()

	setDisabled(c.cancel, busy)
	setDisabled(c.later, busy)
	setDisabled(c.start, busy)
	c.cancel.SetText(pick(c.busy == "cancel", "Canceling…", "Cancel download"))
	c.later.SetText(pick(c.busy == "later", "Saving…", "Download later"))
	c.start.SetText(pick(c.busy == "start", "Starting…", "Start download"))
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func (c *Confirm) goFn(f func()) {
	if c.o.Go != nil {
		c.o.Go(f)
		return
	}
	c.app.Go(f)
}

func (c *Confirm) chooseCategory(name string) {
	c.category = name
	c.dir = confirm.CategoryFolder(c.settings, name)
	c.Folder.SetText(c.dir)
	c.refresh()
}

func (c *Confirm) onBrowse() {
	p := c.o.PickFolder
	if p == nil && c.app.Platform != nil {
		p = c.app.Platform.PickFolder
	}
	if p == nil {
		return
	}
	initial := text.TrimJS(c.dir)
	if initial == "" {
		initial = c.settings.GetDefaultDir()
	}
	c.goFn(func() {
		ctx, cancel := c.app.CallContext()
		picked, err := p(ctx, initial)
		cancel()
		if err != nil || picked == "" {
			return
		}
		c.app.UI(func() {
			if c.gone {
				return
			}
			c.dir = picked
			c.Folder.SetText(picked)
			c.refresh()
		})
	})
}

// confirm is Start download (start) and Download later.
func (c *Confirm) confirm(start bool) {
	if c.busy != "" || c.tooSoon() || c.gone {
		return
	}
	c.attempted = true
	c.failure = ""
	c.refresh()
	errs := c.errors()
	if errs.filename != "" || errs.dir != "" {
		if w := c.app.Window; w != nil {
			if errs.filename != "" {
				w.Focus(c.Name)
			} else {
				w.Focus(c.Folder)
			}
		}
		return
	}
	typed := text.TrimJS(c.filename)
	// a web page's video is always one MP4 file: a renamed one keeps the extension
	name := typed
	if c.site && typed != "" && !strings.HasSuffix(strings.ToLower(typed), ".mp4") {
		name = typed + ".mp4"
	}
	folder := text.TrimJS(c.dir)
	c.busy = "later"
	if start {
		c.busy = "start"
	}
	c.refresh()
	var chosen int64 = -1
	if q := c.chosenQueue(); c.offerQueues() && q != nil {
		chosen = q.GetId()
	}
	var named int64
	move := int64(0)
	if chosen >= 0 {
		if (start && chosen != c.proposedQueue) || (!start && c.laterQueued()) {
			named = chosen
		} else if !start && chosen != c.proposedQueue {
			move = chosen
		}
	}
	req := &downexv1.ConfirmRequest{Id: c.download.GetId(), Start: start, QueueId: named}
	if folder != c.proposed.dir {
		req.Dir = folder
	}
	if name != c.proposed.filename {
		req.Filename = name
	}
	if c.info != nil {
		p := c.Picker.Pick()
		req.VideoTrack, req.AudioTrack = p.Video, media.PickedAudio(c.info, p)
	}
	confirmFn := c.o.Confirm
	if confirmFn == nil {
		confirmFn = func(ctx context.Context, r *downexv1.ConfirmRequest) error {
			_, err := c.app.Main.Downloads.Confirm(ctx, r)
			return err
		}
	}
	moveFn := c.moveFn()
	c.goFn(func() {
		ctx, cancel := c.app.CallContext()
		defer cancel()
		var err error
		if move != 0 && moveFn != nil {
			err = moveFn(ctx, move)
		}
		if err == nil {
			err = confirmFn(ctx, req)
		}
		c.app.UI(func() { c.finished(err) })
	})
}

func (c *Confirm) moveFn() func(ctx context.Context, q int64) error {
	if c.o.Move != nil {
		return c.o.Move
	}
	if c.app.Main == nil {
		return nil
	}
	id := c.download.GetId()
	return func(ctx context.Context, q int64) error {
		if _, err := c.app.Main.Queues.Move(ctx, []int64{id}, q, -1); err != nil {
			return err
		}
		c.app.Main.Downloads.AssignQueue([]int64{id}, q)
		return nil
	}
}

func (c *Confirm) finished(err error) {
	if c.gone {
		return
	}
	c.busy = ""
	if err != nil {
		c.failure = state.Message(err)
		c.refresh()
		return
	}
	c.refresh()
	c.done()
}

func (c *Confirm) onCancel() {
	if c.busy != "" || c.tooSoon() || c.gone {
		return
	}
	c.failure = ""
	c.busy = "cancel"
	c.refresh()
	cancelFn := c.o.Cancel
	if cancelFn == nil {
		id := c.download.GetId()
		cancelFn = func(ctx context.Context) error {
			out := c.app.Main.Downloads.Remove(ctx, []int64{id}, false)
			if len(out) > 0 && !out[0].OK() {
				return out[0].Err.Err()
			}
			return nil
		}
	}
	c.goFn(func() {
		ctx, cancel := c.app.CallContext()
		err := cancelFn(ctx)
		cancel()
		c.app.UI(func() { c.finished(err) })
	})
}

// Failure is the refusal shown ("" for none).
func (c *Confirm) Failure() string { return c.failure }

// Download is the download the dialog is about.
func (c *Confirm) Download() *downexv1.Download { return c.download }

// done closes the dialog after a confirmation or a cancel (onclose).
func (c *Confirm) done() {
	c.gone = true
	c.Remove()
	if c.o.OnClose != nil {
		c.o.OnClose()
	}
}

func (c *Confirm) closed() {
	c.gone = true
	if c.o.OnClose != nil {
		c.o.OnClose()
	}
}

// ShowModal is views.Modal's: the guard starts.
func (c *Confirm) ShowModal(w *kit.Window, done func()) kit.Widget {
	c.openedAt = c.now()
	return c.Dialog.ShowModal(w, done)
}

// DismissModal is views.Modal's.
func (c *Confirm) DismissModal() {
	c.gone = true
	c.Dialog.DismissModal()
}

// ---- small widgets ---------------------------------------------------------------------------

// waitingChip is the header's "2 more waiting": a .chip on --info-soft without a border, in
// --info 600; its accessible name the whole sentence.
type waitingChip struct {
	kit.Element
	label *kit.Label
}

func newWaitingChip(shown, full string) *waitingChip {
	c := &waitingChip{}
	c.Semantics.Role = kit.RoleText
	c.Semantics.Name = full
	c.Tooltip = full
	c.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone, Height: kit.Px(22),
		Padding: kit.EdgesInline(kit.Px(8)), Border: kit.EdgesAll(kit.Px(1)),
	}
	f := views.FontHint
	f.Weight = 600
	f.LineHeight = 1
	c.label = kit.NewLabel(shown, f, kit.TextOptions{})
	c.label.Semantics.Role = kit.RoleNone
	c.Add(c.label)
	return c
}

// Text is the chip's visible text.
func (c *waitingChip) Text() string { return c.label.Text() }

// Paint is kit.Painter's.
func (c *waitingChip) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	r := views.SnapBox(p, p.Size().H/2)
	ground := t.InfoSoft
	p.Canvas.FillRoundedRect(r, ground)
	if t.IsForced() {
		p.Canvas.StrokeRoundedRect(r.Inset(0.5), kit.Stroke{Width: 1}, t.Ink)
	}
	c.label.SetColor(t.Info)
}

// fileTile is FileIcon.svelte: the file kind's tile (decorative).
type fileTile struct {
	kit.Element
	kind icons.TileKind
	size kit.DIP
	set  *icons.Set
	res  kit.Resources
}

func newFileTile(kind categories.FileKind, size kit.DIP) *fileTile {
	t := &fileTile{kind: icons.TileOther, size: size}
	for k := icons.TileVideo; k <= icons.TileOther; k++ {
		if k.String() == string(kind) {
			t.kind = k
		}
	}
	t.Semantics.Hidden = true
	t.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	return t
}

// Kind is the tile's kind ("video", "programs", …).
func (t *fileTile) Kind() string { return t.kind.String() }

// Detached is kit.Detacher's.
func (t *fileTile) Detached() {
	if t.set != nil {
		t.set.Release()
		t.set, t.res = nil, nil
	}
}

// Paint is kit.Painter's.
func (t *fileTile) Paint(p *kit.PaintContext) {
	w := t.Window()
	if w == nil || w.Host() == nil {
		return
	}
	if res := w.Host().Resources(); res != t.res || t.set == nil {
		t.Detached()
		t.set, t.res = icons.NewSet(res), res
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	t.set.DrawTile(p.Canvas, t.kind, at, t.size, views.TokensOf(&t.Element))
}

// clampText is Confirm's p.url: the address in mono on --surface-2 with a --line border and 9
// px corners, 7 11 px padding, at most three lines with an ellipsis, the whole as its tooltip.
type clampText struct {
	kit.Element
	label *views.Text
}

func newClampText(s string) *clampText {
	c := &clampText{}
	c.Tooltip = s
	c.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesOf(kit.Px(7), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)), MinWidth: kit.Px(0)}
	c.label = views.NewText(s, views.FontMono, views.InkInk2, kit.TextOptions{Wrap: kit.TextWrapAnywhere, MaxLines: 3, Truncate: kit.TruncateEnd})
	c.label.Style.MinWidth = kit.Px(0)
	c.Add(c.label)
	return c
}

// Text is the whole address.
func (c *clampText) Text() string { return c.label.Text() }

// Paint is kit.Painter's.
func (c *clampText) Paint(p *kit.PaintContext) {
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
