package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"downex/internal/model"
	"downex/internal/testserver"
)

// TestReserveReplace: in replace mode the record's own final file does not take its name
// (the new copy replaces it at completion), but only a regular file: a folder or a link
// under the name is never replaced, and a variant is reserved instead.
func TestReserveReplace(t *testing.T) {
	ctx := context.Background()
	e := New(newMemStore(), nil, Options{})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.bin"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, err := e.reserve(ctx, dir, "f.bin", 0, true)
	if err != nil || name != "f.bin" {
		t.Fatalf("reserve in replace mode = %q, %v; want f.bin", name, err)
	}
	if _, err := os.Stat(partPath(dir, "f.bin")); err != nil {
		t.Fatalf("part file not created: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f.bin")); string(got) != "old" {
		t.Fatalf("the file was changed by reserve: %q", got)
	}
	// Its part file exists now: the name is not free a second time.
	if name, err := e.reserve(ctx, dir, "f.bin", 0, true); err != nil || name != "f (1).bin" {
		t.Fatalf("second reserve = %q, %v; want f (1).bin", name, err)
	}
	// Without replace mode the file takes the name, as always.
	if name, err := e.reserve(ctx, dir, "f.bin", 0, false); err != nil || name != "f (2).bin" {
		t.Fatalf("reserve = %q, %v; want f (2).bin", name, err)
	}

	folder := t.TempDir()
	if err := os.Mkdir(filepath.Join(folder, "g.bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if name, err := e.reserve(ctx, folder, "g.bin", 0, true); err != nil || name != "g (1).bin" {
		t.Fatalf("reserve over a folder = %q, %v; want g (1).bin", name, err)
	}

	links := t.TempDir()
	target := filepath.Join(links, "target.bin")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(links, "h.bin")); err != nil {
		t.Logf("no symbolic links here (%v): the link case is skipped", err)
		return
	}
	if name, err := e.reserve(ctx, links, "h.bin", 0, true); err != nil || name != "h (1).bin" {
		t.Fatalf("reserve over a link = %q, %v; want h (1).bin", name, err)
	}
}

// TestPromoteReplace: in replace mode the part file replaces a regular file of its own
// name; a folder or a link there is left alone and the next free variant is used (and
// announced), as without replace mode.
func TestPromoteReplace(t *testing.T) {
	ctx := context.Background()
	e := New(newMemStore(), nil, Options{})
	write := func(t *testing.T, path, s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}

	t.Run("regular file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "f.bin"), "old")
		write(t, partPath(dir, "f.bin"), "new")
		var variants []string
		final, err := e.promote(ctx, dir, "f.bin", 0, true, func(v string) error { variants = append(variants, v); return nil })
		if err != nil || final != "f.bin" || len(variants) != 0 {
			t.Fatalf("promote = %q, %v (variants %q); want f.bin", final, err, variants)
		}
		if got := read(t, filepath.Join(dir, "f.bin")); got != "new" {
			t.Fatalf("f.bin = %q, want the new copy", got)
		}
		if _, err := os.Lstat(partPath(dir, "f.bin")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("part file left behind: %v", err)
		}
	})
	t.Run("no file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, partPath(dir, "f.bin"), "new")
		if final, err := e.promote(ctx, dir, "f.bin", 0, true, nil); err != nil || final != "f.bin" {
			t.Fatalf("promote = %q, %v; want f.bin", final, err)
		}
		if got := read(t, filepath.Join(dir, "f.bin")); got != "new" {
			t.Fatalf("f.bin = %q", got)
		}
	})
	t.Run("folder", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "f.bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "f.bin", "inside"), "keep")
		write(t, partPath(dir, "f.bin"), "new")
		var variants []string
		final, err := e.promote(ctx, dir, "f.bin", 0, true, func(v string) error { variants = append(variants, v); return nil })
		if err != nil || final != "f (1).bin" || !slices.Equal(variants, []string{"f (1).bin"}) {
			t.Fatalf("promote = %q, %v (variants %q); want f (1).bin", final, err, variants)
		}
		if got := read(t, filepath.Join(dir, "f.bin", "inside")); got != "keep" {
			t.Fatalf("the folder's file = %q", got)
		}
		if got := read(t, filepath.Join(dir, final)); got != "new" {
			t.Fatalf("%s = %q", final, got)
		}
	})
	t.Run("link", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.bin")
		write(t, target, "target")
		if err := os.Symlink(target, filepath.Join(dir, "f.bin")); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		write(t, partPath(dir, "f.bin"), "new")
		final, err := e.promote(ctx, dir, "f.bin", 0, true, nil)
		if err != nil || final != "f (1).bin" {
			t.Fatalf("promote = %q, %v; want f (1).bin", final, err)
		}
		if fi, err := os.Lstat(filepath.Join(dir, "f.bin")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was replaced: %v %v", fi, err)
		}
		if got := read(t, target); got != "target" {
			t.Fatalf("the link's target = %q", got)
		}
	})
}

