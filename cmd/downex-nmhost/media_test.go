package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/daemon"
	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
	"downex/internal/validate"
)

func TestAddMediaRequest(t *testing.T) {
	be := &fakeBackend{}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "m", "type": "addMedia", "manifestUrl": "https://cdn.test/v/master.m3u8?sig=1",
		"headers": [][]string{{"Cookie", "s=1"}, {"Referer", "https://site.test/watch"}},
		"pageUrl": "https://site.test/watch", "title": "Episode\n1", "deadlineMs": 5000, "unknownField": true})
	m := s.recv()
	if m["id"] != "m" || m["ok"] != true || m["type"] != "added" || m["downloadId"] != 1.0 {
		t.Fatalf("addMedia: %v", m)
	}
	be.mu.Lock()
	req, dl := be.media[0], be.deadlines[0]
	be.mu.Unlock()
	if req.GetManifestUrl() != "https://cdn.test/v/master.m3u8?sig=1" || req.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER ||
		req.GetPageUrl() != "https://site.test/watch" || req.GetTitle() != "Episode 1" || len(req.GetHeaders()) != 2 ||
		req.GetHeaders()[0].GetName() != "Cookie" || req.GetHeaders()[0].GetValue() != "s=1" {
		t.Fatalf("AddMediaRequest = %v", req)
	}
	if d := dl.Sub(start); dl.IsZero() || d > 5*time.Second-responseMargin+100*time.Millisecond {
		t.Fatalf("backend deadline %v after the request, want at most %v", d, 5*time.Second-responseMargin)
	}

	for name, msg := range map[string]map[string]any{
		"no manifestUrl":   {"type": "addMedia"},
		"bad manifestUrl":  {"type": "addMedia", "manifestUrl": "file:///etc/passwd"},
		"header not pair":  {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "headers": [][]string{{"a"}}},
		"header injection": {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "headers": [][]string{{"X", "a\r\nb"}}},
		"hop header":       {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "headers": [][]string{{"Range", "bytes=0-"}}},
		"bad pageUrl":      {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "pageUrl": "about:blank"},
		"long title":       {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "title": strings.Repeat("t", 513)},
		"title type":       {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "title": 5},
		"zero deadline":    {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "deadlineMs": 0},
	} {
		msg["id"] = name
		s.send(msg)
		if m := s.recv(); errorCode(m) != codeInvalidRequest || m["id"] != name {
			t.Errorf("%s: %v", name, m)
		}
	}
	be.mu.Lock()
	n := len(be.media)
	be.mu.Unlock()
	if n != 1 {
		t.Fatalf("invalid requests reached the daemon: %d adds", n)
	}

	// A stream the daemon refuses is rejected, its message starting with the code.
	be.mu.Lock()
	be.mediaErr = connect.NewError(connect.CodeInvalidArgument, errors.New("encrypted: media: content is encrypted or DRM-protected"))
	be.mu.Unlock()
	s.send(map[string]any{"id": "e", "type": "addMedia", "manifestUrl": "https://x.test/drm.mpd"})
	if m := s.recv(); errorCode(m) != codeRejected || !strings.HasPrefix(errorMessage(m), "encrypted: ") {
		t.Fatalf("encrypted: %v", m)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// TestAddMediaTracks checks the optional videoTrack and audioTrack of addMedia (the ids
// of a probeStream answer, docs/VIDEO-DETECT-SPEC.md §3.5): valid ids are forwarded as
// video_track/audio_track, missing or "" ask for the daemon's default pick, and invalid
// ones are invalid_request without reaching the daemon.
func TestAddMediaTracks(t *testing.T) {
	be := &fakeBackend{}
	s := startSession(t, be)
	const manifest = "https://cdn.test/v/master.m3u8"
	for _, c := range []struct {
		name         string
		fields       map[string]any
		video, audio string
	}{
		{"both", map[string]any{"videoTrack": "v1080", "audioTrack": "a-en"}, "v1080", "a-en"},
		{"video only", map[string]any{"videoTrack": "hls-720p-3000000"}, "hls-720p-3000000", ""},
		{"audio only", map[string]any{"audioTrack": "audio/aac/en"}, "", "audio/aac/en"},
		{"missing", map[string]any{}, "", ""},
		{"empty", map[string]any{"videoTrack": "", "audioTrack": ""}, "", ""},
		{"null", map[string]any{"videoTrack": nil, "audioTrack": nil}, "", ""},
		{"longest", map[string]any{"videoTrack": strings.Repeat("v", validate.MaxTrackIDBytes)}, strings.Repeat("v", validate.MaxTrackIDBytes), ""},
	} {
		msg := map[string]any{"id": c.name, "type": "addMedia", "manifestUrl": manifest}
		for k, v := range c.fields {
			msg[k] = v
		}
		s.send(msg)
		if m := s.recv(); m["id"] != c.name || m["type"] != "added" {
			t.Fatalf("%s: %v", c.name, m)
		}
		be.mu.Lock()
		req := be.media[len(be.media)-1]
		be.mu.Unlock()
		if req.GetVideoTrack() != c.video || req.GetAudioTrack() != c.audio || req.GetManifestUrl() != manifest ||
			req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_UNSPECIFIED {
			t.Errorf("%s: forwarded %v", c.name, req)
		}
	}
	be.mu.Lock()
	sent := len(be.media)
	be.mu.Unlock()

	for name, fields := range map[string]map[string]any{
		"video control":   {"videoTrack": "1\n2"},
		"audio control":   {"audioTrack": "a\x00b"},
		"video DEL":       {"videoTrack": "v\x7f"},
		"video too long":  {"videoTrack": strings.Repeat("v", validate.MaxTrackIDBytes+1)},
		"audio too long":  {"audioTrack": strings.Repeat("a", validate.MaxTrackIDBytes+1)},
		"video number":    {"videoTrack": 137},
		"audio array":     {"audioTrack": []string{"a-en"}},
		"video bad UTF-8": {"videoTrack": "v\xff"},
	} {
		msg := map[string]any{"id": name, "type": "addMedia", "manifestUrl": manifest}
		for k, v := range fields {
			msg[k] = v
		}
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if name == "video bad UTF-8" {
			// encoding/json would replace the byte; send it as the browser could.
			b = []byte(`{"id":"` + name + `","type":"addMedia","manifestUrl":"` + manifest + "\",\"videoTrack\":\"v\xff\"}")
		}
		s.sendRaw(frame(b))
		m := s.recv()
		if errorCode(m) != codeInvalidRequest || m["id"] != name {
			t.Errorf("%s: %v", name, m)
		}
		if em := errorMessage(m); name != "video bad UTF-8" && !strings.Contains(em, "Track") {
			t.Errorf("%s: message %q does not name the field", name, em)
		}
	}
	be.mu.Lock()
	n := len(be.media)
	be.mu.Unlock()
	if n != sent {
		t.Fatalf("invalid tracks reached the daemon: %d adds, want %d", n, sent)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// TestAddedAwaiting checks added.awaiting on add and addMedia (as on addVideo): true
// when the daemon holds the download for the user's confirmation, false otherwise.
func TestAddedAwaiting(t *testing.T) {
	be := &fakeBackend{}
	s := startSession(t, be)
	for _, c := range []struct {
		state downexv1.DownloadState
		want  bool
	}{
		{downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, false},
		{downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, false},
		{downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED, false},
	} {
		be.mu.Lock()
		be.state = c.state
		be.mu.Unlock()
		for _, msg := range []map[string]any{
			{"type": "add", "url": "https://example.test/f.mp4", "mime": "video/mp4", "size": 48 << 20},
			{"type": "addMedia", "manifestUrl": "https://cdn.test/v/master.m3u8", "videoTrack": "v720"},
		} {
			msg["id"] = fmt.Sprint(msg["type"], "-", c.state)
			s.send(msg)
			m := s.recv()
			awaiting, has := m["awaiting"]
			if m["type"] != "added" || !has || awaiting != c.want {
				t.Errorf("%s in state %v: %v, want awaiting %v", msg["type"], c.state, m, c.want)
			}
		}
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

func errorMessage(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	msg, _ := e["message"].(string)
	return msg
}

// probeOnly is a daemon.MediaJobs whose probe answers from a function; it never runs.
type probeOnly struct {
	mu     sync.Mutex
	probe  func(sel daemon.MediaSelection) (*daemon.MediaProbe, error)
	probes []daemon.MediaSelection
}

func (p *probeOnly) Probe(_ context.Context, _ transport.Factory, sel daemon.MediaSelection) (*daemon.MediaProbe, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probes = append(p.probes, sel)
	return p.probe(sel)
}

func (p *probeOnly) Run(ctx context.Context, _ daemon.MediaRunOptions, _ daemon.MediaSelection, _ *model.MediaCheckpoint,
	_ func(daemon.MediaProgress), _ func(model.MediaCheckpoint) error) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestAddMediaRoundTrip hands a stream to a real daemon through the relay: a browser
// hand-off awaits confirmation with its tracks; encrypted and live streams are rejected
// with their codes.
func TestAddMediaRoundTrip(t *testing.T) {
	pj := &probeOnly{probe: func(sel daemon.MediaSelection) (*daemon.MediaProbe, error) {
		switch {
		case strings.Contains(sel.ManifestURL, "drm"):
			return nil, fmt.Errorf("parse playlist: %w", media.ErrEncrypted)
		case strings.Contains(sel.ManifestURL, "live"):
			return nil, fmt.Errorf("playlist: %w", media.ErrLive)
		}
		return &daemon.MediaProbe{Format: "dash", Tracks: []media.Track{
			{ID: "720p", Kind: media.KindVideo, Width: 1280, Height: 720, Bandwidth: 3_000_000},
			{ID: "aac", Kind: media.KindAudio, Bandwidth: 128_000},
		}, DefaultVideo: "720p", DefaultAudio: "aac", Duration: time.Minute, Container: "mp4", FragmentsTotal: 31}, nil
	}}
	d := newMediaDaemonServer(t, pj)
	be := &daemonBackend{env: d.env(func() bool { return true }, func(string, ...string) error {
		t.Error("the relay started a daemon although one runs")
		return nil
	})}
	s := startSession(t, be)
	s.send(map[string]any{"id": "m", "type": "addMedia", "manifestUrl": "https://cdn.test/show/manifest.mpd",
		"headers": [][]string{{"Cookie", "s=1"}}, "title": "The Show", "pageUrl": "https://site.test/p",
		"videoTrack": "720p", "audioTrack": "aac"})
	m := s.recv()
	id, ok := m["downloadId"].(float64)
	if m["type"] != "added" || !ok || id <= 0 || m["awaiting"] != true {
		t.Fatalf("addMedia: %v", m)
	}
	got, err := d.dl.Get(context.Background(), connect.NewRequest(&downexv1.GetRequest{Id: int64(id)}))
	if err != nil {
		t.Fatal(err)
	}
	g := got.Msg.GetDownload()
	if g.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION || g.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA ||
		g.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || g.GetFilename() != "The Show.mp4" ||
		len(g.GetMedia().GetTracks()) != 2 || g.GetMedia().GetFormat() != "dash" || g.GetMedia().GetTitle() != "The Show" {
		t.Fatalf("handed-off media download = %v", g)
	}
	pj.mu.Lock()
	if len(pj.probes) != 1 || pj.probes[0].Headers.Get("Cookie") != "s=1" ||
		pj.probes[0].VideoTrack != "720p" || pj.probes[0].AudioTrack != "aac" {
		t.Errorf("probes = %+v", pj.probes)
	}
	pj.mu.Unlock()

	for _, c := range []struct{ url, code string }{
		{"https://cdn.test/drm/manifest.mpd", "encrypted"},
		{"https://cdn.test/live/index.m3u8", "live"},
	} {
		s.send(map[string]any{"id": c.code, "type": "addMedia", "manifestUrl": c.url})
		if m := s.recv(); errorCode(m) != codeRejected || !strings.HasPrefix(errorMessage(m), c.code+": ") {
			t.Errorf("%s: %v", c.code, m)
		}
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// TestAddRenewCheck: add forwards pageUrl to the daemon, and added says renewCheck when
// downexd checks first whether the link continues a download whose link expired
// (docs/BROWSER-SPEC.md §2).
func TestAddRenewCheck(t *testing.T) {
	be := &fakeBackend{state: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED}
	s := startSession(t, be)
	for _, renew := range []bool{true, false} {
		be.mu.Lock()
		be.renew = renew
		be.mu.Unlock()
		s.send(map[string]any{"id": fmt.Sprint("r", renew), "type": "add", "url": "https://cdn.example/f.zip?sig=1",
			"pageUrl": "https://pages.example/files?id=7"})
		m := s.recv()
		if m["type"] != "added" || m["renewCheck"] != renew {
			t.Fatalf("added = %v, want renewCheck %v", m, renew)
		}
	}
	be.mu.Lock()
	page := be.adds[0].GetPageUrl()
	be.mu.Unlock()
	if page != "https://pages.example/files?id=7" {
		t.Fatalf("page_url = %q", page)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}
