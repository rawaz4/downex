package dialogs_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/viewstest"
)

// file is a download named name in a state.
func file(id int64, name string, st downexv1.DownloadState) *downexv1.Download {
	return formattest.Download(id, func(d *downexv1.Download) {
		d.Filename = name
		d.State = st
	})
}

func harness(t *testing.T, list ...*downexv1.Download) (*viewstest.Harness, *statetest.Daemon) {
	t.Helper()
	d := statetest.NewDaemon(list...)
	return viewstest.New(t, viewstest.Options{Daemon: d}), d
}

func show(h *viewstest.Harness, m views.Modal) {
	h.App.Dialogs.Show(m)
	h.Settle()
}

func click(h *viewstest.Harness, e *kit.Element) { h.Click(e) }

// rowText is a choice row's label and note, as its <label>'s textContent holds them.
func rowText(g *widgets.RadioRows, i int) string {
	o := g.Options[i]
	return o.Label + " " + o.Note
}

// restartDialog opens RestartDialog for names (ids 1…) with the props of the vitest's show():
// reason completed unless given, the again files, and a recording onconfirm.
func restartDialog(t *testing.T, h *viewstest.Harness, n int, reason views.RestartReason, again []views.AgainFile) (*dialogs.Restart, *[]downexv1.ExistingFile) {
	t.Helper()
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	var got []downexv1.ExistingFile
	d := dialogs.NewRestart(h.App, views.RestartRequest{IDs: ids, Reason: reason, Again: again}, func(e downexv1.ExistingFile) { got = append(got, e) })
	show(h, d)
	return d, &got
}

