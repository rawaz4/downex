package dialogs

import (
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/dialogs/textrun"
)

// Move is MoveToQueueDialog.svelte (DESIGN-SYSTEM.md §4.10): "Move <name> to the end of:"
// and the queues as selectable rows, each a radio named after its queue (the arrows move
// between them), with the queue's icon (--accent while it runs), its state ("(now)" for the
// queue the downloads are in, else running or stopped) and a Mint check on the chosen one;
// Cancel and Move, which is unavailable for the queue they are in and reads "Moving…" while
// the move runs. A refusal shows the daemon's message in a .note.fail and keeps the dialog.
type Move struct {
	*views.Dialog

	app     *views.App
	ids     []int64
	queues  []*downexv1.Queue
	current int64 // 0: the downloads are in several queues (null)
	move    func(queueID int64) error

	lead    *textrun.Line
	rows    *widgets.RadioRows
	failure *views.Note
	cancel  *widgets.Button
	submit  *widgets.Button

	choice int64
	busy   bool
}

// NewMove makes Move to queue for req, over App.QueueChoices (the current queue is the one
// every download is in). move runs on App.Go with the chosen queue; the dialog closes when it
// returns nil and shows its error otherwise. nil is App.svelte's moveToQueue: queues.move to
// the end, store.assignQueue and the "Moved to …" toast.
func NewMove(app *views.App, req views.MoveRequest, move func(queueID int64) error) *Move {
	m := &Move{app: app, ids: append([]int64(nil), req.IDs...), queues: app.QueueChoices(), move: move}
	if m.move == nil {
		m.move = m.moveToQueue
	}
	m.current = currentQueue(app, m.ids)
	m.choice = initialChoice(m.queues, m.current)
	nameList := namesOf(app, m.ids)

	m.Dialog = app.NewDialog(views.ModalMove, widgets.DialogOptions{Title: "Move to queue", Size: widgets.DialogSm, OnSubmit: m.Submit})

	what := itoa(len(nameList)) + " downloads"
	if len(nameList) == 1 {
		what = text.EscapeBidi(nameList[0])
	}
	bold := views.FontBody
	bold.Weight = 600
	m.lead = textrun.New(LeadParts(what), views.FontBody, bold, views.InkInk2, views.InkInk)
	m.lead.Wrap = true
	m.Body.Add(m.lead)

	opts := make([]widgets.RadioOption, len(m.queues))
	for i, q := range m.queues {
		opts[i] = widgets.RadioOption{Value: queueValue(q.GetId()), Label: text.EscapeBidi(q.GetName()), Note: NoteOf(q, m.current), Icon: "queue"}
	}
	m.rows = widgets.NewRadioRows("Queue", opts, queueValue(m.choice), true, m.choose)
	m.rows.Semantics.Role = kit.RoleGroup // <fieldset> with its sr-only <legend>Queue</legend>
	m.rows.Semantics.Fieldset = true      // [ax]
	m.tuneRows()
	if kids := m.rows.Children(); len(kids) > 0 {
		kids[0].Autofocus = true // data-autofocus={i === 0 ? '' : undefined}
	}
	m.Body.Add(m.rows)

	m.cancel = footButton("Cancel", widgets.Secondary, m.Cancel)
	m.submit = footButton("Move", widgets.Primary, m.Submit)
	m.SetFooter(nil, m.cancel, m.submit)
	m.syncSubmit()
	return m
}

// LeadParts are the lead's runs: "Move ", the name (or "N downloads") in bold, " to the end
// of:".
func LeadParts(what string) []textrun.Part {
	return []textrun.Part{{Text: "Move "}, {Text: what, Mark: true}, {Text: " to the end of:"}}
}

// NoteOf is what a queue's row says beside its name: where the downloads are now, else the
// queue's state.
func NoteOf(q *downexv1.Queue, current int64) string {
	switch {
	case current != 0 && q.GetId() == current:
		return "(now)"
	case q.GetRunning():
		return "running"
	}
	return "stopped"
}

// currentQueue is the queue every download is in (queueOf: 0 is the main queue), 0 when they
// are in several.
func currentQueue(app *views.App, ids []int64) int64 {
	if app.Main == nil {
		return 0
	}
	var cur int64
	for i, id := range ids {
		q := state.MainQueueID
		if d, ok := app.Main.Downloads.Get(id); ok && d.GetQueueId() > 0 {
			q = d.GetQueueId()
		}
		if i > 0 && q != cur {
			return 0
		}
		cur = q
	}
	return cur
}

