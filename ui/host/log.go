package host

import (
	"log/slog"
	"os"
	"path/filepath"

	"downex/internal/platform"
)

// A window process logs to ui.log in downexd's log folder (today's ui/main.go openLog): JSON
// lines at level info and above, never URLs, file contents or secrets. A file larger than
// LogMaxSize is moved to ui.log.1 when a window starts (replacing the older one), so the
// log stays below about twice that size.
const (
	LogName    = "ui.log"
	LogMaxSize = 1 << 20
)

// OpenDefaultLog opens LogName in downexd's log folder (platform.LogDir). Without the
// folder or the file, messages are discarded. closeLog closes the file.
func OpenDefaultLog() (log *slog.Logger, closeLog func()) {
	dir, err := platform.LogDir()
	if err != nil {
		return discardLogger(), func() {}
	}
	return OpenLog(dir, LogName)
}

// OpenLog opens dir/name for appending (0600), first moving a file larger than LogMaxSize
// to dir/name.1. Without dir, or when the file cannot be opened, messages are discarded.
// The folder is not created: downexd creates it. closeLog closes the file.
func OpenLog(dir, name string) (log *slog.Logger, closeLog func()) {
	if dir == "" || name == "" {
		return discardLogger(), func() {}
	}
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && info.Size() > LogMaxSize {
		// Another window may hold the file open (Windows then refuses the move): it is
		// rotated by a later start.
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return discardLogger(), func() {}
	}
	return slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})), func() { _ = f.Close() }
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
