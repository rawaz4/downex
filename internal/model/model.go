// Package model holds the domain types shared by the engine, transport and store.
// See docs/ENGINE-SPEC.md §1.
package model

import (
	"maps"
	"net/http"
	"slices"
	"time"
)

// DownloadID identifies a download in the store.
type DownloadID int64

// Mode is how a download is fetched.
type Mode int

const (
	ModeUnknown Mode = iota // not probed yet
	ModeRanged              // server supports byte ranges and reports a total size
	ModeSingle              // one connection, no splitting, resume restarts from 0
)

func (m Mode) String() string {
	switch m {
	case ModeRanged:
		return "ranged"
	case ModeSingle:
		return "single"
	default:
		return "unknown"
	}
}

// State is the lifecycle state of a download.
type State int

const (
	StateQueued State = iota
	StateProbing
	StateDownloading
	StatePaused
	StateCompleted
	StateFailed
	StateCanceled
	// StateAwaitingConfirmation is a download handed off by the browser that waits
	// for the user to confirm its folder and name (docs/DESKTOP-SPEC.md §1). Nothing
	// runs it until it is confirmed (then queued or paused) or removed.
	StateAwaitingConfirmation

	// StateLast is the highest known state; stored values above it come from a newer build.
	StateLast = StateAwaitingConfirmation
)

func (s State) String() string {
	return [...]string{"queued", "probing", "downloading", "paused", "completed", "failed", "canceled",
		"awaiting"}[s]
}

// Source is who asked for a download (the daemon applies the confirmation policy to
// browser hand-offs). The values match the API's AddSource.
type Source int

const (
	SourceUnknown Source = iota // not recorded (downloads added before sources were stored)
	SourceCLI
	SourceUI
	SourceBrowser
	SourceClipboard // a URL seen on the clipboard (docs/M6-SPEC.md §3): confirmed like a browser hand-off
)

func (s Source) String() string {
	switch s {
	case SourceCLI:
		return "cli"
	case SourceUI:
		return "ui"
	case SourceBrowser:
		return "browser"
	case SourceClipboard:
		return "clipboard"
	default:
		return "unknown"
	}
}

// Request is what the caller (CLI, browser hand-off, UI) asks to download.
// Headers carry browser-supplied context such as Cookie, Referer and User-Agent.
type Request struct {
	URL     string      `json:"url"`
	Method  string      `json:"method,omitempty"` // empty means GET
	Headers http.Header `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"` // bounded POST body, replayed on every range request
}

