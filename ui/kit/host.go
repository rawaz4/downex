package kit

import "time"

// Host is one window as the kit sees it, made by the backend for one Root (see
// Root.Attach). Its methods are called on the UI thread, except Post; they never call
// back into the root (see the package doc), and once the window is closed they do
// nothing (Post and the clipboard fail with ErrClosed).
type Host interface {
	// Environment is the window's scale and the system's settings that change the look.
	// An EnvironmentEvent follows each change.
	Environment() Environment
	// Size is the client area's size in DIPs. A SizeEvent follows each change.
	Size() Size
	// WindowState is whether the window is maximised or minimised. A WindowStateEvent
	// follows each change.
	WindowState() WindowState
	// Resources makes text layouts, paths and images; every window of the backend shares
	// the same one.
	Resources() Resources

	// Invalidate asks for r (window DIPs) to be painted again in the next frame; an empty
	// r asks for nothing. It is cheap, and the areas asked for are merged.
	Invalidate(r Rect)
	// RequestFrame asks for one call of Root.Frame at the display's next refresh, also
	// when nothing is invalid. That call uses the request up: the kit's motion clock asks
	// again from Frame for as long as something moves, and the host produces no frame
	// otherwise (0 paints when idle). No frames come while the window is minimised; a
	// request then waits until it is restored.
	RequestFrame()

	// SetCursor sets the pointer's shape over the client area until the next call (over
	// the frame parts the system draws its own).
	SetCursor(c Cursor)
	// CapturePointer sends every pointer event to this window, also from outside it, while
	// a button is held: for a dragged scrollbar thumb or column edge. The capture ends with
	// ReleasePointer, or by itself after the PointerUp that releases the last button (as
	// DOM pointer capture does); a PointerCancel event tells that something else took it.
	CapturePointer()
	// ReleasePointer ends CapturePointer.
	ReleasePointer()
	// SetTextInput tells the system's input methods whether the focused part takes typed
	// text and where its caret is (see TextInput). Native text fields hosted in the window
	// do this themselves.
	SetTextInput(t TextInput)

	// ClipboardText is the text on the clipboard, "" when it holds none. It fails when the
	// clipboard cannot be opened (another program holds it).
	ClipboardText() (string, error)
	// SetClipboardText puts s on the clipboard as text.
	SetClipboardText(s string) error

	// SetTitle sets the window's title, which the taskbar, Alt+Tab, a system frame's
	// caption and assistive technology show.
	SetTitle(title string)
	// SetMinSize sets the smallest client size, in DIPs, that the user can resize the
	// window to (it follows DPI changes); a side of 0 has no minimum of its own.
	SetMinSize(s Size)
	// Resize asks for a client size in DIPs, keeping the window's top-left corner and
	// fitting the window into its monitor's work area; a SizeEvent follows when the size
	// changes. While the window is maximised or minimised it sets the size it restores to.
	Resize(s Size)
	// SetFrameColors sets the colours the system draws around and behind the content (see
	// FrameColors). The root sets them in Attach, before the window is shown, and again
	// whenever its theme changes.
	SetFrameColors(c FrameColors)
	// Command does what a title-bar button asks for (see WindowCommand).
	Command(c WindowCommand)
	// Close closes the window, without a CloseEvent, once the call in progress (a Root
	// method or a Post function) has returned; Detach is then the root's last call.
	Close()

	// Post runs f on the UI thread after the calls in progress there, in the order of the
	// Post calls. Any goroutine may call it. It fails with ErrClosed once the window is
	// closed; functions still queued then are dropped.
	Post(f func()) error
	// NotifySemantics tells assistive technology that the root's semantic tree changed
	// (see SemanticEvent). It costs next to nothing while no client listens. A host reads
	// the root's SemanticTree only when assistive technology asks (UI Automation's
	// WM_GETOBJECT on Windows): the kit keeps lasting row ids and sends these events only
	// once the tree has been read, so a window nobody reads pays nothing for them.
	NotifySemantics(e SemanticEvent)
}

// RendererInfo is a capability of a Host that can name what draws its window, for the
// About page and "Copy diagnostics": "hardware" (the GPU) or "software", or "" before the
// first frame. The kit finds it with a type assertion on the Host.
type RendererInfo interface {
	Renderer() string
}

