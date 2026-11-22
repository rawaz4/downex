package progress

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
)

// The units of lib/testing.
const (
	KiB = formattest.KiB
	MiB = formattest.MiB
	KB  = formattest.KB
	MB  = formattest.MB
)

// refused is lib/testing's REFUSED: downexd is not running.
const refused = "unavailable: connection refused"

// fakeAPI is ProgressWindow.test.ts's fakeApi: downloads, settings, the calls made with their
// last arguments, a down switch, and replaceable methods. It embeds statetest's daemon for the
// calls a progress window never makes.
type fakeAPI struct {
	*statetest.Daemon
	events state.Hub

	mu       sync.Mutex
	items    map[int64]*downexv1.Download
	settings *downexv1.Settings
	down     bool
	calls    []string
	args     map[string][]any

	// replacements (nil: the fake's own)
	getSettings    func() (*downexv1.Settings, error)
	setOptions     func(req *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error)
	updateSettings func(s *downexv1.Settings) error
	openWith       func() error
	copyText       func(text string) error

	// the window's host
	title  string
	closed int
}

func newFakeAPI(items []*downexv1.Download, settings *downexv1.Settings) *fakeAPI {
	f := &fakeAPI{Daemon: statetest.NewDaemon(), items: map[int64]*downexv1.Download{}, settings: proto.CloneOf(settings), args: map[string][]any{}}
	for _, d := range items {
		f.items[d.GetId()] = d
	}
	return f
}

func (f *fakeAPI) record(name string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.args[name] = args
	if f.down && name != "openFile" && name != "openWith" && name != "openFolder" && name != "copyText" {
		return connect.NewError(connect.CodeUnavailable, errors.New("connection refused"))
	}
	return nil
}

func (f *fakeAPI) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }

func (f *fakeAPI) Calls() []string { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.calls) }

func (f *fakeAPI) count(name string) int {
	n := 0
	for _, c := range f.Calls() {
		if c == name {
			n++
		}
	}
	return n
}

func (f *fakeAPI) lastArgs(name string) []any { f.mu.Lock(); defer f.mu.Unlock(); return f.args[name] }

func (f *fakeAPI) item(id int64) *downexv1.Download {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[id]
}

func (f *fakeAPI) setItem(d *downexv1.Download) { f.mu.Lock(); f.items[d.GetId()] = d; f.mu.Unlock() }

func (f *fakeAPI) setSettings(s *downexv1.Settings) {
	f.mu.Lock()
	f.settings = proto.CloneOf(s)
	f.mu.Unlock()
}

func (f *fakeAPI) emit(events ...*downexv1.Event) { f.events.Publish(events) }

func (f *fakeAPI) edit(id int64, edit func(d *downexv1.Download)) (*downexv1.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.items[id]
	if cur == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", id))
	}
	next := proto.CloneOf(cur)
	edit(next)
	next.UpdatedAt = timestamppb.New(cur.GetUpdatedAt().AsTime().Add(time.Millisecond))
	f.items[id] = next
	return next, nil
}

