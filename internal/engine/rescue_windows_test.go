package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"downex/internal/testserver"
)

// instantClock is the real clock whose timers fire at once: the rename's back-off
// (retrySharing, about 7 s) passes without waiting.
type instantClock struct{ realClock }

func (instantClock) NewTimer(time.Duration) Timer { return realClock{}.NewTimer(0) }

// TestRescueSharingViolation: a partial another program holds open without
// FILE_SHARE_DELETE (a browser still writing, a scanner) cannot be renamed: after the
// retries the rescue is ErrPartialLocked and nothing is left behind.
func TestRescueSharingViolation(t *testing.T) {
	t.Parallel()
	o := fastOpts()
	o.Clock = instantClock{}
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 91}, o)
	partial := writePartial(t, h, "busy.bin.crdownload", 1<<20+1, nil)
	p, err := windows.UTF16PtrFromString(partial)
	if err != nil {
		t.Fatal(err)
	}
	hd, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	was := stateOf(t, partial)
	_, err = h.rescue(RescueInput{Partial: partial})
	_ = windows.CloseHandle(hd)
	if !errors.Is(err, ErrPartialLocked) {
		t.Fatalf("Rescue = %v, want ErrPartialLocked", err)
	}
	checkUntouched(t, h, partial, was)
	if parts, _ := filepath.Glob(filepath.Join(h.dir, "*"+partExt)); len(parts) != 0 {
		t.Fatalf("part files left: %v", parts)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal(err)
	}
}
