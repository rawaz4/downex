package downloads

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/query"
	"downex/ui/core/selection"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// ---- helpers ------------------------------------------------------------------------------------

// fakeRig is DownloadTable.test's table(list): the grid over a fixed list (fakeApi).
func fakeRig(t *testing.T, list ...*downexv1.Download) (*rig, *statetest.Daemon) {
	t.Helper()
	d := statetest.NewDaemon(list...)
	r := newRig(t, viewstest.Options{Daemon: d}, gridBox, 0)
	return r, d
}

// mockRig is DownloadTable.test's setup(count): the generated list (createMockApi with seed 11,
// no ticking).
func mockRig(t *testing.T, count int) *rig {
	t.Helper()
	return newRig(t, viewstest.Options{Scene: func(o *scene.Options) {
		o.Seed, o.Count, o.Scene, o.AutoTick = 11, count, scene.Random, false
	}}, gridBox, 0)
}

// textOf is an element's text content: its labels' texts in tree order, hidden ones left out.
func textOf(e *kit.Element) string {
	var b strings.Builder
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		switch w := e.Widget().(type) {
		case *kit.Label:
			b.WriteString(w.Text())
		case *views.Text:
			b.WriteString(w.Text())
		case *richLine:
			b.WriteString(w.Semantics.Name)
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(e)
	return b.String()
}

func (r *rig) row(id int64) *row {
	w, _ := r.table.RowWidget(kit.ListKey(id)).(*row)
	return w
}

func (r *rig) cell(id int64, key string) *cell {
	w := r.row(id)
	if w == nil {
		return nil
	}
	return w.cells[columnOf(key)]
}

func (r *rig) cellText(id int64, key string) string {
	c := r.cell(id, key)
	if c == nil || c.Style.Hidden {
		return ""
	}
	return strings.TrimSpace(textOf(&c.Element))
}

func (r *rig) node(e *kit.Element) kit.SemanticNode {
	n, _ := r.Window.Semantics().Node(e.NodeID())
	return n
}

func (r *rig) visibleIDs() []int64 { return r.Main.Downloads.VisibleIDs() }

func (r *rig) selectedIDs() []int64 { return views.SelectedIDs(r.Main.Downloads.Selected()) }

func (r *rig) focus() int64 {
	id, _ := r.Main.Downloads.Selection().Focus()
	return id
}

func (r *rig) key(k kit.Key, mods kit.Modifiers) { r.Key(k, mods) }

// press sends a pointer press and release at p.
func (r *rig) press(p kit.Point, button kit.Buttons, mods kit.Modifiers, clicks int) {
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: button, Buttons: button, Mods: mods, Clicks: clicks})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: button, Mods: mods})
	r.Settle()
}

// rowPoint is a point on a row away from its hover actions (its name).
func (r *rig) rowPoint(id int64) kit.Point {
	b := r.row(id).WindowBounds()
	return kit.Pt(b.Min.X+120, b.Min.Y+b.Height()/2)
}

func (r *rig) refresh() {
	r.Settle()
	r.dl.Refresh()
	r.Settle()
}

// ---- DownloadTable.test.ts: DownloadTable ---------------------------------------------------------

