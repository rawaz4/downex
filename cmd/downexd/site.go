package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"downex/internal/browserfetch"
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

// errNoAudio refuses an audio page's stream that is not saved as an MP4 with sound
// (docs/SITE-MEDIA-SPEC.md §3.13).
var errNoAudio = fmt.Errorf("%w: no AAC or MP3 audio on this page", media.ErrUnsupported)

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
// media (mediajob.ProbeMP4Playlists): it is made without the user asking. Of a page with
// several videos (docs/SITE-MEDIA-SPEC.md §3.12) it describes the item sel names
// (sitemedia.ChooseEntry: the page's default for "", site_unavailable for one it no longer
// has) and lists the page's items. A cached resolution whose stream's link expires
// within probeMinValid, or whose stream's server refuses its address (HTTP 401, 403 or
// 410), is read once more past the cache (not for a look-ahead); a refusal that stays
// is site_error (sitemedia.LinkRefusedReason). An audio page's stream that cannot be
// saved gives way to the page's next usable audio (sitemedia.Offer.Next).
func (s *siteMedia) Probe(ctx context.Context, tf transport.Factory, sel daemon.SiteSelection) (*daemon.SiteProbe, error) {
	p, err := s.probe(ctx, tf, sel, false)
	if errors.Is(err, errStaleLink) {
		p, err = s.probe(ctx, tf, sel, true)
	}
	return p, linkRefused(err)
}

// probeMinValid is how long a cached stream's known link expiry must still be ahead for
// a probe to use it.
const probeMinValid = time.Minute

// errStaleLink marks a probe of a cached resolution whose stream's link has expired or
// is refused (staleLink): Probe reads the page again.
var errStaleLink = errors.New("the stream's link is stale")

// probe is Probe of the page, resolved past the cache with fresh. Of a cached
// resolution (not fresh, not a look-ahead) a stream whose known link expiry is less
// than probeMinValid ahead, or whose server refuses its address, is errStaleLink.
func (s *siteMedia) probe(ctx context.Context, tf transport.Factory, sel daemon.SiteSelection, fresh bool) (*daemon.SiteProbe, error) {
	page, err := s.res.Resolve(ctx, pageRequest(sel, fresh))
	if err != nil {
		return nil, playlistError(err, sel.PageURL)
	}
	entry, err := sitemedia.ChooseEntry(page, sel.EntryID, pageHost(sel.PageURL))
	if err != nil {
		return nil, err
	}
	info := entry.Info
	off, err := sitemedia.Offers(info, pageHost(sel.PageURL))
	if err != nil {
		return nil, err
	}
	retry := !fresh && !sel.Prefetch
	p := &daemon.SiteProbe{Site: sitemedia.PageSiteName(info.ExtractorKey, pageHost(sel.PageURL)), Title: info.Title,
		EntryID: entry.ID, ItemsLeftOut: entry.LeftOut, BrowserFetch: sitemedia.BrowserOnly(info.ExtractorKey)}
	for _, it := range entry.Items {
		p.Items = append(p.Items, model.MediaItem{ID: it.ID, Title: it.Title, Duration: it.Duration, Thumbnail: it.Thumbnail})
	}
	for _, c := range info.Chapters {
		p.Chapters = append(p.Chapters, model.Chapter{StartMs: c.StartMs, EndMs: c.EndMs, Title: c.Title})
	}
	for off.Format != sitemedia.FormatHTTP {
		if retry && expiresWithin(off.Expires, s.now().Add(probeMinValid)) {
			return nil, errStaleLink
		}
		if !off.Audio {
			probe := mediajob.ProbeMP4
			if sel.Prefetch {
				probe = mediajob.ProbeMP4Playlists
			}
			mi, err := probe(ctx, tf, manifestSelection(off, sel), sitemedia.ManifestTracksFor(sel.VideoTrack, sel.VideoKey))
			if err != nil {
				return nil, staleLink(err, retry)
			}
			if err := checkManifestMP4(mi); err != nil {
				return nil, err
			}
			p.MediaProbe = daemon.MediaProbe(*mi)
			return p, nil
		}
		// An audio page's stream (docs/SITE-MEDIA-SPEC.md §3.13): its tracks are listed as
		// audio tracks, saved as .m4a.
		probe := mediajob.ProbeAudio
		if sel.Prefetch {
			probe = mediajob.ProbeAudioPlaylists
		}
		mi, err := probe(ctx, tf, manifestSelection(off, sel), sitemedia.ManifestAudioTracks)
		if err == nil {
			mi.Tracks = off.DescribeAudio(mi.Tracks)
			p.MediaProbe, err = audioManifestProbe(mi)
		}
		switch {
		case err == nil:
			return p, nil
		case off.Next() != nil && unusableStream(err):
			off = off.Next()
		case errors.Is(err, mediajob.ErrNoMP4Audio):
			return nil, errNoAudio
		default:
			return nil, staleLink(err, retry)
		}
	}
	pick, err := sitemedia.Select(info, off, sel.VideoTrack, sel.AudioTrack, sel.VideoKey, sel.AudioKey)
	if err != nil {
		return nil, err
	}
	p.MediaProbe = daemon.MediaProbe{Format: off.Format, Tracks: off.Listed(pick), DefaultVideo: pick.Tracks[0].ID,
		Duration: info.Duration, Container: pick.Container}
	switch {
	case off.Audio:
		p.DefaultVideo, p.DefaultAudio = "", pick.Tracks[0].ID
	case len(pick.Tracks) > 1:
		p.DefaultAudio = pick.Tracks[1].ID
	}
	return p, nil
}

