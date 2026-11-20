package main

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Pages with several videos (docs/SITE-MEDIA-SPEC.md §3.12): probeVideo and addVideo
// forward entryId, and videoInfo carries the items, the item described and how many
// other items the page has; thumbnails go after the chapters when an answer is too large.

func TestVideoItems(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	info := siteInfo(2)
	info.EntryId, info.ItemsLeftOut = "itemB", 1
	info.Items = []*downexv1.MediaItem{
		{Id: "itemA", Title: "Video by example", DurationMs: 12_000, Thumbnail: "https://img.example/a.jpg"},
		{Id: "itemB", Title: "Video by example", DurationMs: 7_000, Thumbnail: "https://img.example/b.jpg"},
	}
	be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return info, nil }
	page := "https://www.instagram.com/p/AbCdEf/"
	s.send(map[string]any{"id": "p", "type": "probeVideo", "pageUrl": page, "entryId": "itemB"})
	m := s.recv()
	items, _ := m["items"].([]any)
	if m["type"] != "videoInfo" || m["entryId"] != "itemB" || m["itemsLeftOut"] != 1.0 || len(items) != 2 {
		t.Fatalf("videoInfo: %v", m)
	}
	if it := items[1].(map[string]any); it["id"] != "itemB" || it["durationMs"] != 7000.0 || it["thumbnail"] != "https://img.example/b.jpg" ||
		it["title"] != "Video by example" {
		t.Errorf("item: %v", it)
	}
	s.send(map[string]any{"id": "a", "type": "addVideo", "pageUrl": page, "entryId": "itemA", "videoTrack": "v0"})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("addVideo: %v", m)
	}
	be.mu.Lock()
	probe, add := be.probes[0], be.videoAdds[0]
	be.mu.Unlock()
	if probe.GetEntryId() != "itemB" || add.GetEntryId() != "itemA" || add.GetVideoTrack() != "v0" {
		t.Errorf("forwarded: probe %v, add %v", probe, add)
	}

	// A page with one video answers no items.
	be.mu.Lock()
	be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return siteInfo(2), nil }
	be.mu.Unlock()
	s.send(map[string]any{"id": "one", "type": "probeVideo", "pageUrl": page})
	if m := s.recv(); m["type"] != "videoInfo" || m["entryId"] != nil || m["items"] != nil || m["itemsLeftOut"] != nil {
		t.Errorf("one video: %v", m)
	}

	for name, msg := range map[string]map[string]any{
		"control":    {"type": "probeVideo", "pageUrl": page, "entryId": "a\nb"},
		"too long":   {"type": "addVideo", "pageUrl": page, "entryId": strings.Repeat("x", 257)},
		"wrong type": {"type": "addVideo", "pageUrl": page, "entryId": 3},
	} {
		msg["id"] = name
		s.send(msg)
		if m := s.recv(); errorCode(m) != codeInvalidRequest {
			t.Errorf("%s: %v", name, m)
		}
	}
}

// An answer too large with its chapters left out drops the items' thumbnails next, never
// the items.
func TestVideoItemsAnswerLimit(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	big := siteInfo(100)
	for _, tr := range big.GetTracks() {
		tr.Name = strings.Repeat("n", 2300)
	}
	for i := range 25 {
		big.Items = append(big.Items, &downexv1.MediaItem{Id: "item" + string(rune('A'+i)), Title: "Item",
			Thumbnail: "https://img.example/" + strings.Repeat("t", 2000)})
	}
	be.probe = func(*downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) { return big, nil }
	s.send(map[string]any{"id": "b", "type": "probeVideo", "pageUrl": "https://www.example.com/post/1"})
	m := s.recv()
	items, _ := m["items"].([]any)
	if m["type"] != "videoInfo" || len(items) != maxVideoItems || m["itemsLeftOut"] != 5.0 {
		t.Fatalf("answer: %v", errorMessage(m))
	}
	for _, it := range items {
		if th := it.(map[string]any)["thumbnail"]; th != "" {
			t.Fatalf("thumbnail kept in an oversized answer")
		}
	}
}
