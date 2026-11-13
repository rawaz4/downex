package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode"

	"connectrpc.com/connect"

	"downex/internal/platform"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Shell is the desktop's file handling (NewShell: shell_<os>.go; a fake in tests). Its
// calls block until the system has taken the request (OpenWith: until its dialog
// closes), so the window makes them off its UI thread.
type Shell interface {
	// Open opens the file at path with its default application.
	Open(path string) error
	// Reveal shows the file manager with the file at path selected.
	Reveal(path string) error
	// ShowFolder shows the folder dir in the file manager.
	ShowFolder(dir string) error
	// OpenWith lets the user choose the application that opens the file at path (the
	// system's "Open with" dialog) and opens it with that one. It returns once the choice
	// is made; closing the dialog without one is a connect error with CodeCanceled. A
	// system without such a dialog reports CodeUnimplemented.
	OpenWith(path string) error
}

// PartSuffix is the suffix of an unfinished download's file (engine.PartPath).
const PartSuffix = ".dxpart"

// Risk is what opening a file could do.
type Risk int

const (
	RiskNone Risk = iota
	// RiskContent: the file's application runs code that the file contains, mounts it,
	// or connects to a computer that the file names.
	RiskContent
	// RiskProgram: opening the file runs or installs it.
	RiskProgram
)

// Downloads carry no Mark-of-the-Web yet (U6), so neither the system (SmartScreen) nor
// Office (its block of macros and add-ins from the internet) warns before such files
// run: the confirmation in Files.OpenFile is the only gate. The lists hold extensions in
// lower case, without the dot; the platforms add their own (shell_<os>.go: PATHEXT and
// the types Windows considers dangerous to open, the execute bit on Linux; macos.go: the
// bundles, scripts, installers and locations that macOS opens, and the execute bit).
var (
	// programExtensions are programs, installers, scripts and shortcuts to them.
	programExtensions = extensionSet(
		// Windows programs, installers and packages
		"exe com scr pif cpl msc msi msp mst msu msix msixbundle appx appxbundle appinstaller",
		"application appref-ms xbap vsto gadget jar jnlp",
		// Windows scripts, and shell configuration that runs them
		"bat cmd ps1 psm1 psd1 ps1xml psc1 ps2 ps2xml psc2 msh msh1 msh2 mshxml msh1xml msh2xml",
		"vb vbs vbe js jse wsf wsh wsc ws hta sct chm hlp inf reg scf shb shs",
		// troubleshooting packages, which run their scripts; Excel add-ins (DLLs)
		"diagcab diagcfg diagpkg xll",
		// shortcuts and shell locations
		"lnk url website settingcontent-ms library-ms search-ms",
		// Unix and cross-platform programs, packages and scripts
		"sh bash zsh csh ksh run command desktop appimage flatpak flatpakref snap",
		"deb rpm apk pkg dmg app py pyw pyc pyo pyz pyzw pl rb",
	)
	// contentExtensions are files whose application runs code they contain, mounts
	// them, or connects to a computer they name.
	contentExtensions = extensionSet(
		// Office documents and add-ins with macros, and Access databases
		"docm dotm xlsm xltm xlsb xlam xla xlm pptm potm ppsm ppam sldm",
		"accda accdb accde accdr ade adp mda mdb mde",
		// Excel web queries and symbolic links (formulas that run commands)
		"iqy slk",
		// disk images, whose programs Windows mounts and shows
		"iso img vhd vhdx",
		// remote desktop connections, themes (they load files from other computers),
		// search connectors, sandbox configurations, internet connection settings
		"rdp theme themepack deskthemepack searchconnector-ms wsb ins isp",
	)
)

// extensionSet makes a set of the space-separated extensions in lists.
func extensionSet(lists ...string) map[string]bool {
	set := make(map[string]bool)
	for _, l := range lists {
		for _, ext := range strings.Fields(l) {
			set[ext] = true
		}
	}
	return set
}

// RiskOf reports what opening the file named name (with info, when known) could do on
// this system.
func RiskOf(name string, info fs.FileInfo) Risk {
	return Classify(name, info, osRisk)
}

// Classify is RiskOf with a platform's own rules given as platform, so that the rules of
// every platform can be tested anywhere.
func Classify(name string, info fs.FileInfo, platform func(ext string, info fs.FileInfo) Risk) Risk {
	// Windows ignores trailing dots and spaces: "setup.exe. " opens as "setup.exe".
	base := strings.TrimRight(filepath.Base(name), ". ")
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(base), "."))
	r := RiskNone
	switch {
	case ext == "":
	case programExtensions[ext]:
		return RiskProgram
	case contentExtensions[ext]:
		r = RiskContent
	}
	return max(r, platform(ext, info))
}

