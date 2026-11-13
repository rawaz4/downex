// Package display holds the human-readable labels derived from download state that the table,
// the details pane and the speed and connection panels share, ported from
// ui/frontend/src/lib/display.ts. The functions are pure; "now" is a parameter where the
// TypeScript read Date.now().
package display

import (
	"math"
	"slices"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/progress"
	"downex/ui/core/text"
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

func isMedia(d *downexv1.Download) bool {
	return d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
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

// StatusText names a download's state for the table ("Queued #2", "Needs restart").
func StatusText(d *downexv1.Download) string {
	switch d.GetState() {
	case stateDownloading:
		return "Downloading"
	case stateProbing:
		// A web page's video: yt-dlp reads the page (SITE-MEDIA-SPEC §5.4).
		if isMedia(d) && d.GetMedia().GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
			return "Getting video info"
		}
		return "Connecting"
	case stateQueued:
		if p := d.GetQueuePosition(); p > 0 {
			return "Queued #" + format.Num(float64(p))
		}
		return "Queued"
	case statePaused:
		return "Paused"
	case stateCompleted:
		return "Completed"
	case stateFailed:
		if d.GetErrorCode() == "validator_changed" {
			return "Needs restart"
		}
		return "Failed"
	case stateCanceled:
		return "Canceled"
	case stateAwaiting:
		return "Waiting for confirmation"
	}
	return "Unknown"
}

// SizeHint is the size to show before a download is probed: its size, else the requester's
// hint, else -1.
func SizeHint(d *downexv1.Download) int64 {
	if d.GetSize() > 0 {
		return d.GetSize()
	}
	if d.GetExpectedSize() > 0 {
		return d.GetExpectedSize()
	}
	return -1
}

// SourceText names who added a download ("" when not recorded).
func SourceText(source downexv1.AddSource) string {
	switch source {
	case downexv1.AddSource_ADD_SOURCE_CLI:
		return "Command line"
	case downexv1.AddSource_ADD_SOURCE_UI:
		return "Downex window"
	case downexv1.AddSource_ADD_SOURCE_BROWSER:
		return "Browser"
	case downexv1.AddSource_ADD_SOURCE_CLIPBOARD:
		return "Clipboard"
	}
	return ""
}

var errorHints = map[string]string{
	"validator_changed": "The file on the server changed, or the saved progress no longer matches the partial file. It can only continue from the beginning.",
	"disk_full":         "The destination disk is full. Free some space, then resume.",
	"tls":               "The secure connection could not be verified. Downex never skips certificate checks.",
	"http_status":       "The server refused the request.",
	"network":           "The connection failed. Resuming keeps the data received so far.",
	"invalid_request":   "The daemon rejected the request.",
	"stalled":           "No data arrived for too long. Resuming keeps the data received so far.",
	"internal":          "An internal error occurred in the daemon.",
	"encrypted":         "The stream is encrypted or DRM-protected. Downex does not download protected content.",
	"live":              "The stream is live. Downex downloads only complete (on-demand) streams.",
	"auth_required":     "The server asks for a user name and password. Add a site login for it, then retry.",
}

// ErrorHint explains a failure code ("" for codes without a hint).
func ErrorHint(code string) string { return errorHints[code] }

// JoinPath joins a directory and file name with the separator the directory already uses, for
// display: bidi control characters are made visible (see text.EscapeBidi).
func JoinPath(dir, filename string) string { return text.EscapeBidi(RawJoinPath(dir, filename)) }

// RawJoinPath is JoinPath without the escaping: the path itself, for copying it.
func RawJoinPath(dir, filename string) string {
	if filename == "" {
		return dir
	}
	if dir == "" {
		return filename
	}
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	if strings.HasSuffix(dir, sep) {
		return dir + filename
	}
	return dir + sep + filename
}

// ProgressSummary is the spoken summary of a download's progress (the table's "I" shortcut).
// Live values are left out when the list is stale (the daemon is unreachable).
func ProgressSummary(d *downexv1.Download, stale bool) string {
	parts := []string{StatusText(d)}
	if fraction := format.ProgressFraction(d); !math.IsNaN(fraction) && !math.IsInf(fraction, 0) {
		parts = append(parts, format.Percent(fraction))
	}
	m := d.GetMedia()
	received := format.Bytes(float64(d.GetReceived()))
	if m != nil && m.GetFragmentsTotal() > 0 && !(isSiteDownload(d) && d.GetSize() > 0 && !d.GetSizeApprox()) {
		_, many := progress.PieceNames(d)
		of := ""
		if d.GetSizeApprox() && d.GetSize() > 0 {
			of = " of " + spokenSize(d)
		}
		parts = append(parts, format.Num(float64(m.GetFragmentsDone()))+" of "+format.Num(float64(m.GetFragmentsTotal()))+" "+text.LowerJS(many),
			received+of)
	} else {
		size := "unknown size"
		if d.GetSize() >= 0 {
			size = spokenSize(d)
		}
		parts = append(parts, received+" of "+size)
	}
	if d.GetState() == stateDownloading && !stale {
		parts = append(parts, format.Speed(d.GetSpeedBps()))
		if eta := format.EtaSeconds(d); !math.IsNaN(eta) && !math.IsInf(eta, 0) {
			parts = append(parts, format.Eta(eta)+" left")
		}
	}
	if stale {
		parts = append(parts, "last known state, downexd is not connected")
	}
	return categories.DisplayName(d) + ": " + strings.Join(parts, ", ")
}

// spokenSize is a known size as assistive technology reads it: "about 2.1 GB" for downexd's
// estimate.
func spokenSize(d *downexv1.Download) string {
	if d.GetSizeApprox() {
		return "about " + format.Bytes(float64(d.GetSize()))
	}
	return format.Bytes(float64(d.GetSize()))
}

// RowDescription is a row's stable secondary facts for its accessible description (no
// per-second values). now is the current time in epoch milliseconds.
func RowDescription(d *downexv1.Download, now float64) string {
	size := "unknown"
	if d.GetSize() >= 0 {
		size = spokenSize(d)
	}
	category := ""
	if c := d.GetCategory(); c != "" {
		category = ", category " + text.EscapeBidi(c)
	}
	stream := ""
	if isMedia(d) {
		if d.GetMedia() != nil && isSiteDownload(d) {
			stream = ", " + siteVideoLabel(d.GetMedia())
		} else {
			stream = ", " + MediaBadge(d) + " stream"
		}
	}
	added := "unknown"
	if ms := createdAt(d); ms > 0 {
		added = format.Date(ms, now)
	}
	return "Size " + size + category + ", added " + added + stream
}

// MediaBadge is the short label of a media download's badge: a web page's site ("YouTube",
// else "Video"), a stream's format ("HLS", "DASH"), else "Media".
func MediaBadge(d *downexv1.Download) string {
	m := d.GetMedia()
	if m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
		if s := text.TrimJS(m.GetSite()); s != "" {
			return s
		}
		return "Video"
	}
	if l := formatLabel(m.GetFormat()); l != "" {
		return l
	}
	return "Media"
}

