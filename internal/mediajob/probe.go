package mediajob

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"downex/internal/media"
	"downex/internal/transport"
)

// Manifest fetches in ProbeMP4: few attempts and short waits, because ProbeMP4 answers
// an interactive request (an add from the browser or the CLI).
const (
	probeRetries       = 3
	probeRetryMaxDelay = 4 * time.Second
)

// track is one selected track, planned.
type track struct {
	id       string
	key      string          // media.Track.Key ("" when the track has none)
	kind     media.TrackKind // KindVideo, KindAudio or KindMuxed
	language string          // media.Track.Language, for the remuxer's 'mdhd' (remux.Input.Language)
	plan     *media.Plan
	// container is the plan's container; ContainerUnknown until Run sniffs the data
	// when the manifest does not say.
	container media.Container
	hls       bool
}

// resolved is a parsed presentation with its selection planned.
type resolved struct {
	info   Info
	tracks []*track // the video (or muxed) track first, then the audio track
	// fallbackDuration is the presentation's duration, for plans without durations.
	fallbackDuration time.Duration
}

// resolve fetches and parses the manifest of sel (and, for an HLS master playlist, the
// media playlists of the selected tracks; each at most media.MaxManifestBytes), resolves
// the selection and plans its tracks, for Run; legacy is Options.LegacyTS. Encrypted
// presentations yield an error wrapping media.ErrEncrypted, live ones media.ErrLive,
// selections that cannot be assembled media.ErrUnsupported.
func resolve(ctx context.Context, f transport.Fetcher, rt retrier, sel Selection, legacy bool) (*resolved, error) {
	if err := checkManifestURL(sel.ManifestURL); err != nil {
		return nil, err
	}
	hp := newHeaderPolicy(sel.ManifestURL, sel.Headers)
	body, base, ctype, err := fetchManifest(ctx, f, rt, hp, sel.ManifestURL)
	if err != nil {
		return nil, fmt.Errorf("mediajob: manifest: %w", err)
	}
	format, err := detectFormat(body, ctype)
	if err != nil {
		return nil, err
	}
	var res *resolved
	if format == "hls" {
		res, err = resolveHLS(ctx, f, rt, hp, sel, base, body, legacy)
	} else {
		res, err = resolveDASH(sel, base, body)
	}
	if err != nil {
		return nil, err
	}
	if err := res.finish(format, legacy); err != nil {
		return nil, err
	}
	return res, nil
}

// finish fills in the Info of the planned selection: the format, the chosen tracks, the
// duration, the expected container (outputContainer; legacy is Options.LegacyTS) and the
// request count.
func (res *resolved) finish(format string, legacy bool) error {
	res.info.Format = format
	for _, t := range res.tracks {
		switch t.kind {
		case media.KindAudio:
			res.info.DefaultAudio = t.id
		default:
			res.info.DefaultVideo = t.id
		}
		res.info.Duration = max(res.info.Duration, t.plan.Duration)
		res.info.FragmentsTotal += len(t.plan.Fragments)
	}
	if res.info.Duration == 0 {
		res.info.Duration = res.fallbackDuration
	}
	var err error
	res.info.Container, err = expectedContainer(res.tracks, legacy)
	return err
}

// probeParallel bounds the media playlists ProbeMP4 fetches at once.
const probeParallel = 4

// errNoMP4Video refuses a presentation without a video that can be saved as an MP4.
var errNoMP4Video = fmt.Errorf("mediajob: %w: no MP4 video in this stream", media.ErrUnsupported)