// InvalidName reports a file name that does not name a file directly inside a folder.
func InvalidName(name string) bool {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return true
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(forbiddenNameChars, r) {
			return true
		}
	}
	return false
}

// DownloadPath returns dir/filename of d after checking that the daemon's record names a
// file inside an absolute folder.
func DownloadPath(d *downexv1.Download) (string, error) {
	dir, name := d.GetDir(), d.GetFilename()
	if name == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d has no file name yet", d.GetId()))
	}
	if !filepath.IsAbs(dir) || InvalidName(name) {
		return "", connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("download %d does not name a usable file (folder %q, file %q)", d.GetId(), dir, name))
	}
	return filepath.Join(filepath.Clean(dir), name), nil
}

// statError maps a failed os.Stat/Lstat of path.
func statError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s does not exist; it may have been moved, renamed or deleted", path))
	case errors.Is(err, fs.ErrPermission):
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s cannot be accessed: %w", path, err))
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot check %s: %w", path, err))
}

// ConfirmText is the question asked before the file at path (a link to target, when they
// differ), whose risk is r, is opened.
func ConfirmText(r Risk, path, target string) (title, text string) {
	name := DisplayName(filepath.Base(path))
	if target != path {
		name += fmt.Sprintf(" (a link to %s)", DisplayName(target))
	}
	folder := DisplayName(filepath.Dir(path))
	if r == RiskProgram {
		return "Run this program?", fmt.Sprintf("%s is a program or script. Opening it runs it on this computer, with your rights.\n\n"+
			"Programs from the internet can harm your computer. Open it only if you trust where it came from.\n\n"+
			"Folder: %s", name, folder)
	}
	return "Open this file?", fmt.Sprintf("%s is a type of file that can run code or connect to other computers when it is opened, "+
		"with your rights (such as a document with macros, a disk image or a remote desktop connection).\n\n"+
		"Files from the internet can harm your computer. Open it only if you trust where it came from.\n\n"+
		"Folder: %s", name, folder)
}

// shellError maps a failure of the desktop shell.
func shellError(verb, path string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot %s %s: %w", verb, path, err))
}

// StateName is a download state for messages ("paused").
func StateName(s downexv1.DownloadState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "DOWNLOAD_STATE_"))
}

