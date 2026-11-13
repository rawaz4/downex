package queues

import (
	"context"
	"slices"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// busyKind is the call in flight (QueueDialog.svelte's busy: 'save' | 'delete' | 'run' |
// 'move' | null).
type busyKind uint8

const (
	notBusy busyKind = iota
	busySave
	busyDelete
	busyRun
	busyMove
)

// MaxRows is how many of a queue's downloads the order lists; more are summarised.
const MaxRows = 200

// Dialog is the Queues dialog: a views.Dialog of kind ModalQueues (a views.Modal), shown with
// app.Dialogs.Show.
type Dialog struct {
	*views.Dialog
	app  *views.App
	caps queues.Capabilities

	all   []*downexv1.Queue // every queue, the main queue first (live)
	stale bool
	// selected is the queue being edited; 0 is a new one, not created yet (queue ids start at 1).
	selected      int64
	form          queues.Form
	attempted     bool
	busy          busyKind
	failure       string
	confirmDelete bool
	errs          queues.FormErrors // what is shown: the result's errors once a save was tried
	valid         bool
	spec          *downexv1.Queue

	icons    iconHolder
	shell    *shell
	qlist    *queueList
	ed       *editor
	head     *headSection
	settings *settingsSection
	sched    *scheduleSection
	order    *orderSection
	confirm  *views.Note

	delGroup  *kit.Element
	delBtn    *widgets.Button
	keepBtn   *widgets.Button
	attention *footMsg
	failMsg   *footMsg
	closeBtn  *widgets.Button
	saveBtn   *widgets.Button

	cancels []func()
	shown   bool
}

// New makes the Queues dialog showing req's queue first (0: the main queue), as App.svelte's
// openQueues does. Show it with app.Dialogs.Show.
func New(app *views.App, req views.QueuesRequest) *Dialog {
	d := &Dialog{app: app, caps: app.QueueCaps()}
	d.Dialog = app.NewDialog(views.ModalQueues, widgets.DialogOptions{
		Title: "Queues", Width: kit.Rem(72), FixedHeight: kit.Rem(48), Flush: true, OnSubmit: d.submit,
	})
	d.all = app.QueueChoices()
	d.stale = app.Stale()
	var first *downexv1.Queue
	if req.Queue != 0 {
		first = d.queueByID(req.Queue)
	}
	if first == nil {
		first = d.mainQueue()
	}
	if first == nil && len(d.all) > 0 {
		first = d.all[0]
	}
	d.selected = first.GetId()
	d.form = queues.NewForm(first, d.now())
	d.build()
	d.loadForm()
	d.refresh()
	return d
}

// now is the wall time (a one-time schedule's proposed date is today's, in local time).
func (d *Dialog) now() time.Time {
	if d.app.Clock == nil {
		return time.Now()
	}
	return d.app.Clock.Now().Local()
}

// queue is the queue being edited, or nil for a new one (or one deleted elsewhere).
func (d *Dialog) queue() *downexv1.Queue {
	if d.selected == 0 {
		return nil
	}
	return d.queueByID(d.selected)
}

func (d *Dialog) queueByID(id int64) *downexv1.Queue {
	for _, q := range d.all {
		if q.GetId() == id {
			return q
		}
	}
	return nil
}

func (d *Dialog) mainQueue() *downexv1.Queue {
	for _, q := range d.all {
		if q.GetIsMain() {
			return q
		}
	}
	return nil
}

// downloadOf is a download of the list (the order's rows).
func (d *Dialog) downloadOf(id int64) *downexv1.Download {
	if d.app.Main == nil {
		return nil
	}
	x, _ := d.app.Main.Downloads.Get(id)
	return x
}

// pendingCount is how many of a queue's downloads wait (completed ones are not counted).
func (d *Dialog) pendingCount(q *downexv1.Queue) int {
	n := 0
	for _, id := range q.GetDownloadIds() {
		if x := d.downloadOf(id); x != nil && x.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			n++
		}
	}
	return n
}

// order is the queue's downloads; pending those that still matter for its order.
func (d *Dialog) orderIDs() []int64 { return d.queue().GetDownloadIds() }

func (d *Dialog) pendingIDs() []int64 {
	var out []int64
	for _, id := range d.orderIDs() {
		if x := d.downloadOf(id); x != nil && x.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			out = append(out, id)
		}
	}
	return out
}

