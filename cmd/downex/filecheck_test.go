package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/store"
	"downex/internal/testserver"
)

// The file check in the CLI (docs/DESKTOP-SPEC.md §9.7).

func peBytes() []byte {
	b := make([]byte, 4096)
	copy(b, "MZ")
	b[0x3C] = 0x80
	copy(b[0x80:], "PE\x00\x00")
	return b
}

func TestCheckCommandOnFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()
	sys := offlineSystem()
	for _, tc := range []struct {
		name, contents, named, result string
		data                          []byte
	}{
		{"movie.mp4", "Windows program", "MP4 video", "Danger: This file is a Windows program, not an MP4 video.", peBytes()},
		{"video.mp4", "web page", "MP4 video", "Caution: This file is a web page, not an MP4 video.", []byte("<!DOCTYPE html><html><body>Sign in</body></html>")},
		{"photo.jpg", "PNG image", "JPEG image", "Note: This file is a PNG image, though its name says a JPEG image.", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)},
		{"notes.txt", "text file", "text file", "no warning", []byte("plain notes, nothing more\n")},
	} {
		r := runWith(ctx, sys, "check", write(tc.name, tc.data))
		if r.code != exitOK || infoRow(r.stdout, "Contents") != tc.contents || infoRow(r.stdout, "Named as") != tc.named ||
			!strings.HasPrefix(infoRow(r.stdout, "Result"), tc.result) {
			t.Errorf("check %s: %v", tc.name, r)
		}
	}
	r := runWith(ctx, sys, "check", "--json", filepath.Join(dir, "movie.mp4"))
	var rep checkReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || rep.FileCheck == nil || rep.FileCheck.Level != "danger" ||
		rep.FileCheck.Reason != "program_disguised" || rep.FileCheck.Stage != "final" || rep.Contents != "Windows program" {
		t.Fatalf("check --json: %+v %v\n%s", rep, err, r.stdout)
	}
	if r := runWith(ctx, sys, "check", filepath.Join(dir, "missing.bin")); r.code != exitError {
		t.Fatalf("missing file: %v", r)
	}
	if r := runWith(ctx, sys, "check"); r.code != exitError {
		t.Fatalf("no argument: %v", r)
	}
}

func TestCheckCommandOnDownload(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "movie.mp4"), peBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	seedDownloads(t, db,
		storedRecord{name: "completed", d: &model.Download{Request: model.Request{URL: "https://example.test/m"}, Dir: dir,
			Filename: "movie.mp4", Size: 4096, State: model.StateCompleted, Mode: model.ModeRanged, ServedType: "video/mp4",
			CompletedAt: time.Now()}},
		storedRecord{name: "paused", d: &model.Download{Request: model.Request{URL: "https://example.test/p"}, Dir: dir,
			Filename: "p.bin", Size: 10, State: model.StatePaused, Mode: model.ModeRanged},
			segs: []model.Segment{{ID: 0, Start: 0, End: 9}}},
	)
	ctx := context.Background()
	r := runWith(ctx, offlineSystem(), "check", "1", "--db", db)
	if r.code != exitOK || infoRow(r.stdout, "Server said") != "video/mp4" ||
		infoRow(r.stdout, "Result") != "Danger: This file is a Windows program, not an MP4 video." {
		t.Fatalf("check 1: %v", r)
	}
	if r := runWith(ctx, offlineSystem(), "check", "2", "--db", db); r.code != exitError || !strings.Contains(r.stderr, "not completed") {
		t.Fatalf("check of a paused download: %v", r)
	}
}

func TestInfoAndLsShowFileCheck(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	dir := t.TempDir()
	seedDownloads(t, db, storedRecord{name: "disguised", d: &model.Download{Request: model.Request{URL: "https://example.test/m"},
		Dir: dir, Filename: "movie.mp4", Size: 4096, State: model.StateCompleted, Mode: model.ModeRanged,
		CompletedAt: time.Now(), FileCheck: &model.FileCheck{Level: model.FileCheckDanger, Stage: model.FileCheckStageFinal,
			Reason: "program_disguised", Content: "pe", Claim: "mp4"}}})
	ctx := context.Background()
	sys := offlineSystem()
	r := runWith(ctx, sys, "info", "1", "--db", db)
	if infoRow(r.stdout, "Contents") != "Windows program (named as MP4 video)" ||
		infoRow(r.stdout, "File check") != "Danger: This file is a Windows program, not an MP4 video." {
		t.Fatalf("info:\n%s", r.stdout)
	}
	if line := lsLine(runWith(ctx, sys, "ls", "--db", db).stdout, 1); !strings.Contains(line, "check file") {
		t.Fatalf("ls row %q", line)
	}
	r = runWith(ctx, sys, "ls", "--json", "--db", db)
	var list []downloadView
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list) != 1 || list[0].FileCheck == nil ||
		list[0].FileCheck.Summary != "This file is a Windows program, not an MP4 video." {
		t.Fatalf("ls --json: %v\n%s", err, r.stdout)
	}
	// check_downloaded_files off in downexd's settings file: nothing is shown.
	if err := os.WriteFile(filepath.Join(filepath.Dir(db), settingsFileName), []byte(`{"desktop":{"check_files":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runWith(ctx, sys, "info", "1", "--db", db)
	if infoRow(r.stdout, "File check") != "" {
		t.Fatalf("info with the check off:\n%s", r.stdout)
	}
	if line := lsLine(runWith(ctx, sys, "ls", "--db", db).stdout, 1); strings.Contains(line, "check file") {
		t.Fatalf("ls row with the check off %q", line)
	}
}

// TestGetWarnsAndStores: a local get of a "movie.mp4" that is a Windows program prints
// the warning and stores the final check.
func TestGetWarnsAndStores(t *testing.T) {
	srv := testserver.New(testserver.Options{Size: 50_000, Seed: 1, Prefix: peBytes(), Filename: "movie.mp4", ContentType: "video/mp4"})
	t.Cleanup(srv.Close)
	db := filepath.Join(t.TempDir(), "downex.db")
	out := t.TempDir()
	r := runWith(context.Background(), offlineSystem(), "get", srv.URL+"/x", "-o", out, "--db", db)
	if r.code != exitOK || !strings.Contains(r.stderr, "Warning: This file is a Windows program, not an MP4 video.") {
		t.Fatalf("get: %v", r)
	}
	st, err := store.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := st.GetDownload(context.Background(), 1)
	if err != nil || d.FileCheck == nil || d.FileCheck.Level != model.FileCheckDanger || d.ServedType != "video/mp4" {
		t.Fatalf("stored = %+v, %v", d, err)
	}
}
