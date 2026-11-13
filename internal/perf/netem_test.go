//go:build perf

package perf

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// netCfg shapes the loopback network of one local scenario.
type netCfg struct {
	TLS   bool `json:"tls"`
	HTTP2 bool `json:"http2"`
	// Rate caps every TCP connection, in bytes per second (0 = unlimited).
	Rate int64 `json:"rate_bps"`
	// SlowConn is the 1-based number of the accepted TCP connection that is served at
	// Rate/SlowFactor (0 = none).
	SlowConn   int   `json:"slow_conn,omitempty"`
	SlowFactor int64 `json:"slow_factor,omitempty"`
	// RTT is added to each connection's TCP handshake (its first read), to its TLS
	// handshake (its first write) and to each request before its response headers.
	RTT time.Duration `json:"rtt_ns"`
	// MaxActive answers 429 (Retry-After: 1) to requests that arrive while this many
	// are being served already (0 = no limit), as nginx's limit_conn does.
	MaxActive int `json:"max_active,omitempty"`
	// Start-up emulation (start_test.go): LineRate caps all connections together (the
	// subscriber line; 0 = none); a connection's own rate starts at SlowStartRate and
	// doubles every SlowStartDoubling (TCP slow start; 0 = off), bounded by Rate when set;
	// every request waits ServerDelay plus a deterministic 0..ServerJitter before its
	// headers (a CDN's time to first byte of a range).
	LineRate          int64         `json:"line_rate_bps,omitempty"`
	SlowStartRate     int64         `json:"slow_start_bps,omitempty"`
	SlowStartDoubling time.Duration `json:"slow_start_doubling_ns,omitempty"`
	ServerDelay       time.Duration `json:"server_delay_ns,omitempty"`
	ServerJitter      time.Duration `json:"server_jitter_ns,omitempty"`
	// SendBuffer emulates the server's socket send buffer and the bytes in flight
	// (0 = none): a connection's writes are queued up to this many bytes and delivered
	// by the pacing above, so a stream the client resets still delivers what was queued
	// before its next response (HTTP/2's reset backlog; docs/ENGINE-SPEC.md §5).
	SendBuffer int `json:"send_buffer,omitempty"`
	// RateSpread varies Rate between connections (0 = all equal): connection n gets
	// Rate·(1 − RateSpread + 2·RateSpread·h(n)) with a deterministic h(n) in [0, 1), as
	// connections sharing a real line get unequal shares.
	RateSpread float64 `json:"rate_spread,omitempty"`
}

// Pacing of the per-connection token bucket.
const (
	paceSlice = 16 << 10              // bytes written to the socket per paced step
	paceBurst = 20 * time.Millisecond // credit a connection may accumulate while idle
	bodyChunk = 64 << 10              // handler writes of the content
	sampleGap = 50 * time.Millisecond
)

// reqRecord is one request the server received.
type reqRecord struct {
	AtMs   float64 `json:"at_ms"` // since the measured start
	Conn   int64   `json:"conn"`  // 1-based accepted TCP connection
	Proto  string  `json:"proto"`
	Range  string  `json:"range,omitempty"`
	Status int     `json:"status"`
	Bytes  int64   `json:"bytes"` // body bytes written
}

// netServer is an httptest server on a throttled loopback listener.
type netServer struct {
	cfg   netCfg
	hs    *httptest.Server
	URL   string
	roots *x509.CertPool

	conns     atomic.Int64 // TCP connections accepted
	open      atomic.Int64
	maxOpen   atomic.Int64
	active    atomic.Int64 // requests being served
	maxActive atomic.Int64
	body      atomic.Int64 // response body bytes written by handlers
	wire      atomic.Int64 // bytes written to sockets (TLS and headers included)
	connBytes sync.Map     // conn number → *atomic.Int64 of wire bytes
	aborted   atomic.Int64 // connections the client gave up during the TLS handshake
	errs      atomic.Int64 // other errors net/http logged

	lineMu   sync.Mutex
	lineNext time.Time
	reqN     atomic.Int64

	mu        sync.Mutex
	start     time.Time
	base      struct{ body, wire, conns int64 } // counters at the start of the measured run
	firstBody time.Time
	reqs      []reqRecord
	samples   []sample
	stop      chan struct{}
	sampling  sync.WaitGroup
}

