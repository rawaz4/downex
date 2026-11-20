package add

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/media"
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// Defaults are the settings the Add dialog starts from (App.svelte's defaults prop).
type Defaults struct {
	// Dir is the default download folder (its placeholder and the folder the picker opens).
	Dir string
	// MaxConnections is the connections a download gets (0: 8).
	MaxConnections int32
}

// Options are what the Add dialog is given beside its request (AddDialog.svelte's props).
// Every function runs off the UI thread (App.Go) and may block; a nil one takes the App's.
type Options struct {
	// Defaults; zero takes App.Settings()'s default folder and connections.
	Defaults *Defaults
	// PickFolder is the folder picker ("" with a nil error: cancelled); nil is the platform's.
	PickFolder func(ctx context.Context, initial string) (string, error)
	// Submit adds files (App.svelte's addDownloads); nil is DownloadsStore.Add.
	Submit func(ctx context.Context, reqs []*downexv1.AddRequest) []state.AddOutcome
	// ProbeMedia lists a manifest's or a web page's qualities; nil is DownloadsStore.ProbeMedia.
	ProbeMedia func(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error)
	// SubmitMedia adds a stream or a page's video (App.svelte's addMediaDownload); nil is
	// DownloadsStore.AddMedia.
	SubmitMedia func(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.Download, error)
	// FilesOnly leaves the "Video or stream" mode out (AddDialog without probeMedia and
	// submitMedia).
	FilesOnly bool
	// FreeSpace is the bytes free on a folder's volume; nil is the platform's. NoFreeSpace
	// shows none.
	FreeSpace   func(dir string) (int64, error)
	NoFreeSpace bool
	// Queues to choose from (fewer than two: no choice); nil is App.QueueChoices().
	Queues []*downexv1.Queue
	// DefaultQueue is the queue proposed (0: the main queue; the list's queue filter).
	DefaultQueue int64
	// Tools is yt-dlp's and Deno's status (their install progress while a page waits for
	// them); nil follows App.Main.Tools.
	Tools func() *downexv1.ToolsStatus
	// OnOpenSettings opens Settings → Video sites (offered when yt-dlp could not be set up).
	OnOpenSettings func()
	// OnClose runs when the dialog has gone: closed, or after a successful add.
	OnClose func()
	// Go runs background work (nil: App.Go). RetryClock is what the web page retry loop
	// waits with (nil: App.Clock).
	Go         func(func())
	RetryClock media.Clock
}

const (
	modeFiles  = "files"
	modeStream = "stream"
)

type busyKind uint8

const (
	idle busyKind = iota
	busyAdd
	busyProbe
)

// failure is an address that could not be added, and why.
type failure struct{ url, err string }

// probeResult is the qualities of the address last listed.
type probeResult struct {
	url  string
	info *downexv1.MediaInfo
}

// running is a call of the "Video or stream" mode in progress: Cancel and a changed address
// stop it.
type running struct {
	cancel context.CancelFunc
	url    string
}

var absoluteDir = regexp.MustCompile(`^([A-Za-z]:[\\/]|[\\/])`)

// Add is AddDialog.svelte (DESIGN-SYSTEM.md §4.6): "Add" with the Files / Video or stream mode in
// the header. Files: the addresses, the folder and More options (name, connections, speed
// limit, queue, paused) behind a summary of what it holds. Video or stream: the address (✓
// Found once listed), the video card, one quality card per class, the audio track beside the
// folder, More options (name, queue, paused); the footer sums up what will be added. It is a
// views.Modal of kind ModalAdd: show it with App.Dialogs.Show.
type Add struct {
	*views.Dialog
	app *views.App
	o   Options

	canStream bool
	defaults  Defaults
	queues    []*downexv1.Queue

	mode        string
	urls        string
	streamURL   string
	dir         string
	filename    string
	connections string
	speedValue  string
	speedUnit   format.RateUnit
	startPaused bool
	queueID     int64
	attempted   bool
	busy        busyKind
	failures    []failure
	moreOpen    bool
	hiddenErr   bool // an error inside More options shows ($effect on hiddenError)

	probed          *probeResult
	pick            media.TrackPick
	streamFailure   string
	failureSettings bool
	run             *running
	waiting         *media.SiteRetry
	startedAt, now  time.Time
	tools           *downexv1.ToolsStatus

	free      int64 // -1: none shown
	freeGen   int
	askedOnce bool
	freeTimer *kit.Timer
	ticker    *kit.Timer
	fitted    kit.Size
	container kit.DIP // the dialog's content width (@container dialog), from the form's
	gone      bool
	stopTools func()

	shownInfo *downexv1.MediaInfo // the info the card and picker were made for

	// widgets
	seg        *widgets.SegmentedControl
	form       *grid
	filesField *views.Field
	urlsHint   *views.Text
	urlsArea   *widgets.TextArea
	urlsErr    *views.Text
	suggest    *views.Note

	source     *kit.Element
	streamBox  *overlay
	stream     *widgets.Input
	foundMark  *kit.Element
	streamHint *views.Text
	streamErr  *views.Text
	waitBox    *kit.Element
	setupPart  *kit.Element
	setupBar   *widgets.ProgressBar // in setupPart while the tools are set up
	setupText  *views.Text
	gettingRow *kit.Element
	spinner    *views.Spinner // in gettingRow while it shows
	elapsed    *views.Text

	cardArea    *kit.Element
	Video       *VideoCard
	qualityArea *kit.Element
	// Items is the row of a page with several videos (nil for other pages,
	// docs/SITE-MEDIA-SPEC.md §3.12): above the quality cards.
	Items  *ItemRow
	Picker *Picker

	pair      *kit.Element
	audioSlot *kit.Element
	// Clip is the "Save only part of the video" section of a found video (nil before).
	Clip     *ClipSection
	dirField *views.Field
	freeText *views.Text
	folder   *widgets.Input
	change   *widgets.Button
	dirErr   *views.Text

	more        *kit.Element
	moreToggle  *moreToggle
	moreFiles   *grid
	moreStream  *kit.Element
	fileName    *widgets.Input // Files mode's
	fileNameHnt *views.Text
	fileNameErr *views.Text
	conns       *widgets.Input
	connsErr    *views.Text
	speed       *widgets.Input
	unit        *widgets.Select
	speedErr    *views.Text
	queueFiles  *queueField
	pausedFiles *widgets.Checkbox
	streamRow   *grid
	streamName  *widgets.Input // Video or stream mode's
	streamNmErr *views.Text
	queueStream *queueField
	pausedStrm  *widgets.Checkbox

	failBox *views.Note

	summary *summaryLine
	cancel  *widgets.Button
	submit  *widgets.Button
}

// queueField is the Queue select with its hint.
type queueField struct {
	*views.Field
	sel  *widgets.Select
	hint *views.Text
}

// New makes the Add dialog for req (App.svelte's openAdd).
func New(app *views.App, req views.AddRequest, o Options) *Add {
	a := &Add{app: app, o: o, free: -1, speedUnit: format.MBps}
	a.canStream = !o.FilesOnly
	if o.Defaults != nil {
		a.defaults = *o.Defaults
	} else if s, _ := app.Settings(); s != nil {
		a.defaults = Defaults{Dir: s.GetDefaultDir(), MaxConnections: s.GetDefaultMaxConnections()}
	}
	a.queues = o.Queues
	if a.queues == nil {
		a.queues = app.QueueChoices()
	}
	initial := validate.SplitURLs(req.URLs)
	manifestGiven := len(initial) == 1 && media.LooksLikeManifest(initial[0])
	startAsStream := a.canStream && (manifestGiven || req.Mode == views.AddStream)
	streamGiven := startAsStream && len(initial) == 1
	a.mode = modeFiles
	if startAsStream {
		a.mode = modeStream
	}
	if streamGiven {
		a.streamURL = initial[0]
	} else {
		a.urls = req.URLs
	}
	a.connections = strconv.Itoa(int(a.maxConns()))
	a.queueID = state.MainQueueID
	for _, q := range a.queues {
		if q.GetId() == o.DefaultQueue {
			a.queueID = o.DefaultQueue
		}
	}
	switch {
	case o.Tools != nil:
		a.tools = o.Tools()
	case app.Main != nil && app.Main.Tools != nil:
		a.tools = app.Main.Tools.Status()
	}
	a.build()
	a.refresh()
	return a
}

func (a *Add) maxConns() int32 {
	if a.defaults.MaxConnections != 0 {
		return a.defaults.MaxConnections
	}
	return 8
}

func (a *Add) goFn(f func()) {
	if a.o.Go != nil {
		a.o.Go(f)
		return
	}
	a.app.Go(f)
}

func (a *Add) clock() media.Clock {
	if a.o.RetryClock != nil {
		return a.o.RetryClock
	}
	return appClock{a.app.Clock}
}

// appClock is media.Clock over a state.Clock.
type appClock struct{ c state.Clock }

func (c appClock) Now() time.Time { return c.c.Now() }
func (c appClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.c.AfterFunc(d, func() { ch <- c.c.Now() })
	return ch
}

