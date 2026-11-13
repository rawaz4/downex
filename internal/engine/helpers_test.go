package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/limiter"
	"downex/internal/model"
	"downex/internal/store"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// ---- in-memory store -------------------------------------------------------------

type memStore struct {
	mu        sync.Mutex
	next      model.DownloadID
	downloads map[model.DownloadID]model.Download
	segments  map[model.DownloadID][]model.Segment
	saves     int
	onSave    func(d model.Download, segs []model.Segment) // called under no lock
	saveErr   error                                        // when set, SaveCheckpoint fails with it
}

var _ store.Store = (*memStore)(nil)

func newMemStore() *memStore {
	return &memStore{downloads: map[model.DownloadID]model.Download{}, segments: map[model.DownloadID][]model.Segment{}}
}

func cloneDownload(d model.Download) model.Download {
	d.Request.Headers = d.Request.Headers.Clone()
	d.Request.Body = bytes.Clone(d.Request.Body)
	return d
}

func (m *memStore) CreateDownload(_ context.Context, d *model.Download) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	d.ID = m.next
	m.downloads[d.ID] = cloneDownload(*d)
	return nil
}

func (m *memStore) UpdateDownload(_ context.Context, d *model.Download) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.downloads[d.ID]; !ok {
		return store.ErrNotFound
	}
	m.downloads[d.ID] = cloneDownload(*d)
	return nil
}

func (m *memStore) GetDownload(_ context.Context, id model.DownloadID) (*model.Download, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.downloads[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := cloneDownload(d)
	return &c, nil
}

func (m *memStore) ListDownloads(context.Context) ([]model.Download, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Values(m.downloads)), nil
}

func (m *memStore) DeleteDownload(_ context.Context, id model.DownloadID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.downloads, id)
	delete(m.segments, id)
	return nil
}

func (m *memStore) SaveCheckpoint(_ context.Context, d *model.Download, segs []model.Segment) error {
	m.mu.Lock()
	if err := m.saveErr; err != nil {
		m.mu.Unlock()
		return err
	}
	if _, ok := m.downloads[d.ID]; !ok {
		m.mu.Unlock()
		return store.ErrNotFound
	}
	m.downloads[d.ID] = cloneDownload(*d)
	m.segments[d.ID] = slices.Clone(segs)
	m.saves++
	hook := m.onSave
	m.mu.Unlock()
	if hook != nil {
		hook(cloneDownload(*d), slices.Clone(segs))
	}
	return nil
}

func (m *memStore) LoadSegments(_ context.Context, id model.DownloadID) ([]model.Segment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.downloads[id]; !ok {
		return nil, store.ErrNotFound
	}
	return slices.Clone(m.segments[id]), nil
}

func (m *memStore) Close() error { return nil }

func (m *memStore) segs(id model.DownloadID) []model.Segment {
	s, _ := m.LoadSegments(context.Background(), id)
	return s
}

func (m *memStore) get(t *testing.T, id model.DownloadID) *model.Download {
	t.Helper()
	d, err := m.GetDownload(context.Background(), id)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	return d
}

// ---- minimal net/http transport (the real one lives in internal/transport) --------

type httpFactory struct {
	newTransport func() *http.Transport
	slots        atomic.Int64
}

func (f *httpFactory) NewSlot() transport.Fetcher {
	f.slots.Add(1)
	tr := f.newTransport()
	return &httpFetcher{tr: tr, client: &http.Client{Transport: tr}}
}

type httpFetcher struct {
	tr     *http.Transport
	client *http.Client
}

func (h *httpFetcher) Close() { h.tr.CloseIdleConnections() }

func (h *httpFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	method := rr.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(rr.Body) > 0 {
		body = bytes.NewReader(rr.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rr.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range rr.Headers {
		req.Header[k] = slices.Clone(v)
	}
	if rr.End >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rr.Start, rr.End))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", rr.Start))
	}
	if rr.IfRange != "" {
		req.Header.Set("If-Range", rr.IfRange)
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	out := &transport.Response{
		StatusCode:       resp.StatusCode,
		Header:           resp.Header,
		Body:             resp.Body,
		ContentLength:    resp.ContentLength,
		ContentRange:     parseContentRange(resp.Header.Get("Content-Range")),
		AcceptRangesNone: strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "none"),
		FinalURL:         resp.Request.URL.String(),
		ContentType:      resp.Header.Get("Content-Type"),
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		out.Validators.ETag = etag
		out.Validators.StrongETag = !strings.HasPrefix(etag, "W/")
	}
	out.Validators.LastModified = resp.Header.Get("Last-Modified")
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
			out.Filename = transport.SanitizeFilename(params["filename"])
		}
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			out.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return out, nil
}

