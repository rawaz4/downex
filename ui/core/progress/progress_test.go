package progress_test

import (
	"math"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
	"downex/ui/core/progress"
)

// The tables of ui/frontend/src/lib/progress.test.ts, case for case (its joinPath cases are
// in ui/core/display's tests).

const (
	queued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	probing     = downexv1.DownloadState_DOWNLOAD_STATE_PROBING
	downloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	paused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	completed   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	failed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	awaiting    = downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION

	connecting = downexv1.ConnectionState_CONNECTION_STATE_CONNECTING
	requesting = downexv1.ConnectionState_CONNECTION_STATE_REQUESTING
	receiving  = downexv1.ConnectionState_CONNECTION_STATE_RECEIVING
	retrying   = downexv1.ConnectionState_CONNECTION_STATE_RETRYING
	idle       = downexv1.ConnectionState_CONNECTION_STATE_IDLE
	done       = downexv1.ConnectionState_CONNECTION_STATE_DONE
	errored    = downexv1.ConnectionState_CONNECTION_STATE_ERROR
)

func TestMain(m *testing.M) {
	f, err := locale.New("en-US", "UTC")
	if err != nil {
		panic(err)
	}
	format.SetLocale(f)
	m.Run()
}

func with(d *downexv1.Download, edit func(*downexv1.Download)) *downexv1.Download {
	c := proto.Clone(d).(*downexv1.Download)
	edit(c)
	return c
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func utc(y int, mo time.Month, d, h, mi, s int) int64 {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC).UnixMilli()
}

func TestFormatElapsed(t *testing.T) {
	for _, c := range []struct {
		ms   float64
		want string
	}{
		{0, "<1s"}, {400, "<1s"}, {1499, "1s"}, {45_000, "45s"}, {59_600, "1m 00s"}, {754_250, "12m 34s"},
		{2*3600_000 + 7*60_000 + 12_000, "2h 07m 12s"}, {3*86400_000 + 4*3600_000 + 10*60_000, "3d 04h 10m"},
		{-1, "—"}, {math.NaN(), "—"},
	} {
		eq(t, "FormatElapsed", progress.FormatElapsed(c.ms), c.want)
	}
}

func timed(active, started, completedAt int64) *downexv1.Download {
	return &downexv1.Download{ActiveMs: active, StartedAt: Millis(started), CompletedAt: Millis(completedAt)}
}

func TestTimeTaken(t *testing.T) {
	t0 := utc(2026, 10, 20, 8, 0, 0)
	// downexd counts the running time without pauses: it wins over the clock.
	eq(t, "active", progress.TimeTakenText(timed(90_000, t0, t0+3600_000)), "1m 30s")
	// An older downexd: from the first start to completion.
	eq(t, "clock", progress.TimeTakenText(timed(0, t0, t0+3723_000)), "1h 02m 03s")
	eq(t, "no start", progress.TimeTakenText(timed(0, 0, t0)), "Unknown")
	eq(t, "no end", progress.TimeTakenText(timed(0, t0, 0)), "Unknown")
	eq(t, "end before start", progress.TimeTakenText(timed(0, t0+5, t0)), "Unknown")
}

func TestTimeTakenSummary(t *testing.T) {
	t0 := utc(2026, 10, 20, 8, 0, 0)
	doneDl := func(id int64, edit func(*downexv1.Download)) string {
		return progress.TimeTakenSummary(Download(id, func(d *downexv1.Download) {
			d.State = completed
			d.StartedAt = Millis(t0)
			d.CompletedAt = Millis(t0 + 3600_000)
			edit(d)
		}))
	}
	// 1368 MiB in 3 minutes of running (an hour with pauses): 7.6 MiB/s
	eq(t, "1", doneDl(1, func(d *downexv1.Download) { d.Size = 1368 * MiB; d.Received = 1368 * MiB; d.ActiveMs = 180_000 }), "3m 00s · average 8.0 MB/s")
	// a short one is still the whole download
	eq(t, "2", doneDl(2, func(d *downexv1.Download) { d.Size = 10 * MiB; d.Received = 10 * MiB; d.ActiveMs = 400 }), "<1s · average 26.2 MB/s")
	// an older downexd: the time from the first start includes pauses, so no average from it
	eq(t, "3", doneDl(3, func(d *downexv1.Download) { d.Received = 10 * MiB; d.CompletedAt = Millis(t0 + 3723_000) }), "1h 02m 03s")
	eq(t, "4", doneDl(4, func(d *downexv1.Download) { d.Received = 10 * MiB; d.StartedAt = nil }), "Unknown")
	// no bytes known: no average
	eq(t, "5", doneDl(5, func(d *downexv1.Download) { d.Size = 0; d.Received = 0; d.ActiveMs = 5000 }), "5s")
}

