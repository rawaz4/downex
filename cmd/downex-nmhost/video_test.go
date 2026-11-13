package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// probeMedia makes the M3 fake a backend; plain fakes have no video side.
func (f *fakeBackend) probeMedia(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("no video side in this fake"))
}

// videoBackend is a fakeBackend with a video side: probes (origin SITE or MANIFEST) answer
// from probe, video adds (AddMedia with origin SITE) from addState; both wait for gate when
// it is set.
type videoBackend struct {
	*fakeBackend

	mu        sync.Mutex
	probes    []*downexv1.ProbeMediaRequest
	videoAdds []*downexv1.AddMediaRequest
	deadlines []time.Time // of each probe and video add, in call order
	running   int         // video calls in progress
	most      int         // the most video calls seen in progress at once
	probe     func(req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error)
	addState  downexv1.DownloadState
	addErr    error
	gate      chan struct{}
	started   chan string // page URL (a stream probe: the manifest) of each video call as it starts, when set
}

func (v *videoBackend) enter(ctx context.Context, page string) (func(), error) {
	v.mu.Lock()
	v.running++
	v.most = max(v.most, v.running)
	gate, started := v.gate, v.started
	v.mu.Unlock()
	leave := func() {
		v.mu.Lock()
		v.running--
		v.mu.Unlock()
	}
	if started != nil {
		started <- page
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			leave()
			return nil, ctx.Err()
		}
	}
	return leave, nil
}

func (v *videoBackend) probeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	v.mu.Lock()
	v.probes = append(v.probes, req)
	v.deadlines = append(v.deadlines, deadlineOf(ctx))
	v.mu.Unlock()
	target := req.GetPageUrl()
	if req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
		target = req.GetManifestUrl()
	}
	leave, err := v.enter(ctx, target)
	if err != nil {
		return nil, err
	}
	defer leave()
	if v.probe == nil {
		return siteInfo(3), nil
	}
	return v.probe(req)
}

func (v *videoBackend) addMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.Download, error) {
	if req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
		return v.fakeBackend.addMedia(ctx, req)
	}
	v.mu.Lock()
	v.videoAdds = append(v.videoAdds, req)
	v.deadlines = append(v.deadlines, deadlineOf(ctx))
	v.mu.Unlock()
	leave, err := v.enter(ctx, req.GetPageUrl())
	if err != nil {
		return nil, err
	}
	defer leave()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.addErr != nil {
		return nil, v.addErr
	}
	return &downexv1.Download{Id: 42, Url: req.GetPageUrl(), Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, State: v.addState}, nil
}

// deadlineOf is ctx's deadline, zero when it has none.
func deadlineOf(ctx context.Context) time.Time {
	dl, _ := ctx.Deadline()
	return dl
}

// siteInfo is a probed page with n MP4 qualities under their standard names and one
// audio track.
func siteInfo(n int) *downexv1.MediaInfo {
	m := &downexv1.MediaInfo{
		Format: "http", Container: "mp4", Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Site: "YouTube",
		PageUrl: "https://www.youtube.com/watch?v=aaaaaaaaaaa", Title: "A video", DurationMs: 212_000,
		SelectedAudio: "140",
	}
	for i := range n {
		h := int32(2160 >> i)
		m.Tracks = append(m.Tracks, &downexv1.MediaTrack{
			Id: fmt.Sprint(400 + i), Kind: "video", Ext: "mp4", Codecs: "avc1.640028", Width: h * 16 / 9, Height: h,
			FrameRate: 60, Bandwidth: 9_000_000 >> i, Size: 412_000_000 >> i, Name: media.QualityName(int(h*16/9), int(h), 60, false),
		})
	}
	m.SelectedVideo = "400"
	m.Tracks = append(m.Tracks, &downexv1.MediaTrack{
		Id: "140", Kind: "audio", Ext: "m4a", Codecs: "mp4a.40.2", Bandwidth: 129_000, Size: 3_400_000,
		SizeApprox: true, Language: "en", Name: "English original",
	})
	return m
}