// ---- the Studio list and details pane (DESIGN-SYSTEM.md §3.11, §3.15) -----------------------

// KindLabel is the label of a file kind ("Video"), for a download without a category.
func KindLabel(kind categories.FileKind) string {
	switch kind {
	case categories.Video:
		return "Video"
	case categories.Audio:
		return "Audio"
	case categories.Archives:
		return "Archives"
	case categories.Documents:
		return "Documents"
	case categories.Programs:
		return "Programs"
	case categories.Other:
		return "Other"
	}
	return ""
}

// RowHost is the host a row names: the hostname without "www." of a web page's video (its
// page), else of the final address (else the first). "" when the address does not parse.
func RowHost(d *downexv1.Download) string {
	addr := ""
	if isMedia(d) && d.GetMedia().GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
		addr = d.GetMedia().GetPageUrl()
	}
	if addr == "" {
		addr = d.GetFinalUrl()
	}
	if addr == "" {
		addr = d.GetUrl()
	}
	u, err := text.ParseURL(addr)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if len(host) >= 4 && strings.EqualFold(host[:4], "www.") {
		host = host[4:]
	}
	return text.EscapeBidi(host)
}

// RowCategory is a row's category: the one downexd chose, else the label of the file's kind.
func RowCategory(d *downexv1.Download) string {
	if c := d.GetCategory(); c != "" {
		return text.EscapeBidi(c)
	}
	return KindLabel(categories.KindOf(d))
}