// NativeFields is a capability of a Host that holds the system's own text fields in its
// window (on Windows an EDIT control of Common Controls 6): typing, input methods (IME
// composition and candidate windows), the caret, selection, undo, the clipboard and the
// field's context menu are the system's, and so is the field's accessibility. The kit lays
// the field out and draws its box (ground, border, focus ring) around it; the field fills
// the text area inside. The kit finds the capability with a type assertion on the Host.
//
// While a field has the keyboard focus, each key that reaches it goes to the root first as
// a KeyEvent (the root's focused element being the field's widget, so its handlers and the
// window's shortcuts see the key, as a browser's keydown bubbles from an input to the
// window), and a key the root uses never reaches the field: Escape that clears the search,
// Tab that moves the focus, Ctrl+K that opens the palette. Typed text goes to the field
// alone. Over the field the root still gets PointerMove, so its box shows hover, but the
// presses go to the field itself; the host keeps the root's focus in step through
// TextFieldCallbacks.Focused.
type NativeFields interface {
	// NewTextField makes a single-line field, hidden until SetBounds places it. The
	// callbacks are calls into the root: they run on the UI thread and never inside another
	// call into the root (the host queues them).
	NewTextField(opts TextFieldOptions, cb TextFieldCallbacks) (NativeField, error)
}

// NativeField is one native text field of a window (see NativeFields). Its methods belong
// to the UI thread and do nothing once it is released or its window is closed.
type NativeField interface {
	// SetBounds places the field's text area, in window DIPs: the content box of the box the
	// kit draws. The field centres its line in it vertically, as a browser's text field does,
	// and starts its text at its left edge. An empty rectangle hides the field.
	SetBounds(r Rect)
	// SetOptions changes how the field looks (after a theme change).
	SetOptions(o TextFieldOptions)
	// Text is the field's text.
	Text() string
	// SetText replaces the text without a Changed callback, as setting an input's value
	// fires no input event; the caret goes to the end.
	SetText(s string)
	// Focus gives the field the keyboard focus; Blur gives it back to the window when the
	// field has it.
	Focus()
	Blur()
	// SelectAll selects the whole text.
	SelectAll()
	// Release removes the field. It is idempotent.
	Release()
}

// TextFieldOptions are how a native field looks.
type TextFieldOptions struct {
	// Style is the text's font; Color is the text's colour and Background the ground the
	// field fills behind it.
	Style      TextStyle
	Color      Color
	Background Color
	// Placeholder is shown while the field is empty, in PlaceholderColor (HTML's
	// placeholder); the host draws it with the text engine that draws the rest of the
	// window.
	Placeholder      string
	PlaceholderColor Color
	// Name is the field's accessible name (aria-label).
	Name string
	// Align is where the text and the placeholder sit on the line (CSS text-align: start,
	// center or end: numbers and limits are end); Direction is the field's direction, which
	// start and end follow (the window's: in a right-to-left window, end is the left).
	Align     TextAlign
	Direction Direction
	// AutoDirection is HTML's dir=auto (names and titles): the field's direction is its
	// text's, that of the first strong character (UAX #9 P2 and P3, left to right without
	// one: an empty field is left to right), in place of Direction, and the host follows it
	// as the text changes. Start and end, the reading order of the text and where the caret
	// starts follow that direction: a Kurdish or Arabic name sits at the right edge, read
	// right to left, with its ".mp4" at the left.
	AutoDirection bool
}

// AlignsRight reports whether o puts the text at the field's right edge: end in a
// left-to-right field, start in a right-to-left one (Direction's; AutoDirection's
// fields are AlignsRightAt the direction their text resolved to).
func (o TextFieldOptions) AlignsRight() bool { return o.AlignsRightAt(o.Direction) }

// AlignsRightAt is AlignsRight in a field whose direction is dir (DirLTR or DirRTL).
func (o TextFieldOptions) AlignsRightAt(dir Direction) bool {
	return o.Align != TextAlignCenter && (o.Align == TextAlignEnd) != (dir == DirRTL)
}