// DisplayName makes s safe to show in a native dialog: bidirectional formatting and other
// invisible or control characters, which can make "exe" read as another type, are shown
// as ⟨U+XXXX⟩.
func DisplayName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) || r == ' ' || r == ' ' {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ExistingDir returns dir when it is an existing absolute folder, else its nearest
// existing parent, else "": where a folder or file picker starts.
func ExistingDir(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	for d := filepath.Clean(dir); ; {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// The pickers' titles (today's PickFolder and PickFile bindings).
const (
	FolderPickerTitle = "Choose a folder"
	FilePickerTitle   = "Choose a file"
	// maxPickerTitle bounds a caller's file picker title.
	maxPickerTitle = 200
)

// FilePickerTitleOf is the file picker's title for a caller's title: FilePickerTitle
// when it is empty or longer than 200 bytes.
func FilePickerTitleOf(title string) string {
	if title == "" || len(title) > maxPickerTitle {
		return FilePickerTitle
	}
	return title
}

// ---- the files of downloads -----------------------------------------------------------

// Files opens, shows and checks the files of downloads for the window. Its methods block
// (on the daemon, on the system's shell, and on the confirmation dialog), so the window
// calls them off its UI thread; ctx bounds the daemon call (CallTimeout). Its fields are
// set before first use; SetConfirm may be called at any time.
type Files struct {
	Daemon Daemon
	Shell  Shell
	// Log may be nil.
	Log *slog.Logger
	// FreeSpaceOf reads the bytes free on the volume of an absolute folder
	// (platform.FreeSpace when nil).
	FreeSpaceOf func(dir string) (int64, error)
	// Executable is the running program, whose installation holds the extension and the
	// relay (os.Executable when nil; tests: a program in a temporary installation).
	Executable func() (string, error)
	// RunRelay runs the relay for RegisterBrowsers (the program itself, no shell, when nil;
	// tests: a fake, so that no test registers anything with the user's browsers).
	RunRelay RunRelayFunc

	confirm atomic.Pointer[ConfirmFunc]
}

// ConfirmFunc asks a yes/no question (title, message) in a native dialog whose default
// button is No, and reports a yes. It blocks until the user answers.
type ConfirmFunc func(title, message string) (bool, error)

// SetConfirm sets the window's yes/no dialog; nil while there is no window, and then a
// file that needs the question is not opened (unavailable).
func (f *Files) SetConfirm(fn ConfirmFunc) {
	if fn == nil {
		f.confirm.Store(nil)
		return
	}
	f.confirm.Store(&fn)
}

func (f *Files) logger() *slog.Logger {
	if f.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return f.Log
}

// OpenFile opens download id with the default application, if it is completed and its
// file exists. A program, or a file that can run code or connect out (RiskOf), is opened
// only after the user confirms it in a native dialog; when they decline, nothing happens
// and no error is reported.
func (f *Files) OpenFile(ctx context.Context, id int64) error {
	return f.openCompleted(ctx, id, f.Shell.Open)
}

// OpenWith opens download id with an application that the user chooses (Shell.OpenWith,
// docs/DESKTOP-SPEC.md §8), after the checks and the confirmation of OpenFile: the chosen
// application may run the file as well. Closing the dialog without a choice is canceled;
// a system without such a dialog unimplemented.
func (f *Files) OpenWith(ctx context.Context, id int64) error {
	return f.openCompleted(ctx, id, f.Shell.OpenWith)
}

// openCompleted hands the file of download id to open once it is a completed download
// whose file is a regular file with a usable name, and once the user has confirmed a
// program or a file that can run code (see OpenFile).
func (f *Files) openCompleted(ctx context.Context, id int64, open func(path string) error) error {
	d, err := f.Daemon.Get(ctx, id)
	if err != nil {
		return err
	}
	if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("download %d is not completed (%s)", id, StateName(d.GetState())))
	}
	path, err := DownloadPath(d)
	if err != nil {
		return err
	}
	link, err := os.Lstat(path)
	if err != nil {
		return statError(path, err)
	}
	info, target := link, path
	if link.Mode()&fs.ModeSymlink != 0 {
		// Judge what opening it would open: the link's target.
		if target, err = filepath.EvalSymlinks(path); err != nil {
			return statError(path, err)
		}
		if info, err = os.Stat(target); err != nil {
			return statError(path, err)
		}
	}
	if info.IsDir() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is a folder, not a downloaded file", path))
	}
	if !info.Mode().IsRegular() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is not a regular file", path))
	}
	// The name of the link and of its target decide; the target's mode (execute bit).
	if r := max(RiskOf(path, info), RiskOf(target, info)); r != RiskNone {
		confirm := f.confirm.Load()
		if confirm == nil {
			return ErrNoWindow()
		}
		title, text := ConfirmText(r, path, target)
		ok, err := (*confirm)(title, text)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("confirmation dialog: %w", err))
		}
		if !ok {
			f.logger().Info("opening a program was declined", "download", id)
			return nil
		}
	}
	if err := open(path); err != nil {
		return shellError("open", path, err)
	}
	return nil
}

// OpenFolder shows download id in the file manager: its file (or its unfinished part
// file) selected, else its folder.
func (f *Files) OpenFolder(ctx context.Context, id int64) error {
	d, err := f.Daemon.Get(ctx, id)
	if err != nil {
		return err
	}
	dir := d.GetDir()
	if !filepath.IsAbs(dir) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d has no absolute folder (%q)", id, dir))
	}
	dir = filepath.Clean(dir)
	if path, err := DownloadPath(d); err == nil {
		for _, p := range []string{path, path + PartSuffix} {
			if _, err := os.Lstat(p); err == nil {
				if err := f.Shell.Reveal(p); err != nil {
					return shellError("show", p, err)
				}
				return nil
			}
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return statError(dir, err)
	}
	if !info.IsDir() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is not a folder", dir))
	}
	if err := f.Shell.ShowFolder(dir); err != nil {
		return shellError("show", dir, err)
	}
	return nil
}

// FileExists reports whether the file of download id is on disk. The Download again
// dialog asks it: without the file there is nothing to keep or replace
// (docs/ENGINE-SPEC.md §7.1). A download without a file name yet, or whose name is not a
// usable file in an absolute folder, has none.
func (f *Files) FileExists(ctx context.Context, id int64) (bool, error) {
	d, err := f.Daemon.Get(ctx, id)
	if err != nil {
		return false, err
	}
	path, err := DownloadPath(d)
	if err != nil {
		return false, nil
	}
	_, serr := os.Lstat(path)
	return serr == nil, nil
}

