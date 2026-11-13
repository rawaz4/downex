package add

import (
	"math"
	"slices"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/media"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// MediaPicker.svelte's module functions.

// modernCodecs are smaller than H.264 for the same picture but need a recent player.
var modernCodecs = map[string]bool{"AV1": true, "HEVC": true, "VP9": true}

// CodecNote is the line under the cards when they mix H.264 with a newer codec
// (DESIGN-SYSTEM.md §3.19), naming the codecs present; "" when they do not mix.
func CodecNote(tracks []*downexv1.MediaTrack) string {
	names := map[string]bool{}
	for _, t := range tracks {
		first, _, _ := strings.Cut(t.GetCodecs(), ",")
		if n := media.CodecName(first); n != "" {
			names[n] = true
		}
	}
	if !names["H.264"] {
		return ""
	}
	var modern []string
	for n := range names {
		if modernCodecs[n] {
			modern = append(modern, n)
		}
	}
	if len(modern) == 0 {
		return ""
	}
	slices.Sort(modern)
	list := modern[0]
	verb := "is"
	if len(modern) > 1 {
		list = strings.Join(modern[:len(modern)-1], ", ") + " and " + modern[len(modern)-1]
		verb = "are"
	}
	return list + " " + verb + " smaller; H.264 plays everywhere."
}

// CardSize is a card's size: the file the quality makes (with the audio it is paired with),
// "~" when it is an estimate; else the bitrate; "" unknown.
func CardSize(info *downexv1.MediaInfo, pick media.TrackPick, t *downexv1.MediaTrack) string {
	size := media.PickSizeOf(info, media.WithVideo(info, pick, t.GetId()))
	switch size.Kind {
	case media.SizeExact:
		return format.Bytes(float64(size.Bytes))
	case media.SizeApprox, media.SizeEstimate:
		return "~" + format.Bytes(float64(size.Bytes))
	}
	return format.Bitrate(float64(t.GetBandwidth()))
}

// AudioDetail is what an audio track is besides its name: "AAC 129 kbit/s" (a page's),
// "128 kbit/s · mp4a.40.2" (a manifest's).
func AudioDetail(t *downexv1.MediaTrack, site bool) string {
	rate := format.Bitrate(float64(t.GetBandwidth()))
	var parts []string
	if site {
		if t.GetCodecs() != "" {
			if c := media.CodecNames(t.GetCodecs()); c != "" {
				parts = append(parts, c)
			}
		}
		if rate != "" {
			parts = append(parts, rate)
		}
		return strings.Join(parts, " ")
	}
	if rate != "" {
		parts = append(parts, rate)
	}
	if t.GetCodecs() != "" {
		parts = append(parts, t.GetCodecs())
	}
	return strings.Join(parts, " · ")
}

// QualityHint is the hint beside "Quality".
func QualityHint(videos int) string {
	if videos == 1 {
		return "The only MP4 quality offered"
	}
	return "One MP4 per resolution, best first"
}

// NoQualitiesText says that a stream offers nothing to choose.
const NoQualitiesText = "The stream offers no qualities to choose from."

// cardText is a quality card's texts (MediaPicker's cards).
type cardText struct {
	cls, suffix, codec, size, label, tip string
}

func cardTexts(info *downexv1.MediaInfo, pick media.TrackPick, t *downexv1.MediaTrack, site bool) cardText {
	name := media.QualityName(t)
	if name == "" {
		name = "Video"
		if t.GetKind() == "muxed" {
			name = "Video and audio"
		}
	}
	cls := ShortClassName(name)
	if cls == "" {
		cls = name
	}
	suffix := text.TrimJS(name[min(len(cls), len(name)):])
	if !strings.HasPrefix(name, cls) {
		suffix = ""
	}
	size := CardSize(info, pick, t)
	codec := ""
	if t.GetCodecs() != "" {
		first, _, _ := strings.Cut(t.GetCodecs(), ",")
		codec = media.CodecName(first)
	}
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		return strings.Join(keep, " · ")
	}
	rate := ""
	if !site {
		rate = format.Bitrate(float64(t.GetBandwidth()))
	}
	sound := ""
	if t.GetKind() == "muxed" {
		sound = "with its sound"
	}
	return cardText{
		cls: cls, suffix: suffix, codec: codec, size: size,
		label: join(name, media.CodecNames(t.GetCodecs()), size),
		tip:   join(name, media.VideoPictureText(t, site), rate, sound),
	}
}

