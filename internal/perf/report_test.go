//go:build perf

package perf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// report collects the results of one harness run.
type report struct {
	mu  sync.Mutex
	cfg config

	Label     string            `json:"label"`
	Started   time.Time         `json:"started"`
	Finished  time.Time         `json:"finished"`
	Machine   map[string]string `json:"machine"`
	Build     map[string]string `json:"build"`
	Notes     []string          `json:"notes,omitempty"`
	Scenarios []scenarioInfo    `json:"scenarios"`
	Summary   []aggregate       `json:"summary"`
	Ranges    []rangeCheck      `json:"range_checks,omitempty"`
	RealSum   []aggregate       `json:"real_summary,omitempty"`
	RealBytes int64             `json:"real_bytes,omitempty"`
	Local     []runResult       `json:"local_runs"`
	Real      []runResult       `json:"real_runs,omitempty"`
}

type scenarioInfo struct {
	ID       string  `json:"id"`
	Variant  string  `json:"variant"`
	Desc     string  `json:"desc"`
	Net      netCfg  `json:"net"`
	Size     int64   `json:"size,omitempty"`
	Segments int     `json:"segments,omitempty"`
	MaxConn  int     `json:"max_conn"`
	CapConns float64 `json:"cap_conns,omitempty"`
}

// aggregate summarises the runs of one scenario variant (or one real host and tool).
type aggregate struct {
	Scenario    string  `json:"scenario"`
	Variant     string  `json:"variant"`
	Tool        string  `json:"tool"`
	Runs        int     `json:"runs"`
	OK          int     `json:"ok"`
	WallMed     float64 `json:"wall_s_median"`
	WallMin     float64 `json:"wall_s_min"`
	WallMax     float64 `json:"wall_s_max"`
	IdealS      float64 `json:"ideal_s,omitempty"`
	Eff         float64 `json:"efficiency_median,omitempty"`
	AvgMiBps    float64 `json:"avg_mibps_median"`
	PeakMiBps   float64 `json:"peak_mibps_median"`
	CapMiBps    float64 `json:"cap_mibps,omitempty"`
	T90         float64 `json:"t90_s_median"`
	TCap90      float64 `json:"tcap90_s_median,omitempty"`
	TTFBms      float64 `json:"ttfb_ms_median"`
	ConnsMax    int     `json:"conns_max"`
	TCPConns    float64 `json:"tcp_conns_median,omitempty"`
	AbortedTLS  float64 `json:"aborted_tls_median,omitempty"`
	Requests    float64 `json:"requests_median,omitempty"`
	WastedMiB   float64 `json:"wasted_mib_median,omitempty"` // body bytes served beyond the file
	Status429   float64 `json:"status_429_median,omitempty"`
	MaxActive   float64 `json:"max_active_requests_median,omitempty"`
	CPUS        float64 `json:"cpu_s_median"`
	CPUCores    float64 `json:"cpu_cores_median"`
	AllocMiB    float64 `json:"alloc_mib_median"`
	ProbeS      float64 `json:"media_probe_s_median,omitempty"`
	FetchS      float64 `json:"media_fetch_s_median,omitempty"`
	AssembleS   float64 `json:"media_assemble_s_median,omitempty"`
	HashesMatch *bool   `json:"hashes_match,omitempty"` // real: every run's SHA-256 equal
}

func newReport(cfg config) *report {
	r := &report{cfg: cfg, Label: cfg.label, Started: time.Now()}
	r.Machine = map[string]string{
		"goos": runtime.GOOS, "goarch": runtime.GOARCH, "num_cpu": fmt.Sprint(runtime.NumCPU()),
		"gomaxprocs": fmt.Sprint(runtime.GOMAXPROCS(0)), "cpu": os.Getenv("PROCESSOR_IDENTIFIER"),
	}
	r.Build = map[string]string{"go": runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision", "vcs.modified", "vcs.time", "-tags":
				r.Build[s.Key] = s.Value
			}
		}
	}
	return r
}

func (r *report) describe(sc scenario) {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxConn := sc.maxConn
	if maxConn <= 0 {
		maxConn = defaultMaxConnections
	}
	r.Scenarios = append(r.Scenarios, scenarioInfo{ID: sc.id, Variant: sc.variant, Desc: sc.desc, Net: sc.net,
		Size: sc.size, Segments: sc.segments, MaxConn: maxConn, CapConns: sc.capConns})
}

func (r *report) addLocal(res runResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Local = append(r.Local, res)
}

func (r *report) addReal(res runResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Real = append(r.Real, res)
}

func (r *report) addRange(rc rangeCheck) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Ranges = append(r.Ranges, rc)
}

func (r *report) note(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.Notes, s) {
		r.Notes = append(r.Notes, s)
	}
}

