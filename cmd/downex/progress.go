package main

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"downex/internal/engine"
	"downex/internal/model"
	"downex/internal/store"
)

// renderInterval bounds progress redraws to 4 per second.
const renderInterval = 250 * time.Millisecond

// progressView shows a running download's progress on the console. The engine calls
// update on its coordinator goroutine; events pass through a one-slot mailbox that
// keeps only the latest, so a slow or paused terminal never stalls the download.
type progressView struct {
	con *console
	st  store.Store
	id  model.DownloadID

	mailbox chan engine.Progress
	quit    chan struct{}
	done    chan struct{}

	// Owned by the run goroutine until done is closed.
	named bool // the target path was printed
	last  engine.Progress
	seen  bool
}

// newProgressView returns a view of download id. named reports whether the target
// path was already printed; otherwise it is printed once the engine has chosen it.
func newProgressView(con *console, st store.Store, id model.DownloadID, named bool) *progressView {
	v := &progressView{
		con: con, st: st, id: id, named: named,
		mailbox: make(chan engine.Progress, 1),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go v.run()
	return v
}

// update hands p to the view without blocking, replacing an event not yet shown.
// It must only be called from one goroutine at a time (the engine's Run).
func (v *progressView) update(p engine.Progress) {
	for {
		select {
		case v.mailbox <- p:
			return
		default:
		}
		select {
		case <-v.mailbox:
		default:
		}
	}
}

// stop shows the last event, ends the progress line and returns the last event.
func (v *progressView) stop() (engine.Progress, bool) {
	close(v.quit)
	<-v.done
	return v.last, v.seen
}

func (v *progressView) run() {
	defer close(v.done)
	finish := func() {
		select {
		case p := <-v.mailbox:
			v.show(p)
		default:
		}
		v.con.keep()
	}
	for {
		select {
		case p := <-v.mailbox:
			v.show(p)
		case <-v.quit:
			finish()
			return
		}
		t := time.NewTimer(renderInterval)
		select {
		case <-t.C:
		case <-v.quit:
			t.Stop()
			finish()
			return
		}
	}
}

func (v *progressView) show(p engine.Progress) {
	v.last, v.seen = p, true
	if !v.named {
		v.announce()
	}
	v.con.status(progressLine(p))
}

// announce prints the target path once the engine has chosen the filename.
func (v *progressView) announce() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, err := v.st.GetDownload(ctx, v.id)
	if err != nil || d.Filename == "" {
		return
	}
	v.named = true
	v.con.printf("Saving to %s\n", filepath.Join(d.Dir, d.Filename))
}

// progressLine formats p as one short line: percent, bytes, speed, connections, ETA.
func progressLine(p engine.Progress) string {
	var b strings.Builder
	if p.Size >= 0 {
		fmt.Fprintf(&b, "%5.1f%%  %s / %s", percent(p.Received, p.Size), formatBytes(p.Received), formatBytes(p.Size))
	} else {
		b.WriteString(formatBytes(p.Received))
	}
	fmt.Fprintf(&b, "  %s/s  %d conn", formatBytes(int64(p.SpeedBps)), p.Connections)
	if p.Size >= 0 {
		fmt.Fprintf(&b, "  ETA %s", formatETA(p.Size-p.Received, p.SpeedBps))
	}
	return b.String()
}

// percent returns n as a percentage of size; an empty file is 100% done.
func percent(n, size int64) float64 {
	if size <= 0 {
		return 100
	}
	return min(float64(n)*100/float64(size), 100)
}

// formatBytes formats n with decimal units (1 KB = 1000 B): "512 B", "1.5 KB", "12.3 MB".
// A value that would round to 1000.0 of a unit is shown in the next one (999,999 B is
// "1.0 MB", never "1000.0 KB").
func formatBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	return decimalUnits(float64(n))
}

// decimalUnits formats n ≥ 1000 bytes with one decimal in the first decimal unit (KB,
// MB, GB, TB, PB, EB) in which it does not round to 1000.0.
func decimalUnits(n float64) string {
	return stepUnits(n/1000, "KB", "MB", "GB", "TB", "PB", "EB")
}

// stepUnits formats v, given in units[0], with one decimal in the first of the units (each
// 1000 times the one before) in which it does not round to 1000.0.
func stepUnits(v float64, units ...string) string {
	i := 0
	for math.Round(v*10) >= 10000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// formatETA formats the time to transfer remaining bytes at speed bytes per second,
// or "--" when the speed is too low to estimate.
func formatETA(remaining int64, speed float64) string {
	if remaining <= 0 {
		return "0s"
	}
	if speed < 1 {
		return "--"
	}
	secs := float64(remaining) / speed
	if secs > 100*24*3600 {
		return "--"
	}
	d := time.Duration(math.Ceil(secs)) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return formatDuration(d)
}

// formatDuration formats d compactly: "850ms", "12s", "4m05s", "1h02m".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	d = d.Round(time.Minute)
	return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// formatRate formats a speed limit.
func formatRate(bps int64) string {
	if bps <= 0 {
		return "unlimited"
	}
	return formatBytes(bps) + "/s"
}