func (a *Add) wallNow() time.Time {
	if a.app.Clock != nil {
		return a.app.Clock.Now()
	}
	return time.Now()
}

// ---- what the dialog derives ----------------------------------------------------------------

func (a *Add) urlList() []string { return validate.SplitURLs(a.urls) }

func (a *Add) site() bool { return !media.LooksLikeManifest(a.streamURL) }

func (a *Add) offerQueues() bool { return len(a.queues) > 1 }

func (a *Add) chosenQueue() *downexv1.Queue {
	for _, q := range a.queues {
		if q.GetId() == a.queueID {
			return q
		}
	}
	return nil
}

// Info is the qualities of the address in the field (a changed address must be listed again).
func (a *Add) Info() *downexv1.MediaInfo {
	if a.probed != nil && a.probed.url == text.TrimJS(a.streamURL) {
		return a.probed.info
	}
	return nil
}

// Found: a video's or stream's qualities are found; the dialog widens, the qualities get a
// column.
func (a *Add) Found() bool { return a.mode == modeStream && a.Info() != nil }

func (a *Add) stoppable() bool {
	return a.site() && (a.busy == busyProbe || (a.busy == busyAdd && a.waiting != nil))
}

func (a *Add) addForm() validate.AddForm {
	q := int64(0)
	if a.offerQueues() && a.queueID != state.MainQueueID {
		q = a.queueID
	}
	return validate.AddForm{URLs: a.urls, Dir: a.dir, Filename: a.filename, Connections: a.connections,
		SpeedValue: a.speedValue, SpeedUnit: a.speedUnit, StartPaused: a.startPaused, QueueID: q}
}

// formErrors are the shown errors (none until a submit was attempted).
type formErrors struct {
	urls, url, dir, filename, connections, speed string
}

func (a *Add) errors() formErrors {
	if !a.attempted {
		return formErrors{}
	}
	if a.mode == modeStream {
		_, e, ok := validate.ValidateStreamForm(validate.StreamForm{URL: a.streamURL, Dir: a.dir, Filename: a.filename})
		if ok {
			return formErrors{}
		}
		return formErrors{url: e.URL, dir: e.Dir, filename: e.Filename}
	}
	_, e, ok := validate.ValidateAddForm(a.addForm())
	if ok {
		return formErrors{}
	}
	return formErrors{urls: e.URLs, dir: e.Dir, filename: e.Filename, connections: e.Connections, speed: e.Speed}
}

func (e formErrors) hidden() bool { return e.filename != "" || e.connections != "" || e.speed != "" }

func (a *Add) folderPath() string {
	d := text.TrimJS(a.dir)
	if d == "" {
		d = a.defaults.Dir
	}
	return text.TrimJS(d)
}

func (a *Add) needed() int64 {
	if info := a.Info(); a.mode == modeStream && info != nil {
		return media.PickSizeOf(info, a.pick).Bytes
	}
	return -1
}

func (a *Add) tight() bool { n := a.needed(); return a.free >= 0 && n > 0 && n > a.free }

func (a *Add) settingUp() bool {
	return a.waiting != nil && a.waiting.Kind == media.ToolsInstalling || state.IsFirstInstall(a.tools)
}

func (a *Add) setupFraction() float64 {
	if f := state.InstallFraction(a.tools); !math.IsNaN(f) && !math.IsInf(f, 0) {
		return f
	}
	if a.waiting != nil && a.waiting.Kind == media.ToolsInstalling {
		return a.waiting.Fraction
	}
	return math.NaN()
}

// SubmitLabel is the primary button's text.
func (a *Add) SubmitLabel() string {
	if a.mode == modeStream {
		switch {
		case a.busy == busyProbe:
			return "Finding qualities…"
		case a.busy == busyAdd:
			return "Adding…"
		case a.Info() == nil:
			return "Find qualities"
		case a.site() && media.AudioPage(a.Info()):
			return "Add audio"
		case a.site():
			return "Add video"
		}
		return "Add stream"
	}
	if a.busy != idle {
		return "Adding…"
	}
	if n := len(a.urlList()); n > 1 {
		return "Add " + strconv.Itoa(n) + " downloads"
	}
	return "Add download"
}

func (a *Add) submitIcon() string {
	switch {
	case a.busy != idle:
		return ""
	case a.mode == modeStream && a.Info() == nil:
		return "search"
	}
	return "arrow-down"
}

// limitSummary is the footer's limit: as typed with the unit, or read back when a unit was
// typed ("2 MiB").
var plainNumber = regexp.MustCompile(`^[\d.,\s]+$`)

func limitSummary(t string, unit format.RateUnit) string {
	if plainNumber.MatchString(t) {
		return t + " " + string(unit)
	}
	if bps, ok := format.ParseRate(t, unit, 0); ok && bps > 0 {
		return format.Speed(float64(bps))
	}
	return t + " " + string(unit)
}

func (a *Add) queueName() string {
	if q := a.chosenQueue(); a.offerQueues() && q != nil {
		return text.EscapeBidi(q.GetName())
	}
	return ""
}

func (a *Add) startText() string {
	if a.startPaused {
		return "Add paused"
	}
	return "Start now"
}

// streamName is the file name More options would use, as far as the dialog knows it.
func (a *Add) streamFileName() string {
	ext := ".mp4"
	if info := a.Info(); info != nil && a.site() {
		ext = media.SiteExtension(info) // an audio page's .m4a or .mp3 (SITE-MEDIA-SPEC §3.13)
	}
	if typed := text.TrimJS(a.filename); typed != "" {
		if strings.HasSuffix(strings.ToLower(typed), ext) {
			return typed
		}
		return typed + ext
	}
	title := ""
	if info := a.Info(); info != nil {
		title = text.TrimJS(info.GetTitle())
	}
	switch {
	case title != "":
		return media.ClipName(title+ext, a.clip()) // the part's times, live (MEDIA-SPEC §8 C19)
	case a.site():
		return "Name from the video title"
	}
	return "Name from the stream"
}

func joinDot(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
}

// MoreSummary is what More options holds, while it is closed.
func (a *Add) MoreSummary() string {
	if a.mode == modeStream {
		return joinDot(a.queueName(), a.startText(), text.EscapeBidi(a.streamFileName()))
	}
	conns := text.TrimJS(a.connections)
	if conns == "" {
		conns = strconv.Itoa(int(a.maxConns()))
	}
	name := "Names from the URLs"
	if len(a.urlList()) <= 1 {
		name = text.EscapeBidi(text.TrimJS(a.filename))
		if name == "" {
			name = "Name from the server"
		}
	}
	limit := "No speed limit"
	if v := text.TrimJS(a.speedValue); v != "" {
		limit = "limit " + limitSummary(v, a.speedUnit)
	}
	return joinDot(name, conns+" connections", limit, a.queueName(), a.startText())
}

// streamSummary is the footer's summary of a listed video: "1080p · H.264", "+ English audio",
// "about 544 MB", "part 1:20 – 2:45".
type streamSummary struct{ head, sound, prefix, size, part string }

// clip is the part the section holds (the zero Clip: none, or the form has errors).
func (a *Add) clip() imedia.Clip {
	if a.Clip == nil {
		return imedia.Clip{}
	}
	c, _ := a.Clip.Clip()
	return c
}

func (a *Add) streamSum() *streamSummary {
	info := a.Info()
	if info == nil {
		return nil
	}
	video := media.TrackByID(info, a.pick.Video)
	audio := media.TrackByID(info, media.PickedAudio(info, a.pick))
	head := "Audio only"
	if video != nil {
		codec := ""
		if video.GetCodecs() != "" {
			first, _, _ := strings.Cut(video.GetCodecs(), ",")
			codec = media.CodecName(first)
		}
		head = joinDot(ShortClassName(media.QualityName(video)), codec)
		if head == "" {
			head = "Video"
		}
	}
	lang := ""
	if audio != nil {
		lang = media.LanguageName(audio.GetLanguage())
	}
	sound := ""
	switch {
	case video == nil:
		if lang != "" {
			sound = "· " + lang
		}
	case video.GetKind() == "muxed":
		sound = "with its sound"
	case media.NoSound(info, video):
		sound = "· no sound"
	case audio != nil:
		if lang != "" {
			sound = "+ " + lang + " audio"
		} else {
			sound = "+ audio"
		}
	}
	size := media.ClipSize(media.PickSizeOf(info, a.pick), a.clip(), info.GetDurationMs())
	prefix := ""
	switch size.Kind {
	case media.SizeEstimate:
		prefix = "about "
	case media.SizeApprox:
		prefix = "~"
	}
	s := ""
	if size.Bytes > 0 {
		s = format.Bytes(float64(size.Bytes))
	}
	return &streamSummary{head: head, sound: sound, prefix: prefix, size: s, part: media.ClipSummary(a.clip(), info.GetDurationMs())}
}

// FooterSummary is the footer's summary as its text reads ("2 links · 4 connections").
func (a *Add) FooterSummary() string { return a.summary.Text() }

func (a *Add) chosenPicture() string {
	info := a.Info()
	if info == nil {
		return ""
	}
	if v := media.TrackByID(info, a.pick.Video); v != nil {
		return media.VideoPictureText(v, media.IsSite(info))
	}
	return ""
}

