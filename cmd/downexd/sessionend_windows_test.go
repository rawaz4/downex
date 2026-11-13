//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"connectrpc.com/connect"
	"golang.org/x/sys/windows"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

var procSendMessageW = user32.NewProc("SendMessageW")

const endSessionLogoff = 0x80000000 // ENDSESSION_LOGOFF

func sendMessage(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

// TestSessionWatcher: WM_QUERYENDSESSION starts the stop and allows the end of the
// session; WM_ENDSESSION does not return (so Windows does not terminate the process)
// until the daemon has finished.
func TestSessionWatcher(t *testing.T) {
	ended := make(chan struct{})
	w, err := watchSessionEnd(func() { close(ended) })
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	if r := sendMessage(w.hwnd, wmQueryEndSession, 0, endSessionLogoff); r != 1 {
		t.Fatalf("WM_QUERYENDSESSION returned %d, want TRUE", r)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the end of the session was not reported")
	}
	returned := make(chan struct{})
	go func() {
		sendMessage(w.hwnd, wmEndSession, 1, endSessionLogoff)
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("WM_ENDSESSION returned before the daemon finished")
	case <-time.After(300 * time.Millisecond):
	}
	w.finished()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("WM_ENDSESSION did not return after the daemon finished")
	}
	// A second watcher in the same process works independently.
	w2, err := watchSessionEnd(func() {})
	if err != nil {
		t.Fatal(err)
	}
	w2.close()
}

// daemonWindow finds the session watcher window of process pid.
func daemonWindow(pid int) windows.HWND {
	var found windows.HWND
	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var owner uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &owner); err != nil || int(owner) != pid {
			return 1
		}
		buf := make([]uint16, 256)
		if n, err := windows.GetClassName(hwnd, &buf[0], int32(len(buf))); err == nil &&
			strings.HasPrefix(windows.UTF16ToString(buf[:n]), sessionWindowClass) {
			found = hwnd
			return 0
		}
		return 1
	})
	_ = windows.EnumWindows(cb, unsafe.Pointer(nil))
	return found
}

// TestSessionEndPausesDownloads (L3): when the user session ends, Windows sends the
// end-of-session messages to the daemon's windows (it gets no console events). The
// daemon pauses its running download, commits it as queued for the next start, and
// exits 0 before WM_ENDSESSION returns.
func TestSessionEndPausesDownloads(t *testing.T) {
	const size, seed = 8 << 20, 17
	ts := testserver.New(testserver.Options{Size: size, Seed: seed, ETag: `"s"`})
	t.Cleanup(ts.Close)
	endpoint := testEndpoint(t)
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	args := []string{"--db", filepath.Join(dir, "downex.db"), "--endpoint", endpoint, "--log-dir", logDir}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "DOWNEXD_TEST_MAIN="+strings.Join(args, "\n"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	c := dial(t, endpoint)
	waitReady(t, c, &daemonRun{done: make(chan int), stderr: &syncBuffer{}})
	res, err := c.dl.Add(t.Context(), connect.NewRequest(&downexv1.AddRequest{
		Url: ts.URL + "/session.bin", Dir: filepath.Join(dir, "out"), SpeedLimit: 512 << 10,
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.GetDownload().GetId()
	deadline := time.Now().Add(20 * time.Second)
	for {
		g, err := c.dl.Get(t.Context(), connect.NewRequest(&downexv1.GetRequest{Id: id}))
		if err == nil && g.Msg.GetDownload().GetReceived() > 64<<10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	hwnd := daemonWindow(cmd.Process.Pid)
	if hwnd == 0 {
		t.Fatal("downexd has no window to receive the end of the user session")
	}
	if r := sendMessage(hwnd, wmQueryEndSession, 0, endSessionLogoff); r != 1 {
		t.Fatalf("WM_QUERYENDSESSION returned %d", r)
	}
	start := time.Now()
	sendMessage(hwnd, wmEndSession, 1, endSessionLogoff) // returns once the daemon stopped
	t.Logf("WM_ENDSESSION returned after %v", time.Since(start))
	// Windows may terminate the process from now on: everything must be committed.
	if logs, err := os.ReadFile(filepath.Join(logDir, logFileName)); err != nil ||
		!bytes.Contains(logs, []byte(`"msg":"downexd stopped"`)) {
		t.Errorf("WM_ENDSESSION returned before downexd stopped (%v):\n%s", err, logs)
	}
	select {
	case <-exited:
		if waitErr != nil {
			t.Fatalf("downexd exited with %v: %s", waitErr, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("downexd did not exit after the end of the session")
	}
	logs, err := os.ReadFile(filepath.Join(logDir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"msg":"stopping: the user session is ending"`, `"msg":"downexd stopped"`} {
		if !bytes.Contains(logs, []byte(want)) {
			t.Errorf("log lacks %s:\n%s", want, logs)
		}
	}
	if bytes.Contains(logs, []byte("did not pause in time")) {
		t.Errorf("the download was not paused in time:\n%s", logs)
	}
	st, err := store.OpenSQLite(filepath.Join(dir, "downex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec, err := st.GetDownload(t.Context(), model.DownloadID(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != model.StateQueued {
		t.Errorf("download state after the session ended = %v, want queued for the next start", rec.State)
	}
}
