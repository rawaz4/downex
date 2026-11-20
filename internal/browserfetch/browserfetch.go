// Package browserfetch is the browser transport of browser-fetched saves
// (docs/SITE-MEDIA-SPEC.md §2.8): some sites' video servers answer only web browsers, so
// the user's own browser makes a download's requests, the same ones its player makes, and
// hands the bytes back. A Broker keeps the fetch ticket of each download a browser fetches
// for, the requests its run makes (Factory, a transport.Factory) until the browser takes
// them (Take), and writes what the browser received (Put) into the requests' answers, which
// the run reads and checks exactly as it does its own.
//
// The browser is reached only through a ticket: 16 random bytes, kept in memory, made for one
// download (Bind) and ended with it (Forget). Nothing is stored. Memory is bounded: a put
// carries at most MaxPiece bytes and blocks until the run has read them (the backpressure),
// so a fetch holds at most one piece.
package browserfetch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

// Bounds (docs/SITE-MEDIA-SPEC.md §2.8).
const (
	// Lanes is how many requests a run on the browser transport makes at once.
	Lanes = 2
	// MaxPiece bounds the bytes of one put.
	MaxPiece = 256 << 10
	// MaxTickets bounds the tickets kept; the oldest download's goes first.
	MaxTickets = 1024
	// MaxTakeTickets bounds the tickets of one take.
	MaxTakeTickets = 64
	// MaxWait bounds how long a take waits for a request.
	MaxWait = 25 * time.Second
	// MaxError bounds the error text of a put.
	MaxError = 256
	// TakeTimeout is how long a request waits to be taken while no browser serves its ticket
	// before the run fails (ErrGone): no take named the ticket and no browser put anything.
	TakeTimeout = 30 * time.Second
	// HeadTimeout is how long a taken request waits for the head of the browser's answer.
	HeadTimeout = time.Minute
	// ticketBytes is the random length of a ticket (twice as many hex characters).
	ticketBytes = 16
)

// NoTab is the error a browser puts when no tab of the site is open to fetch from.
const NoTab = "no_tab"

var (
	// ErrGone fails a request that no browser takes in time, or one the browser cannot make
	// because no tab of the site is open: the browser no longer fetches for the download.
	// It is permanent (transport.ErrGone).
	ErrGone = fmt.Errorf("%w: the browser is no longer fetching this video for Downex", transport.ErrGone)
	// ErrNotMedia fails a request whose successful answer is not media (a page, an error
	// text): the browser got something else than the video. It is permanent.
	ErrNotMedia = fmt.Errorf("%w: the browser got a page instead of the video", transport.ErrGone)
	// ErrBrowser is a fetch that failed in the browser (its network, CORS); it is retried.
	ErrBrowser = errors.New("the browser could not fetch the video")
	// ErrInvalid refuses a take or put that breaks the contract (a bad ticket, too much data).
	ErrInvalid = errors.New("invalid browser fetch")

	errNoAnswer = errors.New("the browser did not answer the request in time")
	errTooLong  = errors.New("the browser received more bytes than the answer announced")
	errHead     = errors.New("the browser's answer has no valid head")
)

// Fetch is a request handed to the browser.
type Fetch struct {
	Ticket, ID, URL string
	Start, End      int64 // End -1 = to the end
	Total           int64 // the resource's exact size, 0 = unknown
}

// Head is the head of the browser's answer.
type Head struct {
	Status        int
	ContentType   string
	ContentRange  string
	ContentLength int64 // -1 = unknown
}

// Broker keeps the tickets and the requests waiting for the browser. It is safe for
// concurrent use.
type Broker struct {
	mu       sync.Mutex
	byTicket map[string]*session
	byID     map[model.DownloadID]*session
	wake     chan struct{} // closed and replaced whenever a request waits
	order    uint64
	put      time.Time // when a browser last put an answer's piece (any ticket)

	takeAfter, headAfter time.Duration
	random               io.Reader
}

// New returns an empty Broker.
func New() *Broker { return NewWithTimeouts(TakeTimeout, HeadTimeout) }

// NewWithTimeouts is New with other waits for a request to be taken and for its head
// (tests).
func NewWithTimeouts(take, head time.Duration) *Broker {
	return &Broker{
		byTicket: map[string]*session{}, byID: map[model.DownloadID]*session{}, wake: make(chan struct{}),
		takeAfter: take, headAfter: head, random: rand.Reader,
	}
}

type session struct {
	ticket  string
	id      model.DownloadID
	waiting []*fetch
	running map[string]*fetch
	gone    chan struct{} // closed by Forget
	named   time.Time     // when a take last named the ticket
}

