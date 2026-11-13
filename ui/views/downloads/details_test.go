package downloads

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/core/format"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// pane is DetailsPanel.test's show(download): the pane over a fixed list with the first
// download selected (several when many is given).
type pane struct {
	*rig
	daemon *statetest.Daemon
}

func showPane(t *testing.T, d *downexv1.Download, prep func(r *rig, daemon *statetest.Daemon)) *pane {
	t.Helper()
	daemon := statetest.NewDaemon(d)
	r := newRig(t, viewstest.Options{Daemon: daemon}, kit.R(225, 103, 1200, 727), 226)
	if prep != nil {
		prep(r, daemon)
	}
	r.Main.Downloads.SelectOnly(d.GetId())
	r.refresh()
	return &pane{rig: r, daemon: daemon}
}

// root is the pane's single section (or the strip).
func (p *pane) root() *kit.Element { return &p.pane.Element }

// fact is the value shown for a fact ("Video", "Output", …), its spaces collapsed.
func (p *pane) fact(term string) (string, bool) {
	var found *kit.Element
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if found != nil || e.Style.Hidden {
			return
		}
		if tx, ok := e.Widget().(*views.Text); ok && tx.Semantics.Role == kit.RoleTerm && tx.Text() == term {
			found = e.Parent()
			return
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(p.root())
	if found == nil {
		return "", false
	}
	return strings.Join(strings.Fields(textOf(found.Children()[1])), " "), true
}

func (p *pane) text() string { return textOf(p.root()) }

// buttonNamed is the button named s (its label or its accessible name), or nil.
func (p *pane) buttonNamed(s string) *kit.Element {
	var found *kit.Element
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if found != nil || e.Style.Hidden {
			return
		}
		if e.Semantics.Role == kit.RoleButton && (e.Semantics.Name == s || textOf(e) == s) {
			found = e
			return
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(p.root())
	return found
}

// find is the first element under the pane of a type.
func find[T kit.Widget](e *kit.Element) (T, bool) {
	var zero T
	var walk func(e *kit.Element) (T, bool)
	walk = func(e *kit.Element) (T, bool) {
		if e.Style.Hidden {
			return zero, false
		}
		if w, ok := e.Widget().(T); ok {
			return w, true
		}
		for _, c := range e.Children() {
			if w, ok := walk(c); ok {
				return w, true
			}
		}
		return zero, false
	}
	return walk(e)
}

func (p *pane) pill() *widgets.Pill { w, _ := find[*widgets.Pill](p.root()); return w }

func (p *pane) conns() *connections { w, _ := find[*connections](p.root()); return w }

func (p *pane) sub() string {
	if p.pane.single == nil {
		return ""
	}
	head := p.pane.single.Children()[0]
	titles := head.Children()[1]
	if len(titles.Children()) < 2 {
		return ""
	}
	return textOf(titles.Children()[1])
}

func (p *pane) badge() string {
	tag, ok := find[*views.Tag](p.root())
	if !ok {
		return ""
	}
	return tag.Semantics.Name
}

func (p *pane) big() string {
	var parts []string
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if tx, ok := e.Widget().(*views.Text); ok && (tx.Font() == fontBig || tx.Font() == fontBigUnit) {
			parts = append(parts, tx.Text())
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(p.root())
	return strings.Join(parts, " ")
}

// ---- DetailsPanel.test.ts: media downloads -------------------------------------------------------------

func TestDetailsMedia(t *testing.T) {
	t.Run("shows the stream, its chosen tracks, the fragments and the output container", func(t *testing.T) {
		p := showPane(t, MediaDownload(4, func(d *downexv1.Download) {
			d.State, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 120*MiB, 4*MiB, 6
			d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
		}), nil)
		if b := p.badge(); b != "HLS" {
			t.Errorf("badge %q", b)
		}
		for term, want := range map[string]string{
			"Manifest": "https://media.example.org/talk/master.m3u8",
			"Stream":   "HLS · 10:00",
			"Video":    "1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps",
			"Audio":    "English · 128 kbit/s · mp4a.40.2",
			"Output":   "MP4",
			"Received": "126 MB · 25%",
		} {
			if got, _ := p.fact(term); got != want {
				t.Errorf("%s: %q, want %q", term, got, want)
			}
		}
		// the sub-line says what it is, with the quality in plain words
		if s := p.sub(); s != "media.example.org · Video · 1080p · H.264 · AAC 128 kbit/s" {
			t.Errorf("sub %q", s)
		}
		if b := p.big(); b != "4.2 MB/s" {
			t.Errorf("big %q", b)
		}
		c := p.conns()
		if c.boxes.Semantics.Name != "Fragments downloaded" || c.boxes.Semantics.Range.Value != 25 {
			t.Errorf("bar %q %v", c.boxes.Semantics.Name, c.boxes.Semantics.Range)
		}
		if !strings.Contains(p.text(), "50 of 200") || strings.Contains(p.text(), "Segments") {
			t.Errorf("text %q", p.text())
		}
	})

	t.Run("shows a stream’s estimated size and the time left from it", func(t *testing.T) {
		p := showPane(t, MediaDownload(4, func(d *downexv1.Download) {
			d.State, d.Size, d.SizeApprox, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 2100*MiB, true, 700*MiB, 7*MiB, 6
			d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
		}), nil)
		if got, _ := p.fact("Received"); got != "734 MB of ~2.2 GB · 25%" {
			t.Errorf("received %q", got)
		}
		if findText(p.root(), "3m 20s left") == nil {
			t.Error("no time left")
		}
		if !regexp.MustCompile(`Done ≈ \S`).MatchString(p.text()) {
			t.Errorf("no Done ≈: %q", p.text())
		}
	})

	t.Run("says a muxed track carries the sound and an undecided container is pending", func(t *testing.T) {
		p := showPane(t, MediaDownload(5, func(d *downexv1.Download) {
			d.State = state.StateQueued
			d.Media = MediaInfo(func(m *downexv1.MediaInfo) {
				m.SelectedVideo, m.SelectedAudio, m.Container, m.FragmentsTotal = "v2", "", "", 0
			})
		}), nil)
		if got, _ := p.fact("Audio"); got != "In the video track" {
			t.Errorf("audio %q", got)
		}
		if got, _ := p.fact("Output"); got != "Decided when the stream is assembled" {
			t.Errorf("output %q", got)
		}
		if !strings.Contains(p.text(), "counted when the download starts") {
			t.Error("fragments not counted yet")
		}
	})

	t.Run("offers no retry for a stream refused as encrypted", func(t *testing.T) {
		p := showPane(t, MediaDownload(6, func(d *downexv1.Download) {
			d.State, d.ErrorCode, d.Error = state.StateFailed, "encrypted", "An initialization segment is encrypted"
		}), nil)
		if !strings.Contains(p.text(), "does not download protected content") {
			t.Errorf("text %q", p.text())
		}
		if p.buttonNamed("Retry") != nil {
			t.Error("Retry")
		}
	})

	t.Run("shows the connections overview for files", func(t *testing.T) {
		p := showPane(t, Download(7, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MiB }), nil)
		c := p.conns()
		if c == nil || !strings.HasPrefix(c.head.Semantics.Name, "Connections") {
			t.Fatal("no Connections heading")
		}
		if !strings.Contains(p.text(), "Not running") {
			t.Error("Not running")
		}
		if got, _ := p.fact("Address"); !strings.Contains(got, "file-7.bin") {
			t.Errorf("address %q", got)
		}
		if got, _ := p.fact("Received"); got != "1.0 MB of 10.5 MB · 10%" {
			t.Errorf("received %q", got)
		}
		if c.boxes.Semantics.Name != "Received" || c.boxes.Semantics.Range.Value != 10 {
			t.Errorf("bar %q %v", c.boxes.Semantics.Name, c.boxes.Semantics.Range)
		}
	})
}

// ---- DetailsPanel: video pages ----------------------------------------------------------------------------

func TestDetailsVideoPages(t *testing.T) {
	t.Run("shows the page, the site, the chosen quality, the parts and the size in bytes", func(t *testing.T) {
		p := showPane(t, SiteDownload(8, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 460*MiB, 115*MiB, 4*MiB, 4
			d.Media = SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal, m.FragmentsDone = 46, 11 })
		}), nil)
		if b := p.badge(); b != "YouTube" {
			t.Errorf("badge %q", b)
		}
		for term, want := range map[string]string{
			"Page":     Page,
			"Site":     "YouTube video · 10:00",
			"Video":    "1080p (Full HD) 60fps · H.264 · 472 MB · 1920×1080",
			"Audio":    "English original · AAC · 129 kbit/s · 10.5 MB",
			"Output":   "MP4",
			"Received": "121 MB of 482 MB · 25%",
		} {
			if got, _ := p.fact(term); got != want {
				t.Errorf("%s: %q, want %q", term, got, want)
			}
		}
		if s := p.sub(); s != "youtube.com · Video · 1080p · H.264 · AAC 129 kbit/s" {
			t.Errorf("sub %q", s)
		}
		c := p.conns()
		if !strings.HasPrefix(c.head.Semantics.Name, "Parts") || !strings.Contains(c.head.Semantics.Name, "11 of 46") {
			t.Errorf("heading %q", c.head.Semantics.Name)
		}
		if c.boxes.Semantics.Name != "Parts downloaded" {
			t.Errorf("bar %q", c.boxes.Semantics.Name)
		}
		if strings.Contains(p.text(), "Manifest") {
			t.Error("Manifest")
		}
	})

	t.Run("says it is getting the video info while yt-dlp reads the page", func(t *testing.T) {
		p := showPane(t, SiteDownload(9, func(d *downexv1.Download) { d.State = state.StateProbing }), nil)
		if s := textOf(&p.pill().Element); s != "Getting video info" {
			t.Errorf("pill %q", s)
		}
	})

	t.Run("waits for the quality with its exact size before it starts", func(t *testing.T) {
		p := showPane(t, SiteDownload(10, func(d *downexv1.Download) { d.State = state.StateAwaiting }), nil)
		if !strings.Contains(p.text(), "This video from the browser waits until you choose its quality") {
			t.Error("note")
		}
		if got, _ := p.fact("Received"); got != "Not started (482 MB)" {
			t.Errorf("received %q", got)
		}
		p.Click(p.buttonNamed("Review…"))
		if !p.Calls.Has("Confirm") {
			t.Error("Review… did not confirm")
		}
	})

	t.Run("explains a page that failed, without offering what cannot help", func(t *testing.T) {
		p := showPane(t, SiteDownload(11, func(d *downexv1.Download) {
			d.State, d.ErrorCode, d.Error = state.StateFailed, "site_unavailable", "this video is not available: Private video"
		}), nil)
		if !strings.Contains(p.text(), "This video is not available (private, removed") {
			t.Errorf("text %q", p.text())
		}
		if p.buttonNamed("Retry") == nil {
			t.Error("no Retry")
		}
	})

	t.Run("offers no site login for a video page that wants a sign-in, and no retry for a gone quality", func(t *testing.T) {
		p := showPane(t, SiteDownload(12, func(d *downexv1.Download) {
			d.State, d.ErrorCode, d.Error = state.StateFailed, "auth_required", "the site asks to sign in"
		}), nil)
		if !strings.Contains(p.text(), "The site wants you to sign in") {
			t.Errorf("text %q", p.text())
		}
		if p.buttonNamed("Add site login…") != nil {
			t.Error("Add site login…")
		}
	})

	t.Run("offers no retry for a quality the page no longer has", func(t *testing.T) {
		p := showPane(t, SiteDownload(13, func(d *downexv1.Download) {
			d.State, d.ErrorCode, d.Error = state.StateFailed, "media_invalid", "the chosen quality is gone"
		}), nil)
		if findText(p.root(), "The chosen quality is no longer offered. Add the video again.") == nil {
			t.Errorf("text %q", p.text())
		}
		if p.buttonNamed("Retry") != nil {
			t.Error("Retry")
		}
	})
}

