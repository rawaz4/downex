package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// errorCodeMeta is the Connect error metadata that carries the stable code of a refused
// AddMedia or ProbeMedia (daemon.ErrorCodeMeta).
const errorCodeMeta = "Downex-Error-Code"

// mediaPartsExt names the part directory of a media download next to its file
// (daemon.MediaPartsDir).
const mediaPartsExt = ".dxparts"

func (a *app) media(ctx context.Context, args []string) error {
	fs := a.flagSet("media", "media <manifest-url> [flags]\n       downex media --site <page-url> [flags]",
		"Download the streaming video of an HLS (.m3u8) or DASH (.mpd) manifest: downexd fetches\n"+
			"the fragments of the chosen tracks and joins them into one .mp4 file (MPEG-TS streams\n"+
			"are converted). Without --video and --audio the best video and its best audio are\n"+
			"chosen. --list only shows the tracks that can be saved as MP4, one video per quality\n"+
			"(4K, 1440p, 1080p, ...; the default pick is marked with *). Encrypted or DRM-protected\n"+
			"streams, live streams and streams without an MP4 video (audio only, WebM) cannot be\n"+
			"downloaded. With --wait, follow the progress until the download completes; Ctrl+C then\n"+
			"stops following, not the download.\n\n"+
			"With --site the address is a web page with a video (YouTube and the other sites yt-dlp\n"+
			"understands): downexd resolves it with yt-dlp, which it downloads with Deno on first use,\n"+
			"and saves the video as one .mp4 file. --list shows one MP4 video per quality the page\n"+
			"offers (4K, 1440p, 1080p, ...), best first, and its audio tracks; --video picks any of\n"+
			"them, or another MP4 format of the page by its ID. -H takes only Cookie and Referer.\n"+
			"While yt-dlp is being downloaded or the page is being read, downex retries every 2\n"+
			"seconds for up to 5 minutes.")
	t := a.daemonFlags(fs)
	var (
		headers headerFlag
		list    bool
		site    bool
		video   string
		audio   string
		dir     string
		name    string
		paused  bool
		wait    bool
		asJSON  bool
		queue   string
	)
	fs.BoolVar(&site, "site", false, "the address is a web page with a video, resolved with yt-dlp (saved as .mp4)")
	fs.BoolVar(&list, "list", false, "list the stream's tracks (one MP4 video per quality) instead of downloading it")
	fs.StringVar(&video, "video", "", "download the video track `ID` (see --list)")
	fs.StringVar(&audio, "audio", "", "download the audio track `ID` (see --list)")
	fs.StringVar(&dir, "o", "", "save into `dir` (default: the daemon's Video folder or default directory)")
	fs.StringVar(&name, "name", "", "save as `file` (.mp4 is added)")
	fs.Var(&headers, "H", "request header \"Name: value\" (repeatable), e.g. Cookie, Referer or User-Agent\n(--site: only Cookie and Referer)")
	fs.BoolVar(&paused, "paused", false, "add the download paused instead of queueing it")
	fs.BoolVar(&wait, "wait", false, "follow the download's progress until it completes")
	fs.BoolVar(&asJSON, "json", false, "--list: print JSON")
	fs.StringVar(&queue, "queue", "", queueFlagUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		if site {
			return usageError{"media", "expects exactly one page URL"}
		}
		return usageError{"media", "expects exactly one manifest URL"}
	}
	set := visited(fs)
	if list {
		for _, f := range []string{"video", "audio", "o", "name", "paused", "wait", "queue"} {
			if set[f] {
				return usageError{"media", "--list cannot be combined with --" + f}
			}
		}
	} else if asJSON {
		return usageError{"media", "--json applies only with --list"}
	}
	if paused && wait {
		return usageError{"media", "--paused and --wait cannot be combined"}
	}
	if set["name"] && name == "" {
		return usageError{"media", "--name must not be empty"}
	}
	if site {
		for _, k := range sortedKeys(headers.h) {
			if k != "Cookie" && k != "Referer" {
				return usageError{"media", fmt.Sprintf("-H %s: with --site only the Cookie and Referer headers are used", k)}
			}
		}
	}
	if dir != "" {
		// The daemon requires an absolute directory; -o is relative to this process.
		if dir, err = filepath.Abs(dir); err != nil {
			return fmt.Errorf("directory: %w", err)
		}
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	// A page is resolved (and yt-dlp perhaps downloaded first) while the call is retried.
	call := func(fn func(ctx context.Context) error) error {
		if site {
			return a.siteRetry(ctx, fn)
		}
		cctx, cancel := rpcContext(ctx)
		defer cancel()
		return fn(cctx)
	}
	probe := &downexv1.ProbeMediaRequest{ManifestUrl: pos[0], Headers: rpcHeaders(headers.h)}
	if site {
		probe.ManifestUrl, probe.PageUrl, probe.Origin = "", pos[0], downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
	}
	if list {
		var info *downexv1.MediaInfo
		err := call(func(ctx context.Context) error {
			resp, err := c.Downloads.ProbeMedia(ctx, connect.NewRequest(probe))
			if err == nil {
				info = resp.Msg.GetMedia()
			}
			return err
		})
		if err != nil {
			return mediaRPCError(err, site)
		}
		v := newMediaView(info)
		if asJSON {
			return writeJSON(a.stdout, v)
		}
		return writeTracks(a.stdout, v)
	}
	var q *downexv1.Queue
	if set["queue"] {
		if q, err = a.resolveQueueFlag(ctx, c, queue); err != nil {
			return err
		}
	}
	add := &downexv1.AddMediaRequest{
		ManifestUrl: probe.GetManifestUrl(), PageUrl: probe.GetPageUrl(), Origin: probe.GetOrigin(), Headers: probe.GetHeaders(),
		Dir: dir, Filename: name, Source: downexv1.AddSource_ADD_SOURCE_CLI, VideoTrack: video, AudioTrack: audio,
		StartPaused: paused, QueueId: q.GetId(),
	}
	var d *downexv1.Download
	err = call(func(ctx context.Context) error {
		resp, err := c.Downloads.AddMedia(ctx, connect.NewRequest(add))
		if err == nil {
			d = resp.Msg.GetDownload()
		}
		return err
	})
	if err != nil {
		return mediaRPCError(err, site)
	}
	fmt.Fprintln(a.stdout, d.GetId())
	a.con.printf("Download %d %s: %s (%s)\n", d.GetId(), describeState(d), printable(d.GetFilename()),
		describeSelection(d.GetMedia()))
	a.hintStoppedQueue(t, q, d)
	if !wait {
		return nil
	}
	return a.follow(ctx, c, t, d)
}

// Codes of a site-media call that ask the caller to try again (docs/SITE-MEDIA-SPEC.md
// §3.2).
const (
	codeToolsInstalling = "tools_installing"
	codeSiteResolving   = "site_resolving"
)

// siteRetry calls fn (one daemon call, bounded by rpcContext) until it answers anything
// but tools_installing or site_resolving: each try a.retryEvery after the start of the
// last one (at once after a call that took that long), for at most a.retryFor, the last
// answer then being the error. Meanwhile one status line shows what downexd does:
// "Downloading yt-dlp and Deno: 34%" or "Getting video info… 12 s" (on a terminal,
// redrawn; elsewhere printed once per step).
func (a *app) siteRetry(ctx context.Context, fn func(ctx context.Context) error) error {
	start := time.Now()
	var shown string       // the step whose line was printed (not on a terminal)
	defer a.con.printf("") // erases the status line
	for {
		tried := time.Now()
		cctx, cancel := rpcContext(ctx)
		err := fn(cctx)
		cancel()
		code := errorCode(err)
		if err == nil || (code != codeToolsInstalling && code != codeSiteResolving) {
			return err
		}
		elapsed := time.Since(start)
		if elapsed >= a.retryFor {
			return err
		}
		step, line := "Getting video info…", "Getting video info… "+strconv.Itoa(int(elapsed.Seconds()))+" s"
		if code == codeToolsInstalling {
			step, line = "Downloading yt-dlp and Deno…", "Downloading yt-dlp and Deno…"
			if p := installPercent(err); p != "" {
				line = "Downloading yt-dlp and Deno: " + p
			}
		}
		if a.con.live {
			a.con.status(line)
		} else if shown != code {
			shown = code
			a.con.printf("%s\n", step)
		}
		if wait := a.retryEvery - time.Since(tried); wait > 0 && !client.Sleep(ctx, wait) {
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// errorCode returns the stable code of a refused daemon call (its errorCodeMeta), or "".
func errorCode(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Meta().Get(errorCodeMeta)
	}
	return ""
}

// installPercent returns the progress a tools_installing refusal carries ("34%"), or "".
func installPercent(err error) string {
	msg := err.Error()
	i := strings.LastIndex(msg, "(")
	if i < 0 || !strings.HasSuffix(msg, "%)") {
		return ""
	}
	n, perr := strconv.Atoi(msg[i+1 : len(msg)-2])
	if perr != nil || n < 0 || n > 100 {
		return ""
	}
	return strconv.Itoa(n) + "%"
}

// siteErrorTexts explain the refusals of a page (docs/SITE-MEDIA-SPEC.md §5.5); withReason
// adds the daemon's reason.
var siteErrorTexts = map[string]struct {
	text       string
	withReason bool
}{
	"site_unsupported":  {"Downex found no video on this page", false},
	"site_unavailable":  {"this video is not available (private, removed, blocked in your country, or the site asks to try again later)", true},
	"auth_required":     {"the site wants you to sign in (age check, members-only or bot check); pass your sign-in cookie with -H \"Cookie: ...\"", true},
	"media_unsupported": {"no MP4 video on this page (or a playlist: open a single video of it)", false},
	"media_invalid":     {"the chosen quality is not offered (see --list), or the page's answer was unusable", true},
	"site_error":        {"yt-dlp could not read this page", true},
	"tools_unavailable": {"Downex could not set up yt-dlp/Deno (see \"downex tools\")", true},
	"site_media_off":    {"video downloads from web pages are turned off (downex settings --site-media on)", false},
	codeToolsInstalling: {"yt-dlp and Deno are still being downloaded; try again in a moment (see \"downex tools\")", false},
	codeSiteResolving:   {"getting the video info takes long; try again in a moment", false},
}

// mediaRPCError describes a refused AddMedia or ProbeMedia (of a web page with site),
// explaining the media codes.
func mediaRPCError(err error, site bool) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		code := ce.Meta().Get(errorCodeMeta)
		switch {
		case code == "encrypted":
			return &daemonError{msg: "media: the stream is encrypted or DRM-protected; Downex does not download protected content", err: err}
		case code == "live":
			return &daemonError{msg: "media: this is a live stream; only complete (video-on-demand) streams can be downloaded", err: err}
		case code == "media_unsupported" && !site && ce.Code() == connect.CodeInvalidArgument:
			// A manifest refused by the probe (docs/VIDEO-DETECT-SPEC.md §3.3).
			return &daemonError{msg: "media: no MP4 video in this stream (Downex saves streams as MP4 only: H.264 or HEVC " +
				"video with AAC audio; audio-only and WebM streams cannot be saved)", err: err}
		}
		if s, ok := siteErrorTexts[code]; ok && site {
			msg := "media: " + s.text
			if reason := siteReason(ce.Message(), code); s.withReason && reason != "" {
				msg += ": " + reason
			}
			return &daemonError{msg: msg, err: err}
		}
	}
	return rpcError("media", err)
}

// siteReason is the cause in a refusal's message "<code>: <sentinel text>: <reason>", or
// the message without its code when it has no further reason.
func siteReason(msg, code string) string {
	msg = strings.TrimPrefix(msg, code+": ")
	if _, reason, ok := strings.Cut(msg, ": "); ok {
		return reason
	}
	return ""
}

// mediaView is the JSON form of a streaming presentation ("media --list --json", and
// the media of "ls --json" and "info --json"). Origin is "manifest" or "site" (a web page
// resolved with yt-dlp, docs/SITE-MEDIA-SPEC.md: PageURL is the page and Site its name).
type mediaView struct {
	Origin         string      `json:"origin"`
	ManifestURL    string      `json:"manifestUrl,omitempty"`
	PageURL        string      `json:"pageUrl,omitempty"`
	Site           string      `json:"site,omitempty"`
	Format         string      `json:"format"`
	Title          string      `json:"title,omitempty"`
	Duration       float64     `json:"durationSeconds,omitempty"`
	Container      string      `json:"container,omitempty"`
	SelectedVideo  string      `json:"selectedVideo,omitempty"`
	SelectedAudio  string      `json:"selectedAudio,omitempty"`
	FragmentsTotal int         `json:"fragmentsTotal,omitempty"`
	FragmentsDone  int         `json:"fragmentsDone,omitempty"`
	Tracks         []trackView `json:"tracks"`
}

// Values of mediaView.Origin.
const (
	originManifest = "manifest"
	originSite     = "site"
)

type trackView struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Bandwidth  int64   `json:"bandwidth,omitempty"` // bits per second
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FrameRate  float64 `json:"frameRate,omitempty"`
	Codecs     string  `json:"codecs,omitempty"`
	Language   string  `json:"language,omitempty"`
	Name       string  `json:"name,omitempty"`
	Size       int64   `json:"size,omitempty"` // bytes as the site declares them, or as the daemon measured a manifest track
	SizeApprox bool    `json:"sizeApprox,omitempty"`
	Ext        string  `json:"ext,omitempty"`
}

