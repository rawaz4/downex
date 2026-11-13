//go:build perf

package perf

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// config is read from the environment:
//
//	DOWNEX_PERF_OUT        directory of the reports (default: <temp>/downex-perf)
//	DOWNEX_PERF_LABEL      report name: <label>.json and <label>.md (default "perf")
//	DOWNEX_PERF_WORK       directory downloads are written into, then deleted (default: <temp>)
//	DOWNEX_PERF_RUNS       runs of every local scenario (default: each scenario's own)
//	DOWNEX_PERF_PROFILE    regexp of "<scenario>/<variant>" names whose first run is
//	                       CPU-profiled into <out>/<label>-<scenario>-<variant>.pprof
//	                       (default "^S3/"; "-" profiles nothing)
//	DOWNEX_PERF_REAL=1     also measure real servers (network traffic!)
//	DOWNEX_PERF_REAL_URLS  comma-separated URLs (default: defaultRealURLs)
//	DOWNEX_PERF_REAL_RUNS  downex runs per URL at the default connections (default 3)
//	DOWNEX_PERF_REAL_CURL  curl runs per URL, alternating with them (default 2)
//	DOWNEX_PERF_REAL_EXTRA downex runs per URL at 16 connections (default 1)
//	DOWNEX_PERF_REAL_BUDGET most bytes the real suite may download (default 3e9)
//	DOWNEX_PERF_REAL_TIMEOUT seconds one real download may take (default 300)
//	DOWNEX_PERF_CURL       the curl executable (default: curl on PATH, else System32\curl.exe)
//	DOWNEX_PERF_NOTE       a line of text added to the report (e.g. the code revision)
//	DOWNEX_PERF_EXTRA=1    also runs extraScenarios (S9: a gigabit line)
//	DOWNEX_PERF_FLUSH_BYTES diagnostic: engine.Options.FlushBytes of file runs (default: the engine's)
//	DOWNEX_PERF_RAMP_MS    diagnostic: engine.Options.RampInterval in ms (default: the engine's)
type config struct {
	out, label, work string
	runs             int
	profile          *regexp.Regexp
	real             bool
	realURLs         []string
	realRuns         int
	realCurl         int
	realExtra        int
	realBudget       int64
	realTimeout      time.Duration
	localTimeout     time.Duration
}

func loadConfig(t *testing.T) config {
	c := config{
		out:          envOr("DOWNEX_PERF_OUT", filepath.Join(os.TempDir(), "downex-perf")),
		label:        envOr("DOWNEX_PERF_LABEL", "perf"),
		work:         envOr("DOWNEX_PERF_WORK", os.TempDir()),
		runs:         envInt("DOWNEX_PERF_RUNS", 0),
		real:         os.Getenv("DOWNEX_PERF_REAL") == "1",
		realURLs:     defaultRealURLs,
		realRuns:     envInt("DOWNEX_PERF_REAL_RUNS", 3),
		realCurl:     envInt("DOWNEX_PERF_REAL_CURL", 2),
		realExtra:    envInt("DOWNEX_PERF_REAL_EXTRA", 1),
		realBudget:   int64(envInt("DOWNEX_PERF_REAL_BUDGET", 3_000_000_000)),
		realTimeout:  time.Duration(envInt("DOWNEX_PERF_REAL_TIMEOUT", 300)) * time.Second,
		localTimeout: 5 * time.Minute,
	}
	if v := os.Getenv("DOWNEX_PERF_REAL_URLS"); v != "" {
		c.realURLs = strings.Split(v, ",")
	}
	prof := envOr("DOWNEX_PERF_PROFILE", "^S3/")
	if prof != "-" {
		re, err := regexp.Compile(prof)
		if err != nil {
			t.Fatalf("DOWNEX_PERF_PROFILE: %v", err)
		}
		c.profile = re
	}
	for _, d := range []string{c.out, c.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	return c
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return int(n)
		}
	}
	return def
}

