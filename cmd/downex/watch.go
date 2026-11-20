package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

func (a *app) watch(ctx context.Context, args []string) error {
	fs := a.flagSet("watch", "watch [id...]",
		"Show a live table of downexd's downloads (all of them, or the given ids): state,\n"+
			"progress, speed, connections and ETA, redrawn at most 4 times per second. Ctrl+C\n"+
			"stops watching; the downloads continue. When the output is not a terminal, a line\n"+
			"is printed whenever a download's row changes. downexd is started if needed.")
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	ids := make([]int64, 0, len(pos))
	for _, p := range pos {
		id, err := parseID("watch", []string{p})
		if err != nil {
			return err
		}
		ids = append(ids, int64(id))
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	tv := newTableView(a.stdout, nil)
	if size, restore, ok := a.sys.terminal(a.stdout); ok {
		defer restore()
		tv = newTableView(a.stdout, size)
	}
	return a.eventLoop(ctx, c, ids, func(evs []*downexv1.Event) bool {
		tv.apply(evs)
		return false
	}, tv.render)
}

// eventLoop streams the Watch events of ids (all downloads when empty). It calls
// apply for every batch as it arrives, and render right away for the first batch,
// then at most once per renderInterval while batches keep changing things, and once
// more at the end. It returns nil when apply reports done, ctx's error when ctx is
// done, and an error when the stream ends.
func (a *app) eventLoop(ctx context.Context, c *client.Client, ids []int64,
	apply func([]*downexv1.Event) (done bool), render func()) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.Downloads.Watch(sctx, connect.NewRequest(&downexv1.WatchRequest{Ids: ids}))
	if err != nil {
		return rpcError("watch", err)
	}

	// The receiver goroutine sends every batch before reporting the end of the stream.
	batches := make(chan []*downexv1.Event)
	ended := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		for stream.Receive() {
			select {
			case batches <- stream.Msg().GetEvents():
			case <-sctx.Done():
				return
			}
		}
		ended <- stream.Err()
	}()
	defer func() {
		cancel() // aborts a blocked Receive
		<-exited
		_ = stream.Close()
	}()

	var (
		last    time.Time        // when render last ran
		pending <-chan time.Time // a deferred render, while one is scheduled
		dirty   bool
	)
	draw := func() {
		if dirty {
			render()
			dirty, last, pending = false, time.Now(), nil
		}
	}
	for {
		select {
		case evs := <-batches:
			dirty = true
			if apply(evs) {
				draw()
				return nil
			}
			if pending == nil {
				if wait := renderInterval - time.Since(last); wait > 0 {
					pending = time.After(wait)
				} else {
					draw()
				}
			}
		case <-pending:
			draw()
		case err := <-ended:
			draw()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return rpcError("watch", err)
			}
			return errStreamClosed
		case <-ctx.Done():
			draw()
			return ctx.Err()
		}
	}
}

// tableView renders the watch table. On a terminal it redraws the whole table in
// place (ANSI cursor movement), fitted to the terminal's size; elsewhere it prints a
// line for each download whose row changed, so that a file or pipe gets a log.
type tableView struct {
	w    io.Writer
	size func() (width, height int) // nil when w is not a terminal
	rows map[int64]*downexv1.Download

	drawn   int              // terminal: lines of the frame on screen
	header  bool             // log: header printed
	printed map[int64]string // log: last row printed per download
	removed []int64          // log: removals not yet printed
}

func newTableView(w io.Writer, size func() (int, int)) *tableView {
	return &tableView{w: w, size: size, rows: make(map[int64]*downexv1.Download), printed: make(map[int64]string)}
}

// apply updates the table with a batch of events.
func (v *tableView) apply(evs []*downexv1.Event) {
	for _, ev := range evs {
		switch k := ev.GetKind().(type) {
		case *downexv1.Event_Snapshot:
			v.set(k.Snapshot)
		case *downexv1.Event_Updated:
			v.set(k.Updated)
		case *downexv1.Event_Removed:
			if _, ok := v.rows[k.Removed]; ok {
				delete(v.rows, k.Removed)
				if v.size == nil { // the log reports removals; a frame just drops the row
					v.removed = append(v.removed, k.Removed)
				}
			}
		}
	}
}

func (v *tableView) set(d *downexv1.Download) {
	if d != nil {
		v.rows[d.GetId()] = d
	}
}

// sorted returns the downloads in id order.
func (v *tableView) sorted() []*downexv1.Download {
	ds := make([]*downexv1.Download, 0, len(v.rows))
	for _, d := range v.rows {
		ds = append(ds, d)
	}
	slices.SortFunc(ds, func(x, y *downexv1.Download) int { return cmp.Compare(x.GetId(), y.GetId()) })
	return ds
}