func newMediaView(m *downexv1.MediaInfo) *mediaView {
	if m == nil {
		return nil
	}
	v := &mediaView{
		Origin: originManifest, ManifestURL: m.GetManifestUrl(), PageURL: m.GetPageUrl(), Site: m.GetSite(),
		Format: m.GetFormat(), Title: m.GetTitle(),
		Duration: float64(m.GetDurationMs()) / 1000, Container: m.GetContainer(),
		SelectedVideo: m.GetSelectedVideo(), SelectedAudio: m.GetSelectedAudio(),
		FragmentsTotal: int(m.GetFragmentsTotal()), FragmentsDone: int(m.GetFragmentsDone()),
		Tracks: make([]trackView, 0, len(m.GetTracks())),
	}
	if m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
		v.Origin = originSite
	}
	for _, t := range m.GetTracks() {
		v.Tracks = append(v.Tracks, trackView{
			ID: t.GetId(), Kind: t.GetKind(), Bandwidth: t.GetBandwidth(), Width: int(t.GetWidth()),
			Height: int(t.GetHeight()), FrameRate: t.GetFrameRate(), Codecs: t.GetCodecs(), Language: t.GetLanguage(),
			Name: t.GetName(), Size: max(t.GetSize(), 0), SizeApprox: t.GetSizeApprox(), Ext: t.GetExt(),
		})
	}
	v.sortTracks()
	return v
}

