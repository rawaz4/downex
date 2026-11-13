//go:build windows

package win

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/win/internal/com"
	"downex/ui/win/internal/sdkheader"
)

// Every slot and GUID of the pickers' and the drop target's bindings, against the SDK's
// ShObjIdl_core.h, oleidl.h and objidl.h.
func TestGlueSlotsMatchTheSDK(t *testing.T) {
	dir, ok := sdkheader.Dir()
	if !ok {
		t.Skipf(`Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE)`, dir)
	}
	s, err := sdkheader.Parse(dir, "objidl.h", "oleidl.h", "ShObjIdl_core.h")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		iface, method string
		slot          int
	}{
		{"IFileOpenDialog", "Show", slotFDShow},
		{"IFileOpenDialog", "SetFileTypes", slotFDSetFileTypes},
		{"IFileOpenDialog", "SetOptions", slotFDSetOptions},
		{"IFileOpenDialog", "GetOptions", slotFDGetOptions},
		{"IFileOpenDialog", "SetFolder", slotFDSetFolder},
		{"IFileOpenDialog", "SetTitle", slotFDSetTitle},
		{"IFileOpenDialog", "GetResult", slotFDGetResult},
		{"IFileOpenDialog", "SetClientGuid", slotFDSetClientGuid},
		{"IShellItem", "GetDisplayName", slotSIGetDisplayName},
		{"IDropTarget", "DragEnter", slotDTDragEnter},
		{"IDropTarget", "DragOver", slotDTDragOver},
		{"IDropTarget", "DragLeave", slotDTDragLeave},
		{"IDropTarget", "Drop", slotDTDrop},
		{"IDataObject", "GetData", slotDOGetData},
		{"IDataObject", "QueryGetData", slotDOQueryData},
	} {
		got, err := s.Slot(c.iface, c.method)
		if err != nil {
			t.Errorf("%s.%s: %v", c.iface, c.method, err)
			continue
		}
		if got != c.slot {
			t.Errorf("%s.%s is slot %d, the binding uses %d", c.iface, c.method, got, c.slot)
		}
	}
	if all, err := s.AllMethods("IDropTarget"); err != nil || len(all) != dropMethods {
		t.Errorf("IDropTarget has %d methods (%v), the object has %d", len(all), err, dropMethods)
	}
	// IDropTarget's methods pass (this, ...): DragEnter and Drop 5 words on x64 (POINTL in one).
	for _, c := range []struct {
		method string
		words  int
	}{{"DragEnter", 5}, {"DragOver", 4}, {"DragLeave", 1}, {"Drop", 5}} {
		if n, err := s.Arity("IDropTarget", c.method); err != nil || n != c.words {
			t.Errorf("IDropTarget.%s takes %d words (%v), the callback %d", c.method, n, err, c.words)
		}
	}
	for _, c := range []struct {
		iface string
		guid  com.GUID
	}{
		{"IFileOpenDialog", iidFileOpenDialog}, {"IShellItem", iidShellItem}, {"IDropTarget", iidIDropTarget},
	} {
		want, ok := s.IID(c.iface)
		if !ok || !strings.EqualFold(com.GUIDString(c.guid), want) {
			t.Errorf("%s: IID %s, the header says %q", c.iface, com.GUIDString(c.guid), want)
		}
	}
	src, err := os.ReadFile(filepath.Join(dir, "ShObjIdl_core.h"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"FOS_PICKFOLDERS\t= 0x20", "SIGDN_FILESYSPATH\t= ( int  )0x80058000"} {
		if !strings.Contains(string(src), v) {
			t.Errorf("ShObjIdl_core.h does not say %q", v)
		}
	}
}

// The folder picker's client GUID is today's: the v5 UUID of "PickFolder" in the nil
// namespace (5af87399-d6a3-5b65-af6e-2b1591fc5252, from Python's uuid.uuid5), so the dialog
// opens where today's last left it.
func TestFolderRoleGUIDIsTodays(t *testing.T) {
	if g := com.GUIDString(roleGUID("PickFolder")); !strings.EqualFold(g, "5af87399-d6a3-5b65-af6e-2b1591fc5252") {
		t.Fatalf("roleGUID(PickFolder) = %s", g)
	}
}

type pickAnswer struct {
	path string
	err  error
}

// drivePicker shows a picker from another goroutine, lets drive act on its dialog (on the
// test desktop), and returns the answer.
func drivePicker(t *testing.T, desk string, a *App, w *Window, title string, show func() (string, error), drive func(dlg uintptr)) (pickAnswer, uintptr) {
	t.Helper()
	res := make(chan pickAnswer, 1)
	go func() {
		p, err := show()
		res <- pickAnswer{p, err}
	}()
	var owner uintptr
	drv := onDesktop(t, desk, func() {
		dlg := waitWindow("#32770", title, 15*time.Second)
		if dlg == 0 {
			t.Errorf("no dialog titled %q", title)
			return
		}
		owner = ownerOf(dlg)
		drive(dlg)
		// A dialog the drive did not close is cancelled, so that a failure never hangs the
		// test in the dialog's modal loop.
		for i := 0; i < 100 && waitWindow("#32770", title, time.Millisecond) != 0; i++ {
			time.Sleep(50 * time.Millisecond)
		}
		if waitWindow("#32770", title, time.Millisecond) != 0 {
			t.Errorf("the dialog %q stayed open", title)
			postMsg(dlg, 0x0111, 2, 0)
		}
	})
	var got pickAnswer
	pump(t, a, w, "the picker's answer", 30*time.Second, func() bool {
		select {
		case got = <-res:
			return true
		default:
			return false
		}
	})
	<-drv
	return got, owner
}

