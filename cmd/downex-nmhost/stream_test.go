package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/daemon"
	"downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// Stream probes (probeStream, docs/VIDEO-DETECT-SPEC.md §3.5): the MP4 qualities of a
// manifest a page loaded, through ProbeMedia with origin MANIFEST on the video pool.

// testManifest is a signed master playlist; its query must never reach the log.
const testManifest = "https://cdn.test/v/master.m3u8?token=manifest-secret"

// streamInfo is a probed HLS master as downexd's MP4-only probe describes it, with the
// manifest and page the daemon echoes in MediaInfo, which the relay never forwards.
func streamInfo() *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		ManifestUrl: testManifest, Format: "hls", Container: "mp4", Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST,
		PageUrl: "https://site.test/watch?v=page-secret", Title: "Episode 1", DurationMs: 1_320_000,
		SelectedVideo: "v1080", SelectedAudio: "a-en", FragmentsTotal: 440, FragmentsDone: 3,
		Tracks: []*downexv1.MediaTrack{
			{Id: "v1080", Kind: "video", Codecs: "avc1.640028", Width: 1920, Height: 1080, FrameRate: 30, Bandwidth: 6_000_000, Name: "1080p (Full HD)"},
			{Id: "v720", Kind: "video", Codecs: "avc1.4d401f", Width: 1280, Height: 720, FrameRate: 30, Bandwidth: 3_000_000, Name: "720p (HD)"},
			{Id: "a-en", Kind: "audio", Codecs: "mp4a.40.2", Bandwidth: 128_000, Language: "en", Name: "English"},
		},
	}
}

// streamBackend is a videoBackend whose probes answer streamInfo.
func streamBackend() *videoBackend {
	return &videoBackend{fakeBackend: &fakeBackend{}, probe: func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
		return streamInfo(), nil
	}}
}

func TestProbeStream(t *testing.T) {
	be := streamBackend()
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "s", "type": "probeStream", "manifestUrl": testManifest,
		"headers": [][]string{{"Cookie", "sid=cookie-secret"}, {"Referer", "https://site.test/watch"}},
		"pageUrl": "https://site.test/watch?v=1", "deadlineMs": 5000,
		// Fields of other requests mean nothing here: a stream probe is never a look-ahead
		// (it starts downexd) and has no tracks or title.
		"prefetch": true, "videoTrack": "v720", "title": 5, "unknownField": 1})
	m := s.recv()
	if keys := slices.Sorted(maps.Keys(m)); fmt.Sprint(keys) !=
		"[container durationMs format id ok selectedAudio selectedVideo site title tracks type]" {
		t.Fatalf("videoInfo fields %v: %v", keys, m)
	}
	if m["id"] != "s" || m["ok"] != true || m["type"] != "videoInfo" || m["site"] != "" || m["title"] != "Episode 1" ||
		m["durationMs"] != 1_320_000.0 || m["format"] != "hls" || m["container"] != "mp4" ||
		m["selectedVideo"] != "v1080" || m["selectedAudio"] != "a-en" {
		t.Fatalf("videoInfo: %v", m)
	}
	tracks, _ := m["tracks"].([]any)
	if len(tracks) != 3 {
		t.Fatalf("tracks: %v", m["tracks"])
	}
	want := map[string]any{"id": "v720", "kind": "video", "ext": "", "codecs": "avc1.4d401f", "width": 1280.0,
		"height": 720.0, "frameRate": 30.0, "bandwidth": 3e6, "size": 0.0, "sizeApprox": false, "language": "",
		"name": "720p (HD)"}
	if second, _ := tracks[1].(map[string]any); fmt.Sprint(second) != fmt.Sprint(want) {
		t.Fatalf("second track %v, want %v", second, want)
	}
	// Never a URL: neither the manifest nor the page the daemon echoes.
	if b, _ := json.Marshal(m); bytes.Contains(b, []byte("://")) || bytes.Contains(b, []byte("secret")) {
		t.Fatalf("videoInfo carries URLs: %s", b)
	}

	be.mu.Lock()
	req, dl := be.probes[0], be.deadlines[0]
	be.mu.Unlock()
	var hs []string
	for _, h := range req.GetHeaders() {
		hs = append(hs, h.GetName()+": "+h.GetValue())
	}
	if req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST || req.GetManifestUrl() != testManifest ||
		req.GetPageUrl() != "https://site.test/watch?v=1" || req.GetPrefetch() ||
		strings.Join(hs, "|") != "Cookie: sid=cookie-secret|Referer: https://site.test/watch" {
		t.Fatalf("ProbeMediaRequest = %v", req)
	}
	if d := dl.Sub(start); d < 5*time.Second-responseMargin || d > 5*time.Second-responseMargin+time.Second {
		t.Fatalf("backend deadline %v after the request, want about %v", d, 5*time.Second-responseMargin)
	}

	// Without a page and headers: only the manifest.
	s.send(map[string]any{"id": "bare", "type": "probeStream", "manifestUrl": "https://cdn.test/d/manifest.mpd"})
	if m := s.recv(); m["type"] != "videoInfo" {
		t.Fatalf("bare probeStream: %v", m)
	}
	be.mu.Lock()
	req = be.probes[1]
	n := len(be.videoAdds)
	be.mu.Unlock()
	be.fakeBackend.mu.Lock()
	n += len(be.fakeBackend.media) + len(be.fakeBackend.adds)
	be.fakeBackend.mu.Unlock()
	if req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST || req.GetManifestUrl() != "https://cdn.test/d/manifest.mpd" ||
		req.GetPageUrl() != "" || len(req.GetHeaders()) != 0 || req.GetPrefetch() {
		t.Fatalf("ProbeMediaRequest = %v", req)
	}
	if n != 0 {
		t.Fatalf("a stream probe added %d downloads", n)
	}
}