// ProbeMP4 fetches the manifest of sel and describes the tracks that can be saved as one
// MP4 (docs/VIDEO-DETECT-SPEC.md §3.3), for every MANIFEST probe and for a web page's
// manifest fallback (docs/SITE-MEDIA-SPEC.md §3.11). Info.Tracks lists the video, muxed
// and audio tracks that are fragmented MP4 with an initialization segment (DASH
// Representations of segment lists or templates whose type is not WebM, Matroska or
// MPEG-TS; HLS tracks whose media playlist has an EXT-X-MAP) or HLS transport streams
// that internal/remux converts (a TS EXT-X-MAP, .ts segments, or no EXT-X-MAP at all;
// CODECS that media.TSConvertible accepts, or none; not packed audio). A video-only
// track is listed only when an audio track of its container (TS with TS, fragmented MP4
// with fragmented MP4) is listed to pair it with, or when the presentation has no audio
// at all; an audio track only with such a video. Each offered media playlist is fetched
// once, at most probeParallel at once. Of the media, only what describes a listed HLS
// video or muxed track without a BANDWIDTH (a bare media playlist's) is fetched
// (describeTracks): a few bounded requests, one at a time, for its size and, when
// undeclared, its picture, frame rate and codecs, whose failure never fails the probe;
// at most describeMaxTracks tracks are measured, the default video first.
//
// offer (nil keeps the tracks as listed) filters the candidates further and orders them
// best first; it may drop tracks but never adds any. When planning refuses an offered
// track for its container or codecs, offer is asked once more without the refused
// tracks and the tracks it newly offers are planned too (at most two rounds), so that a
// quality whose best variant cannot be saved falls back to its next one. The selection
// is resolved among the listed tracks only (a track left out is not found). Without a
// named video the default is the first listed video or muxed track (when only an audio
// track is named, the first video of its container), with the best audio of its
// container; a named video and audio of different containers are ErrInvalidSelection.
// A presentation without a listed video is refused with media.ErrUnsupported ("no MP4
// video in this stream"; or, when a candidate was refused for a reason, such as
// encryption, with the first of those reasons); a failed manifest fetch fails the
// probe. Info.Container is "mp4".
func ProbeMP4(ctx context.Context, tf transport.Factory, sel Selection, offer func([]media.Track) []media.Track) (*Info, error) {
	return probeMP4(ctx, tf, sel, offer, true)
}

// ProbeMP4Playlists is ProbeMP4 that fetches the manifest and media playlists only,
// nothing of the media: no track is measured (describeTracks), so a bare media
// playlist's track is listed without a size or picture. A look-ahead uses it
// (docs/SITE-MEDIA-SPEC.md §5.6): it is made without the user asking.
func ProbeMP4Playlists(ctx context.Context, tf transport.Factory, sel Selection, offer func([]media.Track) []media.Track) (*Info, error) {
	return probeMP4(ctx, tf, sel, offer, false)
}

