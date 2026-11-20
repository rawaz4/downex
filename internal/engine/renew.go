package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"downex/internal/model"
)

// ProveOptions change what ProveLink accepts (docs/ENGINE-SPEC.md §7.3).
type ProveOptions struct {
	// RestartIfUnproven: when too little can be compared (ErrLinkUnproven), the proof's
	// result is Fresh (the renewal downloads again from byte 0) instead of the error.
	RestartIfUnproven bool
	// rand draws the random windows (nil: crypto/rand); tests fix it.
	rand io.Reader
}

// LinkProof is the result of ProveLink: a new link that serves the bytes a download
// already holds (or, with Fresh, a link for a download that has nothing to keep). Renew
// applies it while the download is unchanged.
type LinkProof struct {
	Request     model.Request    // the new request (GET, no body)
	FinalURL    string           // where its first answer came from after redirects ("" when Fresh)
	Validators  model.Validators // the new server's (possibly none)
	RemoteName  string           // the new server's name for the file ("" when Fresh)
	NameDiffers bool             // RemoteName is not the download's name (P5)
	// Fresh: nothing can be spliced (no durable bytes, single mode, no part file, or
	// unproven with RestartIfUnproven): the renewal restarts from byte 0.
	Fresh    bool
	Compared int64 // bytes compared
	// PageURL, when set by the caller, replaces the download's page at Renew.
	PageURL string

	stamp proofStamp
}

// proofStamp identifies the state of the record a proof was made for (P7): Renew refuses
// a record that changed since (ErrLinkStale).
type proofStamp struct {
	size       int64
	validators model.Validators
	durable    int64
	segments   int
	updated    time.Time
	state      model.State
}

func stampOf(d *model.Download, segs []model.Segment) proofStamp {
	st := proofStamp{size: d.Size, validators: d.Validators, segments: len(segs), updated: d.UpdatedAt, state: d.State}
	for _, s := range segs {
		st.durable += max(s.Durable, 0)
	}
	return st
}

