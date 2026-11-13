package progress

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
)

// The vitest cases of ProgressWindow.test.ts, each a subtest named as its it().

const longURL = "https://downloads.example.com/releases/2026/very/long/path/to/the/file/distro-installer.iso"

func conn(index int32, edit func(c *downexv1.ConnectionInfo)) *downexv1.ConnectionInfo {
	return formattest.Connection(index, edit)
}

// running is the test's running(): a 100 MiB ranged file a quarter done on two connections,
// the second waiting to retry.
func running(edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.Download(7, func(d *downexv1.Download) {
		d.Url = longURL
		d.Filename = "distro-installer.iso"
		d.Dir = "/home/you/Downloads/Archives"
		d.State = state.StateDownloading
		d.Mode = downexv1.Mode_MODE_RANGED
		d.Size = 100 * MiB
		d.Received = 25 * MiB
		d.SpeedBps = 5 * MiB
		d.Connections = 2
		d.Segments = []*downexv1.Segment{{Start: 0, End: 50*MiB - 1, Received: 15 * MiB}, {Start: 50 * MiB, End: 100*MiB - 1, Received: 10 * MiB}}
		d.ConnectionInfo = []*downexv1.ConnectionInfo{
			conn(1, func(c *downexv1.ConnectionInfo) { c.Start, c.End, c.Received, c.SpeedBps = 0, 50*MiB-1, 15*MiB, 3*MiB }),
			conn(2, func(c *downexv1.ConnectionInfo) {
				c.State, c.Start, c.End, c.Received, c.Detail = downexv1.ConnectionState_CONNECTION_STATE_RETRYING, 50*MiB, 100*MiB-1, 10*MiB, "HTTP 503, retrying in 4 s"
			}),
		}
		if edit != nil {
			edit(d)
		}
	})
}

// doneBy is "15s · Done ≈ 11:59": the time left and when it should be done.
func doneBy(left string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(left) + ` · Done ≈ \d{1,2}:\d{2}`)
}

func show(t *testing.T, d *downexv1.Download) *rig { return newRig(t, d, nil, rigOptions{}) }

func want(t *testing.T, what, got, wanted string) {
	t.Helper()
	if got != wanted {
		t.Fatalf("%s = %q, want %q", what, got, wanted)
	}
}

func wantMatch(t *testing.T, what, got string, re *regexp.Regexp) {
	t.Helper()
	if !re.MatchString(got) {
		t.Fatalf("%s = %q, want %v", what, got, re)
	}
}

