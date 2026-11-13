// Package views is the native window's screens: today's Svelte window (ui/frontend/src)
// rebuilt on ui/kit and its widgets, with the same layout, sizes, spacing, colours, fonts,
// icons, wording, motion and keyboard. This root package is the base every screen builds on;
// the screens live in its subpackages, one lane each:
//
//	shell      the main window's frame: title bar, sidebar, page header, banners, status bar
//	app        the composition (App.svelte): fills the Openers and Actions, the confirm queue,
//	           arrivals, the context menu; the only package that imports the others
//	downloads  the download grid (DownloadTable, DownloadRow) and the details pane
//	progress   the progress window (ProgressWindow.svelte)
//	dialogs    Remove, Restart / Download again, Open confirmation, Move to queue, Confirm
//	palette    the command palette (CommandPalette.svelte)
//	add        the Add dialog (AddDialog.svelte)
//	settings   the Settings dialog and its pages
//	queues     the Queues dialog (QueueDialog.svelte)
//
// The root holds the contracts the lanes code against, so that no lane imports another:
//
//   - App: what every screen is handed: the ui/state stores, the daemon and the platform's
//     calls, toasts and notices, the prefs, theme and density, the clock, the capabilities,
//     the window, its layers and its dialog host.
//   - DialogHost and Modal: one modal dialog at a time, as App.svelte's `dialog` state, plus
//     the palette beside it; the main content inert meanwhile; the stacking of toasts,
//     notices and banners as today's z-indexes put them (Layers).
//   - Openers and Actions: typed function fields for every dialog and screen the window
//     opens and every cross-screen action; the composition fills them in.
//   - Commands: the command registry the palette, the keyboard shortcuts and the context
//     menu share (App.svelte's commands, onWindowKeydown and menuItems).
//   - The slots the shell composes (DownloadGrid, DetailsPane, StatusBar).
//   - Shared screen-level styles from app.css (Field, LabelRow, Hint, ErrorText, Caps, Note,
//     Mono, VSep, Tag, Codec, Spinner) and helpers several screens repeat.
//
// The package is platform-free. Its test harness is views/viewstest.
package views