// ResolveDirection is the direction of a field holding text: with AutoDirection that of
// the text's first strong character (left to right without one), else Direction.
func (o TextFieldOptions) ResolveDirection(text string) Direction {
	if !o.AutoDirection {
		return o.Direction
	}
	return FirstStrong(text)
}

// TextFieldCallbacks are what a native field tells the root (see NativeFields).
type TextFieldCallbacks struct {
	// Changed: the user changed the text (typing, pasting, cutting, undoing).
	Changed func(text string)
	// Focused: the field got (true) or lost (false) the keyboard focus.
	Focused func(in bool)
}

// Root is the content of one window as a backend sees it: the kit's side of a Host. The
// host calls it on the UI thread, one call at a time (see the package doc).
type Root interface {
	// Attach gives the root its host. It is the first call, made before the window is
	// shown: the root lays out for h.Size() and h.Environment(), and sets the window's
	// title, sizes and frame colours. The host shows the window with the first frame the
	// root paints.
	Attach(h Host)
	// Detach is the last call: the window is closed and its host must not be used again.
	// The root releases the layouts, paths and images it holds.
	Detach()
	// Frame starts a frame at now, the monotonic time of the display refresh it is for:
	// the root advances its motion clock to now and lays out what changed, and what it
	// invalidates is painted in this frame. The host calls Frame for every frame it makes,
	// after RequestFrame and whenever an area is invalid (through Invalidate, or because
	// the window was uncovered or resized), at most once per display refresh, and then
	// Paint if any area is invalid.
	Frame(now time.Time)
	// Paint draws the frame's dirty area on c, which is clipped to it and has the
	// identity transform. dirty is in window DIPs and on whole device pixels (the host
	// rounds it out). Paint covers every pixel of it, background included, since what was
	// there is undefined. It must not keep c.
	Paint(c Canvas, dirty Rect)
	// Event handles one input or window event and reports whether the root used it. A
	// KeyDown the root does not use keeps its system meaning (Alt+F4, Alt+Space, F10),
	// and a CloseEvent it does not use closes the window; for the other events the result
	// does not matter.
	Event(e Event) bool
	// FrameHit tells the host what part of a custom window frame is at p (window DIPs):
	// the title bar's caption and buttons, for dragging, the system's double click and
	// Snap Layouts. The host finds the resize borders itself, the top one included, and
	// asks only about points inside them. It is called on every pointer move over the
	// window, so it must be quick.
	FrameHit(p Point) FramePart
	// Semantics is the window's semantic tree, or nil while the root keeps none.
	Semantics() SemanticTree
}

// Environment is a window's scale and the system settings that change how it looks. It is
// comparable: compare the one an EnvironmentEvent brings with the last one to see what
// changed.
type Environment struct {
	// Scale is the window's monitor scale and the text-size factor.
	Scale Scale
	// Dark: the system's colour mode for apps is dark (Settings › Personalisation ›
	// Colours). The app's System theme follows it.
	Dark bool
	// HighContrast: a contrast theme is on, and the kit draws with SystemColors (CSS
	// forced-colors: active).
	HighContrast bool
	// SystemColors are the contrast theme's colours; they matter while HighContrast.
	SystemColors SystemColors
	// ReducedMotion: the system's animation effects are off (CSS prefers-reduced-motion:
	// reduce).
	ReducedMotion bool
	// ReducedTransparency: the system's transparency effects are off (CSS
	// prefers-reduced-transparency: reduce), so nothing is drawn see-through and blurred.
	ReducedTransparency bool
	// HoverTime is how long the pointer must rest before a tooltip shows: the system's
	// mouse hover time (Windows: SPI_GETMOUSEHOVERTIME, 400 ms unless changed). Zero: the
	// host does not know it.
	HoverTime time.Duration
	// [mac scroll]
	// ScrollBars is the kind of scroll bar the system asks for (scrollmac.go): zero, Chromium's
	// Fluent bars (Windows); the macOS host follows System Settings › Appearance › Show scroll
	// bars and the pointing device (NSScroller.preferredScrollerStyle).
	ScrollBars ScrollBarStyle
	// ScrollClickJumps: a click in a scroll bar's track jumps to the spot clicked instead of
	// paging (macOS's "Click in the scroll bar to: Jump to the spot that's clicked").
	ScrollClickJumps bool
	// [/mac scroll]
	// PaintedOverflow is no longer read: what an element paints outside its box by itself
	// (PaintOverflower) is now repainted with it on every host, since the Windows window
	// left the palette's old shadow behind too. The macOS host (ui/mac window.go) still sets
	// it; remove both together.
	PaintedOverflow bool
	// [mac webkit]
	// WebKit: the page this window stands in for is WebKit's on macOS (today's Mac window is
	// a WKWebView), not Chromium's (WebView2). Where the two engines draw app.css
	// differently the kit then draws as WebKit does: macOS's own check boxes, tab labels
	// centred in their bold twin's width and overflow-wrap:anywhere in the settings' inline
	// text. Only the macOS
	// host sets it; Windows leaves it false and nothing of it runs there.
	WebKit bool
	// [/mac webkit]
}

