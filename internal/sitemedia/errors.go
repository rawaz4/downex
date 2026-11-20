// Package sitemedia turns what yt-dlp prints for a web page into what Downex offers
// (docs/SITE-MEDIA-SPEC.md §3.10, §3.11): validated formats, every distinct MP4 quality,
// the default pick, stable keys and labels. It is pure: it runs no process and performs
// no network access, and everything it reads is untrusted.
//
// This file holds what the daemon needs without depending on the resolver or the tools
// manager: the values of model.Media.Origin and the sentinel errors of site media.
package sitemedia

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Values of model.Media.Origin (docs/SITE-MEDIA-SPEC.md §3.1, §3.3). A record has
// exactly one spelling for each: the manifest origin is the empty string, which every
// record written before site media already has.
const (
	// OriginManifest is an HLS/DASH manifest URL (docs/MEDIA-SPEC.md).
	OriginManifest = ""
	// OriginSite is a web page resolved with yt-dlp.
	OriginSite = "site"
)

// Sentinel errors of site media, one per error code of docs/SITE-MEDIA-SPEC.md §3.2 (the
// code in each comment); test with errors.Is. Errors wrap them with a short reason after
// ": " (or, for ErrToolsInstalling, the progress, e.g. "downloading yt-dlp and Deno
// (34%)"). Their texts are what users read after the code prefix, and they are distinct
// and none contains another, so that a failure known only by its stored message maps
// back to its code. The codes encrypted, live, media_unsupported and media_invalid use
// the internal/media sentinels instead.
var (
	// ErrSiteUnsupported: yt-dlp found no video on the page (site_unsupported).
	ErrSiteUnsupported = errors.New("no video found on this page")
	// ErrSiteUnavailable: the video is private, removed or geo-blocked, or the site
	// rate-limits (site_unavailable).
	ErrSiteUnavailable = errors.New("this video is not available")
	// ErrAuthRequired: the site wants a sign-in, an age check, a membership or a bot
	// check (auth_required).
	ErrAuthRequired = errors.New("the site asks to sign in")
	// ErrSiteError: yt-dlp failed otherwise: an unknown error, bad options, a crash, a
	// timeout or a proxy it cannot use (site_error).
	ErrSiteError = errors.New("yt-dlp could not read this page")
	// ErrSiteResolving: the extraction was still running when the wait ended; it goes on
	// and its result is cached, so the caller retries (site_resolving).
	ErrSiteResolving = errors.New("still getting the video info")
	// ErrToolsInstalling: yt-dlp and Deno are being downloaded; the caller retries
	// (tools_installing).
	ErrToolsInstalling = errors.New("downloading yt-dlp and Deno")
	// ErrToolsUnavailable: the install or verification failed, the platform has no
	// build, or this daemon never downloads tools (tools_unavailable).
	ErrToolsUnavailable = errors.New("yt-dlp and Deno are not set up")
	// ErrSiteMediaOff: Settings.site_media is off (site_media_off).
	ErrSiteMediaOff = errors.New("video downloads from web pages are turned off (Settings → Video sites)")
)

// ReasonForbidden is the reason of a site_error for a refusal with HTTP status 403
// (docs/SITE-MEDIA-SPEC.md §2.6, §3.10, amendment 2026-11-18): of the page (yt-dlp's
// "HTTP Error 403"), or of its video server again after the addresses were renewed. It
// is most often a site's bot protection, which Downex never tries to get past.
const ReasonForbidden = "the site refused the request (HTTP 403, often bot protection)"

// LinkRefusedReason is the reason of a site_error for a page's stream whose server
// refuses its address with HTTP status code (401, 403 or 410) even after the page was
// read again (docs/SITE-MEDIA-SPEC.md §2.6, amendment 2026-11-18): the signed links of
// some sites expire within minutes, and a site may block the download.
func LinkRefusedReason(code int) string {
	return fmt.Sprintf("the site refused the link to the stream (HTTP %d %s): it has expired or the site blocks the download",
		code, http.StatusText(code))
}

// ErrPlaylist marks the refusal of a playlist without any video Downex reads (a list of
// links): open a single video of it (media_unsupported, docs/SITE-MEDIA-SPEC.md §3.12).
var ErrPlaylist = errors.New("the page is a playlist")

// PlaylistError is the refusal of a playlist page on pageHost: "open a single track of
// it" on an audio site (AudioSite), else "open a single video of it".
func PlaylistError(unsupported error, pageHost string) error {
	if AudioSite(pageHost) {
		return fmt.Errorf("%w: %w; open a single track of it", unsupported, ErrPlaylist)
	}
	return fmt.Errorf("%w: %w; open a single video of it", unsupported, ErrPlaylist)
}

// AudioSite reports the host of an audio site's page (docs/VIDEO-DETECT-SPEC.md §3.8,
// as the address bar shows them): soundcloud.com, mixcloud.com (with www. or m.) and
// bandcamp.com with any artist's subdomain. A port and the case are ignored.
func AudioSite(pageHost string) bool {
	h := normalizeHost(pageHost)
	for _, p := range []string{"www.", "m."} {
		h = strings.TrimPrefix(h, p)
	}
	return h == "soundcloud.com" || h == "mixcloud.com" || h == "bandcamp.com" || strings.HasSuffix(h, ".bandcamp.com")
}

// Browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8).
const (
	// ReasonBrowserOnly is the reason of a site_error for a download of a site whose video
	// servers answer only web browsers (BrowserOnly) that no browser fetches for: added
	// from the desktop or the CLI, or after the browser's ticket ended.
	ReasonBrowserOnly = "the site's video server answers only a web browser: download it with Downex's button in your browser"
	// ReasonBrowserGone is the reason of a site_error for a run whose requests no browser
	// takes any more, or that no tab of the site can make.
	ReasonBrowserGone = "your browser is no longer fetching this video for Downex: open the video in the browser and " +
		"click Download again, or keep a tab of the site open and resume"
	// ReasonBrowserNotMedia is the reason of a site_error for a run whose browser got a page
	// instead of the video.
	ReasonBrowserNotMedia = "the browser got a page instead of the video"
	// ReasonBrowserFailed is the reason of a site_error for a run whose browser could not
	// fetch the video (its network, the site's CORS rules).
	ReasonBrowserFailed = "the browser could not fetch the video"
	// ReasonBrowserRefused is the reason of a site_error for a run whose browser's requests
	// were refused with HTTP 403 again after the renewal: a quality the tab's player did not
	// fetch, whose address the site gives only to yt-dlp's session (TikTok).
	ReasonBrowserRefused = "the site refused this quality to your browser too: play the video on its page, " +
		"then download the quality it plays"
)

// BrowserError is a site_error (it wraps ErrSiteError) of a browser-fetched save whose text is
// reason alone: what went wrong is the browser's fetch, not yt-dlp's reading of the page
// (docs/SITE-MEDIA-SPEC.md §2.8).
func BrowserError(reason string) error { return browserError(reason) }

type browserError string

func (e browserError) Error() string { return string(e) }
func (e browserError) Unwrap() error { return ErrSiteError }

// browserOnly are the extractor keys of the sites whose video servers answer only web
// browsers (docs/SITE-MEDIA-SPEC.md §2.8: TikTok's answer 403 to every other client,
// measured 2026-11-18).
var browserOnly = map[string]bool{"TikTok": true}

// BrowserOnly reports a site (by yt-dlp's extractor key) whose video servers answer only
// web browsers: its downloads added from the browser are fetched by the browser.
func BrowserOnly(extractorKey string) bool { return browserOnly[extractorKey] }
