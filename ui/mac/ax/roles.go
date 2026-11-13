// Package ax is the platform-free half of the native macOS window's accessibility (the
// [ax] part of ui/mac): it reads a window's kit.SemanticTree when assistive technology
// asks, decides what NSAccessibility shows of each node (role, subrole, label, value,
// states, table structure, actions), keeps one element per lasting NodeID, turns the kit's
// SemanticEvents into NSAccessibility notifications and announcements, and maps the
// accessibility actions back to kit.ActionRequests with the refusal rules of the Windows
// bridge (ui/win/uia). The Objective-C elements themselves are ui/mac's: this package
// talks to them through System, so it builds and is tested on every OS.
//
// The roles follow what WebKit gives VoiceOver for the same ARIA in today's window (the
// W3C Core-AAM "AX API" column), so that a VoiceOver user hears the same roles and names.
package ax

import "downex/ui/kit"

// The NSAccessibility role and subrole strings the bridge uses (AppKit's constants have
// these values; WebKit's own subroles have no AppKit constant).
const (
	RoleWindow       = "AXWindow"
	RoleGroup        = "AXGroup"
	RoleToolbar      = "AXToolbar"
	RoleStaticText   = "AXStaticText"
	RoleHeading      = "AXHeading"
	RoleImage        = "AXImage"
	RoleButton       = "AXButton"
	RoleLink         = "AXLink"
	RoleCheckBox     = "AXCheckBox"
	RoleRadioButton  = "AXRadioButton"
	RoleRadioGroup   = "AXRadioGroup"
	RoleTextField    = "AXTextField"
	RoleIncrementor  = "AXIncrementor"
	RoleComboBox     = "AXComboBox"
	RoleList         = "AXList"
	RoleMenu         = "AXMenu"
	RoleMenuItem     = "AXMenuItem"
	RoleSplitter     = "AXSplitter"
	RoleTabGroup     = "AXTabGroup"
	RoleTable        = "AXTable"
	RoleRow          = "AXRow"
	RoleCell         = "AXCell"
	RoleProgress     = "AXProgressIndicator"
	RoleSlider       = "AXSlider"
	RoleScrollBar    = "AXScrollBar"
	RoleUnknown      = "AXUnknown"
	SubLandmarkMain  = "AXLandmarkMain"
	SubLandmarkNav   = "AXLandmarkNavigation"
	SubLandmarkReg   = "AXLandmarkRegion"
	SubLandmarkBan   = "AXLandmarkBanner"
	SubDialog        = "AXApplicationDialog"
	SubAlertDialog   = "AXApplicationAlertDialog"
	SubAlert         = "AXApplicationAlert"
	SubStatus        = "AXApplicationStatus"
	SubTooltip       = "AXUserInterfaceTooltip"
	SubDocument      = "AXDocument"
	SubTabPanel      = "AXTabPanel"
	SubTerm          = "AXTerm"
	SubDefinition    = "AXDescription" // WebKit's dd (AXDescription, "description")
	SubContentList   = "AXContentList"
	SubDescList      = "AXDescriptionList" // WebKit's dl
	SubAppGroup      = "AXApplicationGroup"
	SubFieldset      = "AXFieldset"
	SubToggle        = "AXToggle"
	SubSwitch        = "AXSwitch"
	SubTabButton     = "AXTabButton"
	SubSecureField   = "AXSecureTextField"
	SubSortButton    = "AXSortButton"
	SubTableRow      = "AXTableRow"
	SubOutlineRow    = "AXOutlineRow"
	SubStandardWin   = "AXStandardWindow"
	SubDialogWindow  = "AXDialog"
	SubFloatingPanel = "AXFloatingWindow"
)

