package mediajob

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"

	"downex/internal/media"
)

// Download again → Replace of a completed stream or web page video: with ReplaceExisting
// the assembled (or copied) output replaces the regular file at OutPath once it is
// complete, through Run, RunRanged's join and RunRanged's copy alike; a folder there is
// never replaced (the error matches fs.ErrExist and the folder stays).
func TestRunReplaceExisting(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		s := newServer(t)
		want := addTSPlaylist(s, "/o/", 2, 5)
		opts := testOptions(t, &fakeAssembler{})
		opts.ReplaceExisting = true
		writeOld(t, opts.OutPath)
		if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/o/index.m3u8")}, nil, nil, nil); err != nil {
			t.Fatalf("Run with ReplaceExisting over a regular file: %v", err)
		}
		if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
			t.Fatalf("output = %d bytes, want the new copy (%d bytes)", len(got), len(want))
		}
	})
	t.Run("run without replace", func(t *testing.T) {
		s := newServer(t)
		addTSPlaylist(s, "/o/", 2, 5)
		opts := testOptions(t, &fakeAssembler{})
		writeOld(t, opts.OutPath)
		if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/o/index.m3u8")}, nil, nil, nil); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("Run over a file without ReplaceExisting = %v, want fs.ErrExist", err)
		}
		if got := readFile(t, opts.OutPath); string(got) != "the old copy" {
			t.Fatalf("the old file changed: %q", got)
		}
	})
	t.Run("ranged join", func(t *testing.T) {
		s := newSiteServer(t)
		sel, v, a := pairSelection(t, s, "/")
		opts := rangedOptions(t)
		opts.ReplaceExisting = true
		writeOld(t, opts.OutPath)
		if c, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil || c != "mp4" {
			t.Fatalf("RunRanged (join) with ReplaceExisting = %q, %v", c, err)
		}
		checkMP4(t, opts.OutPath, v.samples, a.samples)
	})
	t.Run("ranged copy", func(t *testing.T) {
		s := newSiteServer(t)
		data := slices.Concat(ftyp(), box("moov", box("mvhd", make([]byte, 100)), box("trak", box("tkhd", make([]byte, 84)))),
			box("mdat", filler(1500<<10, 6)))
		s.add("/18.mp4", data)
		opts := rangedOptions(t)
		opts.ReplaceExisting = true
		writeOld(t, opts.OutPath)
		sel := RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{
			rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), int64(len(data))),
		}}
		if c, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil || c != "mp4" {
			t.Fatalf("RunRanged (copy) with ReplaceExisting = %q, %v", c, err)
		}
		if !bytes.Equal(readFile(t, opts.OutPath), data) {
			t.Fatal("the copied output did not replace the old file")
		}
	})
	t.Run("ranged copy over a folder", func(t *testing.T) {
		s := newSiteServer(t)
		data := slices.Concat(ftyp(), box("moov", box("mvhd", make([]byte, 100)), box("trak", box("tkhd", make([]byte, 84)))),
			box("mdat", filler(1500<<10, 6)))
		s.add("/18.mp4", data)
		opts := rangedOptions(t)
		opts.ReplaceExisting = true
		if err := os.Mkdir(opts.OutPath, 0o700); err != nil {
			t.Fatal(err)
		}
		sel := RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{
			rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), int64(len(data))),
		}}
		if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("RunRanged over a folder = %v, want fs.ErrExist", err)
		}
		if fi, err := os.Lstat(opts.OutPath); err != nil || !fi.IsDir() {
			t.Fatalf("the folder was replaced: %v", err)
		}
	})
}

func writeOld(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("the old copy"), 0o644); err != nil {
		t.Fatal(err)
	}
}
