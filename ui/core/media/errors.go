package media

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"connectrpc.com/connect"
)

// Refusals and errors (MEDIA-SPEC §6.3, SITE-MEDIA-SPEC §3.2, §5.5).

// apiCodes are the Connect codes as today's window names them.
var apiCodes = map[string]bool{
	"canceled": true, "unknown": true, "invalid_argument": true, "deadline_exceeded": true, "not_found": true,
	"already_exists": true, "permission_denied": true, "resource_exhausted": true, "failed_precondition": true,
	"aborted": true, "out_of_range": true, "unimplemented": true, "internal": true, "unavailable": true,
	"data_loss": true, "unauthenticated": true,
}

// apiError reads an error as today's window does (api.ts toApiError): a Connect error's code and
// message; else a text "<code>: <message>" with a known code; else "unknown" and the text.
func apiError(err error) (code, message string) {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code().String(), ce.Message()
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	if c, rest, ok := cutCode(text, 1, len(text)); ok && apiCodes[c] {
		if rest == "" {
			return c, text
		}
		return c, rest
	}
	if text == "" {
		text = "Unknown error"
	}
	return "unknown", text
}

// Refusal is why downexd refuses a stream (MEDIA-SPEC §2, §6.3).
type Refusal uint8

const (
	// NotRefused: the failure is something else.
	NotRefused Refusal = iota
	Encrypted
	Live
)

var (
	encryptedWord = regexp.MustCompile(`\b(encrypted|drm)\b`)
	liveWord      = regexp.MustCompile(`\blive\b`)
)

// MediaRefusal recognises downexd's refusal of a stream: invalid_argument naming the stable code
// "encrypted" (also DRM) or "live" in its message. Other failures are NotRefused.
func MediaRefusal(err error) Refusal {
	code, message := apiError(err)
	if code != "invalid_argument" {
		return NotRefused
	}
	m := jsLower(message)
	switch {
	case encryptedWord.MatchString(m):
		return Encrypted
	case liveWord.MatchString(m):
		return Live
	}
	return NotRefused
}

// RefusalText is what the window says of a refused stream ("" for NotRefused).
func RefusalText(r Refusal) string {
	switch r {
	case Encrypted:
		return "This stream is encrypted or DRM-protected. Downex does not download protected content."
	case Live:
		return "This is a live stream. Downex downloads only complete (on-demand) streams."
	}
	return ""
}

// sitePhrases are downexd's codes for video pages with the phrase its message carries after the
// code ("site_error: yt-dlp could not read this page: <reason>"; internal/sitemedia/errors.go).
var sitePhrases = map[string]string{
	"site_unsupported":  "no video found on this page",
	"site_unavailable":  "this video is not available",
	"auth_required":     "the site asks to sign in",
	"site_error":        "yt-dlp could not read this page",
	"site_resolving":    "still getting the video info",
	"tools_installing":  "downloading yt-dlp and Deno",
	"tools_unavailable": "yt-dlp and Deno are not set up",
	"site_media_off":    "video downloads from web pages are turned off (Settings → Video sites)",
}

// SiteTexts is what every UI says about a video page's code (SITE-MEDIA-SPEC §5.5).
var SiteTexts = map[string]string{
	"site_unsupported":  "Downex found no video on this page.",
	"site_unavailable":  "This video is not available (private, removed, blocked in your country, or the site asks to try again later).",
	"auth_required":     "The site wants you to sign in (age check, members-only or bot check).",
	"live":              "This is a live stream. Downex downloads only finished videos.",
	"encrypted":         "This video is DRM-protected. Downex does not download protected content.",
	"media_unsupported": "No MP4 video on this page.",
	"playlist":          "Open a single video of this playlist.",
	"media_invalid":     "The chosen quality is no longer offered. Add the video again.",
	"site_error":        "yt-dlp could not read this page",
	"tools_unavailable": "Downex could not set up yt-dlp/Deno",
	"site_media_off":    "Video downloads from web pages are turned off (Settings → Video sites).",
	"site_resolving":    "Getting video info…",
	"tools_installing":  "Downloading yt-dlp and Deno",
	"outdated":          "Update Downex to download videos.",
}

// cutCode reads /^([a-z_]{min,max}):\s*(.*)$/s (JavaScript's \s): a code of lower-case letters
// and underscores, a colon, white space, the rest. A code starting with "_" is one only for
// apiError's pattern (min 1 there); SplitCode's needs a letter first.
func cutCode(s string, min, max int) (code, rest string, ok bool) {
	colon := strings.IndexByte(s, ':')
	if colon < min || colon > max {
		return "", "", false
	}
	for i := 0; i < colon; i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c == '_') {
			return "", "", false
		}
	}
	return s[:colon], strings.TrimLeftFunc(s[colon+1:], isJSSpace), true
}

