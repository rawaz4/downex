package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

func TestHelloStatusPing(t *testing.T) {
	be := &fakeBackend{st: &downexv1.StatusResponse{Version: "1.2.3", Active: 2, Queued: 5}}
	s := startSession(t, be)

	s.send(map[string]any{"id": "h", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.3.0"})
	if m := s.recv(); m["id"] != "h" || m["ok"] != true || m["type"] != "welcome" || m["protocol"] != 1.0 || m["daemonVersion"] != "1.2.3" {
		t.Fatalf("hello: %v", m)
	}
	s.send(map[string]any{"id": "s", "type": "status"})
	if m := s.recv(); m["type"] != "status" || m["running"] != true || m["active"] != 2.0 || m["queued"] != 5.0 {
		t.Fatalf("status: %v", m)
	}
	s.send(map[string]any{"id": "p", "type": "ping"})
	if m := s.recv(); m["id"] != "p" || m["ok"] != true || m["type"] != "pong" {
		t.Fatalf("ping: %v", m)
	}

	// No daemon: hello still welcomes (without a version), status says so.
	be.mu.Lock()
	be.st = nil
	be.mu.Unlock()
	s.send(map[string]any{"id": "h2", "type": "hello", "protocol": 1, "browser": "firefox", "extVersion": "x"})
	if m := s.recv(); m["type"] != "welcome" || m["daemonVersion"] != "" {
		t.Fatalf("hello without daemon: %v", m)
	}
	s.send(map[string]any{"id": "s2", "type": "status"})
	if m := s.recv(); m["type"] != "status" || m["running"] != false || m["active"] != 0.0 || m["queued"] != 0.0 {
		t.Fatalf("status without daemon: %v", m)
	}
	// An unreachable endpoint fails status, but not hello.
	be.mu.Lock()
	be.stErr = &unavailableError{errors.New("the endpoint is held by a foreign process")}
	be.mu.Unlock()
	s.send(map[string]any{"id": "s3", "type": "status"})
	if m := s.recv(); errorCode(m) != codeDaemonUnavailable || m["id"] != "s3" {
		t.Fatalf("status with a foreign endpoint: %v", m)
	}
	s.send(map[string]any{"id": "h3", "type": "hello", "protocol": 1, "browser": "edge"})
	if m := s.recv(); m["type"] != "welcome" {
		t.Fatalf("hello with a foreign endpoint: %v", m)
	}
	if err := s.closeInput(); err != nil {
		t.Fatalf("serve after stdin EOF: %v", err)
	}
}

// TestHelloDoesNotWaitForAHungDaemon checks that a daemon that accepts connections but
// never answers delays the welcome by about a second, well within the extension's
// 5 s hello timeout, and that a hello asks the backend to start downexd.
func TestHelloDoesNotWaitForAHungDaemon(t *testing.T) {
	be := &fakeBackend{stHang: true}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "h", "type": "hello", "protocol": 1, "browser": "firefox"})
	m := s.recv()
	if m["type"] != "welcome" || m["daemonVersion"] != "" {
		t.Fatalf("hello with a hung daemon: %v", m)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("welcome after %v; the extension gives up on a hello after 5 s", d)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if be.warms != 1 {
		t.Fatalf("hello started downexd %d times, want once", be.warms)
	}
}

// TestAddHonoursDeadline checks that an add with deadlineMs is answered before the
// extension gives up, and that the backend gets a deadline that leaves time for the
// answer to arrive.
func TestAddHonoursDeadline(t *testing.T) {
	never := make(chan struct{})
	be := &fakeBackend{gate: func(*downexv1.AddRequest) <-chan struct{} { return never }}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "a", "type": "add", "url": "https://example.test/f.zip", "deadlineMs": 600})
	m := s.recv()
	elapsed := time.Since(start)
	if errorCode(m) != codeDaemonUnavailable || m["id"] != "a" {
		t.Fatalf("add past its deadline: %v", m)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("answered after %v, want about the 600 ms deadline", elapsed)
	}
	be.mu.Lock()
	dl := be.deadlines[0]
	be.mu.Unlock()
	if dl.IsZero() || dl.Sub(start) > 600*time.Millisecond-responseMargin+100*time.Millisecond {
		t.Fatalf("backend deadline %v after the request, want at most %v", dl.Sub(start), 600*time.Millisecond-responseMargin)
	}
	// Without deadlineMs the relay's own bound applies.
	close(never)
	s.send(map[string]any{"id": "b", "type": "add", "url": "https://example.test/g.zip"})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("add without a deadline: %v", m)
	}
	be.mu.Lock()
	dl = be.deadlines[1]
	be.mu.Unlock()
	if d := time.Until(dl); d < addTimeout-5*time.Second || d > addTimeout {
		t.Fatalf("backend deadline in %v, want about %v", d, addTimeout)
	}
}