// storedMediaView is newMediaView for a media description read from the database.
func storedMediaView(d *model.Download) *mediaView {
	m := d.Media
	if m == nil {
		return nil
	}
	v := &mediaView{
		Origin: originManifest, ManifestURL: d.Request.URL, PageURL: m.PageURL, Site: m.Site, Format: m.Format,
		Title: m.Title, Duration: m.Duration.Seconds(),
		Container: m.Container, SelectedVideo: m.SelectedVideo, SelectedAudio: m.SelectedAudio,
		FragmentsTotal: m.FragmentsTotal, FragmentsDone: m.FragmentsDone,
		Tracks: make([]trackView, 0, len(m.Tracks)),
	}
	if m.Origin == originSite {
		v.Origin, v.ManifestURL = originSite, ""
		if v.PageURL == "" {
			v.PageURL = d.Request.URL
		}
	}
	for _, t := range m.Tracks {
		v.Tracks = append(v.Tracks, trackView{
			ID: t.ID, Kind: t.Kind, Bandwidth: t.Bandwidth, Width: t.Width, Height: t.Height, FrameRate: t.FrameRate,
			Codecs: t.Codecs, Language: t.Language, Name: t.Name, Size: max(t.Size, 0), SizeApprox: t.SizeApprox, Ext: t.Ext,
		})
	}
	v.sortTracks()
	return v
}

