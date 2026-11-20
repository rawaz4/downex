package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"downex/internal/browserfetch"
	"downex/internal/daemon"
	"downex/internal/model"
	"downex/internal/played"
	"downex/internal/sitemedia"
	"downex/internal/transport"
)

// playedSel is a played selection of the test pair (or of the video alone).
func playedSel(srv *fileServer, withAudio bool) daemon.PlayedSelection {
	video, audio := pairVideo, pairAudio
	sel := daemon.PlayedSelection{Output: played.OutputCopy, Tracks: []model.PlayedTrack{
		{ID: "r1", Kind: "video", URL: srv.url("/v.mp4"), Size: int64(len(video.file())), Fragmented: true},
	}}
	if !withAudio {
		sel.Output = played.OutputRemux // a fragmented video alone is joined into a progressive MP4
	}
	if withAudio {
		sel.Output = played.OutputRemux
		sel.Tracks = append(sel.Tracks, model.PlayedTrack{ID: "r2", Kind: "audio", URL: srv.url("/a.mp4"), Size: int64(len(audio.file()))})
	}
	return sel
}

// A played video and its sound are saved by downexd's own requests and joined.
func TestPlayedRunJoin(t *testing.T) {
	video, audio := testPair(t)
	srv := newFileServer(t)
	srv.add("/v.mp4", video.file())
	srv.add("/a.mp4", audio.file())
	opts := runOptions(t, daemon.MediaRunOptions{})
	container, err := (&playedMedia{}).Run(siteContext(t), opts, playedSel(srv, true), nil, nil, nil)
	if err != nil || container != "mp4" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	checkOutput(t, opts.OutPath, video, audio)
	for _, r := range srv.requests() {
		if r.rng == "" {
			t.Fatalf("a request without a range: %+v", r)
		}
	}
}

// A video without sound is copied as it is.
func TestPlayedRunCopy(t *testing.T) {
	video, _ := testPair(t)
	srv := newFileServer(t)
	srv.add("/v.mp4", video.file())
	opts := runOptions(t, daemon.MediaRunOptions{})
	if _, err := (&playedMedia{}).Run(siteContext(t), opts, playedSel(srv, false), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkOutput(t, opts.OutPath, video)
}

// downexd's own requests are refused: the run goes on through the browser that added it
// (once marked), and a record marked so goes there from the start.
func TestPlayedRunRefusedToBrowser(t *testing.T) {
	for _, marked := range []bool{false, true} {
		video, audio := testPair(t)
		srv := newFileServer(t)
		srv.browserOnly = true
		srv.add("/v.mp4", video.file())
		srv.add("/a.mp4", audio.file())
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
		sel := playedSel(srv, true)
		sel.BrowserFetch = marked
		if _, err := (&playedMedia{}).Run(ctx, opts, sel, nil, nil, nil); err != nil {
			t.Fatalf("marked %v: %v", marked, err)
		}
		checkOutput(t, opts.OutPath, video, audio)
		if want := map[bool]int32{false: 1, true: 0}[marked]; marks.Load() != want {
			t.Errorf("marked %v: marked %d times", marked, marks.Load())
		}
		if tb.fetched.Load() == 0 {
			t.Errorf("marked %v: nothing went through the browser", marked)
		}
		if direct := len(srv.requests()) - int(tb.fetched.Load()); marked && direct != 0 {
			t.Errorf("a marked record made %d requests of its own", direct)
		}
	}
}

// Refused without a browser: the reason says to use the browser's button; refused by the
// browser too: the link expired. Neither names a sign-in.
func TestPlayedRunRefused(t *testing.T) {
	video, _ := testPair(t)
	srv := newFileServer(t)
	srv.add("/v.mp4", video.file())
	srv.deny["/v.mp4"] = 403
	_, err := (&playedMedia{}).Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), playedSel(srv, false), nil, nil, nil)
	if !errors.Is(err, sitemedia.ErrSiteError) || !strings.Contains(err.Error(), played.ReasonBrowserOnly) {
		t.Fatalf("no browser: %v", err)
	}

	b := browserfetch.New()
	ticket, _ := b.Bind(1)
	ctx := siteContext(t)
	(&testBrowser{}).run(ctx, t, b, ticket)
	opts := runOptions(t, daemon.MediaRunOptions{
		Browser: func(sizeOf func(string) int64) transport.Factory { return b.Factory(1, sizeOf) },
	})
	_, err = (&playedMedia{}).Run(ctx, opts, playedSel(srv, false), nil, nil, nil)
	if !errors.Is(err, sitemedia.ErrSiteError) || !strings.Contains(err.Error(), played.ReasonExpired) ||
		strings.Contains(strings.ToLower(err.Error()), "sign") {
		t.Fatalf("browser refused too: %v", err)
	}
}

// Review 2026-11-18 (Instagram and Facebook reels): the browser hides Content-Range from the
// page, with or without a known size: the run still saves the video, never "link expired".
func TestPlayedRunHiddenContentRange(t *testing.T) {
	for _, known := range []bool{true, false} {
		video, audio := testPair(t)
		srv := newFileServer(t)
		srv.browserOnly = true
		srv.add("/v.mp4", video.file())
		srv.add("/a.mp4", audio.file())
		b := browserfetch.New()
		ticket, _ := b.Bind(1)
		tb := &testBrowser{hideRange: true}
		ctx := siteContext(t)
		tb.run(ctx, t, b, ticket)
		opts := runOptions(t, daemon.MediaRunOptions{
			Browser: func(sizeOf func(string) int64) transport.Factory { return b.Factory(1, sizeOf) },
		})
		sel := playedSel(srv, true)
		sel.BrowserFetch = true
		if !known {
			for i := range sel.Tracks {
				sel.Tracks[i].Size = 0
			}
		}
		if _, err := (&playedMedia{}).Run(ctx, opts, sel, nil, nil, nil); err != nil {
			t.Fatalf("size known %v: %v", known, err)
		}
		checkOutput(t, opts.OutPath, video, audio)
	}
}

// Review 2026-11-18: downexd's own requests for a played file never reach this computer or a
// local network, whatever name the address uses (a public name can resolve to 127.0.0.1).
func TestPlayedRunPublicOnly(t *testing.T) {
	video, _ := testPair(t)
	srv := newFileServer(t)
	srv.add("/v.mp4", video.file())
	sel := playedSel(srv, false)
	sel.Tracks[0].URL = strings.Replace(sel.Tracks[0].URL, "127.0.0.1", "localhost", 1)
	_, err := (&playedMedia{publicOnly: true}).Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil)
	if !errors.Is(err, transport.ErrLocalAddress) {
		t.Fatalf("got %v", err)
	}
	if n := len(srv.requests()); n != 0 {
		t.Fatalf("the local server saw %d requests", n)
	}
}
