package kit

// NodeID names one node of a window's semantic tree. The kit gives every node its own id,
// never 0 and never reused while the window lives, so that assistive technology can hold
// on to it (UI Automation's runtime id).
type NodeID uint64

// SemanticNode is what a node of the semantic tree is at one moment: the ARIA role, name,
// states and values of a widget, in the terms UI Automation needs (its control types and
// the patterns Grid, Table, GridItem, TableItem, Selection, SelectionItem, Invoke, Value,
// RangeValue, Toggle, ExpandCollapse and ScrollItem). It is comparable, so a host can tell
// what changed since it last read the node.
type SemanticNode struct {
	ID NodeID
	// Parent is the node's parent, 0 for the root.
	Parent NodeID
	Role   Role
	// Name is the accessible name (aria-label, aria-labelledby, or the text itself): a
	// row's name and status, a button's label; for the root, the window's title.
	Name string
	// Description is longer text about the node (aria-describedby: a field's hint or
	// error, the error of a failed download's status).
	Description string
	// Value is the node's value as text: a text field's text, a combo box's choice, a
	// range's aria-valuetext ("45%"). A node with NodeProtected never exposes it.
	Value string
	// Range is the number of a progress bar, slider or spin button.
	Range RangeValue
	// States are the node's flags.
	States NodeState
	// Toggle is the state of a check box, switch, toggle button or checkable menu item.
	Toggle ToggleState
	// Expand is the state of what opens and closes something: a menu button, a combo box,
	// a disclosure.
	Expand ExpandState
	// Actions are what assistive technology may ask the node to do (see
	// SemanticTree.Perform).
	Actions Actions
	// Bounds is where the node is, in window DIPs (the host turns them into screen
	// pixels). A node scrolled out of view has NodeOffscreen and keeps its bounds.
	Bounds Rect
	// Live makes the node a live region whose changes are announced (aria-live; ARIA's
	// role=alert is assertive and role=status polite).
	Live Politeness
	// Orientation is the direction of a tab list, toolbar, separator, slider or scroll
	// bar (aria-orientation).
	Orientation Orientation
	// Sort is a column header's sort order (aria-sort).
	Sort SortDirection
	// Grid is the size of a grid or table.
	Grid GridInfo
	// Cell places a row, cell or header in its grid or table.
	Cell CellInfo
	// PosInSet and SetSize place a list item, option, tab, radio button or menu item in
	// its set, from 1 (0: not given). Level is a heading's rank (h1 to h4) or an item's
	// nesting level, from 1 (0: none).
	PosInSet, SetSize, Level int
	// Shortcut is the key that activates the node, written as the UI shows it ("Ctrl+K",
	// aria-keyshortcuts).
	Shortcut string
	// Controls is the node this one controls (aria-controls: the search field controls
	// the grid, a tab its panel, a combo box its list), or 0.
	Controls NodeID
	// AutomationID is a stable name for tests and tools ("titlebar.search"), the same in
	// every run, or "".
	AutomationID string
	// Fieldset marks the <fieldset> of today's window (a group or radio group): WebKit
	// gives it to VoiceOver as AXGroup/AXFieldset. [ax] Only the macOS bridge reads it.
	Fieldset bool
}

// Role is what a node is: the ARIA role, or the HTML element's implicit role, that the
// WebView window gives the same widget.
type Role uint8