// TestProbeStreamValidation checks the fields of probeStream with addMedia's limits.
func TestProbeStreamValidation(t *testing.T) {
	const manifest = "https://cdn.test/v/master.m3u8?sig=1"
	msg := func(kv ...any) map[string]any {
		m := map[string]any{"id": "x", "type": typeProbeStream, "manifestUrl": manifest}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(m, kv[i].(string))
			} else {
				m[kv[i].(string)] = kv[i+1]
			}
		}
		return m
	}
	many := make([][]string, validate.MaxHeaderCount+1)
	for i := range many {
		many[i] = []string{"X-N", "v"}
	}
	bad := map[string]map[string]any{
		"no manifest":         msg("manifestUrl", nil),
		"empty manifest":      msg("manifestUrl", ""),
		"file manifest":       msg("manifestUrl", "file:///etc/passwd"),
		"blob manifest":       msg("manifestUrl", "blob:https://site.test/0b8f6a"),
		"data manifest":       msg("manifestUrl", "data:application/vnd.apple.mpegurl;base64,I0VYVE0zVQ=="),
		"relative manifest":   msg("manifestUrl", "/v/master.m3u8"),
		"manifest control":    msg("manifestUrl", "https://cdn.test/a\nb.m3u8"),
		"manifest too long":   msg("manifestUrl", manifest+"&"+strings.Repeat("a", validate.MaxURLBytes)),
		"manifest number":     msg("manifestUrl", 5),
		"headers string":      msg("headers", "Cookie: a"),
		"header pair":         msg("headers", [][]string{{"Cookie"}}),
		"header triple":       msg("headers", [][]string{{"Cookie", "a", "b"}}),
		"header CRLF":         msg("headers", [][]string{{"Referer", "https://a.example/\r\nX-Injected: 1"}}),
		"header NUL":          msg("headers", [][]string{{"Cookie", "a\x00b"}}),
		"Host header":         msg("headers", [][]string{{"Host", "evil.example"}}),
		"Range header":        msg("headers", [][]string{{"Range", "bytes=0-"}}),
		"too many headers":    msg("headers", many),
		"headers too large":   msg("headers", [][]string{{"Cookie", strings.Repeat("c", validate.MaxHeaderBytes)}}),
		"page file":           msg("pageUrl", "file:///etc/passwd"),
		"page about":          msg("pageUrl", "about:blank"),
		"page number":         msg("pageUrl", 5),
		"page too long":       msg("pageUrl", "https://site.test/?"+strings.Repeat("a", validate.MaxURLBytes)),
		"deadline zero":       msg("deadlineMs", 0),
		"deadline negative":   msg("deadlineMs", -1),
		"deadline fractional": msg("deadlineMs", 1500.5),
		"deadline string":     msg("deadlineMs", "5000"),
	}
	for name, m := range bad {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(m)
			req, rerr := parseRequest(b)
			if rerr == nil {
				t.Fatalf("accepted: %+v", req)
			}
			if rerr.id != "x" || !strings.HasPrefix(rerr.msg, "probeStream: ") {
				t.Fatalf("error %+v", rerr)
			}
			for _, leak := range []string{"X-Injected", "a\x00b"} {
				if strings.Contains(rerr.msg, leak) {
					t.Fatalf("message leaks a header value: %s", rerr.msg)
				}
			}
		})
	}
	good := map[string]map[string]any{
		"minimal":          msg(),
		"dash":             msg("manifestUrl", "http://cdn.test/live-or-not/manifest.mpd"),
		"headers":          msg("headers", [][]string{{"Cookie", "a=b"}, {"Referer", "https://site.test/"}, {"User-Agent", "UA/1"}}),
		"empty headers":    msg("headers", [][]string{}),
		"page":             msg("pageUrl", "https://site.test/watch?v=1"),
		"empty page":       msg("pageUrl", ""),
		"huge deadline":    msg("deadlineMs", int64(1)<<62),
		"unknown field":    msg("future", map[string]any{"x": 1}),
		"prefetch ignored": msg("prefetch", true),
		"title ignored":    msg("title", 5),
		"tracks ignored":   msg("videoTrack", 137, "audioTrack", "a\nb"),
	}
	for name, m := range good {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(m)
			req, rerr := parseRequest(b)
			if rerr != nil {
				t.Fatalf("rejected: %v", rerr)
			}
			if !req.video() || req.media != nil || req.probe.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST ||
				req.probe.GetManifestUrl() != m["manifestUrl"] || req.probe.GetPrefetch() || req.info.method != "GET" ||
				req.info.headers != len(req.probe.GetHeaders()) {
				t.Fatalf("parsed %+v (probe %v)", req, req.probe)
			}
		})
	}
}

