package main

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// addMedia and addVideo carry a part (clipStartMs/clipEndMs, docs/MEDIA-SPEC.md §8),
// checked with the daemon's bounds before anything reaches it.
func TestAddClipFields(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	s.send(map[string]any{"id": "m", "type": "addMedia", "manifestUrl": "https://cdn.test/v/master.m3u8",
		"clipStartMs": 80_000, "clipEndMs": 165_000})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("addMedia: %v", m)
	}
	s.send(map[string]any{"id": "v", "type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa",
		"clipStartMs": 61_500})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("addVideo: %v", m)
	}
	be.fakeBackend.mu.Lock()
	media := be.fakeBackend.media[0]
	be.fakeBackend.mu.Unlock()
	be.mu.Lock()
	video := be.videoAdds[0]
	be.mu.Unlock()
	if c := media.GetClip(); c.GetStartMs() != 80_000 || c.GetEndMs() != 165_000 {
		t.Errorf("addMedia clip %v", c)
	}
	if c := video.GetClip(); c.GetStartMs() != 61_500 || c.GetEndMs() != 0 {
		t.Errorf("addVideo clip %v", c)
	}

	for name, msg := range map[string]map[string]any{
		"end only":    {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "clipEndMs": 5000},
		"too short":   {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "clipStartMs": 5000, "clipEndMs": 5500},
		"reversed":    {"type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa", "clipStartMs": 9000, "clipEndMs": 5000},
		"negative":    {"type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa", "clipStartMs": -1},
		"over 168 h":  {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "clipStartMs": 0, "clipEndMs": validate.MaxClipMs + 1},
		"not integer": {"type": "addMedia", "manifestUrl": "https://x.test/a.mpd", "clipStartMs": 1.5},
		"wrong type":  {"type": "addVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa", "clipStartMs": "80"},
	} {
		msg["id"] = name
		s.send(msg)
		if m := s.recv(); errorCode(m) != codeInvalidRequest {
			t.Errorf("%s: %v", name, m)
		}
	}
	be.fakeBackend.mu.Lock()
	n := len(be.fakeBackend.media)
	be.fakeBackend.mu.Unlock()
	be.mu.Lock()
	n += len(be.videoAdds)
	be.mu.Unlock()
	if n != 2 {
		t.Errorf("refused parts reached the daemon: %d adds", n)
	}
}

// videoInfo carries the chapters; they are dropped first when the answer would exceed
// its limit.
func TestVideoInfoChapters(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	info := siteInfo(3)
	info.Chapters = []*downexv1.Chapter{{StartMs: 0, EndMs: 80_000, Title: "Intro"}, {StartMs: 80_000, EndMs: 212_000, Title: "Rest"}}
	be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return info, nil }
	s.send(map[string]any{"id": "p", "type": "probeVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa"})
	m := s.recv()
	cs, _ := m["chapters"].([]any)
	if len(cs) != 2 || cs[1].(map[string]any)["title"] != "Rest" || cs[1].(map[string]any)["startMs"] != 80_000.0 {
		t.Fatalf("chapters %v", m["chapters"])
	}
	big := siteInfo(100)
	for i := range validate.MaxChapters {
		big.Chapters = append(big.Chapters, &downexv1.Chapter{StartMs: int64(i) * 1000, EndMs: int64(i+1) * 1000, Title: strings.Repeat("c", 200)})
	}
	for _, tr := range big.GetTracks() {
		tr.Name = strings.Repeat("n", 1500)
	}
	be.mu.Lock()
	be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return big, nil }
	be.mu.Unlock()
	s.send(map[string]any{"id": "b", "type": "probeVideo", "pageUrl": "https://www.youtube.com/watch?v=aaaaaaaaaaa"})
	m = s.recv()
	if m["type"] != "videoInfo" || len(m["chapters"].([]any)) != 0 || len(m["tracks"].([]any)) != 101 {
		t.Fatalf("an oversized answer keeps its tracks without chapters: %v", errorMessage(m))
	}
}
