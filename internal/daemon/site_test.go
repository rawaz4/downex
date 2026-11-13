package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

const (
	originSite      = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
	stProbing       = downexv1.DownloadState_DOWNLOAD_STATE_PROBING
	sourceBrowser   = downexv1.AddSource_ADD_SOURCE_BROWSER
	testPageReferer = "https://www.video.test/"
)

func (h *harness) probeMedia(req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	res, err := h.dl.ProbeMedia(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetMedia(), nil
}

func (h *harness) addMediaReq(req *downexv1.AddMediaRequest) (*downexv1.Download, error) {
	res, err := h.dl.AddMedia(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetDownload(), nil
}

// siteHeaders are the headers a browser hand-off of testPage sends: two cookies, the
// referer and one that is ignored.
func siteHeaders() []*downexv1.Header {
	return []*downexv1.Header{
		{Name: "Cookie", Value: "SID=1"}, {Name: "cookie", Value: "HSID=2"},
		{Name: "Referer", Value: testPageReferer}, {Name: "User-Agent", Value: "Browser/1"},
	}
}

// TestSiteAddConfirmAnotherQuality: a browser hand-off of a page awaits confirmation with
// every offered MP4 quality and the default pick; Confirm switches to another quality
// (tracks, keys and size change, the container and the .mp4 name stay) and the run
// downloads it with the cookie the page was added with, which is never stored.
func TestSiteAddConfirmAnotherQuality(t *testing.T) {
	fs := newFakeSite(3)
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	d := h.addMedia(&downexv1.AddMediaRequest{
		Origin: originSite, PageUrl: testPage, Source: sourceBrowser, Title: "Tab title", Headers: siteHeaders(),
	})
	if d.GetState() != stAwaiting || d.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA || d.GetUrl() != testPage ||
		d.GetFinalUrl() != "" || d.GetCategory() != "Video" || d.GetFilename() != "A video title.mp4" ||
		d.GetExpectedSize() != 416_000_000 || d.GetSize() != -1 {
		t.Fatalf("added = %v", d)
	}
	mi := d.GetMedia()
	if mi.GetOrigin() != originSite || mi.GetManifestUrl() != "" || mi.GetPageUrl() != testPage || mi.GetSite() != "YouTube" ||
		mi.GetTitle() != "A video title" || mi.GetFormat() != "http" || mi.GetContainer() != "mp4" ||
		mi.GetSelectedVideo() != "137" || mi.GetSelectedAudio() != "140" || len(mi.GetTracks()) != 5 || mi.GetDurationMs() != 212_000 {
		t.Fatalf("media = %v", mi)
	}
	if tr := mi.GetTracks()[1]; tr.GetId() != "399" || tr.GetSize() != 300_000_000 || !tr.GetSizeApprox() || tr.GetExt() != "mp4" ||
		tr.GetCodecs() != "av01.0.08M.08" || tr.GetHeight() != 720 || tr.GetName() != "720p (HD)" {
		t.Errorf("track = %v", tr)
	}
	probes, _ := fs.calls()
	if len(probes) != 1 || probes[0] != (SiteSelection{PageURL: testPage, Cookie: "SID=1; HSID=2", Referer: testPageReferer}) {
		t.Errorf("probes = %+v", probes)
	}
	rec := storedState(t, h.st, d.GetId())
	if rec.Request.URL != testPage || len(rec.Request.Headers) != 1 || rec.Request.Headers.Get("Referer") != testPageReferer ||
		rec.Media.Origin != sitemedia.OriginSite || rec.Media.Site != "YouTube" || rec.Media.PageURL != testPage ||
		rec.Media.Tracks[0].Key != "s1-v137" || rec.Media.Tracks[3].Ext != "m4a" || rec.ExpectedSize != 416_000_000 {
		t.Errorf("stored = %+v media %+v", rec, rec.Media)
	}
	if b, err := json.Marshal(rec); err != nil || bytes.Contains(b, []byte("SID=1")) {
		t.Errorf("the stored record carries the cookie: %s (%v)", b, err)
	}

	// Another quality: AV1, video only, keeps its audio.
	c, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, VideoTrack: "399"})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetMedia().GetSelectedVideo() != "399" || c.GetMedia().GetSelectedAudio() != "140" || c.GetFilename() != "A video title.mp4" ||
		c.GetMedia().GetContainer() != "mp4" || c.GetExpectedSize() != 304_000_000 || c.GetCategory() != "Video" {
		t.Fatalf("confirmed = %v", c)
	}
	done := h.waitCompleted(d.GetId())
	out := filepath.Join(done.GetDir(), "A video title.mp4")
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, wantOutput(3)) {
		t.Fatalf("output %q, %v", got, err)
	}
	if done.GetSize() != int64(len(wantOutput(3))) || done.GetMedia().GetSelectedVideo() != "399" {
		t.Errorf("completed = %v", done)
	}
	_, runs := fs.calls()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	r := runs[0]
	want := SiteSelection{PageURL: testPage, Cookie: "SID=1; HSID=2", Referer: testPageReferer, Format: "http",
		VideoTrack: "399", AudioTrack: "140", VideoKey: "s1-v399", AudioKey: "s1-a140"}
	if r.sel != want || r.resume != nil || r.opts.OutPath != out || r.opts.PartDir != out+".dxparts" ||
		r.opts.MaxConnections != DefaultMaxConnections {
		t.Errorf("run = %+v", r)
	}
	if c := h.m.siteCookies.get(model.DownloadID(d.GetId())); c != "" {
		t.Errorf("the cookie is kept after completion")
	}

	// A muxed quality drops the separate audio and cannot take one.
	d2 := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser})
	_, err = h.confirmReq(&downexv1.ConfirmRequest{Id: d2.GetId(), VideoTrack: "22", AudioTrack: "140"})
	if mediaCode(t, err) != CodeMediaInvalid || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("muxed + audio: %v", err)
	}
	_, err = h.confirmReq(&downexv1.ConfirmRequest{Id: d2.GetId(), VideoTrack: "999"})
	if mediaCode(t, err) != CodeMediaInvalid {
		t.Errorf("unknown track: %v", err)
	}
	c, err = h.confirmReq(&downexv1.ConfirmRequest{Id: d2.GetId(), VideoTrack: "22", Filename: "clip.webm"})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetMedia().GetSelectedVideo() != "22" || c.GetMedia().GetSelectedAudio() != "" || c.GetExpectedSize() != 90_000_000 ||
		c.GetFilename() != "clip.mp4" || c.GetState() != stPaused {
		t.Errorf("confirmed muxed = %v", c)
	}
}

