package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"downex/internal/dlock"
	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/limiter"
	"downex/internal/model"
	"downex/internal/store"
)

func (a *app) get(ctx context.Context, args []string) (err error) {
	fs := a.flagSet("get", "get <url> [flags]",
		"Download url into a directory with up to -c connections in this process, without\n"+
			"downexd, then exit. Ctrl+C pauses the download and saves its progress; continue it\n"+
			"with \"downex resume <id> --local\" (or hand it to downexd with \"downex resume <id>\").")
	db := a.dbFlag(fs)
	var (
		netf     netFlags
		headers  headerFlag
		dir      string
		conns    int
		limitArg string
		name     string
		restart  bool
		queue    string
	)
	netf.register(fs)
	fs.StringVar(&dir, "o", ".", "save into `dir` (created if needed)")
	fs.IntVar(&conns, "c", 0, fmt.Sprintf("maximum `connections` (1-%d; 0 = default %d)",
		engine.HardMaxConnections, engine.DefaultMaxConnections))
	fs.StringVar(&limitArg, "limit", "0", "speed limit as a `rate` in bytes per second, e.g. 500K = 500 KB/s or 1.5M = 1.5 MB/s (0 = unlimited)")
	fs.Var(&headers, "H", "extra request header \"Name: value\" (repeatable), e.g. Cookie or Referer")
	fs.StringVar(&name, "name", "", "save as `file` instead of the name suggested by the server or URL")
	fs.BoolVar(&restart, "restart", false, "if the remote file changes during the download, start over instead of failing")
	fs.StringVar(&queue, "queue", "", "not for get: queues belong to downexd (use \"downex add --queue `queue` --wait\")")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if visited(fs)["queue"] {
		return usageError{"get", fmt.Sprintf("--queue applies to downexd's queues, and get runs the download in this process without "+
			"downexd; use %q to download into that queue and follow it", "downex add --queue "+shellQuote(queue)+" --wait <url>")}
	}
	if len(pos) != 1 {
		return usageError{"get", "expects exactly one URL"}
	}
	if err := checkConnections("get", conns); err != nil {
		return err
	}
	limit, err := parseRate(limitArg)
	if err != nil {
		return usageError{"get", "--limit: " + err.Error()}
	}

	st, dbPath, err := openStore(*db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()
	eng, err := a.newEngine(st, netf)
	if err != nil {
		return err
	}
	d, err := eng.Add(ctx, model.Request{URL: pos[0], Headers: headers.h}, dir, name, conns, limit)
	if err != nil {
		return err
	}
	d.Source = model.SourceCLI
	if err := st.UpdateDownload(context.WithoutCancel(ctx), d); err != nil {
		return fmt.Errorf("save download %d: %w", d.ID, err)
	}
	a.con.printf("Download %d: %s\n", d.ID, d.Request.URL)
	return a.download(ctx, st, eng, d.ID, runOptions{restart: restart, dbPath: dbPath, dbFlag: *db, added: true})
}

// resumeLocal runs download id in this process until it completes, is paused by ctx
// (Ctrl+C) or fails: "resume --local".
func (a *app) resumeLocal(ctx context.Context, id model.DownloadID, netf netFlags, o runOptions) (err error) {
	st, dbPath, err := openStore(o.dbFlag)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()
	o.dbPath = dbPath
	eng, err := a.newEngine(st, netf)
	if err != nil {
		return err
	}
	return a.download(ctx, st, eng, id, o)
}

// runOptions configures one download run.
type runOptions struct {
	restart bool // start over when the remote file changed or progress is unusable
	// again ("resume --restart"): a completed download is downloaded again, its new copy
	// under a free name, or with replace in place of the file once it is complete
	// (docs/ENGINE-SPEC.md §7.1).
	again, replace bool
	dbPath         string // absolute database path (lock directory)
	dbFlag         string // the --db value, repeated in printed commands
	added          bool   // the download was just added by "get"
	conns          *int   // new stored connection limit, if set
	limit          *int64 // new stored speed limit, if set
}

// download runs id under its cross-process lock with live progress until it
// completes, is paused by ctx, or fails.
func (a *app) download(ctx context.Context, st store.Store, eng *engine.Engine, id model.DownloadID, o runOptions) error {
	lock, err := dlock.Acquire(dlock.Dir(o.dbPath), id)
	if err != nil {
		return err
	}
	defer lock.Release()

	d, err := st.GetDownload(ctx, id)
	if err != nil {
		return lookupError(id, err)
	}
	if d.State == model.StateCompleted && !o.again {
		fmt.Fprintf(a.stdout, "Download %d is already complete: %s\n", id, printable(filepath.Join(d.Dir, d.Filename)))
		return nil
	}
	// The refusals come before anything changes the record or the folder.
	if d.State == model.StateAwaitingConfirmation {
		return fmt.Errorf("download %d awaits confirmation of its folder and name; confirm it with "+
			"\"downex confirm %d\" (downexd runs it) or remove it with \"downex rm %d\"", id, id, id)
	}
	if d.Kind != model.KindFile {
		// The engine fetches single files; streams are downloaded and joined by downexd.
		return fmt.Errorf("download %d is a streaming-media download, which only downexd runs; use \"downex resume %d\" without --local", id, id)
	}
	again := false
	if d.State == model.StateCompleted {
		if err := eng.Restart(ctx, id, engine.RestartOptions{Completed: true, Replace: o.replace}); err != nil {
			return fmt.Errorf("download %d again: %w", id, err)
		}
		if d, err = st.GetDownload(ctx, id); err != nil {
			return lookupError(id, err)
		}
		again = true
		a.con.printf("Downloading %d again: %s\n", id, printable(filepath.Join(d.Dir, d.Filename)))
		if d.ReplaceFile {
			a.con.printf("The new copy replaces the file once it is complete; until then the file stays as it is.\n")
		}
	}
	if o.conns != nil || o.limit != nil {
		if o.conns != nil {
			d.MaxConnections = *o.conns
		}
		if o.limit != nil {
			d.SpeedLimit = *o.limit
		}
		d.UpdatedAt = time.Now()
		if err := st.UpdateDownload(ctx, d); err != nil {
			return fmt.Errorf("update download %d: %w", id, err)
		}
	}
	segs, err := st.LoadSegments(ctx, id)
	if err != nil {
		return fmt.Errorf("load download %d: %w", id, err)
	}
	saved := dlview.Received(d, segs)
	named := d.Filename != ""
	if !o.added && !again {
		a.con.printf("Resuming download %d: %s\n", id, describeTarget(d, saved))
		if d.Mode == model.ModeSingle && saved > 0 {
			a.con.printf("note: the server does not support resuming, so download %d starts over from the beginning\n", id)
		}
	} else if named {
		a.con.printf("Saving to %s\n", filepath.Join(d.Dir, d.Filename))
	}

	view := newProgressView(a.con, st, id, named)
	start := time.Now()
	restarted, err := a.runEngine(ctx, eng, id, limiter.New(d.SpeedLimit), view.update, o.restart)
	view.stop()
	if restarted {
		saved = 0
	}
	return a.report(ctx, st, id, o, err, start, saved)
}

// runEngine runs id once. With restart, a download whose remote file changed (or
// whose saved progress is unusable) is reset and run once more from the beginning;
// restarted reports whether that happened.
func (a *app) runEngine(ctx context.Context, eng *engine.Engine, id model.DownloadID, lim limiter.Limiter,
	onProgress func(engine.Progress), restart bool) (restarted bool, err error) {
	err = eng.Run(ctx, id, lim, onProgress)
	if !restart || !needsRestart(err) || ctx.Err() != nil {
		return false, err
	}
	a.con.printf("warning: %s; downloading %d again from the beginning\n", restartReason(err), id)
	if err := eng.Restart(ctx, id, engine.RestartOptions{}); err != nil {
		return false, fmt.Errorf("restart download %d: %w", id, err)
	}
	return true, eng.Run(ctx, id, lim, onProgress)
}

// report prints the outcome of a run and returns the command's error.
func (a *app) report(ctx context.Context, st store.Store, id model.DownloadID, o runOptions, runErr error,
	start time.Time, savedBefore int64) error {
	sctx := context.WithoutCancel(ctx)
	d, segs, loadErr := loadDownload(sctx, st, id)
	switch {
	case runErr == nil:
		if loadErr != nil {
			return loadErr
		}
		elapsed := time.Since(start)
		fetched := max(d.Size-savedBefore, 0)
		speed := int64(float64(fetched) / max(elapsed.Seconds(), 1e-3))
		fmt.Fprintf(a.stdout, "Saved %s (%s in %s, %s/s)\n",
			printable(filepath.Join(d.Dir, d.Filename)), formatBytes(d.Size), formatDuration(elapsed), formatBytes(speed))
		return nil

	case interrupted(ctx, runErr):
		if runErr != ctx.Err() { //nolint:errorlint // Run returns ctx.Err() itself after a clean pause
			// Pausing itself failed (for example the final checkpoint): say so.
			a.con.printf("warning: %v\n", runErr)
		}
		at := ""
		if loadErr == nil {
			at = " with " + describeSaved(d.Size, dlview.Received(d, segs))
		}
		a.con.printf("Paused download %d%s.\nResume with: %s\n", id, at, resumeCommand(id, o.dbFlag, "--local"))
		return runErr

	case needsRestart(runErr):
		a.con.printf("downex: error: download %d cannot be resumed: %v\n", id, runErr)
		if errors.Is(runErr, engine.ErrValidatorChanged) {
			a.con.printf("The file on the server changed since this download started (its ETag,\n" +
				"Last-Modified date or size no longer match), so the data saved so far cannot be reused.\n")
		} else {
			a.con.printf("The saved progress does not match the partial file on disk, so it cannot be\n" +
				"reused safely.\n")
		}
		if o.restart {
			a.con.printf("It changed again while downloading; try again later.\n")
		} else {
			a.con.printf("To discard the saved data and download the file again from the beginning, run:\n  %s\n",
				resumeCommand(id, o.dbFlag, "--restart", "--local"))
		}
		return silentError{runErr}

	default:
		a.con.printf("downex: error: download %d failed: %v\n", id, runErr)
		if loadErr == nil && d.Mode == model.ModeRanged && dlview.Received(d, segs) > 0 {
			a.con.printf("Its progress (%s) is kept.\n", describeSaved(d.Size, dlview.Received(d, segs)))
		}
		a.con.printf("Try again with: %s\n", resumeCommand(id, o.dbFlag, "--local"))
		return silentError{runErr}
	}
}

func needsRestart(err error) bool {
	return errors.Is(err, engine.ErrValidatorChanged) || errors.Is(err, engine.ErrCorruptCheckpoint)
}

func restartReason(err error) string {
	if errors.Is(err, engine.ErrValidatorChanged) {
		return "the file on the server changed"
	}
	return "the saved progress cannot be used"
}

// loadDownload reads a download record and its segments.
func loadDownload(ctx context.Context, st store.Store, id model.DownloadID) (*model.Download, []model.Segment, error) {
	d, err := st.GetDownload(ctx, id)
	if err != nil {
		return nil, nil, lookupError(id, err)
	}
	segs, err := st.LoadSegments(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load download %d: %w", id, err)
	}
	return d, segs, nil
}

func lookupError(id model.DownloadID, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no download with id %d (see \"downex ls\")", id)
	}
	return fmt.Errorf("load download %d: %w", id, err)
}