// SystemColors are the CSS system colours that app.css uses in forced-colours mode, taken
// from the Windows colours named.
type SystemColors struct {
	Canvas        Color // COLOR_WINDOW
	CanvasText    Color // COLOR_WINDOWTEXT
	LinkText      Color // COLOR_HOTLIGHT
	GrayText      Color // COLOR_GRAYTEXT
	Highlight     Color // COLOR_HIGHLIGHT
	HighlightText Color // COLOR_HIGHLIGHTTEXT
	ButtonFace    Color // COLOR_BTNFACE
	ButtonText    Color // COLOR_BTNTEXT
}

// WindowState is how the window is shown.
type WindowState struct {
	// Maximized: the window fills its monitor's work area (the title bar shows Restore).
	Maximized bool
	// Minimized: the window is on the taskbar only.
	Minimized bool
}

// WindowCommand is what a title-bar button asks the window to do.
type WindowCommand uint8

// The window commands.
const (
	WindowMinimize WindowCommand = iota + 1
	WindowMaximize
	WindowRestore
	// WindowClose asks to close the window, as the close button and Alt+F4 do: the root
	// gets a CloseEvent, and the window closes unless the root uses it.
	WindowClose
)

// FrameColors are the colours the system draws a window's frame and ground in, taken from
// the theme's tokens so that the frame reads as part of the page (Windows 11; earlier
// systems ignore the colours). A zero (transparent) colour leaves the system's.
type FrameColors struct {
	// Background is the window's ground (--chrome for the main window): the system shows
	// it before the first frame and where a live resize gets ahead of the paint.
	Background Color
	// Caption, Text and InactiveText are a system frame's caption ground and its title,
	// active and inactive (the progress windows: --surface, --ink and --faint).
	Caption, Text, InactiveText Color
	// Border and InactiveBorder are the window's 1 px border, active and inactive
	// (--line-strong and --line).
	Border, InactiveBorder Color
	// Dark asks for the dark frame and for dark system menus.
	Dark bool
}

// FramePart is what a point of a window with a custom frame is (see Root.FrameHit).
type FramePart uint8

// The frame parts. Over the three buttons the host still sends the root its pointer
// events, so that the root draws their hover and press and acts on a click (with
// Host.Command); the system never acts on clicks there itself. The host tells the system
// where the maximise button is (that is what opens Snap Layouts), and may do the same for
// the other two.
const (
	// FrameClient is the root's own: its input goes to the root.
	FrameClient FramePart = iota
	// FrameCaption is the title bar's empty ground: the system drags the window from it,
	// maximises or restores it on a double click and opens the system menu on a right
	// click.
	FrameCaption
	// FrameMinimize is the minimise button.
	FrameMinimize
	// FrameMaximize is the maximise or restore button: on Windows 11 the Snap Layouts
	// flyout opens while the pointer rests on it.
	FrameMaximize
	// FrameClose is the close button.
	FrameClose
)

// Cursor is the pointer's shape.
type Cursor uint8