func TestProgressWindowStatus(t *testing.T) {
	t.Run(`shows the live state of its download and titles the window "<percent>% <name>"`, func(t *testing.T) {
		r := show(t, running(nil))
		want(t, "state", r.stateLine(), "Receiving data…")
		want(t, "title", r.api.title, "25% distro-installer.iso")
		want(t, "File size", r.mustRow("File size"), "105 MB")
		want(t, "Downloaded", r.mustRow("Downloaded"), "26.2 MB (25%)")
		want(t, "Transfer rate", r.mustRow("Transfer rate"), "5.2 MB/s")
		wantMatch(t, "Time left", r.mustRow("Time left"), doneBy("15s"))
		want(t, "Resumable", r.mustRow("Resumable"), "Yes")
		// The address keeps its end in view (middle ellipsis) and can be copied.
		want(t, "Address", r.mustRow("Address"), longURL)
		if v := r.s().bar.Semantics.Range; !v.Valid || v.Value != 25 {
			t.Fatal("progressbar value", v)
		}
		if !slices.Contains(r.api.Calls(), "get") {
			t.Fatal(r.api.Calls())
		}
		r.update(7, func(d *downexv1.Download) { d.Received = 60 * MiB })
		want(t, "title", r.api.title, "60% distro-installer.iso")
		want(t, "Downloaded", r.mustRow("Downloaded"), "62.9 MB (60%)")
		// Another download's events do not matter here.
		r.api.emit(statetest.Updated(formattest.Download(8, func(d *downexv1.Download) { d.State = state.StateCompleted })))
		r.api.emit(statetest.Removed(8))
		r.settle()
		if r.api.closed != 0 {
			t.Fatal("closed")
		}
		want(t, "Downloaded", r.mustRow("Downloaded"), "62.9 MB (60%)")
	})

	t.Run("shows the average speed of the whole download beside the live rate, once it has run a few seconds", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) { d.ActiveMs = 1500 }))
		// its first seconds: no average yet
		want(t, "rate", r.mustRow("Transfer rate"), "5.2 MB/s")
		r.update(7, func(d *downexv1.Download) { d.Received, d.ActiveMs = 40*MiB, 5000 })
		want(t, "rate", r.mustRow("Transfer rate"), "5.2 MB/s · avg 8.4 MB/s")
		// cut in a narrow window with larger text: whole in its tooltip
		want(t, "title", r.s().statusV.rate.Parent().Tooltip, "5.2 MB/s · avg 8.4 MB/s")
		r.update(7, func(d *downexv1.Download) { d.State, d.SpeedBps = state.StatePaused, 0 })
		want(t, "rate", r.mustRow("Transfer rate"), "—")
	})

	t.Run("shows only the name while the size is unknown, with an indeterminate bar", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) {
			d.Size, d.Mode, d.Segments, d.ConnectionInfo = -1, downexv1.Mode_MODE_SINGLE, nil, nil
		}))
		want(t, "state", r.stateLine(), "Receiving data…")
		want(t, "title", r.api.title, "distro-installer.iso")
		want(t, "File size", r.mustRow("File size"), "Unknown")
		want(t, "Resumable", r.mustRow("Resumable"), "No")
		want(t, "valuetext", r.s().bar.Semantics.Value, "Size unknown")
	})

	t.Run("pauses and resumes; Cancel pauses and closes the window", func(t *testing.T) {
		r := show(t, running(nil))
		r.click(r.s().actions.pause)
		want(t, "state", r.stateLine(), "Paused")
		if !slices.Contains(r.api.Calls(), "pause") {
			t.Fatal(r.api.Calls())
		}
		r.click(r.s().actions.resume)
		want(t, "state", r.stateLine(), "Waiting in queue")
		if a := r.api.lastArgs("resume"); len(a) != 2 || a[0] != int64(7) || a[1] != (resumeOpts{}) {
			t.Fatal(a)
		}
		want(t, "cancel", r.s().actions.cancel.Label().Text(), "Cancel")
		r.click(r.s().actions.cancel)
		if r.api.closed != 1 || r.api.count("pause") != 2 || r.api.item(7).GetState() != state.StatePaused {
			t.Fatal(r.api.closed, r.api.Calls())
		}
	})

	t.Run("closes without pausing what is not running", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) { d.State, d.SpeedBps = state.StatePaused, 0 }))
		want(t, "button", r.s().actions.cancel.Label().Text(), "Close")
		r.click(r.s().actions.cancel)
		if r.api.closed != 1 || slices.Contains(r.api.Calls(), "pause") {
			t.Fatal(r.api.closed, r.api.Calls())
		}
	})

	t.Run("closes when its download is removed", func(t *testing.T) {
		r := show(t, running(nil))
		r.api.emit(statetest.Removed(7))
		r.settle()
		if r.api.closed != 1 {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("says so when the download does not exist", func(t *testing.T) {
		r := show(t, nil)
		want(t, "heading", r.s().message.title.Text(), "This download no longer exists")
		if !visible(&r.s().message.title.Element) {
			t.Fatal("no heading")
		}
		// The host reports a download it cannot watch as removed: the window stays to say why.
		r.api.emit(statetest.Removed(7))
		r.settle()
		if r.api.closed != 0 {
			t.Fatal("closed")
		}
		r.click(r.s().message.actions.Children()[0].Widget())
		if r.api.closed != 1 {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("shows a stream with its fragments, quality and the fragment of each connection", func(t *testing.T) {
		r := show(t, formattest.MediaDownload(7, func(d *downexv1.Download) {
			d.State, d.Received, d.SpeedBps = state.StateDownloading, 40*MiB, 2*MiB
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
			d.ConnectionInfo = []*downexv1.ConnectionInfo{
				conn(1, func(c *downexv1.ConnectionInfo) { c.Fragment, c.Received = 50, 300*KiB }),
				conn(2, func(c *downexv1.ConnectionInfo) {
					c.Fragment, c.State = 51, downexv1.ConnectionState_CONNECTION_STATE_REQUESTING
				}),
			}
		}))
		want(t, "state", r.stateLine(), "Receiving data…")
		want(t, "Manifest", r.mustRow("Manifest"), "https://media.example.org/talk/master.m3u8")
		want(t, "Fragments", r.mustRow("Fragments"), "50 / 200")
		// The human codec text; the picture and the raw codecs are its tooltip.
		want(t, "Quality", r.mustRow("Quality"), "1080p · H.264 · AAC 128 kbit/s")
		wantMatch(t, "Quality title", r.s().statusV.quality.Tooltip, regexp.MustCompile(`1920×1080.*English`))
		r.click(r.s().actions.toggle)
		infos := r.infos()
		for _, w := range []string{"Fragment 51 · Receiving data…", "Fragment 52 · Sending request…"} {
			if !slices.Contains(infos, w) {
				t.Fatal(infos)
			}
		}
		if !visible(&r.s().details.frags.Element) || r.s().details.frags.Label != "Fragments downloaded" {
			t.Fatal("no fragments bar", r.s().details.frags.Label)
		}
	})

	t.Run(`shows a stream’s estimated size with "~" and the time left from it, instead of "Unknown"`, func(t *testing.T) {
		r := show(t, formattest.MediaDownload(7, func(d *downexv1.Download) {
			d.State, d.Size, d.SizeApprox, d.Received, d.SpeedBps = state.StateDownloading, 2100*MiB, true, 700*MiB, 7*MiB
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
		}))
		want(t, "File size", r.mustRow("File size"), "~2.2 GB")
		want(t, "Downloaded", r.mustRow("Downloaded"), "734 MB (25%)")
		wantMatch(t, "Time left", r.mustRow("Time left"), doneBy("3m 20s"))
		// The header's percentage says it is an estimate.
		want(t, "figure", r.figure(), "~25%")
		r.update(7, func(d *downexv1.Download) {
			d.Size, d.Received = 2000*MiB, 1000*MiB
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 100 })
		})
		want(t, "File size", r.mustRow("File size"), "~2.1 GB")
		wantMatch(t, "Time left", r.mustRow("Time left"), doneBy("2m 23s"))
	})

	t.Run("reports an unreachable downexd and reads the download again when it is back", func(t *testing.T) {
		r := newRig(t, running(nil), nil, rigOptions{statusEvery: time.Second})
		want(t, "state", r.stateLine(), "Receiving data…")
		r.api.setDown(true)
		r.advance(time.Second)
		if !visible(&r.s().banner.Element) || !strings.Contains(textOf(&r.s().banner.Element), "downexd is not reachable") {
			t.Fatal("no banner")
		}
		calls := len(r.api.Calls())
		r.click(r.s().actions.pause)
		for _, c := range r.api.Calls()[calls:] {
			if c != "status" {
				t.Fatal(r.api.Calls()[calls:])
			}
		}
		r.api.setDown(false)
		d := r.api.item(7)
		r.api.setItem(running(func(x *downexv1.Download) {
			x.Received = 80 * MiB
			x.UpdatedAt = formattest.Millis(d.GetUpdatedAt().AsTime().UnixMilli() + 5000)
		}))
		r.advance(time.Second)
		if visible(&r.s().banner.Element) {
			t.Fatal("banner stays")
		}
		want(t, "Downloaded", r.mustRow("Downloaded"), "83.9 MB (80%)")
	})
}

// infos are the Info texts of the table's rows.
func (r *rig) infos() []string {
	var out []string
	tbl := r.s().details.table
	for _, row := range tbl.rows[:tbl.shown] {
		out = append(out, row.cells[3].Text())
	}
	return out
}

// tableRows are the table's rows' texts, the header first (cells joined by spaces).
func (r *rig) tableRows() []string {
	tbl := r.s().details.table
	var out []string
	h := make([]string, 4)
	for i, c := range tbl.head.cells {
		h[i] = c.Text()
	}
	out = append(out, strings.Join(h, " "))
	for _, row := range tbl.rows[:tbl.shown] {
		cs := make([]string, 4)
		for i, c := range row.cells {
			cs[i] = c.Text()
		}
		out = append(out, strings.Join(cs, " "))
	}
	if tbl.shown == 0 {
		out = append(out, tbl.empty.Text())
	}
	return out
}

func TestProgressWindowHeader(t *testing.T) {
	t.Run("shows the file tile, the name, the state and the big percentage in the state colours", func(t *testing.T) {
		r := show(t, running(nil))
		h := r.s().head
		if h.tile.kind != icons.TileArchives { // an .iso
			t.Fatal(h.tile.kind)
		}
		want(t, "name", h.name.Text(), "distro-installer.iso")
		want(t, "state", r.stateLine(), "Receiving data…")
		want(t, "figure", r.figure(), "25%")
		if h.figure.Ink(r.pw.App.Tokens()) != views.InkInk(r.pw.App.Tokens()) {
			t.Fatal("figure not --ink")
		}
		// The bar is the segmented one (decorative); the progressbar around it carries the value.
		if !r.s().bar.seg.Semantics.Hidden || r.s().bar.Semantics.Role != kit.RoleProgressBar || r.s().bar.Semantics.Name != "Download progress" {
			t.Fatal("bar semantics")
		}
		r.update(7, func(d *downexv1.Download) { d.State, d.SpeedBps = state.StatePaused, 0 })
		want(t, "state", r.stateLine(), "Paused")
		if h.figure.Ink(r.pw.App.Tokens()) != r.pw.App.Tokens().Warn {
			t.Fatal("figure not warn")
		}
		// A paused download has no time left, so nothing says when it is done.
		want(t, "Time left", r.mustRow("Time left"), "—")
		r.update(7, func(d *downexv1.Download) {
			d.State, d.Error, d.ErrorCode = state.StateFailed, "HTTP 503 Service Unavailable", "http_status"
		})
		want(t, "state", r.stateLine(), "Failed: HTTP 503 Service Unavailable")
		if h.figure.Ink(r.pw.App.Tokens()) != r.pw.App.Tokens().Danger {
			t.Fatal("figure not danger")
		}
		// Unknown size: no percentage at all.
		r.update(7, func(d *downexv1.Download) {
			d.State, d.Size, d.Mode, d.Segments = state.StateDownloading, -1, downexv1.Mode_MODE_SINGLE, nil
		})
		if r.figure() != "" {
			t.Fatal(r.figure())
		}
	})

	t.Run("names a video download by its tile and says when a running download is done", func(t *testing.T) {
		r := show(t, formattest.MediaDownload(7, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.SpeedBps = state.StateDownloading, 400*MiB, 100*MiB, 10*MiB
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
		}))
		if r.s().head.tile.kind != icons.TileVideo {
			t.Fatal(r.s().head.tile.kind)
		}
		wantMatch(t, "Time left", r.mustRow("Time left"), doneBy("30s"))
	})

	t.Run("marks the Speed limit tab while a limit is on, and the tabs control one panel", func(t *testing.T) {
		r := show(t, running(nil))
		limits := r.tab(tabLimits)
		// the Mint dot after the label (drawn, not read out)
		if visible(r.s().limitsDot) {
			t.Fatal("dot shown")
		}
		if limits.Base().Semantics.Controls != r.s().panel {
			t.Fatal("aria-controls")
		}
		r.update(7, func(d *downexv1.Download) { d.SpeedLimit = 512 * KiB })
		if !visible(r.s().limitsDot) || !r.s().limitsDot.Semantics.Hidden {
			t.Fatal("no dot")
		}
		// the name stays "Speed limit"
		want(t, "tab", textOf(limits.Base()), "Speed limit")
	})

	t.Run("chooses the speed limit unit from the keyboard", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) { d.SpeedLimit = 300 * KB }))
		r.click(r.tab(tabLimits))
		unit := r.s().limitsV.unit
		want(t, "unit", unit.Value(), "KB/s")
		r.focus(unit)
		r.key(kit.KeyArrowDown, 0)
		if !unit.IsOpen() {
			t.Fatal("not expanded")
		}
		r.key(kit.KeyArrowDown, 0)
		r.key(kit.KeyEnter, 0)
		want(t, "unit", unit.Value(), "MB/s")
		r.advance(100 * time.Millisecond)
		wantArgs(t, r, "{speedLimit: 300000000}")
	})
}

