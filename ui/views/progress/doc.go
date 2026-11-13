// Package progress is the progress window of one download (DESKTOP-SPEC §7), the process
// "downex-ui --progress <id>": ProgressWindow.svelte ported to the kit. It is also the
// download complete dialog (§8), which is what a launch for a completed download shows.
//
// The window shows, from the top: the offline banner while downexd cannot be reached; the
// header (the file's tile, its name over its state, the big percentage); the connection map
// (SegmentBar lg); the Status and Speed limit tabs over one panel; an action error; the
// actions (Show/Hide details, Pause/Resume, Cancel/Close, or Retry/Start over and Close for
// a failure); and, expanded, the details: the segment map (a fragments bar for a stream) and
// the table of connections. In place of all that it shows the loading, "no longer exists" and
// "could not read" views, or the download complete view (Open, Open with…, Open folder,
// Close, "Don’t show this dialog again").
//
// The window fits what it shows (DESIGN-SYSTEM.md §4.8): the complete view and the progress
// view without its details get the height their content needs, plus the frame and a little
// slack, so that neither shows a needless scroll bar at 100, 125 or 150 % scaling; it fits
// again when that content changes and after the display's scaling changed, never when the
// user resizes it. The collapsed view never goes below its usual 300 px, the complete view
// stays within 260-720 px, and on the Speed limit tab, and on a switch between the tabs,
// the window only grows. The expanded view keeps its 560 px, where the connections scroll.
//
// New builds the whole composition (the store, the App, the kit window and the screen); a
// backend (ui/win) hosts Window.Kit in a window with the system frame, sized InitialSize.
// Every string, size and colour is today's.
package progress
