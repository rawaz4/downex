package daemon

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/browserfetch"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
)

// Browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8): a site download added from the
// browser with browser_fetch gets a fetch ticket, its runs get the browser transport, a run
// that goes on through the browser marks the record, and the ticket ends with the
// download; a probe says when the site's video servers answer only browsers.
func TestSiteFetchTicket(t *testing.T) {
	fs := newFakeSite(1)
	fs.probe.BrowserFetch = true
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})

	info, err := h.dl.ProbeMedia(ctxT(t), connect.NewRequest(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage}))
	if err != nil || !info.Msg.GetMedia().GetBrowserFetch() {
		t.Fatalf("probe = %v, %v; want browser_fetch", info, err)
	}

	add := func(req *downexv1.AddMediaRequest) *downexv1.AddMediaResponse {
		t.Helper()
		res, err := h.dl.AddMedia(ctxT(t), connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}
	// From the desktop or the CLI there is no browser to fetch: no ticket.
	cli := add(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: downexv1.AddSource_ADD_SOURCE_CLI,
		BrowserFetch: true, StartPaused: true})
	if cli.GetFetchTicket() != "" || h.m.browser.Has(model.DownloadID(cli.GetDownload().GetId())) {
		t.Fatalf("a CLI add got a ticket %q", cli.GetFetchTicket())
	}
	// Without browser_fetch: none either.
	plain := add(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser, StartPaused: true})
	if plain.GetFetchTicket() != "" {
		t.Fatalf("an add without browser_fetch got a ticket")
	}

	fs.set(func(f *fakeSite) {
		f.onRun = func(o MediaRunOptions) {
			if o.OnBrowser != nil {
				o.OnBrowser() // the run went on through the browser
			}
		}
	})
	res := add(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser, BrowserFetch: true})
	if !browserfetch.ValidTicket(res.GetFetchTicket()) || !res.GetDownload().GetMedia().GetBrowserFetch() {
		t.Fatalf("browser add: ticket %q, media %v", res.GetFetchTicket(), res.GetDownload().GetMedia())
	}
	id := res.GetDownload().GetId()
	if res.GetDownload().GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: id, Start: true}); err != nil {
			t.Fatal(err)
		}
	}
	done := h.waitCompleted(id)
	if !done.GetMedia().GetBrowserFetch() {
		t.Error("the completed record is not marked as fetched by the browser")
	}
	_, runs := fs.calls()
	if len(runs) != 1 || runs[0].opts.Browser == nil || !runs[0].sel.BrowserFetch {
		t.Fatalf("runs = %+v, want one with the browser transport", runs)
	}
	if h.m.browser.Has(model.DownloadID(id)) {
		t.Error("the ticket outlived the completed download")
	}

	// A removed download's ticket ends; a take lists it as ended.
	paused := add(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser, BrowserFetch: true,
		StartPaused: true})
	if _, err := h.dl.Remove(ctxT(t), connect.NewRequest(&downexv1.RemoveRequest{Id: paused.GetDownload().GetId()})); err != nil {
		t.Fatal(err)
	}
	take, err := h.dl.TakeBrowserFetch(ctxT(t), connect.NewRequest(&downexv1.TakeBrowserFetchRequest{Tickets: []string{paused.GetFetchTicket()}}))
	if err != nil || take.Msg.GetFetch() != nil || len(take.Msg.GetEndedTickets()) != 1 {
		t.Fatalf("take after the removal = %v, %v", take, err)
	}
	// Invalid takes and puts are InvalidArgument.
	if _, err := h.dl.TakeBrowserFetch(ctxT(t), connect.NewRequest(&downexv1.TakeBrowserFetchRequest{Tickets: []string{"x"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad take: %v", err)
	}
	if _, err := h.dl.PutBrowserFetch(ctxT(t), connect.NewRequest(&downexv1.PutBrowserFetchRequest{Ticket: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad put: %v", err)
	}
}

// The fetch ticket of a download added from the browser is kept with its record: a new
// downexd binds it again, so the browser that still holds it serves a resume; a completed
// download's ticket is not kept (docs/SITE-MEDIA-SPEC.md §2.8, amendment 2026-11-19).
func TestFetchTicketSurvivesRestart(t *testing.T) {
	st := store.NewMemory()
	fs := newFakeSite(1)
	fs.probe.BrowserFetch = true
	h := newSiteHarness(t, harnessOptions{st: st}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	add := func(req *downexv1.AddMediaRequest) *downexv1.AddMediaResponse {
		t.Helper()
		res, err := h.dl.AddMedia(ctxT(t), connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}
	paused := add(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser, BrowserFetch: true,
		StartPaused: true})
	done := add(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser, BrowserFetch: true})
	if done.GetDownload().GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: done.GetDownload().GetId(), Start: true}); err != nil {
			t.Fatal(err)
		}
	}
	h.waitCompleted(done.GetDownload().GetId())
	if rec := storedState(t, st, paused.GetDownload().GetId()); rec.Media.FetchTicket != paused.GetFetchTicket() {
		t.Fatalf("stored ticket %q, want %q", rec.Media.FetchTicket, paused.GetFetchTicket())
	}
	if rec := storedState(t, st, done.GetDownload().GetId()); rec.Media.FetchTicket != "" {
		t.Errorf("a completed download kept its ticket")
	}

	m2, err := New(ctxT(t), Config{Store: st, Transport: h.m.cfg.Transport, DownloadsDir: func() (string, error) { return h.dir, nil },
		Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m2.Shutdown(context.Background()) }()
	if !m2.browser.Has(model.DownloadID(paused.GetDownload().GetId())) || m2.browser.Has(model.DownloadID(done.GetDownload().GetId())) {
		t.Fatal("the new downexd did not bind the stored ticket of the unfinished download only")
	}
	_, ended, err := m2.browser.Take(ctxT(t), []string{paused.GetFetchTicket(), done.GetFetchTicket()}, 0)
	if err != nil || !slices.Equal(ended, []string{done.GetFetchTicket()}) {
		t.Fatalf("take after the restart: ended %v, %v", ended, err)
	}
}
