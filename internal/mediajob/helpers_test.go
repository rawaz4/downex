package mediajob

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/remux"
	"downex/internal/transport"
)

// server serves generated manifests and fragments and records what it was asked.
type server struct {
	ts *httptest.Server

	mu          sync.Mutex
	files       map[string]served
	hits        map[string]int
	ranges      map[string][]string
	cookies     map[string][]string
	referers    map[string][]string
	fail        map[string]failure
	ignoreRange map[string]bool
	chunk       map[string]int64
	hook        func(r *http.Request)
	delay       time.Duration
	active      int
	maxActive   int
}

type served struct {
	data  []byte
	ctype string
}

type failure struct {
	n          int // remaining failures
	status     int
	retryAfter string
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := newUnstartedServer()
	s.ts.Start()
	t.Cleanup(s.ts.Close)
	return s
}

// newUnstartedServer returns a server whose httptest server the caller configures and
// starts (and closes).
func newUnstartedServer() *server {
	s := &server{
		files:       make(map[string]served),
		hits:        make(map[string]int),
		ranges:      make(map[string][]string),
		cookies:     make(map[string][]string),
		referers:    make(map[string][]string),
		fail:        make(map[string]failure),
		ignoreRange: make(map[string]bool),
		chunk:       make(map[string]int64),
	}
	s.ts = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	return s
}

func (s *server) url(path string) string { return s.ts.URL + path }

func (s *server) add(path string, data []byte, ctype string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = served{data: data, ctype: ctype}
}

func (s *server) failNext(path string, n, status int, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[path] = failure{n: n, status: status, retryAfter: retryAfter}
}

func (s *server) setHook(h func(r *http.Request)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = h
}

func (s *server) hitCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *server) setDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

func (s *server) setIgnoreRange(path string, ignore bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignoreRange[path] = ignore
}

// setChunk makes the server answer every range request for path with at most n bytes.
func (s *server) setChunk(path string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunk[path] = n
}

type seenRequests struct {
	ranges, cookies, referers []string
}

func (s *server) seen(path string) seenRequests {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seenRequests{
		ranges:   append([]string(nil), s.ranges[path]...),
		cookies:  append([]string(nil), s.cookies[path]...),
		referers: append([]string(nil), s.referers[path]...),
	}
}

// capRange rewrites "bytes=a-b" or "bytes=a-" to cover at most n bytes.
func capRange(v string, n int64) string {
	spec, ok := strings.CutPrefix(v, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return v
	}
	first, last, _ := strings.Cut(spec, "-")
	a, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return v
	}
	end := a + n - 1
	if b, err := strconv.ParseInt(last, 10, 64); err == nil {
		end = min(end, b)
	}
	return fmt.Sprintf("bytes=%d-%d", a, end)
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	s.mu.Lock()
	s.hits[p]++
	s.ranges[p] = append(s.ranges[p], r.Header.Get("Range"))
	s.cookies[p] = append(s.cookies[p], r.Header.Get("Cookie"))
	s.referers[p] = append(s.referers[p], r.Header.Get("Referer"))
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	f, ok := s.files[p]
	fl := s.fail[p]
	if fl.n > 0 {
		s.fail[p] = failure{n: fl.n - 1, status: fl.status, retryAfter: fl.retryAfter}
	}
	ignore, chunk, hook, delay := s.ignoreRange[p], s.chunk[p], s.hook, s.delay
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if hook != nil {
		hook(r)
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if fl.n > 0 {
		if fl.retryAfter != "" {
			w.Header().Set("Retry-After", fl.retryAfter)
		}
		w.WriteHeader(fl.status)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	ct := f.ctype
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	switch rng := r.Header.Get("Range"); {
	case ignore:
		r.Header.Del("Range")
	case chunk > 0 && rng != "":
		r.Header.Set("Range", capRange(rng, chunk))
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
}

func testFactory(t *testing.T) transport.Factory {
	t.Helper()
	o := transport.DefaultHTTPOptions()
	o.Proxy = "direct"
	f, err := transport.NewHTTPFactory(o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// fakeAssembler joins the inputs byte by byte (init, then fragments, per input) and
// records what it was given. It answers container, "mp4" when that is "": what
// remux.Assemble makes of transport streams and fragmented MP4 alike.
type fakeAssembler struct {
	mu        sync.Mutex
	calls     int
	inputs    []remux.Input
	container string
}

func (a *fakeAssembler) assemble(ctx context.Context, inputs []remux.Input, outPath string) (string, error) {
	a.mu.Lock()
	a.calls++
	a.inputs = append([]remux.Input(nil), inputs...)
	container := cmp.Or(a.container, "mp4")
	a.mu.Unlock()
	var out bytes.Buffer
	for _, in := range inputs {
		paths := in.Fragments
		if in.Container == "fmp4" {
			paths = append([]string{in.Init}, paths...)
		}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			out.Write(b)
		}
	}
	return container, os.WriteFile(outPath, out.Bytes(), 0o600)
}

func (a *fakeAssembler) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// tsChecker is an Options.CheckTS that records what it was given and answers err, for
// the tracks of kind only when kind is set.
type tsChecker struct {
	mu       sync.Mutex
	err      error
	kind     remux.Kind
	prefixes [][]byte
	kinds    []remux.Kind
}

func (c *tsChecker) check(prefix []byte, k remux.Kind) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prefixes = append(c.prefixes, bytes.Clone(prefix))
	c.kinds = append(c.kinds, k)
	if c.kind != "" && k != c.kind {
		return nil
	}
	return c.err
}

func (c *tsChecker) calls() ([][]byte, []remux.Kind) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.prefixes), slices.Clone(c.kinds)
}

