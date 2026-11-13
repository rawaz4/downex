//go:build perf

package perf

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/daemon"
	"downex/internal/engine"
	"downex/internal/limiter"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/remux"
	"downex/internal/sitemedia"
	"downex/internal/store"
	"downex/internal/transport"
)

// defaultMaxConnections is the daemon's default_max_connections setting of a new
// installation (internal/daemon settings.go).
const defaultMaxConnections = daemon.DefaultMaxConnections

// client is the daemon's download stack (cmd/downexd main.go): the network runtime as
// proxy resolver, credentials and host rules, and an HTTP transport factory with HTTP/2.
type client struct {
	network *daemon.NetworkRuntime
	tf      *transport.HTTPFactory
}

// newClient builds the stack. Local runs go direct (proxy NONE) and trust roots; real
// runs use the settings of a new installation (SYSTEM proxy) and the system roots.
func newClient(ctx context.Context, real bool, roots *x509.CertPool) (*client, error) {
	network := daemon.NewNetworkRuntime(nil, nil)
	settings := daemon.DefaultNetworkSettings()
	if !real {
		settings.Proxy.Mode = daemon.ProxyModeNone
	}
	if err := network.Update(ctx, settings); err != nil {
		network.Close()
		return nil, fmt.Errorf("network settings: %w", err)
	}
	topts := transport.DefaultHTTPOptions()
	topts.UserAgent = "Downex/perf"
	topts.Resolver, topts.Credentials = network, network
	tf, err := transport.NewHTTPFactoryWithRoots(topts, roots)
	if err != nil {
		network.Close()
		return nil, err
	}
	return &client{network: network, tf: tf}, nil
}

func (c *client) close() { c.network.Close() }

// limiterChain returns a download's limiter as the daemon builds it (queue_limit.go):
// global → main queue → download, each level a chainLink.
func limiterChain(global, queue, download int64) limiter.Limiter {
	g := limiter.New(global)
	q := newChainLink(queue, g)
	return newChainLink(download, q)
}

// chainLink is internal/daemon's queue_limit.go chainLink (unexported there), copied so
// that the engine sees the same limiter type, with the same per-slice cost, as in the
// daemon.
type chainLink struct {
	own    limiter.Limiter
	parent atomic.Pointer[parentLimiter]
}

type parentLimiter struct{ limiter.Limiter }

const chainChunk = 64 << 10

func newChainLink(bytesPerSecond int64, parent limiter.Limiter) *chainLink {
	c := &chainLink{own: limiter.New(bytesPerSecond)}
	c.parent.Store(&parentLimiter{parent})
	return c
}