// ---- building --------------------------------------------------------------------------------

func (a *Add) build() {
	title := "Add downloads"
	o := widgets.DialogOptions{Title: title, Size: widgets.DialogMd, OnSubmit: a.onSubmit, OnClose: a.closed}
	if a.canStream {
		o.Title = "Add"
		o.AriaLabel = "Add downloads"
		o.Width = kit.Rem(56) // the widest it gets; fitWidth narrows it to 38rem until found
	}
	a.Dialog = a.app.NewDialog(views.ModalAdd, o)

	if a.canStream {
		a.seg = widgets.NewSegmentedControl([]widgets.SegmentOption{
			{Value: modeFiles, Label: "Files", Icon: "link"},
			{Value: modeStream, Label: "Video or stream", Icon: "play"},
		}, a.mode, widgets.SegmentedOptions{Legend: "What to add"}, a.setMode)
		a.SetAside(a.seg)
	}

	a.form = newGrid(func(w kit.DIP, s kit.Scale) []float32 {
		if a.Found() && a.containerWide(w, s) {
			return []float32{1, 1.25}
		}
		return []float32{1}
	}, 16, 0)
	a.form.gaps = func(w kit.DIP, s kit.Scale) (kit.DIP, kit.DIP) {
		switch {
		case a.Found() && a.containerWide(w, s):
			return 12, 20
		case a.Found():
			return 12, 0
		}
		return 16, 0
	}
	a.form.start = true
	a.form.flexRow = func(w kit.DIP, s kit.Scale) int {
		if a.Found() && a.containerWide(w, s) {
			return 4
		}
		return 0
	}
	a.form.place = a.placeSplit
	a.form.onArrange = func(w kit.DIP) {
		if w >= 0 {
			a.container = w + 40
		}
	}
	a.Body.Add(a.form)

	a.buildFiles()
	a.buildSource()
	a.cardArea = &kit.Element{Style: kit.Style{MinWidth: kit.Px(0)}}
	a.qualityArea = &kit.Element{Style: kit.Style{MinWidth: kit.Px(0)}}
	a.buildPair()
	a.buildMore()
	a.failBox = views.NewNote(views.NoteFail, "")
	a.failBox.Semantics.Role = kit.RoleAlert
	a.failBox.Semantics.Live = kit.LiveAssertive
	a.failBox.Style.RowGap = tokens.Sp2
	a.form.Add(a.filesField, a.source, a.cardArea, a.qualityArea, a.pair, a.more, a.failBox)

	a.summary = newSummaryLine()
	a.cancel = widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, a.onCancel)
	a.submit = widgets.NewButton("Add download", "arrow-down", widgets.Primary, widgets.SizeMd, a.onSubmit)
	a.submit.Semantics.States |= kit.NodeDefault
	a.SetFooter(a.summary, a.cancel, a.submit)
}

// containerWide is the dialog container's (min-width: 42rem) for the form's width w.
func (a *Add) containerWide(w kit.DIP, s kit.Scale) bool {
	// the dialog's content box is the form and the body's 20 px padding either side
	return w < 0 || w+40 >= s.Rem(42)
}

// placeSplit is the split layout's areas (the Add dialog's .form.split rows).
func (a *Add) placeSplit(c *kit.Element, w kit.DIP, s kit.Scale) (area, bool) {
	if !a.Found() || !a.containerWide(w, s) {
		return area{}, false
	}
	switch c {
	case a.source.Base():
		return area{col: 1, colSpan: -1, row: 1}, true
	case a.cardArea.Base():
		return area{col: 1, colSpan: -1, row: 2}, true
	case a.qualityArea.Base():
		return area{col: 1, row: 3, rowSpan: 2}, true
	case a.pair.Base():
		return area{col: 2, row: 3}, true
	case a.more.Base():
		return area{col: 2, row: 4}, true
	}
	return area{colSpan: -1}, false // .split > * { grid-column: 1 / -1 }
}

func (a *Add) buildFiles() {
	a.urlsHint = views.NewHint("")
	row := views.NewLabelRow("URLs", a.urlsHint)
	a.urlsArea = widgets.NewTextArea(a.urls, 4, widgets.InputOptions{Name: "URLs", Placeholder: "https://example.com/file.zip"}, func(s string) {
		a.urls = s
		a.refresh()
	})
	a.urlsArea.Autofocus = a.mode == modeFiles
	area := &monoArea{TextArea: a.urlsArea}
	a.urlsArea.OnSubmit = a.onSubmit
	a.urlsErr = views.NewErrorText("")
	link := newFlow(
		run{text: "This address is a streaming playlist. ", font: views.FontCell, ink: views.InkInk2},
		run{text: "Download it as a video", font: linkFont(), ink: accentInk, press: func() { a.setMode(modeStream) }, name: "Download it as a video"},
		run{text: " to save the video rather than the playlist file.", font: views.FontCell, ink: views.InkInk2},
	)
	a.suggest = views.NewNote(views.NoteInfo, "", link)
	a.suggest.Style.Margin = kit.Edges{Top: tokens.Sp2}
	a.filesField = views.NewField("", row, area, a.urlsErr, a.suggest)
}

// monoArea is the URL list's textarea.input.mono: .input.mono's 0.75rem/1.4 wins over
// textarea.input's 1.55 (it comes later with a higher specificity), so its rows are 1.4 lines.
type monoArea struct{ *widgets.TextArea }

// HandleKey is kit.KeyHandler's: Ctrl+Enter adds ("Ctrl+Enter adds"), Enter breaks the line.
func (m *monoArea) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Key == kit.KeyEnter && (e.Mods == kit.ModCtrl || e.Mods == kit.ModMeta) {
		if m.OnSubmit != nil {
			m.OnSubmit()
		}
		return true
	}
	return m.TextArea.HandleKey(e)
}

// ContentHeight is kit.Layouter's: rows lines of 0.75rem × 1.4.
func (m *monoArea) ContentHeight(ctx *kit.LayoutContext, _ kit.DIP) kit.DIP {
	return kit.DIP(m.Rows) * ctx.Scale.Rem(0.75) * 1.4
}

func linkFont() kit.Font {
	f := views.FontCell
	f.Weight = 600
	return f
}

func accentInk(t *tokens.Theme) kit.Color { return t.AccentText }

func (a *Add) buildSource() {
	a.source = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp7, MinWidth: kit.Px(0)}}
	a.stream = widgets.NewInput(a.streamURL, widgets.InputOptions{Name: "Video page or stream address", Mono: true, Placeholder: "https://www.youtube.com/watch?v=…"}, func(s string) {
		a.streamURL = s
		a.streamChanged()
	})
	a.stream.Autofocus = a.mode == modeStream
	a.stream.Style.Padding.Start = kit.Px(34)
	a.streamBox = newOverlay(a.stream)
	a.streamBox.at(icon("globe", 16, faint), false, 11)
	a.foundMark = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(4)}}
	a.foundMark.PassThrough = true
	fm := views.FontHint
	fm.Weight = 600
	a.foundMark.Add(icon("check", 14, accentText), views.NewText("Found", fm, accentInk, kit.TextOptions{}))
	a.streamBox.at(a.foundMark, true, 11)
	a.streamHint = views.NewHint("A video page (YouTube and the other sites yt-dlp supports: one MP4 file in the quality you choose), or an HLS (.m3u8) or DASH (.mpd) manifest. DRM-protected and live videos cannot be downloaded.")
	a.streamErr = views.NewErrorText("")
	field := views.NewField("", a.streamBox, a.streamHint, a.streamErr)

	a.waitBox = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, Padding: kit.EdgesOf(tokens.Sp5, tokens.Sp6), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RXl)}}
	a.waitBox.Semantics.Live = kit.LivePolite
	a.setupPart = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3}}
	a.setupText = views.NewParagraph("", views.FontCell, views.InkInk)
	a.setupPart.Add(a.setupText, views.NewHint("The first time, Downex downloads yt-dlp (from github.com/yt-dlp) and Deno (from github.com/denoland/deno), which read the page."))
	a.gettingRow = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4}}
	gf := views.FontCell
	gf.Weight = 500
	a.elapsed = views.NewText("", views.FontCell, views.InkMuted, kit.TextOptions{})
	a.elapsed.Semantics.Hidden = true
	a.gettingRow.Add(views.NewText("Getting video info…", gf, views.InkInk2, kit.TextOptions{}), a.elapsed)
	a.waitBox.Add(a.setupPart, a.gettingRow)
	a.source.Add(field, &groundBox{Element: a.waitBox})
}

// showSpinner puts the spinner of "Getting video info…" in its row while the row shows, and
// takes it out otherwise: a spinner turns from the moment it is in the window, and a hidden one
// would repaint the idle dialog every frame ({#if} in AddDialog.svelte makes it only then).
func (a *Add) showSpinner(on bool) {
	switch {
	case on && a.spinner == nil:
		a.spinner = views.NewSpinner()
		a.gettingRow.Insert(0, a.spinner)
	case !on && a.spinner != nil:
		a.gettingRow.Remove(a.spinner)
		a.spinner = nil
	}
}

