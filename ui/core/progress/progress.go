// Package progress is what the progress window of one download shows (DESKTOP-SPEC §7) and
// the texts of its download complete view (§8), as pure functions of the download, ported
// from ui/frontend/src/lib/progress.ts. A web page's video (SITE-MEDIA-SPEC §5.4) says
// "Page", "Getting video info…", "Part N" and "Merging…".
package progress

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
)

const (
	stateQueued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	stateProbing     = downexv1.DownloadState_DOWNLOAD_STATE_PROBING
	stateDownloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	statePaused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	stateCompleted   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	stateFailed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	stateCanceled    = downexv1.DownloadState_DOWNLOAD_STATE_CANCELED
	stateAwaiting    = downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION
)

// Window sizes in device-independent pixels; the host opens the window collapsed
// (ui/progress.go).
const (
	Width           = 520
	CollapsedHeight = 300
	ExpandedHeight  = 560
	// MinHeight is the host's minimum height (ui/progress.go).
	MinHeight = 260
	// CompleteHeight is the completion view's height when its content cannot be measured.
	CompleteHeight = 420
	// MaxHeight is the most the completion view grows the window; beyond it, its content
	// scrolls.
	MaxHeight = 720
	// Frame is the title bar and frame when the host cannot tell the window's outer size.
	Frame = 40
	// FitSlack is added to a fitted height: the host truncates window sizes to device pixels
	// both ways, up to about 1 px each at 125 % or 150 % scaling, and a page 1 px short shows
	// a full scrollbar. Invisible in the page's 12 px bottom padding.
	FitSlack = 3
)

// FitView is what a window fitted to its content shows.
type FitView string

// The fitted views: the completion view, or the progress view without its details.
const (
	ViewComplete  FitView = "complete"
	ViewCollapsed FitView = "collapsed"
)

// WindowFrame is the title bar and frame of the window: its outer height (the host's size;
// NaN when the host cannot tell) less the page's; the usual one when the difference cannot be
// a frame.
func WindowFrame(outerPx, innerPx float64) float64 {
	frame := outerPx - innerPx
	if !math.IsNaN(frame) && !math.IsInf(frame, 0) && frame >= 0 && frame <= 200 {
		return frame
	}
	return Frame
}

// FitWindowHeight is the window height that shows contentPx (the page's height with nothing
// in it scrolling, fractional) and the frame, rounded up with FitSlack, within the view's
// limits: the completion view from MinHeight, the collapsed progress view never below its
// usual CollapsedHeight; both at most MaxHeight (beyond it, the view scrolls). Content that
// cannot be measured (0, no layout) gets the view's usual height.
func FitWindowHeight(view FitView, contentPx, framePx float64) float64 {
	floor := float64(CollapsedHeight)
	if view == ViewComplete {
		floor = MinHeight
	}
	if !(contentPx > 0) {
		if view == ViewComplete {
			return CompleteHeight
		}
		return CollapsedHeight
	}
	frame := float64(Frame)
	if !math.IsNaN(framePx) && !math.IsInf(framePx, 0) && framePx >= 0 && framePx <= 200 {
		frame = framePx
	}
	return math.Min(MaxHeight, math.Max(floor, math.Ceil(contentPx+frame+FitSlack)))
}

// DetailsStorageKey is where a viewer's choice to show the details is kept (shared by all
// progress windows).
const DetailsStorageKey = "downex.progress.details"

func isMedia(d *downexv1.Download) bool {
	return d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
}

