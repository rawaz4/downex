// Package host is the Wails-free host of Downex's native window: everything a window
// process needs from the system and from downexd that is not drawing. It is copied and
// adapted from today's window (ui/*.go, package main), whose behaviour it keeps; today's
// window does not import it.
//
//   - the session (session.go): one window process's lifecycle as today's App keeps it.
//     NewSession reads the command line, Start forwards the download events (a progress
//     window's: its own download only), Close stops them within ShutdownWait, and
//     SecondInstance handles a later start. CallContext bounds one call (CallTimeout).
//   - the launch contract (launch.go): which view a process shows (ViewOf: "--progress
//     <id>"), the "--confirm <id>" requests (ConfirmArgs, Confirmations), the
//     single-instance ids of the main and the progress windows, the hand-off of a second
//     start to the running window in the names and WM_COPYDATA payload Wails v2.17 uses
//     (HandoffNamesOf, EncodeHandoff, DecodeHandoff), the progress window's program
//     (StartProgressWindow, ProgressProgram) and the foreground hand-off
//     (AllowForegroundHandoff);
//   - the daemon client (daemon.go, daemon_m6.go, daemon_site.go): ClientDaemon reaches
//     downexd with internal/client's Connect clients and downex.v1 messages directly; its
//     first contact may start downexd, later contacts never do, an elevated window never
//     does, and $DOWNEX_ENDPOINT is honoured (ipc.Endpoint);
//   - the event bridge (events.go): Watch with an id filter, a 1-10 s backoff, snapshot
//     reconciliation, and not_found reported as removed;
//   - files (files.go, shell_<os>.go): open, open with, reveal, Downex's own folders, the
//     free space, and the confirmation before a program or a file with active content
//     opens; each Windows shell call runs on an OS thread of its own with COM in a
//     single-threaded apartment, as today;
//   - settings (settings.go): edited on the message GetSettings returned (EditSettings),
//     or a draft saved with the unknown fields of the daemon's current settings
//     (SaveSettings, KeepUnknown), so fields a newer downexd has survive;
//   - the stored theme choice (theme.go, ui-theme.json) and the main window's size
//     (winstate.go, ui-window.json's format at a path the caller names);
//   - About (about.go): the build, diagnostics without private data, the open-source
//     notices; and ui.log with its rotation (log.go).
//
// Errors are Connect errors (connect.CodeOf); ErrorOf gives the "<code>: <message>" text
// today's window shows. Nothing here draws, and nothing imports a window toolkit: the
// package builds with CGO_ENABLED=0 for windows, darwin and linux.
package host
