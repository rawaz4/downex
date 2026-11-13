package sitejob

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"downex/internal/media"
	"downex/internal/sitemedia"
)

// maxReason bounds the user-visible reason taken from yt-dlp's stderr, in bytes.
const maxReason = 200

// classes maps phrases of yt-dlp's last ERROR: line to the error of their code
// (docs/SITE-MEDIA-SPEC.md §3.10), first match wins. The phrases are matched in lower
// case against the line without its "[extractor] id:" prefix and without URLs.
// Unavailable comes before sign-in: a private video's message also says "Sign in if
// you've been granted access", and most messages end with yt-dlp's advice about
// cookies.
var classes = []struct {
	err     error
	phrases []string
}{
	{media.ErrEncrypted, []string{"drm"}},
	{media.ErrLive, []string{"live event will begin", "premieres in", "is live"}},
	{sitemedia.ErrSiteUnavailable, []string{"private video", "video unavailable", "has been removed",
		"available in your country", "geo", "content isn't available", "try again later", "rate-limit",
		"http error 429", "no longer available", "terminated", "copyright"}},
	{sitemedia.ErrAuthRequired, []string{"sign in", "log in", "login required", "confirm your age", "age-restricted",
		"not a bot", "members-only", "join this channel", "premium", "cookies"}},
	{sitemedia.ErrSiteUnsupported, []string{"unsupported url"}},
}

// proxyLogin is the phrase of a proxy that asked yt-dlp for credentials it could not
// give (an NTLM/Negotiate proxy, or a login Downex does not have): site_error with
// reasonProxyLogin instead of yt-dlp's text.
const (
	proxyLogin       = "proxy authentication required"
	reasonProxyLogin = "the proxy needs a sign-in yt-dlp cannot do"
)

var (
	// extractorPrefix is the "[extractor] id: " that yt-dlp puts before a message
	// about one video.
	extractorPrefix = regexp.MustCompile(`^\[[^\]\s]{1,64}\]\s+[^\s:]{1,256}:\s*`)
	// urlText finds URLs in a message, which never reach the user (§6.1).
	urlText = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,16}://\S*`)
	// optionText finds a command-line option of yt-dlp ("--cookies", "-U").
	optionText = regexp.MustCompile(`(^|\s)--?[A-Za-z]`)
	// sentenceEnd ends a sentence of a message.
	sentenceEnd = regexp.MustCompile(`[.!?]\s+`)
)

// classify returns the error of a yt-dlp run that exited with code 1, or with 0
// without JSON (docs/SITE-MEDIA-SPEC.md §3.10): the last ERROR: line of stderr
// matched against classes, wrapped as "<sentinel>: <reason>". A run without an ERROR:
// line, or whose line matches nothing, is sitemedia.ErrSiteError.
func classify(stderr []byte, exitCode int) error {
	line, ok := errorLine(stderr)
	if !ok {
		return fmt.Errorf("%w: %s", sitemedia.ErrSiteError, fallbackReason(stderr, exitCode))
	}
	plain := strings.ToLower(spaces(urlText.ReplaceAllString(line, " ")))
	if strings.Contains(plain, proxyLogin) {
		return fmt.Errorf("%w: %s", sitemedia.ErrSiteError, reasonProxyLogin)
	}
	reason := cleanReason(line, true)
	if reason == "" {
		reason = fallbackReason(nil, exitCode)
	}
	for _, c := range classes {
		for _, p := range c.phrases {
			if strings.Contains(plain, p) {
				return fmt.Errorf("%w: %s", c.err, reason)
			}
		}
	}
	return fmt.Errorf("%w: %s", sitemedia.ErrSiteError, reason)
}

// errorLine returns the last line of stderr that starts with "ERROR:", without that
// prefix and without the "[extractor] id:" before the message.
func errorLine(stderr []byte) (string, bool) {
	lines := strings.Split(strings.ToValidUTF8(string(stderr), "�"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if rest, ok := strings.CutPrefix(l, "ERROR:"); ok {
			rest = strings.TrimSpace(rest)
			return extractorPrefix.ReplaceAllString(rest, ""), true
		}
	}
	return "", false
}

// lastLine returns the last line of stderr that is not empty and not a warning.
func lastLine(stderr []byte) string {
	lines := strings.Split(strings.ToValidUTF8(string(stderr), "�"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "WARNING:") {
			return l
		}
	}
	return ""
}

// fallbackReason describes a failed run without an ERROR: line: the last line yt-dlp
// wrote, else its exit code.
func fallbackReason(stderr []byte, exitCode int) string {
	if r := cleanReason(lastLine(stderr), false); r != "" {
		return r
	}
	if exitCode < 0 {
		return "it was stopped"
	}
	return fmt.Sprintf("it exited with code %d", exitCode)
}

// cleanReason makes a line of yt-dlp's stderr fit to show (docs/SITE-MEDIA-SPEC.md
// §3.10): URLs removed, bidi and control characters stripped, white space collapsed,
// at most maxReason bytes. With advice, the sentences after the first that name a URL
// or a command-line option (yt-dlp's advice, which Downex users cannot follow) are
// left out.
func cleanReason(line string, advice bool) string {
	if ss := sentences(line); advice && len(ss) > 1 {
		kept := ss[:1]
		for _, s := range ss[1:] {
			if !urlText.MatchString(s) && !optionText.MatchString(s) {
				kept = append(kept, s)
			}
		}
		line = strings.Join(kept, " ")
	}
	s := urlText.ReplaceAllString(line, " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c:
			return -1
		case unicode.IsControl(r) || r == utf8.RuneError:
			return ' '
		}
		return r
	}, s)
	s = strings.ReplaceAll(spaces(s), " ,", ",")
	return cut(strings.TrimRight(s, ": "), maxReason)
}

// sentences splits a message after each '.', '!' or '?' followed by white space,
// keeping the punctuation.
func sentences(s string) []string {
	var out []string
	last := 0
	for _, m := range sentenceEnd.FindAllStringIndex(s, -1) {
		out = append(out, strings.TrimSpace(s[last:m[1]]))
		last = m[1]
	}
	if rest := strings.TrimSpace(s[last:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// spaces collapses runs of white space into one space and trims the ends.
func spaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// cut returns at most n bytes of s, ending on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s[:n])
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(bytes.TrimRight(b, " "))
}
