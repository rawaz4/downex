package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"downex/internal/avscan"
	"downex/internal/filemark"
	"downex/internal/model"
)

// ScanOutcome is what the antivirus scan of a completed download found.
type ScanOutcome uint8

const (
	// ScanSkipped: scanning is off (or nothing was scanned).
	ScanSkipped ScanOutcome = iota
	// ScanClean: the scanner exited with 0.
	ScanClean
	// ScanProblem: the scanner exited with another code (PostResult.ExitCode): it
	// reported a problem with the file.
	ScanProblem
	// ScanFailed: the scan did not happen or did not finish (the scanner could not be
	// started, timed out, or the daemon shut down meanwhile).
	ScanFailed
)

// PostResult is the outcome of PostProcessor.Process. The download stays completed
// whatever it says.
type PostResult struct {
	Marked   bool        // the file got its origin mark
	Scan     ScanOutcome // the antivirus scan's outcome
	ExitCode int         // the scanner's exit code (ScanProblem)
	// Note is the text for Download.error ("" = none): set when the scan reported a
	// problem or did not finish, so the user sees it with the completed download.
	Note string
}

// Alarming reports whether the user must be told about the scan: it reported a
// problem or did not finish. Such a file is never opened automatically.
func (r PostResult) Alarming() bool { return r.Scan == ScanProblem || r.Scan == ScanFailed }

// NotificationText is the notification of an alarming scan of the file named name
// ("" when the scan is not alarming).
func (r PostResult) NotificationText(name string) string {
	switch r.Scan {
	case ScanProblem:
		return "Scanner reported a problem with " + name
	case ScanFailed:
		return "Could not scan " + name
	}
	return ""
}

// PostProcessor finishes a completed download's file before the daemon announces the
// completion (docs/M6-SPEC.md §3): it marks the file with its origin
// (mark_downloaded_files) and then runs the antivirus scanner on it (av_scan_enabled).
// Neither ever fails the download: a failed mark is logged, a scan that reports a
// problem or does not finish leaves a note (PostResult.Note) for Download.error and an
// alarming result for the notification. The zero value is not usable; create it with
// NewPostProcessor (tests replace the fields).
type PostProcessor struct {
	// Mark marks a file with its origin (filemark.Mark).
	Mark func(path string, o filemark.Origin) error
	// Scan runs the scanner on file (avscan.Run with avscan.DefaultTimeout).
	Scan func(ctx context.Context, command, args, file string) (avscan.Result, error)
	// Now is the time recorded in the mark.
	Now func() time.Time
	Log *slog.Logger
}

// NewPostProcessor returns a PostProcessor with the platform's marking and the real
// scanner run; log may be nil.
func NewPostProcessor(log *slog.Logger) *PostProcessor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &PostProcessor{
		Mark: filemark.Mark,
		Scan: func(ctx context.Context, command, args, file string) (avscan.Result, error) {
			return avscan.Run(ctx, command, args, file, avscan.Options{})
		},
		Now: time.Now,
		Log: log,
	}
}

// Process marks and then scans the final file of d, a download that has just
// completed, as s asks. ctx bounds the scan (it should end when the daemon shuts down,
// not with the short finalisation timeout: a scan may take up to ten minutes). It
// never fails and never panics.
func (p *PostProcessor) Process(ctx context.Context, d *model.Download, s DesktopSettings) (res PostResult) {
	log := p.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	defer func() {
		if v := recover(); v != nil {
			log.Error("post-processing of a completed download panicked", "download", int64(d.ID), "panic", fmt.Sprint(v))
			if s.scanConfigured() && res.Scan == ScanSkipped {
				res.Scan, res.Note = ScanFailed, "Antivirus scan could not run"
			}
		}
	}()
	if d.Filename == "" {
		return res
	}
	path := filepath.Join(d.Dir, d.Filename)
	if s.MarkDownloadedFiles && p.Mark != nil {
		now := time.Now()
		if p.Now != nil {
			now = p.Now()
		}
		if err := p.Mark(path, downloadOrigin(d, now)); err != nil {
			log.Warn("cannot mark the downloaded file with its origin", "download", int64(d.ID), "err", err)
		} else {
			res.Marked = true
		}
	}
	if !s.scanConfigured() || p.Scan == nil {
		return res
	}
	started := time.Now()
	r, err := p.Scan(ctx, s.AVScanCommand, s.AVScanArgs, path)
	switch {
	case errors.Is(err, avscan.ErrTimeout):
		res.Scan, res.Note = ScanFailed, fmt.Sprintf("Antivirus scan did not finish within %v", avscan.DefaultTimeout)
		log.Warn("antivirus scan timed out", "download", int64(d.ID), "after", time.Since(started).Round(time.Second))
	case err != nil && ctx.Err() != nil:
		res.Scan, res.Note = ScanFailed, "Antivirus scan was interrupted"
		log.Warn("antivirus scan interrupted", "download", int64(d.ID), "err", err)
	case err != nil:
		res.Scan, res.Note = ScanFailed, "Antivirus scan could not run"
		log.Warn("antivirus scan could not run", "download", int64(d.ID), "err", err)
	case r.ExitCode != 0:
		res.Scan, res.ExitCode = ScanProblem, r.ExitCode
		res.Note = fmt.Sprintf("Antivirus scan reported a problem (exit code %d)", r.ExitCode)
		log.Warn("antivirus scan reported a problem", "download", int64(d.ID), "exit_code", r.ExitCode,
			"duration", r.Duration.Round(time.Millisecond), "output", r.Output)
	default:
		res.Scan = ScanClean
		log.Info("antivirus scan found nothing", "download", int64(d.ID), "duration", r.Duration.Round(time.Millisecond))
	}
	return res
}

// postProcess marks and scans the file of rec, which its run has just completed and
// stored, as the desktop settings ask (finish calls it before the completion is
// announced). A note of the scan is stored as rec.Error; the download stays completed.
// The scan holds the download's slot and ends when Shutdown starts.
func (m *Manager) postProcess(rec *model.Download) PostResult {
	m.mu.Lock()
	desk := m.settings.Desktop
	m.mu.Unlock()
	if !desk.MarkDownloadedFiles && !desk.scanConfigured() {
		return PostResult{}
	}
	res := m.post.Process(m.base, rec, desk)
	if res.Note == "" {
		return res
	}
	rec.Error, rec.UpdatedAt = res.Note, time.Now()
	// The run's finalisation context may have expired during a long scan.
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	if err := m.st.UpdateDownload(ctx, rec); err != nil {
		m.log.Warn("cannot store the antivirus scan's note", "download", int64(rec.ID), "err", err)
	}
	return res
}

// downloadOrigin is where d's file came from: its final URL (else the requested one;
// a media download's manifest), and the Referer it was requested with (else a media
// download's page).
func downloadOrigin(d *model.Download, now time.Time) filemark.Origin {
	o := filemark.Origin{URL: d.FinalURL, Time: now}
	if o.URL == "" {
		o.URL = d.Request.URL
	}
	o.Referrer = d.Request.Headers.Get("Referer")
	if o.Referrer == "" && d.Media != nil {
		o.Referrer = d.Media.PageURL
	}
	return o
}