// ---- DetailsPanel: visibility ------------------------------------------------------------------------------

func TestDetailsVisibility(t *testing.T) {
	t.Run("is not rendered when nothing is selected", func(t *testing.T) {
		r, _ := fakeRig(t, Download(1, nil))
		if r.pane.Mode() != "" || len(r.pane.Children()) != 0 {
			t.Errorf("mode %q", r.pane.Mode())
		}
	})

	t.Run("is a strip with the size and the selection’s actions when several are selected", func(t *testing.T) {
		list := []*downexv1.Download{
			Download(1, func(d *downexv1.Download) { d.State, d.Size = state.StateDownloading, 3*1024*MiB }),
			Download(2, func(d *downexv1.Download) { d.State, d.Size = state.StateQueued, 1288490188 }),
			Download(3, func(d *downexv1.Download) { d.State, d.Size = state.StateCompleted, 0 }),
		}
		daemon := statetest.NewDaemon(list...)
		r := newRig(t, viewstest.Options{Daemon: daemon}, kit.R(225, 103, 1200, 727), 226)
		// the store may still name a focused download: several selected win
		r.Main.Downloads.SelectAll()
		r.refresh()
		p := &pane{rig: r, daemon: daemon}
		if r.pane.Mode() != "strip" {
			t.Fatal(r.pane.Mode())
		}
		if s := r.pane.strip.title.Text(); s != "3 downloads selected · 4.5 GB" {
			t.Errorf("title %q", s)
		}
		if _, ok := find[*columnsBox](p.root()); ok {
			t.Error("columns")
		}
		r.Click(p.buttonNamed("Pause"))
		r.Click(p.buttonNamed("Remove"))
		if got := r.Calls.Names(); !slices.Equal(got, []string{"Pause", "Remove"}) {
			t.Fatal(got)
		}
		// nothing in the selection can resume
		res := p.buttonNamed("Resume")
		if !res.Disabled || !res.DisabledFocusable {
			t.Error("Resume is not aria-disabled")
		}
		r.Click(res)
		if len(r.Calls.List) != 2 {
			t.Error(r.Calls.List)
		}
	})

	t.Run("shows only its header when collapsed, and offers to show the rest", func(t *testing.T) {
		p := showPane(t, Download(2, func(d *downexv1.Download) { d.State, d.SpeedBps, d.Received = state.StateDownloading, MiB, MiB }), func(r *rig, _ *statetest.Daemon) {
			r.App.Details.Set(true)
		})
		// the shell lays the pane out at its own height (flex: none)
		if p.pane.height != 44 {
			t.Errorf("height %v", p.pane.height)
		}
		if _, ok := find[*widgets.ScrollPane](p.root()); ok {
			t.Error("the body shows")
		}
		toggle := p.buttonNamed("Show details")
		if toggle == nil || toggle.Semantics.Expand != kit.ExpandClosed {
			t.Fatal("toggle")
		}
		p.Click(toggle)
		if !p.Calls.Has("ToggleDetails") {
			t.Error("toggle did not toggle")
		}
	})
}

