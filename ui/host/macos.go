package host

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	"downex/internal/client"
)

// The macOS rules of the window (docs/MACOS-SPEC.md §5). They are plain functions of
// names, paths and command output, compiled on every system so that they are tested on
// every system; shell_darwin.go applies them. Paths here are macOS paths: slash-separated
// (package path, not path/filepath).

// macProgramExtensions are files and bundles (folders that the Finder shows as one item)
// that macOS runs, installs or loads when they are opened, beyond the shared
// programExtensions (app, command, pkg, dmg, jar, the shell scripts …). The lists hold
// extensions in lower case, without the dot.
var (
	macProgramExtensions = extensionSet(
		// scripts that Terminal runs, and Terminal settings files, which can run a command
		"tool tcsh terminal",
		// AppleScript and Automator (opening a workflow or an action can install it)
		"scpt scptd applescript workflow action",
		// installer packages, and bundles that System Settings installs when opened
		"mpkg prefpane saver wdgt",
		// configuration profiles, and iPhone and iPad apps (which Macs with Apple silicon run)
		"mobileconfig ipa",
		// Finder locations of local files, which open that file (a program, say)
		"fileloc",
	)
	// macContentExtensions connect to a computer they name, mount a disk image, or run
	// the actions they contain.
	macContentExtensions = extensionSet(
		// Finder internet, server and screen-sharing locations (a URL may also start an app)
		"webloc inetloc afploc ftploc vncloc",
		// disk images (iso, img and dmg are in the shared lists)
		"sparseimage sparsebundle cdr",
		// Shortcuts, whose actions include shell scripts
		"shortcut",
	)
)

// macRisk is osRisk on macOS. The name decides for bundles as well as for files: an app,
// an installer or a workflow is a folder, so a folder named "Setup.app" is a program
// (programExtensions). A regular file with an execute permission bit is a program too:
// macOS opens it in Terminal, which runs it.
func macRisk(ext string, info fs.FileInfo) Risk {
	switch {
	case macProgramExtensions[ext]:
		return RiskProgram
	case info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0:
		return RiskProgram
	case macContentExtensions[ext]:
		return RiskContent
	}
	return RiskNone
}

// macOpensAsItem reports a folder that the Finder may open as an item instead of showing
// what it contains: macOS takes a folder for a bundle (an app, an installer package, a
// document) by the extension of its name, and opening a bundle can run it. Open folder
// selects such a folder in its parent instead of opening it.
func macOpensAsItem(dir string) bool {
	return path.Ext(path.Base(dir)) != ""
}

// interfaceStyleIsDark reports the output of "defaults read -g AppleInterfaceStyle": "Dark"
// while the system uses the dark appearance (also when it switches automatically); the
// value is missing (an error, no output) in the light one.
func interfaceStyleIsDark(out []byte) bool {
	return strings.EqualFold(string(bytes.TrimSpace(out)), "Dark")
}

// openMessageMax bounds the part of what /usr/bin/open printed that an error repeats.
const openMessageMax = 300

// openToolFailure maps a failure (err) of /usr/bin/open for target, which printed stderr.
// Launch Services finding no app for the file's type (kLSApplicationNotFoundErr, -10814)
// is failed_precondition, as a missing association is on Windows; any other failure
// repeats the first line that open printed.
func openToolFailure(target string, stderr []byte, err error) error {
	text := strings.TrimSpace(string(bytes.ToValidUTF8(stderr, []byte("�"))))
	if strings.Contains(text, "kLSApplicationNotFoundErr") || strings.Contains(text, "Code=-10814") ||
		strings.Contains(text, "No application knows how to open") {
		kind := "this type of file"
		if ext := strings.ToLower(path.Ext(path.Base(target))); ext != "" {
			kind = ext + " files"
		}
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no app is set to open %s on this Mac", kind))
	}
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if len(line) > openMessageMax {
		cut := openMessageMax
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "…"
	}
	if line == "" {
		return fmt.Errorf("/usr/bin/open: %w", err)
	}
	return fmt.Errorf("%s (/usr/bin/open: %w)", line, err)
}

// headBuffer keeps the first max bytes written to it and drops the rest, so that a
// command's output cannot grow without bound.
type headBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *headBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Bytes returns what was kept.
func (b *headBuffer) Bytes() []byte { return b.buf.Bytes() }

// handoffBundles are the app bundles of the installation l that a Downex window lets
// take the focus from it (AllowForegroundHandoff): Downex.app and its progress app.
// Outside a bundle there are none (nothing is opened through Launch Services).
func handoffBundles(l client.Layout) []string {
	if l.Bundle == "" {
		return nil
	}
	return []string{l.Bundle, path.Join(l.Dir, client.MacProgressApp)}
}