// probeMP4 is ProbeMP4, which measures the tracks that need it when describe is set.
func probeMP4(ctx context.Context, tf transport.Factory, sel Selection, offer func([]media.Track) []media.Track,
	describe bool) (*Info, error) {
	if tf == nil {
		return nil, fmt.Errorf("%w: no transport factory", ErrInvalidOptions)
	}
	if err := checkManifestURL(sel.ManifestURL); err != nil {
		return nil, err
	}
	f := tf.NewSlot()
	defer f.Close()
	rt := retrier{clock: RealClock(), max: probeRetries, base: DefaultRetryBaseDelay, maxDelay: probeRetryMaxDelay}
	hp := newHeaderPolicy(sel.ManifestURL, sel.Headers)
	body, base, ctype, err := fetchManifest(ctx, f, rt, hp, sel.ManifestURL)
	if err != nil {
		return nil, fmt.Errorf("mediajob: manifest: %w", err)
	}
	format, err := detectFormat(body, ctype)
	if err != nil {
		return nil, err
	}
	var (
		cands    []media.Track
		plan     func(context.Context, []media.Track) ([]candidate, error)
		fallback time.Duration
	)
	if format == "hls" {
		pl, err := media.ParseHLS(base, body)
		if err != nil {
			return nil, fmt.Errorf("mediajob: playlist: %w", err)
		}
		cands = pl.Options()
		plan = func(ctx context.Context, ts []media.Track) ([]candidate, error) {
			return planHLSTracks(ctx, tf, rt, hp, pl, ts)
		}
	} else {
		mpd, err := media.ParseDASH(base, body)
		if err != nil {
			return nil, fmt.Errorf("mediajob: MPD: %w", err)
		}
		cands = mpd.Options()
		plan = func(_ context.Context, ts []media.Track) ([]candidate, error) { return planDASHTracks(mpd, ts), nil }
		fallback = mpd.MediaPresentationDuration
	}
	cands = slices.DeleteFunc(cands, func(t media.Track) bool { return t.Kind == media.KindSubtitles })
	hasAudio := slices.ContainsFunc(cands, func(t media.Track) bool { return t.Kind == media.KindAudio })
	if offer == nil {
		offer = func(ts []media.Track) []media.Track { return ts }
	}
	offered := offer(slices.Clone(cands))
	planned := make(map[string]candidate, len(offered))
	if err := planInto(ctx, plan, offered, planned); err != nil {
		return nil, err
	}
	if refused := refusedTracks(offered, planned); len(refused) > 0 {
		// The second round: the offer without the refused tracks, of which only what it
		// newly offers is planned.
		offered = offer(slices.DeleteFunc(slices.Clone(cands), func(t media.Track) bool { return refused[t.ID] }))
		var fresh []media.Track
		for _, t := range offered {
			if _, ok := planned[t.ID]; !ok {
				fresh = append(fresh, t)
			}
		}
		if err := planInto(ctx, plan, fresh, planned); err != nil {
			return nil, err
		}
	}
	tracks, byID, reason := listMP4(offered, planned, hasAudio)
	if !slices.ContainsFunc(tracks, func(t media.Track) bool { return t.Kind != media.KindAudio }) {
		if reason != nil {
			return nil, reason
		}
		return nil, errNoMP4Video
	}
	containerOf := func(id string) media.Container { return assumedContainer(byID[id]) }
	if sel.VideoTrack == "" && sel.VideoKey == "" {
		sel.VideoTrack = defaultVideo(tracks, sel, containerOf)
	}
	video, audio, err := pick(tracks, sel, nil)
	if err != nil {
		return nil, err
	}
	if video != nil && audio != nil && containerOf(video.ID) != containerOf(audio.ID) {
		if sel.AudioTrack != "" || sel.AudioKey != "" {
			return nil, fmt.Errorf("%w: track %q (%s) and track %q (%s) cannot be joined into one MP4", ErrInvalidSelection,
				video.ID, containerOf(video.ID), audio.ID, containerOf(audio.ID))
		}
		// The best audio of the video's container, as DefaultPair pairs them.
		pair := []media.Track{*video}
		for _, t := range tracks {
			if t.Kind == media.KindAudio && containerOf(t.ID) == containerOf(video.ID) {
				pair = append(pair, t)
			}
		}
		_, da := media.DefaultPair(pair)
		audio = clone(da)
	}
	res := &resolved{info: Info{Tracks: tracks}, fallbackDuration: fallback}
	for _, t := range [...]*media.Track{video, audio} {
		if t != nil {
			res.tracks = append(res.tracks, byID[t.ID])
		}
	}
	if err := res.finish(format, false); err != nil {
		return nil, err
	}
	if describe && format == "hls" {
		describeTracks(ctx, tf, hp, &res.info, byID)
	}
	return &res.info, nil
}

// planInto plans the tracks ts with plan and records the candidate of each in into,
// by its ID.
func planInto(ctx context.Context, plan func(context.Context, []media.Track) ([]candidate, error), ts []media.Track,
	into map[string]candidate) error {
	if len(ts) == 0 {
		return nil
	}
	cs, err := plan(ctx, ts)
	if err != nil {
		return err
	}
	for i, c := range cs {
		into[ts[i].ID] = c
	}
	return nil
}

// refusedTracks returns the IDs of the offered tracks that planning refused for their
// container or codecs (planned without a plan and without a reason).
func refusedTracks(offered []media.Track, planned map[string]candidate) map[string]bool {
	refused := make(map[string]bool)
	for _, t := range offered {
		if c := planned[t.ID]; c.plan == nil && c.err == nil {
			refused[t.ID] = true
		}
	}
	return refused
}

