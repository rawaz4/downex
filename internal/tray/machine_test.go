package tray

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// fakeShell records the shell calls of a machine.
type fakeShell struct {
	calls    []string
	addFails int // the next n adds fail
	exists   bool
	menuCmd  Command
	menuOK   bool
}

func (f *fakeShell) add(tip string) error {
	f.calls = append(f.calls, "add "+tip)
	if f.addFails > 0 {
		f.addFails--
		return errors.New("the taskbar is not there")
	}
	f.exists = true
	return nil
}

func (f *fakeShell) setTip(tip string) error {
	f.calls = append(f.calls, "tip "+tip)
	if !f.exists {
		return errors.New("no icon")
	}
	return nil
}

func (f *fakeShell) balloon(tip string, b Balloon) error {
	kind := "info"
	if b.Error {
		kind = "error"
	}
	f.calls = append(f.calls, fmt.Sprintf("balloon %s %s|%s (tip %s)", kind, b.Title, b.Text, tip))
	return nil
}

func (f *fakeShell) remove() error {
	f.calls = append(f.calls, "remove")
	if !f.exists {
		return errors.New("no icon")
	}
	f.exists = false
	return nil
}

func (f *fakeShell) popupMenu(x, y int32) (Command, bool) {
	f.calls = append(f.calls, fmt.Sprintf("menu %d,%d", x, y))
	return f.menuCmd, f.menuOK
}

func (f *fakeShell) retry(on bool) {
	if on {
		f.calls = append(f.calls, "retry on")
	} else {
		f.calls = append(f.calls, "retry off")
	}
}

// take returns and clears the recorded calls.
func (f *fakeShell) take() []string {
	c := f.calls
	f.calls = nil
	return c
}

func expectCalls(t *testing.T, what string, f *fakeShell, want ...string) {
	t.Helper()
	if got := f.take(); !slices.Equal(got, want) {
		t.Errorf("%s: shell calls %q, want %q", what, got, want)
	}
}

func TestMachineShowHideAndTooltip(t *testing.T) {
	f := &fakeShell{}
	m := newMachine(f, "Downex", nil)
	m.setTooltip("Downex — 0 active, 0 B/s")
	expectCalls(t, "tooltip while hidden", f) // remembered, not sent
	m.notify(Balloon{Title: "dropped"})
	expectCalls(t, "notification while hidden", f)

	m.setVisible(true)
	expectCalls(t, "show", f, "add Downex — 0 active, 0 B/s")
	m.setVisible(true)
	expectCalls(t, "show again", f)
	m.setTooltip("Downex — 0 active, 0 B/s")
	expectCalls(t, "same tooltip", f)
	m.setTooltip("Downex — 1 active, 5.0 MB/s")
	expectCalls(t, "new tooltip", f, "tip Downex — 1 active, 5.0 MB/s")
	m.notify(Balloon{Title: "Download complete", Text: "a.zip"})
	m.notify(Balloon{Title: "Download failed", Text: "b.zip\nThe disk is full.", Error: true})
	expectCalls(t, "notifications", f,
		"balloon info Download complete|a.zip (tip Downex — 1 active, 5.0 MB/s)",
		"balloon error Download failed|b.zip\nThe disk is full. (tip Downex — 1 active, 5.0 MB/s)")

	m.setVisible(false)
	expectCalls(t, "hide", f, "remove")
	m.setTooltip("Downex — 2 active, 1 B/s")
	expectCalls(t, "tooltip while hidden again", f)
	m.setVisible(true)
	expectCalls(t, "show with the latest tooltip", f, "add Downex — 2 active, 1 B/s")
}

