// Package about holds the texts of Settings → About (DESIGN-SYSTEM.md §4.9.1), ported from
// ui/frontend/src/lib/about.ts: what this copy of Downex is, whether updates can work, the
// diagnostics' last guard and the open-source notices. Pure functions; the page shows them.
package about

import (
	"regexp"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/text"
)

// Build is what the window knows of its own build (ui/about.go buildInfo).
type Build struct {
	Version string
	OS      string // runtime.GOOS
	Arch    string // runtime.GOARCH
	// GoVersion is runtime.Version(), "go1.27.0".
	GoVersion string
	// WailsVersion is the Wails module's version ("v2.17.0"); "" for a window built without it.
	WailsVersion string
	Commit       string // vcs.revision
	CommitTime   string // vcs.time, RFC 3339
	Modified     bool   // vcs.modified: built with uncommitted changes
}

// OSLabel names the system as people know it: "Windows x64", "macOS (Apple silicon)",
// "Linux x64".
func OSLabel(os, arch string) string {
	switch os {
	case "windows":
		switch arch {
		case "arm64":
			return "Windows on Arm"
		case "amd64":
			return "Windows x64"
		}
		return "Windows " + arch
	case "darwin":
		switch arch {
		case "arm64":
			return "macOS (Apple silicon)"
		case "amd64":
			return "macOS (Intel)"
		}
		return "macOS " + arch
	case "linux":
		switch arch {
		case "amd64":
			return "Linux x64"
		case "arm64":
			return "Linux ARM64"
		}
		return "Linux " + arch
	}
	return os + "/" + arch
}

// BuildLines says what the window is built with: Go, Wails, and the commit when the build has
// one.
func BuildLines(b Build) []string {
	var lines []string
	if b.GoVersion != "" {
		lines = append(lines, "Go "+strings.TrimPrefix(b.GoVersion, "go"))
	}
	if b.WailsVersion != "" {
		lines = append(lines, "Wails "+strings.TrimPrefix(b.WailsVersion, "v"))
	}
	if b.Commit != "" {
		c := b.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		line := "Commit " + c
		if b.Modified {
			line += " (with uncommitted changes)"
		}
		lines = append(lines, line)
	}
	return lines
}

// VersionsDiffer reports that the window and downexd are different versions (an update that is
// not finished, or a stray downexd); false when either is not known.
func VersionsDiffer(window, daemon string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	w, d := norm(window), norm(daemon)
	if w == "" || d == "" {
		return false
	}
	return w != d
}

// Updates is whether Downex can update itself, and the sentence that says so.
type Updates struct {
	On   bool
	Text string
}

// UpdatesState says whether Downex can update itself: not in a build without a signing key
// (downexd's updates_supported), not without an update address (the saved settings), else how
// it checks. status nil: downexd could not be asked.
//
// downex.v1 has no absent bool: a downexd that does not say reads as unsupported, as today's
// bridge (wails.ts bool()) reads it.
func UpdatesState(status *downexv1.StatusResponse, feedURL string, autoCheck bool) Updates {
	if status == nil {
		return Updates{Text: "Not known while downexd is not reachable"}
	}
	if !status.GetUpdatesSupported() {
		return Updates{Text: "Off in this build (no signing key)"}
	}
	if strings.TrimSpace(feedURL) == "" {
		return Updates{Text: "Off (no update address)"}
	}
	if autoCheck {
		return Updates{On: true, Text: "On · checked every day"}
	}
	return Updates{On: true, Text: "On · checked when you ask"}
}

// ScrubDiagnostics is the diagnostics as copied: the host already leaves out the endpoint,
// addresses, names, logins and the proxy; what the page knows must stay private (the IPC
// endpoint, the update address) is hidden here once more, should a host ever let it through.
// Values shorter than 4 characters (spaces aside) are not hidden.
func ScrubDiagnostics(text string, private []string) string {
	out := text
	for _, v := range private {
		if utf16Len(jsTrim(v)) < 4 {
			continue
		}
		out = strings.ReplaceAll(out, v, "(hidden)")
	}
	return out
}

// NoticeSection is one component of the notices: its title ("github.com/wailsapp/wails/v2
// v2.17.0") and its licence text.
type NoticeSection struct {
	Title string
	Body  string
}

// Notices is THIRD-PARTY-NOTICES.txt: its introduction, then one section per component.
type Notices struct {
	Intro    string
	Sections []NoticeSection
}

// BlockKind is the kind of a block of the notices' introduction.
type BlockKind uint8

const (
	Para BlockKind = iota
	Heading
	Item
)

// NoticeBlock is a block of the notices' introduction as the page shows it.
type NoticeBlock struct {
	Kind BlockKind
	Text string
}

// isRule reports a line of at least 10 "=" (then only white space).
func isRule(line string) bool {
	line = strings.TrimRightFunc(line, isJSSpace)
	return len(line) >= 10 && strings.Trim(line, "=") == ""
}

var (
	underline = regexp.MustCompile(`^[-=]{3,}$`)
	title     = regexp.MustCompile(`(?i)^third-party notices$`)
)

// lines splits text at line ends: \r\n, a lone \r and \n.
func lines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// IntroBlocks reflows the introduction for the page (the file is wrapped for a text editor):
// paragraphs, a line underlined with "-" or "=" as a heading, "- " lines as list items (their
// indented continuation joined). The file's own title ("THIRD-PARTY NOTICES") is left out: the
// view has one.
func IntroBlocks(intro string) []NoticeBlock {
	ls := lines(intro)
	if title.MatchString(jsTrim(ls[0])) {
		ls = ls[1:]
	}
	var blocks []NoticeBlock
	open := -1
	for _, raw := range ls {
		line := jsTrim(raw)
		switch {
		case line == "":
			open = -1
		case underline.MatchString(line):
			// a setext underline: the paragraph above it is a heading
			if open >= 0 && blocks[open].Kind == Para {
				blocks[open].Kind = Heading
			}
			open = -1
		case strings.HasPrefix(line, "- "):
			blocks = append(blocks, NoticeBlock{Kind: Item, Text: jsTrim(line[2:])})
			open = len(blocks) - 1
		case open >= 0:
			blocks[open].Text += " " + line
		default:
			blocks = append(blocks, NoticeBlock{Kind: Para, Text: line})
			open = len(blocks) - 1
		}
	}
	return blocks
}

// ParseNotices splits the notices file (scripts/third-party-notices.mjs) at its section
// headings: a title between two rules of "=". A file without them is all introduction.
func ParseNotices(text string) Notices {
	ls := lines(text)
	var intro []string
	type section struct {
		title string
		body  []string
	}
	var sections []section
	for i := 0; i < len(ls); i++ {
		line := ls[i]
		if isRule(line) && i+2 < len(ls) && isRule(ls[i+2]) && jsTrim(ls[i+1]) != "" {
			sections = append(sections, section{title: jsTrim(ls[i+1])})
			i += 2
			continue
		}
		if n := len(sections); n > 0 {
			sections[n-1].body = append(sections[n-1].body, line)
		} else {
			intro = append(intro, line)
		}
	}
	out := Notices{Intro: jsTrim(strings.Join(intro, "\n"))}
	for _, s := range sections {
		out.Sections = append(out.Sections, NoticeSection{Title: s.title, Body: jsTrim(strings.Join(s.body, "\n"))})
	}
	return out
}

// utf16Len is the length of s in UTF-16 code units (a JavaScript string's length).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// jsTrim is String.prototype.trim, isJSSpace what it removes.
var (
	jsTrim    = text.TrimJS
	isJSSpace = text.IsJSSpace
)
