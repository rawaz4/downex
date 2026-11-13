package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/store"
)

// TestResumeAgainLocalMediaChangesNothing: "resume --restart --local" of a completed
// stream is refused (only downexd runs streams), and the refusal leaves the record and
// the folder as they were: no queued record under a new name, no empty part file that
// downexd would later download unasked.
func TestResumeAgainLocalMediaChangesNothing(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	dir := t.TempDir()
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	done := time.Now().Add(-time.Hour).Truncate(time.Second)
	d := &model.Download{Request: model.Request{URL: testManifest}, Dir: dir, Filename: "show.mp4", Size: 1234,
		State: model.StateCompleted, CompletedAt: done, Kind: model.KindMedia, Media: &model.Media{
			Format: "hls", Title: "The Show", Container: "mp4", SelectedVideo: "720p",
			FragmentsTotal: 4, FragmentsDone: 4, BytesDone: 1234,
			Tracks: []model.MediaTrack{{ID: "720p", Kind: "video"}},
		}}
	if err := st.CreateDownload(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "show.mp4"), make([]byte, 1234), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"resume", "1", "--restart", "--local", "--db", db},
		{"resume", "1", "--restart", "--replace", "--local", "--db", db},
	} {
		r := runWith(context.Background(), offlineSystem(), args...)
		if r.code != exitError || !strings.Contains(r.stderr, "only downexd runs") {
			t.Fatalf("%q of a completed stream: %v", args, r)
		}
	}
	st2, err := store.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.GetDownload(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateCompleted || got.Filename != "show.mp4" || got.Size != 1234 || got.ReplaceFile {
		t.Errorf("after the refusal the record is %s, %q, size %d (replace %v); want completed, show.mp4, 1234",
			got.State, got.Filename, got.Size, got.ReplaceFile)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 {
		t.Errorf("folder after the refusal = %q, want only show.mp4", names)
	}
}
