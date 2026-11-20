package add

import (
	"math"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/media"
	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// The ported cases of ui/frontend/src/lib/components/MediaPicker.test.ts, by their names.

// showPicker renders a picker's part in a window (render(MediaPicker, …)).
func showPicker(t *testing.T, info *downexv1.MediaInfo, part Part, disabled bool) (*viewstest.Harness, *Picker) {
	t.Helper()
	h := newHarness(t)
	p := NewPicker(info, media.DefaultPick(info))
	p.SetDisabled(disabled)
	root := &kit.Element{Style: kit.Style{Width: kit.Px(600)}}
	if w := p.Part(part); w != nil {
		root.Add(w)
	}
	h.Show(root)
	return h, p
}

func TestMediaPickerQualityCards(t *testing.T) {
	t.Run("shows one card per class in downexd’s order: the class, its standard suffix, the codec and the size", func(t *testing.T) {
		_, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAll, false)
		q := p.QualitySection()
		eq(t, "group", q.Semantics.Name, "Quality")
		eq(t, "group role", q.Semantics.Role, kit.RoleGroup)
		var ids []string
		for _, c := range q.Cards() {
			ids = append(ids, c.ID())
		}
		eq(t, "values", ids, []string{"299", "136", "397", "18"})
		first := q.Card("299")
		eq(t, "cls", first.Class(), "1080p")
		eq(t, "suffix", first.Suffix(), "(Full HD) 60fps")
		eq(t, "codec", first.Codec(), "H.264")
		eq(t, "size", first.Size(), "482 MB")
		// a class without a suffix, an AV1 one, a size the site estimated
		c397 := q.Card("397")
		eq(t, "cls", c397.Class(), "480p")
		if c397.suff != nil {
			t.Error("a suffix for 480p")
		}
		eq(t, "codec", c397.Codec(), "AV1")
		eq(t, "size", c397.Size(), "~168 MB")
		// the tooltip names the whole quality (its suffix is cut in a narrow column), then its picture
		eq(t, "tip", first.Tooltip, "1080p (Full HD) 60fps · 1920×1080")
		eq(t, "tip", c397.Tooltip, "480p · 854×480")
		eq(t, "hint", QualityHint(len(p.Videos())), "One MP4 per resolution, best first")
	})
	t.Run("marks the chosen card in Mint with a tick, and follows a click", func(t *testing.T) {
		h, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAll, false)
		q := p.QualitySection()
		eq(t, "checked", q.Checked().ID(), "299")
		if !q.Card("299").On() || !q.Card("299").HasTick() || q.Card("136").HasTick() {
			t.Error("the chosen card's look")
		}
		h.Click(q.Card("136").Base())
		eq(t, "checked", q.Checked().ID(), "136")
		if !q.Card("136").On() || q.Card("299").On() {
			t.Error("the look did not follow")
		}
		ticks := 0
		for _, c := range q.Cards() {
			if c.HasTick() {
				ticks++
			}
		}
		eq(t, "ticks", ticks, 1)
	})
	t.Run("is one native radio group for the keyboard: one name, every card a real radio, Space chooses", func(t *testing.T) {
		h, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAll, false)
		q := p.QualitySection()
		eq(t, "group role", q.Group().Semantics.Role, kit.RoleRadioGroup)
		for _, c := range q.Cards() {
			if c.Semantics.Role != kit.RoleRadioButton {
				t.Errorf("card %s is a %v", c.ID(), c.Semantics.Role)
			}
		}
		// the keyboard tabs to the checked one; the arrows move between them
		eq(t, "tab stop", q.Card("299").Focus, kit.FocusTab)
		target := q.Card("397")
		h.Window.Focus(target)
		if !isFocused(h, target) {
			t.Fatal("not focusable")
		}
		// Space on a focused radio chooses it
		key(h, kit.KeySpace, 0)
		eq(t, "checked", q.Checked(), target)
		key(h, kit.KeyArrowRight, 0)
		eq(t, "after Right", q.Checked().ID(), "18")
		if !isFocused(h, q.Card("18")) {
			t.Error("the focus did not follow the arrow")
		}
		key(h, kit.KeyArrowDown, 0)
		eq(t, "after Down (wraps)", q.Checked().ID(), "299")
	})
	t.Run("names each card as its quality, codecs and size, and offers no audio-only card for a page", func(t *testing.T) {
		_, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAll, false)
		var names []string
		for _, c := range p.QualitySection().Cards() {
			names = append(names, c.Semantics.Name)
		}
		eq(t, "names", names, []string{
			"1080p (Full HD) 60fps · H.264 · 482 MB",
			"720p (HD) · H.264 · 126 MB",
			"480p · AV1 · ~168 MB",
			"360p · H.264 + AAC · ~45.0 MB",
		})
		if p.QualitySection().Card("") != nil {
			t.Error("an audio-only card for a page")
		}
	})
	t.Run(`offers "No video (audio only)" as the last card only where the daemon picked no video`, func(t *testing.T) {
		_, p := showPicker(t, formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "r1" }), PartAll, false)
		q := p.QualitySection()
		var ids []string
		for _, c := range q.Cards() {
			ids = append(ids, c.ID())
		}
		eq(t, "values", ids, []string{"v1", "v2", "v0", ""})
		eq(t, "checked", q.Checked().Semantics.Name, "No video (audio only)")
		eq(t, "cls", q.Card("").Class(), "Audio only")
	})
	t.Run("turns every card off while the dialog is busy", func(t *testing.T) {
		_, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAll, true)
		for _, c := range p.QualitySection().Cards() {
			if !c.Disabled {
				t.Errorf("card %s enabled", c.ID())
			}
		}
		if p.QualitySection().Group().Semantics.States&kit.NodeDisabled == 0 {
			t.Error("the group is not aria-disabled")
		}
	})
	t.Run("explains a mix of H.264 with newer codecs, and says nothing when all are H.264", func(t *testing.T) {
		_, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAll, false)
		q := p.QualitySection()
		if q.note == nil {
			t.Fatal("no note")
		}
		eq(t, "note", q.note.Text(), "AV1 is smaller; H.264 plays everywhere.")
		// the group is described by the hint and the note
		eq(t, "described", q.Group().Semantics.Description, "One MP4 per resolution, best first AV1 is smaller; H.264 plays everywhere.")
		// one line in a narrow quality column: cut with an ellipsis there, whole in its tooltip
		eq(t, "tooltip", q.note.Parent().Tooltip, "AV1 is smaller; H.264 plays everywhere.")
		_, plain := showPicker(t, formattest.MediaInfo(nil), PartAll, false)
		if plain.QualitySection().note != nil {
			t.Error("a note without a mix")
		}
	})
}