// wantArgs checks the last SetDownloadOptions call's options.
func wantArgs(t *testing.T, r *rig, opts string) {
	t.Helper()
	a := r.api.lastArgs("setDownloadOptions")
	if len(a) != 2 || a[0] != int64(7) || a[1] != opts {
		t.Fatalf("setDownloadOptions %v, want [7 %s]", a, opts)
	}
}

func TestProgressWindowSettings(t *testing.T) {
	placeholder := func(r *rig) string { return r.s().limitsV.conns.InputOptions().Placeholder }

	t.Run("reads the settings that failed at first, and again when downexd is back", func(t *testing.T) {
		failures := 1
		r := newRig(t, running(nil), nil, rigOptions{statusEvery: time.Second, setup: func(a *fakeAPI) {
			a.getSettings = func() (*downexv1.Settings, error) {
				if failures > 0 {
					failures--
					return nil, errors.New(refused)
				}
				a.mu.Lock()
				defer a.mu.Unlock()
				return a.settings, nil
			}
		}})
		r.click(r.tab(tabLimits))
		want(t, "placeholder", placeholder(r), "Default")
		r.advance(time.Second)
		want(t, "placeholder", placeholder(r), "Default (8)")
		r.api.setDown(true)
		r.advance(time.Second)
		if !visible(&r.s().banner.Element) {
			t.Fatal("no banner")
		}
		s := settings()
		s.DefaultMaxConnections = 4
		r.api.setSettings(s)
		r.api.setDown(false)
		r.advance(time.Second)
		if visible(&r.s().banner.Element) {
			t.Fatal("banner")
		}
		want(t, "placeholder", placeholder(r), "Default (4)")
	})

	t.Run("reads the settings again when the window gets the focus", func(t *testing.T) {
		r := show(t, running(nil))
		r.click(r.tab(tabLimits))
		want(t, "placeholder", placeholder(r), "Default (8)")
		s := settings()
		s.DefaultMaxConnections = 12
		r.api.setSettings(s)
		r.loop.Send(kit.FocusEvent{In: true})
		r.settle()
		want(t, "placeholder", placeholder(r), "Default (12)")
	})
}