// The cases of RestartDialog.test.ts ("RestartDialog: downloading a completed file again").
func TestRestartDialog(t *testing.T) {
	t.Run("asks to keep both (the default, with the numbered name) or to replace the current file", func(t *testing.T) {
		h, _ := harness(t, file(1, "report.pdf", state.StateCompleted))
		d, got := restartDialog(t, h, 1, views.RestartCompleted, []views.AgainFile{{Name: "report.pdf", Exists: true}})
		if n := d.Card().Semantics.Name; n != "Download again?" {
			t.Errorf("heading %q", n)
		}
		if s := d.Card().Semantics.Description; s != "Downex downloads “\u2068report.pdf\u2069” again from its address." {
			t.Errorf("description %q", s)
		}
		g := d.Choices()
		if g == nil || g.Value != "keepBoth" {
			t.Fatalf("choices %v", g)
		}
		if s := rowText(g, 0); !strings.Contains(s, "Keep both") || !strings.Contains(s, "saved as “\u2068report (1).pdf\u2069”") {
			t.Errorf("keep both reads %q", s)
		}
		if s := rowText(g, 1); !strings.Contains(s, "Replace the current file") || !strings.Contains(s, "stays until the new copy is complete") {
			t.Errorf("replace reads %q", s)
		}
		// the chosen row has the keyboard, like the queue rows of Move to queue
		if f := h.Window.Focused(); f != g.Children()[0] {
			t.Errorf("focused %v", f)
		}
		_, submit := d.Buttons()
		if submit.Label().Text() != "Download again" {
			t.Errorf("submit %q", submit.Label().Text())
		}
		click(h, &submit.Element)
		if !slices.Equal(*got, []downexv1.ExistingFile{state.KeepBoth}) {
			t.Errorf("onconfirm %v", *got)
		}
		if h.App.Dialogs.DialogOpen() {
			t.Error("the dialog stayed")
		}
	})

	t.Run("remembers Replace for the next time, on this computer", func(t *testing.T) {
		h, _ := harness(t, file(1, "report.pdf", state.StateCompleted))
		again := []views.AgainFile{{Name: "report.pdf", Exists: true}}
		first, got := restartDialog(t, h, 1, views.RestartCompleted, again)
		click(h, first.Choices().Children()[1])
		if first.Choices().Value != "replace" || first.Choice() != state.Replace {
			t.Fatalf("chosen %q", first.Choices().Value)
		}
		_, submit := first.Buttons()
		click(h, &submit.Element)
		if !slices.Equal(*got, []downexv1.ExistingFile{state.Replace}) {
			t.Errorf("onconfirm %v", *got)
		}
		if v, _, _ := h.Storage.Get(state.ExistingFileKey); v != "replace" {
			t.Errorf("%s = %q", state.ExistingFileKey, v)
		}
		second, _ := restartDialog(t, h, 1, views.RestartCompleted, again)
		if second.Choices().Value != "replace" {
			t.Errorf("the second dialog chose %q", second.Choices().Value)
		}
	})

	t.Run("works when storage cannot be read or written", func(t *testing.T) {
		h, _ := harness(t, file(1, "report.pdf", state.StateCompleted))
		h.App.Storage = statetest.Broken{}
		d, got := restartDialog(t, h, 1, views.RestartCompleted, []views.AgainFile{{Name: "report.pdf", Exists: true}})
		if d.Choices().Value != "keepBoth" {
			t.Errorf("chosen %q", d.Choices().Value)
		}
		click(h, d.Choices().Children()[1])
		_, submit := d.Buttons()
		click(h, &submit.Element)
		if !slices.Equal(*got, []downexv1.ExistingFile{state.Replace}) {
			t.Errorf("onconfirm %v", *got)
		}
	})

	t.Run("offers no choice when the file is no longer there: it comes back under its own name", func(t *testing.T) {
		h, _ := harness(t, file(1, "report.pdf", state.StateCompleted))
		d, got := restartDialog(t, h, 1, views.RestartCompleted, []views.AgainFile{{Name: "report.pdf", Exists: false}})
		if d.Choices() != nil {
			t.Error("a choice is offered")
		}
		want := "“\u2068report.pdf\u2069” is no longer in its folder. Downex downloads it again from its address and saves it under the same name."
		if s := d.Card().Semantics.Description; s != want {
			t.Errorf("description %q", s)
		}
		_, submit := d.Buttons()
		click(h, &submit.Element)
		if !slices.Equal(*got, []downexv1.ExistingFile{state.KeepBoth}) {
			t.Errorf("onconfirm %v", *got)
		}
	})

	t.Run("says a web page’s video is read from its page again", func(t *testing.T) {
		h, _ := harness(t, file(1, "A talk.mp4", state.StateCompleted))
		d, _ := restartDialog(t, h, 1, views.RestartCompleted, []views.AgainFile{{Name: "A talk.mp4", Exists: true, Site: true}})
		if s := d.Card().Semantics.Description; s != "Downex gets “\u2068A talk.mp4\u2069” again from its video page." {
			t.Errorf("description %q", s)
		}
	})

	t.Run("gives several completed downloads one choice, and says which files are gone", func(t *testing.T) {
		h, _ := harness(t, file(1, "a.zip", state.StateCompleted), file(2, "b.zip", state.StateCompleted), file(3, "c.zip", state.StateCompleted))
		d, _ := restartDialog(t, h, 3, views.RestartCompleted, []views.AgainFile{{Name: "a.zip", Exists: true}, {Name: "b.zip"}, {Name: "c.zip", Exists: true}})
		if s := d.Card().Semantics.Description; s != "Downex downloads 3 completed downloads again from their addresses." {
			t.Errorf("description %q", s)
		}
		var items []string
		for _, li := range d.Names().Children() {
			items = append(items, textOf(li))
		}
		if !slices.Equal(items, []string{"a.zip", "b.zip", "c.zip"}) {
			t.Errorf("list items %q", items)
		}
		if s := rowText(d.Choices(), 0); !strings.Contains(s, "Each new copy gets a numbered name, such as “\u2068a (1).zip\u2069”") {
			t.Errorf("keep both reads %q", s)
		}
		if g := d.GoneNote(); g == nil || g.Text() != "1 of them is no longer in its folder: it comes back under its own name." {
			t.Errorf("gone note %v", g)
		}
	})

	t.Run("with a mixed selection, restarts the others and asks only about the completed ones", func(t *testing.T) {
		h, _ := harness(t, file(1, "big.iso", state.StateFailed), file(2, "notes.pdf", state.StateCompleted))
		d, got := restartDialog(t, h, 2, views.RestartManual, []views.AgainFile{{Name: "notes.pdf", Exists: true}})
		if n := d.Card().Semantics.Name; n != "Restart downloads?" {
			t.Errorf("heading %q", n)
		}
		want := "Restarting 2 downloads discards the data received so far and downloads from the beginning. “\u2068notes.pdf\u2069” is complete: Downex downloads it again."
		if s := d.Card().Semantics.Description; s != want {
			t.Errorf("description %q", s)
		}
		g := d.Choices()
		if g.Semantics.Name != "The current file of “\u2068notes.pdf\u2069”" || len(g.Options) != 2 {
			t.Errorf("radiogroup %q with %d radios", g.Semantics.Name, len(g.Options))
		}
		_, submit := d.Buttons()
		if submit.Label().Text() != "Restart" {
			t.Errorf("submit %q", submit.Label().Text())
		}
		click(h, &submit.Element)
		if !slices.Equal(*got, []downexv1.ExistingFile{state.KeepBoth}) {
			t.Errorf("onconfirm %v", *got)
		}
	})

	t.Run("keeps the restart texts of downloads that are not complete", func(t *testing.T) {
		h, _ := harness(t, file(1, "big.iso", state.StateFailed))
		d, _ := restartDialog(t, h, 1, views.RestartValidatorChanged, nil)
		if n := d.Card().Semantics.Name; n != "Restart from the beginning?" {
			t.Errorf("heading %q", n)
		}
		if !strings.Contains(d.Card().Semantics.Description, "cannot be resumed") {
			t.Errorf("description %q", d.Card().Semantics.Description)
		}
		if d.Choices() != nil {
			t.Error("a choice is offered")
		}
		cancel, _ := d.Buttons()
		if cancel.Label().Text() != "Not now" {
			t.Errorf("cancel %q", cancel.Label().Text())
		}
	})
}