func TestMediaFormatText(t *testing.T) {
	eq(t, "1", progress.MediaFormatText(MediaDownload(1, nil)), "HLS stream, saved as MP4")
	eq(t, "2", progress.MediaFormatText(MediaDownload(2, func(d *downexv1.Download) {
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.Format = "dash"; m.Container = "ts" })
	})), "DASH stream, saved as MPEG-TS")
	eq(t, "3", progress.MediaFormatText(MediaDownload(3, func(d *downexv1.Download) {
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.Container = "" })
	})), "HLS stream")
	eq(t, "4", progress.MediaFormatText(MediaDownload(4, func(d *downexv1.Download) {
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.Format = "" })
	})), "Saved as MP4")
	eq(t, "5", progress.MediaFormatText(Download(5, nil)), "")
}

func TestFitWindowHeightComplete(t *testing.T) {
	slack := float64(progress.FitSlack)
	if slack < 2 {
		t.Errorf("FitSlack = %v", slack)
	}
	eq(t, "300", progress.FitWindowHeight(progress.ViewComplete, 300, 39), 300+39+slack)
	// 254.5 px of content and 24 px of padding (the 125 % case that scrolled by 1 px): never less
	eq(t, "278.5", progress.FitWindowHeight(progress.ViewComplete, 278.5, 39), math.Ceil(278.5+39+slack))
	if h := progress.FitWindowHeight(progress.ViewComplete, 278.5, 39); !(h > 278.5+39+1.9) {
		t.Errorf("278.5: %v", h)
	}
	eq(t, "100", progress.FitWindowHeight(progress.ViewComplete, 100, 39), progress.MinHeight)
	eq(t, "2000", progress.FitWindowHeight(progress.ViewComplete, 2000, 39), progress.MaxHeight)
	// Nothing measurable (no layout): the default height.
	eq(t, "0", progress.FitWindowHeight(progress.ViewComplete, 0, 39), progress.CompleteHeight)
	eq(t, "NaN", progress.FitWindowHeight(progress.ViewComplete, math.NaN(), 39), progress.CompleteHeight)
}

func TestFitWindowHeightCollapsed(t *testing.T) {
	slack := float64(progress.FitSlack)
	// a plain file fits the usual window
	eq(t, "200", progress.FitWindowHeight(progress.ViewCollapsed, 200, 39), progress.CollapsedHeight)
	// a web page's video with more facts gets the room it needs
	eq(t, "284.5", progress.FitWindowHeight(progress.ViewCollapsed, 284.5, 39), math.Ceil(284.5+39+slack))
	eq(t, "5000", progress.FitWindowHeight(progress.ViewCollapsed, 5000, 39), progress.MaxHeight)
	eq(t, "0", progress.FitWindowHeight(progress.ViewCollapsed, 0, 39), progress.CollapsedHeight)
}

func TestWindowFrame(t *testing.T) {
	eq(t, "39", progress.WindowFrame(300, 261), 39)
	if f := progress.WindowFrame(300, 261.4); math.Abs(f-38.6) >= 0.005 {
		t.Errorf("WindowFrame(300, 261.4) = %v", f)
	}
	// the host reported no outer size, or one that cannot be this window's
	eq(t, "null", progress.WindowFrame(math.NaN(), 261), progress.Frame)
	eq(t, "760", progress.WindowFrame(760, 261), progress.Frame)
	eq(t, "200", progress.WindowFrame(200, 261), progress.Frame)
}

func TestTitle(t *testing.T) {
	eq(t, "1", progress.Title(Download(1, func(d *downexv1.Download) {
		d.Filename = "a.iso"
		d.Size = 200 * MiB
		d.Received = 50 * MiB
		d.State = downloading
	})), "25% a.iso")
	eq(t, "2", progress.Title(Download(2, func(d *downexv1.Download) {
		d.Filename = "b.bin"
		d.Size = -1
		d.Received = 5 * MiB
		d.State = downloading
	})), "b.bin")
	eq(t, "3", progress.Title(Download(3, func(d *downexv1.Download) { d.Filename = "c.zip"; d.State = completed; d.Received = 10 * MiB })), "100% c.zip")
	// Never 100% before it is complete; hidden direction marks are shown.
	eq(t, "4", progress.Title(Download(4, func(d *downexv1.Download) {
		d.Filename = "x\u202egpj.exe"
		d.Size = 1000
		d.Received = 999
		d.State = downloading
	})), "99% x⟨U+202E⟩gpj.exe")
	// A stream counts fragments.
	eq(t, "5", progress.Title(MediaDownload(5, func(d *downexv1.Download) {
		d.State = downloading
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
	})), "25% Keynote.mp4")
}