// Part is which part of the picker a place shows (MediaPicker's part).
type Part uint8

// The parts.
const (
	// PartAll: the cards, then the audio track.
	PartAll Part = iota
	// PartVideo: the cards.
	PartVideo
	// PartAudio: the audio track.
	PartAudio
)

// Picker is MediaPicker.svelte (DESIGN-SYSTEM.md §3.19): one card per quality class (downexd's
// list in its order and names), a radio group the arrow keys move in, and the audio track as a
// Select, or as a line when there is no choice. One Picker holds the pick; its parts can be
// placed apart (the Add and Confirm dialogs put the cards in a column of their own): Part
// makes the widget of a part.
type Picker struct {
	Info *downexv1.MediaInfo
	site bool
	pick media.TrackPick
	off  bool

	videos, audios []*downexv1.MediaTrack
	offerNoVideo   bool
	note           string

	// OnChange hears every change of the pick.
	OnChange func(media.TrackPick)
	// OnSubmit hears Enter on a card (a radio's implicit submission).
	OnSubmit func()

	quality *qualitySection
	audio   []*audioPart
}

// NewPicker makes the picker of info with a pick (DefaultPick for a new one).
func NewPicker(info *downexv1.MediaInfo, pick media.TrackPick) *Picker {
	p := &Picker{Info: info, site: media.IsSite(info), pick: pick}
	p.videos = media.VideoTracks(info.GetTracks(), p.site, info.GetSelectedVideo())
	p.audios = media.AudioTracks(info.GetTracks(), p.site)
	p.offerNoVideo = media.AudioOnlyOffered(info)
	p.note = CodecNote(p.videos)
	return p
}

// Pick is the chosen tracks.
func (p *Picker) Pick() media.TrackPick { return p.pick }

// Videos are the cards' tracks; Audios the audio tracks offered.
func (p *Picker) Videos() []*downexv1.MediaTrack { return p.videos }
func (p *Picker) Audios() []*downexv1.MediaTrack { return p.audios }

// Note is the codec note under the cards ("" for none).
func (p *Picker) Note() string { return p.note }

// Choose picks a video track ("" = audio only), as a click on its card does.
func (p *Picker) Choose(video string) { p.set(media.WithVideo(p.Info, p.pick, video)) }

// ChooseAudio picks an audio track, as the Audio track select does.
func (p *Picker) ChooseAudio(audio string) {
	q := p.pick
	q.Audio = audio
	p.set(q)
}

func (p *Picker) set(q media.TrackPick) {
	if q == p.pick {
		return
	}
	p.pick = q
	p.update()
	if p.OnChange != nil {
		p.OnChange(q)
	}
}

// SetDisabled turns every card and the audio select off while the dialog is busy.
func (p *Picker) SetDisabled(off bool) {
	if off == p.off {
		return
	}
	p.off = off
	p.update()
}

// Disabled reports SetDisabled's.
func (p *Picker) Disabled() bool { return p.off }

func (p *Picker) videoOnly() bool { return len(p.audios) > 0 && media.NeedsAudio(p.Info, p.pick.Video) }

// showAudio: the audio is chosen only when there is a choice (SITE-MEDIA-SPEC §5.3).
func (p *Picker) showAudio() bool { return p.videoOnly() && len(p.audios) > 1 }

func (p *Picker) onlyAudio() *downexv1.MediaTrack {
	if p.videoOnly() && len(p.audios) == 1 {
		return p.audios[0]
	}
	return nil
}