func parseContentRange(v string) *transport.ContentRange {
	spec, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return nil
	}
	rng, total, ok := strings.Cut(spec, "/")
	if !ok {
		return nil
	}
	cr := &transport.ContentRange{Start: -1, End: -1, Total: -1}
	if total != "*" {
		n, err := strconv.ParseInt(total, 10, 64)
		if err != nil {
			return nil
		}
		cr.Total = n
	}
	if rng == "*" {
		return cr
	}
	a, b, ok := strings.Cut(rng, "-")
	if !ok {
		return nil
	}
	var err1, err2 error
	cr.Start, err1 = strconv.ParseInt(a, 10, 64)
	cr.End, err2 = strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	return cr
}

// ---- fake limiter --------------------------------------------------------------------

type countingLimiter struct {
	limit atomic.Int64
	mu    sync.Mutex
	calls int
	maxN  int
	total int64
}

var _ limiter.Limiter = (*countingLimiter)(nil)

func (l *countingLimiter) WaitN(ctx context.Context, n int) error {
	l.mu.Lock()
	l.calls++
	l.maxN = max(l.maxN, n)
	l.total += int64(n)
	l.mu.Unlock()
	return ctx.Err()
}
func (l *countingLimiter) SetLimit(bps int64) { l.limit.Store(bps) }
func (l *countingLimiter) Limit() int64       { return l.limit.Load() }
func (l *countingLimiter) Child(bps int64) limiter.Limiter {
	c := &countingLimiter{}
	c.limit.Store(bps)
	return c
}
func (l *countingLimiter) stats() (calls, maxN int, total int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls, l.maxN, l.total
}

// ---- recording part file ---------------------------------------------------------------

// recordingFile wraps a real file and fails the test if any byte is written twice in
// one session. It also remembers which bytes were synced, so checkpoints can be
// checked against what was really flushed.
type recordingFile struct {
	*os.File
	t       *testing.T
	mu      sync.Mutex
	written []span // merged
	synced  []span
	writes  int
}

func (r *recordingFile) WriteAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	lo, hi := off, off+int64(len(p))
	for _, s := range r.written {
		if lo < s.hi && s.lo < hi {
			r.t.Errorf("overlapping write [%d,%d) intersects [%d,%d)", lo, hi, s.lo, s.hi)
		}
	}
	r.written = addSpan(r.written, span{lo, hi})
	r.writes++
	r.mu.Unlock()
	return r.File.WriteAt(p, off)
}

func (r *recordingFile) Sync() error {
	r.mu.Lock()
	snapshot := slices.Clone(r.written)
	r.mu.Unlock()
	err := r.File.Sync()
	if err == nil {
		r.mu.Lock()
		r.synced = snapshot
		r.mu.Unlock()
	}
	return err
}

// syncedCovers reports whether [lo, hi) was synced.
func (r *recordingFile) syncedCovers(lo, hi int64) bool {
	if hi <= lo {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.synced {
		if s.lo <= lo && hi <= s.hi {
			return true
		}
	}
	return false
}

func (r *recordingFile) writtenSpans() []span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.written)
}

func addSpan(set []span, n span) []span {
	set = append(set, n)
	slices.SortFunc(set, func(a, b span) int { return int(a.lo - b.lo) })
	out := set[:0]
	for _, s := range set {
		if k := len(out); k > 0 && s.lo <= out[k-1].hi {
			out[k-1].hi = max(out[k-1].hi, s.hi)
			continue
		}
		out = append(out, s)
	}
	return out
}

// recorder installs recording part files and keeps the latest one.
type recorder struct {
	t    *testing.T
	mu   sync.Mutex
	last *recordingFile
}

func (rc *recorder) open(name string, flag int, perm fs.FileMode) (partFile, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	r := &recordingFile{File: f, t: rc.t}
	rc.mu.Lock()
	rc.last = r
	rc.mu.Unlock()
	return r, nil
}

