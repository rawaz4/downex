package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

const testTimeout = 20 * time.Second

// frame returns msg with its native-endian length prefix.
func frame(msg []byte) []byte {
	b := make([]byte, 4+len(msg))
	binary.NativeEndian.PutUint32(b, uint32(len(msg)))
	copy(b[4:], msg)
	return b
}

// jsonFrame encodes v and frames it.
func jsonFrame(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return frame(b)
}

// testLogger logs through t while the test runs.
func testLogger(t *testing.T) *slog.Logger {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct {
	mu   sync.Mutex
	t    *testing.T
	done bool
	buf  bytes.Buffer
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if !w.done {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (w *testWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
}

// session is a relay serving a fake browser over pipes.
type session struct {
	t      *testing.T
	in     *io.PipeWriter // the browser's end of stdin
	out    *io.PipeReader // the browser's end of stdout
	done   chan error     // serve's result
	cancel context.CancelFunc
}

func startSession(t *testing.T, be backend) *session {
	t.Helper()
	return startSessionFor(t, be, testTimeout)
}

// startSessionFor is startSession whose relay ends after timeout (a request's context
// deadline is never later than the relay's).
func startSessionFor(t *testing.T, be backend, timeout time.Duration) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s := &session{t: t, in: inW, out: outR, done: make(chan error, 1), cancel: cancel}
	r := &relay{be: be, log: testLogger(t)}
	go func() {
		err := r.serve(ctx, inR, outW)
		_ = outW.Close()
		s.done <- err
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outR.Close()
	})
	return s
}

// send writes one request.
func (s *session) send(v any) {
	s.t.Helper()
	s.sendRaw(jsonFrame(s.t, v))
}

func (s *session) sendRaw(b []byte) {
	s.t.Helper()
	errc := make(chan error, 1)
	go func() { _, err := s.in.Write(b); errc <- err }()
	select {
	case err := <-errc:
		if err != nil {
			s.t.Fatalf("write a request: %v", err)
		}
	case <-time.After(testTimeout):
		s.t.Fatal("the relay does not read its input")
	}
}

// recv reads one response.
func (s *session) recv() map[string]any {
	s.t.Helper()
	type result struct {
		msg []byte
		err error
	}
	c := make(chan result, 1)
	go func() {
		msg, err := readFrame(s.out)
		c <- result{msg, err}
	}()
	select {
	case r := <-c:
		if r.err != nil {
			s.t.Fatalf("read a response: %v", r.err)
		}
		if len(r.msg) > maxResponseBytes {
			s.t.Fatalf("response of %d bytes", len(r.msg))
		}
		var m map[string]any
		if err := json.Unmarshal(r.msg, &m); err != nil {
			s.t.Fatalf("response is not JSON: %v: %s", err, r.msg)
		}
		return m
	case <-time.After(testTimeout):
		s.t.Fatal("no response")
		return nil
	}
}

// closeInput closes stdin (the browser disconnects) and returns serve's result.
func (s *session) closeInput() error {
	s.t.Helper()
	_ = s.in.Close()
	return s.wait()
}

func (s *session) wait() error {
	s.t.Helper()
	go func() { _, _ = io.Copy(io.Discard, s.out) }() // never block the relay's writes
	select {
	case err := <-s.done:
		return err
	case <-time.After(testTimeout):
		s.t.Fatal("the relay did not stop")
		return nil
	}
}

// errorCode returns the error code of a failed response, or "" for a success.
func errorCode(m map[string]any) string {
	if m["ok"] == true {
		return ""
	}
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(string)
	if code == "" {
		return "(malformed error)"
	}
	return code
}

// fakeBackend answers from fields; add calls block on gate when it is set.
type fakeBackend struct {
	mu        sync.Mutex
	adds      []*downexv1.AddRequest
	deadlines []time.Time // the context deadline of each add (zero: none)
	addErr    error
	st        *downexv1.StatusResponse
	stErr     error
	stHang    bool // status waits until its context is done, as a hung daemon does
	warms     int  // calls of warm
	gate      func(req *downexv1.AddRequest) <-chan struct{}
	nextID    int64
	ids       map[string]int64 // download id by URL
	started   chan string      // URL of each add as it starts, when set
	media     []*downexv1.AddMediaRequest
	mediaErr  error
	state     downexv1.DownloadState // of the downloads add and addMedia return
	renew     bool                   // add's downloads are held for a renewal check (renew_check)
	rescues   []*downexv1.RescueRequest
	resumes   []int64
	rescueErr error // answer of rescue and resume
	takes     []*downexv1.TakeBrowserFetchRequest
	puts      []*downexv1.PutBrowserFetchRequest
	takeResp  *downexv1.TakeBrowserFetchResponse
	putStop   bool
	fetchErr  error
}

func (f *fakeBackend) addMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.Download, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.media = append(f.media, req)
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	if f.mediaErr != nil {
		return nil, "", f.mediaErr
	}
	f.nextID++
	return &downexv1.Download{Id: f.nextID, Url: req.GetManifestUrl(), Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, State: f.state}, "", nil
}

// takeFetch and putFetch record the browser fetches (docs/SITE-MEDIA-SPEC.md §2.8) and
// answer what takeResp/putStop say.
func (f *fakeBackend) takeFetch(ctx context.Context, req *downexv1.TakeBrowserFetchRequest) (*downexv1.TakeBrowserFetchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes = append(f.takes, req)
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	if f.takeResp == nil {
		return &downexv1.TakeBrowserFetchResponse{}, nil
	}
	return f.takeResp, nil
}

func (f *fakeBackend) putFetch(ctx context.Context, req *downexv1.PutBrowserFetchRequest) (*downexv1.PutBrowserFetchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, req)
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return &downexv1.PutBrowserFetchResponse{Stop: f.putStop}, nil
}

func (f *fakeBackend) warm(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warms++
	return nil
}

func (f *fakeBackend) add(ctx context.Context, req *downexv1.AddRequest) (*downexv1.Download, error) {
	f.mu.Lock()
	f.adds = append(f.adds, req)
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	gate, started := f.gate, f.started
	f.mu.Unlock()
	if started != nil {
		started <- req.GetUrl()
	}
	if gate != nil {
		select {
		case <-gate(req):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return nil, f.addErr
	}
	if f.ids == nil {
		f.ids = make(map[string]int64)
	}
	f.nextID++
	f.ids[req.GetUrl()] = f.nextID
	return &downexv1.Download{Id: f.nextID, Url: req.GetUrl(), State: f.state, RenewCheck: f.renew}, nil
}

func (f *fakeBackend) status(ctx context.Context) (*downexv1.StatusResponse, error) {
	f.mu.Lock()
	hang := f.stHang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, f.stErr
}

// rescue records the request and answers from rescueErr, else with a paused download
// that kept half of the browser's count.
func (f *fakeBackend) rescue(ctx context.Context, req *downexv1.RescueRequest) (*downexv1.Download, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescues = append(f.rescues, req)
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	if f.rescueErr != nil {
		return nil, 0, f.rescueErr
	}
	f.nextID++
	return &downexv1.Download{Id: f.nextID, Url: req.GetUrl(), State: downexv1.DownloadState_DOWNLOAD_STATE_PAUSED},
		req.GetBytesReceived() / 2, nil
}

func (f *fakeBackend) resume(ctx context.Context, id int64) (*downexv1.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, id)
	if f.rescueErr != nil {
		return nil, f.rescueErr
	}
	return &downexv1.Download{Id: id, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED}, nil
}
