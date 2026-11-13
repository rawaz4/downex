package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/store"
	"downex/internal/transport"
)

// Exit statuses.
const (
	exitOK          = 0
	exitError       = 1
	exitInterrupted = 130 // 128 + SIGINT, as shells report an interrupted command
)

// version is printed by "downex --version"; release builds set it with
// -ldflags "-X main.version=<version>".
var version = "dev"

const usage = `Downex: segmented, resumable downloads.

Usage:
  downex add <url> [-o dir] [-c connections] [--limit rate] [-H "Name: value"]... [--name file] [--queue q]
             [--paused] [--wait]
  downex media <manifest-url> [--list [--json]] [--video ID] [--audio ID] [-o dir] [--name file]
               [-H "Name: value"]... [--queue q] [--paused] [--wait]
  downex media --site <page-url> [--list [--json]] [--video ID] [--audio ID] [-o dir] [--name file]
               [-H "Cookie: ..."] [-H "Referer: url"] [--queue q] [--paused] [--wait]
  downex ls [--json]
  downex watch [id...]
  downex pause <id>
  downex resume <id> [--restart [--replace]] [-c connections] [--limit rate] [--wait]
  downex set <id> [--limit rate] [-c connections]
  downex rm <id> [--delete-file]
  downex confirm <id> [-o dir] [--name file] [--video ID] [--audio ID] [--queue q] [--later] [--wait]
  downex queue ls|create|update|rm|start|stop|move|schedule ...   (downex help queue)
  downex cancel-post <queue>
  downex proxy [show] | set system|none|manual|pac ...           (downex help proxy)
  downex login [ls] | add <url-pattern> --user name | rm <id>    (downex help login)
  downex update check|install [--json]
  downex tools [status] [--json] | install | update
  downex settings [--max-concurrent n] [--global-limit rate] [--dir path] [--connections n]
                  [--categories-folders on|off] [--confirm-browser on|off] [--notify-complete on|off]
                  [--notify-failure on|off] [--launch-at-login on|off] [--tray on|off]
                  [--progress-window on|off] [--complete-dialog on|off] [--clipboard on|off]
                  [--mark-files on|off] [--av-scan on|off] [--av-command path] [--av-args args]
                  [--update-feed url] [--auto-update on|off] [--site-media on|off]
                  [--tools-auto-update on|off] [--tools-channel stable|nightly] [--json]
  downex daemon start|stop|status [--json]

  downex get <url> [-o dir] [-c connections] [--limit rate] [-H "Name: value"]... [--name file] [--restart]
  downex resume <id> --local [--restart [--replace]] [-c connections] [--limit rate]
  downex rm <id> --local [--delete-file]
  downex info <id> [--json]
  downex help [command]
  downex --version

add, media, ls, watch, pause, resume, set, rm, confirm, queue, cancel-post, proxy, login, update,
tools, settings and daemon talk to downexd, the per-user background process that runs downloads. They
start it when it is not running (it is looked up next to downex, in Downex.app the menu-bar agent
first, or at $DOWNEX_DAEMON), except ls, which then reads the download database directly, and
"daemon stop" and "daemon status". info reads the database and adds the live connections of a
download that a running downexd runs.

Every download belongs to a queue (--queue takes a queue id or name; default: the main queue,
which always runs). Other queues start their downloads only while they run: "downex queue start",
or their schedule.

media hands an HLS (.m3u8) or DASH (.mpd) stream to downexd, which saves its video and audio
as one .mp4 file (MPEG-TS streams are converted); --list shows the tracks that can be saved as
MP4. Encrypted and live streams, and streams without an MP4 video, are refused.
With --site the URL is a web page (YouTube and the other sites yt-dlp understands): downexd
resolves it with yt-dlp, downloaded with Deno on first use ("downex tools" shows them), and
saves the video as one .mp4; --list shows one MP4 video per quality (4K, 1080p, ...) and
--video picks one.

get runs one download in the foreground without downexd, for scripting; resume and rm
with --local do the same for an existing download (only while downexd is not running).

Global flags (before the command; the commands accept them too):
  --db path        download database (default: <user config directory>/Downex/downex.db);
                   an autostarted downexd opens the same one
  --endpoint path  downexd endpoint, a named pipe (Windows) or unix socket (default:
                   per user, or $DOWNEX_ENDPOINT)

get and resume --local also accept --proxy (direct, http://host:port or socks5://host:port;
default: the HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment) and -v for debug messages.

Rates are bytes per second with optional decimal suffixes: 500K (500 KB/s = 500,000 B/s),
1.5M, 2G (0 = unlimited); KiB, MiB and GiB are binary (512KiB = 524,288 B/s).
Sizes and speeds are shown in decimal units (1 KB = 1,000 bytes, 1 MB = 1,000 KB).
Ctrl+C pauses a "get" download and saves its progress; "downex resume <id> --local"
continues it. Ctrl+C in watch, add --wait or resume --wait only stops watching:
downexd keeps downloading.

Exit status: 0 success, 1 error, 130 interrupted (Ctrl+C or SIGTERM).
`

