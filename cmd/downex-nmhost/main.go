// Command downex-nmhost is the native-messaging relay between the Downex browser
// extension and downexd (docs/BROWSER-SPEC.md §2). The browser starts it with piped
// stdin/stdout and exchanges length-prefixed JSON messages with it; it validates every
// request with the daemon's own limits (docs/DAEMON-SPEC.md §4), starts downexd when an
// add needs it, and forwards adds to DownloadService.Add (streams, "addMedia", to
// DownloadService.AddMedia) over the per-user endpoint. The videos of web pages
// (docs/SITE-MEDIA-SPEC.md §3.5) go to the same services with origin SITE:
// "probeVideo" to DownloadService.ProbeMedia and "addVideo" to DownloadService.AddMedia,
// on a pool of their own so that slow page lookups never hold up capture adds; the
// welcome names the "video" feature.
//
// Usage:
//
//	downex-nmhost [browser arguments...]      relay mode (started by the browser)
//	downex-nmhost register [flags]             register the host with the browsers
//	downex-nmhost unregister [flags]           remove that registration
//	downex-nmhost version
//
// In relay mode the arguments browsers pass (the caller's origin, a parent window
// handle, the manifest path, the add-on ID) are ignored. stdout carries only protocol
// messages; the log goes to nmhost.log in the per-user log directory.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

// version is reported in the log and by "version"; release builds set it with
// -ldflags "-X main.version=<version>".
var version = "dev"

// DefaultChromeExtensionIDs are the Chromium extension IDs that "register" allows when no
// --chrome-extension-id is given, separated by commas. Builds set them with
// -ldflags "-X main.DefaultChromeExtensionIDs=<id>,<id>" from ext/EXTENSION_ID.txt (one ID
// per line): the extension of this repository and its store listings, because the Chrome
// Web Store and Edge Add-ons each give the same extension an ID of their own. When it is
// empty, register requires the flag; an invalid ID in it fails register rather than being
// skipped, so a broken build never registers fewer IDs than it names.
var DefaultChromeExtensionIDs = ""

// Exit statuses.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `downex-nmhost: native-messaging relay between the Downex browser extension and downexd.

Usage:
  downex-nmhost register [--chrome-extension-id ID[,ID]]... [--firefox-extension-id ID[,ID]]...
                         [--browsers chrome,edge,chromium,brave,firefox] [--system] [--keep] [--dry-run]
  downex-nmhost unregister [--browsers list] [--system] [--dry-run]
  downex-nmhost version

Browsers start downex-nmhost themselves once it is registered; run "register" once after
installing it next to downexd. "downex-nmhost <command> -h" describes a command's flags.
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run executes one command line (without the program name) and returns the exit status.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "register":
			return runRegister(args[1:], stdout, stderr)
		case "unregister":
			return runUnregister(args[1:], stdout, stderr)
		case "version", "--version", "-version":
			fmt.Fprintf(stdout, "downex-nmhost %s\n", version)
			return exitOK
		case "help", "-h", "-help", "--help":
			fmt.Fprint(stdout, usage)
			return exitOK
		}
	}
	// Relay mode. Chromium passes the caller's origin (and on Windows --parent-window=N),
	// Firefox the manifest path and the add-on ID: none of them is needed.
	return runRelay(ctx, stdin, stdout, caller(args))
}

// caller returns the browser-supplied identity of the extension for the log: the
// chrome-extension:// origin, or Firefox's add-on ID (its second argument).
func caller(args []string) string {
	for i, a := range args {
		switch {
		case len(a) > len("chrome-extension://") && a[:len("chrome-extension://")] == "chrome-extension://":
			return truncate(a, 128)
		case i == 1 && len(args) == 2:
			return truncate(a, 128)
		}
	}
	return ""
}
