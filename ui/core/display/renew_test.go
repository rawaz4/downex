package display

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// TestExpiredLinkDisplay: docs/DESKTOP-SPEC.md §10's list texts and inline action.
func TestExpiredLinkDisplay(t *testing.T) {
	expired := &downexv1.Download{State: stateFailed, ErrorCode: CodeLinkExpired, Received: 10, Size: 100,
		PageUrl: "https://pages.example/a"}
	if s := StatusText(expired); s != "Link expired" {
		t.Errorf("StatusText = %q", s)
	}
	if a, ok := InlineAction(expired); !ok || a.ID != "renew" || a.Label != "New link…" {
		t.Errorf("InlineAction = %+v, %v", a, ok)
	}
	want := "The link stopped working: it expired, was revoked or now leads to a web page. The data received so far is " +
		"kept. Give the download a new link, or open its page and download it again: Downex continues it when the new " +
		"link serves the same file."
	if h := ErrorHintOf(expired); h != want {
		t.Errorf("hint = %q", h)
	}
	nothing := &downexv1.Download{State: stateFailed, ErrorCode: CodeLinkExpired}
	if h := ErrorHintOf(nothing); h != "The link stopped working: it expired, was revoked or now leads to a web page. "+
		"Give the download a new link, or open its page and download it again." {
		t.Errorf("hint without data = %q", h)
	}
	media := &downexv1.Download{State: stateFailed, ErrorCode: CodeLinkExpired, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA}
	if a, ok := InlineAction(media); ok && a.ID == "renew" {
		t.Errorf("a media download offers New link…")
	}
	if !LinkExpired(expired) || LinkExpired(&downexv1.Download{State: statePaused, ErrorCode: CodeLinkExpired}) {
		t.Error("LinkExpired")
	}
	for _, c := range []struct {
		d    *downexv1.Download
		want bool
	}{
		{expired, true},
		{&downexv1.Download{State: statePaused}, true},
		{&downexv1.Download{State: stateDownloading}, false},
		{&downexv1.Download{State: stateCompleted}, false},
		{&downexv1.Download{State: stateAwaiting}, false},
		{media, false},
		{nil, false},
	} {
		if got := Renewable(c.d); got != c.want {
			t.Errorf("Renewable(%v) = %v", c.d, got)
		}
	}
	if PageURL(expired) != "https://pages.example/a" || PageURL(&downexv1.Download{PageUrl: "javascript:x"}) != "" {
		t.Error("PageURL")
	}
	for _, st := range []downexv1.DownloadState{stateQueued, stateAwaiting} {
		if s := StatusText(&downexv1.Download{State: st, RenewCheck: true}); s != "Checking link…" {
			t.Errorf("held %v = %q", st, s)
		}
	}
}