// globalFlags are the flags accepted before the command name. Commands use them as
// the defaults of their own --db and --endpoint flags.
type globalFlags struct {
	db       string // --db: download database path
	endpoint string // --endpoint: daemon endpoint, overriding ipc.Endpoint
}

// app holds the output streams and services of one CLI invocation.
type app struct {
	stdout   io.Writer // command results
	stderr   io.Writer // progress, warnings and errors
	usageOut io.Writer // flag usage: stdout for "help <command>", else stderr
	con      *console
	sys      system // daemon endpoint, process spawning, terminal; faked by tests
	global   globalFlags
	// Site media (docs/SITE-MEDIA-SPEC.md §3.7); tests shorten them.
	retryEvery time.Duration // between the tries of a page while yt-dlp installs or the page resolves
	retryFor   time.Duration // how long a page is tried
	toolsFor   time.Duration // how long "tools install|update" follows the install
	toolsPoll  time.Duration // between the progress reads of "tools install|update"
}

// Site-media timings of the CLI (docs/SITE-MEDIA-SPEC.md §3.7).
const (
	siteRetryEvery = 2 * time.Second
	siteRetryFor   = 5 * time.Minute
	toolsFollowFor = 15 * time.Minute
	toolsPollEvery = time.Second
)

// run executes the command line args (without the program name) and returns the
// exit status. ctx cancellation (Ctrl+C) pauses a running download.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return newApp(stdout, stderr, defaultSystem()).run(ctx, args)
}

// newApp returns an app writing to stdout and stderr that uses sys for everything
// beyond those streams.
func newApp(stdout, stderr io.Writer, sys system) *app {
	return &app{
		stdout: stdout, stderr: stderr, usageOut: stderr, con: newConsole(stderr), sys: sys,
		retryEvery: siteRetryEvery, retryFor: siteRetryFor, toolsFor: toolsFollowFor, toolsPoll: toolsPollEvery,
	}
}

// run executes one command line and returns its exit status.
func (a *app) run(ctx context.Context, args []string) int {
	if len(args) == 1 && isVersionFlag(args[0]) {
		fmt.Fprintf(a.stdout, "downex %s\n", version)
		return exitOK
	}
	args, err := a.parseGlobal(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(a.stdout, usage)
		return exitOK
	case err != nil:
		fmt.Fprintf(a.stderr, "downex: %v\nRun 'downex help' for usage.\n", err)
		return exitError
	}
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return exitError
	}
	name, rest := args[0], args[1:]
	if isHelpFlag(name) {
		if len(rest) == 0 {
			fmt.Fprint(a.stdout, usage)
			return exitOK
		}
		name, rest, a.usageOut = rest[0], []string{"-h"}, a.stdout
	}
	cmd := a.command(name)
	if cmd == nil {
		fmt.Fprintf(a.stderr, "downex: unknown command %q\nRun 'downex help' for usage.\n", name)
		return exitError
	}
	return a.exitCode(ctx, cmd(ctx, rest))
}

// isVersionFlag reports the forms of "print the version": --version as the other Downex
// programs take it, -version as Go's flag package spells it, and "version" as downex-nmhost's.
func isVersionFlag(s string) bool {
	switch s {
	case "version", "-version", "--version":
		return true
	}
	return false
}

func isHelpFlag(s string) bool {
	switch s {
	case "help", "-h", "-help", "--help":
		return true
	}
	return false
}