func (p *Picker) muxedChosen() bool {
	return len(p.videos) > 0 && len(p.audios) > 0 && media.TrackByID(p.Info, p.pick.Video).GetKind() == "muxed"
}

func (p *Picker) update() {
	if p.quality != nil {
		p.quality.update()
	}
	for _, a := range p.audio {
		a.update()
	}
}

// Part makes the widget of a part of the picker; nil when that part shows nothing.
func (p *Picker) Part(part Part) kit.Widget {
	switch part {
	case PartVideo:
		if len(p.videos) > 0 {
			return p.qualityPart()
		}
		if len(p.audios) == 0 {
			return views.NewHint(NoQualitiesText)
		}
		return nil
	case PartAudio:
		return p.audioPart()
	}
	all := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp7, MinWidth: kit.Px(0)}}
	if len(p.videos) > 0 {
		all.Add(p.qualityPart())
	}
	all.Add(p.audioPart())
	if len(p.videos) == 0 && len(p.audios) == 0 {
		all.Add(views.NewHint(NoQualitiesText))
	}
	return all
}

// QualitySection is the cards' section once made (nil before).
func (p *Picker) QualitySection() *qualitySection { return p.quality }

// ---- the cards -------------------------------------------------------------------------------

// qualitySection is MediaPicker's .quality: "Quality" with its hint, the cards, the codec note;
// a size container of its own (its cards follow its width: 2 under 28rem, 4 from 40rem, else
// 3).
type qualitySection struct {
	kit.Element
	p     *Picker
	group *kit.Element
	cards []*qcard
	none  *qcard
	note  *views.Text
}

func (p *Picker) qualityPart() *qualitySection {
	if p.quality != nil {
		return p.quality
	}
	s := &qualitySection{p: p}
	s.Semantics.Role = kit.RoleGroup
	s.Semantics.Name = "Quality"
	s.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}
	hint := views.NewHint(QualityHint(len(p.videos)))
	row := views.NewLabelRow("Quality", hint)
	s.Add(row)

	cards := newGrid(func(w kit.DIP, sc kit.Scale) []float32 {
		switch {
		case w >= 0 && w <= sc.Rem(27.99):
			return []float32{1, 1}
		case w >= sc.Rem(40):
			return []float32{1, 1, 1, 1}
		}
		return []float32{1, 1, 1}
	}, 8, 8)
	cards.Semantics.Role = kit.RoleRadioGroup
	cards.Semantics.Name = "Video quality"
	desc := QualityHint(len(p.videos))
	if p.note != "" {
		desc += " " + p.note
	}
	cards.Semantics.Description = desc
	s.group = &cards.Element
	for _, t := range p.videos {
		c := newQCard(s, t.GetId())
		s.cards = append(s.cards, c)
		cards.Add(c)
	}
	if p.offerNoVideo {
		s.none = newQCard(s, "")
		s.cards = append(s.cards, s.none)
		cards.Add(s.none)
	}
	for i, c := range s.cards {
		c.Semantics.PosInSet, c.Semantics.SetSize = i+1, len(s.cards)
	}
	s.Add(cards)
	if p.note != "" {
		line := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MinWidth: kit.Px(0)}}
		line.Tooltip = p.note
		info := icon("info", 14, faint)
		s.note = views.NewLine(p.note, views.FontHint, views.InkFaint)
		s.note.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		line.Add(info, s.note)
		s.Add(line)
	}
	p.quality = s
	s.update()
	return s
}

// Cards are the cards in order (the audio-only one last).
func (s *qualitySection) Cards() []*qcard { return s.cards }

// Group is the radio group.
func (s *qualitySection) Group() *kit.Element { return s.group }

// Checked is the chosen card.
func (s *qualitySection) Checked() *qcard {
	for _, c := range s.cards {
		if c.on() {
			return c
		}
	}
	return nil
}

