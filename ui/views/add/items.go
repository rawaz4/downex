package add

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/media"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// ItemRow is the item row of a page with several videos (docs/SITE-MEDIA-SPEC.md §3.12,
// docs/DESKTOP-SPEC.md §1 and §4) in Confirm download and Add → Video: "Video" with "Video 2 of
// 3" beside it (an audio page's: "Track", media.ItemNoun), a select of the page's videos ("Video
// 2 · title · 0:12"), and the note on the
// page's other items (media.ItemsLeftOutText). The window never loads the page's thumbnails: the select names the
// videos. Choosing another one calls OnChoose with its id; the dialog then gets its qualities.
type ItemRow struct {
	kit.Element
	Count  *views.Text
	Select *widgets.Select
	Note   *views.Text
	// OnChoose hears another item chosen.
	OnChoose func(id string)
	value    string
	count    string
}

// NewItemRow makes the row of info (ShowsItems); the select is hidden with one video listed.
func NewItemRow(info *downexv1.MediaInfo) *ItemRow {
	r := &ItemRow{}
	r.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}
	r.Count = views.NewHint("")
	noun := media.ItemNoun(info)
	r.Select = widgets.NewSelect(nil, "", widgets.SelectOptions{Label: noun, Icon: "video"}, func(v string) {
		if v != r.value && r.OnChoose != nil {
			r.OnChoose(v)
		}
	})
	r.Note = views.NewHint("")
	r.Add(views.NewField("", views.NewLabelRow(noun, r.Count), r.Select, r.Note))
	r.SetInfo(info)
	return r
}

// SetInfo shows info's items and the one it describes.
func (r *ItemRow) SetInfo(info *downexv1.MediaInfo) {
	opts := make([]widgets.SelectOption, 0, len(info.GetItems()))
	for _, o := range media.ItemOptions(info) {
		opts = append(opts, widgets.SelectOption{Value: o.Value, Label: o.Label, Detail: o.Detail, Meta: o.Meta, Title: o.Detail})
	}
	r.value = info.GetEntryId()
	r.Select.SetOptions(opts)
	r.Select.SetValue(r.value)
	r.Select.SetHidden(len(opts) < 2)
	r.count = media.ItemsCountText(info)
	r.Count.SetText(r.count)
	note := media.ItemsLeftOutText(info.GetItemsLeftOut(), media.AudioPage(info))
	r.Note.SetText(note)
	r.Note.SetHidden(note == "")
	r.Select.Semantics.Description = joinDot(r.Count.Text(), note)
}

// Value is the item shown.
func (r *ItemRow) Value() string { return r.value }

// Restore shows the item shown again in the select (after another one could not be got).
func (r *ItemRow) Restore() { r.Select.SetValue(r.value) }

// SetLoading says "Getting video info…" beside "Video" while another item's qualities are got.
func (r *ItemRow) SetLoading(on bool) {
	if on {
		r.Count.SetText("Getting video info…")
		return
	}
	r.Count.SetText(r.count)
}

// SetDisabled disables the select while the dialog is busy.
func (r *ItemRow) SetDisabled(off bool) { r.Select.SetDisabled(off) }