// TestSiteAddRequestedTracks: AddMedia with tracks records them (a requested video-only
// quality gets the page's default audio) and names the file as asked.
func TestSiteAddRequestedTracks(t *testing.T) {
	fs := newFakeSite(1)
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs})
	dir := t.TempDir()
	d := h.addMedia(&downexv1.AddMediaRequest{
		Origin: originSite, PageUrl: testPage, VideoTrack: "399", AudioTrack: "140-drc", Dir: dir, Filename: "talk",
		StartPaused: true, Source: downexv1.AddSource_ADD_SOURCE_CLI,
	})
	if d.GetState() != stPaused || d.GetDir() != dir || d.GetFilename() != "talk.mp4" ||
		d.GetMedia().GetSelectedVideo() != "399" || d.GetMedia().GetSelectedAudio() != "140-drc" ||
		d.GetExpectedSize() != 304_100_000 {
		t.Fatalf("added = %v", d)
	}
	probes, _ := fs.calls()
	if probes[0].VideoTrack != "399" || probes[0].AudioTrack != "140-drc" || probes[0].Cookie != "" {
		t.Errorf("probe = %+v", probes[0])
	}
	// A resolver that ignored the request: the daemon applies it with Confirm's rules (a
	// video-only quality instead of a muxed one gets the best audio).
	med := &model.Media{SelectedVideo: "22"}
	for _, tr := range sampleSiteTracks() {
		med.Tracks = append(med.Tracks, modelTrack(tr))
	}
	if err := pickSiteTracks(med, "137", ""); err != nil || med.SelectedVideo != "137" || med.SelectedAudio != "140" {
		t.Errorf("pickSiteTracks = %q + %q, %v", med.SelectedVideo, med.SelectedAudio, err)
	}
	if err := pickSiteTracks(med, "", "140"); err != nil || med.SelectedVideo != "137" {
		t.Errorf("an audio track keeps the video: %q, %v", med.SelectedVideo, err)
	}
	// The file name falls back to the requested title, then the page's last segment.
	for _, c := range []struct{ requested, site, title, page, want string }{
		{"clip.webm", "Site title", "", testPage, "clip.mp4"},
		{"", "Site title", "Tab", testPage, "Site title.mp4"},
		{"", "", "Tab", testPage, "Tab.mp4"},
		{"", "", "", "https://site.test/videos/my-clip", "my-clip.mp4"},
		{"", "", "", "https://site.test/", "video.mp4"},
	} {
		if got := siteFileName(c.requested, c.site, c.title, c.page); got != c.want {
			t.Errorf("siteFileName(%q, %q, %q, %q) = %q, want %q", c.requested, c.site, c.title, c.page, got, c.want)
		}
	}
}

