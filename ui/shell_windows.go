//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"connectrpc.com/connect"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// forbiddenNameChars cannot appear in a Windows file name; ':' would also name an
// alternate data stream.
const forbiddenNameChars = `<>:"/\|?*`

// osRisk adds the extensions that the command processor runs by name (PATHEXT), and
// the types that Windows considers dangerous to open (AssocIsDangerous: its built-in
// list and the types registered as always unsafe, which covers types that Downex's
// lists do not know).
func osRisk(ext string, _ fs.FileInfo) risk {
	if ext == "" {
		return riskNone
	}
	for _, e := range strings.Split(os.Getenv("PATHEXT"), ";") {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(e), "."), ext) {
			return riskProgram
		}
	}
	if assocIsDangerous("." + ext) {
		return riskContent
	}
	return riskNone
}

// assocIsDangerous calls AssocIsDangerous; when Windows cannot answer, the type is
// treated as dangerous.
func assocIsDangerous(ext string) bool {
	p, err := windows.UTF16PtrFromString(ext)
	if err != nil || procAssocIsDangerous.Find() != nil {
		return true
	}
	r, _, _ := procAssocIsDangerous.Call(uintptr(unsafe.Pointer(p)))
	return r != 0 // BOOL
}

var (
	shlwapi              = windows.NewLazySystemDLL("shlwapi.dll")
	procAssocIsDangerous = shlwapi.NewProc("AssocIsDangerous")

	shell32                        = windows.NewLazySystemDLL("shell32.dll")
	procILCreateFromPathW          = shell32.NewProc("ILCreateFromPathW")
	procILFree                     = shell32.NewProc("ILFree")
	procSHOpenFolderAndSelectItems = shell32.NewProc("SHOpenFolderAndSelectItems")
	procSHOpenWithDialog           = shell32.NewProc("SHOpenWithDialog")

	user32                       = windows.NewLazySystemDLL("user32.dll")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
)

// rpcEChangedMode is RPC_E_CHANGED_MODE: COM is already initialised on the thread
// with another concurrency model, which the shell calls below also work with.
const rpcEChangedMode = 0x80010106

// windowsShell opens files with ShellExecuteW and selects them in Explorer with
// SHOpenFolderAndSelectItems. No command line is ever built for cmd.exe.
type windowsShell struct{}

func newShell() Shell { return windowsShell{} }

// withCOM runs fn on one OS thread with COM initialised in a single-threaded
// apartment without OLE1 DDE, as the shell functions require.
func withCOM(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	var errno syscall.Errno
	switch {
	case err == nil:
		defer windows.CoUninitialize()
	case errors.As(err, &errno) && errno == 1: // S_FALSE: already initialised on this thread
		defer windows.CoUninitialize()
	case errors.As(err, &errno) && uint32(errno) == rpcEChangedMode:
	default:
		return fmt.Errorf("initialise COM: %w", err)
	}
	return fn()
}

func shellExecute(verb, file, dir string) error {
	var v *uint16
	if verb != "" {
		var err error
		if v, err = windows.UTF16PtrFromString(verb); err != nil {
			return err
		}
	}
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var d *uint16
	if dir != "" {
		if d, err = windows.UTF16PtrFromString(dir); err != nil {
			return err
		}
	}
	return windows.ShellExecute(0, v, f, nil, d, windows.SW_SHOWNORMAL)
}

// Open opens path with the "open" verb of its file type, else with the type's default
// verb. The user declining an elevation prompt is not an error.
func (windowsShell) Open(path string) error {
	return withCOM(func() error {
		dir := filepath.Dir(path)
		err := shellExecute("open", path, dir)
		if errors.Is(err, windows.ERROR_NO_ASSOCIATION) {
			err = shellExecute("", path, dir)
		}
		switch {
		case err == nil, errors.Is(err, windows.ERROR_CANCELLED):
			return nil
		case errors.Is(err, windows.ERROR_NO_ASSOCIATION):
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("no app is set to open %s files on this computer", strings.ToLower(filepath.Ext(path))))
		}
		return err
	})
}

// Reveal opens an Explorer window on path's folder with path selected (or selects it
// in such a window that is already open). If the shell cannot do that, it starts
// explorer.exe /select,"<path>".
func (windowsShell) Reveal(path string) error {
	err := withCOM(func() error { return selectItem(path) })
	if err == nil {
		return nil
	}
	if ferr := explorerSelect(path); ferr != nil {
		return fmt.Errorf("%w; explorer.exe: %w", err, ferr)
	}
	return nil
}

// ShowFolder opens an Explorer window on dir.
func (windowsShell) ShowFolder(dir string) error {
	return withCOM(func() error { return shellExecute("open", dir, "") })
}