func TestDownloadTable(t *testing.T) {
	t.Run("renders only a window of rows for 10,000 downloads", func(t *testing.T) {
		t0 := time.Now()
		r := mockRig(t, 10_000)
		if n := len(r.visibleIDs()); n != 10_000 {
			t.Fatalf("visible %d", n)
		}
		if n := r.node(&r.table.Element); n.Grid.Rows != 10_001 {
			t.Errorf("rowcount %d", n.Grid.Rows)
		}
		first, last := r.table.Realised()
		if last-first <= 0 || last-first > 80 {
			t.Errorf("realised %d", last-first)
		}
		if n := r.node(&r.row(r.visibleIDs()[0]).Element); n.Cell.Row != 1 { // aria-rowindex 2
			t.Errorf("first row's row index %d", n.Cell.Row)
		}
		if d := time.Since(t0); d > 5*time.Second {
			t.Errorf("took %v", d)
		}
	})

	t.Run("sorts from the column headers with aria-sort", func(t *testing.T) {
		r := mockRig(t, 50)
		size := r.table.header.ths[colSize]
		r.Click(&size.btn.Element)
		r.refresh()
		if s := r.Main.Downloads.Sort(); s != (query.SortSpec{Key: query.SortSize, Dir: query.Desc}) {
			t.Fatal(s)
		}
		if n := r.node(&size.Element); n.Sort != kit.SortDescending {
			t.Errorf("aria-sort %v", n.Sort)
		}
	})

	t.Run("supports keyboard selection with an active descendant", func(t *testing.T) {
		r := mockRig(t, 50)
		r.table.FocusList()
		r.refresh()
		ids := r.visibleIDs()
		if r.focus() != ids[0] || r.Main.Downloads.Selection().Len() != 0 {
			t.Fatal("cursor", r.focus(), r.Main.Downloads.Selection().Len())
		}
		r.key(kit.KeyHome, 0)
		if !slices.Equal(r.selectedIDs(), ids[:1]) {
			t.Fatal(r.selectedIDs())
		}
		r.key(kit.KeyArrowDown, kit.ModShift)
		r.key(kit.KeyArrowDown, kit.ModShift)
		r.refresh()
		if !slices.Equal(r.selectedIDs(), ids[:3]) {
			t.Fatal(r.selectedIDs())
		}
		third := r.row(ids[2])
		if r.table.ActiveDescendant() != third.NodeID() {
			t.Error("active descendant is not the third row")
		}
		if n := r.node(&third.Element); n.States&kit.NodeSelected == 0 {
			t.Error("the third row is not selected")
		}
		r.key(kit.KeyEnd, 0)
		if r.focus() != ids[len(ids)-1] {
			t.Error("End")
		}
		r.key(kit.KeyEscape, 0)
		if r.Main.Downloads.Selection().Len() != 0 {
			t.Error("Escape kept the selection")
		}
	})

	t.Run("selects with click, ctrl-click and shift-click", func(t *testing.T) {
		r := mockRig(t, 50)
		ids := r.visibleIDs()
		r.press(r.rowPoint(ids[0]), kit.ButtonLeft, 0, 1)
		r.press(r.rowPoint(ids[2]), kit.ButtonLeft, kit.ModCtrl, 1)
		if !slices.Equal(r.selectedIDs(), []int64{ids[0], ids[2]}) {
			t.Fatal(r.selectedIDs())
		}
		r.press(r.rowPoint(ids[3]), kit.ButtonLeft, kit.ModShift, 1)
		if !slices.Equal(r.selectedIDs(), []int64{ids[2], ids[3]}) {
			t.Fatal(r.selectedIDs())
		}
		r.refresh()
		if n := r.node(&r.row(ids[1]).Element); n.States&kit.NodeSelected != 0 {
			t.Error("row 1 selected")
		}
	})

	t.Run("opens on Enter and double-click, and offers a context menu", func(t *testing.T) {
		r := mockRig(t, 50)
		id := r.visibleIDs()[1]
		r.press(r.rowPoint(id), kit.ButtonLeft, 0, 1)
		r.press(r.rowPoint(id), kit.ButtonLeft, 0, 2)
		if c, ok := r.Calls.Last("OpenDownload"); !ok || c.Args[0] != id {
			t.Fatal("double click:", r.Calls.List)
		}
		at := kit.Pt(r.rowPoint(id).X, r.rowPoint(id).Y)
		r.press(at, kit.ButtonRight, 0, 1)
		if !slices.Equal(r.selectedIDs(), []int64{id}) {
			t.Error(r.selectedIDs())
		}
		if c, ok := r.Calls.Last("OpenMenu"); !ok || c.Args[0] != at {
			t.Errorf("menu at %v", c)
		}
		r.table.FocusList()
		r.key(kit.KeyEnter, 0)
		if n := count(r.Calls, "OpenDownload"); n != 2 {
			t.Errorf("open calls %d", n)
		}
		r.key(kit.KeyF10, kit.ModShift)
		if n := count(r.Calls, "OpenMenu"); n != 2 {
			t.Errorf("menu calls %d", n)
		}
	})

	t.Run("follows live updates from the daemon", func(t *testing.T) {
		r := mockRig(t, 20)
		var running *downexv1.Download
		for _, d := range r.Main.Downloads.All() {
			if d.GetState() == state.StateDownloading {
				running = d
				break
			}
		}
		if running == nil {
			t.Fatal("nothing runs")
		}
		before := running.GetReceived()
		r.Main.Downloads.SortBy(query.SortStatus)
		r.Mock.Tick(1000)
		r.refresh()
		now, _ := r.Main.Downloads.Get(running.GetId())
		if now.GetReceived() <= before {
			t.Fatal("no progress")
		}
		r.table.EnsureVisible(r.Main.Downloads.IndexOf(running.GetId()))
		r.refresh()
		if s := r.cellText(running.GetId(), "progress"); !regexp.MustCompile(`\d+%`).MatchString(s) {
			t.Errorf("progress cell %q", s)
		}
	})
}

func count(c *viewstest.Calls, name string) int {
	n := 0
	for _, x := range c.List {
		if x.Name == name {
			n++
		}
	}
	return n
}