// TestSiteProbe: ProbeMedia of a page describes every offered MP4 quality, records
// nothing, and resolves with the cookie and referer only.
func TestSiteProbe(t *testing.T) {
	fs := newFakeSite(1)
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	mi, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage, Headers: siteHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	if mi.GetOrigin() != originSite || mi.GetManifestUrl() != "" || mi.GetPageUrl() != testPage || mi.GetSite() != "YouTube" ||
		mi.GetFormat() != "http" || mi.GetContainer() != "mp4" || mi.GetSelectedVideo() != "137" || mi.GetSelectedAudio() != "140" ||
		len(mi.GetTracks()) != 5 || mi.GetFragmentsTotal() != 0 {
		t.Fatalf("media = %v", mi)
	}
	for i, id := range []string{"137", "399", "22", "140", "140-drc"} {
		if mi.GetTracks()[i].GetId() != id {
			t.Errorf("track %d = %s, want %s (the resolver's order)", i, mi.GetTracks()[i].GetId(), id)
		}
	}
	if tr := mi.GetTracks()[3]; tr.GetSize() != 4_000_000 || tr.GetSizeApprox() || tr.GetExt() != "m4a" || tr.GetLanguage() != "en" {
		t.Errorf("audio track = %v", tr)
	}
	if len(h.list()) != 0 {
		t.Error("ProbeMedia recorded a download")
	}
	probes, _ := fs.calls()
	if len(probes) != 1 || probes[0].Cookie != "SID=1; HSID=2" || probes[0].Referer != testPageReferer || probes[0].Format != "" {
		t.Errorf("probes = %+v", probes)
	}
}

// TestSitePrefetch: a look-ahead (ProbeMedia with prefetch, docs/SITE-MEDIA-SPEC.md
// §5.6) is a probe that never asks the tools to set themselves up: tools that are not
// ready (missing, or being installed) answer tools_unavailable at once, before the page
// is resolved. The resolver is told it is a look-ahead, and its refusal to queue behind
// other work answers site_resolving at once. A probe and an add that follow are ordinary
// requests (the resolver's cache, which they share with the look-ahead, serves them).
func TestSitePrefetch(t *testing.T) {
	fs, tools := newFakeSite(1), newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: tools})
	ahead := &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage, Prefetch: true}
	mi, err := h.probeMedia(ahead)
	if err != nil {
		t.Fatal(err)
	}
	if mi.GetOrigin() != originSite || mi.GetPageUrl() != testPage || mi.GetSelectedVideo() != "137" ||
		mi.GetSelectedAudio() != "140" || len(mi.GetTracks()) != 5 {
		t.Fatalf("look-ahead = %v", mi)
	}
	probes, _ := fs.calls()
	if len(probes) != 1 || probes[0] != (SiteSelection{PageURL: testPage, Prefetch: true}) {
		t.Errorf("probes = %+v", probes)
	}
	if n := tools.ensureCount(); n != 0 {
		t.Errorf("the look-ahead asked the tools %d times (Ensure installs them)", n)
	}
	if _, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage}); err != nil {
		t.Fatal(err)
	}
	h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, StartPaused: true})
	probes, _ = fs.calls()
	if len(probes) != 3 || probes[1] != (SiteSelection{PageURL: testPage}) || probes[2] != (SiteSelection{PageURL: testPage}) {
		t.Errorf("the probe and add after the look-ahead: %+v", probes)
	}
	if n := tools.ensureCount(); n != 2 {
		t.Errorf("Ensure called %d times by the probe and the add, want 2", n)
	}

	// Tools that are not ready: refused at once, without asking them or the resolver.
	asked := tools.ensureCount()
	for _, c := range []struct {
		name string
		set  func(bool)
	}{{"missing", tools.setMissing}, {"installing", tools.setInstalling}} {
		c.set(true)
		start := time.Now()
		_, err := h.probeMedia(ahead)
		took := time.Since(start)
		c.set(false)
		if mediaCode(t, err) != CodeToolsUnavailable || connect.CodeOf(err) != connect.CodeFailedPrecondition ||
			!strings.Contains(err.Error(), "tools_unavailable: yt-dlp and Deno are not set up") {
			t.Errorf("%s: look-ahead = %v", c.name, err)
		}
		if took > 500*time.Millisecond {
			t.Errorf("%s: the look-ahead answered after %v", c.name, took)
		}
	}
	if _, installs, _ := tools.recorded(); tools.ensureCount() != asked || len(installs) != 0 {
		t.Errorf("the look-aheads asked the tools: %d more Ensure calls, installs %v", tools.ensureCount()-asked, installs)
	}
	if probes, _ := fs.calls(); len(probes) != 3 {
		t.Errorf("a look-ahead resolved the page without the tools: %d probes", len(probes))
	}
	// Copies not looked for yet since downexd started are not refused: discovery never
	// installs, and the resolver waits for it (tools.Manager.AcquireReady).
	tools.setDiscovering(true)
	if _, err := h.probeMedia(ahead); err != nil {
		t.Errorf("look-ahead while the copies are being looked for = %v", err)
	}
	tools.setDiscovering(false)
	if probes, _ := fs.calls(); len(probes) != 4 || probes[3] != (SiteSelection{PageURL: testPage, Prefetch: true}) {
		t.Errorf("the look-ahead during discovery did not reach the resolver: %+v", probes)
	}
	if n := tools.ensureCount(); n != asked {
		t.Errorf("the look-ahead during discovery called Ensure %d times", n-asked)
	}

	// The resolver will not queue it behind other work: site_resolving at once.
	fs.set(func(f *fakeSite) { f.probeErr = fmt.Errorf("%w: busy with other videos", sitemedia.ErrSiteResolving) })
	start := time.Now()
	_, err = h.probeMedia(ahead)
	if mediaCode(t, err) != CodeSiteResolving || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("busy look-ahead = %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("the busy look-ahead answered after %v", took)
	}
	fs.set(func(f *fakeSite) { f.probeErr = nil })

	// Only pages have look-aheads; site media off refuses them like any request.
	_, err = h.probeMedia(&downexv1.ProbeMediaRequest{ManifestUrl: testManifest, Prefetch: true})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "prefetch") {
		t.Errorf("manifest look-ahead = %v", err)
	}
	s := protoSettings(h.m.Settings())
	s.SiteMedia = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	if _, err := h.probeMedia(ahead); mediaCode(t, err) != CodeSiteMediaOff {
		t.Errorf("look-ahead with site media off = %v", err)
	}
}