// textOf is an element's text: its labels' texts joined.
func textOf(e *kit.Element) string {
	var b []string
	var walk func(*kit.Element)
	walk = func(e *kit.Element) {
		if l, ok := e.Widget().(interface{ TextContent() string }); ok {
			b = append(b, l.TextContent())
			return
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(e)
	return strings.Join(b, " ")
}

// TestRestartBusyAndDefaultBody: Restart reads "Restarting…" while it runs and both buttons are
// unavailable; without a callback it is App.svelte's restartDownloads (store.restart with the
// choice).
func TestRestartBusyAndDefaultBody(t *testing.T) {
	h, daemon := harness(t, file(1, "big.iso", state.StateFailed))
	var seen []string
	daemon.Before = func(name string, _ ...any) error {
		if name == "resume" {
			seen = append(seen, name)
		}
		return nil
	}
	d := dialogs.NewRestart(h.App, views.RestartRequest{IDs: []int64{1}, Reason: views.RestartManual}, nil)
	show(h, d)
	if cancel, _ := d.Buttons(); !cancel.Autofocus || h.Window.Focused() != &cancel.Element {
		t.Error("Cancel is not focused first")
	}
	_, submit := d.Buttons()
	click(h, &submit.Element)
	if !slices.Equal(seen, []string{"resume"}) {
		t.Errorf("daemon calls %v", daemon.Calls())
	}
	if r := daemon.Args("resume"); len(r) == 0 {
		t.Error("no resume request")
	} else if req, ok := r[1].(*downexv1.ResumeRequest); !ok || !req.GetRestart() || req.GetId() != 1 {
		t.Errorf("resume %v", req)
	}
	if h.App.Dialogs.DialogOpen() {
		t.Error("the dialog stayed")
	}
}

// RemoveDialog (no vitest file of its own): its texts for one and several downloads, the
// check box that turns Remove into "Remove and delete" and the tone into the alert, Cancel
// focused first, and the removal (App.svelte's report(store.remove(…))).
func TestRemoveDialog(t *testing.T) {
	t.Run("one download", func(t *testing.T) {
		h, daemon := harness(t, file(1, "podcast.mp3", state.StateCompleted))
		d := dialogs.NewRemove(h.App, views.RemoveRequest{IDs: []int64{1}}, nil)
		show(h, d)
		if d.Card().Semantics.Name != "Remove download?" || d.Card().Semantics.Description != "The download is removed from the list." {
			t.Errorf("%q / %q", d.Card().Semantics.Name, d.Card().Semantics.Description)
		}
		if d.Card().Semantics.Role != kit.RoleAlertDialog {
			t.Error("not an alertdialog")
		}
		if d.Checkbox().Label.Text() != "Also delete the file from disk (including partial data)" {
			t.Errorf("check %q", d.Checkbox().Label.Text())
		}
		cancel, submit := d.Buttons()
		if h.Window.Focused() != &cancel.Element {
			t.Error("Cancel is not focused first")
		}
		click(h, &d.Checkbox().Box.Element)
		if !d.AlsoDelete() || submit.Label().Text() != "Remove and delete" {
			t.Errorf("checked %v, submit %q", d.AlsoDelete(), submit.Label().Text())
		}
		click(h, &submit.Element)
		args := daemon.Args("remove")
		if len(args) == 0 {
			t.Fatalf("calls %v", daemon.Calls())
		}
		if args[0].(int64) != 1 || !args[1].(bool) {
			t.Errorf("remove %v", args)
		}
		if h.App.Dialogs.DialogOpen() {
			t.Error("the dialog stayed")
		}
	})
	t.Run("several downloads: five names, then and N more", func(t *testing.T) {
		var list []*downexv1.Download
		var ids []int64
		for i := range 7 {
			list = append(list, file(int64(i+1), "f"+itoa(i+1)+".bin", state.StatePaused))
			ids = append(ids, int64(i+1))
		}
		h, _ := harness(t, list...)
		var got []bool
		d := dialogs.NewRemove(h.App, views.RemoveRequest{IDs: ids, DeleteFile: true}, func(del bool) { got = append(got, del) })
		show(h, d)
		if d.Card().Semantics.Name != "Remove 7 downloads?" || d.Card().Semantics.Description != "The downloads are removed from the list." {
			t.Errorf("%q / %q", d.Card().Semantics.Name, d.Card().Semantics.Description)
		}
		if d.Checkbox().Label.Text() != "Also delete the files from disk (including partial data)" || !d.Checkbox().Checked() {
			t.Errorf("check %q %v", d.Checkbox().Label.Text(), d.Checkbox().Checked())
		}
		cancel, submit := d.Buttons()
		if submit.Label().Text() != "Remove and delete" {
			t.Errorf("submit %q", submit.Label().Text())
		}
		_ = cancel
		h.Key(kit.KeyEscape, 0)
		h.Advance(200e6)
		if h.App.Dialogs.DialogOpen() || len(got) != 0 {
			t.Errorf("Escape: open %v, confirmed %v", h.App.Dialogs.DialogOpen(), got)
		}
	})
	t.Run("a failure is reported in a toast", func(t *testing.T) {
		h, daemon := harness(t, file(1, "a.bin", state.StatePaused))
		daemon.Before = func(name string, _ ...any) error {
			if name == "remove" {
				return connect.NewError(connect.CodePermissionDenied, errors.New("access denied"))
			}
			return nil
		}
		d := dialogs.NewRemove(h.App, views.RemoveRequest{IDs: []int64{1}}, nil)
		show(h, d)
		_, submit := d.Buttons()
		click(h, &submit.Element)
		items := h.App.Toasts.Items()
		if len(items) == 0 || items[len(items)-1].Message != "Could not remove \u2068a.bin\u2069: \u2068access denied\u2069" {
			t.Errorf("toasts %+v", items)
		}
	})
}

// itoa is strconv.Itoa for small numbers.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// OpenDialog (no vitest file of its own): the program's and the file's questions, the hidden
// controls' warning and the action that opens the file.
func TestOpenDialog(t *testing.T) {
	cases := []struct {
		name, file    string
		with          bool
		title, action string
		warn, danger  bool
	}{
		{"a program", "setup.exe", false, "Run this program?", "Run", false, true},
		{"a program, Open with…", "setup.exe", true, "Open this program with another app?", "Choose app…", false, true},
		{"a name with hidden controls", "invoice\u202Efdp.exe", false, "Run this program?", "Run", true, true},
		{"a document whose name hides its type", "report\u202Etxt.pdf", false, "Open this file?", "Open", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := harness(t, file(1, c.file, state.StateCompleted))
			opened := 0
			var confirm func()
			if c.with {
				confirm = func() { opened++ }
			}
			d := dialogs.NewOpen(h.App, views.OpenRequest{ID: 1, WithApp: c.with}, confirm)
			show(h, d)
			title, desc, action := dialogs.OpenTexts(c.danger, c.with)
			if d.Card().Semantics.Name != c.title || title != c.title || action != c.action || d.Card().Semantics.Description != desc {
				t.Errorf("title %q, action %q", d.Card().Semantics.Name, action)
			}
			if (d.Warning() != nil) != c.warn {
				t.Errorf("warning %v", d.Warning() != nil)
			}
			if d.Warning() != nil && d.Warning().Text() != dialogs.HiddenControlsWarning {
				t.Errorf("warning %q", d.Warning().Text())
			}
			if got := d.NameText().Text(); strings.ContainsRune(got, '\u202E') || c.warn != strings.Contains(got, "⟨U+202E⟩") {
				t.Errorf("name shown %q", got)
			}
			cancel, submit := d.Buttons()
			if h.Window.Focused() != &cancel.Element || submit.Label().Text() != c.action {
				t.Errorf("focus %v, submit %q", h.Window.Focused(), submit.Label().Text())
			}
			click(h, &submit.Element)
			if c.with && opened != 1 {
				t.Errorf("confirmed %d times", opened)
			}
			if !c.with {
				if fp := h.Platform.(*viewstest.FakePlatform); !slices.ContainsFunc(fp.Calls, func(s string) bool { return strings.HasPrefix(s, "OpenFile") }) {
					t.Errorf("platform calls %v", fp.Calls)
				}
			}
			if h.App.Dialogs.DialogOpen() {
				t.Error("the dialog stayed")
			}
		})
	}
	if !dialogs.NeedsOpenConfirm("a.exe") || !dialogs.NeedsOpenConfirm("a\u202Etxt.pdf") || dialogs.NeedsOpenConfirm("a.pdf") {
		t.Error("NeedsOpenConfirm")
	}
}

// MoveToQueueDialog in the store (QueueDialog.test.ts's two MoveToQueueDialog cases are in
// vitest_more_test.go; App.m6.test.ts's 'moves downloads to another queue from the context
// menu' is the composition's): the lead, the rows' states, the queue
// proposed, Move unavailable for the queue they are in, the refusal kept in the dialog, and
// moveToQueue's toast.
func TestMoveToQueueDialog(t *testing.T) {
	newH := func(t *testing.T) (*viewstest.Harness, *statetest.Daemon) {
		h, d := harness(t, file(1, "setup.exe", state.StateQueued), file(2, "b.zip", state.StateQueued))
		d.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" }))
		h.Main.Queues.Load()
		h.Settle()
		return h, d
	}
	t.Run("proposes the other queue and says where they are now", func(t *testing.T) {
		h, daemon := newH(t)
		m := dialogs.NewMove(h.App, views.MoveRequest{IDs: []int64{1}}, nil)
		show(h, m)
		if m.Lead().Text() != "Move setup.exe to the end of:" {
			t.Errorf("lead %q", m.Lead().Text())
		}
		if m.Current() != state.MainQueueID || m.Choice() != 2 {
			t.Errorf("current %d, choice %d", m.Current(), m.Choice())
		}
		var notes []string
		for _, o := range m.Rows().Options {
			notes = append(notes, o.Label+" "+o.Note)
		}
		if !slices.Equal(notes, []string{"Main queue (now)", "Night stopped"}) {
			t.Errorf("rows %q", notes)
		}
		if h.Window.Focused() != m.Rows().Children()[0] {
			t.Error("the first row is not focused first")
		}
		_, submit := m.Buttons()
		click(h, m.Rows().Children()[0])
		if !submit.Disabled {
			t.Error("Move is available for the queue they are in")
		}
		click(h, m.Rows().Children()[1])
		click(h, &submit.Element)
		args := daemon.Args("moveDownloads")
		if len(args) != 3 || args[1].(int64) != 2 || args[2].(int32) != -1 {
			t.Errorf("moveDownloads %v", args)
		}
		items := h.App.Toasts.Items()
		if len(items) == 0 || items[len(items)-1].Message != "Moved to \u2068Night\u2069" {
			t.Errorf("toasts %+v", items)
		}
		if h.App.Dialogs.DialogOpen() {
			t.Error("the dialog stayed")
		}
	})
	t.Run("several downloads in several queues; a refusal stays in the dialog", func(t *testing.T) {
		h, daemon := newH(t)
		h.Main.Downloads.AssignQueue([]int64{2}, 2)
		m := dialogs.NewMove(h.App, views.MoveRequest{IDs: []int64{1, 2}}, nil)
		show(h, m)
		if m.Lead().Text() != "Move 2 downloads to the end of:" || m.Current() != 0 || m.Choice() != state.MainQueueID {
			t.Errorf("lead %q, current %d, choice %d", m.Lead().Text(), m.Current(), m.Choice())
		}
		if m.Rows().Options[0].Note != "running" {
			t.Errorf("main queue note %q", m.Rows().Options[0].Note)
		}
		daemon.Before = func(name string, _ ...any) error {
			if name == "moveDownloads" {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("the queue is locked"))
			}
			return nil
		}
		_, submit := m.Buttons()
		click(h, &submit.Element)
		if f := m.Failure(); f == nil || !h.App.Dialogs.DialogOpen() || submit.Label().Text() != "Move" {
			t.Fatalf("failure %v, open %v", f, h.App.Dialogs.DialogOpen())
		}
	})
}