type connKey struct{}

// newNetServer starts a server of h on a throttled loopback listener.
func newNetServer(cfg netCfg, h http.Handler) *netServer {
	s := &netServer{cfg: cfg}
	hs := httptest.NewUnstartedServer(s.wrap(h))
	hs.Listener = &throttledListener{Listener: hs.Listener, s: s}
	hs.Config.ErrorLog = log.New(errorCounter{s}, "", 0)
	hs.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if tc, ok := c.(*tls.Conn); ok {
			c = tc.NetConn()
		}
		if t, ok := c.(*throttledConn); ok {
			return context.WithValue(ctx, connKey{}, t.n)
		}
		return ctx
	}
	if cfg.TLS {
		hs.EnableHTTP2 = cfg.HTTP2
		hs.StartTLS()
		s.roots = x509.NewCertPool()
		s.roots.AddCert(hs.Certificate())
	} else {
		hs.Start()
	}
	s.hs, s.URL = hs, hs.URL
	return s
}

// close stops sampling and shuts the server down.
func (s *netServer) close() {
	s.endSampling()
	s.hs.CloseClientConnections()
	s.hs.Close()
}

// begin marks the start of the measured run and starts sampling the body bytes.
func (s *netServer) begin(t0 time.Time) {
	s.mu.Lock()
	s.start = t0
	s.base.body, s.base.wire, s.base.conns = s.body.Load(), s.wire.Load(), s.conns.Load()
	s.stop = make(chan struct{})
	s.samples = append(s.samples[:0], sample{T: 0, Bytes: s.delivered()})
	s.mu.Unlock()
	s.sampling.Add(1)
	go func() {
		defer s.sampling.Done()
		tk := time.NewTicker(sampleGap)
		defer tk.Stop()
		for {
			select {
			case <-s.stop:
				return
			case now := <-tk.C:
				s.mu.Lock()
				s.samples = append(s.samples, sample{T: now.Sub(t0).Seconds(), Bytes: s.delivered()})
				s.mu.Unlock()
			}
		}
	}()
}

// delivered is what the timeline counts: body bytes written by the handlers, or with a
// SendBuffer the bytes that left it (TLS records and a reset stream's backlog included),
// since the handlers' writes then run ahead of the line by up to the buffer.
func (s *netServer) delivered() int64 {
	if s.cfg.SendBuffer > 0 {
		return s.wire.Load()
	}
	return s.body.Load()
}

// endSampling takes a last sample and stops the sampler.
func (s *netServer) endSampling() {
	s.mu.Lock()
	stop := s.stop
	s.stop = nil
	if stop != nil {
		s.samples = append(s.samples, sample{T: time.Since(s.start).Seconds(), Bytes: s.delivered()})
	}
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		s.sampling.Wait()
	}
}

// errorCounter counts what net/http logs instead of printing it.
type errorCounter struct{ s *netServer }

func (e errorCounter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("TLS handshake error")) {
		e.s.aborted.Add(1)
	} else {
		e.s.errs.Add(1)
	}
	return len(p), nil
}

// serverSummary is what the server saw of one run.
type serverSummary struct {
	TCPConns     int64           `json:"tcp_conns"` // accepted during the measured run
	MaxOpenConns int64           `json:"max_open_conns"`
	MaxActive    int64           `json:"max_active_requests"`
	Requests     int             `json:"requests"` // received during the measured run
	PreRequests  int             `json:"requests_before_start,omitempty"`
	Status       map[string]int  `json:"status"`
	BodyBytes    int64           `json:"body_bytes"`
	WireBytes    int64           `json:"wire_bytes"`
	ConnBytes    map[int64]int64 `json:"conn_wire_bytes,omitempty"`
	AbortedTLS   int64           `json:"aborted_tls_handshakes,omitempty"` // connections given up before a request
	ServerErrors int64           `json:"server_errors,omitempty"`
	FirstReqMs   []float64       `json:"first_requests_ms,omitempty"` // arrival of the first requests
	ReqLog       []reqRecord     `json:"req_log,omitempty"`           // the first requests in full
	Timeline     []sample        `json:"timeline,omitempty"`          // body bytes sent, sampled every 50 ms
}