// Card is the card of a track id ("" the audio-only one).
func (s *qualitySection) Card(id string) *qcard {
	for _, c := range s.cards {
		if c.id == id {
			return c
		}
	}
	return nil
}

func (s *qualitySection) update() {
	p := s.p
	if p.off {
		s.group.Semantics.States |= kit.NodeDisabled
	} else {
		s.group.Semantics.States &^= kit.NodeDisabled
	}
	s.group.SemanticsChanged()
	checked := s.Checked()
	for _, c := range s.cards {
		c.update(checked == nil && c == s.cards[0])
	}
}

// qcard is a quality card (.qcard): a radio of the group. Two short lines (the class and its
// standard suffix; the codec and the size) in at least 2.75rem: --raised with a --line-strong
// border and 10 px corners, chosen in Mint (--accent border and inset ring, --accent-soft) with
// its tick, hovered --hover with a --line-field border, at 70% while off and not chosen.
type qcard struct {
	kit.Pressable
	s            *qualitySection
	id           string
	cls          *kit.Label
	suff         *views.Text
	codec        *cardCodec
	size         *kit.Label
	tick         *tickMark
	line1, line2 *kit.Element
}

// cardGap is .qcard's gap between its lines; tickInset the tick's inset from the padding box.
const cardGap, tickInset, tickSize = 4, 8, 16

// ContentWidths is kit.Layouter's: the wider line.
func (c *qcard) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	l1, h1 := ctx.IntrinsicWidths(c.line1)
	l2, h2 := ctx.IntrinsicWidths(c.line2)
	return max(l1, l2), max(h1, h2)
}

// ContentHeight is kit.Layouter's: the two lines and their gap.
func (c *qcard) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return c.line1Height(ctx, w) + cardGap + ctx.HeightFor(c.line2, w, w)
}

// line1Height is .line1's height: the class and its suffix aligned on their baseline, with
// each item's baseline where Blink's line layout puts it (whole device pixels, see below).
func (c *qcard) line1Height(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	if ctx.Text == nil {
		return ctx.HeightFor(c.line1, w, w)
	}
	var above, below kit.DIP
	fonts := []kit.Font{c.cls.Font()}
	if c.suff != nil {
		fonts = append(fonts, c.suff.Font())
	}
	for _, f := range fonts {
		st := f.Style(ctx.Scale)
		m, err := ctx.Text.FontMetrics(st)
		if err != nil {
			return ctx.HeightFor(c.line1, w, w)
		}
		// in whole CSS pixels, at every scale (the references lay out alike at 100 and
		// 125 %): the ascent and descent rounded, the half-leading above floored
		// (Blink's FontHeight::AddLeading)
		const px = 1.0
		asc, desc := math.Round(float64(m.Ascent)*px), math.Round(float64(m.Descent)*px)
		lh := float64(m.LineHeight) * px
		base := math.Floor((lh-(asc+desc))/2) + asc
		above, below = max(above, kit.DIP(base/px)), max(below, kit.DIP((lh-base)/px))
	}
	return above + below
}

// LayoutContent is kit.Layouter's: the lines centred in the card (align-content: center), the
// tick 8 px inside the padding box's top end corner (position: absolute).
func (c *qcard) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	h1 := c.line1Height(ctx, w)
	h2 := ctx.HeightFor(c.line2, w, w)
	y := box.Min.Y + (box.Height()-(h1+cardGap+h2))/2
	ctx.Place(c.line1, kit.R(box.Min.X, y, box.Max.X, y+h1), w)
	y += h1 + cardGap
	ctx.Place(c.line2, kit.R(box.Min.X, y, box.Max.X, y+h2), w)
	pad := kit.DIP(10) // padding-inline; padding-block is 6
	top := box.Min.Y - 6 + tickInset
	x := box.Max.X + pad - tickInset - tickSize
	if ctx.RTL {
		x = box.Min.X - pad + tickInset
	}
	ctx.Place(c.tick, kit.R(x, top, x+tickSize, top+tickSize), w)
}

