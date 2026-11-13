// Package daemon is the queue manager behind downexd (docs/DAEMON-SPEC.md §1, §3–§6,
// docs/DESKTOP-SPEC.md §1, §2, §5, §7, §8, docs/M6-SPEC.md §1) and its Connect service
// implementations.
//
// # Queues
//
// Every download belongs to one queue (queue.go; store.QueueStore persists queues and
// their order). The main queue receives downloads added without a queue_id and runs by
// default; other queues run only while started (StartQueue, or their schedule). A queued
// download waits until its queue runs and has a free slot within the queue's
// max_concurrent and the global max_concurrent_downloads; running queues take turns
// (round robin). Speed limits chain global → queue → download (queue_limit.go: the
// engine sees the tightest limit of the chain, and a running download that changes
// queues takes its new queue's limit at once). StopQueue stops a queue's runs, which then
// wait in the queue again. The schedule engine (queue_scheduler.go, pure rules in
// queue_schedule.go) sleeps until the next start, stop or repeat of any schedule, read
// in local time through Config.Clock and Config.Location, applies starts missed while
// asleep if their window is open, derives the state again after the clock was set back,
// and arms Config.Wake for wake_computer. A running queue whose run completed or failed
// its last work (nothing waiting, running, awaiting confirmation or being changed) after
// a completion has finished; pausing, removing or moving its downloads never finishes
// it. Queues other than the main one then stop, and a post-queue action counts down
// PostActionDelay (queue_post.go; Watch post_action events, NoticePostAction) before
// Config.Quit or Config.Power performs it, unless CancelPostAction, new work or a
// deadline long past (the computer slept) stops it; one power action ends the other
// countdowns. Config.KeepAwake keeps the computer from idling into sleep while a queue
// with a post-queue action or a schedule has work (queue_awake.go). An unfiltered Watch
// also streams queue_updated, queue_removed and post_action events.
//
// # Desktop integration
//
// Add chooses a category for every download (category.go) and, without a requested
// dir, its folder. Browser hand-offs wait in model.StateAwaitingConfirmation, which the
// scheduler never starts, until Confirm queues or pauses them (or Remove cancels them);
// the confirmation UI is opened through Config.OpenConfirmation. PauseAll and ResumeAll
// act on every download. SetDownloadOptions changes a download's speed and connection
// limits (live for a running one: its limiter at once, the engine's connection limit as
// soon as it can; stored when the run stops). While a download runs, its rendering
// carries connection_info, one entry per worker connection of the latest progress event
// (never stored). A browser hand-off that starts at once opens its progress window
// through Config.OpenProgress while show_progress_window is on (docs/DESKTOP-SPEC.md
// §7). A download that SetDownloadOptions marked with completion_dialog (the UI does when
// it opens its progress window) opens the same window once when a run completes or fails
// it while show_complete_dialog is on: the download complete dialog (§8; never for a
// pause, removal or restart). Every run is timed with Config.Now: the first one's start
// is stored as started_at, and each run's duration is added to active_ms when it stops
// (renderings of a running download add the current run's time so far). In-process
// observers (the tray) use Subscribe for one-off notices (completed, failed, awaiting
// confirmation, settings changed), Watch for the download stream and Activity for queue
// totals. Config.LaunchAtLogin applies the launch_at_login setting.
//
// # Streaming media
//
// AddMedia and ProbeMedia (media.go, docs/MEDIA-SPEC.md §6) fetch and parse an HLS or
// DASH manifest through Config.Media (a MediaJobs; downexd adapts internal/mediajob) and
// refuse encrypted, DRM-protected and live streams with InvalidArgument and the codes
// "encrypted" and "live" (the message starts with the code; ErrorCodeMeta carries it
// too). A media download is a record of kind model.KindMedia: Request is the manifest
// request, Media the tracks, the selection (default: best video + its best audio;
// Confirm may change it), the expected container and the fragment progress, including
// the job's checkpoint (bitmaps of durable fragments, store migration 3). The scheduler
// runs it like any other download (its queue's limits, a child of its queue's
// limiter, intents for pause/resume/restart/remove/shutdown), through runMedia instead of
// the engine. Its fragments live in the part directory "<dir>/<filename>.dxparts/"
// (MediaPartsDir), the assembled output is written as "<dir>/<filename>.dxpart" and
// renamed to the final file; Remove with delete_file and a restart delete the part
// directory, and a completed run removes it.
//
// # Ownership
//
// A Manager owns one engine.Engine shared by every download, so per-host connection
// limits and 429/503 penalties apply across downloads; a global limiter.Limiter whose
// children throttle the individual downloads; the daemon settings; and one run
// goroutine per running download. The store is supplied by the caller, who closes it
// after Shutdown returns.
//
// While a download runs, only the engine (for a media download, its run goroutine,
// which stores the job's checkpoints) writes its record. Every other change
// (pause, resume, restart, remove, shutdown) is requested by setting the run's intent
// and cancelling its context; the run goroutine applies the intent once engine.Run has
// returned and the engine's final checkpoint is committed. Client operations on one
// download are serialised by a per-download operation slot, so two of them never
// interleave their store writes.
//
// # Locks
//
// Manager.mu guards the in-memory index (entries, queues, waiting downloads, settings,
// counters). It is held only for short, non-blocking work and never across store, file
// or network I/O, except for the non-blocking acquisition of a per-download lock file.
// Manager.queueMu serialises the queue operations that write the store; it is taken
// before Manager.mu, never while holding it. The event hub's and the notice hub's mutexes
// may be taken while Manager.mu is held, never the other way round. Engine
// progress callbacks take neither: they store the latest progress atomically and
// publish to the hub, whose subscribers coalesce updates per download, so a slow
// Watch client can never stall a download.
package daemon
