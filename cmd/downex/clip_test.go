package main

import (
	"context"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// "media --from/--to" sends the part; --chapter N reads the page's chapters first;
// --list prints them (docs/MEDIA-SPEC.md §8, B1).
func TestMediaClipFlags(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	if r := h.run(ctx, "media", testManifest, "--from", "1:20", "--to", "2m45s", "--paused"); r.code != exitOK {
		t.Fatalf("media --from --to: %v", r)
	}
	var got *downexv1.AddMediaRequest
	f.do(func() { got = f.mediaAdds[0] })
	if c := got.GetClip(); c.GetStartMs() != 80_000 || c.GetEndMs() != 165_000 || c.GetChapter() != "" {
		t.Errorf("clip %v", c)
	}
	if r := h.run(ctx, "media", testManifest, "--from", "80.5"); r.code != exitOK {
		t.Fatalf("media --from: %v", r)
	}
	f.do(func() { got = f.mediaAdds[1] })
	if c := got.GetClip(); c.GetStartMs() != 80_500 || c.GetEndMs() != 0 {
		t.Errorf("open end clip %v", c)
	}

	// A web page's chapters.
	f.do(func() {
		f.probe = sampleSiteProbe()
		f.probe.Chapters = []*downexv1.Chapter{{StartMs: 0, EndMs: 80_000, Title: "Intro"}, {StartMs: 80_000, EndMs: 212_000, Title: "Results"}}
	})
	r := h.run(ctx, "media", "--site", testPage, "--list")
	if r.code != exitOK || !strings.Contains(r.stdout, "Chapters:\n") || !strings.Contains(r.stdout, "  2  Results  1:20 – 3:32") {
		t.Errorf("--list chapters:\n%s", r.stdout)
	}
	if r := h.run(ctx, "media", "--site", testPage, "--chapter", "2"); r.code != exitOK {
		t.Fatalf("media --chapter: %v", r)
	}
	f.do(func() { got = f.mediaAdds[len(f.mediaAdds)-1] })
	if c := got.GetClip(); c.GetStartMs() != 80_000 || c.GetEndMs() != 212_000 || c.GetChapter() != "Results" {
		t.Errorf("chapter clip %v", c)
	}
}

func TestMediaClipArgumentErrors(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "1:2"}, "--from: Enter a time like 1:20 or 1:02:03."},
		{[]string{"--to", "soon"}, "--to: Enter a time like 1:20 or 1:02:03."},
		{[]string{"--from", "1:00", "--chapter", "1"}, "--chapter cannot be combined with --from or --to"},
		{[]string{"--chapter", "0"}, "--chapter wants a chapter number from 1"},
		{[]string{"--list", "--from", "1:00"}, "--list cannot be combined with --from"},
		{[]string{"--chapter", "1"}, "--chapter: this video has no chapters"},
	} {
		r := h.run(ctx, append([]string{"media", testManifest}, c.args...)...)
		if r.code == exitOK || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: %v, want %q", c.args, r, c.want)
		}
	}
	f.do(func() {
		if len(f.mediaAdds) != 0 {
			t.Errorf("refused flags added %d downloads", len(f.mediaAdds))
		}
	})
}

// "confirm --from/--to/--whole/--chapter" sends the part.
func TestConfirmClip(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		mi := sampleSiteProbe()
		mi.Chapters = []*downexv1.Chapter{{StartMs: 0, EndMs: 80_000, Title: "Intro"}}
		f.downloads[5] = &downexv1.Download{Id: 5, Url: testPage, Dir: "/v", Filename: "show.mp4", Size: -1,
			State: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
			Media: mi}
	})
	ctx := context.Background()
	for _, args := range [][]string{{"--from", "10", "--to", "20"}, {"--whole"}, {"--chapter", "1"}} {
		f.do(func() { f.downloads[5].State = downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION })
		if r := h.run(ctx, append([]string{"confirm", "5", "--later"}, args...)...); r.code != exitOK {
			t.Fatalf("confirm %v: %v", args, r)
		}
	}
	c := f.rec().confirms
	if len(c) != 3 || c[0].GetClip().GetStartMs() != 10_000 || c[0].GetClip().GetEndMs() != 20_000 ||
		c[1].GetClip() == nil || c[1].GetClip().GetStartMs() != 0 || c[1].GetClip().GetEndMs() != 0 ||
		c[2].GetClip().GetEndMs() != 80_000 || c[2].GetClip().GetChapter() != "Intro" {
		t.Errorf("confirms %v", c)
	}
	if r := h.run(ctx, "confirm", "5", "--whole", "--from", "1"); r.code == exitOK {
		t.Error("--whole with --from was accepted")
	}
}

// info prints the part, with its keyframe once complete (B6).
func TestPartText(t *testing.T) {
	for _, c := range []struct {
		clip clipView
		want string
	}{
		{clipView{StartMs: 80_000, EndMs: 165_000}, "1:20 – 2:45"},
		{clipView{StartMs: 80_000}, "1:20 – 12:34"},
		{clipView{StartMs: 80_000, EndMs: 165_000, KeyframeMs: 77_400, FetchEndMs: 168_000}, "1:20 – 2:45 (keyframe 1:17.4)"},
		{clipView{StartMs: 80_000, EndMs: 165_000, KeyframeMs: 83_000, FetchEndMs: 168_000}, "1:23 – 2:45 (first keyframe after 1:20)"},
	} {
		if got := partText(&c.clip, 754_000*ms(1)); got != c.want {
			t.Errorf("%+v: %q, want %q", c.clip, got, c.want)
		}
	}
}