// TestRestartCompleted: a completed download is restarted only when the caller allows
// it. Keep both reserves a free variant at once and leaves the file alone; replace keeps
// the name, and the file stays until the new copy is complete, which then replaces it;
// a file that is gone is downloaded again under its own name.
func TestRestartCompleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t, testserver.Options{Size: 300 << 10, Seed: 11, ETag: `"a"`}, fastOpts())
	d := h.add("/again.bin", "", 2)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first := h.verify(d.ID)
	path := filepath.Join(first.Dir, first.Filename)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Restart(ctx, d.ID, RestartOptions{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Restart of a completed download without Completed = %v, want ErrInvalidRequest", err)
	}

	// Keep both: a variant at once, the file untouched.
	if err := h.eng.Restart(ctx, d.ID, RestartOptions{Completed: true}); err != nil {
		t.Fatalf("Restart (keep both): %v", err)
	}
	r := h.st.get(t, d.ID)
	if r.State != model.StateQueued || r.Filename != "again (1).bin" || r.ReplaceFile || !r.CompletedAt.IsZero() {
		t.Fatalf("record after keep-both restart: state %v, name %q, replace %v, completed %v", r.State, r.Filename,
			r.ReplaceFile, r.CompletedAt)
	}
	h.srv.Update(func(o *testserver.Options) { o.Seed = 12; o.ETag = `"b"` })
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run (keep both): %v", err)
	}
	both := h.verify(d.ID)
	if both.Filename != "again (1).bin" {
		t.Fatalf("keep both completed as %q", both.Filename)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, original) {
		t.Fatal("keep both changed the original file")
	}

	// Replace: the same name; the file stays until the new copy is complete.
	if err := h.eng.Restart(ctx, d.ID, RestartOptions{Completed: true, Replace: true}); err != nil {
		t.Fatalf("Restart (replace): %v", err)
	}
	r = h.st.get(t, d.ID)
	if r.State != model.StateQueued || r.Filename != "again (1).bin" || !r.ReplaceFile {
		t.Fatalf("record after replace restart: state %v, name %q, replace %v", r.State, r.Filename, r.ReplaceFile)
	}
	kept := filepath.Join(r.Dir, r.Filename)
	before, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("the file to replace is gone before the run: %v", err)
	}
	h.srv.Update(func(o *testserver.Options) { o.Seed = 13; o.ETag = `"c"` })
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run (replace): %v", err)
	}
	replaced := h.verify(d.ID)
	if replaced.Filename != "again (1).bin" || replaced.ReplaceFile {
		t.Fatalf("replace completed as %q (replace flag %v)", replaced.Filename, replaced.ReplaceFile)
	}
	if after, _ := os.ReadFile(kept); bytes.Equal(after, before) {
		t.Fatal("the file was not replaced")
	}
	if names := dirNames(t, r.Dir); !slices.Equal(names, []string{"again (1).bin", "again.bin"}) {
		t.Fatalf("folder after replace = %q", names)
	}

	// The file is gone: downloaded again under its own name, nothing to keep or replace.
	if err := os.Remove(kept); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Restart(ctx, d.ID, RestartOptions{Completed: true, Replace: true}); err != nil {
		t.Fatalf("Restart (file gone): %v", err)
	}
	if r := h.st.get(t, d.ID); r.Filename != "again (1).bin" || r.ReplaceFile {
		t.Fatalf("record after restart of a missing file: name %q, replace %v", r.Filename, r.ReplaceFile)
	}
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run (file gone): %v", err)
	}
	if got := h.verify(d.ID); got.Filename != "again (1).bin" {
		t.Fatalf("completed as %q", got.Filename)
	}
}

// TestRestartReplaceKeepsFileOnFailure: a replacing run that fails half way leaves the
// file as it was, and the next run (a resume) still replaces it once complete; a file
// that became a folder meanwhile is never replaced.
func TestRestartReplaceKeepsFileOnFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 21, ETag: `"a"`}, resumeOpts())
	d := h.add("/r.bin", "", 2)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	done := h.verify(d.ID)
	path := filepath.Join(done.Dir, done.Filename)
	original, _ := os.ReadFile(path)

	if err := h.eng.Restart(ctx, d.ID, RestartOptions{Completed: true, Replace: true}); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	h.srv.Update(func(o *testserver.Options) { o.Seed = 22; o.Throttle = 1 << 20 })
	if err := pauseMidway(t, h, d.ID, 256<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, original) {
		t.Fatal("the file changed while its replacement was downloading")
	}
	h.srv.Update(func(o *testserver.Options) { o.FailFirst = 1 << 30; o.FailStatus = 500 })
	if err := h.run(d.ID, nil, nil); err == nil {
		t.Fatal("Run against a failing server succeeded")
	}
	if r := h.st.get(t, d.ID); r.State != model.StateFailed || !r.ReplaceFile {
		t.Fatalf("record after the failure: state %v, replace %v", r.State, r.ReplaceFile)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, original) {
		t.Fatal("a failed replacement changed the file")
	}

	// The file became a folder: the new copy takes a variant, the folder stays.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	h.srv.Update(func(o *testserver.Options) { o.FailFirst = 0; o.Throttle = 0 })
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run after the failure: %v", err)
	}
	got := h.verify(d.ID)
	if got.Filename != "r (1).bin" || got.ReplaceFile {
		t.Fatalf("completed as %q (replace %v); want r (1).bin", got.Filename, got.ReplaceFile)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.IsDir() {
		t.Fatalf("the folder was replaced: %v %v", fi, err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