// TestSiteRefererWithoutFragment: a Referer's fragment (which a browser never sends) is
// dropped before the resolver and the stored record see it.
func TestSiteRefererWithoutFragment(t *testing.T) {
	fs := newFakeSite(1)
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	headers := []*downexv1.Header{{Name: "Referer", Value: "https://blog.test/post?id=7#access_token=secret"}}
	if _, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage, Headers: headers}); err != nil {
		t.Fatal(err)
	}
	d := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser, Headers: headers})
	probes, _ := fs.calls()
	for _, p := range probes {
		if p.Referer != "https://blog.test/post?id=7" {
			t.Errorf("probe Referer = %q", p.Referer)
		}
	}
	if ref := storedState(t, h.st, d.GetId()).Request.Headers.Get("Referer"); ref != "https://blog.test/post?id=7" {
		t.Errorf("stored Referer = %q", ref)
	}
}

// TestSiteRequestErrors: malformed requests for a page are InvalidArgument and reach
// neither the tools nor the resolver.
func TestSiteRequestErrors(t *testing.T) {
	fs, tools := newFakeSite(1), newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: tools})
	for _, c := range []struct {
		name string
		req  *downexv1.ProbeMediaRequest
		want string
	}{
		{"manifest", &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage, ManifestUrl: testManifest}, "manifest_url must be empty"},
		{"no page", &downexv1.ProbeMediaRequest{Origin: originSite}, "page_url"},
		{"ftp page", &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: "ftp://site.test/v"}, "page_url"},
		{"page login", &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: "https://alice:s3cret@intranet.test/video/1"},
			"page_url"},
		{"referer login", &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage,
			Headers: []*downexv1.Header{{Name: "Referer", Value: "https://bob:hunter2@portal.test/"}}}, "Referer"},
		{"referer", &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage,
			Headers: []*downexv1.Header{{Name: "Referer", Value: "javascript:alert(1)"}}}, "Referer"},
		{"header", &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage,
			Headers: []*downexv1.Header{{Name: "Cookie", Value: "a\nb"}}}, "Cookie"},
		{"origin", &downexv1.ProbeMediaRequest{Origin: downexv1.MediaOrigin(9), PageUrl: testPage}, "origin"},
	} {
		_, err := h.probeMedia(c.req)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want InvalidArgument naming %s", c.name, err, c.want)
		}
		if err != nil && (strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "hunter2")) {
			t.Errorf("%s: the error repeats a password: %v", c.name, err)
		}
		add := &downexv1.AddMediaRequest{Origin: c.req.GetOrigin(), PageUrl: c.req.GetPageUrl(), ManifestUrl: c.req.GetManifestUrl(),
			Headers: c.req.GetHeaders()}
		if _, err := h.addMediaReq(add); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: AddMedia = %v, want InvalidArgument", c.name, err)
		}
	}
	_, err := h.addMediaReq(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, VideoTrack: "a\x01"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad track id: %v", err)
	}
	if probes, _ := fs.calls(); len(probes) != 0 {
		t.Errorf("the resolver was asked: %+v", probes)
	}
	if n := tools.ensureCount(); n != 0 || len(h.list()) != 0 {
		t.Errorf("tools asked %d times; %d downloads", n, len(h.list()))
	}
}