func TestStatus(t *testing.T) {
	st := func(edit func(*downexv1.Download)) string { return progress.Status(Download(1, edit)) }
	eq(t, "probing", st(func(d *downexv1.Download) { d.State = probing }), "Connecting…")
	eq(t, "downloading", st(func(d *downexv1.Download) { d.State = downloading }), "Receiving data…")
	eq(t, "connecting", st(func(d *downexv1.Download) {
		d.State = downloading
		d.ConnectionInfo = []*downexv1.ConnectionInfo{
			Connection(1, func(c *downexv1.ConnectionInfo) { c.State = connecting }),
			Connection(2, func(c *downexv1.ConnectionInfo) { c.State = requesting }),
		}
	}), "Connecting…")
	eq(t, "one receiving", st(func(d *downexv1.Download) {
		d.State = downloading
		d.ConnectionInfo = []*downexv1.ConnectionInfo{Connection(1, func(c *downexv1.ConnectionInfo) { c.State = connecting }), Connection(2, nil)}
	}), "Receiving data…")
	eq(t, "paused", st(func(d *downexv1.Download) { d.State = paused }), "Paused")
	eq(t, "queued 3", st(func(d *downexv1.Download) { d.State = queued; d.QueuePosition = 3 }), "Waiting in queue (#3)")
	eq(t, "queued", st(func(d *downexv1.Download) { d.State = queued }), "Waiting in queue")
	eq(t, "completed", st(func(d *downexv1.Download) { d.State = completed }), "Completed")
	eq(t, "failed 404", st(func(d *downexv1.Download) { d.State = failed; d.Error = "Server responded 404 Not Found" }), "Failed: Server responded 404 Not Found")
	eq(t, "failed", st(func(d *downexv1.Download) { d.State = failed }), "Failed: the download stopped with an error")
	eq(t, "awaiting", st(func(d *downexv1.Download) { d.State = awaiting }), "Waiting for confirmation")
	assembling := MediaDownload(2, func(d *downexv1.Download) {
		d.State = downloading
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 200 })
	})
	eq(t, "IsAssembling", progress.IsAssembling(assembling), true)
	eq(t, "assembling", progress.Status(assembling), "Assembling…")
	eq(t, "rate", progress.RateText(assembling), "—")
	eq(t, "time left", progress.TimeLeftText(assembling), "—")
}

func TestSizesRatesTimeLeftAndResume(t *testing.T) {
	running := Download(1, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = 100 * MiB
		d.Received = 25 * MiB
		d.SpeedBps = 5 * MiB
		d.Mode = downexv1.Mode_MODE_RANGED
	})
	eq(t, "size", progress.SizeText(running), "105 MB")
	eq(t, "downloaded", progress.DownloadedText(running), "26.2 MB (25%)")
	eq(t, "rate", progress.RateText(running), "5.2 MB/s")
	eq(t, "time left", progress.TimeLeftText(running), "15s")
	eq(t, "resume", progress.ResumeSupport(running), "Yes")
	unknown := Download(2, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = -1
		d.Received = 3 * MiB
		d.Mode = downexv1.Mode_MODE_SINGLE
	})
	eq(t, "unknown size", progress.SizeText(unknown), "Unknown")
	eq(t, "unknown downloaded", progress.DownloadedText(unknown), "3.1 MB")
	eq(t, "unknown time left", progress.TimeLeftText(unknown), "—")
	eq(t, "unknown resume", progress.ResumeSupport(unknown), "No")
	eq(t, "default resume", progress.ResumeSupport(Download(3, nil)), "Unknown")
	eq(t, "paused rate", progress.RateText(Download(4, func(d *downexv1.Download) { d.State = paused; d.SpeedBps = 9 })), "—")
	eq(t, "media resume", progress.ResumeSupport(MediaDownload(5, nil)), "Yes")
}