func TestMediaPickerAudioTrack(t *testing.T) {
	t.Run("chooses among a page’s audio tracks with a custom select", func(t *testing.T) {
		h, p := showPicker(t, formattest.SiteMediaInfo(nil), PartAudio, false)
		// only the audio part
		if p.QualitySection() != nil {
			t.Error("cards in the audio part")
		}
		a := p.audio[0].Select
		eq(t, "value", a.Value(), "140")
		eq(t, "values", optionValues(a), []string{"140", "140-1"})
		eq(t, "labels", optionLabels(a), []string{"English original", "Deutsch (German)"})
		choose(t, h, a, "140-1")
		eq(t, "value", a.Value(), "140-1")
		eq(t, "pick", p.Pick().Audio, "140-1")
	})
	t.Run("shows the one track there is, or that the chosen quality has its own sound", func(t *testing.T) {
		info := formattest.SiteMediaInfo(nil)
		var tracks []*downexv1.MediaTrack
		for _, tr := range info.GetTracks() {
			if tr.GetId() != "140-1" {
				tracks = append(tracks, tr)
			}
		}
		info.Tracks = tracks
		h, p := showPicker(t, info, PartAll, false)
		a := p.audio[0]
		eq(t, "mode", a.Mode(), "static")
		eq(t, "static", a.StaticText(), "English original · AAC 129 kbit/s")
		h.Click(p.QualitySection().Card("18").Base())
		eq(t, "static", a.StaticText(), "This video quality includes its sound.")
	})
	t.Run("renders only the cards in the video part", func(t *testing.T) {
		_, p := showPicker(t, formattest.SiteMediaInfo(nil), PartVideo, false)
		eq(t, "cards", len(p.QualitySection().Cards()), 4)
		if len(p.audio) != 0 {
			t.Error("an audio part")
		}
	})
	t.Run("says when a stream offers nothing to choose", func(t *testing.T) {
		h, _ := showPicker(t, formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.Tracks, m.SelectedVideo, m.SelectedAudio = nil, "", "" }), PartAll, false)
		if !hasText(h.Root.Base(), NoQualitiesText) {
			t.Error("no hint")
		}
		eq(t, "text", NoQualitiesText, "The stream offers no qualities to choose from.")
	})
}