// SplitCode splits a downexd message into its stable code ("site_error") and the rest; code ""
// without one.
func SplitCode(message string) (code, rest string) {
	t := jsTrim(message)
	if c, r, ok := cutCode(t, 1, 64); ok && c[0] != '_' {
		return c, r
	}
	return "", t
}

// reasonOf is the reason after a code's phrase ("site_error: yt-dlp could not read this page:
// HTTP 500" → "HTTP 500").
func reasonOf(code, rest string) string {
	reason := jsTrim(rest)
	if phrase, ok := sitePhrases[code]; ok && strings.HasPrefix(jsLower(reason), jsLower(phrase)) {
		reason = sliceUTF16(reason, utf16Len(phrase))
	}
	return jsTrim(trimLeadingPunct(reason))
}

// trimLeadingPunct is .replace(/^[\s:;,.-]+/, "") with JavaScript's \s.
func trimLeadingPunct(s string) string {
	return strings.TrimLeftFunc(s, func(r rune) bool {
		return isJSSpace(r) || strings.ContainsRune(":;,.-", r)
	})
}

var sentenceEnd = regexp.MustCompile(`[.!?…]['")\]]*$`)

// closed is a reason ending its sentence: "HTTP 500" → "HTTP 500."; "" stays "".
func closed(text string) string {
	t := jsTrim(text)
	if t == "" || sentenceEnd.MatchString(t) {
		return t
	}
	return t + "."
}

var namePrefix = regexp.MustCompile(`^[a-z0-9]+[-.]`)

// sentence is a reason as a sentence of its own: "private video" → "Private video."; a name
// such as "yt-dlp" or "deno.exe" keeps its spelling.
func sentence(text string) string {
	t := closed(text)
	if namePrefix.MatchString(t) || t == "" {
		return t
	}
	return upperFirst(t)
}

// upperFirst is t.charAt(0).toUpperCase() + t.slice(1): the first UTF-16 unit in upper case
// (a letter outside the BMP stays as it is).
func upperFirst(t string) string {
	r := []rune(t)[0]
	n := len(string(r))
	switch {
	case r >= 0x10000:
		return t
	case r == 'ß':
		return "SS" + t[n:]
	}
	return string(unicode.ToUpper(r)) + t[n:]
}

// RetryKind is what a probe or add of a web page answered while it should be asked for again
// (SITE-MEDIA-SPEC §3.3).
type RetryKind uint8

const (
	// NoRetry: the answer is something else.
	NoRetry RetryKind = iota
	ToolsInstalling
	SiteResolving
)

// String is the kind as downexd's code names it.
func (k RetryKind) String() string {
	switch k {
	case ToolsInstalling:
		return "tools_installing"
	case SiteResolving:
		return "site_resolving"
	}
	return ""
}

// SiteRetry is an answer that asks for another try.
type SiteRetry struct {
	Kind RetryKind
	// Fraction is the install's progress 0..1 for ToolsInstalling, NaN when the message has
	// none.
	Fraction float64
}

var installPercent = regexp.MustCompile(`\((\d{1,3})%\)`)

// SiteRetryOf recognises tools_installing ("… (34%)") and site_resolving; Kind NoRetry for
// anything else.
func SiteRetryOf(err error) SiteRetry {
	code, message := apiError(err)
	if code != "failed_precondition" {
		return SiteRetry{}
	}
	c, rest := SplitCode(message)
	switch c {
	case "site_resolving":
		return SiteRetry{Kind: SiteResolving}
	case "tools_installing":
		if m := installPercent.FindStringSubmatch(rest); m != nil {
			n, _ := strconv.Atoi(m[1])
			return SiteRetry{Kind: ToolsInstalling, Fraction: math.Min(1, float64(n)/100)}
		}
		return SiteRetry{Kind: ToolsInstalling, Fraction: math.NaN()}
	}
	return SiteRetry{}
}

const (
	// SiteRetryInterval: a probe or add of a web page is asked for again at most this often
	// (SITE-MEDIA-SPEC §3.3)…
	SiteRetryInterval = 2 * time.Second
	// SiteRetryLimit: …and for at most this long: an install takes at most 15 minutes, an
	// extraction 90 s.
	SiteRetryLimit = 16 * time.Minute
)

