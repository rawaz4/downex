package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"connectrpc.com/connect"

	"downex/internal/platform"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Shell is the desktop's file handling (shell_<os>.go in production, a fake in tests).
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

// partSuffix is the suffix of an unfinished download's file (engine.PartPath).
const partSuffix = ".dxpart"

// risk is what opening a file could do.
type risk int

const (
	riskNone risk = iota
	// riskContent: the file's application runs code that the file contains, mounts it,
	// or connects to a computer that the file names.
	riskContent
	// riskProgram: opening the file runs or installs it.
	riskProgram
)

// Downloads carry no Mark-of-the-Web yet (U6), so neither the system (SmartScreen) nor
// Office (its block of macros and add-ins from the internet) warns before such files
// run: the confirmation in openFile is the only gate. The lists hold extensions in lower
// case, without the dot; the platforms add their own (shell_<os>.go: PATHEXT and the
// types Windows considers dangerous to open, the execute bit on Linux; macos.go: the
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

// riskOf reports what opening the file named name (with info, when known) could do.
func riskOf(name string, info fs.FileInfo) risk {
	return classify(name, info, osRisk)
}

// classify is riskOf with the platform's own rules (osRisk) given as platform, so that
// the rules of every platform can be tested anywhere.
func classify(name string, info fs.FileInfo, platform func(ext string, info fs.FileInfo) risk) risk {
	// Windows ignores trailing dots and spaces: "setup.exe. " opens as "setup.exe".
	base := strings.TrimRight(filepath.Base(name), ". ")
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(base), "."))
	r := riskNone
	switch {
	case ext == "":
	case programExtensions[ext]:
		return riskProgram
	case contentExtensions[ext]:
		r = riskContent
	}
	return max(r, platform(ext, info))
}

// invalidName reports a file name that does not name a file directly inside a folder.
func invalidName(name string) bool {
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

// downloadPath returns dir/filename of d after checking that the daemon's record names
// a file inside an absolute folder.
func downloadPath(d *downexv1.Download) (string, error) {
	dir, name := d.GetDir(), d.GetFilename()
	if name == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d has no file name yet", d.GetId()))
	}
	if !filepath.IsAbs(dir) || invalidName(name) {
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

// openFile opens download id with the default application, if it is completed and
// its file exists. A program, or a file that can run code or connect out (riskOf), is
// opened only after the user confirms it in a native dialog; when they decline,
// nothing happens and no error is reported.
func (a *App) openFile(id int64) error {
	return a.openCompleted(id, a.shell.Open)
}

// openWith opens download id with an application that the user chooses (Shell.OpenWith,
// docs/DESKTOP-SPEC.md §8), after the checks and the confirmation of openFile: the
// chosen application may run the file as well.
func (a *App) openWith(id int64) error {
	return a.openCompleted(id, a.shell.OpenWith)
}

// openCompleted hands the file of download id to open once it is a completed download
// whose file is a regular file with a usable name, and once the user has confirmed a
// program or a file that can run code (see openFile).
func (a *App) openCompleted(id int64, open func(path string) error) error {
	ctx, cancel := a.callContext()
	d, err := a.daemon.Get(ctx, id)
	cancel()
	if err != nil {
		return err
	}
	if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("download %d is not completed (%s)", id, stateName(d.GetState())))
	}
	path, err := downloadPath(d)
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
	if r := max(riskOf(path, info), riskOf(target, info)); r != riskNone {
		h := a.runtimeHost()
		if h == nil {
			return errNoWindow()
		}
		title, text := confirmText(r, path, target)
		ok, err := h.Confirm(title, text)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("confirmation dialog: %w", err))
		}
		if !ok {
			a.log.Info("opening a program was declined", "download", id)
			return nil
		}
	}
	if err := open(path); err != nil {
		return shellError("open", path, err)
	}
	return nil
}

// confirmText is the question asked before the file at path (a link to target, when
// they differ), whose risk is r, is opened.
func confirmText(r risk, path, target string) (title, text string) {
	name := displayName(filepath.Base(path))
	if target != path {
		name += fmt.Sprintf(" (a link to %s)", displayName(target))
	}
	folder := displayName(filepath.Dir(path))
	if r == riskProgram {
		return "Run this program?", fmt.Sprintf("%s is a program or script. Opening it runs it on this computer, with your rights.\n\n"+
			"Programs from the internet can harm your computer. Open it only if you trust where it came from.\n\n"+
			"Folder: %s", name, folder)
	}
	return "Open this file?", fmt.Sprintf("%s is a type of file that can run code or connect to other computers when it is opened, "+
		"with your rights (such as a document with macros, a disk image or a remote desktop connection).\n\n"+
		"Files from the internet can harm your computer. Open it only if you trust where it came from.\n\n"+
		"Folder: %s", name, folder)
}

