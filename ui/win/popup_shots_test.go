//go:build windows

package win

import (
	"fmt"
	stdimage "image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
)

// The widgets' offscreen renders (plan steps 10 and 13): every widget in both themes at 100%
// and 125%, drawn by this backend (DirectWrite, Direct2D, ClearType) into a WIC bitmap, and the
// ones the 0.9.2 references show, cropped side by side with them. Set DOWNEX_WIDGET_SHOTS to the
// folder to write native-s1-widget-*.png to, and DOWNEX_REF_092 to the reference folder.

// shotHost is the kit.Host of an offscreen render: the scale and theme asked for, this
// backend's resources, and nothing that needs a window.
type shotHost struct {
	env   kit.Environment
	size  kit.Size
	res   *Resources
	posts []func()
}

func (h *shotHost) Environment() kit.Environment      { return h.env }
func (h *shotHost) Size() kit.Size                    { return h.size }
func (h *shotHost) WindowState() kit.WindowState      { return kit.WindowState{} }
func (h *shotHost) Resources() kit.Resources          { return h.res }
func (h *shotHost) Invalidate(kit.Rect)               {}
func (h *shotHost) RequestFrame()                     {}
func (h *shotHost) SetCursor(kit.Cursor)              {}
func (h *shotHost) CapturePointer()                   {}
func (h *shotHost) ReleasePointer()                   {}
func (h *shotHost) SetTextInput(kit.TextInput)        {}
func (h *shotHost) ClipboardText() (string, error)    { return "", nil }
func (h *shotHost) SetClipboardText(string) error     { return nil }
func (h *shotHost) SetTitle(string)                   {}
func (h *shotHost) SetMinSize(kit.Size)               {}
func (h *shotHost) Resize(kit.Size)                   {}
func (h *shotHost) SetFrameColors(kit.FrameColors)    {}
func (h *shotHost) Command(kit.WindowCommand)         {}
func (h *shotHost) Close()                            {}
func (h *shotHost) NotifySemantics(kit.SemanticEvent) {}
func (h *shotHost) Post(f func()) error {
	h.posts = append(h.posts, f)
	return nil
}

// scene builds a widget tree; prep puts its widgets in the states to show, once laid out.
type scene struct {
	name   string
	size   kit.Size // DIPs
	ground func(t *tokens.Theme) kit.Color
	build  func() (kit.Widget, func(w *kit.Window))
}

var shotTime = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// render draws a scene for a theme at ppd.
func render(t *testing.T, res *Resources, sc scene, dark bool, ppd float32) *stdimage.RGBA {
	t.Helper()
	choice := tokens.ChoiceLight
	if dark {
		choice = tokens.ChoiceDark
	}
	theme := &widgets.Choice{Choice: choice}
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: ppd, Text: 1}, Dark: dark, HoverTime: 400 * time.Millisecond}
	content, prep := sc.build()
	scope := widgets.NewScope(theme, content)
	now := shotTime
	w := kit.NewWindow(scope, kit.WindowOptions{
		Palette: func(e kit.Environment) kit.Palette {
			p := theme.Tokens(e).Palette()
			p.Ground = sc.ground(theme.Tokens(e))
			return p
		},
		Now: func() time.Time { return now },
	})
	h := &shotHost{env: env, size: sc.size, res: res}
	w.Attach(h)
	run := func() {
		for len(h.posts) > 0 {
			ps := h.posts
			h.posts = nil
			for _, f := range ps {
				f()
			}
		}
	}
	step := func() {
		run()
		w.Frame(now)
		now = now.Add(2 * time.Second)
		w.Frame(now)
		run()
	}
	step()
	if prep != nil {
		prep(w)
	}
	step()
	step()
	px := func(v kit.DIP) int { return int(math.Ceil(float64(v * ppd))) }
	o := newOffscreen(t, res, px(sc.size.W), px(sc.size.H), ppd)
	full := kit.RectAt(kit.Point{}, sc.size)
	c, err := o.BeginFrame(full)
	if err != nil {
		t.Fatal(err)
	}
	w.Paint(c, full)
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	img, err := o.RGBA()
	if err != nil {
		t.Fatal(err)
	}
	w.Detach()
	return img
}