// Clock is what RetrySite waits with; tests pass a fake.
type Clock interface {
	Now() time.Time
	// After is time.After.
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RetryOptions tune RetrySite.
type RetryOptions struct {
	// OnWait hears each answer that asks for another try.
	OnWait func(SiteRetry)
	// Clock is the system's when nil.
	Clock Clock
}

// errStopped is RetrySite's answer once its context is done: canceled, "stopped".
func errStopped() error { return connect.NewError(connect.CodeCanceled, errors.New("stopped")) }

// RetrySite calls call until downexd answers something else than tools_installing or
// site_resolving (the install or extraction goes on in downexd meanwhile, and the next call
// joins it). A call starts at most every SiteRetryInterval: one that came back only at downexd's
// wait budget is made again at once, one answered at once after the rest of the interval. Once
// ctx is done no call is made again and RetrySite returns canceled ("stopped"); an answer that
// arrives after the stop still counts when it is a success: an add it made is not lost. After
// SiteRetryLimit the last answer is returned.
func RetrySite[T any](ctx context.Context, call func(context.Context) (T, error), opts RetryOptions) (T, error) {
	clock := opts.Clock
	if clock == nil {
		clock = systemClock{}
	}
	var zero T
	started := clock.Now()
	for {
		if ctx.Err() != nil {
			return zero, errStopped()
		}
		at := clock.Now()
		v, err := call(ctx)
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil {
			return zero, errStopped()
		}
		retry := SiteRetryOf(err)
		if retry.Kind == NoRetry || clock.Now().Sub(started) >= SiteRetryLimit {
			return zero, err
		}
		if opts.OnWait != nil {
			opts.OnWait(retry)
		}
		if wait := at.Add(SiteRetryInterval).Sub(clock.Now()); wait > 0 {
			select {
			case <-ctx.Done():
				return zero, errStopped()
			case <-clock.After(wait):
			}
		}
	}
}

// SiteErrorCode is downexd's code of a failed call ("tools_unavailable"), "" without one.
func SiteErrorCode(err error) string {
	_, message := apiError(err)
	code, _ := SplitCode(message)
	return code
}

var playlistWord = regexp.MustCompile(`(?i)playlist|multi[_-]?video`)

// SiteErrorText is what to tell the user about a failed probe or add of a web page's video
// (SITE-MEDIA-SPEC §5.5): the text of downexd's code, with its reason where it helps.
func SiteErrorText(err error) string {
	apiCode, message := apiError(err)
	// A downexd older than site media has no tools RPCs and refuses a page as a manifest.
	if apiCode == "unimplemented" {
		return SiteTexts["outdated"]
	}
	code, rest := SplitCode(message)
	reason := reasonOf(code, rest)
	switch code {
	case "site_unsupported", "site_unavailable", "auth_required":
		if s := sentence(reason); s != "" {
			return SiteTexts[code] + " " + s
		}
		return SiteTexts[code]
	case "live", "encrypted", "media_invalid", "site_media_off":
		return SiteTexts[code]
	case "media_unsupported":
		if playlistWord.MatchString(rest) {
			return SiteTexts["playlist"]
		}
		return SiteTexts["media_unsupported"]
	case "site_error", "tools_unavailable":
		if reason != "" {
			return SiteTexts[code] + ": " + closed(reason)
		}
		return SiteTexts[code] + "."
	case "site_resolving":
		return SiteTexts["site_resolving"]
	case "tools_installing":
		pct := ""
		if r := SiteRetryOf(err); r.Kind == ToolsInstalling && !math.IsNaN(r.Fraction) {
			pct = " (" + formatPercent(r.Fraction) + ")"
		}
		return SiteTexts["tools_installing"] + pct + "…"
	}
	return message
}

// MediaErrorText is what to tell the user about a failed probe or media add: of a manifest, or
// (site) of a web page.
func MediaErrorText(err error, site bool) string {
	if site {
		return SiteErrorText(err)
	}
	if r := MediaRefusal(err); r != NotRefused {
		return RefusalText(r)
	}
	_, message := apiError(err)
	return message
}

// NeedsToolsSettings reports a failure that needs the tools set up again: the message offers
// Settings → Video sites.
func NeedsToolsSettings(err error) bool {
	code := SiteErrorCode(err)
	return code == "tools_unavailable" || code == "site_media_off"
}

// SiteErrorHint is the hint of a web page's failed download by its error code, shown under
// downexd's message ("" for codes it shares with files, such as network).
func SiteErrorHint(code string) string {
	switch code {
	case "encrypted", "live", "media_unsupported", "media_invalid", "site_unsupported", "site_unavailable", "auth_required", "site_media_off":
		return SiteTexts[code]
	case "site_error":
		return "yt-dlp could not read this page. Retrying may help later, once yt-dlp is updated."
	case "tools_unavailable":
		return "Downex could not set up yt-dlp/Deno. Settings → Video sites shows why."
	}
	return ""
}

// CannotRetry reports a failure that trying again cannot help with: Downex never downloads
// protected or live streams, and a page without an MP4 video (or without the chosen quality)
// stays so.
func CannotRetry(code string) bool {
	switch code {
	case "encrypted", "live", "media_unsupported", "media_invalid", "site_unsupported":
		return true
	}
	return false
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// sliceUTF16 is s.slice(n) in UTF-16 code units.
func sliceUTF16(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if n >= len(u) {
		return ""
	}
	return string(utf16.Decode(u[n:]))
}
