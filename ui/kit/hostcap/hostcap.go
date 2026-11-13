// Package hostcap names the host capabilities for popup windows, tooltips and the native text
// field kinds by their earlier home. They are part of kit's contract now (kit.Popups,
// kit.Tooltips, kit.NativeFieldKinds, kit.FieldControl, kit.FieldReclaimer and their types,
// in ui/kit/host.go); the names here are aliases of those, kept for the backend (ui/win) that
// implemented them under these names. New code uses the kit names.
package hostcap

import "downex/ui/kit"

// The capabilities.
type (
	Popups           = kit.Popups
	Tooltips         = kit.Tooltips
	NativeFieldKinds = kit.NativeFieldKinds
	FieldControl     = kit.FieldControl
	FieldReclaimer   = kit.FieldReclaimer
)

// Their types.
type (
	PopupKind     = kit.PopupKind
	DismissReason = kit.DismissReason
	Dismiss       = kit.Dismiss
	PopupOptions  = kit.PopupOptions
	Popup         = kit.Popup
	FieldKind     = kit.FieldKind
	FieldOptions  = kit.FieldOptions
)

// Their constants.
const (
	PopupMenu = kit.PopupMenu

	DismissPress      = kit.DismissPress
	DismissDeactivate = kit.DismissDeactivate
	DismissMove       = kit.DismissMove
	DismissWheel      = kit.DismissWheel
	DismissClosed     = kit.DismissClosed

	FieldText      = kit.FieldText
	FieldPassword  = kit.FieldPassword
	FieldMultiline = kit.FieldMultiline
)
