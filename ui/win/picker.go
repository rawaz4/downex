//go:build windows

package win

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/sys/windows"

	"downex/ui/core/pageurl"
	"downex/ui/win/internal/com"
)

// The pickers of today's window (Wails v2.17's OpenDirectoryDialog and OpenFileDialog on
// Windows): the shell's common item dialog, owned by the window while it is shown, starting
// in a folder that exists. The caller gives the title and the start folder: the host's
// FolderPickerTitle and FilePickerTitleOf, and ExistingDir of the folder asked for.

// folderRole is the role today's folder picker has: its client GUID keeps the dialog's
// last folder and size apart from other programs' dialogs (roleGUID).
const folderRole = "PickFolder"

// allFiles is the file picker's only filter, as today's: "All Files (*.*)".
var allFiles = [...]string{"All Files (*.*)", "*.*"}

// PickFolder shows the folder picker titled title, starting in start ("" leaves the dialog's
// own choice: the folder it showed last), and returns the chosen folder, or "" when the
// user cancels. A start folder that does not exist is an error, as today's. It blocks until
// the user answers; ctx ends a wait for an earlier dialog of the window only.
func (d *Dialogs) PickFolder(ctx context.Context, title, start string) (string, error) {
	return d.pick(ctx, title, start, true)
}

// PickFile shows the file picker for one existing file, titled title, starting in start, and
// returns the chosen file, or "" when the user cancels.
func (d *Dialogs) PickFile(ctx context.Context, title, start string) (string, error) {
	return d.pick(ctx, title, start, false)
}

func (d *Dialogs) pick(ctx context.Context, title, start string, folders bool) (string, error) {
	if start != "" {
		abs, err := filepath.Abs(start)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", err
		}
		start = abs
	}
	var (
		path   string
		runErr error
	)
	if err := d.do(ctx, func() { path, runErr = d.showPicker(title, start, folders) }); err != nil {
		return "", err
	}
	if errors.Is(runErr, errCancelled) {
		return "", nil
	}
	return path, runErr
}

// showPicker runs one picker on the UI thread.
func (d *Dialogs) showPicker(title, start string, folders bool) (string, error) {
	dlg, err := com.CreateInstance[fileDialog]("FileOpenDialog", &clsidFileOpenDialog, &iidFileOpenDialog)
	if err != nil {
		return "", err
	}
	defer dlg.Release()
	if folders {
		if err := dlg.addOptions(fosPickFolders); err != nil {
			return "", err
		}
	}
	if title != "" {
		if err := dlg.setTitle(title); err != nil {
			return "", err
		}
	}
	if folders {
		g := roleGUID(folderRole)
		if err := dlg.setClientGUID(&g); err != nil {
			return "", err
		}
	}
	if start != "" {
		if err := dlg.setFolder(start); err != nil {
			return "", err
		}
	}
	if !folders {
		name, err1 := windows.UTF16PtrFromString(allFiles[0])
		spec, err2 := windows.UTF16PtrFromString(allFiles[1])
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("picker filter")
		}
		specs := []comFilterSpec{{name: name, spec: spec}}
		if err := dlg.setFileTypes(specs); err != nil {
			return "", err
		}
	}
	if err := dlg.show(uintptr(d.ownerHWND())); err != nil {
		return "", err
	}
	return dlg.result()
}

// externalURL is what OpenExternal opens: an https address with a host, as today's
// openExternal accepts (wails.ts).
var externalURL = regexp.MustCompile(`(?i)^https://[^/]`)

// ErrNotHTTPS is OpenExternal's answer to anything but an https address; today's window
// rejects it as invalid_argument "not an https address".
var ErrNotHTTPS = errors.New("not an https address")

// OpenExternal opens an https address (a licence's page) in the system's browser: the
// shell's "open" of the address, which runs the default browser. It needs no window.
func OpenExternal(ctx context.Context, url string) error {
	if !externalURL.MatchString(url) {
		return ErrNotHTTPS
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return ErrNotHTTPS
	}
	verb, _ := windows.UTF16PtrFromString("open")
	if err := windows.ShellExecute(0, verb, u, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("win: open %s: %w", url, err)
	}
	return nil
}

// OpenPage opens a download's page (docs/DESKTOP-SPEC.md §10, "Open page") in the system's
// browser: an http or https address only (pageurl.Check), re-serialised before the shell's
// "open" sees it, so that no path, file:, shell: or ms-settings: address is ever run. The
// user always clicks for it.
func OpenPage(ctx context.Context, raw string) error {
	page, err := pageurl.Check(raw)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(page)
	if err != nil {
		return pageurl.ErrNotPage
	}
	verb, _ := windows.UTF16PtrFromString("open")
	if err := windows.ShellExecute(0, verb, u, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("win: open the page: %w", err)
	}
	return nil
}