// FreeSpace returns the bytes available to the user on the volume that holds the absolute
// folder dir: the status bar's "D: 214 GiB free" and the Add dialog's free space
// (docs/DESIGN-SYSTEM.md §3.14, §6.2). A folder that does not exist yet is measured at its
// nearest existing parent. An empty or relative dir is invalid_argument, a failed read
// unavailable; no error repeats the folder.
func (f *Files) FreeSpace(dir string) (int64, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("dir must be an absolute path"))
	}
	read := f.FreeSpaceOf
	if read == nil {
		read = platform.FreeSpace
	}
	free, err := read(dir)
	if err != nil || free < 0 {
		// platform.FreeSpace's errors name the folder: none of it leaves this function.
		return 0, connect.NewError(connect.CodeUnavailable, errors.New("could not read the free space"))
	}
	return free, nil
}

// ---- Downex's own folders (Settings → About) ------------------------------------------

// FolderKind names one of Downex's folders; the window never names a path.
type FolderKind string

const (
	FolderData      FolderKind = "data"      // the database and settings
	FolderLogs      FolderKind = "logs"      // downexd.log and ui.log
	FolderTools     FolderKind = "tools"     // the yt-dlp and Deno copies
	FolderDownloads FolderKind = "downloads" // the default download folder
	// The unpacked extension that came with this installation (ExtensionFolderOf).
	FolderExtensionChrome  FolderKind = "extension-chrome"  // for Chrome, Edge and the other Chromium browsers
	FolderExtensionFirefox FolderKind = "extension-firefox" // for Firefox
)

// ParseFolderKind accepts exactly the six kinds.
func ParseFolderKind(s string) (FolderKind, bool) {
	switch k := FolderKind(s); k {
	case FolderData, FolderLogs, FolderTools, FolderDownloads, FolderExtensionChrome, FolderExtensionFirefox:
		return k, true
	}
	return "", false
}

// AppFolder resolves folder kind: downexd's own folders from its Status (the daemon may
// run with another database than the default), the download folder from its settings.
// When downexd cannot be asked, or is too old to say, the user's default folder is used.
// A folder downexd names that is not absolute is refused, never guessed. The extension's
// folders are this installation's (ExtensionFolder): downexd is not asked.
func (f *Files) AppFolder(ctx context.Context, kind FolderKind) (string, error) {
	if _, ok := ParseFolderKind(string(kind)); !ok {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New(`kind must be "data", "logs", "tools", "downloads", "extension-chrome" or "extension-firefox"`))
	}
	var dir string
	if IsExtensionFolder(kind) {
		var err error
		if dir, err = f.ExtensionFolder(kind); err != nil {
			return "", err
		}
	} else if kind == FolderDownloads {
		if s, err := f.Daemon.GetSettings(ctx); err == nil {
			dir = s.GetSettings().GetDefaultDir()
		}
	} else if st, err := f.Daemon.Status(ctx); err == nil {
		switch kind {
		case FolderData:
			dir = st.GetDataDir()
		case FolderLogs:
			dir = st.GetLogDir()
		case FolderTools:
			dir = st.GetToolsDir()
		}
	}
	if dir == "" {
		var err error
		if dir, err = DefaultFolder(kind); err != nil {
			return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no %s folder: %w", kind, err))
		}
	}
	if !filepath.IsAbs(dir) {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the %s folder is not an absolute path", kind))
	}
	return filepath.Clean(dir), nil
}

// DefaultFolder is the user's own folder of kind, as downexd uses it by default.
func DefaultFolder(kind FolderKind) (string, error) {
	switch kind {
	case FolderData:
		base, err := os.UserConfigDir() // store.DefaultPath's folder
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "Downex"), nil
	case FolderLogs:
		return platform.LogDir()
	case FolderTools:
		return platform.ToolsDir()
	}
	return platform.DownloadsDir()
}

// OpenAppFolder shows folder kind in the file manager, if it exists: an unknown kind is
// invalid_argument, a folder that does not exist (yet) failed_precondition.
func (f *Files) OpenAppFolder(ctx context.Context, kind FolderKind) error {
	dir, err := f.AppFolder(ctx, kind)
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the %s folder does not exist yet", kind))
	case err != nil:
		return statError(dir, err)
	case !info.IsDir():
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is not a folder", dir))
	}
	if err := f.Shell.ShowFolder(dir); err != nil {
		return shellError("show", dir, err)
	}
	f.logger().Info("app folder shown", "kind", string(kind))
	return nil
}
