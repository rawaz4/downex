package daemon

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	"downex/internal/browserfetch"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
)

// Browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8): a site download added from the
// browser with browser_fetch has a fetch ticket (addMedia); its runs hand their requests to
// that browser when the site's video servers answer only browsers, or after its own
// requests were refused again (MediaRunOptions.Browser), and the browser puts back what it
// received. The ticket ends with the download (completion, removal) or with downexd.

// TakeBrowserFetch hands the browser the oldest request waiting for one of its tickets,
// waiting up to wait_ms for one, and names the tickets downexd no longer knows.
func (m *Manager) TakeBrowserFetch(ctx context.Context, r *downexv1.TakeBrowserFetchRequest) (*downexv1.TakeBrowserFetchResponse, error) {
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	f, ended, err := m.browser.Take(ctx, r.GetTickets(), time.Duration(r.GetWaitMs())*time.Millisecond)
	switch {
	case errors.Is(err, browserfetch.ErrInvalid):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		return nil, errInternal("take a browser fetch", err)
	}
	resp := &downexv1.TakeBrowserFetchResponse{EndedTickets: ended}
	if f != nil {
		resp.Fetch = &downexv1.BrowserFetch{Ticket: f.Ticket, FetchId: f.ID, Url: f.URL, Start: f.Start, End: f.End, Total: f.Total}
	}
	return resp, nil
}

// PutBrowserFetch writes what the browser received for a fetch into its run's answer;
// stop tells the browser to abort the fetch.
func (m *Manager) PutBrowserFetch(ctx context.Context, r *downexv1.PutBrowserFetchRequest) (*downexv1.PutBrowserFetchResponse, error) {
	var head *browserfetch.Head
	if h := r.GetHead(); h != nil {
		head = &browserfetch.Head{Status: int(h.GetStatus()), ContentType: h.GetContentType(), ContentRange: h.GetContentRange(),
			ContentLength: h.GetContentLength()}
		if head.ContentLength < -1 {
			head.ContentLength = -1
		}
	}
	stop, err := m.browser.Put(ctx, r.GetTicket(), r.GetFetchId(), r.GetSeq(), head, r.GetData(), r.GetEnd(), r.GetError())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &downexv1.PutBrowserFetchResponse{Stop: stop}, nil
}

// browserRun gives a site download's run its browser transport when a browser fetches for
// it, and marks the record when the run goes on through the browser (markBrowser).
func (m *Manager) browserRun(id model.DownloadID, opts *MediaRunOptions, markBrowser func()) {
	if !m.browser.Has(id) {
		return
	}
	opts.Browser = func(sizeOf func(url string) int64) transport.Factory { return m.browser.Factory(id, sizeOf) }
	opts.OnBrowser = markBrowser
}

func (s downloadService) TakeBrowserFetch(ctx context.Context, req *connect.Request[downexv1.TakeBrowserFetchRequest]) (*connect.Response[downexv1.TakeBrowserFetchResponse], error) {
	resp, err := s.m.TakeBrowserFetch(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s downloadService) PutBrowserFetch(ctx context.Context, req *connect.Request[downexv1.PutBrowserFetchRequest]) (*connect.Response[downexv1.PutBrowserFetchResponse], error) {
	resp, err := s.m.PutBrowserFetch(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