// scenario is one local measurement.
type scenario struct {
	id, variant string
	desc        string
	runs        int
	net         netCfg
	kind        string // "file" or "hls"
	size        int64  // file scenarios
	noRanges    bool
	unknownSize bool
	maxConn     int // the download's max_connections (0: the default setting)
	segments    int // hls
	segBytes    int // hls
	// capConns is how many connections at net.Rate the network lets the download use at
	// best (the cap and ideal time derive from it; 0 = no cap, unthrottled).
	capConns float64
}

func (s scenario) name() string { return s.id + "/" + s.variant }

const (
	rate4MiB = 4 << 20
	rtt40    = 40 * time.Millisecond
)

// localScenarios is the local suite of the baseline (S0 calibrates the emulator).
func localScenarios() []scenario {
	h1 := netCfg{TLS: true, Rate: rate4MiB, RTT: rtt40}
	h2 := h1
	h2.HTTP2 = true
	open := netCfg{TLS: true}
	open2 := open
	open2.HTTP2 = true
	slow := h1
	slow.SlowConn, slow.SlowFactor = 2, 10
	busy := h1
	busy.MaxActive = 4
	return []scenario{
		{id: "S0", variant: "h1-c1", runs: 1, net: h1, kind: "file", size: 16 << 20, maxConn: 1, capConns: 1,
			desc: "calibration: one connection, 16 MiB at 4 MiB/s, 40 ms RTT (ideal 4.0 s + 3 RTT)"},
		{id: "S1", variant: "h1-c8", runs: 3, net: h1, kind: "file", size: 200 << 20, capConns: 8,
			desc: "200 MiB, 4 MiB/s per connection, 40 ms RTT, TLS HTTP/1.1, default 8 connections"},
		{id: "S1", variant: "h2-c8", runs: 3, net: h2, kind: "file", size: 200 << 20, capConns: 8,
			desc: "as S1 over HTTP/2 (one TCP connection per slot)"},
		{id: "S1", variant: "h1-c16", runs: 2, net: h1, kind: "file", size: 200 << 20, maxConn: 16, capConns: 16,
			desc: "as S1 with the download's max connections set to 16"},
		{id: "S2", variant: "h1-c8", runs: 3, net: h1, kind: "file", size: 20 << 20, capConns: 8,
			desc: "20 MiB on the S1 network: time to full speed, probe overhead"},
		{id: "S2", variant: "h2-c8", runs: 3, net: h2, kind: "file", size: 20 << 20, capConns: 8,
			desc: "as S2 over HTTP/2"},
		{id: "S3", variant: "tls-h1-c8", runs: 2, net: open, kind: "file", size: 2 << 30,
			desc: "2 GiB unthrottled loopback, TLS HTTP/1.1: CPU and disk ceiling (server in the same process)"},
		{id: "S3", variant: "tls-h2-c8", runs: 2, net: open2, kind: "file", size: 2 << 30,
			desc: "as S3 over HTTP/2"},
		{id: "S3", variant: "plain-h1-c8", runs: 1, net: netCfg{}, kind: "file", size: 2 << 30,
			desc: "as S3 over plain HTTP (no TLS cost)"},
		{id: "S4", variant: "h1-c8", runs: 3, net: h1, kind: "hls", segments: 300, segBytes: 1 << 20, capConns: 8,
			desc: "HLS VOD, 300 TS segments of ~1 MiB, 4 MiB/s per connection, 40 ms RTT, TLS HTTP/1.1; media job incl. TS→MP4"},
		{id: "S4", variant: "h2-c8", runs: 2, net: func() netCfg { c := h1; c.HTTP2 = true; return c }(), kind: "hls",
			segments: 300, segBytes: 1 << 20, capConns: 8, desc: "as S4 over HTTP/2"},
		{id: "S5", variant: "h1-c8-429", runs: 3, net: busy, kind: "file", size: 50 << 20, capConns: 4,
			desc: "50 MiB; the server answers 429 (Retry-After: 1) to requests beyond 4 in flight"},
		{id: "S6", variant: "h1-c8-slow2", runs: 3, net: slow, kind: "file", size: 200 << 20, capConns: 7.1,
			desc: "as S1 h1-c8, but TCP connection #2 (the first one ramp-up adds) runs 10x slower (0.4 MiB/s)"},
		{id: "S7", variant: "h1-norange", runs: 2, net: h1, kind: "file", size: 20 << 20, noRanges: true, capConns: 1,
			desc: "20 MiB, the server ignores Range (200 with Content-Length): one stream"},
		{id: "S8", variant: "h1-unknown", runs: 2, net: h1, kind: "file", size: 20 << 20, unknownSize: true, capConns: 1,
			desc: "20 MiB, unknown size (200 without Content-Length, chunked)"},
	}
}