// ---- DownloadTable: screen-reader semantics --------------------------------------------------------

func TestDownloadTableSemantics(t *testing.T) {
	t.Run("gives rows a stable name and keeps live values out of it", func(t *testing.T) {
		r := mockRig(t, 30)
		id := r.visibleIDs()[0]
		row := r.row(id)
		d, _ := r.Main.Downloads.Get(id)
		n := r.node(&row.Element)
		if want := categories.DisplayName(d) + " " + display.StatusText(d); n.Name != want {
			t.Errorf("name %q, want %q", n.Name, want)
		}
		if !regexp.MustCompile(`^Size .+, added .+`).MatchString(n.Description) {
			t.Errorf("description %q", n.Description)
		}
		// progress bars in rows are decorative; the percentage stays readable as cell text
		if !row.bar.Semantics.Hidden {
			t.Error("the bar is exposed")
		}
	})

	t.Run("keeps the sort buttons out of the Tab order", func(t *testing.T) {
		r := mockRig(t, 5)
		if len(r.table.header.ths) != len(display.Columns()) {
			t.Fatal(len(r.table.header.ths))
		}
		for _, x := range r.table.header.ths {
			if x.btn.Focus != kit.FocusNone {
				t.Error(x.col.Key, "is in the Tab order")
			}
		}
	})

	t.Run("reaches the column headers with ArrowUp and sorts with Enter", func(t *testing.T) {
		r := mockRig(t, 20)
		r.table.FocusList()
		r.refresh()
		th := r.table.header.ths
		r.key(kit.KeyArrowUp, 0)
		if r.table.ActiveDescendant() != th[colName].NodeID() {
			t.Fatal("not on Name's header")
		}
		r.key(kit.KeyArrowRight, 0)
		if r.table.ActiveDescendant() != th[colSize].NodeID() {
			t.Fatal("not on Size's header")
		}
		r.key(kit.KeyEnter, 0)
		if s := r.Main.Downloads.Sort(); s != (query.SortSpec{Key: query.SortSize, Dir: query.Desc}) {
			t.Fatal(s)
		}
		r.key(kit.KeySpace, 0)
		if s := r.Main.Downloads.Sort(); s != (query.SortSpec{Key: query.SortSize, Dir: query.Asc}) {
			t.Fatal(s)
		}
		r.key(kit.KeyArrowDown, 0)
		r.refresh()
		if r.table.ActiveDescendant() != r.cell(r.focus(), "size").NodeID() {
			t.Error("not on the cursor row's Size cell")
		}
	})

	t.Run("moves between cells with Left and Right", func(t *testing.T) {
		r := mockRig(t, 20)
		r.table.FocusList()
		r.refresh()
		r.key(kit.KeyHome, 0)
		id := r.focus()
		if r.table.ActiveDescendant() != r.row(id).NodeID() {
			t.Fatal("not on the row")
		}
		r.key(kit.KeyArrowRight, 0)
		if r.table.ActiveDescendant() != r.cell(id, "name").NodeID() {
			t.Fatal("not on Name")
		}
		r.key(kit.KeyArrowRight, 0)
		r.key(kit.KeyArrowRight, 0)
		c := r.cell(id, "progress")
		if r.table.ActiveDescendant() != c.NodeID() {
			t.Fatal("not on Progress")
		}
		if n := r.node(&c.Element); n.Role != kit.RoleCell || n.Cell.Col != 2 { // aria-colindex 3
			t.Errorf("role %v col %d", n.Role, n.Cell.Col)
		}
		// the column is kept while moving between rows
		r.key(kit.KeyArrowDown, 0)
		r.refresh()
		if r.table.ActiveDescendant() != r.cell(r.focus(), "progress").NodeID() {
			t.Error("the column was not kept")
		}
		r.key(kit.KeyEscape, 0)
		if r.table.ActiveDescendant() != r.row(r.focus()).NodeID() {
			t.Error("Escape did not leave the cell")
		}
		r.key(kit.KeyArrowRight, 0)
		r.key(kit.KeyArrowLeft, 0)
		r.key(kit.KeyArrowLeft, 0)
		if r.table.ActiveDescendant() != r.row(r.focus()).NodeID() {
			t.Error("Left did not reach the row")
		}
	})

	t.Run("reads progress on demand with I", func(t *testing.T) {
		r := mockRig(t, 20)
		ids := r.visibleIDs()
		running := -1
		for i, id := range ids {
			if d, _ := r.Main.Downloads.Get(id); d.GetState() == state.StateDownloading {
				running = i
				break
			}
		}
		if running < 0 {
			t.Fatal("nothing runs")
		}
		r.table.FocusList()
		r.key(kit.KeyHome, 0)
		for range running {
			r.key(kit.KeyArrowDown, 0)
		}
		r.key(kit.KeyI, 0)
		d, _ := r.Main.Downloads.Get(r.focus())
		got := r.dl.announced
		if !strings.HasPrefix(got, d.GetFilename()) || !regexp.MustCompile(`Downloading, \d+%, .+ of .+, .+/s`).MatchString(got) {
			t.Errorf("announced %q", got)
		}
	})
}