// TestSiteRefusalCodes: every refusal of a page carries its code as message prefix and
// metadata, with the Connect code of docs/SITE-MEDIA-SPEC.md §3.2; AddMedia records
// nothing then.
func TestSiteRefusalCodes(t *testing.T) {
	fs, tools := newFakeSite(1), newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: tools})
	audioOnly := fs.probe
	audioOnly.Tracks, audioOnly.DefaultVideo = audioOnly.Tracks[3:], ""
	webm := fs.probe
	webm.Container = "webm"
	for _, c := range []struct {
		name      string
		probeErr  error
		ensureErr error
		probe     *SiteProbe
		code      string
		cc        connect.Code
		text      string // in the message
	}{
		{"unsupported page", fmt.Errorf("%w: Unsupported URL", sitemedia.ErrSiteUnsupported), nil, nil, CodeSiteUnsupported, connect.CodeInvalidArgument, ""},
		{"unavailable", fmt.Errorf("%w: Private video", sitemedia.ErrSiteUnavailable), nil, nil, CodeSiteUnavailable, connect.CodeFailedPrecondition, ""},
		{"sign-in", fmt.Errorf("%w: Sign in to confirm your age", sitemedia.ErrAuthRequired), nil, nil, CodeAuthRequired, connect.CodeFailedPrecondition, ""},
		{"yt-dlp", fmt.Errorf("%w: exit code 2", sitemedia.ErrSiteError), nil, nil, CodeSiteError, connect.CodeFailedPrecondition, ""},
		{"drm", fmt.Errorf("sitemedia: %w", media.ErrEncrypted), nil, nil, CodeEncrypted, connect.CodeInvalidArgument, ""},
		{"live", fmt.Errorf("sitemedia: %w", media.ErrLive), nil, nil, CodeLive, connect.CodeInvalidArgument, ""},
		{"playlist", fmt.Errorf("sitemedia: playlist: %w", media.ErrUnsupported), nil, nil, CodeMediaUnsupported, connect.CodeInvalidArgument, ""},
		{"malformed", fmt.Errorf("sitemedia: %w", media.ErrMalformed), nil, nil, CodeMediaInvalid, connect.CodeInvalidArgument, ""},
		{"network", &net404{}, nil, nil, CodeHTTPStatus, connect.CodeFailedPrecondition, ""},
		{"tools failed", nil, fmt.Errorf("%w: could not download yt-dlp (network)", sitemedia.ErrToolsUnavailable), nil, CodeToolsUnavailable, connect.CodeFailedPrecondition, ""},
		{"tools off", nil, sitemedia.ErrSiteMediaOff, nil, CodeSiteMediaOff, connect.CodeFailedPrecondition, ""},
		{"audio only", nil, nil, &audioOnly, CodeMediaUnsupported, connect.CodeInvalidArgument, "no MP4 video on this page"},
		{"not mp4", nil, nil, &webm, CodeMediaUnsupported, connect.CodeInvalidArgument, "no MP4 video on this page"},
	} {
		fs.set(func(f *fakeSite) {
			f.probeErr = c.probeErr
			if c.probe != nil {
				f.probe = *c.probe
			} else {
				f.probe = newFakeSite(1).probe
			}
		})
		tools.setEnsureErr(c.ensureErr)
		for _, call := range []string{"probe", "add"} {
			var err error
			if call == "probe" {
				_, err = h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage})
			} else {
				_, err = h.addMediaReq(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage})
			}
			if err == nil {
				t.Errorf("%s: %s succeeded", c.name, call)
				continue
			}
			if code := mediaCode(t, err); code != c.code || connect.CodeOf(err) != c.cc || !strings.Contains(err.Error(), c.text) {
				t.Errorf("%s: %s = %v (%s), want %s with %v", c.name, call, err, code, c.code, c.cc)
			}
		}
	}
	if n := len(h.list()); n != 0 {
		t.Errorf("%d downloads recorded by refused adds", n)
	}

	// Site media turned off in the settings: refused before the tools are asked.
	tools.setEnsureErr(nil)
	asked := tools.ensureCount()
	s := protoSettings(h.m.Settings())
	s.SiteMedia = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	_, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage})
	if mediaCode(t, err) != CodeSiteMediaOff || connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Settings → Video sites") {
		t.Errorf("site media off: %v", err)
	}
	if tools.ensureCount() != asked {
		t.Error("the tools were asked while site media is off")
	}
}