// describeSaved formats saved bytes, with the total size and percentage when the
// size is known (not negative).
func describeSaved(size, saved int64) string {
	if size < 0 {
		return formatBytes(saved) + " saved"
	}
	return fmt.Sprintf("%s of %s (%.1f%%) saved", formatBytes(saved), formatBytes(size), percent(saved, size))
}

// describeTarget names a download's file, or its URL before a name was chosen.
func describeTarget(d *model.Download, saved int64) string {
	target := d.Request.URL
	if d.Filename != "" {
		target = filepath.Join(d.Dir, d.Filename)
	}
	if saved > 0 {
		target += " (" + describeSaved(d.Size, saved) + ")"
	}
	return target
}

// resumeCommand returns the command line that resumes id, repeating --db if it was set.
func resumeCommand(id model.DownloadID, dbFlag string, extra ...string) string {
	parts := append([]string{"downex", "resume", strconv.FormatInt(int64(id), 10)}, extra...)
	if dbFlag != "" {
		if abs, err := filepath.Abs(dbFlag); err == nil {
			dbFlag = abs
		}
		parts = append(parts, "--db", shellQuote(dbFlag))
	}
	return strings.Join(parts, " ")
}

// shellQuote double-quotes s when a shell would otherwise split or expand it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'`$&|;<>()*?[]{}!#~%") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func closeStore(st store.Store) error {
	if err := st.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}