func TestProgressWindowDetails(t *testing.T) {
	t.Run("shows and hides the connections, resizing the window and remembering the choice", func(t *testing.T) {
		r := show(t, running(nil))
		r.click(r.s().actions.toggle)
		if s := r.lastSize(); s != (kit.Size{W: 520, H: 560}) {
			t.Fatal(s)
		}
		if v, _, _ := r.storage.Get(state.ProgressDetailsKey); v != "1" {
			t.Fatal(v)
		}
		if !visible(&r.s().details.title.Element) || r.s().details.title.Semantics.Name != "Start positions and progress by connection" {
			t.Fatal("no heading")
		}
		// Each connection with its own speed (a waiting one has none).
		if rows := r.tableRows(); !slices.Equal(rows, []string{
			"N. Downloaded Speed Info",
			"1 15.7 MB 3.1 MB/s Receiving data…",
			"2 10.5 MB — Retrying in 4 s (HTTP 503)",
		}) {
			t.Fatal(rows)
		}
		wantMatch(t, "map", r.s().details.smap.Semantics.Name, regexp.MustCompile(`Segment map: 2 segments, 2 active, 25% received`))
		// Connections come and go with the updates.
		r.update(7, func(d *downexv1.Download) {
			d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(1, func(c *downexv1.ConnectionInfo) {
				c.State, c.Received = downexv1.ConnectionState_CONNECTION_STATE_DONE, 50*MiB
			})}
		})
		if !slices.Contains(r.infos(), "Finished") {
			t.Fatal(r.infos())
		}
		r.click(r.s().actions.toggle)
		// jsdom's mocked layout fits the usual 300 px; here the facts' own height decides,
		// never below it.
		if s := r.lastSize(); s != (kit.Size{W: 520, H: r.fittedCollapsed()}) {
			t.Fatal(s, r.fittedCollapsed())
		}
		if visible(&r.s().details.Element) {
			t.Fatal("table shown")
		}
		if v, _, _ := r.storage.Get(state.ProgressDetailsKey); v != "0" {
			t.Fatal(v)
		}
	})

	t.Run("opens with the details when they were shown last time, keeping a wider window wide", func(t *testing.T) {
		r := newRig(t, running(nil), nil, rigOptions{storage: map[string]string{state.ProgressDetailsKey: "1"}, size: kit.Size{W: 700, H: 261}})
		if s := r.sizes(); !slices.Equal(s, []kit.Size{{W: 700, H: 560}}) {
			t.Fatal(s)
		}
		if !visible(&r.s().details.table.Element) {
			t.Fatal("no table")
		}
		want(t, "toggle", r.s().actions.toggle.Label().Text(), "Hide details")
		if r.s().actions.toggle.Semantics.Expand != kit.ExpandOpen {
			t.Fatal("aria-expanded")
		}
	})

	t.Run("shows no details for a download that does not exist or is complete, whatever was remembered", func(t *testing.T) {
		missing := newRig(t, nil, nil, rigOptions{storage: map[string]string{state.ProgressDetailsKey: "1"}})
		want(t, "heading", missing.s().message.title.Text(), "This download no longer exists")
		done := newRig(t, running(func(d *downexv1.Download) {
			d.Id, d.State, d.Received = 8, state.StateCompleted, 100*MiB
		}), nil, rigOptions{storage: map[string]string{state.ProgressDetailsKey: "1"}})
		want(t, "heading", completeTitle, "Download complete")
		if !done.s().complete {
			t.Fatal("not the complete view")
		}
		if len(missing.sizes()) != 0 {
			t.Fatal(missing.sizes())
		}
		// Sized to the dialog (jsdom: its default height, as it has no layout; here the
		// height its content needs), never to the details.
		s := done.sizes()
		if len(s) != 1 || s[0].W != 520 || s[0].H == 560 || s[0].H != done.fittedComplete() {
			t.Fatal(s, done.fittedComplete())
		}
		if visible(&done.s().details.Element) {
			t.Fatal("table")
		}
	})

	t.Run("counts a retry wait down between reports and starts again from a new report", func(t *testing.T) {
		r := newRig(t, running(nil), nil, rigOptions{storage: map[string]string{state.ProgressDetailsKey: "1"}})
		info := func(i int) string { return r.infos()[i] }
		want(t, "info", info(1), "Retrying in 4 s (HTTP 503)")
		r.advance(2 * time.Second)
		want(t, "info", info(1), "Retrying in 2 s (HTTP 503)")
		r.advance(5 * time.Second)
		want(t, "info", info(1), "Retrying in 0 s (HTTP 503)")
		r.update(7, func(d *downexv1.Download) {
			d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(2, func(c *downexv1.ConnectionInfo) {
				c.State, c.Received, c.Detail = downexv1.ConnectionState_CONNECTION_STATE_RETRYING, 10*MiB, "HTTP 503, retrying in 8 s"
			})}
		})
		want(t, "info", info(0), "Retrying in 8 s (HTTP 503)")
	})

	t.Run("keeps the connections of the last update when an answer in between has none", func(t *testing.T) {
		r := show(t, running(nil))
		r.click(r.s().actions.toggle)
		// Only Watch updates carry connections; SetDownloadOptions answers without them.
		d := running(func(x *downexv1.Download) { x.ConnectionInfo = nil })
		d.UpdatedAt = r.api.item(7).GetUpdatedAt()
		r.api.setItem(d)
		r.click(r.tab(tabLimits))
		r.click(r.s().limitsV.toggle.sw)
		if !slices.Contains(r.api.Calls(), "setDownloadOptions") {
			t.Fatal(r.api.Calls())
		}
		if n := len(r.tableRows()); n != 3 {
			t.Fatal(r.tableRows())
		}
		// An update without connections empties the table.
		r.update(7, func(d *downexv1.Download) { d.ConnectionInfo = nil })
		if !slices.Contains(r.tableRows(), "No connections yet.") {
			t.Fatal(r.tableRows())
		}
	})
}