func TestAddMapsFields(t *testing.T) {
	be := &fakeBackend{}
	s := startSession(t, be)
	body := []byte("a=1&b=\x00\xff")
	s.send(map[string]any{
		"id": "a1", "type": "add", "url": "https://example.test/file.zip?sig=secret", "method": "POST",
		"headers":  [][]string{{"Cookie", "session=s3cr3t"}, {"user-agent", "UA/1"}},
		"body":     base64.StdEncoding.EncodeToString(body),
		"filename": "report.pdf", "referrer": "https://example.test/page", "mime": "application/pdf",
		"size": 12345, "startPaused": true,
	})
	m := s.recv()
	if m["id"] != "a1" || m["ok"] != true || m["type"] != "added" || m["downloadId"] != 1.0 {
		t.Fatalf("add: %v", m)
	}
	be.mu.Lock()
	req := be.adds[0]
	be.mu.Unlock()
	if req.GetUrl() != "https://example.test/file.zip?sig=secret" || req.GetMethod() != "POST" ||
		!bytes.Equal(req.GetBody(), body) || req.GetFilename() != "report.pdf" || !req.GetStartPaused() || req.GetDir() != "" ||
		req.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || req.GetExpectedSize() != 12345 || req.GetMime() != "application/pdf" {
		t.Fatalf("forwarded %v", req)
	}
	var got []string
	for _, h := range req.GetHeaders() {
		got = append(got, h.GetName()+": "+h.GetValue())
	}
	if want := "Cookie: session=s3cr3t|user-agent: UA/1|Referer: https://example.test/page"; strings.Join(got, "|") != want {
		t.Fatalf("headers %q, want %q", got, want)
	}

	// An explicit Referer header wins over the referrer field; GET is the default method.
	s.send(map[string]any{"id": "a2", "type": "add", "url": "http://example.test/x",
		"headers": [][]string{{"referer", "https://a.example/"}}, "referrer": "https://b.example/"})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("add: %v", m)
	}
	be.mu.Lock()
	req = be.adds[1]
	be.mu.Unlock()
	if req.GetMethod() != "GET" || len(req.GetHeaders()) != 1 || req.GetHeaders()[0].GetValue() != "https://a.example/" {
		t.Fatalf("forwarded %v", req)
	}
}

func TestAddErrorsAreMapped(t *testing.T) {
	cases := map[string]struct {
		err  error
		code string
	}{
		"invalid":     {connect.NewError(connect.CodeInvalidArgument, errors.New("dir must be absolute")), codeRejected},
		"precond":     {connect.NewError(connect.CodeFailedPrecondition, errors.New("disk full")), codeRejected},
		"unavailable": {connect.NewError(connect.CodeUnavailable, errors.New("downexd is shutting down")), codeDaemonUnavailable},
		"deadline":    {connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), codeDaemonUnavailable},
		"autostart":   {&unavailableError{fmt.Errorf("cannot start downexd: %w", client.ErrElevatedStart)}, codeDaemonUnavailable},
		"internal":    {connect.NewError(connect.CodeInternal, errors.New("store: boom")), codeInternal},
		"plain":       {errors.New("weird"), codeInternal},
		"ctx":         {context.DeadlineExceeded, codeDaemonUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := startSession(t, &fakeBackend{addErr: c.err})
			s.send(map[string]any{"id": name, "type": "add", "url": "https://example.test/f"})
			m := s.recv()
			if errorCode(m) != c.code || m["id"] != name || m["ok"] != false {
				t.Fatalf("response %v, want code %s", m, c.code)
			}
			if msg := m["error"].(map[string]any)["message"].(string); msg == "" {
				t.Fatal("error without a message")
			}
		})
	}
}

