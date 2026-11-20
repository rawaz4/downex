package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/sitemedia"
	"downex/internal/store"
	"downex/internal/transport"
)

// Stable machine-readable failure codes reported in Download.error_code
// (docs/DAEMON-SPEC.md §6). Clients may rely on these strings.
const (
	CodeValidatorChanged = "validator_changed" // saved progress is unusable (remote file changed, or progress inconsistent): resume with restart
	CodeDiskFull         = "disk_full"         // the target volume is full
	CodeTLS              = "tls"               // certificate verification failed or a downgrade was refused
	CodeHTTPStatus       = "http_status"       // the server answered with an unusable status or response
	CodeNetwork          = "network"           // connection, DNS, timeout or truncated transfer
	CodeInvalidRequest   = "invalid_request"   // the stored request cannot be sent
	CodeStalled          = "stalled"           // a single-connection transfer made no progress
	CodeInternal         = "internal"          // anything else (store or file system errors, bugs)
	// CodeAuthRequired: the server (401) or proxy (407) wants credentials that neither the
	// URL nor a site login (docs/M6-SPEC.md §2) gave, or it rejected them.
	CodeAuthRequired = "auth_required"
	// CodeLinkExpired: the link stopped working (expired, revoked, or it now leads to a web
	// page; docs/ENGINE-SPEC.md §7.2). The data received so far is kept: RenewLink gives
	// the download a new link.
	CodeLinkExpired = "link_expired"

	// Refusals of RenewLink (docs/ENGINE-SPEC.md §7.3), never stored on a download: the
	// message starts with the code and ErrorCodeMeta carries it. RenewLink also refuses
	// with link_expired, auth_required, http_status, network and tls.
	CodeRenewMismatch = "renew_mismatch"  // the new link serves other bytes
	CodeRenewSize     = "renew_size"      // the new link's file has another size
	CodeRenewChanged  = "renew_changed"   // the new link's server reports another version
	CodeRenewNoRanges = "renew_no_ranges" // the new link's server sends only whole files
	CodeRenewUnproven = "renew_unproven"  // too little can be compared (restart_if_unproven)
	CodeRenewName     = "renew_name"      // the new link names the file differently (allow_name_change)

	// Refusals of Rescue (docs/ENGINE-SPEC.md §7.4, docs/DAEMON-SPEC.md §3 "Rescue"), never
	// stored on a download: the message starts with the code and ErrorCodeMeta carries it.
	// Rescue also refuses with link_expired, auth_required, http_status, network, tls and
	// disk_full. rescue_empty is InvalidArgument, the others FailedPrecondition.
	CodeRescueNoRanges    = "rescue_no_ranges"    // the server sends only whole files
	CodeRescueSizeUnknown = "rescue_size_unknown" // the server does not say how big the file is
	CodeRescueChanged     = "rescue_changed"      // the server's file is not the one the partial holds
	CodeRescueUnproven    = "rescue_unproven"     // the partial holds too little that tells files apart
	CodeRescueNotFound    = "rescue_not_found"    // the partial file is gone
	CodeRescueAmbiguous   = "rescue_ambiguous"    // more than one unfinished browser file could be it
	CodeRescueLocked      = "rescue_locked"       // another program still writes or holds the partial
	CodeRescueNoAccess    = "rescue_no_access"    // downexd may not open the partial (macOS: privacy settings)
	CodeRescueEmpty       = "rescue_empty"        // the partial file holds nothing to keep

	// Media downloads (docs/MEDIA-SPEC.md §2, §6); also the codes of refused AddMedia and
	// ProbeMedia calls (see ErrorCodeMeta).
	CodeEncrypted        = "encrypted"         // encrypted or DRM-protected content, which Downex never downloads
	CodeLive             = "live"              // a live presentation (it has no end to download)
	CodeMediaUnsupported = "media_unsupported" // a stream or track combination that cannot be saved yet
	CodeMediaInvalid     = "media_invalid"     // a malformed or oversized manifest, or a track it does not have
	CodeClipInvalid      = "clip_invalid"      // a part of a video that cannot be saved (docs/MEDIA-SPEC.md §8 C2)

	// Site media (docs/SITE-MEDIA-SPEC.md §3.2): video pages resolved with yt-dlp. They
	// also use encrypted, live, media_unsupported, media_invalid and auth_required.
	CodeSiteUnsupported  = "site_unsupported"  // yt-dlp found no video on the page
	CodeSiteUnavailable  = "site_unavailable"  // private, removed, geo-blocked, or the site asks to try again later
	CodeSiteError        = "site_error"        // yt-dlp failed otherwise
	CodeSiteResolving    = "site_resolving"    // retry: the extraction still runs (its result is cached)
	CodeToolsInstalling  = "tools_installing"  // retry: yt-dlp and Deno are being downloaded
	CodeToolsUnavailable = "tools_unavailable" // yt-dlp/Deno cannot be set up, or this daemon never downloads them
	CodeSiteMediaOff     = "site_media_off"    // Settings.site_media is off
	// CodeNotFoundYet: no file of the clicked video was played yet (docs/SITE-MEDIA-SPEC.md
	// §2.9); the extension keeps waiting for it.
	CodeNotFoundYet = "not_found_yet"
)

