//go:build windows || linux || darwin

package ipc

// Platform-neutral endpoint tests. Each platform test file provides
// testEndpoint(t) returning a fresh, unused endpoint for the test.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// listenT listens on ep and closes the listener when the test ends.
func listenT(t *testing.T, ep string) net.Listener {
	t.Helper()
	l, err := Listen(ep)
	if err != nil {
		t.Fatalf("Listen(%q): %v", ep, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// dialT dials ep with a bounded context.
func dialT(t *testing.T, ep string) (net.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return Dial(ctx, ep)
}

// serveLineEcho answers each connection's first line with "echo: <line>" and closes it.
func serveLineEcho(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "echo: "+line)
		}(c)
	}
}

func TestRoundTripH2C(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)

	srv := NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s proto=%d", r.Method, r.URL.Path, r.ProtoMajor)
	}))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}
	})

	client := NewHTTPClient(ep)
	t.Cleanup(client.CloseIdleConnections)
	for i := range 3 { // several requests reuse one HTTP/2 connection
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/ping/%d", BaseURL, i), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read body %d: %v", i, err)
		}
		if resp.ProtoMajor != 2 {
			t.Errorf("client saw HTTP/%d.%d, want HTTP/2", resp.ProtoMajor, resp.ProtoMinor)
		}
		if want := fmt.Sprintf("GET /ping/%d proto=2", i); string(body) != want {
			t.Errorf("body = %q, want %q", body, want)
		}
	}
}

func TestListenTwiceIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)

	l2, err := Listen(ep)
	if err == nil {
		_ = l2.Close()
		t.Fatal("second Listen succeeded, want ErrAlreadyRunning")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen: %v, want ErrAlreadyRunning", err)
	}

	// The first listener keeps serving.
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatalf("Dial after failed second Listen: %v", err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "still here\n"); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || got != "echo: still here\n" {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

func TestListenAgainAfterClose(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l, err := Listen(ep)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l2 := listenT(t, ep)
	go serveLineEcho(l2)
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatalf("Dial after re-Listen: %v", err)
	}
	_ = c.Close()
}

func TestDialNotRunning(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	c, err := dialT(t, ep)
	if err == nil {
		_ = c.Close()
		t.Fatal("Dial succeeded with nothing listening")
	}
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial: %v, want ErrNotRunning", err)
	}

	// Also after a daemon has come and gone.
	l, err := Listen(ep)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if _, err := dialT(t, ep); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial after Close: %v, want ErrNotRunning", err)
	}
}

func TestDialCanceledContext(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c, err := Dial(ctx, ep)
	if err == nil {
		_ = c.Close()
		t.Fatal("Dial with a canceled context succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial: %v, want context.Canceled", err)
	}
}

func TestConcurrentDials(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			c, err := dialT(t, ep)
			if err != nil {
				errs <- fmt.Errorf("dial %d: %w", i, err)
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			msg := fmt.Sprintf("client %d", i)
			if _, err := io.WriteString(c, msg+"\n"); err != nil {
				errs <- fmt.Errorf("write %d: %w", i, err)
				return
			}
			got, err := bufio.NewReader(c).ReadString('\n')
			if err != nil {
				errs <- fmt.Errorf("read %d: %w", i, err)
				return
			}
			if want := "echo: " + msg + "\n"; got != want {
				errs <- fmt.Errorf("client %d got %q, want %q", i, got, want)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestConcurrentHTTPRequests(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	srv := NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.TrimPrefix(r.URL.Path, "/"))
	}))
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := NewHTTPClient(ep)
	t.Cleanup(client.CloseIdleConnections)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/%d", BaseURL, i), nil)
			if err != nil {
				t.Error(err)
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != fmt.Sprint(i) {
				t.Errorf("request %d: body %q, %v", i, body, err)
			}
		})
	}
	wg.Wait()
}

func TestInstanceLockExclusive(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)

	release, err := AcquireInstanceLock(ep)
	if err != nil {
		t.Fatalf("first AcquireInstanceLock: %v", err)
	}
	if r2, err := AcquireInstanceLock(ep); err == nil {
		r2()
		release()
		t.Fatal("second AcquireInstanceLock succeeded while the first is held")
	} else if !errors.Is(err, ErrAlreadyRunning) {
		release()
		t.Fatalf("second AcquireInstanceLock: %v, want ErrAlreadyRunning", err)
	}

	// A different endpoint has its own lock.
	other := testEndpoint(t)
	rOther, err := AcquireInstanceLock(other)
	if err != nil {
		release()
		t.Fatalf("AcquireInstanceLock(other endpoint): %v", err)
	}
	rOther()

	release()
	release() // idempotent

	r3, err := AcquireInstanceLock(ep)
	if err != nil {
		t.Fatalf("AcquireInstanceLock after release: %v", err)
	}
	r3()
}
