package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/engine"
	"downex/internal/ipc"
	"downex/internal/model"
	"downex/internal/polite"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Daemon setting bounds (docs/DAEMON-SPEC.md §3).
const (
	minConcurrent = 1
	maxConcurrent = 16
)

func (a *app) add(ctx context.Context, args []string) error {
	fs := a.flagSet("add", "add <url> [flags]",
		"Hand url to downexd and print the new download's id. downexd downloads it in the\n"+
			"background, starting queued downloads in order while fewer than its max-concurrent\n"+
			"limit run. With --wait, follow the progress until the download completes; Ctrl+C\n"+
			"then stops following, not the download. downexd is started if it is not running.")
	t := a.daemonFlags(fs)
	var (
		headers  headerFlag
		dir      string
		conns    int
		limitArg string
		name     string
		queueArg string
		paused   bool
		wait     bool
	)
	fs.StringVar(&dir, "o", "", "save into `dir` (default: the daemon's default directory, see \"downex settings\")")
	fs.StringVar(&queueArg, "queue", "", queueFlagUsage)
	fs.IntVar(&conns, "c", 0, fmt.Sprintf("maximum `connections` (1-%d; 0 = the daemon's default)", engine.HardMaxConnections))
	fs.StringVar(&limitArg, "limit", "0", "speed limit as a `rate` in bytes per second, e.g. 500K = 500 KB/s or 1.5M = 1.5 MB/s (0 = unlimited)")
	fs.Var(&headers, "H", "extra request header \"Name: value\" (repeatable), e.g. Cookie or Referer")
	fs.StringVar(&name, "name", "", "save as `file` instead of the name suggested by the server or URL")
	fs.BoolVar(&paused, "paused", false, "add the download paused instead of queueing it")
	fs.BoolVar(&wait, "wait", false, "follow the download's progress until it completes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		return usageError{"add", "expects exactly one URL"}
	}
	if err := checkConnections("add", conns); err != nil {
		return err
	}
	limit, err := parseRate(limitArg)
	if err != nil {
		return usageError{"add", "--limit: " + err.Error()}
	}
	if paused && wait {
		return usageError{"add", "--paused and --wait cannot be combined"}
	}
	if dir != "" {
		// The daemon requires an absolute directory; -o is relative to this process.
		if dir, err = filepath.Abs(dir); err != nil {
			return fmt.Errorf("directory: %w", err)
		}
	}
	req := &downexv1.AddRequest{
		Url: pos[0], Headers: rpcHeaders(headers.h), Dir: dir, Filename: name,
		MaxConnections: int32(conns), SpeedLimit: limit, StartPaused: paused,
		Source: downexv1.AddSource_ADD_SOURCE_CLI,
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	var queue *downexv1.Queue
	if visited(fs)["queue"] {
		if queue, err = a.resolveQueueFlag(ctx, c, queueArg); err != nil {
			return err
		}
		req.QueueId = queue.GetId()
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Downloads.Add(cctx, connect.NewRequest(req))
	if err != nil {
		return rpcError("add", err)
	}
	d := resp.Msg.GetDownload()
	fmt.Fprintln(a.stdout, d.GetId())
	a.con.printf("Download %d %s: %s\n", d.GetId(), describeState(d), d.GetUrl())
	a.hintStoppedQueue(t, queue, d)
	if !wait {
		return nil
	}
	return a.follow(ctx, c, t, d)
}

func (a *app) pause(ctx context.Context, args []string) error {
	fs := a.flagSet("pause", "pause <id>",
		"Pause a queued or running download of downexd. Its progress is saved; continue it\n"+
			"with \"downex resume <id>\".")
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	mid, err := parseID("pause", pos)
	if err != nil {
		return err
	}
	id := int64(mid)
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Downloads.Pause(cctx, connect.NewRequest(&downexv1.PauseRequest{Id: id}))
	if err != nil {
		return downloadRPCError("pause", id, err)
	}
	d := resp.Msg.GetDownload()
	if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
		fmt.Fprintf(a.stdout, "Download %d is %s.\n", id, describeState(d))
		return nil
	}
	fmt.Fprintf(a.stdout, "Paused download %d", id)
	if d.GetReceived() > 0 {
		fmt.Fprintf(a.stdout, " with %s", describeSaved(rpcSize(d), d.GetReceived()))
	}
	fmt.Fprintf(a.stdout, ".\nResume with: %s\n", commandLine(t, "resume", idArg(id)))
	return nil
}

func (a *app) resume(ctx context.Context, args []string) error {
	fs := a.flagSet("resume", "resume <id> [flags]",
		"Continue a paused, failed or interrupted download from its saved progress. downexd\n"+
			"queues it again (it is started if it is not running). -c and --limit change the\n"+
			"download's stored settings. With --wait, follow the progress until it completes.\n\n"+
			"--restart discards the saved progress and downloads the file again from the start. A\n"+
			"completed download is downloaded again only with --restart: its file stays and the\n"+
			"new copy is saved under a free name (\"name (1).ext\"), or with --replace the new copy\n"+
			"takes the file's place once it is complete (until then, and if it fails, the file\n"+
			"stays as it is; a folder or a link under the name is never replaced).\n\n"+
			"With --local the download runs in this process in the foreground instead, as with\n"+
			"\"get\" (only while downexd is not running); Ctrl+C then pauses it.")
	t := a.daemonFlags(fs)
	var (
		netf     netFlags
		conns    int
		limitArg string
		restart  bool
		replace  bool
		wait     bool
		local    bool
	)
	netf.register(fs)
	fs.IntVar(&conns, "c", 0, fmt.Sprintf("change the maximum `connections` (1-%d; 0 = default)", engine.HardMaxConnections))
	fs.StringVar(&limitArg, "limit", "", "change the speed limit to `rate` bytes per second, e.g. 500K = 500 KB/s (0 = unlimited)")
	fs.BoolVar(&restart, "restart", false, "discard the saved progress and download the file again (also a completed one)")
	fs.BoolVar(&replace, "replace", false, "with --restart of a completed download: the new copy replaces the file once complete")
	fs.BoolVar(&wait, "wait", false, "follow the download's progress until it completes")
	fs.BoolVar(&local, "local", false, "run the download in this process without downexd, like \"get\"")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	id, err := parseID("resume", pos)
	if err != nil {
		return err
	}
	if replace && !restart {
		return usageError{"resume", "--replace needs --restart (it downloads a completed download again)"}
	}
	o := runOptions{restart: restart, again: restart, replace: replace, dbFlag: t.db}
	set := visited(fs)
	if set["c"] {
		if err := checkConnections("resume", conns); err != nil {
			return err
		}
		o.conns = &conns
	}
	if set["limit"] {
		limit, err := parseRate(limitArg)
		if err != nil {
			return usageError{"resume", "--limit: " + err.Error()}
		}
		o.limit = &limit
	}
	if local {
		if wait {
			return usageError{"resume", "--wait cannot be combined with --local, which always runs in the foreground"}
		}
		if err := a.refuseWhileDaemonRuns(ctx, t, "resume", id); err != nil {
			return err
		}
		return a.resumeLocal(ctx, id, netf, o)
	}
	if set["proxy"] || set["v"] {
		return usageError{"resume", "--proxy and -v apply only with --local (downexd uses its own settings)"}
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	req := &downexv1.ResumeRequest{Id: int64(id), Restart: restart, SpeedLimit: o.limit}
	if replace {
		req.ExistingFile = downexv1.ExistingFile_EXISTING_FILE_REPLACE
	}
	if o.conns != nil {
		n := int32(*o.conns)
		req.MaxConnections = &n
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Downloads.Resume(cctx, connect.NewRequest(req))
	if err != nil {
		return downloadRPCError("resume", int64(id), err)
	}
	d := resp.Msg.GetDownload()
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		fmt.Fprintf(a.stdout, "Download %d is already complete: %s\n", id, printable(rpcPath(d)))
		return nil
	}
	if restart {
		fmt.Fprintf(a.stdout, "Downloading %d again (%s): %s\n", id, describeState(d), printable(rpcPath(d)))
		if replace {
			fmt.Fprintln(a.stdout, "The new copy replaces the file once it is complete; until then the file stays as it is.")
		}
	} else {
		fmt.Fprintf(a.stdout, "Resumed download %d (%s).\n", id, describeState(d))
	}
	if !wait {
		return nil
	}
	return a.follow(ctx, c, t, d)
}

func (a *app) rm(ctx context.Context, args []string) error {
	fs := a.flagSet("rm", "rm <id> [--delete-file]",
		"Remove a download from the list, pausing it first if it is running. Its file (or\n"+
			"partial .dxpart file) is kept unless --delete-file is given. With --local the\n"+
			"database is changed directly, without downexd (only while it is not running).")
	t := a.daemonFlags(fs)
	deleteFile := fs.Bool("delete-file", false, "also delete the downloaded file, or the partial file of an unfinished download")
	local := fs.Bool("local", false, "change the database directly, without downexd")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	mid, err := parseID("rm", pos)
	if err != nil {
		return err
	}
	if *local {
		if err := a.refuseWhileDaemonRuns(ctx, t, "rm", mid); err != nil {
			return err
		}
		return a.rmLocal(ctx, mid, t.db, *deleteFile)
	}

	id := int64(mid)
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	got, err := c.Downloads.Get(cctx, connect.NewRequest(&downexv1.GetRequest{Id: id}))
	if err != nil {
		return downloadRPCError("rm", id, err)
	}
	path := rpcPath(got.Msg.GetDownload())
	if _, err := c.Downloads.Remove(cctx, connect.NewRequest(&downexv1.RemoveRequest{Id: id, DeleteFile: *deleteFile})); err != nil {
		return downloadRPCError("rm", id, err)
	}
	a.printRemoved(mid, path, *deleteFile)
	return nil
}

// refuseWhileDaemonRuns fails when downexd is running on t's endpoint: it owns the
// downloads, and a second writer could change the same download and files.
func (a *app) refuseWhileDaemonRuns(ctx context.Context, t *daemonTarget, cmd string, id model.DownloadID) error {
	ep, err := a.endpointOf(t)
	if err != nil {
		return nil // no endpoint, so no daemon to conflict with
	}
	if up, err := a.sys.Running(ctx, ep); err != nil || !up {
		return nil
	}
	return fmt.Errorf("downexd is running and manages the downloads; use %q without --local, or stop downexd first (downex daemon stop)",
		commandLine(t, cmd, idArg(int64(id))))
}

func (a *app) settings(ctx context.Context, args []string) error {
	fs := a.flagSet("settings", "settings [flags]",
		"Show downexd's settings, including the download categories, or change those given as\n"+
			"flags (every other setting is kept). on/off flags also accept true/false. downexd is\n"+
			"started if it is not running.")
	t := a.daemonFlags(fs)
	var (
		concurrent int
		limitArg   string
		dir        string
		conns      int
		asJSON     bool
		folders    bool
		confirm    bool
		notifyDone bool
		notifyFail bool
		atLogin    bool
		tray       bool
		progress   bool
		complete   bool
		clipboard  bool
		markFiles  bool
		avScan     bool
		avCommand  string
		avArgs     string
		feedURL    string
		autoUpdate bool
		siteMedia  bool
		toolsAuto  bool
		channel    string
		checkFiles bool
		renewLinks string
		politeOn   bool
		politeMs   int
	)
	fs.IntVar(&concurrent, "max-concurrent", 0, fmt.Sprintf("run at most `n` downloads at once (%d-%d)", minConcurrent, maxConcurrent))
	fs.StringVar(&limitArg, "global-limit", "", "limit all downloads together to `rate` bytes per second, e.g. 10M = 10 MB/s (0 = unlimited)")
	fs.StringVar(&dir, "dir", "", "default download `directory` of \"add\" without -o")
	fs.IntVar(&conns, "connections", 0, fmt.Sprintf("default maximum connections per download, `n` (1-%d)", engine.HardMaxConnections))
	fs.Var(onOff{&folders}, "categories-folders", "`on|off`: save downloads without -o into their category's folder")
	fs.Var(onOff{&confirm}, "confirm-browser", "`on|off`: downloads handed off by the browser wait for confirmation")
	fs.Var(onOff{&notifyDone}, "notify-complete", "`on|off`: notify when a download completes")
	fs.Var(onOff{&notifyFail}, "notify-failure", "`on|off`: notify when a download fails")
	fs.Var(onOff{&atLogin}, "launch-at-login", "`on|off`: start downexd when you sign in")
	fs.Var(onOff{&tray}, "tray", "`on|off`: show the Downex icon in the notification area")
	fs.Var(onOff{&progress}, "progress-window", "`on|off`: open a download's progress window when it starts from the\nwindow or the browser")
	fs.Var(onOff{&complete}, "complete-dialog", "`on|off`: the progress window shows a completion view when its download\ncompletes (off: the window closes)")
	fs.Var(onOff{&clipboard}, "clipboard", "`on|off`: offer download links copied to the clipboard")
	fs.Var(onOff{&markFiles}, "mark-files", "`on|off`: mark downloaded files with where they came from, as browsers do\n(Windows Mark of the Web, macOS quarantine)")
	fs.Var(onOff{&avScan}, "av-scan", "`on|off`: scan every completed download with --av-command")
	fs.StringVar(&avCommand, "av-command", "", "absolute `path` of the virus scanner run after each download (\"\" = none)")
	fs.StringVar(&avArgs, "av-args", "", "the scanner's `arguments`; [File] stands for the downloaded file (default \"[File]\")")
	fs.StringVar(&feedURL, "update-feed", "", "https `url` of the signed update feed (\"\" = updates off)")
	fs.Var(onOff{&autoUpdate}, "auto-update", "`on|off`: check the update feed at start and daily")
	fs.Var(onOff{&siteMedia}, "site-media", "`on|off`: download videos from web pages with yt-dlp (downloaded on first use)")
	fs.Var(onOff{&toolsAuto}, "tools-auto-update", "`on|off`: update Downex's own yt-dlp and Deno daily")
	fs.StringVar(&channel, "tools-channel", "", "yt-dlp release `channel`: stable or nightly (recommended by yt-dlp)")
	fs.Var(onOff{&checkFiles}, "check-files", "`on|off`: warn when a downloaded file is not what its name says (a program\nnamed like a video, an error page instead of the file)")
	fs.StringVar(&renewLinks, "renew-links", "", "`auto|ask|off`: what a new link to a file whose link expired does: continue that\ndownload (auto), ask first (ask) or nothing (off); the bytes are compared first")
	fs.Var(onOff{&politeOn}, "polite", "`on|off`: Polite mode: slow down when downloads make calls, games or streaming\nlag; speed back up when the line is quiet")
	fs.IntVar(&politeMs, "polite-target", 0, fmt.Sprintf("Polite mode keeps the line's extra delay under `ms` milliseconds (%d-%d;\n0 = the default %d)",
		polite.MinTarget/time.Millisecond, polite.MaxTarget/time.Millisecond, polite.DefaultTarget/time.Millisecond))
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"settings", "takes no arguments"}
	}
	renewMode, renewOK := renewLinksValues[renewLinks]
	set := visited(fs)
	var limit int64
	switch {
	case set["max-concurrent"] && (concurrent < minConcurrent || concurrent > maxConcurrent):
		return usageError{"settings", fmt.Sprintf("--max-concurrent must be between %d and %d, got %d", minConcurrent, maxConcurrent, concurrent)}
	case set["connections"] && (conns < 1 || conns > engine.HardMaxConnections):
		return usageError{"settings", fmt.Sprintf("--connections must be between 1 and %d, got %d", engine.HardMaxConnections, conns)}
	case set["dir"] && dir == "":
		return usageError{"settings", "--dir must not be empty"}
	case set["av-command"] && avCommand != "" && !filepath.IsAbs(avCommand):
		return usageError{"settings", fmt.Sprintf("--av-command must be an absolute path (it runs without a shell or PATH lookup), got %q", avCommand)}
	case set["av-args"] && strings.IndexFunc(avArgs, needsEscape) >= 0:
		return usageError{"settings", "--av-args must not contain control characters"}
	case set["tools-channel"] && channel != "stable" && channel != "nightly":
		return usageError{"settings", fmt.Sprintf("--tools-channel must be stable or nightly, got %q", channel)}
	case set["renew-links"] && !renewOK:
		return usageError{"settings", fmt.Sprintf("--renew-links must be auto, ask or off, got %q", renewLinks)}
	case set["polite-target"] && !polite.ValidTargetMs(politeMs):
		return usageError{"settings", fmt.Sprintf("--polite-target must be between %d and %d (or 0 for the default), got %d",
			polite.MinTarget/time.Millisecond, polite.MaxTarget/time.Millisecond, politeMs)}
	}
	if set["update-feed"] {
		if feedURL, err = checkFeedURL(feedURL); err != nil {
			return usageError{"settings", "--update-feed: " + err.Error()}
		}
	}
	if set["global-limit"] {
		if limit, err = parseRate(limitArg); err != nil {
			return usageError{"settings", "--global-limit: " + err.Error()}
		}
	}
	if set["dir"] {
		if dir, err = filepath.Abs(dir); err != nil {
			return fmt.Errorf("directory: %w", err)
		}
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	got, err := c.Daemon.GetSettings(cctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		return rpcError("settings", err)
	}
	s := got.Msg.GetSettings()
	if s == nil {
		s = &downexv1.Settings{}
	}
	// Settings are replaced as a whole: s, as read, carries every field (also ones this
	// build does not know, kept by protobuf), and only the flags given change it.
	changes := []struct {
		flag  string
		apply func()
	}{
		{"max-concurrent", func() { s.MaxConcurrentDownloads = int32(concurrent) }},
		{"global-limit", func() { s.GlobalSpeedLimit = limit }},
		{"dir", func() { s.DefaultDir = dir }},
		{"connections", func() { s.DefaultMaxConnections = int32(conns) }},
		{"categories-folders", func() { s.UseCategoryFolders = folders }},
		{"confirm-browser", func() { s.ConfirmBrowserDownloads = confirm }},
		{"notify-complete", func() { s.NotifyOnComplete = notifyDone }},
		{"notify-failure", func() { s.NotifyOnFailure = notifyFail }},
		{"launch-at-login", func() { s.LaunchAtLogin = atLogin }},
		{"tray", func() { s.ShowTrayIcon = tray }},
		{"progress-window", func() { s.ShowProgressWindow = progress }},
		{"complete-dialog", func() { s.ShowCompleteDialog = complete }},
		{"clipboard", func() { s.ClipboardMonitor = clipboard }},
		{"mark-files", func() { s.MarkDownloadedFiles = markFiles }},
		{"av-scan", func() { s.AvScanEnabled = avScan }},
		{"av-command", func() { s.AvScanCommand = avCommand }},
		{"av-args", func() { s.AvScanArgs = avArgs }},
		{"update-feed", func() { s.UpdateFeedUrl = feedURL }},
		{"auto-update", func() { s.AutoCheckUpdates = autoUpdate }},
		{"site-media", func() { s.SiteMedia = siteMedia }},
		{"tools-auto-update", func() { s.ToolsAutoUpdate = toolsAuto }},
		{"tools-channel", func() { s.ToolsChannel = channel }},
		{"check-files", func() { s.CheckDownloadedFiles = checkFiles }},
		{"renew-links", func() { s.RenewLinks = renewMode }},
		{"polite", func() { s.PoliteMode = politeOn }},
		{"polite-target", func() { s.PoliteTargetMs = int32(politeMs) }},
	}
	changed := false
	for _, ch := range changes {
		if set[ch.flag] {
			ch.apply()
			changed = true
		}
	}
	if (set["av-scan"] || set["av-command"]) && s.GetAvScanEnabled() && s.GetAvScanCommand() == "" {
		return usageError{"settings", "the virus scan needs the scanner: give --av-command path"}
	}
	if changed {
		upd, err := c.Daemon.UpdateSettings(cctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
		if err != nil {
			return rpcError("settings", err)
		}
		if us := upd.Msg.GetSettings(); us != nil {
			s = us
		}
	}
	v := newSettingsView(s)
	if asJSON {
		return writeJSON(a.stdout, v)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Max concurrent downloads:\t%d\n", v.MaxConcurrentDownloads)
	fmt.Fprintf(tw, "Global speed limit:\t%s\n", formatRate(v.GlobalSpeedLimit))
	fmt.Fprintf(tw, "Default directory:\t%s\n", printable(v.DefaultDir))
	fmt.Fprintf(tw, "Default connections:\t%d\n", v.DefaultMaxConnections)
	fmt.Fprintf(tw, "Category folders:\t%s\n", onOffText(v.UseCategoryFolders))
	fmt.Fprintf(tw, "Confirm browser downloads:\t%s\n", onOffText(v.ConfirmBrowserDownloads))
	fmt.Fprintf(tw, "Notify on completion:\t%s\n", onOffText(v.NotifyOnComplete))
	fmt.Fprintf(tw, "Notify on failure:\t%s\n", onOffText(v.NotifyOnFailure))
	fmt.Fprintf(tw, "Launch at login:\t%s\n", onOffText(v.LaunchAtLogin))
	fmt.Fprintf(tw, "Tray icon:\t%s\n", onOffText(v.ShowTrayIcon))
	fmt.Fprintf(tw, "Progress window:\t%s\n", onOffText(v.ShowProgressWindow))
	fmt.Fprintf(tw, "Completion view:\t%s\n", onOffText(v.ShowCompleteDialog))
	fmt.Fprintf(tw, "Clipboard monitor:\t%s\n", onOffText(v.ClipboardMonitor))
	fmt.Fprintf(tw, "Check downloaded files:\t%s\n", onOffText(v.CheckDownloadedFiles))
	fmt.Fprintf(tw, "Expired links:\t%s\n", renewLinksText[v.RenewLinks])
	fmt.Fprintf(tw, "Mark downloaded files:\t%s\n", onOffText(v.MarkDownloadedFiles))
	scan := onOffText(v.AVScanEnabled)
	if v.AVScanCommand != "" {
		scan += ": " + printable(v.AVScanCommand)
		if v.AVScanArgs != "" {
			scan += " " + printable(v.AVScanArgs)
		}
	}
	fmt.Fprintf(tw, "Virus scan:\t%s\n", scan)
	fmt.Fprintf(tw, "Proxy:\t%s\n", describeProxy(v.Proxy))
	feed := "none (updates off)"
	if v.UpdateFeedURL != "" {
		feed = printable(v.UpdateFeedURL)
	}
	fmt.Fprintf(tw, "Update feed:\t%s\n", feed)
	fmt.Fprintf(tw, "Automatic update checks:\t%s\n", onOffText(v.AutoCheckUpdates))
	fmt.Fprintf(tw, "Video sites (yt-dlp):\t%s\n", onOffText(v.SiteMedia))
	fmt.Fprintf(tw, "Update yt-dlp and Deno:\t%s (%s channel)\n", onOffText(v.ToolsAutoUpdate), printable(v.ToolsChannel))
	fmt.Fprintf(tw, "Polite mode:\t%s\n", politeSettingText(v.PoliteMode, v.PoliteTargetMs))
	fmt.Fprintf(tw, "Site logins:\t%d (see \"downex login ls\")\n", len(v.SiteLogins))
	if err := tw.Flush(); err != nil {
		return err
	}
	if err := writeCategories(a.stdout, v.Categories); err != nil {
		return err
	}
	return writeHostRules(a.stdout, v.HostRules)
}

// settingsView is the JSON form of the daemon settings.
type settingsView struct {
	MaxConcurrentDownloads  int            `json:"maxConcurrentDownloads"`
	GlobalSpeedLimit        int64          `json:"globalSpeedLimit"` // bytes per second, 0 = unlimited
	DefaultDir              string         `json:"defaultDir"`
	DefaultMaxConnections   int            `json:"defaultMaxConnections"`
	Categories              []categoryView `json:"categories"`
	UseCategoryFolders      bool           `json:"useCategoryFolders"`
	ConfirmBrowserDownloads bool           `json:"confirmBrowserDownloads"`
	NotifyOnComplete        bool           `json:"notifyOnComplete"`
	NotifyOnFailure         bool           `json:"notifyOnFailure"`
	LaunchAtLogin           bool           `json:"launchAtLogin"`
	ShowTrayIcon            bool           `json:"showTrayIcon"`
	ShowProgressWindow      bool           `json:"showProgressWindow"`
	ShowCompleteDialog      bool           `json:"showCompleteDialog"`
	// M6 (docs/M6-SPEC.md). Passwords are never part of the settings.
	Proxy                proxyView       `json:"proxy"`
	HostRules            []hostRuleView  `json:"hostRules"`
	SiteLogins           []siteLoginView `json:"siteLogins"`
	ClipboardMonitor     bool            `json:"clipboardMonitor"`
	MarkDownloadedFiles  bool            `json:"markDownloadedFiles"`
	CheckDownloadedFiles bool            `json:"checkDownloadedFiles"` // the file check (docs/DESKTOP-SPEC.md §9)
	RenewLinks           string          `json:"renewLinks"`           // expired links: auto, ask or off (docs/DESKTOP-SPEC.md §10)
	AVScanEnabled        bool            `json:"avScanEnabled"`
	AVScanCommand        string          `json:"avScanCommand,omitempty"`
	AVScanArgs           string          `json:"avScanArgs,omitempty"`
	UpdateFeedURL        string          `json:"updateFeedUrl,omitempty"`
	AutoCheckUpdates     bool            `json:"autoCheckUpdates"`
	// Site media (docs/SITE-MEDIA-SPEC.md §3.8).
	SiteMedia       bool   `json:"siteMedia"`
	ToolsAutoUpdate bool   `json:"toolsAutoUpdate"`
	ToolsChannel    string `json:"toolsChannel"`
	// Polite mode (docs/POLITE-SPEC.md §7); the target in milliseconds, 0 = the default.
	PoliteMode     bool `json:"politeMode"`
	PoliteTargetMs int  `json:"politeTargetMs"`
}

// renewLinksValues maps --renew-links to the setting.
var renewLinksValues = map[string]downexv1.RenewLinks{
	"auto": downexv1.RenewLinks_RENEW_LINKS_AUTO,
	"ask":  downexv1.RenewLinks_RENEW_LINKS_ASK,
	"off":  downexv1.RenewLinks_RENEW_LINKS_OFF,
}

// renewLinksText words the setting for "settings".
var renewLinksText = map[string]string{
	"auto": "continue with a matching new link",
	"ask":  "ask before continuing",
	"off":  "don't look for new links",
}

// renewLinksName is the setting's name; UNSPECIFIED is auto.
func renewLinksName(v downexv1.RenewLinks) string {
	switch v {
	case downexv1.RenewLinks_RENEW_LINKS_ASK:
		return "ask"
	case downexv1.RenewLinks_RENEW_LINKS_OFF:
		return "off"
	}
	return "auto"
}

// hostRuleView is one per-host limit: at most MaxConnections per download (0 = the
// default), SpeedLimit bytes per second shared by the host's downloads (0 = unlimited) and
// requests of at most MaxRequestBytes (0 = no cap).
type hostRuleView struct {
	Pattern         string `json:"pattern"`
	MaxConnections  int    `json:"maxConnections"`
	SpeedLimit      int64  `json:"speedLimit"`
	MaxRequestBytes int64  `json:"maxRequestBytes"`
}

// categoryView is one download category: its configured dir ("" = below the default
// directory) and the folder that follows from it.
type categoryView struct {
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
	Dir        string   `json:"dir,omitempty"`
	Folder     string   `json:"folder"`
}

func newSettingsView(s *downexv1.Settings) settingsView {
	v := settingsView{
		MaxConcurrentDownloads: int(s.GetMaxConcurrentDownloads()), GlobalSpeedLimit: s.GetGlobalSpeedLimit(),
		DefaultDir: s.GetDefaultDir(), DefaultMaxConnections: int(s.GetDefaultMaxConnections()),
		Categories:         make([]categoryView, 0, len(s.GetCategories())),
		UseCategoryFolders: s.GetUseCategoryFolders(), ConfirmBrowserDownloads: s.GetConfirmBrowserDownloads(),
		NotifyOnComplete: s.GetNotifyOnComplete(), NotifyOnFailure: s.GetNotifyOnFailure(),
		LaunchAtLogin: s.GetLaunchAtLogin(), ShowTrayIcon: s.GetShowTrayIcon(),
		ShowProgressWindow: s.GetShowProgressWindow(), ShowCompleteDialog: s.GetShowCompleteDialog(),
		Proxy: newProxyView(s.GetProxy()), HostRules: make([]hostRuleView, 0, len(s.GetHostRules())),
		SiteLogins: newSiteLoginViews(s.GetSiteLogins()), ClipboardMonitor: s.GetClipboardMonitor(),
		MarkDownloadedFiles: s.GetMarkDownloadedFiles(), AVScanEnabled: s.GetAvScanEnabled(),
		AVScanCommand: s.GetAvScanCommand(), AVScanArgs: s.GetAvScanArgs(), UpdateFeedURL: s.GetUpdateFeedUrl(),
		AutoCheckUpdates: s.GetAutoCheckUpdates(), SiteMedia: s.GetSiteMedia(), ToolsAutoUpdate: s.GetToolsAutoUpdate(),
		ToolsChannel:         s.GetToolsChannel(),
		CheckDownloadedFiles: s.GetCheckDownloadedFiles(),
		RenewLinks:           renewLinksName(s.GetRenewLinks()),
		PoliteMode:           s.GetPoliteMode(),
		PoliteTargetMs:       int(s.GetPoliteTargetMs()),
	}
	if v.ToolsChannel == "" {
		v.ToolsChannel = "nightly" // the daemon's default
	}
	for _, r := range s.GetHostRules() {
		v.HostRules = append(v.HostRules, hostRuleView{Pattern: r.GetPattern(), MaxConnections: int(r.GetMaxConnections()),
			SpeedLimit: r.GetSpeedLimit(), MaxRequestBytes: r.GetMaxRequestBytes()})
	}
	for _, c := range s.GetCategories() {
		folder := c.GetDir()
		if folder == "" && s.GetDefaultDir() != "" {
			folder = filepath.Join(s.GetDefaultDir(), c.GetName())
		}
		exts := c.GetExtensions()
		if exts == nil {
			exts = []string{}
		}
		v.Categories = append(v.Categories, categoryView{Name: c.GetName(), Extensions: exts, Dir: c.GetDir(), Folder: folder})
	}
	return v
}

// writeHostRules prints the per-host limits as a table (nothing when there are none).
func writeHostRules(w io.Writer, rs []hostRuleView) error {
	if len(rs) == 0 {
		return nil
	}
	fmt.Fprintln(w, "\nHost rules:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  HOST\tCONNECTIONS\tSPEED LIMIT\tMAX REQUEST")
	for _, r := range rs {
		conns := "default"
		if r.MaxConnections > 0 {
			conns = strconv.Itoa(r.MaxConnections)
		}
		request := "no cap"
		if r.MaxRequestBytes > 0 {
			request = formatBytes(r.MaxRequestBytes)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", printable(r.Pattern), conns, formatRate(r.SpeedLimit), request)
	}
	return tw.Flush()
}

// checkFeedURL validates the update feed URL: https only ("" = updates off).
func checkFeedURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("want an https URL without credentials, got %q", s)
	}
	return s, nil
}

// writeCategories prints the categories as a table.
func writeCategories(w io.Writer, cs []categoryView) error {
	if len(cs) == 0 {
		fmt.Fprintln(w, "\nCategories: none")
		return nil
	}
	fmt.Fprintln(w, "\nCategories:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tFOLDER\tEXTENSIONS")
	for _, c := range cs {
		folder := printable(c.Folder)
		if c.Dir == "" {
			folder += " (default)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", printable(c.Name), folder, strings.Join(c.Extensions, " "))
	}
	return tw.Flush()
}

func (a *app) daemon(ctx context.Context, args []string) error {
	fs := a.flagSet("daemon", "daemon start|stop|status [--json]",
		"Control downexd, the per-user background process that runs downloads:\n"+
			"  start   start downexd unless it is already running\n"+
			"  stop    pause the running downloads (their progress is saved) and stop downexd\n"+
			"  status  show whether downexd is running; the exit status is 1 when it is not")
	t := a.daemonFlags(fs)
	asJSON := fs.Bool("json", false, "status: print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		return usageError{"daemon", "expects one of start, stop or status"}
	}
	switch pos[0] {
	case "start", "stop", "status":
	default:
		return usageError{"daemon", fmt.Sprintf("unknown subcommand %q (want start, stop or status)", pos[0])}
	}
	ep, err := a.endpointOf(t)
	if err != nil {
		return err
	}
	up, err := a.sys.Running(ctx, ep)
	if err != nil {
		return err
	}
	c := a.sys.Client(ep)
	switch pos[0] {
	case "start":
		if up {
			st, err := a.status(ctx, c)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "downexd is already running (pid %d).\n", st.GetPid())
			return nil
		}
		st, err := a.startDaemon(ctx, t, c)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Started downexd (pid %d) on %s.\n", st.GetPid(), endpointOrDefault(st.GetEndpoint(), ep))
		return nil
	case "stop":
		if !up {
			fmt.Fprintln(a.stdout, "downexd is not running.")
			return nil
		}
		return a.stopDaemon(ctx, c)
	case "status":
		if !up {
			if *asJSON {
				if err := writeJSON(a.stdout, statusView{Running: false, Endpoint: ep}); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(a.stdout, "downexd is not running.")
			}
			return silentError{ipc.ErrNotRunning}
		}
		st, err := a.status(ctx, c)
		if err != nil {
			return err
		}
		return a.printStatus(st, ep, *asJSON)
	}
	return usageError{"daemon", fmt.Sprintf("unknown subcommand %q (want start, stop or status)", pos[0])}
}

func (a *app) status(ctx context.Context, c *client.Client) (*downexv1.StatusResponse, error) {
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Daemon.Status(cctx, connect.NewRequest(&downexv1.StatusRequest{}))
	if err != nil {
		return nil, rpcError("status", err)
	}
	return resp.Msg, nil
}

// stopDaemon asks the daemon to shut down and waits until its endpoint is gone.
func (a *app) stopDaemon(ctx context.Context, c *client.Client) error {
	cctx, cancel := context.WithTimeout(ctx, a.sys.stopTimeout)
	defer cancel()
	if _, err := c.Daemon.Shutdown(cctx, connect.NewRequest(&downexv1.ShutdownRequest{})); err != nil {
		// The daemon may exit before its answer arrives: that is a success too.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if up, perr := a.sys.Running(ctx, c.Endpoint); perr != nil || up {
			return rpcError("stop", err)
		}
	}
	for {
		up, err := a.sys.Running(cctx, c.Endpoint)
		if err == nil && !up {
			fmt.Fprintln(a.stdout, "Stopped downexd.")
			return nil
		}
		if !client.Sleep(cctx, a.sys.PollInterval) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("downexd is still running %s after it was asked to stop", a.sys.stopTimeout)
}

// statusView is the JSON form of "daemon status".
type statusView struct {
	Running   bool        `json:"running"`
	Version   string      `json:"version,omitempty"`
	PID       int         `json:"pid,omitempty"`
	Endpoint  string      `json:"endpoint"`
	StartedAt *time.Time  `json:"startedAt,omitempty"`
	Active    int         `json:"active"`
	Queued    int         `json:"queued"`
	Polite    *politeView `json:"polite,omitempty"` // absent from an older daemon
}

// politeView is Polite mode's status (docs/POLITE-SPEC.md §7): the state's name as the
// relay spells it, the cap in bytes per second (0 = none) and the line delay over the
// base and the base, in milliseconds (-1 = unknown).
type politeView struct {
	State     string `json:"state"`
	RateLimit int64  `json:"rateLimit"`
	DelayMs   int32  `json:"delayMs"`
	BaseMs    int32  `json:"baseMs"`
}

// newPoliteView returns the view of a daemon's Polite mode status, nil for a daemon
// without Polite mode.
func newPoliteView(p *downexv1.PoliteStatus) *politeView {
	name := politeStateName(p.GetState())
	if name == "" {
		return nil
	}
	return &politeView{State: name, RateLimit: p.GetRateLimit(), DelayMs: p.GetDelayMs(), BaseMs: p.GetBaseMs()}
}

// politeStateName is the name of a Polite mode state ("" for an older daemon's).
func politeStateName(s downexv1.PoliteState) string {
	switch s {
	case downexv1.PoliteState_POLITE_STATE_OFF:
		return "off"
	case downexv1.PoliteState_POLITE_STATE_IDLE:
		return "idle"
	case downexv1.PoliteState_POLITE_STATE_WATCHING:
		return "watching"
	case downexv1.PoliteState_POLITE_STATE_SLOWED:
		return "slowed"
	case downexv1.PoliteState_POLITE_STATE_MEASURING:
		return "measuring"
	case downexv1.PoliteState_POLITE_STATE_NO_SIGNAL:
		return "no_signal"
	case downexv1.PoliteState_POLITE_STATE_UNAVAILABLE:
		return "unavailable"
	}
	return ""
}

// politeStatusText is the "Polite mode:" line of "daemon status" (docs/POLITE-SPEC.md
// §8).
func politeStatusText(v *politeView) string {
	delay := ""
	if v.DelayMs >= 0 {
		delay = fmt.Sprintf(" (line delay +%d ms)", v.DelayMs)
	}
	switch v.State {
	case "off":
		return "off"
	case "idle":
		return "on (nothing downloading)"
	case "watching":
		return "on, full speed" + delay
	case "slowed":
		return "slowed to " + formatRate(v.RateLimit) + " to keep the line smooth" + delay
	case "measuring":
		return "measuring the line"
	case "no_signal":
		return "on, but this connection can't be measured: full speed"
	case "unavailable":
		return "not available in this downexd"
	}
	return v.State
}

// politeSettingText is the "Polite mode:" line of "settings".
func politeSettingText(on bool, targetMs int) string {
	if !on {
		return "off"
	}
	return fmt.Sprintf("on (target +%d ms)", polite.TargetOf(targetMs)/time.Millisecond)
}

func (a *app) printStatus(st *downexv1.StatusResponse, ep string, asJSON bool) error {
	v := statusView{
		Running: true, Version: st.GetVersion(), PID: int(st.GetPid()), Endpoint: endpointOrDefault(st.GetEndpoint(), ep),
		Active: int(st.GetActive()), Queued: int(st.GetQueued()), Polite: newPoliteView(st.GetPolite()),
	}
	if ts := st.GetStartedAt(); ts != nil {
		t := ts.AsTime()
		v.StartedAt = &t
	}
	if asJSON {
		return writeJSON(a.stdout, v)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Status:\trunning\n")
	if v.Version != "" {
		fmt.Fprintf(tw, "Version:\t%s\n", v.Version)
	}
	fmt.Fprintf(tw, "PID:\t%d\n", v.PID)
	fmt.Fprintf(tw, "Endpoint:\t%s\n", v.Endpoint)
	if v.StartedAt != nil {
		fmt.Fprintf(tw, "Started:\t%s (up %s)\n", formatTime(*v.StartedAt), formatDuration(time.Since(*v.StartedAt)))
	}
	fmt.Fprintf(tw, "Downloads:\t%d active, %d queued\n", v.Active, v.Queued)
	if v.Polite != nil {
		fmt.Fprintf(tw, "Polite mode:\t%s\n", politeStatusText(v.Polite))
	}
	return tw.Flush()
}

func endpointOrDefault(reported, dialed string) string {
	if reported != "" {
		return reported
	}
	return dialed
}

// listDaemon returns the downloads of a running daemon; ok is false when none is
// running, so that the caller reads the database instead.
func (a *app) listDaemon(ctx context.Context, t *daemonTarget) (views []downloadView, ok bool, err error) {
	c, err := a.connectIfRunning(ctx, t)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		a.con.printf("warning: %v; reading the database instead\n", err)
		return nil, false, nil
	}
	if c == nil {
		return nil, false, nil
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Downloads.List(cctx, connect.NewRequest(&downexv1.ListRequest{}))
	if err != nil {
		return nil, false, rpcError("ls", err)
	}
	views = make([]downloadView, 0, len(resp.Msg.GetDownloads()))
	for _, d := range resp.Msg.GetDownloads() {
		views = append(views, rpcView(d))
	}
	return views, true, nil
}

// rpcView converts a daemon download to the view printed by ls.
func rpcView(d *downexv1.Download) downloadView {
	v := downloadView{
		ID: model.DownloadID(d.GetId()), URL: d.GetUrl(), FinalURL: d.GetFinalUrl(),
		Dir: d.GetDir(), Filename: d.GetFilename(), Path: rpcPath(d),
		State: rpcStateName(d.GetState()), Mode: rpcMode(d.GetMode()).String(),
		Size: d.GetSize(), SizeApprox: d.GetSizeApprox(), Downloaded: d.GetReceived(),
		MaxConnections: int(d.GetMaxConnections()), SpeedLimit: d.GetSpeedLimit(),
		Error: d.GetError(), ErrorCode: d.GetErrorCode(), PageURL: d.GetPageUrl(),
		SpeedBps: d.GetSpeedBps(), Connections: int(d.GetConnections()), QueuePosition: int(d.GetQueuePosition()),
		Category: d.GetCategory(), ExpectedSize: d.GetExpectedSize(), MIME: d.GetMime(), Kind: model.KindFile.String(),
	}
	if d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA {
		v.Kind, v.Media = model.KindMedia.String(), newMediaView(d.GetMedia())
	}
	switch d.GetSource() {
	case downexv1.AddSource_ADD_SOURCE_CLI:
		v.Source = model.SourceCLI.String()
	case downexv1.AddSource_ADD_SOURCE_UI:
		v.Source = model.SourceUI.String()
	case downexv1.AddSource_ADD_SOURCE_BROWSER:
		v.Source = model.SourceBrowser.String()
	case downexv1.AddSource_ADD_SOURCE_CLIPBOARD:
		v.Source = "clipboard"
	}
	v.QueueID = d.GetQueueId()
	v.FileCheck = rpcFileCheckView(d.GetFileCheck())
	if ts := d.GetCreatedAt(); ts != nil {
		v.CreatedAt = ts.AsTime()
	}
	if ts := d.GetUpdatedAt(); ts != nil {
		v.UpdatedAt = ts.AsTime()
	}
	if ts := d.GetCompletedAt(); ts != nil {
		t := ts.AsTime()
		v.CompletedAt = &t
	}
	return v
}

// rpcState maps a daemon state to the domain state; ok is false for values this
// build does not know.
func rpcState(s downexv1.DownloadState) (st model.State, ok bool) {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
		return model.StateQueued, true
	case downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return model.StateProbing, true
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:
		return model.StateDownloading, true
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return model.StatePaused, true
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return model.StateCompleted, true
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return model.StateFailed, true
	case downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return model.StateCanceled, true
	case downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION:
		return model.StateAwaitingConfirmation, true
	}
	return 0, false
}

// rpcStateName names a daemon state as ls and info name stored states.
func rpcStateName(s downexv1.DownloadState) string {
	if st, ok := rpcState(s); ok {
		return st.String()
	}
	if s == downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED {
		return "unknown"
	}
	return fmt.Sprintf("state(%d)", int32(s))
}

func rpcMode(m downexv1.Mode) model.Mode {
	switch m {
	case downexv1.Mode_MODE_RANGED:
		return model.ModeRanged
	case downexv1.Mode_MODE_SINGLE:
		return model.ModeSingle
	}
	return model.ModeUnknown
}

// rpcPath is the file of a daemon download on disk (see downloadPath); "" while it
// awaits confirmation (its name is only proposed).
func rpcPath(d *downexv1.Download) string {
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		return ""
	}
	return downloadPath(d.GetDir(), d.GetFilename(), d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED)
}