// openAsInfo is OPENASINFO (shlobj_core.h), the argument of SHOpenWithDialog.
type openAsInfo struct {
	file  *uint16 // pcszFile
	class *uint16 // pcszClass: nil = the type of file's own extension
	flags uint32  // oaifInFlags (OPEN_AS_INFO_FLAGS)
}

const (
	// oaifAllowRegistration (OAIF_ALLOW_REGISTRATION) offers "Always use this app"; the
	// association changes only when the user ticks it.
	oaifAllowRegistration = 0x1
	// oaifExec (OAIF_EXEC) opens the file with the chosen application.
	oaifExec = 0x4
	// openWithFlags are the flags Downex passes: never OAIF_REGISTER_EXT or
	// OAIF_FORCE_REGISTRATION, which would change the association by themselves.
	openWithFlags = oaifExec | oaifAllowRegistration
	// hresultCancelled is HRESULT_FROM_WIN32(ERROR_CANCELLED): the dialog was closed
	// without a choice.
	hresultCancelled = 0x800704C7
)

// canOpenWith: Windows has the "Open with" dialog (OpenWith); Startup tells the page.
const canOpenWith = true

// OpenWith shows the system's "Open with" dialog (SHOpenWithDialog) for path, and the
// application the user picks opens it (OAIF_EXEC). No command line is ever built. The
// dialog is modal to this window when it is the foreground window; the call returns when
// the dialog closes.
func (windowsShell) OpenWith(path string) error {
	if err := procSHOpenWithDialog.Find(); err != nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("this version of Windows has no Open with dialog: %w", err))
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return withCOM(func() error {
		info := &openAsInfo{file: p, flags: openWithFlags}
		hr, _, _ := procSHOpenWithDialog.Call(uintptr(ownerWindow()), uintptr(unsafe.Pointer(info)))
		runtime.KeepAlive(info)
		runtime.KeepAlive(p)
		return openWithResult(uint32(hr))
	})
}

// openWithResult maps the HRESULT of SHOpenWithDialog.
func openWithResult(hr uint32) error {
	switch {
	case hr == hresultCancelled:
		return connect.NewError(connect.CodeCanceled, errors.New("no app was chosen"))
	case int32(hr) < 0:
		return fmt.Errorf("SHOpenWithDialog: HRESULT %#08x", hr)
	}
	return nil
}

// ownerWindow returns the foreground window when this process owns it (the window whose
// button was just clicked), else 0: a dialog is never attached to another program's window.
func ownerWindow() windows.HWND {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid != windows.GetCurrentProcessId() {
		return 0
	}
	return hwnd
}

func selectItem(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	pidl, _, callErr := procILCreateFromPathW.Call(uintptr(unsafe.Pointer(p)))
	if pidl == 0 {
		return fmt.Errorf("ILCreateFromPathW: %w", callErr)
	}
	defer func() { _, _, _ = procILFree.Call(pidl) }()
	// With no child items, the first argument names the single item to select.
	hr, _, _ := procSHOpenFolderAndSelectItems.Call(pidl, 0, 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("SHOpenFolderAndSelectItems: HRESULT %#08x", uint32(hr))
	}
	return nil
}

// explorerSelect starts explorer.exe /select,"<path>". The command line is written
// out because explorer.exe parses its own: Go's quoting would wrap the whole
// "/select,<path>" argument in quotes. path is absolute and a Windows path cannot
// contain '"', so quoting it is enough.
func explorerSelect(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\"\x00") {
		return fmt.Errorf("unusable path %q", path)
	}
	winDir, err := windows.GetWindowsDirectory()
	if err != nil {
		return err
	}
	exe := filepath.Join(winDir, "explorer.exe")
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: explorerCmdLine(exe, path)}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func explorerCmdLine(exe, path string) string {
	return `"` + exe + `" /select,"` + path + `"`
}

// allowForegroundHandoff lets another process bring its window to the front. A second
// start of Downex calls it before it hands over to the running instance and exits:
// it is the foreground process (the user just started it), so the running instance
// may then raise its window instead of only flashing its taskbar button.
func allowForegroundHandoff() {
	const asfwAny = 0xFFFFFFFF // ASFW_ANY
	_, _, _ = procAllowSetForegroundWindow.Call(uintptr(asfwAny))
}

// prefersDark reports whether apps use the dark theme (Settings > Personalisation >
// Colours), for the window's background before the page has loaded.
func prefersDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// webviewDataDir is where WebView2 keeps its profile: the local (not roaming)
// application data, as caches do not belong in a roaming profile.
func webviewDataDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Downex", "WebView2")
}
