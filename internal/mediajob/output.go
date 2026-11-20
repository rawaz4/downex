package mediajob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"downex/internal/model"
)

// outputRecordName is the file in PartDir that records an assembled output. Track
// directories are named "<kind>-<id>", so it never collides with one.
const outputRecordName = "output.json"

// outputRecord identifies the output a job assembled: written, synced, into PartDir
// before the part file is renamed to OutPath, and left there (with the fragments) until
// the caller removes PartDir after storing the completion. A Run that finds it, and the
// file it describes under the part name or the final name, finishes without fetching:
// the job had completed but its caller had not recorded that (it died, or storing the
// completion failed).
type outputRecord struct {
	Name      string `json:"name"`      // base name of OutPath
	Size      int64  `json:"size"`      // size of the assembled file
	ModTime   int64  `json:"modTime"`   // its modification time (Unix nanoseconds), which renames keep
	Container string `json:"container"` // what Assemble returned
	// Clip describes the assembled part of a run with a clip (Progress.Clip).
	Clip *model.MediaClip `json:"clip,omitempty"`
}

// matches reports whether fi is the file r describes.
func (r *outputRecord) matches(fi fs.FileInfo) bool {
	return fi.Mode().IsRegular() && fi.Size() == r.Size && fi.ModTime().UnixNano() == r.ModTime
}

// recordOutput durably records the assembled part file of o in o.PartDir.
func recordOutput(ctx context.Context, o Options, part, container string, clip *model.MediaClip) error {
	fi, err := os.Stat(part)
	if err != nil {
		return err
	}
	b, err := json.Marshal(outputRecord{Name: filepath.Base(o.OutPath), Size: fi.Size(), ModTime: fi.ModTime().UnixNano(), Container: container, Clip: clip})
	if err != nil {
		return err
	}
	path := filepath.Join(o.PartDir, outputRecordName)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameRetry(ctx, o.Clock, tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(o.PartDir)
	return nil
}

// forgetOutput removes the output record of o (best effort).
func forgetOutput(o Options) { _ = os.Remove(filepath.Join(o.PartDir, outputRecordName)) }

// placeOutput renames the assembled part file to o.OutPath. It never replaces an existing
// file (the error then matches fs.ErrExist), except with o.ReplaceExisting a regular file
// there, in one atomic rename (renameReplace); a folder, a link or a Windows reparse
// point is never replaced. Sharing violations are retried. The caller syncs the folder.
func placeOutput(ctx context.Context, o Options, part string) error {
	if o.ReplaceExisting {
		ok, err := replaceable(o.OutPath)
		if err != nil {
			return err
		}
		if ok {
			return withSharingRetry(ctx, o.Clock, func() error { return renameReplace(part, o.OutPath) })
		}
	}
	return renameNoReplaceRetry(ctx, o.Clock, part, o.OutPath)
}

// replaceable reports whether path is a file ReplaceExisting may replace: it exists and
// is a regular file, not a folder, a link, a Windows reparse point or a device (as
// engine.Replaceable). A missing file is not (false, nil).
func replaceable(path string) (bool, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	return fi.Mode().IsRegular() && !isReparsePoint(fi), nil
}

// finishAssembled completes a job whose output was already assembled (outputRecord):
// it returns the container and true when OutPath is the recorded file, after renaming
// the recorded part file to OutPath when the job stopped before that (with
// ReplaceExisting also over the file it replaces, see placeOutput). A record that
// describes neither file is stale and removed; Run then proceeds as usual.
func finishAssembled(ctx context.Context, o Options) (string, bool, error) {
	c, _, done, err := finishRecorded(ctx, o)
	return c, done, err
}

// finishRecorded is finishAssembled that also returns the record's Clip.
func finishRecorded(ctx context.Context, o Options) (string, *model.MediaClip, bool, error) {
	f, err := os.Open(filepath.Join(o.PartDir, outputRecordName))
	if err != nil {
		return "", nil, false, nil
	}
	var rec outputRecord
	err = json.NewDecoder(io.LimitReader(f, 4<<10)).Decode(&rec)
	f.Close()
	if err != nil || rec.Name != filepath.Base(o.OutPath) || (rec.Container != "mp4" && rec.Container != "ts" && rec.Container != containerMP3) {
		forgetOutput(o)
		return "", nil, false, nil
	}
	part := o.OutPath + ".dxpart"
	out, outErr := os.Lstat(o.OutPath)
	pf, partErr := os.Lstat(part)
	switch {
	case outErr == nil && errors.Is(partErr, fs.ErrNotExist) && rec.matches(out):
		return rec.Container, rec.Clip, true, nil
	case partErr == nil && rec.matches(pf) && (errors.Is(outErr, fs.ErrNotExist) || outErr == nil && o.ReplaceExisting):
		if err := placeOutput(ctx, o, part); err != nil {
			return "", nil, false, fmt.Errorf("mediajob: renaming the output: %w", err)
		}
		syncDir(filepath.Dir(o.OutPath))
		return rec.Container, rec.Clip, true, nil
	}
	forgetOutput(o)
	return "", nil, false, nil
}

// checkpointTotals returns the fragments a checkpoint covers (nil: none).
func checkpointTotals(cp *Checkpoint) int {
	n := 0
	if cp != nil {
		for _, t := range cp.Tracks {
			n += max(t.Total, 0)
		}
	}
	return n
}