func newQCard(s *qualitySection, id string) *qcard {
	c := &qcard{s: s, id: id}
	c.OnPress = func() {
		if !c.s.p.off {
			c.s.p.Choose(c.id)
		}
	}
	c.Focus = kit.FocusTab
	c.Cursor = kit.CursorPointer
	c.Semantics.Role = kit.RoleRadioButton
	c.Style = kit.Style{
		MinWidth: kit.Px(0), MinHeight: kit.Rem(2.75), Padding: kit.EdgesOf(kit.Px(6), kit.Px(10)), Border: kit.EdgesAll(kit.Px(1)),
		Radius: kit.CornersAll(tokens.RLg),
	}
	c.line1 = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(5), MinWidth: kit.Px(0), Padding: kit.Edges{End: kit.Px(16)}}}
	c.line2 = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, MinHeight: kit.Px(16)}}
	clsFont := kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeLead, Weight: 650, LineHeight: 1.2, LetterSpacing: -0.01, AutoOptical: true}
	sizeFont := views.FontHint
	sizeFont.Weight = 600
	if id == "" {
		c.cls = kit.NewLabel("Audio only", clsFont, kit.TextOptions{})
		c.suff = views.NewLine("(no video)", views.FontHint, views.InkMuted)
		c.Semantics.Name = "No video (audio only)"
	} else {
		t := media.TrackByID(s.p.Info, id)
		ct := cardTexts(s.p.Info, s.p.pick, t, s.p.site)
		c.cls = kit.NewLabel(text.EscapeBidi(ct.cls), clsFont, kit.TextOptions{Direction: kit.DirLTR})
		if ct.suffix != "" {
			c.suff = views.NewLine(text.EscapeBidi(ct.suffix), views.FontHint, views.InkMuted)
		}
		if ct.codec != "" {
			c.codec = newCardCodec(ct.codec, c)
		}
		c.Tooltip = ct.tip
	}
	c.cls.Style.Flex = kit.FlexNone
	c.cls.Semantics.Role = kit.RoleNone
	c.line1.Add(c.cls)
	if c.suff != nil {
		c.suff.Ink = c.suffInk // --muted at the card's opacity (made once: a method value allocates)
		c.suff.Semantics.Role = kit.RoleNone
		c.suff.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		c.line1.Add(c.suff)
	}
	if c.codec != nil {
		c.line2.Add(c.codec)
	}
	c.size = kit.NewLabel("", sizeFont, kit.TextOptions{})
	c.size.Semantics.Role = kit.RoleNone
	c.size.Style.Margin = kit.Edges{Start: kit.Auto}
	c.size.Style.Flex = kit.FlexNone
	c.line2.Add(c.size)
	c.Add(c.line1, c.line2)
	c.tick = &tickMark{c: c}
	c.tick.Semantics.Hidden = true
	c.tick.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Width: kit.Px(16), Height: kit.Px(16)}
	c.tick.check = icon("check", 12, func(t *tokens.Theme) kit.Color { return t.AccentOn })
	c.tick.Add(c.tick.check)
	c.Add(c.tick)
	return c
}

// ID is the card's track id ("" for "Audio only").
func (c *qcard) ID() string { return c.id }

// Class, Suffix, Codec and Size are its texts (.cls, .suffix, .codec, .size).
func (c *qcard) Class() string { return c.cls.Text() }
func (c *qcard) Suffix() string {
	if c.suff == nil {
		return ""
	}
	return c.suff.Text()
}
func (c *qcard) Codec() string {
	if c.codec == nil {
		return ""
	}
	return c.codec.Semantics.Name
}
func (c *qcard) Size() string { return c.size.Text() }

// HasTick reports the chosen card's tick.
func (c *qcard) HasTick() bool { return !c.tick.Style.Hidden }

