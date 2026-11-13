//go:build perf

package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/platform"
	"downex/internal/transport"
)

// TestStart is the offline start-up benchmark: a shared 10 MiB/s line (the owner's
// ~84 Mbit/s), per-connection TCP slow start, a CDN's variable time to first byte, TLS
// and HTTP/2, comparing the engine with a reference client that opens all its ranges at
// once (the size known in advance). Run with
//
//	go test -tags perf -run TestStart -timeout 0 -v ./internal/perf
//
// Settings: DOWNEX_PERF_RUNS (pairs per variant, default 3), DOWNEX_PERF_START (substring
// of variant names), DOWNEX_PERF_START_TOOLS (default "downex,burst"; "downex" alone
// skips the reference client), DOWNEX_PERF_OUT, DOWNEX_PERF_WORK, DOWNEX_PERF_LABEL.
func TestStart(t *testing.T) {
	cfg := loadConfig(t)
	runs := cfg.runs
	if runs <= 0 {
		runs = 3
	}
	filter := os.Getenv("DOWNEX_PERF_START")
	type variant struct {
		name    string
		net     netCfg
		size    int64
		maxConn int
	}
	line := func(rtt time.Duration, h2 bool) netCfg {
		return netCfg{TLS: true, HTTP2: h2, RTT: rtt, LineRate: 10 << 20,
			SlowStartRate: 256 << 10, SlowStartDoubling: 500 * time.Millisecond,
			ServerDelay: 150 * time.Millisecond, ServerJitter: 500 * time.Millisecond}
	}
	// The end game: unequal connections (0.2–0.8 MiB/s each, 10 MiB/s together) so that
	// fast ones finish first and split the slow ones' segments, behind a 2 MiB send buffer
	// (waits behind a reset stream of up to ~10 s, as measured on the real line).
	buffered := func(c netCfg) netCfg {
		c.SendBuffer, c.Rate, c.RateSpread = 2<<20, 512<<10, 0.6
		return c
	}
	tools := strings.Split(envOr("DOWNEX_PERF_START_TOOLS", "downex,burst"), ",")
	vs := []variant{
		{"S10/h2-rtt40-27m-c32", line(rtt40, true), 28163301, 32},
		{"S10/h2-rtt40-27m-c8", line(rtt40, true), 28163301, 8},
		{"S10/h2-rtt40-146m-c32", line(rtt40, true), 153583359, 32},
		{"S10/h2-rtt90-27m-c32", line(90*time.Millisecond, true), 28163301, 32},
		{"S10/h1-rtt40-27m-c32", line(rtt40, false), 28163301, 32},
		// A send buffer (socket buffer and bytes in flight, as a CDN's): a stream the
		// client resets still delivers what was queued, ahead of its connection's next
		// response (the end-game backlog measured on the real line).
		{"S10/h2-rtt40-146m-c32-sb2m", buffered(line(rtt40, true)), 153583359, 32},
		{"S10/h1-rtt40-146m-c32-sb2m", buffered(line(rtt40, false)), 153583359, 32},
	}
	warmUp(t, cfg)
	var all []startResult
	for _, v := range vs {
		if filter != "" && !strings.Contains(v.name, filter) {
			continue
		}
		t.Run(v.name, func(t *testing.T) {
			for run := 1; run <= runs; run++ {
				order := slices.Clone(tools)
				if run%2 == 0 {
					slices.Reverse(order)
				}
				for _, tool := range order {
					srv := newNetServer(v.net, fileHandler{size: v.size})
					var r startResult
					name := fmt.Sprintf("s10-%d.bin", run)
					if tool == "downex" {
						res := runEngine(context.Background(), cfg.work, fileRun{url: srv.URL + "/" + name, roots: srv.roots,
							maxConn: v.maxConn, size: v.size, verify: true, server: srv, timeout: cfg.localTimeout})
						r = startResult{OK: res.OK, Err: res.Error, WallS: res.WallS, Steps: res.ConnSteps}
					} else {
						r = runBurst(cfg.work, srv, srv.URL+"/"+name, v.size, v.maxConn)
					}
					srv.close()
					r.Variant, r.Tool, r.Run = v.name, tool, run
					r.fromTimeline(srv.timeline(), float64(v.net.LineRate))
					all = append(all, r)
					t.Logf("%s %-6s run %d: ok=%v wall %.2f s ttfb %.2f t50 %.2f t90 %.2f mib@1s %.1f @2s %.1f @3s %.1f conns %s",
						v.name, tool, run, r.OK, r.WallS, r.TTFB, r.T50, r.T90, r.MiB1, r.MiB2, r.MiB3, r.stepText())
					if !r.OK {
						t.Errorf("%s %s run %d: %s", v.name, tool, run, r.Err)
					}
				}
			}
		})
	}
	label := envOr("DOWNEX_PERF_LABEL", "start")
	b, _ := json.MarshalIndent(all, "", " ")
	_ = os.WriteFile(filepath.Join(cfg.out, label+".json"), b, 0o644)
	t.Log(startSummary(all))
}

