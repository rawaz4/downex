package sitemedia

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"downex/internal/media"
)

// Pages with several videos (docs/SITE-MEDIA-SPEC.md §3.12), from yt-dlp output trimmed
// to example hosts: a carousel of three silent videos (testdata/carousel.json) and a
// news page with a repeated entry, a link, a live entry, a WebM-only entry and items
// playlist_count names beyond the list (testdata/multi.json).

const pageHostExample = "www.example.com"

func TestParseCarousel(t *testing.T) {
	info := parseFixture(t, "carousel.json")
	if !info.Playlist || info.ID != "CarouselPost1" || info.Title != "Post by example" || len(info.Formats) != 0 {
		t.Fatalf("playlist = %+v", info)
	}
	var ids []string
	for _, e := range info.Entries {
		ids = append(ids, e.ID)
		if !e.Silent || e.ExtractorKey != "Instagram" || e.Site != "Instagram" || e.Title != "Video by example" {
			t.Errorf("entry %s = %+v", e.ID, e)
		}
	}
	if want := []string{"CarouselItem1", "CarouselItem2", "CarouselItem3"}; !slices.Equal(ids, want) {
		t.Fatalf("entries = %v, want %v", ids, want)
	}
	if info.LeftOut != 0 {
		t.Errorf("LeftOut = %d, want 0", info.LeftOut)
	}
	if got := info.Entries[0].Thumbnail; !strings.HasPrefix(got, "https://img.example/carousel/1.jpg?") {
		t.Errorf("thumbnail 1 = %q", got)
	}
	if got := info.Entries[2].Thumbnail; got != "" {
		t.Errorf("an http thumbnail was kept: %q", got)
	}

	e, err := ChooseEntry(info, "", pageHostExample)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "CarouselItem1" || e.Info != info.Entries[0] || len(e.Items) != 3 || e.LeftOut != 0 {
		t.Fatalf("default entry = %+v", e)
	}
	off, err := Offers(e.Info, pageHostExample)
	if err != nil || off.DefaultVideo != "dash-975165557122390v" || off.DefaultAudio != "" {
		t.Fatalf("offer of item 1 = %+v, %v", off, err)
	}
	second, err := ChooseEntry(info, "CarouselItem2", pageHostExample)
	if err != nil || second.ID != "CarouselItem2" {
		t.Fatalf("item 2 = %+v, %v", second, err)
	}
	off2, err := Offers(second.Info, pageHostExample)
	if err != nil || off2.DefaultVideo != "dash-681006613649773v" {
		t.Fatalf("offer of item 2 = %+v, %v", off2, err)
	}
	// The same format id in two items is two tracks: keys carry the item's id.
	k1, k2 := info.Entries[0].Format("0").Key, info.Entries[1].Format("0").Key
	if k1 == "" || k1 == k2 {
		t.Errorf("keys of format 0 in items 1 and 2: %q, %q", k1, k2)
	}
	if _, err := ChooseEntry(info, "CarouselGone", pageHostExample); !errors.Is(err, ErrItemGone) ||
		!errors.Is(err, ErrSiteUnavailable) {
		t.Errorf("a gone item = %v, want ErrItemGone (site_unavailable)", err)
	}
}

func TestParseMultiDuplicatesAndLeftOut(t *testing.T) {
	info := parseFixture(t, "multi.json")
	var ids []string
	for _, e := range info.Entries {
		ids = append(ids, e.ID)
	}
	if want := []string{"me1", "me2", "me5"}; !slices.Equal(ids, want) {
		t.Fatalf("entries = %v, want %v (the repeated me1 skipped)", ids, want)
	}
	// The link, the live entry and the two items playlist_count names beyond the six listed.
	if info.LeftOut != 4 {
		t.Errorf("LeftOut = %d, want 4", info.LeftOut)
	}
	if info.Title != info.ID {
		t.Errorf("a playlist without a title is named by its id: %q", info.Title)
	}
	if e := info.Entries[1]; e.ExtractorKey != "CNN" || e.Duration != 98*time.Second {
		t.Errorf("entry me2 (no extractor_key of its own) = %+v", e)
	}
	e, err := ChooseEntry(info, "", pageHostExample)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		{ID: "me1", Title: "The first story", Duration: 112 * time.Second, Thumbnail: "https://img.example/news/1.jpg"},
		{ID: "me2", Title: "The second story", Duration: 98 * time.Second, Thumbnail: "https://img.example/news/2.jpg"},
	}
	if e.ID != "me1" || !slices.Equal(e.Items, want) || e.LeftOut != 5 {
		t.Fatalf("entry = id %q items %+v left out %d; want me1, %+v, 5 (me5 has no MP4)", e.ID, e.Items, e.LeftOut, want)
	}
	if _, err := ChooseEntry(info, "me5", pageHostExample); !errors.Is(err, ErrItemGone) {
		t.Errorf("an item without an offer = %v, want ErrItemGone", err)
	}
	if e, err := ChooseEntry(info, "me2", pageHostExample); err != nil || e.Info.Title != "The second story" {
		t.Errorf("me2 = %+v, %v", e, err)
	}
}

// entryJSON is a playlist entry with one muxed MP4 format.
func entryJSON(id string) map[string]any {
	return map[string]any{"id": id, "title": "Video " + id, "formats": []any{map[string]any{
		"format_id": "f", "ext": "mp4", "protocol": "https", "vcodec": "avc1.64001F", "acodec": "mp4a.40.2",
		"width": 1280, "height": 720, "url": "https://media.example/" + id + ".mp4",
	}}}
}

