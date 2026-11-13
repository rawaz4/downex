//go:build darwin

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// openTool is the command that asks Launch Services to start an application bundle;
// tests replace it with a stand-in that records its arguments.
var openTool = []string{"/usr/bin/open"}

// openTimeout bounds how long StartApp waits for open(1), which returns as soon as
// Launch Services has started the application.
const openTimeout = 30 * time.Second

// StartApp starts the desktop application exe with args detached from the caller
// (docs/MACOS-SPEC.md §2), because the user asked for its window. When exe is the main
// executable of an application bundle (".../<Name>.app/Contents/MacOS/<exe>"), the
// bundle is started through Launch Services with "open -n <bundle> --args <args>": a
// new instance even when one runs already (the single-instance rules are the
// application's), registered as an application of its own (Dock and menu bar as its
// Info.plist says, its own permissions) rather than a child of the caller, and
// activated by Launch Services. (Since macOS 14 an app that is not active can only ask
// to be activated, and gets it when the active app yields: a caller that is the active
// app yields to the bundle before it calls StartApp, ui/dock_darwin.m.) Launch Services
// gives it the user session's environment, plus the caller's DOWNEX_* variables.
// StartApp then waits for open(1) (at most openTimeout) and reports its failure, with
// its first message line.
//
// Any other executable is started like StartDetached.
func StartApp(exe string, args ...string) error { return startApp(exe, false, args) }

// StartAppInBackground is StartApp for a window that nobody asked for at that moment
// (the progress windows that downexd opens by itself): "open -n -g", so that Launch
// Services does not activate it and it does not take the focus from the app the user
// works in.
func StartAppInBackground(exe string, args ...string) error { return startApp(exe, true, args) }

func startApp(exe string, background bool, args []string) error {
	bundle, ok := appBundle(exe)
	if !ok {
		return StartDetached(exe, args...)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("platform: start %s: %w", exe, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	argv := append(openTool[1:len(openTool):len(openTool)], openArgs(bundle, background, forwardedEnv(os.Environ()), args)...)
	cmd := exec.CommandContext(ctx, openTool[0], argv...)
	cmd.Dir = "/"
	stderr := &cappedBuffer{max: 4 << 10}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if msg := firstLine(stderr.Bytes()); msg != "" {
			return fmt.Errorf("platform: start %s: open: %w: %s", bundle, err, msg)
		}
		return fmt.Errorf("platform: start %s: open: %w", bundle, err)
	}
	return nil
}

// cappedBuffer keeps the first max bytes written to it and discards the rest.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// firstLine is the first non-blank line of out, without surrounding blanks and at most
// 300 bytes long.
func firstLine(out []byte) string {
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 300 {
				line = strings.ToValidUTF8(line[:300], "") + "…"
			}
			return line
		}
	}
	return ""
}