// The roles.
const (
	// RoleNone is not exposed: its children stand in its place (layout boxes, decoration,
	// role=presentation).
	RoleNone       Role = iota
	RoleWindow          // the root of a window
	RoleGroup           // a group with a name (role=group, fieldset)
	RoleRegion          // a section with a name, or a scrolling pane (role=region, section)
	RoleNavigation      // a navigation landmark (nav: the sidebar)
	RoleMain            // the main landmark (main: the list and its header)
	RoleTitleBar        // the custom title bar
	RoleToolbar         // a row of controls
	RoleDialog
	RoleAlertDialog
	RoleAlert   // an urgent message (role=alert)
	RoleStatus  // an advisory message (role=status: the status bar, a "Copied" note)
	RoleTooltip // a tooltip's text
	RoleText    // plain text
	RoleHeading // a heading; Level is its rank
	RoleImage   // a meaningful image (role=img with a name: a file tile)
	// RoleButton is a button; with a ToggleState it is a toggle button (aria-pressed).
	RoleButton
	RoleLink
	RoleCheckBox
	RoleRadioButton
	RoleRadioGroup
	RoleSwitch
	// RoleTextField is editable text (input, textarea); with NodeProtected, a password.
	RoleTextField
	// RoleSpinButton is a number stepped with the arrow keys (a part of the schedule's
	// time field).
	RoleSpinButton
	RoleComboBox // a Select's button, the palette's field
	RoleListBox
	RoleOption
	RoleList       // ul, ol, dl
	RoleListItem   // li
	RoleTerm       // dt: a fact's label
	RoleDefinition // dd: a fact's value
	RoleMenu
	RoleMenuItem
	RoleMenuItemCheckBox
	RoleMenuItemRadio
	RoleSeparator
	RoleTabList
	RoleTab
	RoleTabPanel
	// RoleGrid is an interactive table (the download list): rows that can be selected, a
	// cell cursor, sortable column headers.
	RoleGrid
	RoleTable // a table to read (the progress window's connections)
	RoleRowGroup
	RoleRow
	RoleCell // a grid cell, or a table's td
	RoleColumnHeader
	RoleRowHeader
	RoleProgressBar
	RoleSlider
	RoleScrollBar
	RoleDocument // a long text to read (the open-source notices)
)

// NodeState is a set of a node's flags.
type NodeState uint32

// The node states.
const (
	NodeFocusable NodeState = 1 << iota
	NodeFocused
	NodeSelectable
	NodeSelected
	// NodeDisabled: shown, perhaps focusable, but not usable (disabled, aria-disabled).
	NodeDisabled
	NodeReadOnly
	NodeRequired
	NodeInvalid // the value is wrong (aria-invalid)
	NodeBusy    // the content is being updated (aria-busy)
	// NodeOffscreen: scrolled or clipped out of view, though laid out.
	NodeOffscreen
	NodeModal // a dialog that blocks the rest of the window (aria-modal)
	// NodeMultiSelectable: a grid or list box in which several items can be selected
	// (aria-multiselectable).
	NodeMultiSelectable
	// NodeHasPopup: activating the node opens a menu, list or dialog (aria-haspopup).
	NodeHasPopup
	NodeDefault   // the button Enter presses in its dialog
	NodeProtected // a password field: Value is never exposed
)

// ToggleState is the state of a node that can be checked (UI Automation's Toggle
// pattern).
type ToggleState uint8

// The toggle states.
const (
	// ToggleNone: the node cannot be checked.
	ToggleNone ToggleState = iota
	ToggleOff
	ToggleOn
	ToggleMixed // aria-checked=mixed
)

// ExpandState is the state of a node that opens and closes something (UI Automation's
// ExpandCollapse pattern).
type ExpandState uint8

// The expand states.
const (
	// ExpandNone: the node opens nothing.
	ExpandNone   ExpandState = iota
	ExpandClosed             // UI Automation's Collapsed (aria-expanded=false)
	ExpandOpen               // UI Automation's Expanded (aria-expanded=true)
)

// Politeness is how a live region's changes are announced (aria-live).
type Politeness uint8

// The politeness levels.
const (
	LiveOff       Politeness = iota // not a live region
	LivePolite                      // after what is being read (toasts, role=status)
	LiveAssertive                   // at once (role=alert)
)

// Orientation is the direction a node's items run in.
type Orientation uint8

// The orientations.
const (
	OrientationNone Orientation = iota
	OrientationHorizontal
	OrientationVertical
)

// SortDirection is how a column header sorts its grid (aria-sort).
type SortDirection uint8

// The sort directions.
const (
	SortNone SortDirection = iota
	SortAscending
	SortDescending
)

// RangeValue is the number of a progress bar, slider or spin button (aria-valuemin,
// aria-valuemax and aria-valuenow: UI Automation's RangeValue pattern).
type RangeValue struct {
	// Valid: the node has a number; an indeterminate progress bar ("Getting video info")
	// has none.
	Valid           bool
	Min, Max, Value float64
	// Step is the change one arrow key makes, 0 when it makes none.
	Step float64
}

// GridInfo is the size of a grid or table (aria-rowcount, aria-colcount): every row of
// the data, not only the rows laid out (the list is virtual), the header row included.
type GridInfo struct {
	Rows, Cols int
}