// showSetupBar makes the tools' set-up bar while it shows and takes it away after
// ({#if settingUp}): an indeterminate bar's hatch moves from the moment it is in the window.
func (a *Add) showSetupBar(on bool) {
	switch {
	case on && a.setupBar == nil:
		a.setupBar = widgets.NewProgressBar("Downloading yt-dlp and Deno", false)
		a.setupPart.Insert(0, a.setupBar)
	case !on && a.setupBar != nil:
		a.setupPart.Remove(a.setupBar)
		a.setupBar = nil
	}
}

// groundBox paints a box on --surface-2 with a --line border and 12 px corners (.waiting).
type groundBox struct {
	*kit.Element
}

// Paint is kit.Painter's.
func (g *groundBox) Paint(p *kit.PaintContext) {
	t := views.TokensOf(g.Element)
	r := views.SnapBox(p, 12)
	p.Canvas.FillRoundedRect(r, t.Line)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 12-w), t.Surface2)
}

func (a *Add) buildPair() {
	a.pair = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: tokens.Sp7, RowGap: tokens.Sp6, AlignItems: kit.AlignStart, MinWidth: kit.Px(0)}}
	a.audioSlot = &kit.Element{Style: kit.Style{Flex: kit.FlexOf(1, 1, kit.Px(0)), MinWidth: kit.Px(0)}}
	a.freeText = views.NewText("", views.FontHint, a.freeInk, kit.TextOptions{})
	row := views.NewLabelRow("Save to", a.freeText)
	a.folder = widgets.NewInput(a.dir, widgets.InputOptions{Name: "Save to folder", Mono: true, Placeholder: a.dirPlaceholder(), Description: "Leave empty to use the default folder."}, func(s string) {
		a.dir = s
		a.refresh()
		a.askFree()
	})
	a.folder.Style.Padding = kit.Edges{Start: kit.Px(34), End: kit.Px(76)}
	box := newOverlay(a.folder)
	box.at(icon("folder", 16, faint), false, 11)
	a.change = widgets.NewButton("Change", "", widgets.Ghost, widgets.SizeSm, a.browse)
	a.change.Semantics.Name = "Change folder"
	a.change.Tooltip = "Choose a folder"
	mutedLabel(a.change)
	box.at(a.change, true, 5)
	a.dirErr = views.NewErrorText("")
	a.dirField = views.NewField("", row, box, a.dirErr)
	a.dirField.Style.Flex = kit.FlexOf(1, 1, kit.Px(0))
	a.pair.Add(a.audioSlot, a.dirField)
}

func (a *Add) dirPlaceholder() string {
	if a.defaults.Dir != "" {
		return a.defaults.Dir
	}
	return "Default download folder"
}

func (a *Add) freeInk(t *tokens.Theme) kit.Color {
	if a.tight() {
		return t.Warn
	}
	return t.Faint
}

// mutedLabel gives the folder's .btn.ghost.sm.change its text in --muted (.change { color:
// var(--muted) }, in every state): the button's own label, which the button colours as a ghost
// button's, gives way to a text of the same font and options in --muted.
func mutedLabel(b *widgets.Button) {
	l := b.Label()
	t := views.NewText(l.Text(), l.Font(), views.InkMuted, l.Options())
	t.Style = l.Style
	t.Semantics.Role = kit.RoleNone
	l.SetHidden(true)
	b.Add(t)
}

func (a *Add) buildMore() {
	a.more = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp6, MinWidth: kit.Px(0)}}
	a.moreToggle = newMoreToggle(func() {
		a.moreOpen = !a.moreOpen
		a.refresh()
	})
	a.moreToggle.Style.AlignSelf = kit.AlignStart
	a.more.Add(a.moreToggle)

	// Files: File name, Connections; Speed limit, Queue; Add paused (two to a row)
	a.fileNameHnt = views.NewHint("A file name can only be set for a single URL.")
	a.fileNameErr = views.NewErrorText("")
	a.fileName = widgets.NewInput(a.filename, widgets.InputOptions{Name: "File name", Placeholder: "From the server or the URL"}, func(s string) {
		a.filename = s
		a.refresh()
	})
	nameField := views.NewField("File name", a.fileName, a.fileNameHnt, a.fileNameErr)
	a.conns = widgets.NewInput(a.connections, widgets.InputOptions{Name: "Connections", Description: "1–32", Number: true}, func(s string) {
		a.connections = s
		a.refresh()
	})
	a.connsErr = views.NewErrorText("")
	connsHint := views.NewHint(strconv.Itoa(validate.MinConnections) + "–" + strconv.Itoa(validate.MaxConnections))
	connsField := views.NewField("", views.NewLabelRow("Connections", connsHint), a.conns, a.connsErr)
	a.speed = widgets.NewInput(a.speedValue, widgets.InputOptions{Name: "Speed limit", Placeholder: "No limit"}, func(s string) {
		a.speedValue = s
		a.refresh()
	})
	a.speed.Style.Flex = kit.Flex1
	a.speed.Style.Width = kit.Length{}
	units := make([]widgets.SelectOption, 0, 2)
	for _, u := range format.RateUnits() {
		units = append(units, widgets.SelectOption{Value: string(u), Label: string(u)})
	}
	a.unit = widgets.NewSelect(units, string(a.speedUnit), widgets.SelectOptions{AriaLabel: "Speed limit unit"}, func(v string) {
		a.speedUnit = format.RateUnit(v)
		a.refresh()
	})
	unitBox := &kit.Element{Style: kit.Style{Flex: kit.FlexOf(0, 0, kit.Rem(6.75)), Display: kit.DisplayFlex}}
	unitBox.Add(a.unit)
	withUnit := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: tokens.Sp4, MinWidth: kit.Px(0)}}
	withUnit.Add(a.speed, unitBox)
	a.speedErr = views.NewErrorText("")
	speedField := views.NewField("Speed limit", withUnit, a.speedErr)
	a.queueFiles = a.newQueueField()
	a.pausedFiles = widgets.NewCheckbox("Add paused (start later)", a.startPaused, a.setPaused)
	a.pausedFiles.Style.AlignSelf = kit.AlignStart
	a.moreFiles = newGrid(func(w kit.DIP, s kit.Scale) []float32 {
		if a.container > 0 && a.container <= s.Rem(32) { // @container dialog (max-width: 32rem)
			return []float32{1}
		}
		return []float32{1, 1}
	}, 12, 16)
	a.moreFiles.start = true
	a.moreFiles.place = func(c *kit.Element, _ kit.DIP, _ kit.Scale) (area, bool) {
		if c == a.pausedFiles.Base() {
			return area{colSpan: -1}, false
		}
		return area{}, false
	}
	a.moreFiles.Add(nameField, connsField, speedField, a.queueFiles, a.pausedFiles)

	// Video or stream: File name (and Queue) in a row, Add paused
	a.streamName = widgets.NewInput(a.filename, widgets.InputOptions{Name: "File name", Description: "The extension (.mp4) is added for you."}, func(s string) {
		a.filename = s
		a.refresh()
	})
	a.streamNmErr = views.NewErrorText("")
	streamNameField := views.NewField("File name", a.streamName, views.NewHint("The extension (.mp4) is added for you."), a.streamNmErr)
	a.queueStream = a.newQueueField()
	a.streamRow = newGrid(func(kit.DIP, kit.Scale) []float32 {
		if a.offerQueues() {
			return []float32{1.5, 1}
		}
		return []float32{1}
	}, 16, 16)
	a.streamRow.start = true
	a.streamRow.Add(streamNameField, a.queueStream)
	a.pausedStrm = widgets.NewCheckbox("Add paused (start later)", a.startPaused, a.setPaused)
	a.pausedStrm.Style.AlignSelf = kit.AlignStart
	a.moreStream = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp7}}
	a.moreStream.Add(a.streamRow, a.pausedStrm)

	body := &moreBody{}
	body.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesAll(tokens.Sp7), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RXl)}
	body.Add(a.moreFiles, a.moreStream)
	a.more.Add(body)
}

func (a *Add) newQueueField() *queueField {
	opts := make([]widgets.SelectOption, 0, len(a.queues))
	for _, q := range a.queues {
		l := text.EscapeBidi(q.GetName())
		if !q.GetRunning() {
			l += " (stopped)"
		}
		opts = append(opts, widgets.SelectOption{Value: strconv.FormatInt(q.GetId(), 10), Label: l})
	}
	f := &queueField{}
	f.hint = views.NewHint("")
	f.sel = widgets.NewSelect(opts, strconv.FormatInt(a.queueID, 10), widgets.SelectOptions{Label: "Queue"}, func(v string) {
		id, _ := strconv.ParseInt(v, 10, 64)
		a.queueID = id
		a.refresh()
	})
	f.Field = views.NewField("", views.NewLabelRow("Queue", nil), f.sel, f.hint)
	return f
}

// moreBody is .more-body: --surface-2 with a --line border and 12 px corners.
type moreBody struct{ kit.Element }