func TestInvalidRequestsAreAnswered(t *testing.T) {
	s := startSession(t, &fakeBackend{})
	sendAndExpect := func(id string, raw []byte) {
		t.Helper()
		s.sendRaw(frame(raw))
		m := s.recv()
		if m["id"] != id || errorCode(m) != codeInvalidRequest {
			t.Fatalf("%s: %v", raw, m)
		}
	}
	sendAndExpect("u1", []byte(`{"id":"u1","type":"launch"}`))
	sendAndExpect("u2", []byte(`{"id":"u2"}`))
	sendAndExpect("u3", []byte(`{"id":"u3","type":"add","url":"file:///etc/passwd"}`))
	sendAndExpect("u4", []byte("{\"id\":\"u4\",\"type\":\"ping\",\"x\":\"\xff\xfe\"}")) // invalid UTF-8, id readable
	sendAndExpect("u5", []byte(`{"id":"u5","type":"hello","protocol":2,"browser":"chrome"}`))
	// The relay keeps serving.
	s.send(map[string]any{"id": "ok", "type": "ping"})
	if m := s.recv(); m["type"] != "pong" {
		t.Fatalf("ping after invalid requests: %v", m)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

func TestUnanswerableMessagesStopTheRelay(t *testing.T) {
	long := `{"id":"` + strings.Repeat("i", maxIDBytes+1) + `","type":"ping"}`
	cases := map[string][]byte{
		"not JSON":          frame([]byte(`{"id":"x",`)),
		"JSON array":        frame([]byte(`["ping"]`)),
		"no id":             frame([]byte(`{"type":"ping"}`)),
		"empty id":          frame([]byte(`{"id":"","type":"ping"}`)),
		"numeric id":        frame([]byte(`{"id":7,"type":"ping"}`)),
		"id too long":       frame([]byte(long)),
		"bad UTF-8, no id":  frame([]byte("{\"type\":\"\xff\"}")),
		"zero length":       {0, 0, 0, 0},
		"oversized":         oversizedHeader(),
		"truncated message": frame([]byte(`{"id":"t","type":"ping"}`))[:10],
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			s := startSession(t, &fakeBackend{})
			s.sendRaw(in)
			_ = s.in.Close()
			if err := s.wait(); err == nil {
				t.Fatal("serve returned nil, want an error")
			}
		})
	}
}

func oversizedHeader() []byte {
	b := frame(nil)
	b[0], b[1], b[2], b[3] = 0xff, 0xff, 0xff, 0x7f
	return b
}