// TestStreamDeadlines checks the bounds of the two stream requests: probeStream runs
// on the video pool (videoTimeout, 30 s), addMedia stays on the add path (addTimeout,
// 15 s); deadlineMs only shortens either, less the response margin.
func TestStreamDeadlines(t *testing.T) {
	received := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in      string
		timeout time.Duration // what dispatch bounds the type with
		parsed  time.Duration // req.info.deadline
		want    time.Duration // after received
	}{
		{`{"id":"x","type":"probeStream","manifestUrl":"https://cdn.test/m.m3u8"}`, videoTimeout, 0, videoTimeout},
		{`{"id":"x","type":"probeStream","manifestUrl":"https://cdn.test/m.m3u8","deadlineMs":28000}`, videoTimeout, 28 * time.Second, 28*time.Second - responseMargin},
		{`{"id":"x","type":"probeStream","manifestUrl":"https://cdn.test/m.m3u8","deadlineMs":90000}`, videoTimeout, videoTimeout, videoTimeout - responseMargin},
		{`{"id":"x","type":"probeStream","manifestUrl":"https://cdn.test/m.m3u8","deadlineMs":4611686018427387904}`, videoTimeout, videoTimeout, videoTimeout - responseMargin},
		{`{"id":"x","type":"probeStream","manifestUrl":"https://cdn.test/m.m3u8","deadlineMs":1000}`, videoTimeout, time.Second, 800 * time.Millisecond},
		{`{"id":"x","type":"addMedia","manifestUrl":"https://cdn.test/m.m3u8"}`, addTimeout, 0, addTimeout},
		{`{"id":"x","type":"addMedia","manifestUrl":"https://cdn.test/m.m3u8","deadlineMs":90000}`, addTimeout, addTimeout, addTimeout - responseMargin},
	} {
		req, rerr := parseRequest([]byte(c.in))
		if rerr != nil {
			t.Fatalf("%s: %v", c.in, rerr)
		}
		if req.info.deadline != c.parsed {
			t.Errorf("%s: deadlineMs read as %v, want %v", c.in, req.info.deadline, c.parsed)
		}
		if video := req.typ == typeProbeStream; req.video() != video {
			t.Errorf("%s: video() = %v", c.in, req.video())
		}
		req.received = received
		if got := requestDeadline(req, c.timeout).Sub(received); got != c.want {
			t.Errorf("%s: deadline after %v, want %v", c.in, got, c.want)
		}
	}

	// The backend sees those bounds (served without startSession, whose context would
	// end first).
	be := streamBackend()
	r := &relay{be: be, log: testLogger(t)}
	in := bytes.NewBuffer(nil)
	budgets := map[string]time.Duration{}
	for _, c := range []struct {
		msg    map[string]any
		budget time.Duration
	}{
		{map[string]any{"manifestUrl": "https://cdn.test/plain.m3u8"}, videoTimeout},
		{map[string]any{"manifestUrl": "https://cdn.test/long.m3u8", "deadlineMs": 90000}, videoTimeout - responseMargin},
		{map[string]any{"manifestUrl": "https://cdn.test/short.m3u8", "deadlineMs": 2000}, 2*time.Second - responseMargin},
	} {
		c.msg["id"], c.msg["type"] = c.msg["manifestUrl"], typeProbeStream
		in.Write(jsonFrame(t, c.msg))
		budgets[c.msg["manifestUrl"].(string)] = c.budget
	}
	in.Write(jsonFrame(t, map[string]any{"id": "add", "type": typeAddMedia, "manifestUrl": "https://cdn.test/add.m3u8"}))
	start := time.Now()
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	check := func(what string, dl time.Time, budget time.Duration) {
		t.Helper()
		if d := dl.Sub(start); dl.IsZero() || d < budget || d > budget+time.Second {
			t.Errorf("%s: backend deadline %v after the request, want about %v", what, d, budget)
		}
	}
	be.mu.Lock()
	if len(be.probes) != len(budgets) {
		t.Errorf("%d probes reached the backend, want %d", len(be.probes), len(budgets))
	}
	for i, p := range be.probes {
		check(p.GetManifestUrl(), be.deadlines[i], budgets[p.GetManifestUrl()])
	}
	be.mu.Unlock()
	be.fakeBackend.mu.Lock()
	if len(be.fakeBackend.deadlines) != 1 {
		t.Fatalf("%d adds reached the backend, want 1", len(be.fakeBackend.deadlines))
	}
	check("addMedia", be.fakeBackend.deadlines[0], addTimeout)
	be.fakeBackend.mu.Unlock()
}

