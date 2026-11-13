//go:build windows

package clipboard

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestStartClose creates and destroys real listener windows without touching the
// clipboard's content.
func TestStartClose(t *testing.T) {
	for range 3 {
		m, err := Start(func(string) {}, nil)
		if err != nil {
			t.Fatal(err)
		}
		watchersMu.Lock()
		n := len(watchers)
		watchersMu.Unlock()
		if n != 1 {
			t.Fatalf("%d listener windows, want 1", n)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		<-m.Done()
		watchersMu.Lock()
		n = len(watchers)
		watchersMu.Unlock()
		if n != 0 {
			t.Fatalf("%d listener windows after Close, want 0", n)
		}
	}
}

var (
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
)

// setClipboard replaces the clipboard's content with text and, when given, the
// CanIncludeInClipboardHistory DWORD or the exclusion format.
func setClipboard(t *testing.T, text string, history *uint32, exclude bool) {
	t.Helper()
	var opened bool
	for range 20 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		t.Fatal("cannot open the clipboard")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	put := func(format uintptr, data []byte) {
		h, _, err := procGlobalAlloc.Call(0x0002 /* GMEM_MOVEABLE */, uintptr(len(data)))
		if h == 0 {
			t.Fatalf("GlobalAlloc: %v", err)
		}
		p, _, _ := procGlobalLock.Call(h)
		copy(unsafe.Slice((*byte)(pointer(p)), len(data)), data)
		procGlobalUnlock.Call(h)
		if r, _, err := procSetClipboardData.Call(format, h); r == 0 {
			procGlobalFree.Call(h)
			t.Fatalf("SetClipboardData: %v", err)
		}
	}
	u := windows.StringToUTF16(text)
	put(cfUnicodeText, unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2))
	if history != nil {
		v := *history
		put(cfHistory, unsafe.Slice((*byte)(unsafe.Pointer(&v)), 4))
	}
	if exclude {
		put(cfExclude, []byte{0})
	}
}

// TestRealClipboard changes the clipboard's content, so it runs only when asked for
// with DOWNEX_TEST_REAL_CLIPBOARD=1.
func TestRealClipboard(t *testing.T) {
	if os.Getenv("DOWNEX_TEST_REAL_CLIPBOARD") != "1" {
		t.Skip("changes the clipboard: set DOWNEX_TEST_REAL_CLIPBOARD=1 to run")
	}
	got := make(chan string, 8)
	m, err := Start(func(s string) { got <- s }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	zero, one := uint32(0), uint32(1)
	expect := func(want string) {
		t.Helper()
		select {
		case s := <-got:
			if s != want {
				t.Fatalf("got %q, want %q", s, want)
			}
		case <-time.After(5 * time.Second):
			if want != "" {
				t.Fatalf("%q not seen", want)
			}
		}
	}
	setClipboard(t, "https://example.com/plain.zip", nil, false)
	expect("https://example.com/plain.zip")
	setClipboard(t, "https://example.com/history1.zip", &one, false)
	expect("https://example.com/history1.zip")
	setClipboard(t, "https://example.com/secret.zip", &zero, false)
	setClipboard(t, "https://example.com/excluded.zip", nil, true)
	setClipboard(t, "https://example.com/after.zip", nil, false)
	expect("https://example.com/after.zip") // the two before were ignored
	setClipboard(t, "", nil, false)
}
