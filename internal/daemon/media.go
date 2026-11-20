package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/bits"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/limiter"
	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
	"downex/internal/validate"
)

// Streaming-media downloads (docs/MEDIA-SPEC.md §4–§6).
//
// A media download is a record of kind model.KindMedia whose Request is the manifest
// request and whose Media holds the presentation's tracks, the selection and the
// fragment progress. The scheduler runs it like any other download (one slot of
// max_concurrent_downloads, a child of the global limiter, pause/resume/remove
// through the run's intent), but through Config.Media instead of the engine: the job
// downloads the fragments into the part directory "<dir>/<filename>.dxparts/",
// assembles them into "<dir>/<filename>.dxpart" and renames that to the final file.
// The run goroutine owns the record while it runs: it stores the job's checkpoints
// (bitmaps of durable fragments), which a later run resumes from. Remove with
// delete_file deletes the part file and the part directory.

// MediaJobs runs streaming-media jobs. downexd implements it with internal/mediajob;
// the types mirror that package's (MediaSelection, MediaProbe and MediaProgress have
// its fields in its order, so they convert directly).
type MediaJobs interface {
	// Probe fetches and parses the manifest(s) of sel (each at most 8 MiB), lists the
	// tracks that can be saved as an MP4 (docs/VIDEO-DETECT-SPEC.md §3.3) and resolves
	// its track selection among them. Encrypted or DRM-protected content fails with an
	// error wrapping media.ErrEncrypted, a live presentation with media.ErrLive, one
	// without an MP4 video (audio only included) with errors.ErrUnsupported.
	Probe(ctx context.Context, tf transport.Factory, sel MediaSelection) (*MediaProbe, error)
	// Run downloads the selected tracks into opts.PartDir, resuming from resume (nil:
	// from scratch), and assembles them into opts.OutPath (written as
	// OutPath+".dxpart", then renamed; never replacing an existing file). It returns the
	// container written, "mp4" ("ts" only for opts.LegacyTS), and leaves PartDir, which
	// runMedia removes once the completion is stored; until then a Run finds the finished
	// output there and returns at once, so a completion that was not stored is not
	// downloaded again.
	// onProgress is called at most 4 times per second; onCheckpoint after the
	// fragments it reports are durable (at most every 2 s, and once more when Run
	// stops). Cancelling ctx pauses the job: Run saves a checkpoint and returns ctx's
	// error. Combinations the assembler does not handle fail with an error wrapping
	// errors.ErrUnsupported or ErrMediaUnsupported.
	Run(ctx context.Context, opts MediaRunOptions, sel MediaSelection, resume *model.MediaCheckpoint,
		onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (container string, err error)
}

// MediaSelection names a presentation and the tracks to download ("" = the default pick).
// VideoKey and AudioKey are the Keys of the chosen tracks as probed ("" = none): they
// find the tracks again when the manifest now lists them under other IDs.
type MediaSelection struct {
	ManifestURL            string
	Headers                http.Header
	VideoTrack, AudioTrack string
	VideoKey, AudioKey     string
	// Clip is the part of the video to save (docs/MEDIA-SPEC.md §8); zero = all of it.
	Clip media.Clip
}

// MediaProbe describes a probed presentation and its resolved selection.
type MediaProbe struct {
	Format                     string // "hls" | "dash"
	Tracks                     []media.Track
	DefaultVideo, DefaultAudio string // the tracks the selection resolves to ("" = none)
	Duration                   time.Duration
	Container                  string // expected output: "mp4" (anything else is refused)
	FragmentsTotal             int
}

// MediaProgress is a snapshot of a running media job.
type MediaProgress struct {
	FragmentsTotal int
	FragmentsDone  int
	BytesDone      int64
	BytesTotal     int64 // size of the selected tracks when known, an estimate when BytesTotalApprox, else 0
	// BytesTotalApprox marks a BytesTotal estimated from the durable fragments of a stream.
	BytesTotalApprox bool
	SpeedBps         float64
	Connections      int
	// Workers is one view per fragment request slot (Fragment = the fragment it fetches,
	// Start/End its byte range or -1); rendered as Download.connection_info, never stored.
	Workers []model.WorkerView
	// Clip describes the assembled part (docs/MEDIA-SPEC.md §8 C20): set only once the
	// output of a run with a part is complete.
	Clip *model.MediaClip
}

// MediaRunOptions configures one MediaJobs.Run (and SiteMedia.Run).
type MediaRunOptions struct {
	Factory        transport.Factory
	Limiter        limiter.Limiter // this download's child of the global limiter
	MaxConnections int
	PartDir        string // "<dir>/<filename>.dxparts"
	OutPath        string // "<dir>/<filename>"
	// HostRules are the per-host limits of the network settings (Config.Engine.HostRules:
	// connection caps, shared speed limiters and, through engine.RequestCaps, the request
	// length cap); nil applies none.
	HostRules engine.HostRules
	// LegacyTS is set for a "ts" record with a checkpoint (stored before 2026-10-23):
	// keep the byte-for-byte .ts join (docs/VIDEO-DETECT-SPEC.md §3.3). SiteMedia.Run
	// ignores it.
	LegacyTS bool
	// ReplaceExisting: a completed download downloaded again to replace its file
	// (model.Download.ReplaceFile, docs/ENGINE-SPEC.md §7.1): the assembled output renames
	// over a regular file at OutPath (never a folder or a link); without it an existing
	// OutPath is never replaced.
	ReplaceExisting bool
	// Browser, set for a site download a browser fetches for (docs/SITE-MEDIA-SPEC.md
	// §2.8), returns the transport whose requests that browser makes; sizeOf gives the exact
	// size of the resource at an address (0 = unknown). nil: no browser fetches for it.
	// OnBrowser is called when a run goes on through the browser after its own requests
	// were refused: it marks the record (model.Media.BrowserFetch). SiteMedia only.
	Browser   func(sizeOf func(url string) int64) transport.Factory
	OnBrowser func()
}

// ErrMediaUnsupported reports a stream or track combination that cannot be saved yet
// (error code media_unsupported). Errors wrapping errors.ErrUnsupported are treated alike.
var ErrMediaUnsupported = errors.New("media: this stream cannot be saved yet")

// errNoMediaJobs is returned while no MediaJobs is configured.
var errNoMediaJobs = fmt.Errorf("%w: this downexd cannot download streaming media", ErrMediaUnsupported)

// errNoMP4Stream refuses a manifest without an MP4 video Downex can save (an audio-only
// stream, or one whose tracks are saved as another container).
var errNoMP4Stream = fmt.Errorf("%w: no MP4 video in this stream", ErrMediaUnsupported)

// ErrorCodeMeta is the response header (Connect error metadata) that carries the stable
// error code of a refused AddMedia or ProbeMedia; the error message starts with the
// same code followed by ": ".
const ErrorCodeMeta = "Downex-Error-Code"

const (
	// mediaProbeTimeout bounds fetching and parsing the manifests of AddMedia/ProbeMedia.
	mediaProbeTimeout = 60 * time.Second
	// mediaPartsExt is appended to a media download's file name to name its part
	// directory.
	mediaPartsExt = ".dxparts"
)

// MediaPartsDir is the part directory of a media download saved as name in dir.
func MediaPartsDir(dir, name string) string { return filepath.Join(dir, name+mediaPartsExt) }

// mediaOutputRecord is the file in a part directory that records an assembled output
// (mediajob's outputRecordName): a resume finishes from it without fetching.
const mediaOutputRecord = "output.json"

// mediaResumable reports whether stopped media download rec has anything a resume
// reuses: durable fragments, chunk prefixes or a part's head in its checkpoint.
func mediaResumable(rec *model.Download) bool {
	if rec.Media == nil || rec.Media.Checkpoint == nil {
		return false
	}
	cp := rec.Media.Checkpoint
	if cp.DurableBytes > 0 {
		return true
	}
	if _, done := checkpointCounts(cp); done > 0 {
		return true
	}
	for _, t := range cp.Tracks {
		if len(t.Partial) > 0 || t.Head > 0 {
			return true
		}
	}
	return false
}

// dropDeadParts deletes the part directory of media download rec, which has just
// failed, when nothing in it can be resumed (docs/DAEMON-SPEC.md, amendment
// 2026-11-19): no durable progress in its checkpoint and no record of an assembled
// output. Only Downex's own <name>.dxparts directory is touched, never the file.
func (m *Manager) dropDeadParts(rec *model.Download) {
	if rec.Kind != model.KindMedia || rec.Filename == "" || mediaResumable(rec) {
		return
	}
	parts := MediaPartsDir(rec.Dir, rec.Filename)
	if _, err := os.Lstat(filepath.Join(parts, mediaOutputRecord)); err == nil {
		return
	}
	if err := os.RemoveAll(parts); err != nil {
		m.log.Warn("cannot delete the part directory of a failed media download", "download", int64(rec.ID), "err", err)
	}
}

// ---- AddMedia / ProbeMedia --------------------------------------------------------

// mediaInput is a validated AddMediaRequest: a manifest (sel) or, with site, a web page
// (page).
type mediaInput struct {
	sel         MediaSelection
	site        bool
	page        siteRequest
	played      *playedRequest // origin PLAYED (docs/SITE-MEDIA-SPEC.md §2.9)
	dir         string
	dirGiven    bool
	filename    string // sanitised, "" = from the title or manifest
	title       string
	pageURL     string
	source      model.Source
	startPaused bool
	clip        *requestedClip // the part asked for (nil: the whole video)
}

// parseMediaSelection validates a manifest URL and its headers like Add's
// (docs/DAEMON-SPEC.md §4) and the requested track ids.
func parseMediaSelection(manifestURL string, hs []*downexv1.Header, video, audio string) (MediaSelection, error) {
	if err := validate.URL(manifestURL); err != nil {
		return MediaSelection{}, fmt.Errorf("manifest_url: %w", err)
	}
	headers, err := validate.Headers(hs)
	if err != nil {
		return MediaSelection{}, err
	}
	for _, id := range []string{video, audio} {
		if err := validate.TrackID(id); err != nil {
			return MediaSelection{}, err
		}
	}
	return MediaSelection{ManifestURL: manifestURL, Headers: headers, VideoTrack: video, AudioTrack: audio}, nil
}

func parseAddMedia(r *downexv1.AddMediaRequest, defaultDir string) (mediaInput, error) {
	in := mediaInput{startPaused: r.GetStartPaused(), source: sourceFromProto(r.GetSource())}
	var err error
	if playedOrigin(r.GetOrigin()) {
		switch {
		case r.GetEntryId() != "":
			return in, errEntryNotSite
		case r.GetCookieDomain() != "":
			return in, errCookieDomainNotSite
		case r.GetClip() != nil:
			return in, errPlayedClip
		}
		p, err := parsePlayed(r.GetManifestUrl(), r.GetPageUrl(), r.GetHeaders(), r.GetPlayed(), r.GetVideoTrack(), r.GetAudioTrack())
		if err != nil {
			return in, err
		}
		in.played = &p
	} else if r.GetPlayed() != nil {
		return in, errors.New("played is only for origin PLAYED")
	} else if in.site, err = isSiteOrigin(r.GetOrigin()); err != nil {
		return in, err
	}
	switch {
	case in.played != nil:
	case in.site:
		if in.page, err = parseSiteRequest(r.GetManifestUrl(), r.GetPageUrl(), r.GetHeaders(), r.GetVideoTrack(),
			r.GetAudioTrack(), r.GetEntryId(), r.GetCookieDomain()); err != nil {
			return in, err
		}
	case r.GetEntryId() != "":
		return in, errEntryNotSite
	case r.GetCookieDomain() != "":
		return in, errCookieDomainNotSite
	default:
		if in.sel, err = parseMediaSelection(r.GetManifestUrl(), r.GetHeaders(), r.GetVideoTrack(), r.GetAudioTrack()); err != nil {
			return in, err
		}
	}
	switch dir := r.GetDir(); {
	case dir != "":
		if in.dir, err = checkDir(dir, defaultDir); err != nil {
			return in, err
		}
		in.dirGiven = true
	case defaultDir == "":
		return in, errors.New("dir is required: no default download directory is configured")
	default:
		in.dir = defaultDir
	}
	if in.filename, err = validate.Filename(r.GetFilename()); err != nil {
		return in, err
	}
	if in.title, err = validate.Title(r.GetTitle()); err != nil {
		return in, err
	}
	switch p := r.GetPageUrl(); {
	case in.played != nil:
		in.pageURL = in.played.pageURL
	case in.site:
		in.pageURL = in.page.pageURL
	case p != "":
		if err := validate.URL(p); err != nil {
			return in, fmt.Errorf("page_url: %w", err)
		}
		in.pageURL = p
	}
	return in, nil
}

// AddMedia adds a streaming-media download (docs/MEDIA-SPEC.md §6 step 3). It validates
// r like Add, fetches and parses the manifest (Config.Media's Probe; an encrypted
// stream is InvalidArgument with code "encrypted", a live one "live"; see errMediaProbe),
// and records the download with its tracks, the selected (else the default) tracks,
// format, duration and title. Only an MP4 with a video is recorded
// (docs/VIDEO-DETECT-SPEC.md §3.3; anything else is media_unsupported). The category is
// Video; the folder and the confirmation of browser hand-offs follow Add's rules; the
// file name is the requested one, else the title, else the manifest's path, with
// ".mp4". Nothing is created on disk until the download starts.
//
// With origin SITE (docs/SITE-MEDIA-SPEC.md §3.3) page_url is a web page, resolved with
// probeSite (codes of §3.2; tools_installing and site_resolving ask the caller to retry):
// the download's URL is the page, its media has origin "site", every offered MP4 quality
// with its size, and the pick (always with a video); the name is the requested one, else
// the site's title, else the requested title, else the page's last path segment, with
// ".mp4"; expected_size is the size of the pick when known. Only the Referer header is
// stored; a Cookie header is kept in memory for the download's runs (siteCookies).
func (m *Manager) AddMedia(ctx context.Context, r *downexv1.AddMediaRequest) (*downexv1.Download, error) {
	d, _, err := m.addMedia(ctx, r)
	return d, err
}

// addMedia is AddMedia, and the download's fetch ticket when the request asked for one
// (browser_fetch: a site download added from the browser, docs/SITE-MEDIA-SPEC.md §2.8);
// "" otherwise.
func (m *Manager) addMedia(ctx context.Context, r *downexv1.AddMediaRequest) (*downexv1.Download, string, error) {
	m.mu.Lock()
	closed, s := m.closed, m.settings
	m.mu.Unlock()
	if closed {
		return nil, "", errShuttingDown()
	}
	in, err := parseAddMedia(r, s.DefaultDir)
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if in.clip, err = parseClip(r.GetClip()); err != nil {
		return nil, "", err
	}
	queue, err := m.queueForAdd(r.GetQueueId(), model.MainQueueID)
	if err != nil {
		return nil, "", err
	}
	var (
		med      *model.Media
		req      model.Request
		name     string
		expected int64
	)
	if in.played != nil {
		if m.cfg.Played == nil {
			return nil, "", mediaError(CodeMediaUnsupported, connect.CodeUnimplemented, errNoPlayedMedia)
		}
		if med, err = playedMediaOf(*in.played, in.title); err != nil {
			return nil, "", err
		}
		name = siteFileName(in.filename, med.Title, "", in.pageURL, med.Container)
		if in.filename == "" && med.Title == "" {
			name = siteFileName("", cmp.Or(med.Site, "Video")+" video "+time.Now().Format("2006-01-02 15.04"), "", "", med.Container)
		}
		req = model.Request{URL: in.pageURL, Method: http.MethodGet}
		expected = siteExpectedSize(med)
	} else if in.site {
		if med, err = m.siteMedia(ctx, in.page); err != nil {
			return nil, "", err
		}
		// Thumbnails are for a probe's answer: never stored (docs/SITE-MEDIA-SPEC.md §3.12).
		for i := range med.Items {
			med.Items[i].Thumbnail = ""
		}
		name = siteFileName(in.filename, siteTitle(med), in.title, in.page.pageURL, med.Container)
		med.Title = cmp.Or(med.Title, in.title)
		req = model.Request{URL: in.page.pageURL, Method: http.MethodGet}
		if in.page.referer != "" {
			req.Headers = http.Header{"Referer": {in.page.referer}}
		}
		expected = siteExpectedSize(med)
	} else {
		info, err := m.probeMedia(ctx, in.sel)
		if err != nil {
			return nil, "", err
		}
		if med, err = mediaFromProbe(info); err != nil {
			return nil, "", err
		}
		med.Title, med.PageURL = in.title, in.pageURL
		req = model.Request{URL: in.sel.ManifestURL, Method: http.MethodGet, Headers: in.sel.Headers}
		name = mediaFileName(in.filename, in.title, in.sel.ManifestURL, med.Container)
	}
	// A part of the video (docs/MEDIA-SPEC.md §8): checked against the duration, and
	// named by its times unless the client named the file.
	if _, err := applyClip(med, in.clip); err != nil {
		return nil, "", err
	}
	if c := clipOf(med); !c.IsZero() {
		if in.filename == "" {
			name = clipFileName(name, c)
		}
		if in.site {
			expected = clipExpectedSize(med, expected)
		}
	}

	category := mediaCategory(s.Categories, med)
	awaiting := handOff(in.source) && s.ConfirmBrowserDownloads
	unasked := awaiting && !m.confirm.canAsk()
	awaiting = awaiting && !unasked
	fromSettings := false
	if !in.dirGiven && s.UseCategoryFolders {
		if dir, ok := s.categoryFolder(category); ok {
			in.dir, fromSettings = dir, true
		}
	}
	switch {
	case fromSettings:
		err = checkTrustedDir(in.dir)
	case awaiting:
		err = checkCreatable(in.dir)
	default:
		err = ensureDir(in.dir)
	}
	if err != nil {
		return nil, "", err
	}

	state := model.StateQueued
	switch {
	case awaiting:
		state = model.StateAwaitingConfirmation
	case in.startPaused:
		state = model.StatePaused
	}
	// The browser that adds a played video, or a site download with browser_fetch, fetches
	// what downexd's own requests cannot get (docs/SITE-MEDIA-SPEC.md §2.8, §2.9). Its
	// ticket is stored with the record, so that it serves the download again after
	// downexd restarts (amendment 2026-11-19), and bound before the first run can start.
	ticket := ""
	if in.source == model.SourceBrowser && (in.played != nil || (in.site && r.GetBrowserFetch())) {
		if ticket, err = m.browser.NewTicket(); err != nil {
			m.log.Warn("no fetch ticket for a media download added from the browser", "err", err)
			ticket = ""
		}
		med.FetchTicket = ticket
	}
	now := time.Now()
	d := &model.Download{
		Request: req, Dir: in.dir, Size: -1, Mode: model.ModeUnknown, State: state,
		MaxConnections: s.DefaultMaxConnections, CreatedAt: now, UpdatedAt: now, ExpectedSize: expected,
		Source: in.source, Category: category, Kind: model.KindMedia, Media: med, QueueID: queue,
	}
	// Names are reserved in the store (freeName); adds of media are serialised so that
	// two of them never pick the same free name.
	m.mediaMu.Lock()
	d.Filename, err = m.freeName(ctx, in.dir, name, 0)
	if err == nil {
		if cerr := m.st.CreateDownload(ctx, d); cerr != nil {
			err = errAdd(fmt.Errorf("create download: %w", cerr))
		}
	}
	m.mediaMu.Unlock()
	if err != nil {
		return nil, "", err
	}

	if ticket != "" {
		if err := m.browser.Restore(d.ID, ticket); err != nil {
			m.log.Warn("no fetch ticket for a media download added from the browser", "download", int64(d.ID), "err", err)
			ticket = ""
		}
	}
	if in.site {
		m.siteCookies.set(d.ID, siteSignIn{in.page.cookie, in.page.cookieDomain}) // before the first run can start
	}

	pd := m.indexAdded(d, awaiting, nil)
	m.log.Info("media download added", "download", int64(d.ID), "manifest", redactURL(d.Request.URL),
		"origin", med.Origin, "headers", len(d.Request.Headers), "format", med.Format, "tracks", len(med.Tracks),
		"clip", med.Clip != nil, "chapters", len(med.Chapters), "browser_fetch", ticket != "",
		"video", med.SelectedVideo, "audio", med.SelectedAudio, "container", med.Container,
		"fragments", med.FragmentsTotal, "dir", d.Dir, "state", d.State.String(), "source", d.Source.String(),
		"category", d.Category)
	if unasked {
		m.log.Info("browser media download queued without confirmation: nothing can ask the user",
			"download", int64(d.ID))
	}
	return pd, ticket, nil
}

// ProbeMedia fetches and parses a manifest and describes its tracks and the default
// selection, without adding anything; with origin SITE it resolves page_url and describes
// every offered MP4 quality (docs/SITE-MEDIA-SPEC.md §3.3), and with prefetch it is the
// look-ahead of §5.6 (probeSite). Errors are those of AddMedia; prefetch without origin
// SITE is InvalidArgument.
func (m *Manager) ProbeMedia(ctx context.Context, r *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	if playedOrigin(r.GetOrigin()) {
		return m.probePlayed(r)
	}
	if r.GetPlayed() != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("played is only for origin PLAYED"))
	}
	site, err := isSiteOrigin(r.GetOrigin())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if site {
		req, err := parseSiteRequest(r.GetManifestUrl(), r.GetPageUrl(), r.GetHeaders(), "", "", r.GetEntryId(), r.GetCookieDomain())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		req.prefetch = r.GetPrefetch()
		if req.prefetch && req.cookie != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errPrefetchCookie)
		}
		med, err := m.siteMedia(ctx, req)
		if err != nil {
			return nil, err
		}
		return protoMedia(req.pageURL, med), nil
	}
	if r.GetPrefetch() {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("prefetch is only for web pages (origin SITE)"))
	}
	if r.GetEntryId() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEntryNotSite)
	}
	if r.GetCookieDomain() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errCookieDomainNotSite)
	}
	sel, err := parseMediaSelection(r.GetManifestUrl(), r.GetHeaders(), "", "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	info, err := m.probeMedia(ctx, sel)
	if err != nil {
		return nil, err
	}
	med, err := mediaFromProbe(info)
	if err != nil {
		return nil, err
	}
	return protoMedia(sel.ManifestURL, med), nil
}

