package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	"downex/internal/dlock"
	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

func (a *app) ls(ctx context.Context, args []string) error {
	flags := a.flagSet("ls", "ls [--json]",
		"List all downloads. While downexd is running its live list is shown; otherwise the\n"+
			"download database is read directly (downexd is not started).")
	t := a.daemonFlags(flags)
	asJSON := flags.Bool("json", false, "print a JSON array instead of a table")
	pos, err := parseArgs(flags, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"ls", "takes no arguments"}
	}
	views, ok, err := a.listDaemon(ctx, t)
	if err != nil {
		return err
	}
	if !ok {
		if views, err = listStore(ctx, t.db); err != nil {
			return err
		}
	}
	if *asJSON {
		return writeJSON(a.stdout, views)
	}
	return writeList(a.stdout, views)
}

// listStore reads every download from the database at db (or the default one).
func listStore(ctx context.Context, db string) (views []downloadView, err error) {
	st, _, err := openStore(db)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()

	list, err := st.ListDownloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	views = make([]downloadView, 0, len(list))
	for i := range list {
		segs, err := st.LoadSegments(ctx, list[i].ID)
		if err != nil {
			return nil, fmt.Errorf("load download %d: %w", list[i].ID, err)
		}
		views = append(views, newDownloadView(&list[i], segs))
	}
	return views, nil
}