// TestConcurrentRequestsOutOfOrder serves maxInFlight adds that complete in the
// reverse order of their arrival, plus pings answered meanwhile, and then a burst of
// many more requests than can be in flight.
func TestConcurrentRequestsOutOfOrder(t *testing.T) {
	gates := make(map[string]chan struct{})
	var gmu sync.Mutex
	gateOf := func(url string) chan struct{} {
		gmu.Lock()
		defer gmu.Unlock()
		if gates[url] == nil {
			gates[url] = make(chan struct{})
		}
		return gates[url]
	}
	started := make(chan string, 1024)
	be := &fakeBackend{started: started, gate: func(req *downexv1.AddRequest) <-chan struct{} { return gateOf(req.GetUrl()) }}
	s := startSession(t, be)

	const n = maxInFlight - 1 // leaves room for the pings
	for i := range n {
		s.send(map[string]any{"id": fmt.Sprintf("add-%d", i), "type": "add", "url": fmt.Sprintf("https://example.test/%d", i)})
	}
	for range n {
		<-started
	}
	// A ping is answered while every add is still waiting.
	s.send(map[string]any{"id": "ping-1", "type": "ping"})
	if m := s.recv(); m["id"] != "ping-1" {
		t.Fatalf("ping while adds wait: %v", m)
	}
	// Release the adds in reverse order: responses follow the completion order.
	for i := n - 1; i >= 0; i-- {
		close(gateOf(fmt.Sprintf("https://example.test/%d", i)))
		m := s.recv()
		if m["id"] != fmt.Sprintf("add-%d", i) || m["type"] != "added" {
			t.Fatalf("after releasing add %d: %v", i, m)
		}
		be.mu.Lock()
		want := be.ids[fmt.Sprintf("https://example.test/%d", i)]
		be.mu.Unlock()
		if m["downloadId"] != float64(want) {
			t.Fatalf("add-%d answered with download %v, want %d", i, m["downloadId"], want)
		}
	}

	// A burst far beyond maxInFlight, completing in whatever order.
	be.mu.Lock()
	be.gate, be.started = nil, nil
	be.mu.Unlock()
	const burst = 300
	go func() {
		for i := range burst {
			if i%3 == 0 {
				s.send(map[string]any{"id": fmt.Sprintf("b-%d", i), "type": "ping"})
			} else {
				s.send(map[string]any{"id": fmt.Sprintf("b-%d", i), "type": "add", "url": fmt.Sprintf("https://burst.test/%d", i)})
			}
		}
	}()
	seen := make(map[string]bool)
	for range burst {
		m := s.recv()
		id, _ := m["id"].(string)
		if seen[id] || !strings.HasPrefix(id, "b-") || m["ok"] != true {
			t.Fatalf("burst response %v", m)
		}
		seen[id] = true
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// TestDisconnectCancelsRequests checks that closing stdin cancels adds in progress
// and that the relay then exits cleanly.
func TestDisconnectCancelsRequests(t *testing.T) {
	never := make(chan struct{})
	started := make(chan string, 1)
	be := &fakeBackend{started: started, gate: func(*downexv1.AddRequest) <-chan struct{} { return never }}
	s := startSession(t, be)
	s.send(map[string]any{"id": "a", "type": "add", "url": "https://example.test/slow"})
	<-started
	if err := s.closeInput(); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestRunIgnoresBrowserArguments runs the relay as browsers start it: with their
// arguments, which are not commands, and stdin at EOF at once.
func TestRunIgnoresBrowserArguments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("HOME", dir) // macOS: ~/Library/Logs/Downex
	for _, args := range [][]string{
		{"chrome-extension://dlgpbdafcmciojddbbjikgeeefipejcn/", "--parent-window=1234"},
		{"/home/u/.mozilla/native-messaging-hosts/com.downex.host.json", "extension@downex.app"},
		nil,
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != exitOK || stdout.Len() != 0 {
			t.Errorf("run(%q) = %d, stdout %q, stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
	// The log went to the per-user log directory.
	logs, _ := filepath.Glob(filepath.Join(dir, "*", "*", logFileName))
	logs2, _ := filepath.Glob(filepath.Join(dir, "*", logFileName))
	logs3, _ := filepath.Glob(filepath.Join(dir, "Library", "Logs", "Downex", logFileName))
	if len(logs)+len(logs2)+len(logs3) == 0 {
		t.Error("no nmhost.log was written")
	}
	// A bad frame is an error exit, never output.
	var stdout bytes.Buffer
	if code := run(context.Background(), nil, bytes.NewReader([]byte{0, 0, 0, 0}), &stdout, io.Discard); code != exitError || stdout.Len() != 0 {
		t.Errorf("zero-length frame: exit %d, stdout %q", code, stdout.String())
	}
	if code := run(context.Background(), []string{"version"}, nil, &stdout, io.Discard); code != exitOK ||
		!strings.Contains(stdout.String(), "downex-nmhost") {
		t.Errorf("version: %d %q", code, stdout.String())
	}
}

func TestLogNeverRecordsSecrets(t *testing.T) {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	r := &relay{be: &fakeBackend{}, log: newTestJSONLogger(w)}
	in := bytes.NewBuffer(nil)
	in.Write(jsonFrame(t, map[string]any{
		"id": "a", "type": "add", "url": "https://user:pw@example.test/f.bin?token=abc", "method": "POST",
		"headers": [][]string{{"Cookie", "sid=cookie-secret"}, {"Authorization", "Bearer auth-secret"}},
		"body":    base64.StdEncoding.EncodeToString([]byte("body-secret")),
	}))
	in.Write(jsonFrame(t, map[string]any{"id": "b", "type": "add", "url": "https://example.test/x",
		"headers": [][]string{{"Cookie", "bad\r\nvalue-secret"}}}))
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	log := w.buf.String()
	w.mu.Unlock()
	for _, secret := range []string{"cookie-secret", "auth-secret", "body-secret", "token=abc", "pw@", "value-secret", "Ym9keS1zZWNyZXQ"} {
		if strings.Contains(log, secret) || strings.Contains(out.String(), secret) {
			t.Errorf("%q leaked:\nlog: %s\nout: %s", secret, log, out.String())
		}
	}
	if !strings.Contains(log, "https://[redacted]@example.test/f.bin?[redacted]") {
		t.Errorf("log lacks the redacted URL: %s", log)
	}
}

func newTestJSONLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
