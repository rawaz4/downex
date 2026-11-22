package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"downex/internal/client"
	"downex/internal/platform"
)

// Installer applies a verified package to the running installation. The caller
// (downexd) runs Prepare before it downloads anything and Stage once the package is
// verified, both while it keeps serving; only when Stage succeeded does it pause its
// downloads, call Commit and exit, whatever Commit returns.
type Installer interface {
	// FileName is the file name that the package of version v is saved under.
	FileName(v Version) string
	// Prepare reports whether the running installation can be updated in place (errors
	// wrap ErrUnsupported or ErrNotUpdatable).
	Prepare(ctx context.Context) error
	// Stage checks the downloaded package at path, the package of version want in a
	// verified manifest, and prepares everything that can fail without changing the
	// installation.
	Stage(ctx context.Context, path string, want Version) (Staged, error)
}

// Staged is a package ready to be installed.
type Staged interface {
	// Commit installs the package. The daemon has paused its downloads and exits right
	// after it, whatever the result: the new version (or, when the installation could not
	// be changed, the old one) is started by Setup or by a relaunch helper that waits for
	// the daemon to exit.
	Commit() error
	// Discard drops a staged package that will not be installed.
	Discard()
}

// System is what installers use of the running system. Zero fields select the real
// ones (DefaultSystem); tests replace them.
type System struct {
	// GOOS selects the installer (runtime.GOOS).
	GOOS string
	// Layout is the running installation (client.FindLayout(os.Executable)).
	Layout func() (client.Layout, error)
	// Run runs a command and returns its combined output (exec with a timeout).
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// StartDetached starts a process that outlives the daemon (platform.StartDetached).
	StartDetached func(exe string, args ...string) error
	// StartDetachedCommandLine is StartDetached with the whole command line, argv[0]
	// included, given as it is (Windows: platform.StartDetachedCommandLine; the Setup's
	// /D= must come last and unquoted).
	StartDetachedCommandLine func(exe, cmdLine string) error
	// Getenv reads the environment (os.Getenv): DOWNEX_ALLOW_UNSIGNED_UPDATE.
	Getenv func(string) string
	// Authenticode reports the Authenticode signature of a file (Windows): its signer and
	// nil for a valid signature, an error wrapping ErrUnsigned without one, any other error
	// for an invalid one. The default is VerifyAuthenticode (WinVerifyTrust on Windows).
	Authenticode func(path string) (Signer, error)
	// Pid is the daemon's process id (os.Getpid), which a relaunch helper waits for.
	Pid int
	// Logger receives what the installer does (default: discarded).
	Logger *slog.Logger
}

// Signer is who signed a file with Authenticode (VerifyAuthenticode).
type Signer struct {
	// Subject is the simple display name (the common name) of the signing certificate's
	// subject, e.g. "Microsoft Corporation"; "" when it could not be read.
	Subject string
	// SubjectName is the signing certificate's whole subject distinguished name as it is
	// encoded in the certificate (DER): the publisher, which a renewed certificate keeps
	// while its thumbprint changes. Empty when it could not be read.
	SubjectName []byte
}

// ErrUnsigned: a file has no Authenticode signature (System.Authenticode).
var ErrUnsigned = errors.New("the file has no Authenticode signature")

// AllowUnsignedEnv names the environment variable with which an ad-hoc signed macOS build
// (no Developer ID team) installs an ad-hoc signed update without checking that the new
// bundle's version is the manifest's (tests).
const AllowUnsignedEnv = "DOWNEX_ALLOW_UNSIGNED_UPDATE"

// commandTimeout bounds one command of an installer.
const commandTimeout = 5 * time.Minute

// withDefaults fills the zero fields of s.
func (s System) withDefaults() System {
	if s.GOOS == "" {
		s.GOOS = runtime.GOOS
	}
	if s.Layout == nil {
		s.Layout = func() (client.Layout, error) { return client.FindLayout(os.Executable) }
	}
	if s.Run == nil {
		s.Run = runCommand
	}
	if s.StartDetached == nil {
		s.StartDetached = platform.StartDetached
	}
	if s.StartDetachedCommandLine == nil {
		s.StartDetachedCommandLine = startDetachedCommandLine
	}
	if s.Getenv == nil {
		s.Getenv = os.Getenv
	}
	if s.Authenticode == nil {
		s.Authenticode = VerifyAuthenticode
	}
	if s.Pid == 0 {
		s.Pid = os.Getpid()
	}
	if s.Logger == nil {
		s.Logger = slog.New(slog.DiscardHandler)
	}
	return s
}

// NewInstaller returns the installer of sys.GOOS: Windows Setup, the macOS app bundle,
// or one that refuses with ErrUnsupported (Linux and others: the package manager
// updates Downex there).
func NewInstaller(sys System) Installer {
	sys = sys.withDefaults()
	switch sys.GOOS {
	case "windows":
		return &windowsInstaller{sys: sys}
	case "darwin":
		return &macInstaller{sys: sys}
	}
	return unsupportedInstaller{}
}

type unsupportedInstaller struct{}

func (unsupportedInstaller) FileName(v Version) string { return "Downex-" + v.String() + ".pkg" }
func (unsupportedInstaller) Prepare(context.Context) error {
	return fmt.Errorf("%w: install it with the system's package manager", ErrUnsupported)
}
func (unsupportedInstaller) Stage(context.Context, string, Version) (Staged, error) {
	return nil, ErrUnsupported
}

// runCommand runs name with args (no shell) and returns its combined output, at most
// 64 KiB of it.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = os.TempDir()
	out := &capped{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.buf.Bytes(), err
}

type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// commandError describes a failed command with the first line of its output.
func commandError(what string, out []byte, err error) error {
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimSpace(line); line != "" {
			return fmt.Errorf("%s: %w: %s", what, err, truncate(line, 300))
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}
