package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
	"downex/internal/transport"
	"downex/internal/validate"
)

// Site media (docs/SITE-MEDIA-SPEC.md): the video of a web page that yt-dlp understands,
// saved as one .mp4. AddMedia and ProbeMedia with origin SITE resolve the page through
// Config.Site (after Config.Tools has yt-dlp and Deno ready) within a wait budget, and
// record one media download whose Request is the page (only its Referer header is kept;
// a Cookie header lives in memory only, see siteCookies) and whose Media has origin
// "site", the offered MP4 qualities and the pick. Its runs resolve the page again and
// download the chosen tracks through Config.Site.Run; everything else (scheduling,
// checkpoints, completion, the part directory) is runMedia's, as for manifests.

// SiteSelection names a web page to resolve and, for a run, the tracks to download.
// Format is the format recorded at Add ("" for a probe): a run fails rather than switch
// between a direct format and a manifest. The keys find the chosen tracks again whatever
// ids the page gives them now. Prefetch marks the probe of a look-ahead
// (docs/SITE-MEDIA-SPEC.md §5.6): the resolver answers it from its cache or the running
// extraction of the page, starts one only when it has nothing else to do (else
// sitemedia.ErrSiteResolving at once), and caches its result for the probe, add and run
// that follow.
type SiteSelection struct {
	PageURL                string
	Cookie, Referer        string    // "" = none
	CookieDomain           string    // the domain of Cookie's sign-in (§6.3); "" = the page's host
	Proxy                  SiteProxy // the route for PageURL
	Format                 string    // "" (probe) | "http" | "hls" | "dash": the format recorded at Add
	VideoTrack, AudioTrack string
	VideoKey, AudioKey     string
	Prefetch               bool       // Probe only: a look-ahead
	Clip                   media.Clip // Run only: the part of the video (docs/MEDIA-SPEC.md §8)
	// EntryID is the video of a page with several (docs/SITE-MEDIA-SPEC.md §3.12) by the
	// site's id of it; "" = the page's default (a probe) or a page with one video. A run
	// of an item the page no longer has fails (site_unavailable), never takes another.
	EntryID string
	// BrowserFetch (Run only): the record says its bytes come through the browser
	// (model.Media.BrowserFetch, docs/SITE-MEDIA-SPEC.md §2.8).
	BrowserFetch bool
}

// SiteProxy is the proxy route of a page for yt-dlp: URL "" is direct, else
// "http://host:port", "https://host:port", "socks5h://" (a SOCKS5 proxy resolves host
// names, as Downex's own SOCKS5 routes do), "socks4://" or "socks4a://" without user
// information; the credentials, when known, are apart. It is never logged.
type SiteProxy struct{ URL, Username, Password string }

// SiteProbe describes a resolved page: MediaProbe's fields (Format "http", "hls" or
// "dash"; Container "mp4"; the offered tracks, best first; the default pick, or the
// tracks the selection named; FragmentsTotal 0 for "http"), the site's display name and
// the video's title.
type SiteProbe struct {
	MediaProbe
	Site, Title string
	// Chapters are the video's chapters, checked (docs/MEDIA-SPEC.md §8 C14).
	Chapters []model.Chapter
	// A page with several videos (docs/SITE-MEDIA-SPEC.md §3.12): the item described
	// (EntryID), the page's videos Downex can save (Items, with thumbnails) and how many
	// other items it has (ItemsLeftOut). EntryID "" and no items for a page with one video.
	EntryID      string
	Items        []model.MediaItem
	ItemsLeftOut int
	// BrowserFetch: the site's video servers answer only web browsers
	// (sitemedia.BrowserOnly, docs/SITE-MEDIA-SPEC.md §2.8).
	BrowserFetch bool
}