// Paint is kit.Painter's.
func (b *moreBody) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&b.Element)
	r := views.SnapBox(p, 12)
	p.Canvas.FillRoundedRect(r, t.Line)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 12-w), t.Surface2)
}

func (a *Add) setPaused(v bool) {
	a.startPaused = v
	a.refresh()
}

// ---- keeping the widgets in step -------------------------------------------------------------

// refresh shows the dialog's state: every text, error, part and button.
func (a *Add) refresh() {
	stream := a.mode == modeStream
	errs := a.errors()
	if h := errs.hidden(); h != a.hiddenErr {
		a.hiddenErr = h
		if h {
			a.moreOpen = true // More options opens by itself when an error inside it appears
		}
	}
	urls := a.urlList()
	n := len(urls)
	info := a.Info()
	found := a.Found()
	site := a.site()

	// names
	if a.canStream {
		name := "Add downloads"
		if stream {
			name = "Add video or stream"
		}
		if c := a.Card(); c.Semantics.Name != name {
			c.Semantics.Name = name
			c.SemanticsChanged()
		}
		a.seg.SetValue(a.mode)
	}
	a.SetBusy(a.busy != idle && !a.stoppable())

	// Files
	a.filesField.SetHidden(stream)
	hint := "One URL per line (http or https). Ctrl+Enter adds."
	if n > 1 {
		hint = "One URL per line (http or https). " + strconv.Itoa(n) + " URLs. Ctrl+Enter adds."
	}
	hint = kit.KeyLabel(hint)
	a.urlsHint.SetText(hint)
	setError(a.urlsErr, errs.urls)
	a.urlsArea.SetInvalid(errs.urls != "")
	a.urlsArea.Semantics.Description = describe(hint, errs.urls)
	a.suggest.SetHidden(!(a.canStream && n == 1 && media.LooksLikeManifest(urls[0])))

	// Video or stream
	a.source.SetHidden(!stream)
	a.source.Style.RowGap = tokens.Sp7
	if found {
		a.source.Style.RowGap = tokens.Sp6
	}
	a.foundMark.SetHidden(info == nil)
	pad := kit.DIP(0)
	if info != nil {
		pad = 82
	}
	if a.stream.Style.Padding.End != kit.Px(pad) {
		a.stream.Style.Padding.End = kit.Px(pad)
		a.stream.Relayout()
	}
	a.streamHint.SetHidden(info != nil)
	setError(a.streamErr, errs.url)
	a.stream.SetInvalid(errs.url != "")
	desc := "A video page (YouTube and the other sites yt-dlp supports: one MP4 file in the quality you choose), or an HLS (.m3u8) or DASH (.mpd) manifest. DRM-protected and live videos cannot be downloaded."
	if info != nil {
		desc = "Found"
	}
	a.stream.Semantics.Description = describe(desc, errs.url)
	waiting := a.busy != idle && site
	a.waitBox.SetHidden(!waiting)
	a.showSpinner(waiting && !a.settingUp())
	a.showSetupBar(waiting && a.settingUp())
	if waiting {
		up := a.settingUp()
		a.setupPart.SetHidden(!up)
		a.gettingRow.SetHidden(up)
		if up {
			a.setupBar.Set(a.setupFraction(), widgets.BarDownloading)
			a.setupText.SetText(state.InstallText(a.tools, a.setupFraction(), false))
		}
		el := a.elapsedSeconds()
		a.elapsed.SetText(strconv.Itoa(el) + " s")
		a.elapsed.SetHidden(el <= 0)
	}
	a.syncTicker(waiting)

	// the card and the qualities
	if info != a.shownInfo {
		a.showInfo(info)
	}
	a.cardArea.SetHidden(!found)
	a.qualityArea.SetHidden(!found)
	a.audioSlot.SetHidden(!found)
	if a.Picker != nil {
		a.Picker.SetDisabled(a.busy != idle)
		if a.Video != nil {
			a.Video.SetPicture(a.chosenPicture())
		}
	}
	if a.Clip != nil {
		a.Clip.SetDisabled(a.busy != idle)
	}
	if a.Items != nil {
		a.Items.SetDisabled(a.busy != idle)
		if a.run == nil {
			// another item that could not be got: the select shows the item shown again
			a.Items.Restore()
		}
	}
	// the sizes and the length are the part's while one is chosen (the whole video's while the
	// part is off, the whole, or has errors)
	if a.Picker != nil {
		a.Picker.SetClip(a.clip())
	}
	if a.Video != nil {
		a.Video.SetPart(a.clip())
	}

	// the folder
	a.pair.Style.Axis = kit.AxisRow
	a.pair.Style.AlignItems = kit.AlignStart
	a.pair.Style.RowGap = tokens.Sp6
	if found { // .split > .pair: a column beside the qualities
		a.pair.Style.Axis = kit.AxisColumn
		a.pair.Style.AlignItems = kit.AlignStretch
		a.audioSlot.Style.Flex = kit.FlexNone
		a.dirField.Style.Flex = kit.FlexNone
	} else {
		a.audioSlot.Style.Flex = kit.FlexOf(1, 1, kit.Px(0))
		a.dirField.Style.Flex = kit.FlexOf(1, 1, kit.Px(0))
	}
	a.pair.Relayout()
	if a.free >= 0 {
		a.freeText.SetText(format.Bytes(float64(a.free)) + " free")
		a.freeText.SetHidden(false)
		a.freeText.Tooltip = ""
		fnt := views.FontHint
		if a.tight() {
			a.freeText.Tooltip = "Not enough free space for this video"
			fnt.Weight = 600
		}
		a.freeText.SetFont(fnt)
		a.freeText.Invalidate()
	} else {
		a.freeText.SetHidden(true)
	}
	setError(a.dirErr, errs.dir)
	a.folder.SetInvalid(errs.dir != "")
	a.folder.Semantics.Description = describe("Leave empty to use the default folder.", errs.dir)

	// More options
	a.moreToggle.set(a.moreOpen, a.MoreSummary())
	body := a.more.Children()[1]
	body.SetHidden(!a.moreOpen)
	a.moreFiles.SetHidden(stream)
	a.moreStream.SetHidden(!stream)
	a.fileName.SetDisabled(n > 1)
	ph := "From the server or the URL"
	if n > 1 {
		ph = "Taken from each URL"
	}
	a.fileName.SetPlaceholder(ph)
	a.fileNameHnt.SetHidden(n <= 1)
	setError(a.fileNameErr, errs.filename)
	a.fileName.SetInvalid(errs.filename != "")
	setError(a.connsErr, errs.connections)
	a.conns.SetInvalid(errs.connections != "")
	setError(a.speedErr, errs.speed)
	a.speed.SetInvalid(errs.speed != "")
	sph := "From the stream title or address"
	switch {
	case info != nil && info.GetTitle() != "":
		sph = text.EscapeBidi(info.GetTitle())
	case site:
		sph = "From the video title"
	}
	a.streamName.SetPlaceholder(sph)
	setError(a.streamNmErr, errs.filename)
	a.streamName.SetInvalid(errs.filename != "")
	for _, q := range []*queueField{a.queueFiles, a.queueStream} {
		q.SetHidden(!a.offerQueues())
		q.sel.SetValue(strconv.FormatInt(a.queueID, 10))
		h := "Downloads start in the order of their queue."
		if cq := a.chosenQueue(); cq != nil && !cq.GetRunning() {
			h = "This queue is stopped: the download waits until it starts."
		}
		q.hint.SetText(h)
		q.sel.Semantics.Description = h
	}
	a.streamRow.SetHidden(false)
	a.pausedFiles.SetChecked(a.startPaused)
	a.pausedStrm.SetChecked(a.startPaused)

	// failures
	a.showFailures()

	// the footer
	a.refreshFooter()
	a.fitWidth()
}

func (a *Add) scale() kit.Scale {
	if w := a.app.Window; w != nil {
		return w.Environment().Scale
	}
	return kit.Scale{PixelsPerDIP: 1, Text: 1}
}

func (a *Add) refreshFooter() {
	if a.mode == modeStream {
		if s := a.streamSum(); s != nil {
			a.summary.setStream(s)
		} else {
			a.summary.clear()
		}
	} else if n := len(a.urlList()); n > 0 {
		head := "1 link"
		if n > 1 {
			head = strconv.Itoa(n) + " links"
		}
		conns := text.TrimJS(a.connections)
		if conns == "" {
			conns = strconv.Itoa(int(a.maxConns()))
		}
		rest := " · " + conns + " connections"
		if q := a.queueName(); q != "" {
			rest += " · " + q
		}
		a.summary.setFiles(head, rest)
	} else {
		a.summary.clear()
	}
	if a.stoppable() {
		setDisabled(a.cancel, false)
	} else {
		setDisabled(a.cancel, a.busy != idle)
	}
	a.submit.SetText(a.SubmitLabel())
	ic := a.submitIcon()
	if iw := a.submit.IconWidget(); iw != nil {
		iw.SetHidden(ic == "")
		if ic != "" {
			n, _ := icons.ByName(ic)
			iw.SetName(n)
		}
	}
	setDisabled(a.submit, a.busy != idle)
}

