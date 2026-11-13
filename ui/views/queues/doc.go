// Package queues is the Queues dialog (plan step 16): QueueDialog.svelte and its parts,
// ScheduleEditor.svelte and the DateTimeField it uses (DESIGN-SYSTEM.md §4.10). It lists the
// queues in a column like the sidebar's and edits the chosen one: its heading and state with
// Start/Stop, its settings (name, simultaneous downloads, speed limit and unit, what happens
// when it is done, offered by what the system can do), its schedule (24-hour time and date
// fields, weekday chips or one date, the retry interval and, where the computer can be woken,
// waking it) and the order of its downloads (dragged, or moved with the arrows, through
// MoveDownloads). Delete asks inline first; nothing changes while downexd is unreachable or a
// call runs.
//
// The composition opens it from App.Open.Queues:
//
//	app.Open.Queues = func(r views.QueuesRequest) { app.Dialogs.Show(queues.New(app, r)) }
//
// The dialog reads the queues and downloads from app.Main and follows their changes while it is
// shown; its calls go through app.Go with app.CallContext, and their answers come back on the
// UI thread. Wording, sizes, colours and rules are today's (QueueDialog.svelte's markup and
// CSS, lib/queues.ts through ui/core/queues).
package queues