// ---- DownloadTable: keyboard position --------------------------------------------------------------

func TestDownloadTableKeyboardPosition(t *testing.T) {
	t.Run("starts at the first row in view when focused after scrolling with the mouse", func(t *testing.T) {
		r := mockRig(t, 500)
		if h := r.table.RowHeightNow(); h != 46 {
			t.Fatal("row height", h)
		}
		r.table.ScrollTo(300 * 46)
		r.Settle()
		r.table.FocusList()
		r.refresh()
		if i := slices.Index(r.visibleIDs(), r.focus()); i != 300 {
			t.Fatal("cursor at", i)
		}
		if r.Main.Downloads.Selection().Len() != 0 {
			t.Error("selected")
		}
		if r.table.ActiveDescendant() != r.row(r.focus()).NodeID() {
			t.Error("active descendant")
		}
	})

	t.Run("keeps the cursor row rendered after removing the rows around it", func(t *testing.T) {
		r := mockRig(t, 400)
		r.table.FocusList()
		r.key(kit.KeyEnd, 0)
		ids := r.visibleIDs()
		last := ids[len(ids)-3:]
		s := r.Main.Downloads
		s.Click(last[0], selectionMods(false, false))
		s.Click(last[2], selectionMods(true, false))
		ctx, cancel := r.App.CallContext()
		s.Remove(ctx, last, false)
		cancel()
		r.refresh()
		ids = r.visibleIDs()
		if r.focus() != ids[len(ids)-1] {
			t.Fatal("cursor", r.focus())
		}
		if w := r.row(r.focus()); w == nil || r.table.ActiveDescendant() != w.NodeID() {
			t.Error("the cursor row is not rendered")
		}
	})

	t.Run("marks a stale list and blanks live values", func(t *testing.T) {
		running := Download(1, func(d *downexv1.Download) {
			d.State, d.Received, d.SpeedBps = state.StateDownloading, MiB, MiB
		})
		r, daemon := fakeRig(t, running, Download(2, nil))
		if r.cellText(1, "speed") == "" {
			t.Fatal("no speed")
		}
		daemon.SetDown(true)
		r.Main.Downloads.RefreshStatus()
		r.refresh()
		if !r.table.stale {
			t.Fatal("not stale")
		}
		if n := r.node(&r.table.Element); n.Name != "Downloads, last known state" {
			t.Errorf("name %q", n.Name)
		}
		if s := r.cellText(1, "speed"); s != "" {
			t.Errorf("speed %q", s)
		}
		if s := r.cellText(1, "eta"); s != "" {
			t.Errorf("eta %q", s)
		}
	})
}

// ---- DownloadTable.test.ts: fitColumns ----------------------------------------------------------------

func keys(cols []display.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Key
	}
	return out
}

func TestFitColumns(t *testing.T) {
	widthOf := func(c display.Column) float64 { return c.Width * 16 }
	all := keys(display.Columns())
	pad := float64(display.RowPadStart + display.RowPadEnd)
	cases := []struct {
		name  string
		avail float64
		of    func(display.Column) float64
		want  []string
		not   string
	}{
		{"shows every column when there is room", 1200, widthOf, all, ""},
		// a 1,000 px window − the 224 px sidebar − the rows' padding and the card's border
		{"drops Added, Conn. and Time left first in a narrower window", 1000 - 224 - pad - 2, widthOf, []string{"name", "size", "progress", "speed", "status"}, ""},
		{"keeps Name, Progress, Speed and Status in the 900 px minimum window", 900 - 224 - pad - 2, widthOf, []string{"name", "progress", "speed", "status"}, ""},
		{"never drops Name, Progress or Status", 200, widthOf, []string{"name", "progress", "status"}, ""},
		{"accounts for resized columns", 1000, func(c display.Column) float64 {
			if c.Key == "status" {
				return 400
			}
			return c.Width * 16
		}, nil, "added"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := keys(display.FitColumns(c.avail, 16, c.of))
			if c.want != nil && !slices.Equal(got, c.want) {
				t.Errorf("%v, want %v", got, c.want)
			}
			if c.not != "" && slices.Contains(got, c.not) {
				t.Errorf("%v has %s", got, c.not)
			}
		})
	}
}