func setError(t *views.Text, msg string) {
	t.SetText(msg)
	t.SetHidden(msg == "")
}

func describe(hint, err string) string {
	switch {
	case hint == "":
		return err
	case err == "":
		return hint
	}
	return hint + " " + err
}

// showInfo makes the card and the qualities of a listed address (none for nil).
func (a *Add) showInfo(info *downexv1.MediaInfo) {
	a.shownInfo = info
	a.cardArea.RemoveAll()
	a.qualityArea.RemoveAll()
	a.audioSlot.RemoveAll()
	a.Video, a.Picker, a.Clip, a.Items = nil, nil, nil, nil
	if info == nil {
		return
	}
	a.Video = NewVideoCard(info, CardOptions{URL: a.streamURL, NoCookies: true})
	a.cardArea.Add(a.Video)
	if media.ShowsItems(info) {
		a.Items = NewItemRow(info)
		a.Items.OnChoose = a.chooseItem
		a.Items.Style.Margin = kit.Edges{Bottom: tokens.Sp6}
		a.qualityArea.Add(a.Items)
	}
	a.Picker = NewPicker(info, a.pick)
	a.Picker.OnChange = func(p media.TrackPick) {
		a.pick = p
		a.refresh()
	}
	a.Picker.OnSubmit = a.onSubmit
	if media.AudioPage(info) {
		// An audio page (SITE-MEDIA-SPEC §3.13): its audio tracks, no part.
		a.qualityArea.Add(views.NewHint(media.AudioPageHint(info)))
		if w := a.Picker.Part(PartAudio); w != nil {
			a.qualityArea.Add(w)
		}
		return
	}
	if w := a.Picker.Part(PartVideo); w != nil {
		a.qualityArea.Add(w)
	}
	if w := a.Picker.Part(PartAudio); w != nil {
		a.audioSlot.Add(w)
	}
	// The part of the video, after the qualities, inside the stream area (not in More: it
	// changes what is downloaded; docs/MEDIA-SPEC.md §8, B1).
	a.Clip = NewClipSection(info, nil)
	a.Clip.OnChange = a.refresh
	slot := a.qualityArea // under the cards, in their column: the dialog keeps its fit
	if len(slot.Children()) == 0 || (a.Items != nil && len(slot.Children()) == 1) {
		slot = a.audioSlot
	}
	if len(slot.Children()) > 0 {
		a.Clip.Style.Margin = kit.Edges{Top: tokens.Sp6}
	}
	slot.Add(a.Clip)
}

// fitWidth keeps the dialog 38rem wide until a video is found, 56rem once it is (min(…,
// 100vw - 32px)): widgets.Dialog takes its width once, so the narrower one is the room its
// layer leaves it.
func (a *Add) fitWidth() {
	if !a.canStream {
		return
	}
	w := a.app.Window
	if w == nil {
		return
	}
	size := w.Size()
	s := w.Environment().Scale
	pad := kit.DIP(0)
	if !a.Found() {
		pad = max(0, (size.W-32-s.Rem(38))/2)
	}
	a.fitted = size
	if a.Dialog.Dialog.Style.Padding.Start != kit.Px(pad) {
		a.Dialog.Dialog.Style.Padding = kit.EdgesInline(kit.Px(pad))
		a.Dialog.Dialog.Relayout()
	}
}

// LaidOut is kit.LayoutFollower's: a resized window fits the dialog's width again.
func (a *Add) LaidOut() {}

// ---- the ticker, the free space --------------------------------------------------------------

func (a *Add) elapsedSeconds() int {
	return max(0, int(math.Floor(float64(a.now.Sub(a.startedAt))/float64(time.Second))))
}

// syncTicker runs the one-second clock of "Getting video info… 3 s" while a page is waited for.
func (a *Add) syncTicker(on bool) {
	if !on || a.gone {
		if a.ticker != nil {
			a.ticker.Stop()
			a.ticker = nil
		}
		return
	}
	if a.ticker != nil || a.app.Window == nil {
		return
	}
	var tick func()
	tick = func() {
		a.ticker = nil
		if a.gone {
			return
		}
		a.now = a.wallNow()
		a.ticker = a.app.Window.After(time.Second, tick)
		a.refresh()
	}
	a.ticker = a.app.Window.After(time.Second, tick)
}

func (a *Add) freeSpaceFn() func(string) (int64, error) {
	if a.o.NoFreeSpace {
		return nil
	}
	if a.o.FreeSpace != nil {
		return a.o.FreeSpace
	}
	if a.app.Platform != nil {
		return a.app.Platform.FreeSpace
	}
	return nil
}

// askFree asks for the free space of the folder's volume: at once the first time, 300 ms after
// typing pauses later; nothing for a relative folder or without a free-space call.
func (a *Add) askFree() {
	a.freeGen++
	gen := a.freeGen
	if a.freeTimer != nil {
		a.freeTimer.Stop()
		a.freeTimer = nil
	}
	ask := a.freeSpaceFn()
	target := a.folderPath()
	if ask == nil || !absoluteDir.MatchString(target) || a.app.Window == nil {
		if a.free != -1 {
			a.free = -1
			a.refresh()
		}
		return
	}
	do := func() {
		a.goFn(func() {
			n, err := ask(target)
			a.app.UI(func() {
				if gen != a.freeGen || a.gone {
					return
				}
				if err != nil || n < 0 {
					a.free = -1
				} else {
					a.free = n
				}
				a.refresh()
			})
		})
	}
	delay := time.Duration(0)
	if a.askedOnce {
		delay = 300 * time.Millisecond
	}
	a.askedOnce = true
	if delay == 0 {
		do()
		return
	}
	a.freeTimer = a.app.Window.After(delay, do)
}

// ---- calls -----------------------------------------------------------------------------------

func (a *Add) streamChanged() {
	// a changed address ends the wait for the old one
	if a.run != nil && a.run.url != text.TrimJS(a.streamURL) {
		a.stop()
	}
	a.refresh()
}

// stop stops waiting for a web page (Cancel): nothing more is asked, the dialog stays open.
func (a *Add) stop() {
	if a.run != nil {
		a.run.cancel()
	}
	a.run = nil
	a.busy = idle
	a.waiting = nil
	a.refresh()
}

// call runs a call of the "Video or stream" mode: a web page's is asked for again while
// downexd sets up yt-dlp or reads the page. done gets its result; after a failure (shown) or a
// stop it is not called.
func (a *Add) call(url string, site bool, fn func(ctx context.Context) (any, error), done func(v any)) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, url: url}
	a.run = r
	a.waiting = nil
	a.startedAt = a.wallNow()
	a.now = a.startedAt
	clock := a.clock()
	a.goFn(func() {
		var v any
		var err error
		if site {
			v, err = media.RetrySite(ctx, fn, media.RetryOptions{Clock: clock, OnWait: func(w media.SiteRetry) {
				a.app.UI(func() {
					if a.run == r && !a.gone {
						a.waiting = &w
						a.refresh()
					}
				})
			}})
		} else {
			v, err = fn(ctx)
		}
		stopped := ctx.Err() != nil
		a.app.UI(func() {
			if a.gone {
				return
			}
			if a.run == r {
				a.run = nil
				a.busy = idle
				a.waiting = nil
			}
			if err != nil {
				if !stopped {
					a.streamFailure = media.MediaErrorText(err, site)
					a.failureSettings = site && media.NeedsToolsSettings(err)
				}
				a.refresh()
				return
			}
			done(v)
		})
		cancel()
	})
}

func (a *Add) setMode(next string) {
	if next == a.mode {
		return
	}
	urls := a.urlList()
	if next == modeStream && text.TrimJS(a.streamURL) == "" && len(urls) == 1 {
		a.streamURL = urls[0]
		a.stream.SetText(a.streamURL)
	}
	if next == modeFiles {
		a.urlsArea.ResetHeight() // {#if mode === 'files'} makes the textarea anew
	}
	if next == modeFiles && text.TrimJS(a.urls) == "" && text.TrimJS(a.streamURL) != "" {
		a.urls = text.TrimJS(a.streamURL)
		a.urlsArea.SetText(a.urls)
	}
	a.mode = next
	a.attempted = false
	a.failures = nil
	a.streamFailure = ""
	a.failureSettings = false
	a.fileName.SetText(a.filename)
	a.streamName.SetText(a.filename)
	a.refresh()
	// the focus follows into the mode's field when it was in the other mode's
	if w := a.app.Window; w != nil {
		if f := w.Focused(); f == nil || f.Style.Hidden || !a.Card().Contains(f) {
			if next == modeStream {
				w.Focus(a.stream)
			} else {
				w.Focus(a.urlsArea)
			}
		}
	}
}

