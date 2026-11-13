//go:build windows

// Package w32 binds the Win32 functions ui/win's windows use (user32, dwmapi, imm32 and a
// few of kernel32 and gdi32), with typed wrappers of a fixed number of arguments around
// syscall.SyscallN.
//
// Re-entrancy rule: a function that may run a window procedure before it returns
// (CreateWindowEx, DestroyWindow, DefWindowProc, DispatchMessage, PeekMessage, GetMessage,
// SendMessage, SetWindowPos, ShowWindow, UpdateWindow, BeginPaint, SetFocus, SetCapture,
// ReleaseCapture, SetWindowPlacement, SetWindowText) runs Go code on the calling goroutine,
// whose stack may then move. Pointers passed to those functions must point to heap memory
// that stays referenced (fields of a long-lived struct), never to locals. The other
// functions take pointers to locals as the COM bindings do (see package com).
package w32

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	imm32    = windows.NewLazySystemDLL("imm32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procUnregisterClassW              = user32.NewProc("UnregisterClassW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procUpdateWindow                  = user32.NewProc("UpdateWindow")
	procShowScrollBar                 = user32.NewProc("ShowScrollBar")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procPostThreadMessageW            = user32.NewProc("PostThreadMessageW")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procSetCursor                     = user32.NewProc("SetCursor")
	procBeginPaint                    = user32.NewProc("BeginPaint")
	procEndPaint                      = user32.NewProc("EndPaint")
	procInvalidateRect                = user32.NewProc("InvalidateRect")
	procValidateRect                  = user32.NewProc("ValidateRect")
	procGetUpdateRect                 = user32.NewProc("GetUpdateRect")
	procGetClientRect                 = user32.NewProc("GetClientRect")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procScreenToClient                = user32.NewProc("ScreenToClient")
	procClientToScreen                = user32.NewProc("ClientToScreen")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procAdjustWindowRectExForDpi      = user32.NewProc("AdjustWindowRectExForDpi")
	procMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint              = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procIsIconic                      = user32.NewProc("IsIconic")
	procIsChild                       = user32.NewProc("IsChild")
	procTrackMouseEvent               = user32.NewProc("TrackMouseEvent")
	procSetCapture                    = user32.NewProc("SetCapture")
	procReleaseCapture                = user32.NewProc("ReleaseCapture")
	procGetCapture                    = user32.NewProc("GetCapture")
	procGetKeyState                   = user32.NewProc("GetKeyState")
	procSetWindowTextW                = user32.NewProc("SetWindowTextW")
	procGetDoubleClickTime            = user32.NewProc("GetDoubleClickTime")
	procGetSystemMetricsForDpi        = user32.NewProc("GetSystemMetricsForDpi")
	procSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	procGetSysColor                   = user32.NewProc("GetSysColor")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procEmptyClipboard                = user32.NewProc("EmptyClipboard")
	procGetClipboardData              = user32.NewProc("GetClipboardData")
	procSetClipboardData              = user32.NewProc("SetClipboardData")
	procGetWindowPlacement            = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement            = user32.NewProc("SetWindowPlacement")
	procGetMessageTime                = user32.NewProc("GetMessageTime")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	procGlobalAlloc                   = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                    = kernel32.NewProc("GlobalFree")
	procGlobalLock                    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                    = kernel32.NewProc("GlobalSize")
	procDwmSetWindowAttribute         = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmFlush                      = dwmapi.NewProc("DwmFlush")
	procImmGetContext                 = imm32.NewProc("ImmGetContext")
	procImmReleaseContext             = imm32.NewProc("ImmReleaseContext")
	procImmSetCompositionWindow       = imm32.NewProc("ImmSetCompositionWindow")
	procImmSetCandidateWindow         = imm32.NewProc("ImmSetCandidateWindow")
	procImmAssociateContextEx         = imm32.NewProc("ImmAssociateContextEx")
	procCreateSolidBrush              = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject                  = gdi32.NewProc("DeleteObject")
	procFillRect                      = user32.NewProc("FillRect")

	// The custom frame and hosted native fields.
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	procIsZoomed                     = user32.NewProc("IsZoomed")
	procGetCursorPos                 = user32.NewProc("GetCursorPos")
	procWindowFromPoint              = user32.NewProc("WindowFromPoint")
	procGetAncestor                  = user32.NewProc("GetAncestor")
	procSetWindowLongPtrW            = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW            = user32.NewProc("GetWindowLongPtrW")
	procCallWindowProcW              = user32.NewProc("CallWindowProcW")
	procSendMessageW                 = user32.NewProc("SendMessageW")
	procSetFocus                     = user32.NewProc("SetFocus")
	procGetFocus                     = user32.NewProc("GetFocus")
	procGetDC                        = user32.NewProc("GetDC")
	procReleaseDC                    = user32.NewProc("ReleaseDC")
	procHideCaret                    = user32.NewProc("HideCaret")
	procShowCaret                    = user32.NewProc("ShowCaret")
	procGetWindowTextW               = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW         = user32.NewProc("GetWindowTextLengthW")
	procLoadImageW                   = user32.NewProc("LoadImageW")
	procCreateFontW                  = gdi32.NewProc("CreateFontW")
	procGetTextMetricsW              = gdi32.NewProc("GetTextMetricsW")
	procSelectObject                 = gdi32.NewProc("SelectObject")
	procSetTextColor                 = gdi32.NewProc("SetTextColor")
	procSetBkColor                   = gdi32.NewProc("SetBkColor")
	procCreateCompatibleDC           = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC                     = gdi32.NewProc("DeleteDC")
	procSHAppBarMessage              = shell32.NewProc("SHAppBarMessage")
	procDestroyIcon                  = user32.NewProc("DestroyIcon")
	procGetDpiForMonitor             = shcore.NewProc("GetDpiForMonitor")
)

