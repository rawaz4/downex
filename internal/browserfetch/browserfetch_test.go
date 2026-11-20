package browserfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

const pageVideo = "https://media.example/video/abc/?sig=1"

// fetchAsync runs one Fetch of the browser transport of download id in the background.
func fetchAsync(ctx context.Context, b *Broker, id model.DownloadID, rr transport.RangeRequest, sizes func(string) int64) chan fetchOut {
	out := make(chan fetchOut, 1)
	go func() {
		resp, err := b.Factory(id, sizes).NewSlot().Fetch(ctx, rr)
		out <- fetchOut{resp, err}
	}()
	return out
}

type fetchOut struct {
	resp *transport.Response
	err  error
}

func rangeReq(url string, start, end int64) transport.RangeRequest {
	return transport.RangeRequest{Request: model.Request{URL: url}, Start: start, End: end}
}

func take(t *testing.T, b *Broker, ticket string) *Fetch {
	t.Helper()
	f, ended, err := b.Take(context.Background(), []string{ticket}, 2*time.Second)
	if err != nil || len(ended) != 0 || f == nil {
		t.Fatalf("Take = %+v, %v, %v; want a fetch", f, ended, err)
	}
	return f
}

func TestFetchThroughBrowser(t *testing.T) {
	b := New()
	ticket, err := b.Bind(7)
	if err != nil || !ValidTicket(ticket) {
		t.Fatalf("Bind = %q, %v", ticket, err)
	}
	if again, _ := b.Bind(7); again != ticket {
		t.Fatalf("Bind again = %q, want the same ticket", again)
	}
	data := bytes.Repeat([]byte("0123456789"), 60_000) // 600,000 bytes, three puts
	out := fetchAsync(context.Background(), b, 7, rangeReq(pageVideo, 100, int64(len(data))+99), func(string) int64 { return 1 << 20 })
	f := take(t, b, ticket)
	if f.URL != pageVideo || f.Start != 100 || f.End != int64(len(data))+99 || f.Total != 1<<20 || f.Ticket != ticket {
		t.Fatalf("taken fetch = %+v", f)
	}
	head := &Head{Status: 206, ContentType: "video/mp4", ContentRange: "bytes 100-600099/1048576", ContentLength: int64(len(data))}
	got := make(chan []byte, 1)
	go func() {
		o := <-out
		if o.err != nil {
			t.Errorf("Fetch: %v", o.err)
			got <- nil
			return
		}
		if o.resp.StatusCode != 206 || o.resp.ContentRange == nil || o.resp.ContentRange.Total != 1<<20 || o.resp.ContentType != "video/mp4" {
			t.Errorf("response = %+v", o.resp)
		}
		all, err := io.ReadAll(o.resp.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		o.resp.Body.Close()
		got <- all
	}()
	for seq, off := int64(0), 0; off < len(data); seq++ {
		n := min(MaxPiece, len(data)-off)
		var h *Head
		if seq == 0 {
			h = head
		}
		stop, err := b.Put(context.Background(), ticket, f.ID, seq, h, data[off:off+n], off+n == len(data), "")
		if err != nil {
			t.Fatalf("Put %d: %v", seq, err)
		}
		off += n
		if stop != (off == len(data)) {
			t.Fatalf("Put %d: stop = %v", seq, stop)
		}
	}
	if all := <-got; !bytes.Equal(all, data) {
		t.Fatalf("body = %d bytes, want the %d put", len(all), len(data))
	}
}

func TestPutBlocksUntilRead(t *testing.T) {
	b := New()
	ticket, _ := b.Bind(1)
	out := fetchAsync(context.Background(), b, 1, rangeReq(pageVideo, 0, -1), nil)
	f := take(t, b, ticket)
	if stop, err := b.Put(context.Background(), ticket, f.ID, 0, &Head{Status: 200, ContentType: "video/mp4", ContentLength: 8}, nil, false, ""); stop || err != nil {
		t.Fatalf("head put = %v, %v", stop, err)
	}
	o := <-out
	if o.err != nil {
		t.Fatal(o.err)
	}
	done := make(chan bool, 1)
	go func() {
		stop, _ := b.Put(context.Background(), ticket, f.ID, 1, nil, []byte("abcd"), false, "")
		done <- stop
	}()
	select {
	case <-done:
		t.Fatal("a put returned before the run read its bytes")
	case <-time.After(50 * time.Millisecond):
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(o.resp.Body, buf); err != nil || string(buf) != "abcd" {
		t.Fatalf("read = %q, %v", buf, err)
	}
	if stop := <-done; stop {
		t.Fatal("stop after a piece the run read")
	}
	// The run closes the answer: the browser's next put stops.
	o.resp.Body.Close()
	if stop, _ := b.Put(context.Background(), ticket, f.ID, 2, nil, []byte("efgh"), true, ""); !stop {
		t.Fatal("a put after the answer was closed did not stop")
	}
}

func TestPutChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		head   Head
		pieces []string
		end    bool
		errIs  error  // of Fetch
		read   string // the body read before the failure
		readIs error
	}{
		{name: "a page instead of the video", head: Head{Status: 200, ContentType: "text/html; charset=utf-8", ContentLength: 10}, errIs: ErrNotMedia},
		{name: "a bad status", head: Head{Status: 42}, errIs: errHead},
		{name: "a bad range", head: Head{Status: 206, ContentType: "video/mp4", ContentRange: "bytes x-y/z"}, errIs: errHead},
		{name: "more than announced", head: Head{Status: 200, ContentType: "video/mp4", ContentLength: 6}, pieces: []string{"abcd", "efgh"}, read: "abcdef", readIs: errTooLong},
		{name: "more than the range", head: Head{Status: 206, ContentType: "video/mp4", ContentRange: "bytes 0-3/100", ContentLength: -1}, pieces: []string{"abcdef"}, read: "abcd", readIs: errTooLong},
		{name: "less than announced", head: Head{Status: 200, ContentLength: 10}, pieces: []string{"abcd"}, end: true, read: "abcd", readIs: io.ErrUnexpectedEOF},
		{name: "an error status passes", head: Head{Status: 403, ContentType: "text/html", ContentLength: 3}, pieces: []string{"no!"}, end: true, read: "no!"},
		{name: "untyped", head: Head{Status: 200, ContentLength: 3}, pieces: []string{"mp4"}, end: true, read: "mp4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			ticket, _ := b.Bind(1)
			out := fetchAsync(context.Background(), b, 1, rangeReq(pageVideo, 0, -1), nil)
			f := take(t, b, ticket)
			h := tc.head
			stop, err := b.Put(context.Background(), ticket, f.ID, 0, &h, nil, false, "")
			o := <-out
			if tc.errIs != nil {
				if !errors.Is(o.err, tc.errIs) || !stop || err != nil {
					t.Fatalf("Fetch err = %v (stop %v, %v), want %v", o.err, stop, err, tc.errIs)
				}
				return
			}
			if o.err != nil {
				t.Fatal(o.err)
			}
			read := make(chan error, 1)
			var got []byte
			go func() {
				var err error
				got, err = io.ReadAll(o.resp.Body)
				read <- err
			}()
			for i, p := range tc.pieces {
				b.Put(context.Background(), ticket, f.ID, int64(i+1), nil, []byte(p), tc.end && i == len(tc.pieces)-1, "")
			}
			rerr := <-read
			if string(got) != tc.read || (tc.readIs == nil) != (rerr == nil) || (tc.readIs != nil && !errors.Is(rerr, tc.readIs)) {
				t.Fatalf("body = %q, %v; want %q, %v", got, rerr, tc.read, tc.readIs)
			}
		})
	}
}

