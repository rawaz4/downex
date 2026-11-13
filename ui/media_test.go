package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The JSON shapes below are what frontend/src/lib/wails.ts parses (parseDownload,
// parseMediaInfo); change both together.

func mediaDownload() *downexv1.Download {
	return &downexv1.Download{
		Id: 6, Url: "https://media.example.org/talk/master.m3u8", Dir: `C:\Downloads\Video`, Filename: "Keynote.mp4",
		Size: -1, Received: 3 << 30, State: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION,
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER, Category: "Video",
		Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: sampleMedia("https://media.example.org/talk/master.m3u8"),
	}
}

func TestMediaDownloadJSONShape(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(mediaDownload()), nil)
	raw, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	d := decodeJSON(t, raw)["downloads"].([]any)[0].(map[string]any)
	if d["kind"] != "DOWNLOAD_KIND_MEDIA" || d["received"] != "3221225472" || d["size"] != "-1" {
		t.Errorf("download = %v", d)
	}
	want := map[string]any{
		"manifestUrl": "https://media.example.org/talk/master.m3u8",
		"format":      "hls",
		"tracks": []any{
			map[string]any{"id": "v0", "kind": "video", "bandwidth": "800000", "width": float64(640), "height": float64(360),
				"frameRate": float64(25), "codecs": "avc1.4d401e"},
			map[string]any{"id": "v1", "kind": "video", "bandwidth": "5000000", "width": float64(1920), "height": float64(1080),
				"frameRate": 29.97, "codecs": "avc1.640028"},
			map[string]any{"id": "v2", "kind": "muxed", "bandwidth": "1200000", "width": float64(960), "height": float64(540),
				"codecs": "avc1.4d401f,mp4a.40.2"},
			map[string]any{"id": "r0", "kind": "audio", "bandwidth": "128000", "codecs": "mp4a.40.2", "language": "en", "name": "English"},
			// unknown bandwidth (0) is omitted
			map[string]any{"id": "r1", "kind": "audio", "codecs": "mp4a.40.2", "language": "de", "name": "Deutsch"},
		},
		"selectedVideo":  "v1",
		"selectedAudio":  "r0",
		"durationMs":     "5400250", // int64 as a decimal string
		"fragmentsTotal": float64(1800),
		"container":      "mp4",
		"title":          "Keynote",
	}
	if got := d["media"]; !reflect.DeepEqual(got, want) {
		t.Errorf("media =\n%#v\nwant\n%#v", got, want)
	}

	// A file download has neither: kind UNSPECIFIED (= file) and no media are defaults.
	a, _ = newTestApp(t, newFakeDaemon(sampleDownload()), nil)
	raw, err = a.List()
	if err != nil {
		t.Fatal(err)
	}
	d = decodeJSON(t, raw)["downloads"].([]any)[0].(map[string]any)
	for _, k := range []string{"kind", "media"} {
		if _, ok := d[k]; ok {
			t.Errorf("file download has %q: %v", k, d[k])
		}
	}
}