// Load finds every function, so that a missing one fails here with its name instead of
// panicking at its first call.
func Load() error {
	for _, p := range []*windows.LazyProc{procRegisterClassExW, procUnregisterClassW, procCreateWindowExW, procDestroyWindow,
		procDefWindowProcW, procShowWindow, procUpdateWindow, procGetMessageW, procTranslateMessage,
		procDispatchMessageW, procPostMessageW, procPostQuitMessage, procPostThreadMessageW, procLoadCursorW, procSetCursor, procBeginPaint, procEndPaint,
		procInvalidateRect, procValidateRect, procGetUpdateRect, procGetClientRect, procGetWindowRect, procScreenToClient,
		procClientToScreen, procSetWindowPos, procGetDpiForWindow, procSetProcessDpiAwarenessContext, procAdjustWindowRectExForDpi,
		procMonitorFromWindow, procMonitorFromPoint, procGetMonitorInfoW, procIsIconic, procIsChild,
		procTrackMouseEvent, procSetCapture, procReleaseCapture, procGetCapture, procGetKeyState, procSetWindowTextW,
		procGetDoubleClickTime, procGetSystemMetricsForDpi, procSystemParametersInfoW, procGetSysColor, procOpenClipboard,
		procCloseClipboard, procEmptyClipboard, procGetClipboardData, procSetClipboardData, procGetWindowPlacement,
		procSetWindowPlacement, procGetMessageTime, procGetModuleHandleW,
		procGlobalAlloc, procGlobalFree, procGlobalLock, procGlobalUnlock, procGlobalSize, procDwmSetWindowAttribute, procDwmFlush,
		procImmGetContext, procImmReleaseContext, procImmSetCompositionWindow, procImmSetCandidateWindow, procImmAssociateContextEx,
		procCreateSolidBrush, procDeleteObject, procFillRect,
		procDwmExtendFrameIntoClientArea, procIsZoomed, procGetCursorPos, procWindowFromPoint, procGetAncestor,
		procSetWindowLongPtrW, procCallWindowProcW, procSendMessageW, procSetFocus, procGetFocus, procGetDC, procReleaseDC,
		procHideCaret, procShowCaret, procGetWindowTextW, procGetWindowTextLengthW, procLoadImageW, procCreateFontW,
		procGetTextMetricsW, procSelectObject, procSetTextColor, procSetBkColor, procCreateCompatibleDC, procDeleteDC,
		procDestroyIcon} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("w32: %w", err)
		}
	}
	return nil
}

// Types and structures (winuser.h, dwmapi.h, imm.h).
type (
	HWND = windows.HWND

	Rect  struct{ Left, Top, Right, Bottom int32 }
	Point struct{ X, Y int32 }

	MSG struct {
		Hwnd    HWND
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      Point
		Private uint32
	}
	WNDCLASSEXW struct {
		Size       uint32
		Style      uint32
		WndProc    uintptr
		ClsExtra   int32
		WndExtra   int32
		Instance   windows.Handle
		Icon       windows.Handle
		Cursor     windows.Handle
		Background windows.Handle
		MenuName   *uint16
		ClassName  *uint16
		IconSm     windows.Handle
	}
	CREATESTRUCTW struct {
		CreateParams uintptr
		Instance     windows.Handle
		Menu         windows.Handle
		Parent       HWND
		Cy, Cx       int32
		Y, X         int32
		Style        int32
		Name         *uint16
		Class        *uint16
		ExStyle      uint32
	}
	PAINTSTRUCT struct {
		Hdc         windows.Handle
		Erase       int32
		Paint       Rect
		Restore     int32
		IncUpdate   int32
		RgbReserved [32]byte
	}
	MINMAXINFO struct {
		Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize Point
	}
	MONITORINFO struct {
		Size    uint32
		Monitor Rect
		Work    Rect
		Flags   uint32
	}
	TRACKMOUSEEVENT struct {
		Size      uint32
		Flags     uint32
		Track     HWND
		HoverTime uint32
	}
	// WINDOWPLACEMENT is 44 bytes: WinUser.h's rcDevice exists only under _MAC, and
	// SetWindowPlacement refuses any other length (ERROR_INVALID_PARAMETER).
	WINDOWPLACEMENT struct {
		Length         uint32
		Flags          uint32
		ShowCmd        uint32
		MinPosition    Point
		MaxPosition    Point
		NormalPosition Rect
	}
	HIGHCONTRAST struct {
		Size          uint32
		Flags         uint32
		DefaultScheme *uint16
	}
	COMPOSITIONFORM struct {
		Style      uint32
		CurrentPos Point
		Area       Rect
	}
	CANDIDATEFORM struct {
		Index      uint32
		Style      uint32
		CurrentPos Point
		Area       Rect
	}
	// NCCALCSIZE_PARAMS is WM_NCCALCSIZE's argument when wParam is TRUE.
	NCCALCSIZE_PARAMS struct {
		Rgrc [3]Rect
		Pos  uintptr // *WINDOWPOS
	}
	// MARGINS is DwmExtendFrameIntoClientArea's (uxtheme.h).
	MARGINS struct {
		LeftWidth, RightWidth, TopHeight, BottomHeight int32
	}
	// APPBARDATA is SHAppBarMessage's (shellapi.h).
	APPBARDATA struct {
		Size     uint32
		Hwnd     HWND
		CallBack uint32
		Edge     uint32
		Rc       Rect
		LParam   uintptr
	}
	// TEXTMETRICW is GetTextMetricsW's (wingdi.h).
	TEXTMETRICW struct {
		Height, Ascent, Descent, InternalLeading, ExternalLeading int32
		AveCharWidth, MaxCharWidth, Weight, Overhang              int32
		DigitizedAspectX, DigitizedAspectY                        int32
		FirstChar, LastChar, DefaultChar, BreakChar               uint16
		Italic, Underlined, StruckOut, PitchAndFamily, CharSet    uint8
	}
)