// render draws the current table.
func (v *tableView) render() {
	if v.size == nil {
		v.renderLog()
		return
	}
	width, height := v.size()
	v.drawFrame(v.frame(width, height))
}

// renderLog prints the header once, then the rows that changed and the removals.
func (v *tableView) renderLog() {
	var b strings.Builder
	idw := idWidth(v.rows)
	if !v.header {
		b.WriteString(tableHeader(idw) + "\n")
		v.header = true
	}
	for _, id := range v.removed {
		fmt.Fprintf(&b, "%-*d  removed\n", idw, id)
		delete(v.printed, id)
	}
	v.removed = v.removed[:0]
	for _, d := range v.sorted() {
		line := tableRow(idw, d)
		if v.printed[d.GetId()] != line {
			b.WriteString(line + "\n")
			v.printed[d.GetId()] = line
		}
	}
	_, _ = io.WriteString(v.w, b.String())
}

// frame returns the lines of the table for a terminal of the given size: the header,
// as many rows as fit, and a summary line, each cut to width-1 columns so that no
// line wraps (wrapped lines would break the redraw).
func (v *tableView) frame(width, height int) []string {
	ds := v.sorted()
	idw := idWidth(v.rows)
	lines := []string{tableHeader(idw)}
	room := max(height-3, 1) // header, summary, and the cursor's line
	shown := ds
	if len(ds) > room {
		shown = ds[:max(room-1, 0)]
	}
	for _, d := range shown {
		lines = append(lines, tableRow(idw, d))
	}
	if more := len(ds) - len(shown); more > 0 {
		lines = append(lines, fmt.Sprintf("... and %d more", more))
	}
	lines = append(lines, summaryLine(ds))
	for i, l := range lines {
		lines[i] = truncate(l, max(width-1, 10))
	}
	return lines
}

// drawFrame replaces the frame on screen with lines: it moves the cursor back to the
// frame's first line, rewrites each line, and clears what is left below.
func (v *tableView) drawFrame(lines []string) {
	var b strings.Builder
	if v.drawn > 0 {
		fmt.Fprintf(&b, "\r\x1b[%dA", v.drawn)
	}
	for _, l := range lines {
		b.WriteString("\x1b[2K")
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString("\x1b[J")
	_, _ = io.WriteString(v.w, b.String())
	v.drawn = len(lines)
}

// idWidth is the width of the ID column.
func idWidth(rows map[int64]*downexv1.Download) int {
	w := 4
	for id := range rows {
		w = max(w, len(strconv.FormatInt(id, 10)))
	}
	return w
}

const rowFormat = "%-*s  %-11s  %8s  %12s  %5s  %6s  %s"

func tableHeader(idw int) string {
	return fmt.Sprintf(rowFormat, idw, "ID", "STATE", "PROGRESS", "SPEED", "CONNS", "ETA", "NAME")
}

// rpcSize is d's size when it is known: -1 for an unknown size and for downexd's
// estimate of a stream that is not completed (size_approx), which neither measures
// progress (the stream's fragments do) nor is shown as its size.
func rpcSize(d *downexv1.Download) int64 {
	if d.GetSizeApprox() {
		return -1
	}
	return d.GetSize()
}

// approxETA is the time left of a download whose size is an estimate (size_approx), as
// "~8m28s"; "" when there is no estimate, or it is no more than what was received.
func approxETA(d *downexv1.Download) string {
	if !d.GetSizeApprox() || d.GetSize() <= d.GetReceived() {
		return ""
	}
	if eta := formatETA(d.GetSize()-d.GetReceived(), d.GetSpeedBps()); eta != "--" {
		return "~" + eta
	}
	return ""
}

// tableRow formats one download: speed, connections and ETA only while it downloads.
func tableRow(idw int, d *downexv1.Download) string {
	state := rpcStateName(d.GetState())
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_QUEUED && d.GetQueuePosition() > 0 {
		state += " #" + strconv.Itoa(int(d.GetQueuePosition()))
	}
	progress := "-"
	m := d.GetMedia()
	size := rpcSize(d)
	switch {
	case size >= 0:
		progress = fmt.Sprintf("%.1f%%", percent(d.GetReceived(), size))
	case m != nil && m.GetFragmentsTotal() > 0:
		// A stream's size is known once it is joined: count its fragments.
		progress = fmt.Sprintf("%.1f%%", percent(int64(m.GetFragmentsDone()), int64(m.GetFragmentsTotal())))
	case d.GetReceived() > 0:
		progress = formatBytes(d.GetReceived())
	}
	speed, conns, eta := "-", "-", "-"
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING {
		speed = formatBytes(int64(d.GetSpeedBps())) + "/s"
		conns = strconv.Itoa(int(d.GetConnections()))
		switch {
		case size >= 0:
			eta = formatETA(size-d.GetReceived(), d.GetSpeedBps())
		case approxETA(d) != "":
			eta = approxETA(d)
		default:
			eta = "--"
		}
	}
	name := d.GetFilename()
	if name == "" {
		name = d.GetUrl()
	}
	if e := d.GetError(); e != "" {
		name += ": " + e
	}
	name = printable(name) // the error may quote a server's certificate names
	return fmt.Sprintf(rowFormat, idw, strconv.FormatInt(d.GetId(), 10), state, progress, speed, conns, eta, name)
}

// summaryLine counts the downloads and adds up their speed.
func summaryLine(ds []*downexv1.Download) string {
	var active, queued int
	var speed float64
	for _, d := range ds {
		switch d.GetState() {
		case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
			active++
			speed += d.GetSpeedBps()
		case downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
			queued++
		}
	}
	noun := "downloads"
	if len(ds) == 1 {
		noun = "download"
	}
	return fmt.Sprintf("%d %s, %d active, %d queued, %s/s total  (Ctrl+C to stop watching)",
		len(ds), noun, active, queued, formatBytes(int64(speed)))
}

// truncate cuts s to at most n runes, marking the cut with "~".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "~"
}

