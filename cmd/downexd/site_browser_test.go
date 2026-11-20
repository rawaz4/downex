package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/browserfetch"
	"downex/internal/daemon"
	"downex/internal/sitemedia"
	"downex/internal/transport"
)

// testBrowser stands for the extension (docs/SITE-MEDIA-SPEC.md §2.8): it takes the
// requests of its tickets, makes them with the header only a browser's requests carry
// here, and puts back what it received in pieces.
type testBrowser struct {
	fetched atomic.Int64 // requests made
	mu      sync.Mutex
	totals  []int64 // the totals handed to it
	// hideRange: the page cannot read Content-Range (a cross-origin answer that does not
	// expose it, as Instagram's and Facebook's video servers answer).
	hideRange bool
}

func (tb *testBrowser) run(ctx context.Context, t *testing.T, b *browserfetch.Broker, ticket string) {
	t.Helper()
	go func() {
		for ctx.Err() == nil {
			f, _, err := b.Take(ctx, []string{ticket}, time.Second)
			if err != nil || f == nil {
				continue
			}
			tb.mu.Lock()
			tb.totals = append(tb.totals, f.Total)
			tb.mu.Unlock()
			go tb.fetch(ctx, b, f)
		}
	}()
}

func (tb *testBrowser) fetch(ctx context.Context, b *browserfetch.Broker, f *browserfetch.Fetch) {
	tb.fetched.Add(1)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	if f.End >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", f.Start, f.End))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", f.Start))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Put(ctx, f.Ticket, f.ID, 0, nil, nil, false, err.Error())
		return
	}
	defer resp.Body.Close()
	head := &browserfetch.Head{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"),
		ContentRange: resp.Header.Get("Content-Range"), ContentLength: resp.ContentLength}
	if tb.hideRange {
		head.ContentRange = ""
	}
	buf := make([]byte, 100_000)
	for seq := int64(0); ; seq++ {
		n, rerr := io.ReadFull(resp.Body, buf)
		end := rerr != nil
		var h *browserfetch.Head
		if seq == 0 {
			h = head
		}
		stop, _ := b.Put(ctx, f.Ticket, f.ID, seq, h, buf[:n], end, "")
		if stop || end {
			return
		}
	}
}

// A run whose video server answers only browsers is refused, renewed, refused again, and
// goes on through the browser that added it: the record is marked once and the video is
// saved whole; a run of a record marked so fetches through the browser from the start.
func TestSiteRunGoesThroughBrowser(t *testing.T) {
	for _, marked := range []bool{false, true} {
		srv := newFileServer(t)
		srv.browserOnly = true
		old, renewed := pairPage(t, srv, "/old"), pairPage(t, srv, "/new")
		res := &fakeResolver{pages: []string{old, renewed}}
		s := newSiteMedia(res)
		b := browserfetch.New()
		ticket, _ := b.Bind(1)
		tb := &testBrowser{}
		ctx := siteContext(t)
		tb.run(ctx, t, b, ticket)
		var marks atomic.Int32
		opts := runOptions(t, daemon.MediaRunOptions{
			Browser:   func(sizeOf func(string) int64) transport.Factory { return b.Factory(1, sizeOf) },
			OnBrowser: func() { marks.Add(1) },
		})
		sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
			AudioTrack: "140", VideoKey: keyOf(t, old, "137"), AudioKey: keyOf(t, old, "140"), BrowserFetch: marked}
		if _, err := s.Run(ctx, opts, sel, nil, nil, nil); err != nil {
			t.Fatalf("marked %v: Run = %v", marked, err)
		}
		checkOutput(t, opts.OutPath, pairVideo, pairAudio)
		want := []bool{false, true}
		if marked {
			want = []bool{false}
		}
		if got := res.freshness(); !slices.Equal(got, want) {
			t.Errorf("marked %v: resolutions %v, want %v", marked, got, want)
		}
		if n := marks.Load(); n != map[bool]int32{false: 1, true: 0}[marked] {
			t.Errorf("marked %v: the record was marked %d times", marked, n)
		}
		if tb.fetched.Load() == 0 {
			t.Errorf("marked %v: nothing went through the browser", marked)
		}
		if marked {
			for _, r := range srv.requests() {
				if r.rng == "" {
					t.Errorf("an unranged request %s", r.path)
				}
			}
			tb.mu.Lock()
			totals := slices.Clone(tb.totals)
			tb.mu.Unlock()
			if !slices.Contains(totals, int64(len(pairVideo.file()))) {
				t.Errorf("totals %v: the video's exact size never reached the browser", totals)
			}
		}
	}
}

