package add

import (
	"strconv"
	"time"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/media"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// clipBesideCards is the most quality cards under which Confirm puts the section in their
// column (two rows of cards); with more it goes in the fields' column, the shorter one then.
const clipBesideCards = 6

// ClipSection is the "Save only part of the video" section of Confirm and Add → Video
// (docs/MEDIA-SPEC.md §8, B1–B3; DESIGN-SYSTEM §3.19 picker companion): a check box with its
// hint; checked, a row of the Chapter select (when the video has chapters), From and To, the
// note, and one line of error. It holds a media.ClipForm; OnChange hears every change.
type ClipSection struct {
	kit.Element
	info *downexv1.MediaInfo
	form media.ClipForm
	off  bool
	// shown: errors are shown (after a first submit, or once a field was left).
	shown bool

	// OnChange hears every change of the form. Enter in From or To is the dialog's (it
	// submits, as in every other field).
	OnChange func()

	Check   *widgets.Checkbox
	hint    *views.Text
	body    *kit.Element
	Chapter *widgets.Select
	From    *widgets.Input
	To      *widgets.Input
	note    *views.Text
	errText *views.Text
	options []media.ChapterOption
}

// NewClipSection makes the section of info's video, with a part already chosen (nil: none).
func NewClipSection(info *downexv1.MediaInfo, chosen *downexv1.MediaClip) *ClipSection {
	c := &ClipSection{info: info, options: media.ChapterOptions(info)}
	if chosen != nil {
		c.form.On = true
		c.form.From = imedia.FormatClipTime(msDuration(chosen.GetStartMs()))
		if chosen.GetEndMs() > 0 {
			c.form.To = imedia.FormatClipTime(msDuration(chosen.GetEndMs()))
		}
		for _, o := range c.options {
			if o.From == c.form.From && (o.To == c.form.To || c.form.To == "" && o.To == media.DurationText(info)) {
				c.form.Chapter = o.Number
			}
		}
	}
	c.Semantics.Role = kit.RoleGroup
	c.Semantics.Name = media.ClipGroup
	c.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}

	c.Check = widgets.NewCheckbox(media.ClipCheckbox, c.form.On, func(on bool) {
		c.form.On = on
		c.update()
		c.changed()
	})
	c.Check.Box.Semantics.Description = media.ClipHint
	c.Check.Tooltip = media.ClipHint
	c.Add(c.Check)

	c.body = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}}
	row := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: tokens.Sp4, MinWidth: kit.Px(0), AlignItems: kit.AlignEnd}}
	if len(c.options) > 0 {
		opts := []widgets.SelectOption{{Value: "0", Label: media.ClipCustom}}
		for _, o := range c.options {
			opts = append(opts, widgets.SelectOption{Value: strconv.Itoa(o.Number), Label: text.EscapeBidi(o.Label)})
		}
		c.Chapter = widgets.NewSelect(opts, strconv.Itoa(c.form.Chapter), widgets.SelectOptions{Label: media.ClipChapter}, c.chooseChapter)
		f := views.NewField(media.ClipChapter, c.Chapter)
		f.Style.Flex = kit.FlexOf(2, 1, kit.Pct(0))
		f.Style.MinWidth = kit.Px(0)
		row.Add(f)
	}
	c.From = widgets.NewInput(c.form.From, widgets.InputOptions{Name: media.ClipFrom, Placeholder: "0:00"}, func(s string) {
		c.form.From = s
		c.custom()
	})
	c.From.Semantics.Description = media.ClipTimeHint
	ph := media.DurationText(info)
	c.To = widgets.NewInput(c.form.To, widgets.InputOptions{Name: media.ClipTo, Placeholder: ph}, func(s string) {
		c.form.To = s
		c.custom()
	})
	c.To.Semantics.Description = media.ClipTimeHint
	from := views.NewField(media.ClipFrom, c.From)
	to := views.NewField(media.ClipTo, c.To)
	for _, f := range []*views.Field{from, to} {
		f.Style.Flex = kit.Flex1
		f.Style.MinWidth = kit.Px(0)
	}
	row.Add(from, to)
	c.errText = views.NewErrorText("")
	c.errText.Semantics.Live = kit.LiveAssertive
	c.note = views.NewHint(media.ClipNote)
	// Unchecked, the section is its check box alone (one row, so that the dialogs keep
	// their fit); checked, the hint, the times, the error and the note.
	c.hint = views.NewHint(media.ClipHint)
	c.body.Add(c.hint, row, c.errText, c.note)
	c.Add(c.body)
	c.update()
	return c
}