// grid lays images out in rows on a grey ground, gap pixels apart.
func grid(rows [][]stdimage.Image, gap int) *stdimage.RGBA {
	w, h := gap, gap
	for _, row := range rows {
		rw, rh := gap, 0
		for _, im := range row {
			b := im.Bounds()
			rw += b.Dx() + gap
			rh = max(rh, b.Dy())
		}
		w = max(w, rw)
		h += rh + gap
	}
	out := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), &stdimage.Uniform{color.RGBA{128, 128, 128, 255}}, stdimage.Point{}, draw.Src)
	y := gap
	for _, row := range rows {
		x, rh := gap, 0
		for _, im := range row {
			b := im.Bounds()
			draw.Draw(out, stdimage.Rect(x, y, x+b.Dx(), y+b.Dy()), im, b.Min, draw.Src)
			x += b.Dx() + gap
			rh = max(rh, b.Dy())
		}
		y += rh + gap
	}
	return out
}

// sheet renders a scene in both themes at 100% and 125%: light and dark side by side, 100%
// above 125%.
func sheet(t *testing.T, res *Resources, sc scene) *stdimage.RGBA {
	return grid([][]stdimage.Image{
		{render(t, res, sc, false, 1), render(t, res, sc, true, 1)},
		{render(t, res, sc, false, 1.25), render(t, res, sc, true, 1.25)},
	}, 8)
}

func writePNG(t *testing.T, dir, name string, img stdimage.Image) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", name)
}

// ---- the scenes ----

func row(gap kit.DIP, ws ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(gap)}}
	e.Add(ws...)
	return e
}

func column(gap kit.DIP, pad kit.DIP, ws ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(gap), Padding: kit.EdgesAll(kit.Px(pad))}}
	e.Add(ws...)
	return e
}

// hover, press and ring put a widget in a state.
func hover(w *kit.Window, x kit.Widget) {
	w.Event(kit.PointerEvent{Action: kit.PointerMove, Pos: x.Base().WindowBounds().Center()})
}

