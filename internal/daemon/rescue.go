package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// Rescue of a partial file a browser left unfinished (docs/DAEMON-SPEC.md §3 "Rescue",
// docs/ENGINE-SPEC.md §7.4).
const (
	// MaxRescuePathBytes bounds partial_path and target_path.
	MaxRescuePathBytes = 32 << 10
	// MaxRescueValidatorBytes bounds etag and last_modified.
	MaxRescueValidatorBytes = 1024
	// MaxRescueListing is how many entries of a folder are read to find a provisional
	// partial ("Unconfirmed N.crdownload"); a larger folder is rescue_ambiguous.
	MaxRescueListing = 10_000
	// RescueStartWindow is how long a rescue stored paused for the browser relay waits for
	// its first start (ResumeRequest.rescue_start).
	RescueStartWindow = 10 * time.Minute
	// rescueSlack: a provisional partial may be this much larger than the browser's count
	// (bytes written but not counted yet).
	rescueSlack = 16 << 20
	// rescueClockSlack: a partial (or placeholder) older than the browser download's start
	// by more than this is not its own.
	rescueClockSlack = 5 * time.Second
)

// rescueSuffixes are the endings of browser partial files (lower case).
var rescueSuffixes = [...]string{".crdownload", ".part"}

// unconfirmedRE matches the provisional name of a Chromium partial file.
var unconfirmedRE = regexp.MustCompile(`^Unconfirmed [0-9]{1,10}\.crdownload$`)

// rescueInput is a validated RescueRequest.
type rescueInput struct {
	req          model.Request
	partial      string // absolute and clean; "" = find it from target
	target       string // absolute and clean; "" = none
	trusted      int64  // the browser's count of good bytes (0 = unknown)
	expectedSize int64  // 0 = unknown
	validators   model.Validators
	filename     string // sanitised; "" = from the partial's name
	source       model.Source
	maxConn      int
	speedLimit   int64
	startPaused  bool
	startedAt    time.Time // zero = unknown
	pageURL      string
}

// parseRescue validates r (docs/DAEMON-SPEC.md §4). The error is the message for
// CodeInvalidArgument.
func parseRescue(r *downexv1.RescueRequest) (rescueInput, error) {
	in := rescueInput{startPaused: r.GetStartPaused(), source: sourceFromProto(r.GetSource())}
	if err := validate.URL(r.GetUrl()); err != nil {
		return in, err
	}
	headers, err := validate.Headers(r.GetHeaders())
	if err != nil {
		return in, err
	}
	in.req = model.Request{URL: r.GetUrl(), Method: http.MethodGet, Headers: headers}
	switch in.source {
	case model.SourceBrowser, model.SourceUI, model.SourceCLI:
	default:
		return in, errors.New("source must be BROWSER, UI or CLI")
	}
	if in.partial, err = rescuePath("partial_path", r.GetPartialPath()); err != nil {
		return in, err
	}
	if in.target, err = rescuePath("target_path", r.GetTargetPath()); err != nil {
		return in, err
	}
	switch {
	case in.source == model.SourceBrowser && in.partial == "" && in.target == "":
		return in, errors.New("partial_path or target_path is required")
	case in.source != model.SourceBrowser && in.partial == "":
		return in, errors.New("partial_path is required")
	}
	if in.trusted = r.GetBytesReceived(); in.trusted < 0 {
		return in, errors.New("bytes_received must not be negative")
	}
	if in.expectedSize, err = validate.ExpectedSize(r.GetExpectedSize()); err != nil {
		return in, err
	}
	switch ms := r.GetStartedAtMs(); {
	case ms < 0:
		return in, errors.New("started_at_ms must not be negative")
	case ms > 0:
		in.startedAt = time.UnixMilli(ms)
	}
	etag, lm := r.GetEtag(), r.GetLastModified()
	if err := rescueValidator("etag", etag); err != nil {
		return in, err
	}
	if err := rescueValidator("last_modified", lm); err != nil {
		return in, err
	}
	in.validators = model.Validators{ETag: etag, StrongETag: etag != "" && !strings.HasPrefix(etag, "W/"), LastModified: lm}
	if in.filename, err = validate.Filename(r.GetFilename()); err != nil {
		return in, err
	}
	if in.maxConn, err = checkConnections(r.GetMaxConnections()); err != nil {
		return in, err
	}
	if in.speedLimit = r.GetSpeedLimit(); in.speedLimit < 0 {
		return in, errors.New("speed_limit must not be negative")
	}
	if in.pageURL, err = parsePageURL(r.GetPageUrl()); err != nil {
		return in, err
	}
	return in, nil
}