// Messages, styles and other constants (winuser.h).
const (
	WM_CREATE           = 0x0001
	WM_DESTROY          = 0x0002
	WM_MOVE             = 0x0003
	WM_SIZE             = 0x0005
	WM_ACTIVATE         = 0x0006
	WM_SETFOCUS         = 0x0007
	WM_KILLFOCUS        = 0x0008
	WM_SETTEXT          = 0x000C
	WM_PAINT            = 0x000F
	WM_CLOSE            = 0x0010
	WM_ERASEBKGND       = 0x0014
	WM_SYSCOLORCHANGE   = 0x0015
	WM_SETTINGCHANGE    = 0x001A
	WM_SETCURSOR        = 0x0020
	WM_GETMINMAXINFO    = 0x0024
	WM_NCCREATE         = 0x0081
	WM_NCDESTROY        = 0x0082
	WM_NCACTIVATE       = 0x0086
	WM_KEYDOWN          = 0x0100
	WM_KEYUP            = 0x0101
	WM_CHAR             = 0x0102
	WM_DEADCHAR         = 0x0103
	WM_SYSKEYDOWN       = 0x0104
	WM_SYSKEYUP         = 0x0105
	WM_SYSCHAR          = 0x0106
	WM_SYSDEADCHAR      = 0x0107
	WM_UNICHAR          = 0x0109
	WM_SYSCOMMAND       = 0x0112
	WM_ENDSESSION       = 0x0016
	WM_MOUSEMOVE        = 0x0200
	WM_LBUTTONDOWN      = 0x0201
	WM_LBUTTONUP        = 0x0202
	WM_LBUTTONDBLCLK    = 0x0203
	WM_RBUTTONDOWN      = 0x0204
	WM_RBUTTONUP        = 0x0205
	WM_MBUTTONDOWN      = 0x0207
	WM_MBUTTONUP        = 0x0208
	WM_MOUSEWHEEL       = 0x020A
	WM_XBUTTONDOWN      = 0x020B
	WM_XBUTTONUP        = 0x020C
	WM_MOUSEHWHEEL      = 0x020E
	WM_CAPTURECHANGED   = 0x0215
	WM_ENTERSIZEMOVE    = 0x0231
	WM_EXITSIZEMOVE     = 0x0232
	WM_MOUSELEAVE       = 0x02A3
	WM_DPICHANGED       = 0x02E0
	WM_GETDPISCALEDSIZE = 0x02E4
	WM_THEMECHANGED     = 0x031A
	WM_APP              = 0x8000

	WM_GETTEXTLENGTH    = 0x000E
	WM_SETICON          = 0x0080
	WM_DISPLAYCHANGE    = 0x007E
	WM_SETFONT          = 0x0030
	WM_WINDOWPOSCHANGED = 0x0047
	WM_NCCALCSIZE       = 0x0083
	WM_NCHITTEST        = 0x0084
	WM_NCMOUSEMOVE      = 0x00A0
	WM_NCLBUTTONDOWN    = 0x00A1
	WM_NCLBUTTONUP      = 0x00A2
	WM_NCLBUTTONDBLCLK  = 0x00A3
	WM_NCRBUTTONDOWN    = 0x00A4
	WM_NCRBUTTONUP      = 0x00A5
	WM_NCMOUSELEAVE     = 0x02A2
	WM_COMMAND          = 0x0111
	WM_CTLCOLOREDIT     = 0x0133

	HTNOWHERE     = 0
	HTCAPTION     = 2
	HTMINBUTTON   = 8
	HTMAXBUTTON   = 9
	HTLEFT        = 10
	HTRIGHT       = 11
	HTTOP         = 12
	HTTOPLEFT     = 13
	HTTOPRIGHT    = 14
	HTBOTTOM      = 15
	HTBOTTOMLEFT  = 16
	HTBOTTOMRIGHT = 17
	HTCLOSE       = 20

	WS_POPUP         = 0x80000000
	WS_CHILD         = 0x40000000
	WS_VISIBLE       = 0x10000000
	WS_EX_NOACTIVATE = 0x08000000
	ES_AUTOHSCROLL   = 0x0080
	// An EDIT's alignment (ES_LEFT is 0); SetWindowLongPtr changes it on a live EDIT.
	ES_CENTER = 0x0001
	ES_RIGHT  = 0x0002

	EM_GETSEL       = 0x00B0
	EM_SETSEL       = 0x00B1
	EM_REPLACESEL   = 0x00C2
	EM_SCROLLCARET  = 0x00B7
	EM_CHARFROMPOS  = 0x00D7
	EM_POSFROMCHAR  = 0x00D6
	EM_GETRECT      = 0x00B2
	EM_GETLINECOUNT = 0x00BA
	EM_SETMARGINS   = 0x00D3
	EC_LEFTMARGIN   = 0x0001
	EC_RIGHTMARGIN  = 0x0002
	EN_SETFOCUS     = 0x0100
	EN_KILLFOCUS    = 0x0200
	EN_CHANGE       = 0x0300

	SM_CXSIZEFRAME    = 32
	SM_CYSIZEFRAME    = 33
	SM_CXPADDEDBORDER = 92
	SM_CXICON         = 11
	SM_CXSMICON       = 49

	SPI_GETMOUSEHOVERTIME = 0x0066

	ABM_GETSTATE         = 0x00000004
	ABM_GETAUTOHIDEBAREX = 0x0000000B
	ABS_AUTOHIDE         = 0x0000001
	ABE_LEFT             = 0
	ABE_TOP              = 1
	ABE_RIGHT            = 2
	ABE_BOTTOM           = 3

	GA_ROOT = 2

	IMAGE_ICON      = 1
	LR_DEFAULTCOLOR = 0x0000
	ICON_SMALL      = 0
	ICON_BIG        = 1

	FW_NORMAL           = 400
	DEFAULT_CHARSET     = 1
	CLEARTYPE_QUALITY   = 5
	OUT_TT_PRECIS       = 4
	CLIP_DEFAULT_PRECIS = 0

	CS_VREDRAW = 0x0001
	CS_HREDRAW = 0x0002

	WS_OVERLAPPEDWINDOW       = 0x00CF0000
	WS_CLIPCHILDREN           = 0x02000000
	WS_EX_NOREDIRECTIONBITMAP = 0x00200000

	SW_HIDE           = 0
	SW_SHOWNORMAL     = 1
	SW_SHOWMINIMIZED  = 2
	SW_MAXIMIZE       = 3
	SW_SHOWNOACTIVATE = 4
	SW_SHOWNA         = 8
	SW_SHOW           = 5
	SW_MINIMIZE       = 6
	SW_RESTORE        = 9

	SWP_NOSIZE       = 0x0001
	SWP_NOMOVE       = 0x0002
	SWP_NOZORDER     = 0x0004
	SWP_NOACTIVATE   = 0x0010
	SWP_FRAMECHANGED = 0x0020

	SIZE_RESTORED  = 0
	SIZE_MINIMIZED = 1
	SIZE_MAXIMIZED = 2

	SC_MINIMIZE = 0xF020
	SC_MAXIMIZE = 0xF030
	SC_CLOSE    = 0xF060
	SC_RESTORE  = 0xF120
	SC_KEYMENU  = 0xF100

	HTCLIENT = 1

	MK_LBUTTON  = 0x0001
	MK_RBUTTON  = 0x0002
	MK_SHIFT    = 0x0004
	MK_CONTROL  = 0x0008
	MK_MBUTTON  = 0x0010
	MK_XBUTTON1 = 0x0020
	MK_XBUTTON2 = 0x0040

	TME_LEAVE     = 0x00000002
	TME_NONCLIENT = 0x00000010

	VK_SHIFT      = 0x10
	VK_CONTROL    = 0x11
	VK_MENU       = 0x12
	VK_LWIN       = 0x5B
	VK_RWIN       = 0x5C
	VK_PROCESSKEY = 0xE5

	MONITOR_DEFAULTTONEAREST = 2
	MONITOR_DEFAULTTOPRIMARY = 1

	SM_CXDOUBLECLK = 36
	SM_CYDOUBLECLK = 37

	SPI_GETHIGHCONTRAST        = 0x0042
	SPI_GETWHEELSCROLLLINES    = 0x0068
	SPI_GETWHEELSCROLLCHARS    = 0x006C
	SPI_GETCLIENTAREAANIMATION = 0x1042
	HCF_HIGHCONTRASTON         = 0x00000001
	WHEEL_DELTA                = 120
	WHEEL_PAGESCROLL           = 0xFFFFFFFF

	COLOR_WINDOW        = 5
	COLOR_WINDOWTEXT    = 8
	COLOR_HIGHLIGHT     = 13
	COLOR_HIGHLIGHTTEXT = 14
	COLOR_BTNFACE       = 15
	COLOR_GRAYTEXT      = 17
	COLOR_BTNTEXT       = 18
	COLOR_HOTLIGHT      = 26

	CF_UNICODETEXT = 13
	GMEM_MOVEABLE  = 0x0002

	IDC_ARROW       = 32512
	IDC_IBEAM       = 32513
	IDC_WAIT        = 32514
	IDC_SIZEALL     = 32646
	IDC_SIZEWE      = 32644
	IDC_SIZENS      = 32645
	IDC_NO          = 32648
	IDC_HAND        = 32649
	IDC_APPSTARTING = 32650

	DWMWA_CLOAK                   = 13
	DWMWA_USE_IMMERSIVE_DARK_MODE = 20
	DWMWA_BORDER_COLOR            = 34
	DWMWA_CAPTION_COLOR           = 35
	DWMWA_TEXT_COLOR              = 36
	DWMWA_COLOR_DEFAULT           = 0xFFFFFFFF

	CFS_POINT          = 0x0002
	CFS_FORCE_POSITION = 0x0020
	CFS_EXCLUDE        = 0x0080
	IACE_DEFAULT       = 0x0010
)

