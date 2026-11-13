package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"connectrpc.com/connect"

	"downex/internal/client"
)

// ---- the browser extension (Settings → Integration) ----------------------------------

// The unpacked extension ships with every installation (Setup's extension\chrome-mv3 and
// extension\firefox-mv3 next to the programs, Downex.app's Contents/Resources/extension,
// the build's bin/extension), and the relay downex-nmhost sits next to downexd. Both are
// found from the running program (client.FindLayout), never from the current directory or
// PATH: Setup does not put the install folder on PATH.

// extensionTargets are the folders inside "extension" of each extension kind.
var extensionTargets = map[FolderKind]string{
	FolderExtensionChrome:  "chrome-mv3",
	FolderExtensionFirefox: "firefox-mv3",
}

// IsExtensionFolder reports whether kind is one of the extension's folders.
func IsExtensionFolder(kind FolderKind) bool {
	_, ok := extensionTargets[kind]
	return ok
}

// ExtensionFolderOf is where the installation of l keeps the extension folder of kind:
// Contents/Resources/extension/<target> inside Downex.app, else extension/<target> next to
// the programs. false for a kind that is not an extension folder.
func ExtensionFolderOf(l client.Layout, kind FolderKind) (string, bool) {
	target, ok := extensionTargets[kind]
	if !ok {
		return "", false
	}
	if l.Bundle != "" {
		// macOS paths, whatever system checks the rule (as client.LayoutOf)
		return path.Join(l.Bundle, "Contents", "Resources", "extension", target), true
	}
	return filepath.Join(l.Dir, "extension", target), true
}

// RelayOf is the relay downex-nmhost of the installation of l.
func RelayOf(l client.Layout, goos string) string {
	name := "downex-nmhost"
	if goos == "windows" {
		name += ".exe"
	}
	if l.Bundle != "" {
		return path.Join(l.Dir, name)
	}
	return filepath.Join(l.Dir, name)
}

func (f *Files) layout() (client.Layout, error) {
	exe := f.Executable
	if exe == nil {
		exe = os.Executable
	}
	l, err := client.FindLayout(exe)
	if err != nil {
		return client.Layout{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot locate this program: %w", err))
	}
	return l, nil
}

// ErrTranslocated is ExtensionFolder's answer (in a failed_precondition) while Downex.app runs from the copy that
// Gatekeeper's App Translocation made of it: that copy goes away when Downex quits, so a
// browser must never load the extension from there.
var ErrTranslocated = errors.New("Downex runs from a temporary copy that macOS made: move Downex.app to Applications with the Finder and open it again")

// ExtensionFolder is the absolute folder of the extension of kind (FolderExtensionChrome
// or FolderExtensionFirefox) that came with this installation, whether or not it is there:
// the page shows it so that the browser's "Load unpacked" can be pointed at it. It reads
// no file and asks nothing, so it does not block. A translocated Downex.app
// (extensionFolderIn) is failed_precondition.
func (f *Files) ExtensionFolder(kind FolderKind) (string, error) {
	if !IsExtensionFolder(kind) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New(`kind must be "extension-chrome" or "extension-firefox"`))
	}
	l, err := f.layout()
	if err != nil {
		return "", err
	}
	return extensionFolderIn(l, kind)
}

// extensionFolderIn is ExtensionFolder for the installation of l: ExtensionFolderOf, or
// failed_precondition (ErrTranslocated) when l is a copy that App Translocation made
// below /private/var/folders/.../AppTranslocation/ (client.Layout.Translocated).
func extensionFolderIn(l client.Layout, kind FolderKind) (string, error) {
	if l.Translocated() {
		return "", connect.NewError(connect.CodeFailedPrecondition, ErrTranslocated)
	}
	dir, _ := ExtensionFolderOf(l, kind)
	return dir, nil
}

// relayOutputMax bounds what is kept of the relay's output: its messages are a few lines.
const relayOutputMax = 16 << 10

// RunRelayFunc runs the relay exe with args (no shell) and returns its output, stdout and
// stderr together; a non-zero exit is an error.
type RunRelayFunc func(ctx context.Context, exe string, args ...string) ([]byte, error)

// runRelay is the product's RunRelayFunc: exe itself, without a shell and without a
// console window (relayCommand), its output bounded.
func runRelay(ctx context.Context, exe string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	relayCommand(cmd)
	var out capped
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// capped keeps the first relayOutputMax bytes written to it.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := relayOutputMax - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// RegisterBrowsers registers this installation's relay with the browsers again: it runs
// "<the programs' folder>/downex-nmhost register", as Setup does, for when the extension
// says Downex is not set up (a browser installed after Downex, a registration removed).
// A relay that is not there is failed_precondition; a register that fails is internal,
// with the relay's own last message.
func (f *Files) RegisterBrowsers(ctx context.Context) error {
	return f.registerBrowsers(ctx, "register")
}

// KeepBrowsersRegistered is RegisterBrowsers for the macOS main window's start, which nobody
// asked for: "downex-nmhost register --keep" adds the built-in extension IDs and this
// relay's path where they are missing and leaves what is there (IDs registered by hand, a
// .bak) as it is.
func (f *Files) KeepBrowsersRegistered(ctx context.Context) error {
	return f.registerBrowsers(ctx, "register", "--keep")
}

func (f *Files) registerBrowsers(ctx context.Context, args ...string) error {
	l, err := f.layout()
	if err != nil {
		return err
	}
	relay := RelayOf(l, runtime.GOOS)
	if info, err := os.Stat(relay); err != nil || !info.Mode().IsRegular() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("downex-nmhost is not installed next to Downex"))
	}
	run := f.RunRelay
	if run == nil {
		run = runRelay
	}
	out, err := run(ctx, relay, args...)
	if err != nil {
		f.logger().Warn("browser registration failed", "error", err)
		if ctx.Err() != nil {
			return connect.NewError(connect.CodeDeadlineExceeded, errors.New("downex-nmhost did not finish in time"))
		}
		msg := RelayMessage(out)
		if msg == "" {
			msg = "downex-nmhost could not register Downex"
		}
		return connect.NewError(connect.CodeInternal, errors.New(msg))
	}
	f.logger().Info("browsers registered again")
	return nil
}

// RelayMessage is the last line the relay wrote, without its "downex-nmhost register: " or
// "downex-nmhost: " prefix (the window says what it ran).
func RelayMessage(out []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		for _, p := range []string{"downex-nmhost register: ", "downex-nmhost: "} {
			l = strings.TrimPrefix(l, p)
		}
		return l
	}
	return ""
}