// net404 is an HTTP status failure of a manifest fallback's probe.
type net404 struct{}

func (*net404) Error() string { return "mediajob: unexpected HTTP status 404 Not Found" }

// TestSiteWithoutConfig: a daemon without Config.Site refuses pages with
// media_unsupported; manifests are unaffected by the site-media setting.
func TestSiteWithoutConfig(t *testing.T) {
	h := newHarness(t, harnessOptions{media: newFakeMedia(1)})
	_, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage})
	if mediaCode(t, err) != CodeMediaUnsupported || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("probe = %v", err)
	}
	_, err = h.addMediaReq(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage})
	if mediaCode(t, err) != CodeMediaUnsupported {
		t.Errorf("add = %v", err)
	}
	_, err = h.dm.GetTools(ctxT(t), connect.NewRequest(&downexv1.GetToolsRequest{}))
	if mediaCode(t, err) != CodeToolsUnavailable || connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("GetTools = %v", err)
	}
	s := protoSettings(h.m.Settings())
	s.SiteMedia = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	if mi, err := h.probeMedia(&downexv1.ProbeMediaRequest{ManifestUrl: testManifest, PageUrl: testPage}); err != nil ||
		mi.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_UNSPECIFIED || mi.GetManifestUrl() != testManifest {
		t.Errorf("manifest probe with site media off = %v, %v", mi, err)
	}
}

// TestSiteWaitBudget: while the tools install or the page resolves, AddMedia and
// ProbeMedia answer tools_installing or site_resolving before the caller's deadline
// (two seconds early), and record nothing; the work goes on and a retry succeeds.
func TestSiteWaitBudget(t *testing.T) {
	fs, tools := newFakeSite(1), newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: tools})
	call := func(add bool) (time.Duration, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		start := time.Now()
		var err error
		if add {
			_, err = h.dl.AddMedia(ctx, connect.NewRequest(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage}))
		} else {
			_, err = h.dl.ProbeMedia(ctx, connect.NewRequest(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage}))
		}
		return time.Since(start), err
	}

	// While the tools are being installed, a request answers tools_installing with the
	// progress after about siteInstallWait, not at the end of its budget: clients retry
	// every 2 s and show the progress as it goes.
	tools.setInstalling(true)
	for _, add := range []bool{false, true} {
		took, err := call(add)
		if mediaCode(t, err) != CodeToolsInstalling || connect.CodeOf(err) != connect.CodeFailedPrecondition ||
			!strings.Contains(err.Error(), "tools_installing: downloading yt-dlp and Deno (34%)") {
			t.Errorf("add %v while installing: %v", add, err)
		}
		if took < siteInstallWait-100*time.Millisecond || took > siteInstallWait+time.Second {
			t.Errorf("add %v answered after %v, want about %v", add, took, siteInstallWait)
		}
	}
	if probes, _ := fs.calls(); len(probes) != 0 {
		t.Error("the page was resolved before the tools were ready")
	}
	tools.setInstalling(false)

	fs.set(func(f *fakeSite) { f.probeWait = make(chan struct{}) })
	for _, add := range []bool{false, true} {
		took, err := call(add)
		if mediaCode(t, err) != CodeSiteResolving || connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("add %v while resolving: %v", add, err)
		}
		if took > 3500*time.Millisecond {
			t.Errorf("add %v answered after %v", add, took)
		}
	}
	if len(h.list()) != 0 {
		t.Error("a download was recorded while the page was resolving")
	}
	fs.set(func(f *fakeSite) { f.probeWait = nil })
	if _, err := call(true); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := len(h.list()); n != 1 {
		t.Errorf("%d downloads after the retry", n)
	}

	// The caller's own cancellation is returned as such.
	tools.setInstalling(true)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := h.m.ProbeMedia(ctx, &downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled probe = %v", err)
	}
	tools.setInstalling(false)
}