// GWLP_WNDPROC is SetWindowLongPtr's index of the window procedure.
const GWLP_WNDPROC int32 = -4

// GWL_STYLE is the index of a window's style.
const GWL_STYLE int32 = -16

// HWND_MESSAGE is CreateWindowEx's parent for a message-only window ((HWND)-3).
const HWND_MESSAGE = HWND(^uintptr(2))

// CW_USEDEFAULT lets the system place or size a new window.
const CW_USEDEFAULT int32 = -0x80000000

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is ((DPI_AWARENESS_CONTEXT)-4).
const DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = ^uintptr(3)

func lastErr(op string, e syscall.Errno) error {
	if e == 0 {
		e = syscall.EINVAL
	}
	return fmt.Errorf("%s: %w", op, e)
}

// RegisterClassEx registers a window class (wc must stay alive: a heap value).
func RegisterClassEx(wc *WNDCLASSEXW) (uint16, error) {
	r, _, e := syscall.SyscallN(procRegisterClassExW.Addr(), uintptr(unsafe.Pointer(wc)))
	if r == 0 {
		return 0, lastErr("RegisterClassExW", e)
	}
	return uint16(r), nil
}

// UnregisterClass unregisters a window class.
func UnregisterClass(name *uint16, inst windows.Handle) {
	syscall.SyscallN(procUnregisterClassW.Addr(), uintptr(unsafe.Pointer(name)), uintptr(inst))
}

// CreateWindowEx makes a window; param reaches WM_NCCREATE in CREATESTRUCTW.CreateParams.
func CreateWindowEx(exStyle uint32, class, title *uint16, style uint32, x, y, w, h int32, parent HWND, inst windows.Handle, param uintptr) (HWND, error) {
	r, _, e := syscall.SyscallN(procCreateWindowExW.Addr(), uintptr(exStyle), uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(parent), 0, uintptr(inst), param)
	if r == 0 {
		return 0, lastErr("CreateWindowExW", e)
	}
	return HWND(r), nil
}

// DestroyWindow destroys a window.
func DestroyWindow(h HWND) bool {
	r, _, _ := syscall.SyscallN(procDestroyWindow.Addr(), uintptr(h))
	return r != 0
}

// DefWindowProc does a message's default.
func DefWindowProc(h HWND, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procDefWindowProcW.Addr(), uintptr(h), uintptr(msg), wp, lp)
	return r
}

// ShowWindow shows, hides, minimises or maximises a window.
func ShowWindow(h HWND, cmd int32) bool {
	r, _, _ := syscall.SyscallN(procShowWindow.Addr(), uintptr(h), uintptr(cmd))
	return r != 0
}

// UpdateWindow paints a window's invalid area now (WM_PAINT).
// ShowScrollBar shows or hides h's scroll bar bar (SB_VERT).
func ShowScrollBar(h HWND, bar int32, show bool) {
	b := uintptr(0)
	if show {
		b = 1
	}
	syscall.SyscallN(procShowScrollBar.Addr(), uintptr(h), uintptr(bar), b)
}

// SB_VERT is ShowScrollBar's vertical bar.
const SB_VERT int32 = 1

func UpdateWindow(h HWND) {
	syscall.SyscallN(procUpdateWindow.Addr(), uintptr(h))
}

// GetMessage waits for the thread's next message (m must be heap memory).
func GetMessage(m *MSG) int32 {
	r, _, _ := syscall.SyscallN(procGetMessageW.Addr(), uintptr(unsafe.Pointer(m)), 0, 0, 0)
	return int32(r)
}

// TranslateMessage posts the character messages of a key message.
func TranslateMessage(m *MSG) {
	syscall.SyscallN(procTranslateMessage.Addr(), uintptr(unsafe.Pointer(m)))
}

