// Package downloads is the main window's download list and details pane (native-ui plan
// step 11): DownloadTable.svelte with its rows (DownloadRow.svelte, FileName.svelte,
// ListEmpty.svelte) on the kit's virtual list, and DetailsPanel.svelte with its
// ConnectionBoxes and SpeedChart. It fills the views.DownloadGrid and views.DetailsPane
// slots; views/shell places them and views/app wires the composition.
//
// The layout, sizes, colours, fonts, wording, motion and keys are today's: every number in
// this package comes from those components' CSS and app.css, and every string is theirs
// byte for byte. The column logic (widths in rem, minimums, the columns dropped from the
// right, resizing, the widths kept in downex.columnWidths) is columns.go.
//
// Ownership: New makes a Downloads, which owns the grid and the pane and the subscriptions
// to the stores that feed them; Dispose ends them. Everything runs on the UI thread; the
// stores' changes reach it through App.Watch.
package downloads