func (c *qcard) on() bool { return c.s.p.pick.Video == c.id }

// On reports that the card is chosen.
func (c *qcard) On() bool { return c.on() }

// update follows the pick: the size, the tick, the radio state; tab is whether it takes Tab
// (the chosen card, or the first while none is).
func (c *qcard) update(first bool) {
	p := c.s.p
	if c.id == "" {
		s := media.PickSizeOf(p.Info, media.TrackPick{Audio: orStr(p.pick.Audio, p.Info.GetSelectedAudio())})
		size := ""
		switch {
		case s.Kind == media.SizeExact:
			size = format.Bytes(float64(s.Bytes))
		case s.Bytes > 0:
			size = "~" + format.Bytes(float64(s.Bytes))
		}
		c.size.SetText(size)
	} else {
		t := media.TrackByID(p.Info, c.id)
		ct := cardTexts(p.Info, p.pick, t, p.site)
		c.size.SetText(ct.size)
		c.Semantics.Name = text.EscapeBidi(ct.label)
	}
	on := c.on()
	c.tick.SetHidden(!on)
	c.Disabled = p.off
	c.Focus = kit.FocusClick
	if on || first {
		c.Focus = kit.FocusTab
	}
	c.Cursor = kit.CursorPointer
	if p.off {
		c.Cursor = kit.CursorDefault
	}
	c.Invalidate()
	c.SemanticsChanged()
}

// FillSemantics is kit.SemanticFiller's: a radio button, checked or not.
func (c *qcard) FillSemantics(n *kit.SemanticNode) {
	n.Actions |= kit.ActionSelect
	n.Toggle = kit.ToggleOff
	if c.on() {
		n.Toggle = kit.ToggleOn
		n.States |= kit.NodeSelected
	}
	n.States |= kit.NodeSelectable
}

// PerformAction is kit.ActionHandler's: Select and Invoke choose the card.
func (c *qcard) PerformAction(r kit.ActionRequest) error {
	switch r.Action {
	case kit.ActionSelect, kit.ActionInvoke:
		c.Press()
		return nil
	}
	return kit.ErrUnsupported
}

// HandleKey is kit.KeyHandler's: the arrow keys choose the next or previous card and move the
// focus there (a radio group, mirrored in a right-to-left layout), Space chooses, Enter submits
// the form.
func (c *qcard) HandleKey(e kit.KeyEvent) bool {
	if e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	if e.Action == kit.KeyDown {
		step := 0
		rtl := c.Window() != nil && c.Window().Direction() == kit.DirRTL
		switch e.Key {
		case kit.KeyArrowDown:
			step = 1
		case kit.KeyArrowUp:
			step = -1
		case kit.KeyArrowRight:
			step = 1
			if rtl {
				step = -1
			}
		case kit.KeyArrowLeft:
			step = -1
			if rtl {
				step = 1
			}
		case kit.KeyEnter:
			if e.Mods == 0 && c.s.p.OnSubmit != nil {
				c.s.p.OnSubmit()
				return true
			}
			return false
		}
		if step != 0 {
			cards := c.s.cards
			i := slices.Index(cards, c)
			n := len(cards)
			next := cards[((i+step)%n+n)%n]
			if !c.s.p.off {
				c.s.p.Choose(next.id)
			}
			if w := c.Window(); w != nil {
				w.Focus(next)
			}
			return true
		}
	}
	if e.Key == kit.KeyEnter {
		return false
	}
	return c.Pressable.HandleKey(e)
}

// colors are the card's ground, border and its inner ring.
func (c *qcard) colors(t *tokens.Theme) (ground, border kit.Color, ring bool) {
	on := c.on()
	switch {
	case on:
		return t.AccentSoft, t.Accent, true
	case c.Hovered() && !c.s.p.off:
		return t.Hover, t.LineField, false
	}
	return t.Raised, t.LineStrong, false
}

