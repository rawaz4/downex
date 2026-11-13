package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Log rotation (docs/DAEMON-SPEC.md §1): downexd.log plus four backups of 5 MiB each.
const (
	logFileName   = "downexd.log"
	logMaxBytes   = 5 << 20
	logBackups    = 4
	logRetryBytes = 256 << 10 // after a failed rotation, write this much before trying again
	redacted      = "[redacted]"
)

// rotatingWriter appends to a log file and, before a write would make it larger than
// maxBytes, renames it to "<name>.1" (shifting older backups up to "<name>.<backups>",
// the oldest being deleted) and starts a new file. It is safe for concurrent use.
// When a rotation fails (another program holds a backup open, say), writing goes on
// in the current file and rotation is retried later.
type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	f        *os.File
	size     int64
	retryAt  int64 // size at which a failed rotation is retried; 0 = none failed
}

// openRotating opens (creating, mode 0600) the log file at path for appending.
func openRotating(path string, maxBytes int64, backups int) (*rotatingWriter, error) {
	w := &rotatingWriter{path: path, maxBytes: maxBytes, backups: backups}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("open log: %w", err)
	}
	w.f, w.size = f, fi.Size()
	return nil
}

// Write appends p, rotating first when p would not fit.
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, fs.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes && (w.retryAt == 0 || w.size >= w.retryAt) {
		if err := w.rotate(); err != nil {
			w.retryAt = w.size + logRetryBytes
			if w.f == nil { // the current file could not even be reopened
				return 0, err
			}
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the current file, shifts the backups and opens a new file. The
// current file is reopened for appending when the rename fails.
func (w *rotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		w.f = nil
		return errors.Join(fmt.Errorf("close log: %w", err), w.open())
	}
	w.f = nil
	var err error
	if rmErr := os.Remove(w.backup(w.backups)); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		err = rmErr
	}
	for i := w.backups - 1; i >= 1 && err == nil; i-- {
		if mvErr := os.Rename(w.backup(i), w.backup(i+1)); mvErr != nil && !errors.Is(mvErr, fs.ErrNotExist) {
			err = mvErr
		}
	}
	if err == nil {
		err = os.Rename(w.path, w.backup(1))
	}
	if err == nil {
		w.retryAt = 0
	}
	if oerr := w.open(); oerr != nil {
		return errors.Join(err, oerr)
	}
	return err
}

func (w *rotatingWriter) backup(i int) string { return w.path + "." + strconv.Itoa(i) }

// Close closes the log file. Later writes fail with fs.ErrClosed.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// logSetup is the daemon's logger and the file behind it.
type logSetup struct {
	logger *slog.Logger
	file   *rotatingWriter
}

// newLogger logs JSON to dir/downexd.log (rotated) and, when console is non-nil, text
// to console as well. Credentials and request bodies are redacted (see redactAttr).
func newLogger(dir string, level slog.Level, console io.Writer) (*logSetup, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w, err := openRotating(filepath.Join(dir, logFileName), logMaxBytes, logBackups)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	var h slog.Handler = slog.NewJSONHandler(w, opts)
	if console != nil {
		h = slog.NewMultiHandler(h, slog.NewTextHandler(console, opts))
	}
	return &logSetup{logger: slog.New(h), file: w}, nil
}

// parseLevel accepts debug, info, warn(ing) and error.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// sensitiveKey reports attribute keys whose values are never logged: credential
// headers and request bodies.
func sensitiveKey(key string) bool {
	switch strings.ToLower(key) {
	case "cookie", "set-cookie", "authorization", "proxy-authorization", "body", "request_body":
		return true
	}
	return false
}

// redactAttr is a slog ReplaceAttr function that hides the values of credential
// headers and bodies, whether logged as attributes of those names or inside an
// http.Header value, and the credentials URLs carry wherever a URL appears: in a
// string attribute (url, final_url, a stored error message), in the text of an error
// (a *url.Error repeats the full request URL) and in the message. See redactURLs.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		if s := a.Value.String(); strings.Contains(s, "://") {
			return slog.String(a.Key, redactURLs(s))
		}
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case http.Header:
			return slog.Any(a.Key, redactHeader(v))
		case *url.URL:
			if v != nil {
				return slog.String(a.Key, redactURL(v.String()))
			}
		case error:
			if v != nil {
				return slog.String(a.Key, redactURLs(v.Error()))
			}
		}
	}
	return a
}

// urlPattern finds http and https URLs in text: up to the next space, quote or angle
// bracket (a *url.Error quotes its URL).
var urlPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)

// redactURLs replaces every http(s) URL in s with redactURL's form.
func redactURLs(s string) string {
	return urlPattern.ReplaceAllStringFunc(s, redactURL)
}

// redactURL keeps a URL's scheme, host and path and replaces its user information
// (a password becomes an Authorization header) and its query (signed links carry
// their signature and credentials there) with a marker; the fragment is dropped.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparsable URL)"
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString(redacted + "@")
	}
	b.WriteString(u.Host)
	b.WriteString(u.EscapedPath())
	if u.RawQuery != "" || u.ForceQuery {
		b.WriteString("?" + redacted)
	}
	return b.String()
}

// redactHeader returns a copy of h with credential values replaced.
func redactHeader(h http.Header) http.Header {
	out := h.Clone()
	for k := range out {
		if sensitiveKey(k) {
			out[k] = []string{redacted}
		}
	}
	return out
}