func TestMachineRetriesUntilTheTaskbarExists(t *testing.T) {
	f := &fakeShell{addFails: 2}
	m := newMachine(f, "tip", nil)
	m.setVisible(true)
	expectCalls(t, "first add fails", f, "add tip", "retry on")
	m.notify(Balloon{Title: "first"})
	m.notify(Balloon{Title: "second"})
	m.setTooltip("tip2")
	expectCalls(t, "while retrying", f) // the latest notification waits
	m.retryTimer()
	expectCalls(t, "second add fails", f, "add tip2")
	m.retryTimer()
	expectCalls(t, "third add works", f, "add tip2", "balloon info second| (tip tip2)", "retry off")
	m.retryTimer()
	expectCalls(t, "late timer", f)

	// Hidden while retrying: the timer stops and the waiting notification is dropped.
	f2 := &fakeShell{addFails: 1}
	m2 := newMachine(f2, "tip", nil)
	m2.setVisible(true)
	m2.notify(Balloon{Title: "waiting"})
	m2.setVisible(false)
	expectCalls(t, "hidden while retrying", f2, "add tip", "retry on", "retry off")
	m2.setVisible(true)
	expectCalls(t, "shown again", f2, "add tip")
}

func TestMachineTaskbarCreated(t *testing.T) {
	f := &fakeShell{}
	m := newMachine(f, "tip", nil)
	m.taskbarCreated()
	expectCalls(t, "hidden: nothing to re-create", f)
	m.setVisible(true)
	f.take()
	// Explorer restarted: the old icon is gone; deleting it fails harmlessly.
	f.exists = false
	m.taskbarCreated()
	expectCalls(t, "re-created", f, "remove", "add tip")
	// The broadcast after a display change: the icon still exists.
	m.taskbarCreated()
	expectCalls(t, "re-created again", f, "remove", "add tip")
	if !m.added || !f.exists {
		t.Fatal("the icon is not there after TaskbarCreated")
	}
	// Explorer is not ready yet: retried.
	f.addFails = 1
	m.taskbarCreated()
	expectCalls(t, "re-create fails", f, "remove", "add tip", "retry on")
	m.retryTimer()
	expectCalls(t, "re-created by the retry", f, "add tip", "retry off")
}

func TestMachineIconEvents(t *testing.T) {
	f := &fakeShell{}
	m := newMachine(f, "tip", nil)
	if cmds := m.iconEvent(evLButtonDblClk, 0, 0); cmds != nil {
		t.Fatalf("events without an icon: %v", cmds)
	}
	m.setVisible(true)
	f.take()
	for _, c := range []struct {
		event uint16
		want  []Command
	}{
		{evLButtonDblClk, []Command{CommandOpen}},
		{evKeySelect, []Command{CommandOpen}},
		{evBalloonUserClick, []Command{CommandOpen}},
		{evSelect, nil},
		{evBalloonTimeout, nil},
		{0x0200, nil}, // WM_MOUSEMOVE
	} {
		if got := m.iconEvent(c.event, 1, 2); !slices.Equal(got, c.want) {
			t.Errorf("event %#x: commands %v, want %v", c.event, got, c.want)
		}
	}
	expectCalls(t, "no menu for clicks", f)

	f.menuCmd, f.menuOK = CommandPauseAll, true
	if got := m.iconEvent(evContextMenu, -20, 900); !slices.Equal(got, []Command{CommandPauseAll}) {
		t.Errorf("menu choice: %v", got)
	}
	expectCalls(t, "menu", f, "menu -20,900")
	f.menuOK = false
	if got := m.iconEvent(evContextMenu, 5, 6); got != nil {
		t.Errorf("dismissed menu: %v", got)
	}
	expectCalls(t, "dismissed menu", f, "menu 5,6")
}