// DispatchMessage runs a message's window procedure (m must be heap memory).
func DispatchMessage(m *MSG) {
	syscall.SyscallN(procDispatchMessageW.Addr(), uintptr(unsafe.Pointer(m)))
}

// PostMessage queues a message for h's thread. Any thread may call it.
func PostMessage(h HWND, msg uint32, wp, lp uintptr) error {
	r, _, e := syscall.SyscallN(procPostMessageW.Addr(), uintptr(h), uintptr(msg), wp, lp)
	if r == 0 {
		return lastErr("PostMessageW", e)
	}
	return nil
}

// PostThreadMessage queues a message for a thread, with no window. Any thread may call it.
func PostThreadMessage(thread uint32, msg uint32, wp, lp uintptr) error {
	r, _, e := syscall.SyscallN(procPostThreadMessageW.Addr(), uintptr(thread), uintptr(msg), wp, lp)
	if r == 0 {
		return lastErr("PostThreadMessageW", e)
	}
	return nil
}

// PostQuitMessage ends the thread's message loop.
func PostQuitMessage(code int32) {
	syscall.SyscallN(procPostQuitMessage.Addr(), uintptr(code))
}

// LoadCursor is one of the system's cursors (an IDC_ value).
func LoadCursor(id uintptr) windows.Handle {
	r, _, _ := syscall.SyscallN(procLoadCursorW.Addr(), 0, id)
	return windows.Handle(r)
}

// SetCursor sets the pointer's shape.
func SetCursor(c windows.Handle) {
	syscall.SyscallN(procSetCursor.Addr(), uintptr(c))
}

// BeginPaint starts painting (ps must be heap memory: it can send WM_ERASEBKGND).
func BeginPaint(h HWND, ps *PAINTSTRUCT) windows.Handle {
	r, _, _ := syscall.SyscallN(procBeginPaint.Addr(), uintptr(h), uintptr(unsafe.Pointer(ps)))
	return windows.Handle(r)
}

// EndPaint ends painting.
func EndPaint(h HWND, ps *PAINTSTRUCT) {
	syscall.SyscallN(procEndPaint.Addr(), uintptr(h), uintptr(unsafe.Pointer(ps)))
}

// InvalidateRect marks rc (nil: all) of h for painting.
func InvalidateRect(h HWND, rc *Rect, erase bool) {
	e := uintptr(0)
	if erase {
		e = 1
	}
	syscall.SyscallN(procInvalidateRect.Addr(), uintptr(h), uintptr(unsafe.Pointer(rc)), e)
}

// ValidateRect marks rc (nil: all) of h as painted.
func ValidateRect(h HWND, rc *Rect) {
	syscall.SyscallN(procValidateRect.Addr(), uintptr(h), uintptr(unsafe.Pointer(rc)))
}

// GetUpdateRect is the bounds of h's invalid area, or false when none is invalid.
func GetUpdateRect(h HWND) (Rect, bool) {
	var rc Rect
	r, _, _ := syscall.SyscallN(procGetUpdateRect.Addr(), uintptr(h), uintptr(unsafe.Pointer(&rc)), 0)
	return rc, r != 0
}

// GetClientRect is h's client area, (0, 0) at its top-left.
func GetClientRect(h HWND) Rect {
	var rc Rect
	syscall.SyscallN(procGetClientRect.Addr(), uintptr(h), uintptr(unsafe.Pointer(&rc)))
	return rc
}

// GetWindowRect is h's outer rectangle on the screen.
func GetWindowRect(h HWND) Rect {
	var rc Rect
	syscall.SyscallN(procGetWindowRect.Addr(), uintptr(h), uintptr(unsafe.Pointer(&rc)))
	return rc
}

// ScreenToClient maps a screen point to h's client coordinates.
func ScreenToClient(h HWND, p Point) Point {
	syscall.SyscallN(procScreenToClient.Addr(), uintptr(h), uintptr(unsafe.Pointer(&p)))
	return p
}

// ClientToScreen maps a client point of h to the screen.
func ClientToScreen(h HWND, p Point) Point {
	syscall.SyscallN(procClientToScreen.Addr(), uintptr(h), uintptr(unsafe.Pointer(&p)))
	return p
}

// SetWindowPos moves and sizes a window.
func SetWindowPos(h HWND, x, y, w, ht int32, flags uint32) bool {
	r, _, _ := syscall.SyscallN(procSetWindowPos.Addr(), uintptr(h), 0, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), uintptr(flags))
	return r != 0
}

// GetDpiForWindow is the DPI of h's monitor (96 at 100%).
func GetDpiForWindow(h HWND) uint32 {
	r, _, _ := syscall.SyscallN(procGetDpiForWindow.Addr(), uintptr(h))
	return uint32(r)
}

// SetProcessDpiAwarenessContext makes the process per-monitor DPI aware (v2). It fails when
// the awareness is already set (by the manifest, say), which is fine.
func SetProcessDpiAwarenessContext(ctx uintptr) bool {
	r, _, _ := syscall.SyscallN(procSetProcessDpiAwarenessContext.Addr(), ctx)
	return r != 0
}

// AdjustWindowRectExForDpi grows a client rectangle to its window rectangle at dpi.
func AdjustWindowRectExForDpi(rc *Rect, style, exStyle, dpi uint32) bool {
	r, _, _ := syscall.SyscallN(procAdjustWindowRectExForDpi.Addr(), uintptr(unsafe.Pointer(rc)), uintptr(style), 0,
		uintptr(exStyle), uintptr(dpi))
	return r != 0
}

// MonitorFromWindow is the monitor h is mostly on.
func MonitorFromWindow(h HWND, flags uint32) windows.Handle {
	r, _, _ := syscall.SyscallN(procMonitorFromWindow.Addr(), uintptr(h), uintptr(flags))
	return windows.Handle(r)
}

// wide reports a 64-bit target, where a POINT passed by value is one argument word (X in the
// low half); on 386 it is two words, X first.
const wide = unsafe.Sizeof(uintptr(0)) == 8

// pointWord is p as the one argument word of a 64-bit target (computed in uint64, so that it
// compiles for 386 too, where it is never used).
func pointWord(p Point) uintptr { return uintptr(uint64(uint32(p.X)) | uint64(uint32(p.Y))<<32) }