func press(w *kit.Window, x kit.Widget) {
	p := x.Base().WindowBounds().Center()
	w.Event(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	w.Event(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
}

func ring(w *kit.Window, x kit.Widget) {
	w.Focus(x)
	w.SetFocusVisible(true)
}

func groundChrome(t *tokens.Theme) kit.Color  { return t.Chrome }
func groundRaised(t *tokens.Theme) kit.Color  { return t.Raised }
func groundSurface(t *tokens.Theme) kit.Color { return t.Surface }

func buttonsScene() scene {
	return scene{name: "buttons", size: kit.Size{W: 760, H: 400}, ground: groundRaised, build: func() (kit.Widget, func(*kit.Window)) {
		type st struct {
			w     kit.Widget
			state string
		}
		var states []st
		variants := []struct {
			v    widgets.Variant
			text string
		}{{widgets.Secondary, "Cancel"}, {widgets.Primary, "Save"}, {widgets.Ghost, "Resume"}, {widgets.Danger, "Remove"}, {widgets.DangerText, "Delete queue…"}, {widgets.Link, "Retry"}}
		col := column(10, 14)
		for _, v := range variants {
			r := row(12)
			for _, s := range []string{"rest", "hover", "press", "ring", "disabled"} {
				icon := ""
				if v.v == widgets.Ghost {
					icon = "resume"
				}
				b := widgets.NewButton(v.text, icon, v.v, widgets.SizeMd, func() {})
				if s == "disabled" {
					b.SetDisabled(true, true)
				}
				states = append(states, st{b, s})
				r.Add(b)
			}
			col.Add(r)
		}
		sizes := row(12, widgets.NewButton("Start", "play", widgets.Secondary, widgets.SizeSm, nil),
			widgets.NewButton("New queue", "add", widgets.Secondary, widgets.SizeMd, nil),
			widgets.NewButton("Add", "add", widgets.Primary, widgets.SizeLg, nil),
			widgets.NewButton("Close", "", widgets.Secondary, widgets.SizeFoot, nil))
		col.Add(sizes)
		ir := row(8)
		for _, s := range []string{"rest", "hover", "press", "expanded", "disabled"} {
			b := widgets.NewIconButton("more", 16, "More actions", false, func() {})
			sm := widgets.NewIconButton("close", 14, "Dismiss", true, func() {})
			switch s {
			case "expanded":
				b.SetExpanded(true)
			case "disabled":
				b.SetDisabled(true, true)
				sm.SetDisabled(true, true)
			}
			states = append(states, st{b, s})
			ir.Add(b, sm)
		}
		ir.Add(widgets.NewSplitButton("Add", "add", func() {}, []widgets.MenuItem{{ID: "files", Label: "Add downloads…"}}, func(string) {}, "More ways to add", "Add"))
		col.Add(ir)
		return col, func(w *kit.Window) {
			// one pointer: the window hovers one chain and a press keeps it, and a press ends
			// focus-visible; so the press goes first (the "press" column of the first row
			// shows it), then the keyboard focus (the "ring" column of the second row)
			var pressed, ringed bool
			for _, s := range states {
				switch {
				case s.state == "press" && !pressed:
					press(w, s.w)
					pressed = true
				}
			}
			for _, s := range states[5:] {
				if s.state == "ring" && !ringed {
					ring(w, s.w)
					ringed = true
				}
			}
		}
	}}
}

func chipsScene() scene {
	return scene{name: "chips", size: kit.Size{W: 640, H: 150}, ground: groundChrome, build: func() (kit.Widget, func(*kit.Window)) {
		live := widgets.NewChip("3 downloading", widgets.Live, true, "", func() {})
		queued := widgets.NewChip("7 queued", widgets.Plain, false, "", func() {})
		failed := widgets.NewChip("1 failed", widgets.Fail, false, "", func() {})
		for _, c := range []*widgets.Chip{live, queued, failed} {
			c.Style.ColumnGap = kit.Px(5)
		}
		pl := widgets.NewChip("3 downloading", widgets.Live, true, "", func() {})
		pl.SetPressed(true)
		pq := widgets.NewChip("7 queued", widgets.Plain, false, "", func() {})
		pq.SetPressed(true)
		pf := widgets.NewChip("1 failed", widgets.Fail, false, "", func() {})
		pf.SetPressed(true)
		days := row(4)
		for i, d := range []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} {
			c := widgets.NewChip(d, widgets.Plain, false, "", func() {})
			c.Style.Height, c.Style.MinWidth, c.Style.Justify = kit.Px(28), kit.Rem(2.75), kit.JustifyCenter
			c.SetPressed(i < 5)
			days.Add(c)
		}
		pills := row(8, widgets.NewPill("Stopped", widgets.Plain), widgets.NewPill("Running", widgets.Ok), widgets.NewPill("Update", widgets.Ok),
			widgets.NewPill("Installing", widgets.Info), widgets.NewPill("Paused", widgets.Warn), widgets.NewPill("Failed", widgets.Fail),
			widgets.NewPill("Done", widgets.Done), widgets.NewKbd("Ctrl K"), widgets.NewKbd("Ctrl+N"))
		return column(10, 14, row(6, live, queued, failed), row(6, pl, pq, pf), days, pills), func(w *kit.Window) { hover(w, queued) }
	}}
}

func togglesScene() scene {
	return scene{name: "toggles", size: kit.Size{W: 640, H: 360}, ground: groundSurface, build: func() (kit.Widget, func(*kit.Window)) {
		unchecked := widgets.NewCheckbox("Also delete the file from disk (including partial data)", false, nil)
		checked := widgets.NewCheckbox("Stop at", true, nil)
		small := widgets.NewCheckBox(14, true, nil)
		disabled := widgets.NewCheckbox("Clear the saved password", true, nil)
		disabled.SetDisabled(true)
		on := widgets.NewSwitchRow("Start and stop this queue on a schedule", "", true, nil)
		off := widgets.NewSwitchRow("Wake the computer from sleep to start", "While Downex runs in the background.", false, nil)
		dis := widgets.NewSwitchRow("Show the tray icon", "", true, nil)
		dis.SetDisabled(true)
		radios := widgets.NewRadioRows("When the file is already there", []widgets.RadioOption{
			{Value: "keep", Label: "Keep both", Note: "Saves the new one beside the old one.", Icon: "copy"},
			{Value: "replace", Label: "Replace", Note: "Deletes the old file first.", Icon: "restart"},
		}, "keep", false, nil)
		radios.Style.Width = kit.Px(420)
		col := column(8, 14, row(16, unchecked), row(16, checked, small, disabled), on, off, dis, radios)
		for _, r := range []*widgets.SwitchRow{on, off, dis} {
			r.Style.Width = kit.Px(420)
		}
		return col, func(w *kit.Window) { ring(w, checked.Box) }
	}}
}

func segmentsScene() scene {
	return scene{name: "segmented-tabs", size: kit.Size{W: 600, H: 250}, ground: groundRaised, build: func() (kit.Widget, func(*kit.Window)) {
		mode := widgets.NewSegmentedControl([]widgets.SegmentOption{{Value: "files", Label: "Files", Icon: "link"}, {Value: "stream", Label: "Video or stream", Icon: "play"}},
			"files", widgets.SegmentedOptions{Legend: "Add mode"}, nil)
		when := widgets.NewSegmentedControl([]widgets.SegmentOption{{Value: "days", Label: "Every week"}, {Value: "once", Label: "Once"}},
			"days", widgets.SegmentedOptions{AriaLabel: "When", Small: true}, nil)
		theme := widgets.NewSegmentedControl([]widgets.SegmentOption{{Value: "system", Label: "System"}, {Value: "light", Label: "Light", Count: 2, HasCount: true}, {Value: "dark", Label: "Dark", Disabled: true}},
			"light", widgets.SegmentedOptions{AriaLabel: "Theme"}, nil)
		tabs := widgets.NewTabs([]widgets.TabOption{{Value: "status", Label: "Status"}, {Value: "limit", Label: "Speed limit", Dot: true}, {Value: "log", Label: "Log", Count: 3, HasCount: true}, {Value: "gone", Label: "Gone", Disabled: true}},
			"limit", widgets.TabsOptions{AriaLabel: "Progress view"}, nil)
		small := widgets.NewTabs([]widgets.TabOption{{Value: "status", Label: "Status"}, {Value: "limit", Label: "Speed limit"}},
			"status", widgets.TabsOptions{AriaLabel: "Progress view", Small: true}, nil)
		return column(14, 14, row(12, mode, when), theme, tabs, small), func(w *kit.Window) { hover(w, tabs.TabWidget("status")) }
	}}
}

func fieldsScene() scene {
	return scene{name: "fields", size: kit.Size{W: 640, H: 330}, ground: groundRaised, build: func() (kit.Widget, func(*kit.Window)) {
		md := widgets.NewInput("", widgets.InputOptions{Placeholder: "https://example.com/file.zip", Name: "Address"}, nil)
		focused := widgets.NewInput("", widgets.InputOptions{Placeholder: "Night", Size: widgets.FieldSm}, nil)
		invalid := widgets.NewInput("", widgets.InputOptions{Placeholder: "0 = the global setting", Size: widgets.FieldSm, Invalid: true}, nil)
		disabled := widgets.NewInput("", widgets.InputOptions{Placeholder: "No limit", Size: widgets.FieldSm, Disabled: true}, nil)
		clock := widgets.NewDateTimeField(queues.TimeField, "", "Start at", nil)
		date := widgets.NewDateTimeField(queues.DateField, "", "Date", nil)
		area := widgets.NewTextArea("", 4, widgets.InputOptions{Placeholder: "https://example.com/file.zip", Name: "URLs"}, nil)
		sel := widgets.NewSelect([]widgets.SelectOption{{Value: "run", Label: "Keep running"}}, "run", widgets.SelectOptions{Label: "When the queue is done", Size: widgets.SelectSm, Icon: "power"}, nil)
		sel.Style.Width = kit.Px(188)
		unit := widgets.NewSelect([]widgets.SelectOption{{Value: "mb", Label: "MB/s"}}, "mb", widgets.SelectOptions{Label: "Unit", Size: widgets.SelectSm}, nil)
		unit.Style.Width = kit.Px(97)
		audio := widgets.NewSelect([]widgets.SelectOption{{Value: "a", Label: "English", Detail: "AAC 129 kbit/s"}}, "a", widgets.SelectOptions{Label: "Audio"}, nil)
		audio.Style.Width = kit.Px(300)
		inline := widgets.NewSelect([]widgets.SelectOption{{Value: "u", Label: "Unlimited"}}, "u", widgets.SelectOptions{Label: "Speed limit", Size: widgets.SelectInline}, nil)
		for _, f := range []*widgets.Input{focused, invalid, disabled} {
			f.Style.Width = kit.Px(186)
		}
		md.Style.Width = kit.Px(400)
		area.Style.Width = kit.Px(400)
		return column(10, 14, row(12, md, audio), row(12, focused, invalid, disabled), row(12, clock, date, sel, unit, inline), area), func(w *kit.Window) { ring(w, focused) }
	}}
}

func menuScene() scene {
	return scene{name: "menu", size: kit.Size{W: 640, H: 400}, ground: groundChrome, build: func() (kit.Widget, func(*kit.Window)) {
		menu := widgets.MenuPreview(widgets.MenuOptions{Label: "More actions", Footnote: "Saved on this computer right away.", Items: []widgets.MenuItem{
			{ID: "comfortable", Label: "Comfortable", Kind: widgets.ItemRadio, Checked: true, Heading: "Density"},
			{ID: "compact", Label: "Compact", Kind: widgets.ItemRadio, Note: "18 rows"},
			{ID: "pane", Label: "Details pane", Kind: widgets.ItemCheckbox, Checked: true, Separated: true},
			{ID: "queues", Label: "Manage queues…", Icon: "queue"},
			{ID: "open", Label: "Open", Disabled: true, Icon: "open"},
			{ID: "add", Label: "Add downloads…", Shortcut: "Ctrl+N", Icon: "add"},
			{ID: "remove", Label: "Remove…", Danger: true, Icon: "remove", Separated: true},
		}}, kit.Size{W: 1200, H: 760})
		list := widgets.ListPreview([]widgets.SelectOption{
			{Value: "none", Label: "Unlimited"}, {Value: "20", Label: "20 MiB/s"}, {Value: "10", Label: "10 MiB/s", Detail: "half"},
			{Value: "5", Label: "5 MiB/s", Disabled: true}, {Value: "1", Label: "1 MiB/s", Meta: "~1 GiB/h"},
		}, "20", 1, true, 200)
		r := row(40, menu, list)
		r.Style.AlignItems = kit.AlignStart
		r.Style.Padding = kit.Edges{Top: kit.Px(30), Start: kit.Px(60)}
		return r, nil
	}}
}

func dialogScene() scene {
	return scene{name: "dialog", size: kit.Size{W: 1200, H: 760}, ground: groundChrome, build: func() (kit.Widget, func(*kit.Window)) {
		holder := &kit.Element{}
		return holder, func(w *kit.Window) { removeDialog(w) }
	}}
}

// removeDialog is RemoveDialog.svelte's one-download state, in the shell.
func removeDialog(w *kit.Window) *widgets.Dialog {
	d := widgets.NewDialog(widgets.DialogOptions{Title: "Remove download?", Description: "The download is removed from the list.", Alert: true, Size: widgets.DialogSm, ModalTop: 44})
	d.SetIcon(newToneTile("remove"))
	names := newNamesBox("podcast-episode-41.mp3")
	check := widgets.NewCheckbox("Also delete the file from disk (including partial data)", false, nil)
	check.Label.SetFont(kit.Font{Family: tokens.FontUI, Size: tokens.TypeCell, Weight: 400, LineHeight: 1.4, Tabular: true, AutoOptical: true})
	d.Body.Add(names, check)
	cancel := widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil)
	cancel.Autofocus = true
	d.SetFooter(nil, cancel, widgets.NewButton("Remove", "", widgets.Danger, widgets.SizeMd, nil))
	d.Show(w)
	w.SetFocusVisible(true)
	return d
}

