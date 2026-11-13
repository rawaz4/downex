package kit

// Event is an input or window event for a Root (see Root.Event): one of the event types
// below, which only this package defines.
type Event interface {
	isEvent()
}

// PointerEvent is the mouse (and, through the system's mouse emulation, a pen or a
// finger) over the client area and over the frame buttons the root declared (see
// FramePart), or anywhere while the pointer is captured.
type PointerEvent struct {
	Action PointerAction
	// Pos is where the pointer is, in window DIPs (outside the client area while captured).
	Pos Point
	// Button is the button that went down or up, for PointerDown and PointerUp; 0
	// otherwise.
	Button Buttons
	// Buttons are the buttons held after the event.
	Buttons Buttons
	// Mods are the modifier keys held.
	Mods Modifiers
	// Clicks counts the presses of a PointerDown in a row, by the system's double-click
	// time and distance: 1, then 2 for a double click and 3 for a triple one. It is 0 for
	// the other actions.
	Clicks int
}

// PointerAction is what a PointerEvent reports.
type PointerAction uint8

// The pointer actions.
const (
	// PointerMove: the pointer moved.
	PointerMove PointerAction = iota + 1
	// PointerDown: a button went down.
	PointerDown
	// PointerUp: a button went up.
	PointerUp
	// PointerEnter: the pointer came over the window, before the first PointerMove there.
	// Moving between the client area and a frame button stays inside the window.
	PointerEnter
	// PointerLeave: the pointer left the window, or went over a native control hosted in
	// it (which handles its own input; over a native text field the moves still come, see
	// NativeFields).
	PointerLeave
	// PointerCancel: the window lost the pointer capture to something else (another
	// window, Alt+Tab, a system menu): drop any press or drag in progress, as DOM's
	// pointercancel.
	PointerCancel
)

// Buttons is a set of mouse buttons.
type Buttons uint8

// The mouse buttons.
const (
	ButtonLeft Buttons = 1 << iota
	ButtonRight
	ButtonMiddle
	ButtonBack    // the first side button (Windows' XBUTTON1)
	ButtonForward // the second side button (XBUTTON2)
)

// Modifiers is a set of modifier keys held during an event. AltGr, on layouts that have
// it, is reported as Ctrl+Alt, as Windows reports it; what it types arrives as a
// TextEvent.
type Modifiers uint8

// The modifier keys.
const (
	ModShift Modifiers = 1 << iota
	ModCtrl
	ModAlt
	ModMeta // the Windows key (Command on a Mac)
)

// WheelEvent asks to scroll what is under Pos by Delta.
type WheelEvent struct {
	// Pos is the pointer's position in window DIPs.
	Pos Point
	// Delta is how far to scroll, in DIPs (in pages for WheelPages): a positive Y towards
	// the end of the content (down), a positive X to the right, as the DOM's deltaY and
	// deltaX.
	Delta Point
	// Mode tells how Delta came about.
	Mode WheelMode
	// Mods are the modifier keys held (the kit scrolls sideways with Shift).
	Mods Modifiers
}

// WheelMode is how a WheelEvent's Delta came about.
type WheelMode uint8

// The wheel modes.
const (
	// WheelLines: whole notches of a wheel. Delta is notches × the system's lines per
	// notch × 100/3 DIPs, as Chromium scrolls: 100 DIPs a notch at Windows' default of 3
	// lines. The kit may animate the scroll.
	WheelLines WheelMode = iota + 1
	// WheelPrecise: a precision touchpad or a free-spinning wheel, in steps smaller than a
	// notch. Delta is exact; scroll by it at once.
	WheelPrecise
	// WheelPages: the system is set to scroll one screen at a time; Delta is in pages.
	WheelPages
)

// KeyEvent is a key going down or up while the window has the keyboard focus, also while a
// native text field hosted in it has it (the root sees the key first, see NativeFields).
type KeyEvent struct {
	Action KeyAction
	Key    Key
	// Mods are the modifier keys held; on a modifier's own KeyDown it is already among
	// them, as in the DOM.
	Mods Modifiers
	// Repeat marks a KeyDown the system repeats while the key is held.
	Repeat bool
}

// KeyAction is what a KeyEvent reports.
type KeyAction uint8

// The key actions.
const (
	KeyDown KeyAction = iota + 1
	KeyUp
)

// Key is a virtual key: what a key means in the current keyboard layout, so KeyA is the
// key that types an A, and Ctrl+KeyA is Ctrl+A on every layout with Latin letters. The
// values are the Windows virtual-key codes, which other backends map their keys to. The
// two Shift, Control, Alt and Windows keys are not told apart, nor are Enter and the
// keypad's Enter. The punctuation keys are named after the US layout, though other
// layouts may put other characters on them; the characters typed arrive as TextEvents.
type Key uint16

