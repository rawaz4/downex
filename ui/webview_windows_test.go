//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wailsapp/go-webview2/pkg/edge"
)

// The shape webviewOf reads in Wails v2's Windows frontend: the WebView in an unexported
// chromium field and the window's Invoke and Handle promoted from an embedded control.
type (
	fakeHWND    uintptr
	fakeControl struct{ handle fakeHWND }
	fakeWindow  struct{ fakeControl }
	fakeFront   struct {
		chromium   *edge.Chromium
		mainWindow *fakeWindow
	}
)

func (c *fakeControl) Invoke(f func()) bool { f(); return true }
func (c *fakeControl) Handle() fakeHWND     { return c.handle }

func frontendCtx(fe any) context.Context {
	return context.WithValue(context.Background(), "frontend", fe) //nolint:staticcheck // Wails' own key
}

func TestWebviewOfReadsWailsFrontend(t *testing.T) {
	ch := &edge.Chromium{}
	wv := webviewOf(frontendCtx(&fakeFront{chromium: ch, mainWindow: &fakeWindow{fakeControl{handle: 42}}}))
	if wv == nil {
		t.Fatal("webviewOf found nothing in the frontend's shape")
	}
	if wv.chromium != ch || wv.hwnd != 42 {
		t.Fatalf("webview %+v, want chromium %p and window 42", wv, ch)
	}
	ran := false
	if !wv.invoke(func() { ran = true }) || !ran {
		t.Fatal("invoke did not run the function through the window's Invoke")
	}
}

func TestWebviewOfOtherShapes(t *testing.T) {
	type noWindow struct{ chromium *edge.Chromium }
	type wrongType struct {
		chromium   *int
		mainWindow *fakeWindow
	}
	type plainWindow struct{}
	type noInvoke struct {
		chromium   *edge.Chromium
		mainWindow *plainWindow
	}
	win := &fakeWindow{fakeControl{handle: 42}}
	for name, fe := range map[string]any{
		"no frontend":    nil,
		"not a pointer":  fakeFront{chromium: &edge.Chromium{}, mainWindow: win},
		"nil webview":    &fakeFront{mainWindow: win},
		"no window":      &noWindow{chromium: &edge.Chromium{}},
		"wrong type":     &wrongType{chromium: new(int), mainWindow: win},
		"no Invoke":      &noInvoke{chromium: &edge.Chromium{}, mainWindow: &plainWindow{}},
		"no window yet":  &fakeFront{chromium: &edge.Chromium{}, mainWindow: &fakeWindow{}},
		"something else": "frontend",
	} {
		if wv := webviewOf(frontendCtx(fe)); wv != nil {
			t.Errorf("%s: webviewOf = %+v, want nil", name, wv)
		}
	}
}

func TestSetMemoryLowWhenTheWindowIsGone(t *testing.T) {
	wv := &webview{chromium: &edge.Chromium{}, invoke: func(func()) bool { return false }, hwnd: 42}
	if err := wv.setMemoryLow(context.Background(), true); !errors.Is(err, errWindowGone) {
		t.Fatalf("setMemoryLow with a closed window: %v, want errWindowGone", err)
	}
	// A WebView without its controller yet (or any more) is gone as well.
	wv.invoke = func(f func()) bool { f(); return true }
	if err := wv.setMemoryLow(context.Background(), false); !errors.Is(err, errWindowGone) {
		t.Fatalf("setMemoryLow without a controller: %v, want errWindowGone", err)
	}
}

// A window thread that is busy is not a closed window: the watch goes on, and the set that
// is still queued runs before any later one.
func TestSetMemoryLowWhenTheWindowIsBusy(t *testing.T) {
	prev := invokeTimeout
	invokeTimeout = 10 * time.Millisecond
	t.Cleanup(func() { invokeTimeout = prev })
	wv := &webview{chromium: &edge.Chromium{}, invoke: func(func()) bool { return true }, hwnd: 42}
	if err := wv.setMemoryLow(context.Background(), true); !errors.Is(err, errBusy) {
		t.Fatalf("setMemoryLow with a busy window: %v, want errBusy", err)
	}
}