func toastScene() scene {
	return scene{name: "toasts", size: kit.Size{W: 520, H: 420}, ground: groundChrome, build: func() (kit.Widget, func(*kit.Window)) {
		holder := &kit.Element{}
		return holder, func(w *kit.Window) {
			ts := widgets.NewToasts(w)
			ns := widgets.NewNotices(w)
			l := widgets.NewToastLayer(ts, ns)
			l.Push(w)
			ts.Success("2 downloads paused")
			ts.Show("Nothing to pause", widgets.ToastInfo)
			ts.Error("Could not reach downexd. Start it with downex daemon start.")
			ns.Show(widgets.Notice{Key: "k", Title: "Download from your browser", Detail: "ubuntu-26.04-desktop-amd64.iso",
				Actions: []widgets.NoticeAction{{Label: "Later"}, {Label: "Confirm…", Primary: true}}})
		}
	}}
}

func progressScene() scene {
	return scene{name: "progress", size: kit.Size{W: 520, H: 260}, ground: groundSurface, build: func() (kit.Widget, func(*kit.Window)) {
		bars := []*widgets.ProgressBar{widgets.NewProgressBar("Progress", false), widgets.NewProgressBar("Progress", false), widgets.NewProgressBar("Progress", false),
			widgets.NewProgressBar("Progress", false), widgets.NewProgressBar("Progress", true)}
		segs := []*widgets.SegmentBar{widgets.NewSegmentBar(widgets.SegRow), widgets.NewSegmentBar(widgets.SegCompact), widgets.NewSegmentBar(widgets.SegMini), widgets.NewSegmentBar(widgets.SegLg)}
		col := column(12, 16)
		for _, b := range bars {
			col.Add(b)
		}
		for _, s := range segs {
			col.Add(row(0, s))
		}
		return col, func(w *kit.Window) {
			bars[0].Set(0.45, widgets.BarDownloading)
			bars[1].Set(0.6, widgets.BarPaused)
			bars[2].Set(0.3, widgets.BarFailed)
			bars[3].Set(math.NaN(), widgets.BarDownloading)
			bars[4].Set(0.82, widgets.BarCompleted)
			segs[0].Set([]widgets.SegCell{{Fraction: 0.45, Live: true}}, widgets.BarDownloading)
			segs[1].Set([]widgets.SegCell{{Fraction: 0.6}}, widgets.BarPaused)
			segs[2].Set([]widgets.SegCell{{Fraction: 0.3}}, widgets.BarDownloading)
			segs[3].Set([]widgets.SegCell{{Fraction: 1}, {Fraction: 1}, {Fraction: 0.7, Live: true}, {Fraction: 0.4, Live: true}, {Fraction: 0.02, Live: true}, {}, {}, {Fraction: 1}}, widgets.BarDownloading)
		}
	}}
}