// ---- DownloadTable: media downloads ------------------------------------------------------------------

func TestDownloadTableMedia(t *testing.T) {
	t.Run("marks streams with a badge and counts their fragments as progress", func(t *testing.T) {
		r, _ := fakeRig(t,
			MediaDownload(1, func(d *downexv1.Download) {
				d.State, d.Received, d.SpeedBps = state.StateDownloading, 40*MiB, MiB
				d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 60 })
			}),
			MediaDownload(2, func(d *downexv1.Download) {
				d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.Format = "dash" })
			}),
			Download(3, func(d *downexv1.Download) { d.Filename = "plain.zip" }),
		)
		badge := func(id int64) string {
			row := r.row(id)
			if row.tag.Style.Hidden {
				return ""
			}
			return row.tag.Semantics.Name
		}
		if b := badge(1); b != "HLS" {
			t.Errorf("badge 1 %q", b)
		}
		if b := badge(2); b != "DASH" {
			t.Errorf("badge 2 %q", b)
		}
		if b := badge(3); b != "" {
			t.Errorf("badge 3 %q", b)
		}
		if d := r.row(1).Semantics.Description; !regexp.MustCompile(`^Size unknown, category Video, added .+, HLS stream$`).MatchString(d) {
			t.Errorf("description 1 %q", d)
		}
		if d := r.row(3).Semantics.Description; strings.Contains(d, "stream") {
			t.Errorf("description 3 %q", d)
		}
		if s := r.cellText(1, "progress"); !strings.Contains(s, "30%") {
			t.Errorf("progress %q", s)
		}
	})

	t.Run(`shows a stream’s estimated size with "~", and its time left`, func(t *testing.T) {
		r, _ := fakeRig(t,
			MediaDownload(1, func(d *downexv1.Download) {
				d.State, d.Size, d.SizeApprox, d.Received, d.SpeedBps = state.StateDownloading, 2100*MiB, true, 700*MiB, 7*MiB
				d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
			}),
			MediaDownload(2, func(d *downexv1.Download) {
				d.State, d.Size, d.Received = state.StateCompleted, 1900*MiB, 1900*MiB
				d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 200 })
			}),
		)
		if s := r.cellText(1, "size"); s != "~2.2 GB" {
			t.Errorf("size 1 %q", s)
		}
		if s := r.cellText(1, "eta"); s != "3m 20s" {
			t.Errorf("eta %q", s)
		}
		if s := r.cellText(2, "size"); s != "2.0 GB" {
			t.Errorf("size 2 %q", s)
		}
		if d := r.row(1).Semantics.Description; !regexp.MustCompile(`^Size about 2\.2 GB, category Video, added .+, HLS stream$`).MatchString(d) {
			t.Errorf("description %q", d)
		}
	})
}

// ---- DownloadTable: density ----------------------------------------------------------------------------

func TestDownloadTableDensity(t *testing.T) {
	t.Run("sizes rows from the root font: Comfortable 46 px, Compact 24 px at 16 px", func(t *testing.T) {
		for _, c := range []struct {
			rem     float64
			compact bool
			want    float64
		}{{16, false, 46}, {16, true, 24}, {12, false, 44}, {12, true, 24}, {20, false, 58}, {20, true, 30}} {
			if got := display.RowHeightFor(c.rem, c.compact); got != c.want {
				t.Errorf("rowHeightFor(%v, %v) = %v, want %v", c.rem, c.compact, got, c.want)
			}
		}
	})

	t.Run("follows the density store: two lines and 26 px tiles, or one line with the host in the tooltip", func(t *testing.T) {
		d := Download(1, func(d *downexv1.Download) {
			d.Url, d.Filename, d.State, d.Received, d.SpeedBps = "https://www.example.com/pub/plan.xlsx", "plan.xlsx", state.StateDownloading, MiB, MiB
		})
		r, _ := fakeRig(t, d, Download(2, nil))
		if h := r.table.RowHeightNow(); h != 46 {
			t.Fatalf("row height %v", h)
		}
		row := r.row(1)
		if row.sub.Style.Hidden || row.sub.Text() != "example.com · Documents" {
			t.Errorf("sub %q", row.sub.Text())
		}
		if row.tile.size != 26 {
			t.Errorf("tile %v", row.tile.size)
		}
		if y := r.row(r.visibleIDs()[1]).WindowBounds().Min.Y - r.row(r.visibleIDs()[0]).WindowBounds().Min.Y; y != 46 {
			t.Errorf("second row at +%v", y)
		}
		r.App.Density.Set(state.Compact)
		r.refresh()
		if v, ok, _ := r.Storage.Get(state.DensityKey); !ok || v != "compact" {
			t.Errorf("stored density %q", v)
		}
		if h := r.table.RowHeightNow(); h != 24 {
			t.Errorf("compact row height %v", h)
		}
		if y := r.row(r.visibleIDs()[1]).WindowBounds().Min.Y - r.row(r.visibleIDs()[0]).WindowBounds().Min.Y; y != 24 {
			t.Errorf("second row at +%v", y)
		}
		row = r.row(1)
		if !row.sub.Style.Hidden {
			t.Error("the sub-line shows in Compact")
		}
		if row.tile.size != 16 {
			t.Errorf("compact tile %v", row.tile.size)
		}
		if row.name.Tooltip != "plan.xlsx\nexample.com · Documents" {
			t.Errorf("title %q", row.name.Tooltip)
		}
	})

	t.Run("keeps the header at 30 px", func(t *testing.T) {
		r, _ := fakeRig(t, Download(1, nil))
		if h := r.table.HeaderBounds().Height(); h != 30 {
			t.Errorf("header %v", h)
		}
	})
}

