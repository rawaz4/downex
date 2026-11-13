package sitejob

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"downex/internal/media"
	"downex/internal/sitemedia"
	"downex/internal/tools"
)

// noDirectJSON is a YouTube page as yt-dlp prints it with the manifests skipped when the
// page has no direct MP4 video: only a WebM format.
const noDirectJSON = `{"id": "aaaaaaaaaaa", "extractor_key": "Youtube", "title": "A video", "duration": 12.5,
 "formats": [{"format_id": "248", "protocol": "https", "url": "https://rr1.googlevideo.com/videoplayback?itag=248",
  "ext": "webm", "container": "webm_dash", "vcodec": "vp9", "acodec": "none", "height": 1080}]}`

// manifestJSON is the same page with its HLS manifest kept: an MP4 variant appears.
const manifestJSON = `{"id": "aaaaaaaaaaa", "extractor_key": "Youtube", "title": "A video", "duration": 12.5,
 "formats": [{"format_id": "248", "protocol": "https", "url": "https://rr1.googlevideo.com/videoplayback?itag=248",
  "ext": "webm", "container": "webm_dash", "vcodec": "vp9", "acodec": "none", "height": 1080},
  {"format_id": "96", "protocol": "m3u8_native", "ext": "mp4", "vcodec": "avc1.640028", "acodec": "mp4a.40.2",
   "height": 1080, "tbr": 4500,
   "url": "https://manifest.googlevideo.com/api/manifest/hls_playlist/id/96/index.m3u8",
   "manifest_url": "https://manifest.googlevideo.com/api/manifest/hls_variant/id/file/index.m3u8"}]}`

// argsOf returns the arguments a run of the fake tools' lease gets for page.
func argsOf(f *fakeTools, page string, manifests bool) []string {
	return buildArgs(argsSpec{CacheDir: filepath.Join(f.root, "cache", "yt-dlp"), PageURL: page, Manifests: manifests})
}

// A YouTube page without a direct MP4 video while its manifests are skipped is resolved
// exactly once more with them (the same copy, the same arguments otherwise), so the
// manifest fallback still works; the final result is cached as usual.
func TestResolveWithManifestsWhenNoDirectVideo(t *testing.T) {
	f := newFakeTools(t, "manifests")
	f.setOutput(t, noDirectJSON)
	f.setManifestsOutput(t, manifestJSON)
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	info, err := r.Resolve(ctx, Request{PageURL: testPage})
	if err != nil {
		t.Fatal(err)
	}
	off, err := sitemedia.Offers(info, "www.youtube.com")
	if err != nil || off.Format != sitemedia.FormatHLS {
		t.Fatalf("offer = %+v, %v; want the manifest fallback", off, err)
	}
	recs := f.records(t)
	if len(recs) != 2 {
		t.Fatalf("%d runs, want 2", len(recs))
	}
	if !slices.Equal(recs[0].Args, argsOf(f, testPage, false)) || !slices.Equal(recs[1].Args, argsOf(f, testPage, true)) {
		t.Errorf("arguments\n first %q\nsecond %q", recs[0].Args, recs[1].Args)
	}
	if got := f.outcomes(); !slices.Equal(got, []tools.Outcome{tools.OK, tools.OK}) {
		t.Errorf("reports %v, want [OK OK]", got)
	}
	if n := f.acquireCalls(); n != 1 {
		t.Errorf("Acquire called %d times, want 1 (both runs on one lease)", n)
	}
	if entries, _ := os.ReadDir(f.workRoot()); len(entries) != 0 {
		t.Errorf("work folders were left behind: %d entries", len(entries))
	}
	if again, err := r.Resolve(ctx, Request{PageURL: testPage}); err != nil || again != info || len(f.records(t)) != 2 {
		t.Errorf("a later request: %p, %v, %d runs; want the cached %p", again, err, len(f.records(t)), info)
	}
}

// The page is resolved with its manifests at most once: when that still gives no direct
// MP4 video, its result is the answer (refused by the offer), and it is cached.
func TestResolveWithManifestsAtMostOnce(t *testing.T) {
	f := newFakeTools(t, "json")
	f.setOutput(t, noDirectJSON)
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	info, err := r.Resolve(ctx, Request{PageURL: testPage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sitemedia.Offers(info, "www.youtube.com"); !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("offer: %v, want media_unsupported", err)
	}
	recs := f.records(t)
	if len(recs) != 2 || !slices.Equal(recs[1].Args, argsOf(f, testPage, true)) {
		t.Fatalf("%d runs (%v), want 2, the second with the manifests", len(recs), recs)
	}
	if _, err := r.Resolve(ctx, Request{PageURL: testPage}); err != nil || len(f.records(t)) != 2 {
		t.Errorf("the result was not cached: %d runs, %v", len(f.records(t)), err)
	}
}

// Only YouTube pages are resolved again: the skip changes nothing for other sites, and a
// YouTube page with a direct MP4 video or one yt-dlp refused needs no manifests.
func TestResolveWithManifestsOnlyWhenItHelps(t *testing.T) {
	for _, c := range []struct{ name, out string }{
		{"other site", `{"id": "v1", "extractor_key": "Vimeo", "title": "A video",
 "formats": [{"format_id": "w", "protocol": "https", "url": "https://cdn.vimeo.example/v.webm", "ext": "webm",
  "vcodec": "vp9", "acodec": "opus", "height": 720}]}`},
		{"direct video", pageJSON},
		{"live", `{"id": "aaaaaaaaaaa", "extractor_key": "Youtube", "live_status": "is_live"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeTools(t, "manifests")
			f.setOutput(t, c.out)
			f.setManifestsOutput(t, manifestJSON)
			r := newTestResolver(t, f, ResolverConfig{})
			_, _ = r.Resolve(testContext(t), Request{PageURL: testPage})
			if recs := f.records(t); len(recs) != 1 || !skipsManifests(recs[0].Args) {
				t.Errorf("%d runs (%v), want 1 with the manifests skipped", len(recs), recs)
			}
		})
	}
}