// Validators identify the remote representation for safe resume.
type Validators struct {
	ETag         string `json:"etag,omitempty"`
	StrongETag   bool   `json:"strongEtag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

// IfRange returns the value to send in an If-Range header, or "" when no safe validator exists.
func (v Validators) IfRange() string {
	if v.ETag != "" && v.StrongETag {
		return v.ETag
	}
	return v.LastModified
}

// Download is the persisted record of one download.
type Download struct {
	ID             DownloadID `json:"id"`
	Request        Request    `json:"request"`
	FinalURL       string     `json:"finalUrl,omitempty"`
	Dir            string     `json:"dir"`
	Filename       string     `json:"filename"`
	Size           int64      `json:"size"` // -1 when unknown
	Mode           Mode       `json:"mode"`
	State          State      `json:"state"`
	Validators     Validators `json:"validators"`
	MaxConnections int        `json:"maxConnections"` // 0 means engine default
	SpeedLimit     int64      `json:"speedLimit"`     // bytes/s, 0 means unlimited
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	CompletedAt    time.Time  `json:"completedAt,omitempty"`
	// Requester's hints and the daemon's classification (docs/DESKTOP-SPEC.md §1–2).
	Source       Source `json:"source,omitempty"`
	ExpectedSize int64  `json:"expectedSize,omitempty"` // size hint from the requester; 0 = unknown (Size wins once probed)
	MIME         string `json:"mime,omitempty"`         // MIME hint from the requester, normalised ("" = none)
	Category     string `json:"category,omitempty"`     // category name chosen at Add ("" = none)
	// What the download is (docs/MEDIA-SPEC.md §6). A media download's Request is the
	// manifest request (URL and headers), Filename the output file, and Media its
	// presentation, track selection and fragment progress; the engine never runs it.
	Kind  Kind   `json:"kind,omitempty"`
	Media *Media `json:"media,omitempty"` // set for KindMedia, nil otherwise
	// Download complete dialog (docs/DESKTOP-SPEC.md §8). CompletionDialog is set once the
	// user opened the download's progress window: its completion or failure then opens
	// the dialog. StartedAt is when the download first started running (zero: never, or
	// not recorded); ActiveMS sums the milliseconds of all of its runs, so it excludes
	// the time it was paused, queued or stopped.
	CompletionDialog bool      `json:"completionDialog,omitempty"`
	StartedAt        time.Time `json:"startedAt,omitempty"`
	ActiveMS         int64     `json:"activeMs,omitempty"`
	// QueueID is the queue the download belongs to (docs/M6-SPEC.md §1; 0 = the main
	// queue when creating). Stores set it with CreateDownload and change it only through
	// their queue operations (QueueStore.MoveDownloads, DeleteQueue): UpdateDownload and
	// SaveCheckpoint keep the stored value, because runs write copies of the record that
	// were read before a move.
	QueueID QueueID `json:"queueId,omitempty"`
	// ReplaceFile: this download was completed and is downloaded again to replace its file
	// (docs/ENGINE-SPEC.md §7.1). The file under Filename stays until the new copy is
	// complete, which then replaces it (a regular file only); cleared when it completes.
	ReplaceFile bool `json:"replaceFile,omitempty"`
	// File check (docs/DESKTOP-SPEC.md §9). ServedType is the media type of the probe
	// response's Content-Type (lower case, no parameters; "" = none), set by the engine.
	// NameControls: the name the server or requester proposed held bidi overrides,
	// embeddings or isolates that sanitising removed. FileCheck is the final check of a
	// completed file: stores write it only through SetFileCheck, and UpdateDownload and
	// SaveCheckpoint keep the stored value (the QueueID rule: runs write copies).
	ServedType   string     `json:"servedType,omitempty"`
	NameControls bool       `json:"nameControls,omitempty"`
	FileCheck    *FileCheck `json:"fileCheck,omitempty"`
	// Expired links (docs/ENGINE-SPEC.md §7.2-7.3). PageURL is the page a file download
	// was started from (http or https; "" = unknown), set from the add. RemoteName is the
	// server's name for the file at the first probe (Content-Disposition, else the URL),
	// before any "name (n).ext" variant; "" before the probe and after a restart.
	PageURL    string `json:"pageUrl,omitempty"`
	RemoteName string `json:"remoteName,omitempty"`
	// RescuedBytes is how many bytes of this download were adopted from a partial file
	// another program (a browser) left unfinished (docs/ENGINE-SPEC.md §7.4); 0 = none.
	// Kept after completion; cleared by a restart (every reset of the remote state).
	RescuedBytes int64 `json:"rescuedBytes,omitempty"`
}

// FileCheckLevel is how much a file check has to say (docs/DESKTOP-SPEC.md §9).
type FileCheckLevel int

const (
	FileCheckNone    FileCheckLevel = iota // nothing to say
	FileCheckNote                          // a harmless difference, details only
	FileCheckCaution                       // probably not the wanted file; Open not held
	FileCheckDanger                        // the file pretends to be another type; Open asks first
)

// FileCheckStage is what a file check looked at (docs/DESKTOP-SPEC.md §9.1).
type FileCheckStage int

const (
	FileCheckStageName  FileCheckStage = iota // name, served type and MIME hint only
	FileCheckStageEarly                       // the first 64 KiB of a running download
	FileCheckStageFinal                       // the completed file, archive listing included
)

// FileCheck is the result of a file check (docs/DESKTOP-SPEC.md §9). It is advisory:
// it never says a file is safe.
type FileCheck struct {
	Level             FileCheckLevel `json:"level"`
	Stage             FileCheckStage `json:"stage"`
	Reason            string         `json:"reason"`            // stable code (§9.9)
	Content           string         `json:"content,omitempty"` // sniffed content id ("" = unknown)
	Brand             string         `json:"brand,omitempty"`   // content subtype (ISO-BMFF brand family, container kind, "dll")
	Claim             string         `json:"claim,omitempty"`   // the claimed type's label key (an extension or a served-type family)
	Served            string         `json:"served,omitempty"`  // the served Content-Type media type
	Member            string         `json:"member,omitempty"`  // the archive member the reason names
	ProgramMembers    int            `json:"programMembers,omitempty"`
	ListingIncomplete bool           `json:"listingIncomplete,omitempty"`
	CheckedAt         time.Time      `json:"checkedAt,omitempty"`
}

// Kind is what a download fetches.
type Kind int

const (
	KindFile  Kind = iota // one file through the segmented engine
	KindMedia             // an HLS/DASH presentation: fragments downloaded and assembled into one file
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindMedia:
		return "media"
	default:
		return "unknown"
	}
}

// MediaTrack is one selectable track of a streaming presentation (docs/MEDIA-SPEC.md §1).
type MediaTrack struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"` // "video" | "audio" | "muxed" | "subtitles"
	Bandwidth int64   `json:"bandwidth,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	FrameRate float64 `json:"frameRate,omitempty"` // 0 when unknown; always finite
	Codecs    string  `json:"codecs,omitempty"`
	Language  string  `json:"language,omitempty"`
	Name      string  `json:"name,omitempty"`
	// Key is the internal/media Track.Key ("" when the track has none): it finds the
	// chosen track again when the manifest lists its tracks in another order, which
	// changes HLS track IDs. Site tracks always have one (docs/SITE-MEDIA-SPEC.md §3.11).
	Key string `json:"key,omitempty"`
	// Size is the track's size in bytes as the site declares it (0 = unknown), an
	// estimate when SizeApprox is set; Ext is the site's file type for the track, "mp4"
	// or "m4a" ("" = unknown). Site media sets them (docs/SITE-MEDIA-SPEC.md §3.1); a
	// manifest's track has a Size only when the probe measured it (an HLS track without
	// a BANDWIDTH, docs/VIDEO-DETECT-SPEC.md §3.3), always with SizeApprox, and no Ext.
	Size       int64  `json:"size,omitempty"`
	SizeApprox bool   `json:"sizeApprox,omitempty"`
	Ext        string `json:"ext,omitempty"`
}

// Media is the streaming presentation of a media download and its progress.
type Media struct {
	// Origin is where the presentation comes from: "" for an HLS/DASH manifest
	// (docs/MEDIA-SPEC.md; every record written before site media), "site" for a web
	// page resolved with yt-dlp (docs/SITE-MEDIA-SPEC.md; then Request.URL and PageURL
	// are the page, and Site is the site's display name, e.g. "YouTube"). The
	// internal/sitemedia Origin constants name the values.
	Origin        string       `json:"origin,omitempty"`
	Site          string       `json:"site,omitempty"`
	Format        string       `json:"format"` // "hls" | "dash"; site media: "http" | "hls" | "dash"
	Tracks        []MediaTrack `json:"tracks"` // every quality option of the manifest
	SelectedVideo string       `json:"selectedVideo,omitempty"`
	SelectedAudio string       `json:"selectedAudio,omitempty"`
	// Duration of the presentation; 0 when unknown.
	Duration time.Duration `json:"duration,omitempty"`
	// Container is the output container: "mp4" or "ts" ("" = not known yet; site media:
	// always "mp4"). It is the expected one until the download completes, then the one
	// written.
	Container string `json:"container,omitempty"`
	Title     string `json:"title,omitempty"`   // page/media title hint
	PageURL   string `json:"pageUrl,omitempty"` // page the stream was found on
	// FragmentsTotal counts the requests of the selected tracks (0 = not known yet);
	// FragmentsDone and BytesDone the durable ones, as of the last checkpoint.
	FragmentsTotal int   `json:"fragmentsTotal,omitempty"`
	FragmentsDone  int   `json:"fragmentsDone,omitempty"`
	BytesDone      int64 `json:"bytesDone,omitempty"`
	// Clip is the part of the video to save (docs/MEDIA-SPEC.md §8); nil = the whole
	// video. It is fixed at Add or Confirm and kept by Pause, Resume and Download again.
	Clip *MediaClip `json:"clip,omitempty"`
	// Chapters are the video's chapters (site media only, docs/MEDIA-SPEC.md §8 C14),
	// for choosing a part; never embedded in the file.
	Chapters []Chapter `json:"chapters,omitempty"`
	// EntryID is the item of a page with several videos that the download saves, by the
	// site's id of it (site media only, docs/SITE-MEDIA-SPEC.md §3.12; "" = a page with
	// one video, or a record written before items): every run picks it by that id and
	// fails rather than take another. Items are the page's videos as the probe listed them
	// (without thumbnails) and ItemsLeftOut its other items, for the confirmation's list.
	EntryID      string      `json:"entryId,omitempty"`
	Items        []MediaItem `json:"items,omitempty"`
	ItemsLeftOut int         `json:"itemsLeftOut,omitempty"`
	// BrowserFetch: the bytes of this site download come through the user's browser
	// (docs/SITE-MEDIA-SPEC.md §2.8): its site's video servers answer only browsers, or a
	// run's own requests were refused again and it went on through the browser. Runs then
	// fetch through the browser from the start while one fetches for the download.
	BrowserFetch bool `json:"browserFetch,omitempty"`
	// FetchTicket is the browser fetch ticket of a download added from the browser with
	// browser_fetch (docs/SITE-MEDIA-SPEC.md §2.8, amendment 2026-11-19): kept so that
	// the same ticket serves the download again after downexd restarts, while the browser
	// still holds it. "" = none; cleared when the download completes. Never sent to clients.
	FetchTicket string `json:"fetchTicket,omitempty"`
	// Played holds the chosen files of a video saved from what the user's browser played
	// (origin "played", docs/SITE-MEDIA-SPEC.md §2.9): there is no page to read again, so
	// pause, resume and a restart of downexd use these addresses, as a manifest record
	// uses its manifest URL. Never logged beyond scheme://host/path, never shown; it holds
	// no cookie (none exists).
	Played []PlayedTrack `json:"played,omitempty"`
	// Checkpoint records which fragments are durable on disk; nil before the first one
	// and after completion. Stores persist it apart from the rest (it changes often).
	Checkpoint *MediaCheckpoint `json:"-"`
}

// PlayedTrack is one chosen file of a played video (docs/SITE-MEDIA-SPEC.md §2.9): ID is
// its track id, Kind "video", "audio" or "muxed", Size its exact size (0 = unknown),
// Fragmented that its samples are in movie fragments (it is joined, not copied, into a
// progressive MP4) and Browser that its first bytes needed the tab, so it is fetched
// through the browser.
type PlayedTrack struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
	Size       int64  `json:"size,omitempty"`
	Fragmented bool   `json:"fragmented,omitempty"`
	Browser    bool   `json:"browser,omitempty"`
}

// MediaClip is a part of a video (docs/MEDIA-SPEC.md §8): [StartMs, EndMs) in
// presentation time, EndMs 0 = to the end. The fields after Chapter are set by the
// daemon when the download completes (C20) and are shown only then.
type MediaClip struct {
	StartMs int64 `json:"startMs,omitempty"`
	EndMs   int64 `json:"endMs,omitempty"`
	// Chapter is the title of the chapter the part was chosen by ("" = custom times);
	// display only.
	Chapter string `json:"chapter,omitempty"`
	// KeyframeMs is the presentation time of the first decoded frame (≤ StartMs, or
	// later when there is no keyframe before it); FetchStartMs/FetchEndMs what was
	// downloaded; FetchedWhole: the resource had no index, so it was downloaded in full
	// and cut locally (C9).
	KeyframeMs   int64 `json:"keyframeMs,omitempty"`
	FetchStartMs int64 `json:"fetchStartMs,omitempty"`
	FetchEndMs   int64 `json:"fetchEndMs,omitempty"`
	FetchedWhole bool  `json:"fetchedWhole,omitempty"`
}

// MediaItem is one video of a page with several (docs/SITE-MEDIA-SPEC.md §3.12).
type MediaItem struct {
	ID       string        `json:"id"`
	Title    string        `json:"title,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
	// Thumbnail is the page's https image of the item, for a probe's answer only: it is
	// never stored (AddMedia clears it, and JSON leaves it out).
	Thumbnail string `json:"-"`
}

