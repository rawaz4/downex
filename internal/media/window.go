package media

import (
	"fmt"
	"time"
)

// PlanWindow describes the window Plan.Window cut from a plan (docs/MEDIA-SPEC.md §8
// C4/C5).
type PlanWindow struct {
	// First is the full-plan index of the window's first fragment (its initialization
	// data when it has some).
	First int
	// From is the plan time (Fragment.Start) of the window's first media fragment: the
	// presentation time that the earliest sample of the window is taken to have when
	// times are anchored to it (docs/MEDIA-SPEC.md §8 C3).
	From time.Duration
	// Exact is false when a discontinuity lies between the plan's first media fragment
	// and the window's (its plan times may not match presentation times).
	Exact bool
	// AtStart: the window begins with the plan's first media fragment, so no lead-in
	// can extend it further back.
	AtStart bool
	// LeadIn is the number of media fragments the window was extended back by.
	LeadIn int
}

// Window returns the sub-plan of p that a clip needs (docs/MEDIA-SPEC.md §8 C4/C5): the
// media fragments f with f.Start < c.End (any, for an open end) and f.Start+f.Duration >
// c.Start, extended back by leadIn more media fragments (never before the first), with
// the initialization data that applies to the first of them and every initialization
// entry between them (a map change). Each fragment keeps its Start and its FullIndex
// (its index in p); Index counts the sub-plan. The zero Clip returns p's fragments
// whole.
//
// A plan with a media fragment of unknown duration (0) cannot be windowed:
// ErrUnsupported (the caller downloads every fragment and cuts the part locally, C9). A
// clip that starts at or after the end of the plan's media is a *ClipError.
func (p *Plan) Window(c Clip, leadIn int) (*Plan, PlanWindow, error) {
	if p.SingleResource {
		return nil, PlanWindow{}, errf(ErrUnsupported, "a single-resource plan has no fragment times")
	}
	var media []int // full indexes of the media fragments
	for i := range p.Fragments {
		f := &p.Fragments[i]
		if f.Init {
			continue
		}
		if f.Duration <= 0 {
			return nil, PlanWindow{}, errf(ErrUnsupported, "fragment %d has no duration, so a part cannot be chosen by time", i+1)
		}
		media = append(media, i)
	}
	if len(media) == 0 {
		return nil, PlanWindow{}, errf(ErrNotFound, "the plan has no media fragments")
	}
	lo, hi := -1, -1 // positions in media
	for k, i := range media {
		f := &p.Fragments[i]
		if (c.End == 0 || f.Start < c.End) && f.Start+f.Duration > c.Start {
			if lo < 0 {
				lo = k
			}
			hi = k
		}
	}
	if lo < 0 {
		return nil, PlanWindow{}, &ClipError{Reason: fmt.Sprintf(ClipReasonPastEnd, FormatClipTime(p.Duration))}
	}
	w := PlanWindow{}
	if leadIn > 0 {
		back := min(leadIn, lo)
		lo -= back
		w.LeadIn = back
	}
	w.AtStart = lo == 0
	firstMedia := media[lo]
	// The initialization data that applies: the last entry before the first media
	// fragment.
	first := firstMedia
	for i := firstMedia - 1; i >= 0; i-- {
		if p.Fragments[i].Init {
			first = i
			break
		}
	}
	last := media[hi]
	w.First, w.From, w.Exact = first, p.Fragments[firstMedia].Start, true
	for i := media[0] + 1; i <= firstMedia; i++ {
		if p.Fragments[i].Discontinuity {
			w.Exact = false
		}
	}
	out := &Plan{Container: p.Container}
	for i := first; i <= last; i++ {
		f := p.Fragments[i]
		if i > first && i < firstMedia {
			continue // media before the window, or an earlier map
		}
		f.Index = len(out.Fragments)
		out.Fragments = append(out.Fragments, f)
		out.Duration = addDuration(out.Duration, f.Duration)
	}
	return out, w, nil
}

// WindowFrom returns the fragments of p that overlap [from, end) (end 0 = to the end),
// without lead-in: the audio track of a clip, from the start of the video's first kept
// fragment (docs/MEDIA-SPEC.md §8 B7).
func (p *Plan) WindowFrom(from, end time.Duration) (*Plan, PlanWindow, error) {
	if from <= 0 && end <= 0 {
		// The zero Clip would mean the whole plan, which is what [0, end) is too.
		from = 0
	}
	return p.Window(Clip{Start: from, End: end}, 0)
}
