//go:build windows

package win

import (
	"bytes"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"downex/ui/win/internal/w32"
)

var instanceSeq atomic.Int32

// testNames are a lock's names of this test alone, in the host's pattern.
func testNames() InstanceNames {
	base := fmt.Sprintf("wails-app-downex-win-test-%d-%d", os.Getpid(), instanceSeq.Add(1))
	return InstanceNames{Mutex: base + "sim", Class: base + "-sic", Window: base + "-siw"}
}

// The first start holds the lock and listens; a later start finds the listener and hands
// its payload over by WM_COPYDATA, which OnData gets after the send returned; other data
// kinds and oversized payloads are ignored; WM_CLOSE asks OnClose and the listener stays;
// Close lets the lock go.
func TestInstanceHandsOver(t *testing.T) {
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	n := testNames()
	first, running, err := ClaimInstance(n, 0)
	if err != nil || running.OK() || !first.First() {
		t.Fatalf("first claim: %v %v %v", first, running, err)
	}
	var got [][]byte
	var closes int
	if err := first.Listen(ListenOptions{Kind: 1542, Max: 64,
		OnData:  func(b []byte) { got = append(got, b) },
		OnClose: func() { closes++ },
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := first.Listen(ListenOptions{Kind: 1542, Max: 64}); err == nil {
		t.Fatal("Listen twice")
	}

	second, running, err := ClaimInstance(n, time.Second)
	if err != nil || second != nil || !running.OK() {
		t.Fatalf("second claim: %v %v %v", second, running, err)
	}
	payload := []byte("{\x00\"A\x00\x00\x00") // bytes as they are: the receiver decodes them
	sent := make(chan error, 4)
	go func() { // from another thread, as another process sends
		sent <- running.Forward(1542, payload, 5*time.Second)
		sent <- running.Forward(77, []byte("other kind"), 5*time.Second)
		sent <- running.Forward(1542, bytes.Repeat([]byte("x"), 65), 5*time.Second)
		sent <- running.Forward(1542, []byte("again"), 5*time.Second)
	}()
	pump(t, a, w, "two hand-offs", 10*time.Second, func() bool { return len(got) >= 2 })
	for i := 0; i < 4; i++ {
		if err := <-sent; err != nil {
			t.Errorf("Forward %d: %v", i, err)
		}
	}
	if !bytes.Equal(got[0], payload) || string(got[1]) != "again" || len(got) != 2 {
		t.Fatalf("received %q", got)
	}
	w32.SendMessage(w32.HWND(running.Window()), w32.WM_CLOSE, 0, 0)
	if closes != 1 || findWindow(n.Class, n.Window) == 0 {
		t.Fatalf("WM_CLOSE: OnClose %d times, listener there %v", closes, findWindow(n.Class, n.Window) != 0)
	}
	first.Close()
	if findWindow(n.Class, n.Window) != 0 {
		t.Fatal("the listener outlived Close")
	}
	again, running, err := ClaimInstance(n, 0)
	if err != nil || running.OK() || !again.First() {
		t.Fatalf("claim after Close: %v %v %v", again, running, err)
	}
	again.Close()
}

// A mutex without a listener (a window that never listened, or one that is closing) does
// not keep a start waiting longer than wait: it opens a window of its own.
func TestInstanceWithoutListener(t *testing.T) {
	n := testNames()
	first, _, err := ClaimInstance(n, 0)
	if err != nil || !first.First() {
		t.Fatal(err)
	}
	defer first.Close()
	start := time.Now()
	second, running, err := ClaimInstance(n, 150*time.Millisecond)
	took := time.Since(start)
	if err != nil || running.OK() || second == nil || second.First() {
		t.Fatalf("second claim: %v %v %v", second, running, err)
	}
	second.Close()
	if took < 150*time.Millisecond || took > 2*time.Second {
		t.Errorf("waited %v for a listener, want about 150 ms", took)
	}
	if _, _, err := ClaimInstance(InstanceNames{}, 0); err == nil {
		t.Error("empty names accepted")
	}
	if err := (Running{}).Forward(1542, nil, time.Second); err == nil {
		t.Error("Forward without a window")
	}
}

// A second start brings the window to the front: a minimised one is restored to the state
// it had (maximised stays maximised), a hidden one shown. On the test's own desktop
// SetForegroundWindow cannot take the user's foreground.
func TestBringToFront(t *testing.T) {
	_ = onTestDesktop(t)
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	visible := func() bool { return w32.ShowWindow(w.hwnd, w32.SW_SHOWNA) } // reports the previous visibility
	w32.ShowWindow(w.hwnd, 7 /* SW_SHOWMINNOACTIVE */)
	pump(t, a, w, "minimised", 5*time.Second, func() bool { return w32.IsIconic(w.hwnd) })
	fg := w.BringToFront()
	if w32.IsIconic(w.hwnd) {
		t.Fatal("still minimised")
	}
	t.Logf("SetForegroundWindow on the test desktop answered %v", fg)

	w32.ShowWindow(w.hwnd, w32.SW_MAXIMIZE)
	w32.ShowWindow(w.hwnd, 7 /* SW_SHOWMINNOACTIVE */)
	pump(t, a, w, "minimised", 5*time.Second, func() bool { return w32.IsIconic(w.hwnd) })
	w.BringToFront()
	if w32.IsIconic(w.hwnd) || !w32.IsZoomed(w.hwnd) {
		t.Fatalf("a maximised window came back iconic %v, zoomed %v", w32.IsIconic(w.hwnd), w32.IsZoomed(w.hwnd))
	}
	w32.ShowWindow(w.hwnd, w32.SW_RESTORE)
	w32.ShowWindow(w.hwnd, 0 /* SW_HIDE */)
	w.BringToFront()
	if !visible() {
		t.Fatal("a hidden window stayed hidden")
	}
}

// The listener keeps at most MaxPendingHandoffs payloads its thread has not handed on yet:
// a WM_COPYDATA beyond them is refused (answered 0), so a flood from any process on the
// desktop while the UI thread is busy cannot grow it without bound.
func TestListenerBoundsWhatWaits(t *testing.T) {
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	n := testNames()
	first, _, err := ClaimInstance(n, 0)
	if err != nil || !first.First() {
		t.Fatalf("claim: %v %v", first, err)
	}
	defer first.Close()
	var got int
	if err := first.Listen(ListenOptions{Kind: 1542, Max: 64, OnData: func([]byte) { got++ }}); err != nil {
		t.Fatal(err)
	}
	h := findWindow(n.Class, n.Window)
	payload := []byte("x")
	cds := &copyDataStruct{data: 1542, size: uint32(len(payload)), ptr: uintptr(unsafe.Pointer(&payload[0]))}
	var taken, refused int
	for i := 0; i < MaxPendingHandoffs+5; i++ { // on the listener's own thread: nothing is handed on in between
		if w32.SendMessage(h, wmCopyData, 0, uintptr(unsafe.Pointer(cds))) != 0 {
			taken++
		} else {
			refused++
		}
	}
	if taken != MaxPendingHandoffs || refused != 5 {
		t.Errorf("taken %d, refused %d; want %d and 5", taken, refused, MaxPendingHandoffs)
	}
	pump(t, a, w, "the waiting hand-offs", 5*time.Second, func() bool { return got >= MaxPendingHandoffs })
	if got != MaxPendingHandoffs {
		t.Errorf("handed on %d, want %d", got, MaxPendingHandoffs)
	}
	if w32.SendMessage(h, wmCopyData, 0, uintptr(unsafe.Pointer(cds))) == 0 {
		t.Error("once handed on, a new hand-off is refused")
	}
}