// CellInfo places a row, cell or header in its grid or table, counting from 0 (ARIA's
// aria-rowindex - 1 and aria-colindex - 1; UI Automation's GridItem). A row uses Row
// only, and a span of 0 counts as 1.
type CellInfo struct {
	Row, Col         int
	RowSpan, ColSpan int
}

// Actions is a set of what assistive technology may ask a node to do.
type Actions uint32

// The actions, with the UI Automation pattern each one serves (see SemanticTree.Perform).
const (
	ActionInvoke              Actions = 1 << iota // press: a button, link or menu item (Invoke)
	ActionToggle                                  // check or uncheck (Toggle)
	ActionExpand                                  // open (ExpandCollapse)
	ActionCollapse                                // close (ExpandCollapse)
	ActionSelect                                  // select only this one (SelectionItem)
	ActionAddToSelection                          // add to the selection (SelectionItem)
	ActionRemoveFromSelection                     // take out of the selection (SelectionItem)
	ActionScrollIntoView                          // scroll it into view (ScrollItem)
	ActionSetValue                                // set Value to ActionRequest.Text (Value)
	ActionSetRange                                // set the range to ActionRequest.Number (RangeValue)
	ActionFocus                                   // move the keyboard focus to it
)

// ActionRequest asks a node to do one action.
type ActionRequest struct {
	Node NodeID
	// Action is one of the Actions.
	Action Actions
	// Text is the value of ActionSetValue.
	Text string
	// Number is the value of ActionSetRange.
	Number float64
}

// SemanticTree is a window's semantic tree, kept by the kit and read by the host's
// accessibility bridge (UI Automation on Windows), which makes its elements from it on
// demand: nothing is copied while no client asks. Nodes exist for the widgets laid out,
// so in a virtual list for the rows realised; Cell stands a node in for any other row.
type SemanticTree interface {
	// Root is the root node's id.
	Root() NodeID
	// Node is node id as it is now, or false when there is no such node.
	Node(id NodeID) (SemanticNode, bool)
	// AppendChildren appends the ids of id's children, in reading order, to dst.
	AppendChildren(dst []NodeID, id NodeID) []NodeID
	// Focused is the node with the keyboard focus, or 0. When the focus stays on a field
	// while the arrow keys move through a list or a menu (aria-activedescendant), it is
	// the active item.
	Focused() NodeID
	// HitTest is the deepest exposed node at p (window DIPs), or the root.
	HitTest(p Point) NodeID
	// Cell is the node of the cell at row and col (from 0) of grid, or 0 when there is no
	// such cell (UI Automation's Grid.GetItem). For a row that is not laid out the node is
	// made for the call, and it keeps its id while its row exists.
	Cell(grid NodeID, row, col int) NodeID
	// AppendSelection appends to dst the ids of container's selected items, those not laid
	// out included (UI Automation's Selection.GetSelection).
	AppendSelection(dst []NodeID, container NodeID) []NodeID
	// Perform does what r asks, as a click or a key would. It fails with an error that
	// wraps ErrNoNode for a node that is gone, ErrNotEnabled for a disabled one and
	// ErrUnsupported for an action the node does not offer. It returns promptly: what
	// waits for the user (a dialog) carries on after it has returned.
	Perform(r ActionRequest) error
}

// SemanticEvent tells the host what changed in a semantic tree (see Host.NotifySemantics);
// the host reads what it needs from the tree and raises the system's events itself.
type SemanticEvent struct {
	Kind SemanticEventKind
	// Node is the node the event is about.
	Node NodeID
	// Text and Politeness are a SemanticAnnounce's message and how to announce it.
	Text       string
	Politeness Politeness
}

// SemanticEventKind is what a SemanticEvent reports.
type SemanticEventKind uint8

// The semantic events.
const (
	// SemanticFocusChanged: the keyboard focus is now on Node (0: on no node).
	SemanticFocusChanged SemanticEventKind = iota + 1
	// SemanticNodeChanged: Node's own properties may have changed (name, value, states,
	// range): the host compares them with what it last read, raises the matching property
	// events, and announces a live region's change. A move alone (Bounds) needs no event.
	SemanticNodeChanged
	// SemanticChildrenChanged: Node's children were added, removed or reordered.
	SemanticChildrenChanged
	// SemanticAnnounce: announce Text with Politeness, as a live region would, without a
	// node of its own (the grid's I key reads out a download's progress). Node is the node
	// it is about, or 0.
	SemanticAnnounce
)