// probeMedia runs Config.Media's Probe, bounded by mediaProbeTimeout, and maps its
// failure (errMediaProbe).
func (m *Manager) probeMedia(ctx context.Context, sel MediaSelection) (*MediaProbe, error) {
	if m.cfg.Media == nil {
		return nil, mediaError(CodeMediaUnsupported, connect.CodeUnimplemented, errNoMediaJobs)
	}
	pctx, cancel := context.WithTimeout(ctx, mediaProbeTimeout)
	defer cancel()
	info, err := m.cfg.Media.Probe(pctx, m.cfg.Transport, sel)
	switch {
	case err != nil:
		return nil, errMediaProbe(ctx, err)
	case info == nil:
		return nil, errInternal("probe media", errors.New("no result"))
	}
	return info, nil
}

// errMediaProbe maps a failed probe: the caller's cancellation as is; a stream that
// cannot be downloaded (encrypted, live, invalid, unsupported) InvalidArgument; a
// failed fetch (network, TLS, HTTP status) FailedPrecondition; anything else Internal.
// The message starts with the stable code ("encrypted: ...") and the code is also sent
// as ErrorCodeMeta.
func errMediaProbe(ctx context.Context, err error) error {
	if ctx.Err() != nil && isContextError(err) {
		return ctx.Err()
	}
	if isContextError(err) {
		return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("fetching the manifest took too long: %w", err))
	}
	switch code := mediaErrorCode(err); code {
	case CodeEncrypted, CodeLive, CodeMediaInvalid, CodeMediaUnsupported, CodeInvalidRequest:
		return mediaError(code, connect.CodeInvalidArgument, err)
	case CodeNetwork, CodeTLS, CodeHTTPStatus:
		return mediaError(code, connect.CodeFailedPrecondition, fmt.Errorf("cannot fetch the manifest: %w", err))
	default:
		return mediaError(code, connect.CodeInternal, err)
	}
}

