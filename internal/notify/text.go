package notify

import (
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Length bounds of the texts, in runes. The tray truncates again to its own limits.
const (
	maxNameRunes  = 120
	maxErrorRunes = 120
)

// Titles of the notifications.
const (
	titleCompleted = "Download complete"
	titleFailed    = "Download failed"
	titleConfirm   = "Confirm download"
)

// Clean returns s fit to show in a notification or tooltip: control characters,
// format characters (which include the bidirectional controls that can make a name
// read backwards, such as U+202E, and zero-width characters) and invalid UTF-8 are
// removed; line breaks and tabs become spaces; runs of white space collapse to one
// and the result is trimmed.
func Clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size <= 1:
			// invalid UTF-8: dropped
		case r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029:
			b.WriteByte(' ')
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// truncate shortens s to at most n runes, ending a shortened text with "…".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return strings.TrimRightFunc(string(runes[:n-1]), unicode.IsSpace) + "…"
}

// DisplayName is the name a notification shows for d: its file name, else the last
// segment of its URL's path, else the URL's host, else "Download <id>"; cleaned
// (see Clean) and bounded. It never includes a URL's query or fragment.
func DisplayName(d *downexv1.Download) string {
	if name := truncate(Clean(d.GetFilename()), maxNameRunes); name != "" {
		return name
	}
	if name := truncate(Clean(urlName(d.GetUrl())), maxNameRunes); name != "" {
		return name
	}
	return fmt.Sprintf("Download %d", d.GetId())
}

// urlName returns the last segment of raw's path, or its host when the path has none.
func urlName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if seg := path.Base(u.Path); seg != "/" && seg != "." && seg != "" {
		return seg
	}
	return u.Hostname()
}

// shortErrors are the texts for the daemon's stable failure codes (docs/DAEMON-SPEC.md §6).
var shortErrors = map[string]string{
	"disk_full":         "The disk is full.",
	"tls":               "The secure connection failed.",
	"network":           "A network error interrupted the download.",
	"validator_changed": "The file changed on the server; restart the download.",
	"stalled":           "The transfer stalled.",
	"invalid_request":   "The request could not be sent.",
}

var (
	// statusPattern finds an HTTP status (and its standard text) in an error message.
	statusPattern = regexp.MustCompile(`HTTP status (\d{3})(?: ([A-Za-z][A-Za-z' -]*[A-Za-z]))?`)
	// urlPattern finds absolute URLs in an error message.
	urlPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s"'<>]+`)
)

// ShortError is the failure text of a download with the given error code and message.
// Known codes get a fixed sentence; an HTTP status failure names the status. Otherwise
// it is the message's first line with every URL reduced to its scheme and host (a
// URL's path or query may carry a token), cleaned and bounded.
func ShortError(code, message string) string {
	if s, ok := shortErrors[code]; ok {
		return s
	}
	if code == "http_status" {
		if m := statusPattern.FindStringSubmatch(message); m != nil {
			status := m[1]
			if m[2] != "" {
				status += " " + m[2]
			}
			return "The server answered HTTP " + status + "."
		}
		return "The server's response could not be used."
	}
	line, _, _ := strings.Cut(message, "\n")
	line = urlPattern.ReplaceAllStringFunc(line, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "<url>"
		}
		return u.Scheme + "://" + u.Host
	})
	if s := truncate(Clean(line), maxErrorRunes); s != "" {
		return s
	}
	return "Unknown error."
}

// Tooltip is the tray tooltip for s: "Downex — N active, X/s".
func Tooltip(s Summary) string {
	return fmt.Sprintf("Downex — %d active, %s/s", s.Active, FormatBytes(s.Speed))
}

// FormatBytes formats n bytes with decimal units (1 KB = 1000 B), like the CLI: "512 B",
// "1.5 KB", "12.3 MB"; a value that would round to 1000.0 of a unit is shown in the next
// one ("1.0 MB", never "1000.0 KB"). Negative and non-finite values count as 0.
func FormatBytes(n float64) string {
	if !(n > 0) || math.IsInf(n, 0) {
		n = 0
	}
	if r := math.Round(n); r < 1000 {
		return fmt.Sprintf("%d B", int64(r))
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	v := n / 1000
	i := 0
	for math.Round(v*10) >= 10000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