func TestProgressWindowSpeedLimit(t *testing.T) {
	t.Run("applies the speed limit at once and shows it as active", func(t *testing.T) {
		r := show(t, running(nil))
		r.click(r.tab(tabLimits))
		lv := r.s().limitsV
		r.click(lv.toggle.sw)
		wantArgs(t, r, "{speedLimit: 500000}")
		r.typeIn(lv.value, "2")
		r.chooseUnit("MB/s")
		wantArgs(t, r, "{speedLimit: 2000000}")
		r.typeIn(lv.value, "fast")
		r.focus(lv.value)
		r.key(kit.KeyEnter, 0)
		if !visible(&lv.limitError.Element) || !strings.Contains(lv.limitError.Text(), "Enter a number") {
			t.Fatal(lv.limitError.Text())
		}
		r.typeIn(lv.value, "1.5")
		r.key(kit.KeyEnter, 0)
		wantArgs(t, r, "{speedLimit: 1500000}")
		r.click(r.tab(tabStatus))
		want(t, "Speed limit", r.mustRow("Speed limit"), "1.5 MB/s for this download")
		r.click(r.tab(tabLimits))
		r.click(r.s().limitsV.toggle.sw)
		wantArgs(t, r, "{speedLimit: 0}")
		r.click(r.tab(tabStatus))
		if _, ok := r.row("Speed limit"); ok {
			t.Fatal("a Speed limit row")
		}
	})

	t.Run("applies typed values after a pause, and the connection limit", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) { d.SpeedLimit, d.MaxConnections = 256*KB, 4 }))
		r.click(r.tab(tabLimits))
		lv := r.s().limitsV
		if !lv.toggle.sw.On {
			t.Fatal("not checked")
		}
		want(t, "value", lv.value.Text(), "256")
		r.typeIn(lv.value, "300")
		// Only the window's mark for the complete dialog has been sent so far.
		if r.api.count("setDownloadOptions") != 1 {
			t.Fatal(r.api.Calls())
		}
		wantArgs(t, r, "{completionDialog: true}")
		r.advance(60 * time.Millisecond)
		wantArgs(t, r, "{speedLimit: 300000}")
		want(t, "conns", lv.conns.Text(), "4")
		r.focus(lv.conns)
		r.typeIn(lv.conns, "40")
		r.key(kit.KeyEnter, 0)
		if !strings.Contains(lv.connsError.Text(), "from 1 to 32") || !visible(&lv.connsError.Element) {
			t.Fatal(lv.connsError.Text())
		}
		r.typeIn(lv.conns, "16")
		r.key(kit.KeyEnter, 0)
		wantArgs(t, r, "{maxConnections: 16}")
		// Empty = the daemon's default.
		r.typeIn(lv.conns, "")
		r.blur()
		wantArgs(t, r, "{maxConnections: 0}")
		want(t, "placeholder", lv.conns.InputOptions().Placeholder, "Default (8)")
	})

	t.Run("follows limits changed elsewhere while the fields are not being edited", func(t *testing.T) {
		r := show(t, running(nil))
		r.click(r.tab(tabLimits))
		lv := r.s().limitsV
		sent := func() int { return r.api.count("setDownloadOptions") }
		if lv.toggle.sw.On {
			t.Fatal("checked")
		}
		// "downex set 7 --limit 1M -c 6" while the window is open.
		r.update(7, func(d *downexv1.Download) { d.SpeedLimit, d.MaxConnections = MB, 6 })
		if !lv.toggle.sw.On || lv.value.Text() != "1" || lv.unit.Value() != "MB/s" || lv.conns.Text() != "6" {
			t.Fatal(lv.toggle.sw.On, lv.value.Text(), lv.unit.Value(), lv.conns.Text())
		}
		// Unticking removes the limit in force.
		r.click(lv.toggle.sw)
		wantArgs(t, r, "{speedLimit: 0}")
		// A limit set here and removed elsewhere: the fields follow, and leaving them sends nothing.
		r.click(lv.toggle.sw)
		r.focus(lv.value)
		r.typeIn(lv.value, "2")
		r.key(kit.KeyEnter, 0)
		wantArgs(t, r, "{speedLimit: 2000000}")
		before := sent()
		r.blur()
		r.update(7, func(d *downexv1.Download) { d.SpeedLimit, d.MaxConnections = 0, 3 })
		if lv.toggle.sw.On || lv.conns.Text() != "3" {
			t.Fatal(lv.toggle.sw.On, lv.conns.Text())
		}
		r.focus(lv.value)
		r.blur()
		r.focus(lv.conns)
		r.blur()
		r.advance(80 * time.Millisecond)
		if sent() != before {
			t.Fatal(sent(), before, r.api.Calls())
		}
		r.click(r.tab(tabStatus))
		if _, ok := r.row("Speed limit"); ok {
			t.Fatal("a Speed limit row")
		}
	})

	t.Run("keeps a field the user is editing, and shows what the daemon applied", func(t *testing.T) {
		r := newRig(t, running(func(d *downexv1.Download) { d.SpeedLimit, d.MaxConnections = 256*KB, 4 }), nil, rigOptions{setup: func(a *fakeAPI) {
			// Like downexd: 0 connections = its default, which the answer reports.
			a.setOptions = func(req *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
				resp, err := a.applyOptions(req)
				if err != nil {
					return nil, err
				}
				if req.MaxConnections == nil || req.GetMaxConnections() != 0 {
					return resp.GetDownload(), nil
				}
				return a.edit(req.GetId(), func(d *downexv1.Download) { d.MaxConnections = 8 })
			}
		}})
		r.click(r.tab(tabLimits))
		lv := r.s().limitsV
		sent := func() int { return r.api.count("setDownloadOptions") }
		// Typing waits for its timer; a change from elsewhere does not overwrite it.
		r.focus(lv.value)
		r.typeIn(lv.value, "300")
		r.update(7, func(d *downexv1.Download) { d.SpeedLimit = MB })
		want(t, "value", lv.value.Text(), "300")
		r.advance(60 * time.Millisecond)
		wantArgs(t, r, "{speedLimit: 300000}")
		// Nor while the field has the focus without an edit; leaving it shows the limit in force.
		r.update(7, func(d *downexv1.Download) { d.SpeedLimit = 2 * MB })
		want(t, "value", lv.value.Text(), "300")
		before := sent()
		r.blur()
		want(t, "value", lv.value.Text(), "2")
		want(t, "unit", lv.unit.Value(), "MB/s")
		if sent() != before {
			t.Fatal(sent(), before)
		}
		// An empty connections field asks for the default: the field shows what was applied.
		// (The test's keyDown and blur go to the field without focusing it.)
		r.typeIn(lv.conns, "")
		lv.conns.OnEnter()
		r.settle()
		wantArgs(t, r, "{maxConnections: 0}")
		want(t, "conns", lv.conns.Text(), "8")
		lv.conns.onBlur()
		r.settle()
		r.advance(80 * time.Millisecond)
		if sent() != before+1 {
			t.Fatal(sent(), before+1)
		}
	})

	t.Run("switches tabs with the arrow keys", func(t *testing.T) {
		r := show(t, running(nil))
		status := r.tab(tabStatus)
		r.focus(status)
		r.key(kit.KeyArrowRight, 0)
		limits := r.tab(tabLimits)
		if r.s().tabs.Value != tabLimits || r.pw.Kit.Focused() != limits.Base() {
			t.Fatal(r.s().tabs.Value)
		}
		r.key(kit.KeyArrowRight, 0)
		if r.s().tabs.Value != tabStatus {
			t.Fatal(r.s().tabs.Value)
		}
	})
}

// chooseUnit picks a unit with the keyboard (lib/testing's chooseOption).
func (r *rig) chooseUnit(u string) {
	r.t.Helper()
	unit := r.s().limitsV.unit
	r.focus(unit)
	r.key(kit.KeyArrowDown, 0)
	for range 4 {
		if unit.Active() == u {
			break
		}
		r.key(kit.KeyArrowDown, 0)
	}
	r.key(kit.KeyEnter, 0)
	if unit.Value() != u {
		r.t.Fatalf("unit %q, want %q", unit.Value(), u)
	}
}