// extraScenarios are measured only with DOWNEX_PERF_EXTRA=1, so that the default suite
// stays the baseline's (S0–S8).
func extraScenarios() []scenario {
	giga := netCfg{TLS: true, Rate: 16 << 20, RTT: 10 * time.Millisecond}
	return []scenario{
		{id: "S9", variant: "h1-c1-1g", runs: 1, net: giga, kind: "file", size: 64 << 20, maxConn: 1, capConns: 1,
			desc: "calibration of S9: one connection, 64 MiB at 16 MiB/s, 10 ms RTT"},
		{id: "S9", variant: "h1-c8-1g", runs: 3, net: giga, kind: "file", size: 1 << 30, capConns: 8,
			desc: "1 GiB, 16 MiB/s per connection (8 × 16 = 128 MiB/s, about 1.07 Gbit/s), 10 ms RTT, TLS HTTP/1.1: a gigabit line, with the writer's flushes on the path"},
	}
}

// handshakeRTTs is the number of round trips before the first body byte of a fresh
// connection: TCP, TLS (when on) and the request.
func handshakeRTTs(c netCfg) float64 {
	if c.TLS {
		return 3
	}
	return 2
}

// TestPerf runs the local suite (and with DOWNEX_PERF_REAL=1 the real one) and writes
// the report. Filter with -run 'TestPerf/local/S1' and the like.
func TestPerf(t *testing.T) {
	cfg := loadConfig(t)
	rep := newReport(cfg)
	if n := os.Getenv("DOWNEX_PERF_NOTE"); n != "" {
		rep.note(n)
	}
	for _, k := range []string{"DOWNEX_PERF_FLUSH_BYTES", "DOWNEX_PERF_RAMP_MS"} {
		if v := os.Getenv(k); v != "" {
			rep.note(fmt.Sprintf("diagnostic override %s=%s (file runs)", k, v))
		}
	}
	defer func() {
		if err := rep.write(); err != nil {
			t.Errorf("report: %v", err)
		} else {
			t.Logf("report: %s", filepath.Join(cfg.out, cfg.label+".md"))
		}
	}()
	t.Run("local", func(t *testing.T) {
		warmUp(t, cfg)
		scs := localScenarios()
		if os.Getenv("DOWNEX_PERF_EXTRA") == "1" {
			scs = append(scs, extraScenarios()...)
		}
		for _, sc := range scs {
			t.Run(sc.name(), func(t *testing.T) { runScenario(t, cfg, rep, sc) })
		}
	})
	t.Run("real", func(t *testing.T) {
		if !cfg.real {
			t.Skip("set DOWNEX_PERF_REAL=1 to measure real servers")
		}
		realSuite(t, cfg, rep)
	})
}

// warmUp runs one small unmeasured download so that the first measured run does not pay
// for first-use costs (certificates, page faults, code paths).
func warmUp(t *testing.T, cfg config) {
	srv := newNetServer(netCfg{TLS: true}, fileHandler{size: 8 << 20})
	defer srv.close()
	res := runEngine(context.Background(), cfg.work, fileRun{url: srv.URL + "/warmup.bin", roots: srv.roots, size: 8 << 20,
		verify: true, timeout: time.Minute})
	if !res.OK {
		t.Fatalf("warm-up download failed: %s", res.Error)
	}
}