func (f *fakeAPI) Get(_ context.Context, id int64) (*downexv1.Download, error) {
	if err := f.record("get", id); err != nil {
		return nil, err
	}
	if d := f.item(id); d != nil {
		return d, nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", id))
}

func (f *fakeAPI) Pause(_ context.Context, req *downexv1.PauseRequest) (*downexv1.PauseResponse, error) {
	if err := f.record("pause", req.GetId()); err != nil {
		return nil, err
	}
	d, err := f.edit(req.GetId(), func(d *downexv1.Download) { d.State, d.SpeedBps = state.StatePaused, 0 })
	if err != nil {
		return nil, err
	}
	return &downexv1.PauseResponse{Download: d}, nil
}

// resumeOpts is the resume call's options as the test reads them ({} or {restart: true}).
type resumeOpts struct{ Restart bool }

func (f *fakeAPI) Resume(_ context.Context, req *downexv1.ResumeRequest) (*downexv1.ResumeResponse, error) {
	if err := f.record("resume", req.GetId(), resumeOpts{Restart: req.GetRestart()}); err != nil {
		return nil, err
	}
	d, err := f.edit(req.GetId(), func(d *downexv1.Download) { d.State = state.StateQueued })
	if err != nil {
		return nil, err
	}
	return &downexv1.ResumeResponse{Download: d}, nil
}

// optionsArg is a SetDownloadOptions call's options as the tests write them.
type optionsArg struct {
	SpeedLimit       *int64
	MaxConnections   *int32
	CompletionDialog *bool
}

func (o optionsArg) String() string {
	var parts []string
	if o.SpeedLimit != nil {
		parts = append(parts, fmt.Sprintf("speedLimit: %d", *o.SpeedLimit))
	}
	if o.MaxConnections != nil {
		parts = append(parts, fmt.Sprintf("maxConnections: %d", *o.MaxConnections))
	}
	if o.CompletionDialog != nil {
		parts = append(parts, fmt.Sprintf("completionDialog: %v", *o.CompletionDialog))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func (f *fakeAPI) SetDownloadOptions(_ context.Context, req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error) {
	if err := f.record("setDownloadOptions", req.GetId(), optionsArg{req.SpeedLimit, req.MaxConnections, req.CompletionDialog}.String()); err != nil {
		return nil, err
	}
	if f.setOptions != nil {
		d, err := f.setOptions(req)
		if err != nil {
			return nil, err
		}
		return &downexv1.SetDownloadOptionsResponse{Download: d}, nil
	}
	return f.applyOptions(req)
}

func (f *fakeAPI) applyOptions(req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error) {
	d, err := f.edit(req.GetId(), func(d *downexv1.Download) {
		if req.SpeedLimit != nil {
			d.SpeedLimit = req.GetSpeedLimit()
		}
		if req.MaxConnections != nil {
			d.MaxConnections = req.GetMaxConnections()
		}
		if req.CompletionDialog != nil {
			d.CompletionDialog = req.GetCompletionDialog()
		}
	})
	if err != nil {
		return nil, err
	}
	return &downexv1.SetDownloadOptionsResponse{Download: d}, nil
}

func (f *fakeAPI) GetSettings(context.Context) (*downexv1.GetSettingsResponse, error) {
	if err := f.record("getSettings"); err != nil {
		return nil, err
	}
	if f.getSettings != nil {
		s, err := f.getSettings()
		if err != nil {
			return nil, err
		}
		return &downexv1.GetSettingsResponse{Settings: s}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &downexv1.GetSettingsResponse{Settings: proto.CloneOf(f.settings)}, nil
}

func (f *fakeAPI) UpdateSettings(_ context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	if err := f.record("updateSettings", proto.CloneOf(req.GetSettings())); err != nil {
		return nil, err
	}
	if f.updateSettings != nil {
		if err := f.updateSettings(req.GetSettings()); err != nil {
			return nil, err
		}
	}
	f.setSettings(req.GetSettings())
	return &downexv1.UpdateSettingsResponse{Settings: req.GetSettings()}, nil
}

func (f *fakeAPI) Status(context.Context) (*downexv1.StatusResponse, error) {
	if err := f.record("status"); err != nil {
		return nil, err
	}
	return &downexv1.StatusResponse{Version: "1.0.0-test"}, nil
}

// ---- the platform (api.openFile, openWith, openFolder, copyText) ----------------------------

type fakePlatform struct {
	views.Platform // the calls a progress window never makes (nil: they would panic)
	f              *fakeAPI
}

func (p *fakePlatform) OpenFile(_ context.Context, id int64) error {
	return p.f.record("openFile", id)
}

func (p *fakePlatform) OpenWith(_ context.Context, id int64) error {
	_ = p.f.record("openWith", id)
	if p.f.openWith != nil {
		return p.f.openWith()
	}
	return nil
}

func (p *fakePlatform) OpenFolder(_ context.Context, id int64) error {
	return p.f.record("openFolder", id)
}

func (p *fakePlatform) CopyText(_ context.Context, text string) error {
	_ = p.f.record("copyText", text)
	if p.f.copyText != nil {
		return p.f.copyText(text)
	}
	return nil
}

// ---- the rig -------------------------------------------------------------------------------

// testHost is hostcaptest's host whose window resizes as it is asked (after the call, as a
// backend does) and that counts closes; Res, when set, is the real text engine.
type testHost struct {
	*hostcaptest.Host
	res      kit.Resources
	r        *rig
	noResize bool // the page keeps its height (a host that resizes it only later)
}

func (h *testHost) Resources() kit.Resources {
	if h.res != nil {
		return h.res
	}
	return h.Host.Resources()
}

func (h *testHost) Resize(s kit.Size) {
	h.Host.Resize(s)
	if h.noResize {
		return
	}
	_ = h.Post(func() {
		h.SizeV = s
		h.r.pw.Root.Event(kit.SizeEvent{Size: s})
	})
}

func (h *testHost) Close() { h.r.api.closed++ }

func (h *testHost) SetTitle(t string) {
	h.Host.SetTitle(t)
	h.r.api.title = t
}

// rig is a progress window driven headless (kittest's loop, hostcaptest's host), over a fake
// api, with the store's clock (wall time) and the window's (timers, motion) moved together.
type rig struct {
	t       testing.TB
	api     *fakeAPI
	pw      *Window
	host    *testHost
	loop    *kittest.Loop
	clock   *kittest.Clock
	wall    *statetest.Clock
	storage *statetest.Memory
	prompts []OpenQuestion
	modals  []*fakeModal
}

// rigOptions are a rig's choices.
type rigOptions struct {
	// size is the window's client size (520 × 260, a 300 px window with a 40 px frame).
	size kit.Size
	// frame is the window's frame (0 × 39: the jsdom tests' 39 px; the reference host's is 40).
	frame       *kit.Size
	statusEvery time.Duration // 60 s
	applyDelay  time.Duration // 50 ms
	noOpenWith  bool
	storage     map[string]string
	noStart     bool
	scale       float32
	font        int
	theme       string
	real        kit.Resources
	motion      bool
	setup       func(*fakeAPI)
	noResize    bool
	started     bool // the window opens as its download starts (Options.Started, "--started")
}

// settings is ProgressWindow.test.ts's SETTINGS.
func settings() *downexv1.Settings {
	s := state.DefaultSettings()
	s.DefaultDir = "/home/you/Downloads"
	s.DefaultMaxConnections = 8
	return s
}

func newRig(t testing.TB, d *downexv1.Download, s *downexv1.Settings, o rigOptions) *rig {
	t.Helper()
	var items []*downexv1.Download
	id := int64(7)
	if d != nil {
		items = append(items, d)
		id = d.GetId()
	}
	if s == nil {
		s = settings()
	}
	api := newFakeAPI(items, s)
	if o.setup != nil {
		o.setup(api)
	}
	return newRigWith(t, api, id, o)
}

func newRigWith(t testing.TB, api *fakeAPI, id int64, o rigOptions) *rig {
	t.Helper()
	if o.size == (kit.Size{}) {
		o.size = kit.Size{W: 520, H: 261}
	}
	frame := kit.Size{H: 39}
	if o.frame != nil {
		frame = *o.frame
	}
	if o.statusEvery == 0 {
		o.statusEvery = 60 * time.Second
	}
	if o.applyDelay == 0 {
		o.applyDelay = 50 * time.Millisecond
	}
	if o.scale == 0 {
		o.scale = 1
	}
	if o.font == 0 {
		o.font = 16
	}
	r := &rig{t: t, api: api, clock: kittest.NewClock(), wall: statetest.NewClock(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC).UnixMilli())}
	r.storage = statetest.NewMemory(o.storage)
	choice := tokens.ChoiceLight
	if o.theme == "dark" {
		choice = tokens.ChoiceDark
	}
	systemLight := false
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}
	pw, err := New(Options{
		ID: id, Started: o.started, Daemon: api, Events: &api.events, Platform: &fakePlatform{f: api},
		Caps: host.Capabilities{OpenWith: !o.noOpenWith}, Storage: r.storage, Clock: r.wall, Call: call, Go: statetest.Sync,
		FixedTheme: &choice, SystemDark: &systemLight, StatusInterval: o.statusEvery, ApplyDelay: o.applyDelay,
		Frame: func() kit.Size { return frame },
		OpenPrompt: func(app *views.App, q OpenQuestion) views.Modal {
			r.prompts = append(r.prompts, q)
			m := &fakeModal{q: q}
			r.modals = append(r.modals, m)
			return m
		},
		Now: r.clock.Now, Scheduler: r.clock, NoMotion: !o.motion,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.pw = pw
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: o.scale, Text: float32(o.font) / 16}, HoverTime: 400 * time.Millisecond, ReducedMotion: !o.motion}
	r.host = &testHost{Host: hostcaptest.NewHost(o.size, env, r.clock), res: o.real, r: r, noResize: o.noResize}
	r.loop = &kittest.Loop{Host: r.host.Host.Host, Root: pw.Root, Canvas: &kittest.Canvas{}, Clock: r.clock, Refresh: time.Second / 60}
	pw.Root.Attach(r.host)
	if !o.noStart {
		pw.Start()
	}
	r.settle()
	t.Cleanup(r.close)
	return r
}

func (r *rig) close() {
	r.pw.Dispose()
	r.pw.Kit.Detach()
}

// frameNow runs what was posted and a frame at the clock's time, without moving the clock.
func (r *rig) frameNow() {
	l := r.loop
	h := l.Host
	h.RunPosted()
	if h.FrameWanted || !h.Invalid.Empty() {
		h.FrameWanted = false
		l.Frames++
		l.Root.Frame(l.Clock.Now())
		if !h.Invalid.Empty() {
			dirty := h.Invalid
			h.Invalid = kit.Rect{}
			l.Paints++
			l.Painted = dirty
			l.Root.Paint(l.Canvas, dirty)
		}
	}
	h.RunPosted()
}

// settle runs frames, the clock standing still, until nothing is asked for.
func (r *rig) settle() {
	for range 50 {
		r.frameNow()
		for _, p := range r.host.Open() {
			p.Loop.Host.RunPosted()
			p.Loop.Settle(5)
		}
		if r.loop.Idle() {
			return
		}
	}
}

// advance moves both clocks by d (a fake timer's vi.advanceTimersByTime), and settles.
func (r *rig) advance(d time.Duration) {
	step := 10 * time.Millisecond
	for left := d; left > 0; left -= step {
		s := min(step, left)
		r.wall.Advance(s)
		r.clock.Advance(s)
		r.settle()
	}
}

// update is the test's update(api, id, patch): the download changed, its updatedAt one second
// on, as an event.
func (r *rig) update(id int64, patch func(d *downexv1.Download)) *downexv1.Download {
	cur := proto.CloneOf(r.api.item(id))
	patch(cur)
	cur.UpdatedAt = timestamppb.New(cur.GetUpdatedAt().AsTime().Add(time.Second))
	r.api.setItem(cur)
	r.api.emit(statetest.Updated(cur))
	r.settle()
	return cur
}

// ---- finding things -----------------------------------------------------------------------

func (r *rig) s() *Screen { return r.pw.Screen }

// visible reports that e and its ancestors are shown and in the window.
func visible(e *kit.Element) bool {
	if e == nil || e.Window() == nil {
		return false
	}
	for p := e; p != nil; p = p.Parent() {
		if p.Style.Hidden {
			return false
		}
	}
	return true
}

// row is the value of a status row ("Downloaded", …): the dd's text, spaces collapsed; "" with
// none shown.
func (r *rig) row(term string) (string, bool) {
	var f *facts
	if r.s().complete {
		f = r.s().done.facts
	} else {
		f = r.s().statusV.facts
	}
	if !visible(&f.Element) {
		return "", false
	}
	for _, it := range f.items {
		if it.show && it.dt.Text() == term && visible(it.dt.Base()) {
			return strings.Join(strings.Fields(textOf(it.dd.Base())), " "), true
		}
	}
	return "", false
}

func (r *rig) mustRow(term string) string {
	r.t.Helper()
	v, ok := r.row(term)
	if !ok {
		r.t.Fatalf("no row %q", term)
	}
	return v
}

// textOf is the text shown inside e (its labels, in order, hidden ones left out).
func textOf(e *kit.Element) string {
	if e.Style.Hidden {
		return ""
	}
	var b strings.Builder
	if l, ok := e.Widget().(interface{ Text() string }); ok {
		if _, isField := e.Widget().(*field); !isField {
			b.WriteString(l.Text())
		}
	}
	for _, c := range e.Children() {
		b.WriteString(textOf(c))
	}
	return b.String()
}

// stateLine is the header's state line.
func (r *rig) stateLine() string { return r.s().head.status.Text() }

// figure is the header's big percentage ("" when it shows none).
func (r *rig) figure() string {
	if r.s().head.figure.Style.Hidden {
		return ""
	}
	return r.s().head.figure.Text()
}

// click presses a widget as the pointer does.
func (r *rig) click(w kit.Widget) {
	r.t.Helper()
	e := w.Base()
	if !visible(e) {
		r.t.Fatalf("click on a hidden %T", w)
	}
	p := e.WindowBounds().Center()
	r.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	r.settle()
}

// key presses k with mods on what has the focus.
func (r *rig) key(k kit.Key, mods kit.Modifiers) bool {
	used := r.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: k, Mods: mods})
	r.loop.Send(kit.KeyEvent{Action: kit.KeyUp, Key: k, Mods: mods})
	r.settle()
	return used
}

