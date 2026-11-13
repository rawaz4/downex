package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// Downloading a completed download again (docs/ENGINE-SPEC.md §7.1): "resume --restart"
// keeps the file and saves the new copy under a free name; with --replace the new copy
// replaces the file once it is complete.

func TestResumeAgainRemote(t *testing.T) {
	f, h := newFakeDaemon(t)
	dir := t.TempDir()
	f.do(func() {
		f.downloads[5] = &downexv1.Download{Id: 5, Url: "https://example.test/a.bin", Dir: dir, Filename: "a.bin",
			Size: 100, Received: 100, State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED}
	})
	ctx := context.Background()

	r := h.run(ctx, "resume", "5")
	if r.code != exitOK || !strings.Contains(r.stdout, "Download 5 is already complete") {
		t.Fatalf("resume of a completed download: %v", r)
	}
	r = h.run(ctx, "resume", "5", "--restart")
	if r.code != exitOK || !strings.Contains(r.stdout, "Downloading 5 again (queued, position 2 in the queue): "+filepath.Join(dir, "a (1).bin")) {
		t.Fatalf("resume --restart: %v", r)
	}
	if q := f.rec().resumes[1]; !q.GetRestart() || q.GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_UNSPECIFIED {
		t.Errorf("resume --restart sent %v", q)
	}

	f.do(func() {
		f.downloads[6] = &downexv1.Download{Id: 6, Url: "https://example.test/b.bin", Dir: dir, Filename: "b.bin",
			Size: 100, Received: 100, State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED}
	})
	r = h.run(ctx, "resume", "6", "--restart", "--replace")
	if r.code != exitOK || !strings.Contains(r.stdout, "Downloading 6 again (queued, position 2 in the queue): "+filepath.Join(dir, "b.bin")) ||
		!strings.Contains(r.stdout, "replaces the file once it is complete") {
		t.Fatalf("resume --restart --replace: %v", r)
	}
	if q := f.rec().resumes[2]; !q.GetRestart() || q.GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_REPLACE {
		t.Errorf("resume --restart --replace sent %v", q)
	}

	n := len(f.rec().resumes)
	if r := h.run(ctx, "resume", "6", "--replace"); r.code != exitError || !strings.Contains(r.stderr, "--replace needs --restart") {
		t.Errorf("--replace without --restart: %v", r)
	}
	if got := len(f.rec().resumes); got != n {
		t.Errorf("--replace without --restart reached downexd")
	}
	if r := h.run(ctx, "help", "resume"); r.code != exitOK || !strings.Contains(r.stdout+r.stderr, "-replace") {
		t.Errorf("help resume: %v", r)
	}
}

func TestResumeAgainLocal(t *testing.T) {
	t.Parallel()
	const size = 300 << 10
	srv := newServer(t, testserver.Options{Size: size, Seed: 41, ETag: `"a"`})
	e := newEnv(t)
	if r := e.get(context.Background(), srv.URL+"/again.bin"); r.code != exitOK {
		t.Fatalf("get: %v", r)
	}
	path := filepath.Join(e.dir, "again.bin")
	first := fileSum(t, path)

	srv.Update(func(o *testserver.Options) { o.Seed, o.ETag = 42, `"b"` })
	if r := e.cli(context.Background(), "resume", "1", "--local"); r.code != exitOK || !strings.Contains(r.stdout, "already complete") {
		t.Fatalf("resume --local of a completed download: %v", r)
	}
	r := e.cli(context.Background(), "resume", "1", "--restart", "--local", "--proxy", "direct")
	if r.code != exitOK {
		t.Fatalf("resume --restart --local: %v", r)
	}
	if fileSum(t, path) != first {
		t.Fatal("keep both changed the file")
	}
	if fileSum(t, filepath.Join(e.dir, "again (1).bin")) != testserver.Sum256(42, size) {
		t.Fatal("the new copy does not match the new content")
	}

	srv.Update(func(o *testserver.Options) { o.Seed, o.ETag = 43, `"c"` })
	r = e.cli(context.Background(), "resume", "1", "--restart", "--replace", "--local", "--proxy", "direct")
	if r.code != exitOK {
		t.Fatalf("resume --restart --replace --local: %v", r)
	}
	if fileSum(t, filepath.Join(e.dir, "again (1).bin")) != testserver.Sum256(43, size) {
		t.Fatal("the file was not replaced")
	}
	if d, _ := e.load(t, 1); d.Filename != "again (1).bin" || d.ReplaceFile {
		t.Fatalf("record = %+v", d)
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 2 {
		t.Fatalf("folder has %d entries, want the two copies", len(entries))
	}
	noPartFiles(t, e.dir)
}
