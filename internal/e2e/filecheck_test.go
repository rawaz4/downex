package e2e

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// TestFileCheckOfADisguisedProgram (docs/DESKTOP-SPEC.md §9): real downexd and downex, a
// server whose "movie.mp4" is a Windows program. downexd stores the final DANGER check,
// `downex info` and `ls --json` show it, `downex check` agrees, and the file is still
// marked with its origin (the check never replaces the mark).
func TestFileCheckOfADisguisedProgram(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)
	pe := make([]byte, 4096)
	copy(pe, "MZ")
	pe[0x3C] = 0x80
	copy(pe[0x80:], "PE\x00\x00")
	ts := newServer(t, testserver.Options{Size: 300 << 10, Seed: 81, ETag: `"pe"`, Prefix: pe,
		Filename: "movie.mp4", ContentType: "video/mp4"})
	d := h.addTo(ts.URL+"/watch", 0, 0)
	h.waitCompleted(d.GetId())
	got := h.get(d.GetId())
	fc := got.GetFileCheck()
	if fc.GetLevel() != downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER || fc.GetStage() != downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL ||
		fc.GetSummary() != "This file is a Windows program, not an MP4 video." || fc.GetServedType() != "video/mp4" {
		t.Fatalf("file check = %v", fc)
	}
	id := strconv.FormatInt(d.GetId(), 10)
	info := h.cli("info", id).ok(t)
	if !strings.Contains(info.stdout, "Danger: This file is a Windows program, not an MP4 video.") ||
		!strings.Contains(info.stdout, "Windows program (named as MP4 video)") {
		t.Errorf("info:\n%s", info.stdout)
	}
	var list []struct {
		ID        int64 `json:"id"`
		FileCheck *struct {
			Level  string `json:"level"`
			Reason string `json:"reason"`
		} `json:"fileCheck"`
	}
	ls := h.cli("ls", "--json").ok(t)
	if err := json.Unmarshal([]byte(ls.stdout), &list); err != nil {
		t.Fatalf("ls --json: %v\n%s", err, ls.stdout)
	}
	found := false
	for _, e := range list {
		if e.ID == d.GetId() {
			found = e.FileCheck != nil && e.FileCheck.Level == "danger" && e.FileCheck.Reason == "program_disguised"
		}
	}
	if !found {
		t.Errorf("ls --json without the check:\n%s", ls.stdout)
	}
	check := h.cli("check", id).ok(t)
	if !strings.Contains(check.stdout, "Danger: This file is a Windows program, not an MP4 video.") {
		t.Errorf("check:\n%s", check.stdout)
	}
	path := filepath.Join(h.out, got.GetFilename())
	if _, err := readOriginMark(path); err != nil && err != errNoMarks {
		t.Errorf("the mark of %s: %v", path, err)
	}
	h.stop(p)
}