// mediaError is a Connect error whose message starts with code and whose metadata
// carries it (ErrorCodeMeta).
func mediaError(code string, c connect.Code, err error) error {
	ce := connect.NewError(c, fmt.Errorf("%s: %w", code, err))
	ce.Meta().Set(ErrorCodeMeta, code)
	return ce
}

// siteMedia resolves the page of r (probeSite) and converts it into the stored media
// description with the tracks r names, or the default pick.
func (m *Manager) siteMedia(ctx context.Context, r siteRequest) (*model.Media, error) {
	info, err := m.probeSite(ctx, r)
	if err != nil {
		return nil, err
	}
	med, err := mediaFromSiteProbe(info, r.pageURL)
	if err != nil {
		return nil, err
	}
	if err := pickSiteTracks(med, r.video, r.audio); err != nil {
		return nil, err
	}
	// Untrusted text from the page: kept only when it is a valid title.
	if med.Title, err = validate.Title(med.Title); err != nil {
		med.Title = ""
	}
	if med.Site, err = validate.Title(med.Site); err != nil || len(med.Site) > 64 {
		med.Site = ""
	}
	return med, nil
}

// mediaFromProbe converts a probe result into the stored media description and checks
// its selection: an MP4 (docs/VIDEO-DETECT-SPEC.md §3.3) with a video.
func mediaFromProbe(info *MediaProbe) (*model.Media, error) {
	switch info.Format {
	case "hls", "dash":
	default:
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument,
			fmt.Errorf("%w: unknown manifest format %q", ErrMediaUnsupported, truncate(info.Format, 32)))
	}
	if info.Container != "mp4" {
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument,
			fmt.Errorf("%w (the selected tracks would be saved as %q)", errNoMP4Stream, truncate(info.Container, 32)))
	}
	med := &model.Media{
		Format: info.Format, Tracks: make([]model.MediaTrack, 0, len(info.Tracks)),
		SelectedVideo: info.DefaultVideo, SelectedAudio: info.DefaultAudio,
		Duration: max(info.Duration, 0), Container: info.Container, FragmentsTotal: max(info.FragmentsTotal, 0),
	}
	for _, t := range info.Tracks {
		mt := modelTrack(t)
		// The probe's size of the track (an estimate when SizeApprox) is kept; the file type
		// is a site's only.
		mt.Ext = ""
		med.Tracks = append(med.Tracks, mt)
	}
	if err := checkSelection(med, med.SelectedVideo, med.SelectedAudio); err != nil {
		return nil, err
	}
	if med.SelectedVideo == "" {
		return nil, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument, fmt.Errorf("%w (audio only)", errNoMP4Stream))
	}
	return med, nil
}

