package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downex/internal/model"
	"downex/internal/platform"
	"downex/internal/transport"
)

// Rescue constants (docs/ENGINE-SPEC.md §7.4).
const (
	// RescueTornStep is how far below a partial file's end the proof tries once more when
	// only its last window differs (a torn tail: the last bytes a browser wrote before it
	// stopped may not have reached the disk).
	RescueTornStep = 4 << 20
	// rescueHoleBlock: a possibly sparse partial file (larger than the browser's count of
	// good bytes) is trusted only up to its first aligned block of this many zero bytes.
	rescueHoleBlock = ProofWindowBytes
	// rescueAttempts bounds the requests for one window (transient failures are retried).
	rescueAttempts = 3
)

// rescueSuffixes are the endings of the partial files browsers write (§7.4 A2), in lower
// case.
var rescueSuffixes = [...]string{".crdownload", ".part"}

// unconfirmedPrefix starts the provisional name Chromium gives a download before its
// name is settled ("Unconfirmed 123.crdownload").
const unconfirmedPrefix = "Unconfirmed "

// RescueInput is what Rescue adopts: a partial file another program (a browser) left
// unfinished and the request that continues it (docs/ENGINE-SPEC.md §7.4).
type RescueInput struct {
	Request model.Request // a GET of an http or https address, without body
	Partial string        // absolute path of the partial file
	// Target is the browser's final path for the file ("" = none): its base name names a
	// partial whose own name is provisional (A2).
	Target string
	// Trusted is the browser's count of good bytes (0 = unknown). Bytes of the partial
	// beyond it are discarded, and a partial larger than it may hold holes (V2).
	Trusted int64
	// ExpectedSize is the browser's total (≤ 0 = unknown): the server's must equal it.
	ExpectedSize int64
	// Validators are the original response's (zero = unknown): the server's must not
	// differ (V4).
	Validators model.Validators
	// Filename is the final name ("" = from the partial's name, A2).
	Filename       string
	MaxConnections int // ≤ 0 = the engine default
	SpeedLimit     int64
	Paused         bool // store it paused instead of queued
	// Placeholder is the 0-byte file a browser made at the final path (A3; "" = none):
	// removed before the name is reserved while it is still a regular file of 0 bytes.
	Placeholder string
	// Prepare, when set, completes the new record before it is created (source,
	// category, queue, hints). It must not change the fields Rescue sets.
	Prepare func(d *model.Download)
	// Created, when set, is called once the record exists (state PROBING, the id held by
	// Rescue until it returns): a caller indexes it without running it.
	Created func(d *model.Download)

	rand         io.Reader // random windows (nil: crypto/rand); tests fix it
	beforeRename func()    // tests: runs just before the stability check and the rename
}

// RescueResult is a rescued download.
type RescueResult struct {
	Download *model.Download // as stored: ranged, queued or paused
	Kept     int64           // bytes kept from the partial file
	Windows  int             // windows compared with the server
	Compared int64           // bytes compared
	// ValidatorsMatched: the server sent a validator the input knew, with the same value.
	ValidatorsMatched bool
}

// rescueProof is a successful verification (V1-V8).
type rescueProof struct {
	kept        int64 // the trusted length L
	size        int64 // the server's total
	validators  model.Validators
	finalURL    string
	remoteName  string
	controls    bool
	contentType string
	windows     int
	compared    int64
	matched     bool
}