func TestWelcomeNamesVideo(t *testing.T) {
	s := startSession(t, &videoBackend{fakeBackend: &fakeBackend{st: &downexv1.StatusResponse{Version: "1.0.0"}}})
	s.send(map[string]any{"id": "h", "type": "hello", "protocol": 1, "browser": "chrome"})
	m := s.recv()
	got, _ := m["features"].([]any)
	// "prefetch": probeVideo forwards the look-ahead mark, so the extension may send one.
	// "streams": probeStream and the tracks of addMedia (docs/VIDEO-DETECT-SPEC.md §3.5).
	if m["type"] != "welcome" || fmt.Sprint(got) != "[video prefetch streams]" {
		t.Fatalf("welcome: %v", m)
	}
}

// TestProbeVideoPrefetch checks that the look-ahead mark of probeVideo reaches the daemon
// as ProbeMediaRequest.prefetch, that a probe without it (or with false) is a user's
// probe, and that the log names it.
func TestProbeVideoPrefetch(t *testing.T) {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	r := &relay{be: be, log: newTestJSONLogger(w)}
	in := bytes.NewBuffer(nil)
	const page = "https://www.youtube.com/watch?v=aaaaaaaaaaa"
	in.Write(jsonFrame(t, map[string]any{"id": "ahead", "type": "probeVideo", "pageUrl": page, "prefetch": true, "deadlineMs": 28000}))
	in.Write(jsonFrame(t, map[string]any{"id": "user", "type": "probeVideo", "pageUrl": page}))
	in.Write(jsonFrame(t, map[string]any{"id": "off", "type": "probeVideo", "pageUrl": page, "prefetch": false}))
	in.Write(jsonFrame(t, map[string]any{"id": "null", "type": "probeVideo", "pageUrl": page, "prefetch": nil}))
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	answered := 0
	for rd := bytes.NewReader(out.Bytes()); rd.Len() > 0; answered++ {
		b, err := readFrame(rd)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil || m["type"] != "videoInfo" {
			t.Fatalf("probe answer %s (%v)", b, err)
		}
	}
	be.mu.Lock()
	probes := be.probes
	be.mu.Unlock()
	if answered != 4 || len(probes) != 4 {
		t.Fatalf("answered %d, daemon saw %d probes", answered, len(probes))
	}
	n := 0
	for _, p := range probes {
		if p.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || p.GetPageUrl() != page {
			t.Fatalf("ProbeMediaRequest = %v", p)
		}
		if p.GetPrefetch() {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d probes marked prefetch, want only the look-ahead", n)
	}
	w.mu.Lock()
	log := w.buf.String()
	w.mu.Unlock()
	if !strings.Contains(log, `"id":"ahead"`) || !strings.Contains(log, `"prefetch":true`) || !strings.Contains(log, `"prefetch":false`) {
		t.Fatalf("log lacks the prefetch mark: %s", log)
	}
}

func TestProbeVideo(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "p", "type": "probeVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa",
		"headers": [][]string{{"Cookie", "SID=secret"}, {"Referer", "https://blog.example/post"}}, "deadlineMs": 5000,
		"videoTrack": "ignored", "unknownField": 1})
	m := s.recv()
	if m["id"] != "p" || m["ok"] != true || m["type"] != "videoInfo" || m["site"] != "YouTube" || m["title"] != "A video" ||
		m["durationMs"] != 212_000.0 || m["format"] != "http" || m["container"] != "mp4" ||
		m["selectedVideo"] != "400" || m["selectedAudio"] != "140" {
		t.Fatalf("videoInfo: %v", m)
	}
	tracks, _ := m["tracks"].([]any)
	if len(tracks) != 4 {
		t.Fatalf("tracks: %v", m["tracks"])
	}
	first, _ := tracks[0].(map[string]any)
	want := map[string]any{"id": "400", "kind": "video", "ext": "mp4", "codecs": "avc1.640028", "width": 3840.0,
		"height": 2160.0, "frameRate": 60.0, "bandwidth": 9e6, "size": 412e6, "sizeApprox": false, "language": "",
		"name": "4K (2160p) 60fps"}
	if fmt.Sprint(first) != fmt.Sprint(want) {
		t.Fatalf("first track %v, want %v", first, want)
	}
	if audio, _ := tracks[3].(map[string]any); audio["sizeApprox"] != true || audio["ext"] != "m4a" || audio["language"] != "en" {
		t.Fatalf("audio track %v", audio)
	}
	if b, _ := json.Marshal(m); bytes.Contains(b, []byte("googlevideo")) || bytes.Contains(b, []byte("manifest")) {
		t.Fatalf("videoInfo carries URLs: %s", b)
	}
	be.mu.Lock()
	req, dl := be.probes[0], be.deadlines[0]
	be.mu.Unlock()
	if req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || req.GetPageUrl() != "https://www.youtube.com/watch?v=aaaaaaaaaaa" ||
		req.GetManifestUrl() != "" || len(req.GetHeaders()) != 2 || req.GetHeaders()[1].GetName() != "Referer" {
		t.Fatalf("ProbeMediaRequest = %v", req)
	}
	if d := dl.Sub(start); d > 5*time.Second-responseMargin+time.Second || d < 4*time.Second {
		t.Fatalf("backend deadline %v after the request, want about %v", d, 5*time.Second-responseMargin)
	}
}

