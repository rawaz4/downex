//go:build perf

package perf

import (
	"context"
	"log/slog"
	"slices"
	"sync"
)

// sample is the number of bytes received (or served) T seconds after the start.
type sample struct {
	T     float64 `json:"t"`
	Bytes int64   `json:"b"`
}

// rateStats derives the peak rate and the time it was first nearly reached from a
// cumulative byte timeline, over a sliding window of window seconds.
type rateStats struct {
	PeakBps float64 // highest windowed rate
	T90     float64 // end of the first window whose rate was ≥ 90% of PeakBps
	TCap90  float64 // end of the first window whose rate was ≥ 90% of capBps (0: never or no cap)
}

func analyze(samples []sample, window, capBps float64) rateStats {
	var st rateStats
	type point struct{ t, bps float64 }
	var pts []point
	j := 0
	for i := range samples {
		for j < i && samples[i].T-samples[j+1].T >= window {
			j++
		}
		dt := samples[i].T - samples[j].T
		if dt < window*0.999 {
			continue
		}
		bps := float64(samples[i].Bytes-samples[j].Bytes) / dt
		pts = append(pts, point{samples[i].T, bps})
		st.PeakBps = max(st.PeakBps, bps)
	}
	for _, p := range pts {
		if st.T90 == 0 && p.bps >= 0.9*st.PeakBps {
			st.T90 = p.t
		}
		if capBps > 0 && st.TCap90 == 0 && p.bps >= 0.9*capBps {
			st.TCap90 = p.t
		}
	}
	if len(pts) == 0 && len(samples) > 1 { // shorter than one window: the average
		last := samples[len(samples)-1]
		if last.T > 0 {
			st.PeakBps = float64(last.Bytes-samples[0].Bytes) / last.T
			st.T90 = last.T
		}
	}
	return st
}

// connStep is a change of the live connection count reported by progress events.
type connStep struct {
	T     float64 `json:"t"`
	Conns int     `json:"conns"`
}

// warnCounter is the engine's logger: it counts warnings by message and remembers the
// lowest per-host limit a 429/503/421 penalty set. Debug and info records are dropped,
// as by the daemon's log at its default level.
type warnCounter struct {
	mu       sync.Mutex
	counts   map[string]int
	minLimit int64
}

func newWarnCounter() *warnCounter { return &warnCounter{counts: map[string]int{}} }

func (w *warnCounter) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

func (w *warnCounter) Handle(_ context.Context, r slog.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.counts[r.Message]++
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "limit" && a.Value.Kind() == slog.KindInt64 {
			if v := a.Value.Int64(); w.minLimit == 0 || v < w.minLimit {
				w.minLimit = v
			}
		}
		return true
	})
	return nil
}

func (w *warnCounter) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnCounter) WithGroup(string) slog.Handler      { return w }

func (w *warnCounter) snapshot() (map[string]int, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.counts))
	for k, v := range w.counts {
		out[k] = v
	}
	return out, w.minLimit
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