// ---- DetailsPanel: header actions ------------------------------------------------------------------------

func TestDetailsHeader(t *testing.T) {
	t.Run("pauses, shows in folder, copies and opens the row menu", func(t *testing.T) {
		p := showPane(t, Download(3, func(d *downexv1.Download) { d.State, d.SpeedBps, d.Received = state.StateDownloading, MiB, 4*MiB }), nil)
		if s := textOf(&p.pill().Element); s != "Downloading · 40%" {
			t.Errorf("pill %q", s)
		}
		for _, b := range []string{"Pause", "Show in folder", "Copy link", "More download actions"} {
			p.Click(p.buttonNamed(b))
		}
		if got := p.Calls.Names(); !slices.Equal(got, []string{"Pause", "OpenFolder", "CopyURLs", "OpenMenu"}) {
			t.Fatal(got)
		}
		if p.Calls.List[1].Args[0] != int64(3) {
			t.Error(p.Calls.List[1])
		}
	})

	t.Run("resumes a paused download and offers nothing while downexd is unreachable", func(t *testing.T) {
		p := showPane(t, Download(4, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MiB }), nil)
		p.Click(p.buttonNamed("Resume"))
		if c, ok := p.Calls.Last("Resume"); !ok || c.Args[0].([]int64)[0] != 4 {
			t.Fatal(p.Calls.List)
		}
		p.Calls.Reset()
		p.daemon.SetDown(true)
		p.Main.Downloads.RefreshStatus()
		p.refresh()
		res := p.buttonNamed("Resume")
		if res == nil || !res.Disabled || !res.DisabledFocusable {
			t.Fatal("Resume is not aria-disabled")
		}
		p.Click(res)
		if p.Calls.Has("Resume") {
			t.Error("resumed while stale")
		}
	})

	t.Run("offers to download a completed download again (not while downexd is unreachable)", func(t *testing.T) {
		d := Download(7, func(d *downexv1.Download) {
			d.State, d.Received, d.CompletedAt = state.StateCompleted, 10*MiB, Millis(1_700_000_100_000)
		})
		p := showPane(t, d, nil)
		if p.buttonNamed("Resume") != nil {
			t.Error("Resume")
		}
		p.Click(p.buttonNamed("Download again…"))
		if c, ok := p.Calls.Last("Restart"); !ok || c.Args[0].(views.RestartRequest).IDs[0] != 7 {
			t.Fatal(p.Calls.List)
		}
		// only for completed ones
		paused := showPane(t, Download(8, func(d *downexv1.Download) { d.State = state.StatePaused }), nil)
		if paused.buttonNamed("Download again…") != nil {
			t.Error("Download again… for a paused one")
		}
		p.Calls.Reset()
		p.daemon.SetDown(true)
		p.Main.Downloads.RefreshStatus()
		p.refresh()
		again := p.buttonNamed("Download again…")
		if again == nil || !again.Disabled {
			t.Fatal("not aria-disabled")
		}
		p.Click(again)
		if p.Calls.Has("Restart") {
			t.Error("restarted while stale")
		}
	})

	t.Run("says where it came from and when", func(t *testing.T) {
		f, err := locale.New("en-GB", "UTC")
		if err != nil {
			t.Skip(err)
		}
		prev := format.Locale()
		format.SetLocale(f)
		defer format.SetLocale(prev)
		now := float64(time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC).UnixMilli())
		at := func(y int, mo time.Month, d, h, mi int) *downexv1.Download {
			return Download(5, func(x *downexv1.Download) {
				x.CreatedAt = Millis(time.Date(y, mo, d, h, mi, 0, 0, time.UTC).UnixMilli())
			})
		}
		today := at(2026, 10, 28, 14, 2)
		today.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		if s := display.AddedText(today, now); s != "Today, 14:02 · from the browser" {
			t.Errorf("%q", s)
		}
		y := at(2026, 10, 27, 9, 15)
		y.Source = downexv1.AddSource_ADD_SOURCE_UI
		if s := display.AddedText(y, now); s != "Yesterday, 9:15 · from the Downex window" {
			t.Errorf("%q", s)
		}
		if s := display.AddedText(at(2026, 10, 21, 18, 40), now); s != "21 Oct, 18:40" {
			t.Errorf("%q", s)
		}
	})

	t.Run("names the queue and the place in it", func(t *testing.T) {
		p := showPane(t, Download(6, func(d *downexv1.Download) { d.State, d.QueuePosition = state.StateQueued, 2 }), nil)
		if got, _ := p.fact("Queue"); got != "Main queue · position 2" {
			t.Errorf("queue %q", got)
		}
	})
}