func playlistJSON(t *testing.T, id string, extra map[string]any, entries ...any) []byte {
	t.Helper()
	m := map[string]any{"_type": "playlist", "id": id, "extractor_key": "Example", "entries": entries}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseEntriesCap(t *testing.T) {
	var entries []any
	for i := range MaxEntries + 5 {
		entries = append(entries, entryJSON(fmt.Sprintf("v%02d", i)))
	}
	entries = append(entries, entryJSON("v00"), entryJSON("v22")) // the same videos again
	info, err := Parse(playlistJSON(t, "list", nil, entries...), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Entries) != MaxEntries || info.Entries[MaxEntries-1].ID != "v19" || info.LeftOut != 5 {
		t.Fatalf("%d entries (last %s), %d left out; want %d, v19, 5", len(info.Entries),
			info.Entries[len(info.Entries)-1].ID, info.LeftOut, MaxEntries)
	}
	// A link with a video's id is not that video.
	link := map[string]any{"_type": "url", "id": "v1", "url": "https://www.example.com/v1"}
	info, err = Parse(playlistJSON(t, "list", nil, link, entryJSON("v1")), testNow)
	if err != nil || len(info.Entries) != 1 || info.Entries[0].ID != "v1" || info.LeftOut != 1 {
		t.Errorf("a link before its video: %+v, %v", info, err)
	}
}

func TestChooseEntry(t *testing.T) {
	// The item whose id is the playlist's own is the default.
	info, err := Parse(playlistJSON(t, "b", nil, entryJSON("a"), entryJSON("b"), entryJSON("c")), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := ChooseEntry(info, "", pageHostExample); err != nil || e.ID != "b" || len(e.Items) != 3 {
		t.Errorf("default = %+v, %v; want b", e, err)
	}
	// A page with one video is its own entry.
	single := parseFixture(t, "muxed.json")
	for _, id := range []string{"", single.ID} {
		if e, err := ChooseEntry(single, id, pageHostExample); err != nil || e.Info != single || e.ID != "" || e.Items != nil {
			t.Errorf("single page, id %q = %+v, %v", id, e, err)
		}
	}
	if _, err := ChooseEntry(single, "other", pageHostExample); !errors.Is(err, ErrItemGone) {
		t.Errorf("single page, another id = %v, want ErrItemGone", err)
	}
	// No entry with an offer: the first entry, whose offer says why.
	webm := map[string]any{"id": "w", "formats": []any{map[string]any{"format_id": "v", "ext": "webm", "protocol": "https",
		"vcodec": "vp9", "acodec": "opus", "url": "https://media.example/w.webm"}}}
	info, err = Parse(playlistJSON(t, "p", nil, webm), testNow)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ChooseEntry(info, "", pageHostExample)
	if err != nil || e.ID != "w" || len(e.Items) != 0 || e.LeftOut != 1 {
		t.Fatalf("no offer = %+v, %v", e, err)
	}
	if _, err := Offers(e.Info, pageHostExample); !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("offer = %v, want media_unsupported", err)
	}
}

func TestParsePlaylistRefusals(t *testing.T) {
	live := map[string]any{"id": "l", "is_live": true}
	link := map[string]any{"_type": "url", "id": "u", "url": "https://www.example.com/u"}
	cases := map[string]struct {
		data []byte
		want error
	}{
		"links only":      {playlistJSON(t, "p", nil, link), media.ErrUnsupported},
		"live only":       {playlistJSON(t, "p", nil, live), media.ErrLive},
		"live and a link": {playlistJSON(t, "p", nil, live, link), media.ErrUnsupported},
		"no entries":      {playlistJSON(t, "p", map[string]any{"playlist_count": 3}), media.ErrUnsupported},
		"bad entries":     {[]byte(`{"_type":"multi_video","id":"p","extractor_key":"E","entries":[{"id":1},"x",[]]}`), media.ErrUnsupported},
		"no id":           {[]byte(`{"_type":"playlist","extractor_key":"E","entries":[]}`), media.ErrMalformed},
	}
	for name, c := range cases {
		if _, err := Parse(c.data, testNow); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// An entry with too many formats is left out, not the page.
	var formats []any
	for i := range MaxFormats + 1 {
		formats = append(formats, map[string]any{"format_id": fmt.Sprint(i)})
	}
	big := map[string]any{"id": "big", "formats": formats}
	info, err := Parse(playlistJSON(t, "p", nil, big, entryJSON("ok")), testNow)
	if err != nil || len(info.Entries) != 1 || info.LeftOut != 1 {
		t.Errorf("an entry with too many formats: %+v, %v", info, err)
	}
}

func TestThumbnailURL(t *testing.T) {
	for raw, want := range map[string]string{
		`"https://img.example/a.jpg?x=1"`:  "https://img.example/a.jpg?x=1",
		`"http://img.example/a.jpg"`:       "",
		`"https://127.0.0.1/a.jpg"`:        "",
		`"https://[::1]/a.jpg"`:            "",
		`"https://localhost/a.jpg"`:        "",
		`"https://cdn.localhost/a.jpg"`:    "",
		`"https://intranet/a.jpg"`:         "",
		`"https://u:p@img.example/a.jpg"`:  "",
		`"https://img.example/a.jpg#frag"`: "",
		`"data:image/png;base64,AAAA"`:     "",
		`42`:                               "",
		`"https://img.example/` + strings.Repeat("a", maxThumbnailBytes) + `"`: "",
	} {
		if got := thumbnailURL(json.RawMessage(raw)); got != want {
			t.Errorf("thumbnailURL(%.40s) = %q, want %q", raw, got, want)
		}
	}
}