func TestAverageBps(t *testing.T) {
	avg := func(received, active int64) float64 {
		return progress.AverageBps(&downexv1.Download{Received: received, ActiveMs: active})
	}
	if got, want := avg(1368*MiB, 180_000), float64(1368*MiB)/180; math.Abs(got-want) >= 0.005 {
		t.Errorf("AverageBps = %v, want ≈ %v", got, want)
	}
	eq(t, "0 bytes", avg(0, 4000), 0)
	// no running time counted (not started, an older downexd): unknown, never a division by 0
	if v := avg(5*MiB, 0); !math.IsNaN(v) {
		t.Errorf("no running time: %v", v)
	}
	if v := avg(-1, 4000); !math.IsNaN(v) {
		t.Errorf("no bytes: %v", v)
	}
}

func TestAverageRateText(t *testing.T) {
	run := func(edit func(*downexv1.Download)) *downexv1.Download {
		return Download(1, func(d *downexv1.Download) {
			d.State = downloading
			d.Size = 100 * MiB
			d.Received = 24 * MiB
			d.SpeedBps = 5 * MiB
			edit(d)
		})
	}
	if progress.AverageMinActiveMS < 2000 {
		t.Errorf("AverageMinActiveMS = %d", progress.AverageMinActiveMS)
	}
	// its first seconds are mostly connecting: no figure yet
	eq(t, "0", progress.AverageRateText(run(func(d *downexv1.Download) { d.ActiveMs = 0 })), "")
	eq(t, "almost", progress.AverageRateText(run(func(d *downexv1.Download) { d.ActiveMs = progress.AverageMinActiveMS - 1 })), "")
	eq(t, "3000", progress.AverageRateText(run(func(d *downexv1.Download) { d.ActiveMs = 3000 })), "8.4 MB/s")
	// earlier runs count: 24 MiB over 12 s of running in all
	eq(t, "12000", progress.AverageRateText(run(func(d *downexv1.Download) { d.ActiveMs = 12_000 })), "2.1 MB/s")
	// no live rate (paused, queued, assembling): no average beside it
	eq(t, "paused", progress.AverageRateText(run(func(d *downexv1.Download) { d.State = paused; d.SpeedBps = 0; d.ActiveMs = 3000 })), "")
	eq(t, "queued", progress.AverageRateText(run(func(d *downexv1.Download) { d.State = queued; d.SpeedBps = 0; d.ActiveMs = 3000 })), "")
	assembling := MediaDownload(2, func(d *downexv1.Download) {
		d.State = downloading
		d.ActiveMs = 60_000
		d.Received = 300 * MiB
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 200 })
	})
	eq(t, "assembling", progress.AverageRateText(assembling), "")
}

func TestStreamEstimate(t *testing.T) {
	stream := MediaDownload(1, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = 2100 * MiB
		d.SizeApprox = true
		d.Received = 700 * MiB
		d.SpeedBps = 7 * MiB
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
	})
	eq(t, "size", progress.SizeText(stream), "~2.2 GB")
	// The percentage stays the fragments'.
	eq(t, "downloaded", progress.DownloadedText(stream), "734 MB (25%)")
	eq(t, "title", progress.Title(stream), "25% Keynote.mp4")
	// (2100 − 700) MiB at 7 MiB/s.
	eq(t, "time left", progress.TimeLeftText(stream), "3m 20s")
	// Without an estimate: unknown, and the time left from the fragments (700 MiB were 25%).
	unsized := with(stream, func(d *downexv1.Download) { d.Size = -1; d.SizeApprox = false })
	eq(t, "unsized size", progress.SizeText(unsized), "Unknown")
	eq(t, "unsized time left", progress.TimeLeftText(unsized), "5m 00s")
	// Completed: the file's exact size.
	eq(t, "completed", progress.SizeText(with(stream, func(d *downexv1.Download) {
		d.State = completed
		d.Size = 1900 * MiB
		d.SizeApprox = false
		d.Received = 1900 * MiB
	})), "2.0 GB")
	// A web page's video counts its bytes, never its parts, even while its size is an estimate
	// (decision N1, 0.8.2: a part's fraction went back on pause and resume).
	site := SiteDownload(2, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = 460 * MiB
		d.SizeApprox = true
		d.Received = 100 * MiB
		d.Media = SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal = 46; m.FragmentsDone = 23 })
	})
	eq(t, "site size", progress.SizeText(site), "~482 MB")
	if f := format.ProgressFraction(site); math.Abs(f-100.0/460) >= 0.005 {
		t.Errorf("site fraction = %v", f)
	}
}

