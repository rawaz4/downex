package add

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/media"
)

// Confirm download's "Save only part of the video" (docs/MEDIA-SPEC.md §8, SITE-MEDIA-SPEC
// §5.3/§5.4): unchecked by default, one row; checked, From and To with the B3 errors; the name
// follows the part while it is the proposed one; the request carries the part.
func TestConfirmDialogPart(t *testing.T) {
	t.Run("sends the part, names the file by it and blocks Start on an error", func(t *testing.T) {
		d := mediaDownload(nil) // 10:00
		r := setupConfirm(t, confirmSetup{d: d})
		c := r.c.Clip
		if c == nil {
			t.Fatal("no part section")
		}
		eq(t, "group", c.Semantics.Name, media.ClipGroup)
		eq(t, "check", c.Check.Box.Semantics.Name, media.ClipCheckbox)
		if c.Check.Checked() || !c.body.Style.Hidden {
			t.Fatal("the section starts open")
		}
		proposed := r.c.filename
		r.h.Click(c.Check.Box.Base())
		r.h.Settle()
		if c.body.Style.Hidden {
			t.Fatal("checked, the times are hidden")
		}
		eq(t, "to placeholder", media.DurationText(r.c.info), "10:00")
		c.From.SetText("1:20")
		c.form.From = "1:20"
		c.form.To = "1:20.5"
		c.custom()
		r.h.Settle()
		eq(t, "error", c.ErrorText(), media.ClipShort)
		if !r.c.start.Base().Disabled {
			t.Error("Start is on while an error shows")
		}
		c.form.To = "2:45"
		c.custom()
		r.h.Settle()
		eq(t, "no error", c.ErrorText(), "")
		stem := strings.TrimSuffix(proposed, ".mp4")
		eq(t, "name", r.c.filename, stem+" (01m20s-02m45s).mp4")
		if !strings.Contains(r.c.nameHint.Text(), "~") {
			t.Errorf("the size of a part is not an estimate: %q", r.c.nameHint.Text())
		}
		r.start()
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
		got := r.confirmed()
		if got.GetClip().GetStartMs() != 80_000 || got.GetClip().GetEndMs() != 165_000 || got.GetFilename() != "" {
			t.Errorf("confirm = %v", got)
		}
	})
	t.Run("a download proposed with a part sends the whole video when unchecked", func(t *testing.T) {
		d := mediaDownload(func(d *downexv1.Download) {
			d.Filename = "talk (00m10s-00m20s).mp4"
			d.Media.Clip = &downexv1.MediaClip{StartMs: 10_000, EndMs: 20_000}
		})
		r := setupConfirm(t, confirmSetup{d: d})
		c := r.c.Clip
		if !c.Check.Checked() || c.Form().From != "0:10" || c.Form().To != "0:20" {
			t.Fatalf("form %+v", c.Form())
		}
		r.h.Click(c.Check.Box.Base())
		r.h.Settle()
		eq(t, "name", r.c.filename, "talk.mp4")
		r.start()
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
		got := r.confirmed()
		if got.GetClip() == nil || got.GetClip().GetStartMs() != 0 || got.GetClip().GetEndMs() != 0 {
			t.Errorf("confirm = %v", got)
		}
	})
	t.Run("a busy dialog turns every control of the part off", func(t *testing.T) {
		c := NewClipSection(mediaDownload(nil).GetMedia(), &downexv1.MediaClip{StartMs: 10_000})
		c.SetDisabled(true)
		if !c.Check.Box.Base().Disabled || !c.From.Disabled || !c.To.Disabled {
			t.Error("a control of the part stays on while the dialog is busy")
		}
		c.SetDisabled(false)
		if c.From.Disabled || c.To.Disabled {
			t.Error("the times stay off")
		}
	})
	t.Run("a file download has no part", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		if r.c.Clip != nil {
			t.Error("a file download offers a part")
		}
	})
}

// setTimes types From and To as the fields do.
func setTimes(c *ClipSection, from, to string) {
	c.From.SetText(from)
	c.To.SetText(to)
	c.form.From, c.form.To = from, to
	c.custom()
}

func cardSizes(s *qualitySection) []string {
	var out []string
	for _, c := range s.Cards() {
		out = append(out, c.Size())
	}
	return out
}