// TestVideoDeadline checks the deadline of video requests: deadlineMs less the
// response margin, at most videoTimeout, counted from when the relay read the request.
func TestVideoDeadline(t *testing.T) {
	received := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		`{"id":"x","type":"probeVideo","pageUrl":"https://www.youtube.com/watch?v=aaaaaaaaaaa","deadlineMs":28000}`: 28*time.Second - responseMargin,
		`{"id":"x","type":"probeVideo","pageUrl":"https://www.youtube.com/watch?v=aaaaaaaaaaa"}`:                    videoTimeout,
		`{"id":"x","type":"addVideo","pageUrl":"https://www.youtube.com/watch?v=aaaaaaaaaaa","deadlineMs":90000}`:   videoTimeout - responseMargin,
		`{"id":"x","type":"addVideo","pageUrl":"https://www.youtube.com/watch?v=aaaaaaaaaaa","deadlineMs":1000}`:    800 * time.Millisecond,
	} {
		req, rerr := parseRequest([]byte(in))
		if rerr != nil {
			t.Fatalf("%s: %v", in, rerr)
		}
		req.received = received
		if got := requestDeadline(req, videoTimeout).Sub(received); got != want {
			t.Errorf("%s: deadline after %v, want %v", in, got, want)
		}
	}
}

func TestProbeVideoAnswerLimits(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	ask := func(id string, info *downexv1.MediaInfo) map[string]any {
		t.Helper()
		be.mu.Lock()
		be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return info, nil }
		be.mu.Unlock()
		s.send(map[string]any{"id": id, "type": "probeVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa"})
		return s.recv()
	}
	if m := ask("max", siteInfo(maxVideoTracks-1)); m["type"] != "videoInfo" || len(m["tracks"].([]any)) != maxVideoTracks {
		t.Fatalf("%d tracks: %v", maxVideoTracks, errorMessage(m))
	}
	if m := ask("over", siteInfo(maxVideoTracks)); errorCode(m) != codeInternal {
		t.Fatalf("%d tracks: %v", maxVideoTracks+1, m)
	}
	// Long names under the track limit still exceed the answer's 256 KiB.
	big := siteInfo(150)
	for _, tr := range big.GetTracks() {
		tr.Name = strings.Repeat("n", 2000)
	}
	if m := ask("big", big); errorCode(m) != codeInternal || !strings.Contains(errorMessage(m), "262144 bytes") {
		t.Fatalf("oversized videoInfo: %v", m)
	}
}