// SiteMedia resolves web pages and downloads their videos. downexd implements it with
// internal/sitejob, internal/sitemedia and internal/mediajob (cmd/downexd/site.go).
type SiteMedia interface {
	// Probe resolves sel's page (a cached extraction when one qualifies) and describes
	// every offered MP4 quality and the default pick, or the tracks sel names. It returns
	// by ctx's deadline: an extraction still running then fails with an error wrapping
	// sitemedia.ErrSiteResolving and goes on (its result is cached). Refusals wrap the
	// internal/media sentinels (encrypted, live, unsupported, malformed) or the
	// internal/sitemedia ones.
	Probe(ctx context.Context, tf transport.Factory, sel SiteSelection) (*SiteProbe, error)
	// Run resolves the page again, finds the chosen tracks by key and downloads them as
	// MediaJobs.Run does (the same options, checkpoints, progress and output rules); it
	// returns the container written, "mp4".
	Run(ctx context.Context, opts MediaRunOptions, sel SiteSelection, resume *model.MediaCheckpoint,
		onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (container string, err error)
}

// Tools finds, installs and updates the yt-dlp and Deno copies that SiteMedia runs
// (docs/SITE-MEDIA-SPEC.md §4). downexd implements it with internal/tools. Its methods
// are safe for concurrent use; installs belong to the implementation, never to a call.
type Tools interface {
	// Configure applies the site-media settings: whether site media is on, whether the
	// copies are updated automatically, and the yt-dlp channel.
	Configure(enabled, autoUpdate bool, channel string)
	// Status describes the tools now.
	Status() ToolsSnapshot
	// Ensure returns nil once the tools are ready, starting (or joining) their install
	// first when needed. At ctx's end it fails with an error wrapping
	// sitemedia.ErrToolsInstalling (with the progress, e.g. "(34%)"); it fails with
	// sitemedia.ErrToolsUnavailable when they cannot be set up and with
	// sitemedia.ErrSiteMediaOff while site media is off.
	Ensure(ctx context.Context) error
	// Install starts or joins the install (with update, after looking for newer releases)
	// and returns the status when it ends, or at ctx's end with an error wrapping
	// sitemedia.ErrToolsInstalling (the install goes on).
	Install(ctx context.Context, update bool) (ToolsSnapshot, error)
	// CheckUpdate looks for newer releases and installs them (the daily timer of
	// StartTools).
	CheckUpdate(ctx context.Context) error
	// Subscribe calls fn after every change of the status, until cancel is called. fn
	// must not block.
	Subscribe(fn func()) (cancel func())
}

// ToolState is the state of one tool (ToolState of the API).
type ToolState uint8

// Tool states.
const (
	ToolStateUnknown    ToolState = iota
	ToolStateMissing              // no copy yet
	ToolStateInstalling           // downloading or updating; a ready copy keeps working meanwhile
	ToolStateReady
	ToolStateFailed
	ToolStateDisabled // site media is off, or no build exists for this platform
)

// ToolSnapshot describes one tool (ToolInfo of the API, field for field).
type ToolSnapshot struct {
	Name          string // "yt-dlp" | "deno"
	State         ToolState
	Version       string // of the copy in use ("" = none)
	Path          string // absolute path of the copy in use
	Managed       bool   // true: Downex's tools folder; false: found on PATH
	LatestVersion string // last seen release ("" = not checked)
	CheckedAt     time.Time
	BytesDone     int64  // while installing
	BytesTotal    int64  // while installing; -1 = unknown
	Error         string // short text of the last failure, without URLs
	ErrorCode     string // network | tls | http_status | checksum | signature | authenticode | disk_full | ...
	Verified      string // "openpgp+sha256" | "sha256+authenticode" | "sha256" | "" (PATH copy)
}

// ToolsSnapshot is the status of the tools (ToolsStatus of the API). Ready is true when
// yt-dlp is ready and Deno is ready or disabled for the platform. Discovering (not in
// the API) is true while the copies have not been looked for yet since the start: Ready
// may then turn true without an install.
type ToolsSnapshot struct {
	Tools       []ToolSnapshot
	Enabled     bool
	Ready       bool
	Discovering bool
}

// Site-media timings (docs/SITE-MEDIA-SPEC.md §3.3).
const (
	// siteWaitBudget bounds how long AddMedia and ProbeMedia of a page wait for the tools
	// and the extraction together; the work goes on and the caller retries.
	siteWaitBudget = 25 * time.Second
	// siteDeadlineMargin is the time left to answer before the caller's deadline.
	siteDeadlineMargin = 2 * time.Second
	// siteInstallWait bounds the wait of a request for a page while yt-dlp or Deno is
	// being installed: it answers tools_installing with the progress after this long
	// rather than at the end of its budget, so that clients, which retry every 2 s, show
	// the install's progress as it goes.
	siteInstallWait = time.Second
	// maxToolError bounds the error text of a tool sent to clients.
	maxToolError = 200
)

// errNoSiteMedia is the error of site media in a daemon without Config.Site.
var errNoSiteMedia = fmt.Errorf("%w: this downexd cannot download videos from web pages", ErrMediaUnsupported)

// maxItems bounds the items of a page with several videos that are recorded and sent
// (docs/SITE-MEDIA-SPEC.md §3.12, sitemedia.MaxEntries).
const maxItems = 20

// errEntryNotSite refuses an item for a manifest: only a web page has items
// (docs/SITE-MEDIA-SPEC.md §3.12).
var errEntryNotSite = errors.New("entry_id is only for web pages (origin SITE)")

// errCookieDomainNotSite refuses a sign-in domain for a manifest: its headers go to the
// manifest's own origin (docs/SITE-MEDIA-SPEC.md §6.3).
var errCookieDomainNotSite = errors.New("cookie_domain is only for web pages (origin SITE)")

// errPrefetchCookie refuses a look-ahead that carries cookies: a look-ahead never does
// (docs/SITE-MEDIA-SPEC.md §5.6, §6.3).
var errPrefetchCookie = errors.New("a look-ahead (prefetch) never carries cookies")

// errNoMP4 refuses a page without an MP4 video Downex can save (an audio-only page, or a
// manifest that is not fragmented MP4).
var errNoMP4 = fmt.Errorf("%w: no MP4 video on this page", ErrMediaUnsupported)

// errNoAudio refuses an audio page's answer that is not audio tracks with a pick
// (docs/SITE-MEDIA-SPEC.md §3.13).
var errNoAudio = fmt.Errorf("%w: no AAC or MP3 audio on this page", ErrMediaUnsupported)

// errPrefetchTools refuses a look-ahead while yt-dlp and Deno are known not to be ready:
// only a user's request sets them up (docs/SITE-MEDIA-SPEC.md §5.6, §6.6).
var errPrefetchTools = fmt.Errorf("%w: a look-ahead waits until a download sets them up", sitemedia.ErrToolsUnavailable)

// ---- requests ---------------------------------------------------------------------

// siteRequest is a validated request for a web page (origin SITE).
type siteRequest struct {
	pageURL         string
	cookie, referer string // "" = none
	cookieDomain    string // the domain the cookie's sign-in is for (§6.3); "" = the page's host
	video, audio    string // requested track ids ("" = the default pick)
	entry           string // the item of a page with several videos ("" = the page's default)
	prefetch        bool   // a look-ahead (ProbeMedia only)
}

// isSiteOrigin reports whether an API origin names a web page; unknown values are
// refused.
func isSiteOrigin(o downexv1.MediaOrigin) (bool, error) {
	switch o {
	case downexv1.MediaOrigin_MEDIA_ORIGIN_UNSPECIFIED, downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST:
		return false, nil
	case downexv1.MediaOrigin_MEDIA_ORIGIN_SITE:
		return true, nil
	}
	return false, fmt.Errorf("unknown media origin %d", int32(o))
}

// parseSiteRequest validates a request for a web page (docs/SITE-MEDIA-SPEC.md §3.3):
// page_url is required and manifest_url must be empty; the headers pass validate.Headers
// and only Cookie (joined) and Referer (an http(s) URL) are used, every other header is
// ignored; track ids are validated like Add's, the item's id with validate.EntryID
// (§3.12); the Cookie (at most 16 KiB) and the domain of its sign-in pass
// validate.SiteCookie (§6.3: a domain that holds the page and is no public suffix).
// page_url and Referer never carry user information: they reach yt-dlp's arguments,
// which other users of the machine can read.
func parseSiteRequest(manifestURL, pageURL string, hs []*downexv1.Header, video, audio, entry, cookieDomain string) (siteRequest, error) {
	if manifestURL != "" {
		return siteRequest{}, errors.New("manifest_url must be empty for a web page (origin SITE); send the page as page_url")
	}
	if err := siteURL(pageURL); err != nil {
		return siteRequest{}, fmt.Errorf("page_url: %w", err)
	}
	headers, err := validate.Headers(hs)
	if err != nil {
		return siteRequest{}, err
	}
	for _, id := range []string{video, audio} {
		if err := validate.TrackID(id); err != nil {
			return siteRequest{}, err
		}
	}
	if err := validate.EntryID(entry); err != nil {
		return siteRequest{}, err
	}
	req := siteRequest{pageURL: pageURL, video: video, audio: audio, entry: entry}
	var cookies []string
	for _, c := range headers.Values("Cookie") {
		if c = strings.TrimSpace(c); c != "" {
			cookies = append(cookies, c)
		}
	}
	req.cookie = strings.Join(cookies, "; ")
	if err := validate.SiteCookie(pageURL, req.cookie, cookieDomain); err != nil {
		return siteRequest{}, err
	}
	req.cookieDomain = cookieDomain
	if ref := headers.Get("Referer"); ref != "" {
		if err := siteURL(ref); err != nil {
			return siteRequest{}, fmt.Errorf("header Referer: %w", err)
		}
		// A browser never sends a fragment: it is not passed on (VIDEO-DETECT-SPEC §6.3).
		ref, _, _ = strings.Cut(ref, "#")
		req.referer = ref
	}
	return req, nil
}

// siteURL checks a page or Referer URL of a site request: validate.URL, and no user
// information. Its errors never repeat the URL.
func siteURL(raw string) error {
	if err := validate.URL(raw); err != nil {
		return err
	}
	if u, err := url.Parse(raw); err != nil || u.User != nil {
		return errors.New("url must not carry a user name or password")
	}
	return nil
}

// selection is the probe selection of r.
func (r siteRequest) selection() SiteSelection {
	return SiteSelection{PageURL: r.pageURL, Cookie: r.cookie, CookieDomain: r.cookieDomain, Referer: r.referer,
		VideoTrack: r.video, AudioTrack: r.audio,
		Prefetch: r.prefetch, EntryID: r.entry}
}

// ---- probing ----------------------------------------------------------------------

// probeSite resolves the page of r for AddMedia and ProbeMedia: site media must be on and
// configured; Config.Tools.Ensure and Config.Site.Probe share the wait budget
// (siteBudget), at whose end the answer is tools_installing or site_resolving while the
// install or the extraction goes on. While the tools are being installed, the answer
// comes after siteInstallWait (ensureTools). Refusals carry their code (siteError).
//
// A look-ahead (r.prefetch, docs/SITE-MEDIA-SPEC.md §5.6) never calls Ensure, which
// starts installs: tools known not to be ready answer tools_unavailable at once. While
// the copies have not been looked for yet since the start (Discovering), it goes on: the
// resolver's tools wait for that discovery, which never installs, and answer
// tools_unavailable themselves when it finds nothing. The resolver answers it as a
// probe, except that it never waits behind other extractions (site_resolving at once).
func (m *Manager) probeSite(ctx context.Context, r siteRequest) (*SiteProbe, error) {
	if !m.Settings().Media.SiteMedia {
		return nil, mediaError(CodeSiteMediaOff, connect.CodeFailedPrecondition, sitemedia.ErrSiteMediaOff)
	}
	if m.cfg.Site == nil {
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument, errNoSiteMedia)
	}
	bctx, cancel := siteBudget(ctx)
	defer cancel()
	sel := r.selection()
	proxy, err := m.siteProxy(bctx, r.pageURL)
	if err != nil {
		return nil, siteError(ctx, err, sitemedia.ErrSiteError)
	}
	sel.Proxy = proxy
	if t := m.cfg.Tools; t != nil {
		var err error
		if !r.prefetch {
			err = ensureTools(bctx, t)
		} else if s := t.Status(); !s.Ready && !s.Discovering {
			err = errPrefetchTools
		}
		if err != nil {
			return nil, siteError(ctx, err, sitemedia.ErrToolsInstalling)
		}
	}
	info, err := m.cfg.Site.Probe(bctx, m.cfg.Transport, sel)
	switch {
	case err != nil:
		return nil, siteError(ctx, err, sitemedia.ErrSiteResolving)
	case info == nil:
		return nil, errInternal("resolve page", errors.New("no result"))
	}
	return info, nil
}