func (c *chainLink) WaitN(ctx context.Context, n int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for n > 0 && c.Limit() > 0 {
		chunk := min(n, chainChunk)
		if err := c.own.WaitN(ctx, chunk); err != nil {
			return err
		}
		if err := c.parent.Load().WaitN(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func (c *chainLink) SetLimit(bytesPerSecond int64) { c.own.SetLimit(bytesPerSecond) }
func (c *chainLink) Limit() int64                  { return tightest(c.own.Limit(), c.parent.Load().Limit()) }
func (c *chainLink) Child(bytesPerSecond int64) limiter.Limiter {
	return newChainLink(bytesPerSecond, c)
}

func tightest(a, b int64) int64 {
	switch {
	case a <= 0:
		return max(b, 0)
	case b <= 0:
		return a
	}
	return min(a, b)
}

// runResult is one measured download.
type runResult struct {
	Scenario string `json:"scenario"`
	Variant  string `json:"variant"`
	Tool     string `json:"tool"` // "downex" or "curl"
	Run      int    `json:"run"`
	URL      string `json:"url,omitempty"` // real runs only
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Verified bool   `json:"verified"`

	Bytes     int64      `json:"bytes"`
	WallS     float64    `json:"wall_s"`
	AvgMiBps  float64    `json:"avg_mibps"`
	PeakMiBps float64    `json:"peak_mibps"`
	T90S      float64    `json:"t90_s"`               // first time the windowed rate reached 90% of the run's peak
	CapMiBps  float64    `json:"cap_mibps,omitempty"` // what the emulated network allows at most
	TCap90S   float64    `json:"tcap90_s,omitempty"`  // first time the windowed rate reached 90% of the cap
	TTFBS     float64    `json:"ttfb_s"`              // first body byte (server side; real runs: first progress event with bytes)
	IdealS    float64    `json:"ideal_s,omitempty"`
	Eff       float64    `json:"efficiency,omitempty"` // ideal / wall
	ConnsMax  int        `json:"conns_max"`
	ConnSteps []connStep `json:"conn_steps,omitempty"`
	CPUS      float64    `json:"cpu_s"`
	CPUCores  float64    `json:"cpu_cores"` // cpu_s / wall_s
	AllocMiB  float64    `json:"alloc_mib"` // heap allocated during the run (whole process)
	GCs       uint32     `json:"gcs"`

	Warnings     map[string]int `json:"warnings,omitempty"`
	MinHostLimit int64          `json:"min_host_limit,omitempty"` // lowest per-host cap a 429/503/421 penalty set
	Server       *serverSummary `json:"server,omitempty"`
	Media        *mediaSummary  `json:"media,omitempty"`
	Curl         *curlSummary   `json:"curl,omitempty"`
	Profile      string         `json:"cpu_profile,omitempty"`
	SHA256       string         `json:"sha256,omitempty"`
	Timeline     []sample       `json:"timeline,omitempty"` // client-side progress (bytes received)
}

type mediaSummary struct {
	Fragments int     `json:"fragments"`
	ProbeS    float64 `json:"probe_s"`    // mediajob.ProbeMP4 (the daemon's Add/Probe; not in wall_s)
	FetchS    float64 `json:"fetch_s"`    // Run start → assembly start
	AssembleS float64 `json:"assemble_s"` // remux.Assemble
	OutBytes  int64   `json:"out_bytes"`
	Samples   int     `json:"video_samples"`
}

// fileRun describes one engine download.
type fileRun struct {
	url        string
	real       bool
	roots      *x509.CertPool
	maxConn    int   // the download's max_connections (the daemon passes the default for 0)
	speedLimit int64 // download speed limit (B/s, 0 = none)
	size       int64 // expected size for verification (-1: unknown, skip content check)
	verify     bool
	profile    string // CPU profile path ("" = none)
	server     *netServer
	timeout    time.Duration
	cl         *client // shared download stack (real runs: kept like the daemon's); nil = a fresh one
	hash       bool    // record the SHA-256 of the output (real runs)
}

// runEngine downloads fr.url with a fresh engine on a fresh SQLite store under work, as
// one daemon run does, and measures it.
func runEngine(ctx context.Context, work string, fr fileRun) (res runResult) {
	dir, err := os.MkdirTemp(work, "run-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	if fr.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, fr.timeout)
		defer cancel()
	}
	st, err := store.OpenSQLite(filepath.Join(dir, "downex.db"))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer st.Close()
	cl := fr.cl
	if cl == nil {
		if cl, err = newClient(ctx, fr.real, fr.roots); err != nil {
			res.Error = err.Error()
			return res
		}
		defer cl.close()
	}
	logs := newWarnCounter()
	eng := engine.New(st, cl.tf, engine.Options{
		HostRules:             cl.network,
		DefaultMaxConnections: defaultMaxConnections,
		Logger:                slog.New(logs),
		// Diagnostic overrides (0 = the engine's default, as the daemon runs it).
		FlushBytes:   int64(envInt("DOWNEX_PERF_FLUSH_BYTES", 0)),
		RampInterval: time.Duration(envInt("DOWNEX_PERF_RAMP_MS", 0)) * time.Millisecond,
	})
	maxConn := fr.maxConn
	if maxConn <= 0 {
		maxConn = defaultMaxConnections
	}
	d, err := eng.Add(ctx, model.Request{URL: fr.url}, dir, "", maxConn, fr.speedLimit)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	lim := limiterChain(0, 0, fr.speedLimit)

	var (
		mu      sync.Mutex
		samples = []sample{{}}
		steps   []connStep
		last    = -1
		maxC    int
		ttfb    float64
	)
	var t0 time.Time
	onProgress := func(p engine.Progress) {
		t := time.Since(t0).Seconds()
		mu.Lock()
		defer mu.Unlock()
		samples = append(samples, sample{T: t, Bytes: p.Received})
		if p.Received > 0 && ttfb == 0 {
			ttfb = t
		}
		if p.Connections != last {
			steps = append(steps, connStep{T: t, Conns: p.Connections})
			last = p.Connections
		}
		maxC = max(maxC, p.Connections)
	}
	var prof *os.File
	if fr.profile != "" {
		if prof, err = os.Create(fr.profile); err == nil {
			if err = pprof.StartCPUProfile(prof); err != nil {
				prof.Close()
				prof = nil
			}
		}
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	cpu0 := cpuTime()
	t0 = time.Now()
	if fr.server != nil {
		fr.server.begin(t0)
	}
	runErr := eng.Run(ctx, d.ID, lim, onProgress)
	wall := time.Since(t0)
	cpu := cpuTime() - cpu0
	runtime.ReadMemStats(&m1)
	res.AllocMiB, res.GCs = float64(m1.TotalAlloc-m0.TotalAlloc)/mib, m1.NumGC-m0.NumGC
	if fr.server != nil {
		fr.server.endSampling()
	}
	if prof != nil {
		pprof.StopCPUProfile()
		prof.Close()
		res.Profile = fr.profile
	}

	res.WallS, res.CPUS = wall.Seconds(), cpu.Seconds()
	res.CPUCores = res.CPUS / max(res.WallS, 1e-9)
	res.Warnings, res.MinHostLimit = logs.snapshot()
	mu.Lock()
	res.ConnsMax, res.ConnSteps, res.TTFBS = maxC, steps, ttfb
	tl := append([]sample(nil), samples...)
	mu.Unlock()
	if len(tl) > 0 && tl[len(tl)-1].T < res.WallS {
		// the last event comes with the stop; keep the timeline ending at the wall time
		tl = append(tl, sample{T: res.WallS, Bytes: tl[len(tl)-1].Bytes})
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	rec, err := st.GetDownload(context.WithoutCancel(ctx), d.ID)
	if err != nil {
		res.Error = errors.Join(runErr, err).Error()
		return res
	}
	res.OK = runErr == nil && rec.State == model.StateCompleted
	res.Bytes = rec.Size
	if !res.OK && len(tl) > 0 { // what was received, not the file's size
		res.Bytes = tl[len(tl)-1].Bytes
	}
	if res.OK && fr.hash {
		if sum, err := fileSHA256(filepath.Join(rec.Dir, rec.Filename)); err == nil {
			res.SHA256 = sum
		}
	}
	if res.OK && fr.verify {
		path := filepath.Join(rec.Dir, rec.Filename)
		size := fr.size
		if size < 0 {
			size = rec.Size
		}
		if err := verifyContent(path, size); err != nil {
			res.OK, res.Error = false, "verify: "+err.Error()
		} else {
			res.Verified = true
		}
	}
	res.AvgMiBps = float64(res.Bytes) / mib / max(res.WallS, 1e-9)
	if fr.server != nil {
		sum := fr.server.summary()
		sum.Timeline = fr.server.timeline()
		res.Server = &sum
		res.TTFBS = fr.server.firstByte().Seconds()
		rs := analyze(fr.server.timeline(), 0.5, 0)
		res.PeakMiBps, res.T90S = rs.PeakBps/mib, rs.T90
	} else {
		rs := analyze(tl, 1.0, 0)
		res.PeakMiBps, res.T90S = rs.PeakBps/mib, rs.T90
		res.Timeline = tl
	}
	return res
}

const mib = 1 << 20

// mediaRun describes one media job.
type mediaRun struct {
	manifest string
	roots    *x509.CertPool
	maxConn  int
	server   *netServer
	frames   int // expected video samples (0: not checked)
	profile  string
	timeout  time.Duration
}

// runMedia probes and downloads a manifest as the daemon does (cmd/downexd media.go:
// ProbeMP4 with sitemedia.ManifestTracksFor, then mediajob.Run with MP4Only, the
// transport factory, the run's limiter, max_connections and the host rules).
func runMedia(ctx context.Context, work string, mr mediaRun) (res runResult) {
	dir, err := os.MkdirTemp(work, "media-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	if mr.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, mr.timeout)
		defer cancel()
	}
	cl, err := newClient(ctx, false, mr.roots)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer cl.close()
	sel := mediajob.Selection{ManifestURL: mr.manifest}
	tp := time.Now()
	info, err := mediajob.ProbeMP4(ctx, cl.tf, sel, sitemedia.ManifestTracksFor(sel.VideoTrack, sel.VideoKey))
	probe := time.Since(tp)
	if err != nil {
		res.Error = "probe: " + err.Error()
		return res
	}
	sel.VideoTrack, sel.AudioTrack = info.DefaultVideo, info.DefaultAudio
	maxConn := mr.maxConn
	if maxConn <= 0 {
		maxConn = defaultMaxConnections
	}
	name := "video.mp4"
	out := filepath.Join(dir, name)
	var asmStart, asmEnd time.Time
	assemble := func(ctx context.Context, in []remux.Input, outPath string) (string, error) {
		asmStart = time.Now()
		defer func() { asmEnd = time.Now() }()
		return remux.Assemble(ctx, in, outPath)
	}
	opts := mediajob.Options{
		Factory: cl.tf, Limiter: limiterChain(0, 0, 0), MaxConnections: maxConn,
		PartDir: daemon.MediaPartsDir(dir, name), OutPath: out,
		HostRules: cl.network, MP4Only: true, Assemble: assemble,
	}
	var (
		mu      sync.Mutex
		samples = []sample{{}}
		steps   []connStep
		last    = -1
		maxC    int
	)
	var t0 time.Time
	onProgress := func(p mediajob.Progress) {
		t := time.Since(t0).Seconds()
		mu.Lock()
		defer mu.Unlock()
		samples = append(samples, sample{T: t, Bytes: p.BytesDone})
		if p.Connections != last {
			steps = append(steps, connStep{T: t, Conns: p.Connections})
			last = p.Connections
		}
		maxC = max(maxC, p.Connections)
	}
	// The daemon stores each checkpoint (JSON in SQLite); encoding it is the part that
	// costs the job's goroutine.
	onCheckpoint := func(c mediajob.Checkpoint) error {
		_, err := json.Marshal(c)
		return err
	}
	var prof *os.File
	if mr.profile != "" {
		if prof, err = os.Create(mr.profile); err == nil {
			if err = pprof.StartCPUProfile(prof); err != nil {
				prof.Close()
				prof = nil
			}
		}
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	cpu0 := cpuTime()
	t0 = time.Now()
	if mr.server != nil {
		mr.server.begin(t0)
	}
	container, runErr := mediajob.Run(ctx, opts, sel, nil, onProgress, onCheckpoint)
	wall := time.Since(t0)
	cpu := cpuTime() - cpu0
	runtime.ReadMemStats(&m1)
	res.AllocMiB, res.GCs = float64(m1.TotalAlloc-m0.TotalAlloc)/mib, m1.NumGC-m0.NumGC
	if mr.server != nil {
		mr.server.endSampling()
	}
	if prof != nil {
		pprof.StopCPUProfile()
		prof.Close()
		res.Profile = mr.profile
	}
	res.WallS, res.CPUS = wall.Seconds(), cpu.Seconds()
	res.CPUCores = res.CPUS / max(res.WallS, 1e-9)
	ms := &mediaSummary{Fragments: info.FragmentsTotal, ProbeS: probe.Seconds()}
	if !asmStart.IsZero() {
		ms.FetchS = asmStart.Sub(t0).Seconds()
		ms.AssembleS = asmEnd.Sub(asmStart).Seconds()
	}
	res.Media = ms
	mu.Lock()
	res.ConnsMax, res.ConnSteps = maxC, steps
	mu.Unlock()
	if runErr != nil {
		res.Error = runErr.Error()
	} else if container != "mp4" {
		res.Error = "container " + container
	}
	res.OK = res.Error == ""
	if mr.server != nil {
		sum := mr.server.summary()
		sum.Timeline = mr.server.timeline()
		res.Server = &sum
		res.Bytes = sum.BodyBytes
		res.TTFBS = mr.server.firstByte().Seconds()
		rs := analyze(mr.server.timeline(), 0.5, 0)
		res.PeakMiBps, res.T90S = rs.PeakBps/mib, rs.T90
	}
	if fi, err := os.Stat(out); err == nil {
		ms.OutBytes = fi.Size()
		if n, err := videoSamples(out); err == nil {
			ms.Samples = n
			res.Verified = mr.frames == 0 || n == mr.frames
		} else if res.OK {
			res.OK, res.Error = false, "mp4: "+err.Error()
		}
		if res.OK && mr.frames > 0 && !res.Verified {
			res.OK, res.Error = false, fmt.Sprintf("mp4 has %d video samples, want %d", ms.Samples, mr.frames)
		}
	}
	// The fetch phase's rate: the media bytes over the time until assembly.
	if ms.FetchS > 0 {
		res.AvgMiBps = float64(res.Bytes) / mib / ms.FetchS
	} else {
		res.AvgMiBps = float64(res.Bytes) / mib / max(res.WallS, 1e-9)
	}
	return res
}

// videoSamples counts the samples of the first track of the progressive MP4 at path.
func videoSamples(path string) (int, error) {
	fd, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return 0, err
	}
	if f.Moov == nil || len(f.Moov.Traks) == 0 {
		return 0, errors.New("no tracks")
	}
	return int(f.Moov.Traks[0].Mdia.Minf.Stbl.Stsz.GetNrSamples()), nil
}