// Rescue proves that in.Request serves the file whose first bytes in.Partial holds, then
// adopts the partial file as a new ranged download that continues from them (§7.4,
// V1-V8 and A1-A8). Nothing on disk changes before the proof succeeds: the partial is
// then renamed (never copied) into the download's part file in its own folder. A
// failure after that renames it back where its path is free and deletes the record.
//
// Refusals: ErrInvalidRequest (a request other than a plain http(s) GET, a partial that
// is not an absolute path of a regular file), ErrPartialEmpty, ErrPartialLocked, the
// proof's ErrLinkNoRanges (ErrSizeUnknown with it), ErrLinkSize, ErrLinkChanged,
// ErrLinkMismatch and ErrLinkUnproven, ErrLinkExpired, ErrAuthRequired, a *StatusError,
// a transport error, or a file error (fs.ErrNotExist, fs.ErrPermission, ErrDiskFull).
func (e *Engine) Rescue(ctx context.Context, in RescueInput) (*RescueResult, error) {
	if err := checkRenewRequest(in.Request); err != nil {
		return nil, err
	}
	req := renewRequest(in.Request)
	partial := filepath.Clean(in.Partial)
	if !filepath.IsAbs(in.Partial) || strings.ContainsRune(in.Partial, 0) {
		return nil, fmt.Errorf("%w: the partial file's path is not absolute", ErrInvalidRequest)
	}
	before, err := lstatRegular(partial)
	if err != nil {
		return nil, err
	}
	if err := e.notOwnFile(ctx, partial); err != nil {
		return nil, err
	}
	f, err := os.Open(partial)
	if err != nil {
		return nil, fmt.Errorf("open the partial file: %w", err)
	}
	kept, sparse := trustedLength(before.Size(), in.Trusted)
	if sparse && kept > 0 {
		if kept, err = holeFree(ctx, f, kept); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if kept == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(partial), ErrPartialEmpty)
	}
	rnd := in.rand
	if rnd == nil {
		rnd = rand.Reader
	}
	proof, err := e.verifyPartial(ctx, f, requestFor(req), kept, sparse, in, rnd)
	// The handle is closed before the rename: Windows refuses to rename an open file.
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	if in.beforeRename != nil {
		in.beforeRename()
	}
	return e.adopt(ctx, in, req, partial, before, proof)
}

// lstatRegular returns the file information of path when it is a regular file (not a
// link, a folder, a device or a Windows reparse point).
func lstatRegular(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("the partial file: %w", err)
	}
	if !fi.Mode().IsRegular() || isReparsePoint(fi) {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrInvalidRequest, filepath.Base(path))
	}
	return fi, nil
}

// notOwnFile refuses a partial that a stored download names, as its file or as its part
// file (A1): adopting it would take the file from under that download (a Downex download
// picked by mistake in the window or the CLI). ErrInvalidRequest names the download.
func (e *Engine) notOwnFile(ctx context.Context, partial string) error {
	all, err := e.st.ListDownloads(ctx)
	if err != nil {
		return fmt.Errorf("list downloads: %w", err)
	}
	dir, base := nameKey(filepath.Dir(partial)), nameKey(filepath.Base(partial))
	for _, d := range all {
		if d.Filename == "" || nameKey(d.Dir) != dir {
			continue
		}
		if name := nameKey(d.Filename); name == base || name+partExt == base {
			return fmt.Errorf("%w: %s belongs to download %d; resume that download instead", ErrInvalidRequest,
				filepath.Base(partial), int64(d.ID))
		}
	}
	return nil
}

// trustedLength is V2: the bytes of a partial file of size bytes that may be kept, given
// the browser's count of good bytes (0 = unknown), and whether the file is larger than
// that count, so that it may hold holes (a browser that writes slices at several offsets
// counts only what it received).
func trustedLength(size, trusted int64) (kept int64, sparse bool) {
	if trusted > 0 && trusted < size {
		return trusted, true
	}
	return max(size, 0), false
}

