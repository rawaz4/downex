package add

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// Pages with several videos (docs/SITE-MEDIA-SPEC.md §3.12, docs/DESKTOP-SPEC.md §1 and §4):
// the item row of Add → Video and Confirm download.

// itemsInfo is a carousel's answer for item id: the site's tracks, its own title, three items
// (two with the same title) and two other items left out.
func itemsInfo(page, id string) *downexv1.MediaInfo {
	return formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) {
		m.PageUrl = page
		m.Site = "Instagram"
		m.EntryId = id
		m.ItemsLeftOut = 2
		m.Items = []*downexv1.MediaItem{
			{Id: "itemA", Title: "Video by example", DurationMs: 12_000, Thumbnail: "https://img.example/a.jpg"},
			{Id: "itemB", Title: "Video by example", DurationMs: 7_500},
			{Id: "itemC", Title: "‮Third"},
		}
		for _, it := range m.Items {
			if it.GetId() == id {
				m.Title = it.GetTitle()
			}
		}
		if id == "itemB" {
			m.SelectedVideo = "136"
		}
	})
}

func itemsProbe(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	id := req.GetEntryId()
	switch id {
	case "":
		id = "itemA"
	case "gone":
		return nil, connect.NewError(connect.CodeFailedPrecondition, errorString("site_unavailable: this video is not available: this item is no longer on the page"))
	}
	return itemsInfo(req.GetPageUrl(), id), nil
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestAddDialogItems(t *testing.T) {
	const page = "https://www.example.com/p/POST1/"
	r := setupAdd(t, rigOptions{probe: itemsProbe, submitMedia: siteAdd})
	r.enterPage(t, page)
	r.primary()
	r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
	row := r.a.Items
	if row == nil {
		t.Fatal("no item row")
	}
	// above the quality cards, in their column
	if kids := r.a.qualityArea.Children(); len(kids) == 0 || kids[0] != row.Base() {
		t.Error("the item row is not first in the qualities' column")
	}
	eq(t, "count", row.Count.Text(), "Video 1 of 3")
	eq(t, "note", row.Note.Text(), "2 other items not listed: photos, links or videos Downex cannot save.")
	eq(t, "values", optionValues(row.Select), []string{"itemA", "itemB", "itemC"})
	eq(t, "labels", optionLabels(row.Select), []string{"Video 1", "Video 2", "Video 3"})
	eq(t, "value", row.Select.Value(), "itemA")
	// the window never loads the page's images: no thumbnail anywhere in the row
	if strings.Contains(dialogTextOf(row.Base()), "img.example") {
		t.Error("a thumbnail address is shown")
	}

	choose(t, r.h, row.Select, "itemB")
	r.settle(t, "item B", func() bool { return r.a.Info().GetEntryId() == "itemB" && r.a.busy == idle })
	eq(t, "probe", r.probe.last().GetEntryId(), "itemB")
	eq(t, "count", r.a.Items.Count.Text(), "Video 2 of 3")
	eq(t, "checked", r.checked(), "136")
	r.primary()
	r.settle(t, "added", func() bool { return r.media.n() > 0 })
	eq(t, "add's item", r.media.last().GetEntryId(), "itemB")
	eq(t, "add's video", r.media.last().GetVideoTrack(), "136")
}

func TestAddDialogItemGone(t *testing.T) {
	const page = "https://www.example.com/p/POST1/"
	r := setupAdd(t, rigOptions{probe: itemsProbe, submitMedia: siteAdd})
	r.enterPage(t, page)
	r.primary()
	r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
	r.a.Items.Select.Options = append(r.a.Items.Select.Options, r.a.Items.Select.Options[0])
	r.a.Items.Select.Options[3].Value = "gone"
	choose(t, r.h, r.a.Items.Select, "gone")
	r.settle(t, "the failure", func() bool { return r.a.busy == idle && r.a.streamFailure != "" })
	// the video shown stays, and the select shows it again
	eq(t, "item", r.a.Info().GetEntryId(), "itemA")
	eq(t, "value", r.a.Items.Select.Value(), "itemA")
}

func TestConfirmDialogItems(t *testing.T) {
	d := siteConfirm(func(d *downexv1.Download) {
		d.Media = itemsInfo(formattest.Page, "itemC")
		d.Filename = "Third.mp4"
	})
	var probes calls[*downexv1.ProbeMediaRequest]
	r := setupConfirm(t, confirmSetup{d: d})
	r.c.o.ProbeMedia = func(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
		probes.add(req)
		return itemsProbe(ctx, req)
	}
	row := r.c.Items
	if row == nil {
		t.Fatal("no item row")
	}
	eq(t, "count", row.Count.Text(), "Video 3 of 3")
	eq(t, "value", row.Select.Value(), "itemC")
	// the title is shown escaped (bidi controls are never live)
	if lbl := row.Select.Options[2].Detail; strings.ContainsRune(lbl, '‮') {
		t.Errorf("unescaped title %q", lbl)
	}

	choose(t, r.h, row.Select, "itemB")
	waitUntil(t, r.h, "item B", func() bool { return r.c.info.GetEntryId() == "itemB" && r.c.busy == "" })
	eq(t, "probe", probes.last().GetEntryId(), "itemB")
	eq(t, "probe page", probes.last().GetPageUrl(), formattest.Page)
	eq(t, "count", r.c.Items.Count.Text(), "Video 2 of 3")
	eq(t, "title", r.c.Video.Title.Text(), "Video by example")
	// a proposed name follows the video (another item has its title: its position is added)
	eq(t, "name", r.c.Name.Text(), "Video by example 2.mp4")
	eq(t, "checked", r.qs().Checked().ID(), "136")
	r.start()
	r.waitConfirm(t)
	got := r.confirmed()
	eq(t, "entry", got.GetEntryId(), "itemB")
	eq(t, "video", got.GetVideoTrack(), "136")
	// the proposed name is downexd's to make
	eq(t, "filename", got.GetFilename(), "")
}

// TestConfirmDialogItemNames: the name proposed for another video follows downexd's rule (a title
// with "/" or past the name limit is sanitised and shortened), so the dialog never refuses its own
// proposal, and Start download sends the item.
func TestConfirmDialogItemNames(t *testing.T) {
	long := strings.Repeat("Long news title ", 20) // 320 characters; a title may have 512
	for name, c := range map[string]struct{ title, want string }{
		"slash": {"24/7 coverage", "24_7 coverage.mp4"},
		"long":  {long, strings.Repeat("Long news title ", 14)[:223] + ".mp4"},
	} {
		t.Run(name, func(t *testing.T) {
			mk := func(page, id string) *downexv1.MediaInfo {
				m := itemsInfo(page, id)
				m.Items[2].Title = c.title
				if id == "itemC" {
					m.Title = c.title
				}
				return m
			}
			d := siteConfirm(func(d *downexv1.Download) {
				d.Media = mk(formattest.Page, "itemA")
				d.Filename = "Video by example 1.mp4"
			})
			r := setupConfirm(t, confirmSetup{d: d})
			r.c.o.ProbeMedia = func(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
				return mk(req.GetPageUrl(), req.GetEntryId()), nil
			}
			choose(t, r.h, r.c.Items.Select, "itemC")
			waitUntil(t, r.h, "item C", func() bool { return r.c.info.GetEntryId() == "itemC" && r.c.busy == "" })
			eq(t, "name", r.c.Name.Text(), c.want)
			eq(t, "name error", r.c.errors().filename, "")
			r.start()
			r.waitConfirm(t)
			got := r.confirmed()
			eq(t, "entry", got.GetEntryId(), "itemC")
			eq(t, "filename", got.GetFilename(), "")
		})
	}
}

func TestConfirmDialogItemFailure(t *testing.T) {
	d := siteConfirm(func(d *downexv1.Download) { d.Media = itemsInfo(formattest.Page, "itemA") })
	r := setupConfirm(t, confirmSetup{d: d})
	r.c.o.ProbeMedia = itemsProbe
	r.c.Items.Select.Options[2].Value = "gone"
	choose(t, r.h, r.c.Items.Select, "gone")
	waitUntil(t, r.h, "the failure", func() bool { return r.c.busy == "" && r.c.failure != "" })
	if !strings.Contains(r.c.failure, "no longer on the page") {
		t.Errorf("failure %q", r.c.failure)
	}
	eq(t, "item", r.c.info.GetEntryId(), "itemA")
	eq(t, "value", r.c.Items.Select.Value(), "itemA")
	// Start download still sends the proposed video, without an item change
	r.start()
	r.waitConfirm(t)
	eq(t, "entry", r.confirmed().GetEntryId(), "")
}

func TestConfirmDialogNoItemsForOneVideo(t *testing.T) {
	r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
	if r.c.Items != nil {
		t.Error("an item row for a page with one video")
	}
}

// dialogTextOf is the text and tooltips of e's shown widgets.
func dialogTextOf(e *kit.Element) string {
	var b strings.Builder
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		if w, ok := e.Widget().(interface{ Text() string }); ok {
			b.WriteString(w.Text() + " ")
		}
		b.WriteString(e.Tooltip + " ")
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(e)
	return b.String()
}

// TestItemDialogsFit: the item row keeps Confirm download inside the window, without scrolling,
// for a carousel with four qualities: the strict sizes at 16 and 20 px text (the fit rule), and the
// small windows (960×608, 900×600) at 16 px text, where the same dialog of one video fits too; at
// 20 px a small window may scroll (allowFit), in one region with the footer in view. The row sits
// in the qualities' column, above the quality cards.
func TestItemDialogsFit(t *testing.T) {
	for _, sz := range []struct {
		w, h  int
		scale float32
		font  int
	}{
		{1200, 760, 1, 16}, {1190, 752, 1, 16}, {1200, 760, 1, 20}, {1920, 1080, 1, 16},
		{960, 608, 1, 16}, {960, 608, 1.25, 16}, {900, 600, 1, 16}, {900, 600, 1.25, 16}, {900, 600, 1, 20},
	} {
		t.Run(fmt.Sprintf("%dx%d@%v@%d", sz.w, sz.h, sz.scale, sz.font), func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{NoStart: true, Width: sz.w, Height: sz.h, Scale: sz.scale, Font: sz.font})
			d := siteConfirm(func(d *downexv1.Download) { d.Media = itemsInfo(formattest.Page, "itemB") })
			c := NewConfirm(h.App, d, ConfirmOptions{Settings: confirmSettings(), Requested: true, Queues: []*downexv1.Queue{}})
			h.App.Dialogs.Show(c)
			h.Settle()
			if p := fitProblems(h, c, "confirm-items", sz.w, sz.h, sz.scale, sz.font); len(p) > 0 {
				t.Errorf("Confirm: %v", p)
			}
			if kids := c.qualityArea.Children(); len(kids) == 0 || kids[0] != c.Items.Base() {
				t.Error("the item row is not first in the qualities' column")
			}
		})
	}
}