// checkSelection validates a track selection against med's tracks: the video track
// (if any) is video or muxed, the audio track (if any) is audio, at least one is
// chosen, and a muxed track carries its own audio. The error is InvalidArgument with
// code media_invalid.
func checkSelection(med *model.Media, video, audio string) error {
	fail := func(format string, args ...any) error {
		return mediaError(CodeMediaInvalid, connect.CodeInvalidArgument, fmt.Errorf(format, args...))
	}
	var vt *model.MediaTrack
	if video != "" {
		if vt = med.Track(video); vt == nil {
			return fail("video track %q does not exist", truncate(video, 64))
		}
		if vt.Kind != string(media.KindVideo) && vt.Kind != string(media.KindMuxed) {
			return fail("track %q is not a video track (it is %s)", truncate(video, 64), vt.Kind)
		}
	}
	if audio != "" {
		at := med.Track(audio)
		if at == nil {
			return fail("audio track %q does not exist", truncate(audio, 64))
		}
		if at.Kind != string(media.KindAudio) {
			return fail("track %q is not an audio track (it is %s)", truncate(audio, 64), at.Kind)
		}
		if vt != nil && vt.Kind == string(media.KindMuxed) {
			return fail("track %q already carries its audio; it cannot be combined with an audio track", truncate(video, 64))
		}
	}
	if video == "" && audio == "" {
		return fail("the stream has no video or audio track to download")
	}
	return nil
}

