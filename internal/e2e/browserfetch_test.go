package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// e2eBrowser stands for the extension's pump (docs/SITE-MEDIA-SPEC.md §2.8) over the real
// API: it takes the requests of its tickets with TakeBrowserFetch, makes each with the
// header only a browser's requests carry here, and puts back the answer in pieces of at
// most 256 KiB, each after the last was acknowledged.
type e2eBrowser struct {
	h       *harness
	mu      sync.Mutex
	tickets []string
	fetches atomic.Int64
	stops   atomic.Int64 // puts answered stop before the body ended
}

func (b *e2eBrowser) add(ticket string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tickets = append(b.tickets, ticket)
}

func (b *e2eBrowser) run(ctx context.Context) {
	sem := make(chan struct{}, 2)
	for ctx.Err() == nil {
		b.mu.Lock()
		tickets := append([]string(nil), b.tickets...)
		b.mu.Unlock()
		if len(tickets) == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		res, err := b.h.dl.TakeBrowserFetch(ctx, connect.NewRequest(&downexv1.TakeBrowserFetchRequest{Tickets: tickets, WaitMs: 2000}))
		if err != nil || res.Msg.GetFetch() == nil {
			<-sem
			if err != nil {
				time.Sleep(100 * time.Millisecond)
			}
			continue
		}
		go func(f *downexv1.BrowserFetch) {
			defer func() { <-sem }()
			b.fetch(ctx, f)
		}(res.Msg.GetFetch())
	}
}

func (b *e2eBrowser) put(ctx context.Context, r *downexv1.PutBrowserFetchRequest) bool {
	res, err := b.h.dl.PutBrowserFetch(ctx, connect.NewRequest(r))
	return err != nil || res.Msg.GetStop()
}

func (b *e2eBrowser) fetch(ctx context.Context, f *downexv1.BrowserFetch) {
	b.fetches.Add(1)
	base := &downexv1.PutBrowserFetchRequest{Ticket: f.GetTicket(), FetchId: f.GetFetchId()}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.GetUrl(), nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	rng := fmt.Sprintf("bytes=%d-", f.GetStart())
	if f.GetEnd() >= 0 {
		rng += fmt.Sprint(f.GetEnd())
	}
	req.Header.Set("Range", rng)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.put(ctx, &downexv1.PutBrowserFetchRequest{Ticket: base.Ticket, FetchId: base.FetchId, Error: "TypeError: Failed to fetch"})
		return
	}
	defer resp.Body.Close()
	head := &downexv1.BrowserFetchHead{Status: int32(resp.StatusCode), ContentType: resp.Header.Get("Content-Type"),
		ContentRange: resp.Header.Get("Content-Range"), ContentLength: resp.ContentLength}
	buf := make([]byte, 256<<10)
	for seq := int64(0); ; seq++ {
		n, rerr := io.ReadFull(resp.Body, buf)
		r := &downexv1.PutBrowserFetchRequest{Ticket: base.Ticket, FetchId: base.FetchId, Seq: seq, Data: buf[:n], End: rerr != nil}
		if seq == 0 {
			r.Head = head
		}
		if b.put(ctx, r) && !r.End {
			b.stops.Add(1)
			return
		}
		if r.End {
			return
		}
	}
}

// Browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8, §8.2): a video server that answers
// only browsers refuses downexd's own requests, the renewed ones too; the download, added
// from the browser with a fetch ticket, goes on through the browser (the stand-in pump over
// the real API) and is saved whole, byte for byte; a pause while the browser fetches stops
// its fetch, and the resumed run is fetched by the browser again from the start of the
// chunk's rest; a download added without a ticket fails with the bot-protection reason.
func TestSiteBrowserFetch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newSite(true, nil)
	st := newSiteTracks(t)
	page := s.talkPage("/watch/browser", st)
	d := h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.siteNetwork()
	s.srv.mu.Lock()
	s.srv.browserOnly = true
	s.srv.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := &e2eBrowser{h: h}
	go br.run(ctx)

	// Video chunks past the second are held until the pause.
	s.srv.setHold(func(r siteRequest) bool { return r.Browser && r.Format == "137" && chunk(r) >= 2 })
	actx, acancel := context.WithTimeout(ctx, 60*time.Second)
	res, err := h.dl.AddMedia(actx, connect.NewRequest(&downexv1.AddMediaRequest{PageUrl: page, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE,
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER, BrowserFetch: true, Dir: h.out}))
	acancel()
	if err != nil {
		t.Fatal(err)
	}
	ticket, id := res.Msg.GetFetchTicket(), res.Msg.GetDownload().GetId()
	if len(ticket) != 32 {
		t.Fatalf("fetch ticket %q", ticket)
	}
	if res.Msg.GetDownload().GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		if _, err := h.dl.Confirm(ctx, connect.NewRequest(&downexv1.ConfirmRequest{Id: id, Start: true})); err != nil {
			t.Fatal(err)
		}
	}
	br.add(ticket)
	// Wait until the browser's request for chunk 2 is held, then pause.
	deadline := time.Now().Add(progressTimeout)
	for held := false; !held; {
		for _, r := range s.srv.requests(0) {
			held = held || r.Browser && r.Format == "137" && chunk(r) >= 2 && r.Status == 0
		}
		if time.Now().After(deadline) {
			t.Fatalf("no browser request reached video chunk 2: %v, %+v", h.get(id), s.srv.requests(0))
		}
		time.Sleep(pollInterval)
	}
	h.cli("pause", idArg(id)).ok(t)
	h.waitState(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED)
	if !h.get(id).GetMedia().GetBrowserFetch() {
		t.Error("the paused record is not marked as fetched by the browser")
	}
	s.srv.setHold(nil)
	before := len(s.srv.requests(0))
	h.cli("resume", idArg(id)).ok(t)
	h.waitCompleted(id)
	checkMP4(t, filepath.Join(h.out, "An e2e talk.mp4"), st.video, st.audio)

	own, browser, refused := 0, 0, 0
	for _, r := range s.srv.requests(0) {
		switch {
		case !r.Browser:
			own++
			if r.Status != http.StatusForbidden {
				t.Errorf("downexd's own request %+v was not refused", r)
			}
		case r.Status == http.StatusForbidden:
			refused++
		default:
			browser++
		}
	}
	if own == 0 || browser == 0 || refused != 0 {
		t.Errorf("%d own requests, %d browser requests (%d refused); want own ones refused, the browser's answered", own, browser, refused)
	}
	for _, r := range s.srv.requests(before) {
		if !r.Browser {
			t.Errorf("the resumed run made its own request %+v (the record says the browser fetches)", r)
		}
	}
	if runs := s.runs(page); len(runs) < 2 {
		t.Errorf("%d extractions, want the add's and one renewal at least", len(runs))
	}

	// Without a ticket nothing fetches for it: the bot-protection reason, as before.
	h.cli("rm", idArg(id)).ok(t)
	id2 := h.cli("media", "--site", page, "-o", filepath.Join(h.root, "out2")).ok(t).id(t)
	if dl := h.waitFailed(id2); dl.GetErrorCode() != "site_error" {
		t.Errorf("a CLI download of a browser-only server: %v", dl)
	}
	h.stop(d)
}
