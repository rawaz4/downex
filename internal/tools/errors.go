package tools

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"downex/internal/sitemedia"
)

// Error codes of a tool's last failure (ToolInfo.error_code, docs/SITE-MEDIA-SPEC.md
// §3.1).
const (
	CodeNetwork             = "network"
	CodeTLS                 = "tls"
	CodeHTTPStatus          = "http_status"
	CodeChecksum            = "checksum"
	CodeSignature           = "signature"
	CodeAuthenticode        = "authenticode"
	CodeDiskFull            = "disk_full"
	CodeUnsupportedPlatform = "unsupported_platform"
	CodeSmokeTest           = "smoke_test"
	CodeInternal            = "internal"
)

// Causes wrapped by Error (test with errors.Is). Their texts never contain URLs.
var (
	// ErrNoDownloads: the manager has no HTTP client, so it never downloads tools (a
	// daemon other than the user's own).
	ErrNoDownloads = errors.New("this Downex daemon does not download tools")
	// ErrClosed: the manager was closed.
	ErrClosed = errors.New("the tools manager is closed")
	// ErrSignature: the OpenPGP signature of yt-dlp's checksum list is malformed, does
	// not verify with a pinned key, or is older than the installed copy's.
	ErrSignature = errors.New("the signature does not verify")
	// ErrChecksum: a checksum file is malformed, or a download does not have its
	// published SHA-256.
	ErrChecksum = errors.New("the checksum does not match")
	// ErrArchive: a Deno archive does not hold exactly one executable at its root, an
	// onedir yt-dlp archive has an entry that is not a plain file or folder inside it or
	// no single executable at its root, or an archive unpacks to more than its limit.
	ErrArchive = errors.New("unexpected archive layout")
	// ErrTooLarge: a download is larger than its limit.
	ErrTooLarge = errors.New("the download is larger than its limit")
	// ErrNotFound: an optional release file does not exist (HTTP 404).
	ErrNotFound = errors.New("not published")
	// ErrBadRelease: the latest release's tag is malformed, older than the installed copy
	// or marked unsuitable.
	ErrBadRelease = errors.New("unusable release")
	// ErrInUse: a version folder with other contents is held by a running extraction.
	ErrInUse = errors.New("the version folder is in use")
)

// maxErrorText bounds Error's text (ToolInfo.error).
const maxErrorText = 200

// Error is the failure of one tool's download, verification or install. Its text is the
// short message users read (docs/SITE-MEDIA-SPEC.md §4.6), at most 200 bytes and
// without URLs; Err keeps the cause.
type Error struct {
	Tool   string // ToolYtDlp or ToolDeno
	Code   string // one of the Code constants
	Detail string // optional: the HTTP status of CodeHTTPStatus, the cause of CodeInternal
	GOOS   string // names deno.exe on Windows
	Err    error
}

func (e *Error) Error() string {
	name := displayName(e.Tool)
	var s string
	switch e.Code {
	case CodeNetwork:
		s = "could not download " + name + " (network)"
	case CodeTLS:
		s = "could not download " + name + " (TLS)"
	case CodeHTTPStatus:
		s = "could not download " + name + " (" + orDefault(e.Detail, "HTTP error") + ")"
	case CodeChecksum:
		s = name + " failed its checksum check"
	case CodeSignature:
		s = name + " failed its signature check"
	case CodeAuthenticode:
		s = exeName(e.Tool, orDefault(e.GOOS, "windows")) + " is not signed by its publisher"
	case CodeDiskFull:
		s = "not enough free disk space to install " + name
	case CodeUnsupportedPlatform:
		s = "no " + name + " build for this platform"
		if e.Tool == ToolDeno {
			s += "; YouTube offers fewer formats"
		}
	case CodeSmokeTest:
		s = "the downloaded " + name + " does not run"
	default:
		s = "could not install " + name
		if e.Detail != "" {
			s += ": " + e.Detail
		}
	}
	return truncateText(s, maxErrorText)
}

func (e *Error) Unwrap() error { return e.Err }

// ErrorCode returns the code of the first Error in err's chain, CodeInternal for any
// other error and "" for nil.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// toolError returns err as an Error of tool: an Error keeps its code, anything else is
// CodeInternal with err's text as the detail.
func toolError(tool, goos string, err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		c := *e
		c.Tool, c.GOOS = tool, goos
		return &c
	}
	return &Error{Tool: tool, Code: CodeInternal, Detail: err.Error(), GOOS: goos, Err: err}
}

// coded returns an Error with code and cause err; the tool is filled in by toolError.
func coded(code string, err error) *Error { return &Error{Code: code, Err: err} }

// unavailable is the error of Ensure, Install and Acquire when the tools cannot be used:
// it wraps sitemedia.ErrToolsUnavailable and cause, and reads
// "<ErrToolsUnavailable>: <cause>".
func unavailable(cause error) error {
	if cause == nil {
		return sitemedia.ErrToolsUnavailable
	}
	return fmt.Errorf("%w: %w", sitemedia.ErrToolsUnavailable, cause)
}

// installing is Ensure's answer while an install still runs at the caller's deadline:
// exactly "<ErrToolsInstalling> (<percent>%)".
func installing(percent int) error {
	return fmt.Errorf("%w (%d%%)", sitemedia.ErrToolsInstalling, percent)
}

// displayName is the name users read for tool.
func displayName(tool string) string {
	if tool == ToolDeno {
		return "Deno"
	}
	return "yt-dlp"
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// truncateText cuts s to at most n bytes at a rune boundary, replacing control
// characters with spaces.
func truncateText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
