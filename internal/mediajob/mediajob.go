// Package mediajob downloads one unencrypted HTTP streaming presentation
// (docs/MEDIA-SPEC.md §6 steps 3–5) as one MP4 (docs/VIDEO-DETECT-SPEC.md §3.3).
// ProbeMP4 fetches and parses the manifest (an HLS playlist per RFC 8216, or a static
// MPEG-DASH MPD per ISO/IEC 23009-1) with internal/media, lists the tracks that can be
// saved as an MP4 and resolves the track selection. Run plans the selected tracks,
// downloads their fragments in parallel over the shared transport and limiter into a
// job-private part directory, checks every fMP4 initialization segment for protection
// and the start of every transport stream (remux.CheckTS) before any other media
// fragment is fetched, commits a per-track bitmap of durable fragments through a
// checkpoint callback, and finally assembles the output with internal/remux, which
// converts transport streams to MP4.
//
// RunRanged downloads the formats of a web page resolved with yt-dlp
// (docs/SITE-MEDIA-SPEC.md §2.4–§2.6): each chosen format is one resource, fetched as
// byte-range chunks over several connections into one file per track, made durable in
// synced batches, checked for protection before anything but its first chunk is
// fetched, and joined with internal/remux (or, for one muxed MP4 file, moved into place
// as it is). Expired addresses are renewed through the caller's Refresh.
//
// Cancelling the context of Run (or RunRanged) pauses the job: fragments already durable stay on disk,
// the last checkpoint describes them, and a later Run with that checkpoint fetches only
// the rest. Errors are values that wrap the sentinels of this package, of internal/media
// (ErrEncrypted, ErrLive, ErrUnsupported, ...) or of internal/transport. Error messages
// never repeat URLs, which may carry credentials.
package mediajob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"downex/internal/limiter"
	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/remux"
	"downex/internal/transport"
)

// Selection names a presentation and the tracks to download.
type Selection struct {
	// ManifestURL is the absolute http(s) URL of the HLS playlist or DASH MPD.
	ManifestURL string `json:"manifestUrl"`
	// Headers are the request headers the page used for the manifest (Cookie, Referer,
	// User-Agent, ...). Every request of the job carries them, except that
	// credentials (Authorization, Cookie) are sent only to the manifest URL's origin.
	Headers http.Header `json:"headers,omitempty"`
	// VideoTrack and AudioTrack are internal/media Track IDs; "" selects the default
	// pick (media.DefaultPair). An audio track cannot be added to a muxed video track.
	VideoTrack string `json:"videoTrack,omitempty"`
	AudioTrack string `json:"audioTrack,omitempty"`
	// VideoKey and AudioKey are the media.Track.Key values of the chosen tracks as an
	// earlier ProbeMP4 listed them ("" when unknown or when the track has none). A key
	// selects its track wherever the manifest now lists it (an HLS master playlist may
	// reorder its variants, which changes their IDs); a key the manifest no longer has
	// fails the selection (ErrInvalidSelection, media.ErrNotFound) rather than picking
	// whatever track now has the ID.
	VideoKey string `json:"videoKey,omitempty"`
	AudioKey string `json:"audioKey,omitempty"`
}

// Info describes a probed presentation and the resolved selection.
type Info struct {
	// Format is "hls" or "dash".
	Format string
	// Tracks lists the quality options of the manifest (media.HLSPlaylist.Options or
	// media.MPD.Options) that ProbeMP4 offers: those saved as an MP4.
	Tracks []media.Track
	// DefaultVideo and DefaultAudio are the IDs of the tracks the selection resolves to
	// ("" for none): the default pick when the selection names none.
	DefaultVideo, DefaultAudio string
	// Duration is the media duration of the longest selected track.
	Duration time.Duration
	// Container is the expected output: "mp4" ("ts" only for a LegacyTS run's own plan,
	// never from ProbeMP4).
	Container string
	// FragmentsTotal counts the requests of the selected tracks, initialization
	// segments included.
	FragmentsTotal int
}