// fakeConcat is an Options.ConcatTS that joins its input byte by byte (init, then
// fragments) and records it.
type fakeConcat struct {
	mu     sync.Mutex
	calls  int
	inputs []remux.Input
}

func (c *fakeConcat) concat(ctx context.Context, in remux.Input, outPath string) error {
	c.mu.Lock()
	c.calls++
	c.inputs = append(c.inputs, in)
	c.mu.Unlock()
	var out bytes.Buffer
	for _, p := range append([]string{in.Init}, in.Fragments...) {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out.Write(b)
	}
	return os.WriteFile(outPath, out.Bytes(), 0o600)
}

func (c *fakeConcat) recorded() (int, []remux.Input) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, slices.Clone(c.inputs)
}

// testOptions are Run's options for a test: a fresh part directory and output, short
// retries, asm as the assembler (nil: remux.Assemble) and a CheckTS that accepts every
// transport stream (the fragments of the tests are filler packets, which the real
// check would refuse).
func testOptions(t *testing.T, asm *fakeAssembler) Options {
	t.Helper()
	dir := t.TempDir()
	o := Options{
		Factory:        testFactory(t),
		MaxConnections: 4,
		PartDir:        filepath.Join(dir, "part"),
		OutPath:        filepath.Join(dir, "out.bin"),
		RetryBaseDelay: 5 * time.Millisecond,
		RetryMaxDelay:  20 * time.Millisecond,
		CheckTS:        func([]byte, remux.Kind) error { return nil },
	}
	if asm != nil {
		o.Assemble = asm.assemble
	}
	return o
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// tsSegment returns packets of 188 bytes, each starting with the MPEG-TS sync byte.
func tsSegment(seed uint64, packets int) []byte {
	rng := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, 188*packets)
	for i := range b {
		if i%188 == 0 {
			b[i] = 0x47
		} else {
			b[i] = byte(rng.Uint32())
		}
	}
	return b
}

func box(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n))
	copy(b[4:], typ)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

// initSegment is a minimal fMP4 initialization segment; protected adds a 'pssh' box.
func initSegment(tag string, protected bool) []byte {
	moov := [][]byte{box("mvhd", make([]byte, 100)), box("udta", []byte(tag))}
	if protected {
		moov = append(moov, box("pssh", make([]byte, 24)))
	}
	return append(box("ftyp", []byte("isom\x00\x00\x02\x00isomiso6")), box("moov", moov...)...)
}

func mediaSegment(seed uint64, n int) []byte {
	rng := rand.New(rand.NewPCG(seed, 2))
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte(rng.Uint32())
	}
	return append(box("moof", box("mfhd", make([]byte, 8))), box("mdat", payload)...)
}

const hlsType = "application/vnd.apple.mpegurl"

// mediaPlaylist renders a VOD media playlist of the given segment URIs.
func mediaPlaylist(mapURI string, segs []string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	if mapURI != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", mapURI)
	}
	for _, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:4.000,\n%s\n", s)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// addTSPlaylist serves a media playlist of n TS segments under dir and returns the
// expected concatenation.
func addTSPlaylist(s *server, dir string, n int, seed uint64) []byte {
	var segs []string
	var all []byte
	for i := range n {
		name := fmt.Sprintf("seg%d.ts", i)
		data := tsSegment(seed*1000+uint64(i), 3+i%4)
		s.add(dir+name, data, "video/mp2t")
		segs = append(segs, name)
		all = append(all, data...)
	}
	s.add(dir+"index.m3u8", []byte(mediaPlaylist("", segs)), hlsType)
	return all
}

// addFMP4Playlist serves an fMP4 media playlist (init + n segments) under dir and
// returns the init and the concatenated segments.
func addFMP4Playlist(s *server, dir string, n int, seed uint64) (init, segs []byte) {
	init = initSegment(dir, false)
	s.add(dir+"init.mp4", init, "video/mp4")
	var names []string
	for i := range n {
		name := fmt.Sprintf("s%d.m4s", i)
		data := mediaSegment(seed*1000+uint64(i), 500+97*i)
		s.add(dir+name, data, "video/iso.segment")
		names = append(names, name)
		segs = append(segs, data...)
	}
	s.add(dir+"index.m3u8", []byte(mediaPlaylist("init.mp4", names)), hlsType)
	return init, segs
}

// recorder collects the callbacks of Run.
type recorder struct {
	progress    []Progress
	progressAt  []time.Time
	checkpoints []Checkpoint
}

func (r *recorder) onProgress(p Progress) {
	r.progress = append(r.progress, p)
	r.progressAt = append(r.progressAt, time.Now())
}

func (r *recorder) onCheckpoint(cp Checkpoint) error {
	r.checkpoints = append(r.checkpoints, cp)
	return nil
}

func (r *recorder) last() *Checkpoint {
	if len(r.checkpoints) == 0 {
		return nil
	}
	return &r.checkpoints[len(r.checkpoints)-1]
}

func bitSet(done []byte, i int) bool { return done[i>>3]&(1<<(i&7)) != 0 }

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