func msDuration(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

// Form is the section's form.
func (c *ClipSection) Form() media.ClipForm { return c.form }

// Clip is the part chosen (the zero Clip for none) and its errors, for a video of the
// section's duration.
func (c *ClipSection) Clip() (imedia.Clip, media.ClipErrors) {
	return c.form.Parse(c.info.GetDurationMs())
}

// Request is the part for the request (nil: the whole video) and whether the form has
// errors.
func (c *ClipSection) Request() (*downexv1.MediaClip, bool) {
	clip, errs := c.Clip()
	if errs.Any() {
		return nil, false
	}
	chapter := ""
	if c.form.Chapter > 0 && c.form.Chapter <= len(c.options) {
		chapter = c.info.GetChapters()[c.form.Chapter-1].GetTitle()
	}
	return media.Request(clip, chapter), true
}

// ErrorText is the error shown ("" for none, or while errors are not shown yet).
func (c *ClipSection) ErrorText() string { return c.errText.Text() }

// ShowErrors shows the errors from now on (a submit).
func (c *ClipSection) ShowErrors() {
	c.shown = true
	c.update()
}

// FirstInvalid is the field to focus when the form has an error (nil for none).
func (c *ClipSection) FirstInvalid() kit.Widget {
	_, errs := c.Clip()
	switch {
	case errs.From != "":
		return c.From
	case errs.To != "" || errs.Range != "":
		return c.To
	}
	return nil
}

// SetDisabled turns the section off while the dialog is busy.
func (c *ClipSection) SetDisabled(off bool) {
	if off == c.off {
		return
	}
	c.off = off
	c.update()
}

func (c *ClipSection) chooseChapter(v string) {
	n, _ := strconv.Atoi(v)
	c.form.Chapter = n
	if n > 0 && n <= len(c.options) {
		o := c.options[n-1]
		c.form.From, c.form.To = o.From, o.To
		c.From.SetText(o.From)
		c.To.SetText(o.To)
	}
	c.update()
	c.changed()
}

// custom: an edited time sets the select back to "Custom times".
func (c *ClipSection) custom() {
	if c.form.Chapter != 0 {
		c.form.Chapter = 0
		if c.Chapter != nil {
			c.Chapter.SetValue("0")
		}
	}
	c.update()
	c.changed()
}

func (c *ClipSection) changed() {
	if c.OnChange != nil {
		c.OnChange()
	}
}

func (c *ClipSection) update() {
	c.body.SetHidden(!c.form.On)
	c.Check.SetDisabled(c.off)
	c.From.SetDisabled(c.off)
	c.To.SetDisabled(c.off)
	if c.Chapter != nil {
		c.Chapter.SetDisabled(c.off)
	}
	_, errs := c.Clip()
	msg := ""
	if c.shown || errs.Range != "" {
		msg = errs.Text()
	}
	setError(c.errText, msg)
	c.From.SetInvalid(c.shown && errs.From != "")
	c.To.SetInvalid((c.shown && errs.To != "") || errs.Range != "")
	c.From.Semantics.Description = describe(media.ClipTimeHint, pick(errs.From != "" && c.shown, errs.From, ""))
	c.To.Semantics.Description = describe(media.ClipTimeHint, pick(errs.To != "" && c.shown || errs.Range != "", errs.Text(), ""))
	c.Relayout()
}
