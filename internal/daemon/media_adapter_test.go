package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"downex/internal/media"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/remux"
)

// The job-facing types mirror internal/mediajob's (field names, types and order), so that
// downexd's adapter converts them directly instead of copying field by field.
var (
	_ = MediaSelection(mediajob.Selection{})
	_ = mediajob.Selection(MediaSelection{})
	_ = MediaProbe(mediajob.Info{})
	_ = MediaProgress(mediajob.Progress{})
	_ = model.MediaTrackCheckpoint(mediajob.TrackCheckpoint{})
	_ = mediajob.TrackCheckpoint(model.MediaTrackCheckpoint{})
)

// The daemon knows the job's own errors only by their text (it depends on the job
// through MediaJobs): every sentinel of internal/mediajob and internal/remux, wrapped as
// the job wraps them, maps to its stable code, both live and from the persisted message.
func TestMediaJobErrorCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("mediajob: track %q: %w", "v1", media.ErrEncrypted), CodeEncrypted},
		{fmt.Errorf("mediajob: playlist: %w", media.ErrLive), CodeLive},
		{fmt.Errorf("mediajob: %w: WebM", media.ErrUnsupported), CodeMediaUnsupported},
		{fmt.Errorf("mediajob: assemble: %w", fmt.Errorf("input 1: %w", remux.ErrUnsupported)), CodeMediaUnsupported},
		{fmt.Errorf("mediajob: assemble: input 0 fragment 3: %w", remux.ErrMalformed), CodeMediaInvalid},
		{fmt.Errorf("%w: %w: no track %q", mediajob.ErrInvalidSelection, media.ErrNotFound, "v9"), CodeMediaInvalid},
		{fmt.Errorf("%w: track %q is not a video track", mediajob.ErrInvalidSelection, "a1"), CodeMediaInvalid},
		{mediajob.ErrNotManifest, CodeMediaInvalid},
		{fmt.Errorf("mediajob: MPD: %w", media.ErrMalformed), CodeMediaInvalid},
		{fmt.Errorf("mediajob: manifest: %w", media.ErrTooLarge), CodeMediaInvalid},
		{fmt.Errorf("mediajob: fragment 4 of track %q: %w", "v1", &mediajob.StatusError{Code: 403}), CodeHTTPStatus},
		{fmt.Errorf("mediajob: fragment 4: %w", mediajob.ErrRangeMismatch), CodeHTTPStatus},
		{fmt.Errorf("mediajob: fragment 4: %w", mediajob.ErrTruncated), CodeNetwork},
		// A full volume while the tracks are joined, tagged as the job tags it.
		{fmt.Errorf("mediajob: assembling: %w: %w", mediajob.ErrDiskFull, fmt.Errorf("remux: write output: %w",
			&fs.PathError{Op: "write", Path: `D:ideo.mp4.dxpart`, Err: syscall.Errno(112)})), CodeDiskFull},
	} {
		if got := mediaErrorCode(c.err); got != c.code {
			t.Errorf("mediaErrorCode(%q) = %q, want %q", c.err, got, c.code)
		}
		rec := &model.Download{Kind: model.KindMedia, Error: c.err.Error()}
		if got := recordErrorCode(rec); got != c.code {
			t.Errorf("recordErrorCode(%q) = %q, want %q", c.err, got, c.code)
		}
	}
	if !errors.Is(remux.ErrUnsupported, errors.ErrUnsupported) {
		t.Error("remux.ErrUnsupported does not wrap errors.ErrUnsupported")
	}
}