// sentinelCode is the code of the errors that wrap err.
type sentinelCode struct {
	err  error
	code string
}

// siteCodes maps the site-media sentinels (internal/sitemedia) to their codes.
var siteCodes = []sentinelCode{
	{sitemedia.ErrSiteUnsupported, CodeSiteUnsupported},
	{sitemedia.ErrSiteUnavailable, CodeSiteUnavailable},
	{sitemedia.ErrAuthRequired, CodeAuthRequired},
	{sitemedia.ErrSiteError, CodeSiteError},
	{sitemedia.ErrSiteResolving, CodeSiteResolving},
	{sitemedia.ErrToolsInstalling, CodeToolsInstalling},
	{sitemedia.ErrToolsUnavailable, CodeToolsUnavailable},
	{sitemedia.ErrSiteMediaOff, CodeSiteMediaOff},
}

// runErrorCode maps the error of a run of a download of kind k to its stable code:
// mediaErrorCode for media downloads, ErrorCode otherwise.
func runErrorCode(k model.Kind, err error) string {
	if k == model.KindMedia {
		return mediaErrorCode(err)
	}
	return ErrorCode(err)
}

// mediaErrorCode maps an error of a media job (or of its probe) to its stable code:
// the media codes for internal/media's refusals and unsupported streams, else
// ErrorCode's. It returns "" for nil.
func mediaErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, media.ErrEncrypted):
		return CodeEncrypted
	case errors.Is(err, media.ErrLive):
		return CodeLive
	case errors.Is(err, media.ErrClip):
		return CodeClipInvalid
	case errors.Is(err, ErrMediaUnsupported), errors.Is(err, errors.ErrUnsupported):
		return CodeMediaUnsupported
	case errors.Is(err, media.ErrMalformed), errors.Is(err, media.ErrTooLarge), errors.Is(err, media.ErrNotFound):
		return CodeMediaInvalid
	}
	for _, c := range siteCodes {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	if code := ErrorCode(err); code != CodeInternal {
		return code
	}
	// The job's own errors (an HTTP status, a truncated response, ...) are known by
	// their text: the daemon depends on the job only through MediaJobs.
	return mediaCodeFromMessage(err.Error())
}

// mediaMessageCodes maps texts of media-job failures that no sentinel of the daemon's
// dependencies identifies (the job's own errors) to codes.
var mediaMessageCodes = []struct{ text, code string }{
	{"invalid track selection", CodeMediaInvalid},
	{"not an HLS playlist or DASH MPD", CodeMediaInvalid},
	{"remux: malformed input", CodeMediaInvalid}, // fragments that are not what their container says
	{"different byte range", CodeHTTPStatus},
	{"response ended early", CodeNetwork},
	{"mediajob: disk full", CodeDiskFull},                 // mediajob.ErrDiskFull: the free-space check, ENOSPC
	{errors.ErrUnsupported.Error(), CodeMediaUnsupported}, // "unsupported operation" (the assembler)
	// mediajob.ErrContentCoded: coded bytes where plain ones were asked for (after the
	// unsupported coding of a manifest, which matches the entry above).
	{"response has a content coding", CodeHTTPStatus},
}

// mediaCodeFromMessage is codeFromMessage for a media download's failure.
func mediaCodeFromMessage(msg string) string {
	code := codeFromMessage(msg)
	if code != CodeInternal {
		return code
	}
	for _, c := range mediaMessageCodes {
		if strings.Contains(msg, c.text) {
			return c.code
		}
	}
	return code
}

// ErrorCode maps an error returned by engine.Run to its stable code. It returns ""
// for nil.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var status *engine.StatusError
	switch {
	case errors.Is(err, engine.ErrLinkExpired):
		return CodeLinkExpired
	case errors.Is(err, engine.ErrAuthRequired):
		return CodeAuthRequired
	case errors.Is(err, engine.ErrValidatorChanged), errors.Is(err, engine.ErrCorruptCheckpoint):
		// Both mean the same to a client: only Resume with restart can continue.
		return CodeValidatorChanged
	case errors.Is(err, engine.ErrDiskFull):
		return CodeDiskFull
	case errors.Is(err, engine.ErrTLS), errors.Is(err, transport.ErrInsecureRedirect):
		return CodeTLS
	case errors.Is(err, engine.ErrStalled):
		return CodeStalled
	case errors.Is(err, engine.ErrInvalidRequest), errors.Is(err, transport.ErrInvalidRequest):
		return CodeInvalidRequest
	case errors.As(err, &status), errors.Is(err, engine.ErrRangeIgnored),
		errors.Is(err, transport.ErrTooLarge), errors.Is(err, transport.ErrRedirects):
		return CodeHTTPStatus
	case isNetworkError(err):
		return CodeNetwork
	}
	return CodeInternal
}