// rescuePath checks an absolute path of at most MaxRescuePathBytes ("" = none) and
// returns it cleaned.
func rescuePath(field, p string) (string, error) {
	switch {
	case p == "":
		return "", nil
	case len(p) > MaxRescuePathBytes:
		return "", fmt.Errorf("%s is longer than %d bytes", field, MaxRescuePathBytes)
	case strings.ContainsRune(p, 0) || !filepath.IsAbs(p):
		return "", fmt.Errorf("%s must be an absolute path", field)
	}
	return filepath.Clean(p), nil
}

// rescueValidator checks an entity tag or a date: at most MaxRescueValidatorBytes, no
// control characters.
func rescueValidator(field, v string) error {
	switch {
	case len(v) > MaxRescueValidatorBytes:
		return fmt.Errorf("%s is longer than %d bytes", field, MaxRescueValidatorBytes)
	case strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return fmt.Errorf("%s contains control characters", field)
	}
	return nil
}

// partialFS is the file system findPartial reads (os in the daemon, a stand-in in tests).
type partialFS interface {
	Lstat(name string) (fs.FileInfo, error)
	// ReadDir returns at most n entries of dir.
	ReadDir(dir string, n int) ([]fs.DirEntry, error)
}

type osPartialFS struct{}

func (osPartialFS) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(name) }

func (osPartialFS) ReadDir(dir string, n int) ([]fs.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := f.ReadDir(n)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return list, err
}

// partialLoc is a found partial file and, for Firefox, its placeholder.
type partialLoc struct {
	partial     string
	placeholder string // "" = none
}

// rescueCoded is a refusal with its code (rescueRefusal turns it into the RPC error).
type rescueCoded struct {
	code string
	err  error
}

func (c *rescueCoded) Error() string { return c.err.Error() }
func (c *rescueCoded) Unwrap() error { return c.err }

func coded(code string, format string, args ...any) error {
	return &rescueCoded{code: code, err: fmt.Errorf(format, args...)}
}

// fileRefusal maps a file error while looking for the partial (R-F4).
func fileRefusal(what string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return &rescueCoded{code: CodeRescueNoAccess, err: fmt.Errorf("%s: %w", what, err)}
	case errors.Is(err, fs.ErrNotExist):
		return &rescueCoded{code: CodeRescueNotFound, err: fmt.Errorf("%s: %w", what, err)}
	}
	return &rescueCoded{code: CodeRescueNotFound, err: fmt.Errorf("%s: %w", what, err)}
}