// The cursors. CursorGrab and CursorGrabbing fall back to the nearest system cursor where
// the platform has no hand cursors of its own.
const (
	CursorDefault    Cursor = iota // the arrow
	CursorPointer                  // the hand: links, and CSS cursor: pointer (buttons, chips)
	CursorText                     // the I-beam over text that can be selected or edited
	CursorColResize                // a column's edge (CSS col-resize)
	CursorRowResize                // a row's edge
	CursorGrab                     // something that can be dragged (CSS grab: the queue order)
	CursorGrabbing                 // while it is dragged
	CursorMove                     // moving in any direction
	CursorNotAllowed               // a drop that is refused
	CursorWait                     // busy, input blocked
	CursorProgress                 // busy, input still taken
)

// TextInput is what the system's input methods (IME composition and candidate windows,
// the touch keyboard) need to know about the focused part of the root.
type TextInput struct {
	// Active: the focused part takes typed text, so an input method may compose there.
	// TextEvents arrive either way: menus and lists use them to find items by typing.
	Active bool
	// Caret is where the caret is (window DIPs), so input methods open their windows there.
	Caret Rect
}

// ---- popup windows, tooltips and native field kinds ----------------------------------------
//
// These capabilities were the widget layer's hostcap package (plan steps 10 and 13); they are
// part of the contract now, found like the others with a type assertion on a Host or a
// NativeField, so a backend without them still runs every widget (headless tests, a later
// macOS backend). Package hostcap keeps aliases of them for the backend that implements them.

// Popups is a capability of a Host that opens popup windows above its window: top-level
// windows of their own, so that the native text fields hosted in the window never sit above
// a menu or a list, and so that a popup may reach past the window's edge. A popup never takes
// the activation or the keyboard focus from its owner: the owner's root keeps the keyboard and
// hands the popup the keys it should have (Popup.Deliver), as a browser's focused menu or
// combobox does inside one document. The pointer over a popup is the popup's own.
type Popups interface {
	// OpenPopup opens a popup whose content is root (a Window, typically) at o.Bounds.
	// root.Attach runs before OpenPopup returns, with a Host whose Environment is the
	// owner's. The popup shows with its root's first frame.
	OpenPopup(root Root, o PopupOptions) (Popup, error)
}

// Tooltips is a capability of a Host that shows the system's tooltips over its window, as
// the WebView window shows an element's title (HTML's title attribute is drawn by the
// browser host with the system's tooltip, never by app.css). The host follows the pointer;
// once it has rested for the system's hover time it asks Source for the tooltip at that
// point (window DIPs; Window.TooltipAt answers it, with the whole text of a label an
// ellipsis cuts) and shows it by the pointer, until the pointer leaves the area Source
// gave, a button or a key goes down, or the window loses the activation.
type Tooltips interface {
	// SetTooltipSource sets the source (nil turns the window's tooltips off). Source runs as
	// a call into the root.
	SetTooltipSource(source func(p Point) (text string, area Rect, ok bool))
}

// PopupKind is what a popup is for; it decides how the system treats it.
type PopupKind uint8

// The popup kinds.
const (
	// PopupMenu is a menu or a Select's list: it takes the pointer where its root's content
	// is (see PopupOptions.Hit), and a press anywhere else, or the owner losing the
	// activation, moving or changing size, dismisses it (PopupOptions.OnDismiss).
	PopupMenu PopupKind = iota
)

// DismissReason is why the host asks a popup's owner to close it.
type DismissReason uint8

// The dismiss reasons.
const (
	// DismissPress: a mouse button went down outside the popup's Hit area (Pos says where,
	// in the owner's window DIPs; Inside says whether that was over the owner's client area,
	// where the press goes on to the owner's root as usual).
	DismissPress DismissReason = iota + 1
	// DismissDeactivate: the owner window lost the activation (another application, Alt+Tab).
	DismissDeactivate
	// DismissMove: the owner window moved, changed size, was minimised or maximised.
	DismissMove
	// DismissWheel: the wheel turned outside the popup (what is under it scrolls, as a
	// document's scroll closes today's menus and lists).
	DismissWheel
	// DismissClosed: the owner window is closing; the popup is closed by the host already.
	DismissClosed
)