// While a part is chosen, the quality cards, the audio tracks and the card's length are the
// part's (SITE-MEDIA-SPEC §5.3/§5.4, as the browser panel shows them): "~" sizes, "1:25 of
// 10:00"; the whole video's again when the part is off or has an error.
func TestPartSizesAndLength(t *testing.T) {
	whole := []string{"482 MB", "126 MB", "~168 MB", "~45.0 MB"}
	part := []string{"~72.4 MB", "~18.9 MB", "~25.2 MB", "~6.8 MB"} // × 90 s ÷ 600 s
	t.Run("Confirm download", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) {
			d.Media.Chapters = []*downexv1.Chapter{{StartMs: 0, EndMs: 80_000, Title: "Intro"}, {StartMs: 80_000, EndMs: 165_000, Title: "Results"}}
		})})
		c := r.c.Clip
		eq(t, "whole sizes", cardSizes(r.qs()), whole)
		eq(t, "whole facts", strings.Join(r.c.Video.Facts(), " "), "10:00 Up to 1080p Saved as MP4")
		r.h.Click(c.Check.Box.Base())
		r.h.Settle()
		eq(t, "checked, no times yet: whole", cardSizes(r.qs()), whole)
		setTimes(c, "1:20", "2:45")
		r.h.Settle()
		eq(t, "part sizes", cardSizes(r.qs()), part)
		eq(t, "chosen card and Size: agree", r.c.nameHint.Text(), "Size: "+part[0]+" · saved as MP4")
		eq(t, "card name", r.qs().Card("299").Semantics.Name, "1080p (Full HD) 60fps · H.264 · ~72.4 MB")
		eq(t, "part facts", strings.Join(r.c.Video.Facts(), " "), "1:25 of 10:00 Up to 1080p Saved as MP4")
		eq(t, "length name", r.c.Video.length.Semantics.Name, "1:25 selected of 10:00")
		if a := r.audioSelect(); a != nil && a.Select != nil {
			for _, o := range a.Select.Options {
				if !strings.HasPrefix(o.Meta, "~") {
					t.Errorf("audio %q: %q", o.Label, o.Meta)
				}
			}
		}
		r.h.Click(r.qs().Card("397").Base())
		eq(t, "another quality", r.c.nameHint.Text(), "Size: "+part[2]+" · saved as MP4")
		// an error: the whole video's again, until it is fixed
		setTimes(c, "1:20", "1:20.5")
		r.h.Settle()
		eq(t, "error sizes", cardSizes(r.qs()), whole)
		eq(t, "error facts", r.c.Video.Facts()[0], "10:00")
		// a chapter fills the times: its part
		c.chooseChapter("1")
		r.h.Settle()
		eq(t, "chapter facts", r.c.Video.Facts()[0], "1:20 of 10:00")
		if got := cardSizes(r.qs()); got[0] == whole[0] || !strings.HasPrefix(got[0], "~") {
			t.Errorf("chapter sizes %v", got)
		}
		// unchecked: the whole video
		r.h.Click(c.Check.Box.Base())
		r.h.Settle()
		eq(t, "unchecked sizes", cardSizes(r.qs()), whole)
		eq(t, "unchecked facts", r.c.Video.Facts()[0], "10:00")
		eq(t, "length name", r.c.Video.length.Semantics.Name, "10:00")
	})
	t.Run("Confirm download proposed with a part shows it from the start", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) {
			d.Media.Clip = &downexv1.MediaClip{StartMs: 80_000, EndMs: 165_000}
		})})
		eq(t, "sizes", cardSizes(r.qs()), part)
		eq(t, "facts", r.c.Video.Facts()[0], "1:25 of 10:00")
	})
	t.Run("Confirm download of a part of a video of unknown length shows the part's length", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) {
			d.Media.DurationMs = 0
			d.Media.Clip = &downexv1.MediaClip{StartMs: 80_000, EndMs: 165_000}
		})})
		eq(t, "facts", r.c.Video.Facts()[0], "1:25 selected")
		eq(t, "length name", r.c.Video.length.Semantics.Name, "1:25 selected")
		if r.c.Video.length.Style.Hidden {
			t.Error("length chip hidden")
		}
		r.h.Click(r.c.Clip.Check.Box.Base())
		r.h.Settle()
		if !r.c.Video.length.Style.Hidden {
			t.Errorf("no part, no length: chip shown %q", r.c.Video.Facts())
		}
	})
	t.Run("Add → Video", func(t *testing.T) {
		r := setupAdd(t, rigOptions{probe: siteProbe, submitMedia: siteAdd})
		r.enterPage(t, formattest.Page)
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		qs := r.a.Picker.QualitySection()
		c := r.a.Clip
		eq(t, "whole sizes", cardSizes(qs), whole)
		r.h.Click(c.Check.Box.Base())
		r.h.Settle()
		setTimes(c, "1:20", "2:45")
		r.h.Settle()
		eq(t, "part sizes", cardSizes(qs), part)
		eq(t, "facts", r.a.Video.Facts()[0], "1:25 of 10:00")
		eq(t, "footer", r.a.FooterSummary(), "1080p · H.264 + English audio · "+part[0]+" · part 1:20 – 2:45")
		setTimes(c, "1:2", "2:45")
		r.h.Settle()
		eq(t, "error sizes", cardSizes(qs), whole)
		eq(t, "error facts", r.a.Video.Facts()[0], "10:00")
	})
}