// hasRescueSuffix reports a browser partial's name.
func hasRescueSuffix(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range rescueSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// findPartial finds the partial file of in (R-F1-R-F3). Errors are *rescueCoded or, for
// input a client should not send, plain errors (InvalidArgument).
func findPartial(fsys partialFS, in rescueInput) (partialLoc, error) {
	browser := in.source == model.SourceBrowser
	if in.partial != "" {
		base := filepath.Base(in.partial)
		if browser {
			if !hasRescueSuffix(base) {
				return partialLoc{}, errors.New("partial_path must end in .crdownload or .part")
			}
			if in.target != "" && !samePath(filepath.Dir(in.partial), filepath.Dir(in.target)) {
				return partialLoc{}, errors.New("partial_path must lie in target_path's folder")
			}
		}
		fi, err := fsys.Lstat(in.partial)
		if err != nil {
			return partialLoc{}, fileRefusal(base, err)
		}
		if !fi.Mode().IsRegular() {
			return partialLoc{}, coded(CodeRescueNotFound, "%s is not a regular file", base)
		}
		return partialLoc{partial: in.partial, placeholder: placeholderFor(fsys, in, in.partial)}, nil
	}
	// Only the target (a browser): its own partial names first, then a provisional one.
	for _, suf := range [...]string{".crdownload", ".part"} {
		p := in.target + suf
		fi, err := fsys.Lstat(p)
		switch {
		case err == nil && fi.Mode().IsRegular():
			return partialLoc{partial: p, placeholder: placeholderFor(fsys, in, p)}, nil
		case err != nil && errors.Is(err, fs.ErrPermission):
			return partialLoc{}, fileRefusal(filepath.Base(p), err)
		}
	}
	dir := filepath.Dir(in.target)
	list, err := fsys.ReadDir(dir, MaxRescueListing+1)
	if err != nil {
		return partialLoc{}, fileRefusal("the download folder", err)
	}
	if len(list) > MaxRescueListing {
		return partialLoc{}, coded(CodeRescueAmbiguous, "the download folder holds more than %d entries", MaxRescueListing)
	}
	var found []string
	for _, de := range list {
		if !unconfirmedRE.MatchString(de.Name()) {
			continue
		}
		fi, err := de.Info()
		if err != nil || !fi.Mode().IsRegular() || !provisionalFits(fi, in) {
			continue
		}
		found = append(found, filepath.Join(dir, de.Name()))
	}
	switch len(found) {
	case 0:
		return partialLoc{}, coded(CodeRescueNotFound, "no unfinished browser file for %s", filepath.Base(in.target))
	case 1:
		return partialLoc{partial: found[0]}, nil
	}
	return partialLoc{}, coded(CodeRescueAmbiguous, "%d unfinished browser files could be %s", len(found), filepath.Base(in.target))
}

// provisionalFits reports whether a provisional partial can be in's (R-F2): at least
// max(1, bytes_received) bytes, at most 16 MiB more than a known count, and not older
// than the download's start (less 5 s).
func provisionalFits(fi fs.FileInfo, in rescueInput) bool {
	n := fi.Size()
	switch {
	case n < max(1, in.trusted):
		return false
	case in.trusted > 0 && n > in.trusted+rescueSlack:
		return false
	case !in.startedAt.IsZero() && fi.ModTime().Before(in.startedAt.Add(-rescueClockSlack)):
		return false
	}
	return true
}

// placeholderFor is R-F3: the 0-byte file Firefox keeps at the target while it writes
// <target>.part, when it is not older than the download's start.
func placeholderFor(fsys partialFS, in rescueInput, partial string) string {
	t := in.target
	if t == "" || in.startedAt.IsZero() || !samePath(partial, t+".part") {
		return ""
	}
	fi, err := fsys.Lstat(t)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 || fi.ModTime().Before(in.startedAt.Add(-rescueClockSlack)) {
		return ""
	}
	return t
}

// rescueLocks serialises rescues of one partial path (R-D4).
type rescueLocks struct {
	mu    sync.Mutex
	paths map[string]*sync.Mutex
	refs  map[string]int
}

func (l *rescueLocks) lock(path string) func() {
	key := path
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(path)
	}
	l.mu.Lock()
	if l.paths == nil {
		l.paths, l.refs = map[string]*sync.Mutex{}, map[string]int{}
	}
	mu := l.paths[key]
	if mu == nil {
		mu = new(sync.Mutex)
		l.paths[key] = mu
	}
	l.refs[key]++
	l.mu.Unlock()
	mu.Lock()
	return func() {
		mu.Unlock()
		l.mu.Lock()
		if l.refs[key]--; l.refs[key] == 0 {
			delete(l.paths, key)
			delete(l.refs, key)
		}
		l.mu.Unlock()
	}
}