// holeFree returns how many of the first n bytes of r lie before its first aligned
// block of rescueHoleBlock zero bytes: the data before a hole a sparse partial left.
func holeFree(ctx context.Context, r io.ReaderAt, n int64) (int64, error) {
	buf := make([]byte, rescueHoleBlock)
	for off := int64(0); off < n; off += rescueHoleBlock {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		k := min(int64(rescueHoleBlock), n-off)
		if _, err := r.ReadAt(buf[:k], off); err != nil {
			return 0, fmt.Errorf("read the partial file at %d: %w", off, err)
		}
		if k == rescueHoleBlock && allZero(buf) {
			return off, nil
		}
	}
	return n, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// rescueWindows returns the windows compared for a trusted length of kept bytes: all of
// them in one window up to FullProofMaxBytes, else proofWindows over [0, kept) (the last
// window first, then the first, then random ones).
func rescueWindows(kept int64, rnd io.Reader) (windows []byteRun, full bool) {
	run := byteRun{0, kept - 1}
	if kept <= FullProofMaxBytes {
		return []byteRun{run}, true
	}
	return proofWindows([]byteRun{run}, ProofWindowsUnvouched, rnd), false
}

// tornTail marks a mismatch in the first window of a sampled proof only (V5).
type tornTail struct{ err error }

func (t *tornTail) Error() string { return t.err.Error() }
func (t *tornTail) Unwrap() error { return t.err }

// verifyPartial is V1-V8 for the first kept bytes of part: it proves them once, and
// when only the last window differs once more with kept lowered to below a torn tail.
func (e *Engine) verifyPartial(ctx context.Context, part io.ReaderAt, req model.Request, kept int64, sparse bool,
	in RescueInput, rnd io.Reader) (rescueProof, error) {
	f := e.tf.NewSlot()
	defer f.Close()
	p, err := e.provePrefix(ctx, f, req, part, kept, sparse, in, rnd)
	var torn *tornTail
	if errors.As(err, &torn) {
		lower := (kept - RescueTornStep) / proofAlign * proofAlign
		if lower < ProofWindowBytes {
			return rescueProof{}, torn.err
		}
		e.log.Info("the partial file's last bytes differ; checking it without them", "kept", lower)
		p, err = e.provePrefix(ctx, f, req, part, lower, sparse, in, rnd)
		if errors.As(err, &torn) {
			err = torn.err
		}
	}
	return p, err
}

// provePrefix compares the first kept bytes of part with what req serves (V3-V6).
func (e *Engine) provePrefix(ctx context.Context, f transport.Fetcher, req model.Request, part io.ReaderAt, kept int64,
	sparse bool, in RescueInput, rnd io.Reader) (rescueProof, error) {
	windows, full := rescueWindows(kept, rnd)
	size := int64(-1)
	if in.ExpectedSize > 0 {
		size = in.ExpectedSize
	}
	p := rescueProof{kept: kept}
	var informative int64
	for i := 0; i < len(windows); i++ { // an index loop: a vouching answer shortens windows
		w := windows[i]
		ans, err := e.fetchWindowRetry(ctx, f, req, w, size)
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) && se.Code == http.StatusRequestedRangeNotSatisfiable {
				return rescueProof{}, fmt.Errorf("bytes %s are beyond the server's file: %w", w, ErrLinkSize)
			}
			return rescueProof{}, err
		}
		v := ans.resp.Validators
		if i == 0 {
			size = ans.resp.ContentRange.Total
			p.size, p.validators, p.finalURL = size, v, ans.resp.FinalURL
			p.remoteName, p.controls = chooseName(ans.resp, req.URL)
			p.contentType = ans.resp.ContentType
			if err := compareValidators(in.Validators, v); err != nil {
				return rescueProof{}, fmt.Errorf("%w: %w", ErrLinkChanged, err)
			}
			p.matched = validatorsMatch(in.Validators, v)
			if !full && !sparse && vouched(in.Validators, v) && len(windows) > ProofWindowsVouched {
				windows = windows[:ProofWindowsVouched]
			}
		} else if v != p.validators {
			return rescueProof{}, fmt.Errorf("the answers carry different validators: %w", ErrLinkChanged)
		}
		if err := compareWindow(part, w, ans.data); err != nil {
			if i == 0 && !full {
				return rescueProof{}, &tornTail{err}
			}
			return rescueProof{}, err
		}
		p.windows++
		p.compared += w.len()
		if !flat(ans.data) {
			informative += w.len()
		}
	}
	// Bytes of one repeated value prove nothing (the rule of ProveLink, §7.3 P7).
	need := int64(ProofMinInformative)
	if full {
		need = min(need, p.compared)
	}
	if informative < need || informative == 0 {
		return rescueProof{}, fmt.Errorf("%d of %d compared bytes tell the file apart: %w", informative, p.compared, ErrLinkUnproven)
	}
	return p, nil
}

