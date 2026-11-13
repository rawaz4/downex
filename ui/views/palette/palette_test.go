package palette_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	core "downex/ui/core/palette"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
)

// The module cases of CommandPalette.test.ts are ported in ui/core/palette, whose functions
// this view calls (one vitest case per line, then its Go test):
//
//	'matches subsequences, preferring word starts and runs': TestFuzzyScore
//	'ranks the best match first and keeps the order when the query is empty': TestFilterCommands
//	'lists the running downloads first with an empty query, then the commands in their order': TestEntriesEmptyQuery
//	'ranks commands and downloads together, commands first on a tie': TestEntriesRanking
//	'never puts an unavailable command first among equal matches': TestUnavailableNeverFirst
//	'finds commands by whole words of their description only': TestDescriptionWholeWords
//	'highlights the matched letters in runs': TestHighlightParts
//
// The component cases (describe 'CommandPalette') are here.

const MiB = formattest.MiB

// recorder holds what the palette was asked to do.
type recorder struct {
	ran     []string
	opened  []int64
	folders []int64
}

// cmd is the vitest's cmd(): a command of the group Downloads.
func cmd(id, label string, edit func(c *core.Command)) core.Command {
	c := core.Command{ID: id, Label: label, Group: "Downloads"}
	if edit != nil {
		edit(&c)
	}
	return c
}

// open renders the palette (the vitest's render(CommandPalette, { props })) in a harness.
func open(t *testing.T, commands []core.Command, downloads []core.Download, withDownloads bool) (*viewstest.Harness, *palette.Palette, *recorder) {
	t.Helper()
	h := viewstest.New(t, viewstest.Options{Daemon: statetest.NewDaemon()})
	rec := &recorder{}
	o := palette.Options{
		Commands:  func() []core.Command { return commands },
		Downloads: func() []core.Download { return downloads },
		Run:       func(id string) { rec.ran = append(rec.ran, id) },
	}
	if withDownloads {
		o.OpenDownload = func(id int64) { rec.opened = append(rec.opened, id) }
		o.ShowFolder = func(id int64) { rec.folders = append(rec.folders, id) }
	} else {
		o.OpenDownload = func(int64) {}
	}
	p := palette.New(h.App, o)
	h.App.Dialogs.Show(p)
	h.Settle()
	return h, p, rec
}

// typeQuery is fireEvent.input on the field.
func typeQuery(h *viewstest.Harness, p *palette.Palette, q string) {
	p.SetQuery(q)
	h.Settle()
}

func optionTexts(p *palette.Palette) []string {
	var out []string
	for _, r := range p.Rows() {
		out = append(out, r.Label().Text())
	}
	return out
}

func selected(p *palette.Palette) int { return p.CurrentIndex() }