// typeIn replaces a field's text as the user typing does (the input event).
func (r *rig) typeIn(f *field, s string) {
	r.t.Helper()
	nf, ok := f.Native().(*hostcaptest.Field)
	if !ok {
		r.t.Fatalf("the field has no native field (shown: %v)", visible(&f.Element))
	}
	nf.Replace(s)
	r.loop.Host.RunPosted()
}

// focus gives w the focus; blur takes it away (focus to the window).
func (r *rig) focus(w kit.Widget) {
	r.pw.Kit.Focus(w)
	r.settle()
}

func (r *rig) blur() {
	r.pw.Kit.Blur()
	r.settle()
}

// tab is the tab of a value.
func (r *rig) tab(value string) kit.Widget { return r.s().tabs.TabWidget(value) }

// sizes are the outer window sizes asked for, as {width, height}.
func (r *rig) sizes() []kit.Size { return slices.Clone(r.s().fit.sizes) }

func (r *rig) lastSize() kit.Size {
	r.t.Helper()
	s := r.sizes()
	if len(s) == 0 {
		r.t.Fatal("no size asked for")
	}
	return s[len(s)-1]
}

// fakeModal is the open prompt (the dialogs lane's OpenDialog) as a test drives it.
type fakeModal struct {
	q      OpenQuestion
	done   func()
	layer  *kit.Element
	closed bool
}

func (m *fakeModal) ModalKind() views.ModalKind { return views.ModalOpen }

func (m *fakeModal) ShowModal(w *kit.Window, done func()) kit.Widget {
	m.done = done
	m.layer = &kit.Element{}
	w.PushLayer(m.layer)
	return m.layer
}

func (m *fakeModal) DismissModal() { m.finish() }

func (m *fakeModal) finish() {
	if m.closed {
		return
	}
	m.closed = true
	if w := m.layer.Window(); w != nil {
		w.RemoveLayer(m.layer)
	}
	if m.done != nil {
		m.done()
	}
	m.q.Closed()
}

// confirm is the prompt's action button: confirm first, then close (OpenDialog's onsubmit).
func (m *fakeModal) confirm() {
	m.q.Confirm()
	m.finish()
}

var _ = widgets.FontBody