// aggregateRuns groups runs by scenario, variant and tool, in order of appearance.
func aggregateRuns(runs []runResult) []aggregate {
	type key struct{ s, v, t string }
	var order []key
	groups := map[key][]runResult{}
	for _, x := range runs {
		k := key{x.Scenario, x.Variant, x.Tool}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], x)
	}
	var out []aggregate
	for _, k := range order {
		g := groups[k]
		a := aggregate{Scenario: k.s, Variant: k.v, Tool: k.t, Runs: len(g)}
		var wall, eff, avg, peak, t90, tcap, ttfb, cpu, cores, tcp, abt, reqs, waste, s429, act, probe, fetch, asm, alloc []float64
		hashes := map[string]bool{}
		for _, x := range g {
			if x.OK {
				a.OK++
			}
		}
		// The medians describe the successful runs; a group without one shows its failures.
		for _, x := range g {
			if a.OK > 0 && !x.OK {
				continue
			}
			wall = append(wall, x.WallS)
			avg = append(avg, x.AvgMiBps)
			peak = append(peak, x.PeakMiBps)
			t90 = append(t90, x.T90S)
			ttfb = append(ttfb, x.TTFBS*1000)
			cpu = append(cpu, x.CPUS)
			cores = append(cores, x.CPUCores)
			alloc = append(alloc, x.AllocMiB)
			a.ConnsMax = max(a.ConnsMax, x.ConnsMax)
			if x.IdealS > 0 {
				a.IdealS, a.CapMiBps = x.IdealS, x.CapMiBps
				eff = append(eff, x.Eff)
				tcap = append(tcap, x.TCap90S)
			}
			if s := x.Server; s != nil {
				tcp = append(tcp, float64(s.TCPConns))
				abt = append(abt, float64(s.AbortedTLS))
				reqs = append(reqs, float64(s.Requests))
				if x.Bytes > 0 && x.Media == nil {
					waste = append(waste, float64(s.BodyBytes-x.Bytes)/mib)
				}
				s429 = append(s429, float64(s.Status["429"]))
				act = append(act, float64(s.MaxActive))
			}
			if m := x.Media; m != nil {
				probe = append(probe, m.ProbeS)
				fetch = append(fetch, m.FetchS)
				asm = append(asm, m.AssembleS)
			}
			if x.SHA256 != "" {
				hashes[x.SHA256] = true
			}
		}
		a.WallMed, a.WallMin, a.WallMax = median(wall), slices.Min(wall), slices.Max(wall)
		a.Eff, a.AvgMiBps, a.PeakMiBps, a.T90, a.TCap90 = median(eff), median(avg), median(peak), median(t90), median(tcap)
		a.TTFBms, a.CPUS, a.CPUCores = median(ttfb), median(cpu), median(cores)
		a.AbortedTLS, a.AllocMiB = median(abt), median(alloc)
		a.TCPConns, a.Requests, a.WastedMiB, a.Status429, a.MaxActive = median(tcp), median(reqs), median(waste), median(s429), median(act)
		a.ProbeS, a.FetchS, a.AssembleS = median(probe), median(fetch), median(asm)
		out = append(out, a)
	}
	return out
}

// write saves <label>.json and <label>.md in the output directory.
func (r *report) write() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Finished = time.Now()
	r.Summary = aggregateRuns(r.Local)
	r.RealSum = aggregateRuns(r.Real)
	r.RealBytes = 0 // what the real runs received (curl: size_download)
	for _, x := range r.Real {
		r.RealBytes += max(x.Bytes, 0)
	}
	// the outputs of every run of a real host, by any tool, must be the same bytes
	byHost := map[string]map[string]bool{}
	for _, x := range r.Real {
		if x.SHA256 == "" {
			continue
		}
		if byHost[x.Scenario] == nil {
			byHost[x.Scenario] = map[string]bool{}
		}
		byHost[x.Scenario][x.SHA256] = true
	}
	for i := range r.RealSum {
		if h, ok := byHost[r.RealSum[i].Scenario]; ok {
			match := len(h) == 1
			r.RealSum[i].HashesMatch = &match
		}
	}
	js, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.cfg.out, r.Label+".json"), js, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.cfg.out, r.Label+".md"), []byte(r.markdown()), 0o644)
}