// TestStreamProbesShareTheVideoPool fills the video slots with stream and page probes:
// together they never run more than maxVideoInFlight at once, more than maxVideoWaiting
// are refused at once, and capture adds and addMedia are still answered meanwhile.
func TestStreamProbesShareTheVideoPool(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan string, 64)
	be := &videoBackend{fakeBackend: &fakeBackend{}, gate: gate, started: started}
	s := startSession(t, be)
	// Half stream probes, half video requests hold every slot …
	for i := range maxVideoInFlight {
		if i%2 == 0 {
			s.send(map[string]any{"id": fmt.Sprint("hold-s", i), "type": "probeStream", "manifestUrl": fmt.Sprintf("https://cdn.test/%d/master.m3u8", i)})
		} else {
			s.send(map[string]any{"id": fmt.Sprint("hold-v", i), "type": "probeVideo", "pageUrl": fmt.Sprintf("https://www.youtube.com/watch?v=%011d", i)})
		}
	}
	for range maxVideoInFlight {
		<-started
	}
	// … and stream probes wait for one, up to the pool's bound.
	for i := range maxVideoWaiting - maxVideoInFlight {
		s.send(map[string]any{"id": fmt.Sprint("wait", i), "type": "probeStream", "manifestUrl": fmt.Sprintf("https://cdn.test/w%d/master.m3u8", i)})
	}
	start := time.Now()
	s.send(map[string]any{"id": "extra", "type": "probeStream", "manifestUrl": "https://cdn.test/extra/master.m3u8"})
	if m := s.recv(); m["id"] != "extra" || errorCode(m) != codeDaemonUnavailable || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("stream probe over the bound: %v after %v", m, time.Since(start))
	}
	// Adds have their own slots: capture adds and stream adds are answered at once.
	for i := range maxInFlight + 4 {
		s.send(map[string]any{"id": fmt.Sprint("a", i), "type": "add", "url": fmt.Sprintf("https://example.test/%d.zip", i)})
		if m := s.recv(); m["id"] != fmt.Sprint("a", i) || m["type"] != "added" {
			t.Fatalf("capture add while stream probes wait: %v", m)
		}
	}
	s.send(map[string]any{"id": "media", "type": "addMedia", "manifestUrl": "https://cdn.test/0/master.m3u8", "videoTrack": "v720"})
	if m := s.recv(); m["id"] != "media" || m["type"] != "added" {
		t.Fatalf("addMedia while stream probes wait: %v", m)
	}
	select {
	case p := <-started:
		t.Fatalf("a video call started beyond the %d slots: %s", maxVideoInFlight, p)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	seen := map[string]bool{}
	for range maxVideoWaiting {
		m := s.recv()
		if m["type"] != "videoInfo" {
			t.Fatalf("probe: %v", m)
		}
		seen[m["id"].(string)] = true
	}
	be.mu.Lock()
	most, probes := be.most, len(be.probes)
	be.mu.Unlock()
	if len(seen) != maxVideoWaiting || probes != maxVideoWaiting || most > maxVideoInFlight {
		t.Fatalf("answered %d probes, the daemon saw %d, at most %d at once", len(seen), probes, most)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// TestStreamProbeWaitsWithinItsDeadline: a stream probe waiting for a video slot is
// answered by its deadline, without reaching the daemon.
func TestStreamProbeWaitsWithinItsDeadline(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan string, maxVideoInFlight)
	be := &videoBackend{fakeBackend: &fakeBackend{}, gate: gate, started: started}
	s := startSession(t, be)
	for i := range maxVideoInFlight {
		s.send(map[string]any{"id": fmt.Sprint("hold", i), "type": "probeVideo", "pageUrl": fmt.Sprintf("https://www.youtube.com/watch?v=%011d", i)})
	}
	for range maxVideoInFlight {
		<-started
	}
	start := time.Now()
	s.send(map[string]any{"id": "late", "type": "probeStream", "manifestUrl": "https://cdn.test/m.m3u8", "deadlineMs": 600})
	m := s.recv()
	if m["id"] != "late" || errorCode(m) != codeDaemonUnavailable {
		t.Fatalf("stream probe without a slot: %v", m)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("answered after %v, want about the 600 ms deadline", d)
	}
	close(gate)
	for range maxVideoInFlight {
		if m := s.recv(); m["type"] != "videoInfo" {
			t.Fatalf("probe: %v", m)
		}
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	for _, p := range be.probes {
		if p.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST {
			t.Fatalf("the stream probe reached the daemon after its deadline: %v", p)
		}
	}
}

// TestProbeStreamRefusals checks that the daemon's refusals of a stream reach the
// extension with their reason and message.
func TestProbeStreamRefusals(t *testing.T) {
	coded := func(code connect.Code, reason, msg string) error {
		e := connect.NewError(code, errors.New(msg))
		e.Meta().Set(errorCodeMeta, reason)
		return e
	}
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	for _, c := range []struct {
		err          error
		code, reason string
	}{
		{coded(connect.CodeInvalidArgument, "encrypted", "encrypted: media: content is encrypted or DRM-protected"), codeRejected, "encrypted"},
		{coded(connect.CodeInvalidArgument, "live", "live: media: live streams cannot be downloaded"), codeRejected, "live"},
		{coded(connect.CodeInvalidArgument, "media_unsupported", "media_unsupported: No MP4 video in this stream"), codeRejected, "media_unsupported"},
		{coded(connect.CodeInvalidArgument, "media_invalid", "media_invalid: remux: malformed input"), codeRejected, "media_invalid"},
		{coded(connect.CodeFailedPrecondition, "http_status", "http_status: the server answered 403"), codeRejected, "http_status"},
		{coded(connect.CodeFailedPrecondition, "network", "network: connection refused"), codeRejected, "network"},
		{coded(connect.CodeFailedPrecondition, "tls", "tls: certificate signed by unknown authority"), codeRejected, "tls"},
		{coded(connect.CodeDeadlineExceeded, "", "fetching the manifest took too long"), codeDaemonUnavailable, ""},
		{connect.NewError(connect.CodeUnimplemented, errors.New("media_unsupported: this daemon has no media jobs")), codeInternal, ""},
	} {
		be.mu.Lock()
		be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return nil, c.err }
		be.mu.Unlock()
		id := fmt.Sprint("s-", c.reason, "-", connect.CodeOf(c.err))
		s.send(map[string]any{"id": id, "type": "probeStream", "manifestUrl": "https://cdn.test/m.m3u8"})
		m := s.recv()
		e, _ := m["error"].(map[string]any)
		reason, has := e["reason"]
		if m["id"] != id || errorCode(m) != c.code || (c.reason == "") == has || (has && reason != c.reason) ||
			!strings.Contains(errorMessage(m), c.err.(*connect.Error).Message()) {
			t.Errorf("%v: %v", c.err, m)
		}
	}
}

// TestProbeStreamAnswerLimits: a stream's videoInfo is bounded like a page's.
func TestProbeStreamAnswerLimits(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	ask := func(id string, info *downexv1.MediaInfo) map[string]any {
		t.Helper()
		info.Site, info.Format, info.Origin = "", "dash", downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST
		be.mu.Lock()
		be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return info, nil }
		be.mu.Unlock()
		s.send(map[string]any{"id": id, "type": "probeStream", "manifestUrl": "https://cdn.test/m.mpd"})
		return s.recv()
	}
	if m := ask("max", siteInfo(maxVideoTracks-1)); m["type"] != "videoInfo" || len(m["tracks"].([]any)) != maxVideoTracks {
		t.Fatalf("%d tracks: %v", maxVideoTracks, errorMessage(m))
	}
	if m := ask("over", siteInfo(maxVideoTracks)); errorCode(m) != codeInternal {
		t.Fatalf("%d tracks: %v", maxVideoTracks+1, m)
	}
	big := siteInfo(150)
	for _, tr := range big.GetTracks() {
		tr.Name = strings.Repeat("n", 2000)
	}
	if m := ask("big", big); errorCode(m) != codeInternal || !strings.Contains(errorMessage(m), "262144 bytes") {
		t.Fatalf("oversized videoInfo: %v", m)
	}
}

