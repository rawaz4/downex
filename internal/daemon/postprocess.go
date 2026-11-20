package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"downex/internal/avscan"
	"downex/internal/filecheck"
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
	// Checked: the file check ran (docs/DESKTOP-SPEC.md §9); Check is its result (nil =
	// nothing to say, or the check could not read the file).
	Checked bool
	Check   *model.FileCheck
}

// withoutPath drops the file's path from a file error: the file check's log lines never
// carry file names (docs/DESKTOP-SPEC.md §9.1 F1).
func withoutPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// FileCheckNotice is the notification text of the file check of the file named name:
// DANGER always, CAUTION only while notify_on_complete is on (§9.7). ok is false when
// there is none.
func (r PostResult) FileCheckNotice(name string, notifyOnComplete bool) (text string, ok bool) {
	switch {
	case r.Check == nil:
		return "", false
	case r.Check.Level == model.FileCheckDanger,
		r.Check.Level == model.FileCheckCaution && notifyOnComplete:
		return filecheck.NotificationText(r.Check, name), true
	}
	return "", false
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
// completion (docs/M6-SPEC.md §3): it checks whether the file is what its name says
// (check_downloaded_files, docs/DESKTOP-SPEC.md §9; file downloads only), marks the file
// with its origin (mark_downloaded_files) whatever the check found, and then runs the
// antivirus scanner on it (av_scan_enabled).
// Neither ever fails the download: a failed mark is logged, a scan that reports a
// problem or does not finish leaves a note (PostResult.Note) for Download.error and an
// alarming result for the notification. The zero value is not usable; create it with
// NewPostProcessor (tests replace the fields).
type PostProcessor struct {
	// Check runs the final file check on the completed file at path (filecheck.CheckPath
	// with d's name, hidden-controls flag and served type, within filecheck.Timeout).
	Check func(ctx context.Context, path string, d *model.Download) (*model.FileCheck, error)
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
		Check: func(ctx context.Context, path string, d *model.Download) (*model.FileCheck, error) {
			return filecheck.CheckPath(ctx, path, d.Filename, d.NameControls, d.ServedType)
		},
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
	if s.CheckFiles && d.Kind == model.KindFile && p.Check != nil {
		fc, err := p.Check(ctx, path, d)
		if err != nil { // advisory: no result, the download is unaffected
			log.Warn("cannot check the downloaded file", "download", int64(d.ID), "err", withoutPath(err))
		} else {
			res.Checked, res.Check = true, fc
			if fc != nil {
				log.Info("file check", "download", int64(d.ID), "level", int(fc.Level), "reason", fc.Reason, "content", fc.Content)
			}
		}
	}
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

// postProcess checks, marks and scans the file of rec, which its run has just completed
// and stored, as the desktop settings ask (finish calls it before the completion is
// announced). The check's result is stored with SetFileCheck and set as rec.FileCheck
// (docs/DESKTOP-SPEC.md §9); a note of the scan is stored as rec.Error; the download
// stays completed.
// The scan holds the download's slot and ends when Shutdown starts.
func (m *Manager) postProcess(rec *model.Download) PostResult {
	m.mu.Lock()
	desk := m.settings.Desktop
	m.mu.Unlock()
	var res PostResult
	if desk.MarkDownloadedFiles || desk.scanConfigured() || desk.CheckFiles {
		res = m.post.Process(m.base, rec, desk)
	}
	// The run's finalisation context may have expired during a long scan.
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	// The stored check describes this file only: a check that did not run (setting off,
	// a media download, a read error) leaves none rather than one of an earlier copy.
	if res.Checked || rec.FileCheck != nil {
		if res.Check != nil {
			res.Check.CheckedAt = m.now()
		}
		if err := m.st.SetFileCheck(ctx, rec.ID, res.Check); err != nil {
			m.log.Warn("cannot store the file check", "download", int64(rec.ID), "err", err)
		}
		rec.FileCheck = res.Check
	}
	if res.Note == "" {
		return res
	}
	rec.Error, rec.UpdatedAt = res.Note, time.Now()
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