// IsAssembling reports a media download whose fragments are all there: the daemon is
// assembling the file.
func IsAssembling(d *downexv1.Download) bool {
	m := d.GetMedia()
	return d.GetState() == stateDownloading && isMedia(d) && m != nil && m.GetFragmentsTotal() > 0 &&
		m.GetFragmentsDone() >= m.GetFragmentsTotal()
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Title is the window title: "<percent>% <file name>", just the name while the size is
// unknown.
func Title(d *downexv1.Download) string {
	name := categories.DisplayName(d)
	if fraction := format.ProgressFraction(d); finite(fraction) {
		return format.Percent(fraction) + " " + name
	}
	return name
}

// AddressLabel is the label of a download's address: a web page's "Page", a stream's
// "Manifest", else "Address".
func AddressLabel(d *downexv1.Download) string {
	if !isMedia(d) {
		return "Address"
	}
	if isSiteDownload(d) {
		return "Page"
	}
	return "Manifest"
}

// PieceNames is what a media download's pieces are called: a web page's direct formats come
// in parts (byte ranges of one file, format "http"), streams in fragments.
func PieceNames(d *downexv1.Download) (one, many string) {
	if isMedia(d) && d.GetMedia().GetFormat() == "http" {
		return "Part", "Parts"
	}
	return "Fragment", "Fragments"
}

// Status is the Status row. A failure carries the daemon's reason (shown bidi-escaped by the
// caller).
func Status(d *downexv1.Download) string {
	switch d.GetState() {
	case stateProbing:
		// yt-dlp reads the page (and is set up first, the first time).
		if isSiteDownload(d) {
			return "Getting video info…"
		}
		return "Connecting…"
	case stateDownloading:
		if IsAssembling(d) {
			if isSiteDownload(d) {
				return "Merging…"
			}
			return "Assembling…"
		}
		conns := d.GetConnectionInfo()
		waiting := len(conns) > 0
		for _, c := range conns {
			if s := c.GetState(); s != downexv1.ConnectionState_CONNECTION_STATE_CONNECTING && s != downexv1.ConnectionState_CONNECTION_STATE_REQUESTING {
				waiting = false
				break
			}
		}
		if waiting {
			return "Connecting…"
		}
		return "Receiving data…"
	case statePaused:
		return "Paused"
	case stateQueued:
		if p := d.GetQueuePosition(); p > 0 {
			return "Waiting in queue (#" + strconv.Itoa(int(p)) + ")"
		}
		return "Waiting in queue"
	case stateCompleted:
		return "Completed"
	case stateFailed:
		if e := d.GetError(); e != "" {
			return "Failed: " + e
		}
		return "Failed: the download stopped with an error"
	case stateCanceled:
		return "Canceled"
	case stateAwaiting:
		return "Waiting for confirmation"
	}
	return "Unknown"
}

// ResumeSupport is "Resume capability": ranged transfers continue where they stopped; a
// stream keeps its fragments.
func ResumeSupport(d *downexv1.Download) string {
	if isMedia(d) {
		return "Yes"
	}
	switch d.GetMode() {
	case downexv1.Mode_MODE_RANGED:
		return "Yes"
	case downexv1.Mode_MODE_SINGLE:
		return "No"
	}
	return "Unknown"
}

// DownloadedText is "1.2 GB (45%)"; the percentage is left out while the size is unknown.
func DownloadedText(d *downexv1.Download) string {
	fraction := format.ProgressFraction(d)
	bytes := format.Bytes(float64(d.GetReceived()))
	if finite(fraction) && d.GetState() != stateCompleted {
		return bytes + " (" + format.Percent(fraction) + ")"
	}
	return bytes
}

// SizeText is "1.2 GB"; "~2.1 GB" while downexd estimates a stream's size; "Unknown" without
// one.
func SizeText(d *downexv1.Download) string {
	if d.GetSize() >= 0 && (d.GetSize() > 0 || d.GetState() == stateCompleted) {
		return format.Size(d)
	}
	return "Unknown"
}

// RateText is the transfer rate while data arrives, else a dash.
func RateText(d *downexv1.Download) string {
	if d.GetState() == stateDownloading && !IsAssembling(d) {
		return format.Speed(d.GetSpeedBps())
	}
	return format.Dash
}

// AverageMinActiveMS is the running time before the progress view shows an average speed: a
// download's first seconds are mostly connecting, and their figure jumps with every update.
const AverageMinActiveMS = 3000

// AverageBps is the average speed of the whole download in bytes per second: the bytes
// received over the time all its runs took (pauses excluded, as downexd counts it in
// activeMs). NaN while that time or the bytes are unknown.
func AverageBps(d *downexv1.Download) float64 {
	if !(d.GetActiveMs() > 0) || !(d.GetReceived() >= 0) {
		return math.NaN()
	}
	return float64(d.GetReceived()) / (float64(d.GetActiveMs()) / 1000)
}

// AverageRateText is the average shown beside the live rate ("7.6 MB/s"): only while data
// arrives, once the download has run AverageMinActiveMS in all; "" otherwise.
func AverageRateText(d *downexv1.Download) string {
	if d.GetState() != stateDownloading || IsAssembling(d) || !(d.GetActiveMs() >= AverageMinActiveMS) {
		return ""
	}
	if bps := AverageBps(d); bps > 0 {
		return format.Speed(bps)
	}
	return ""
}

// TimeLeftText is the time left while data arrives, else a dash.
func TimeLeftText(d *downexv1.Download) string {
	if d.GetState() != stateDownloading || IsAssembling(d) {
		return format.Dash
	}
	if eta := format.EtaSeconds(d); finite(eta) {
		return format.Eta(eta)
	}
	return format.Dash
}

// DoneAtText is when a running download should be done, on the locale's clock ("11:59",
// "11:59 AM"), from its time left; "" when that is unknown (DESIGN-SYSTEM.md §4.8: "Done ≈
// 11:59"). now is the current time in epoch milliseconds.
func DoneAtText(d *downexv1.Download, now float64) string {
	if d.GetState() != stateDownloading || IsAssembling(d) {
		return ""
	}
	eta := format.EtaSeconds(d)
	if !finite(eta) || eta < 0 {
		return ""
	}
	return format.Clock(now + eta*1000)
}

// HeaderPercent is the header's big percentage: "42%", "~42%" while the size is an estimate,
// "" while the size is unknown (DESIGN-SYSTEM.md §4.8).
func HeaderPercent(d *downexv1.Download) string {
	fraction := format.ProgressFraction(d)
	if !finite(fraction) {
		return ""
	}
	if d.GetSizeApprox() && d.GetState() != stateCompleted {
		return "~" + format.Percent(fraction)
	}
	return format.Percent(fraction)
}

// FragmentsText is "450 / 1800" for a media download (fragments done of all), else "".
func FragmentsText(d *downexv1.Download) string {
	m := d.GetMedia()
	if !isMedia(d) || m == nil || !(m.GetFragmentsTotal() > 0) {
		return ""
	}
	return strconv.Itoa(int(m.GetFragmentsDone())) + " / " + strconv.Itoa(int(m.GetFragmentsTotal()))
}

// QualityText is the chosen quality of a stream or a web page's video in plain words: "1080p ·
// H.264 · AAC 128 kbit/s" (DESIGN-SYSTEM.md §4.8); "" for a file or when nothing is known.
func QualityText(d *downexv1.Download) string {
	if !isMedia(d) || d.GetMedia() == nil {
		return ""
	}
	return codecSummary(d.GetMedia())
}

// QualityDetail is the details behind QualityText, for its tooltip: each chosen track's name,
// codecs, rate and picture ("1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps ·
// English · 128 kbit/s · mp4a.40.2"); "" for a file.
func QualityDetail(d *downexv1.Download) string {
	if !isMedia(d) || d.GetMedia() == nil {
		return ""
	}
	video, audio := chosenTracks(d.GetMedia())
	var parts []string
	for _, s := range []string{video, audio} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

// ConnectionText is the Info column of a connection (from its state and the daemon's detail);
// a media download's names its piece ("Fragment 3 · …", "Part 3 · …" for a web page's video;
// piece is "Fragment" when "").
func ConnectionText(c *downexv1.ConnectionInfo, media bool, piece string) string {
	if piece == "" {
		piece = "Fragment"
	}
	t := connectionStateText(c)
	if media && c.GetFragment() >= 0 {
		return piece + " " + strconv.Itoa(int(c.GetFragment())+1) + " · " + t
	}
	return t
}

// retryIn is downexd's "<reason>, retrying in <wait>" (e.g. "HTTP 503, retrying in 4 s"):
// /^(.+?),\s*retrying in (.+)$/i, with JavaScript's \s and ASCII-only case folding.
var retryIn = regexp.MustCompile(`^([^\n\r\x{2028}\x{2029}]+?),[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*[rR][eE][tT][rR][yY][iI][nN][gG] [iI][nN] ([^\n\r\x{2028}\x{2029}]+)$`)

// mentionsRetry is /retry/i.
func mentionsRetry(s string) bool { return strings.Contains(asciiLower(s), "retry") }

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func connectionStateText(c *downexv1.ConnectionInfo) string {
	switch c.GetState() {
	case downexv1.ConnectionState_CONNECTION_STATE_CONNECTING:
		return "Connecting…"
	case downexv1.ConnectionState_CONNECTION_STATE_REQUESTING:
		return "Sending request…"
	case downexv1.ConnectionState_CONNECTION_STATE_RECEIVING:
		return "Receiving data…"
	case downexv1.ConnectionState_CONNECTION_STATE_RETRYING:
		detail := c.GetDetail()
		if detail == "" {
			return "Retrying…"
		}
		if m := retryIn.FindStringSubmatch(detail); m != nil {
			return "Retrying in " + m[2] + " (" + m[1] + ")"
		}
		if mentionsRetry(detail) {
			return detail
		}
		return "Retrying (" + detail + ")"
	case downexv1.ConnectionState_CONNECTION_STATE_IDLE:
		return "Idle"
	case downexv1.ConnectionState_CONNECTION_STATE_DONE:
		return "Finished"
	case downexv1.ConnectionState_CONNECTION_STATE_ERROR:
		if detail := c.GetDetail(); detail != "" {
			return "Error: " + detail
		}
		return "Error"
	}
	if detail := c.GetDetail(); detail != "" {
		return detail
	}
	return format.Dash
}

// ConnectionReceived is the Downloaded column: what the connection received of its current
// range.
func ConnectionReceived(c *downexv1.ConnectionInfo) string {
	return format.Bytes(math.Max(0, float64(c.GetReceived())))
}

// ---- the download complete dialog (DESKTOP-SPEC §8) ---------------------------------

func pad2(n float64) string {
	s := format.Num(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// FormatElapsed is a running time: "<1s", "45s", "3m 05s", "2h 07m 12s", "3d 04h 10m"; "—"
// when unknown.
func FormatElapsed(ms float64) string {
	if !finite(ms) || ms < 0 {
		return format.Dash
	}
	s := format.Round(ms / 1000)
	switch {
	case s < 1:
		return "<1s"
	case s < 60:
		return format.Num(s) + "s"
	case s < 3600:
		return format.Num(math.Floor(s/60)) + "m " + pad2(math.Mod(s, 60)) + "s"
	case s < 86400:
		return format.Num(math.Floor(s/3600)) + "h " + pad2(math.Floor(math.Mod(s, 3600)/60)) + "m " + pad2(math.Mod(s, 60)) + "s"
	}
	return format.Num(math.Floor(s/86400)) + "d " + pad2(math.Floor(math.Mod(s, 86400)/3600)) + "h " + pad2(math.Floor(math.Mod(s, 3600)/60)) + "m"
}

// tsMillis is a timestamp as the window reads it: epoch milliseconds, 0 when unset or not
// after the epoch (wails.ts timestamp()).
func tsMillis(ts *timestamppb.Timestamp) float64 {
	if ts == nil {
		return 0
	}
	if ms := ts.AsTime().UnixMilli(); ms > 0 {
		return float64(ms)
	}
	return 0
}

// TimeTakenText is "Time taken": the time the download spent running (pauses excluded) as
// downexd counts it; without that, the time from its first start to its completion; else
// unknown.
func TimeTakenText(d *downexv1.Download) string {
	if d.GetActiveMs() > 0 {
		return FormatElapsed(float64(d.GetActiveMs()))
	}
	started, completed := tsMillis(d.GetStartedAt()), tsMillis(d.GetCompletedAt())
	if started > 0 && completed >= started {
		return FormatElapsed(completed - started)
	}
	return "Unknown"
}

// TimeTakenSummary is the completion view's "Time taken" with the average speed of the whole
// download over it ("3m 00s · average 7.6 MB/s"); just TimeTakenText when downexd did not
// count the running time (the time from the first start includes pauses) or no bytes are
// known.
func TimeTakenSummary(d *downexv1.Download) string {
	taken := TimeTakenText(d)
	if bps := AverageBps(d); bps > 0 {
		return taken + " · average " + format.Speed(bps)
	}
	return taken
}

// MediaFormatText is a stream's format and the file it became ("HLS stream, saved as MP4"); a
// web page's video and its file ("YouTube video, saved as MP4"); "" for files. The quality is
// QualityText.
func MediaFormatText(d *downexv1.Download) string {
	m := d.GetMedia()
	if !isMedia(d) || m == nil {
		return ""
	}
	if isSiteDownload(d) {
		if container := containerLabel(m.GetContainer()); container != "" {
			return siteVideoLabel(m) + ", saved as " + container
		}
		return siteVideoLabel(m)
	}
	f := formatLabel(m.GetFormat())
	container := containerLabel(m.GetContainer())
	switch {
	case f != "" && container != "":
		return f + " stream, saved as " + container
	case f != "":
		return f + " stream"
	case container != "":
		return "Saved as " + container
	}
	return ""
}

// IsRefused reports a failure that resuming cannot help with: Downex never downloads
// protected or live streams, and a page without an MP4 video (or the chosen quality) stays so.
func IsRefused(d *downexv1.Download) bool { return cannotRetry(d.GetErrorCode()) }