// TestStreamLogNeverRecordsSecrets: the log keeps the manifest as scheme, host and path,
// the header count and the track ids, never header values or the query.
func TestStreamLogNeverRecordsSecrets(t *testing.T) {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	r := &relay{be: streamBackend(), log: newTestJSONLogger(w)}
	in := bytes.NewBuffer(nil)
	headers := [][]string{{"Cookie", "sid=cookie-secret"}, {"Authorization", "Bearer auth-secret"}}
	in.Write(jsonFrame(t, map[string]any{"id": "p", "type": "probeStream", "manifestUrl": testManifest, "headers": headers,
		"pageUrl": "https://site.test/watch?v=page-secret", "deadlineMs": 15000}))
	in.Write(jsonFrame(t, map[string]any{"id": "a", "type": "addMedia", "manifestUrl": testManifest, "headers": headers,
		"pageUrl": "https://site.test/watch?v=page-secret", "videoTrack": "v720", "audioTrack": "a-en", "title": "Episode"}))
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	log := w.buf.String()
	w.mu.Unlock()
	for _, secret := range []string{"cookie-secret", "auth-secret", "manifest-secret", "page-secret"} {
		if strings.Contains(log, secret) {
			t.Errorf("%q leaked into the log: %s", secret, log)
		}
	}
	for _, want := range []string{
		`"type":"probeStream","ms":`, `"manifest":"https://cdn.test/v/master.m3u8?[redacted]","headers":2,"deadline_ms":15000`,
		`"type":"addMedia","ms":`, `"video_track":"v720","audio_track":"a-en"`, `"title_chars":7`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %s: %s", want, log)
		}
	}
}