func TestCommandPalette(t *testing.T) {
	t.Run("filters as you type and runs the current command with Enter", func(t *testing.T) {
		pause := cmd("pause", "Pause all", nil)
		settings := cmd("settings", "Open settings", func(c *core.Command) { c.Group, c.Keywords = "View", "preferences" })
		h, p, rec := open(t, []core.Command{pause, settings}, nil, false)
		if p.Field().Semantics.Role != kit.RoleComboBox || p.Field().Semantics.Name != "Search commands" {
			t.Errorf("field %v %q", p.Field().Semantics.Role, p.Field().Semantics.Name)
		}
		if p.Card().Semantics.Role != kit.RoleDialog || p.Card().Semantics.Name != "Command palette" {
			t.Errorf("dialog %q", p.Card().Semantics.Name)
		}
		if len(p.Rows()) != 2 {
			t.Fatalf("%d options", len(p.Rows()))
		}
		if h.Window.Focused() != &p.Field().Element {
			t.Error("the field is not focused")
		}
		typeQuery(h, p, "prefs")
		if got := optionTexts(p); !slices.Equal(got, []string{"Open settings"}) {
			t.Errorf("options %q", got)
		}
		if !p.Rows()[0].Current() {
			t.Error("the first option is not the active descendant")
		}
		h.Key(kit.KeyEnter, 0)
		if h.App.Dialogs.PaletteOpen() {
			t.Error("Enter did not close the palette")
		}
		if !slices.Equal(rec.ran, []string{"settings"}) {
			t.Errorf("ran %v", rec.ran)
		}
	})

	t.Run("moves with the arrows, wraps, and does not run disabled commands", func(t *testing.T) {
		a := cmd("a", "Resume all", func(c *core.Command) { c.Disabled = true })
		b := cmd("b", "Pause all", nil)
		h, p, rec := open(t, []core.Command{a, b}, nil, false)
		h.Key(kit.KeyEnter, 0)
		if !h.App.Dialogs.PaletteOpen() || len(rec.ran) != 0 {
			t.Errorf("Enter on a disabled command: open %v, ran %v", h.App.Dialogs.PaletteOpen(), rec.ran)
		}
		h.Key(kit.KeyArrowUp, 0)
		if selected(p) != 1 {
			t.Errorf("ArrowUp selected %d", selected(p))
		}
		h.Key(kit.KeyArrowDown, 0)
		if selected(p) != 0 {
			t.Errorf("ArrowDown selected %d", selected(p))
		}
		if p.Rows()[0].Semantics.States&kit.NodeDisabled == 0 {
			t.Error("the first option is not disabled")
		}
	})

	t.Run("names its groups, says why a command is unavailable and announces the count", func(t *testing.T) {
		a := cmd("a", "Pause selected", func(c *core.Command) { c.Disabled, c.DisabledReason = true, "nothing selected" })
		b := cmd("b", "Open settings", func(c *core.Command) { c.Group = "View" })
		h, p, _ := open(t, []core.Command{a, b}, nil, false)
		var groups []string
		for _, g := range p.List().Content.Children() {
			if g.Semantics.Role == kit.RoleGroup {
				groups = append(groups, g.Semantics.Name)
			}
		}
		if !slices.Equal(groups, []string{"Downloads", "View"}) {
			t.Errorf("groups %q", groups)
		}
		pause := p.Rows()[0]
		if pause.Semantics.Name != "Pause selected" || pause.Semantics.Description != "Unavailable: nothing selected" {
			t.Errorf("option %q described %q", pause.Semantics.Name, pause.Semantics.Description)
		}
		if !slices.Contains(pause.Texts(), "Unavailable: nothing selected") {
			t.Errorf("the current option shows %q", pause.Texts())
		}
		if p.Summary() != "2 commands" {
			t.Errorf("status %q", p.Summary())
		}
		typeQuery(h, p, "settings")
		if p.Summary() != "1 command" {
			t.Errorf("status %q", p.Summary())
		}
	})

	t.Run("closes on Escape and says when nothing matches", func(t *testing.T) {
		h, p, _ := open(t, []core.Command{cmd("a", "Pause all", nil)}, nil, false)
		typeQuery(h, p, "zzz")
		if len(p.Rows()) != 0 {
			t.Errorf("%d options", len(p.Rows()))
		}
		if p.Summary() != "No matching commands" {
			t.Errorf("status %q", p.Summary())
		}
		h.Key(kit.KeyEscape, 0)
		if h.App.Dialogs.PaletteOpen() {
			t.Error("Escape did not close the palette")
		}
	})

	t.Run("describes every command in plain words", func(t *testing.T) {
		pause := cmd("pause-all", "Pause all", func(c *core.Command) {
			c.Description, c.Shortcut = "Stops the 3 active downloads, keeps their progress", "Ctrl+Shift+P"
		})
		_, p, _ := open(t, []core.Command{pause}, nil, false)
		r := p.Rows()[0]
		if r.Semantics.Name != "Pause all" || r.Semantics.Description != "Stops the 3 active downloads, keeps their progress" {
			t.Errorf("option %q described %q", r.Semantics.Name, r.Semantics.Description)
		}
		if k := r.Keys(); !slices.Equal(k, []string{"Ctrl", "Shift", "P"}) {
			t.Errorf("kbd %q", k)
		}
	})

	t.Run("shows running downloads with their progress; Enter opens one, Ctrl+Enter shows it in its folder", func(t *testing.T) {
		live := formattest.Download(5, func(d *downexv1.Download) {
			d.Filename, d.State, d.Size, d.Received, d.SpeedBps = "conference-keynote.mp4", state.StateDownloading, 100*MiB, 42*MiB, 6*MiB
		})
		done := formattest.Download(6, func(d *downexv1.Download) {
			d.Filename, d.State, d.Received = "keynote-2026.mp4", state.StateCompleted, 10*MiB
			d.CompletedAt = timestamppb.New(time.UnixMilli(1_700_000_500_000))
		})
		downloads := []core.Download{
			{ID: 5, Name: "conference-keynote.mp4", Host: "youtube.com", Kind: "video", Tag: "YouTube", Download: live},
			{ID: 6, Name: "keynote-2026.mp4", Host: "media.example.org", Kind: "video", Download: done},
		}
		h, p, rec := open(t, []core.Command{cmd("add", "Add download…", nil)}, downloads, true)
		if g := p.Entries()[0].Group; g != "Running downloads" {
			t.Errorf("group %q", g)
		}
		row := p.Rows()[0]
		text := strings.Join(row.Texts(), " ")
		for _, want := range []string{"conference-keynote.mp4", "42%", "6.3 MB/s", "left"} {
			if !strings.Contains(text, want) {
				t.Errorf("row %q lacks %q", text, want)
			}
		}
		if !row.HasBar() {
			t.Error("no mini segment bar")
		}
		// only running downloads before a query
		if slices.Contains(optionTexts(p), "keynote-2026.mp4") {
			t.Error("a completed download is listed before a query")
		}
		if p.Summary() != "1 download · 1 command" {
			t.Errorf("status %q", p.Summary())
		}
		if !strings.Contains(p.Field().Semantics.Description, "show in folder") {
			t.Errorf("hints %q", p.Field().Semantics.Description)
		}
		if !row.Current() {
			t.Error("the running download is not the active descendant")
		}
		h.Key(kit.KeyEnter, 0)
		if h.App.Dialogs.PaletteOpen() || !slices.Equal(rec.opened, []int64{5}) {
			t.Errorf("open %v, opened %v", h.App.Dialogs.PaletteOpen(), rec.opened)
		}

		h2, p2, rec2 := open(t, nil, downloads, true)
		typeQuery(h2, p2, "keynote 2026")
		if len(p2.Rows()) != 1 {
			t.Fatalf("options %q", optionTexts(p2))
		}
		found := strings.Join(p2.Rows()[0].Texts(), " ")
		if !strings.Contains(found, "Completed") || !strings.Contains(found, "media.example.org") {
			t.Errorf("found %q", found)
		}
		if len(p2.Rows()[0].Marks()) == 0 {
			t.Error("no highlighted letters")
		}
		h2.Key(kit.KeyEnter, kit.ModCtrl)
		if !slices.Equal(rec2.folders, []int64{6}) || len(rec2.opened) != 0 {
			t.Errorf("folders %v, opened %v", rec2.folders, rec2.opened)
		}
	})
}