// ---- the references ----

// refPair is a reference crop and the native scene that shows the same thing.
type refPair struct {
	name  string
	file  func(theme string, scale string) string // under screens/
	rect  stdimage.Rectangle                      // at 1x
	scene scene
}

func loadPNG(t *testing.T, path string) stdimage.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func crop(img stdimage.Image, r stdimage.Rectangle) *stdimage.RGBA {
	out := stdimage.NewRGBA(stdimage.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

func scaled(r stdimage.Rectangle, k float64) stdimage.Rectangle {
	return stdimage.Rect(int(float64(r.Min.X)*k), int(float64(r.Min.Y)*k), int(math.Ceil(float64(r.Max.X)*k)), int(math.Ceil(float64(r.Max.Y)*k)))
}

// headerScene is the main window's page header from x 400: the chips, the selection's buttons
// (no selection: aria-disabled), the bulk pair, the More button and Add, at the references'
// places (measure-dialogs' layout dump of main__1200x760).
func headerScene() scene {
	return scene{name: "header", size: kit.Size{W: 800, H: 44}, ground: groundChrome, build: func() (kit.Widget, func(*kit.Window)) {
		p := &placer{}
		at := func(x, y, w, h kit.DIP, wd kit.Widget) { p.Put(wd, kit.R(x-400, y-52, x-400+w, y-52+h)) }
		live := widgets.NewChip("3 downloading", widgets.Live, true, "", func() {})
		queued := widgets.NewChip("7 queued", widgets.Plain, false, "", func() {})
		failed := widgets.NewChip("1 failed", widgets.Fail, false, "", func() {})
		for _, c := range []*widgets.Chip{live, queued, failed} {
			c.Style.ColumnGap = kit.Px(5)
		}
		at(409.1, 63, 253, 22, row(6, live, queued, failed))
		for i, b := range []struct {
			x, w       kit.DIP
			text, icon string
		}{{698.9, 88.2, "Resume", "resume"}, {789.1, 76, "Pause", "pause"}, {867.2, 89.4, "Remove", "remove"}} {
			_ = i
			btn := widgets.NewButton(b.text, b.icon, widgets.Ghost, widgets.SizeMd, nil)
			btn.SetIconSize(15)                            // Toolbar.svelte's
			btn.Style.Padding = kit.EdgesInline(kit.Px(9)) // .actions .btn.ghost
			btn.SetDisabled(true, true)
			at(b.x, 59, b.w+0.5, 30, btn) // the reference boxes are their content's width, rounded
		}
		for i, ic := range []string{"resume-all", "pause-all"} {
			btn := widgets.NewButton("", ic, widgets.Ghost, widgets.SizeMd, nil)
			btn.Style.Padding = kit.Edges{}
			btn.SetIconSize(15)
			at(973.6+32*kit.DIP(i), 59, 30, 30, btn)
		}
		at(1039.6, 59, 30, 30, widgets.NewIconButton("more", 18, "More actions", false, nil))
		at(1079.6, 59, 108.4, 30, widgets.NewSplitButton("Add", "add", nil, nil, nil, "More ways to add", "Add"))
		return p, nil
	}}
}

func removeScene() scene {
	sc := dialogScene()
	sc.name = "remove"
	return sc
}

// pairs are the references cropped beside the native renders.
func pairs() []refPair {
	main := func(theme, scale string) string {
		return filepath.Join("main", fmt.Sprintf("main__1200x760__%s__16px__%s.png", scale, theme))
	}
	state := func(name string) func(string, string) string {
		return func(theme, scale string) string {
			return filepath.Join("states", name, fmt.Sprintf("%s__1200x760__%s__16px__%s.png", name, scale, theme))
		}
	}
	return []refPair{
		{name: "header", file: main, rect: stdimage.Rect(400, 52, 1200, 96), scene: headerScene()},
		{name: "remove-dialog", file: state("remove"), rect: stdimage.Rect(380, 284, 820, 520), scene: removeScene()},
	}
}

// TestWidgetShots writes the widgets' sheets and the side-by-side comparisons.
func TestWidgetShots(t *testing.T) {
	dir := os.Getenv("DOWNEX_WIDGET_SHOTS")
	if dir == "" {
		t.Skip("set DOWNEX_WIDGET_SHOTS to the folder for the widget renders")
	}
	res := newRes(t)
	t.Cleanup(func() { widgets.ReleaseIcons(res) })
	for _, sc := range []scene{buttonsScene(), chipsScene(), togglesScene(), segmentsScene(), fieldsScene(), menuScene(), toastScene(), progressScene()} {
		writePNG(t, dir, "native-s1-widget-"+sc.name+".png", sheet(t, res, sc))
	}
	ref := os.Getenv("DOWNEX_REF_092")
	if ref == "" {
		t.Log("UNVERIFIED: set DOWNEX_REF_092 to compare with the 0.9.2 references")
		return
	}
	for _, p := range pairs() {
		var rows [][]stdimage.Image
		for _, sc := range []struct {
			scale string
			k     float32
		}{{"1x", 1}, {"1.25x", 1.25}} {
			var r []stdimage.Image
			for _, theme := range []string{"light", "dark"} {
				rect := scaled(p.rect, float64(sc.k))
				refImg := crop(loadPNG(t, filepath.Join(ref, "screens", p.file(theme, sc.scale))), rect)
				nat := render(t, res, p.scene, theme == "dark", sc.k)
				var natCrop *stdimage.RGBA
				if p.scene.size.W >= 1200 { // a whole-window scene: the same crop
					natCrop = crop(nat, rect)
				} else {
					natCrop = nat
				}
				r = append(r, grid([][]stdimage.Image{{refImg}, {natCrop}}, 2))
			}
			rows = append(rows, r)
		}
		writePNG(t, dir, "native-s1-widget-ref-"+p.name+".png", grid(rows, 8))
	}
}

func iconByName(n string) (icons.Name, bool) { return icons.ByName(n) }

// placer lays its children out at the rectangles given (the references' layout dump).
type placer struct {
	kit.Element
	rects []kit.Rect
}

func (p *placer) Put(w kit.Widget, r kit.Rect) {
	p.Add(w)
	p.rects = append(p.rects, r)
}

func (p *placer) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }
func (p *placer) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return 0 }
func (p *placer) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	for i, c := range p.Children() {
		ctx.Place(c.Widget(), p.rects[i], box.Width())
	}
}