// unusableStream reports the failure of an audio page's stream that its content causes
// (it cannot be saved: unsupported, encrypted, malformed), not the network or the site.
func unusableStream(err error) bool {
	return errors.Is(err, media.ErrUnsupported) || errors.Is(err, media.ErrEncrypted) || errors.Is(err, media.ErrMalformed)
}

// refusedLink returns the HTTP status with which a stream's server refused an address
// of it (401, 403 or 410: a signed link that expired, or a refusal), else 0.
func refusedLink(err error) int {
	var se *mediajob.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusGone:
			return se.Code
		}
	}
	return 0
}

// staleLink is err, marked errStaleLink when retry is set and the stream's server
// refused its address (refusedLink).
func staleLink(err error, retry bool) error {
	if retry && refusedLink(err) != 0 {
		return fmt.Errorf("%w: %w", errStaleLink, err)
	}
	return err
}

// linkRefused is err, except that a stream's address refused after the page was read
// past the cache (refusedLink) is site_error with sitemedia.LinkRefusedReason.
func linkRefused(err error) error {
	if code := refusedLink(err); code != 0 {
		return fmt.Errorf("%w: %s", sitemedia.ErrSiteError, sitemedia.LinkRefusedReason(code))
	}
	return err
}

// expiresWithin reports a known expiry (not zero) before t.
func expiresWithin(expires, t time.Time) bool { return !expires.IsZero() && expires.Before(t) }

// playlistError is err, except that a playlist refused on an audio site
// (sitemedia.AudioSite) says to open a single track of it.
func playlistError(err error, pageURL string) error {
	if host := pageHost(pageURL); errors.Is(err, sitemedia.ErrPlaylist) && sitemedia.AudioSite(host) {
		return sitemedia.PlaylistError(media.ErrUnsupported, host)
	}
	return err
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
	opts.AudioOnly = page.off.Audio
	if page.off.Format != sitemedia.FormatHTTP {
		container, err := s.runManifest(ctx, opts, sel, page, resume, progress, checkpoint)
		return audioContainer(page.off, container), err
	}
	// Browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8): a site whose video servers
	// answer only browsers, or a download that went on through the browser before, is
	// fetched by the browser from the start when one fetches for it.
	browserOnly := sitemedia.BrowserOnly(page.info.ExtractorKey)
	viaBrowser := o.Browser != nil && (sel.BrowserFetch || browserOnly)
	maxConns := o.MaxConnections
	if viaBrowser {
		maxConns = min(max(maxConns, 1), browserfetch.Lanes)
	}
	tracks := rangedTracks(page, o.HostRules, resume, maxConns)
	sizes := newTrackSizes(tracks)
	rsel := mediajob.RangedSelection{Tracks: tracks, Output: page.pick.Output, NotBefore: page.pick.NotBefore,
		Refresh: sizes.refresh(s.refresher(sel, tracks)), Clip: sel.Clip}
	if viaBrowser {
		opts.Factory, opts.MaxConnections = o.Browser(sizes.of), maxConns
		if !sel.BrowserFetch && o.OnBrowser != nil {
			o.OnBrowser()
		}
	}
	// The last checkpoint saved: a run that goes on through the browser resumes from it.
	last := jobCheckpoint(resume)
	save := checkpoint
	if checkpoint != nil {
		save = func(c mediajob.Checkpoint) error {
			err := checkpoint(c)
			if err == nil {
				last = &c
			}
			return err
		}
	}
	container, err := mediajob.RunRanged(ctx, opts, rsel, last, progress, save)
	if !viaBrowser && o.Browser != nil && refusedAgain(err) && ctx.Err() == nil {
		// Its own requests were refused again after the renewal: the browser that added it
		// makes them now, keeping every chunk saved (same keys and chunk sizes).
		viaBrowser = true
		if o.OnBrowser != nil {
			o.OnBrowser()
		}
		opts.Factory, opts.MaxConnections = o.Browser(sizes.of), min(max(o.MaxConnections, 1), browserfetch.Lanes)
		container, err = mediajob.RunRanged(ctx, opts, rsel, last, progress, save)
	}
	return audioContainer(page.off, container), ctxError(ctx, browserError(forbiddenError(err, browserOnly && !viaBrowser, viaBrowser)))
}

