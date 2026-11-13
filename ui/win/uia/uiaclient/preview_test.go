//go:build windows

package uiaclient

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The ids this test reads (UIAutomationClient.h; package uia's consts_test checks them).
const (
	controlTypeProperty  = 30003
	nameProperty         = 30005
	automationIDProperty = 30011
	gridPattern          = 10006
	windowControl        = 50032
	titleBarControl      = 50037
	buttonControl        = 50000
	editControl          = 50004
	dataGridControl      = 50028
	dataItemControl      = 50029
)

// TestPreviewOutOfProcess reads the native preview's window from another process, as
// Narrator and Accessibility Insights do: DOWNEX_UIA_PREVIEW names a preview executable
// built with an instance name of its own (go build -ldflags "-X
// main.instanceName=Local\..." ./ui/native), which the test starts without activation
// and closes with WM_CLOSE.
func TestPreviewOutOfProcess(t *testing.T) {
	exe := os.Getenv("DOWNEX_UIA_PREVIEW")
	if exe == "" {
		t.Skip("set DOWNEX_UIA_PREVIEW to a preview executable built with its own instance name")
	}
	report := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.Command(exe, "-noactivate", "-size", "1200x760", "-report", report, "-exit-after", "2m")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
	}()
	var rep struct {
		HWND uint64 `json:"hwnd"`
		PID  int    `json:"pid"`
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(report); err == nil && json.Unmarshal(b, &rep) == nil && rep.HWND != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the preview wrote no report")
		}
	}
	hwnd := uintptr(rep.HWND)
	t.Logf("preview pid %d, window %#x", rep.PID, hwnd)
	defer postClose(hwnd)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := Init(); err != nil {
		t.Fatal(err)
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	root, err := c.ElementFromHandle(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Release()
	if name, _ := root.Name(); name != "Downex" {
		t.Errorf("window name %q", name)
	}
	if ct, _ := root.ControlType(); ct != windowControl {
		t.Errorf("window control type %d", ct)
	}
	first := func(e *Element, scope int32, cond *Condition, err error) *Element {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer cond.Release()
		found, err := e.FindFirst(scope, cond)
		if err != nil || found == nil {
			t.Fatalf("FindFirst: %v", err)
		}
		return found
	}
	cond, err := c.StringCondition(automationIDProperty, "titlebar")
	bar := first(root, TreeScope_Descendants, cond, err)
	defer bar.Release()
	if ct, _ := bar.ControlType(); ct != titleBarControl {
		t.Errorf("title bar control type %d", ct)
	}
	for _, name := range []string{"Minimise", "Maximise", "Close", "Settings", "Commands"} {
		cond, err := c.StringCondition(nameProperty, name)
		b := first(bar, TreeScope_Descendants, cond, err)
		if ct, _ := b.ControlType(); ct != buttonControl {
			t.Errorf("%q control type %d", name, ct)
		}
		b.Release()
	}
	countOf := func(ct int32) int {
		cond, err := c.IntCondition(controlTypeProperty, ct)
		if err != nil {
			t.Fatal(err)
		}
		defer cond.Release()
		all, err := root.FindAll(TreeScope_Descendants, cond)
		if err != nil {
			t.Fatal(err)
		}
		defer all.Release()
		n, _ := all.Len()
		return n
	}
	if n := countOf(editControl); n != 1 {
		t.Errorf("%d edit fields, want the search alone", n)
	}
	if n := countOf(titleBarControl); n != 1 {
		t.Errorf("%d title bars, want the window's own alone", n)
	}
	cond, err = c.IntCondition(controlTypeProperty, dataGridControl)
	grid := first(root, TreeScope_Descendants, cond, err)
	defer grid.Release()
	if name, _ := grid.Name(); name != "Downloads" {
		t.Errorf("grid name %q", name)
	}
	gp, err := Pattern[GridPattern](grid, gridPattern, &IID_IUIAutomationGridPattern)
	if err != nil || gp == nil {
		t.Fatalf("Grid pattern: %v", err)
	}
	defer gp.Release()
	rows, _ := gp.RowCount()
	cols, _ := gp.ColumnCount()
	t.Logf("grid %d rows × %d columns", rows, cols)
	if rows != 10001 {
		t.Errorf("grid rows %d, want 10001 (10,000 downloads and the header)", rows)
	}
	cond, err = c.IntCondition(controlTypeProperty, dataItemControl)
	all, err := grid.FindAll(TreeScope_Children, cond)
	cond.Release()
	if err != nil {
		t.Fatal(err)
	}
	defer all.Release()
	n, _ := all.Len()
	var named []string
	for i := 0; i < n && len(named) < 3; i++ {
		row, err := all.GetElement(i)
		if err != nil {
			t.Fatal(err)
		}
		if name, _ := row.Name(); strings.Contains(name, ", ") {
			named = append(named, name)
		}
		row.Release()
	}
	t.Logf("%d rows laid out; the first named: %q", n, named)
	if len(named) == 0 {
		t.Error("no row is named by its download's name and status")
	}
}

func postClose(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	user32.NewProc("PostMessageW").Call(hwnd, 0x0010 /* WM_CLOSE */, 0, 0)
}