// RoleOf is the NSAccessibility role and subrole of node n, as WebKit exposes the same ARIA
// role (Core-AAM's AX API mapping). A RoleNone node is never exposed (the kit leaves it
// out of the tree), and gets AXUnknown here.
func RoleOf(n *kit.SemanticNode) (role, subrole string) {
	if n.Fieldset && (n.Role == kit.RoleGroup || n.Role == kit.RoleRadioGroup) {
		// A <fieldset>, its radios inside (SegmentedControl.svelte, the Move dialog's
		// queues): WebKit's AXGroup/AXFieldset, described "group".
		return RoleGroup, SubFieldset
	}
	switch n.Role {
	case kit.RoleWindow:
		return RoleWindow, SubStandardWin
	case kit.RoleGroup:
		// role=group (WebKit's AXApplicationGroup; a <fieldset>, above, AXFieldset).
		return RoleGroup, SubAppGroup
	case kit.RoleRowGroup:
		return RoleGroup, ""
	case kit.RoleRegion:
		return RoleGroup, SubLandmarkReg
	case kit.RoleNavigation:
		return RoleGroup, SubLandmarkNav
	case kit.RoleMain:
		return RoleGroup, SubLandmarkMain
	case kit.RoleTitleBar:
		// Today's title bar is TitleBar.svelte's <header>: WebKit's banner landmark.
		return RoleGroup, SubLandmarkBan
	case kit.RoleToolbar:
		return RoleToolbar, ""
	case kit.RoleDialog:
		return RoleGroup, SubDialog
	case kit.RoleAlertDialog:
		return RoleGroup, SubAlertDialog
	case kit.RoleAlert:
		return RoleGroup, SubAlert
	case kit.RoleStatus:
		return RoleGroup, SubStatus
	case kit.RoleTooltip:
		return RoleGroup, SubTooltip
	case kit.RoleText:
		return RoleStaticText, ""
	case kit.RoleHeading:
		return RoleHeading, ""
	case kit.RoleImage:
		return RoleImage, ""
	case kit.RoleButton:
		if n.Toggle != kit.ToggleNone { // aria-pressed
			return RoleCheckBox, SubToggle
		}
		if n.States&kit.NodeHasPopup != 0 {
			// WebKit's popup button (aria-haspopup on a button): AXPopUpButton.
			return "AXPopUpButton", ""
		}
		return RoleButton, ""
	case kit.RoleLink:
		return RoleLink, ""
	case kit.RoleCheckBox:
		return RoleCheckBox, ""
	case kit.RoleSwitch:
		return RoleCheckBox, SubSwitch
	case kit.RoleRadioButton:
		return RoleRadioButton, ""
	case kit.RoleRadioGroup:
		return RoleRadioGroup, ""
	case kit.RoleTextField:
		if n.States&kit.NodeProtected != 0 {
			return RoleTextField, SubSecureField
		}
		return RoleTextField, ""
	case kit.RoleSpinButton:
		return RoleIncrementor, ""
	case kit.RoleComboBox:
		return RoleComboBox, ""
	case kit.RoleListBox:
		return RoleList, ""
	case kit.RoleOption:
		// WebKit's listbox option is static text inside the AXList, selectable.
		return RoleStaticText, ""
	case kit.RoleList:
		return RoleList, SubContentList
	case kit.RoleListItem:
		return RoleGroup, ""
	case kit.RoleTerm:
		return RoleGroup, SubTerm
	case kit.RoleDefinition:
		return RoleGroup, SubDefinition
	case kit.RoleMenu:
		return RoleMenu, ""
	case kit.RoleMenuItem, kit.RoleMenuItemCheckBox, kit.RoleMenuItemRadio:
		return RoleMenuItem, ""
	case kit.RoleSeparator:
		return RoleSplitter, ""
	case kit.RoleTabList:
		return RoleTabGroup, ""
	case kit.RoleTab:
		return RoleRadioButton, SubTabButton
	case kit.RoleTabPanel:
		return RoleGroup, SubTabPanel
	case kit.RoleGrid, kit.RoleTable:
		return RoleTable, ""
	case kit.RoleRow:
		return RoleRow, SubTableRow
	case kit.RoleCell, kit.RoleColumnHeader, kit.RoleRowHeader:
		return RoleCell, ""
	case kit.RoleProgressBar:
		return RoleProgress, ""
	case kit.RoleSlider:
		return RoleSlider, ""
	case kit.RoleScrollBar:
		return RoleScrollBar, ""
	case kit.RoleDocument:
		return RoleGroup, SubDocument
	}
	return RoleUnknown, ""
}

