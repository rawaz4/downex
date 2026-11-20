package media

import (
	"errors"
	"net/http"
	"testing"

	"downex/internal/played"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

// A site's refusal of a web page download says its short reason and the action that helps,
// and offers no Retry (SITE-MEDIA-SPEC §5.5, amendment 2026-11-19); downexd's own reasons
// are what is matched.
func TestSiteRefusal(t *testing.T) {
	siteErr := func(err error) string { return err.Error() }
	site := func(code, msg string) *downexv1.Download {
		return &downexv1.Download{
			Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, State: downexv1.DownloadState_DOWNLOAD_STATE_FAILED,
			Media: &downexv1.MediaInfo{Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, PageUrl: "https://www.tiktok.com/@a/video/1"}, ErrorCode: code, Error: msg,
		}
	}
	forbidden := siteErr(errors.Join(sitemedia.ErrSiteError, errors.New(sitemedia.ReasonForbidden)))
	cases := []struct {
		name, code, msg string
		want            SiteRefusal
	}{
		{"refused to the browser too", "site_error", sitemedia.BrowserError(sitemedia.ReasonBrowserRefused).Error(),
			SiteRefusal{refusedQualityReason, refusedQualityAction}},
		{"browser only", "site_error", sitemedia.BrowserError(sitemedia.ReasonBrowserOnly).Error(),
			SiteRefusal{browserOnlyReason, inBrowserAction}},
		{"played browser only", "site_error", played.ReasonBrowserOnly, SiteRefusal{browserOnlyReason, inBrowserAction}},
		{"bot protection", "site_error", forbidden, SiteRefusal{browserForbidReason, inBrowserAction}},
		{"link refused 403", "site_error", "yt-dlp could not read this page: " + sitemedia.LinkRefusedReason(http.StatusForbidden),
			SiteRefusal{browserForbidReason, inBrowserAction}},
		{"http_status 403", "http_status", "HTTP 403 Forbidden", SiteRefusal{browserForbidReason, inBrowserAction}},
	}
	for _, c := range cases {
		d := site(c.code, c.msg)
		got, ok := SiteRefusalOfDownload(d)
		if !ok || got != c.want {
			t.Errorf("%s: SiteRefusalOfDownload = %+v, %v; want %+v", c.name, got, ok, c.want)
		}
		if !CannotRetryDownload(d) {
			t.Errorf("%s: CannotRetryDownload = false", c.name)
		}
	}
	for _, d := range []*downexv1.Download{
		site("site_error", "yt-dlp could not read this page: HTTP Error 500"),
		site("site_error", sitemedia.BrowserError(sitemedia.ReasonBrowserGone).Error()), // resume helps
		site("site_error", "yt-dlp could not read this page: "+sitemedia.LinkRefusedReason(http.StatusGone)),
		site("http_status", "HTTP 500 Internal Server Error"),
		site("network", sitemedia.ReasonForbidden),
		{Kind: downexv1.DownloadKind_DOWNLOAD_KIND_FILE, State: downexv1.DownloadState_DOWNLOAD_STATE_FAILED, ErrorCode: "http_status", Error: "HTTP 403 Forbidden"},
	} {
		if r, ok := SiteRefusalOfDownload(d); ok {
			t.Errorf("%q (%s): refusal %+v", d.GetError(), d.GetErrorCode(), r)
		}
		if CannotRetryDownload(d) {
			t.Errorf("%q (%s): CannotRetryDownload = true", d.GetError(), d.GetErrorCode())
		}
	}
	running := site("site_error", sitemedia.ReasonForbidden)
	running.State = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	if _, ok := SiteRefusalOfDownload(running); ok {
		t.Error("a running download is no refusal")
	}
}
