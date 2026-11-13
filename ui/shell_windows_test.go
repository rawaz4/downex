//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"connectrpc.com/connect"
	"golang.org/x/sys/windows"

	downexv1 "downex/internal/rpc/downex/v1"
)

func TestPathExtIsAProgram(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE; .Foo ;")
	if riskOf(`C:\d\tool.foo`, nil) != riskProgram || riskOf(`C:\d\TOOL.FOO`, nil) != riskProgram {
		t.Error("an extension listed in PATHEXT is not a program")
	}
	if r := riskOf(`C:\d\notes.bar`, nil); r != riskNone {
		t.Errorf(".bar is risky (%v)", r)
	}
}

// Types that Windows itself treats as dangerous to open (AssocIsDangerous) are risky
// even when Downex's own lists do not name them.
func TestSystemDangerousTypesAreRisky(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE")
	for _, name := range []string{`C:\d\a.vsw`, `C:\d\a.mcf`, `C:\d\a.CNT`} {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
		if programExtensions[ext] || contentExtensions[ext] {
			t.Fatalf("%s is in Downex's lists; the test needs a type that only Windows knows", ext)
		}
		if r := riskOf(name, nil); r != riskContent {
			t.Errorf("riskOf(%s), which Windows considers dangerous, = %v", name, r)
		}
	}
	if riskOf(`C:\d\notes.txt`, nil) != riskNone || riskOf(`C:\d\clip.mp4`, nil) != riskNone {
		t.Error("a document is risky")
	}
}

func TestWindowsNamesRefused(t *testing.T) {
	for _, name := range []string{"a.txt:evil.exe", `a\b.txt`, "a/b.txt", "a?.txt", "a*.txt", `a".txt`, "a|b", "a<b", "a>b", "C:a.txt"} {
		if _, err := downloadPath(&downexv1.Download{Id: 1, Dir: `C:\d`, Filename: name}); err == nil {
			t.Errorf("file name %q accepted", name)
		}
	}
	if p, err := downloadPath(&downexv1.Download{Id: 1, Dir: `C:\d\`, Filename: "ok (1).zip"}); err != nil || p != `C:\d\ok (1).zip` {
		t.Errorf("downloadPath = %q, %v", p, err)
	}
}

func TestExplorerCommandLine(t *testing.T) {
	got := explorerCmdLine(`C:\Windows\explorer.exe`, `C:\My Files\a, b (1).zip`)
	if want := `"C:\Windows\explorer.exe" /select,"C:\My Files\a, b (1).zip"`; got != want {
		t.Errorf("command line %s, want %s", got, want)
	}
	if err := explorerSelect(`C:\a"b`); err == nil {
		t.Error("a path with a quote was passed to explorer.exe")
	}
	if err := explorerSelect(`relative\a.txt`); err == nil {
		t.Error("a relative path was passed to explorer.exe")
	}
}

// OPENASINFO is three fields: two string pointers, then the flags.
func TestOpenAsInfoLayout(t *testing.T) {
	var info openAsInfo
	ptr := unsafe.Sizeof(uintptr(0))
	if unsafe.Offsetof(info.class) != ptr || unsafe.Offsetof(info.flags) != 2*ptr {
		t.Errorf("OPENASINFO offsets: class %d, flags %d", unsafe.Offsetof(info.class), unsafe.Offsetof(info.flags))
	}
	// The chosen app opens the file; the association changes only when the user ticks
	// "Always use this app" (no OAIF_REGISTER_EXT 0x2, no OAIF_FORCE_REGISTRATION 0x8).
	if openWithFlags != 0x5 {
		t.Errorf("flags %#x, want OAIF_EXEC|OAIF_ALLOW_REGISTRATION", openWithFlags)
	}
}

func TestOpenWithResult(t *testing.T) {
	if err := openWithResult(0); err != nil {
		t.Errorf("S_OK: %v", err)
	}
	if err := openWithResult(1); err != nil { // S_FALSE is a success code
		t.Errorf("S_FALSE: %v", err)
	}
	if err := openWithResult(hresultCancelled); connect.CodeOf(err) != connect.CodeCanceled {
		t.Errorf("ERROR_CANCELLED: %v, want canceled", err)
	}
	if err := openWithResult(0x80004005); err == nil || connect.CodeOf(err) == connect.CodeCanceled || !strings.Contains(err.Error(), "0x80004005") {
		t.Errorf("E_FAIL: %v", err)
	}
}

// The owner of a dialog is a window of this process, or none.
func TestOwnerWindowIsOurs(t *testing.T) {
	hwnd := ownerWindow()
	if hwnd == 0 {
		return
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid != windows.GetCurrentProcessId() {
		t.Errorf("owner window %#x belongs to process %d (%v)", hwnd, pid, err)
	}
}

func TestWithCOM(t *testing.T) {
	sentinel := errors.New("ran")
	// Twice on possibly the same thread: S_FALSE (already initialised) is fine too.
	for range 2 {
		if err := withCOM(func() error { return sentinel }); !errors.Is(err, sentinel) {
			t.Fatalf("withCOM = %v", err)
		}
	}
}