// MonitorFromPoint is the monitor of a screen point.
func MonitorFromPoint(p Point, flags uint32) windows.Handle {
	var r uintptr
	if wide {
		r, _, _ = syscall.SyscallN(procMonitorFromPoint.Addr(), pointWord(p), uintptr(flags))
	} else {
		r, _, _ = syscall.SyscallN(procMonitorFromPoint.Addr(), uintptr(uint32(p.X)), uintptr(uint32(p.Y)), uintptr(flags))
	}
	return windows.Handle(r)
}

// GetMonitorInfo is a monitor's rectangle and work area.
func GetMonitorInfo(m windows.Handle) (MONITORINFO, bool) {
	mi := MONITORINFO{Size: uint32(unsafe.Sizeof(MONITORINFO{}))}
	r, _, _ := syscall.SyscallN(procGetMonitorInfoW.Addr(), uintptr(m), uintptr(unsafe.Pointer(&mi)))
	return mi, r != 0
}

// IsIconic reports a minimised window.
func IsIconic(h HWND) bool {
	r, _, _ := syscall.SyscallN(procIsIconic.Addr(), uintptr(h))
	return r != 0
}

// IsChild reports whether child is a descendant of parent.
func IsChild(parent, child HWND) bool {
	r, _, _ := syscall.SyscallN(procIsChild.Addr(), uintptr(parent), uintptr(child))
	return r != 0
}

// TrackMouseEvent asks for WM_MOUSELEAVE.
func TrackMouseEvent(t *TRACKMOUSEEVENT) {
	syscall.SyscallN(procTrackMouseEvent.Addr(), uintptr(unsafe.Pointer(t)))
}

// SetCapture sends the mouse to h (it can send WM_CAPTURECHANGED to the old owner).
func SetCapture(h HWND) {
	syscall.SyscallN(procSetCapture.Addr(), uintptr(h))
}

// ReleaseCapture ends the mouse capture (it sends WM_CAPTURECHANGED).
func ReleaseCapture() {
	syscall.SyscallN(procReleaseCapture.Addr())
}

// GetCapture is the window that has the mouse capture on this thread, or 0.
func GetCapture() HWND {
	r, _, _ := syscall.SyscallN(procGetCapture.Addr())
	return HWND(r)
}

// KeyDown reports whether the virtual key is down (GetKeyState's high bit).
func KeyDown(vk int32) bool {
	r, _, _ := syscall.SyscallN(procGetKeyState.Addr(), uintptr(vk))
	return int16(r) < 0
}

// SetWindowText sets a window's title (title must stay alive: a heap string).
func SetWindowText(h HWND, title *uint16) {
	syscall.SyscallN(procSetWindowTextW.Addr(), uintptr(h), uintptr(unsafe.Pointer(title)))
}

// GetDoubleClickTime is the system's double-click time in milliseconds.
func GetDoubleClickTime() uint32 {
	r, _, _ := syscall.SyscallN(procGetDoubleClickTime.Addr())
	return uint32(r)
}

// GetSystemMetricsForDpi is a system metric at dpi.
func GetSystemMetricsForDpi(index int32, dpi uint32) int32 {
	r, _, _ := syscall.SyscallN(procGetSystemMetricsForDpi.Addr(), uintptr(index), uintptr(dpi))
	return int32(r)
}

// SystemParametersInfo reads a system parameter into p.
func SystemParametersInfo(action, param uint32, p unsafe.Pointer) bool {
	r, _, _ := syscall.SyscallN(procSystemParametersInfoW.Addr(), uintptr(action), uintptr(param), uintptr(p), 0)
	return r != 0
}

// GetSysColor is a system colour as 0x00BBGGRR.
func GetSysColor(index int32) uint32 {
	r, _, _ := syscall.SyscallN(procGetSysColor.Addr(), uintptr(index))
	return uint32(r)
}

// GetMessageTime is the time of the message being handled, in milliseconds.
func GetMessageTime() int32 {
	r, _, _ := syscall.SyscallN(procGetMessageTime.Addr())
	return int32(r)
}

// GetWindowPlacement is h's show state and restored rectangle (p must be heap memory).
func GetWindowPlacement(h HWND, p *WINDOWPLACEMENT) error {
	p.Length = uint32(unsafe.Sizeof(*p))
	r, _, e := syscall.SyscallN(procGetWindowPlacement.Addr(), uintptr(h), uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return lastErr("GetWindowPlacement", e)
	}
	return nil
}

// SetWindowPlacement sets h's show state and restored rectangle (p must be heap memory).
func SetWindowPlacement(h HWND, p *WINDOWPLACEMENT) error {
	p.Length = uint32(unsafe.Sizeof(*p))
	r, _, e := syscall.SyscallN(procSetWindowPlacement.Addr(), uintptr(h), uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return lastErr("SetWindowPlacement", e)
	}
	return nil
}

// GetModuleHandle is the executable's module handle.
func GetModuleHandle() windows.Handle {
	r, _, _ := syscall.SyscallN(procGetModuleHandleW.Addr(), 0)
	return windows.Handle(r)
}

// DwmSetWindowAttribute sets a DWM attribute of h to the size bytes at v.
func DwmSetWindowAttribute(h HWND, attr uint32, v unsafe.Pointer, size uint32) error {
	r, _, _ := syscall.SyscallN(procDwmSetWindowAttribute.Addr(), uintptr(h), uintptr(attr), uintptr(v), uintptr(size))
	if int32(r) < 0 {
		return fmt.Errorf("DwmSetWindowAttribute(%d): HRESULT %#08x", attr, uint32(r))
	}
	return nil
}

// DwmFlush waits for the desktop compositor's next frame.
func DwmFlush() error {
	r, _, _ := syscall.SyscallN(procDwmFlush.Addr())
	if int32(r) < 0 {
		return fmt.Errorf("DwmFlush: HRESULT %#08x", uint32(r))
	}
	return nil
}

// ImmGetContext is h's input context (release it).
func ImmGetContext(h HWND) uintptr {
	r, _, _ := syscall.SyscallN(procImmGetContext.Addr(), uintptr(h))
	return r
}

// ImmReleaseContext releases an input context.
func ImmReleaseContext(h HWND, imc uintptr) {
	syscall.SyscallN(procImmReleaseContext.Addr(), uintptr(h), imc)
}

