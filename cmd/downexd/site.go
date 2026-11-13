package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"downex/internal/daemon"
	"downex/internal/engine"
	"downex/internal/media"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/sitejob"
	"downex/internal/sitemedia"
	"downex/internal/transport"
)

// Timings of site runs (docs/SITE-MEDIA-SPEC.md §2.6).
const (
	// siteRetryDelay is the wait before a run resolves the page once more, past the
	// cache, when a chosen track is missing.
	siteRetryDelay = 5 * time.Second
	// siteMinValid is how long the chosen formats' known URL expiry must still be ahead
	// for a cached resolution to start a run.
	siteMinValid = 30 * time.Minute
)

// errTrackGone refuses a run whose chosen quality the page no longer offers: a track
// key it no longer has, or another kind of format than the one recorded
// (docs/SITE-MEDIA-SPEC.md §2.6, §9 #9). Downex never falls back to another quality.
var errTrackGone = fmt.Errorf("%w: %w: the chosen quality is no longer offered", mediajob.ErrInvalidSelection,
	media.ErrNotFound)

// errNoMP4 refuses a manifest fallback that is not saved as an MP4 with a video.
var errNoMP4 = fmt.Errorf("%w: no MP4 video on this page", media.ErrUnsupported)

// pageResolver resolves web pages with yt-dlp (*sitejob.Resolver).
type pageResolver interface {
	Resolve(ctx context.Context, req sitejob.Request) (*sitemedia.Info, error)
}

// siteMedia downloads the videos of web pages for the daemon (daemon.SiteMedia,
// docs/SITE-MEDIA-SPEC.md §2): internal/sitejob resolves the page with yt-dlp,
// internal/sitemedia turns what it says into the MP4 offer and the chosen tracks, and
// internal/mediajob downloads them, direct formats with RunRanged and the manifest
// fallback with Run. It is the only place the three meet.
type siteMedia struct {
	res        pageResolver
	retryDelay time.Duration    // siteRetryDelay
	now        func() time.Time // the clock of the expiry check
	// freeSpace returns the bytes available on the volume of a folder; RunRanged then
	// refuses a download the volume has no room for (docs/SITE-MEDIA-SPEC.md §9 #10).
	// nil skips that check.
	freeSpace func(dir string) (int64, error)
}

var _ daemon.SiteMedia = (*siteMedia)(nil)

// newSiteMedia returns the daemon's site media over res, the resolver it shares with
// nothing else (its cache serves the probes and the runs of the same pages).
func newSiteMedia(res pageResolver) *siteMedia {
	return &siteMedia{res: res, retryDelay: siteRetryDelay, now: time.Now}
}

// Probe resolves the page of sel (a cached extraction when one qualifies) and describes
// what it offers: for direct formats one MP4 video per quality class, best first, and
// the default pick, or the tracks sel names (sitemedia.Select; a video the class rule
// left out is then listed in place of its class's, sitemedia.Offer.Listed); for the
// manifest fallback the probed manifest's tracks that are saved as an MP4
// (mediajob.ProbeMP4), best first (sitemedia.ManifestTracksFor: one video per class, a
// named one the rule leaves out in place of its class's), and the first video with its
// audio, or the tracks sel names among them. The resolver's errors pass as they are: at ctx's end an extraction
// still running is ErrSiteResolving. A look-ahead (sel.Prefetch, docs/SITE-MEDIA-SPEC.md
// §5.6) asks the resolver as one (sitejob.Request.Prefetch: never queued, ErrBusy
// instead) and is otherwise a probe, except that it fetches nothing of a manifest's
// media (mediajob.ProbeMP4Playlists): it is made without the user asking.
func (s *siteMedia) Probe(ctx context.Context, tf transport.Factory, sel daemon.SiteSelection) (*daemon.SiteProbe, error) {
	info, err := s.res.Resolve(ctx, pageRequest(sel, false))
	if err != nil {
		return nil, err
	}
	off, err := sitemedia.Offers(info, pageHost(sel.PageURL))
	if err != nil {
		return nil, err
	}
	p := &daemon.SiteProbe{Site: info.Site, Title: info.Title}
	if off.Format != sitemedia.FormatHTTP {
		probe := mediajob.ProbeMP4
		if sel.Prefetch {
			probe = mediajob.ProbeMP4Playlists
		}
		mi, err := probe(ctx, tf, manifestSelection(off, sel), sitemedia.ManifestTracksFor(sel.VideoTrack, sel.VideoKey))
		if err != nil {
			return nil, err
		}
		if err := checkManifestMP4(mi); err != nil {
			return nil, err
		}
		p.MediaProbe = daemon.MediaProbe(*mi)
		return p, nil
	}
	pick, err := sitemedia.Select(info, off, sel.VideoTrack, sel.AudioTrack, sel.VideoKey, sel.AudioKey)
	if err != nil {
		return nil, err
	}
	p.MediaProbe = daemon.MediaProbe{Format: off.Format, Tracks: off.Listed(pick), DefaultVideo: pick.Tracks[0].ID,
		Duration: info.Duration, Container: off.Container}
	if len(pick.Tracks) > 1 {
		p.DefaultAudio = pick.Tracks[1].ID
	}
	return p, nil
}

