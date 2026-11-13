package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// windowsInstaller updates a per-user installation made by Downex Setup
// (installer/downex.nsi): it runs "Downex-Setup-<v>.exe /S /RELAUNCH" detached. Setup finds the
// installation through its Uninstall entry, stops the processes running from it, replaces
// the files and starts the new downexd.
type windowsInstaller struct{ sys System }

func (w *windowsInstaller) FileName(v Version) string { return "Downex-Setup-" + v.String() + ".exe" }

// Prepare refuses a copy that Setup did not install (no uninstall.exe next to downexd,
// e.g. a build folder): Setup would install a second copy elsewhere.
func (w *windowsInstaller) Prepare(context.Context) error {
	l, err := w.sys.Layout()
	if err != nil {
		return fmt.Errorf("update: locate the installation: %w", err)
	}
	if fi, err := os.Stat(filepath.Join(l.Dir, "uninstall.exe")); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: this copy of Downex was not installed with Setup; download and run the new Setup yourself", ErrNotUpdatable)
	}
	return nil
}

// Stage checks the Authenticode signature of the Setup when the running downexd is
// signed: then the Setup must carry a valid signature too.
func (w *windowsInstaller) Stage(_ context.Context, path string, _ Version) (Staged, error) {
	l, err := w.sys.Layout()
	if err != nil {
		return nil, fmt.Errorf("update: locate the installation: %w", err)
	}
	switch err := w.sys.Authenticode(l.Exe); {
	case err == nil:
		if serr := w.sys.Authenticode(path); serr != nil {
			return nil, fmt.Errorf("%w: this Downex is signed, the Setup is not validly signed: %v", ErrCodeSignature, serr)
		}
	case errors.Is(err, ErrUnsigned):
		w.sys.Logger.Info("this downexd is not Authenticode signed; the update is checked by its signed manifest only")
	default:
		w.sys.Logger.Warn("cannot check the Authenticode signature of this downexd; the update is checked by its signed manifest only", "err", err)
	}
	return &stagedSetup{sys: w.sys, path: path}, nil
}

type stagedSetup struct {
	sys  System
	path string
}

// Commit starts the Setup silently and detached from the daemon. /RELAUNCH has Setup
// open the window again when it closed one for the update (installer/downex.nsi), as
// Settings promises; a Setup without that option ignores it.
func (s *stagedSetup) Commit() error {
	if err := s.sys.StartDetached(s.path, "/S", "/RELAUNCH"); err != nil {
		return fmt.Errorf("update: start the Setup: %w", err)
	}
	return nil
}

func (s *stagedSetup) Discard() {}