// Chapter is one chapter of a video (docs/MEDIA-SPEC.md §8 C14).
type Chapter struct {
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Title   string `json:"title,omitempty"`
}

// MediaCheckpoint is the persisted fragment progress of a media download.
type MediaCheckpoint struct {
	Tracks []MediaTrackCheckpoint `json:"tracks"`
	// LeadIn is the number of media fragments a part's window was extended back by to
	// find a keyframe (docs/MEDIA-SPEC.md §8 C5); 0 without a part.
	LeadIn int `json:"leadIn,omitempty"`
	// DurableBytes counts the bytes the checkpoint records as durable (its fragments and
	// chunk prefixes); BytesTotal is the exact size of the selected tracks when a site
	// job knows every one, else 0 (docs/SITE-MEDIA-SPEC.md §2.5). Checkpoints written
	// before 0.8.2 have neither.
	DurableBytes int64 `json:"durableBytes,omitempty"`
	BytesTotal   int64 `json:"bytesTotal,omitempty"`
}

// MediaTrackCheckpoint records which fragments of one track are durable: bit i of
// Done (Done[i/8] & (1 << (i%8))) is set when fragment i of the track's plan, which has
// Total fragments, is on disk. A ranged track (site media, docs/SITE-MEDIA-SPEC.md §2.5)
// records the chunk size its plan was cut with in ChunkBytes (0 for manifest tracks);
// its chunks are reused only with the same size. Partial maps a ranged chunk without its
// bit to the length of its durable prefix (from the chunk's start), so that a resume
// continues inside the chunk; nil in checkpoints written before 0.8.2.
type MediaTrackCheckpoint struct {
	TrackID    string        `json:"trackId"`
	Total      int           `json:"total"`
	Done       []byte        `json:"done"`
	Key        string        `json:"key,omitempty"` // the track's Key when it has one (see MediaTrack.Key)
	ChunkBytes int64         `json:"chunkBytes,omitempty"`
	Partial    map[int]int64 `json:"partial,omitempty"`
	// A part's window (docs/MEDIA-SPEC.md §8 C11/C12): First is the full-plan index of
	// the window's first fragment; Base the resource offset of a ranged track's first
	// byte in its file (a span); Head the length of its synced head file.
	First int   `json:"first,omitempty"`
	Base  int64 `json:"base,omitempty"`
	Head  int64 `json:"head,omitempty"`
}