// parseGlobal parses the global flags that precede the command name and returns the
// remaining arguments.
func (a *app) parseGlobal(args []string) ([]string, error) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "-") || isHelpFlag(args[0]) {
		return args, nil
	}
	fs := flag.NewFlagSet("downex", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the error is reported by run
	fs.StringVar(&a.global.db, "db", "", "")
	fs.StringVar(&a.global.endpoint, "endpoint", "", "")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func (a *app) command(name string) func(context.Context, []string) error {
	switch name {
	case "get":
		return a.get
	case "add":
		return a.add
	case "media":
		return a.media
	case "resume":
		return a.resume
	case "pause":
		return a.pause
	case "ls", "list":
		return a.ls
	case "watch":
		return a.watch
	case "info":
		return a.info
	case "rm", "remove":
		return a.rm
	case "confirm":
		return a.confirm
	case "set":
		return a.set
	case "settings":
		return a.settings
	case "daemon":
		return a.daemon
	case "queue", "queues":
		return a.queue
	case "cancel-post":
		return a.cancelPost
	case "proxy":
		return a.proxy
	case "login", "logins":
		return a.login
	case "update":
		return a.update
	case "tools":
		return a.tools
	}
	return nil
}

// exitCode reports err (unless it was already reported) and maps it to an exit status.
func (a *app) exitCode(ctx context.Context, err error) int {
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return exitOK
	case interrupted(ctx, err):
		return exitInterrupted
	}
	var (
		silent silentError
		ue     usageError
	)
	switch {
	case errors.As(err, &silent):
	case errors.As(err, &ue):
		a.con.printf("downex %s: %s\nRun 'downex help %s' for usage.\n", ue.cmd, ue.msg, ue.cmd)
	default:
		a.con.printf("downex: error: %v\n", err)
	}
	return exitError
}

// interrupted reports whether err is the cancellation of ctx (Ctrl+C or SIGTERM),
// including a daemon call that was canceled by it.
func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		connect.CodeOf(err) == connect.CodeCanceled)
}

// silentError wraps an error whose message has already been printed.
type silentError struct{ err error }

func (e silentError) Error() string { return e.err.Error() }
func (e silentError) Unwrap() error { return e.err }

// usageError is a malformed command line.
type usageError struct{ cmd, msg string }

func (e usageError) Error() string { return e.cmd + ": " + e.msg }

// flagSet returns a flag set for a command whose usage starts with synopsis.
func (a *app) flagSet(name, synopsis, about string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.usageOut)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: downex %s\n\n%s\n\nFlags:\n", synopsis, about)
		fs.PrintDefaults()
	}
	return fs
}

const dbUsage = "download database `path` (default: <user config dir>/Downex/downex.db)"

// dbFlag registers --db, defaulting to the global --db.
func (a *app) dbFlag(fs *flag.FlagSet) *string {
	return fs.String("db", a.global.db, dbUsage)
}

// daemonTarget selects the daemon a command talks to.
type daemonTarget struct {
	endpoint string // --endpoint, or "" for the per-user default
	db       string // --db: passed to an autostarted daemon (and read by ls without one)
}

// daemonFlags registers --endpoint and --db, defaulting to the global flags.
func (a *app) daemonFlags(fs *flag.FlagSet) *daemonTarget {
	t := &daemonTarget{}
	fs.StringVar(&t.db, "db", a.global.db, dbUsage+";\nan autostarted downexd opens it")
	fs.StringVar(&t.endpoint, "endpoint", a.global.endpoint,
		"downexd endpoint `path`: a named pipe (Windows) or unix socket (default: per user, or $"+envEndpoint+")")
	return t
}

// netFlags are the flags of commands that run downloads in this process.
type netFlags struct {
	proxy   string
	verbose bool
}

func (n *netFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&n.proxy, "proxy", "", "proxy `url`: direct, http://[user:pass@]host:port or socks5://[user:pass@]host:port\n(default: the HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment)")
	fs.BoolVar(&n.verbose, "v", false, "print debug messages (connections, splits, retries)")
}

// openStore opens the database at dbFlag, or the per-user default when it is empty,
// and returns it with its absolute path.
func openStore(dbFlag string) (*store.SQLite, string, error) {
	path := dbFlag
	if path == "" {
		p, err := store.DefaultPath()
		if err != nil {
			return nil, "", err
		}
		path = p
	} else {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, "", fmt.Errorf("database path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return nil, "", fmt.Errorf("create database directory: %w", err)
		}
		path = abs
	}
	st, err := store.OpenSQLite(path)
	if err != nil {
		return nil, "", fmt.Errorf("open database %s: %w", path, err)
	}
	return st, path, nil
}

// newEngine returns an engine persisting to st that fetches through the real HTTP
// transport and reports its warnings (and, with -v, debug events) on the console.
func (a *app) newEngine(st store.Store, n netFlags) (*engine.Engine, error) {
	opts := transport.DefaultHTTPOptions()
	opts.Proxy = n.proxy
	tf, err := transport.NewHTTPFactory(opts)
	if err != nil {
		return nil, err
	}
	level := slog.LevelWarn
	if n.verbose {
		level = slog.LevelDebug
	}
	return engine.New(st, tf, engine.Options{Logger: slog.New(newConsoleHandler(a.con, level))}), nil
}
