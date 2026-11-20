package media

import (
	"strconv"
	"strings"
	"time"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
	"downex/ui/core/text"
)

// Pages with several videos (docs/SITE-MEDIA-SPEC.md §3.12, docs/DESKTOP-SPEC.md §1): the item
// row of Confirm download and Add → Video, with the extension's words
// (ext/src/lib/item-strip.ts).

// ShowsItems reports whether a page's info has an item row: two or more videos, or other
// items left out of the list (photos, links, videos Downex cannot save).
func ShowsItems(info *downexv1.MediaInfo) bool {
	return IsSite(info) && (len(info.GetItems()) > 1 || info.GetItemsLeftOut() > 0)
}

// ItemIndex is the position of the item info describes in its list (-1 when none).
func ItemIndex(info *downexv1.MediaInfo) int {
	for i, it := range info.GetItems() {
		if it.GetId() == info.GetEntryId() {
			return i
		}
	}
	return -1
}

// ItemNoun names the items of a page: "Track" on an audio page (AudioPage, docs/SITE-MEDIA-SPEC.md
// §3.13), else "Video".
func ItemNoun(info *downexv1.MediaInfo) string {
	if AudioPage(info) {
		return "Track"
	}
	return "Video"
}

// ItemsCountText is "Video 2 of 3" ("3 videos" while none is chosen; "" without items); an
// audio page's are tracks ("Track 2 of 3", ItemNoun).
func ItemsCountText(info *downexv1.MediaInfo) string {
	noun := ItemNoun(info)
	n := len(info.GetItems())
	switch i := ItemIndex(info); {
	case n == 0:
		return ""
	case i >= 0:
		return noun + " " + strconv.Itoa(i+1) + " of " + strconv.Itoa(n)
	case n == 1:
		return "1 " + strings.ToLower(noun)
	}
	return strconv.Itoa(n) + " " + strings.ToLower(noun) + "s"
}

// ItemsLeftOutText is the note on the other items of the page ("" for none). Photos, links,
// nested lists, live streams, videos without an MP4 offer and videos past 20 are all left out
// (docs/SITE-MEDIA-SPEC.md §3.12), so it names no single cause. Of an audio page (audio) the
// items are links or audio Downex cannot save.
func ItemsLeftOutText(n int32, audio bool) string {
	switch {
	case n <= 0:
		return ""
	case n == 1 && audio:
		return "1 other item not listed: a link or audio Downex cannot save."
	case n == 1:
		return "1 other item not listed: a photo, a link or a video Downex cannot save."
	case audio:
		return strconv.Itoa(int(n)) + " other items not listed: links or audio Downex cannot save."
	}
	return strconv.Itoa(int(n)) + " other items not listed: photos, links or videos Downex cannot save."
}

// ItemOption is one item of the row's select: "Video 2" ("Track 2"), its title (bidi-escaped)
// and length.
type ItemOption struct {
	Value, Label, Detail, Meta string
}

// ItemOptions are the select's options, in the page's order.
func ItemOptions(info *downexv1.MediaInfo) []ItemOption {
	noun := ItemNoun(info)
	out := make([]ItemOption, 0, len(info.GetItems()))
	for i, it := range info.GetItems() {
		o := ItemOption{Value: it.GetId(), Label: noun + " " + strconv.Itoa(i+1), Detail: text.EscapeBidi(text.TrimJS(it.GetTitle()))}
		if ms := it.GetDurationMs(); ms > 0 {
			o.Meta = imedia.FormatClipTime(time.Duration(ms) * time.Millisecond)
		}
		out = append(out, o)
	}
	return out
}

// ItemName is the name proposed for a page's video while the dialog shows it
// (docs/SITE-MEDIA-SPEC.md §3.12): its title, with its position when another item has the same
// title ("Video by example 2"), and ".mp4"; "video.mp4" without a title. It is made by downexd's
// own rule (transport.MediaFileName: sanitised and shortened), so it is the name the file gets and
// one the dialog's file check accepts.
func ItemName(info *downexv1.MediaInfo) string {
	title := text.TrimJS(info.GetTitle())
	if title == "" {
		return "video" + SiteExtension(info)
	}
	pos, same := 0, false
	for i, it := range info.GetItems() {
		switch {
		case it.GetId() == info.GetEntryId():
			pos = i + 1
		case it.GetTitle() == info.GetTitle():
			same = true
		}
	}
	if pos > 0 && same {
		title += " " + strconv.Itoa(pos)
	}
	return transport.MediaFileName(title, strings.TrimPrefix(SiteExtension(info), "."))
}
