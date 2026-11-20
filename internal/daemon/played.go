package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"connectrpc.com/connect"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/played"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
	"downex/internal/validate"
)

// Save what's playing (docs/SITE-MEDIA-SPEC.md §2.9): a video saved from the files the
// user's browser played for it. AddMedia and ProbeMedia with origin PLAYED describe the
// files' heads (internal/played, pure), and the record keeps the chosen files' addresses
// (model.Media.Played); Config.Played downloads them.

// PlayedSelection is what a played run downloads: the chosen files (the video or muxed
// file first, then the audio file), how they are saved (played.OutputCopy or
// played.OutputRemux), and whether the record says its bytes come through the browser.
type PlayedSelection struct {
	Tracks       []model.PlayedTrack
	Output       string
	BrowserFetch bool
}

// PlayedMedia downloads played videos. downexd implements it over mediajob.RunRanged
// (cmd/downexd/played.go): direct requests first, the browser's when they are refused.
// Its arguments and results are those of SiteMedia.Run.
type PlayedMedia interface {
	Run(ctx context.Context, opts MediaRunOptions, sel PlayedSelection, resume *model.MediaCheckpoint,
		onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error)
}

// errNoPlayedMedia is returned while no PlayedMedia is configured.
var errNoPlayedMedia = fmt.Errorf("%w: this downexd cannot save played videos", ErrMediaUnsupported)

// errPlayedClip refuses a part of a played video: whole files only (§2.9 "Never").
var errPlayedClip = errors.New("a part of a played video cannot be saved yet: save the whole video")

// playedOrigin reports an API origin of PLAYED.
func playedOrigin(o downexv1.MediaOrigin) bool { return o == downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED }

// isPlayed reports a played video's record.
func isPlayed(med *model.Media) bool { return med != nil && med.Origin == played.OriginPlayed }

// isPageVideo reports a web page's video saved from direct files: a site's (yt-dlp) or a
// played one. Both list their tracks best first and measure progress in bytes.
func isPageVideo(med *model.Media) bool { return isSite(med) || isPlayed(med) }

// playedRequest is a validated request of origin PLAYED.
type playedRequest struct {
	pageURL      string
	src          played.Source
	video, audio string
}

// parsePlayed validates a PLAYED request (docs/SITE-MEDIA-SPEC.md §2.9 "Limits"):
// manifest_url empty, page_url a web page (the frame's address, a hint), no Cookie
// header, and a source within internal/played's limits. Its errors never repeat an
// address.
func parsePlayed(manifestURL, pageURL string, hs []*downexv1.Header, p *downexv1.PlayedSource, video, audio string) (playedRequest, error) {
	if manifestURL != "" {
		return playedRequest{}, errors.New("manifest_url must be empty for a played video (origin PLAYED)")
	}
	if err := siteURL(pageURL); err != nil {
		return playedRequest{}, fmt.Errorf("page_url: %w", err)
	}
	if u, err := url.Parse(pageURL); err == nil && played.YouTubeHost(u.Hostname()) {
		return playedRequest{}, errors.New("page_url: YouTube's videos are not saved this way")
	}
	headers, err := validate.Headers(hs)
	if err != nil {
		return playedRequest{}, err
	}
	if headers.Get("Cookie") != "" {
		return playedRequest{}, errors.New("a played video never carries a Cookie header")
	}
	for _, id := range []string{video, audio} {
		if err := validate.TrackID(id); err != nil {
			return playedRequest{}, err
		}
	}
	if p == nil {
		return playedRequest{}, errors.New("played is required for origin PLAYED")
	}
	src := played.Source{Element: played.Element{
		Duration: time.Duration(p.GetDurationMs()) * time.Millisecond, Width: int(p.GetVideoWidth()),
		Height: int(p.GetVideoHeight()), HasAudio: int(p.GetHasAudio()),
	}}
	if p.GetDurationMs() < 0 || p.GetDurationMs() > played.MaxDuration.Milliseconds() {
		return playedRequest{}, errors.New("played.duration_ms is out of range")
	}
	if len(p.GetOtherDurationsMs()) > played.MaxOthers {
		return playedRequest{}, fmt.Errorf("played: at most %d other elements' durations", played.MaxOthers)
	}
	for _, ms := range p.GetOtherDurationsMs() {
		if ms <= 0 || ms > played.MaxDuration.Milliseconds() {
			return playedRequest{}, errors.New("played.other_durations_ms is out of range")
		}
		src.Element.Others = append(src.Element.Others, time.Duration(ms)*time.Millisecond)
	}
	if len(p.GetResources()) > played.MaxResources {
		return playedRequest{}, fmt.Errorf("played: at most %d files", played.MaxResources)
	}
	for _, r := range p.GetResources() {
		src.Resources = append(src.Resources, played.Resource{ID: r.GetId(), URL: r.GetUrl(), Size: r.GetSize(), Kind: r.GetKind(),
			Head: r.GetHead(), Browser: r.GetBrowser()})
	}
	if err := played.Check(src); err != nil {
		return playedRequest{}, err
	}
	return playedRequest{pageURL: pageURL, src: src, video: video, audio: audio}, nil
}