func TestFragmentsAndQuality(t *testing.T) {
	m := MediaDownload(1, func(d *downexv1.Download) { d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 12 }) })
	eq(t, "fragments", progress.FragmentsText(m), "12 / 200")
	// DESIGN-SYSTEM.md §4.8: the human codec text; the picture and raw codecs move to the tooltip.
	eq(t, "quality", progress.QualityText(m), "1080p · H.264 · AAC 128 kbit/s")
	eq(t, "detail", progress.QualityDetail(m), "1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps · English · 128 kbit/s · mp4a.40.2")
	// a muxed quality's second codec is its sound (960×540 is in the 480p class)
	eq(t, "muxed", progress.QualityText(MediaDownload(3, func(d *downexv1.Download) {
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.SelectedVideo = "v2"; m.SelectedAudio = "" })
	})), "480p · H.264 · AAC")
	// audio only
	eq(t, "audio only", progress.QualityText(MediaDownload(4, func(d *downexv1.Download) {
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.SelectedVideo = ""; m.SelectedAudio = "r1" })
	})), "AAC 96 kbit/s")
	eq(t, "file fragments", progress.FragmentsText(Download(2, nil)), "")
	eq(t, "file quality", progress.QualityText(Download(2, nil)), "")
	eq(t, "file detail", progress.QualityDetail(Download(2, nil)), "")
}

func TestDoneAtText(t *testing.T) {
	at := float64(utc(2026, 10, 28, 11, 0, 0))
	running := Download(1, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = 100 * MiB
		d.Received = 25 * MiB
		d.SpeedBps = 5 * MiB
	})
	// 75 MiB at 5 MiB/s: 15 s from now
	eq(t, "running", progress.DoneAtText(running, at), format.Clock(at+15_000))
	eq(t, "speed 0", progress.DoneAtText(with(running, func(d *downexv1.Download) { d.SpeedBps = 0 }), at), "")
	eq(t, "size -1", progress.DoneAtText(with(running, func(d *downexv1.Download) { d.Size = -1 }), at), "")
	eq(t, "paused", progress.DoneAtText(with(running, func(d *downexv1.Download) { d.State = paused }), at), "")
	eq(t, "assembling", progress.DoneAtText(MediaDownload(2, func(d *downexv1.Download) {
		d.State = downloading
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 200 })
	}), at), "")
}

func TestHeaderPercent(t *testing.T) {
	d := Download(1, func(d *downexv1.Download) { d.State = downloading; d.Size = 200 * MiB; d.Received = 50 * MiB })
	eq(t, "downloading", progress.HeaderPercent(d), "25%")
	eq(t, "paused", progress.HeaderPercent(with(d, func(d *downexv1.Download) { d.State = paused })), "25%")
	eq(t, "estimate", progress.HeaderPercent(with(d, func(d *downexv1.Download) { d.SizeApprox = true })), "~25%")
	eq(t, "unknown", progress.HeaderPercent(with(d, func(d *downexv1.Download) { d.Size = -1 })), "")
	// never 100% before it is complete
	eq(t, "almost", progress.HeaderPercent(with(d, func(d *downexv1.Download) { d.Received = 200*MiB - 1 })), "99%")
	eq(t, "completed", progress.HeaderPercent(with(d, func(d *downexv1.Download) {
		d.State = completed
		d.SizeApprox = true
		d.Received = 200 * MiB
	})), "100%")
}

func TestConnectionText(t *testing.T) {
	c := func(state downexv1.ConnectionState, detail string) string {
		return progress.ConnectionText(Connection(1, func(c *downexv1.ConnectionInfo) { c.State = state; c.Detail = detail }), false, "")
	}
	eq(t, "connecting", c(connecting, ""), "Connecting…")
	eq(t, "requesting", c(requesting, ""), "Sending request…")
	eq(t, "receiving", c(receiving, ""), "Receiving data…")
	eq(t, "retrying in", c(retrying, "Retrying in 4 s (HTTP 503)"), "Retrying in 4 s (HTTP 503)")
	eq(t, "retrying reason", c(retrying, "HTTP 503"), "Retrying (HTTP 503)")
	// downexd's own wording.
	eq(t, "downexd", c(retrying, "HTTP 503, retrying in 4 s"), "Retrying in 4 s (HTTP 503)")
	eq(t, "downexd min", c(retrying, "connection reset, retrying in 2 min"), "Retrying in 2 min (connection reset)")
	eq(t, "retrying", c(retrying, ""), "Retrying…")
	eq(t, "idle", c(idle, ""), "Idle")
	eq(t, "done", c(done, ""), "Finished")
	eq(t, "error detail", c(errored, "disk full"), "Error: disk full")
	eq(t, "error", c(errored, ""), "Error")
	// Media: the fragment it fetches (shown from 1).
	eq(t, "fragment", progress.ConnectionText(Connection(2, func(c *downexv1.ConnectionInfo) { c.Fragment = 41 }), true, ""), "Fragment 42 · Receiving data…")
	eq(t, "no fragment", progress.ConnectionText(Connection(2, func(c *downexv1.ConnectionInfo) { c.Fragment = -1 }), true, ""), "Receiving data…")
}