// RowSubline is a row's second line: "youtube.com · Video" (the host is left out when there is
// none).
func RowSubline(d *downexv1.Download) string {
	var parts []string
	for _, s := range []string{RowHost(d), RowCategory(d)} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

// SourcePhrase is who added a download as a phrase ("from the browser"); "" when not recorded.
func SourcePhrase(source downexv1.AddSource) string {
	switch source {
	case downexv1.AddSource_ADD_SOURCE_CLI:
		return "from the command line"
	case downexv1.AddSource_ADD_SOURCE_UI:
		return "from the Downex window"
	case downexv1.AddSource_ADD_SOURCE_BROWSER:
		return "from the browser"
	case downexv1.AddSource_ADD_SOURCE_CLIPBOARD:
		return "from the clipboard"
	}
	return ""
}

// sameDay reports whether ms falls on the same calendar day as day (in the format time zone):
// format.Date shows a time of day only for the day it is given as "now".
func sameDay(ms, day float64) bool { return format.Date(ms, day) == format.Date(ms, ms) }

// createdAt is when a download was added, in epoch milliseconds (0 when unknown).
func createdAt(d *downexv1.Download) float64 { return tsMillis(d.GetCreatedAt()) }

// AddedText says when and from where a download was added, for the details pane: "Today,
// 14:02 · from the browser", "Yesterday, 09:15", "Oct 21, 18:40". "—" when the time is not
// known. now is the current time in epoch milliseconds.
func AddedText(d *downexv1.Download, now float64) string {
	ms := createdAt(d)
	if !(ms > 0) {
		return format.Date(ms, now)
	}
	if from := SourcePhrase(d.GetSource()); from != "" {
		return DayTimeText(ms, now) + " · " + from
	}
	return DayTimeText(ms, now)
}

// DayTimeText is a moment as "Today, 14:02", "Yesterday, 09:15" or "25 Oct, 18:31" (no time
// without a moment: "—").
func DayTimeText(ms, now float64) string {
	if !(ms > 0) {
		return format.Date(ms, now)
	}
	var day string
	switch {
	case sameDay(ms, now):
		day = "Today"
	case sameDay(ms, now-86_400_000):
		day = "Yesterday"
	default:
		day = format.Date(ms, now)
	}
	return day + ", " + format.Clock(ms)
}

// PillTone is the tone of a state pill (`.pill.<tone>` in app.css).
type PillTone string

// The pill tones.
const (
	ToneOK   PillTone = "ok"
	ToneInfo PillTone = "info"
	ToneWarn PillTone = "warn"
	ToneFail PillTone = "fail"
	ToneDone PillTone = "done"
)

// Pill is the details pane's state pill.
type Pill struct {
	Tone PillTone
	Text string
}

// StatePill is the details pane's state pill: "Downloading · 42%", "Paused · 18%", "Queued #2",
// "Failed".
func StatePill(d *downexv1.Download) Pill {
	status := StatusText(d)
	pct := format.Percent(format.ProgressFraction(d))
	withPct := status
	if pct != "" {
		withPct = status + " · " + pct
	}
	switch d.GetState() {
	case stateDownloading:
		return Pill{ToneOK, withPct}
	case stateProbing:
		return Pill{ToneOK, status}
	case statePaused:
		return Pill{ToneWarn, withPct}
	case stateFailed:
		return Pill{ToneFail, status}
	case stateAwaiting:
		return Pill{ToneInfo, status}
	}
	return Pill{ToneDone, status}
}

// ReceivedOf is the bytes received of a total with the unit said once where both share it:
// "1.4 of 3.4 GB", "900 MB of 3.4 GB", "~" before an estimated total; only the bytes when the
// total is unknown.
func ReceivedOf(received, size float64, approx bool) string {
	got := format.Bytes(jsMax(0, received))
	if !(size >= 0) {
		return got
	}
	total := format.Bytes(size)
	if approx {
		total = "~" + total
	}
	unit := func(s string) string { return s[strings.LastIndexByte(s, ' ')+1:] }
	if unit(got) == unit(total) {
		return jsSliceTo(got, strings.LastIndexByte(got, ' ')) + " of " + total
	}
	return got + " of " + total
}

// jsSliceTo is JavaScript's s.slice(0, end) for an end found by lastIndexOf: -1 cuts the last
// UTF-16 code unit.
func jsSliceTo(s string, end int) string {
	if end >= 0 {
		return s[:end]
	}
	if s == "" {
		return ""
	}
	r := []rune(s)
	if r[len(r)-1] > 0xFFFF {
		// a supplementary character is two code units: its high surrogate stays, as U+FFFD
		return string(r[:len(r)-1]) + "\ufffd"
	}
	return string(r[:len(r)-1])
}

// jsMax is JavaScript's Math.max of two numbers: NaN when either is.
func jsMax(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return math.Max(a, b)
}

// SelectionSummary is the multi-select strip: "3 downloads selected · 4.2 GB" (the sizes that
// are known; "~" when some are estimates).
func SelectionSummary(ds []*downexv1.Download) string {
	head := format.Num(float64(len(ds))) + " download"
	if len(ds) != 1 {
		head += "s"
	}
	head += " selected"
	total := 0.0
	approx := false
	for _, d := range ds {
		if d.GetSize() > 0 {
			total += float64(d.GetSize())
		}
		if d.GetSizeApprox() || d.GetSize() < 0 {
			approx = true
		}
	}
	if total > 0 {
		if approx {
			return head + " · ~" + format.Bytes(total)
		}
		return head + " · " + format.Bytes(total)
	}
	return head
}

// mb is 1 MB = 1,000,000 B (sizes and speeds are shown in decimal units, lib/format.ts).
const mb = 1000 * 1000

// ---- the list (DESIGN-SYSTEM.md §3.11) ----

// Column is a column of the download list.
type Column struct {
	// Key is the column's sort key (lib/query.ts SortKey).
	Key   string
	Label string
	// Title is the full name when the label is abbreviated.
	Title string
	End   bool
	// Width is the default width in rem; 0 = the flexible column (Name) that takes the rest.
	Width float64
	// Min is the smallest width in rem (resizing and fitting stop here).
	Min float64
	// Drop orders hiding when space runs out: from the highest down; 0 = always shown.
	Drop int
}

// columns is DESIGN-SYSTEM.md §3.11: Name flexible (min 12), Size 5, Progress 11, Speed 5.5,
// Time left 5, Conn. 3.25, Status 11.25, Added 4.25 (rem). Status is wide enough for its
// longest text, "Waiting for confirmation", on one line beside its mark; Added holds "27 Oct"
// or "14:02".
var columns = [...]Column{
	{Key: "name", Label: "Name", Width: 0, Min: 12, Drop: 0},
	{Key: "size", Label: "Size", End: true, Width: 5, Min: 4, Drop: 2},
	{Key: "progress", Label: "Progress", Width: 11, Min: 7, Drop: 0},
	{Key: "speed", Label: "Speed", End: true, Width: 5.5, Min: 4.5, Drop: 1},
	{Key: "eta", Label: "Time left", End: true, Width: 5, Min: 4, Drop: 3},
	{Key: "connections", Label: "Conn.", Title: "Connections", End: true, Width: 3.25, Min: 2.75, Drop: 4},
	{Key: "status", Label: "Status", Width: 11.25, Min: 6, Drop: 0},
	{Key: "added", Label: "Added", End: true, Width: 4.25, Min: 4, Drop: 5},
}

// Columns lists the list's columns in their order.
func Columns() []Column { return slices.Clone(columns[:]) }

// The rows' and header's inline padding (px): the name starts 20 px in, with its cell's 6 px.
const (
	RowPadStart = 14
	RowPadEnd   = 10
)

// RowHeightFor is the row height (px) for a root font size: Comfortable two lines (46 at
// 16 px), Compact one (24).
func RowHeightFor(rem float64, compact bool) float64 {
	if compact {
		return jsMax(24, format.Round(rem*1.5))
	}
	return jsMax(44, format.Round(rem*2.875))
}

// FitColumns is the columns that fit available px: low-priority columns are hidden first
// (Added, Conn., Time left, Size, Speed). Name, Progress and Status always stay.
func FitColumns(available, rem float64, widthOf func(Column) float64) []Column {
	cols := Columns()
	if !(available > 0) {
		return cols
	}
	need := func() float64 {
		sum := 0.0
		for _, c := range cols {
			if c.Width == 0 {
				sum += c.Min * rem
			} else {
				sum += widthOf(c)
			}
		}
		return sum
	}
	droppable := slices.DeleteFunc(Columns(), func(c Column) bool { return c.Drop <= 0 })
	slices.SortStableFunc(droppable, func(a, b Column) int { return b.Drop - a.Drop })
	for _, c := range droppable {
		if need() <= available {
			break
		}
		cols = slices.DeleteFunc(cols, func(x Column) bool { return x.Key == c.Key })
	}
	return cols
}

// ---- row cells by state (DESIGN-SYSTEM.md §3.12) ----

// ProgressCell is what the progress cell shows for a state.
type ProgressCell string

// The progress cells.
const (
	CellBar    ProgressCell = "bar"
	CellProbe  ProgressCell = "probe"
	CellReview ProgressCell = "review"
	CellNone   ProgressCell = "none"
)

// ProgressCellOf is what the progress cell shows for a download (DESIGN-SYSTEM.md §3.12).
func ProgressCellOf(d *downexv1.Download) ProgressCell {
	switch d.GetState() {
	case stateDownloading, statePaused:
		return CellBar
	case stateProbing:
		return CellProbe
	case stateQueued, stateFailed:
		// with progress kept from an earlier run
		if d.GetReceived() > 0 {
			return CellBar
		}
		return CellNone
	case stateAwaiting:
		return CellReview
	}
	// completed, canceled: the status says it, the Added column has the date
	return CellNone
}

// Action is a row's inline action.
type Action struct {
	// ID is "resume" or "restart".
	ID    string
	Label string
}

// InlineAction is the inline action of a failed row: Retry, Restart… (the file changed), or
// none (ok false) when retrying cannot help.
func InlineAction(d *downexv1.Download) (a Action, ok bool) {
	if d.GetState() != stateFailed || cannotRetry(d.GetErrorCode()) {
		return Action{}, false
	}
	if d.GetErrorCode() == "validator_changed" {
		return Action{ID: "restart", Label: "Restart…"}, true
	}
	return Action{ID: "resume", Label: "Retry"}, true
}

// ShowsConnections reports whether the panel can show live connections (else the static
// segment overview).
func ShowsConnections(d *downexv1.Download, stale bool) bool {
	return d.GetState() == stateDownloading && !stale && len(d.GetConnectionInfo()) > 0
}

// ---- the details pane's speed limit and figures (DESIGN-SYSTEM.md §3.15) ----

// CustomLimit is "Custom…" in the speed limit menu: the progress window's Speed limit tab.
const CustomLimit = -1

// LimitPresets lists the download speed limits offered in the pane (bytes per second;
// 0 = unlimited).
func LimitPresets() []float64 { return []float64{0, 20 * mb, 10 * mb, 5 * mb, mb} }

// LimitOption is an entry of the speed limit menu.
type LimitOption struct {
	Value float64
	Label string
}

// LimitOptions is the speed limit menu for a download: the presets, its own limit when it is
// another, and Custom….
func LimitOptions(current float64) []LimitOption {
	presets := LimitPresets()
	values := slices.Clone(presets)
	if current > 0 && !slices.Contains(values, current) {
		values = append(values, current)
	}
	positive := slices.DeleteFunc(slices.Clone(values), func(v float64) bool { return !(v > 0) })
	slices.SortStableFunc(positive, func(a, b float64) int { return jsCompare(b - a) })
	sorted := append([]float64{0}, positive...)
	out := make([]LimitOption, 0, len(sorted)+1)
	for _, v := range sorted {
		label := format.Speed(v)
		switch {
		case v == 0:
			label = "Unlimited"
		case slices.Contains(presets, v):
			label = format.Num(v/mb) + " MB/s"
		}
		out = append(out, LimitOption{Value: v, Label: label})
	}
	return append(out, LimitOption{Value: CustomLimit, Label: "Custom…"})
}

// jsCompare reads a comparator's number as Array.prototype.sort does (NaN as equal).
func jsCompare(d float64) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}

// SpeedParts splits a speed for the big figure: "5.8" and "MB/s".
func SpeedParts(bps float64) (n, unit string) {
	s := format.Speed(bps)
	if at := strings.LastIndexByte(s, ' '); at > 0 {
		return s[:at], s[at+1:]
	}
	return s, ""
}
