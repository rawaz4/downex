//go:build windows

package win

import (
	"fmt"
	"time"
	"unicode/utf16"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"
)

// openClipboard opens the clipboard for h, trying a few times over 40 ms: clipboard
// managers hold it briefly after every change.
func openClipboard(h w32.HWND) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = w32.OpenClipboard(h); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("win: the clipboard is held by another program: %w", err)
}

// ClipboardText implements kit.Host.
func (w *Window) ClipboardText() (string, error) {
	if w.closed {
		return "", fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	if err := openClipboard(w.hwnd); err != nil {
		return "", err
	}
	defer w32.CloseClipboard()
	h := w32.GetClipboardData(w32.CF_UNICODETEXT)
	if h == 0 {
		return "", nil
	}
	p := w32.GlobalLock(h)
	if p == 0 {
		return "", fmt.Errorf("win: the clipboard's text cannot be read")
	}
	defer w32.GlobalUnlock(h)
	units := unsafe.Slice((*uint16)(com.Addr(p)), w32.GlobalSize(h)/2)
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
	}
	return string(utf16.Decode(units)), nil
}

// SetClipboardText implements kit.Host.
func (w *Window) SetClipboardText(s string) error {
	if w.closed {
		return fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	u := utf16.Encode([]rune(stripNUL(s)))
	u = append(u, 0)
	mem, err := w32.GlobalAlloc(uintptr(len(u)) * 2)
	if err != nil {
		return fmt.Errorf("win: %w", err)
	}
	p := w32.GlobalLock(mem)
	if p == 0 {
		w32.GlobalFree(mem)
		return fmt.Errorf("win: GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(com.Addr(p)), len(u)), u)
	w32.GlobalUnlock(mem)
	if err := openClipboard(w.hwnd); err != nil {
		w32.GlobalFree(mem)
		return err
	}
	defer w32.CloseClipboard()
	if err := w32.EmptyClipboard(); err != nil {
		w32.GlobalFree(mem)
		return fmt.Errorf("win: %w", err)
	}
	if err := w32.SetClipboardData(w32.CF_UNICODETEXT, mem); err != nil {
		w32.GlobalFree(mem) // the system took it only on success
		return fmt.Errorf("win: %w", err)
	}
	return nil
}