// Checkpoint is the persisted progress of a job. It is JSON-serialisable.
type Checkpoint struct {
	Tracks []TrackCheckpoint `json:"tracks"`
	// DurableBytes counts the bytes this checkpoint records as durable: those of the
	// durable fragments (chunks) and of the durable chunk prefixes (TrackCheckpoint.Partial).
	// After the last checkpoint of a run it equals the last Progress.BytesDone of a
	// RunRanged job. It mirrors model.MediaCheckpoint.DurableBytes.
	DurableBytes int64 `json:"durableBytes,omitempty"`
	// BytesTotal is the exact size of the selected tracks when RunRanged knows every one
	// (0 otherwise, and for Run): the total survives a pause. It mirrors
	// model.MediaCheckpoint.BytesTotal.
	BytesTotal int64 `json:"bytesTotal,omitempty"`
}

// TrackCheckpoint records which fragments of one track are durable.
type TrackCheckpoint struct {
	// TrackID is the internal/media Track ID.
	TrackID string `json:"trackId"`
	// Total is the number of fragments of the track's plan, initialization segments
	// included. A resumed track whose plan has a different count starts over.
	Total int `json:"total"`
	// Done is a bitmap: bit i (Done[i/8] & (1 << (i%8))) is set when fragment i is
	// durable on disk.
	Done []byte `json:"done"`
	// Key is the media.Track.Key of the track ("" when it has none). A resumed track is
	// matched by its key when it has one (its ID may now name another track); when the
	// selected track's key differs, its fragments are not reused. Checkpoints without a
	// key (DASH, or written before keys existed) are matched by TrackID.
	Key string `json:"key,omitempty"`
	// ChunkBytes is the chunk size of a ranged track's plan (media.RangedTrack.ChunkBytes,
	// docs/SITE-MEDIA-SPEC.md §2.5); 0 for manifest tracks and for a site track fetched as
	// one whole resource (unknown size, or a server without ranges). A resumed ranged
	// track whose chunk size differs starts over. It mirrors
	// model.MediaTrackCheckpoint.ChunkBytes.
	ChunkBytes int64 `json:"chunkBytes,omitempty"`
	// Partial maps the index of a chunk of a ranged track (ChunkBytes > 0) whose bit is not
	// set to the length of its durable prefix: bytes from the chunk's start that were
	// written before a sync that completed before this checkpoint
	// (docs/SITE-MEDIA-SPEC.md §2.5). A resumed chunk continues after its prefix. Entries
	// that do not fit the plan (an index out of range, a chunk with its bit set, a length
	// not between 0 and the chunk's length, a track fetched as one whole resource) are
	// ignored: the chunk is fetched in full. Checkpoints written before prefixes existed
	// have none. It mirrors model.MediaTrackCheckpoint.Partial.
	Partial map[int]int64 `json:"partial,omitempty"`
}

// Progress is a snapshot of a running job, emitted at most 4 times per second and
// once more when Run stops.
type Progress struct {
	FragmentsTotal int
	FragmentsDone  int
	// BytesDone counts the bytes of durable fragments plus those of fragments being
	// received; Run takes back the bytes of a failed attempt, RunRanged keeps what a
	// chunk wrote (its retries continue after it) and takes back only a failed
	// whole-resource request. It is not what a caller should store as durable: that is
	// Checkpoint.DurableBytes.
	BytesDone int64
	// BytesTotal is the size of the selected tracks when it is known (a ranged job whose
	// tracks all have a size, docs/SITE-MEDIA-SPEC.md §2.4; the output's size, from a Run
	// that finds the job assembled), else, for a stream, an estimate of the file once each
	// track has a durable media fragment (BytesTotalApprox, the bytes of the fragments
	// expected in all: the MP4 written from them differs; see estimator), else 0. It
	// mirrors daemon.MediaProgress.BytesTotal.
	BytesTotal int64
	// BytesTotalApprox marks a BytesTotal that is an estimate: every stream's total
	// while Run downloads it. It mirrors daemon.MediaProgress.BytesTotalApprox.
	BytesTotalApprox bool
	// SpeedBps is an exponentially weighted moving average in bytes per second.
	SpeedBps float64
	// Connections is the number of requests in flight.
	Connections int
	// Workers has one entry per fetch worker of the current phase (initialization
	// segments, then media fragments), ordered by Slot (1-based). Fragment is the index
	// of the worker's current (or, once idle or done, last) fragment in the job,
	// counting the selected tracks' fragments in order (0 <= Fragment < FragmentsTotal);
	// Start/End its byte range in the resource, -1/-1 for a whole resource; Received
	// the bytes of the current attempt. Detail is short text without URLs or header
	// values: while retrying the reason and the wait left ("HTTP 503, retrying in 4 s",
	// RetryAt the time of the next attempt), the reason after a failure, the step while
	// connecting. The slice is fresh for every event: the receiver may keep it.
	Workers []model.WorkerView
}