// waitChild waits for the dialog's control id (the common item dialog makes them a moment
// after it shows).
func waitChild(dlg uintptr, id int) uintptr {
	for i := 0; i < 500; i++ {
		if h := dlgItem(dlg, id); h != 0 {
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

// The folder picker is the shell's folder dialog titled as asked, owned by the window,
// starting in the folder asked for: its Select Folder button picks that folder. Cancel
// answers "" without an error.
func TestPickFolder(t *testing.T) {
	desk := onTestDesktop(t)
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	d, err := NewDialogs(w)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	start := t.TempDir()
	const title = "Choose a folder" // host.FolderPickerTitle
	got, owner := drivePicker(t, desk, a, w, title, func() (string, error) {
		return d.PickFolder(context.Background(), title, start)
	}, func(dlg uintptr) {
		ok := waitChild(dlg, 1) // IDOK: Select Folder
		time.Sleep(300 * time.Millisecond)
		postMsg(dlg, 0x0111, 1, ok)
	})
	if got.err != nil || !strings.EqualFold(filepath.Clean(got.path), filepath.Clean(start)) {
		t.Fatalf("PickFolder = %q, %v; want the start folder %q", got.path, got.err, start)
	}
	if owner != uintptr(w.hwnd) {
		t.Errorf("owner %#x, want %#x", owner, w.hwnd)
	}
	got, _ = drivePicker(t, desk, a, w, title, func() (string, error) {
		return d.PickFolder(context.Background(), title, start)
	}, func(dlg uintptr) {
		waitChild(dlg, 1)
		time.Sleep(300 * time.Millisecond)
		postMsg(dlg, 0x0111, 2 /* IDCANCEL */, 0)
	})
	if got.err != nil || got.path != "" {
		t.Fatalf("a cancelled picker answered %q, %v", got.path, got.err)
	}
	// A start folder that does not exist is an error, as today's (the host gives ExistingDir).
	if _, err := d.PickFolder(context.Background(), title, filepath.Join(start, "missing")); err == nil {
		t.Fatal("a missing start folder was accepted")
	}
}

// The file picker picks one existing file, starting in the folder asked for, with today's
// "All Files (*.*)" filter.
func TestPickFile(t *testing.T) {
	desk := onTestDesktop(t)
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	d, err := NewDialogs(w)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	start := t.TempDir()
	file := filepath.Join(start, "scanner.exe")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	const title = "Choose the scanner"
	var filter string
	got, _ := drivePicker(t, desk, a, w, title, func() (string, error) {
		return d.PickFile(context.Background(), title, start)
	}, func(dlg uintptr) {
		combo := waitChild(dlg, 1148) // cmb13: the file name box
		if combo == 0 {
			t.Error("no file name box")
			postMsg(dlg, 0x0111, 2, 0)
			return
		}
		if types := dlgItem(dlg, 1136); types != 0 { // cmb1: the file types
			filter = windowText(types)
		}
		edit := sendMsg(combo, 0x0407 /* CBEM_GETEDITCONTROL */, 0, 0)
		if edit == 0 {
			edit = findChild(combo, 0, "Edit")
		}
		time.Sleep(time.Second) // the dialog fills its folder view first
		// The name in the start folder: the dialog started where it was asked to.
		name, _ := windows.UTF16PtrFromString(filepath.Base(file))
		sendMsg(edit, 0x000C /* WM_SETTEXT */, 0, uintptr(unsafe.Pointer(name)))
		time.Sleep(300 * time.Millisecond)
		postMsg(dlg, 0x0111, 1, dlgItem(dlg, 1))
	})
	if got.err != nil || !strings.EqualFold(got.path, file) {
		t.Fatalf("PickFile = %q, %v; want %q", got.path, got.err, file)
	}
	if filter != "" && filter != "All Files (*.*)" {
		t.Errorf("filter %q, want All Files (*.*)", filter)
	}
	t.Logf("file types box: %q", filter)
}

// A call waiting for its turn ends with its context; the pickers are not for a closed window.
func TestPickerContextAndClose(t *testing.T) {
	_ = onTestDesktop(t)
	_, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	d, err := NewDialogs(w)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res := make(chan error, 1)
	go func() { _, err := d.PickFolder(ctx, "x", ""); res <- err }() // the loop is not run: it waits
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting picker ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the context did not end the wait")
	}
	d.Close()
}

// OpenExternal opens https addresses only, as today's openExternal: anything else is
// rejected before the shell sees it.
func TestOpenExternalOnlyHTTPS(t *testing.T) {
	for _, u := range []string{"http://example.com/", "https://", "https:///x", "file:///C:/Windows/notepad.exe",
		"javascript:alert(1)", "C:\\Windows\\notepad.exe", "", " https://example.com"} {
		if err := OpenExternal(context.Background(), u); !errors.Is(err, ErrNotHTTPS) {
			t.Errorf("OpenExternal(%q) = %v, want ErrNotHTTPS", u, err)
		}
	}
	if !externalURL.MatchString("HTTPS://ffmpeg.org/legal.html") || !externalURL.MatchString("https://x") {
		t.Error("an https address is refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := OpenExternal(ctx, "https://example.com/"); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled call: %v", err)
	}
}