type startResult struct {
	Variant string     `json:"variant"`
	Tool    string     `json:"tool"`
	Run     int        `json:"run"`
	OK      bool       `json:"ok"`
	Err     string     `json:"error,omitempty"`
	WallS   float64    `json:"wall_s"`
	TTFB    float64    `json:"ttfb_s"` // server: first body byte
	T50     float64    `json:"t50_s"`  // end of the first 0.5 s window ≥ 50% of the line
	T90     float64    `json:"t90_s"`  // ... ≥ 90% of the line
	MiB1    float64    `json:"mib_at_1s"`
	MiB2    float64    `json:"mib_at_2s"`
	MiB3    float64    `json:"mib_at_3s"`
	Steps   []connStep `json:"conn_steps,omitempty"`
}

func (r *startResult) fromTimeline(tl []sample, lineBps float64) {
	for _, s := range tl {
		if s.Bytes > 0 && r.TTFB == 0 {
			r.TTFB = s.T
		}
		switch {
		case s.T <= 1:
			r.MiB1 = float64(s.Bytes) / mib
		case s.T <= 2:
			r.MiB2 = float64(s.Bytes) / mib
		case s.T <= 3:
			r.MiB3 = float64(s.Bytes) / mib
		}
	}
	j := 0
	for i := range tl {
		for j < i && tl[i].T-tl[j+1].T >= 0.5 {
			j++
		}
		dt := tl[i].T - tl[j].T
		if dt < 0.4995 {
			continue
		}
		bps := float64(tl[i].Bytes-tl[j].Bytes) / dt
		if r.T50 == 0 && bps >= 0.5*lineBps {
			r.T50 = tl[i].T
		}
		if r.T90 == 0 && bps >= 0.9*lineBps {
			r.T90 = tl[i].T
		}
	}
}

func (r startResult) stepText() string {
	var b strings.Builder
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "%d@%.1f ", s.Conns, s.T)
	}
	return strings.TrimSpace(b.String())
}

func startSummary(all []startResult) string {
	type key struct{ v, tool string }
	g := map[key][]startResult{}
	var keys []key
	for _, r := range all {
		k := key{r.Variant, r.Tool}
		if _, ok := g[k]; !ok {
			keys = append(keys, k)
		}
		g[k] = append(g[k], r)
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].v < keys[j].v })
	var b strings.Builder
	b.WriteString("\n| variant | tool | runs | wall s (median, min–max) | ttfb s | t50 s | t90 s | MiB by 2 s |\n|---|---|---|---|---|---|---|---|\n")
	for _, k := range keys {
		rs := g[k]
		var wall, ttfb, t50, t90, m2 []float64
		for _, r := range rs {
			wall, ttfb, t50, t90, m2 = append(wall, r.WallS), append(ttfb, r.TTFB), append(t50, r.T50), append(t90, r.T90), append(m2, r.MiB2)
		}
		lo, hi := wall[0], wall[0]
		for _, w := range wall {
			lo, hi = min(lo, w), max(hi, w)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %.2f (%.2f–%.2f) | %.2f | %.2f | %.2f | %.1f |\n", k.v, k.tool, len(rs),
			median(wall), lo, hi, median(ttfb), median(t50), median(t90), median(m2))
	}
	return b.String()
}

// runBurst downloads url with n ranges opened at once over the daemon's transport (one
// slot each), the size known in advance: the start-up a scheduler could reach at best.
func runBurst(work string, srv *netServer, url string, size int64, n int) startResult {
	ctx := context.Background()
	r := startResult{}
	cl, err := newClient(ctx, false, srv.roots)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer cl.close()
	dir, err := os.MkdirTemp(work, "burst-")
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "burst.bin")
	f, err := os.Create(path)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	if err := platform.Preallocate(f, size); err != nil {
		f.Close()
		r.Err = err.Error()
		return r
	}
	t0 := time.Now()
	srv.begin(t0)
	chunk := size / int64(n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		s, e := int64(i)*chunk, int64(i+1)*chunk-1
		if i == n-1 {
			e = size - 1
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fe := cl.tf.NewSlot()
			defer fe.Close()
			resp, err := fe.Fetch(ctx, transport.RangeRequest{Request: model.Request{URL: url}, Start: s, End: e})
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			buf := make([]byte, 256<<10)
			off := s
			for off <= e {
				k, err := resp.Body.Read(buf[:min(int64(len(buf)), e-off+1)])
				if k > 0 {
					if _, werr := f.WriteAt(buf[:k], off); werr != nil {
						errs <- werr
						return
					}
					off += int64(k)
				}
				if err != nil {
					if off <= e && err != io.EOF {
						errs <- err
					} else if off <= e {
						errs <- io.ErrUnexpectedEOF
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	r.WallS = time.Since(t0).Seconds()
	srv.endSampling()
	close(errs)
	f.Close()
	for err := range errs {
		r.Err = err.Error()
		return r
	}
	if err := verifyContent(path, size); err != nil {
		r.Err = "verify: " + err.Error()
		return r
	}
	r.OK = true
	return r
}