// subroleDescriptions are WebKit's role descriptions (en) of the subroles AppKit has no
// description of its own for; the others are NSAccessibilityRoleDescription's.
var subroleDescriptions = map[string]string{
	SubLandmarkMain: "main",
	SubLandmarkNav:  "navigation",
	SubLandmarkReg:  "region",
	SubLandmarkBan:  "banner",
	SubDialog:       "web dialog",
	SubAlertDialog:  "web alert dialog",
	SubAlert:        "alert",
	SubStatus:       "application status",
	SubTooltip:      "tooltip",
	SubDocument:     "document",
	SubTabPanel:     "tab panel",
	SubTerm:         "term",
	SubDefinition:   "description",
	SubDescList:     "description list",
	SubAppGroup:     "group",
	SubFieldset:     "group",
	SubToggle:       "toggle button",
	SubSwitch:       "switch",
	SubTabButton:    "tab",
}

// roleDescriptions are WebKit's role descriptions of roles AppKit does not describe.
var roleDescriptions = map[string]string{
	RoleHeading: "heading",
}

// RoleDescription is the description of a role and subrole that this package knows
// better than AppKit (WebKit's own subroles), or "" for AppKit's
// NSAccessibilityRoleDescription.
func RoleDescription(role, subrole string) string {
	if d, ok := subroleDescriptions[subrole]; ok {
		return d
	}
	return roleDescriptions[role]
}

// Priority is how urgently an announcement is read (NSAccessibilityPriorityLevel).
type Priority uint8

// The priorities: a polite live region's change waits for what is being read
// (NSAccessibilityPriorityMedium); an assertive one interrupts it (High).
const (
	PriorityMedium Priority = iota + 1
	PriorityHigh
)

// PriorityOf maps a politeness to the announcement's priority (LiveOff: none, 0).
func PriorityOf(p kit.Politeness) Priority {
	switch p {
	case kit.LivePolite:
		return PriorityMedium
	case kit.LiveAssertive:
		return PriorityHigh
	}
	return 0
}

// liveSetting is how a node's changes are announced: its own aria-live, else its role's
// (ARIA: role=alert is assertive, role=status polite).
func liveSetting(n *kit.SemanticNode) kit.Politeness {
	switch {
	case n.Live != kit.LiveOff:
		return n.Live
	case n.Role == kit.RoleAlert:
		return kit.LiveAssertive
	case n.Role == kit.RoleStatus:
		return kit.LivePolite
	}
	return kit.LiveOff
}

// selected reports a selected item (a radio button checked).
func selected(n *kit.SemanticNode) bool {
	return n.States&kit.NodeSelected != 0 || (n.Role == kit.RoleRadioButton && n.Toggle == kit.ToggleOn)
}

func isGrid(r kit.Role) bool { return r == kit.RoleGrid || r == kit.RoleTable }

func isCell(r kit.Role) bool {
	return r == kit.RoleCell || r == kit.RoleColumnHeader || r == kit.RoleRowHeader
}

func isSelectionContainer(r kit.Role) bool {
	switch r {
	case kit.RoleGrid, kit.RoleListBox, kit.RoleTabList, kit.RoleRadioGroup, kit.RoleMenu:
		return true
	}
	return false
}

// isControl reports a form control: WebKit gives it AXInvalid even while it is valid.
func isControl(r kit.Role) bool {
	switch r {
	case kit.RoleButton, kit.RoleCheckBox, kit.RoleRadioButton, kit.RoleSwitch, kit.RoleTextField,
		kit.RoleSpinButton, kit.RoleComboBox, kit.RoleListBox, kit.RoleSlider:
		return true
	}
	return false
}

func isMenuItem(r kit.Role) bool {
	return r == kit.RoleMenuItem || r == kit.RoleMenuItemCheckBox || r == kit.RoleMenuItemRadio
}

// valueReadOnly reports a node whose Value cannot be set.
func valueReadOnly(n *kit.SemanticNode) bool {
	return n.States&(kit.NodeReadOnly|kit.NodeDisabled) != 0 || n.Actions&kit.ActionSetValue == 0
}

// rangeReadOnly reports a node whose range cannot be set.
func rangeReadOnly(n *kit.SemanticNode) bool {
	return !n.Range.Valid || n.States&(kit.NodeReadOnly|kit.NodeDisabled) != 0 || n.Actions&kit.ActionSetRange == 0
}