// Run resolves the page again (docs/SITE-MEDIA-SPEC.md §2.6), finds the chosen tracks
// by key and downloads them: direct formats with mediajob.RunRanged, each in chunks of
// its §2.4 size and renewed through Refresh when its address expires; the manifest
// fallback with mediajob.Run on the fresh manifest URL. A chosen track or format kind
// the page no longer has makes it resolve once more, past the cache, after
// siteRetryDelay, and then fails (media_invalid). Cancelling ctx (a pause) stops any
// step with ctx's error.
func (s *siteMedia) Run(ctx context.Context, o daemon.MediaRunOptions, sel daemon.SiteSelection, resume *model.MediaCheckpoint,
	onProgress func(daemon.MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	opts := mediajob.Options{
		Factory: o.Factory, Limiter: o.Limiter, MaxConnections: o.MaxConnections, PartDir: o.PartDir, OutPath: o.OutPath,
		HostRules: o.HostRules, FreeSpace: s.freeSpace, ReplaceExisting: o.ReplaceExisting,
	}
	var progress func(mediajob.Progress)
	if onProgress != nil {
		progress = func(p mediajob.Progress) { onProgress(daemon.MediaProgress(p)) }
	}
	var checkpoint func(mediajob.Checkpoint) error
	if onCheckpoint != nil {
		checkpoint = func(c mediajob.Checkpoint) error { return onCheckpoint(modelCheckpoint(c)) }
	}
	page, err := s.resolveRun(ctx, sel)
	if err != nil {
		return "", ctxError(ctx, err)
	}
	if page.off.Format != sitemedia.FormatHTTP {
		return s.runManifest(ctx, opts, sel, page, resume, progress, checkpoint)
	}
	tracks := rangedTracks(page, o.HostRules, resume, o.MaxConnections)
	rsel := mediajob.RangedSelection{Tracks: tracks, Output: page.pick.Output, NotBefore: page.pick.NotBefore,
		Refresh: s.refresher(sel, tracks)}
	container, err := mediajob.RunRanged(ctx, opts, rsel, jobCheckpoint(resume), progress, checkpoint)
	return container, ctxError(ctx, err)
}

// sitePage is a page resolved for a run: its offer and, for direct formats, the
// chosen tracks.
type sitePage struct {
	info *sitemedia.Info
	off  *sitemedia.Offer
	pick *sitemedia.Pick // FormatHTTP only
}

// resolveRun resolves the page of sel for a run (docs/SITE-MEDIA-SPEC.md §2.6). A cached
// resolution serves only when every chosen URL whose expiry is known stays valid for
// siteMinValid more; otherwise the page is resolved past the cache. A chosen track or
// format kind that is missing gets one more resolution past the cache after
// retryDelay.
func (s *siteMedia) resolveRun(ctx context.Context, sel daemon.SiteSelection) (*sitePage, error) {
	fresh, retried := false, false
	for {
		page, err := s.resolvePage(ctx, sel, fresh)
		switch {
		case err == nil && !fresh && page.pick != nil && expiresBefore(page.pick, s.now().Add(siteMinValid)):
			fresh = true
			continue
		case err == nil:
			return page, nil
		case retried || !errors.Is(err, errTrackGone):
			return nil, err
		}
		retried, fresh = true, true
		if err := sleepContext(ctx, s.retryDelay); err != nil {
			return nil, err
		}
	}
}

// resolvePage resolves the page of sel (past the cache with fresh) and finds the chosen
// tracks: errTrackGone when the page now offers another kind of format than sel's, or
// no longer has a chosen key.
func (s *siteMedia) resolvePage(ctx context.Context, sel daemon.SiteSelection, fresh bool) (*sitePage, error) {
	info, err := s.res.Resolve(ctx, pageRequest(sel, fresh))
	if err != nil {
		return nil, err
	}
	off, err := sitemedia.Offers(info, pageHost(sel.PageURL))
	if err != nil {
		return nil, err
	}
	if sel.Format != "" && off.Format != sel.Format {
		return nil, fmt.Errorf("%w (the page now offers %s instead of %s)", errTrackGone, off.Format, sel.Format)
	}
	page := &sitePage{info: info, off: off}
	if off.Format == sitemedia.FormatHTTP {
		page.pick, err = sitemedia.Select(info, off, sel.VideoTrack, sel.AudioTrack, sel.VideoKey, sel.AudioKey)
		switch {
		case errors.Is(err, media.ErrNotFound):
			return nil, errTrackGone
		case err != nil:
			return nil, err
		}
	}
	return page, nil
}

// runManifest downloads the manifest fallback with mediajob.Run (docs/SITE-MEDIA-SPEC.md
// §3.11), MP4 only: a chosen track that would be saved as anything else is refused
// before its fragments are fetched. When a chosen track's key is gone from the manifest,
// the page is resolved once more past the cache after retryDelay and the run tried
// again with its manifest URL.
func (s *siteMedia) runManifest(ctx context.Context, opts mediajob.Options, sel daemon.SiteSelection, page *sitePage,
	resume *model.MediaCheckpoint, progress func(mediajob.Progress), checkpoint func(mediajob.Checkpoint) error) (string, error) {
	opts.MP4Only = true
	for retried := false; ; retried = true {
		container, err := mediajob.Run(ctx, opts, manifestSelection(page.off, sel), jobCheckpoint(resume), progress, checkpoint)
		if err == nil || retried || ctx.Err() != nil || !errors.Is(err, mediajob.ErrInvalidSelection) {
			return container, ctxError(ctx, err)
		}
		if err := sleepContext(ctx, s.retryDelay); err != nil {
			return "", err
		}
		if page, err = s.resolvePage(ctx, sel, true); err != nil {
			return "", ctxError(ctx, err)
		}
	}
}

// rangedTracks returns the chosen tracks of page with their chunk size
// (docs/SITE-MEDIA-SPEC.md §2.4): the one resume's checkpoint recorded for the track's
// key, which stays for the checkpoint's life, else the smaller of the format's own
// chunk size and the host's request cap (mediajob.ChunkSize), lowered for a track of
// known size to about two chunks per connection the download may use for its host
// (mediajob.FitChunk, maxConns = its max_connections).
func rangedTracks(page *sitePage, rules engine.HostRules, resume *model.MediaCheckpoint, maxConns int) []media.RangedTrack {
	tracks := slices.Clone(page.pick.Tracks)
	for i := range tracks {
		t := &tracks[i]
		if c := checkpointChunk(resume, t.Key); c > 0 {
			t.ChunkBytes = c
			continue
		}
		var formatChunk int64
		if f := page.info.Format(t.ID); f != nil {
			formatChunk = f.ChunkSize
		}
		t.ChunkBytes = mediajob.FitChunk(rules, t.URL, mediajob.ChunkSize(rules, t.URL, formatChunk), t.Size, maxConns)
	}
	return tracks
}

// checkpointChunk returns the chunk size resume recorded for the track with key, or 0.
func checkpointChunk(resume *model.MediaCheckpoint, key string) int64 {
	if resume == nil || key == "" {
		return 0
	}
	for _, t := range resume.Tracks {
		if t.Key == key && t.ChunkBytes >= media.MinChunkBytes && t.ChunkBytes <= media.MaxChunkBytes {
			return t.ChunkBytes
		}
	}
	return 0
}

// refresher returns the RangedSelection.Refresh of a run of tracks: it resolves the page
// past the cache and returns the same tracks, found by key, with their fresh URLs,
// headers, expiry and address family, and their chunk sizes unchanged. A key that is
// gone, or a page that now offers another kind of format, wraps
// mediajob.ErrInvalidSelection.
func (s *siteMedia) refresher(sel daemon.SiteSelection, tracks []media.RangedTrack) func(context.Context) ([]media.RangedTrack, error) {
	chosen := slices.Clone(tracks)
	sel.VideoTrack, sel.VideoKey = chosen[0].ID, chosen[0].Key
	sel.AudioTrack, sel.AudioKey = "", ""
	if len(chosen) > 1 {
		sel.AudioTrack, sel.AudioKey = chosen[1].ID, chosen[1].Key
	}
	sel.Format = sitemedia.FormatHTTP
	return func(ctx context.Context) ([]media.RangedTrack, error) {
		page, err := s.resolvePage(ctx, sel, true)
		if err != nil {
			return nil, err
		}
		fresh := page.pick.Tracks
		if len(fresh) != len(chosen) {
			return nil, errTrackGone
		}
		for i := range fresh {
			fresh[i].ChunkBytes = chosen[i].ChunkBytes
		}
		return fresh, nil
	}
}

// manifestSelection is the mediajob selection of the manifest fallback: off's manifest
// with the headers of its format, and the tracks sel chose.
func manifestSelection(off *sitemedia.Offer, sel daemon.SiteSelection) mediajob.Selection {
	return mediajob.Selection{ManifestURL: off.ManifestURL, Headers: off.Headers.Clone(), VideoTrack: sel.VideoTrack,
		AudioTrack: sel.AudioTrack, VideoKey: sel.VideoKey, AudioKey: sel.AudioKey}
}

// checkManifestMP4 accepts a probed manifest fallback only when it is saved as an MP4
// and its selection has a video (docs/SITE-MEDIA-SPEC.md §3.11): audio alone is never a
// download.
func checkManifestMP4(mi *mediajob.Info) error {
	if mi.Container != sitemedia.ContainerMP4 {
		return errNoMP4
	}
	for _, t := range mi.Tracks {
		if t.ID == mi.DefaultVideo && t.Kind != media.KindAudio {
			return nil
		}
	}
	return errNoMP4
}

// pageRequest is the resolver request for sel's page (a look-ahead's when sel is one).
func pageRequest(sel daemon.SiteSelection, fresh bool) sitejob.Request {
	return sitejob.Request{PageURL: sel.PageURL, Cookie: sel.Cookie, Referer: sel.Referer, Proxy: sitejob.Proxy(sel.Proxy),
		Fresh: fresh, Prefetch: sel.Prefetch}
}

// pageHost returns the host of a page URL ("" when it does not parse).
func pageHost(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// expiresBefore reports whether a chosen track's URL has a known expiry before t.
func expiresBefore(p *sitemedia.Pick, t time.Time) bool {
	return slices.ContainsFunc(p.Tracks, func(rt media.RangedTrack) bool { return !rt.Expires.IsZero() && rt.Expires.Before(t) })
}

// ctxError returns ctx's error once ctx has ended (a pause), else err.
func ctxError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// sleepContext waits for d or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
