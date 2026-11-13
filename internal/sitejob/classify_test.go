package sitejob

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"downex/internal/media"
	"downex/internal/sitemedia"
)

// codeErrors maps the codes of docs/SITE-MEDIA-SPEC.md §3.2 that a failed run can have
// to their sentinels.
var codeErrors = map[string]error{
	"encrypted":        media.ErrEncrypted,
	"live":             media.ErrLive,
	"auth_required":    sitemedia.ErrAuthRequired,
	"site_unavailable": sitemedia.ErrSiteUnavailable,
	"site_unsupported": sitemedia.ErrSiteUnsupported,
	"site_error":       sitemedia.ErrSiteError,
}

// Every fixture in testdata/stderr (named "<code>-<case>.txt") is classified as its
// code, with yt-dlp's advice, URLs and control characters left out of the reason.
func TestClassifyFixtures(t *testing.T) {
	reasons := map[string]string{
		"auth_required-age.txt":        "Sign in to confirm your age. This video may be inappropriate for some users.",
		"auth_required-bot.txt":        "Sign in to confirm you’re not a bot.",
		"auth_required-members.txt":    "Join this channel to get access to members-only content like this video, and other exclusive perks.",
		"auth_required-premium.txt":    "This video is only available for Music Premium members",
		"auth_required-login.txt":      "Login required to view this video.",
		"site_unavailable-private.txt": "Private video. Sign in if you've been granted access to this video.",
		"site_unavailable-removed.txt": "Video unavailable. This video has been removed by the uploader",
		"site_unavailable-country.txt": "The uploader has not made this video available in your country",
		"site_unavailable-geo.txt":     "This video is not available from your location due to geo restriction",
		"site_unavailable-ratelimit.txt": "The current session has been rate-limited by YouTube for up to an hour. It is recommended " +
			"to use `-t sleep` to add a delay between video requests to avoid exceeding the rate limit.",
		"site_unavailable-429.txt": "Unable to download API page: HTTP Error 429: Too Many Requests (caused by <HTTPError 429: " +
			"Too Many Requests>)",
		"site_unavailable-terminated.txt": "Video unavailable. This video is no longer available because the YouTube account " +
			"associated with this video has been terminated.",
		"site_unavailable-tryagain.txt": "Video unavailable. This content isn't available, try again later.",
		"encrypted-drm.txt":             "This video is DRM protected",
		"live-upcoming.txt":             "This live event will begin in 3 hours.",
		"live-premiere.txt":             "Premieres in 2 days",
		"site_unsupported-generic.txt":  "Unsupported URL",
		"site_error-proxy.txt":          "the proxy needs a sign-in yt-dlp cannot do",
		"site_error-extract.txt":        "Failed to extract any player response; please report this issue on, filling out the appropriate issue template.",
		"site_error-crash.txt":          "MemoryError",
		"site_error-controls.txt":       "Bad name here",
	}
	files, err := filepath.Glob(filepath.Join("testdata", "stderr", "*.txt"))
	if err != nil || len(files) != len(reasons) {
		t.Fatalf("%d fixtures (%v), %d expected", len(files), err, len(reasons))
	}
	for _, path := range files {
		name := filepath.Base(path)
		code, _, _ := strings.Cut(name, "-")
		want, ok := codeErrors[code]
		reason, known := reasons[name]
		if !ok || !known {
			t.Errorf("%s: no expectation", name)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got := classify(data, 1)
		if !errors.Is(got, want) {
			t.Errorf("%s: classified as %v, want %s", name, got, code)
		}
		for c, other := range codeErrors {
			if c != code && errors.Is(got, other) {
				t.Errorf("%s: also %s", name, c)
			}
		}
		if msg := got.Error(); msg != want.Error()+": "+reason {
			t.Errorf("%s: message\n got %q\nwant %q", name, msg, want.Error()+": "+reason)
		}
		if strings.Contains(got.Error(), "://") || strings.Contains(got.Error(), "example") {
			t.Errorf("%s: the reason keeps a URL: %q", name, got)
		}
	}
}

// Classification looks at the last ERROR: line only, ignores case, matches phrases
// only outside URLs, and falls back to the last line or the exit code.
func TestClassifyRules(t *testing.T) {
	for _, c := range []struct {
		name, stderr string
		exit         int
		want         error
		reason       string
	}{
		{"last error wins", "ERROR: Private video\nERROR: [x] 1: something broke\n", 1, sitemedia.ErrSiteError, "something broke"},
		{"case ignored", "ERROR: THIS VIDEO IS DRM-PROTECTED\n", 1, media.ErrEncrypted, "THIS VIDEO IS DRM-PROTECTED"},
		{"phrase in a URL", "ERROR: [generic] Unable to download webpage https://geo.example.com/sign-in/drm\n", 1,
			sitemedia.ErrSiteError, "[generic] Unable to download webpage"},
		{"no error line", "WARNING: slow\nsomething went wrong\nWARNING: still slow\n", 1, sitemedia.ErrSiteError, "something went wrong"},
		{"nothing at all", "", 1, sitemedia.ErrSiteError, "it exited with code 1"},
		{"empty error line", "ERROR: [x] 1: https://example.com/a\n", 1, sitemedia.ErrSiteError, "it exited with code 1"},
		{"crlf", "ERROR: [x] abc: Video unavailable\r\n", 1, sitemedia.ErrSiteUnavailable, "Video unavailable"},
		{"no extractor id", "ERROR: [youtube:tab] This channel does not have a videos tab\n", 1, sitemedia.ErrSiteError,
			"[youtube:tab] This channel does not have a videos tab"},
	} {
		got := classify([]byte(c.stderr), c.exit)
		if !errors.Is(got, c.want) || got.Error() != c.want.Error()+": "+c.reason {
			t.Errorf("%s: classify = %q, want %v: %q", c.name, got, c.want, c.reason)
		}
	}
}

// A reason is at most 200 bytes of valid UTF-8, without bidi controls or control
// characters.
func TestCleanReason(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes
	if r := cleanReason(long, true); len(r) > maxReason || !utf8.ValidString(r) || len(r) < maxReason-1 {
		t.Errorf("long reason: %d bytes, valid %v", len(r), utf8.ValidString(r))
	}
	for in, want := range map[string]string{
		"a‮b⁦c‏d":                          "abcd",
		"tab\there\x00and\x1bthere":        "tab here and there",
		"see https://x.example/a?b=c here": "see here",
		"one. two --opt. three":            "one. two --opt. three", // without advice
		"   spaced   out   ":               "spaced out",
		"ends with:":                       "ends with",
	} {
		if got := cleanReason(in, false); got != want {
			t.Errorf("cleanReason(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanReason("First https://x.example/. Use --cookies. Then this. See https://y.example/ now", true); got != "First Then this." {
		t.Errorf("advice = %q", got)
	}
}