// focusInvalid opens More options for an error inside it and focuses the first wrong field.
func (a *Add) focusInvalid() {
	if a.errors().hidden() {
		a.moreOpen = true
	}
	a.refresh()
	w := a.app.Window
	if w == nil {
		return
	}
	stream := a.mode == modeStream
	e := a.errors()
	var order []struct {
		bad bool
		w   kit.Widget
	}
	if stream {
		order = append(order, struct {
			bad bool
			w   kit.Widget
		}{e.url != "", a.stream}, struct {
			bad bool
			w   kit.Widget
		}{e.dir != "", a.folder}, struct {
			bad bool
			w   kit.Widget
		}{e.filename != "", a.streamName})
	} else {
		order = append(order, struct {
			bad bool
			w   kit.Widget
		}{e.urls != "", a.urlsArea}, struct {
			bad bool
			w   kit.Widget
		}{e.dir != "", a.folder}, struct {
			bad bool
			w   kit.Widget
		}{e.filename != "", a.fileName}, struct {
			bad bool
			w   kit.Widget
		}{e.connections != "", a.conns}, struct {
			bad bool
			w   kit.Widget
		}{e.speed != "", a.speed})
	}
	for _, o := range order {
		if o.bad {
			w.Focus(o.w)
			return
		}
	}
}

func (a *Add) onSubmit() {
	if a.busy != idle || a.gone {
		return
	}
	if a.mode == modeStream {
		a.submitStream()
		return
	}
	a.attempted = true
	reqs, _, ok := validate.ValidateAddForm(a.addForm())
	if !ok {
		a.focusInvalid()
		return
	}
	a.busy = busyAdd
	a.failures = nil
	a.refresh()
	submit := a.o.Submit
	if submit == nil {
		submit = a.app.Main.Downloads.Add
	}
	a.goFn(func() {
		ctx, cancel := a.app.CallContext()
		outcomes := submit(ctx, reqs)
		cancel()
		a.app.UI(func() {
			if a.gone {
				return
			}
			a.busy = idle
			var failed []failure
			for _, o := range outcomes {
				if !o.OK() {
					failed = append(failed, failure{url: o.URL, err: o.Err.Message})
				}
			}
			if len(failed) == 0 {
				a.refresh()
				a.done()
				return
			}
			a.failures = failed
			var urls []string
			for _, f := range failed {
				urls = append(urls, f.url)
			}
			a.urls = strings.Join(urls, "\n")
			a.urlsArea.SetText(a.urls)
			a.refresh()
		})
	})
}

// submitStream first lists the qualities (the user then chooses), then adds the stream or video.
// A web page goes as origin site, without a manifest (SITE-MEDIA-SPEC §3.3).
func (a *Add) submitStream() {
	a.attempted = true
	checked, _, ok := validate.ValidateStreamForm(validate.StreamForm{URL: a.streamURL, Dir: a.dir, Filename: a.filename})
	if !ok {
		a.focusInvalid()
		return
	}
	a.streamFailure = ""
	a.failureSettings = false
	site := !media.LooksLikeManifest(checked.URL)
	target := &downexv1.ProbeMediaRequest{ManifestUrl: checked.URL}
	if site {
		target = &downexv1.ProbeMediaRequest{PageUrl: checked.URL, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE}
	}
	listed := a.Info()
	if listed == nil {
		probe := a.o.ProbeMedia
		if probe == nil {
			probe = a.app.Main.Downloads.ProbeMedia
		}
		a.busy = busyProbe
		a.call(checked.URL, site, func(ctx context.Context) (any, error) { return probe(ctx, target) }, func(v any) {
			found := v.(*downexv1.MediaInfo)
			a.probed = &probeResult{url: checked.URL, info: found}
			a.pick = media.DefaultPick(found)
			a.refresh()
			// the keyboard goes on with the choice: the chosen quality (or the audio track)
			if w := a.app.Window; w != nil && a.Picker != nil {
				if q := a.Picker.QualitySection(); q != nil && q.Checked() != nil {
					w.Focus(q.Checked())
				} else {
					for _, p := range a.Picker.audio {
						if p.Select != nil && p.mode == 1 {
							w.Focus(p.Select)
						}
					}
				}
			}
		})
		a.refresh()
		return
	}
	add := a.o.SubmitMedia
	if add == nil {
		add = a.app.Main.Downloads.AddMedia
	}
	var clip *downexv1.MediaClip
	if a.Clip != nil {
		var ok bool
		if clip, ok = a.Clip.Request(); !ok {
			// An error under the part blocks the add (B3); the keyboard goes to its field.
			a.Clip.ShowErrors()
			a.refresh()
			if w := a.app.Window; w != nil {
				if f := a.Clip.FirstInvalid(); f != nil {
					w.Focus(f)
				}
			}
			return
		}
	}
	req := &downexv1.AddMediaRequest{
		ManifestUrl: target.ManifestUrl, PageUrl: target.PageUrl, Origin: target.Origin,
		Dir: checked.Dir, Filename: checked.Filename, Source: downexv1.AddSource_ADD_SOURCE_UI,
		VideoTrack: a.pick.Video, AudioTrack: media.PickedAudio(listed, a.pick), StartPaused: a.startPaused,
		Clip: clip, EntryId: listed.GetEntryId(),
	}
	if a.offerQueues() && a.queueID != state.MainQueueID {
		req.QueueId = a.queueID
	}
	a.busy = busyAdd
	a.call(checked.URL, site, func(ctx context.Context) (any, error) { return add(ctx, req) }, func(any) {
		a.refresh()
		a.done()
	})
	a.refresh()
}

// chooseItem gets the qualities of another video of the page (docs/SITE-MEDIA-SPEC.md §3.12):
// the page is asked again with its id, the cards follow the answer, and Add sends that video.
func (a *Add) chooseItem(id string) {
	listed := a.Info()
	if a.busy != idle || a.gone || listed == nil || id == listed.GetEntryId() {
		if a.Items != nil {
			a.Items.Restore()
		}
		return
	}
	url := a.probed.url
	probe := a.o.ProbeMedia
	if probe == nil {
		probe = a.app.Main.Downloads.ProbeMedia
	}
	target := &downexv1.ProbeMediaRequest{PageUrl: url, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, EntryId: id}
	a.streamFailure = ""
	a.failureSettings = false
	a.busy = busyProbe
	a.call(url, true, func(ctx context.Context) (any, error) { return probe(ctx, target) }, func(v any) {
		found := v.(*downexv1.MediaInfo)
		a.probed = &probeResult{url: url, info: found}
		a.pick = media.DefaultPick(found)
		a.refresh()
		if w := a.app.Window; w != nil && a.Items != nil {
			w.Focus(a.Items.Select)
		}
	})
	a.refresh()
}

func (a *Add) browse() {
	pick := a.o.PickFolder
	if pick == nil && a.app.Platform != nil {
		pick = a.app.Platform.PickFolder
	}
	if pick == nil {
		return
	}
	initial := a.dir
	if initial == "" {
		initial = a.defaults.Dir
	}
	a.goFn(func() {
		ctx, cancel := a.app.CallContext()
		picked, err := pick(ctx, initial)
		cancel()
		if err != nil || picked == "" {
			return
		}
		a.app.UI(func() {
			if a.gone {
				return
			}
			a.dir = picked
			a.folder.SetText(picked)
			a.refresh()
			a.askFree()
		})
	})
}

func (a *Add) onCancel() {
	if a.stoppable() {
		a.stop()
		return
	}
	a.Close()
}

// done closes the dialog after a successful add (onclose).
func (a *Add) done() {
	a.finish()
	a.Remove()
	if a.o.OnClose != nil {
		a.o.OnClose()
	}
}

// closed is Escape's and the close button's, after the closing motion.
func (a *Add) closed() {
	a.finish()
	if a.o.OnClose != nil {
		a.o.OnClose()
	}
}

func (a *Add) finish() {
	if a.gone {
		return
	}
	a.gone = true
	if a.run != nil {
		a.run.cancel()
		a.run = nil
	}
	if a.ticker != nil {
		a.ticker.Stop()
		a.ticker = nil
	}
	if a.freeTimer != nil {
		a.freeTimer.Stop()
		a.freeTimer = nil
	}
	if a.stopTools != nil {
		a.stopTools()
		a.stopTools = nil
	}
}

// ShowModal is views.Modal's: the dialog, its free space asked for, the tools followed.
func (a *Add) ShowModal(w *kit.Window, done func()) kit.Widget {
	layer := a.Dialog.ShowModal(w, done)
	a.fitWidth()
	if a.o.Tools == nil && a.app.Main != nil && a.app.Main.Tools != nil {
		a.stopTools = a.app.Watch(&a.app.Main.Tools.Changes, func() {
			if a.gone {
				return
			}
			a.tools = a.app.Main.Tools.Status()
			a.refresh()
		})
	}
	a.askFree()
	a.watchSize()
	return layer
}

// DismissModal is views.Modal's.
func (a *Add) DismissModal() {
	a.finish()
	a.Dialog.DismissModal()
}

// watchSize fits the width again whenever the window's size changed (a layout pass follows).
func (a *Add) watchSize() {
	a.form.onLayout = func() {
		if a.gone || a.app.Window == nil || a.app.Window.Size() == a.fitted {
			return
		}
		a.app.Window.AfterLayout(a.fitWidth)
	}
}

// ---- failures --------------------------------------------------------------------------------