// dim is how opaque the card's content is: 0.7 while off and not chosen.
func (c *qcard) dim() float32 {
	if c.s.p.off && !c.on() {
		return 0.7
	}
	return 1
}

// ink is col at the card's opacity over the dialog's --raised.
func (c *qcard) ink(t *tokens.Theme, col kit.Color) kit.Color {
	if d := c.dim(); d < 1 {
		return kit.Mix(col, d, t.Raised)
	}
	return col
}

// Paint is kit.Painter's.
func (c *qcard) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	ground, border, ring := c.colors(t)
	if t.IsForced() {
		border = t.Ink
		if c.on() {
			border = t.Focus
		}
	}
	r := views.SnapBox(p, 10)
	w := p.Scale.SnapWidth(1)
	bw := w
	if ring {
		bw = 2 * w
	}
	if g := c.ink(t, ground); g.A < 1 {
		// a see-through ground (the dark theme's --accent-soft and --hover) lies under the
		// border too (background-clip: border-box), the border over it
		p.Canvas.FillRoundedRect(r, g)
		p.Canvas.StrokeRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(bw/2)), 10-bw/2), kit.Stroke{Width: bw}, c.ink(t, border))
	} else {
		p.Canvas.FillRoundedRect(r, c.ink(t, border))
		p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(bw)), 10-bw), g)
	}
	c.cls.SetColor(c.ink(t, t.Ink))
	c.size.SetColor(c.ink(t, t.Ink2))
}

func (c *qcard) suffInk(t *tokens.Theme) kit.Color { return c.ink(t, t.Muted) }

// HoverChanged is kit.HoverHandler's.
func (c *qcard) HoverChanged(h bool) {
	c.Pressable.HoverChanged(h)
	c.Invalidate()
}

// tickMark is the chosen card's .tick: a 16 px --accent disc with the 12 px check in
// --accent-on.
type tickMark struct {
	kit.Element
	c     *qcard
	check *widgets.Icon
}

// Paint is kit.Painter's: a 16 px --accent disc.
func (m *tickMark) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&m.Element)
	p.Canvas.FillEllipse(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), t.Accent)
}

// cardCodec is a card's .codec: views.Codec, on --raised while its card is chosen.
type cardCodec struct {
	*views.Codec
	c     *qcard
	label *kit.Label
}

func newCardCodec(s string, c *qcard) *cardCodec {
	cc := &cardCodec{Codec: views.NewCodec(s), c: c}
	for _, k := range cc.Codec.Children() {
		if l, ok := k.Widget().(*kit.Label); ok {
			cc.label = l
		}
	}
	return cc
}

// Paint is kit.Painter's: .codec, its ground --raised on a chosen card (.qcard.on .codec).
func (cc *cardCodec) Paint(p *kit.PaintContext) {
	t := views.TokensOf(cc.Base())
	ground := t.Chrome
	if cc.c.on() {
		ground = t.Raised
	}
	r := views.SnapBox(p, 4)
	p.Canvas.FillRoundedRect(r, cc.c.ink(t, t.Line))
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 4-w), cc.c.ink(t, ground))
	if cc.label != nil {
		cc.label.SetColor(cc.c.ink(t, t.Ink2))
	}
}

// ---- the audio track -------------------------------------------------------------------------

// audioPart is MediaPicker's audio part: "Audio track" over a Select of the tracks when there
// is a choice, the one track (or "This video quality includes its sound.") as a line when there
// is none, nothing otherwise.
type audioPart struct {
	kit.Element
	p      *Picker
	Select *widgets.Select
	static *staticLine
	field  *views.Field
	mode   int // 0 none, 1 select, 2 static
}

func (p *Picker) audioPart() *audioPart {
	a := &audioPart{p: p, mode: -1}
	a.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}
	p.audio = append(p.audio, a)
	a.update()
	return a
}

