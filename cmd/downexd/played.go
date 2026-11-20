package main

import (
	"context"
	"errors"
	"net/http"

	"downex/internal/browserfetch"
	"downex/internal/daemon"
	"downex/internal/media"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/played"
	"downex/internal/sitemedia"
	"downex/internal/transport"
)

// playedMedia downloads the videos the user's browser played for the daemon
// (daemon.PlayedMedia, docs/SITE-MEDIA-SPEC.md §2.9): the stored files with
// mediajob.RunRanged, copied or joined, fetched directly first (only Range: no cookie, no
// Referer) and through the browser that added the download when its server refuses
// downexd. There is no page to read again, so nothing renews an address.
type playedMedia struct {
	// freeSpace: as siteMedia's (nil skips the check).
	freeSpace func(dir string) (int64, error)
	// publicOnly: downexd's own requests connect only to public addresses
	// (transport.PublicOnly): a name of the played file that resolves to this computer or a
	// local network is refused at the dial (docs/SITE-MEDIA-SPEC.md §2.9 "Limits"). Tests of
	// local servers leave it off.
	publicOnly bool
}

var _ daemon.PlayedMedia = (*playedMedia)(nil)

// Run downloads sel's files into opts.OutPath. A record whose chosen file needed the tab
// goes through the browser from the start (sel.BrowserFetch) when one fetches for it; a
// direct run refused (refusedDirect) goes on through it with the last checkpoint and marks
// the record (opts.OnBrowser). Refusals without a browser read
// played.ReasonBrowserOnly; refused by both, played.ReasonExpired.
func (p *playedMedia) Run(ctx context.Context, o daemon.MediaRunOptions, sel daemon.PlayedSelection, resume *model.MediaCheckpoint,
	onProgress func(daemon.MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	direct := o.Factory
	if p.publicOnly {
		direct = transport.PublicOnly(direct)
	}
	opts := mediajob.Options{
		Factory: direct, Limiter: o.Limiter, MaxConnections: o.MaxConnections, PartDir: o.PartDir, OutPath: o.OutPath,
		HostRules: o.HostRules, FreeSpace: p.freeSpace, ReplaceExisting: o.ReplaceExisting,
	}
	var progress func(mediajob.Progress)
	if onProgress != nil {
		progress = func(pr mediajob.Progress) { onProgress(daemon.MediaProgress(pr)) }
	}
	viaBrowser := o.Browser != nil && sel.BrowserFetch
	maxConns := o.MaxConnections
	if viaBrowser {
		maxConns = min(max(maxConns, 1), browserfetch.Lanes)
	}
	tracks, sizes := playedTracks(sel, o, resume, maxConns)
	output := mediajob.OutputCopy
	if sel.Output == played.OutputRemux {
		output = mediajob.OutputRemux
	}
	rsel := mediajob.RangedSelection{Tracks: tracks, Output: output}
	if viaBrowser {
		opts.Factory, opts.MaxConnections = o.Browser(sizes.of), maxConns
	}
	last := jobCheckpoint(resume)
	save := func(c mediajob.Checkpoint) error {
		if onCheckpoint == nil {
			return nil
		}
		err := onCheckpoint(modelCheckpoint(c))
		if err == nil {
			last = &c
		}
		return err
	}
	container, err := mediajob.RunRanged(ctx, opts, rsel, last, progress, save)
	if err != nil && !viaBrowser && o.Browser != nil && refusedDirect(err) && ctx.Err() == nil {
		// downexd's own request was refused: the browser that added it makes them now,
		// keeping every chunk saved (same keys and chunk sizes).
		viaBrowser = true
		if o.OnBrowser != nil {
			o.OnBrowser()
		}
		opts.Factory, opts.MaxConnections = o.Browser(sizes.of), min(max(o.MaxConnections, 1), browserfetch.Lanes)
		container, err = mediajob.RunRanged(ctx, opts, rsel, last, progress, save)
	}
	return container, ctxError(ctx, playedRunError(err, viaBrowser))
}

// playedTracks are sel's files as ranged tracks, with their chunk sizes (rangedTracks'
// rule: the checkpoint's, else fitted to the host and the connections), and their sizes
// by address for the browser.
func playedTracks(sel daemon.PlayedSelection, o daemon.MediaRunOptions, resume *model.MediaCheckpoint, maxConns int) ([]media.RangedTrack, *trackSizes) {
	tracks := make([]media.RangedTrack, 0, len(sel.Tracks))
	for _, t := range sel.Tracks {
		rt := media.RangedTrack{ID: t.ID, Key: t.ID, Kind: media.TrackKind(t.Kind), Ext: "mp4", URL: t.URL, Size: t.Size}
		if rt.Kind == media.KindAudio {
			rt.Ext = "m4a"
		}
		if c := checkpointChunk(resume, rt.Key); c > 0 {
			rt.ChunkBytes = c
		} else {
			rt.ChunkBytes = mediajob.FitChunk(o.HostRules, rt.URL, mediajob.ChunkSize(o.HostRules, rt.URL, 0), rt.Size, maxConns)
		}
		tracks = append(tracks, rt)
	}
	return tracks, newTrackSizes(tracks)
}

// refusedDirect reports a direct run whose server refused downexd: HTTP 400, 401, 403,
// 404, 410 or 451, or an answer that is not the file (a page in its place). The
// browser's request is the player's own, which the server answered.
func refusedDirect(err error) bool {
	var se *mediajob.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone,
			http.StatusUnavailableForLegalReasons:
			return true
		}
		return false
	}
	return errors.Is(err, media.ErrMalformed) || errors.Is(err, mediajob.ErrRangeMismatch)
}

// playedRunError is a played run's failure as the user reads it (docs/SITE-MEDIA-SPEC.md
// §2.9 "Errors"): the browser's own failures by their reason (browserError), a refusal
// through the browser too as an expired link, a refusal without a browser as one only the
// browser can get; anything else as it is.
func playedRunError(err error, viaBrowser bool) error {
	if err == nil {
		return nil
	}
	if b := browserError(err); b != err {
		return b
	}
	if !refusedDirect(err) {
		return err
	}
	if viaBrowser {
		// Only a refusal (an HTTP status) is an expired link; an answer the run could not use
		// (a missing header, not the file) never sends the user back to click again.
		var se *mediajob.StatusError
		if errors.As(err, &se) {
			return sitemedia.BrowserError(played.ReasonExpired)
		}
		return sitemedia.BrowserError(sitemedia.ReasonBrowserFailed)
	}
	return sitemedia.BrowserError(played.ReasonBrowserOnly)
}