// ImmSetCompositionWindow places the input method's composition window.
func ImmSetCompositionWindow(imc uintptr, f *COMPOSITIONFORM) {
	syscall.SyscallN(procImmSetCompositionWindow.Addr(), imc, uintptr(unsafe.Pointer(f)))
}

// ImmSetCandidateWindow places the input method's candidate list.
func ImmSetCandidateWindow(imc uintptr, f *CANDIDATEFORM) {
	syscall.SyscallN(procImmSetCandidateWindow.Addr(), imc, uintptr(unsafe.Pointer(f)))
}

// ImmAssociateContextEx gives h the default input context (IACE_DEFAULT) or none (0).
func ImmAssociateContextEx(h HWND, flags uint32) {
	syscall.SyscallN(procImmAssociateContextEx.Addr(), uintptr(h), 0, uintptr(flags))
}

// CreateSolidBrush makes a GDI brush of the colour 0x00BBGGRR.
func CreateSolidBrush(bgr uint32) windows.Handle {
	r, _, _ := syscall.SyscallN(procCreateSolidBrush.Addr(), uintptr(bgr))
	return windows.Handle(r)
}

// DeleteObject frees a GDI object.
func DeleteObject(o windows.Handle) {
	syscall.SyscallN(procDeleteObject.Addr(), uintptr(o))
}

// FillRect fills rc of a device context with a brush.
func FillRect(hdc windows.Handle, rc *Rect, brush windows.Handle) {
	syscall.SyscallN(procFillRect.Addr(), uintptr(hdc), uintptr(unsafe.Pointer(rc)), uintptr(brush))
}

// Clipboard functions: OpenClipboard (with the owner window), CloseClipboard, EmptyClipboard,
// GetClipboardData and SetClipboardData, and the HGLOBAL calls they need.

// OpenClipboard opens the clipboard for h.
func OpenClipboard(h HWND) error {
	r, _, e := syscall.SyscallN(procOpenClipboard.Addr(), uintptr(h))
	if r == 0 {
		return lastErr("OpenClipboard", e)
	}
	return nil
}

// CloseClipboard closes it.
func CloseClipboard() {
	syscall.SyscallN(procCloseClipboard.Addr())
}

// EmptyClipboard empties it and makes the opener its owner.
func EmptyClipboard() error {
	r, _, e := syscall.SyscallN(procEmptyClipboard.Addr())
	if r == 0 {
		return lastErr("EmptyClipboard", e)
	}
	return nil
}

// GetClipboardData is the clipboard's data in a format, 0 when it has none.
func GetClipboardData(format uint32) uintptr {
	r, _, _ := syscall.SyscallN(procGetClipboardData.Addr(), uintptr(format))
	return r
}

// SetClipboardData gives the system mem (an HGLOBAL) as the clipboard's data in a format.
func SetClipboardData(format uint32, mem uintptr) error {
	r, _, e := syscall.SyscallN(procSetClipboardData.Addr(), uintptr(format), mem)
	if r == 0 {
		return lastErr("SetClipboardData", e)
	}
	return nil
}

// GlobalAlloc allocates movable global memory.
func GlobalAlloc(size uintptr) (uintptr, error) {
	r, _, e := syscall.SyscallN(procGlobalAlloc.Addr(), GMEM_MOVEABLE, size)
	if r == 0 {
		return 0, lastErr("GlobalAlloc", e)
	}
	return r, nil
}

// GlobalFree frees it.
func GlobalFree(mem uintptr) {
	syscall.SyscallN(procGlobalFree.Addr(), mem)
}

// GlobalLock locks it and returns its address, 0 on failure.
func GlobalLock(mem uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procGlobalLock.Addr(), mem)
	return r
}

// GlobalUnlock unlocks it.
func GlobalUnlock(mem uintptr) {
	syscall.SyscallN(procGlobalUnlock.Addr(), mem)
}

// GlobalSize is its size in bytes.
func GlobalSize(mem uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procGlobalSize.Addr(), mem)
	return r
}

// NewCallback makes a window procedure that Win32 can call. Each call makes one of the
// process's limited callbacks, so it is made once per process.
func NewCallback(fn func(h HWND, msg uint32, wp, lp uintptr) uintptr) uintptr {
	return syscall.NewCallback(fn)
}

// DwmExtendFrameIntoClientArea extends the window's frame into its client area by m (m must
// be heap memory: the change can reach the window procedure).
func DwmExtendFrameIntoClientArea(h HWND, m *MARGINS) error {
	r, _, _ := syscall.SyscallN(procDwmExtendFrameIntoClientArea.Addr(), uintptr(h), uintptr(unsafe.Pointer(m)))
	if int32(r) < 0 {
		return fmt.Errorf("DwmExtendFrameIntoClientArea: HRESULT %#08x", uint32(r))
	}
	return nil
}

// IsZoomed reports a maximised window.
func IsZoomed(h HWND) bool {
	r, _, _ := syscall.SyscallN(procIsZoomed.Addr(), uintptr(h))
	return r != 0
}

// GetCursorPos is the pointer's position on the screen, in physical pixels for a
// per-monitor aware thread.
func GetCursorPos() (Point, bool) {
	var p Point
	r, _, _ := syscall.SyscallN(procGetCursorPos.Addr(), uintptr(unsafe.Pointer(&p)))
	return p, r != 0
}

// WindowFromPoint is the window at a screen point (it can send WM_NCHITTEST to the
// thread's own windows).
func WindowFromPoint(p Point) HWND {
	var r uintptr
	if wide {
		r, _, _ = syscall.SyscallN(procWindowFromPoint.Addr(), pointWord(p))
	} else {
		r, _, _ = syscall.SyscallN(procWindowFromPoint.Addr(), uintptr(uint32(p.X)), uintptr(uint32(p.Y)))
	}
	return HWND(r)
}

// GetAncestor is h's ancestor of a kind (GA_ROOT: its top-level window).
func GetAncestor(h HWND, flags uint32) HWND {
	r, _, _ := syscall.SyscallN(procGetAncestor.Addr(), uintptr(h), uintptr(flags))
	return HWND(r)
}

// SetWindowLongPtr sets a window's value at index (GWLP_WNDPROC: its procedure) and returns
// the old one.
func SetWindowLongPtr(h HWND, index int32, v uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procSetWindowLongPtrW.Addr(), uintptr(h), uintptr(index), v)
	return r
}