// ensureTools waits for the tools within ctx, a request's budget (Tools.Ensure, which
// also starts their install). While an install runs it answers after siteInstallWait at
// most, with Ensure's tools_installing and the progress; a discovery of the copies is
// waited for as long as ctx allows.
func ensureTools(ctx context.Context, t Tools) error {
	wctx, cancel := context.WithTimeout(ctx, siteInstallWait)
	err := t.Ensure(wctx)
	cancel()
	if err == nil || ctx.Err() != nil || !errors.Is(err, sitemedia.ErrToolsInstalling) || toolsInstalling(t.Status()) {
		return err
	}
	return t.Ensure(ctx)
}

// toolsInstalling reports tools that are not ready while one of them is being
// installed.
func toolsInstalling(s ToolsSnapshot) bool {
	return !s.Ready && slices.ContainsFunc(s.Tools, func(t ToolSnapshot) bool { return t.State == ToolStateInstalling })
}

// siteBudget bounds the wait of one request for a page: siteWaitBudget, ending
// siteDeadlineMargin before the caller's deadline when that comes sooner.
func siteBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	end := time.Now().Add(siteWaitBudget)
	if d, ok := ctx.Deadline(); ok {
		if e := d.Add(-siteDeadlineMargin); e.Before(end) {
			end = e
		}
	}
	return context.WithDeadline(ctx, end)
}