func site(edit func(*downexv1.Download)) *downexv1.Download {
	return SiteDownload(1, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = 460 * MiB
		d.Received = 115 * MiB
		d.Media = SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal = 46; m.FragmentsDone = 11 })
		if edit != nil {
			edit(d)
		}
	})
}

func TestSitePageInfoPartsMerging(t *testing.T) {
	eq(t, "page", progress.AddressLabel(site(nil)), "Page")
	eq(t, "manifest", progress.AddressLabel(MediaDownload(2, nil)), "Manifest")
	eq(t, "address", progress.AddressLabel(Download(3, nil)), "Address")
	eq(t, "site probing", progress.Status(site(func(d *downexv1.Download) { d.State = probing })), "Getting video info…")
	eq(t, "stream probing", progress.Status(MediaDownload(2, func(d *downexv1.Download) { d.State = probing })), "Connecting…")
	if one, many := progress.PieceNames(site(nil)); one != "Part" || many != "Parts" {
		t.Errorf("PieceNames(site) = %q %q", one, many)
	}
	if one, many := progress.PieceNames(MediaDownload(2, nil)); one != "Fragment" || many != "Fragments" {
		t.Errorf("PieceNames(stream) = %q %q", one, many)
	}
	eq(t, "part", progress.ConnectionText(Connection(1, func(c *downexv1.ConnectionInfo) { c.Fragment = 11 }), true, "Part"), "Part 12 · Receiving data…")
	merging := site(func(d *downexv1.Download) {
		d.Media = SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal = 46; m.FragmentsDone = 46 })
	})
	eq(t, "IsAssembling", progress.IsAssembling(merging), true)
	eq(t, "merging", progress.Status(merging), "Merging…")
	eq(t, "assembling", progress.Status(MediaDownload(2, func(d *downexv1.Download) {
		d.State = downloading
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 200 })
	})), "Assembling…")
}

func TestSiteSizeFromBytes(t *testing.T) {
	eq(t, "size", progress.SizeText(site(nil)), "482 MB")
	// 115 of 460 MiB: 25%, though 11 of 46 parts would be 23%
	eq(t, "downloaded", progress.DownloadedText(site(nil)), "121 MB (25%)")
	eq(t, "fraction", format.ProgressFraction(site(nil)), 0.25)
	eq(t, "title", progress.Title(site(nil)), "25% A talk.mp4")
	eq(t, "fragments", progress.FragmentsText(site(nil)), "11 / 46")
	// a stream counts its fragments; a web page's video with no size yet has no fraction (never its parts)
	eq(t, "stream", format.ProgressFraction(MediaDownload(2, func(d *downexv1.Download) {
		d.Received = 1
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
	})), 0.25)
	if f := format.ProgressFraction(site(func(d *downexv1.Download) { d.Size = -1 })); !math.IsNaN(f) {
		t.Errorf("site without size: %v", f)
	}
}

func TestSiteNamesOnceComplete(t *testing.T) {
	d := site(func(d *downexv1.Download) { d.State = completed; d.Received = 460 * MiB })
	eq(t, "quality", progress.QualityText(d), "1080p · H.264 · AAC 129 kbit/s")
	eq(t, "format", progress.MediaFormatText(d), "YouTube video, saved as MP4")
	muxed := site(func(d *downexv1.Download) {
		d.Media = SiteMediaInfo(func(m *downexv1.MediaInfo) { m.Site = ""; m.SelectedVideo = "18"; m.SelectedAudio = "" })
	})
	eq(t, "muxed format", progress.MediaFormatText(muxed), "Video, saved as MP4")
	eq(t, "muxed quality", progress.QualityText(muxed), "360p · H.264 · AAC")
}

func TestIsRefused(t *testing.T) {
	for code, want := range map[string]bool{"media_invalid": true, "site_unsupported": true, "site_unavailable": false, "encrypted": true} {
		eq(t, code, progress.IsRefused(&downexv1.Download{ErrorCode: code}), want)
	}
}