// Dismiss is what OnDismiss hears.
type Dismiss struct {
	Reason DismissReason
	// Pos is where a DismissPress went down, in the owner's window DIPs.
	Pos Point
	// Inside reports whether the press was over the owner's client area.
	Inside bool
}

// PopupOptions place and describe a popup.
type PopupOptions struct {
	Kind PopupKind
	// Bounds is the popup's client area in the owner's window DIPs: the content and the room
	// its shadow needs around it. It may reach past the owner's client area; the host keeps
	// it on the owner's monitor.
	Bounds Rect
	// Hit is the part of the popup that takes the pointer, in the popup's own DIPs (its
	// root's coordinates): the content box. Over the rest (the shadow) the pointer goes to
	// what is under the popup. An empty Hit is the whole popup.
	Hit Rect
	// Name is the popup window's title (assistive technology reads it).
	Name string
	// OnDismiss hears why the host asks for the popup to close (PopupMenu only). It runs as a
	// call into the owner's root, on the UI thread, never inside another call into it.
	OnDismiss func(d Dismiss)
}

// Popup is one open popup window. Its methods belong to the UI thread and do nothing once
// it is closed.
type Popup interface {
	// SetBounds moves or resizes the popup (owner window DIPs, as PopupOptions.Bounds), and
	// SetHit changes the area that takes the pointer.
	SetBounds(r Rect)
	SetHit(r Rect)
	// Deliver gives the popup's root an event as a call into it, now: the keys the owner's
	// root hands on while the popup has the keyboard in all but name. It is meant to be
	// called from inside a call into the owner's root and reports whether the root used e.
	Deliver(e Event) bool
	// Close closes the popup: its root gets Detach once the call in progress has returned.
	// It is idempotent.
	Close()
}

// FieldKind is what a native text field holds.
type FieldKind uint8

// The field kinds.
const (
	// FieldText is a single line of text (NativeFields' own field).
	FieldText FieldKind = iota
	// FieldPassword is a single line whose characters show as the system's password mark
	// (input type=password): its text never reaches the clipboard or assistive technology,
	// and input methods are off in it, as a browser has them.
	FieldPassword
	// FieldMultiline is several lines that wrap at the field's width and scroll vertically
	// (a textarea): Enter is the root's to insert a line break with ReplaceSelection, so that
	// Ctrl+Enter can still submit.
	FieldMultiline
)

// FieldOptions are what a field of a kind needs beyond TextFieldOptions.
type FieldOptions struct {
	Kind FieldKind
	// MaxLength is the most UTF-16 code units the user may type (HTML's maxlength); 0 is no
	// limit.
	MaxLength int
	// LineHeight is a multi-line field's line pitch, as a multiple of the font size (CSS
	// line-height: textarea's 1.55); 0 is the font's own.
	LineHeight float32
}

// NativeFieldKinds is a capability of a Host that makes native fields of every kind. The
// callbacks follow NativeFields' rules. A FieldText field may come from NativeFields
// itself.
type NativeFieldKinds interface {
	NewField(o TextFieldOptions, k FieldOptions, cb TextFieldCallbacks) (NativeField, error)
}

// FieldControl is a capability of a NativeField that exposes its caret and selection,
// and the edits a widget makes for the user (one undoable step each). Offsets are in UTF-16
// code units, as HTML's selectionStart and setSelectionRange count them.
type FieldControl interface {
	// Selection is the selected range; start equals end for a bare caret.
	Selection() (start, end int)
	// SetSelection selects [start, end) (a caret when equal), clamped to the text.
	SetSelection(start, end int)
	// ReplaceSelection replaces the selection with s as typing would (undoable; the field's
	// Changed callback follows).
	ReplaceSelection(s string)
	// SetMaxLength changes FieldOptions.MaxLength.
	SetMaxLength(n int)
	// SetPassword makes a single-line field a password field (FieldPassword) or a plain one
	// again: its characters show as the system's password mark, and input methods are off in
	// it while it is one.
	SetPassword(on bool)
}

// FieldReclaimer is a capability of a NativeField that is not placed by the kit itself (a
// multi-line field): Reclaim takes the system's focus back after the window was activated
// again, when the field had it when the window lost the activation.
type FieldReclaimer interface {
	Reclaim()
}