func (d *Dialog) shownIDs() []int64 {
	p := d.pendingIDs()
	if len(p) > MaxRows {
		p = p[:MaxRows]
	}
	return p
}

// locked: nothing can be changed (downexd is unreachable, or a call runs).
func (d *Dialog) locked() bool { return d.stale || d.busy != notBusy }

// others are the queues other than the one being edited (for the name).
func (d *Dialog) others() []*downexv1.Queue {
	var out []*downexv1.Queue
	for _, q := range d.all {
		if q.GetId() != d.selected {
			out = append(out, q)
		}
	}
	return out
}

// attach starts following the stores while the dialog is in its window.
func (d *Dialog) attach() {
	if d.shown {
		return
	}
	d.shown = true
	if m := d.app.Main; m != nil {
		d.cancels = append(d.cancels,
			d.app.Watch(&m.Queues.Changes, d.storesChanged),
			d.app.Watch(&m.Downloads.Changes, d.storesChanged))
	}
	d.storesChanged()
}

// detach stops following them and releases the icons' paths.
func (d *Dialog) detach() {
	d.shown = false
	for _, c := range d.cancels {
		c()
	}
	d.cancels = nil
	d.icons.release()
}

// storesChanged reads the queues, the downloads and whether downexd is reachable again.
func (d *Dialog) storesChanged() {
	d.all = d.app.QueueChoices()
	d.stale = d.app.Stale()
	// The queue being edited was deleted elsewhere: show the main queue.
	if d.selected != 0 && d.queue() == nil && d.busy == notBusy {
		next := d.mainQueue()
		if next == nil && len(d.all) > 0 {
			next = d.all[0]
		}
		d.selectQueue(next.GetId())
		return
	}
	d.refresh()
}

// selectQueue edits another queue (0: a new one), with a fresh form.
func (d *Dialog) selectQueue(id int64) {
	d.selected = id
	d.form = queues.NewForm(d.queue(), d.now())
	d.attempted = false
	d.failure = ""
	d.confirmDelete = false
	d.loadForm()
	d.refresh()
}

// newQueue edits a new queue and puts the keyboard in its name.
func (d *Dialog) newQueue() {
	d.selectQueue(0)
	if w := d.Window(); w != nil {
		w.Focus(d.settings.name)
	}
}

// formChanged re-validates after an edit of the form.
func (d *Dialog) formChanged() {
	d.validate()
	d.refreshErrors()
	d.refreshFooter()
}

func (d *Dialog) validate() {
	spec, errs, ok := queues.ValidateForm(d.form, d.others(), d.caps, d.queue())
	d.spec, d.valid = spec, ok
	d.errs = queues.FormErrors{}
	if d.attempted && !ok {
		d.errs = errs
	}
}

// run makes a call off the UI thread with the dialog busy; then runs on the UI thread with its
// answer (failure shows the refusal).
func (d *Dialog) run(kind busyKind, call func(ctx context.Context) (*downexv1.Queue, error), then func(q *downexv1.Queue, ok bool)) {
	d.busy = kind
	d.failure = ""
	d.refresh()
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		q, err := call(ctx)
		cancel()
		d.app.UI(func() {
			d.busy = notBusy
			if err != nil {
				d.failure = views.ErrorMessage(err)
				if d.failure == "" {
					d.failure = err.Error()
				}
			}
			d.all = d.app.QueueChoices()
			d.stale = d.app.Stale()
			if then != nil {
				then(q, err == nil)
			}
			d.storesChanged()
		})
	})
}

// submit saves the form: a new queue is created, the chosen one updated (Save queue, Enter in a
// field). What needs attention is shown instead, the keyboard on the first such field.
func (d *Dialog) submit() {
	if d.locked() {
		return
	}
	d.attempted = true
	d.validate()
	if !d.valid {
		d.refreshErrors()
		d.refreshFooter()
		if w := d.Window(); w != nil {
			if f := d.firstInvalid(); f != nil {
				w.Focus(f)
			}
		}
		return
	}
	spec, id := d.spec, d.selected
	qs := d.app.Main.Queues
	d.run(busySave, func(ctx context.Context) (*downexv1.Queue, error) {
		if id == 0 {
			return qs.Create(ctx, spec)
		}
		return qs.Update(ctx, id, spec)
	}, func(saved *downexv1.Queue, ok bool) {
		if !ok || saved == nil {
			return
		}
		d.selected = saved.GetId()
		d.all = d.app.QueueChoices()
		if d.queueByID(saved.GetId()) == nil {
			d.all = append(slices.Clone(d.all), saved)
		}
		d.form = queues.NewForm(saved, d.now())
		d.attempted = false
		d.loadForm()
	})
}

