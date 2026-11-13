package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogWritesJSONLines(t *testing.T) {
	dir := t.TempDir()
	log, closeLog := OpenLog(dir, LogName)
	log.Info("window started", "version", "1.2.3")
	log.Debug("not kept")
	closeLog()
	b, err := os.ReadFile(filepath.Join(dir, "ui.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("log = %q, want one line (info and above)", b)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil || rec["msg"] != "window started" || rec["version"] != "1.2.3" || rec["level"] != "INFO" {
		t.Errorf("record = %v, %v", rec, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "ui.log")); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("ui.log mode %v, want 0600", info.Mode().Perm())
	}
}

// A log larger than LogMaxSize moves to ui.log.1 when a window starts, replacing the older
// one; a smaller one is appended to.
func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LogName)
	if err := os.WriteFile(path+".1", []byte("oldest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", LogMaxSize)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log, closeLog := OpenLog(dir, LogName)
	log.Info("after rotation")
	closeLog()
	if b, err := os.ReadFile(path + ".1"); err != nil || len(b) != LogMaxSize+1 {
		t.Errorf("ui.log.1 holds %d bytes, %v; want the rotated %d", len(b), err, LogMaxSize+1)
	}
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "after rotation") || len(b) > 200 {
		t.Errorf("ui.log = %q, %v", b, err)
	}

	// At exactly LogMaxSize it is kept and appended to.
	if err := os.WriteFile(path, []byte(strings.Repeat("y", LogMaxSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	log, closeLog = OpenLog(dir, LogName)
	log.Info("appended")
	closeLog()
	if b, _ := os.ReadFile(path); len(b) <= LogMaxSize || !strings.HasPrefix(string(b), "yyy") {
		t.Errorf("a log of exactly LogMaxSize was rotated (%d bytes now)", len(b))
	}
}

// Without its folder the log discards messages: a window never fails to open over it.
func TestLogWithoutFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, dir := range []string{"", missing} {
		log, closeLog := OpenLog(dir, LogName)
		log.Info("dropped")
		closeLog()
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("OpenLog created the log folder")
	}
}