func (r *report) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Downex download benchmark: %s\n\n", r.Label)
	fmt.Fprintf(&b, "%s → %s · %s/%s · %s logical CPUs · %s · %s\n\n", r.Started.Format("2006-01-02 15:04:05"),
		r.Finished.Format("15:04:05"), r.Machine["goos"], r.Machine["goarch"], r.Machine["num_cpu"], r.Machine["cpu"], r.Build["go"])
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n")
	}
	if len(r.Summary) > 0 {
		b.WriteString("## Local scenarios (loopback, emulated network)\n\n")
		b.WriteString("Medians over the runs. Wall = engine.Run (media: mediajob.Run incl. TS→MP4). Ideal = size / cap + handshake RTTs. ")
		b.WriteString("Eff = ideal / wall. t90 = first 0.5 s window at ≥ 90% of the run's peak (server side); t90cap = at ≥ 90% of the cap. ")
		b.WriteString("Wasted = body bytes the server sent beyond the file (aborted/overlapping ranges). CPU = whole process (client + emulated server).\n\n")
		b.WriteString("| Scenario | Variant | OK | Wall s (min–max) | Ideal s | Eff | Avg MiB/s | Peak MiB/s | Cap MiB/s | t90 s | t90cap s | TTFB ms | Conns | TCP (aborted) | Reqs | 429s | Wasted MiB | CPU s | Cores | Alloc MiB |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, a := range r.Summary {
			fmt.Fprintf(&b, "| %s | %s | %d/%d | %.2f (%.2f–%.2f) | %s | %s | %.1f | %.1f | %s | %.2f | %s | %.0f | %d | %.0f (%.0f) | %.0f | %.0f | %.2f | %.2f | %.2f | %.0f |\n",
				a.Scenario, a.Variant, a.OK, a.Runs, a.WallMed, a.WallMin, a.WallMax, opt(a.IdealS, "%.2f"), optPct(a.Eff),
				a.AvgMiBps, a.PeakMiBps, opt(a.CapMiBps, "%.1f"), a.T90, opt(a.TCap90, "%.2f"), a.TTFBms, a.ConnsMax,
				a.TCPConns, a.AbortedTLS, a.Requests, a.Status429, a.WastedMiB, a.CPUS, a.CPUCores, a.AllocMiB)
		}
		b.WriteString("\n")
		var media []aggregate
		for _, a := range r.Summary {
			if a.FetchS > 0 || a.ProbeS > 0 {
				media = append(media, a)
			}
		}
		if len(media) > 0 {
			b.WriteString("Media job phases (medians): probe = mediajob.ProbeMP4 (not in wall), fetch = Run start → assembly, assemble = remux.Assemble. Avg MiB/s of media rows = segment bytes / fetch time.\n\n")
			b.WriteString("| Scenario | Variant | Probe s | Fetch s | Assemble s | Wall s |\n|---|---|---|---|---|---|\n")
			for _, a := range media {
				fmt.Fprintf(&b, "| %s | %s | %.3f | %.2f | %.2f | %.2f |\n", a.Scenario, a.Variant, a.ProbeS, a.FetchS, a.AssembleS, a.WallMed)
			}
			b.WriteString("\n")
		}
		b.WriteString("### Scenarios\n\n")
		for _, s := range r.Scenarios {
			fmt.Fprintf(&b, "- **%s %s** — %s\n", s.ID, s.Variant, s.Desc)
		}
		b.WriteString("\n### Connection ramp (first run of each variant: time s → live connections)\n\n")
		seen := map[string]bool{}
		for _, x := range r.Local {
			k := x.Scenario + "/" + x.Variant
			if seen[k] {
				continue
			}
			seen[k] = true
			var steps []string
			for i, s := range x.ConnSteps {
				if i == 24 {
					steps = append(steps, "…")
					break
				}
				steps = append(steps, fmt.Sprintf("%.2f→%d", s.T, s.Conns))
			}
			fmt.Fprintf(&b, "- %s: %s", k, strings.Join(steps, ", "))
			if x.Server != nil && len(x.Server.FirstReqMs) > 0 {
				var ms []string
				for _, v := range x.Server.FirstReqMs {
					ms = append(ms, fmt.Sprintf("%.0f", v))
				}
				fmt.Fprintf(&b, " · first requests at ms: %s", strings.Join(ms, ", "))
			}
			if len(x.Warnings) > 0 {
				var ws []string
				for k, v := range x.Warnings {
					ws = append(ws, fmt.Sprintf("%q×%d", k, v))
				}
				slices.Sort(ws)
				fmt.Fprintf(&b, " · warnings: %s", strings.Join(ws, "; "))
			}
			if x.MinHostLimit > 0 {
				fmt.Fprintf(&b, " · lowest host limit %d", x.MinHostLimit)
			}
			b.WriteString("\n")
		}
		var profs []string
		for _, x := range r.Local {
			if x.Profile != "" {
				profs = append(profs, fmt.Sprintf("- %s %s: `%s`", x.Scenario, x.Variant, filepath.Base(x.Profile)))
			}
		}
		if len(profs) > 0 {
			b.WriteString("\n### CPU profiles\n\nView with `go tool pprof -top -nodecount=30 <binary> <profile>`.\n\n")
			b.WriteString(strings.Join(profs, "\n") + "\n")
		}
		var failed []string
		for _, x := range r.Local {
			if !x.OK {
				failed = append(failed, fmt.Sprintf("- %s %s run %d: %s", x.Scenario, x.Variant, x.Run, x.Error))
			}
		}
		if len(failed) > 0 {
			b.WriteString("\n### Failed runs\n\n" + strings.Join(failed, "\n") + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.Ranges) > 0 {
		b.WriteString("## Real network\n\n### Range support (GET Range: bytes=0-0)\n\n")
		b.WriteString("| URL | Status | Content-Range | Ranged | Proto | Server | TTFB ms | Error |\n|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.Ranges {
			fmt.Fprintf(&b, "| %s | %d | %s | %v | %s | %s | %.0f | %s |\n", c.URL, c.Status, c.ContentRange, c.Ranged, c.Proto, c.Server, c.TTFBms, c.Error)
		}
		b.WriteString("\n")
	}
	if len(r.RealSum) > 0 {
		fmt.Fprintf(&b, "### Downloads (medians of the successful runs; %d bytes received in all)\n\n", r.RealBytes)
		b.WriteString("t90 (downex) = first 1 s window at ≥ 90% of the run's peak, from progress events (4 Hz). TTFB downex = first progress event with bytes (≤ 250 ms resolution); curl = time_starttransfer.\n\n")
		b.WriteString("| Host | Tool | Conns | OK | Wall s (min–max) | Avg MiB/s | Peak MiB/s | t90 s | TTFB ms | CPU s | Same SHA-256 |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, a := range r.RealSum {
			match := "–"
			if a.HashesMatch != nil {
				match = fmt.Sprint(*a.HashesMatch)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d/%d | %.2f (%.2f–%.2f) | %.2f | %.2f | %.2f | %.0f | %.2f | %s |\n",
				strings.TrimPrefix(a.Scenario, "R:"), a.Tool, a.Variant, a.OK, a.Runs, a.WallMed, a.WallMin, a.WallMax,
				a.AvgMiBps, a.PeakMiBps, a.T90, a.TTFBms, a.CPUS, match)
		}
		var failed []string
		for _, x := range r.Real {
			if !x.OK {
				failed = append(failed, fmt.Sprintf("- %s %s %s run %d: %s", x.Scenario, x.Tool, x.Variant, x.Run, x.Error))
			}
		}
		if len(failed) > 0 {
			b.WriteString("\nFailed runs:\n\n" + strings.Join(failed, "\n") + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func opt(v float64, f string) string {
	if v == 0 {
		return "–"
	}
	return fmt.Sprintf(f, v)
}

func optPct(v float64) string {
	if v == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

// TestPerfMerge joins earlier reports (DOWNEX_PERF_MERGE: comma-separated .json paths)
// into <out>/<label>.json and .md, aggregating all their runs together: separate local
// and real invocations, or repeated runs of one binary. Skipped without the variable.
func TestPerfMerge(t *testing.T) {
	paths := os.Getenv("DOWNEX_PERF_MERGE")
	if paths == "" {
		t.Skip("set DOWNEX_PERF_MERGE to merge reports")
	}
	cfg := loadConfig(t)
	out := newReport(cfg)
	out.Started = time.Time{}
	seen := map[string]bool{}
	for _, p := range strings.Split(paths, ",") {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var in report
		if err := json.Unmarshal(data, &in); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if out.Started.IsZero() || in.Started.Before(out.Started) {
			out.Started, out.Machine = in.Started, in.Machine
		}
		for _, n := range in.Notes {
			out.note(n)
		}
		out.note(fmt.Sprintf("merged from %s (%s → %s)", filepath.Base(p), in.Started.Format("15:04:05"), in.Finished.Format("15:04:05")))
		for _, s := range in.Scenarios {
			if k := s.ID + "/" + s.Variant; !seen[k] {
				seen[k] = true
				out.Scenarios = append(out.Scenarios, s)
			}
		}
		// Reports written before the fix in runEngine gave a failed run
		// the file's size as its bytes: take what it received from its timeline.
		for _, runs := range [][]runResult{in.Local, in.Real} {
			for i := range runs {
				if x := &runs[i]; !x.OK && x.Tool == "downex" && len(x.Timeline) > 0 && x.Media == nil {
					x.Bytes = x.Timeline[len(x.Timeline)-1].Bytes
					x.AvgMiBps = float64(x.Bytes) / mib / max(x.WallS, 1e-9)
				}
			}
		}
		out.Local = append(out.Local, in.Local...)
		out.Real = append(out.Real, in.Real...)
		out.Ranges = append(out.Ranges, in.Ranges...)
	}
	if err := out.write(); err != nil {
		t.Fatal(err)
	}
	t.Logf("merged into %s", filepath.Join(cfg.out, cfg.label+".md"))
}