// sortTracks orders the tracks for people: a web page's as the daemon lists them (every
// MP4 quality best first, then the audio tracks), a manifest's with sortTracks.
func (v *mediaView) sortTracks() {
	if v.Origin != originSite {
		sortTracks(v.Tracks)
	}
}

// trackRank orders track kinds: video (and muxed) first, then audio, then subtitles.
func trackRank(kind string) int {
	switch kind {
	case "video", "muxed":
		return 0
	case "audio":
		return 1
	}
	return 2
}

// sortTracks orders tracks as the quality picker does (docs/MEDIA-SPEC.md §6): video by
// resolution, then bandwidth (best first); audio by language, then bandwidth.
func sortTracks(ts []trackView) {
	slices.SortStableFunc(ts, func(a, b trackView) int {
		if c := cmp.Compare(trackRank(a.Kind), trackRank(b.Kind)); c != 0 {
			return c
		}
		if trackRank(a.Kind) == 0 {
			if c := cmp.Compare(int64(b.Width)*int64(b.Height), int64(a.Width)*int64(a.Height)); c != 0 {
				return c
			}
			return cmp.Compare(b.Bandwidth, a.Bandwidth)
		}
		if c := cmp.Compare(a.Language, b.Language); c != 0 {
			return c
		}
		return cmp.Compare(b.Bandwidth, a.Bandwidth)
	})
}

