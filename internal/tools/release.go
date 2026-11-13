package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Release tags (docs/SITE-MEDIA-SPEC.md §4.2): yt-dlp's are its version (YYYY.MM.DD,
// nightly builds .HHMMSS more), Deno's are "v" and a semantic version.
var (
	ytdlpTagRE = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(\.\d{6})?$`)
	denoTagRE  = regexp.MustCompile(`^v\d{1,6}\.\d{1,6}\.\d{1,6}$`)
	// ytdlpVersionRE matches what yt-dlp --version prints, and version folder names.
	ytdlpVersionRE = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(\.\d{1,9})?$`)
	denoVersionRE  = regexp.MustCompile(`^\d{1,6}\.\d{1,6}\.\d{1,6}$`)
	// denoVersionLineRE is the first line of deno --version.
	denoVersionLineRE = regexp.MustCompile(`^deno (\d{1,6}\.\d{1,6}\.\d{1,6})(\s|$)`)
)

// minDeno is the oldest Deno that yt-dlp runs its challenge solver with.
var minDeno = denoVersion{2, 3, 0}

// ytVersion is a yt-dlp version: a release date and an optional build number (a hotfix
// counter, or HHMMSS for nightly builds). Without a build number it sorts first.
type ytVersion struct {
	date  time.Time
	build int64 // -1 = none
}

// parseYtDlpVersion parses a yt-dlp version (YYYY.MM.DD[.N]) with a valid date.
func parseYtDlpVersion(s string) (ytVersion, bool) {
	if !ytdlpVersionRE.MatchString(s) {
		return ytVersion{}, false
	}
	parts := strings.Split(s, ".")
	y, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	d, _ := strconv.Atoi(parts[2])
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return ytVersion{}, false
	}
	v := ytVersion{date: t, build: -1}
	if len(parts) == 4 {
		v.build, _ = strconv.ParseInt(parts[3], 10, 64)
	}
	return v, true
}

func (v ytVersion) compare(o ytVersion) int {
	if c := v.date.Compare(o.date); c != 0 {
		return c
	}
	switch {
	case v.build < o.build:
		return -1
	case v.build > o.build:
		return 1
	}
	return 0
}

// compareYtDlp orders two valid yt-dlp versions (an invalid one sorts first).
func compareYtDlp(a, b string) int {
	va, oka := parseYtDlpVersion(a)
	vb, okb := parseYtDlpVersion(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return -1
	case !okb:
		return 1
	}
	return va.compare(vb)
}

// denoVersion is a Deno version.
type denoVersion struct{ major, minor, patch int }

func parseDenoVersion(s string) (denoVersion, bool) {
	if !denoVersionRE.MatchString(s) {
		return denoVersion{}, false
	}
	parts := strings.Split(s, ".")
	var v denoVersion
	v.major, _ = strconv.Atoi(parts[0])
	v.minor, _ = strconv.Atoi(parts[1])
	v.patch, _ = strconv.Atoi(parts[2])
	return v, true
}

func (v denoVersion) compare(o denoVersion) int {
	for _, d := range [3]int{v.major - o.major, v.minor - o.minor, v.patch - o.patch} {
		switch {
		case d < 0:
			return -1
		case d > 0:
			return 1
		}
	}
	return 0
}

// compareDeno orders two valid Deno versions (an invalid one sorts first).
func compareDeno(a, b string) int {
	va, oka := parseDenoVersion(a)
	vb, okb := parseDenoVersion(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return -1
	case !okb:
		return 1
	}
	return va.compare(vb)
}

// validVersion reports whether version is a valid version (and so a folder name) of tool.
func validVersion(tool, version string) bool {
	switch tool {
	case ToolYtDlp:
		_, ok := parseYtDlpVersion(version)
		return ok
	case ToolDeno:
		_, ok := parseDenoVersion(version)
		return ok
	}
	return false
}

// compareVersions orders two versions of tool.
func compareVersions(tool, a, b string) int {
	if tool == ToolDeno {
		return compareDeno(a, b)
	}
	return compareYtDlp(a, b)
}

// versionOfTag returns the version a release tag names: the tag itself for yt-dlp, the
// tag without its "v" for Deno.
func versionOfTag(tool, tag string) (string, bool) {
	switch tool {
	case ToolYtDlp:
		if ytdlpTagRE.MatchString(tag) && validVersion(tool, tag) {
			return tag, true
		}
	case ToolDeno:
		if denoTagRE.MatchString(tag) {
			return tag[1:], true
		}
	}
	return "", false
}

// releaseURL is the download URL of asset in release tag of repo.
func releaseURL(base, repo, tag, asset string) string {
	return base + "/" + repo + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(asset)
}

// latestTag finds the tag of repo's latest release from the redirect of
// <base>/<repo>/releases/latest (one request, no API, no rate limit). The redirect is
// not followed; its target must be <base's host>/<repo>/releases/tag/<tag>.
func (f *fetcher) latestTag(ctx context.Context, base, repo string) (string, error) {
	raw := base + "/" + repo + "/releases/latest"
	c := *f.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := f.do(ctx, &c, raw)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", statusError(resp.StatusCode)
	}
	loc, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Scheme != "https" {
		return "", coded(CodeNetwork, errors.New("the latest release redirect is not an https address"))
	}
	prefix := "/" + repo + "/releases/tag/"
	p := loc.EscapedPath()
	ok := len(p) > len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
	if !ok || !strings.EqualFold(loc.Host, resp.Request.URL.Host) {
		return "", coded(CodeHTTPStatus, fmt.Errorf("%w: the latest release redirect has an unexpected target", ErrBadRelease))
	}
	tag := p[len(prefix):]
	if t, err := url.PathUnescape(tag); err == nil {
		tag = t
	}
	if strings.ContainsAny(tag, "/\\") || len(tag) > 64 {
		return "", coded(CodeHTTPStatus, fmt.Errorf("%w: malformed tag", ErrBadRelease))
	}
	return tag, nil
}