// Named keys.
const (
	KeyBackspace   Key = 0x08
	KeyTab         Key = 0x09
	KeyEnter       Key = 0x0D
	KeyShift       Key = 0x10
	KeyControl     Key = 0x11
	KeyAlt         Key = 0x12
	KeyPause       Key = 0x13
	KeyCapsLock    Key = 0x14
	KeyEscape      Key = 0x1B
	KeySpace       Key = 0x20
	KeyPageUp      Key = 0x21
	KeyPageDown    Key = 0x22
	KeyEnd         Key = 0x23
	KeyHome        Key = 0x24
	KeyArrowLeft   Key = 0x25
	KeyArrowUp     Key = 0x26
	KeyArrowRight  Key = 0x27
	KeyArrowDown   Key = 0x28
	KeyPrintScreen Key = 0x2C
	KeyInsert      Key = 0x2D
	KeyDelete      Key = 0x2E
	KeyMeta        Key = 0x5B // a Windows key
	KeyContextMenu Key = 0x5D // the Menu key (Shift+F10 is KeyF10 with ModShift)
	KeyNumLock     Key = 0x90
	KeyScrollLock  Key = 0x91

	KeySemicolon     Key = 0xBA // ;
	KeyEqual         Key = 0xBB // =
	KeyComma         Key = 0xBC // , (Ctrl+, opens Settings)
	KeyMinus         Key = 0xBD // -
	KeyPeriod        Key = 0xBE // .
	KeySlash         Key = 0xBF // /
	KeyBackquote     Key = 0xC0 // `
	KeyBracketLeft   Key = 0xDB // [
	KeyBackslash     Key = 0xDC // \
	KeyBracketRight  Key = 0xDD // ]
	KeyQuote         Key = 0xDE // '
	KeyIntlBackslash Key = 0xE2 // the key beside the left Shift on ISO keyboards
)

// The digit keys of the main block: their ASCII codes, '0' to '9'.
const (
	Key0 Key = 0x30 + iota
	Key1
	Key2
	Key3
	Key4
	Key5
	Key6
	Key7
	Key8
	Key9
)

// The letter keys: their ASCII codes, 'A' to 'Z'.
const (
	KeyA Key = 0x41 + iota
	KeyB
	KeyC
	KeyD
	KeyE
	KeyF
	KeyG
	KeyH
	KeyI
	KeyJ
	KeyK
	KeyL
	KeyM
	KeyN
	KeyO
	KeyP
	KeyQ
	KeyR
	KeyS
	KeyT
	KeyU
	KeyV
	KeyW
	KeyX
	KeyY
	KeyZ
)

// The numeric keypad's keys (with Num Lock on; with it off they arrive as the arrow,
// Home, End, Page, Insert and Delete keys).
const (
	KeyNumpad0 Key = 0x60 + iota
	KeyNumpad1
	KeyNumpad2
	KeyNumpad3
	KeyNumpad4
	KeyNumpad5
	KeyNumpad6
	KeyNumpad7
	KeyNumpad8
	KeyNumpad9
	KeyNumpadMultiply
	KeyNumpadAdd
	KeyNumpadSeparator
	KeyNumpadSubtract
	KeyNumpadDecimal
	KeyNumpadDivide
)

// The function keys.
const (
	KeyF1 Key = 0x70 + iota
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyF13
	KeyF14
	KeyF15
	KeyF16
	KeyF17
	KeyF18
	KeyF19
	KeyF20
	KeyF21
	KeyF22
	KeyF23
	KeyF24
)

// TextEvent is text typed, or composed by an input method: one or more characters, never
// control characters (Enter, Tab, Backspace and Escape are KeyEvents only). When the root
// used the KeyDown that typed it, the host drops it, as a browser does once keydown's
// default action is prevented: Space that pauses a download types no space.
type TextEvent struct {
	Text string
}

// FocusEvent: the window got (In) or lost the keyboard focus as a whole; a native control
// hosted in the window having the focus counts as the window having it.
type FocusEvent struct {
	In bool
}

// SizeEvent: the client area's size is now Size (DIPs). A DPI change keeps the size in
// DIPs (the host makes it exact), so it comes with an EnvironmentEvent and a SizeEvent
// only if the size changed too.
type SizeEvent struct {
	Size Size
}

// EnvironmentEvent: the window's scale or a system setting changed; Env is the new
// environment.
type EnvironmentEvent struct {
	Env Environment
}

// WindowStateEvent: the window was maximised, minimised or restored.
type WindowStateEvent struct {
	State WindowState
}

// CloseEvent: the user or the system asks to close the window: its close button (through
// WindowClose), Alt+F4, the taskbar, or an installer's WM_CLOSE. The root uses it to
// close the window later itself, or not at all; when it does not use it, the window
// closes.
type CloseEvent struct{}

func (PointerEvent) isEvent()     {}
func (WheelEvent) isEvent()       {}
func (KeyEvent) isEvent()         {}
func (TextEvent) isEvent()        {}
func (FocusEvent) isEvent()       {}
func (SizeEvent) isEvent()        {}
func (EnvironmentEvent) isEvent() {}
func (WindowStateEvent) isEvent() {}
func (CloseEvent) isEvent()       {}
