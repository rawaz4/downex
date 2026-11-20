package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// unquarantine removes macOS's quarantine flag from the installed Downex.app and from the
// tools folder (the user's own daemon only). Downex is not notarized yet: a flagged copy makes
// Gatekeeper ask "Move to Trash?" each time a browser starts the native-messaging helper, the
// login item starts the agent, or a helper app opens. The user already chose to open Downex,
// so its own files need not be asked about again. A copy that App Translocation runs is left
// alone (its path goes away). Best effort and in the background: a failure is only logged.
func unquarantine(log *slog.Logger, toolsDir string) {
	if runtime.GOOS != "darwin" {
		return
	}
	var paths []string
	if l, err := installLayout(); err == nil && l.Bundle != "" && !l.Translocated() {
		paths = append(paths, l.Bundle)
	}
	if toolsDir != "" {
		if _, err := os.Stat(toolsDir); err == nil {
			paths = append(paths, toolsDir)
		}
	}
	if len(paths) == 0 {
		return
	}
	go func() {
		for _, p := range paths {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			// xattr -d fails on files without the flag; -r goes on through the rest.
			out, err := exec.CommandContext(ctx, "/usr/bin/xattr", "-dr", "com.apple.quarantine", p).CombinedOutput()
			cancel()
			if err != nil && len(out) > 0 && !isNoSuchXattr(out) {
				log.Debug("could not remove the quarantine flag", "path", p, "err", err)
			}
		}
	}()
}

// isNoSuchXattr reports xattr's complaint about files that never had the flag.
func isNoSuchXattr(out []byte) bool { return bytes.Contains(out, []byte("No such xattr")) }