// mediaCategory picks the category of a media download, a video (docs/VIDEO-DETECT-SPEC.md
// §3.3): the one named Video, else the first listing the output's extension. An audio
// page's download (docs/SITE-MEDIA-SPEC.md §3.13) takes the one named Audio, else the
// first listing its extension (.m4a or .mp3).
func mediaCategory(cats []Category, med *model.Media) string {
	want := "Video"
	if isAudioMedia(med) {
		want = "Audio"
	}
	for _, c := range cats {
		if strings.EqualFold(c.Name, want) {
			return c.Name
		}
	}
	if c := categoryForExtension(cats, med.Container); c != nil {
		return c.Name
	}
	return ""
}

// genericManifestNames are manifest file names that say nothing about the content; the
// path segment before them names the file instead.
var genericManifestNames = map[string]bool{
	"index": true, "master": true, "playlist": true, "manifest": true, "stream": true, "main": true,
	"chunklist": true, "video": true, "media": true, "hls": true, "dash": true, "prog_index": true,
}

// mediaFileName returns the name of a media download's output file: requested, else
// title, else the manifest's path, by transport.MediaFileName: sanitised, with a known media
// extension replaced by (or the name extended with) the container's: ".mp4" (".ts" only for a
// download stored as MPEG-TS before 2026-10-23).
func mediaFileName(requested, title, manifestURL, container string) string {
	base := requested
	if base == "" {
		base = title
	}
	if base == "" {
		base = manifestBaseName(manifestURL)
	}
	return transport.MediaFileName(base, container)
}

// manifestBaseName names a manifest by its last path segment without a media extension,
// or by one of the two segments before it when that one is generic
// ("…/big-buck-bunny/hls/master.m3u8" → "big-buck-bunny"); "video" when the path names
// nothing.
func manifestBaseName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "video"
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	fallback := ""
	for i := len(segs) - 1; i >= 0 && i >= len(segs)-3; i-- {
		stem := segs[i]
		if ext := transport.MediaExtension(stem); ext != "" {
			stem = stem[:len(stem)-len(ext)-1]
		}
		if stem == "" {
			continue
		}
		if !genericManifestNames[strings.ToLower(stem)] {
			return stem
		}
		if fallback == "" {
			fallback = stem
		}
	}
	if fallback != "" {
		return fallback
	}
	return "video"
}

// ---- running ----------------------------------------------------------------------