// ProveLink checks that req, a new link for download id, serves the same file (P1-P7):
// the same total size, the same validators where they can be compared (same origin),
// and the bytes the part file already holds. It compares every durable byte when there
// are at most FullProofMaxBytes of them in at most FullProofMaxRuns runs, else a sample of
// windows (proofWindows). It never writes anything. It holds the download like Restart
// (ErrAlreadyRunning while it runs). ctx bounds the whole proof.
//
// Refusals: ErrInvalidRequest (a media or completed download, a request other than a
// plain GET, an address that is not http or https), ErrLinkExpired, ErrAuthRequired,
// ErrLinkNoRanges, ErrLinkSize, ErrLinkChanged, ErrLinkMismatch, ErrLinkUnproven, a
// *StatusError, or a transport error.
func (e *Engine) ProveLink(ctx context.Context, id model.DownloadID, req model.Request, o ProveOptions) (LinkProof, error) {
	if err := checkRenewRequest(req); err != nil {
		return LinkProof{}, err
	}
	if err := e.acquire(id, false); err != nil {
		return LinkProof{}, err
	}
	defer e.releaseRun(id)
	d, err := e.st.GetDownload(ctx, id)
	if err != nil {
		return LinkProof{}, fmt.Errorf("load download %d: %w", id, err)
	}
	if err := renewable(d); err != nil {
		return LinkProof{}, err
	}
	segs, err := e.st.LoadSegments(ctx, id)
	if err != nil {
		return LinkProof{}, fmt.Errorf("load segments: %w", err)
	}
	req = renewRequest(req)
	p := LinkProof{Request: req, stamp: stampOf(d, segs)}
	runs := durableRuns(segs)
	if d.Mode != model.ModeRanged || d.Size <= 0 || len(runs) == 0 || d.Filename == "" {
		p.Fresh = true
		return p, nil
	}
	part, err := os.Open(partPath(d.Dir, d.Filename))
	if errors.Is(err, fs.ErrNotExist) {
		p.Fresh = true
		return p, nil
	}
	if err != nil {
		return LinkProof{}, diskError("open part file", err)
	}
	defer part.Close()
	if fi, err := part.Stat(); err != nil || fi.Size() != d.Size {
		p.Fresh = true
		return p, nil
	}
	rnd := o.rand
	if rnd == nil {
		rnd = rand.Reader
	}
	full := runsTotal(runs) <= FullProofMaxBytes && len(runs) <= FullProofMaxRuns
	var windows []byteRun
	if full {
		windows = runs
	} else {
		windows = proofWindows(runs, ProofWindowsUnvouched, rnd)
	}
	f := e.tf.NewSlot()
	defer f.Close()
	fetchReq := requestFor(req)
	var informative int64
	// An index loop: a vouching first answer shortens windows (P2), which a range over the
	// slice would not see.
	for i := 0; i < len(windows); i++ {
		w := windows[i]
		ans, err := fetchWindow(ctx, f, fetchReq, w, d.Size)
		if err != nil {
			return LinkProof{}, err
		}
		v := ans.resp.Validators
		if i == 0 {
			p.FinalURL, p.Validators = ans.resp.FinalURL, v
			p.RemoteName, _ = chooseName(ans.resp, req.URL)
			p.NameDiffers = !nameEqual(p.RemoteName, d.RemoteName, d.Filename)
			if sameOrigin(p.FinalURL, storedOrigin(d)) {
				if err := compareValidators(d.Validators, v); err != nil {
					return LinkProof{}, fmt.Errorf("%w: %w", ErrLinkChanged, err)
				}
			}
			if !full && vouched(d.Validators, v) && len(windows) > ProofWindowsVouched {
				windows = windows[:ProofWindowsVouched]
			}
		} else if v != p.Validators {
			return LinkProof{}, fmt.Errorf("the answers carry different validators: %w", ErrLinkChanged)
		}
		if err := compareWindow(part, w, ans.data); err != nil {
			return LinkProof{}, err
		}
		p.Compared += w.len()
		if !flat(ans.data) {
			informative += w.len()
		}
	}
	// Bytes of one repeated value prove nothing (P7): a sampled proof needs
	// ProofMinInformative bytes outside such windows, a full proof as many, or all of its
	// bytes when it compares fewer (a run of zeros alone is no proof).
	need := int64(ProofMinInformative)
	if full {
		need = min(need, p.Compared)
	}
	if informative < need || informative == 0 {
		if o.RestartIfUnproven {
			p.Fresh = true
			return p, nil
		}
		return LinkProof{}, fmt.Errorf("%d of %d compared bytes tell the file apart: %w", informative, p.Compared, ErrLinkUnproven)
	}
	return p, nil
}

// Renew gives download id the link of proof p (P8) and stores it queued (start) or
// paused. Unless p is Fresh, the durable bytes, the layout, the size, the name and the
// times are kept, and the request, the final URL and the validators are replaced (the old
// entity tag sent as If-Range to a new server would turn every range into a 200). A
// Fresh proof restarts the download from byte 0 with the new request, as Restart does.
// A download changed since the proof (progress, validators, state) is ErrLinkStale.
func (e *Engine) Renew(ctx context.Context, id model.DownloadID, p LinkProof, start bool) error {
	if err := checkRenewRequest(p.Request); err != nil {
		return err
	}
	if err := e.acquire(id, false); err != nil {
		return err
	}
	defer e.releaseRun(id)
	d, err := e.st.GetDownload(ctx, id)
	if err != nil {
		return fmt.Errorf("load download %d: %w", id, err)
	}
	if err := renewable(d); err != nil {
		return err
	}
	segs, err := e.st.LoadSegments(ctx, id)
	if err != nil {
		return fmt.Errorf("load segments: %w", err)
	}
	if stampOf(d, segs) != p.stamp {
		return fmt.Errorf("download %d: %w", id, ErrLinkStale)
	}
	d.Request = renewRequest(p.Request)
	if p.PageURL != "" {
		d.PageURL = p.PageURL
	}
	d.State = model.StatePaused
	if start {
		d.State = model.StateQueued
	}
	d.UpdatedAt = e.clock.Now()
	if !p.Fresh {
		d.FinalURL, d.Validators, d.Error = p.FinalURL, p.Validators, ""
		if err := e.st.UpdateDownload(ctx, d); err != nil {
			return fmt.Errorf("save download: %w", err)
		}
		return nil
	}
	// Restart's reset: the record is saved before the part file is emptied.
	resetRemote(d)
	d.StartedAt, d.ActiveMS = time.Time{}, 0
	if err := e.st.SaveCheckpoint(ctx, d, nil); err != nil {
		return fmt.Errorf("save download: %w", err)
	}
	if d.Filename != "" {
		if err := os.Truncate(partPath(d.Dir, d.Filename), 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.log.Warn("cannot empty the part file of a renewed download", "download", int64(id), "err", err)
		}
	}
	return nil
}