// listMP4 returns the offered tracks that ProbeMP4 lists, in offer's order, with the
// planned track of each, and the first reason a candidate was refused for: every muxed
// track that was planned; a video-only track when the presentation has no audio
// (hasAudio false) or a planned audio track of its container exists; an audio track
// when a listed video-only track of its container exists.
func listMP4(offered []media.Track, planned map[string]candidate, hasAudio bool) ([]media.Track, map[string]*track, error) {
	var reason error
	audioIn := make(map[media.Container]bool) // containers of the planned audio tracks
	for _, t := range offered {
		c := planned[t.ID]
		reason = cmp.Or(reason, c.err)
		if c.plan != nil && t.Kind == media.KindAudio {
			audioIn[assumedContainer(c.track)] = true
		}
	}
	videoIn := make(map[media.Container]bool) // containers of the listed video-only tracks
	for _, t := range offered {
		if c := planned[t.ID]; c.plan != nil && t.Kind == media.KindVideo && (!hasAudio || audioIn[assumedContainer(c.track)]) {
			videoIn[assumedContainer(c.track)] = true
		}
	}
	var tracks []media.Track
	byID := make(map[string]*track)
	for _, t := range offered {
		c := planned[t.ID]
		if c.plan == nil {
			continue
		}
		switch t.Kind {
		case media.KindMuxed:
		case media.KindVideo, media.KindAudio:
			if !videoIn[assumedContainer(c.track)] {
				continue
			}
		default:
			continue
		}
		tracks = append(tracks, t)
		byID[t.ID] = c.track
	}
	return tracks, byID, reason
}

// defaultVideo is ProbeMP4's default video among the listed tracks: the first video or
// muxed track; when sel names an audio track only, the first video-only track of that
// track's container (when there is none, the first video, which pick then refuses or
// pairs as named).
func defaultVideo(tracks []media.Track, sel Selection, containerOf func(id string) media.Container) string {
	var aud *media.Track
	if sel.AudioTrack != "" || sel.AudioKey != "" {
		if a, err := find(tracks, "audio", sel.AudioTrack, sel.AudioKey); err == nil && a.Kind == media.KindAudio {
			aud = a
		}
	}
	first := ""
	for _, t := range tracks {
		if t.Kind != media.KindVideo && t.Kind != media.KindMuxed {
			continue
		}
		if aud == nil {
			return t.ID
		}
		if first == "" {
			first = t.ID
		}
		if t.Kind == media.KindVideo && containerOf(t.ID) == containerOf(aud.ID) {
			return t.ID
		}
	}
	return first
}

// candidate is a track ProbeMP4 considered: planned (plan set) when it can be saved as
// an MP4 (savableHLS; for DASH, fragmented MP4 with an initialization segment), without
// a plan when it cannot, and err when it cannot be planned (encrypted, malformed, ...).
type candidate struct {
	*track
	err error
}

// planHLSTracks plans the tracks ts of playlist pl for ProbeMP4, one candidate per
// track. The media playlists of a master playlist are fetched at most probeParallel at
// once, each worker with its own transport slot; a failed fetch fails the whole call.
func planHLSTracks(ctx context.Context, tf transport.Factory, rt retrier, hp headerPolicy, pl *media.HLSPlaylist, ts []media.Track) ([]candidate, error) {
	out := make([]candidate, len(ts))
	planOne := func(i int, mp *media.HLSPlaylist, err error) {
		t := &ts[i]
		c := candidate{track: &track{id: t.ID, key: t.Key, kind: t.Kind, language: t.Language, hls: true}, err: err}
		if err == nil && mp != nil {
			if plan, perr := media.PlanHLS(mp); perr != nil {
				c.err = fmt.Errorf("mediajob: track %q: %w", t.ID, perr)
			} else if c.container = plan.Container; savableHLS(c.track, plan, t.Codecs) {
				c.plan = plan
			}
		}
		out[i] = c
	}
	if !pl.Master {
		for i := range ts {
			planOne(i, pl, nil)
		}
		return out, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu     sync.Mutex
		failed error
		wg     sync.WaitGroup
	)
	next := make(chan int)
	for range min(probeParallel, len(ts)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := tf.NewSlot()
			defer f.Close()
			for i := range next {
				t := &ts[i]
				if t.URL == "" {
					planOne(i, nil, nil)
					continue
				}
				data, b, _, err := fetchManifest(ctx, f, rt, hp, t.URL)
				if err != nil {
					mu.Lock()
					if failed == nil {
						failed = fmt.Errorf("mediajob: media playlist of track %q: %w", t.ID, err)
					}
					mu.Unlock()
					cancel()
					continue
				}
				mp, err := media.ParseHLS(b, data)
				if err != nil {
					err = fmt.Errorf("mediajob: media playlist of track %q: %w", t.ID, err)
				}
				planOne(i, mp, err)
			}
		}()
	}