// TestPaletteFromTheStore: with no options the palette lists app.Commands and the store's
// downloads, and a command runs through app.Commands after the palette has gone (the focus
// back where it was).
func TestPaletteFromTheStore(t *testing.T) {
	d := statetest.NewDaemon(formattest.Download(1, func(d *downexv1.Download) {
		d.Filename, d.State, d.Size, d.Received = "big.iso", state.StateDownloading, 100*MiB, 10*MiB
	}))
	h := viewstest.New(t, viewstest.Options{Daemon: d})
	p := palette.New(h.App, palette.Options{})
	h.App.Dialogs.Show(p)
	h.Settle()
	if len(p.Entries()) == 0 || p.Entries()[0].Key != "d-1" {
		t.Fatalf("entries %v", p.Entries())
	}
	if len(p.Entries()) != 1+len(h.App.Commands.List()) {
		t.Errorf("%d entries for %d commands", len(p.Entries()), len(h.App.Commands.List()))
	}
	typeQuery(h, p, "add download")
	if c := p.Current(); c == nil || c.Key != "c-add" {
		t.Fatalf("current %v", c)
	}
	h.Calls.Reset()
	h.Key(kit.KeyEnter, 0)
	if c, ok := h.Calls.Last("Add"); !ok || c.Args[0].(views.AddRequest) != (views.AddRequest{}) {
		t.Errorf("calls %v", h.Calls.List)
	}
}

// TestPaletteBackdropAndPointer: a press on the backdrop closes it; a pointer over a row makes
// it current and a click runs it.
func TestPaletteBackdropAndPointer(t *testing.T) {
	h, p, rec := open(t, []core.Command{cmd("a", "Pause all", nil), cmd("b", "Resume all", nil)}, nil, false)
	second := &p.Rows()[1].Element
	h.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: second.WindowBounds().Center()})
	h.Settle()
	if selected(p) != 1 {
		t.Errorf("hover selected %d", selected(p))
	}
	h.Click(second)
	if !slices.Equal(rec.ran, []string{"b"}) || h.App.Dialogs.PaletteOpen() {
		t.Errorf("ran %v, open %v", rec.ran, h.App.Dialogs.PaletteOpen())
	}
	h2, p2, _ := open(t, []core.Command{cmd("a", "Pause all", nil)}, nil, false)
	b := p2.Card().WindowBounds()
	h2.ClickAt(kit.Pt(b.Min.X/2, b.Max.Y+40))
	if h2.App.Dialogs.PaletteOpen() {
		t.Error("a press on the backdrop did not close it")
	}
}