// follow shows download d's progress on the console until it completes, fails, or is
// paused or removed elsewhere. Ctrl+C (ctx) stops following only: downexd keeps
// downloading.
func (a *app) follow(ctx context.Context, c *client.Client, t *daemonTarget, d *downexv1.Download) error {
	f := &follower{con: a.con, id: d.GetId(), named: d.GetFilename() != ""}
	err := a.eventLoop(ctx, c, []int64{f.id}, f.apply, f.render)
	a.con.keep()
	id := idArg(f.id)
	switch {
	case interrupted(ctx, err):
		a.con.printf("Stopped watching; download %d continues in the background.\nWatch it with: %s\n",
			f.id, commandLine(t, "watch", id))
		return err
	case err != nil:
		return err
	case f.removed:
		return fmt.Errorf("download %d was removed", f.id)
	}
	last := f.last
	switch last.GetState() {
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		a.con.printf("Saved %s (%s)\n", rpcPath(last), formatBytes(last.GetSize()))
		return nil
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		a.con.printf("Download %d was paused.\nResume with: %s\n", f.id, commandLine(t, "resume", id))
		return silentError{fmt.Errorf("download %d was paused", f.id)}
	case downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return fmt.Errorf("download %d was canceled", f.id)
	}
	msg := last.GetError()
	if msg == "" {
		msg = "unknown error"
	}
	a.con.printf("downex: error: download %d failed: %s\n", f.id, msg)
	if m := last.GetMedia(); m != nil {
		switch last.GetErrorCode() {
		case "encrypted":
			a.con.printf("The stream is encrypted or DRM-protected; Downex does not download protected content.\n")
			return silentError{errors.New(msg)}
		case "live":
			a.con.printf("This is a live stream; only complete (video-on-demand) streams can be downloaded.\n")
			return silentError{errors.New(msg)}
		case "media_unsupported":
			a.con.printf("Downex cannot save these tracks as one MP4 (their codecs or container cannot be converted);\n" +
				"try other tracks (downex media --list).\n")
			return silentError{errors.New(msg)}
		}
		if last.GetErrorCode() == "site_error" && strings.Contains(msg, sitemedia.ReasonForbidden) {
			// The site's video server refused even the renewed addresses
			// (docs/SITE-MEDIA-SPEC.md §2.6): trying again rarely helps, and only when
			// progress is kept.
			a.con.printf("The site's video server refused Downex (often bot protection, which Downex does not get past).\n")
			if !(directSite(m) && last.GetReceived() > 0 || m.GetFragmentsDone() > 0) {
				return silentError{errors.New(msg)}
			}
		}
		// Measured as ls, info, pause and the progress line measure it: a web page's direct
		// video by its bytes of an exact size, else by its parts or fragments.
		switch size := rpcSize(last); {
		case directSite(m) && size > 0 && last.GetReceived() > 0:
			a.con.printf("Its progress (%s) is kept.\n", describeSaved(size, last.GetReceived()))
		case m.GetFragmentsDone() > 0:
			a.con.printf("Its progress (%s) is kept.\n", pieceProgress(m))
		}
		a.con.printf("Try again with: %s\n", commandLine(t, "resume", id))
		return silentError{errors.New(msg)}
	}
	if last.GetErrorCode() == codeLinkExpired {
		a.con.printf("The link stopped working (it expired, was revoked or leads to a web page).")
		if last.GetReceived() > 0 {
			a.con.printf(" Its progress (%s) is kept.", keptText(rpcSize(last), last.GetReceived()))
		}
		a.con.printf("\nGive it a new link with:\n  %s\n", commandLine(t, "renew", id, "<new-url>"))
		if p := last.GetPageUrl(); p != "" {
			a.con.printf("The page it came from: %s\n", printable(p))
		}
		return silentError{errors.New(msg)}
	}
	if last.GetErrorCode() == "validator_changed" {
		// The code also covers saved progress that does not match the part file.
		if strings.Contains(msg, engine.ErrCorruptCheckpoint.Error()) {
			a.con.printf("The saved progress does not match the partial file on disk, so it cannot be\n" +
				"reused. ")
		} else {
			a.con.printf("The file on the server changed since this download started, so the data saved\n" +
				"so far cannot be reused. ")
		}
		a.con.printf("To discard it and download the file again, run:\n  %s\n", commandLine(t, "resume", id, "--restart"))
	} else {
		if last.GetReceived() > 0 && rpcMode(last.GetMode()) == model.ModeRanged {
			a.con.printf("Its progress (%s) is kept.\n", describeSaved(rpcSize(last), last.GetReceived()))
		}
		a.con.printf("Try again with: %s\n", commandLine(t, "resume", id))
	}
	return silentError{errors.New(msg)}
}