// ---- DownloadTable: cells by state -------------------------------------------------------------------------

func TestDownloadTableCells(t *testing.T) {
	t.Run("shows a segmented bar of at most 8 cells with the percentage for an active row", func(t *testing.T) {
		segs := make([]*downexv1.Segment, 16)
		for i := range segs {
			segs[i] = &downexv1.Segment{Start: int64(i) * 4 * MiB, End: int64(i+1)*4*MiB - 1, Received: 2 * MiB}
		}
		r, _ := fakeRig(t, Download(1, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.SpeedBps, d.Connections, d.Segments = state.StateDownloading, 64*MiB, 32*MiB, MiB, 16, segs
		}))
		row := r.row(1)
		if !row.bar.Semantics.Hidden {
			t.Error("bar exposed")
		}
		if n := len(row.bar.Shown()); n == 0 || n > 8 {
			t.Errorf("cells %d", n)
		}
		if s := row.pct.Text(); s != "50%" {
			t.Errorf("pct %q", s)
		}
		if s := r.cellText(1, "connections"); s != "16" {
			t.Errorf("conns %q", s)
		}
	})

	t.Run(`shows a completed row without a bar and only "Completed" (the date is in Added)`, func(t *testing.T) {
		r, _ := fakeRig(t, Download(1, func(d *downexv1.Download) {
			d.State, d.Received, d.CompletedAt = state.StateCompleted, 10*MiB, Millis(1_700_000_500_000)
		}))
		row := r.row(1)
		if !row.bar.Style.Hidden {
			t.Error("bar")
		}
		if s := r.cellText(1, "progress"); s != "" {
			t.Errorf("progress %q", s)
		}
		if s := r.cellText(1, "status"); s != "Completed" {
			t.Errorf("status %q", s)
		}
		if s := r.cellText(1, "added"); s == "" {
			t.Error("no date")
		}
	})

	t.Run("keeps earlier progress of a queued row in grey, and none when it has none", func(t *testing.T) {
		r, _ := fakeRig(t,
			Download(1, func(d *downexv1.Download) { d.State, d.Received, d.QueuePosition = state.StateQueued, 3*MiB, 1 }),
			Download(2, func(d *downexv1.Download) { d.State, d.QueuePosition = state.StateQueued, 2 }),
		)
		if r.row(1).bar.Style.Hidden || barState(state.StateQueued) != 2 /* BarQueued: tone-idle */ {
			t.Error("queued bar")
		}
		if s := r.row(1).pct.Text(); s != "30%" {
			t.Errorf("pct %q", s)
		}
		if !r.row(2).bar.Style.Hidden {
			t.Error("bar without progress")
		}
		// "Queued #2" with the position set apart, read as one text
		if s := r.cellText(2, "status"); s != "Queued #2" {
			t.Errorf("status %q", s)
		}
		if s := strings.TrimSpace(r.row(2).pos.Text()); s != "#2" {
			t.Errorf("pos %q", s)
		}
	})

	t.Run("offers Retry only on failures that retrying can help, and Restart… when the file changed", func(t *testing.T) {
		r, _ := fakeRig(t,
			Download(1, func(d *downexv1.Download) {
				d.State, d.Received, d.ErrorCode, d.Error = state.StateFailed, MiB, "http_status", "HTTP 503 Service Unavailable"
			}),
			Download(2, func(d *downexv1.Download) {
				d.State, d.Received, d.ErrorCode = state.StateFailed, MiB, "validator_changed"
			}),
			MediaDownload(3, func(d *downexv1.Download) { d.State, d.ErrorCode = state.StateFailed, "encrypted" }),
			Download(4, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MiB }),
		)
		retry := r.row(1).inline
		if retry.Style.Hidden || retry.label.Text() != "Retry" {
			t.Fatalf("retry %q", retry.label.Text())
		}
		if !retry.Semantics.Hidden || retry.Focus != kit.FocusNone {
			t.Error("retry exposed or focusable")
		}
		// the reason stays in the cell's tooltip; the status text stays the state
		if tip := r.cell(1, "status").Tooltip; tip != "HTTP 503 Service Unavailable" {
			t.Errorf("tooltip %q", tip)
		}
		if s := r.row(1).status.Text(); s != "Failed" {
			t.Errorf("status %q", s)
		}
		r.Click(&retry.Element)
		if c, ok := r.Calls.Last("Resume"); !ok || !slices.Equal(c.Args[0].([]int64), []int64{1}) {
			t.Errorf("retry called %v", r.Calls.List)
		}
		// a click on it is not a click on the row
		if r.Main.Downloads.Selection().Len() != 0 {
			t.Error("the row was selected")
		}
		restart := r.row(2).inline
		if restart.label.Text() != "Restart…" {
			t.Errorf("restart %q", restart.label.Text())
		}
		r.Click(&restart.Element)
		if c, ok := r.Calls.Last("Restart"); !ok || c.Args[0].(views.RestartRequest).IDs[0] != 2 {
			t.Errorf("restart called %v", c)
		}
		if !r.row(3).inline.Style.Hidden || !r.row(4).inline.Style.Hidden {
			t.Error("inline actions where they cannot help")
		}
		if _, ok := display.InlineAction(&downexv1.Download{State: state.StateFailed, ErrorCode: "site_unsupported"}); ok {
			t.Error("site_unsupported")
		}
		if a, ok := display.InlineAction(&downexv1.Download{State: state.StateFailed, ErrorCode: "network"}); !ok || a != (display.Action{ID: "resume", Label: "Retry"}) {
			t.Error("network", a)
		}
	})

	t.Run("offers no inline actions while downexd is unreachable", func(t *testing.T) {
		r, daemon := fakeRig(t, Download(1, func(d *downexv1.Download) { d.State, d.ErrorCode, d.Received = state.StateFailed, "network", MiB }))
		daemon.SetDown(true)
		r.Main.Downloads.RefreshStatus()
		r.refresh()
		row := r.row(1)
		if !row.inline.Style.Hidden || !row.review.Style.Hidden || !row.acts.Style.Hidden {
			t.Error("row actions while stale")
		}
	})

	t.Run("offers Review › for a download to confirm", func(t *testing.T) {
		r, _ := fakeRig(t, Download(1, func(d *downexv1.Download) { d.State, d.Size, d.ExpectedSize = state.StateAwaiting, -1, 2*MiB }))
		rv := r.row(1).review
		if rv.Style.Hidden || rv.label.Text() != "Review" {
			t.Fatal("no Review")
		}
		if !rv.Semantics.Hidden || rv.Focus != kit.FocusNone {
			t.Error("review exposed or focusable")
		}
		r.Click(&rv.Element)
		if c, ok := r.Calls.Last("Confirm"); !ok || c.Args[0] != (views.ConfirmRequest{ID: 1, Requested: true}) {
			t.Errorf("confirm %v", c)
		}
		// the size the browser announced, until it is probed
		if s := r.cellText(1, "size"); s != "~2.1 MB" {
			t.Errorf("size %q", s)
		}
		if s := r.row(1).status.Text(); s != "Waiting for confirmation" {
			t.Errorf("status %q", s)
		}
	})

	t.Run("maps every state to its progress cell", func(t *testing.T) {
		for _, c := range []struct {
			st   downexv1.DownloadState
			recv int64
			want display.ProgressCell
		}{
			{state.StateDownloading, 0, display.CellBar}, {state.StatePaused, 0, display.CellBar},
			{state.StateProbing, 0, display.CellProbe}, {state.StateFailed, 0, display.CellNone},
			{state.StateFailed, 1, display.CellBar}, {state.StateAwaiting, 0, display.CellReview},
			{state.StateCompleted, 9, display.CellNone}, {state.StateCanceled, 9, display.CellNone},
		} {
			if got := display.ProgressCellOf(&downexv1.Download{State: c.st, Received: c.recv}); got != c.want {
				t.Errorf("%v/%d: %v, want %v", c.st, c.recv, got, c.want)
			}
		}
	})

	t.Run("keeps the hover actions with their titles", func(t *testing.T) {
		r, _ := fakeRig(t, Download(1, func(d *downexv1.Download) { d.State, d.Received, d.SpeedBps = state.StateDownloading, MiB, MiB }))
		a := r.row(1).acts
		var titles []string
		for _, b := range a.btns {
			titles = append(titles, b.Tooltip)
		}
		if !slices.Equal(titles, []string{"Pause", "Remove…"}) {
			t.Error(titles)
		}
		// they show (and take the pointer) while the pointer is over the row
		r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: r.rowPoint(1)})
		r.Advance(200 * time.Millisecond)
		if a.Style.Hidden || a.Inert || a.alpha != 1 {
			t.Errorf("hover: hidden %v inert %v alpha %v", a.Style.Hidden, a.Inert, a.alpha)
		}
		r.Click(&a.btns[0].Element)
		if c, ok := r.Calls.Last("Pause"); !ok || !slices.Equal(c.Args[0].([]int64), []int64{1}) {
			t.Errorf("pause %v", r.Calls.List)
		}
	})
}