func TestAddVideo(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}, addState: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "a", "type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa",
		"videoTrack": "401", "audioTrack": "140", "title": "A\nvideo", "headers": [][]string{{"Cookie", "SID=1"}}, "deadlineMs": 5000})
	m := s.recv()
	if m["id"] != "a" || m["type"] != "added" || m["downloadId"] != 42.0 || m["awaiting"] != true {
		t.Fatalf("addVideo: %v", m)
	}
	be.mu.Lock()
	req, dl := be.videoAdds[0], be.deadlines[0]
	be.addState = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	be.mu.Unlock()
	if req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || req.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER ||
		req.GetPageUrl() != "https://www.youtube.com/watch?v=aaaaaaaaaaa" || req.GetManifestUrl() != "" ||
		req.GetVideoTrack() != "401" || req.GetAudioTrack() != "140" || req.GetTitle() != "A video" || len(req.GetHeaders()) != 1 {
		t.Fatalf("AddMediaRequest = %v", req)
	}
	if d := dl.Sub(start); d > 5*time.Second-responseMargin+time.Second || d < 4*time.Second {
		t.Fatalf("backend deadline %v after the request", d)
	}
	// Queued at once (no confirmation), and the daemon's default pick.
	s.send(map[string]any{"id": "b", "type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa"})
	if m := s.recv(); m["type"] != "added" || m["awaiting"] != false {
		t.Fatalf("queued addVideo: %v", m)
	}
	be.mu.Lock()
	req = be.videoAdds[1]
	be.mu.Unlock()
	if req.GetVideoTrack() != "" || req.GetAudioTrack() != "" || req.GetTitle() != "" {
		t.Fatalf("default pick forwarded as %v", req)
	}
}

func TestVideoValidation(t *testing.T) {
	const page = "https://www.youtube.com/watch?v=aaaaaaaaaaa"
	msg := func(typ string, kv ...any) map[string]any {
		m := map[string]any{"id": "x", "type": typ, "pageUrl": page}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(m, kv[i].(string))
			} else {
				m[kv[i].(string)] = kv[i+1]
			}
		}
		return m
	}
	bad := map[string]map[string]any{
		"probe no page":         msg(typeProbeVideo, "pageUrl", nil),
		"probe empty page":      msg(typeProbeVideo, "pageUrl", ""),
		"probe file page":       msg(typeProbeVideo, "pageUrl", "file:///etc/passwd"),
		"probe page too long":   msg(typeProbeVideo, "pageUrl", page+"&"+strings.Repeat("a", validate.MaxURLBytes)),
		"probe page type":       msg(typeProbeVideo, "pageUrl", 5),
		"probe header pair":     msg(typeProbeVideo, "headers", [][]string{{"Cookie"}}),
		"probe header CRLF":     msg(typeProbeVideo, "headers", [][]string{{"Referer", "https://a.example/\r\nX-Injected: 1"}}),
		"probe Host header":     msg(typeProbeVideo, "headers", [][]string{{"Host", "evil.example"}}),
		"probe deadline zero":   msg(typeProbeVideo, "deadlineMs", 0),
		"probe deadline string": msg(typeProbeVideo, "deadlineMs", "28000"),
		"probe prefetch string": msg(typeProbeVideo, "prefetch", "true"),
		"probe prefetch number": msg(typeProbeVideo, "prefetch", 1),
		"probe prefetch object": msg(typeProbeVideo, "prefetch", map[string]any{"on": true}),
		"add no page":           msg(typeAddVideo, "pageUrl", nil),
		"add video track ctrl":  msg(typeAddVideo, "videoTrack", "1\n2"),
		"add audio track long":  msg(typeAddVideo, "audioTrack", strings.Repeat("a", validate.MaxTrackIDBytes+1)),
		"add track type":        msg(typeAddVideo, "videoTrack", 137),
		"add long title":        msg(typeAddVideo, "title", strings.Repeat("t", validate.MaxTitleRunes+1)),
		"add deadline negative": msg(typeAddVideo, "deadlineMs", -1),
	}
	for name, m := range bad {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(m)
			req, rerr := parseRequest(b)
			if rerr == nil {
				t.Fatalf("accepted: %+v", req)
			}
			if rerr.id != "x" || strings.Contains(rerr.msg, "X-Injected") {
				t.Fatalf("error %+v", rerr)
			}
		})
	}
	good := map[string]map[string]any{
		"probe minimal":     msg(typeProbeVideo),
		"probe any page":    msg(typeProbeVideo, "pageUrl", "https://video.example/watch/12"),
		"probe huge limit":  msg(typeProbeVideo, "deadlineMs", int64(1)<<62),
		"probe prefetch":    msg(typeProbeVideo, "prefetch", true),
		"probe no prefetch": msg(typeProbeVideo, "prefetch", false),
		"add prefetch":      msg(typeAddVideo, "prefetch", "ignored: only probeVideo reads it"),
		"add minimal":       msg(typeAddVideo),
		"add tracks":        msg(typeAddVideo, "videoTrack", "137", "audioTrack", "140-drc"),
		"add empty tracks":  msg(typeAddVideo, "videoTrack", "", "audioTrack", ""),
		"add unknown field": msg(typeAddVideo, "future", true),
	}
	for name, m := range good {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(m)
			if _, rerr := parseRequest(b); rerr != nil {
				t.Fatalf("rejected: %v", rerr)
			}
		})
	}
	r, _ := parseRequest([]byte(`{"id":"x","type":"probeVideo","pageUrl":"` + page + `","deadlineMs":4611686018427387904}`))
	if r.info.deadline != videoTimeout {
		t.Fatalf("deadline %v, want the %v cap", r.info.deadline, videoTimeout)
	}
}