// initialChoice is the first queue the downloads are not in, else the first queue, else 0.
func initialChoice(qs []*downexv1.Queue, current int64) int64 {
	for _, q := range qs {
		if q.GetId() != current {
			return q.GetId()
		}
	}
	if len(qs) > 0 {
		return qs[0].GetId()
	}
	return 0
}

func queueValue(id int64) string { return strconv.FormatInt(id, 10) }

// tuneRows gives the rows Move to queue's own looks where RadioRows' inline rows differ (see
// fixRows): the state in --faint (.qnote) rather than --muted, and the icon in --accent for a
// running queue (.qicon.running) as for the chosen one.
func (m *Move) tuneRows() {
	fixRows(m.rows, func(i int, row *kit.Element, icon *widgets.Icon, note *kit.Label) {
		if i >= len(m.queues) {
			return
		}
		value, running := queueValue(m.queues[i].GetId()), m.queues[i].GetRunning()
		icon.SetColor(func(t *tokens.Theme) kit.Color {
			if running || m.rows.Value == value {
				return t.Accent
			}
			return t.Muted
		})
		faint := views.NewText(note.Text(), note.Font(), views.InkFaint, note.Options())
		faint.Style, faint.Semantics = note.Style, note.Semantics
		row.Remove(note)
		row.Insert(2, faint)
	})
}

func (m *Move) choose(v string) {
	id, _ := strconv.ParseInt(v, 10, 64)
	m.choice = id
	m.syncSubmit()
}

// isCurrent reports the queue the downloads are in (none when they are in several).
func (m *Move) isCurrent(id int64) bool { return m.current != 0 && id == m.current }

func (m *Move) syncSubmit() {
	m.submit.SetDisabled(m.busy || m.isCurrent(m.choice), false)
}

// Choice is the queue chosen.
func (m *Move) Choice() int64 { return m.choice }

// Current is the queue the downloads are in (0: several).
func (m *Move) Current() int64 { return m.current }

// Rows are the queue rows.
func (m *Move) Rows() *widgets.RadioRows { return m.rows }

// Lead is the line above the rows.
func (m *Move) Lead() *textrun.Line { return m.lead }

// Failure is the refusal shown (nil for none).
func (m *Move) Failure() *views.Note { return m.failure }

// Buttons are Cancel and Move.
func (m *Move) Buttons() (cancel, submit *widgets.Button) { return m.cancel, m.submit }

// Cancel is the Cancel button: the dialog goes at once.
func (m *Move) Cancel() {
	if !m.busy {
		m.Dialog.Remove()
	}
}

// Submit moves the downloads to the end of the chosen queue.
func (m *Move) Submit() {
	if m.busy || m.choice <= 0 || !m.Shown() || m.isCurrent(m.choice) {
		return
	}
	m.setBusy(true)
	m.setFailure("")
	queue := m.choice
	m.app.Go(func() {
		err := m.move(queue)
		m.app.UI(func() {
			m.setBusy(false)
			if err != nil {
				m.setFailure(views.ErrorMessage(err))
				return
			}
			m.Dialog.Remove()
		})
	})
}

func (m *Move) setBusy(b bool) {
	m.busy = b
	m.SetBusy(b)
	m.cancel.SetDisabled(b, false)
	m.syncSubmit()
	if b {
		m.submit.SetText("Moving…")
	} else {
		m.submit.SetText("Move")
	}
}

// setFailure shows the daemon's refusal under the rows ("" hides it).
func (m *Move) setFailure(msg string) {
	if m.failure != nil {
		m.Body.Remove(m.failure)
		m.failure = nil
	}
	if msg == "" {
		return
	}
	m.failure = views.NewNote(views.NoteFail, text.EscapeBidi(msg))
	m.failure.Semantics.Role = kit.RoleAlert
	m.Body.Add(m.failure)
}

// moveToQueue is App.svelte's moveToQueue.
func (m *Move) moveToQueue(queueID int64) error {
	a := m.app
	if a.Main == nil {
		return nil
	}
	ctx, cancel := a.CallContext()
	defer cancel()
	if _, err := a.Main.Queues.Move(ctx, m.ids, queueID, -1); err != nil {
		return err
	}
	a.Main.Downloads.AssignQueue(m.ids, queueID)
	name := text.Isolate(a.Main.Queues.NameOf(queueID))
	if len(m.ids) == 1 {
		a.Success("Moved to " + name)
	} else {
		a.Success(itoa(len(m.ids)) + " downloads moved to " + name)
	}
	return nil
}

// BodyPane is the dialog's scrolling body.
func (m *Move) BodyPane() (*widgets.ScrollPane, bool) { return bodyPane(m.Dialog) }