// writeList prints downloads as a table.
func writeList(w io.Writer, views []downloadView) error {
	if len(views) == 0 {
		fmt.Fprintln(w, "No downloads.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tPROGRESS\tSIZE\tNAME")
	for _, v := range views {
		progress := "-"
		if pct, ok := v.progressPercent(); ok {
			progress = fmt.Sprintf("%.1f%%", pct)
		}
		name := v.Filename
		if name == "" {
			name = v.URL
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", v.ID, v.State, progress, v.sizeText(), printable(name))
	}
	return tw.Flush()
}

// progressPercent is v's progress as ls and info show it: its bytes of an exact size,
// else the fragments (parts) of a media download whose size is an estimate or unknown;
// ok is false when neither is known.
func (v downloadView) progressPercent() (pct float64, ok bool) {
	switch {
	case v.Size >= 0 && !v.SizeApprox:
		return percent(v.Downloaded, v.Size), true
	case v.Media != nil && v.Media.FragmentsTotal > 0:
		return percent(int64(v.Media.FragmentsDone), int64(v.Media.FragmentsTotal)), true
	}
	return 0, false
}

// sizeText is v's size as ls and info show it: "?" when unknown, "~" before an estimate.
func (v downloadView) sizeText() string {
	if v.SizeApprox && v.Size > 0 {
		return "~" + formatBytes(v.Size)
	}
	return formatBytes(v.Size)
}

func (a *app) info(ctx context.Context, args []string) (err error) {
	flags := a.flagSet("info", "info <id> [--json]",
		"Show one download and its segments, as stored in the download database. While downexd\n"+
			"runs the download, its live connections are shown too (downexd is not started).")
	t := a.daemonFlags(flags)
	asJSON := flags.Bool("json", false, "print JSON instead of text")
	pos, err := parseArgs(flags, args)
	if err != nil {
		return silentError{err}
	}
	id, err := parseID("info", pos)
	if err != nil {
		return err
	}
	st, _, err := openStore(t.db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()

	d, segs, err := loadDownload(ctx, st, id)
	if err != nil {
		return err
	}
	v := infoView{downloadView: newDownloadView(d, segs), Segments: segs}
	live, err := a.liveDownload(ctx, t, id)
	if err != nil {
		return err
	}
	if live != nil {
		v.SpeedBps, v.Connections = live.GetSpeedBps(), int(live.GetConnections())
		v.ConnectionInfo = newConnectionViews(live.GetConnectionInfo())
		// The running total includes the current run, which the database does not hold
		// yet, and so does a completion_dialog mark set during the run.
		v.ActiveMS = max(v.ActiveMS, live.GetActiveMs())
		v.liveReceived = live.GetReceived()
		v.CompletionDialog = live.GetCompletionDialog()
		if v.StartedAt == nil && live.GetStartedAt() != nil {
			started := live.GetStartedAt().AsTime()
			v.StartedAt = &started
		}
	}
	if *asJSON {
		return writeJSON(a.stdout, v)
	}
	return writeInfo(a.stdout, d, v)
}

// liveDownload returns download id as a running downexd reports it while it runs there;
// nil when no downexd is running, it does not know the download (another database) or
// the download is not running. A failing downexd is only reported as a warning.
func (a *app) liveDownload(ctx context.Context, t *daemonTarget, id model.DownloadID) (*downexv1.Download, error) {
	c, err := a.connectIfRunning(ctx, t)
	if err == nil && c != nil {
		cctx, cancel := rpcContext(ctx)
		defer cancel()
		var resp *connect.Response[downexv1.GetResponse]
		resp, err = c.Downloads.Get(cctx, connect.NewRequest(&downexv1.GetRequest{Id: int64(id)}))
		if err == nil {
			switch d := resp.Msg.GetDownload(); d.GetState() {
			case downexv1.DownloadState_DOWNLOAD_STATE_PROBING, downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:
				return d, nil
			}
			return nil, nil
		}
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil, nil
		}
		err = rpcError("info", err)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		a.con.printf("warning: %v; showing the stored state only\n", err)
	}
	return nil, nil
}

// rmLocal removes download id from the database at db directly: "rm --local".
func (a *app) rmLocal(ctx context.Context, id model.DownloadID, db string, deleteFile bool) (err error) {
	st, dbPath, err := openStore(db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()

	lock, err := dlock.Acquire(dlock.Dir(dbPath), id)
	if err != nil {
		return err
	}
	defer lock.Release()
	d, err := st.GetDownload(ctx, id)
	if err != nil {
		return lookupError(id, err)
	}
	path := filePath(d)
	if deleteFile && path != "" {
		// Delete the file first: if that fails the record stays, so rm can be retried.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete %s (download %d was not removed): %w", path, id, err)
		}
		if d.Kind == model.KindMedia {
			// The fragments of a media download (daemon.MediaPartsDir).
			parts := filepath.Join(d.Dir, d.Filename+mediaPartsExt)
			if err := os.RemoveAll(parts); err != nil {
				return fmt.Errorf("delete %s (download %d was not removed): %w", parts, id, err)
			}
		}
	}
	if err := st.DeleteDownload(ctx, id); err != nil {
		return fmt.Errorf("remove download %d: %w", id, err)
	}
	lock.Discard()
	a.printRemoved(id, path, deleteFile)
	return nil
}

// filePath is the download's file on disk (see downloadPath); "" while it awaits
// confirmation (its name is only proposed, no file exists).
func filePath(d *model.Download) string {
	if d.State == model.StateAwaitingConfirmation {
		return ""
	}
	return downloadPath(d.Dir, d.Filename, d.State == model.StateCompleted)
}

// downloadPath is a download's file on disk: the final file once completed, else the
// partial file, or "" before a filename was chosen.
func downloadPath(dir, filename string, completed bool) string {
	switch {
	case filename == "":
		return ""
	case completed:
		return filepath.Join(dir, filename)
	}
	return engine.PartPath(dir, filename)
}

// downloadView is the JSON form of a download in "ls --json" and "info --json". The
// request (method, headers, body) and validators are known only from the database;
// the live fields only from a running downexd.
type downloadView struct {
	ID             model.DownloadID `json:"id"`
	URL            string           `json:"url"`
	FinalURL       string           `json:"finalUrl,omitempty"`
	Method         string           `json:"method,omitempty"`
	Headers        http.Header      `json:"headers,omitempty"` // credential values are redacted
	BodyBytes      int              `json:"bodyBytes,omitempty"`
	Dir            string           `json:"dir"`
	Filename       string           `json:"filename,omitempty"`
	Path           string           `json:"path,omitempty"` // final file, or the partial file while unfinished
	State          string           `json:"state"`
	Mode           string           `json:"mode"`
	Size           int64            `json:"size"`                 // -1 when unknown
	SizeApprox     bool             `json:"sizeApprox,omitempty"` // Size is an estimate (a stream not completed, or a video of estimated size)
	Downloaded     int64            `json:"downloaded"`           // bytes saved to disk
	MaxConnections int              `json:"maxConnections"`
	SpeedLimit     int64            `json:"speedLimit"` // bytes per second, 0 = unlimited
	Validators     model.Validators `json:"validators,omitzero"`
	Error          string           `json:"error,omitempty"`
	ErrorCode      string           `json:"errorCode,omitempty"` // stable machine code from downexd, e.g. "http_status"
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	CompletedAt    *time.Time       `json:"completedAt,omitempty"`
	SpeedBps       float64          `json:"speedBps,omitempty"`      // live: bytes per second
	Connections    int              `json:"connections,omitempty"`   // live: open connections
	QueuePosition  int              `json:"queuePosition,omitempty"` // 1-based position among queued downloads
	QueueID        int64            `json:"queueId,omitempty"`       // the queue it belongs to (1 = main), from downexd
	Source         string           `json:"source,omitempty"`        // who added it: cli, ui or browser
	Category       string           `json:"category,omitempty"`      // category chosen when it was added
	ExpectedSize   int64            `json:"expectedSize,omitempty"`  // size hint of the requester
	MIME           string           `json:"mime,omitempty"`          // MIME hint of the requester
	Kind           string           `json:"kind"`                    // "file" or "media" (an HLS/DASH stream)
	Media          *mediaView       `json:"media,omitempty"`         // the stream of a media download
	// The download complete dialog (docs/DESKTOP-SPEC.md §8): when it first started, how
	// long its runs took altogether (pauses excluded, the current run included while
	// downexd runs it), and whether its end opens the dialog.
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	ActiveMS         int64      `json:"activeMs,omitempty"`
	CompletionDialog bool       `json:"completionDialog,omitempty"`
}

// infoView adds the stored segments for "info --json", and the live connections while
// downexd runs the download.
type infoView struct {
	downloadView
	Segments       []model.Segment  `json:"segments"`
	ConnectionInfo []connectionView `json:"connectionInfo,omitempty"`
	// liveReceived is what a running downexd has received of the download (its running
	// total's bytes); 0 without one.
	liveReceived int64
}

// newDownloadView is stored download d as downexd shows it while it is stopped: its size,
// estimate and bytes on disk by the rules of dlview.
func newDownloadView(d *model.Download, segs []model.Segment) downloadView {
	size, approx := dlview.Size(d)
	v := downloadView{
		ID: d.ID, URL: d.Request.URL, FinalURL: d.FinalURL, Method: d.Request.Method,
		BodyBytes: len(d.Request.Body), Dir: d.Dir, Filename: d.Filename, Path: filePath(d),
		State: stateName(d.State), Mode: d.Mode.String(), Size: size, SizeApprox: approx, Downloaded: dlview.Received(d, segs),
		MaxConnections: d.MaxConnections, SpeedLimit: d.SpeedLimit, Validators: d.Validators,
		Error: d.Error, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		Category: d.Category, ExpectedSize: d.ExpectedSize, MIME: d.MIME, Kind: d.Kind.String(),
		ActiveMS: max(d.ActiveMS, 0), CompletionDialog: d.CompletionDialog,
	}
	if !d.StartedAt.IsZero() {
		t := d.StartedAt
		v.StartedAt = &t
	}
	if d.Kind == model.KindMedia {
		v.Media = storedMediaView(d)
	}
	if d.Source != model.SourceUnknown {
		v.Source = d.Source.String()
	}
	if len(d.Request.Headers) > 0 {
		v.Headers = d.Request.Headers.Clone()
		// These headers carry credentials; their values are never printed.
		for _, k := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
			if len(v.Headers.Values(k)) > 0 {
				v.Headers[http.CanonicalHeaderKey(k)] = []string{"[redacted]"}
			}
		}
	}
	if !d.CompletedAt.IsZero() {
		t := d.CompletedAt
		v.CompletedAt = &t
	}
	return v
}

// stateName is State.String that tolerates values this build does not know.
func stateName(s model.State) string {
	if s < model.StateQueued || s > model.StateLast {
		return fmt.Sprintf("state(%d)", int(s))
	}
	return s.String()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeInfo prints a download and its segments for people.
func writeInfo(w io.Writer, d *model.Download, v infoView) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, format string, args ...any) {
		fmt.Fprintf(tw, "%s:\t"+format+"\n", append([]any{k}, printableArgs(args)...)...)
	}
	row("ID", "%d", v.ID)
	row("URL", "%s", v.URL)
	if v.FinalURL != "" && v.FinalURL != v.URL {
		row("Final URL", "%s", v.FinalURL)
	}
	if v.Method != "" && v.Method != http.MethodGet {
		row("Method", "%s (%d-byte body)", v.Method, v.BodyBytes)
	}
	for _, k := range sortedKeys(v.Headers) {
		for _, val := range v.Headers[k] {
			row("Header", "%s: %s", k, val)
		}
	}
	if v.Path != "" {
		row("File", "%s", v.Path)
	} else {
		row("Directory", "%s", v.Dir)
	}
	row("State", "%s", v.State)
	if v.Error != "" {
		row("Error", "%s", v.Error)
	}
	if m := v.Media; m != nil {
		if m.Origin == originSite {
			// A web page's video (docs/SITE-MEDIA-SPEC.md §3.7); its address is the page.
			row("Kind", "video page (%s)", cmp.Or(m.Site, "web page"))
		} else {
			row("Kind", "media (%s stream)", strings.ToUpper(m.Format))
		}
		if m.Title != "" {
			row("Title", "%s", m.Title)
		}
		row("Tracks", "%s", describeTrackIDs(m))
		if m.Container != "" {
			row("Container", "%s", m.Container)
		}
		if m.Duration > 0 {
			row("Duration", "%s", formatDuration(time.Duration(m.Duration*float64(time.Second))))
		}
		if m.Origin == originSite && m.Format == "http" {
			// Direct formats download in parts of a few MiB, not in fragments.
			row("Parts", "%s", partProgress(m.FragmentsDone, m.FragmentsTotal))
		} else {
			row("Fragments", "%s", fragmentProgress(m.FragmentsDone, m.FragmentsTotal))
		}
	} else {
		row("Mode", "%s", v.Mode)
	}
	switch {
	case v.Size < 0:
		row("Size", "unknown")
	case v.SizeApprox:
		row("Size", "%s (estimated)", v.sizeText())
	default:
		row("Size", "%s (%d bytes)", formatBytes(v.Size), v.Size)
	}
	if pct, ok := v.progressPercent(); ok {
		row("Downloaded", "%s (%.1f%%)", formatBytes(v.Downloaded), pct)
	} else {
		row("Downloaded", "%s", formatBytes(v.Downloaded))
	}
	if v.MaxConnections > 0 {
		row("Connections", "up to %d", v.MaxConnections)
	} else {
		row("Connections", "up to %d (default)", engine.DefaultMaxConnections)
	}
	row("Speed limit", "%s", formatRate(v.SpeedLimit))
	if v.Connections > 0 || v.SpeedBps > 0 {
		row("Live", "%d connections, %s/s", v.Connections, formatBytes(int64(v.SpeedBps)))
	}
	if d.Validators.ETag != "" {
		strength := "weak"
		if d.Validators.StrongETag {
			strength = "strong"
		}
		row("ETag", "%s (%s)", d.Validators.ETag, strength)
	}
	if d.Validators.LastModified != "" {
		row("Last-Modified", "%s", d.Validators.LastModified)
	}
	row("Created", "%s", formatTime(v.CreatedAt))
	if v.StartedAt != nil {
		row("Started", "%s", formatTime(*v.StartedAt))
	}
	row("Updated", "%s", formatTime(v.UpdatedAt))
	if v.CompletedAt != nil {
		row("Completed", "%s", formatTime(*v.CompletedAt))
	}
	if taken := timeTaken(d.State, v); taken != "" {
		row("Time taken", "%s", taken)
		if avg := averageSpeed(d.State, v); avg != "" {
			row("Average speed", "%s", avg)
		}
	}
	if v.CompletionDialog {
		row("Complete dialog", "%s", "opens when it ends (its progress window was opened)")
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if v.Media != nil {
		fmt.Fprintf(w, "\nStream tracks (%d):\n", len(v.Media.Tracks))
		if err := writeTracks(w, v.Media); err != nil {
			return err
		}
	} else if err := writeSegments(w, v.Segments); err != nil {
		return err
	}
	if len(v.ConnectionInfo) > 0 {
		return writeConnections(w, v.ConnectionInfo)
	}
	return nil
}

// timeTaken describes how long the download has been running altogether, pauses
// excluded: "" when nothing is known (it never ran, or ran before Downex recorded it).
func timeTaken(state model.State, v infoView) string {
	if v.ActiveMS <= 0 && v.StartedAt == nil {
		return ""
	}
	taken := formatDuration(time.Duration(v.ActiveMS) * time.Millisecond)
	if state != model.StateCompleted {
		taken += " so far"
	}
	return taken
}

// averageSpeed is the average speed of the whole download: its bytes over the time its
// runs took (pauses excluded), with a running downexd's live bytes and total; "" while
// that time is unknown.
func averageSpeed(state model.State, v infoView) string {
	bytes := v.Downloaded
	if v.liveReceived > 0 {
		bytes = v.liveReceived
	}
	if v.ActiveMS <= 0 || bytes < 0 {
		return ""
	}
	avg := formatBytes(dlview.AverageBps(bytes, v.ActiveMS)) + "/s"
	if state != model.StateCompleted {
		avg += " so far"
	}
	return avg
}

// writeSegments prints the stored segments of a file download.
func writeSegments(w io.Writer, segs []model.Segment) error {
	fmt.Fprintf(w, "\nSegments (%d):\n", len(segs))
	if len(segs) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "ID\tSTART\tEND\tSAVED\tDONE\t")
	for _, s := range segs {
		end, done := "open", "-"
		if s.End >= 0 {
			end = fmt.Sprint(s.End)
			done = fmt.Sprintf("%.1f%%", percent(s.Durable, s.End-s.Start+1))
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\t%d\t%s\t\n", s.ID, s.Start, end, s.Durable, done)
	}
	return tw.Flush()
}

// describeTrackIDs names the selected tracks of a stored media download.
func describeTrackIDs(m *mediaView) string {
	var parts []string
	if m.SelectedVideo != "" {
		parts = append(parts, "video "+m.SelectedVideo)
	}
	if m.SelectedAudio != "" {
		parts = append(parts, "audio "+m.SelectedAudio)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " + ")
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func sortedKeys(h http.Header) []string {
	return slices.Sorted(maps.Keys(h))
}
