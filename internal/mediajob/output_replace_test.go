package mediajob

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestPlaceOutputReplace: without ReplaceExisting an existing OutPath is never replaced;
// with it a regular file is, but never a folder or a link (fs.ErrExist, part kept).
func TestPlaceOutputReplace(t *testing.T) {
	ctx := context.Background()
	write := func(t *testing.T, p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setup := func(t *testing.T) (Options, string) {
		dir := t.TempDir()
		o := Options{PartDir: filepath.Join(dir, "out.mp4.dxparts"), OutPath: filepath.Join(dir, "out.mp4"), Clock: realClock{}}
		part := o.OutPath + ".dxpart"
		write(t, part, "new")
		return o, part
	}

	t.Run("free", func(t *testing.T) {
		o, part := setup(t)
		if err := placeOutput(ctx, o, part); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(o.OutPath); string(b) != "new" {
			t.Fatalf("out = %q", b)
		}
	})
	t.Run("taken without replace", func(t *testing.T) {
		o, part := setup(t)
		write(t, o.OutPath, "old")
		if err := placeOutput(ctx, o, part); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("placeOutput = %v, want fs.ErrExist", err)
		}
		if b, _ := os.ReadFile(o.OutPath); string(b) != "old" {
			t.Fatalf("out = %q", b)
		}
	})
	t.Run("replace", func(t *testing.T) {
		o, part := setup(t)
		o.ReplaceExisting = true
		write(t, o.OutPath, "old")
		if err := placeOutput(ctx, o, part); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(o.OutPath); string(b) != "new" {
			t.Fatalf("out = %q", b)
		}
		if _, err := os.Lstat(part); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("part left behind: %v", err)
		}
	})
	t.Run("replace a folder", func(t *testing.T) {
		o, part := setup(t)
		o.ReplaceExisting = true
		if err := os.Mkdir(o.OutPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := placeOutput(ctx, o, part); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("placeOutput over a folder = %v, want fs.ErrExist", err)
		}
		if fi, err := os.Lstat(o.OutPath); err != nil || !fi.IsDir() {
			t.Fatalf("the folder: %v %v", fi, err)
		}
		if b, _ := os.ReadFile(part); string(b) != "new" {
			t.Fatalf("part = %q", b)
		}
	})
	t.Run("replace a link", func(t *testing.T) {
		o, part := setup(t)
		o.ReplaceExisting = true
		target := filepath.Join(t.TempDir(), "target")
		write(t, target, "target")
		if err := os.Symlink(target, o.OutPath); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		if err := placeOutput(ctx, o, part); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("placeOutput over a link = %v, want fs.ErrExist", err)
		}
		if fi, err := os.Lstat(o.OutPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link: %v %v", fi, err)
		}
		if b, _ := os.ReadFile(target); string(b) != "target" {
			t.Fatalf("target = %q", b)
		}
	})
}

// TestFinishAssembledReplace: a replacing job that stopped between recording its output
// and the rename finishes by replacing the old file; without ReplaceExisting the old file
// stays and the record is stale.
func TestFinishAssembledReplace(t *testing.T) {
	ctx := context.Background()
	for _, replace := range []bool{true, false} {
		dir := t.TempDir()
		o := Options{PartDir: filepath.Join(dir, "v.mp4.dxparts"), OutPath: filepath.Join(dir, "v.mp4"), Clock: realClock{},
			ReplaceExisting: replace}
		if err := os.MkdirAll(o.PartDir, 0o755); err != nil {
			t.Fatal(err)
		}
		part := o.OutPath + ".dxpart"
		if err := os.WriteFile(o.OutPath, []byte("the old copy"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(part, []byte("the new copy"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := recordOutput(ctx, o, part, "mp4"); err != nil {
			t.Fatal(err)
		}
		container, done, err := finishAssembled(ctx, o)
		if err != nil {
			t.Fatalf("replace %v: finishAssembled: %v", replace, err)
		}
		b, _ := os.ReadFile(o.OutPath)
		switch {
		case replace && (!done || container != "mp4" || string(b) != "the new copy"):
			t.Fatalf("replace: done %v, container %q, out %q", done, container, b)
		case !replace && (done || string(b) != "the old copy"):
			t.Fatalf("keep: done %v, out %q", done, b)
		}
		if !replace {
			if _, err := os.Stat(filepath.Join(o.PartDir, outputRecordName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("stale record kept: %v", err)
			}
		}
	}
}