// TestSiteRunProbesUntilProgress: a site download stays probing while it waits for the
// tools and until its job reports progress; then its size is the size of its tracks, and
// once completed the file's. A pause while probing pauses it.
func TestSiteRunProbesUntilProgress(t *testing.T) {
	fs, tools := newFakeSite(3), newFakeTools()
	fs.job.gate = make(chan struct{})
	start := make(chan struct{})
	fs.startGate = start
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: tools})
	d := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, StartPaused: true})
	// The tools are updated (or deleted) between the add and the run.
	tools.setInstalling(true)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	h.waitState(d.GetId(), stProbing)
	time.Sleep(100 * time.Millisecond)
	if _, runs := fs.calls(); len(runs) != 0 {
		t.Fatal("the job ran before the tools were ready")
	}
	if got := h.get(d.GetId()); got.GetState() != stProbing || got.GetSize() != -1 {
		t.Fatalf("waiting for the tools = %v", got)
	}

	// A pause while waiting for the tools: paused, not failed.
	if p := h.pause(d.GetId()); p.GetState() != stPaused {
		t.Fatalf("paused = %v", p)
	}
	h.waitState(d.GetId(), stPaused)
	tools.setInstalling(false)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	waitUntil(t, "the job's start", func() bool { _, runs := fs.calls(); return len(runs) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := h.get(d.GetId()); got.GetState() != stProbing || got.GetSize() != -1 {
		t.Fatalf("resolving = %v", got)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.State != model.StateProbing {
		t.Errorf("stored state while resolving = %v", rec.State)
	}

	close(start)
	fs.job.feed(t, 1)
	got := h.waitFor(d.GetId(), "the first progress", func(d *downexv1.Download) bool { return d.GetState() == stDownloading })
	if got.GetSize() != 416_000_000 || got.GetReceived() == 0 {
		t.Errorf("downloading = %v", got)
	}
	waitUntil(t, "a stored checkpoint", func() bool {
		rec := storedState(t, h.st, d.GetId())
		return rec.State == model.StateDownloading && rec.Media.Checkpoint != nil
	})
	fs.job.feed(t, 2)
	done := h.waitCompleted(d.GetId())
	if done.GetSize() != int64(len(wantOutput(3))) {
		t.Errorf("completed size = %d", done.GetSize())
	}
	_, runs := fs.calls()
	if r := runs[0]; r.opts.Factory == nil || r.opts.Limiter == nil || r.sel.Format != "http" || r.sel.VideoKey != "s1-v137" {
		t.Errorf("run = %+v", r)
	}
}

// TestSiteRunFailures: a site run fails with the code of its cause, which also maps back
// from the stored message; site media off fails runs that have not started.
func TestSiteRunFailures(t *testing.T) {
	fs, tools := newFakeSite(2), newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: tools})
	add := func() int64 {
		return h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, StartPaused: true}).GetId()
	}
	for _, c := range []struct {
		name  string
		setup func()
		code  string
	}{
		{"yt-dlp", func() {
			fs.set(func(f *fakeSite) { f.runErr = fmt.Errorf("%w: HTTP Error 500", sitemedia.ErrSiteError) })
		}, CodeSiteError},
		{"gone quality", func() {
			fs.set(func(f *fakeSite) {
				f.runErr = fmt.Errorf("mediajob: invalid track selection: %w: the chosen quality is no longer offered", media.ErrNotFound)
			})
		}, CodeMediaInvalid},
		{"tools", func() {
			tools.setEnsureErr(fmt.Errorf("%w: yt-dlp failed its signature check", sitemedia.ErrToolsUnavailable))
		}, CodeToolsUnavailable},
		{"not MP4", func() {
			fs.job.set(func(f *fakeMedia) { f.container = "ts" })
		}, CodeMediaUnsupported},
		{"off", func() {
			s := protoSettings(h.m.Settings())
			s.SiteMedia = false
			if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
				t.Fatal(err)
			}
		}, CodeSiteMediaOff},
	} {
		fs.set(func(f *fakeSite) { f.runErr = nil })
		fs.job.set(func(f *fakeMedia) { f.container = "mp4" })
		tools.setEnsureErr(nil)
		id := add()
		c.setup()
		h.resume(&downexv1.ResumeRequest{Id: id})
		d := h.waitState(id, stFailed)
		if d.GetErrorCode() != c.code {
			t.Errorf("%s: failed with %q (%s), want %s", c.name, d.GetErrorCode(), d.GetError(), c.code)
		}
		if c.code == CodeMediaUnsupported {
			// A file that is not an MP4 is never left under the .mp4 name.
			_, runs := fs.calls()
			if _, err := os.Stat(runs[len(runs)-1].opts.OutPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s: the output is still there (%v)", c.name, err)
			}
		}
		rec := storedState(t, h.st, id)
		if got := recordErrorCode(&rec); got != c.code {
			t.Errorf("%s: stored message %q maps to %q", c.name, rec.Error, got)
		}
	}
}