// follower tracks one download for follow.
type follower struct {
	con     *console
	id      int64
	last    *downexv1.Download
	named   bool // the target path was printed
	removed bool
}

// apply takes a batch of events and reports whether the download stopped.
func (f *follower) apply(evs []*downexv1.Event) bool {
	for _, ev := range evs {
		var d *downexv1.Download
		switch k := ev.GetKind().(type) {
		case *downexv1.Event_Snapshot:
			d = k.Snapshot
		case *downexv1.Event_Updated:
			d = k.Updated
		case *downexv1.Event_Removed:
			if k.Removed == f.id {
				f.removed = true
				return true
			}
		}
		if d == nil || d.GetId() != f.id {
			continue
		}
		f.last = d
		if stopped(d.GetState()) {
			return true
		}
	}
	return false
}

// stopped reports whether a download in state s no longer makes progress by itself.
func stopped(s downexv1.DownloadState) bool {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED, downexv1.DownloadState_DOWNLOAD_STATE_FAILED,
		downexv1.DownloadState_DOWNLOAD_STATE_CANCELED, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return true
	}
	return false
}

// render shows the latest state on the progress line, announcing the target path
// once the daemon has chosen the file name.
func (f *follower) render() {
	d := f.last
	if d == nil {
		return
	}
	if !f.named && d.GetFilename() != "" {
		f.named = true
		f.con.printf("Saving to %s\n", filepath.Join(d.GetDir(), d.GetFilename()))
	}
	f.con.status(followLine(d))
}

// followLine formats the progress line of a followed download.
func followLine(d *downexv1.Download) string {
	switch d.GetState() {
	case downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
		if p := d.GetQueuePosition(); p > 0 {
			return fmt.Sprintf("Queued (position %d)", p)
		}
		return "Queued"
	case downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return "Connecting..."
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		if m := d.GetMedia(); m != nil && rpcSize(d) < 0 {
			return mediaProgressLine(d)
		}
		return progressLine(engine.Progress{
			Size: d.GetSize(), Received: d.GetReceived(), SpeedBps: d.GetSpeedBps(), Connections: int(d.GetConnections()),
		})
	}
	return rpcStateName(d.GetState())
}

// liveTerminal reports whether w is a terminal on which the watch table can be
// redrawn in place, enabling escape-sequence processing where that is needed.
func liveTerminal(w io.Writer) (size func() (int, int), restore func(), ok bool) {
	f, isFile := w.(*os.File)
	if !isFile || !isTerminal(f) || os.Getenv("TERM") == "dumb" {
		return nil, nil, false
	}
	if _, _, ok := termSize(f); !ok { // e.g. the null device, which is a character device too
		return nil, nil, false
	}
	restore, err := enableVT(f)
	if err != nil {
		return nil, nil, false
	}
	return func() (int, int) {
		if w, h, ok := termSize(f); ok {
			return w, h
		}
		return 80, 24
	}, restore, true
}
