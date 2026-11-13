package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	l := layout{dir: t.TempDir(), goos: "windows"}
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	st := stateFile{
		Version: stateVersion,
		YtDlp: toolRecord{
			entry: entry{Version: "2026.10.10.233141", Channel: "nightly", File: l.rel(ToolYtDlp, "2026.10.10.233141"),
				SHA256: hexSum("y"), Size: 18 << 20, SigCreated: at, Verified: "openpgp+sha256", InstalledAt: at},
			Previous:   &entry{Version: "2026.10.01", Channel: "nightly", File: l.rel(ToolYtDlp, "2026.10.01"), SHA256: hexSum("p"), Size: 1},
			Bad:        []string{"2026.10.05"},
			Unsuitable: []pathVersion{{Path: filepath.Join(l.dir, "Scripts", "yt-dlp.exe"), Version: "2026.09.01"}},
			Latest:     "2026.10.10.233141",
			CheckedAt:  at,
		},
		Deno: toolRecord{entry: entry{Version: "2.5.0", File: "deno/2.5.0/deno.exe", SHA256: hexSum("d"), Size: 100,
			Verified: "sha256+authenticode", Signer: "Deno Land Inc."}},
	}
	if err := saveState(l, st); err != nil {
		t.Fatal(err)
	}
	got := loadState(l)
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(st.clone())
	if string(a) != string(b) {
		t.Errorf("round trip:\n got %s\nwant %s", a, b)
	}
	if !strings.Contains(string(readAll(t, l.state())), `"version": 1`) {
		t.Error("tools.json lacks its version")
	}
	if entries, _ := filepath.Glob(filepath.Join(l.dir, ".tools-*.tmp")); len(entries) != 0 {
		t.Errorf("temporary files left: %v", entries)
	}
}

func TestStateRejects(t *testing.T) {
	l := layout{dir: t.TempDir(), goos: "linux"}
	write := func(s string) {
		if err := os.WriteFile(l.state(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	empty := func(s stateFile) bool { return s.YtDlp.Version == "" && s.Deno.Version == "" }
	good := `{"version":1,"ytdlp":{"version":"2026.10.01","file":"yt-dlp/2026.10.01/yt-dlp","sha256":"` + hexSum("y") + `","size":10}}`
	write(good)
	if s := loadState(l); s.YtDlp.Version != "2026.10.01" {
		t.Fatalf("good state not read: %+v", s)
	}
	for name, s := range map[string]string{
		"other version":   strings.Replace(good, `"version":1`, `"version":2`, 1),
		"not JSON":        "{",
		"traversal":       strings.Replace(good, "yt-dlp/2026.10.01/yt-dlp", "../../evil", 1),
		"bad version":     strings.ReplaceAll(good, "2026.10.01", "../x"),
		"short hash":      strings.Replace(good, hexSum("y"), hexSum("y")[:60], 1),
		"uppercase hash":  strings.Replace(good, hexSum("y"), strings.ToUpper(hexSum("y")), 1),
		"zero size":       strings.Replace(good, `"size":10`, `"size":0`, 1),
		"windows on unix": strings.Replace(good, "yt-dlp/2026.10.01/yt-dlp", "yt-dlp/2026.10.01/yt-dlp.exe", 1),
		"oversized":       good[:len(good)-1] + `,"latest":"` + strings.Repeat("x", maxStateFile) + `"}`,
	} {
		write(s)
		if got := loadState(l); !empty(got) {
			t.Errorf("%s: read %+v", name, got.YtDlp.entry)
		}
	}
	// Bad list entries and PATH entries are checked one by one.
	abs, _ := json.Marshal(filepath.Join(l.dir, "bin", "yt-dlp"))
	write(`{"version":1,"ytdlp":{"bad":["2026.10.01","nope"],"unsuitable":[{"path":"relative","version":"2026.10.01"},{"path":` + string(abs) + `,"version":"2026.10.01"}],"latest":"x"}}`)
	s := loadState(l)
	if len(s.YtDlp.Bad) != 1 || len(s.YtDlp.Unsuitable) != 1 || s.YtDlp.Latest != "" {
		t.Errorf("lists not sanitized: %+v", s.YtDlp)
	}
	// A missing file is empty.
	os.Remove(l.state())
	if s := loadState(l); !empty(s) || s.Version != stateVersion {
		t.Errorf("missing file: %+v", s)
	}
}
