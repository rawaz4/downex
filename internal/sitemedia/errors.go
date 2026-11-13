// Package sitemedia turns what yt-dlp prints for a web page into what Downex offers
// (docs/SITE-MEDIA-SPEC.md §3.10, §3.11): validated formats, every distinct MP4 quality,
// the default pick, stable keys and labels. It is pure: it runs no process and performs
// no network access, and everything it reads is untrusted.
//
// This file holds what the daemon needs without depending on the resolver or the tools
// manager: the values of model.Media.Origin and the sentinel errors of site media.
package sitemedia

import "errors"

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
