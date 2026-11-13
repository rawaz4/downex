package mediajob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/media"
)

// Two masters listing the same variants in opposite orders.
const (
	masterAB = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e"
a/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.4d401f"
b/index.m3u8
`
	masterBA = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.4d401f"
b/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e"
a/index.m3u8
`
)

// reorderFixture serves masterAB with eight fMP4 segments per variant, runs the job for
// sel (the 720p variant b, "v1") until segment 4 of b is requested, and then makes the
// server list b first. It returns the server, the options, the last checkpoint and the
// expected outputs of the two variants.
func reorderFixture(t *testing.T, sel Selection) (s *server, opts Options, cp *Checkpoint, wantA, wantB []byte) {
	t.Helper()
	s = newServer(t)
	s.add("/m/master.m3u8", []byte(masterAB), hlsType)
	aInit, aSegs := addFMP4Playlist(s, "/m/a/", 8, 31)
	bInit, bSegs := addFMP4Playlist(s, "/m/b/", 8, 32)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var once sync.Once
	s.setHook(func(r *http.Request) {
		if r.URL.Path == "/m/b/s4.m4s" {
			once.Do(func() {
				cancel()
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			})
		}
	})
	opts = testOptions(t, &fakeAssembler{})
	opts.MaxConnections = 1
	var rec recorder
	sel.ManifestURL = s.url("/m/master.m3u8")
	if _, err := Run(ctx, opts, sel, nil, nil, rec.onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if cp = rec.last(); cp == nil || len(cp.Tracks) != 1 || !bitSet(cp.Tracks[0].Done, 4) || bitSet(cp.Tracks[0].Done, 5) {
		t.Fatalf("checkpoint = %+v", cp)
	}
	s.setHook(nil)
	s.add("/m/master.m3u8", []byte(masterBA), hlsType)
	return s, opts, cp, bytes.Join([][]byte{aInit, aSegs}, nil), bytes.Join([][]byte{bInit, bSegs}, nil)
}

// TestResumeDoesNotMixVariants: a resumed selection by position ("v1") that now names
// another variant starts that track over instead of joining the fragments of b already
// on disk with those of a.
func TestResumeDoesNotMixVariants(t *testing.T) {
	sel := Selection{VideoTrack: "v1"}
	s, opts, cp, wantA, _ := reorderFixture(t, sel)
	sel.ManifestURL = s.url("/m/master.m3u8")
	if _, err := Run(testContext(t), opts, sel, cp, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, wantA) {
		t.Fatalf("output (%d bytes) is not variant a (%d bytes): fragments of two variants were joined", len(got), len(wantA))
	}
	for i := range 8 {
		if got := s.hitCount(fmt.Sprintf("/m/a/s%d.m4s", i)); got != 1 {
			t.Fatalf("segment %d of a requested %d times, want 1", i, got)
		}
	}
}

// keyOf returns the Key of track id of an HLS master playlist.
func keyOf(t *testing.T, master, id string) string {
	t.Helper()
	pl, err := media.ParseHLS(nil, []byte(master))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range pl.Options() {
		if tr.ID == id {
			return tr.Key
		}
	}
	t.Fatalf("no track %s", id)
	return ""
}

// TestResumeFollowsTheChosenVariant: with the key of the chosen track (as ProbeMP4
// listed it), a resumed job follows variant b to its new position: it keeps the fragments of b
// on disk, fetches only the rest of b and nothing of a. A key the manifest no longer has
// fails the selection instead of downloading another quality.
func TestResumeFollowsTheChosenVariant(t *testing.T) {
	key := keyOf(t, masterAB, "v1")
	if key == "" || key == keyOf(t, masterAB, "v0") || key != keyOf(t, masterBA, "v0") {
		t.Fatalf("keys do not follow the variant: %q", key)
	}
	sel := Selection{VideoTrack: "v1", VideoKey: key}
	s, opts, cp, _, wantB := reorderFixture(t, sel)
	if cp.Tracks[0].TrackID != "v1" || cp.Tracks[0].Key != key {
		t.Fatalf("checkpoint = %+v", cp.Tracks[0])
	}
	sel.ManifestURL = s.url("/m/master.m3u8")

	info, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
	if err != nil || info.DefaultVideo != "v0" {
		t.Fatalf("ProbeMP4 = %+v, %v; want the 720p variant, now v0", info, err)
	}
	probedA := s.hitCount("/m/a/index.m3u8") // ProbeMP4 plans every variant
	var rec recorder
	if _, err := Run(testContext(t), opts, sel, cp, nil, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, wantB) {
		t.Fatalf("output: %d bytes, want variant b (%d bytes)", len(got), len(wantB))
	}
	for i := range 8 {
		want := 1
		if i == 4 {
			want = 2 // in flight when the job paused
		}
		if got := s.hitCount(fmt.Sprintf("/m/b/s%d.m4s", i)); got != want {
			t.Errorf("segment %d of b requested %d times, want %d", i, got, want)
		}
	}
	if n := s.hitCount("/m/b/init.mp4"); n != 1 {
		t.Errorf("init of b requested %d times", n)
	}
	if n := s.hitCount("/m/a/index.m3u8") - probedA; n != 0 {
		t.Errorf("Run requested the playlist of a %d times", n)
	}
	if last := rec.last(); last == nil || last.Tracks[0].TrackID != "v0" || last.Tracks[0].Key != key {
		t.Fatalf("checkpoint after resuming = %+v", last)
	}

	// The 720p variant is gone.
	s.add("/m/master.m3u8", []byte(strings.Replace(masterBA, "1280x720", "1920x1080", 1)), hlsType)
	for _, run := range []func() error{
		func() error { _, err := ProbeMP4(testContext(t), testFactory(t), sel, nil); return err },
		func() error {
			_, err := Run(testContext(t), testOptions(t, &fakeAssembler{}), sel, nil, nil, nil)
			return err
		},
	} {
		if err := run(); !errors.Is(err, ErrInvalidSelection) || !errors.Is(err, media.ErrNotFound) {
			t.Errorf("err = %v, want ErrInvalidSelection and media.ErrNotFound", err)
		}
	}
}

// TestRunAfterAnInterruptedCompletion: when the caller did not record a finished job
// (it died, or storing the completion failed), a later Run with the last checkpoint
// finds the output and succeeds without fetching or assembling anything again; one
// interrupted between writing the part file and renaming it only renames it.
func TestRunAfterAnInterruptedCompletion(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/w/", 6, 41)
	sel := Selection{ManifestURL: s.url("/w/index.m3u8")}
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	var rec recorder
	if c, err := Run(testContext(t), opts, sel, nil, nil, rec.onCheckpoint); err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	cp := rec.last()
	hits := func() (n int) {
		n = s.hitCount("/w/index.m3u8")
		for i := range 6 {
			n += s.hitCount(fmt.Sprintf("/w/seg%d.ts", i))
		}
		return n
	}
	before := hits()

	var rec2 recorder
	c, err := Run(testContext(t), opts, sel, cp, rec2.onProgress, rec2.onCheckpoint)
	if err != nil || c != "mp4" {
		t.Fatalf("second Run = %q, %v", c, err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output changed")
	}
	if hits() != before || asm.callCount() != 1 {
		t.Fatalf("second Run made %d requests and assembled %d times", hits()-before, asm.callCount())
	}

	// Interrupted between the assembly and the rename.
	if err := os.Rename(opts.OutPath, opts.OutPath+".dxpart"); err != nil {
		t.Fatal(err)
	}
	if c, err := Run(testContext(t), opts, sel, cp, nil, nil); err != nil || c != "mp4" {
		t.Fatalf("third Run = %q, %v", c, err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output differs after the rename")
	}
	if _, err := os.Stat(opts.OutPath + ".dxpart"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("part file left behind: %v", err)
	}
	if hits() != before || asm.callCount() != 1 {
		t.Fatalf("third Run made %d requests and assembled %d times", hits()-before, asm.callCount())
	}
	// A file that replaced the output is not taken for it: the job assembles again and
	// refuses to replace it.
	if err := os.WriteFile(opts.OutPath, []byte("someone else's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(testContext(t), opts, sel, cp, nil, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Run over a foreign file: err = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, opts.OutPath); !strings.HasPrefix(string(got), "someone") {
		t.Fatal("a foreign file was replaced")
	}
}