// ---- DetailsPanel: speed limit -------------------------------------------------------------------------------

func labelsOf(o []display.LimitOption) []string {
	out := make([]string, len(o))
	for i, x := range o {
		out[i] = x.Label
	}
	return out
}

func TestDetailsSpeedLimit(t *testing.T) {
	t.Run("offers the presets, a limit set elsewhere and Custom…", func(t *testing.T) {
		if got := labelsOf(display.LimitOptions(0)); !slices.Equal(got, []string{"Unlimited", "20 MB/s", "10 MB/s", "5 MB/s", "1 MB/s", "Custom…"}) {
			t.Error(got)
		}
		var vals []float64
		for _, o := range display.LimitOptions(3 * MB) {
			vals = append(vals, o.Value)
		}
		if !slices.Equal(vals, []float64{0, 20 * MB, 10 * MB, 5 * MB, 3 * MB, MB, display.CustomLimit}) {
			t.Error(vals)
		}
		// a limit set in MiB/s before is its own item, in decimal units
		if got := labelsOf(display.LimitOptions(10 * MiB)); !slices.Equal(got, []string{"Unlimited", "20 MB/s", "10.5 MB/s", "10 MB/s", "5 MB/s", "1 MB/s", "Custom…"}) {
			t.Error(got)
		}
	})

	t.Run("sets a download’s limit, and Custom… opens its progress window", func(t *testing.T) {
		p := showPane(t, Download(7, func(d *downexv1.Download) {
			d.State, d.SpeedBps, d.Received, d.SpeedLimit = state.StateDownloading, MiB, MiB, 5*MB
		}), nil)
		sel, ok := find[*widgets.Select](p.root())
		if !ok {
			t.Fatal("no select")
		}
		if sel.Value() != "5000000" {
			t.Errorf("value %q", sel.Value())
		}
		sel.SetValue("10000000")
		sel.OnChange("10000000")
		if c, ok := p.Calls.Last("SetDownloadLimit"); !ok || c.Args[0] != int64(7) || c.Args[1] != int64(10*MB) {
			t.Fatalf("limit %v", p.Calls.List)
		}
		sel.SetValue("-1")
		sel.OnChange("-1")
		if c, ok := p.Calls.Last("OpenProgress"); !ok || c.Args[0] != int64(7) {
			t.Fatalf("progress %v", p.Calls.List)
		}
		// Custom… is an action, not a value: the select keeps the download's limit
		if sel.Value() != "5000000" {
			t.Errorf("value after Custom… %q", sel.Value())
		}
		if count(p.Calls, "SetDownloadLimit") != 1 {
			t.Error(p.Calls.List)
		}
	})

	t.Run("is text only for a completed download", func(t *testing.T) {
		p := showPane(t, Download(8, func(d *downexv1.Download) { d.State, d.Received, d.SpeedLimit = state.StateCompleted, 10*MiB, MB }), nil)
		if _, ok := find[*widgets.Select](p.root()); ok {
			t.Error("a select")
		}
		if got, _ := p.fact("Speed limit"); got != "1.0 MB/s" {
			t.Errorf("limit %q", got)
		}
	})

	t.Run("shows when a finished download finished instead of an empty speed chart", func(t *testing.T) {
		completedAt := time.Date(2026, 10, 25, 18, 31, 0, 0, time.Local).UnixMilli()
		p := showPane(t, Download(9, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.ActiveMs, d.CompletedAt = state.StateCompleted, 10*MiB, 10*MiB, 4000, Millis(completedAt)
		}), nil)
		if c, ok := find[*speedChart](p.root()); ok {
			t.Error("a chart", c)
		}
		// "Finished 25 Oct, 18:31" on the locale's own date and clock, without seconds
		if !strings.Contains(p.text(), "Finished "+display.DayTimeText(float64(completedAt), p.dl.grid.table.nowMS())) {
			t.Errorf("text %q", p.text())
		}
		if !strings.HasPrefix(display.DayTimeText(float64(completedAt), float64(completedAt+3_600_000)), "Today, ") {
			t.Error("today")
		}
		if strings.HasSuffix(display.DayTimeText(float64(completedAt), p.dl.grid.table.nowMS()), ":00") {
			t.Error("seconds")
		}
	})

	t.Run("says what a stopped download did last instead of an empty chart, with its limit", func(t *testing.T) {
		updatedAt := time.Date(2026, 10, 25, 9, 5, 0, 0, time.Local).UnixMilli()
		failed := showPane(t, Download(11, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.UpdatedAt, d.Error = state.StateFailed, 100*MiB, 18*MiB, Millis(updatedAt), "HTTP 503"
		}), nil)
		if _, ok := find[*speedChart](failed.root()); ok {
			t.Error("a chart")
		}
		if !strings.Contains(failed.text(), "Stopped "+display.DayTimeText(float64(updatedAt), failed.dl.grid.table.nowMS())) {
			t.Errorf("text %q", failed.text())
		}
		if !strings.Contains(failed.text(), "Limit none") {
			t.Error("Limit none")
		}
		paused := showPane(t, Download(12, func(d *downexv1.Download) { d.State, d.Size, d.Received = state.StatePaused, 1300*MiB, 252*MiB }), nil)
		if !strings.Contains(paused.text(), "Paused · 264 MB of 1.4 GB") {
			t.Errorf("text %q", paused.text())
		}
		queued := showPane(t, Download(13, func(d *downexv1.Download) { d.State, d.QueuePosition = state.StateQueued, 2 }), nil)
		if !strings.Contains(queued.text(), "Queued #2") {
			t.Errorf("text %q", queued.text())
		}
	})

	t.Run("lets the keyboard scroll the body when it overflows", func(t *testing.T) {
		p := showPane(t, Download(14, func(d *downexv1.Download) { d.State, d.Size, d.Received = state.StatePaused, 10*MiB, MiB }), nil)
		sp, ok := find[*widgets.ScrollPane](p.root())
		if !ok {
			t.Fatal("no body")
		}
		if !sp.Focusable || sp.Semantics.Name != "Download details" {
			t.Errorf("focusable %v name %q", sp.Focusable, sp.Semantics.Name)
		}
	})

	t.Run("keeps the speed chart of a download that finished within the last minute", func(t *testing.T) {
		d := Download(10, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.SpeedBps = state.StateDownloading, 10*MiB, 5*MiB, 2*MiB
		})
		p := showPane(t, d, nil)
		p.Advance(2 * time.Second)
		run := Download(10, func(x *downexv1.Download) {
			x.State, x.Size, x.Received, x.SpeedBps, x.UpdatedAt = state.StateDownloading, 10*MiB, 8*MiB, 3*MiB, Millis(2_000_000_000_000)
		})
		p.daemon.Emit(statetest.Updated(run))
		done := Download(10, func(x *downexv1.Download) {
			x.State, x.Size, x.Received, x.UpdatedAt = state.StateCompleted, 10*MiB, 10*MiB, Millis(2_000_000_001_000)
			x.CompletedAt = Millis(p.StoreClock.Now().UnixMilli())
		})
		p.daemon.Emit(statetest.Updated(done))
		p.refresh()
		c, ok := find[*speedChart](p.root())
		if !ok || !strings.HasPrefix(c.plot.Semantics.Name, "Speed over the last minute") {
			t.Fatal("no chart")
		}
	})
}