// playedMediaOf describes and matches r's source (played.Probe) and converts the offer
// into the stored media description with the tracks r names, or the default pick.
func playedMediaOf(r playedRequest, title string) (*model.Media, error) {
	o, err := played.Probe(r.src)
	if err != nil {
		return nil, playedError(err)
	}
	med := &model.Media{
		Origin: played.OriginPlayed, Site: played.SiteName(r.pageURL), Format: sitemedia.FormatHTTP,
		SelectedVideo: o.DefaultVideo, SelectedAudio: o.DefaultAudio, Duration: max(o.Duration, 0),
		Container: sitemedia.ContainerMP4, Title: title, PageURL: r.pageURL,
	}
	if med.Site, err = validate.Title(med.Site); err != nil {
		med.Site = ""
	}
	for _, t := range o.Tracks {
		med.Tracks = append(med.Tracks, modelTrack(t))
	}
	if err := pickSiteTracks(med, r.video, r.audio); err != nil {
		return nil, err
	}
	// Every listed file, so that another quality chosen at the confirmation has its address.
	for _, t := range o.Tracks {
		f := o.Files[t.ID]
		med.Played = append(med.Played, model.PlayedTrack{ID: t.ID, Kind: string(f.Kind), URL: f.URL, Size: max(f.Size, 0),
			Fragmented: f.Fragmented, Browser: f.Browser})
	}
	med.BrowserFetch = playedViaBrowser(med)
	return med, nil
}

// playedError maps the errors of internal/played to the API's: an invalid source is
// invalid_request, no file of the video yet not_found_yet (the extension keeps waiting),
// protected files encrypted.
func playedError(err error) error {
	switch {
	case errors.Is(err, played.ErrInvalid):
		return mediaError(CodeInvalidRequest, connect.CodeInvalidArgument, err)
	case errors.Is(err, played.ErrNotFound):
		return mediaError(CodeNotFoundYet, connect.CodeFailedPrecondition, err)
	case errors.Is(err, media.ErrEncrypted):
		return mediaError(CodeEncrypted, connect.CodeInvalidArgument, err)
	}
	return mediaError(cmp.Or(mediaErrorCode(err), CodeMediaInvalid), connect.CodeInvalidArgument, err)
}

// probePlayed answers ProbeMedia with origin PLAYED: the offer of the source, with the
// default pick; nothing is fetched.
func (m *Manager) probePlayed(r *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	req, err := parsePlayed(r.GetManifestUrl(), r.GetPageUrl(), r.GetHeaders(), r.GetPlayed(), "", "")
	if err != nil {
		return nil, mediaError(CodeInvalidRequest, connect.CodeInvalidArgument, err)
	}
	if r.GetPrefetch() || r.GetEntryId() != "" || r.GetCookieDomain() != "" {
		return nil, mediaError(CodeInvalidRequest, connect.CodeInvalidArgument,
			errors.New("prefetch, entry_id and cookie_domain are not for played videos"))
	}
	med, err := playedMediaOf(req, "")
	if err != nil {
		return nil, err
	}
	return protoMedia(req.pageURL, med), nil
}

// runPlayed runs the job of played download rec (runMedia for origin played).
func (m *Manager) runPlayed(ctx context.Context, rec *model.Download, opts MediaRunOptions,
	onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	med := rec.Media
	sel := PlayedSelection{BrowserFetch: med.BrowserFetch || playedViaBrowser(med)}
	byID := map[string]model.PlayedTrack{}
	for _, t := range med.Played {
		byID[t.ID] = t
	}
	for _, id := range []string{med.SelectedVideo, med.SelectedAudio} {
		if id == "" {
			continue
		}
		t, ok := byID[id]
		if !ok {
			return "", fmt.Errorf("%w: the chosen file of this played video is not recorded; click Download on the video again",
				ErrMediaUnsupported)
		}
		sel.Tracks = append(sel.Tracks, t)
	}
	if len(sel.Tracks) == 0 || sel.Tracks[0].Kind == string(media.KindAudio) {
		return "", fmt.Errorf("%w: a played video needs its video file", ErrMediaUnsupported)
	}
	sel.Output = played.Output(media.TrackKind(sel.Tracks[0].Kind), sel.Tracks[0].Fragmented, len(sel.Tracks) == 2)
	return m.cfg.Played.Run(ctx, opts, sel, med.Checkpoint.Clone(), onProgress, onCheckpoint)
}

// playedViaBrowser reports a played video whose chosen file's head needed the tab: its
// bytes come through the browser from the start.
func playedViaBrowser(med *model.Media) bool {
	for _, t := range med.Played {
		if t.Browser && (t.ID == med.SelectedVideo || t.ID == med.SelectedAudio) {
			return true
		}
	}
	return false
}