// RenewHeaders are the headers a pasted new link for a download whose stored request is
// stored is sent with when the caller gives none (docs/DAEMON-SPEC.md §3, R2): the
// stored ones, without Authorization, Cookie and Cookie2 when newURL is on another
// origin, as a cross-origin redirect drops them.
func RenewHeaders(stored model.Request, newURL string) http.Header {
	h := stored.Headers.Clone()
	if h != nil && !sameOrigin(stored.URL, newURL) {
		for _, k := range [...]string{"Authorization", "Cookie", "Cookie2"} {
			h.Del(k)
		}
	}
	return h
}

// NameError is ProveLink's result with another name, as a refusal: the new link names
// the file New, the download is Old.
type NameError struct {
	ID       model.DownloadID
	New, Old string
}

func (e *NameError) Error() string {
	return fmt.Sprintf("the new link names the file %q; download %d is %q", e.New, e.ID, e.Old)
}

// renewable refuses the downloads a new link cannot apply to (P1).
func renewable(d *model.Download) error {
	switch {
	case d.Kind != model.KindFile:
		return fmt.Errorf("%w: download %d is not a file download", ErrInvalidRequest, d.ID)
	case d.State == model.StateCompleted:
		return fmt.Errorf("%w: download %d is already completed", ErrInvalidRequest, d.ID)
	}
	return nil
}

// checkRenewRequest accepts a plain GET of an http or https address.
func checkRenewRequest(req model.Request) error {
	if (req.Method != "" && req.Method != http.MethodGet) || len(req.Body) > 0 {
		return fmt.Errorf("%w: only a GET link can continue a download", ErrInvalidRequest)
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: the new link is not an http or https address", ErrInvalidRequest)
	}
	return nil
}

// renewRequest is req as a download stores it: a GET without body, its own headers.
func renewRequest(req model.Request) model.Request {
	req.Method, req.Body = http.MethodGet, nil
	req.Headers = req.Headers.Clone()
	return req
}

// vouched reports a new server whose first answer carries the stored strong entity tag.
func vouched(stored, got model.Validators) bool {
	return stored.StrongETag && got.StrongETag && stored.ETag != "" && stored.ETag == got.ETag
}

// storedOrigin is the address whose origin a new link's validators are compared with:
// the stored final URL, else the stored request's.
func storedOrigin(d *model.Download) string {
	if d.FinalURL != "" {
		return d.FinalURL
	}
	return d.Request.URL
}

// sameOrigin reports whether a and b have the same RFC 6454 origin.
func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Host != "" && originOf(ua) == originOf(ub)
}

// variantRE matches the " (n)" the engine adds before an extension (numbered).
var variantRE = regexp.MustCompile(` \(\d+\)$`)

// StripVariant drops a trailing " (n)" before the extension of name: the name the
// engine numbered to keep files apart ("a (1).zip" → "a.zip").
func StripVariant(name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		return name
	}
	if stripped := variantRE.ReplaceAllString(base, ""); stripped != "" {
		return stripped + ext
	}
	return name
}

// NameKey compares file names as the file system does: case-folded where names are
// case-insensitive by default (Windows, macOS).
func NameKey(name string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(name)
	}
	return name
}

// nameEqual reports whether a new link's name is the download's (P5): its stored
// remote name, else its file name without a variant.
func nameEqual(remote, storedRemote, filename string) bool {
	want := storedRemote
	if want == "" {
		want = StripVariant(filename)
	}
	return NameKey(remote) == NameKey(want)
}
