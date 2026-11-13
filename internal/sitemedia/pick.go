package sitemedia

import (
	"fmt"
	"slices"
	"time"

	"downex/internal/media"
)

// Pick is a selection of an offer ready to download: the chosen formats as ranged
// tracks and how they become the output file (docs/SITE-MEDIA-SPEC.md §2.4, §3.4).
type Pick struct {
	// Tracks are the video or muxed track first, then the audio track; 1 or 2. Their
	// headers are clones. Size is set only when the site gave an exact size (an
	// estimate leaves 0, which makes the job ask the server first). ChunkBytes is still
	// 0: the caller applies §2.4 with the format's Format.ChunkSize
	// (mediajob.ChunkSize).
	Tracks []media.RangedTrack
	// Output is OutputRemux or OutputCopy; Container is always ContainerMP4.
	Output, Container string
	// NotBefore is the latest available_at of the chosen formats: no request before it.
	NotBefore time.Time
}

// Select picks tracks of an offer made from info by Offers (docs/SITE-MEDIA-SPEC.md
// §2.6, §3.11). A key finds its track wherever the page now lists it; an ID without a
// key selects only within the resolution that listed it (a pick made right after a
// probe); neither selects the default. Both also find a usable MP4 video that the
// one-per-class rule left out (a key of an earlier offer whose class now offers another
// format, or an ID the user typed); Listed then shows it. A video-only track is paired
// with the chosen audio, else with the best one; a muxed track takes no audio.
//
// A track or key the offer no longer has, and an offer that is not FormatHTTP (the
// page now offers only a manifest), are media.ErrNotFound: the caller resolves once
// more and then fails with media_invalid, never falling back to another quality. An
// audio track for a muxed video, or a track of the wrong kind, is media.ErrMalformed.
func Select(info *Info, off *Offer, video, audio, videoKey, audioKey string) (*Pick, error) {
	switch {
	case info == nil || off == nil:
		return nil, fmt.Errorf("%w: no offer to select from", media.ErrMalformed)
	case off.Format != FormatHTTP:
		return nil, fmt.Errorf("%w: the page now offers a stream manifest instead of the chosen formats", media.ErrNotFound)
	}
	v, err := find(off, video, videoKey, false)
	if err != nil {
		return nil, err
	}
	if v == nil {
		if v = trackByID(off, off.DefaultVideo); v == nil {
			return nil, fmt.Errorf("%w: the page offers no video", media.ErrNotFound)
		}
	}
	a, err := find(off, audio, audioKey, true)
	if err != nil {
		return nil, err
	}
	pick := &Pick{Container: ContainerMP4}
	chosen := []*media.Track{v}
	switch {
	case v.Kind == media.KindMuxed && a != nil:
		return nil, fmt.Errorf("%w: an audio track cannot be added to a muxed video track", media.ErrMalformed)
	case v.Kind == media.KindMuxed:
		pick.Output = OutputCopy
	default:
		if a == nil {
			a = bestAudio(off)
		}
		if a == nil {
			return nil, fmt.Errorf("%w: the page offers no audio for the chosen video", media.ErrNotFound)
		}
		chosen = append(chosen, a)
		pick.Output = OutputRemux
	}
	for _, t := range chosen {
		f := info.Format(t.ID)
		if f == nil || f.Key != t.Key {
			return nil, fmt.Errorf("%w: format %s is no longer offered", media.ErrNotFound, t.ID)
		}
		if f.Kind == media.KindMuxed && f.fragmented() {
			pick.Output = OutputRemux
		}
		pick.Tracks = append(pick.Tracks, media.RangedTrack{
			ID:       f.ID,
			Key:      f.Key,
			Kind:     f.Kind,
			Ext:      f.Ext,
			URL:      f.URL,
			Headers:  f.Headers.Clone(),
			Size:     f.Size,
			Expires:  f.Expires,
			Network:  f.Network,
			Unranged: f.Unranged,
		})
		if f.AvailableAt.After(pick.NotBefore) {
			pick.NotBefore = f.AvailableAt
		}
	}
	return pick, nil
}

// find returns the track that key (else id) names among the offered tracks and the
// videos the class rule left out; nil when both are "". It must be an audio track when
// audio is set, a video or muxed track otherwise.
func find(off *Offer, id, key string, audio bool) (*media.Track, error) {
	var t *media.Track
	switch {
	case key != "":
		t = trackBy(off, func(t *media.Track) bool { return t.Key == key })
		if t == nil || (id != "" && t.ID != id) {
			return nil, fmt.Errorf("%w: the chosen quality is no longer offered", media.ErrNotFound)
		}
	case id != "":
		if t = trackByID(off, id); t == nil {
			return nil, fmt.Errorf("%w: track %q is not offered", media.ErrNotFound, id)
		}
	default:
		return nil, nil
	}
	if (t.Kind == media.KindAudio) != audio {
		if audio {
			return nil, fmt.Errorf("%w: track %q is not an audio track", media.ErrMalformed, t.ID)
		}
		return nil, fmt.Errorf("%w: track %q is not a video track", media.ErrMalformed, t.ID)
	}
	return t, nil
}

// trackByID returns the offered track with id (or the video the class rule left out
// with it), or nil.
func trackByID(off *Offer, id string) *media.Track {
	if id == "" {
		return nil
	}
	return trackBy(off, func(t *media.Track) bool { return t.ID == id })
}

// trackBy returns the first offered track that match accepts, else the first video the
// class rule left out that it accepts, or nil.
func trackBy(off *Offer, match func(*media.Track) bool) *media.Track {
	for _, ts := range [...][]media.Track{off.Tracks, off.more} {
		for i := range ts {
			if match(&ts[i]) {
				return &ts[i]
			}
		}
	}
	return nil
}

// Listed is the tracks a probe of off lists when p was picked from it: off.Tracks, and
// when p's video is one the class rule left out (Select found it by key or ID), that
// video in place of the offered video of its class (a video without a picture that the
// cap left out: in place of the last offered video), so that the pick is among the
// tracks shown and the list still has one video per class. off.Tracks is not changed.
func (off *Offer) Listed(p *Pick) []media.Track {
	if p == nil || len(p.Tracks) == 0 || slices.ContainsFunc(off.Tracks, func(t media.Track) bool { return t.ID == p.Tracks[0].ID }) {
		return off.Tracks
	}
	i := slices.IndexFunc(off.more, func(t media.Track) bool { return t.ID == p.Tracks[0].ID })
	if i < 0 {
		return off.Tracks
	}
	class := off.class[off.more[i].ID]
	video := func(t media.Track) bool { return t.Kind != media.KindAudio }
	at := slices.IndexFunc(off.Tracks, func(t media.Track) bool { return video(t) && class > 0 && off.class[t.ID] == class })
	if class == 0 {
		at = slices.IndexFunc(off.Tracks, func(t media.Track) bool { return !video(t) }) - 1
		if at < -1 {
			at = len(off.Tracks) - 1
		}
	}
	out := slices.Clone(off.Tracks)
	if at < 0 {
		// No offered video to stand in for (never with Offers' lists): after the videos
		// of its class or better.
		at = 0
		for at < len(out) && video(out[at]) && off.class[out[at].ID] >= class {
			at++
		}
		return slices.Insert(out, at, off.more[i])
	}
	out[at] = off.more[i]
	return out
}

// bestAudio returns the first (best) audio track of the offer, or nil.
func bestAudio(off *Offer) *media.Track {
	for i := range off.Tracks {
		if off.Tracks[i].Kind == media.KindAudio {
			return &off.Tracks[i]
		}
	}
	return nil
}