// siteError maps a failure of site media for a request: the caller's end (cancelled or
// past its deadline) as is, whatever the step returned; the end of the wait budget (a
// context error without a site sentinel) as timeout, the sentinel of the step that was
// waiting (tools_installing or site_resolving). Refusals of the page (encrypted, live,
// invalid, unsupported, no video) are InvalidArgument, conditions that may change
// (unavailable, sign-in, yt-dlp failures, tools, retries, settings, the network)
// FailedPrecondition, anything else Internal. The message starts with the code, which is
// also sent as ErrorCodeMeta.
func siteError(ctx context.Context, err, timeout error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if isContextError(err) && !hasSiteSentinel(err) {
		err = timeout
	}
	switch code := mediaErrorCode(err); code {
	case CodeEncrypted, CodeLive, CodeMediaInvalid, CodeMediaUnsupported, CodeInvalidRequest, CodeSiteUnsupported:
		return mediaError(code, connect.CodeInvalidArgument, err)
	case CodeSiteUnavailable, CodeAuthRequired, CodeSiteError, CodeSiteResolving, CodeToolsInstalling,
		CodeToolsUnavailable, CodeSiteMediaOff, CodeNetwork, CodeTLS, CodeHTTPStatus:
		return mediaError(code, connect.CodeFailedPrecondition, err)
	default:
		return mediaError(code, connect.CodeInternal, err)
	}
}