// AudioOptions are the Select's options.
func (p *Picker) AudioOptions() []widgets.SelectOption {
	opts := make([]widgets.SelectOption, 0, len(p.audios))
	for _, t := range p.audios {
		o := widgets.SelectOption{
			Value:  t.GetId(),
			Label:  text.EscapeBidi(media.AudioTrackTitle(t, p.site)),
			Detail: AudioDetail(t, p.site),
			Title:  text.EscapeBidi(media.AudioTrackLabel(t, p.site)),
		}
		if p.site {
			o.Meta = media.TrackSizeText(t)
		}
		opts = append(opts, o)
	}
	return opts
}

func (a *audioPart) update() {
	p := a.p
	mode := 0
	switch {
	case p.showAudio():
		mode = 1
	case p.onlyAudio() != nil || p.muxedChosen():
		mode = 2
	}
	if mode != a.mode {
		a.RemoveAll()
		a.field = nil
		switch mode {
		case 1:
			a.Select = widgets.NewSelect(p.AudioOptions(), p.pick.Audio, widgets.SelectOptions{Label: "Audio track", Icon: "headphones", Disabled: p.off}, func(v string) {
				p.ChooseAudio(v)
			})
			a.field = views.NewField("Audio track", a.Select)
		case 2:
			a.static = newStaticLine()
			a.field = views.NewField("Audio track", a.static)
		}
		if a.field != nil {
			a.Add(a.field)
		}
		a.mode = mode
		a.SetHidden(mode == 0)
	}
	switch mode {
	case 1:
		if a.Select.Value() != p.pick.Audio {
			a.Select.SetValue(p.pick.Audio)
		}
		a.Select.SetDisabled(p.off)
	case 2:
		if t := p.onlyAudio(); t != nil {
			detail := AudioDetail(t, p.site)
			if detail != "" {
				detail = " · " + detail
			}
			a.static.set(text.EscapeBidi(media.AudioTrackTitle(t, p.site)), detail, text.EscapeBidi(media.AudioTrackLabel(t, p.site)))
		} else {
			a.static.set("This video quality includes its sound.", "", "")
		}
	}
}

// Mode reports what the part shows: "select", "static" or "".
func (a *audioPart) Mode() string {
	switch a.mode {
	case 1:
		return "select"
	case 2:
		return "static"
	}
	return ""
}

// StaticText is the line's text (.static's textContent, trimmed), StaticTitle its tooltip.
func (a *audioPart) StaticText() string {
	if a.static == nil {
		return ""
	}
	return a.static.name.Text() + a.static.detail.Text()
}
func (a *audioPart) StaticTitle() string {
	if a.static == nil {
		return ""
	}
	return a.static.sound.Tooltip
}

// staticLine is MediaPicker's .static: a 36 px line on --surface-2 with a --line border and
// 9 px corners, the 16 px headphones in --faint, the track's name and its detail in --muted, cut
// with an ellipsis.
type staticLine struct {
	kit.Element
	sound  *kit.Element
	name   *views.Text
	detail *views.Text
}

func newStaticLine() *staticLine {
	s := &staticLine{}
	s.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(8), MinHeight: kit.Px(36), MinWidth: kit.Px(0),
		Padding: kit.EdgesInline(kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(9)),
	}
	s.sound = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline, Flex: kit.FlexOf(0, 1, kit.Length{})}}
	s.name = views.NewLine("", views.FontBody, views.InkInk)
	s.name.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	s.detail = views.NewLine("", views.FontBody, views.InkMuted)
	s.detail.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	s.sound.Add(s.name, s.detail)
	s.Add(icon("headphones", 16, faint), s.sound)
	return s
}

func (s *staticLine) set(name, detail, title string) {
	s.name.SetText(name)
	s.detail.SetText(detail)
	s.detail.SetHidden(detail == "")
	s.sound.Tooltip = title
}

// Paint is kit.Painter's.
func (s *staticLine) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&s.Element)
	r := views.SnapBox(p, 9)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	p.Canvas.FillRoundedRect(r, border)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 9-w), t.Surface2)
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
