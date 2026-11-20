package mediajob

import (
	"errors"
	"fmt"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/remux"
)

// Parts of a stream (clips, docs/MEDIA-SPEC.md §8). A Run with Selection.Clip fetches
// only the window of media fragments that overlap the part (C4), the initialization data
// that applies to them, and, for a separate audio track, its fragments from the start of
// the video's first kept fragment (B7). When the assembler finds no keyframe at or before
// the part's start in the video's first fragments (remux.ErrNoKeyframe), the window is
// extended back by one more media fragment, at most maxLeadIn times and never before the
// first (C5); the lead-in is stored in the checkpoint before the new fragment is fetched,
// so a resume computes the same window (C12). Fragment files keep their index in the full
// plan (C11), so a lead-in keeps the fragments already fetched.

// maxLeadIn bounds the lead-in extensions of a part's window (C5).
const maxLeadIn = 4

// clipWindow is the window of one track of a clip run.
type clipWindow struct {
	media.PlanWindow
	// span is the length of the window in the full plan: the checkpoint's Total, its
	// bitmap counting full-plan indexes from First (C11).
	span int
}

// clipRun is the clip state of a Run.
type clipRun struct {
	clip   media.Clip
	leadIn int
	// local: the video plan has fragments without durations, so every fragment is
	// fetched and the part is cut from the whole stream (C4, C9).
	local bool
	// from is the plan time of the video's first kept fragment (the window's origin,
	// docs/MEDIA-SPEC.md §8 C3); fetchEnd the end of its last.
	from, fetchEnd time.Duration
	// atStart: the video window begins with the plan's first media fragment.
	atStart bool
	full    []*media.Plan // the tracks' full plans, in res.tracks order
}

// newClipRun keeps the full plans of res for windowing; nil without a clip.
func newClipRun(res *resolved, clip media.Clip, leadIn int) *clipRun {
	if clip.IsZero() {
		return nil
	}
	c := &clipRun{clip: clip, leadIn: min(max(leadIn, 0), maxLeadIn)}
	for _, t := range res.tracks {
		c.full = append(c.full, t.plan)
	}
	return c
}

// window replaces the plans of res's tracks with their windows for the current lead-in
// and returns the windows (nil entries for a local cut). The video (or muxed) track
// comes first; an audio track follows the video's window (B7).
func (c *clipRun) window(res *resolved) ([]*clipWindow, error) {
	wins := make([]*clipWindow, len(res.tracks))
	c.local = false
	for k, t := range res.tracks {
		full := c.full[k]
		if c.local {
			t.plan = full
			continue
		}
		var sub *media.Plan
		var w media.PlanWindow
		var err error
		if k == 0 {
			sub, w, err = full.Window(c.clip, c.leadIn)
		} else {
			sub, w, err = full.WindowFrom(c.from, c.clip.End)
		}
		switch {
		case errors.Is(err, media.ErrUnsupported) && k == 0:
			// No fragment times: the whole stream, cut locally.
			c.local, c.from, c.fetchEnd, c.atStart = true, 0, 0, true
			t.plan = full
			continue
		case errors.Is(err, media.ErrUnsupported):
			// An audio plan without times: all of it.
			t.plan = full
			continue
		case err != nil:
			return nil, fmt.Errorf("mediajob: track %q: %w", t.id, err)
		}
		if k == 0 {
			c.from, c.atStart = w.From, w.AtStart
			last := sub.Fragments[len(sub.Fragments)-1]
			c.fetchEnd = last.Start + last.Duration
		}
		t.plan = sub
		wins[k] = &clipWindow{PlanWindow: w, span: sub.Fragments[len(sub.Fragments)-1].FullIndex - w.First + 1}
	}
	if c.local {
		for k := range wins {
			wins[k] = nil
		}
		for k, t := range res.tracks {
			t.plan = c.full[k]
		}
	}
	return wins, nil
}

// remuxWindow is the window the assembler cuts (remux.Window, OriginFirst: the inputs
// begin with the window's first fragment, whose plan time is from).
func (c *clipRun) remuxWindow() remux.Window {
	w := remux.Window{Start: c.clip.Start - c.from, Origin: remux.OriginFirst, Late: c.local || c.atStart || c.leadIn >= maxLeadIn}
	if c.clip.End > 0 {
		w.End = c.clip.End - c.from
	}
	if w.Start <= 0 && w.End <= 0 {
		// The window starts at the part's start and runs to the end: still a cut, so
		// that the edit list is exact; a start of one tick keeps the window non-zero.
		w.Start = time.Nanosecond
	}
	return w
}

// extendable reports whether the window can be extended back by one more fragment.
func (c *clipRun) extendable() bool {
	return !c.local && !c.atStart && c.leadIn < maxLeadIn
}

// result describes the assembled part for the caller (docs/MEDIA-SPEC.md §8 C20).
func (c *clipRun) result(r remux.Result, total time.Duration) *model.MediaClip {
	out := &model.MediaClip{
		StartMs: c.clip.Start.Milliseconds(), EndMs: c.clip.End.Milliseconds(),
		KeyframeMs:   (r.Keyframe + c.from).Milliseconds(),
		FetchStartMs: c.from.Milliseconds(), FetchEndMs: c.fetchEnd.Milliseconds(),
	}
	if c.local {
		// Every fragment was fetched (a plan without fragment times). FetchedWhole is a site
		// resource's mark (C9: "this site's file has no index"); a stream says it by its span.
		out.FetchStartMs, out.FetchEndMs = 0, total.Milliseconds()
	}
	return out
}
