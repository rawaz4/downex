package kittest

import (
	"errors"
	"sync"
	"testing"

	"downex/ui/kit"
)

// TestHostCloseWhilePosting closes the host while other goroutines post, as a view's
// background work does when its window closes; under -race, Close and Post must not race
// on Closed. After Close, Post refuses.
func TestHostCloseWhilePosting(t *testing.T) {
	h := NewHost(kit.Size{W: 100, H: 100}, kit.Environment{})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 200 {
				if err := h.Post(func() {}); err != nil && !errors.Is(err, kit.ErrClosed) {
					t.Errorf("Post: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	h.Close()
	wg.Wait()
	if !h.IsClosed() || !h.Closed {
		t.Fatal("the host is not closed")
	}
	if err := h.Post(func() {}); !errors.Is(err, kit.ErrClosed) {
		t.Fatalf("Post after Close = %v, want kit.ErrClosed", err)
	}
	if _, err := h.ClipboardText(); !errors.Is(err, kit.ErrClosed) {
		t.Fatalf("ClipboardText after Close = %v, want kit.ErrClosed", err)
	}
}