func (s *netServer) summary() serverSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := serverSummary{
		TCPConns: s.conns.Load() - s.base.conns, MaxOpenConns: s.maxOpen.Load(), MaxActive: s.maxActive.Load(),
		Status: map[string]int{}, BodyBytes: s.body.Load() - s.base.body, WireBytes: s.wire.Load() - s.base.wire,
		ConnBytes: map[int64]int64{}, AbortedTLS: s.aborted.Load(), ServerErrors: s.errs.Load(),
	}
	// handlers append their record when they end: order them by arrival
	slices.SortStableFunc(s.reqs, func(a, b reqRecord) int { return cmp.Compare(a.AtMs, b.AtMs) })
	for _, r := range s.reqs {
		if r.AtMs < 0 {
			sum.PreRequests++
			continue
		}
		sum.Requests++
		sum.Status[strconv.Itoa(r.Status)]++
		if len(sum.FirstReqMs) < 12 {
			sum.FirstReqMs = append(sum.FirstReqMs, r.AtMs)
		}
	}
	sum.ReqLog = append(sum.ReqLog, s.reqs[:min(len(s.reqs), 40)]...)
	s.connBytes.Range(func(k, v any) bool {
		sum.ConnBytes[k.(int64)] = v.(*atomic.Int64).Load()
		return true
	})
	return sum
}

// timeline returns the body-byte samples of the run.
func (s *netServer) timeline() []sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sample(nil), s.samples...)
}

// firstByte returns the time from the measured start to the first body byte.
func (s *netServer) firstByte() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstBody.IsZero() || s.start.IsZero() {
		return 0
	}
	return s.firstBody.Sub(s.start)
}

func (s *netServer) since() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.start.IsZero() {
		return -1 // before the measured run (a media job's probe)
	}
	return float64(time.Since(s.start).Microseconds()) / 1000
}

// wrap adds the request round trip, the 429 limit and the accounting to h.
func (s *netServer) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.active.Add(1)
		defer s.active.Add(-1)
		raise(&s.maxActive, n)
		conn, _ := r.Context().Value(connKey{}).(int64)
		rec := reqRecord{AtMs: s.since(), Conn: conn, Proto: r.Proto, Range: r.Header.Get("Range")}
		if d := s.cfg.RTT + s.serverDelay(); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-r.Context().Done():
				t.Stop()
				return
			}
		}
		cw := &countingWriter{ResponseWriter: w, s: s}
		if s.cfg.MaxActive > 0 && n > int64(s.cfg.MaxActive) {
			cw.Header().Set("Retry-After", "1")
			http.Error(cw, "too many connections", http.StatusTooManyRequests)
		} else {
			h.ServeHTTP(cw, r)
		}
		rec.Status, rec.Bytes = cw.status(), cw.n
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()
	})
}

// serverDelay is ServerDelay plus a deterministic pseudo-random share of ServerJitter.
func (s *netServer) serverDelay() time.Duration {
	if s.cfg.ServerDelay <= 0 && s.cfg.ServerJitter <= 0 {
		return 0
	}
	h := mix(uint64(s.reqN.Add(1)))
	j := time.Duration(0)
	if s.cfg.ServerJitter > 0 {
		j = time.Duration(h % uint64(s.cfg.ServerJitter))
	}
	return s.cfg.ServerDelay + j
}

// mix is a deterministic pseudo-random hash of n.
func mix(n uint64) uint64 { return (n*0x9E3779B97F4A7C15 ^ 0xD1B54A32D192ED03) >> 11 }

// paceLine waits until n more bytes fit the shared line rate.
func (s *netServer) paceLine(n int) {
	s.lineMu.Lock()
	now := time.Now()
	if floor := now.Add(-paceBurst); s.lineNext.Before(floor) {
		s.lineNext = floor
	}
	s.lineNext = s.lineNext.Add(time.Duration(float64(n) * float64(time.Second) / float64(s.cfg.LineRate)))
	d := s.lineNext.Sub(now)
	s.lineMu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

func raise(m *atomic.Int64, v int64) {
	for {
		cur := m.Load()
		if v <= cur || m.CompareAndSwap(cur, v) {
			return
		}
	}
}

// countingWriter counts body bytes and records the status and the first body byte.
type countingWriter struct {
	http.ResponseWriter
	s    *netServer
	code int
	n    int64
}

func (c *countingWriter) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if c.n == 0 && len(p) > 0 {
		c.s.mu.Lock()
		if c.s.firstBody.IsZero() && !c.s.start.IsZero() {
			c.s.firstBody = time.Now()
		}
		c.s.mu.Unlock()
	}
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	c.s.body.Add(int64(n))
	return n, err
}