// openFolder shows download id in the file manager: its file (or its unfinished part
// file) selected, else its folder.
func (a *App) openFolder(id int64) error {
	ctx, cancel := a.callContext()
	d, err := a.daemon.Get(ctx, id)
	cancel()
	if err != nil {
		return err
	}
	dir := d.GetDir()
	if !filepath.IsAbs(dir) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d has no absolute folder (%q)", id, dir))
	}
	dir = filepath.Clean(dir)
	if path, err := downloadPath(d); err == nil {
		for _, p := range []string{path, path + partSuffix} {
			if _, err := os.Lstat(p); err == nil {
				if err := a.shell.Reveal(p); err != nil {
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
	if err := a.shell.ShowFolder(dir); err != nil {
		return shellError("show", dir, err)
	}
	return nil
}

// shellError maps a failure of the desktop shell.
func shellError(verb, path string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot %s %s: %w", verb, path, err))
}

// stateName is a download state for messages ("paused").
func stateName(s downexv1.DownloadState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "DOWNLOAD_STATE_"))
}

// displayName makes s safe to show in a native dialog: bidirectional formatting and
// other invisible or control characters, which can make "exe" read as another type,
// are shown as ⟨U+XXXX⟩.
func displayName(s string) string {
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

// existingDir returns dir when it is an existing absolute folder, else its nearest
// existing parent, else "".
func existingDir(dir string) string {
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

// ---- Downex's own folders (Settings → About) -----------------------------------------

// appFolderRequest is the argument of OpenAppFolder.
type appFolderRequest struct {
	Kind string `json:"kind"`
}

// folderKind names one of Downex's folders; the page never sends a path.
type folderKind string

const (
	folderData      folderKind = "data"      // the database and settings
	folderLogs      folderKind = "logs"      // downexd.log and ui.log
	folderTools     folderKind = "tools"     // the yt-dlp and Deno copies
	folderDownloads folderKind = "downloads" // the default download folder
)

// parseFolderKind accepts exactly the four kinds.
func parseFolderKind(s string) (folderKind, bool) {
	switch k := folderKind(s); k {
	case folderData, folderLogs, folderTools, folderDownloads:
		return k, true
	}
	return "", false
}

// appFolder resolves folder kind: downexd's own folders from its Status (the daemon may
// run with another database than the default), the download folder from its settings.
// When downexd cannot be asked, or is too old to say, the user's default folder is used.
// A folder downexd names that is not absolute is refused, never guessed.
func (a *App) appFolder(kind folderKind) (string, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	var (
		dir  string
		err  error
		told bool // downexd answered with a folder
	)
	if kind == folderDownloads {
		if s, serr := a.daemon.GetSettings(ctx); serr == nil {
			dir = s.GetSettings().GetDefaultDir()
		}
	} else if st, serr := a.daemon.Status(ctx); serr == nil {
		switch kind {
		case folderData:
			dir = st.GetDataDir()
		case folderLogs:
			dir = st.GetLogDir()
		case folderTools:
			dir = st.GetToolsDir()
		}
	}
	told = dir != ""
	if !told {
		dir, err = defaultFolder(kind)
		if err != nil {
			return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no %s folder: %w", kind, err))
		}
	}
	if !filepath.IsAbs(dir) {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the %s folder is not an absolute path", kind))
	}
	return filepath.Clean(dir), nil
}

// defaultFolder is the user's own folder of kind, as downexd uses it by default.
func defaultFolder(kind folderKind) (string, error) {
	switch kind {
	case folderData:
		base, err := os.UserConfigDir() // store.DefaultPath's folder
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "Downex"), nil
	case folderLogs:
		return platform.LogDir()
	case folderTools:
		return platform.ToolsDir()
	}
	return platform.DownloadsDir()
}

// openAppFolder shows folder kind in the file manager, if it exists.
func (a *App) openAppFolder(kind folderKind) error {
	dir, err := a.appFolder(kind)
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
	if err := a.shell.ShowFolder(dir); err != nil {
		return shellError("show", dir, err)
	}
	a.log.Info("app folder shown", "kind", string(kind))
	return nil
}

// fileExistsResult is the result of FileExists.
type fileExistsResult struct {
	Exists bool `json:"exists"`
}

// FileExists reports whether the file of a download ({id}) is on disk: {exists}. The
// Download again dialog asks it: without the file there is nothing to keep or replace
// (docs/ENGINE-SPEC.md §7.1). A download without a file name yet, or whose name is not a
// usable file in an absolute folder, has none.
func (a *App) FileExists(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.GetRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	d, err := a.daemon.Get(ctx, in.GetId())
	cancel()
	if err != nil {
		return nil, apiError(err)
	}
	exists := false
	if path, err := downloadPath(d); err == nil {
		_, serr := os.Lstat(path)
		exists = serr == nil
	}
	b, err := json.Marshal(fileExistsResult{Exists: exists})
	if err != nil {
		return nil, apiError(connect.NewError(connect.CodeInternal, err))
	}
	return b, nil
}