// hasSiteSentinel reports whether err wraps one of the site-media sentinels.
func hasSiteSentinel(err error) bool {
	return slices.ContainsFunc(siteCodes, func(c sentinelCode) bool { return errors.Is(err, c.err) })
}

// siteProxy returns the route the network settings give pageURL, for yt-dlp
// (docs/SITE-MEDIA-SPEC.md §3.3): direct without Config.Network or when the first route
// is direct, else the first proxy with its credentials when Downex has them (the route's
// own, or the stored login of a manual proxy). A proxy that signs in as the current
// user (NTLM/Negotiate) is given without credentials: yt-dlp cannot do that sign-in, and
// fails if the proxy asks for it.
func (m *Manager) siteProxy(ctx context.Context, pageURL string) (SiteProxy, error) {
	rt := m.cfg.Network
	if rt == nil {
		return SiteProxy{}, nil
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return SiteProxy{}, fmt.Errorf("%w: the page address is malformed", sitemedia.ErrSiteError)
	}
	routes, err := rt.ResolveProxy(ctx, u)
	if err != nil {
		if isContextError(err) {
			return SiteProxy{}, err
		}
		return SiteProxy{}, fmt.Errorf("%w: cannot find the proxy for this page: %v", sitemedia.ErrSiteError, err)
	}
	for _, p := range routes {
		switch p.Kind {
		case transport.ProxyDirect:
			return SiteProxy{}, nil
		case transport.ProxyHTTP, transport.ProxyHTTPS, transport.ProxySOCKS5, transport.ProxySOCKS4, transport.ProxySOCKS4A:
			scheme := p.Kind.Scheme()
			if p.Kind == transport.ProxySOCKS5 {
				// yt-dlp resolves host names itself for socks5, through the proxy for socks5h.
				scheme = "socks5h"
			}
			sp := SiteProxy{URL: scheme + "://" + p.Addr, Username: p.Username, Password: p.Password}
			if sp.Username == "" && sp.Password == "" {
				if l, ok := rt.ProxyLogin(p); ok && !l.CurrentUser {
					sp.Username, sp.Password = l.Username, l.Password
				}
			}
			return sp, nil
		}
	}
	return SiteProxy{}, nil
}