// ---- DetailsPanel.test.ts: SpeedChart's y-axis and drawing ------------------------------------------------

func TestSpeedChart(t *testing.T) {
	t.Run("rounds the top up to 1, 2 or 5 of a unit (decimal: KB/s, MB/s) and labels half of it", func(t *testing.T) {
		for _, c := range []struct {
			peak float64
			want [3]string
		}{
			{5.9 * MB, [3]string{"10 MB/s", "5 MB/s", "0"}},
			{5.9 * MiB, [3]string{"10 MB/s", "5 MB/s", "0"}}, // 5.9 MiB/s = 6.2 MB/s
			{11.2 * MB, [3]string{"20 MB/s", "10 MB/s", "0"}},
			{1.4 * MB, [3]string{"2 MB/s", "1 MB/s", "0"}},
			{931 * 1000, [3]string{"1 MB/s", "0.5 MB/s", "0"}},
			{999_999, [3]string{"1 MB/s", "0.5 MB/s", "0"}},
			{300 * 1000, [3]string{"500 KB/s", "250 KB/s", "0"}},
			{0, [3]string{"1 MB/s", "0.5 MB/s", "0"}},
		} {
			if got := display.SpeedAxisFor(c.peak).Labels; got != c.want {
				t.Errorf("speedAxis(%v) = %v, want %v", c.peak, got, c.want)
			}
		}
		if m := display.SpeedAxisFor(5 * MB).Max; m != 5*MB {
			t.Errorf("max %v", m)
		}
	})

	t.Run("draws the last minute with its axis labels, average and peak", func(t *testing.T) {
		const t0 = 1_800_000_000_000
		samples := []display.SpeedSample{
			{T: t0 - 90_000, V: 50 * MB}, // older than a minute: left out
			{T: t0 - 40_000, V: 4 * MB},
			{T: t0 - 20_000, V: 6 * MB},
			{T: t0, V: 5 * MB},
		}
		st := display.SpeedStatsOf(samples)
		if st.Peak != 6*MB || st.Avg != 5*MB {
			t.Fatal(st)
		}
		r, _ := fakeRig(t)
		c := newSpeedChart(56)
		r.Show(c)
		c.set(samples, 56)
		r.Settle()
		var ticks []string
		for _, l := range c.axis.ticks {
			ticks = append(ticks, l.Text())
		}
		if !slices.Equal(ticks, []string{"10 MB/s", "5 MB/s", "0"}) {
			t.Error(ticks)
		}
		if !c.hasPaths() || !c.plot.none.Style.Hidden {
			t.Error("no line")
		}
		if c.plot.Semantics.Name != "Speed over the last minute: now 5.0 MB/s, average 5.0 MB/s, peak 6.0 MB/s" {
			t.Errorf("label %q", c.plot.Semantics.Name)
		}
	})

	t.Run("says so when there is nothing to draw", func(t *testing.T) {
		c := newSpeedChart(56)
		if c.hasPaths() || c.plot.none.Style.Hidden || c.plot.none.Text() != "No speed in the last minute" {
			t.Error("no message")
		}
	})
}