// refusedAgain reports a run of direct formats whose request was refused with 403 again
// after the renewal (docs/SITE-MEDIA-SPEC.md §2.6).
func refusedAgain(err error) bool {
	var se *mediajob.StatusError
	return errors.Is(err, mediajob.ErrRefusedAgain) && errors.As(err, &se) && se.Code == http.StatusForbidden
}

// browserError is err, except that a run whose browser stopped fetching, got a page
// instead of the video or could not fetch it (docs/SITE-MEDIA-SPEC.md §2.8) is site_error
// with the reason that says so.
func browserError(err error) error {
	switch {
	case errors.Is(err, browserfetch.ErrGone):
		return sitemedia.BrowserError(sitemedia.ReasonBrowserGone)
	case errors.Is(err, browserfetch.ErrNotMedia):
		return sitemedia.BrowserError(sitemedia.ReasonBrowserNotMedia)
	case errors.Is(err, browserfetch.ErrBrowser):
		return sitemedia.BrowserError(sitemedia.ReasonBrowserFailed)
	}
	return err
}

// trackSizes maps the addresses of a run's tracks to their exact sizes, renewed with the
// addresses (the browser uses a size only to find the same resource its player fetched,
// docs/SITE-MEDIA-SPEC.md §2.8).
type trackSizes struct {
	mu sync.Mutex
	m  map[string]int64
}

func newTrackSizes(tracks []media.RangedTrack) *trackSizes {
	ts := &trackSizes{}
	ts.set(tracks)
	return ts
}

func (ts *trackSizes) set(tracks []media.RangedTrack) {
	m := make(map[string]int64, len(tracks))
	for _, t := range tracks {
		if t.Size > 0 {
			m[t.URL] = t.Size
		}
	}
	ts.mu.Lock()
	ts.m = m
	ts.mu.Unlock()
}

// of is the exact size of the track at url, 0 = unknown.
func (ts *trackSizes) of(url string) int64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.m[url]
}

// refresh wraps a run's Refresh so that the renewed addresses keep their sizes.
func (ts *trackSizes) refresh(f func(context.Context) ([]media.RangedTrack, error)) func(context.Context) ([]media.RangedTrack, error) {
	return func(ctx context.Context) ([]media.RangedTrack, error) {
		tracks, err := f(ctx)
		if err == nil {
			ts.set(tracks)
		}
		return tracks, err
	}
}

// audioContainer is the container a site run reports for the file mediajob wrote: an
// audio page's MP4 is its .m4a (docs/SITE-MEDIA-SPEC.md §3.13); anything else as it is.
func audioContainer(off *sitemedia.Offer, container string) string {
	if off.Audio && container == sitemedia.ContainerMP4 {
		return sitemedia.ContainerM4A
	}
	return container
}