func TestAddMediaForwardsTheRequest(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.AddMedia(json.RawMessage(`{"manifestUrl":"https://media.example.org/talk/master.m3u8",
		"headers":[{"name":"Referer","value":"https://media.example.org/talk"}],"dir":"D:\\Films","filename":"talk",
		"source":"ADD_SOURCE_UI","videoTrack":"v0","audioTrack":"r1","pageUrl":"https://media.example.org/talk",
		"title":"The talk","startPaused":true}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.AddMediaRequest{
		ManifestUrl: "https://media.example.org/talk/master.m3u8",
		Headers:     []*downexv1.Header{{Name: "Referer", Value: "https://media.example.org/talk"}},
		Dir:         `D:\Films`, Filename: "talk", Source: downexv1.AddSource_ADD_SOURCE_UI,
		VideoTrack: "v0", AudioTrack: "r1", PageUrl: "https://media.example.org/talk", Title: "The talk", StartPaused: true,
	}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
	d := decodeJSON(t, raw)["download"].(map[string]any)
	media, _ := d["media"].(map[string]any)
	if d["id"] != "98" || d["kind"] != "DOWNLOAD_KIND_MEDIA" || d["source"] != "ADD_SOURCE_UI" || media == nil ||
		media["selectedVideo"] != "v0" || media["selectedAudio"] != "r1" {
		t.Errorf("AddMediaResponse = %s", raw)
	}

	// Only the manifest is required.
	if _, err := a.AddMedia(json.RawMessage(`{"manifestUrl":"https://media.example.org/a.mpd"}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/a.mpd"}) {
		t.Errorf("daemon got %v", got)
	}
}

func TestProbeMediaListsTracks(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.ProbeMedia(json.RawMessage(`{"manifestUrl":"https://media.example.org/talk/master.m3u8",
		"headers":[{"name":"Cookie","value":"s=1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.ProbeMediaRequest{
		ManifestUrl: "https://media.example.org/talk/master.m3u8",
		Headers:     []*downexv1.Header{{Name: "Cookie", Value: "s=1"}},
	}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
	v := decodeJSON(t, raw)
	media, ok := v["media"].(map[string]any)
	if !ok || len(v) != 1 {
		t.Fatalf("ProbeMediaResponse = %s", raw)
	}
	if tracks, _ := media["tracks"].([]any); len(tracks) != 5 || media["selectedVideo"] != "v1" || media["durationMs"] != "5400250" {
		t.Errorf("media = %v", media)
	}
}

func TestMediaErrorsAreConnectText(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	for name, call := range map[string]func(json.RawMessage) (json.RawMessage, error){"AddMedia": a.AddMedia, "ProbeMedia": a.ProbeMedia} {
		_, err := call(json.RawMessage(`{"manifestUrl":"https://media.example.org/drm/master.m3u8"}`))
		if err == nil || err.Error() != "invalid_argument: encrypted: the stream is encrypted or DRM-protected" {
			t.Errorf("%s of an encrypted stream = %v", name, err)
		}
		if m := frontendError.FindStringSubmatch(errString(err)); m == nil || m[1] != "invalid_argument" {
			t.Errorf("%s: %v is not \"<code>: <message>\"", name, err)
		}
		_, err = call(json.RawMessage(`{"manifestUrl":"https://media.example.org/a.m3u8","url":"x"}`))
		if err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: malformed request: ") {
			t.Errorf("%s with an unknown field = %v", name, err)
		}
	}
	f.mu.Lock()
	f.err = connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running"))
	f.mu.Unlock()
	if _, err := a.ProbeMedia(nil); err == nil || err.Error() != "unavailable: downexd is not running" {
		t.Errorf("ProbeMedia while unreachable = %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestConfirmForwardsTheTracks(t *testing.T) {
	f := newFakeDaemon(mediaDownload())
	a, _ := newTestApp(t, f, nil)
	raw, err := a.Confirm(json.RawMessage(`{"id":"6","start":true,"videoTrack":"v0","audioTrack":"r1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.ConfirmRequest{Id: 6, Start: true, VideoTrack: "v0", AudioTrack: "r1"}) {
		t.Errorf("Confirm sent %v", got)
	}
	media := decodeJSON(t, raw)["download"].(map[string]any)["media"].(map[string]any)
	if media["selectedVideo"] != "v0" || media["selectedAudio"] != "r1" {
		t.Errorf("ConfirmResponse media = %v", media)
	}
	// Left out = the daemon's pick.
	f.mu.Lock()
	f.downloads[6] = mediaDownload()
	f.mu.Unlock()
	if _, err := a.Confirm(json.RawMessage(`{"id":6,"start":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.ConfirmRequest); got.GetVideoTrack() != "" || got.GetAudioTrack() != "" {
		t.Errorf("Confirm without tracks sent %v", got)
	}
}

func TestClientDaemonMedia(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	a, _ := newTestApp(t, newClientDaemon(sys.env(t)), nil)
	raw, err := a.ProbeMedia(json.RawMessage(`{"manifestUrl":"https://media.example.org/a.mpd"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"format":"dash"`) || !strings.Contains(string(raw), `"manifestUrl":"https://media.example.org/a.mpd"`) {
		t.Errorf("ProbeMedia = %s", raw)
	}
	raw, err = a.AddMedia(json.RawMessage(`{"manifestUrl":"https://media.example.org/a.mpd","videoTrack":"video-720"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"DOWNLOAD_KIND_MEDIA"`) || !strings.Contains(string(raw), `"selectedVideo":"video-720"`) {
		t.Errorf("AddMedia = %s", raw)
	}
	// The daemon's refusal survives the real transport as Connect text.
	if _, err := a.AddMedia(json.RawMessage(`{"manifestUrl":"https://media.example.org/live.mpd"}`)); err == nil ||
		err.Error() != "invalid_argument: live: the presentation is live" {
		t.Errorf("AddMedia of a live stream = %v", err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}

// ---- site media (docs/SITE-MEDIA-SPEC.md §3.1, §5.4) ------------------------------------

func siteDownload() *downexv1.Download {
	return &downexv1.Download{
		Id: 12, Url: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", Dir: `C:\Downloads\Video`, Filename: "A talk.mp4",
		Size: -1, ExpectedSize: 339_700_000, State: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION,
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER, Category: "Video",
		Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: sampleSiteMedia("https://www.youtube.com/watch?v=aqz-KE-bpKQ"),
	}
}

func TestSiteMediaJSONShape(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(siteDownload()), nil)
	raw, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	d := decodeJSON(t, raw)["downloads"].([]any)[0].(map[string]any)
	if d["url"] != "https://www.youtube.com/watch?v=aqz-KE-bpKQ" || d["expectedSize"] != "339700000" {
		t.Errorf("download = %v", d)
	}
	media := d["media"].(map[string]any)
	want := map[string]any{
		// no manifestUrl: a page has none
		"format":     "http",
		"container":  "mp4",
		"origin":     "MEDIA_ORIGIN_SITE",
		"site":       "YouTube",
		"pageUrl":    "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		"title":      "A talk",
		"durationMs": "600000",
		"tracks": []any{
			map[string]any{"id": "137", "kind": "video", "bandwidth": "4400000", "width": float64(1920), "height": float64(1080),
				"frameRate": float64(30), "codecs": "avc1.640028", "name": "1080p (Full HD)", "size": "330000000", "ext": "mp4"},
			// an estimated size says so
			map[string]any{"id": "399", "kind": "video", "bandwidth": "2100000", "width": float64(1280), "height": float64(720),
				"frameRate": float64(30), "codecs": "av01.0.08M.08", "name": "720p (HD)", "size": "157500000", "sizeApprox": true, "ext": "mp4"},
			// an unknown size (0) is omitted
			map[string]any{"id": "18", "kind": "muxed", "bandwidth": "600000", "width": float64(640), "height": float64(360),
				"frameRate": float64(30), "codecs": "avc1.42001E, mp4a.40.2", "name": "360p", "ext": "mp4"},
			map[string]any{"id": "140", "kind": "audio", "bandwidth": "129000", "codecs": "mp4a.40.2", "language": "en",
				"name": "medium", "size": "9700000", "ext": "m4a"},
		},
		"selectedVideo": "137",
		"selectedAudio": "140",
	}
	if !reflect.DeepEqual(media, want) {
		t.Errorf("media =\n%#v\nwant\n%#v", media, want)
	}

	// A manifest's origin is the proto default: it is left out, as before site media.
	a, _ = newTestApp(t, newFakeDaemon(mediaDownload()), nil)
	raw, err = a.List()
	if err != nil {
		t.Fatal(err)
	}
	media = decodeJSON(t, raw)["downloads"].([]any)[0].(map[string]any)["media"].(map[string]any)
	for _, k := range []string{"origin", "site", "pageUrl"} {
		if _, ok := media[k]; ok {
			t.Errorf("manifest media has %q: %v", k, media[k])
		}
	}
}

func TestProbeSiteForwardsThePage(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.ProbeMedia(json.RawMessage(`{"pageUrl":"https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		"origin":"MEDIA_ORIGIN_SITE","headers":[{"name":"Referer","value":"https://example.org/"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.ProbeMediaRequest{
		PageUrl: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE,
		Headers: []*downexv1.Header{{Name: "Referer", Value: "https://example.org/"}},
	}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
	media := decodeJSON(t, raw)["media"].(map[string]any)
	if tracks, _ := media["tracks"].([]any); len(tracks) != 4 || media["origin"] != "MEDIA_ORIGIN_SITE" || media["selectedVideo"] != "137" {
		t.Errorf("media = %v", media)
	}
	// The origin also arrives as its number.
	if _, err := a.ProbeMedia(json.RawMessage(`{"pageUrl":"https://example.org/v","origin":2}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.ProbeMediaRequest); !isSite(got.GetOrigin()) || got.GetPageUrl() != "https://example.org/v" {
		t.Errorf("daemon got %v", got)
	}
}

func TestAddSiteMediaForwardsTheRequest(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.AddMedia(json.RawMessage(`{"pageUrl":"https://www.youtube.com/watch?v=aqz-KE-bpKQ","origin":"MEDIA_ORIGIN_SITE",
		"dir":"D:\\Films","filename":"talk","source":"ADD_SOURCE_UI","videoTrack":"399","audioTrack":"140","queueId":"3"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.AddMediaRequest{
		PageUrl: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE,
		Dir: `D:\Films`, Filename: "talk", Source: downexv1.AddSource_ADD_SOURCE_UI, VideoTrack: "399", AudioTrack: "140", QueueId: 3,
	}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
	d := decodeJSON(t, raw)["download"].(map[string]any)
	media, _ := d["media"].(map[string]any)
	if d["filename"] != "A talk.mp4" || media == nil || media["selectedVideo"] != "399" || media["origin"] != "MEDIA_ORIGIN_SITE" {
		t.Errorf("AddMediaResponse = %s", raw)
	}
}

func TestConfirmSiteSendsTheChosenQuality(t *testing.T) {
	f := newFakeDaemon(siteDownload())
	a, _ := newTestApp(t, f, nil)
	raw, err := a.Confirm(json.RawMessage(`{"id":"12","start":true,"videoTrack":"18"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.ConfirmRequest{Id: 12, Start: true, VideoTrack: "18"}) {
		t.Errorf("Confirm sent %v", got)
	}
	d := decodeJSON(t, raw)["download"].(map[string]any)
	if d["filename"] != "A talk.mp4" || d["media"].(map[string]any)["selectedVideo"] != "18" {
		t.Errorf("ConfirmResponse = %s", raw)
	}
}

func TestSiteErrorsAreConnectText(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	for _, c := range []struct {
		err  error
		want string
	}{
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("tools_installing: downloading yt-dlp and Deno (34%)")),
			"failed_precondition: tools_installing: downloading yt-dlp and Deno (34%)"},
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("site_resolving: still getting the video info")),
			"failed_precondition: site_resolving: still getting the video info"},
		{connect.NewError(connect.CodeInvalidArgument, errors.New("site_unsupported: no video found on this page")),
			"invalid_argument: site_unsupported: no video found on this page"},
	} {
		f.mu.Lock()
		f.siteErr = c.err
		f.mu.Unlock()
		for name, call := range map[string]func(json.RawMessage) (json.RawMessage, error){"AddMedia": a.AddMedia, "ProbeMedia": a.ProbeMedia} {
			_, err := call(json.RawMessage(`{"pageUrl":"https://example.org/v","origin":"MEDIA_ORIGIN_SITE"}`))
			if err == nil || err.Error() != c.want {
				t.Errorf("%s = %v, want %q", name, err, c.want)
			}
			if m := frontendError.FindStringSubmatch(errString(err)); m == nil {
				t.Errorf("%s: %v is not \"<code>: <message>\"", name, err)
			}
		}
	}
	if _, err := a.ProbeMedia(json.RawMessage(`{"pageUrl":"https://example.org/v","origin":"MEDIA_ORIGIN_PAGE"}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_argument: malformed request: ") {
		t.Errorf("ProbeMedia with an unknown origin = %v", err)
	}
}

func sampleTools() *downexv1.ToolsStatus {
	return &downexv1.ToolsStatus{
		Enabled: true,
		Tools: []*downexv1.ToolInfo{
			{Name: "yt-dlp", State: downexv1.ToolState_TOOL_STATE_INSTALLING, BytesDone: 6 << 20, BytesTotal: 18 << 20,
				LatestVersion: "2026.10.10.233141", CheckedAt: ts("2026-10-22T08:00:00Z")},
			{Name: "deno", State: downexv1.ToolState_TOOL_STATE_FAILED, BytesTotal: -1, Error: "could not download deno (network)",
				ErrorCode: "network"},
		},
	}
}

func TestGetToolsJSONShape(t *testing.T) {
	f := newFakeDaemon()
	f.tools = sampleTools()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.GetTools()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"status": map[string]any{
		"enabled": true, // ready false is the proto default: omitted
		"tools": []any{
			map[string]any{"name": "yt-dlp", "state": "TOOL_STATE_INSTALLING", "bytesDone": "6291456", "bytesTotal": "18874368",
				"latestVersion": "2026.10.10.233141", "checkedAt": "2026-10-22T08:00:00Z"},
			// an unknown total is sent as -1
			map[string]any{"name": "deno", "state": "TOOL_STATE_FAILED", "bytesTotal": "-1",
				"error": "could not download deno (network)", "errorCode": "network"},
		},
	}}
	if got := decodeJSON(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("GetTools =\n%#v\nwant\n%#v", got, want)
	}
	// A daemon without site media.
	f.mu.Lock()
	f.tools = nil
	f.mu.Unlock()
	if _, err := a.GetTools(); err == nil || !strings.HasPrefix(err.Error(), "unimplemented: ") {
		t.Errorf("GetTools of an old daemon = %v", err)
	}
}

func TestInstallToolsForwardsUpdate(t *testing.T) {
	f := newFakeDaemon()
	f.tools = sampleTools()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.InstallTools(json.RawMessage(`{"update":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.InstallToolsRequest{Update: true}) {
		t.Errorf("daemon got %v", got)
	}
	status := decodeJSON(t, raw)["status"].(map[string]any)
	if status["ready"] != true || status["tools"].([]any)[1].(map[string]any)["state"] != "TOOL_STATE_READY" {
		t.Errorf("InstallTools = %s", raw)
	}
	for _, in := range []string{`{}`, ``, `null`} {
		if _, err := a.InstallTools(json.RawMessage(in)); err != nil {
			t.Fatalf("InstallTools(%q) = %v", in, err)
		}
		if got := f.lastRequest(t); !proto.Equal(got, &downexv1.InstallToolsRequest{}) {
			t.Errorf("InstallTools(%q) sent %v", in, got)
		}
	}
	if _, err := a.InstallTools(json.RawMessage(`{"update":"yes"}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_argument: malformed request: ") {
		t.Errorf("InstallTools with a malformed request = %v", err)
	}
	f.mu.Lock()
	f.tools.Enabled = false
	f.mu.Unlock()
	if _, err := a.InstallTools(nil); err == nil ||
		err.Error() != "failed_precondition: site_media_off: video downloads from web pages are turned off (Settings → Video sites)" {
		t.Errorf("InstallTools with site media off = %v", err)
	}
}

func TestToolsEventsReachThePage(t *testing.T) {
	f := newFakeDaemon()
	tools := sampleTools()
	f.watch = func(ctx context.Context) (WatchStream, error) {
		return newFakeStream(ctx, nil,
			&downexv1.WatchResponse{Events: []*downexv1.Event{
				{Kind: &downexv1.Event_Snapshot{Snapshot: &downexv1.Download{Id: 1, Size: -1}}},
				{Kind: &downexv1.Event_Tools{Tools: tools}},
			}},
			&downexv1.WatchResponse{Events: []*downexv1.Event{{Kind: &downexv1.Event_Tools{Tools: &downexv1.ToolsStatus{Enabled: true, Ready: true}}}}},
		), nil
	}
	host := &fakeHost{emitted: make(chan json.RawMessage, 4)}
	newTestApp(t, f, host)
	first := decodeJSON(t, <-host.emitted)["events"].([]any)
	status, _ := first[len(first)-1].(map[string]any)["tools"].(map[string]any)
	if len(first) != 2 || status == nil || status["tools"].([]any)[0].(map[string]any)["state"] != "TOOL_STATE_INSTALLING" {
		t.Errorf("first batch = %v", first)
	}
	second := decodeJSON(t, <-host.emitted)["events"].([]any)
	if want := []any{map[string]any{"tools": map[string]any{"enabled": true, "ready": true}}}; !reflect.DeepEqual(second, want) {
		t.Errorf("second batch = %v, want %v", second, want)
	}
}

func TestClientDaemonTools(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	a, _ := newTestApp(t, newClientDaemon(sys.env(t)), nil)
	// The test daemon has no tools RPCs, like a downexd older than site media: the page
	// gets the Connect text and says that Downex needs an update.
	if _, err := a.GetTools(); err == nil || !strings.HasPrefix(err.Error(), "unimplemented: ") {
		t.Errorf("GetTools = %v", err)
	}
	if _, err := a.InstallTools(json.RawMessage(`{"update":true}`)); err == nil || !strings.HasPrefix(err.Error(), "unimplemented: ") {
		t.Errorf("InstallTools = %v", err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}

func TestUpdateSettingsWritesTheSiteMediaFields(t *testing.T) {
	f := newFakeDaemon()
	f.settings = &downexv1.Settings{SiteMedia: true, ToolsAutoUpdate: true, ToolsChannel: "nightly"}
	a, _ := newTestApp(t, f, nil)
	raw, err := a.UpdateSettings(json.RawMessage(`{"settings":{"siteMedia":false,"toolsAutoUpdate":true,"toolsChannel":"stable",
		"hostRules":[{"pattern":"*.googlevideo.com","maxConnections":4,"maxRequestBytes":"10485760"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	want := &downexv1.Settings{
		ToolsAutoUpdate: true, ToolsChannel: "stable",
		HostRules: []*downexv1.HostRule{{Pattern: "*.googlevideo.com", MaxConnections: 4, MaxRequestBytes: 10 << 20}},
	}
	if !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
	s := decodeJSON(t, raw)["settings"].(map[string]any)
	if _, on := s["siteMedia"]; on || s["toolsChannel"] != "stable" || s["hostRules"].([]any)[0].(map[string]any)["maxRequestBytes"] != "10485760" {
		t.Errorf("UpdateSettings = %s", raw)
	}
}