func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *countingWriter) status() int {
	if c.code == 0 {
		return http.StatusOK
	}
	return c.code
}

// throttledListener wraps accepted connections in throttledConn.
type throttledListener struct {
	net.Listener
	s *netServer
}

func (l *throttledListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	s := l.s
	n := s.conns.Add(1)
	raise(&s.maxOpen, s.open.Add(1))
	rate := s.cfg.Rate
	if f := s.cfg.RateSpread; f > 0 && rate > 0 {
		h := float64(mix(uint64(n))%1000) / 1000
		rate = max(int64(float64(rate)*(1-f+2*f*h)), 1)
	}
	if s.cfg.SlowConn > 0 && int64(s.cfg.SlowConn) == n && s.cfg.SlowFactor > 1 {
		rate = max(rate/s.cfg.SlowFactor, 1)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	counter := new(atomic.Int64)
	s.connBytes.Store(n, counter)
	tc := &throttledConn{Conn: c, s: s, n: n, rate: rate, rtt: s.cfg.RTT, tls: s.cfg.TLS, bytes: counter}
	if s.cfg.SendBuffer > 0 {
		tc.q = newSendQueue(s.cfg.SendBuffer)
		go tc.drain()
	}
	return tc, nil
}

// throttledConn delays its first read (the TCP handshake) and, under TLS, its first
// write (the server's handshake flight) by one RTT, and paces its writes with a token
// bucket of rate bytes per second.
type throttledConn struct {
	net.Conn
	s     *netServer
	n     int64
	rate  int64
	rtt   time.Duration
	tls   bool
	bytes *atomic.Int64

	readOnce  sync.Once
	wmu       sync.Mutex
	wroteOnce bool
	next      time.Time // token bucket: when the bytes paced so far are due
	ssStart   time.Time // first paced write (slow start reference)
	closeOnce sync.Once
	q         *sendQueue // SendBuffer: writes queued for drain; nil = written directly
}

func (c *throttledConn) Read(p []byte) (int, error) {
	c.readOnce.Do(func() {
		if c.rtt > 0 {
			time.Sleep(c.rtt)
		}
	})
	return c.Conn.Read(p)
}

func (c *throttledConn) Write(p []byte) (int, error) {
	if c.q != nil {
		return c.q.put(p)
	}
	return c.writePaced(p)
}

// drain delivers the send queue with the connection's pacing until it is closed or a
// write fails.
func (c *throttledConn) drain() {
	buf := make([]byte, paceSlice)
	for {
		n, ok := c.q.take(buf)
		if !ok {
			return
		}
		if _, err := c.writePaced(buf[:n]); err != nil {
			c.q.fail(err)
			return
		}
	}
}

// writePaced writes p to the socket at the connection's pace.
func (c *throttledConn) writePaced(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if !c.wroteOnce {
		c.wroteOnce = true
		if c.tls && c.rtt > 0 {
			time.Sleep(c.rtt)
		}
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		cfg := c.s.cfg
		if c.rate > 0 || cfg.SlowStartRate > 0 || cfg.LineRate > 0 {
			n = min(n, paceSlice)
			if r := c.rateNow(); r > 0 {
				c.pace(n, r)
			}
			if cfg.LineRate > 0 {
				c.s.paceLine(n)
			}
		}
		m, err := c.Conn.Write(p[:n])
		total += m
		c.s.wire.Add(int64(m))
		c.bytes.Add(int64(m))
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

// rateNow is the connection's rate: its slow-start rate (doubling from the first paced
// write) bounded by its fixed rate; 0 = unlimited.
func (c *throttledConn) rateNow() int64 {
	cfg := c.s.cfg
	if cfg.SlowStartRate <= 0 || cfg.SlowStartDoubling <= 0 {
		return c.rate
	}
	now := time.Now()
	if c.ssStart.IsZero() {
		c.ssStart = now
	}
	k := float64(now.Sub(c.ssStart)) / float64(cfg.SlowStartDoubling)
	r := float64(cfg.SlowStartRate) * math.Pow(2, min(k, 40))
	if c.rate > 0 && r > float64(c.rate) {
		return c.rate
	}
	if r > 1<<40 {
		return 0
	}
	return int64(r)
}

// pace waits until n more bytes fit rate bytes per second.
func (c *throttledConn) pace(n int, rate int64) {
	now := time.Now()
	if floor := now.Add(-paceBurst); c.next.Before(floor) {
		c.next = floor
	}
	c.next = c.next.Add(time.Duration(float64(n) * float64(time.Second) / float64(rate)))
	if d := c.next.Sub(now); d > 0 {
		time.Sleep(d)
	}
}

func (c *throttledConn) Close() error {
	c.closeOnce.Do(func() { c.s.open.Add(-1) })
	if c.q != nil {
		c.q.fail(net.ErrClosed) // what is still queued is dropped
	}
	return c.Conn.Close()
}

// sendQueue is a connection's emulated send buffer: put blocks while size bytes are
// queued; take hands the oldest bytes to the drainer.
type sendQueue struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	size int
	err  error // set once the connection is closed or a write failed
}

func newSendQueue(size int) *sendQueue {
	q := &sendQueue{size: size}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *sendQueue) put(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	total := 0
	for len(p) > 0 {
		for q.err == nil && len(q.buf) >= q.size {
			q.cond.Wait()
		}
		if q.err != nil {
			return total, q.err
		}
		k := min(len(p), q.size-len(q.buf))
		q.buf = append(q.buf, p[:k]...)
		p, total = p[k:], total+k
		q.cond.Broadcast()
	}
	return total, nil
}

// take copies up to len(p) queued bytes into p and removes them; ok is false once the
// queue failed.
func (q *sendQueue) take(p []byte) (n int, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.err == nil && len(q.buf) == 0 {
		q.cond.Wait()
	}
	if q.err != nil {
		return 0, false
	}
	n = copy(p, q.buf)
	q.buf = q.buf[n:]
	if len(q.buf) == 0 {
		q.buf = q.buf[:0:0] // release the drained backing array
	}
	q.cond.Broadcast()
	return n, true
}

func (q *sendQueue) fail(err error) {
	q.mu.Lock()
	if q.err == nil {
		q.err = err
	}
	q.mu.Unlock()
	q.cond.Broadcast()
}

// ---- handlers -----------------------------------------------------------------------

// fileHandler serves the content of contentBlock with a size of size bytes.
type fileHandler struct {
	size int64
	// noRanges ignores Range (200 with Content-Length); unknownSize also leaves the
	// length out (200, chunked in HTTP/1.1).
	noRanges, unknownSize bool
}

const perfETag = `"perf-v1"`

var perfModified = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)

func (f fileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("ETag", perfETag)
	h.Set("Last-Modified", perfModified)
	start, end, status := int64(0), f.size-1, http.StatusOK
	if !f.noRanges && !f.unknownSize {
		h.Set("Accept-Ranges", "bytes")
		ir := r.Header.Get("If-Range")
		if rg := r.Header.Get("Range"); rg != "" && (ir == "" || ir == perfETag || ir == perfModified) {
			a, b, ok := parseRange(rg, f.size)
			switch {
			case !ok:
				h.Set("Content-Range", fmt.Sprintf("bytes */%d", f.size))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			default:
				start, end, status = a, b, http.StatusPartialContent
				h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, f.size))
			}
		}
	}
	if !f.unknownSize {
		h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead || f.size == 0 {
		return
	}
	_, _ = writeContent(w, start, end, bodyChunk)
}

// parseRange parses a single "bytes=a-b" or "bytes=a-" range against size.
func parseRange(v string, size int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(v, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if b != "" {
		e, err := strconv.ParseInt(b, 10, 64)
		if err != nil || e < start {
			return 0, 0, false
		}
		end = min(e, size-1)
	}
	return start, end, true
}

// filesHandler serves fixed bodies by path, with ranges (http.ServeContent).
type filesHandler map[string]servedFile

type servedFile struct {
	data []byte
	mime string
}

func (fh filesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := fh[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.mime)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
}