// Clone returns a deep copy of m (nil for nil).
func (m *Media) Clone() *Media {
	if m == nil {
		return nil
	}
	c := *m
	c.Tracks = slices.Clone(m.Tracks)
	c.Chapters = slices.Clone(m.Chapters)
	c.Items = slices.Clone(m.Items)
	c.Played = slices.Clone(m.Played)
	if m.Clip != nil {
		clip := *m.Clip
		c.Clip = &clip
	}
	c.Checkpoint = m.Checkpoint.Clone()
	return &c
}

// Clone returns a deep copy of c (nil for nil).
func (c *MediaCheckpoint) Clone() *MediaCheckpoint {
	if c == nil {
		return nil
	}
	out := &MediaCheckpoint{DurableBytes: c.DurableBytes, BytesTotal: c.BytesTotal, LeadIn: c.LeadIn}
	if c.Tracks != nil {
		out.Tracks = make([]MediaTrackCheckpoint, len(c.Tracks))
		for i, t := range c.Tracks {
			t.Done = slices.Clone(t.Done)
			t.Partial = maps.Clone(t.Partial)
			out.Tracks[i] = t
		}
	}
	return out
}

// Track returns the track with id, or nil.
func (m *Media) Track(id string) *MediaTrack {
	if m == nil || id == "" {
		return nil
	}
	for i := range m.Tracks {
		if m.Tracks[i].ID == id {
			return &m.Tracks[i]
		}
	}
	return nil
}

