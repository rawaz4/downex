package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// addInput is a validated AddRequest.
type addInput struct {
	req          model.Request
	dir          string // absolute and clean; not yet created
	dirGiven     bool   // dir came from the request (else it is the default directory)
	filename     string // sanitised, or "" to let the engine choose
	maxConn      int    // 0 = settings default
	speedLimit   int64
	startPaused  bool
	source       model.Source
	expectedSize int64  // 0 = unknown
	mime         string // normalised hint, "" = none
}

// parseAdd validates r (docs/DAEMON-SPEC.md §4: clients are untrusted, the browser
// relay forwards data from web pages). defaultDir (the default_dir setting, already
// validated) replaces an empty dir. It does not touch the file system; see ensureDir.
// The error is the message for CodeInvalidArgument.
func parseAdd(r *downexv1.AddRequest, defaultDir string) (addInput, error) {
	in := addInput{startPaused: r.GetStartPaused()}
	if err := validate.URL(r.GetUrl()); err != nil {
		return in, err
	}
	method, err := validate.Method(r.GetMethod())
	if err != nil {
		return in, err
	}
	body := r.GetBody()
	if err := validate.Body(len(body), method); err != nil {
		return in, err
	}
	headers, err := validate.Headers(r.GetHeaders())
	if err != nil {
		return in, err
	}
	switch dir := r.GetDir(); {
	case dir != "":
		if in.dir, err = checkDir(dir, defaultDir); err != nil {
			return in, err
		}
		in.dirGiven = true
	case defaultDir == "":
		return in, errors.New("dir is required: no default download directory is configured")
	default:
		in.dir = defaultDir
	}
	if in.filename, err = validate.Filename(r.GetFilename()); err != nil {
		return in, err
	}
	if in.maxConn, err = checkConnections(r.GetMaxConnections()); err != nil {
		return in, err
	}
	if in.speedLimit = r.GetSpeedLimit(); in.speedLimit < 0 {
		return in, errors.New("speed_limit must not be negative")
	}
	if in.expectedSize, err = validate.ExpectedSize(r.GetExpectedSize()); err != nil {
		return in, err
	}
	if in.mime, err = validate.MIME(r.GetMime()); err != nil {
		return in, err
	}
	in.source = sourceFromProto(r.GetSource())
	in.req = model.Request{URL: r.GetUrl(), Method: method, Headers: headers}
	if len(body) > 0 {
		in.req.Body = bytes.Clone(body)
	}
	return in, nil
}

// sourceFromProto maps an API source; values this build does not know are unknown.
func sourceFromProto(s downexv1.AddSource) model.Source {
	switch s {
	case downexv1.AddSource_ADD_SOURCE_CLI:
		return model.SourceCLI
	case downexv1.AddSource_ADD_SOURCE_UI:
		return model.SourceUI
	case downexv1.AddSource_ADD_SOURCE_BROWSER:
		return model.SourceBrowser
	case downexv1.AddSource_ADD_SOURCE_CLIPBOARD:
		return model.SourceClipboard
	}
	return model.SourceUnknown
}

// checkDir requires an absolute path and returns it cleaned. On Windows the path must
// also be a plain path on a local drive (see checkLocalDir), unless it is trusted: the
// already configured default directory, which may be a redirected network folder.
func checkDir(dir, trusted string) (string, error) {
	dir, err := absDir(dir)
	if err != nil {
		return "", err
	}
	if trusted != "" && samePath(dir, trusted) {
		return dir, nil
	}
	if err := checkLocalDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// absDir requires an absolute path and returns it cleaned.
func absDir(dir string) (string, error) {
	if strings.ContainsRune(dir, 0) || !filepath.IsAbs(dir) {
		return "", errors.New("dir must be an absolute path")
	}
	return filepath.Clean(dir), nil
}

// checkTrustedDir accepts a folder that comes from the settings rather than from a
// client (a category's folder): it is created with its parents when the download
// starts, so only something other than a directory at dir is refused.
func checkTrustedDir(dir string) error {
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return errInvalid("dir %s is not a directory", dir)
	}
	return nil
}

// ensureDir makes sure dir is a directory, creating it when it is missing but its
// parent exists (docs/DAEMON-SPEC.md §4: created only below an existing directory).
func ensureDir(dir string) error {
	exists, err := dirState(dir)
	if err != nil || exists {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return errPrecondition("create dir %s: %v", dir, err)
	}
	return nil
}

// checkCreatable is ensureDir without creating anything: dir is a directory, or it is
// missing and its parent is one.
func checkCreatable(dir string) error {
	_, err := dirState(dir)
	return err
}

// dirState reports whether dir exists as a directory; a missing dir whose parent is no
// directory, and anything else at dir, are errors.
func dirState(dir string) (exists bool, err error) {
	fi, err := os.Stat(dir)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return false, errInvalid("dir %s is not a directory", dir)
		}
		return true, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, errPrecondition("dir %s: %v", dir, err)
	}
	if pfi, err := os.Stat(filepath.Dir(dir)); err != nil || !pfi.IsDir() {
		return false, errInvalid("dir %s does not exist and its parent directory does not exist either", dir)
	}
	return false, nil
}

// checkConnections accepts 0 (the default) to MaxConnectionsLimit.
func checkConnections(n int32) (int, error) {
	if n < 0 || n > MaxConnectionsLimit {
		return 0, fmt.Errorf("max_connections must be between 0 and %d", MaxConnectionsLimit)
	}
	return int(n), nil
}

// handOff reports whether a download from source src was handed off rather than asked
// for in Downex itself (a browser hand-off, a URL seen on the clipboard): the
// confirmation policy (confirm_browser_downloads) and the progress window of an
// interactive start apply to it (docs/DESKTOP-SPEC.md §1, docs/M6-SPEC.md §3).
func handOff(src model.Source) bool {
	return src == model.SourceBrowser || src == model.SourceClipboard
}
