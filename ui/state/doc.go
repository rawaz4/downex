// Package state holds the native window's stores, ported from
// ui/frontend/src/lib/stores: the download list (DownloadsStore), a progress window's
// download (ProgressStore), the queues with their post-queue countdowns (QueuesStore),
// yt-dlp and Deno (ToolsStore), the transient notices (Toasts), and the theme and list
// density choices (Theme, Density).
//
// The stores are fed with downex.v1 messages: Watch batches through an Events hub (the
// ui/host event bridge emits into a Hub), and the answers of the daemon calls they make
// through a host.Daemon. Every store is safe for concurrent use: events arrive on the
// bridge's goroutine, calls run off the UI thread, and the view reads on its own. A
// store reports each change through its Changes signal; the view then reads what it shows
// (a read never blocks on a call, only on the store's own lock).
//
// Messages a store hands out are never modified afterwards, so a view may keep them for
// as long as it draws them.
package state