// TestProbeStreamRoundTrip probes a stream through a real daemon: a stream probe is a
// user's click, so it starts downexd when none runs; it adds nothing; refusals carry
// the daemon's code as reason.
func TestProbeStreamRoundTrip(t *testing.T) {
	pj := &probeOnly{probe: func(sel daemon.MediaSelection) (*daemon.MediaProbe, error) {
		switch {
		case strings.Contains(sel.ManifestURL, "drm"):
			return nil, fmt.Errorf("parse playlist: %w", media.ErrEncrypted)
		case strings.Contains(sel.ManifestURL, "live"):
			return nil, fmt.Errorf("playlist: %w", media.ErrLive)
		}
		// Track URLs (media playlists) stay inside the daemon.
		return &daemon.MediaProbe{Format: "hls", Tracks: []media.Track{
			{ID: "1080p", Kind: media.KindVideo, Width: 1920, Height: 1080, Bandwidth: 6_000_000, Codecs: "avc1.640028",
				URL: "https://cdn.test/show/1080.m3u8?token=track-secret"},
			{ID: "720p", Kind: media.KindVideo, Width: 1280, Height: 720, Bandwidth: 3_000_000, Codecs: "avc1.4d401f",
				URL: "https://cdn.test/show/720.m3u8?token=track-secret"},
			{ID: "aac", Kind: media.KindAudio, Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en",
				URL: "https://cdn.test/show/aac.m3u8?token=track-secret"},
		}, DefaultVideo: "1080p", DefaultAudio: "aac", Duration: time.Minute, Container: "mp4", FragmentsTotal: 31}, nil
	}}
	d := newMediaDaemonServer(t, pj)
	ld := &lateDaemon{t: t, delay: 30 * time.Millisecond}
	s := startSession(t, &daemonBackend{env: d.env(ld.isUp, ld.start)})
	s.send(map[string]any{"id": "s", "type": "probeStream", "manifestUrl": "https://cdn.test/show/master.m3u8?token=manifest-secret",
		"headers": [][]string{{"Cookie", "s=1"}}, "pageUrl": "https://site.test/p", "deadlineMs": 28000})
	m := s.recv()
	tracks, _ := m["tracks"].([]any)
	if m["type"] != "videoInfo" || m["format"] != "hls" || m["container"] != "mp4" || m["site"] != "" ||
		m["selectedVideo"] != "1080p" || m["selectedAudio"] != "aac" || m["durationMs"] != 60_000.0 || len(tracks) != 3 {
		t.Fatalf("probeStream: %v", m)
	}
	if b, _ := json.Marshal(m); bytes.Contains(b, []byte("://")) || bytes.Contains(b, []byte("secret")) {
		t.Fatalf("videoInfo carries URLs: %s", b)
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("the stream probe started downexd %d times, want 1", n)
	}
	pj.mu.Lock()
	if len(pj.probes) != 1 || pj.probes[0].ManifestURL != "https://cdn.test/show/master.m3u8?token=manifest-secret" ||
		pj.probes[0].Headers.Get("Cookie") != "s=1" || pj.probes[0].VideoTrack != "" || pj.probes[0].AudioTrack != "" {
		t.Errorf("probes = %+v", pj.probes)
	}
	pj.mu.Unlock()

	for _, c := range []struct{ url, code string }{
		{"https://cdn.test/drm/manifest.mpd", "encrypted"},
		{"https://cdn.test/live/index.m3u8", "live"},
	} {
		s.send(map[string]any{"id": c.code, "type": "probeStream", "manifestUrl": c.url})
		m := s.recv()
		e, _ := m["error"].(map[string]any)
		if errorCode(m) != codeRejected || e["reason"] != c.code || !strings.HasPrefix(errorMessage(m), c.code+": ") {
			t.Errorf("%s: %v", c.code, m)
		}
	}
	list, err := d.dl.List(context.Background(), connect.NewRequest(&downexv1.ListRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(list.Msg.GetDownloads()); n != 0 {
		t.Fatalf("stream probes added %d downloads", n)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("downexd was started %d times", n)
	}
}