// Rescue adopts a partial file a browser (or another program) left unfinished and
// continues it (docs/DAEMON-SPEC.md §3 "Rescue"): it finds the partial (R-F), checks
// its folder like Add's dir, has the engine prove and adopt it (engine.Rescue), and
// indexes the new download in its queue, queued or, with start_paused, paused. It is
// never held for confirmation. A rescue stored paused for the browser relay starts with
// ResumeRequest.rescue_start. Refusals carry a "<code>: " message prefix and
// ErrorCodeMeta (rescue_*, link_expired, auth_required, http_status, network, tls,
// disk_full); messages never name the URL or a header.
func (m *Manager) Rescue(ctx context.Context, r *downexv1.RescueRequest) (*downexv1.Download, int64, error) {
	m.mu.Lock()
	closed, s := m.closed, m.settings
	m.mu.Unlock()
	if closed {
		return nil, 0, errShuttingDown()
	}
	in, err := parseRescue(r)
	if err != nil {
		return nil, 0, connect.NewError(connect.CodeInvalidArgument, err)
	}
	queue, err := m.queueForAdd(r.GetQueueId(), model.MainQueueID)
	if err != nil {
		return nil, 0, err
	}
	loc, err := findPartial(osPartialFS{}, in)
	if err != nil {
		var c *rescueCoded
		if !errors.As(err, &c) { // input a client should not send (docs/DAEMON-SPEC.md §4)
			return nil, 0, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, 0, rescueRefusal(ctx, err)
	}
	dir := filepath.Dir(loc.partial)
	if _, err := checkDir(dir, s.DefaultDir); err != nil {
		return nil, 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the partial file's folder: %w", err))
	}
	unlock := m.rescues.lock(loc.partial)
	defer unlock()

	maxConn := in.maxConn
	if maxConn == 0 {
		maxConn = s.DefaultMaxConnections
	}
	var created model.DownloadID
	res, err := m.eng.Rescue(ctx, engine.RescueInput{
		Request: in.req, Partial: loc.partial, Target: in.target, Trusted: in.trusted,
		ExpectedSize: in.expectedSize, Validators: in.validators, Filename: in.filename,
		MaxConnections: maxConn, SpeedLimit: in.speedLimit, Paused: in.startPaused, Placeholder: loc.placeholder,
		Prepare: func(d *model.Download) {
			d.Source, d.ExpectedSize, d.PageURL, d.QueueID = in.source, in.expectedSize, in.pageURL, queue
			d.MaxConnections = min(d.MaxConnections, MaxConnectionsLimit)
			d.Category = chooseCategory(s.Categories, d.Filename, in.req.URL, d.ServedType)
		},
		Created: func(d *model.Download) {
			created = d.ID
			m.mu.Lock()
			defer m.mu.Unlock()
			m.rescuing[d.ID] = struct{}{}
			// A List between the record's creation and this call adopted it: drop that.
			if e := m.entries[d.ID]; e != nil && e.foreign {
				m.leaveQueueLocked(e)
				delete(m.entries, d.ID)
				m.dequeueLocked(d.ID)
			}
		},
	})
	m.mu.Lock()
	if created != 0 {
		delete(m.rescuing, created)
		if err != nil { // the engine deleted the record
			m.removed[created] = struct{}{}
		}
	}
	m.mu.Unlock()
	if err != nil {
		m.log.Info("rescue refused", "url", redactURL(in.req.URL), "partial", filepath.Base(loc.partial),
			"source", in.source.String(), "err", scrubURLs(err.Error()))
		return nil, 0, rescueRefusal(ctx, err)
	}
	d := res.Download
	pd := m.indexAdded(d, false, nil)
	m.mu.Lock()
	if e := m.entries[d.ID]; e != nil && e.run == nil {
		// The kept bytes are durable: a stopped download shows them as received.
		e.received = res.Kept
		if in.startPaused && in.source == model.SourceBrowser {
			e.rescueWait = m.now()
		}
		pd = m.protoLocked(e)
	}
	m.mu.Unlock()
	m.hub.publish(d.ID, kindUpdated)
	m.log.Info("download rescued", "download", int64(d.ID), "url", redactURL(d.Request.URL), "dir", d.Dir,
		"name", d.Filename, "kept", res.Kept, "size", d.Size, "windows", res.Windows,
		"validators_matched", res.ValidatorsMatched, "source", d.Source.String(), "state", d.State.String())
	return pd, res.Kept, nil
}

// rescueErrorCode is the code of a refused rescue (docs/DAEMON-SPEC.md §6).
func rescueErrorCode(err error) string {
	var c *rescueCoded
	if errors.As(err, &c) {
		return c.code
	}
	for _, sc := range []sentinelCode{
		{engine.ErrPartialEmpty, CodeRescueEmpty},
		{engine.ErrPartialLocked, CodeRescueLocked},
		{engine.ErrSizeUnknown, CodeRescueSizeUnknown},
		{engine.ErrLinkNoRanges, CodeRescueNoRanges},
		{engine.ErrLinkMismatch, CodeRescueChanged},
		{engine.ErrLinkSize, CodeRescueChanged},
		{engine.ErrLinkChanged, CodeRescueChanged},
		{engine.ErrLinkUnproven, CodeRescueUnproven},
		{fs.ErrPermission, CodeRescueNoAccess},
		{fs.ErrNotExist, CodeRescueNotFound},
	} {
		if errors.Is(err, sc.err) {
			return sc.code
		}
	}
	return ErrorCode(err)
}

// rescueRefusal maps a failed rescue to its RPC error: InvalidArgument for unusable
// input and rescue_empty, FailedPrecondition with the code for the rest.
func rescueRefusal(ctx context.Context, err error) error {
	var c *rescueCoded
	switch {
	case ctx.Err() != nil && isContextError(err):
		return ctx.Err()
	case isContextError(err):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("checking the partial file with the server took too long"))
	case errors.As(err, &c):
	case errors.Is(err, engine.ErrInvalidRequest):
		return mediaError(CodeInvalidRequest, connect.CodeInvalidArgument, errors.New(scrubURLs(err.Error())))
	}
	code := rescueErrorCode(err)
	switch code {
	case CodeInternal:
		return errInternal("rescue download", errors.New(scrubURLs(err.Error())))
	case CodeRescueEmpty:
		return mediaError(code, connect.CodeInvalidArgument, errors.New(scrubURLs(err.Error())))
	}
	return mediaError(code, connect.CodeFailedPrecondition, errors.New(scrubURLs(err.Error())))
}

// urlInText finds addresses in error texts (a transport error names its request).
var urlInText = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"']*`)

// scrubURLs replaces every address in msg (R-D2: messages never contain the URL).
func scrubURLs(msg string) string { return urlInText.ReplaceAllString(msg, "<address>") }
