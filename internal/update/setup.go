package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// windowsInstaller updates a per-user installation made by Downex Setup
// (installer/downex.nsi): it runs "Downex-Setup-<v>.exe /S /RELAUNCH /D=<folder>" detached,
// with the folder of the running installation. Setup stops the processes running from that
// folder, replaces the files in place, removes an installation that its Uninstall entry
// names in another folder (so that one installation remains) and starts the new downexd.
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
// signed: then the Setup must carry a valid signature by the same publisher, the same
// subject distinguished name of the signing certificate (M6-SPEC §4). An unsigned downexd
// (development and today's releases) leaves the Setup to the signed manifest.
func (w *windowsInstaller) Stage(_ context.Context, path string, _ Version) (Staged, error) {
	l, err := w.sys.Layout()
	if err != nil {
		return nil, fmt.Errorf("update: locate the installation: %w", err)
	}
	cmdLine, err := setupCommandLine(path, l.Dir)
	if err != nil {
		return nil, err
	}
	switch own, err := w.sys.Authenticode(l.Exe); {
	case err == nil:
		setup, serr := w.sys.Authenticode(path)
		if serr != nil {
			return nil, fmt.Errorf("%w: this Downex is signed, the Setup is not validly signed: %v", ErrCodeSignature, serr)
		}
		if len(own.SubjectName) == 0 {
			return nil, fmt.Errorf("%w: cannot read the publisher of this Downex's signature", ErrCodeSignature)
		}
		if !bytes.Equal(setup.SubjectName, own.SubjectName) {
			return nil, fmt.Errorf("%w: the Setup is signed by %.64q, not by this Downex's publisher %.64q", ErrCodeSignature, setup.Subject, own.Subject)
		}
	case errors.Is(err, ErrUnsigned):
		w.sys.Logger.Info("this downexd is not Authenticode signed; the update is checked by its signed manifest only")
	default:
		w.sys.Logger.Warn("cannot check the Authenticode signature of this downexd; the update is checked by its signed manifest only", "err", err)
	}
	return &stagedSetup{sys: w.sys, path: path, cmdLine: cmdLine}, nil
}

// setupCommandLine is the command line that runs the Setup at path silently over the
// installation in dir: "<path>" /S /RELAUNCH /D=<dir>. /RELAUNCH has Setup open the window
// again when it closed one for the update, as Settings promises (a Setup without that
// option ignores it). /D= names the running installation's folder, so that Setup replaces
// exactly that one, also when its Uninstall entry is missing or names another folder
// (Setup then removes that other one). NSIS takes /D= only as the last option and
// unquoted, spaces included, which no argument quoting gives.
func setupCommandLine(path, dir string) (string, error) {
	for _, p := range []string{path, dir} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\"\x00\r\n") {
			return "", fmt.Errorf("%w: %q is not a usable absolute path for Setup", ErrNotUpdatable, p)
		}
	}
	return `"` + path + `" /S /RELAUNCH /D=` + dir, nil
}

type stagedSetup struct {
	sys     System
	path    string
	cmdLine string // setupCommandLine
}

// Commit starts the Setup silently and detached from the daemon (setupCommandLine).
func (s *stagedSetup) Commit() error {
	if err := s.sys.StartDetachedCommandLine(s.path, s.cmdLine); err != nil {
		return fmt.Errorf("update: start the Setup: %w", err)
	}
	return nil
}

func (s *stagedSetup) Discard() {}