// runMedia is engine.Run for a media download: it marks the record downloading, runs
// the job and stores its checkpoints, and stores the completed record. Like engine.Run
// it returns nil once completed, ctx's error when the run was cancelled (the last
// checkpoint is stored) and the failure otherwise (finish/settle then mark the record
// failed or paused according to the run's intent).
func (m *Manager) runMedia(e *entry, r *run) error {
	ctx := r.ctx
	sctx := context.WithoutCancel(ctx)
	rec, err := m.st.GetDownload(ctx, e.id)
	if err != nil {
		return fmt.Errorf("load download %d: %w", e.id, err)
	}
	site, playedRec := isSite(rec.Media), isPlayed(rec.Media)
	switch {
	case rec.State == model.StateCompleted:
		return nil
	case rec.Media == nil:
		return fmt.Errorf("%w: media download %d has no media description", engine.ErrInvalidRequest, e.id)
	case site && m.cfg.Site == nil:
		return errNoSiteMedia
	case playedRec && m.cfg.Played == nil:
		return errNoPlayedMedia
	case playedRec:
	case !site && m.cfg.Media == nil:
		return errNoMediaJobs
	}
	if err := os.MkdirAll(rec.Dir, 0o755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	// A download stored as MPEG-TS before 2026-10-23 that has a checkpoint keeps its
	// byte-for-byte ".ts" join; every other run saves an MP4 (docs/VIDEO-DETECT-SPEC.md
	// §3.3).
	legacy := !site && !playedRec && rec.Media.Container == "ts" && rec.Media.Checkpoint != nil
	if rec.Filename == "" || rec.Media.Checkpoint == nil {
		// Not resuming: nothing on disk belongs to the stored name yet, so take another
		// variant if a file appeared there since the name was reserved.
		name := rec.Filename
		// A manifest download stored as another container (a ".ts" never started, or
		// restarted) becomes an MP4 download under a ".mp4" name.
		convert := !site && !playedRec && !legacy && rec.Media.Container != "mp4"
		if convert {
			rec.Media = rec.Media.Clone()
			rec.Media.Container = "mp4"
		}
		switch {
		case name != "" && convert:
			name = mediaFileName(name, "", "", "mp4")
		case name != "":
		case site || playedRec:
			name = clipFileName(siteFileName("", rec.Media.Title, "", rec.Request.URL, rec.Media.Container), clipOf(rec.Media))
		default:
			name = clipFileName(mediaFileName("", rec.Media.Title, rec.Request.URL, rec.Media.Container), clipOf(rec.Media))
		}
		taken, err := anyExists(filepath.Join(rec.Dir, name))
		if err != nil {
			return fmt.Errorf("check %s: %w", filepath.Join(rec.Dir, name), err)
		}
		if taken && rec.ReplaceFile && name == rec.Filename {
			// Downloaded again to replace its own file (ENGINE-SPEC §7.1): a regular file
			// there that no other download names is replaced once the new copy is
			// assembled; anything else takes the name.
			ok, err := engine.MayReplace(sctx, m.st, rec.Dir, name, rec.ID)
			if err != nil {
				return fmt.Errorf("check %s: %w", filepath.Join(rec.Dir, name), err)
			}
			taken = !ok
		}
		if taken || name != rec.Filename {
			m.mediaMu.Lock()
			name, err = m.freeName(sctx, rec.Dir, name, rec.ID)
			m.mediaMu.Unlock()
			if err != nil {
				return fmt.Errorf("reserve file name: %w", err)
			}
			if taken && rec.Filename != "" {
				m.log.Warn("reserved name is taken; using another", "download", int64(e.id), "name", name)
			}
		}
		if convert {
			m.log.Info("media download stored as another container is saved as an MP4", "download", int64(e.id),
				"name", name)
		}
		if name != rec.Filename {
			rec.ReplaceFile = false // another name replaces nothing
		}
		rec.Filename = name
	}
	// A site download stays probing ("Getting video info…") while it waits for the tools,
	// resolves the page and waits for its formats to become available, until the job
	// reports progress (docs/SITE-MEDIA-SPEC.md §3.3).
	rec.State, rec.Error, rec.UpdatedAt = model.StateDownloading, "", time.Now()
	if site || playedRec {
		rec.State = model.StateProbing
	}
	if err := m.st.UpdateDownload(ctx, rec); err != nil {
		return fmt.Errorf("save download: %w", err)
	}
	m.publishRunRecord(e, r, *rec)

	maxConn := rec.MaxConnections
	if maxConn <= 0 {
		maxConn = m.Settings().DefaultMaxConnections
	}
	opts := MediaRunOptions{
		Factory: m.cfg.Transport, Limiter: r.lim, MaxConnections: maxConn,
		PartDir: MediaPartsDir(rec.Dir, rec.Filename), OutPath: filepath.Join(rec.Dir, rec.Filename),
		HostRules: m.cfg.Engine.HostRules, LegacyTS: legacy, ReplaceExisting: rec.ReplaceFile,
	}
	// The keys of the chosen tracks (as probed when the download was added) find them
	// wherever the manifest lists them now.
	sel := MediaSelection{
		ManifestURL: rec.Request.URL, Headers: rec.Request.Headers.Clone(),
		VideoTrack: rec.Media.SelectedVideo, AudioTrack: rec.Media.SelectedAudio,
		VideoKey: trackKey(rec.Media, rec.Media.SelectedVideo), AudioKey: trackKey(rec.Media, rec.Media.SelectedAudio),
		Clip: clipOf(rec.Media),
	}
	var (
		mu  sync.Mutex // serialises checkpoints; guards cur
		cur = rec
	)
	onProgress := func(p MediaProgress) {
		r.mprogress.Store(&p)
		m.hub.publish(e.id, kindUpdated)
	}
	onCheckpoint := func(cp model.MediaCheckpoint) error {
		mu.Lock()
		defer mu.Unlock()
		next := *cur
		next.Media = cur.Media.Clone()
		next.Media.Checkpoint = cp.Clone()
		total, done := checkpointCounts(&cp)
		if total > 0 {
			next.Media.FragmentsTotal = total
		}
		next.Media.FragmentsDone = done
		// The bytes the checkpoint records as durable (fragments and chunk prefixes), never
		// those of requests in flight: a resumed run starts from them, so the bytes done
		// never drop when it resumes (docs/SITE-MEDIA-SPEC.md §2.5).
		next.Media.BytesDone = max(cp.DurableBytes, 0)
		if p := r.mprogress.Load(); p != nil {
			next.State = model.StateDownloading // a site download probed until its first progress
		}
		next.UpdatedAt = time.Now()
		wctx, cancel := context.WithTimeout(sctx, finalizeTimeout)
		defer cancel()
		if err := m.st.UpdateDownload(wctx, &next); err != nil {
			return fmt.Errorf("save media checkpoint: %w", err)
		}
		cur = &next
		m.publishRunRecord(e, r, next)
		return nil
	}
	m.log.Debug("media download started", "download", int64(e.id), "origin", rec.Media.Origin, "video", sel.VideoTrack,
		"audio", sel.AudioTrack, "resume", rec.Media.Checkpoint != nil, "legacy_ts", legacy, "clip", !sel.Clip.IsZero())
	var (
		container string
		runErr    error
	)
	if site || playedRec {
		// A browser that fetches for it (docs/SITE-MEDIA-SPEC.md §2.8, §2.9); a run that goes
		// on through it marks the record, so later runs start there.
		m.browserRun(e.id, &opts, func() {
			mu.Lock()
			defer mu.Unlock()
			if cur.Media.BrowserFetch {
				return
			}
			next := *cur
			next.Media = cur.Media.Clone()
			next.Media.BrowserFetch = true
			next.UpdatedAt = time.Now()
			wctx, cancel := context.WithTimeout(sctx, finalizeTimeout)
			defer cancel()
			if err := m.st.UpdateDownload(wctx, &next); err != nil {
				m.log.Warn("cannot mark a site download as fetched by the browser", "download", int64(e.id), "err", err)
				return
			}
			cur = &next
			m.publishRunRecord(e, r, next)
			m.log.Info("site download fetched by the browser", "download", int64(e.id))
		})
		if playedRec {
			container, runErr = m.runPlayed(ctx, rec, opts, onProgress, onCheckpoint)
		} else {
			container, runErr = m.runSite(ctx, rec, opts, onProgress, onCheckpoint)
		}
	} else {
		container, runErr = m.cfg.Media.Run(ctx, opts, sel, rec.Media.Checkpoint.Clone(), onProgress, onCheckpoint)
	}
	if runErr != nil {
		return runErr
	}
	want := "mp4"
	switch {
	case legacy:
		want = "ts"
	case isAudioMedia(rec.Media):
		want = rec.Media.Container // an audio page's .m4a or .mp3 (docs/SITE-MEDIA-SPEC.md §3.13)
	}
	if container != want {
		// A download is an MP4 or nothing (docs/SITE-MEDIA-SPEC.md, amendment 2026-10-22;
		// docs/VIDEO-DETECT-SPEC.md §3.3), a legacy one the ".ts" it began as: a file of
		// another container is never left under the name, and its part directory (with
		// the record of that output) goes with it.
		if err := os.Remove(opts.OutPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.log.Warn("cannot remove the output of a media download saved in another container", "download", int64(e.id),
				"err", err)
		}
		_ = os.RemoveAll(opts.PartDir)
		if site || playedRec {
			return fmt.Errorf("%w (it would have been saved as %q)", errNoMP4, container)
		}
		return fmt.Errorf("%w (it would have been saved as %q, not %q)", errNoMP4Stream, container, want)
	}

	fi, err := os.Stat(opts.OutPath)
	if err != nil {
		return fmt.Errorf("media job finished without its output: %w", err)
	}
	mu.Lock()
	done := *cur
	mu.Unlock()
	done.Media = done.Media.Clone()
	done.Media.Checkpoint = nil
	if p := r.mprogress.Load(); p != nil && p.FragmentsTotal > 0 {
		done.Media.FragmentsTotal = p.FragmentsTotal
	}
	done.Media.FragmentsDone = done.Media.FragmentsTotal
	done.Media.BytesDone = fi.Size()
	done.Media.Container = container
	done.Media.FetchTicket = "" // the ticket ends with it (Forget below)
	if p := r.mprogress.Load(); p != nil {
		completeClip(done.Media, p.Clip) // what the part became (docs/MEDIA-SPEC.md §8 C20)
	}
	now := time.Now()
	done.State, done.Size, done.Error, done.CompletedAt, done.UpdatedAt = model.StateCompleted, fi.Size(), "", now, now
	done.ReplaceFile = false
	if err := m.st.UpdateDownload(sctx, &done); err != nil {
		return fmt.Errorf("save completed download: %w", err)
	}
	m.siteCookies.set(e.id, siteSignIn{})
	m.browser.Forget(e.id) // its fetch ticket ends with it (docs/SITE-MEDIA-SPEC.md §2.8)
	if err := os.RemoveAll(opts.PartDir); err != nil {
		m.log.Warn("cannot remove the part directory of a completed media download", "download", int64(e.id), "err", err)
	}
	return nil
}

// trackKey returns the Key of med's track id ("" when there is none).
func trackKey(med *model.Media, id string) string {
	if t := med.Track(id); t != nil {
		return t.Key
	}
	return ""
}

// publishRunRecord makes rec the cached record of e's running run r, so that clients
// see what the run stored (file name, fragment totals).
func (m *Manager) publishRunRecord(e *entry, r *run, rec model.Download) {
	m.mu.Lock()
	if e.run == r && !r.finalizing {
		e.rec = rec
	}
	m.mu.Unlock()
	m.hub.publish(e.id, kindUpdated)
}

// checkpointCounts returns the fragments a checkpoint covers and how many of them are
// durable (bits beyond a track's Total are ignored).
func checkpointCounts(cp *model.MediaCheckpoint) (total, done int) {
	for _, t := range cp.Tracks {
		n := max(t.Total, 0)
		total += n
		for i, b := range t.Done {
			if i*8 >= n {
				break
			}
			if rest := n - i*8; rest < 8 {
				b &= byte(1<<rest - 1)
			}
			done += bits.OnesCount8(b)
		}
	}
	return total, done
}

// restartMedia discards a stopped media download's fragments and checkpoint and queues
// it (engine.Restart for media). A completed download is restarted only with
// opts.Completed, and then under the name engine.RestartOptions describes: its own while
// it replaces its own file (engine.OwnFile; ReplaceFile, the job renames over the file
// only once the new copy is assembled), else a free one (its own when the file is gone). Its next run
// starts afresh: a video of a web page is resolved again, with fresh addresses.
func (m *Manager) restartMedia(ctx context.Context, rec *model.Download, opts engine.RestartOptions) error {
	next := *rec
	if rec.State == model.StateCompleted {
		if !opts.Completed {
			return fmt.Errorf("%w: download %d is already completed", engine.ErrInvalidRequest, rec.ID)
		}
		next.ReplaceFile = false
		if next.Filename != "" {
			replace := false
			if opts.Replace {
				var err error
				if replace, err = engine.OwnFile(ctx, m.st, rec); err != nil {
					return fmt.Errorf("check the file: %w", err)
				}
			}
			if replace {
				next.ReplaceFile = true
			} else {
				m.mediaMu.Lock()
				name, err := m.freeName(ctx, next.Dir, next.Filename, next.ID)
				m.mediaMu.Unlock()
				if err != nil {
					return fmt.Errorf("reserve file name: %w", err)
				}
				next.Filename = name
			}
		}
	}
	next.Media = rec.Media.Clone()
	if next.Media != nil {
		next.Media.Checkpoint, next.Media.FragmentsDone, next.Media.BytesDone = nil, 0, 0
		resetClip(next.Media) // the part stays; what the last run made of it goes
	}
	next.State, next.Error, next.Size, next.CompletedAt, next.UpdatedAt = model.StateQueued, "", -1, time.Time{}, time.Now()
	next.StartedAt, next.ActiveMS = time.Time{}, 0 // the time starts over with the bytes (restartDownload)
	if err := m.st.SaveCheckpoint(ctx, &next, nil); err != nil {
		return fmt.Errorf("save download: %w", err)
	}
	if next.Filename != "" {
		// Only disk space is at stake: the next run starts without a checkpoint.
		if err := os.RemoveAll(MediaPartsDir(next.Dir, next.Filename)); err != nil {
			m.log.Warn("cannot delete the fragments of a restarted media download", "download", int64(rec.ID), "err", err)
		}
		if err := os.Remove(engine.PartPath(next.Dir, next.Filename)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			m.log.Warn("cannot delete the part file of a restarted media download", "download", int64(rec.ID), "err", err)
		}
	}
	if rec.Filename != "" && next.Filename != rec.Filename {
		// A completed download kept under its old name: what its cleanup may have left.
		_ = os.RemoveAll(MediaPartsDir(rec.Dir, rec.Filename))
	}
	*rec = next
	return nil
}

// restartDownload discards the progress of stopped download id (engine.Restart, or
// restartMedia for a media download); opts allow a completed one (ENGINE-SPEC §7.1).
// The running time goes with the bytes: started_at and active_ms start over, so the
// time taken and the average of the next copy are its own (DESKTOP-SPEC §8).
func (m *Manager) restartDownload(ctx context.Context, id model.DownloadID, opts engine.RestartOptions) error {
	rec, err := m.st.GetDownload(ctx, id)
	if err != nil {
		return fmt.Errorf("load download %d: %w", id, err)
	}
	if rec.Kind == model.KindMedia {
		return m.restartMedia(ctx, rec, opts)
	}
	if err := m.eng.Restart(ctx, id, opts); err != nil {
		return err
	}
	// engine.Restart keeps these fields, which only the daemon writes (accountRun). The
	// restart stands either way: a failure here only leaves the earlier copy's time in.
	fresh, err := m.st.GetDownload(ctx, id)
	if err == nil && (fresh.ActiveMS != 0 || !fresh.StartedAt.IsZero()) {
		fresh.StartedAt, fresh.ActiveMS = time.Time{}, 0
		err = m.st.UpdateDownload(ctx, fresh)
	}
	if err != nil {
		m.log.Warn("cannot reset the running time of a restarted download", "download", int64(id), "err", err)
	}
	return nil
}

// ---- rendering --------------------------------------------------------------------

// protoMedia renders a media description whose download URL is rawURL: the manifest, or
// for site media the page (manifest_url is then empty and page_url the page).
func protoMedia(rawURL string, med *model.Media) *downexv1.MediaInfo {
	pm := &downexv1.MediaInfo{
		ManifestUrl: rawURL, Format: med.Format, SelectedVideo: med.SelectedVideo,
		SelectedAudio: med.SelectedAudio, DurationMs: med.Duration.Milliseconds(),
		FragmentsTotal: clampInt32(med.FragmentsTotal), FragmentsDone: clampInt32(med.FragmentsDone),
		Container: med.Container, Title: med.Title, Site: med.Site, PageUrl: med.PageURL,
		Clip: protoClip(med.Clip), Chapters: protoChapters(med.Chapters),
	}
	if isPlayed(med) {
		pm.Origin, pm.ManifestUrl, pm.BrowserFetch = downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED, "", med.BrowserFetch
		if pm.PageUrl == "" {
			pm.PageUrl = rawURL
		}
	}
	if isSite(med) {
		pm.Origin, pm.ManifestUrl = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, ""
		if pm.PageUrl == "" {
			pm.PageUrl = rawURL
		}
		pm.EntryId, pm.ItemsLeftOut, pm.BrowserFetch = med.EntryID, clampInt32(med.ItemsLeftOut), med.BrowserFetch
		for _, it := range med.Items {
			pm.Items = append(pm.Items, &downexv1.MediaItem{Id: it.ID, Title: it.Title, DurationMs: it.Duration.Milliseconds(),
				Thumbnail: it.Thumbnail})
		}
	}
	if len(med.Tracks) > 0 {
		pm.Tracks = make([]*downexv1.MediaTrack, len(med.Tracks))
		for i, t := range med.Tracks {
			pm.Tracks[i] = &downexv1.MediaTrack{
				Id: t.ID, Kind: t.Kind, Bandwidth: t.Bandwidth, Width: clampInt32(t.Width), Height: clampInt32(t.Height),
				FrameRate: t.FrameRate, Codecs: t.Codecs, Language: t.Language, Name: t.Name,
				Size: max(t.Size, 0), SizeApprox: t.SizeApprox, Ext: t.Ext,
			}
		}
	}
	return pm
}

func clampInt32(n int) int32 {
	return int32(min(max(n, math.MinInt32), math.MaxInt32))
}

// selectMediaTracks applies the tracks chosen at confirmation to rec (a media download,
// copied before it changes): video and audio, when not "", replace the selection. A
// muxed video track drops a separate audio track that was not chosen with it; a video
// track without audio gets the best audio track when none is chosen: a site download's
// first (its tracks come best first, docs/SITE-MEDIA-SPEC.md §3.11), else the one of the
// highest bandwidth.
// The selection is validated (checkSelection); when it changed, the fragment count is
// unknown until the download starts.
func selectMediaTracks(rec *model.Download, video, audio string) error {
	if video == "" && audio == "" {
		return nil
	}
	med := rec.Media
	if med == nil {
		return errPrecondition("media download %d has no media description", rec.ID)
	}
	v, a := med.SelectedVideo, med.SelectedAudio
	if video != "" {
		v = video
		if t := med.Track(video); t != nil && audio == "" {
			switch t.Kind {
			case string(media.KindMuxed):
				a = ""
			case string(media.KindVideo):
				if a == "" {
					a = bestAudio(med)
				}
			}
		}
	}
	if audio != "" {
		a = audio
	}
	if err := checkSelection(med, v, a); err != nil {
		return err
	}
	if v != med.SelectedVideo || a != med.SelectedAudio {
		rec.Media = med.Clone()
		rec.Media.SelectedVideo, rec.Media.SelectedAudio, rec.Media.FragmentsTotal = v, a, 0
	}
	return nil
}

// bestAudio returns the id of med's best audio track ("" = none): the first of a site
// download, else the one with the highest bandwidth.
func bestAudio(med *model.Media) string {
	tracks := med.Tracks
	best := -1
	for i, t := range tracks {
		if t.Kind != string(media.KindAudio) {
			continue
		}
		if isPageVideo(med) {
			return t.ID
		}
		if best < 0 || t.Bandwidth > tracks[best].Bandwidth {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return tracks[best].ID
}
