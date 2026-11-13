package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downex/internal/engine"
	"downex/internal/model"
	"downex/internal/store"
	"downex/internal/testserver"
)

// Without downexd, a download awaiting confirmation is listed as such, is never run
// by "resume --local", and "rm --local --delete-file" deletes no file for it.
func TestLocalCommandsOnAwaitingDownload(t *testing.T) {
	e := newEnv(t)
	st, err := store.OpenSQLite(e.db)
	if err != nil {
		t.Fatal(err)
	}
	d := model.Download{Request: model.Request{URL: "https://example.test/a.zip"}, Dir: e.dir, Filename: "a.zip",
		Size: -1, State: model.StateAwaitingConfirmation, Source: model.SourceBrowser, Category: "Archives"}
	if err := st.CreateDownload(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// A stray part file of that name, which belongs to no download.
	stray := engine.PartPath(e.dir, "a.zip")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "1"
	if r := e.cli(ctx, "ls"); r.code != exitOK || !strings.Contains(r.stdout, "awaiting") {
		t.Fatalf("ls: %v", r)
	}
	if r := e.cli(ctx, "info", id); r.code != exitOK || !strings.Contains(r.stdout, "awaiting") ||
		strings.Contains(r.stdout, ".dxpart") {
		t.Fatalf("info: %v", r)
	}
	if r := e.cli(ctx, "resume", id, "--local"); r.code != exitError || !strings.Contains(r.stderr, "downex confirm 1") {
		t.Fatalf("resume --local: %v", r)
	}
	if r := e.cli(ctx, "rm", id, "--local", "--delete-file"); r.code != exitOK {
		t.Fatalf("rm --local: %v", r)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("rm deleted a file the download did not own: %v", err)
	}
}

// "get" records its downloads as added by the CLI.
func TestGetRecordsSource(t *testing.T) {
	srv := newServer(t, testserver.Options{Size: 10_000, Seed: 3})
	e := newEnv(t)
	if r := e.get(context.Background(), srv.URL+"/s.bin"); r.code != exitOK {
		t.Fatalf("get: %v", r)
	}
	d, _ := e.load(t, 1)
	if d.Source != model.SourceCLI || d.State != model.StateCompleted {
		t.Fatalf("download = %+v", d)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "s.bin")); err != nil {
		t.Fatal(err)
	}
}