// isNetworkError reports connection-level failures: errors of the net and net/http
// client stack (which implement net.Error) and truncated transfers. File system and
// store errors are not network errors, even though the errno a file error carries
// (syscall.Errno) satisfies net.Error too: an errno counts only inside a *net.OpError.
func isNetworkError(err error) bool {
	var (
		ne    net.Error
		path  *fs.PathError
		link  *os.LinkError
		op    *net.OpError
		errno syscall.Errno
	)
	switch {
	case errors.As(err, &path), errors.As(err, &link),
		errors.Is(err, store.ErrClosed), errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNotFound):
		return false
	case errors.As(err, &ne):
		return !errors.As(ne, &errno) || errors.As(err, &op)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return true
	}
	// HTTP/2 stream and connection errors surface from body reads without a net.Error.
	msg := err.Error()
	return strings.Contains(msg, "http2:") || strings.Contains(msg, "stream error")
}

// recordErrorCode derives the error code of the failure stored in d (codeFromMessage,
// or mediaCodeFromMessage for a media download).
func recordErrorCode(d *model.Download) string {
	if d.Kind == model.KindMedia {
		return mediaCodeFromMessage(d.Error)
	}
	return codeFromMessage(d.Error)
}

// codeFromMessage derives the error code of a failure known only by its persisted
// message (a download that failed before this daemon started, or in another process).
// The engine's sentinel errors are always wrapped with %w, so their text is part of
// the message.
func codeFromMessage(msg string) string {
	if msg == "" {
		return ""
	}
	// A site-media failure names its cause first: its reason (yt-dlp's words) may
	// contain any other text.
	for _, c := range siteCodes {
		if strings.Contains(msg, c.err.Error()) {
			return c.code
		}
	}
	for _, c := range []struct {
		err  error
		code string
	}{
		{engine.ErrLinkExpired, CodeLinkExpired},
		{transport.ErrAuthRequired, CodeAuthRequired},
		{media.ErrEncrypted, CodeEncrypted},
		{media.ErrLive, CodeLive},
		{ErrMediaUnsupported, CodeMediaUnsupported},
		{media.ErrUnsupported, CodeMediaUnsupported},
		{media.ErrMalformed, CodeMediaInvalid},
		{media.ErrTooLarge, CodeMediaInvalid},
		{media.ErrNotFound, CodeMediaInvalid},
		{engine.ErrValidatorChanged, CodeValidatorChanged},
		{engine.ErrCorruptCheckpoint, CodeValidatorChanged},
		{engine.ErrDiskFull, CodeDiskFull},
		{engine.ErrTLS, CodeTLS},
		{transport.ErrInsecureRedirect, CodeTLS},
		{engine.ErrStalled, CodeStalled},
		{engine.ErrInvalidRequest, CodeInvalidRequest},
		{transport.ErrInvalidRequest, CodeInvalidRequest},
		{engine.ErrRangeIgnored, CodeHTTPStatus},
		{transport.ErrTooLarge, CodeHTTPStatus},
		{transport.ErrRedirects, CodeHTTPStatus},
	} {
		if strings.Contains(msg, c.err.Error()) {
			return c.code
		}
	}
	if strings.Contains(msg, "unexpected HTTP status") {
		return CodeHTTPStatus
	}
	for _, s := range []string{
		"connection refused", "connection reset", "no such host", "i/o timeout", "network is unreachable",
		"timeout awaiting", "EOF", "http2:", "stream error", "broken pipe", "forcibly closed",
	} {
		if strings.Contains(msg, s) {
			return CodeNetwork
		}
	}
	return CodeInternal
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// RPC errors (docs/DAEMON-SPEC.md §6). Messages never echo header values or bodies.

func errNotFound(id model.DownloadID) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", id))
}

func errInvalid(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

func errPrecondition(format string, args ...any) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(format, args...))
}

// errAwaiting refuses an operation that would start or pause a download awaiting
// confirmation: only Confirm and Remove apply to it.
func errAwaiting(id model.DownloadID) error {
	return errPrecondition("download %d is awaiting confirmation; confirm it or remove it", id)
}

func errShuttingDown() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("downexd is shutting down"))
}

func errInternal(op string, err error) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", op, err))
}

// errAdd maps a failure of engine.Add: unusable input is InvalidArgument, a target
// directory that cannot be written is FailedPrecondition, anything else Internal.
func errAdd(err error) error {
	var path *fs.PathError
	switch {
	case errors.Is(err, engine.ErrInvalidRequest), errors.Is(err, store.ErrQueueNotFound):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, engine.ErrDiskFull), errors.As(err, &path):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case isContextError(err):
		return err
	}
	return errInternal("add download", err)
}