// writeTracks prints a presentation and its tracks, marking the selected ones. QUALITY
// is a video's standard quality name ("1080p (Full HD)", qualityName) or an audio or
// subtitle track's name; PICTURE its width x height and frame rate. SIZE is what the
// daemon measured of a track without a BANDWIDTH (a bare media playlist's; "~" for an
// estimate), else the BANDWIDTH × the duration ("~"), else "-" (trackSize).
func writeTracks(w io.Writer, v *mediaView) error {
	if v == nil {
		fmt.Fprintln(w, "No media information.")
		return nil
	}
	if v.Origin == originSite {
		return writeSiteTracks(w, v)
	}
	head := []string{"Format: " + strings.ToUpper(v.Format)}
	if v.Duration > 0 {
		head = append(head, "Duration: "+formatDuration(time.Duration(v.Duration*float64(time.Second))))
	}
	if v.Container != "" {
		head = append(head, "Saved as: ."+v.Container)
	}
	if v.FragmentsTotal > 0 {
		head = append(head, "Fragments: "+strconv.Itoa(v.FragmentsTotal))
	}
	fmt.Fprintln(w, strings.Join(head, "  "))
	if len(v.Tracks) == 0 {
		fmt.Fprintln(w, "No tracks.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tKIND\tQUALITY\tPICTURE\tBANDWIDTH\tSIZE\tCODECS\tLANGUAGE")
	for _, t := range v.Tracks {
		quality := t.Name
		if trackRank(t.Kind) == 0 {
			quality = qualityName(t)
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.mark(t), printable(t.ID), printable(t.Kind),
			printable(dash(quality)), trackPicture(t), formatBandwidth(t.Bandwidth), v.trackSize(t), printable(dash(t.Codecs)),
			printable(dash(t.Language)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "* = downloaded by default; choose others with --video ID and --audio ID.")
	return nil
}

// writeSiteTracks prints the MP4 qualities a web page offers (docs/SITE-MEDIA-SPEC.md
// §3.7; one video per quality class since the amendment of 2026-10-23): one row per
// track with ID, KIND, QUALITY (siteQuality), PICTURE, CODECS, SIZE ("~" for the site's
// estimates and for bandwidth × duration, trackSize), EXT and LANG, in the daemon's
// order (best first), the default marked.
func writeSiteTracks(w io.Writer, v *mediaView) error {
	title := "Video"
	if v.Site != "" {
		title = v.Site + " video"
	}
	if v.Title != "" {
		title += ": " + v.Title
	}
	fmt.Fprintln(w, printable(title))
	head := []string{"Saved as: .mp4"}
	if v.Duration > 0 {
		head = append([]string{"Duration: " + formatDuration(time.Duration(v.Duration*float64(time.Second)))}, head...)
	}
	fmt.Fprintln(w, strings.Join(head, "  "))
	if len(v.Tracks) == 0 {
		fmt.Fprintln(w, "No tracks.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tKIND\tQUALITY\tPICTURE\tCODECS\tSIZE\tEXT\tLANG")
	for _, t := range v.Tracks {
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.mark(t), printable(t.ID), printable(t.Kind), siteQuality(t),
			trackPicture(t), printable(dash(codecNames(t.Codecs))), v.trackSize(t), printable(dash(t.Ext)),
			printable(dash(t.Language)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "* = downloaded by default; choose another quality with --video ID (and its audio with --audio ID).")
	return nil
}

// mark is "*" for a selected track, else " ".
func (v *mediaView) mark(t trackView) string {
	if t.ID != "" && (t.ID == v.SelectedVideo || t.ID == v.SelectedAudio) {
		return "*"
	}
	return " "
}

// siteQuality describes a web page's track: a video's quality name ("4K (2160p) 60fps
// HDR", qualityName), else "-"; an audio track's bandwidth and name.
func siteQuality(t trackView) string {
	if t.Kind == "audio" {
		parts := []string{formatBandwidth(t.Bandwidth)}
		if t.Name != "" {
			parts = append(parts, printable(t.Name))
		}
		return strings.Join(parts, " ")
	}
	return printable(dash(qualityName(t)))
}

// qualityName is a video or muxed track's quality name as every UI shows it
// (media.TrackName; docs/SITE-MEDIA-SPEC.md, amendment 2026-10-23): the daemon's
// standard name, else (a record of an older daemon: a raw height such as "1634p",
// "1080p60", "1920x1080", or no name) the standard name of its picture, else the name
// as it is; "" when nothing names it.
func qualityName(t trackView) string {
	return media.TrackName(t.Name, t.Width, t.Height, t.FrameRate)
}

// trackSize formats a track's size: "412.0 MB", "~286.1 MB" for an estimate (the site's,
// or the daemon's measure of a track without a BANDWIDTH); without a size, "~" and the
// track's bandwidth × the presentation's duration, rounded to a byte as the desktop and
// the extension estimate it (ui/frontend/src/lib/media.ts estimatedBytes); "-" when
// neither is known.
func (v *mediaView) trackSize(t trackView) string {
	switch {
	case t.Size > 0 && t.SizeApprox:
		return "~" + formatBytes(t.Size)
	case t.Size > 0:
		return formatBytes(t.Size)
	case t.Bandwidth > 0 && v.Duration > 0:
		if b := math.Round(float64(t.Bandwidth) * v.Duration / 8); b >= 1 && b < math.MaxInt64 {
			return "~" + formatBytes(int64(b))
		}
	}
	return "-"
}

// codecNames names the codecs of a track ("avc1.640028, mp4a.40.2" → "H.264, AAC"); codecs
// it does not know are shown as given.
func codecNames(codecs string) string {
	if codecs == "" {
		return ""
	}
	parts := strings.Split(codecs, ",")
	for i, c := range parts {
		c = strings.TrimSpace(c)
		family, _, _ := strings.Cut(strings.ToLower(c), ".")
		switch family {
		case "avc1", "avc3":
			c = "H.264"
		case "av01":
			c = "AV1"
		case "hvc1", "hev1":
			c = "HEVC"
		case "vp09", "vp9":
			c = "VP9"
		case "mp4a":
			c = "AAC"
		case "opus":
			c = "Opus"
		case "ac-3":
			c = "AC-3"
		case "ec-3":
			c = "E-AC-3"
		}
		parts[i] = c
	}
	return strings.Join(parts, ", ")
}

// trackPicture describes a track's picture, the detail behind its quality name:
// "1920x1080 29.97fps", or "-".
func trackPicture(t trackView) string {
	var parts []string
	if t.Width > 0 && t.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", t.Width, t.Height))
	}
	if t.FrameRate > 0 {
		parts = append(parts, strconv.FormatFloat(t.FrameRate, 'f', -1, 64)+"fps")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

// formatBandwidth formats bits per second with decimal units: "128 kbit/s", "5.0 Mbit/s",
// "1.5 Gbit/s", "-". A value that would round to 1000 of a unit is shown in the next one
// (999,500 bit/s is "1.0 Mbit/s", never "1000 kbit/s").
func formatBandwidth(bps int64) string {
	switch {
	case bps <= 0:
		return "-"
	case bps < 1000:
		return fmt.Sprintf("%d bit/s", bps)
	case bps < 999_500:
		return fmt.Sprintf("%d kbit/s", (bps+500)/1000)
	}
	return stepUnits(float64(bps)/1e6, "Mbit/s", "Gbit/s", "Tbit/s", "Pbit/s", "Ebit/s")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// describeSelection names the tracks a media download fetches by their quality names:
// "HLS: video v3 1080p (Full HD) + audio a1 (en)", or for a web page "YouTube video:
// 1080p (Full HD) H.264 + audio 140 (en)".
func describeSelection(m *downexv1.MediaInfo) string {
	if m == nil {
		return "media"
	}
	site := m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
	byID := make(map[string]*downexv1.MediaTrack, len(m.GetTracks()))
	for _, t := range m.GetTracks() {
		byID[t.GetId()] = t
	}
	var parts []string
	if id := m.GetSelectedVideo(); id != "" {
		s := "video " + printable(id)
		var name string
		t := byID[id]
		if t != nil {
			name = qualityName(trackView{Name: t.GetName(), Width: int(t.GetWidth()), Height: int(t.GetHeight()),
				FrameRate: t.GetFrameRate()})
		}
		switch {
		case name == "":
		case site:
			s = printable(name) + " " + printable(codecNames(t.GetCodecs()))
		default:
			s += " " + printable(name)
		}
		parts = append(parts, strings.TrimSpace(s))
	}
	if id := m.GetSelectedAudio(); id != "" {
		s := "audio " + printable(id)
		if t := byID[id]; t != nil && t.GetLanguage() != "" {
			s += " (" + printable(t.GetLanguage()) + ")"
		}
		parts = append(parts, s)
	}
	prefix := strings.ToUpper(m.GetFormat())
	if site {
		prefix = "video"
		if m.GetSite() != "" {
			prefix = printable(m.GetSite()) + " video"
		}
	}
	if len(parts) == 0 {
		return prefix
	}
	return prefix + ": " + strings.Join(parts, " + ")
}

// fragmentProgress formats a media download's fragment progress: "45/120 fragments".
func fragmentProgress(done, total int) string {
	if total <= 0 {
		return fmt.Sprintf("%d fragments", done)
	}
	return fmt.Sprintf("%d/%d fragments", done, total)
}

// partProgress is fragmentProgress for the parts of a web page's direct formats.
func partProgress(done, total int) string {
	if total <= 0 {
		return fmt.Sprintf("%d parts", done)
	}
	return fmt.Sprintf("%d/%d parts", done, total)
}

// directSite reports a web page's direct formats (format "http"), downloaded in parts of
// uneven sizes and measured by their bytes (docs/SITE-MEDIA-SPEC.md §2.4, §3.7); a site's
// HLS/DASH stream counts fragments like any stream.
func directSite(m *downexv1.MediaInfo) bool {
	return m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE && m.GetFormat() == "http"
}

// pieceProgress is m's parts (directSite) or fragments: "4/37 parts", "45/120 fragments".
func pieceProgress(m *downexv1.MediaInfo) string {
	done, total := int(m.GetFragmentsDone()), int(m.GetFragmentsTotal())
	if directSite(m) {
		return partProgress(done, total)
	}
	return fragmentProgress(done, total)
}

// mediaProgressLine formats the progress line of a running media download: fragments,
// percent, bytes received, speed and connections. When the daemon knows the size of the
// tracks (a web page's video) the percentage and the bytes are of that size; its estimate
// of a stream's size (size_approx) follows the bytes received ("of ~2.1 GB"), with the
// time left it gives, while the fragments make the percentage.
func mediaProgressLine(d *downexv1.Download) string {
	m := d.GetMedia()
	done, total := int(m.GetFragmentsDone()), int(m.GetFragmentsTotal())
	size := rpcSize(d)
	var b strings.Builder
	switch {
	case size > 0:
		fmt.Fprintf(&b, "%5.1f%%  %s/%s  ", percent(d.GetReceived(), size), formatBytes(d.GetReceived()), formatBytes(size))
	case total > 0:
		fmt.Fprintf(&b, "%5.1f%%  ", percent(int64(done), int64(total)))
	}
	if size > 0 {
		fmt.Fprintf(&b, "%s/s  %d conn", formatBytes(int64(d.GetSpeedBps())), d.GetConnections())
		return b.String()
	}
	pieces := pieceProgress(m)
	received := formatBytes(d.GetReceived())
	if d.GetSizeApprox() && d.GetSize() > 0 {
		received += " of ~" + formatBytes(d.GetSize())
	}
	fmt.Fprintf(&b, "%s  %s  %s/s  %d conn", pieces, received, formatBytes(int64(d.GetSpeedBps())), d.GetConnections())
	if eta := approxETA(d); eta != "" {
		fmt.Fprintf(&b, "  ETA %s", eta)
	}
	return b.String()
}
