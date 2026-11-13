package dialogs_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/viewstest"
)

// App.test.ts describe 'RestartDialog': 'isolates the name inside the sentence'.
func TestRestartDialogIsolatesName(t *testing.T) {
	t.Run("isolates the name inside the sentence", func(t *testing.T) {
		h, _ := harness(t, file(1, "(final) report.pdf", state.StateFailed))
		d, _ := restartDialog(t, h, 1, views.RestartManual, nil)
		if d.Card().Semantics.Role != kit.RoleAlertDialog {
			t.Error("not an alertdialog")
		}
		if s := d.Card().Semantics.Description; !strings.Contains(s, "“⁨(final) report.pdf⁩”") {
			t.Errorf("description %q", s)
		}
	})
}

// QueueDialog.test.ts describe 'MoveToQueueDialog': the vitest's open(current = 1) is
// names ['one.iso'], the queues Main queue (running), Night and Weekend, and a recording
// onmove; onclose is the dialog leaving the window.
func TestMoveToQueueDialogVitest(t *testing.T) {
	// setup is open(1): one.iso in the main queue, beside Night and Weekend.
	setup := func(t *testing.T) *viewstest.Harness {
		t.Helper()
		h, d := harness(t, file(1, "one.iso", state.StateQueued))
		d.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" }))
		d.SetQueue(statetest.Queue(3, func(q *downexv1.Queue) { q.Name = "Weekend" }))
		h.Main.Queues.Load()
		h.Settle()
		return h
	}

	t.Run("names each radio after its queue, proposes another queue and says where the downloads are", func(t *testing.T) {
		h := setup(t)
		var moved []int64
		m := dialogs.NewMove(h.App, views.MoveRequest{IDs: []int64{1}}, func(q int64) error { moved = append(moved, q); return nil })
		show(h, m)
		if n := m.Card().Semantics.Name; n != "Move to queue" {
			t.Errorf("dialog named %q", n)
		}
		rows := m.Rows().Children()
		var names []string
		var checked []bool
		for i, r := range rows {
			names = append(names, r.Semantics.Name)
			checked = append(checked, r.Semantics.Role == kit.RoleRadioButton && m.Rows().Options[i].Value == m.Rows().Value)
		}
		// Each radio's name is exactly its queue's name.
		if !slices.Equal(names, []string{"Main queue", "Night", "Weekend"}) {
			t.Errorf("radios %q", names)
		}
		if !slices.Equal(checked, []bool{false, true, false}) {
			t.Errorf("checked %v", checked)
		}
		if rows[0].Semantics.Description == "" {
			t.Error("Main queue's radio has no description (aria-describedby)")
		}
		if !slices.ContainsFunc(m.Rows().Options, func(o widgets.RadioOption) bool { return o.Note == "(now)" }) {
			t.Error("no (now)")
		}
		// The chosen row is marked (not by its tint alone: a check too).
		kids := rows[1].Children()
		tick, ok := kids[len(kids)-1].Widget().(*widgets.Icon)
		if !ok || tick.Name != icons.Check || m.Rows().Value != "2" {
			t.Errorf("Night's row has no check (%v)", kids[len(kids)-1].Widget())
		}
		click(h, rows[2])
		if m.Rows().Value != "3" || m.Choice() != 3 {
			t.Errorf("Weekend not checked: %q", m.Rows().Value)
		}
		_, submit := m.Buttons()
		click(h, &submit.Element)
		h.Settle()
		if !slices.Equal(moved, []int64{3}) {
			t.Errorf("onmove %v", moved)
		}
		if h.App.Dialogs.DialogOpen() {
			t.Error("onclose was not called")
		}
	})

	t.Run("cannot move the downloads to the queue they are in, and reports a refusal", func(t *testing.T) {
		h := setup(t)
		reject := errors.New("not_found: queue 2 not found")
		m := dialogs.NewMove(h.App, views.MoveRequest{IDs: []int64{1}}, func(int64) error {
			err := reject
			reject = nil
			return err
		})
		show(h, m)
		rows := m.Rows().Children()
		click(h, rows[0])
		_, submit := m.Buttons()
		if !submit.Disabled {
			t.Error("Move is available for the queue they are in")
		}
		click(h, rows[1])
		click(h, &submit.Element)
		h.Settle()
		f := m.Failure()
		if f == nil || f.Semantics.Role != kit.RoleAlert || !strings.Contains(f.Text.Text(), "queue 2 not found") {
			t.Fatalf("alert %v", f)
		}
		if !h.App.Dialogs.DialogOpen() {
			t.Error("onclose was called")
		}
	})
}