// toneTile is RemoveDialog's .tone.danger: a 32 px --danger-soft tile with 8 px corners and an
// 18 px icon in --danger.
type toneTile struct {
	kit.Element
}

func newToneTile(icon string) *toneTile {
	t := &toneTile{}
	t.Style = kit.Style{Width: kit.Px(32), Height: kit.Px(32), Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter}
	n, _ := iconByName(icon)
	t.Add(widgets.NewIcon(n, 18, func(th *tokens.Theme) kit.Color { return th.Danger }))
	return t
}

func (t *toneTile) Paint(p *kit.PaintContext) {
	th := widgets.ThemeOf(&t.Element).Tokens(p.Env)
	p.FillRoundedRect(kit.RoundRect(kit.RectAt(kit.Point{}, p.Size()), 8), th.DangerSoft)
}

// namesBox is RemoveDialog's .names: 8 11 px padding, a --line border, 9 px corners,
// --surface-2, 12.5 px --ink.
type namesBox struct {
	kit.Element
	label *kit.Label
}

func newNamesBox(name string) *namesBox {
	b := &namesBox{}
	b.Style = kit.Style{Padding: kit.EdgesOf(kit.Px(8), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(9))}
	b.label = kit.NewLabel(name, kit.Font{Family: tokens.FontUI, Size: tokens.TypeCell, Weight: 400, LineHeight: 1.4, Tabular: true, AutoOptical: true}, kit.TextOptions{Truncate: kit.TruncateEnd})
	b.Add(b.label)
	return b
}

func (b *namesBox) Paint(p *kit.PaintContext) {
	th := widgets.ThemeOf(&b.Element).Tokens(p.Env)
	r := kit.RRect{Rect: p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), Radii: kit.UniformRadii(9)}
	p.Canvas.FillRoundedRect(r, th.Surface2)
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, th.Line)
	b.label.SetColor(th.Ink)
}