// audioManifestProbe describes the probed stream of an audio page (docs/SITE-MEDIA-SPEC.md
// §3.13): saved as an MP4 (.m4a), its listed tracks (sitemedia.ManifestAudioTracks: a
// media playlist's one track, or muxed variants without a picture) presented as audio
// tracks, the probe's default as the default audio and no video. Anything else is
// media_unsupported.
func audioManifestProbe(mi *mediajob.Info) (daemon.MediaProbe, error) {
	if mi.Container != sitemedia.ContainerMP4 || len(mi.Tracks) == 0 {
		return daemon.MediaProbe{}, errNoAudio
	}
	p := daemon.MediaProbe(*mi)
	p.Tracks = slices.Clone(mi.Tracks)
	for i := range p.Tracks {
		p.Tracks[i].Kind = media.KindAudio
	}
	p.DefaultAudio = cmp.Or(mi.DefaultVideo, mi.DefaultAudio)
	p.DefaultVideo = ""
	p.Container = sitemedia.ContainerM4A
	return p, nil
}

// forbiddenError is err, except that a 403 again from the renewed addresses of a page's
// video (docs/SITE-MEDIA-SPEC.md §2.6, amendment 2026-11-18) is site_error with
// sitemedia.ReasonForbidden: the site's video server refuses Downex (most often its bot
// protection, which Downex never tries to get past), as a page refused with 403 reads.
//
// For a site whose video servers answer only browsers (browserOnly) and that no browser
// fetched, the reason says to download it from the browser instead (sitemedia.ReasonBrowserOnly);
// a run whose browser was refused too (viaBrowser) says the site refused the quality to the
// browser (sitemedia.ReasonBrowserRefused), not that yt-dlp could not read the page
// (docs/SITE-MEDIA-SPEC.md §2.8).
func forbiddenError(err error, browserOnly, viaBrowser bool) error {
	if !refusedAgain(err) {
		return err
	}
	switch {
	case viaBrowser:
		return sitemedia.BrowserError(sitemedia.ReasonBrowserRefused)
	case browserOnly:
		return sitemedia.BrowserError(sitemedia.ReasonBrowserOnly)
	}
	return fmt.Errorf("%w: %s", sitemedia.ErrSiteError, sitemedia.ReasonForbidden)
}

// sitePage is a page resolved for a run: its offer and, for direct formats, the
// chosen tracks.
type sitePage struct {
	info *sitemedia.Info
	off  *sitemedia.Offer
	pick *sitemedia.Pick // FormatHTTP only
}

// resolveRun resolves the page of sel for a run (docs/SITE-MEDIA-SPEC.md §2.6). A cached
// resolution serves only when every chosen URL whose expiry is known (of a stream, its
// signed link's, sitemedia.Offer.Expires) stays valid for siteMinValid more; otherwise
// the page is resolved past the cache. A chosen track,
// format kind or item (docs/SITE-MEDIA-SPEC.md §3.12) that is missing gets one more
// resolution past the cache after retryDelay.
func (s *siteMedia) resolveRun(ctx context.Context, sel daemon.SiteSelection) (*sitePage, error) {
	fresh, retried := false, false
	for {
		page, err := s.resolvePage(ctx, sel, fresh)
		switch {
		case err == nil && !fresh && page.pick != nil && expiresBefore(page.pick, s.now().Add(siteMinValid)),
			err == nil && !fresh && page.pick == nil && expiresWithin(page.off.Expires, s.now().Add(siteMinValid)):
			fresh = true
			continue
		case err == nil:
			return page, nil
		case retried || (!errors.Is(err, errTrackGone) && !errors.Is(err, sitemedia.ErrItemGone)):
			return nil, err
		}
		retried, fresh = true, true
		if err := sleepContext(ctx, s.retryDelay); err != nil {
			return nil, err
		}
	}
}