// Connection limits, as in internal/engine.
const (
	DefaultMaxConnections = 32
	HardMaxConnections    = 32
)

// Defaults of the optional Options fields.
const (
	DefaultMaxRetries         = 8
	DefaultRetryBaseDelay     = 500 * time.Millisecond
	DefaultRetryMaxDelay      = 30 * time.Second
	DefaultCheckpointInterval = 2 * time.Second
)

// AssembleFunc joins the downloaded tracks into outPath; remux.Assemble is the default.
type AssembleFunc func(ctx context.Context, inputs []remux.Input, outPath string) (container string, err error)

// Options configures Run.
type Options struct {
	// Factory creates one transport slot per parallel connection (required).
	Factory transport.Factory
	// Limiter throttles the fragment bytes; nil means unlimited.
	Limiter limiter.Limiter
	// MaxConnections bounds the parallel fragment requests: the job uses
	// min(pending fragments, MaxConnections, the hosts' caps of HostRules). 0 selects
	// DefaultMaxConnections; values above HardMaxConnections are clamped.
	MaxConnections int
	// PartDir is the job-private directory for fragments (required). Run creates it and
	// leaves it in place after a successful assembly, with a record of the output in it:
	// the caller removes it once it has stored the completion. Until then a Run with
	// the same OutPath finds the finished output and returns at once, so a completion
	// the caller failed to store is not downloaded again.
	PartDir string
	// OutPath is the final file (required). Run writes OutPath+".dxpart" and renames
	// it to OutPath, never replacing an existing file unless ReplaceExisting is set.
	OutPath string
	// ReplaceExisting: the job downloads a completed download again to replace its file
	// (docs/ENGINE-SPEC.md §7.1). The assembled output then renames over a regular file
	// at OutPath, in one atomic rename once it is complete (placeOutput); a folder, a link
	// or a Windows reparse point there is never replaced (the rename fails with an error
	// matching fs.ErrExist).
	ReplaceExisting bool
	// Clock is the time source for backoff, progress and checkpoint intervals
	// (default: the real clock).
	Clock Clock

	// MaxRetries bounds the consecutive failed attempts of one request
	// (default DefaultMaxRetries).
	MaxRetries int
	// RetryBaseDelay and RetryMaxDelay shape the exponential backoff between attempts
	// (defaults DefaultRetryBaseDelay and DefaultRetryMaxDelay); Retry-After overrides
	// them.
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	// CheckpointInterval is the shortest time between two checkpoint callbacks while
	// fragments complete (default DefaultCheckpointInterval).
	CheckpointInterval time.Duration
	// Assemble replaces remux.Assemble (tests).
	Assemble AssembleFunc
	// CheckTS replaces remux.CheckTS (tests).
	CheckTS func(prefix []byte, k remux.Kind) error
	// ConcatTS replaces remux.ConcatTS (tests).
	ConcatTS func(ctx context.Context, in remux.Input, outPath string) error

	// HostRules supplies per-host connection caps, shared speed limiters and (through
	// the optional HostMaxRequestBytes, engine.RequestCaps) request length caps for Run
	// and RunRanged; nil applies none. RunRanged also halves a host's connections after
	// a 429 or 503 for the rest of the run.
	HostRules HostRules
	// MP4Only makes Run refuse, with an error wrapping media.ErrUnsupported, a selection
	// that would not be saved as an MP4 (every run but a LegacyTS one,
	// docs/VIDEO-DETECT-SPEC.md §3.3; transport streams are converted, so they pass): one
	// planned as another container before any fragment is requested, one whose data
	// turns out to be another container before its media fragments are, and an assembly
	// of another container before it is published. RunRanged writes MP4 only anyway.
	MP4Only bool
	// LegacyTS keeps a record stored before 2026-10-23 as a byte-for-byte .ts: a single
	// TS track is joined with ConcatTS instead of converted, and CheckTS is not run
	// (docs/VIDEO-DETECT-SPEC.md §3.3). It ignores MP4Only.
	LegacyTS bool
	// FreeSpace returns the bytes available to the user on the volume of dir. RunRanged
	// checks before it allocates its track files that the volume has room for the rest
	// of the tracks, the assembled output and 64 MiB (ErrDiskFull otherwise). nil (or an
	// error from it) skips the check.
	FreeSpace func(dir string) (int64, error)

	// syncFile replaces (*os.File).Sync of RunRanged's track files (tests).
	syncFile func(f *os.File) error
}