// describeState names d's state for messages, with its queue position when queued.
func describeState(d *downexv1.Download) string {
	name := rpcStateName(d.GetState())
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_QUEUED && d.GetQueuePosition() > 0 {
		return name + ", position " + strconv.Itoa(int(d.GetQueuePosition())) + " in the queue"
	}
	return name
}

// rpcHeaders converts request headers to the daemon's form, sorted by name.
func rpcHeaders(h http.Header) []*downexv1.Header {
	var out []*downexv1.Header
	for _, k := range sortedKeys(h) {
		for _, v := range h[k] {
			out = append(out, &downexv1.Header{Name: k, Value: v})
		}
	}
	return out
}

// printRemoved reports a removed download whose file was (or is) at path.
func (a *app) printRemoved(id model.DownloadID, path string, deleted bool) {
	shown := printable(path)
	switch {
	case path == "":
		fmt.Fprintf(a.stdout, "Removed download %d.\n", id)
	case deleted:
		fmt.Fprintf(a.stdout, "Removed download %d and deleted %s.\n", id, shown)
	default:
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(a.stdout, "Removed download %d; kept %s.\n", id, shown)
		} else {
			fmt.Fprintf(a.stdout, "Removed download %d.\n", id)
		}
	}
}

// errStreamClosed reports a Watch stream that the daemon ended.
var errStreamClosed = errors.New("downexd closed the event stream (it may have stopped)")
