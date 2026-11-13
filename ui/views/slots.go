package views

import "downex/ui/kit"

// The slots the shell composes in the main window's work card and below it (App.svelte's
// layout): the shell (views/shell) places them, the downloads lane (views/downloads) fills
// them, and neither imports the other; the composition (views/app) hands the shell what the
// downloads lane made.
//
//	.window  title bar (44 px), then .app: the grid of .body and the status bar (32 px)
//	.body    the sidebar (14rem; a 56 px rail from 721 to 999 px; above the card under 721 px)
//	         beside the work card
//	.card    banner slot, page header (58 px), the download grid, the details pane (only while
//	         something is selected)

// DownloadGrid is the download list (DownloadTable.svelte with its rows): the card's third row,
// taking the room left.
type DownloadGrid interface {
	kit.Widget
	// FocusList focuses the grid at its cursor row (table.focus()).
	FocusList()
	// FocusTarget is the element focus falls back to when a dialog's opener is gone
	// (App.svelte's gridElement); App.FallbackFocus returns it.
	FocusTarget() kit.Widget
}

// DetailsPane is the details pane (DetailsPanel.svelte): the card's last row, present only
// while downloads are selected (the shell adds and removes it as the selection changes), its
// height --details-h (12.5rem compact, 11rem in a window 620 px high or less) or just its
// header while App.Details is collapsed.
type DetailsPane interface {
	kit.Widget
}

// StatusBar is the status bar (StatusBar.svelte): the 32 px row under the body, with the
// totals, the speed sparkline, the free space, the global speed limit and When done menus.
type StatusBar interface {
	kit.Widget
	// OpenLimitMenu and OpenWhenDoneMenu open its menus (the palette's "Set global speed
	// limit…" and "When done…"; Actions.OpenLimitMenu and OpenWhenDoneMenu call them).
	OpenLimitMenu()
	OpenWhenDoneMenu()
}

// Slots are what the composition hands the shell.
type Slots struct {
	Grid      DownloadGrid
	Details   DetailsPane
	StatusBar StatusBar
}
