package main

import (
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"downex/internal/platform"
)

// The relay's log: nmhost.log in the per-user log directory (next to downexd.log),
// rotated to nmhost.log.1 at 1 MiB. Browsers may run several relays at once; they
// append to the same file. Header values, cookies, bodies and the query and user
// information of URLs are never logged.
const (
	logFileName = "nmhost.log"
	logMaxBytes = 1 << 20
)

// openLog returns the relay's logger and a function that closes its file. Logging
// is best effort: without a usable log directory nothing is logged. It never writes
// to stdout, which carries the protocol.
func openLog() (*slog.Logger, func()) {
	dir, err := platform.LogDir()
	if err != nil {
		return slog.New(slog.DiscardHandler), func() {}
	}
	w, err := openRotating(filepath.Join(dir, logFileName), logMaxBytes)
	if err != nil {
		return slog.New(slog.DiscardHandler), func() {}
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With("pid", os.Getpid()), func() { _ = w.Close() }
}

// rotatingWriter appends to a file and, before a write would make it larger than
// maxBytes, renames it to "<name>.1" (replacing an older one) and starts a new file.
// When the rename fails (another relay rotated it, or holds it open), writing goes on.
type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func openRotating(path string, maxBytes int64) (*rotatingWriter, error) {
	w := &rotatingWriter{path: path, maxBytes: maxBytes}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f, w.size = f, fi.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, fs.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		_ = w.f.Close()
		w.f = nil
		_ = os.Rename(w.path, w.path+".1") // best effort, see the type comment
		if err := w.open(); err != nil {
			return 0, err
		}
		if w.size+int64(len(p)) > w.maxBytes {
			w.size = 0 // the rename failed: do not try again before another maxBytes
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

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

// redactURL keeps a URL's scheme, host and path for the log; user information and the
// query (signed links carry credentials there) are replaced, the fragment is dropped.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparsable URL)"
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString("[redacted]@")
	}
	b.WriteString(u.Host)
	b.WriteString(truncate(u.EscapedPath(), 512))
	if u.RawQuery != "" || u.ForceQuery {
		b.WriteString("?[redacted]")
	}
	return b.String()
}