func (a *Add) showFailures() {
	stream := a.mode == modeStream
	a.failBox.RemoveAll()
	switch {
	case !stream && len(a.failures) > 0:
		head := strconv.Itoa(len(a.failures)) + " URLs could not be added:"
		if len(a.failures) == 1 {
			head = "This URL could not be added:"
		}
		a.failBox.Add(views.NewParagraph(head, views.FontCell, views.InkDanger))
		list := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Padding: kit.Edges{Start: tokens.Sp8}}}
		list.Semantics.Role = kit.RoleList
		for i, f := range a.failures {
			if i == 5 {
				break
			}
			li := newFlow(
				run{text: text.EscapeBidi(f.url), font: views.FontMono, ink: views.InkDanger, anywhere: true},
				run{text: " — " + text.EscapeBidi(f.err), font: views.FontCell, ink: views.InkDanger},
			)
			li.Semantics.Role = kit.RoleListItem
			list.Add(&bullet{flow: li})
		}
		a.failBox.Add(list)
		a.failBox.SetHidden(false)
	case stream && a.streamFailure != "":
		a.failBox.Add(views.NewParagraph(text.EscapeBidi(a.streamFailure), views.FontCell, views.InkDanger))
		if a.failureSettings && a.o.OnOpenSettings != nil {
			row := &kit.Element{}
			b := widgets.NewButton("Open settings", "", widgets.Secondary, widgets.SizeSm, a.o.OnOpenSettings)
			b.Style.AlignSelf = kit.AlignStart
			row.Add(b)
			a.failBox.Add(row)
		}
		a.failBox.SetHidden(false)
	default:
		a.failBox.SetHidden(true)
	}
}

// FailureText is the alert's text ("" when none shows).
func (a *Add) FailureText() string {
	if a.failBox.Style.Hidden {
		return ""
	}
	var b strings.Builder
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		switch w := e.Widget().(type) {
		case *views.Text:
			b.WriteString(w.Text())
			b.WriteString(" ")
			return
		case *flow:
			b.WriteString(w.Text)
			b.WriteString(" ")
			return
		case *widgets.Button:
			return
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(a.failBox.Base())
	return strings.TrimSpace(b.String())
}

// bullet is a list item with its disc (ul's default marker) before its flow.
type bullet struct {
	*flow
}

// PaintOver is kit.OverPainter's: the marker 14 px before the item's first line.
func (b *bullet) PaintOver(p *kit.PaintContext) {
	t := views.TokensOf(b.Base())
	r := kit.RectAt(p.Origin, p.Size())
	size := p.Scale.Rem(0.78125) * 1.4
	cy := r.Min.Y + size/2
	x := r.Min.X - 12
	if w := b.Window(); w != nil && w.Direction() == kit.DirRTL {
		x = r.Max.X + 12
	}
	p.Canvas.FillEllipse(kit.R(x-2.5, cy-2.5, x+2.5, cy+2.5), t.Danger)
}

// ---- small widgets ---------------------------------------------------------------------------

// moreToggle is the More options button (.more-toggle): its chevron (turned while open, mirrored
// in a right-to-left layout), "More options" and, while closed, the summary of what it holds,
// cut with an ellipsis (the whole as its tooltip).
type moreToggle struct {
	kit.Pressable
	chev    *widgets.Icon
	label   *views.Text
	summary *views.Text
	open    bool
}

func newMoreToggle(onPress func()) *moreToggle {
	m := &moreToggle{}
	m.OnPress = onPress
	m.Focus = kit.FocusTab
	m.Cursor = kit.CursorPointer
	m.Semantics.Role = kit.RoleButton
	m.Semantics.Name = "More options"
	m.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MaxWidth: kit.Pct(100), MinWidth: kit.Px(0),
		Padding: kit.Edges{Top: kit.Px(4), End: kit.Px(8), Bottom: kit.Px(4), Start: kit.Px(2)}, Radius: kit.CornersAll(tokens.RSm),
	}
	m.chev = icon("chevron-right", 14, muted)
	lf := views.FontBody
	lf.Weight = 600
	m.label = views.NewText("More options", lf, views.InkInk2, kit.TextOptions{})
	m.label.Style.Flex = kit.FlexNone
	m.summary = views.NewLine("", views.FontHint, views.InkFaint)
	m.summary.Style.Margin = kit.Edges{Start: kit.Px(4)}
	m.summary.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	m.Add(&chevron{Icon: m.chev, m: m}, m.label, m.summary)
	return m
}

func (m *moreToggle) set(open bool, summary string) {
	if open != m.open {
		m.open = open
		m.Invalidate()
		m.SemanticsChanged()
	}
	m.summary.SetText(summary)
	m.summary.Tooltip = summary
	m.summary.SetHidden(open)
	m.Semantics.Description = ""
	if !open {
		m.Semantics.Description = summary
	}
}

// Open reports aria-expanded; Summary the summary's text.
func (m *moreToggle) Open() bool      { return m.open }
func (m *moreToggle) Summary() string { return m.summary.Text() }

// FillSemantics is kit.SemanticFiller's.
func (m *moreToggle) FillSemantics(n *kit.SemanticNode) {
	m.Pressable.FillSemantics(n)
	n.Expand = kit.ExpandClosed
	if m.open {
		n.Expand = kit.ExpandOpen
	}
}

// Paint is kit.Painter's: --hover under the pointer.
func (m *moreToggle) Paint(p *kit.PaintContext) {
	if m.Hovered() {
		p.Canvas.FillRoundedRect(views.SnapBox(p, 6), views.TokensOf(&m.Element).Hover)
	}
}

// chevron is the toggle's chevron, turned a quarter while open.
type chevron struct {
	*widgets.Icon
	m *moreToggle
}

// Paint is kit.Painter's: the icon turned 90° while open, mirrored in a right-to-left layout.
func (c *chevron) Paint(p *kit.PaintContext) {
	r := kit.RectAt(p.Origin, p.Size())
	center := r.Center()
	switch {
	case c.m.open:
		p.Canvas.PushTransform(kit.Translation(-center.X, -center.Y).Then(kit.Rotation(90)).Then(kit.Translation(center.X, center.Y)))
	case c.Window() != nil && c.Window().Direction() == kit.DirRTL:
		p.Canvas.PushTransform(kit.Translation(-center.X, -center.Y).Then(kit.Scaling(-1, 1)).Then(kit.Translation(center.X, center.Y)))
	default:
		c.Icon.Paint(p)
		return
	}
	c.Icon.Paint(p)
	p.Canvas.Pop()
}

// summaryLine is the footer's summary (.sum): one line of 12.5 px --muted text with its <b>
// parts 600 --ink, cut with an ellipsis.
type summaryLine struct {
	kit.Element
	parts [4]*views.Text
}

func newSummaryLine() *summaryLine {
	s := &summaryLine{}
	s.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline}
	b := views.FontCell
	b.Weight = 600
	s.parts[0] = views.NewLine("", b, views.InkInk)
	s.parts[1] = views.NewLine("", views.FontCell, views.InkMuted)
	s.parts[2] = views.NewLine("", b, views.InkInk)
	s.parts[3] = views.NewLine("", views.FontCell, views.InkMuted)
	for _, p := range s.parts {
		p.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		p.Semantics.Role = kit.RoleNone
		s.Add(&spacedLine{Text: p})
	}
	s.Semantics.Role = kit.RoleText
	return s
}

// spacedLine is a piece of the summary's inline text: its trailing spaces take their room
// before the next piece (" · " then the bold size), as white space inside a line does in a
// page; a label alone measures its text without them (kit's TrailingSpace).
type spacedLine struct {
	*views.Text
}

// ContentWidths is kit.Layouter's: the label's, and its trailing spaces.
func (l *spacedLine) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	lo, hi = l.Text.ContentWidths(ctx)
	if txt := l.Text.Text(); strings.TrimRight(txt, " ") == "" {
		return lo, hi // nothing but spaces: no room of its own (as before a line's end)
	}
	extra := l.Text.TrailingSpace(ctx)
	return lo + extra, hi + extra
}

func (s *summaryLine) setTexts(t0, t1, t2, t3 string) {
	for i, v := range [4]string{t0, t1, t2, t3} {
		s.parts[i].SetText(v)
		s.parts[i].SetHidden(v == "")
	}
	s.Semantics.Name = s.Text()
	s.SemanticsChanged()
}

func (s *summaryLine) setFiles(head, rest string) { s.setTexts(head, rest, "", "") }

func (s *summaryLine) setStream(m *streamSummary) {
	rest := ""
	if m.sound != "" {
		rest = " " + m.sound
	}
	size, sizeLead := "", ""
	if m.size != "" {
		sizeLead = " · " + m.prefix
		size = m.size
	}
	part := ""
	if m.part != "" {
		part = " · " + m.part
	}
	s.parts[1].SetText(rest + sizeLead)
	s.setTexts(m.head, rest+sizeLead, size, part)
}

func (s *summaryLine) clear() { s.setTexts("", "", "", "") }

// Text is the summary as it reads, white space collapsed.
func (s *summaryLine) Text() string {
	var b strings.Builder
	for _, p := range s.parts {
		if !p.Style.Hidden {
			b.WriteString(p.Text())
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
