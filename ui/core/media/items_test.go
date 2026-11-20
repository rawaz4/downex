package media

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

func TestItems(t *testing.T) {
	info := &downexv1.MediaInfo{Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, EntryId: "b", Title: "Video by example", ItemsLeftOut: 2,
		Items: []*downexv1.MediaItem{
			{Id: "a", Title: "Video by example", DurationMs: 12_000},
			{Id: "b", Title: "Video by example", DurationMs: 75_500},
			{Id: "c", Title: " Third "},
		}}
	if !ShowsItems(info) || ItemIndex(info) != 1 || ItemsCountText(info) != "Video 2 of 3" {
		t.Errorf("shows %v, index %d, count %q", ShowsItems(info), ItemIndex(info), ItemsCountText(info))
	}
	want := []ItemOption{{"a", "Video 1", "Video by example", "0:12"}, {"b", "Video 2", "Video by example", "1:15.5"}, {"c", "Video 3", "Third", ""}}
	got := ItemOptions(info)
	if len(got) != len(want) {
		t.Fatalf("options %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for name, c := range map[string]struct {
		info *downexv1.MediaInfo
		want string
	}{
		"shared title":    {info, "Video by example 2.mp4"},
		"own title":       {&downexv1.MediaInfo{EntryId: "c", Title: "Third", Items: info.Items}, "Third.mp4"},
		"no title":        {&downexv1.MediaInfo{EntryId: "c", Items: info.Items}, "video.mp4"},
		"one video":       {&downexv1.MediaInfo{Title: "A talk"}, "A talk.mp4"},
		"item not listed": {&downexv1.MediaInfo{EntryId: "z", Title: "Video by example", Items: info.Items}, "Video by example.mp4"},
		// downexd's rule (transport.MediaFileName): sanitised, shortened, never refused by the dialog
		"slash":     {&downexv1.MediaInfo{EntryId: "c", Title: "24/7 coverage: live?", Items: info.Items}, "24_7 coverage_ live_.mp4"},
		"dots only": {&downexv1.MediaInfo{EntryId: "c", Title: "...", Items: info.Items}, "download.mp4"},
		"long":      {&downexv1.MediaInfo{EntryId: "c", Title: strings.Repeat("Long news title ", 20), Items: info.Items}, strings.Repeat("Long news title ", 14)[:223] + ".mp4"},
	} {
		if got := ItemName(c.info); got != c.want {
			t.Errorf("%s: ItemName = %q, want %q", name, got, c.want)
		}
	}
	for n, want := range map[int32]string{0: "", 1: "1 other item not listed: a photo, a link or a video Downex cannot save.",
		3: "3 other items not listed: photos, links or videos Downex cannot save."} {
		if got := ItemsLeftOutText(n, false); got != want {
			t.Errorf("ItemsLeftOutText(%d) = %q", n, got)
		}
	}
	// A page with one video, and a manifest, have no row; a page with one video and photos has.
	one := &downexv1.MediaInfo{Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, EntryId: "a", Items: info.Items[:1]}
	if ShowsItems(one) || ShowsItems(&downexv1.MediaInfo{Items: info.Items}) || ItemsCountText(&downexv1.MediaInfo{}) != "" {
		t.Error("a row without several videos")
	}
	one.ItemsLeftOut = 1
	if !ShowsItems(one) || ItemsCountText(one) != "Video 1 of 1" {
		t.Errorf("one video with photos: %v %q", ShowsItems(one), ItemsCountText(one))
	}
	if ItemsCountText(&downexv1.MediaInfo{Items: info.Items}) != "3 videos" {
		t.Error("count without a chosen item")
	}
}