func (rc *recorder) file() *recordingFile {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.last
}

// ---- harness -------------------------------------------------------------------------------

type harness struct {
	t   *testing.T
	srv *testserver.Server
	st  *memStore
	fac *httpFactory
	eng *Engine
	dir string
}

func newHarness(t *testing.T, so testserver.Options, opts Options) *harness {
	t.Helper()
	srv := testserver.New(so)
	t.Cleanup(srv.Close)
	h := &harness{t: t, srv: srv, st: newMemStore(), dir: t.TempDir()}
	h.fac = &httpFactory{newTransport: srv.NewTransport}
	h.eng = New(h.st, h.fac, opts)
	return h
}

func (h *harness) add(path string, filename string, maxConn int) *model.Download {
	h.t.Helper()
	d, err := h.eng.Add(context.Background(), model.Request{URL: h.srv.URL + path}, h.dir, filename, maxConn, 0)
	if err != nil {
		h.t.Fatalf("Add: %v", err)
	}
	return d
}

func (h *harness) run(id model.DownloadID, lim limiter.Limiter, onProgress func(Progress)) error {
	ctx, cancel := runContext(h.t)
	defer cancel()
	return h.eng.Run(ctx, id, lim, onProgress)
}

// runContext returns the context of a whole Run over the real network and disk. It ends
// shortly before the test binary's -timeout rather than after a fixed time, which a
// loaded machine (fsync, close and rename included) can exceed while nothing is wrong;
// a hang still ends in a failure that names the run.
func runContext(t *testing.T) (context.Context, context.CancelFunc) {
	if dl, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), dl.Add(-deadlineMargin(dl)))
	}
	return context.WithCancel(context.Background())
}

// deadlineMargin is the time left before the binary's deadline dl for a test to report
// a hang itself: a tenth of what is left, at most 30 s.
func deadlineMargin(dl time.Time) time.Duration { return min(30*time.Second, time.Until(dl)/10) }

// awaitRun waits for a Run started in the background to return. The engine owns its
// part file until then, so giving up after a fixed time would leave the handle open
// under t.TempDir's cleanup (on Windows the removal then fails too); a Run still running
// shortly before the binary's -timeout fails the test with every goroutine's stack.
func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	var limit <-chan time.Time
	if dl, ok := t.Deadline(); ok {
		timer := time.NewTimer(time.Until(dl) - deadlineMargin(dl))
		defer timer.Stop()
		limit = timer.C
	}
	select {
	case err := <-done:
		return err
	case <-limit:
		t.Fatalf("Run did not return; goroutines:\n%s", goroutines())
		return nil
	}
}

// verify checks that the completed download's file matches the served content.
func (h *harness) verify(id model.DownloadID) *model.Download {
	h.t.Helper()
	d := h.st.get(h.t, id)
	if d.State != model.StateCompleted {
		h.t.Fatalf("state = %v (%s), want completed", d.State, d.Error)
	}
	path := filepath.Join(d.Dir, d.Filename)
	got, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("read result: %v", err)
	}
	o := h.srv.Options()
	if int64(len(got)) != o.Size {
		h.t.Fatalf("size = %d, want %d", len(got), o.Size)
	}
	if sha256.Sum256(got) != h.srv.Sum256() {
		for i := range got {
			if got[i] != testserver.Byte(o.Seed, int64(i)) {
				h.t.Fatalf("content differs first at byte %d", i)
			}
		}
	}
	if d.Size != o.Size {
		h.t.Fatalf("record size = %d, want %d", d.Size, o.Size)
	}
	if _, err := os.Stat(partPath(d.Dir, d.Filename)); !os.IsNotExist(err) {
		h.t.Fatalf("part file still present: %v", err)
	}
	return d
}

// maxConns returns an onProgress callback and a getter for the peak connection count.
func maxConns() (func(Progress), func() int) {
	var peak atomic.Int64
	return func(p Progress) {
			for {
				cur := peak.Load()
				if int64(p.Connections) <= cur || peak.CompareAndSwap(cur, int64(p.Connections)) {
					return
				}
			}
		}, func() int {
			return int(peak.Load())
		}
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