// ---- recording --------------------------------------------------------------------

// mediaFromSiteProbe converts a resolved page into the stored media description: origin
// site, format http/hls/dash, container mp4 and a pick with a video (audio alone is never
// a download of a video page), checked like a manifest's selection. An audio page's
// answer (docs/SITE-MEDIA-SPEC.md §3.13: container m4a or mp3) is audio tracks only and
// a pick of one of them, without video.
func mediaFromSiteProbe(info *SiteProbe, pageURL string) (*model.Media, error) {
	switch info.Format {
	case "http", "hls", "dash":
	default:
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument,
			fmt.Errorf("%w: unknown format %q", ErrMediaUnsupported, truncate(info.Format, 32)))
	}
	audio := sitemedia.IsAudioContainer(info.Container)
	if info.Container != sitemedia.ContainerMP4 && !audio {
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument, errNoMP4)
	}
	med := &model.Media{
		Origin: sitemedia.OriginSite, Site: info.Site, Format: info.Format,
		Tracks:        make([]model.MediaTrack, 0, len(info.Tracks)),
		SelectedVideo: info.DefaultVideo, SelectedAudio: info.DefaultAudio, Duration: max(info.Duration, 0),
		Container: info.Container, FragmentsTotal: max(info.FragmentsTotal, 0), Title: info.Title, PageURL: pageURL,
		Chapters: checkChapters(info.Chapters, max(info.Duration, 0)),
		EntryID:  info.EntryID, ItemsLeftOut: max(info.ItemsLeftOut, 0), BrowserFetch: info.BrowserFetch,
	}
	for _, it := range info.Items {
		if len(med.Items) == maxItems {
			med.ItemsLeftOut++
			continue
		}
		if validate.EntryID(it.ID) != nil || it.ID == "" {
			continue
		}
		title, err := validate.Title(it.Title)
		if err != nil {
			title = ""
		}
		med.Items = append(med.Items, model.MediaItem{ID: it.ID, Title: title, Duration: max(it.Duration, 0),
			Thumbnail: it.Thumbnail})
	}
	for _, t := range info.Tracks {
		med.Tracks = append(med.Tracks, modelTrack(t))
	}
	switch {
	case audio && (med.SelectedVideo != "" || med.SelectedAudio == "" ||
		slices.ContainsFunc(med.Tracks, func(t model.MediaTrack) bool { return t.Kind != string(media.KindAudio) })):
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument, errNoAudio)
	case !audio && med.SelectedVideo == "":
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument, errNoMP4)
	}
	if err := checkSelection(med, med.SelectedVideo, med.SelectedAudio); err != nil {
		return nil, err
	}
	return med, nil
}