func TestPutOrder(t *testing.T) {
	b := New()
	ticket, _ := b.Bind(1)
	other, _ := b.Bind(2)
	out := fetchAsync(context.Background(), b, 1, rangeReq(pageVideo, 0, -1), nil)
	f := take(t, b, ticket)
	head := &Head{Status: 200, ContentType: "video/mp4", ContentLength: 2}
	for _, tc := range []struct {
		name          string
		ticket, fetch string
		seq           int64
	}{
		{"another ticket", other, f.ID, 0},
		{"another fetch", ticket, "999", 0},
		{"a later seq first", ticket, f.ID, 1},
	} {
		if stop, err := b.Put(context.Background(), tc.ticket, tc.fetch, tc.seq, head, nil, false, ""); !stop || err != nil {
			t.Errorf("%s: Put = %v, %v; want stop", tc.name, stop, err)
		}
	}
	stopped := make(chan bool, 1)
	go func() {
		stop, _ := b.Put(context.Background(), ticket, f.ID, 0, head, []byte("ok"), true, "")
		stopped <- stop
	}()
	o := <-out
	if o.err != nil {
		t.Fatal(o.err)
	}
	if all, err := io.ReadAll(o.resp.Body); string(all) != "ok" || err != nil {
		t.Fatalf("body = %q, %v", all, err)
	}
	if !<-stopped {
		t.Fatal("the whole answer in one put did not end the fetch")
	}
	// The same seq again changes nothing.
	if stop, _ := b.Put(context.Background(), ticket, f.ID, 0, head, []byte("ok"), true, ""); !stop {
		t.Fatal("a repeated put did not stop")
	}
}