// fetchWindowRetry is fetchWindow with transient failures retried (V1), at most
// rescueAttempts requests, after the engine's back-off.
func (e *Engine) fetchWindowRetry(ctx context.Context, f transport.Fetcher, req model.Request, w byteRun, size int64) (proofAnswer, error) {
	for attempt := 1; ; attempt++ {
		ans, err := fetchWindow(ctx, f, req, w, size)
		if err == nil || attempt >= rescueAttempts || ctx.Err() != nil || !isTransient(err) {
			return ans, err
		}
		if isMisdirected(err) {
			f.Close()
		}
		if serr := sleep(ctx, e.clock, backoffDelay(&e.opts, attempt, retryAfterOf(err))); serr != nil {
			return proofAnswer{}, serr
		}
	}
}

// validatorsMatch reports whether got repeats a validator known carries (V4): the same
// opaque entity tag, or the same Last-Modified date.
func validatorsMatch(known, got model.Validators) bool {
	return (known.ETag != "" && got.ETag != "" && opaqueTag(known.ETag) == opaqueTag(got.ETag)) ||
		(known.LastModified != "" && got.LastModified != "" && sameHTTPDate(known.LastModified, got.LastModified))
}

// RescueName is the name a rescued download is saved under (§7.4 A2): filename when
// given, else the partial's base name without ".crdownload" or ".part"; a provisional
// "Unconfirmed …" name, or one that is empty without its suffix, gives way to target's
// base name, else to remote (the server's name; "" when it is not known yet). The result
// is sanitised; "" means the server names it.
func RescueName(filename, partial, target, remote string) string {
	raw := rescueRawName(filename, partial, target, remote)
	if raw == "" {
		return ""
	}
	return transport.SanitizeFilename(raw)
}