var hlsCache = map[string]*hlsAsset{}

func runScenario(t *testing.T, cfg config, rep *report, sc scenario) {
	runs := sc.runs
	if cfg.runs > 0 {
		runs = cfg.runs
	}
	var asset *hlsAsset
	if sc.kind == "hls" {
		key := fmt.Sprintf("%d/%d", sc.segments, sc.segBytes)
		if asset = hlsCache[key]; asset == nil {
			t0 := time.Now()
			a, err := genHLS(sc.segments, sc.segBytes)
			if err != nil {
				t.Fatalf("generate HLS: %v", err)
			}
			asset, hlsCache[key] = a, a
			t.Logf("generated %d segments, %d bytes, %d frames in %s", a.segments, a.bytes, a.frames, time.Since(t0).Round(time.Millisecond))
		}
	}
	rep.describe(sc)
	for run := 1; run <= runs; run++ {
		var h http.Handler = fileHandlerFor(sc)
		if asset != nil {
			h = asset.files
		}
		srv := newNetServer(sc.net, h)
		profile := ""
		if run == 1 && cfg.profile != nil && cfg.profile.MatchString(sc.name()) {
			profile = filepath.Join(cfg.out, fmt.Sprintf("%s-%s-%s.pprof", cfg.label, sc.id, sc.variant))
		}
		var res runResult
		switch sc.kind {
		case "hls":
			res = runMedia(context.Background(), cfg.work, mediaRun{manifest: srv.URL + "/hls/index.m3u8", roots: srv.roots,
				maxConn: sc.maxConn, server: srv, frames: asset.frames, profile: profile, timeout: cfg.localTimeout})
		default:
			res = runEngine(context.Background(), cfg.work, fileRun{url: srv.URL + "/" + fileName(sc), roots: srv.roots,
				maxConn: sc.maxConn, size: sc.size, verify: true, profile: profile, server: srv, timeout: cfg.localTimeout})
		}
		srv.close()
		res.Scenario, res.Variant, res.Tool, res.Run = sc.id, sc.variant, "downex", run
		if sc.capConns > 0 && sc.net.Rate > 0 {
			capBps := sc.capConns * float64(sc.net.Rate)
			bytes := float64(sc.size)
			if asset != nil {
				bytes = float64(asset.bytes)
			}
			res.CapMiBps = capBps / mib
			res.IdealS = bytes/capBps + handshakeRTTs(sc.net)*sc.net.RTT.Seconds()
			res.Eff = res.IdealS / max(res.WallS, 1e-9)
			if res.Server != nil {
				// the server timeline is in the run's samples; recompute against the cap
				res.TCap90S = analyze(srv.timeline(), 0.5, capBps).TCap90
			}
		}
		rep.addLocal(res)
		_ = rep.write() // keep what is measured if the process is stopped
		if !res.OK {
			t.Errorf("%s run %d failed: %s", sc.name(), run, res.Error)
		}
		t.Logf("%s run %d: ok=%v wall %.3f s (ideal %.3f) avg %.2f MiB/s peak %.2f t90 %.2f tcap90 %.2f ttfb %.0f ms conns %d cpu %.2f s",
			sc.name(), run, res.OK, res.WallS, res.IdealS, res.AvgMiBps, res.PeakMiBps, res.T90S, res.TCap90S, res.TTFBS*1000,
			res.ConnsMax, res.CPUS)
	}
}

func fileName(sc scenario) string {
	return fmt.Sprintf("%s-%s.bin", strings.ToLower(sc.id), sc.variant)
}

func fileHandlerFor(sc scenario) fileHandler {
	return fileHandler{size: sc.size, noRanges: sc.noRanges, unknownSize: sc.unknownSize}
}