// TestSiteErrorCodeMapping: every site sentinel maps to its code, live and from its
// message, also wrapped with a reason and inside other text.
func TestSiteErrorCodeMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{sitemedia.ErrSiteUnsupported, CodeSiteUnsupported},
		{sitemedia.ErrSiteUnavailable, CodeSiteUnavailable},
		{sitemedia.ErrAuthRequired, CodeAuthRequired},
		{sitemedia.ErrSiteError, CodeSiteError},
		{sitemedia.ErrSiteResolving, CodeSiteResolving},
		{sitemedia.ErrToolsInstalling, CodeToolsInstalling},
		{sitemedia.ErrToolsUnavailable, CodeToolsUnavailable},
		{sitemedia.ErrSiteMediaOff, CodeSiteMediaOff},
	} {
		// A reason that names other failures does not hide the cause.
		err := fmt.Errorf("%w: ERROR: connection reset; track not found (EOF)", c.err)
		if got := mediaErrorCode(err); got != c.code {
			t.Errorf("mediaErrorCode(%v) = %q, want %q", err, got, c.code)
		}
		if got := codeFromMessage(err.Error()); got != c.code {
			t.Errorf("codeFromMessage(%q) = %q, want %q", err, got, c.code)
		}
		if got := recordErrorCode(&model.Download{Kind: model.KindMedia, Error: "run: " + err.Error()}); got != c.code {
			t.Errorf("recordErrorCode(%q) = %q, want %q", err, got, c.code)
		}
	}
	if got := mediaErrorCode(fmt.Errorf("%w (34%%)", sitemedia.ErrToolsInstalling)); got != CodeToolsInstalling {
		t.Errorf("installing with progress = %q", got)
	}
}

// TestSiteProxyRoute: yt-dlp gets the route of the network settings for the page: direct,
// or the first proxy with the credentials Downex has for it.
func TestSiteProxyRoute(t *testing.T) {
	keys := newMemKeyring()
	rt := NewNetworkRuntime(keys, nil)
	fs := newFakeSite(1)
	h := newSiteHarness(t, harnessOptions{network: rt}, siteHarnessOptions{site: fs})
	setProxy := func(p *downexv1.ProxySettings) {
		t.Helper()
		s := protoSettings(h.m.Settings())
		s.Proxy = p
		if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
			t.Fatal(err)
		}
	}
	setProxy(&downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_NONE})
	if p, err := h.m.siteProxy(ctxT(t), testPage); err != nil || p != (SiteProxy{}) {
		t.Errorf("none = %+v, %v", p, err)
	}
	setProxy(&downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy.test:3128",
		HttpsProxy: "proxy.test:3128", Username: "alice"})
	if err := keys.Set(secretName(SecretKindProxy, ""), []byte("pw")); err != nil {
		t.Fatal(err)
	}
	want := SiteProxy{URL: "http://proxy.test:3128", Username: "alice", Password: "pw"}
	if p, err := h.m.siteProxy(ctxT(t), testPage); err != nil || p != want {
		t.Errorf("manual = %+v, %v", p, err)
	}
	if _, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage}); err != nil {
		t.Fatal(err)
	}
	if probes, _ := fs.calls(); len(probes) != 1 || probes[0].Proxy != want {
		t.Errorf("probe proxy = %+v", probes)
	}
	setProxy(&downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, SocksProxy: "socks5://socks.test:1080",
		Bypass: "*.local"})
	// SOCKS5 names reach yt-dlp as socks5h, so that the proxy resolves host names as
	// Downex's own SOCKS5 routes do.
	if p, err := h.m.siteProxy(ctxT(t), testPage); err != nil || p.URL != "socks5h://socks.test:1080" {
		t.Errorf("socks = %+v, %v", p, err)
	}
	if p, err := h.m.siteProxy(ctxT(t), "https://cam.local/v"); err != nil || p != (SiteProxy{}) {
		t.Errorf("bypassed = %+v, %v", p, err)
	}
}

// TestSiteMediaRunsGetHostRules: the runs of site and manifest downloads get the engine's
// host rules (docs/SITE-MEDIA-SPEC.md §3.3).
func TestSiteMediaRunsGetHostRules(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	fs, fm := newFakeSite(1), newFakeMedia(1)
	h := newSiteHarness(t, harnessOptions{media: fm}, siteHarnessOptions{site: fs, hostRules: rt})
	a := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage})
	b := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	h.waitCompleted(a.GetId())
	h.waitCompleted(b.GetId())
	_, siteRuns := fs.calls()
	_, mediaRuns := fm.calls()
	if len(siteRuns) != 1 || siteRuns[0].opts.HostRules != rt || len(mediaRuns) != 1 || mediaRuns[0].opts.HostRules != rt {
		t.Errorf("host rules: site %+v, manifest %+v", siteRuns, mediaRuns)
	}
}