// Segment is one byte range of a download. Ranges are inclusive.
// Durable counts bytes from Start that are flushed to disk and committed.
type Segment struct {
	ID      int   `json:"id"`
	Start   int64 `json:"start"`
	End     int64 `json:"end"` // inclusive; -1 means open-ended (single mode only)
	Durable int64 `json:"durable"`
}

// Done reports whether the segment's range is fully durable.
func (s Segment) Done() bool {
	return s.End >= 0 && s.Start+s.Durable > s.End
}

// WorkerState is what one worker connection of a running download is doing
// (docs/DESKTOP-SPEC.md §7). The values match the API's ConnectionState.
type WorkerState int

const (
	WorkerUnknown    WorkerState = iota
	WorkerConnecting             // dialling, TLS handshake or proxy setup
	WorkerRequesting             // request sent, waiting for the response head
	WorkerReceiving              // body bytes flowing
	WorkerRetrying               // waiting before a retry; Detail and RetryAt say why and when
	WorkerIdle                   // slot open without work
	WorkerDone                   // finished its range
	WorkerError                  // failed permanently; Detail has the reason
)

// WorkerView is a live snapshot of one worker connection, reported with progress events and
// never persisted. Detail is short human text without URLs or header values.
type WorkerView struct {
	Slot     int         // 1-based, stable while the download runs
	State    WorkerState //
	Start    int64       // current range, inclusive; -1 when none
	End      int64       //
	Received int64       // bytes received in the current range
	SpeedBps float64     //
	Detail   string      // e.g. "HTTP 503, retrying in 4 s"
	RetryAt  time.Time   // zero unless State == WorkerRetrying
	Fragment int         // media jobs: current fragment index, -1 otherwise
}
