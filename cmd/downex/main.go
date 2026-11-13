// Command downex is the Downex command-line download manager.
//
// Most commands (add, ls, watch, pause, resume, rm, settings, daemon) are clients of
// downexd, the per-user daemon that owns the downloads (docs/DAEMON-SPEC.md §7); they
// start it when it is not running. "downex get" (and resume/rm --local) instead run
// the segmented engine (docs/ENGINE-SPEC.md) in this process on the per-user SQLite
// store, for scripting. Run "downex help" for usage.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// The first Ctrl+C (or SIGTERM) pauses the running download cleanly; once it has
	// arrived, signal handling is restored so that a second one terminates at once.
	// Even then no progress is lost beyond what was not yet flushed: the store never
	// claims bytes that are not on disk.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