// TestVideoRequestsKeepCaptureAddsMoving holds every video slot with slow probes and
// checks that capture adds are still answered at once, that no more than
// maxVideoInFlight video calls run together, and that the waiting ones are served
// when slots free up.
func TestVideoRequestsKeepCaptureAddsMoving(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan string, 64)
	be := &videoBackend{fakeBackend: &fakeBackend{}, gate: gate, started: started}
	s := startSession(t, be)
	const probes = maxVideoInFlight + 2
	for i := range probes {
		s.send(map[string]any{"id": fmt.Sprint("p", i), "type": "probeVideo", "pageUrl": fmt.Sprintf("https://www.youtube.com/watch?v=%011d", i)})
	}
	for range maxVideoInFlight {
		<-started
	}
	// Capture adds are served by their own semaphore meanwhile, many more than video slots.
	for i := range maxInFlight + 4 {
		s.send(map[string]any{"id": fmt.Sprint("a", i), "type": "add", "url": fmt.Sprintf("https://example.test/%d.zip", i)})
		if m := s.recv(); m["id"] != fmt.Sprint("a", i) || m["type"] != "added" {
			t.Fatalf("capture add while probes wait: %v", m)
		}
	}
	select {
	case p := <-started:
		t.Fatalf("a video call started beyond the %d slots: %s", maxVideoInFlight, p)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	seen := map[string]bool{}
	for range probes {
		m := s.recv()
		if m["type"] != "videoInfo" {
			t.Fatalf("probe: %v", m)
		}
		seen[m["id"].(string)] = true
	}
	be.mu.Lock()
	most := be.most
	be.mu.Unlock()
	if len(seen) != probes || most > maxVideoInFlight {
		t.Fatalf("answered %d probes, at most %d at once", len(seen), most)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// TestVideoRequestsAreBounded checks that video requests beyond maxVideoWaiting are
// refused at once, and that one waiting for a slot past its deadline is answered in time.
func TestVideoRequestsAreBounded(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan string, maxVideoWaiting)
	be := &videoBackend{fakeBackend: &fakeBackend{}, gate: gate, started: started}
	s := startSession(t, be)
	// These hold every slot until the gate opens …
	for i := range maxVideoInFlight {
		s.send(map[string]any{"id": fmt.Sprint("hold", i), "type": "probeVideo",
			"pageUrl": fmt.Sprintf("https://www.youtube.com/watch?v=%011d", i)})
	}
	for range maxVideoInFlight {
		<-started
	}
	// … and these wait up to 1 s for one.
	for i := range maxVideoWaiting - maxVideoInFlight {
		s.send(map[string]any{"id": fmt.Sprint("wait", i), "type": "probeVideo", "deadlineMs": 1000,
			"pageUrl": fmt.Sprintf("https://www.youtube.com/watch?v=w%010d", i)})
	}
	start := time.Now()
	s.send(map[string]any{"id": "extra", "type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=xxxxxxxxxxx"})
	m := s.recv()
	if m["id"] != "extra" || errorCode(m) != codeDaemonUnavailable || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("request over the bound: %v after %v", m, time.Since(start))
	}
	for range maxVideoWaiting - maxVideoInFlight {
		m := s.recv()
		if !strings.HasPrefix(m["id"].(string), "wait") || errorCode(m) != codeDaemonUnavailable || errorMessage(m) == "" {
			t.Fatalf("probe past its deadline: %v", m)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("answered after %v, want about the 1 s deadline", d)
	}
	close(gate)
	for range maxVideoInFlight {
		if m := s.recv(); !strings.HasPrefix(m["id"].(string), "hold") || m["type"] != "videoInfo" {
			t.Fatalf("probe holding a slot: %v", m)
		}
	}
	be.mu.Lock()
	n := len(be.probes)
	be.mu.Unlock()
	if n != maxVideoInFlight {
		t.Fatalf("%d probes reached the daemon, want only the %d that held slots", n, maxVideoInFlight)
	}
}

// TestReasonIsForwarded checks that the daemon's Downex-Error-Code reaches the
// extension as reason for every request type, and that the message keeps its prefix.
func TestReasonIsForwarded(t *testing.T) {
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
		{coded(connect.CodeFailedPrecondition, "tools_installing", "tools_installing: downloading yt-dlp and Deno (34%)"), codeRejected, "tools_installing"},
		{coded(connect.CodeFailedPrecondition, "site_resolving", "site_resolving: still getting the video info"), codeRejected, "site_resolving"},
		{coded(connect.CodeInvalidArgument, "media_unsupported", "media_unsupported: No MP4 video on this page"), codeRejected, "media_unsupported"},
		{coded(connect.CodeFailedPrecondition, "auth_required", "auth_required: the site asks to sign in"), codeRejected, "auth_required"},
		{coded(connect.CodeInvalidArgument, "Bad-Code!", "weird"), codeRejected, ""},
		{coded(connect.CodeInvalidArgument, strings.Repeat("a", maxReasonBytes+1), "long"), codeRejected, ""},
		{connect.NewError(connect.CodeInvalidArgument, errors.New("no reason")), codeRejected, ""},
	} {
		be.mu.Lock()
		be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return nil, c.err }
		be.addErr = c.err
		be.mu.Unlock()
		be.fakeBackend.mu.Lock()
		be.fakeBackend.addErr, be.fakeBackend.mediaErr = c.err, c.err
		be.fakeBackend.mu.Unlock()
		for _, msg := range []map[string]any{
			{"type": "probeVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa"},
			{"type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa"},
			{"type": "add", "url": "https://example.test/f.zip"},
			{"type": "addMedia", "manifestUrl": "https://example.test/m.m3u8"},
			{"type": "probeStream", "manifestUrl": "https://example.test/m.m3u8"},
		} {
			msg["id"] = msg["type"].(string) + c.reason
			s.send(msg)
			m := s.recv()
			e, _ := m["error"].(map[string]any)
			reason, has := e["reason"]
			if errorCode(m) != c.code || (c.reason == "") == has || (has && reason != c.reason) ||
				e["message"] != c.err.(*connect.Error).Message() {
				t.Errorf("%s with %v: %v", msg["type"], c.err, m)
			}
		}
	}
}

func TestVideoLogNeverRecordsSecrets(t *testing.T) {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	r := &relay{be: &videoBackend{fakeBackend: &fakeBackend{}}, log: newTestJSONLogger(w)}
	in := bytes.NewBuffer(nil)
	in.Write(jsonFrame(t, map[string]any{"id": "p", "type": "probeVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa&si=tracking-secret",
		"headers": [][]string{{"Cookie", "SID=cookie-secret"}}}))
	in.Write(jsonFrame(t, map[string]any{"id": "a", "type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa",
		"headers": [][]string{{"Cookie", "SID=cookie-secret"}}, "title": "Title"}))
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	log := w.buf.String()
	w.mu.Unlock()
	for _, secret := range []string{"cookie-secret", "tracking-secret"} {
		if strings.Contains(log, secret) {
			t.Errorf("%q leaked into the log: %s", secret, log)
		}
	}
	for _, want := range []string{`"page":"https://www.youtube.com/watch?[redacted]"`, `"headers":1`, `"title_chars":5`} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %s: %s", want, log)
		}
	}
}
