package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"downex/internal/client"
	"downex/internal/tools"
)

// The browsers' registration at the start (docs/BROWSER-SPEC.md §1, "Kept registered"): the
// user's own downexd runs "downex-nmhost register --keep" of its installation in the
// background, as the macOS window does at its start. The Confirm and progress windows, all
// that runs on many days, never register, so without it an extension ID that an update added
// to the relay (a store's copy) would not reach the manifests until the main window opened.
const (
	// registerDelay is how long after the start the registration waits: often a browser's
	// hand-off started this daemon, and that request is served first.
	registerDelay = 5 * time.Second
	// registerTimeout bounds one run of register (the macOS window allows it as long).
	registerTimeout = time.Minute
	// registerOutputMax bounds what is kept of register's output, for the log.
	registerOutputMax = 64 << 10
)

// registration keeps the relay of this installation registered with the browsers; tests
// replace its parts.
type registration struct {
	goos string
	// layout is this installation (installLayout).
	layout func() (client.Layout, error)
	// isFile reports a regular file at p.
	isFile func(p string) bool
	// run runs relay with args and returns what it wrote.
	run   func(ctx context.Context, relay string, args ...string) ([]byte, error)
	delay time.Duration
}

// browserRegistration is the registration of this executable's installation.
func browserRegistration() registration {
	return registration{
		goos:   runtime.GOOS,
		layout: func() (client.Layout, error) { return installLayout() },
		isFile: regularFile,
		run:    runRegister,
		delay:  registerDelay,
	}
}

// relay returns the downex-nmhost to run "register --keep" with, or "" and why there is none.
// Only the user's own daemon (own) of an installed Downex registers: on Windows one not in a
// bin folder, on macOS one inside a Downex.app that App Translocation does not run, and on
// neither one inside a source checkout (a go.mod, or the .git file of a worktree, in a folder
// above it). A development build would point the user's browsers at itself, and a translocated
// copy goes away. Linux installations register when they are installed.
func (r registration) relay(own bool) (string, string) {
	if !own {
		return "", "a private daemon"
	}
	var join func(...string) string
	var dir, base func(string) string
	switch r.goos {
	case "windows":
		join, dir, base = filepath.Join, filepath.Dir, filepath.Base
	case "darwin":
		// Layouts of macOS paths are handled with package path (client.LayoutOf).
		join, dir, base = path.Join, path.Dir, path.Base
	default:
		return "", "the installation registers the browsers on this system"
	}
	l, err := r.layout()
	switch {
	case err != nil:
		return "", "cannot locate downexd: " + err.Error()
	case r.goos == "darwin" && l.Bundle == "":
		return "", "downexd is not inside Downex.app"
	case r.goos == "darwin" && l.Translocated():
		return "", "Downex.app runs from a temporary copy (App Translocation)"
	case r.goos == "windows" && strings.EqualFold(base(l.Dir), "bin"):
		return "", "a development build (in a bin folder)"
	}
	for d := l.Dir; ; d = dir(d) {
		if r.isFile(join(d, "go.mod")) || r.isFile(join(d, ".git")) {
			return "", "a development build (in the source tree " + d + ")"
		}
		if dir(d) == d {
			break
		}
	}
	name := "downex-nmhost"
	if r.goos == "windows" {
		name += ".exe"
	}
	relay := join(l.Dir, name)
	if !r.isFile(relay) {
		return "", "downex-nmhost is not installed next to downexd"
	}
	return relay, ""
}

// start runs "register --keep" of relay(own) in the background, r.delay after now, and
// returns a function that stops it (a register still running is killed: it replaces each file
// at once) and waits for it. A failure is only logged: Settings → Integration registers again.
func (r registration) start(ctx context.Context, log *slog.Logger, own bool) (stop func()) {
	relay, why := r.relay(own)
	if relay == "" {
		log.Debug("the browsers' registration is left as it is", "reason", why)
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(r.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		rctx, rcancel := context.WithTimeout(ctx, registerTimeout)
		out, err := r.run(rctx, relay, "register", "--keep")
		rcancel()
		last := lastLine(out)
		switch {
		case ctx.Err() != nil:
			// Stopping: the daemon's end, not a failure of the registration.
		case err != nil:
			log.Warn("cannot keep Downex registered with the browsers", "relay", relay, "err", err, "output", last)
		case strings.HasPrefix(last, "Done:"):
			log.Info("Downex registered with the browsers again", "relay", relay, "output", last)
		default:
			log.Debug("the browsers' registration is up to date", "relay", relay, "output", last)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// runRegister runs relay with args the way downexd runs its helpers (tools.Exec: no console
// window on Windows, killed with ctx), with this process's environment: register finds the
// user's folders through it.
func runRegister(ctx context.Context, relay string, args ...string) ([]byte, error) {
	res, err := tools.Exec(ctx, tools.ExecSpec{
		Path:      relay,
		Args:      args,
		Env:       os.Environ(),
		Timeout:   registerTimeout,
		MaxStdout: registerOutputMax,
		MaxStderr: registerOutputMax,
	})
	out := append(res.Stdout, res.StderrTail...)
	switch {
	case err != nil:
		return out, err
	case res.TimedOut:
		return out, errors.New("downex-nmhost did not finish in time")
	case res.ExitCode != 0:
		return out, fmt.Errorf("downex-nmhost ended with exit status %d", res.ExitCode)
	}
	return out, nil
}

// lastLine is the last line of out that is not blank, trimmed.
func lastLine(out []byte) string {
	lines := bytes.Split(bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n")), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 {
			return string(l)
		}
	}
	return ""
}