// GetWindowLongPtr is a window's value at index (GWL_STYLE: its style).
func GetWindowLongPtr(h HWND, index int32) uintptr {
	r, _, _ := syscall.SyscallN(procGetWindowLongPtrW.Addr(), uintptr(h), uintptr(index))
	return r
}

// CallWindowProc runs the window procedure proc (a subclassed control's own).
func CallWindowProc(proc uintptr, h HWND, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procCallWindowProcW.Addr(), proc, uintptr(h), uintptr(msg), wp, lp)
	return r
}

// SendMessage runs h's window procedure for a message now (lp, when it points, must point
// to heap memory).
func SendMessage(h HWND, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procSendMessageW.Addr(), uintptr(h), uintptr(msg), wp, lp)
	return r
}

// SetFocus gives h the keyboard focus and returns the window that had it.
func SetFocus(h HWND) HWND {
	r, _, _ := syscall.SyscallN(procSetFocus.Addr(), uintptr(h))
	return HWND(r)
}

// GetFocus is the thread's window with the keyboard focus, or 0.
func GetFocus() HWND {
	r, _, _ := syscall.SyscallN(procGetFocus.Addr())
	return HWND(r)
}

// GetDC is h's client-area device context (release it).
func GetDC(h HWND) windows.Handle {
	r, _, _ := syscall.SyscallN(procGetDC.Addr(), uintptr(h))
	return windows.Handle(r)
}

// ReleaseDC releases a device context of h.
func ReleaseDC(h HWND, dc windows.Handle) {
	syscall.SyscallN(procReleaseDC.Addr(), uintptr(h), uintptr(dc))
}

// HideCaret hides h's caret.
func HideCaret(h HWND) { syscall.SyscallN(procHideCaret.Addr(), uintptr(h)) }

// ShowCaret shows h's caret again.
func ShowCaret(h HWND) { syscall.SyscallN(procShowCaret.Addr(), uintptr(h)) }

// GetWindowText copies h's text into buf, which must be heap memory (a control of this
// thread answers WM_GETTEXT in its procedure), and returns the units copied.
func GetWindowText(h HWND, buf []uint16) int {
	if len(buf) == 0 {
		return 0
	}
	r, _, _ := syscall.SyscallN(procGetWindowTextW.Addr(), uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return int(r)
}

// GetWindowTextLength is the length of h's text in UTF-16 units.
func GetWindowTextLength(h HWND) int {
	r, _, _ := syscall.SyscallN(procGetWindowTextLengthW.Addr(), uintptr(h))
	return int(r)
}

// LoadIcon loads the icon resource id of the module at a size in pixels (0: the system's).
// The icon is not shared: free it with DestroyIcon.
func LoadIcon(inst windows.Handle, id uint16, size int32) windows.Handle {
	r, _, _ := syscall.SyscallN(procLoadImageW.Addr(), uintptr(inst), uintptr(id), IMAGE_ICON, uintptr(size), uintptr(size), LR_DEFAULTCOLOR)
	return windows.Handle(r)
}

// CreateFont makes a GDI font of a family (heap memory: the name) at a character height in
// pixels and a weight.
func CreateFont(height, weight int32, face *uint16) windows.Handle {
	r, _, _ := syscall.SyscallN(procCreateFontW.Addr(), uintptr(-height), 0, 0, 0, uintptr(weight), 0, 0, 0,
		DEFAULT_CHARSET, OUT_TT_PRECIS, CLIP_DEFAULT_PRECIS, CLEARTYPE_QUALITY, 0, uintptr(unsafe.Pointer(face)))
	return windows.Handle(r)
}

// FontMetrics are a GDI font's text metrics, read through a memory device context.
func FontMetrics(font windows.Handle) (TEXTMETRICW, bool) {
	var tm TEXTMETRICW
	dc, _, _ := syscall.SyscallN(procCreateCompatibleDC.Addr(), 0)
	if dc == 0 {
		return tm, false
	}
	defer syscall.SyscallN(procDeleteDC.Addr(), dc)
	old, _, _ := syscall.SyscallN(procSelectObject.Addr(), dc, uintptr(font))
	r, _, _ := syscall.SyscallN(procGetTextMetricsW.Addr(), dc, uintptr(unsafe.Pointer(&tm)))
	syscall.SyscallN(procSelectObject.Addr(), dc, old)
	return tm, r != 0
}

// SetTextColor sets a device context's text colour (0x00BBGGRR).
func SetTextColor(dc windows.Handle, bgr uint32) {
	syscall.SyscallN(procSetTextColor.Addr(), uintptr(dc), uintptr(bgr))
}

// SetBkColor sets a device context's ground colour (0x00BBGGRR).
func SetBkColor(dc windows.Handle, bgr uint32) {
	syscall.SyscallN(procSetBkColor.Addr(), uintptr(dc), uintptr(bgr))
}

// SHAppBarMessage sends an appbar message to the taskbar (d must be heap memory: the call
// waits on the shell, and the thread's window procedures may run meanwhile). shell32 is
// loaded at the first call, not by Load: a window that is never maximised does not pay for
// it. It returns 0 when shell32 cannot be loaded.
func SHAppBarMessage(msg uint32, d *APPBARDATA) uintptr {
	if procSHAppBarMessage.Find() != nil {
		return 0
	}
	d.Size = uint32(unsafe.Sizeof(*d))
	r, _, _ := syscall.SyscallN(procSHAppBarMessage.Addr(), uintptr(msg), uintptr(unsafe.Pointer(d)))
	return r
}

// DestroyIcon frees an icon that LoadIcon made.
func DestroyIcon(icon windows.Handle) {
	if icon != 0 {
		syscall.SyscallN(procDestroyIcon.Addr(), uintptr(icon))
	}
}

// GetDpiForMonitor is a monitor's effective DPI (MDT_EFFECTIVE_DPI), or 0 when it cannot be
// read. shcore is loaded at the first call, not by Load.
func GetDpiForMonitor(m windows.Handle) uint32 {
	if procGetDpiForMonitor.Find() != nil {
		return 0
	}
	var x, y uint32
	r, _, _ := syscall.SyscallN(procGetDpiForMonitor.Addr(), uintptr(m), 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	if int32(r) < 0 {
		return 0
	}
	return x
}
