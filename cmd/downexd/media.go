package main

import (
	"context"

	"downex/internal/daemon"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/remux"
	"downex/internal/sitemedia"
	"downex/internal/transport"
)

// mediaJobs runs the daemon's streaming-media downloads with internal/mediajob
// (docs/MEDIA-SPEC.md §6 steps 3–5), which assembles them with internal/remux: every
// download is one MP4, MPEG-TS converted (docs/VIDEO-DETECT-SPEC.md §3.3). The daemon's
// job-facing types mirror mediajob's field for field, so they convert directly; a field
// added on one side only breaks this file's compilation. Host rules cap media jobs too
// (docs/SITE-MEDIA-SPEC.md §3.3: connections, speed and request length per host).
type mediaJobs struct {
	// assemble, checkTS and concatTS replace remux.AssembleWindow, remux.CheckTS and
	// remux.ConcatTS in the jobs (tests); nil keeps them.
	assemble mediajob.AssembleFunc
	checkTS  func(prefix []byte, k remux.Kind) error
	concatTS func(ctx context.Context, in remux.Input, outPath string) error
}

var _ daemon.MediaJobs = mediaJobs{}

// Probe lists the tracks of sel's manifest that are saved as an MP4, best first, with
// the default pick or the tracks sel names: mediajob.ProbeMP4 with the offer of the site
// fallback, sitemedia.ManifestTracks, for every client; a video sel names that the
// one-per-class rule leaves out is listed in place of its class's
// (sitemedia.ManifestTracksFor), so that it can be chosen.
func (mediaJobs) Probe(ctx context.Context, tf transport.Factory, sel daemon.MediaSelection) (*daemon.MediaProbe, error) {
	info, err := mediajob.ProbeMP4(ctx, tf, mediajob.Selection(sel), sitemedia.ManifestTracksFor(sel.VideoTrack, sel.VideoKey))
	if err != nil {
		return nil, err
	}
	p := daemon.MediaProbe(*info)
	return &p, nil
}

// Run downloads sel as an MP4 (mediajob.Options.MP4Only), or, for a download stored as
// MPEG-TS before 2026-10-23 that resumes from its checkpoint (o.LegacyTS), as the
// byte-for-byte .ts join it began as.
func (j mediaJobs) Run(ctx context.Context, o daemon.MediaRunOptions, sel daemon.MediaSelection, resume *model.MediaCheckpoint,
	onProgress func(daemon.MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	opts := mediajob.Options{
		Factory: o.Factory, Limiter: o.Limiter, MaxConnections: o.MaxConnections, PartDir: o.PartDir, OutPath: o.OutPath,
		HostRules: o.HostRules, MP4Only: !o.LegacyTS, LegacyTS: o.LegacyTS, ReplaceExisting: o.ReplaceExisting,
		Assemble: j.assemble, CheckTS: j.checkTS, ConcatTS: j.concatTS,
	}
	var progress func(mediajob.Progress)
	if onProgress != nil {
		progress = func(p mediajob.Progress) { onProgress(daemon.MediaProgress(p)) }
	}
	var checkpoint func(mediajob.Checkpoint) error
	if onCheckpoint != nil {
		checkpoint = func(c mediajob.Checkpoint) error { return onCheckpoint(modelCheckpoint(c)) }
	}
	return mediajob.Run(ctx, opts, mediajob.Selection(sel), jobCheckpoint(resume), progress, checkpoint)
}

// jobCheckpoint converts a stored checkpoint (nil: none) for mediajob.Run. The bitmaps
// and chunk prefixes are copied.
func jobCheckpoint(c *model.MediaCheckpoint) *mediajob.Checkpoint {
	if c == nil {
		return nil
	}
	c = c.Clone()
	out := &mediajob.Checkpoint{DurableBytes: c.DurableBytes, BytesTotal: c.BytesTotal, LeadIn: c.LeadIn}
	if c.Tracks != nil {
		out.Tracks = make([]mediajob.TrackCheckpoint, len(c.Tracks))
		for i, t := range c.Tracks {
			out.Tracks[i] = mediajob.TrackCheckpoint(t)
		}
	}
	return out
}

// modelCheckpoint converts a job's checkpoint for the store. The bitmaps and chunk
// prefixes are copied: the job keeps updating its own.
func modelCheckpoint(c mediajob.Checkpoint) model.MediaCheckpoint {
	out := model.MediaCheckpoint{DurableBytes: c.DurableBytes, BytesTotal: c.BytesTotal, LeadIn: c.LeadIn}
	if c.Tracks != nil {
		out.Tracks = make([]model.MediaTrackCheckpoint, len(c.Tracks))
		for i, t := range c.Tracks {
			out.Tracks[i] = model.MediaTrackCheckpoint(t)
		}
	}
	return *out.Clone()
}
