package engine

import (
	"io"
	"io/fs"
	"log/slog"
	"time"
)

// Connection limits (docs/ENGINE-SPEC.md §3).
const (
	// DefaultMaxConnections is the default per-host connection limit.
	DefaultMaxConnections = 32
	// HardMaxConnections is the absolute connection cap per download.
	HardMaxConnections = 32
)

// Default timings and sizes (docs/ENGINE-SPEC.md §5, §6, §8, §9).
const (
	defaultFlushInterval = 8 * time.Second
	// Each flush is a synchronous sync in the one writer goroutine, which every connection
	// waits on: 64 MiB measured slower than 256 MiB on disk-bound downloads.
	defaultFlushBytes         = 256 << 20
	defaultCheckpointInterval = 2500 * time.Millisecond
	defaultStallTimeout       = 20 * time.Second
	defaultMidStallTimeout    = 5 * time.Second
	defaultRampInterval       = 250 * time.Millisecond
	defaultMaxRetries         = 8
	defaultRetryBaseDelay     = 500 * time.Millisecond
	defaultRetryMaxDelay      = 30 * time.Second
	defaultHostPenaltyTTL     = 5 * time.Minute

	tickInterval     = 100 * time.Millisecond // speed recomputation and ramp-up steps
	progressInterval = 250 * time.Millisecond // progress events at most 4 Hz
	slowIdleMin      = 2 * time.Second        // slow worker: idle ≥ max(2 s, 4·RTT)

	// A read returns about one TLS record (16 KiB): a bigger slice mostly waits empty in
	// the write queue (256 KiB until 2026-10-29: up to 16 MiB per download).
	sliceUncapped = 64 << 10 // read slice without a speed cap
	sliceCapped   = 32 << 10 // read slice while a speed cap is active
	writeQueueLen = 32       // bounded worker→writer queue (slices in flight)
)

// Options configures an Engine. Zero values select the defaults of docs/ENGINE-SPEC.md.
type Options struct {
	// DefaultMaxConnections is the per-host limit for downloads that do not set
	// their own (default 32). It is clamped to HardCap.
	DefaultMaxConnections int
	// HardCap bounds every connection limit (default and maximum 32).
	HardCap int
	// FlushInterval is the longest time written data stays unflushed (default 8 s).
	FlushInterval time.Duration
	// FlushBytes forces a flush once this many bytes are unflushed (default 256 MiB).
	FlushBytes int64
	// CheckpointInterval bounds how often a changed segment layout is committed
	// between flushes (default 2.5 s). Durable offsets only advance at flushes.
	CheckpointInterval time.Duration
	// StallTimeout cancels a connection that made no progress for this long and
	// reassigns its remaining range (default 20 s). In single mode, which cannot
	// resume a response, the download fails (ErrStalled) and starts over.
	StallTimeout time.Duration
	// MidStallTimeout replaces StallTimeout for a connection whose current response
	// already delivered body bytes (default 5 s; at least 4·RTT once a request latency
	// is known and twice the longest wait for that response's bytes, at most
	// StallTimeout). Single mode keeps StallTimeout.
	MidStallTimeout time.Duration
	// RampInterval is the minimum time between ramp-up steps after the first (default 250 ms).
	RampInterval time.Duration
	// MaxRetries bounds consecutive failed attempts of one request (default 8).
	MaxRetries int
	// RetryBaseDelay and RetryMaxDelay shape the exponential backoff between
	// attempts (defaults 500 ms and 30 s). Retry-After overrides them.
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	// HostPenaltyTTL is how long a per-host limit lowered after 429/503/421 or a refused
	// connection is remembered (default 5 min).
	HostPenaltyTTL time.Duration
	// Clock is the time source (default: the real clock).
	Clock Clock
	// Logger receives warnings and debug events (default: discarded).
	Logger *slog.Logger

	// HostRules supplies per-host connection caps and shared speed limiters
	// (docs/M6-SPEC.md §2). nil applies no host rules.
	HostRules HostRules

	// openFile opens part files; tests substitute recording files.
	openFile func(name string, flag int, perm fs.FileMode) (partFile, error)
}

// partFile is the subset of *os.File the engine writes through.
type partFile interface {
	io.WriterAt
	Sync() error
	Truncate(size int64) error
	Stat() (fs.FileInfo, error)
	Close() error
}

func (o Options) withDefaults() Options {
	if o.HardCap <= 0 || o.HardCap > HardMaxConnections {
		o.HardCap = HardMaxConnections
	}
	if o.DefaultMaxConnections <= 0 {
		o.DefaultMaxConnections = DefaultMaxConnections
	}
	o.DefaultMaxConnections = min(o.DefaultMaxConnections, o.HardCap)
	if o.FlushInterval <= 0 {
		o.FlushInterval = defaultFlushInterval
	}
	if o.FlushBytes <= 0 {
		o.FlushBytes = defaultFlushBytes
	}
	if o.CheckpointInterval <= 0 {
		o.CheckpointInterval = defaultCheckpointInterval
	}
	if o.StallTimeout <= 0 {
		o.StallTimeout = defaultStallTimeout
	}
	if o.MidStallTimeout <= 0 {
		o.MidStallTimeout = defaultMidStallTimeout
	}
	if o.RampInterval <= 0 {
		o.RampInterval = defaultRampInterval
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = defaultMaxRetries
	}
	if o.RetryBaseDelay <= 0 {
		o.RetryBaseDelay = defaultRetryBaseDelay
	}
	if o.RetryMaxDelay <= 0 {
		o.RetryMaxDelay = defaultRetryMaxDelay
	}
	o.RetryMaxDelay = max(o.RetryMaxDelay, o.RetryBaseDelay)
	if o.HostPenaltyTTL <= 0 {
		o.HostPenaltyTTL = defaultHostPenaltyTTL
	}
	if o.Clock == nil {
		o.Clock = RealClock()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.openFile == nil {
		o.openFile = openOSFile
	}
	return o
}