// modelTrack converts a probed track to its stored form (negative numbers become 0, a
// frame rate that is not finite 0).
func modelTrack(t media.Track) model.MediaTrack {
	fr := t.FrameRate
	if math.IsNaN(fr) || math.IsInf(fr, 0) || fr < 0 {
		fr = 0
	}
	return model.MediaTrack{
		ID: t.ID, Kind: string(t.Kind), Bandwidth: max(t.Bandwidth, 0), Width: max(t.Width, 0),
		Height: max(t.Height, 0), FrameRate: fr, Codecs: t.Codecs, Language: t.Language, Name: t.Name,
		Key: t.Key, Size: max(t.Size, 0), SizeApprox: t.SizeApprox && t.Size > 0, Ext: t.Ext,
	}
}

// pickSiteTracks applies the tracks a request named to a resolved page's media: the
// resolver normally resolved them already; otherwise they are chosen as Confirm chooses
// them (selectMediaTracks). A pick without video is refused, except of an audio page
// (docs/SITE-MEDIA-SPEC.md §3.13), which has no video.
func pickSiteTracks(med *model.Media, video, audio string) error {
	if (video == "" || video == med.SelectedVideo) && (audio == "" || audio == med.SelectedAudio) {
		return nil
	}
	tmp := model.Download{Media: med}
	if err := selectMediaTracks(&tmp, video, audio); err != nil {
		return err
	}
	*med = *tmp.Media
	if med.SelectedVideo == "" && !isAudioMedia(med) {
		return mediaError(CodeMediaInvalid, connect.CodeInvalidArgument, errors.New("a video download needs a video track"))
	}
	return nil
}

// siteExpectedSize is the size of a site download's pick: the sum of the chosen tracks'
// sizes (estimates included), or 0 (unknown) when one of them has none.
func siteExpectedSize(med *model.Media) int64 {
	var total int64
	for _, id := range []string{med.SelectedVideo, med.SelectedAudio} {
		if id == "" {
			continue
		}
		t := med.Track(id)
		if t == nil || t.Size <= 0 {
			return 0
		}
		total += t.Size
	}
	return total
}

// siteFileName names a site download: the requested name, else the site's title, else
// the requested title, else the page path's last segment, else "video"; sanitised, with
// the container's extension: ".mp4", or an audio page's ".m4a" or ".mp3"
// (docs/SITE-MEDIA-SPEC.md §3.13).
func siteFileName(requested, siteTitle, title, pageURL, container string) string {
	return mediaFileName(cmp.Or(requested, siteTitle, title, pageSegment(pageURL)), "", "", container)
}

// isAudioMedia reports the download of an audio page (docs/SITE-MEDIA-SPEC.md §3.13): a
// site download saved as .m4a or .mp3.
func isAudioMedia(med *model.Media) bool {
	return isSite(med) && sitemedia.IsAudioContainer(med.Container)
}