feed:
	for i := range ts {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if failed != nil {
		return nil, failed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// savableHLS reports whether ProbeMP4 can save a planned HLS track as an MP4: fragmented
// MP4 with an initialization segment, or a transport stream (a TS EXT-X-MAP, .ts-like
// segment names, or neither an EXT-X-MAP nor a known segment type) whose codecs
// internal/remux converts (media.TSConvertible; none named is accepted, Run checks the
// data) and that is not packed audio.
func savableHLS(t *track, plan *media.Plan, codecs string) bool {
	switch assumedContainer(t) {
	case media.ContainerFMP4:
		return planHasInit(plan)
	case media.ContainerTS:
		return media.TSConvertible(codecs) && !packedAudio(plan)
	}
	return false
}

// packedAudio reports a media playlist of raw audio segments (RFC 8216 §3.4: AAC, AC-3,
// E-AC-3 or MP3 with ID3 timestamps; docs/VIDEO-DETECT-SPEC.md §9 #4) or of subtitles
// (WebVTT, SRT, TTML: a player's subtitle playlist, which the extension detects like any
// other), which is not a transport stream: the path of its first media segment ends in
// .aac, .ac3, .ec3, .mp3, .vtt, .webvtt, .srt, .ttml or .dfxp.
func packedAudio(plan *media.Plan) bool {
	for i := range plan.Fragments {
		f := &plan.Fragments[i]
		if f.Init {
			continue
		}
		p := f.URL
		if u, err := url.Parse(f.URL); err == nil {
			p = u.Path
		}
		switch strings.ToLower(path.Ext(p)) {
		case ".aac", ".ac3", ".ec3", ".mp3":
			return true
		case ".vtt", ".webvtt", ".srt", ".ttml", ".dfxp":
			return true // subtitles: not a transport stream either
		}
		return false
	}
	return false
}

// planDASHTracks plans the tracks ts of mpd for ProbeMP4, one candidate per track: a
// Representation is planned when it has no SegmentBase, no WebM, Matroska or MPEG-TS
// type and a fragmented MP4 plan with an initialization segment.
func planDASHTracks(mpd *media.MPD, ts []media.Track) []candidate {
	out := make([]candidate, len(ts))
	for i := range ts {
		t := &ts[i]
		c := candidate{track: &track{id: t.ID, key: t.Key, kind: t.Kind, language: t.Language}}
		out[i] = c
		if rep := findRepresentation(mpd, t.ID); rep == nil || rep.Base != nil {
			continue
		}
		plan, err := media.PlanDASH(mpd, t.ID)
		if err != nil {
			out[i].err = fmt.Errorf("mediajob: track %q: %w", t.ID, err)
			continue
		}
		if slices.ContainsFunc(plan.Fragments, func(f media.Fragment) bool { return f.IndexRange != nil || f.InitRange != nil }) {
			continue
		}
		if c.container = plan.Container; assumedContainer(c.track) == media.ContainerFMP4 && planHasInit(plan) {
			c.plan = plan
		}
	}
	return out
}

func checkManifestURL(raw string) error {
	if raw == "" || len(raw) > media.MaxURIBytes {
		return fmt.Errorf("%w: manifest URL is empty or longer than %d bytes", ErrInvalidSelection, media.MaxURIBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: manifest URL must be an absolute http(s) URL", ErrInvalidSelection)
	}
	return nil
}

// fetchManifest fetches one playlist or MPD of at most media.MaxManifestBytes and
// returns it with the URL it came from after redirects (the base of its relative URIs)
// and its media type.
func fetchManifest(ctx context.Context, f transport.Fetcher, rt retrier, hp headerPolicy, rawURL string) ([]byte, *url.URL, string, error) {
	buf := &limitedBuffer{max: media.MaxManifestBytes}
	cp := &copier{buf: make([]byte, sliceCapped), clock: rt.clock}
	var meta respMeta
	err := rt.do(ctx, func() error {
		buf.b = buf.b[:0]
		m, _, err := fetchInto(ctx, f, fetchSpec{url: rawURL, headers: hp.forURL(rawURL)}, buf, cp, nil)
		meta = m
		return err
	})
	if err != nil {
		return nil, nil, "", err
	}
	final := meta.finalURL
	if final == "" {
		final = rawURL
	}
	base, err := url.Parse(final)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: unusable URL after redirects", ErrNotManifest)
	}
	return buf.b, base, meta.contentType, nil
}

// detectFormat identifies a manifest by its first bytes, then by its media type
// (docs/MEDIA-SPEC.md §6 step 1 lists the types).
func detectFormat(body []byte, contentType string) (string, error) {
	b := bytes.TrimPrefix(body, []byte("\xEF\xBB\xBF"))
	b = bytes.TrimLeft(b, " \t\r\n")
	if bytes.HasPrefix(b, []byte("#EXTM3U")) {
		return "hls", nil
	}
	if head := b[:min(len(b), 64<<10)]; bytes.HasPrefix(head, []byte("<")) &&
		(bytes.Contains(head, []byte("<MPD")) || bytes.Contains(head, []byte(":MPD"))) {
		return "dash", nil
	}
	switch strings.ToLower(contentType) {
	case "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl":
		return "hls", nil // ParseHLS reports what is wrong with it
	case "application/dash+xml":
		return "dash", nil
	}
	return "", ErrNotManifest
}

// resolveHLS resolves sel in an HLS playlist and plans its tracks. Unless legacy
// (Options.LegacyTS), a transport stream that ProbeMP4 would not list (savableHLS:
// unconvertible CODECS, packed audio) is refused before anything of it is fetched.
func resolveHLS(ctx context.Context, f transport.Fetcher, rt retrier, hp headerPolicy, sel Selection, base *url.URL, body []byte,
	legacy bool) (*resolved, error) {
	pl, err := media.ParseHLS(base, body)
	if err != nil {
		return nil, fmt.Errorf("mediajob: playlist: %w", err)
	}
	tracks := pl.Options()
	video, audio, err := pick(tracks, sel, nil)
	if err != nil {
		return nil, err
	}
	res := &resolved{info: Info{Tracks: tracks}}
	for _, t := range [...]*media.Track{video, audio} {
		if t == nil {
			continue
		}
		mp := pl
		if pl.Master {
			if t.URL == "" {
				return nil, fmt.Errorf("%w: track %q has no media playlist", ErrInvalidSelection, t.ID)
			}
			data, b, _, err := fetchManifest(ctx, f, rt, hp, t.URL)
			if err != nil {
				return nil, fmt.Errorf("mediajob: media playlist of track %q: %w", t.ID, err)
			}
			if mp, err = media.ParseHLS(b, data); err != nil {
				return nil, fmt.Errorf("mediajob: media playlist of track %q: %w", t.ID, err)
			}
		}
		plan, err := media.PlanHLS(mp)
		if err != nil {
			return nil, fmt.Errorf("mediajob: track %q: %w", t.ID, err)
		}
		tr := &track{id: t.ID, key: t.Key, kind: t.Kind, language: t.Language, plan: plan, container: plan.Container, hls: true}
		if !legacy && assumedContainer(tr) == media.ContainerTS && !savableHLS(tr, plan, t.Codecs) {
			return nil, fmt.Errorf("mediajob: track %q: %w: this MPEG-TS stream cannot be converted to MP4 (its codecs, or packed audio)",
				t.ID, media.ErrUnsupported)
		}
		res.tracks = append(res.tracks, tr)
	}
	return res, nil
}

func resolveDASH(sel Selection, base *url.URL, body []byte) (*resolved, error) {
	mpd, err := media.ParseDASH(base, body)
	if err != nil {
		return nil, fmt.Errorf("mediajob: MPD: %w", err)
	}
	tracks := mpd.Options()
	// The default pick prefers Representations that can be assembled.
	usable := func(t *media.Track) bool {
		rep := findRepresentation(mpd, t.ID)
		if rep == nil || rep.Base != nil {
			return false
		}
		mime := strings.ToLower(rep.MimeType)
		return !strings.Contains(mime, "webm") && !strings.Contains(mime, "matroska")
	}
	video, audio, err := pick(tracks, sel, usable)
	if err != nil {
		return nil, err
	}
	res := &resolved{info: Info{Tracks: tracks}}
	for _, t := range [...]*media.Track{video, audio} {
		if t == nil {
			continue
		}
		if rep := findRepresentation(mpd, t.ID); rep != nil && rep.Base != nil {
			return nil, fmt.Errorf("mediajob: track %q: %w: single-file Representations (SegmentBase with a segment index) are not supported yet", t.ID, media.ErrUnsupported)
		}
		plan, err := media.PlanDASH(mpd, t.ID)
		if err != nil {
			return nil, fmt.Errorf("mediajob: track %q: %w", t.ID, err)
		}
		for i := range plan.Fragments {
			if plan.Fragments[i].IndexRange != nil || plan.Fragments[i].InitRange != nil {
				return nil, fmt.Errorf("mediajob: track %q: %w: single-file Representations (SegmentBase with a segment index) are not supported yet", t.ID, media.ErrUnsupported)
			}
		}
		res.tracks = append(res.tracks, &track{id: t.ID, key: t.Key, kind: t.Kind, language: t.Language, plan: plan,
			container: plan.Container})
	}
	res.fallbackDuration = mpd.MediaPresentationDuration
	return res, nil
}

// findRepresentation returns the first Representation with the given @id.
func findRepresentation(mpd *media.MPD, id string) *media.Representation {
	for pi := range mpd.Periods {
		for ai := range mpd.Periods[pi].AdaptationSets {
			as := &mpd.Periods[pi].AdaptationSets[ai]
			for ri := range as.Representations {
				if as.Representations[ri].ID == id {
					return &as.Representations[ri]
				}
			}
		}
	}
	return nil
}

// pick resolves a selection against tracks. A chosen track is found by its key when the
// selection has one, else by its ID (find). Without either the default pick
// (media.DefaultPair) is taken among the tracks usable approves (all tracks when it
// approves none or is nil); a chosen video track without a chosen audio track gets the
// best audio for it. The results are copies.
func pick(tracks []media.Track, sel Selection, usable func(*media.Track) bool) (video, audio *media.Track, err error) {
	pool := tracks
	if usable != nil {
		var ok []media.Track
		for i := range tracks {
			if usable(&tracks[i]) {
				ok = append(ok, tracks[i])
			}
		}
		if len(ok) > 0 {
			pool = ok
		}
	}
	wantVideo := sel.VideoTrack != "" || sel.VideoKey != ""
	wantAudio := sel.AudioTrack != "" || sel.AudioKey != ""
	if wantVideo {
		if video, err = find(tracks, "video", sel.VideoTrack, sel.VideoKey); err != nil {
			return nil, nil, err
		}
		if video.Kind != media.KindVideo && video.Kind != media.KindMuxed {
			return nil, nil, fmt.Errorf("%w: track %q is not a video track", ErrInvalidSelection, video.ID)
		}
	}
	if wantAudio {
		if audio, err = find(tracks, "audio", sel.AudioTrack, sel.AudioKey); err != nil {
			return nil, nil, err
		}
		if audio.Kind != media.KindAudio {
			return nil, nil, fmt.Errorf("%w: track %q is not an audio track", ErrInvalidSelection, audio.ID)
		}
	}
	switch {
	case !wantVideo:
		dv, da := media.DefaultPair(pool)
		video = clone(dv)
		if audio == nil {
			audio = clone(da)
		}
	case audio == nil && video.Kind == media.KindVideo:
		// The best audio for the chosen video, as DefaultPair pairs them.
		cands := []media.Track{*video}
		for i := range pool {
			if pool[i].Kind == media.KindAudio {
				cands = append(cands, pool[i])
			}
		}
		_, da := media.DefaultPair(cands)
		audio = clone(da)
	}
	if video != nil && video.Kind == media.KindMuxed && audio != nil {
		if wantAudio {
			return nil, nil, fmt.Errorf("%w: track %q carries its own audio", ErrInvalidSelection, video.ID)
		}
		audio = nil
	}
	if video == nil && audio == nil {
		return nil, nil, fmt.Errorf("mediajob: %w: the manifest has no video or audio track", media.ErrNotFound)
	}
	return video, audio, nil
}

// find returns a copy of the chosen track: the one with key when key is set (whatever
// its ID is now), else the one with id. A key the manifest no longer has fails rather
// than falling back to the ID, which may now name another track.
func find(tracks []media.Track, what, id, key string) (*media.Track, error) {
	if key == "" {
		if t := byID(tracks, id); t != nil {
			return t, nil
		}
		return nil, fmt.Errorf("%w: %w: no track %q", ErrInvalidSelection, media.ErrNotFound, id)
	}
	for i := range tracks {
		if tracks[i].Key == key {
			return clone(&tracks[i]), nil
		}
	}
	return nil, fmt.Errorf("%w: %w: the manifest no longer offers the chosen %s track (%q when it was chosen)",
		ErrInvalidSelection, media.ErrNotFound, what, id)
}

func byID(tracks []media.Track, id string) *media.Track {
	for i := range tracks {
		if tracks[i].ID == id {
			return clone(&tracks[i])
		}
	}
	return nil
}

func clone(t *media.Track) *media.Track {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// assumedContainer is the container of t: the plan's, or else what the manifest
// format implies. HLS segments without EXT-X-MAP are transport streams (or packed
// audio, RFC 8216 §3), and DASH media segments are mostly ISO-BMFF. Run checks the
// assumption on the first bytes it receives.
func assumedContainer(t *track) media.Container {
	if t.container != media.ContainerUnknown {
		return t.container
	}
	if t.hls {
		return media.ContainerTS
	}
	return media.ContainerFMP4
}

// expectedContainer is the output of joining tracks as planned: outputContainer of their
// assumed containers (legacy is Options.LegacyTS). Fragmented MP4 without an
// initialization segment cannot be joined.
func expectedContainer(tracks []*track, legacy bool) (string, error) {
	cs := make([]media.Container, len(tracks))
	for i, t := range tracks {
		cs[i] = assumedContainer(t)
		if cs[i] == media.ContainerFMP4 && !planHasInit(t.plan) {
			return "", fmt.Errorf("mediajob: track %q: %w: fragmented MP4 without an initialization segment", t.id, media.ErrUnsupported)
		}
	}
	return outputContainer(cs, legacy)
}

func planHasInit(p *media.Plan) bool {
	for i := range p.Fragments {
		if p.Fragments[i].Init {
			return true
		}
	}
	return false
}

// outputContainer is the output of joining tracks of the given containers: "mp4" for
// one or two fragmented MP4 tracks and for one or two transport streams, which
// internal/remux converts (docs/VIDEO-DETECT-SPEC.md §4.1). With legacy
// (Options.LegacyTS) one transport stream stays "ts", joined byte for byte, and a TS
// video with a separate TS audio track cannot be joined. Other combinations are refused
// with an error wrapping media.ErrUnsupported.
func outputContainer(cs []media.Container, legacy bool) (string, error) {
	all := func(c media.Container) bool {
		for _, x := range cs {
			if x != c {
				return false
			}
		}
		return len(cs) > 0
	}
	switch {
	case len(cs) > 2:
	case all(media.ContainerFMP4):
		return "mp4", nil
	case all(media.ContainerTS) && legacy && len(cs) == 1:
		return "ts", nil
	case all(media.ContainerTS) && legacy:
		return "", fmt.Errorf("mediajob: %w: an MPEG-TS video track with a separate audio track cannot be joined into one .ts file",
			media.ErrUnsupported)
	case all(media.ContainerTS):
		return "mp4", nil
	}
	for _, c := range cs {
		if c == media.ContainerWebM {
			return "", fmt.Errorf("mediajob: %w: WebM/Matroska tracks cannot be saved as MP4", media.ErrUnsupported)
		}
	}
	return "", fmt.Errorf("mediajob: %w: cannot assemble tracks of containers %v", media.ErrUnsupported, cs)
}