func TestInvalid(t *testing.T) {
	b := New()
	ticket, _ := b.Bind(1)
	for name, err := range map[string]error{
		"no ticket":  func() error { _, _, err := b.Take(context.Background(), nil, 0); return err }(),
		"bad ticket": func() error { _, _, err := b.Take(context.Background(), []string{"ABC"}, 0); return err }(),
		"too many":   func() error { _, _, err := b.Take(context.Background(), make([]string, 65), 0); return err }(),
		"long wait":  func() error { _, _, err := b.Take(context.Background(), []string{ticket}, time.Minute); return err }(),
		"big piece": func() error {
			_, err := b.Put(context.Background(), ticket, "1", 1, nil, make([]byte, MaxPiece+1), false, "")
			return err
		}(),
		"long error": func() error {
			_, err := b.Put(context.Background(), ticket, "1", 1, nil, nil, false, strings.Repeat("x", MaxError+1))
			return err
		}(),
		"put bad ticket": func() error { _, err := b.Put(context.Background(), "zz", "1", 0, nil, nil, false, ""); return err }(),
		"negative seq":   func() error { _, err := b.Put(context.Background(), ticket, "1", -1, nil, nil, false, ""); return err }(),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := b.Factory(1, nil).NewSlot().Fetch(context.Background(), transport.RangeRequest{Request: model.Request{URL: pageVideo, Method: "POST"}}); !errors.Is(err, transport.ErrInvalidRequest) {
		t.Errorf("POST: err = %v", err)
	}
}

func TestNotTakenIsGone(t *testing.T) {
	b := New()
	b.takeAfter = 30 * time.Millisecond
	b.Bind(1)
	_, err := b.Factory(1, nil).NewSlot().Fetch(context.Background(), rangeReq(pageVideo, 0, -1))
	if !errors.Is(err, ErrGone) || !errors.Is(err, transport.ErrGone) {
		t.Fatalf("err = %v, want ErrGone", err)
	}
	// Nothing waits any more.
	if f, _, _ := b.Take(context.Background(), []string{mustTicket(b, 1)}, 0); f != nil {
		t.Fatalf("Take = %+v after the request gave up", f)
	}
	// A download without a ticket has no browser.
	if _, err := b.Factory(9, nil).NewSlot().Fetch(context.Background(), rangeReq(pageVideo, 0, -1)); !errors.Is(err, ErrGone) {
		t.Fatalf("no ticket: err = %v", err)
	}
}

func mustTicket(b *Broker, id model.DownloadID) string {
	t, _ := b.Bind(id)
	return t
}

func TestNoTabAndBrowserError(t *testing.T) {
	b := New()
	ticket, _ := b.Bind(1)
	for text, want := range map[string]error{NoTab: ErrGone, "TypeError: Failed to fetch": ErrBrowser} {
		out := fetchAsync(context.Background(), b, 1, rangeReq(pageVideo, 0, -1), nil)
		f := take(t, b, ticket)
		if stop, err := b.Put(context.Background(), ticket, f.ID, 0, nil, nil, false, text); !stop || err != nil {
			t.Fatalf("%s: Put = %v, %v", text, stop, err)
		}
		if o := <-out; !errors.Is(o.err, want) {
			t.Fatalf("%s: err = %v, want %v", text, o.err, want)
		}
	}
}

func TestCancelAndForget(t *testing.T) {
	b := New()
	ticket, _ := b.Bind(1)
	// A pause cancels a waiting request: it is never taken.
	ctx, cancel := context.WithCancel(context.Background())
	out := fetchAsync(ctx, b, 1, rangeReq(pageVideo, 0, -1), nil)
	time.Sleep(10 * time.Millisecond)
	cancel()
	if o := <-out; !errors.Is(o.err, context.Canceled) {
		t.Fatalf("cancelled: err = %v", o.err)
	}
	if f, _, _ := b.Take(context.Background(), []string{ticket}, 0); f != nil {
		t.Fatalf("Take = %+v after the pause", f)
	}
	// A pause while the browser sends: its next put stops.
	ctx, cancel = context.WithCancel(context.Background())
	out = fetchAsync(ctx, b, 1, rangeReq(pageVideo, 0, -1), nil)
	f := take(t, b, ticket)
	b.Put(context.Background(), ticket, f.ID, 0, &Head{Status: 200, ContentType: "video/mp4", ContentLength: -1}, nil, false, "")
	if o := <-out; o.err != nil {
		t.Fatal(o.err)
	}
	cancel()
	time.Sleep(10 * time.Millisecond)
	if stop, _ := b.Put(context.Background(), ticket, f.ID, 1, nil, []byte("x"), false, ""); !stop {
		t.Fatal("a put after the pause did not stop")
	}
	// Forget fails what waits and lists the ticket as ended.
	out = fetchAsync(context.Background(), b, 1, rangeReq(pageVideo, 0, -1), nil)
	time.Sleep(10 * time.Millisecond)
	b.Forget(1)
	if o := <-out; !errors.Is(o.err, ErrGone) {
		t.Fatalf("forgotten: err = %v", o.err)
	}
	if b.Has(1) {
		t.Fatal("Has after Forget")
	}
	f2, ended, err := b.Take(context.Background(), []string{ticket}, 0)
	if f2 != nil || err != nil || len(ended) != 1 || ended[0] != ticket {
		t.Fatalf("Take = %+v, %v, %v; want the ticket ended", f2, ended, err)
	}
}

func TestTakeWaitsAndOrders(t *testing.T) {
	b := New()
	t1, _ := b.Bind(1)
	t2, _ := b.Bind(2)
	got := make(chan *Fetch, 1)
	go func() {
		f, _, _ := b.Take(context.Background(), []string{t1, t2}, 2*time.Second)
		got <- f
	}()
	time.Sleep(20 * time.Millisecond)
	fetchAsync(context.Background(), b, 2, rangeReq("https://media.example/two", 0, -1), nil)
	if f := <-got; f == nil || f.Ticket != t2 {
		t.Fatalf("waiting take = %+v, want download 2's request", f)
	}
	fetchAsync(context.Background(), b, 1, rangeReq("https://media.example/a", 0, -1), nil)
	time.Sleep(10 * time.Millisecond)
	fetchAsync(context.Background(), b, 2, rangeReq("https://media.example/b", 0, -1), nil)
	time.Sleep(10 * time.Millisecond)
	if f := take(t, b, t2); f.URL != "https://media.example/b" {
		t.Fatalf("one ticket: %+v", f)
	}
	if f, _, _ := b.Take(context.Background(), []string{t1, t2}, 0); f == nil || f.URL != "https://media.example/a" {
		t.Fatalf("oldest first: %+v", f)
	}
}

func TestTicketsBounded(t *testing.T) {
	b := New()
	first, _ := b.Bind(1)
	for id := model.DownloadID(2); id <= MaxTickets+1; id++ {
		b.Bind(id)
	}
	if b.Has(1) || !b.Has(MaxTickets+1) {
		t.Fatal("the oldest download's ticket was not the one dropped")
	}
	if _, ended, _ := b.Take(context.Background(), []string{first}, 0); len(ended) != 1 {
		t.Fatalf("ended = %v", ended)
	}
}

// Restore binds a kept ticket (amendment 2026-11-19): a take that names it waits for the
// download's requests instead of ending it; another download's ticket and a malformed
// one are refused.
func TestRestore(t *testing.T) {
	b := New()
	ticket, err := b.NewTicket()
	if err != nil || !ValidTicket(ticket) || b.Has(7) {
		t.Fatalf("NewTicket = %q, %v", ticket, err)
	}
	if err := b.Restore(7, ticket); err != nil || !b.Has(7) {
		t.Fatalf("Restore = %v", err)
	}
	if got, _ := b.Bind(7); got != ticket {
		t.Fatalf("Bind after Restore = %q, want %q", got, ticket)
	}
	if err := b.Restore(7, ticket); err != nil {
		t.Errorf("Restore again = %v", err)
	}
	if err := b.Restore(8, ticket); !errors.Is(err, ErrInvalid) {
		t.Errorf("Restore of another download's ticket = %v", err)
	}
	if err := b.Restore(8, "nope"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Restore of a malformed ticket = %v", err)
	}
	if _, ended, _ := b.Take(context.Background(), []string{ticket}, 0); len(ended) != 0 {
		t.Fatalf("a restored ticket was ended: %v", ended)
	}
	out := fetchAsync(context.Background(), b, 7, rangeReq(pageVideo, 0, 9), nil)
	f := take(t, b, ticket)
	b.Forget(7)
	if r := <-out; !errors.Is(r.err, ErrGone) || f.Ticket != ticket {
		t.Fatalf("fetch after Forget = %v", r.err)
	}
}

// TestBusyBrowserKeepsWaiting: the extension's pump runs at most 2 transfers for the whole
// browser and takes only while one is free (docs/SITE-MEDIA-SPEC.md §2.8). While one
// download's slow transfers fill both, another download's requests wait for a free one:
// the browser is still serving, so they never fail with ErrGone however long the transfers
// take. A request whose ticket no browser names fails once the browser is idle.
func TestBusyBrowserKeepsWaiting(t *testing.T) {
	const takeAfter = 200 * time.Millisecond
	b := NewWithTimeouts(takeAfter, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ta, _ := b.Bind(1)

	var (
		mu      sync.Mutex
		tickets = []string{ta}
	)
	hold := func(t string) {
		mu.Lock()
		tickets = append(tickets, t)
		mu.Unlock()
	}
	const size, pieces = 64 << 10, 16
	go func() { // the pump (ext/src/lib/browser-fetch.ts)
		lanes := make(chan struct{}, 2)
		for ctx.Err() == nil {
			lanes <- struct{}{}
			mu.Lock()
			ts := slices.Clone(tickets)
			mu.Unlock()
			f, _, err := b.Take(ctx, ts, 50*time.Millisecond)
			if err != nil || f == nil {
				<-lanes
				continue
			}
			go func(f *Fetch) {
				defer func() { <-lanes }()
				n := f.End - f.Start + 1
				head := &Head{Status: 206, ContentType: "video/mp4", ContentLength: n,
					ContentRange: fmt.Sprintf("bytes %d-%d/%d", f.Start, f.End, f.Total)}
				if stop, _ := b.Put(ctx, f.Ticket, f.ID, 0, head, nil, false, ""); stop {
					return
				}
				piece := make([]byte, n/pieces)
				for i := int64(1); i <= pieces; i++ {
					time.Sleep(takeAfter / 4) // a slow network: each transfer outlasts takeAfter
					if stop, _ := b.Put(ctx, f.Ticket, f.ID, i, nil, piece, i == pieces, ""); stop {
						return
					}
				}
			}(f)
		}
	}()

	get := func(id model.DownloadID, start int64) chan error {
		out := make(chan error, 1)
		go func() {
			resp, err := b.Factory(id, func(string) int64 { return 2 * size }).NewSlot().Fetch(ctx, rangeReq(pageVideo, start, start+size-1))
			if err == nil {
				_, err = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			out <- err
		}()
		return out
	}
	// Download A's two requests fill both transfers; download B is added after them.
	a := []chan error{get(1, 0), get(1, size)}
	time.Sleep(takeAfter / 4)
	tb, _ := b.Bind(2)
	hold(tb)
	bs := []chan error{get(2, 0), get(2, size)}
	for i, out := range append(a, bs...) {
		if err := <-out; err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	// A ticket no browser names fails once the browser is idle.
	b.Bind(3)
	start := time.Now()
	if err := <-get(3, 0); !errors.Is(err, ErrGone) {
		t.Fatalf("a ticket no browser holds: err = %v, want ErrGone", err)
	}
	if d := time.Since(start); d > 10*takeAfter {
		t.Errorf("a ticket no browser holds failed after %v", d)
	}
}

// A cross-origin 206 whose Content-Range the browser hides from the page (measured
// 2026-11-18 on Instagram's and Facebook's video servers): the range asked, of the length
// received, of the known total ("*" without one); never a range the answer does not fit.
func TestHiddenContentRange(t *testing.T) {
	for _, tc := range []struct {
		name              string
		start, end, total int64
		length            int64
		want              *transport.ContentRange
	}{
		{"known total", 100, 199, 4188024, 100, &transport.ContentRange{Start: 100, End: 199, Total: 4188024}},
		{"unknown total", 0, 0, 0, 1, &transport.ContentRange{Start: 0, End: 0, Total: -1}},
		{"the file's end", 4188000, 4188099, 4188024, 24, &transport.ContentRange{Start: 4188000, End: 4188023, Total: 4188024}},
		{"longer than asked", 0, 9, 0, 11, nil},
		{"past the total", 0, -1, 10, 11, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			ticket, _ := b.Bind(3)
			out := fetchAsync(context.Background(), b, 3, rangeReq(pageVideo, tc.start, tc.end), func(string) int64 { return tc.total })
			f := take(t, b, ticket)
			head := &Head{Status: 206, ContentType: "video/mp4", ContentLength: tc.length}
			go func() {
				_, _ = b.Put(context.Background(), ticket, f.ID, 0, head, make([]byte, tc.length), true, "")
			}()
			o := <-out
			if tc.want == nil {
				if o.err == nil && o.resp.ContentRange != nil {
					t.Fatalf("a range the answer does not fit: %+v", o.resp.ContentRange)
				}
				return
			}
			if o.err != nil {
				t.Fatal(o.err)
			}
			defer o.resp.Body.Close()
			if o.resp.ContentRange == nil || *o.resp.ContentRange != *tc.want {
				t.Fatalf("Content-Range = %+v, want %+v", o.resp.ContentRange, tc.want)
			}
		})
	}
}