// siteTitle is the title a site download is named by (siteFileName): the video's, and for
// an item of a page with several videos whose title another item has too (a carousel's
// "Video by …"), that title and the item's position, "Video by example 2"
// (docs/SITE-MEDIA-SPEC.md §3.12).
func siteTitle(med *model.Media) string {
	if med.EntryID == "" || med.Title == "" {
		return med.Title
	}
	pos, same := 0, false
	for i, it := range med.Items {
		switch {
		case it.ID == med.EntryID:
			pos = i + 1
		case it.Title == med.Title:
			same = true
		}
	}
	if pos == 0 || !same {
		return med.Title
	}
	return fmt.Sprintf("%s %d", med.Title, pos)
}

// pageSegment is the last non-empty path segment of rawURL ("" when there is none).
func pageSegment(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	return segs[len(segs)-1]
}

// isSite reports whether med comes from a web page.
func isSite(med *model.Media) bool { return med != nil && med.Origin == sitemedia.OriginSite }

// ---- running ----------------------------------------------------------------------

// runSite runs the job of site download rec (runMedia for origin site): site media must
// still be on, Config.Tools.Ensure waits for the tools (a pause cancels the wait), then
// Config.Site.Run resolves the page again with the cookie the download was added with
// (when this daemon still has it) and the route of the network settings, and downloads
// the tracks chosen by key.
func (m *Manager) runSite(ctx context.Context, rec *model.Download, opts MediaRunOptions,
	onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	if !m.Settings().Media.SiteMedia {
		return "", sitemedia.ErrSiteMediaOff
	}
	if t := m.cfg.Tools; t != nil {
		if err := t.Ensure(ctx); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", err
		}
	}
	med := rec.Media
	signIn := m.siteCookies.get(rec.ID)
	sel := SiteSelection{
		PageURL: rec.Request.URL, Cookie: signIn.cookie, CookieDomain: signIn.domain, Referer: rec.Request.Headers.Get("Referer"),
		Format: med.Format, VideoTrack: med.SelectedVideo, AudioTrack: med.SelectedAudio,
		VideoKey: trackKey(med, med.SelectedVideo), AudioKey: trackKey(med, med.SelectedAudio),
		Clip: clipOf(med), EntryID: med.EntryID, BrowserFetch: med.BrowserFetch,
	}
	proxy, err := m.siteProxy(ctx, sel.PageURL)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	sel.Proxy = proxy
	return m.cfg.Site.Run(ctx, opts, sel, med.Checkpoint.Clone(), onProgress, onCheckpoint)
}

// ---- cookies ----------------------------------------------------------------------

// maxSiteCookies bounds the cookies kept; the oldest downloads' are dropped first.
const maxSiteCookies = 1024

// siteCookies keeps the sign-in a site download was added with (its Cookie header and
// that sign-in's domain, docs/SITE-MEDIA-SPEC.md §6.3) in memory only, so that its runs
// resolve the page the way it was probed, and the resolver reuses that extraction.
// Cookies are never stored or logged: a restarted daemon resolves without them. Safe
// for concurrent use.
type siteCookies struct {
	mu sync.Mutex
	m  map[model.DownloadID]siteSignIn
}

// siteSignIn is the sign-in of a site download: a Cookie header and its domain ("" =
// the page's host).
type siteSignIn struct{ cookie, domain string }

// set remembers c for download id (a cookie "" forgets it).
func (s *siteCookies) set(id model.DownloadID, c siteSignIn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.cookie == "" {
		delete(s.m, id)
		return
	}
	if s.m == nil {
		s.m = make(map[model.DownloadID]siteSignIn)
	}
	if _, ok := s.m[id]; !ok && len(s.m) >= maxSiteCookies {
		delete(s.m, slices.Min(slices.Collect(maps.Keys(s.m))))
	}
	s.m[id] = c
}

// get returns the sign-in of download id (a cookie "" = none).
func (s *siteCookies) get(id model.DownloadID) siteSignIn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}