// A known browser-only site (TikTok) added from the browser goes through it at once; one
// that no browser fetches for fails with the reason that says to use the browser.
func TestSiteRunBrowserOnly(t *testing.T) {
	srv := newFileServer(t)
	srv.browserOnly = true
	page := strings.Replace(pairPage(t, srv, "/p"), `"extractor_key":"Youtube"`, `"extractor_key":"TikTok"`, 1)
	if !strings.Contains(page, `"TikTok"`) {
		t.Fatal("cannot make the page TikTok's")
	}
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}

	_, err := newSiteMedia(&fakeResolver{pages: []string{page, page}}).Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil)
	if !errors.Is(err, sitemedia.ErrSiteError) || err.Error() != sitemedia.ReasonBrowserOnly {
		t.Fatalf("no browser: Run = %v, want %q", err, sitemedia.ReasonBrowserOnly)
	}

	srv2 := newFileServer(t)
	srv2.browserOnly = true
	page = strings.Replace(pairPage(t, srv2, "/p"), `"extractor_key":"Youtube"`, `"extractor_key":"TikTok"`, 1)
	b := browserfetch.New()
	ticket, _ := b.Bind(1)
	ctx := siteContext(t)
	(&testBrowser{}).run(ctx, t, b, ticket)
	var marks atomic.Int32
	opts := runOptions(t, daemon.MediaRunOptions{
		Browser:   func(sizeOf func(string) int64) transport.Factory { return b.Factory(1, sizeOf) },
		OnBrowser: func() { marks.Add(1) },
	})
	res := &fakeResolver{pages: []string{page}}
	if _, err := newSiteMedia(res).Run(ctx, opts, sel, nil, nil, nil); err != nil {
		t.Fatalf("with a browser: Run = %v", err)
	}
	checkOutput(t, opts.OutPath, pairVideo, pairAudio)
	if got := res.freshness(); !slices.Equal(got, []bool{false}) {
		t.Errorf("resolutions %v, want only the run's", got)
	}
	if marks.Load() != 1 {
		t.Errorf("marked %d times, want once", marks.Load())
	}
}

// A run whose browser no longer takes its requests fails at once with the reason that
// says so (never retried); one whose browser got a page instead of the video too.
func TestSiteRunBrowserGone(t *testing.T) {
	srv := newFileServer(t)
	srv.browserOnly = true
	page := pairPage(t, srv, "/p")
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140"), BrowserFetch: true}
	b := browserfetch.NewWithTimeouts(100*time.Millisecond, time.Second)
	b.Bind(1)
	opts := runOptions(t, daemon.MediaRunOptions{
		Browser: func(sizeOf func(string) int64) transport.Factory { return b.Factory(1, sizeOf) },
	})
	start := time.Now()
	_, err := newSiteMedia(&fakeResolver{pages: []string{page}}).Run(siteContext(t), opts, sel, nil, nil, nil)
	if err == nil || !errors.Is(err, sitemedia.ErrSiteError) || err.Error() != sitemedia.ReasonBrowserGone {
		t.Fatalf("Run = %v, want %q", err, sitemedia.ReasonBrowserGone)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("failed after %v: retried", d)
	}

	ticket, _ := b.Bind(2)
	ctx := siteContext(t)
	go func() {
		for ctx.Err() == nil {
			if f, _, _ := b.Take(ctx, []string{ticket}, time.Second); f != nil {
				b.Put(ctx, f.Ticket, f.ID, 0, &browserfetch.Head{Status: 200, ContentType: "text/html", ContentLength: 5}, []byte("login"), true, "")
			}
		}
	}()
	opts = runOptions(t, daemon.MediaRunOptions{
		Browser: func(sizeOf func(string) int64) transport.Factory { return b.Factory(2, sizeOf) },
	})
	_, err = newSiteMedia(&fakeResolver{pages: []string{page}}).Run(ctx, opts, sel, nil, nil, nil)
	if err == nil || !errors.Is(err, sitemedia.ErrSiteError) || !strings.Contains(err.Error(), sitemedia.ReasonBrowserNotMedia) {
		t.Fatalf("a page: Run = %v, want %q", err, sitemedia.ReasonBrowserNotMedia)
	}
}

// A quality the site refuses to the browser too (403 again after the renewal, as TikTok answers
// an address its player never fetched) fails with the browser's reason, never "yt-dlp could not
// read this page": the page was read; the browser's request was refused (docs/SITE-MEDIA-SPEC.md
// §2.8).
func TestSiteRunBrowserRefused(t *testing.T) {
	srv := newFileServer(t)
	srv.browserOnly = true
	page := strings.Replace(pairPage(t, srv, "/p"), `"extractor_key":"Youtube"`, `"extractor_key":"TikTok"`, 1)
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}
	b := browserfetch.New()
	ticket, _ := b.Bind(1)
	ctx := siteContext(t)
	var refused atomic.Int32
	go func() {
		for ctx.Err() == nil {
			if f, _, _ := b.Take(ctx, []string{ticket}, time.Second); f != nil {
				refused.Add(1)
				b.Put(ctx, f.Ticket, f.ID, 0, &browserfetch.Head{Status: 403, ContentType: "text/html", ContentLength: 6}, []byte("denied"), true, "")
			}
		}
	}()
	opts := runOptions(t, daemon.MediaRunOptions{
		Browser: func(sizeOf func(string) int64) transport.Factory { return b.Factory(1, sizeOf) },
	})
	_, err := newSiteMedia(&fakeResolver{pages: []string{page, page}}).Run(ctx, opts, sel, nil, nil, nil)
	if err == nil || !errors.Is(err, sitemedia.ErrSiteError) || err.Error() != sitemedia.ReasonBrowserRefused {
		t.Fatalf("Run = %v, want %q", err, sitemedia.ReasonBrowserRefused)
	}
	if refused.Load() == 0 {
		t.Fatal("nothing went through the browser")
	}
}