// ---- DownloadTable: empty states --------------------------------------------------------------------------

func findText(e *kit.Element, s string) *kit.Element {
	var found *kit.Element
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if found != nil || e.Style.Hidden {
			return
		}
		if l, ok := e.Widget().(*kit.Label); ok && l.Text() == s {
			found = e
			return
		}
		if tx, ok := e.Widget().(*views.Text); ok && tx.Text() == s {
			found = e
			return
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(e)
	return found
}

// button is the button whose label is s inside e (its label's nearest pressable ancestor).
func button(e *kit.Element, s string) *kit.Element {
	l := findText(e, s)
	for p := l; p != nil; p = p.Parent() {
		if p.Semantics.Role == kit.RoleButton {
			return p
		}
	}
	return nil
}

func TestDownloadTableEmpty(t *testing.T) {
	t.Run("shows how to add a download, and where browser downloads come from", func(t *testing.T) {
		r, _ := fakeRig(t)
		e := &r.grid.empty.Element
		if findText(e, "No downloads yet") == nil {
			t.Fatal("no heading")
		}
		if findText(e, "Paste a link anywhere in this window (Ctrl+V), drop one here, or add one.") == nil {
			t.Error("no text")
		}
		for _, b := range []string{"Add download", "Add video or stream", "Get the browser extension"} {
			el := button(e, b)
			if el == nil {
				t.Fatal("no", b)
			}
			r.Click(el)
		}
		if got := r.Calls.Names(); !slices.Equal(got, []string{"Add", "Add", "Settings"}) {
			t.Fatal(got)
		}
		if r.Calls.List[0].Args[0] != (views.AddRequest{}) || r.Calls.List[1].Args[0] != (views.AddRequest{Mode: views.AddStream}) {
			t.Error(r.Calls.List)
		}
	})

	t.Run("leaves out the actions it is not given, and disables adding while downexd is unreachable", func(t *testing.T) {
		d := statetest.NewDaemon()
		h := viewstest.New(t, viewstest.Options{Daemon: d})
		h.App.Open.Settings = nil
		dl, err := New(h.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(dl.Dispose)
		h.Show(dl.Grid())
		e := &dl.Grid().empty.Element
		if button(e, "Get the browser extension") != nil {
			t.Error("the extension link without its opener")
		}
		d.SetDown(true)
		h.Main.Downloads.RefreshStatus()
		h.Settle()
		dl.Refresh()
		h.Settle()
		if b := button(&dl.Grid().empty.Element, "Add download"); b == nil || !b.Disabled {
			t.Error("Add download is not disabled")
		}
	})

	t.Run("says what matched nothing and clears the filters", func(t *testing.T) {
		r, _ := fakeRig(t, Download(1, func(d *downexv1.Download) { d.Filename = "plan.xlsx" }))
		r.Main.Downloads.SetSearch("quarterly")
		r.refresh()
		e := &r.grid.empty.Element
		if findText(e, "No downloads match “⁨quarterly⁩”") == nil {
			t.Fatalf("heading: %q", textOf(e))
		}
		r.Click(button(e, "Clear filters"))
		if !r.Calls.Has("ClearFilters") {
			t.Error("Clear filters")
		}
		r.Main.Downloads.SetSearch("")
		r.Main.Downloads.SetStatusFilter(categories.Failed)
		r.refresh()
		if findText(&r.grid.empty.Element, "Nothing in Failed") == nil {
			t.Errorf("heading: %q", textOf(&r.grid.empty.Element))
		}
	})
}

func selectionMods(shift, toggle bool) selection.Modifiers {
	return selection.Modifiers{Shift: shift, Toggle: toggle}
}