// rescueRawName is RescueName before sanitising.
func rescueRawName(filename, partial, target, remote string) string {
	if filename != "" {
		return filename
	}
	if base, ok := partialBase(partial); ok {
		return base
	}
	if target != "" {
		if base := filepath.Base(target); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	return remote
}

// partialBase is the base name of partial without a browser's suffix, and false when
// that is empty or provisional ("Unconfirmed 123.crdownload").
func partialBase(partial string) (string, bool) {
	base := filepath.Base(partial)
	lower := strings.ToLower(base)
	for _, suf := range rescueSuffixes {
		if strings.HasSuffix(lower, suf) {
			base = base[:len(base)-len(suf)]
			break
		}
	}
	return base, base != "" && !strings.HasPrefix(base, unconfirmedPrefix)
}

// rescueNameControls reports whether the name a rescue chose held hidden text-direction
// characters before sanitising (docs/DESKTOP-SPEC.md §9.5 N1): the requested name, else
// the partial's, else (a provisional or empty one) the target's or the server's.
func rescueNameControls(in RescueInput, partial string, p rescueProof) bool {
	raw := rescueRawName(in.Filename, partial, in.Target, "")
	if raw == "" {
		return p.controls
	}
	return transport.HiddenControls(raw)
}

// adopt is A1-A7: reserve the name in the partial's folder, create the record, rename
// the partial into its part file, size it, and commit the checkpoint.
func (e *Engine) adopt(ctx context.Context, in RescueInput, req model.Request, partial string, before os.FileInfo,
	p rescueProof) (*RescueResult, error) {
	dir := filepath.Dir(partial)
	name := RescueName(in.Filename, partial, in.Target, p.remoteName)
	if name == "" {
		name = transport.SanitizeFilename("")
	}
	if ph := in.Placeholder; ph != "" && filepath.Dir(filepath.Clean(ph)) == dir {
		if fi, err := os.Lstat(ph); err == nil && fi.Mode().IsRegular() && !isReparsePoint(fi) && fi.Size() == 0 {
			if err := os.Remove(ph); err != nil {
				e.log.Warn("cannot remove the browser's empty placeholder; the rescued file gets another name", "err", err)
			}
		}
	}
	// (a) the name, with an empty part file made with O_EXCL.
	reserved, err := e.reserve(ctx, dir, name, 0, false)
	if err != nil {
		return nil, err
	}
	part := partPath(dir, reserved)
	sctx := context.WithoutCancel(ctx)
	// (b) the record, PROBING until the commit: a crash before (e) downloads it again
	// from byte 0 (A6).
	now := e.clock.Now()
	d := &model.Download{
		Request: req, Dir: dir, Filename: reserved, Size: -1, Mode: model.ModeUnknown, State: model.StateProbing,
		MaxConnections: min(max(in.MaxConnections, 0), e.opts.HardCap), SpeedLimit: max(in.SpeedLimit, 0),
		CreatedAt: now, UpdatedAt: now, RemoteName: p.remoteName, ServedType: servedType(p.contentType),
		NameControls: rescueNameControls(in, partial, p),
	}
	if in.Prepare != nil {
		in.Prepare(d)
	}
	if err := e.st.CreateDownload(sctx, d); err != nil {
		_ = os.Remove(part)
		return nil, fmt.Errorf("create download: %w", err)
	}
	if err := e.acquire(d.ID, false); err != nil {
		e.dropRescue(sctx, d, part, "", 0)
		return nil, err
	}
	defer e.releaseRun(d.ID)
	if in.Created != nil {
		in.Created(d)
	}
	// (c) the rename, once the partial is as it was when it was checked (V7).
	after, err := lstatRegular(partial)
	switch {
	case err != nil:
		e.dropRescue(sctx, d, part, "", 0)
		return nil, err
	case after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()):
		e.dropRescue(sctx, d, part, "", 0)
		return nil, fmt.Errorf("%s changed while it was checked: %w", filepath.Base(partial), ErrPartialLocked)
	}
	if err := e.retrySharing(ctx, partial, func() error { return renameReplace(partial, part) }); err != nil {
		e.dropRescue(sctx, d, part, "", 0)
		if isSharingViolation(err) && !errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("%s: %w: %w", filepath.Base(partial), ErrPartialLocked, err)
		}
		return nil, fmt.Errorf("move the partial file: %w", err)
	}
	syncDir(dir)
	// (d) the kept bytes, then the file's full size.
	if err := sizePart(part, p.kept, p.size); err != nil {
		e.dropRescue(sctx, d, part, partial, p.kept)
		return nil, err
	}
	// (e) the commit.
	d.Mode, d.Size, d.Validators, d.FinalURL = model.ModeRanged, p.size, p.validators, p.finalURL
	d.RescuedBytes = p.kept
	d.State = model.StateQueued
	if in.Paused {
		d.State = model.StatePaused
	}
	d.UpdatedAt = e.clock.Now()
	segs := []model.Segment{{Start: 0, End: p.size - 1, Durable: p.kept}}
	if err := e.st.SaveCheckpoint(sctx, d, segs); err != nil {
		e.dropRescue(sctx, d, part, partial, p.kept)
		return nil, fmt.Errorf("save checkpoint: %w", err)
	}
	return &RescueResult{Download: d, Kept: p.kept, Windows: p.windows, Compared: p.compared, ValidatorsMatched: p.matched}, nil
}

// sizePart cuts the part file at path to its kept bytes and extends it to size (A4 d),
// durably.
func sizePart(path string, kept, size int64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return diskError("open part file", err)
	}
	err = f.Truncate(kept)
	if err == nil {
		err = platform.Preallocate(f, size)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return diskError("size part file", err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// dropRescue undoes an adoption that failed after its record was created (A5): the
// part file goes back to the partial's path (cut to its kept bytes) when partial is set
// and that path is free, else it is removed while it holds nothing of the partial; the
// record is deleted.
func (e *Engine) dropRescue(ctx context.Context, d *model.Download, part, partial string, kept int64) {
	switch {
	case partial == "":
		_ = os.Remove(part)
	default:
		if err := renameNoReplace(part, partial); err != nil {
			e.log.Warn("cannot move a rescued partial file back; it stays as the part file", "err", err)
		} else {
			if err := os.Truncate(partial, kept); err != nil {
				e.log.Warn("cannot cut a rescued partial file back", "err", err)
			}
			syncDir(filepath.Dir(partial))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := e.st.DeleteDownload(ctx, d.ID); err != nil {
		e.log.Warn("cannot delete the record of a failed rescue", "download", int64(d.ID), "err", err)
	}
}