// resolvePage resolves the page of sel (past the cache with fresh), finds its item
// (sitemedia.ChooseEntry: an item the page no longer has is site_unavailable, never
// another item) and the chosen tracks: errTrackGone when the page now offers another kind
// of format than sel's, or no longer has a chosen key.
func (s *siteMedia) resolvePage(ctx context.Context, sel daemon.SiteSelection, fresh bool) (*sitePage, error) {
	resolved, err := s.res.Resolve(ctx, pageRequest(sel, fresh))
	if err != nil {
		return nil, playlistError(err, sel.PageURL)
	}
	entry, err := sitemedia.ChooseEntry(resolved, sel.EntryID, pageHost(sel.PageURL))
	if err != nil {
		return nil, err
	}
	info := entry.Info
	off, err := sitemedia.Offers(info, pageHost(sel.PageURL))
	if err != nil {
		return nil, err
	}
	// Of an audio page, the usable group recorded (docs/SITE-MEDIA-SPEC.md §3.13), which
	// follows the best one when the probe could not save the best one's stream.
	for alt := off; alt != nil; alt = alt.Next() {
		if sel.Format != "" && alt.Format != sel.Format {
			continue
		}
		page := &sitePage{info: info, off: alt}
		if alt.Format == sitemedia.FormatHTTP {
			page.pick, err = sitemedia.Select(info, alt, sel.VideoTrack, sel.AudioTrack, sel.VideoKey, sel.AudioKey)
			switch {
			case errors.Is(err, media.ErrNotFound) && alt.Next() != nil:
				continue
			case errors.Is(err, media.ErrNotFound):
				return nil, errTrackGone
			case err != nil:
				return nil, err
			}
		}
		return page, nil
	}
	if off.Format != sel.Format {
		return nil, fmt.Errorf("%w (the page now offers %s instead of %s)", errTrackGone, off.Format, sel.Format)
	}
	return nil, errTrackGone
}

// runManifest downloads the manifest fallback with mediajob.Run (docs/SITE-MEDIA-SPEC.md
// §3.11), MP4 only: a chosen track that would be saved as anything else is refused
// before its fragments are fetched. When a chosen track's key is gone from the manifest,
// the page is resolved once more past the cache after retryDelay and the run tried
// again with its manifest URL; when the stream's server refuses an address of it (HTTP
// 401, 403 or 410: a signed link that expired) before the run saved a checkpoint, at
// once. A refusal that stays is site_error (sitemedia.LinkRefusedReason).
func (s *siteMedia) runManifest(ctx context.Context, opts mediajob.Options, sel daemon.SiteSelection, page *sitePage,
	resume *model.MediaCheckpoint, progress func(mediajob.Progress), checkpoint func(mediajob.Checkpoint) error) (string, error) {
	opts.MP4Only = true
	for retried := false; ; retried = true {
		if page.off.Audio {
			opts.AudioOnly = true
		}
		saved := false
		save := checkpoint
		if checkpoint != nil {
			save = func(c mediajob.Checkpoint) error {
				saved = true
				return checkpoint(c)
			}
		}
		container, err := mediajob.Run(ctx, opts, manifestSelection(page.off, sel), jobCheckpoint(resume), progress, save)
		refused := refusedLink(err) != 0 && !saved
		if err == nil || retried || ctx.Err() != nil || !refused && !errors.Is(err, mediajob.ErrInvalidSelection) {
			return container, ctxError(ctx, linkRefused(err))
		}
		if !refused {
			if err := sleepContext(ctx, s.retryDelay); err != nil {
				return "", err
			}
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
	switch {
	case chosen[0].Kind == media.KindAudio:
		// An audio page's one track (docs/SITE-MEDIA-SPEC.md §3.13).
		sel.VideoTrack, sel.VideoKey, sel.AudioTrack, sel.AudioKey = "", "", chosen[0].ID, chosen[0].Key
	case len(chosen) > 1:
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
// with the headers of its format, and the tracks sel chose. An audio page's chosen track
// (docs/SITE-MEDIA-SPEC.md §3.13), recorded as audio, is the presentation's one track,
// which a media playlist lists without a kind: it is named as the selection's video.
func manifestSelection(off *sitemedia.Offer, sel daemon.SiteSelection) mediajob.Selection {
	ms := mediajob.Selection{ManifestURL: off.ManifestURL, Headers: off.Headers.Clone(), VideoTrack: sel.VideoTrack,
		AudioTrack: sel.AudioTrack, VideoKey: sel.VideoKey, AudioKey: sel.AudioKey, Clip: sel.Clip}
	if off.Audio {
		ms.VideoTrack, ms.VideoKey, ms.AudioTrack, ms.AudioKey = sel.AudioTrack, sel.AudioKey, "", ""
	}
	return ms
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
	return sitejob.Request{PageURL: sel.PageURL, Cookie: sel.Cookie, CookieDomain: sel.CookieDomain, Referer: sel.Referer,
		Proxy: sitejob.Proxy(sel.Proxy), Fresh: fresh, Prefetch: sel.Prefetch}
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