// fetch is one request of a run.
type fetch struct {
	s     *session
	req   Fetch
	order uint64
	since time.Time       // when it began to wait
	taken chan struct{}   // closed when the browser takes it
	head  chan headResult // the head, or why there is none (buffered 1)
	pr    *io.PipeReader
	pw    *io.PipeWriter

	// Guarded by Broker.mu.
	seq  int64
	done bool

	// Guarded by wmu (the puts of one fetch come one at a time; a drop may race them).
	wmu   sync.Mutex
	limit int64 // the bytes the answer announced, -1 unknown
	wrote int64
}

type headResult struct {
	resp *transport.Response
	err  error
}

// Bind makes the fetch ticket of download id, or returns the one it has.
func (b *Broker) Bind(id model.DownloadID) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.byID[id]; ok {
		return s.ticket, nil
	}
	ticket, err := b.newTicketLocked()
	if err != nil {
		return "", err
	}
	b.bindLocked(id, ticket)
	return ticket, nil
}

// NewTicket makes a fresh ticket that is bound to no download yet (Restore binds it), so
// that a caller can store it with the download before the download has an id.
func (b *Broker) NewTicket() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.newTicketLocked()
}

func (b *Broker) newTicketLocked() (string, error) {
	buf := make([]byte, ticketBytes)
	if _, err := io.ReadFull(b.random, buf); err != nil {
		return "", fmt.Errorf("make a fetch ticket: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Restore binds ticket, kept with download id (NewTicket, or a ticket stored before
// downexd restarted), to that download, so that a browser that still holds it serves the
// download again (docs/SITE-MEDIA-SPEC.md §2.8, amendment 2026-11-19). It refuses an
// invalid ticket and one bound to another download; a download already bound keeps its
// ticket when it is the same, else the stored one replaces it.
func (b *Broker) Restore(id model.DownloadID, ticket string) error {
	if !ValidTicket(ticket) {
		return fmt.Errorf("%w: not a ticket", ErrInvalid)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.byTicket[ticket]; ok {
		if s.id != id {
			return fmt.Errorf("%w: the ticket is another download's", ErrInvalid)
		}
		return nil
	}
	if _, ok := b.byID[id]; ok {
		b.forgetLocked(id)
	}
	b.bindLocked(id, ticket)
	return nil
}

// bindLocked binds the unused ticket to download id, which has none.
func (b *Broker) bindLocked(id model.DownloadID, ticket string) {
	if len(b.byID) >= MaxTickets {
		oldest := slices.Min(keys(b.byID))
		b.forgetLocked(oldest)
	}
	s := &session{ticket: ticket, id: id, running: map[string]*fetch{}, gone: make(chan struct{})}
	b.byTicket[s.ticket], b.byID[id] = s, s
}

func keys(m map[model.DownloadID]*session) []model.DownloadID {
	out := make([]model.DownloadID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Has reports whether a browser fetches for download id (it has a ticket).
func (b *Broker) Has(id model.DownloadID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.byID[id]
	return ok
}

// Forget ends download id's ticket: its waiting and running requests fail with ErrGone,
// and a later take lists the ticket as ended.
func (b *Broker) Forget(id model.DownloadID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forgetLocked(id)
}

func (b *Broker) forgetLocked(id model.DownloadID) {
	s, ok := b.byID[id]
	if !ok {
		return
	}
	delete(b.byID, id)
	delete(b.byTicket, s.ticket)
	close(s.gone)
	for _, f := range s.waiting {
		f.done = true
		f.close(ErrGone)
	}
	for _, f := range s.running {
		f.done = true
		f.close(ErrGone)
	}
	s.waiting, s.running = nil, map[string]*fetch{}
}

// ValidTicket reports whether t has a ticket's syntax: 32 lowercase hex characters.
func ValidTicket(t string) bool {
	return len(t) == 2*ticketBytes && !strings.ContainsFunc(t, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') })
}

// Factory returns the transport of download id's runs: each Fetch waits to be taken by the
// browser and answers with what the browser received. sizeOf (may be nil) gives the exact
// size of the resource at an address (0 = unknown), which the browser uses only to find the
// same resource among those its player fetched.
func (b *Broker) Factory(id model.DownloadID, sizeOf func(url string) int64) transport.Factory {
	return factory{b: b, id: id, sizeOf: sizeOf}
}

type factory struct {
	b      *Broker
	id     model.DownloadID
	sizeOf func(string) int64
}

func (f factory) NewSlot() transport.Fetcher { return fetcher(f) }

type fetcher factory

func (f fetcher) Close() {}

// Fetch hands rr to the browser and returns its answer: ErrGone when no browser serves its
// ticket for the take timeout while it waits (Broker.expire) or the ticket ended,
// ErrNotMedia for a successful answer that is not media, ErrBrowser (retried) when the
// browser's fetch failed.
func (f fetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	if ctx == nil {
		return nil, fmt.Errorf("browserfetch: %w: nil context", transport.ErrInvalidRequest)
	}
	if rr.Method != "" && rr.Method != http.MethodGet {
		return nil, fmt.Errorf("browserfetch: %w: only GET is fetched by the browser", transport.ErrInvalidRequest)
	}
	if rr.Start < 0 || (rr.End >= 0 && rr.End < rr.Start) || rr.End < -1 {
		return nil, fmt.Errorf("browserfetch: %w: bad range", transport.ErrInvalidRequest)
	}
	var total int64
	if f.sizeOf != nil {
		total = max(f.sizeOf(rr.URL), 0)
	}
	ft, err := f.b.enqueue(f.id, Fetch{URL: rr.URL, Start: rr.Start, End: rr.End, Total: total})
	if err != nil {
		return nil, err
	}
	take := time.NewTimer(f.b.takeAfter)
	defer take.Stop()
wait:
	for {
		select {
		case <-ft.taken:
			break wait
		case <-ctx.Done():
			f.b.drop(ft, ctx.Err())
			return nil, ctx.Err()
		case <-take.C:
			left, expired := f.b.expire(ft)
			if expired {
				ft.close(ErrGone)
				return nil, ErrGone
			}
			take.Reset(left) // still served, or taken just now (the next select sees it)
		case <-ft.s.gone:
			return nil, ErrGone
		}
	}
	head := time.NewTimer(f.b.headAfter)
	defer head.Stop()
	select {
	case h := <-ft.head:
		if h.err != nil {
			f.b.drop(ft, h.err)
			return nil, h.err
		}
		stop := context.AfterFunc(ctx, func() { f.b.drop(ft, ctx.Err()) })
		h.resp.Body = &body{PipeReader: ft.pr, close: func() { stop(); f.b.drop(ft, errClosed) }}
		return h.resp, nil
	case <-ctx.Done():
		f.b.drop(ft, ctx.Err())
		return nil, ctx.Err()
	case <-head.C:
		f.b.drop(ft, errNoAnswer)
		return nil, errNoAnswer
	case <-ft.s.gone:
		return nil, ErrGone
	}
}

var errClosed = errors.New("the answer was closed")

// body is an answer's body: closing it ends the fetch (the browser's next put stops).
type body struct {
	*io.PipeReader
	close func()
	once  sync.Once
}

func (b *body) Close() error {
	b.once.Do(b.close)
	return nil
}

// enqueue adds a request of download id for the browser.
func (b *Broker) enqueue(id model.DownloadID, req Fetch) (*fetch, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.byID[id]
	if !ok {
		return nil, ErrGone
	}
	b.order++
	req.Ticket, req.ID = s.ticket, strconv.FormatUint(b.order, 10)
	pr, pw := io.Pipe()
	f := &fetch{s: s, req: req, order: b.order, since: time.Now(), taken: make(chan struct{}), head: make(chan headResult, 1), pr: pr, pw: pw, limit: -1}
	s.waiting = append(s.waiting, f)
	close(b.wake)
	b.wake = make(chan struct{})
	return f, nil
}

// expire ends a waiting f (true) when no browser served its ticket for takeAfter: no take
// named the ticket and no browser put anything since f began to wait. A browser whose
// transfer slots are all busy with other requests takes none until one is free, so its
// puts keep f waiting (docs/SITE-MEDIA-SPEC.md §2.8). Otherwise it answers how long f may
// still wait (at least 1 ms; f may have been taken meanwhile).
func (b *Broker) expire(f *fetch) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f.done || !slices.Contains(f.s.waiting, f) {
		return time.Millisecond, false
	}
	last := f.since
	if f.s.named.After(last) {
		last = f.s.named
	}
	if b.put.After(last) {
		last = b.put
	}
	if left := b.takeAfter - time.Since(last); left > 0 {
		return max(left, time.Millisecond), false
	}
	f.done = true
	f.s.waiting = slices.DeleteFunc(f.s.waiting, func(x *fetch) bool { return x == f })
	return 0, true
}

// drop ends f: it leaves its session's lists, its body reads fail with why, and the
// browser's next put of it answers stop.
func (b *Broker) drop(f *fetch, why error) {
	b.mu.Lock()
	if !f.done {
		f.done = true
		f.s.waiting = slices.DeleteFunc(f.s.waiting, func(x *fetch) bool { return x == f })
		if f.s.running[f.req.ID] == f {
			delete(f.s.running, f.req.ID)
		}
	}
	b.mu.Unlock()
	f.close(why)
}

// close ends f's pipe: the run's reads fail with why (after what was written), the
// browser's writes with io.ErrClosedPipe.
func (f *fetch) close(why error) { f.pw.CloseWithError(why) }

// Take returns the oldest request waiting for one of tickets, waiting up to wait for one,
// and the given tickets that no download has (ended). A nil Fetch: none came.
func (b *Broker) Take(ctx context.Context, tickets []string, wait time.Duration) (*Fetch, []string, error) {
	if len(tickets) == 0 || len(tickets) > MaxTakeTickets {
		return nil, nil, fmt.Errorf("%w: 1 to %d tickets", ErrInvalid, MaxTakeTickets)
	}
	for _, t := range tickets {
		if !ValidTicket(t) {
			return nil, nil, fmt.Errorf("%w: a ticket is 32 lowercase hex characters", ErrInvalid)
		}
	}
	if wait < 0 || wait > MaxWait {
		return nil, nil, fmt.Errorf("%w: wait 0 to %d ms", ErrInvalid, MaxWait.Milliseconds())
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	defer b.named(tickets) // a long poll serves its tickets until it returns
	for {
		b.mu.Lock()
		var (
			best  *fetch
			ended []string
		)
		now := time.Now()
		for _, t := range tickets {
			s, ok := b.byTicket[t]
			if !ok {
				if !slices.Contains(ended, t) {
					ended = append(ended, t)
				}
				continue
			}
			s.named = now // a browser serves the ticket: its requests keep waiting
			if len(s.waiting) > 0 && (best == nil || s.waiting[0].order < best.order) {
				best = s.waiting[0]
			}
		}
		if best != nil {
			s := best.s
			s.waiting = s.waiting[1:]
			s.running[best.req.ID] = best
			close(best.taken)
			out := best.req
			b.mu.Unlock()
			return &out, ended, nil
		}
		wake := b.wake
		b.mu.Unlock()
		if wait == 0 {
			return nil, ended, nil
		}
		select {
		case <-wake:
		case <-timer.C:
			return nil, ended, nil
		case <-ctx.Done():
			return nil, ended, ctx.Err()
		}
	}
}

// named marks the tickets a take named as served now.
func (b *Broker) named(tickets []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for _, t := range tickets {
		if s, ok := b.byTicket[t]; ok {
			s.named = now
		}
	}
}

// Put gives what the browser received for a taken fetch, in order (seq 0, 1, …): the head
// with seq 0, then pieces of the body (at most MaxPiece bytes each), then end; or the error
// that ended the fetch in the browser (NoTab when no tab of the site is open). It blocks until
// the run has read data. stop: the run needs no more of the fetch (it ended, was cancelled,
// or the put does not follow the last one).
func (b *Broker) Put(ctx context.Context, ticket, fetchID string, seq int64, head *Head, data []byte, end bool, errText string) (bool, error) {
	switch {
	case !ValidTicket(ticket):
		return true, fmt.Errorf("%w: a ticket is 32 lowercase hex characters", ErrInvalid)
	case len(data) > MaxPiece:
		return true, fmt.Errorf("%w: at most %d bytes a put", ErrInvalid, MaxPiece)
	case len(errText) > MaxError:
		return true, fmt.Errorf("%w: an error of at most %d bytes", ErrInvalid, MaxError)
	case seq < 0:
		return true, fmt.Errorf("%w: a negative seq", ErrInvalid)
	}
	b.mu.Lock()
	s := b.byTicket[ticket]
	var f *fetch
	if s != nil {
		f = s.running[fetchID]
	}
	if f == nil || f.done || seq != f.seq {
		b.mu.Unlock()
		return true, nil
	}
	f.seq++
	b.put = time.Now() // a browser is serving: requests waiting for a free slot keep waiting
	b.mu.Unlock()

	if seq == 0 {
		var h headResult
		switch {
		case errText != "":
			h.err = browserError(errText)
		case head == nil:
			h.err = errHead
		default:
			h.resp, h.err = b.response(f, head)
		}
		f.head <- h
		if h.err != nil {
			return true, nil
		}
	} else if head != nil {
		b.drop(f, errHead)
		return true, nil
	}
	if seq > 0 && errText != "" {
		b.drop(f, fmt.Errorf("%w: %s: %w", ErrBrowser, errText, io.ErrUnexpectedEOF))
		return true, nil
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if len(data) > 0 {
		n, over := int64(len(data)), false
		if f.limit >= 0 && f.wrote+n > f.limit {
			n, over = f.limit-f.wrote, true
		}
		// A put the relay gave up on ends the fetch; the chunk keeps what it has (§2.5).
		stop := context.AfterFunc(ctx, func() {
			f.pw.CloseWithError(fmt.Errorf("%w: the relay stopped: %w", ErrBrowser, io.ErrUnexpectedEOF))
		})
		_, err := f.pw.Write(data[:n])
		stop()
		if err != nil {
			b.drop(f, err)
			return true, nil
		}
		f.wrote += n
		if over {
			b.drop(f, errTooLong)
			return true, nil
		}
	}
	if !end {
		return false, nil
	}
	if f.limit >= 0 && f.wrote < f.limit {
		f.pw.CloseWithError(io.ErrUnexpectedEOF)
	} else {
		f.pw.Close()
	}
	b.mu.Lock()
	if f.s.running[f.req.ID] == f {
		delete(f.s.running, f.req.ID)
	}
	f.done = true
	b.mu.Unlock()
	return true, nil
}

// browserError is the failure the browser put: no tab of the site (ErrGone), else its fetch
// failed (ErrBrowser, retried).
func browserError(text string) error {
	if text == NoTab {
		return ErrGone
	}
	return fmt.Errorf("%w: %s", ErrBrowser, text)
}

// response checks a head and makes the answer the run reads (its body still to come).
func (b *Broker) response(f *fetch, h *Head) (*transport.Response, error) {
	if h.Status < 100 || h.Status > 599 {
		return nil, errHead
	}
	ct := mediaType(h.ContentType)
	if h.Status/100 == 2 && !mediaContent(ct) {
		return nil, fmt.Errorf("%w (%s)", ErrNotMedia, truncate(ct, 64))
	}
	hdr := http.Header{}
	if h.ContentType != "" {
		hdr.Set("Content-Type", h.ContentType)
	}
	resp := &transport.Response{StatusCode: h.Status, Header: hdr, ContentLength: -1, ContentType: ct, FinalURL: f.req.URL}
	contentRange := h.ContentRange
	if contentRange == "" && h.Status == http.StatusPartialContent && h.ContentLength > 0 {
		contentRange = hiddenRange(f.req, h.ContentLength)
	}
	if contentRange != "" {
		cr, err := transport.ParseContentRange(contentRange)
		if err != nil {
			return nil, errHead
		}
		hdr.Set("Content-Range", contentRange)
		if (h.Status == http.StatusPartialContent && cr.Start >= 0) || (h.Status == http.StatusRequestedRangeNotSatisfiable && cr.Start < 0) {
			resp.ContentRange = &cr
		}
	}
	limit := int64(-1)
	if h.ContentLength >= 0 {
		resp.ContentLength, limit = h.ContentLength, h.ContentLength
		hdr.Set("Content-Length", strconv.FormatInt(h.ContentLength, 10))
	} else if resp.ContentRange != nil && h.Status == http.StatusPartialContent {
		limit = resp.ContentRange.End - resp.ContentRange.Start + 1
	}
	f.wmu.Lock()
	f.limit = limit
	f.wmu.Unlock()
	return resp, nil
}

// hiddenRange is the Content-Range of a 206 whose header the browser hides from the page (a
// cross-origin answer exposes only the CORS-safelisted response headers, Content-Length among
// them and Content-Range not; measured 2026-11-18 on Instagram's and Facebook's video servers):
// the range asked, from its start, of the length received, of the total known for the address
// ("*" when none is). "" when the length does not fit the range asked or the total.
func hiddenRange(req Fetch, length int64) string {
	end := req.Start + length - 1
	if req.Start < 0 || (req.End >= 0 && end > req.End) {
		return ""
	}
	total := "*"
	if req.Total > 0 {
		if end >= req.Total {
			return ""
		}
		total = strconv.FormatInt(req.Total, 10)
	}
	return fmt.Sprintf("bytes %d-%d/%s", req.Start, end, total)
}

// mediaType is a Content-Type's media type in lower case without parameters ("" for none).
func mediaType(v string) string {
	if v == "" {
		return ""
	}
	t, _, err := mime.ParseMediaType(v)
	if err != nil {
		t, _, _ = strings.Cut(v, ";")
	}
	return strings.ToLower(strings.TrimSpace(t))
}

// mediaContent reports a type a video or its audio may be served as: video/*, audio/*,
// application/mp4, the untyped octet streams, or none.
func mediaContent(t string) bool {
	switch {
	case t == "", t == "application/mp4", t == "application/octet-stream", t == "binary/octet-stream":
		return true
	case strings.HasPrefix(t, "video/"), strings.HasPrefix(t, "audio/"):
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