func TestMediaPickerTexts(t *testing.T) {
	video := func(id, codecs string, height int32) *downexv1.MediaTrack {
		return &downexv1.MediaTrack{Id: id, Kind: "video", Width: int32(math.Round(float64(height) * 16 / 9)), Height: height, Codecs: codecs, Bandwidth: 2_000_000}
	}
	t.Run("names the codecs that are smaller than H.264, and says nothing without a mix", func(t *testing.T) {
		eq(t, "AV1", CodecNote([]*downexv1.MediaTrack{video("a", "avc1.640028", 1080), video("b", "av01.0.08M.08", 2160)}), "AV1 is smaller; H.264 plays everywhere.")
		eq(t, "AV1 and HEVC", CodecNote([]*downexv1.MediaTrack{video("a", "avc1.640028", 1080), video("b", "hvc1.1.6.L120", 1440), video("c", "av01.0.08M.08", 2160)}), "AV1 and HEVC are smaller; H.264 plays everywhere.")
		eq(t, "all H.264", CodecNote([]*downexv1.MediaTrack{video("a", "avc1.640028", 1080), video("b", "avc1.4d401f", 720)}), "")
		eq(t, "no H.264", CodecNote([]*downexv1.MediaTrack{video("a", "av01.0.08M.08", 1080)}), "")
		// and the picker shows it
		_, p := showPicker(t, formattest.MediaInfo(func(m *downexv1.MediaInfo) {
			m.Tracks = []*downexv1.MediaTrack{video("a", "avc1.640028", 1080), video("b", "av01.0.08M.08", 2160)}
			m.SelectedVideo, m.SelectedAudio = "a", ""
		}), PartAll, false)
		eq(t, "shown", p.QualitySection().note.Text(), "AV1 is smaller; H.264 plays everywhere.")
	})
	t.Run(`sizes a card by the file it makes, "~" when estimated, else its bitrate`, func(t *testing.T) {
		sizeOf := func(info *downexv1.MediaInfo, id string) string {
			_, p := showPicker(t, info, PartAll, false)
			return p.QualitySection().Card(id).Size()
		}
		// a page's declared sizes (the video with its audio), the site's estimate with "~"
		eq(t, "299", sizeOf(formattest.SiteMediaInfo(nil), "299"), "482 MB")
		eq(t, "397", sizeOf(formattest.SiteMediaInfo(nil), "397"), "~168 MB")
		// a manifest's: from the bandwidth and the length (5.128 Mbit/s × 10 min)
		eq(t, "v1", sizeOf(formattest.MediaInfo(nil), "v1"), "~385 MB")
		// nothing to estimate from: the bitrate
		eq(t, "v1 no length", sizeOf(formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.DurationMs = 0 }), "v1"), "5.0 Mbit/s")
	})
	t.Run("names audio tracks with their language and what they are", func(t *testing.T) {
		fr := &downexv1.MediaTrack{Id: "f", Kind: "audio", Language: "fr", Bandwidth: 64_000, Codecs: "opus"}
		bare := &downexv1.MediaTrack{Id: "x", Kind: "audio"}
		site := formattest.SiteMediaInfo(nil)
		site.Tracks = append(site.Tracks, fr, bare)
		_, p := showPicker(t, site, PartAudio, false)
		a := p.audio[0].Select
		eq(t, "labels", optionLabels(a), []string{"English original", "Deutsch (German)", "French", "Audio"})
		eq(t, "details", optionDetails(a), []string{"AAC 129 kbit/s", "AAC 129 kbit/s", "Opus 64 kbit/s", ""})
		// a manifest's: the rate and the raw codecs
		_, hp := showPicker(t, formattest.MediaInfo(nil), PartAudio, false)
		eq(t, "raw", optionDetails(hp.audio[0].Select), []string{"128 kbit/s · mp4a.40.2", "96 kbit/s · mp4a.40.2"})
	})
}

// hasText reports a shown text in e.
func hasText(e *kit.Element, s string) bool {
	if e.Style.Hidden {
		return false
	}
	if l, ok := e.Widget().(interface{ Text() string }); ok && l.Text() == s {
		return true
	}
	for _, k := range e.Children() {
		if hasText(k, s) {
			return true
		}
	}
	return false
}

// A video listed without audio (a silent page, SITE-MEDIA-SPEC §5.3 amendment 2026-11-18): its
// card's name and tooltip say it has no sound, and so does the audio line.
func TestMediaPickerNoSound(t *testing.T) {
	info := formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) {
		var keep []*downexv1.MediaTrack
		for _, tr := range m.GetTracks() {
			if tr.GetKind() == "video" {
				keep = append(keep, tr)
			}
		}
		m.Tracks, m.SelectedVideo, m.SelectedAudio = keep, keep[0].GetId(), ""
	})
	_, p := showPicker(t, info, PartAll, false)
	card := p.QualitySection().Card("299")
	eq(t, "name", card.Semantics.Name, "1080p (Full HD) 60fps · H.264 · No sound · 472 MB")
	eq(t, "tip", card.Tooltip, "1080p (Full HD) 60fps · 1920×1080 · No sound")
	eq(t, "audio part", p.audio[0].Mode(), "static")
	eq(t, "audio line", p.audio[0].StaticText(), "This video has no sound.")
}
