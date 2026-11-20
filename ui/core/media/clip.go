package media

import (
	"fmt"
	"strconv"
	"time"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Parts of a video (clips, docs/MEDIA-SPEC.md §8; DESKTOP-SPEC Confirm and Add → Video): the
// texts and rules of the "Save only part of the video" section, and how a saved part is shown.
// Times are parsed by internal/media's ParseClipTime, the one rule of the window, the CLI and
// the daemon.

// The section's texts (docs/SITE-MEDIA-SPEC.md §5.3, B1–B3).
const (
	ClipGroup     = "Part of the video"
	ClipCheckbox  = "Save only part of the video"
	ClipHint      = "Only the needed parts are downloaded."
	ClipNote      = "Saved without re-encoding. Playback starts at From; a few players start up to a few seconds earlier, at the keyframe before it."
	ClipChapter   = "Chapter"
	ClipCustom    = "Custom times"
	ClipFrom      = "From"
	ClipTo        = "To"
	ClipTimeHint  = "Time, like 1:20"
	ClipBadTime   = "Enter a time like 1:20 or 1:02:03."
	ClipOrder     = "From must be before To."
	ClipShort     = "The part must be at least 1 second long."
	ClipPastEndFn = "To is after the end of the video (%s)."
	ClipWholeLine = "Downloaded in full: this site's file has no index, so the part was cut from the whole file."
	ClipPartLabel = "Part"
)

// ClipForm is what the section holds: the two times as typed and the chosen chapter (0 =
// custom times, else its 1-based number).
type ClipForm struct {
	On       bool
	From, To string
	Chapter  int
}

// ClipErrors are the section's errors, one line each ("" = none).
type ClipErrors struct{ From, To, Range string }

// Any reports an error (the Start button is disabled while one is shown).
func (e ClipErrors) Any() bool { return e.From != "" || e.To != "" || e.Range != "" }

// Text is the one line the section shows (the first error).
func (e ClipErrors) Text() string {
	switch {
	case e.From != "":
		return e.From
	case e.To != "":
		return e.To
	}
	return e.Range
}

// Parse checks the form for a video of durationMs milliseconds (0 = unknown) by B3: an empty
// From is 0, an empty To the end; a To within 1 s past the end is the end. It returns the part
// (the zero Clip for none, or for the whole video) and the errors.
func (f ClipForm) Parse(durationMs int64) (imedia.Clip, ClipErrors) {
	var errs ClipErrors
	if !f.On {
		return imedia.Clip{}, errs
	}
	var c imedia.Clip
	if s := jsTrim(f.From); s != "" {
		d, err := imedia.ParseClipTime(s)
		if err != nil {
			errs.From = ClipBadTime
		}
		c.Start = d
	}
	if s := jsTrim(f.To); s != "" {
		d, err := imedia.ParseClipTime(s)
		if err != nil {
			errs.To = ClipBadTime
		}
		c.End = d
	}
	if errs.From != "" || errs.To != "" {
		return imedia.Clip{}, errs
	}
	dur := time.Duration(durationMs) * time.Millisecond
	switch {
	case dur > 0 && c.End > dur+time.Second:
		errs.Range = fmt.Sprintf(ClipPastEndFn, imedia.FormatClipTime(dur))
	case c.End > 0 && c.Start >= c.End:
		errs.Range = ClipOrder
	case c.End > 0 && c.End-c.Start < imedia.MinClipLen:
		errs.Range = ClipShort
	case c.End == 0 && dur > 0 && c.Start >= dur:
		errs.Range = ClipOrder
	case c.End == 0 && dur > 0 && dur-c.Start < imedia.MinClipLen:
		errs.Range = ClipShort
	}
	if errs.Any() {
		return imedia.Clip{}, errs
	}
	return c.Normalize(dur), errs
}

// Request is the part for AddMediaRequest/ConfirmRequest (nil for the whole video); chapter
// is the chosen chapter's title ("" for custom times).
func Request(c imedia.Clip, chapter string) *downexv1.MediaClip {
	if c.IsZero() {
		return nil
	}
	return &downexv1.MediaClip{StartMs: c.Start.Milliseconds(), EndMs: c.End.Milliseconds(), Chapter: chapter}
}

// ChapterOption is one option of the Chapter select.
type ChapterOption struct {
	Number   int // 1-based
	Label    string
	From, To string // the times it fills in
}

// ChapterOptions labels a video's chapters: "1. Intro (0:00–1:20)".
func ChapterOptions(info *downexv1.MediaInfo) []ChapterOption {
	var out []ChapterOption
	for i, c := range info.GetChapters() {
		from := imedia.FormatClipTime(time.Duration(c.GetStartMs()) * time.Millisecond)
		to := imedia.FormatClipTime(time.Duration(c.GetEndMs()) * time.Millisecond)
		title := jsTrim(c.GetTitle())
		if title == "" {
			title = "Chapter " + strconv.Itoa(i+1)
		}
		out = append(out, ChapterOption{Number: i + 1, Label: strconv.Itoa(i+1) + ". " + title + " (" + from + "–" + to + ")", From: from, To: to})
	}
	return out
}

// DurationText is the To field's placeholder: the video's duration ("12:34"), "" when unknown.
func DurationText(info *downexv1.MediaInfo) string {
	if ms := info.GetDurationMs(); ms > 0 {
		return imedia.FormatClipTime(time.Duration(ms) * time.Millisecond)
	}
	return ""
}

// ClipSummary is the Add footer's "part 1:20 – 2:45".
func ClipSummary(c imedia.Clip, durationMs int64) string {
	if c.IsZero() {
		return ""
	}
	return "part " + imedia.FormatClipRange(c, time.Duration(durationMs)*time.Millisecond)
}

// ClipSize scales a pick's size to the part (B4): the size × (To − From + 5 s, rounded up to
// a second) ÷ the duration, always an estimate ("~"). A size without a duration stays.
func ClipSize(size PickSize, c imedia.Clip, durationMs int64) PickSize {
	if c.IsZero() || size.Bytes <= 0 || durationMs <= 0 {
		return size
	}
	end := c.End.Milliseconds()
	if end <= 0 || end > durationMs {
		end = durationMs
	}
	span := (end - c.Start.Milliseconds() + 5000 + 999) / 1000 * 1000
	if span >= durationMs || span <= 0 {
		return size
	}
	return PickSize{Bytes: max(int64(jsRound(float64(size.Bytes)*float64(span)/float64(durationMs))), 1), Kind: SizeApprox}
}

// ClipTrackSizeText is a track's declared size (TrackSizeText) scaled to the part (ClipSize,
// "~"): what the part takes of that track. Without a part, TrackSizeText; "" when unknown.
func ClipTrackSizeText(t *downexv1.MediaTrack, c imedia.Clip, durationMs int64) string {
	if c.IsZero() || t.GetSize() <= 0 {
		return TrackSizeText(t)
	}
	kind := SizeExact
	if t.GetSizeApprox() {
		kind = SizeApprox
	}
	return PickSizeText(ClipSize(PickSize{Bytes: t.GetSize(), Kind: kind}, c, durationMs))
}

// ClipAudioTrackLabel is AudioTrackLabel with a web page's size the part's (ClipTrackSizeText).
func ClipAudioTrackLabel(t *downexv1.MediaTrack, site bool, c imedia.Clip, durationMs int64) string {
	return audioTrackLabel(t, site, func(t *downexv1.MediaTrack) string { return ClipTrackSizeText(t, c, durationMs) })
}

// InfoClip is the part a media download saves (the zero Clip for the whole video).
func InfoClip(info *downexv1.MediaInfo) imedia.Clip {
	c := info.GetClip()
	if c == nil {
		return imedia.Clip{}
	}
	return imedia.Clip{Start: time.Duration(c.GetStartMs()) * time.Millisecond, End: time.Duration(c.GetEndMs()) * time.Millisecond}
}

// The length's words beside a part's (the browser panel's "1:25 selected").
const (
	lengthOf       = " of "
	lengthSelected = " selected"
)

// LengthText is the video's length as its card's chip and the details pane show it: the
// whole video's ("31:54"), or with a part the part's and the whole's ("8:28 of 31:54"; "8:28
// selected" while the whole is unknown). The part's span keeps tenths ("8:27.5"), like the
// browser panel's "N selected" and From/To; name is how it is read ("8:28 selected of 31:54").
// "" when unknown.
func LengthText(c imedia.Clip, durationMs int64) (shown, name string) {
	whole := ""
	if durationMs > 0 {
		whole = formatDuration(float64(durationMs))
	}
	end := c.End.Milliseconds()
	if end <= 0 || durationMs > 0 && end > durationMs {
		end = durationMs
	}
	if c.IsZero() || end <= c.Start.Milliseconds() {
		return whole, whole
	}
	// The part's span as the browser panel's "N selected" and From/To show it, tenths kept.
	part := imedia.FormatClipTime(time.Duration(end-c.Start.Milliseconds()) * time.Millisecond)
	if whole == "" {
		return part + lengthSelected, part + lengthSelected
	}
	return part + lengthOf + whole, part + lengthSelected + lengthOf + whole
}

// clipDone reports a part whose download completed (downexd then records what it became).
func clipDone(c *downexv1.MediaClip) bool {
	return c.GetKeyframeMs() > 0 || c.GetFetchEndMs() > 0 || c.GetFetchedWhole()
}

// PartFact is the complete view's "Part" fact (B6): "1:20 – 2:45", or the visible start
// when the first keyframe came after the part's start ("1:23 – 2:45 (first keyframe after
// 1:20)"). "" without a part.
func PartFact(info *downexv1.MediaInfo) string {
	c := info.GetClip()
	if c == nil {
		return ""
	}
	dur := time.Duration(info.GetDurationMs()) * time.Millisecond
	clip := imedia.Clip{Start: time.Duration(c.GetStartMs()) * time.Millisecond, End: time.Duration(c.GetEndMs()) * time.Millisecond}
	if clipDone(c) && c.GetKeyframeMs() > c.GetStartMs() {
		late := imedia.Clip{Start: time.Duration(c.GetKeyframeMs()) * time.Millisecond, End: clip.End}
		return imedia.FormatClipRange(late, dur) + " (first keyframe after " + imedia.FormatClipTime(clip.Start) + ")"
	}
	return imedia.FormatClipRange(clip, dur)
}

// PartText is the details pane's and the info line's "Part: 1:20 – 2:45 (keyframe 1:17.4)";
// "" without a part.
func PartText(info *downexv1.MediaInfo) string {
	fact := PartFact(info)
	if fact == "" {
		return ""
	}
	c := info.GetClip()
	if clipDone(c) && c.GetKeyframeMs() <= c.GetStartMs() {
		fact += " (keyframe " + imedia.FormatClipTime(time.Duration(c.GetKeyframeMs())*time.Millisecond) + ")"
	}
	return "Part: " + fact
}

// KeyframeTip is the Part fact's tooltip: "Decoding starts at the keyframe at 1:17.4; players
// that ignore edit lists start there." ("" until complete, or when the start is late).
func KeyframeTip(info *downexv1.MediaInfo) string {
	c := info.GetClip()
	if !clipDone(c) || c.GetKeyframeMs() > c.GetStartMs() {
		return ""
	}
	return "Decoding starts at the keyframe at " + imedia.FormatClipTime(time.Duration(c.GetKeyframeMs())*time.Millisecond) +
		"; players that ignore edit lists start there."
}

// FetchedWholeText is B6's line for a part cut from the whole file ("" otherwise).
func FetchedWholeText(info *downexv1.MediaInfo) string {
	if info.GetClip().GetFetchedWhole() {
		return ClipWholeLine
	}
	return ""
}

// ClipName is the name a part gives a download (internal/media.ClipName, C19): for the name
// field's placeholder and the Add stream name preview.
func ClipName(name string, c imedia.Clip) string { return imedia.ClipName(name, c) }

// PartValue is the Part fact as the complete view and the details pane show it: PartFact,
// and for a part cut from the whole file B6's line after it. tip is its tooltip.
func PartValue(info *downexv1.MediaInfo) (value, tip string) {
	value = PartFact(info)
	if value == "" {
		return "", ""
	}
	tip = KeyframeTip(info)
	if w := FetchedWholeText(info); w != "" {
		value += ". " + w
	}
	return value, tip
}