// Sentinel errors of this package; test with errors.Is.
var (
	// ErrInvalidOptions reports unusable Options.
	ErrInvalidOptions = errors.New("mediajob: invalid options")
	// ErrInvalidSelection reports a selection whose tracks the manifest does not have
	// (also wrapping media.ErrNotFound) or that cannot be combined.
	ErrInvalidSelection = errors.New("mediajob: invalid track selection")
	// ErrNotManifest reports a manifest URL that answered with something that is
	// neither an HLS playlist nor a DASH MPD (for example an HTML page).
	ErrNotManifest = errors.New("mediajob: not an HLS playlist or DASH MPD")
	// ErrRangeMismatch reports a server that answered with a byte range other than the
	// one requested.
	ErrRangeMismatch = errors.New("mediajob: server answered with a different byte range")
	// ErrTruncated reports a response body that ended early or a fragment without data.
	ErrTruncated = errors.New("mediajob: response ended early")
	// ErrDiskFull reports a volume without room for a RunRanged job: its free space is
	// below what the job needs, or a write or allocation failed because it is full.
	ErrDiskFull = errors.New("mediajob: disk full")
)

// StatusError reports an HTTP status the job could not use.
type StatusError struct {
	Code       int
	RetryAfter time.Duration // from Retry-After on 429/503, 0 if absent
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("mediajob: unexpected HTTP status %d %s", e.Code, http.StatusText(e.Code))
}

func (o Options) withDefaults() (Options, error) {
	switch {
	case o.Factory == nil:
		return o, fmt.Errorf("%w: no transport factory", ErrInvalidOptions)
	case o.PartDir == "":
		return o, fmt.Errorf("%w: no part directory", ErrInvalidOptions)
	case o.OutPath == "":
		return o, fmt.Errorf("%w: no output path", ErrInvalidOptions)
	}
	if o.MaxConnections <= 0 {
		o.MaxConnections = DefaultMaxConnections
	}
	o.MaxConnections = min(o.MaxConnections, HardMaxConnections)
	if o.Clock == nil {
		o.Clock = RealClock()
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = DefaultMaxRetries
	}
	if o.RetryBaseDelay <= 0 {
		o.RetryBaseDelay = DefaultRetryBaseDelay
	}
	if o.RetryMaxDelay <= 0 {
		o.RetryMaxDelay = DefaultRetryMaxDelay
	}
	o.RetryMaxDelay = max(o.RetryMaxDelay, o.RetryBaseDelay)
	if o.CheckpointInterval <= 0 {
		o.CheckpointInterval = DefaultCheckpointInterval
	}
	if o.Assemble == nil {
		o.Assemble = remux.Assemble
	}
	if o.CheckTS == nil {
		o.CheckTS = remux.CheckTS
	}
	if o.ConcatTS == nil {
		o.ConcatTS = remux.ConcatTS
	}
	return o, nil
}