// ---- DetailsPanel.test.ts: display: row and pane texts --------------------------------------------------

func TestDisplayTexts(t *testing.T) {
	t.Run("names the host without www and the category, or the kind", func(t *testing.T) {
		for _, c := range []struct {
			d    *downexv1.Download
			want string
		}{
			{Download(1, func(d *downexv1.Download) { d.Url = "https://www.example.com/a.zip" }), "example.com"},
			{Download(1, func(d *downexv1.Download) { d.Url, d.FinalUrl = "https://a.example/x", "https://cdn.example.net/x" }), "cdn.example.net"},
			{SiteDownload(1, nil), "youtube.com"},
			{Download(1, func(d *downexv1.Download) { d.Url = "not a url" }), ""},
		} {
			if got := display.RowHost(c.d); got != c.want {
				t.Errorf("rowHost %q, want %q", got, c.want)
			}
		}
		if s := display.RowSubline(Download(1, func(d *downexv1.Download) { d.Url, d.Filename = "https://files.example.com/plan.xlsx", "plan.xlsx" })); s != "files.example.com · Documents" {
			t.Error(s)
		}
		if s := display.RowSubline(Download(1, func(d *downexv1.Download) { d.Url, d.Category = "https://files.example.com/x.bin", "Books" })); s != "files.example.com · Books" {
			t.Error(s)
		}
	})

	t.Run("says the unit once where both numbers share it", func(t *testing.T) {
		for _, c := range []struct {
			recv, size float64
			approx     bool
			want       string
		}{
			{1.43 * GB, 3.4 * GB, false, "1.4 of 3.4 GB"}, {900 * MB, 3.4 * GB, false, "900 MB of 3.4 GB"},
			{700 * MB, 2100 * MB, true, "700 MB of ~2.1 GB"}, {1_429_658_896, 3.4 * GB, false, "1.4 of 3.4 GB"},
			{5 * MB, -1, false, "5.0 MB"},
		} {
			if got := display.ReceivedOf(c.recv, c.size, c.approx); got != c.want {
				t.Errorf("receivedOf %q, want %q", got, c.want)
			}
		}
	})

	t.Run("puts the state and its progress in the pill", func(t *testing.T) {
		for _, c := range []struct {
			d    *downexv1.Download
			want display.Pill
		}{
			{Download(1, func(d *downexv1.Download) { d.State, d.Received = state.StateDownloading, 4404020 }) /* 4.2 MiB, rounded up to whole bytes */, display.Pill{Tone: display.ToneOK, Text: "Downloading · 42%"}},
			{Download(1, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MiB }), display.Pill{Tone: display.ToneWarn, Text: "Paused · 10%"}},
			{Download(1, func(d *downexv1.Download) { d.State, d.ErrorCode = state.StateFailed, "validator_changed" }), display.Pill{Tone: display.ToneFail, Text: "Needs restart"}},
			{Download(1, func(d *downexv1.Download) { d.State = state.StateAwaiting }), display.Pill{Tone: display.ToneInfo, Text: "Waiting for confirmation"}},
			{Download(1, func(d *downexv1.Download) { d.State = state.StateCompleted }), display.Pill{Tone: display.ToneDone, Text: "Completed"}},
		} {
			if got := display.StatePill(c.d); got != c.want {
				t.Errorf("statePill %v, want %v", got, c.want)
			}
		}
	})

	t.Run("sums the selection’s known sizes", func(t *testing.T) {
		for _, c := range []struct {
			ds   []*downexv1.Download
			want string
		}{
			{[]*downexv1.Download{Download(1, nil), Download(2, nil)}, "2 downloads selected · 21.0 MB"},
			{[]*downexv1.Download{Download(1, func(d *downexv1.Download) { d.Size = -1 }), Download(2, func(d *downexv1.Download) { d.Size = -1 })}, "2 downloads selected"},
			{[]*downexv1.Download{Download(1, nil), Download(2, func(d *downexv1.Download) { d.Size = -1 })}, "2 downloads selected · ~10.5 MB"},
		} {
			if got := display.SelectionSummary(c.ds); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
		}
		if s := format.Clock(0); s != "—" {
			t.Errorf("formatClock(0) %q", s)
		}
	})
}