// toggleRunning starts or stops the chosen queue.
func (d *Dialog) toggleRunning() {
	q := d.queue()
	if q == nil || d.locked() {
		return
	}
	id, running := q.GetId(), q.GetRunning()
	qs := d.app.Main.Queues
	d.run(busyRun, func(ctx context.Context) (*downexv1.Queue, error) {
		if running {
			return qs.StopQueue(ctx, id)
		}
		return qs.StartQueue(ctx, id)
	}, nil)
}

// remove asks first (inline), then deletes the chosen queue: its downloads move to the end of
// the main queue, and the main queue is shown.
func (d *Dialog) remove() {
	q := d.queue()
	if q == nil || q.GetIsMain() || d.locked() {
		return
	}
	if !d.confirmDelete {
		d.confirmDelete = true
		d.refresh()
		return
	}
	id := q.GetId()
	qs := d.app.Main.Queues
	d.run(busyDelete, func(ctx context.Context) (*downexv1.Queue, error) {
		return nil, qs.Remove(ctx, id)
	}, func(_ *downexv1.Queue, ok bool) {
		d.confirmDelete = false
		if ok {
			main := int64(queues.MainQueueID)
			if m := d.mainQueue(); m != nil {
				main = m.GetId()
			}
			d.selectQueue(main)
		}
	})
}

func (d *Dialog) keep() {
	d.confirmDelete = false
	d.refresh()
}

// changesOrder reports whether putting id before before (End: the end) changes the order.
func (d *Dialog) changesOrder(id, before int64) bool {
	if id == before {
		return false
	}
	order := d.orderIDs()
	next := queues.Reorder(order, []int64{id}, before)
	for i, x := range next {
		if i >= len(order) || x != order[i] {
			return true
		}
	}
	return false
}

// moveBefore puts id before before (End: the end of the queue) through MoveDownloads; then
// runs after it was answered.
func (d *Dialog) moveBefore(id, before int64, then func()) {
	q := d.queue()
	if q == nil || d.locked() || !d.changesOrder(id, before) {
		return
	}
	qid := q.GetId()
	was := slices.Clone(q.GetDownloadIds())
	position := queues.InsertionIndex(was, []int64{id}, before)
	ids := []int64{id}
	m := d.app.Main
	d.run(busyMove, func(ctx context.Context) (*downexv1.Queue, error) {
		moved, err := m.Queues.Move(ctx, ids, qid, position)
		if err == nil {
			m.Downloads.AssignQueue(ids, qid)
		}
		return moved, err
	}, func(moved *downexv1.Queue, ok bool) {
		if ok {
			d.refreshPositions(was, moved.GetDownloadIds())
		}
		if then != nil {
			d.app.UI(then)
		}
	})
}

// refreshPositions reads again the waiting downloads whose place in the queue a move changed
// (was → now), off the UI thread: their queue_position ("Queued #n") changed with it, though
// downexd publishes an update only for the download that moved. The store keeps a report it
// learned after a read was sent (state.DownloadsStore.Reread).
func (d *Dialog) refreshPositions(was, now []int64) {
	at := make(map[int64]int, len(was))
	for i, id := range was {
		at[id] = i
	}
	var stale []int64
	for i, id := range now {
		if j, ok := at[id]; ok && j == i {
			continue
		}
		if x, ok := d.app.Main.Downloads.Get(id); ok && x.GetState() == state.StateQueued {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return
	}
	store := d.app.Main.Downloads
	d.app.Go(func() {
		ctx, cancel := d.app.CallContext()
		defer cancel()
		store.Reread(ctx, stale)
	})
}

// step moves a row one place up (-1) or down (+1) with its arrow; the keyboard stays on the
// row that moved (on its other arrow once it reached an end).
func (d *Dialog) step(id int64, delta int) {
	target, ok := queues.NeighbourTarget(d.orderIDs(), d.shownIDs(), id, delta)
	if !ok {
		return
	}
	d.moveBefore(id, target, func() { d.order.focusArrow(id, delta) })
}