func TestMachineClose(t *testing.T) {
	f := &fakeShell{}
	m := newMachine(f, "tip", nil)
	m.setVisible(true)
	f.take()
	m.close()
	expectCalls(t, "close", f, "remove")
	m.setVisible(true)
	m.setTooltip("x")
	m.notify(Balloon{Title: "x"})
	m.taskbarCreated()
	m.retryTimer()
	if cmds := m.iconEvent(evLButtonDblClk, 0, 0); cmds != nil {
		t.Errorf("commands after close: %v", cmds)
	}
	m.close()
	expectCalls(t, "after close", f)

	// Closed while retrying: the timer stops.
	f2 := &fakeShell{addFails: 1}
	m2 := newMachine(f2, "tip", nil)
	m2.setVisible(true)
	m2.close()
	expectCalls(t, "close while retrying", f2, "add tip", "retry on", "retry off")
}

func TestMenuMatchesSpec(t *testing.T) {
	var labels []string
	for _, it := range menu {
		if it.cmd == 0 {
			labels = append(labels, "-")
			continue
		}
		labels = append(labels, it.label)
	}
	want := []string{"Open Downex", "Pause all", "Resume all", "-", "Settings…", "Quit Downex"}
	if !slices.Equal(labels, want) {
		t.Errorf("menu %q, want %q", labels, want)
	}
	if menu[0].cmd != CommandOpen {
		t.Error("the default item is not Open")
	}
	for _, c := range []Command{CommandOpen, CommandPauseAll, CommandResumeAll, CommandSettings, CommandQuit, 0} {
		if c.String() == "" {
			t.Errorf("command %d has no name", c)
		}
	}
}

func TestFillUTF16(t *testing.T) {
	decode := func(b []uint16) string {
		n := slices.Index(b, 0)
		if n < 0 {
			t.Fatalf("no terminating NUL in %v", b)
		}
		for _, v := range b[n:] {
			if v != 0 {
				t.Fatalf("garbage after the NUL: %v", b)
			}
		}
		return string(utf16.Decode(b[:n]))
	}
	buf := make([]uint16, 8)
	for i := range buf {
		buf[i] = 0xAAAA
	}
	fillUTF16(buf, "abc")
	if got := decode(buf); got != "abc" {
		t.Errorf("short: %q", got)
	}
	fillUTF16(buf, "abcdefghij")
	if got := decode(buf); got != "abcdefg" {
		t.Errorf("truncated: %q", got)
	}
	// A surrogate pair that does not fit is dropped whole.
	fillUTF16(buf, "abcdef🎉x")
	if got := decode(buf); got != "abcdef" {
		t.Errorf("pair at the end: %q", got)
	}
	fillUTF16(buf, "ab🎉cd")
	if got := decode(buf); got != "ab🎉cd" {
		t.Errorf("pair inside: %q", got)
	}
	fillUTF16(buf, "a\x00b")
	if got := decode(buf); got != "a" {
		t.Errorf("embedded NUL: %q", got)
	}
	fillUTF16(nil, "x") // no panic
	tip := make([]uint16, tipLen)
	fillUTF16(tip, strings.Repeat("é", 500))
	if got := decode(tip); len([]rune(got)) != tipLen-1 {
		t.Errorf("tip holds %d characters", len([]rune(got)))
	}
}

func TestIconResource(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32} {
		b := iconResource(size)
		maskStride := ((size + 31) / 32) * 4
		if want := 40 + size*size*4 + maskStride*size; len(b) != want {
			t.Errorf("%d px: %d bytes, want %d", size, len(b), want)
		}
		if b[0] != 40 || int(b[4]) != size || int(b[8]) != 2*size || b[14] != 32 {
			t.Errorf("%d px: bad header % x", size, b[:16])
		}
		// Bottom-up BGRA rows: the first pixel is the bottom-left corner, transparent.
		if a := b[40+3]; a != 0 {
			t.Errorf("%d px: corner alpha %d, want transparent", size, a)
		}
		white := 0
		for i := 40; i < 40+size*size*4; i += 4 {
			if b[i] == 255 && b[i+1] == 255 && b[i+2] == 255 && b[i+3] == 255 {
				white++
			}
		}
		if white < size*size/8 {
			t.Errorf("%d px: only %d opaque white pixels", size, white)
		}
	}
}
