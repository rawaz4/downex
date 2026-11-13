package e2e

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// nmhostProc is downex-nmhost started as a browser starts it: with the caller's origin
// as argument and stdin/stdout as the message pipes.
type nmhostProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr syncBuffer
	exited chan struct{} // closed when the process has exited
	err    error         // its Wait result, valid after exited
}

func (h *harness) startNMHost(env []string) *nmhostProc {
	h.t.Helper()
	p := &nmhostProc{t: h.t, exited: make(chan struct{})}
	p.cmd = exec.Command(bin.nmhost, "chrome-extension://dlgpbdafcmciojddbbjikgeeefipejcn/", "--parent-window=0")
	p.cmd.Env, p.cmd.Dir, p.cmd.Stderr = env, h.root, &p.stderr
	in, err := p.cmd.StdinPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	p.stdin, p.stdout = in, bufio.NewReader(out)
	if err := p.cmd.Start(); err != nil {
		h.t.Fatalf("start downex-nmhost: %v", err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.exited) }()
	h.t.Cleanup(func() {
		_ = p.stdin.Close()
		select {
		case <-p.exited:
		case <-time.After(exitTimeout):
			_ = p.cmd.Process.Kill()
			h.t.Errorf("downex-nmhost did not exit after its stdin closed")
		}
		if strings.Contains(p.stderr.String(), "WARNING: DATA RACE") {
			h.t.Errorf("data race in downex-nmhost:\n%s", p.stderr.String())
		}
	})
	return p
}

// send writes one message with its native-endian length prefix.
func (p *nmhostProc) send(v any) {
	p.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	frame := make([]byte, 4+len(b))
	binary.NativeEndian.PutUint32(frame, uint32(len(b)))
	copy(frame[4:], b)
	if _, err := p.stdin.Write(frame); err != nil {
		p.t.Fatalf("write to downex-nmhost: %v\nstderr: %s", err, p.stderr.String())
	}
}

// recv reads one message, failing the test after completeTimeout.
func (p *nmhostProc) recv() map[string]any {
	p.t.Helper()
	type result struct {
		m   map[string]any
		err error
	}
	c := make(chan result, 1)
	go func() {
		var hdr [4]byte
		if _, err := io.ReadFull(p.stdout, hdr[:]); err != nil {
			c <- result{err: err}
			return
		}
		n := binary.NativeEndian.Uint32(hdr[:])
		if n > 1<<20 {
			c <- result{err: fmt.Errorf("response of %d bytes", n)}
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(p.stdout, b); err != nil {
			c <- result{err: err}
			return
		}
		var m map[string]any
		err := json.Unmarshal(b, &m)
		c <- result{m: m, err: err}
	}()
	select {
	case r := <-c:
		if r.err != nil {
			p.t.Fatalf("read from downex-nmhost: %v\nstderr: %s", r.err, p.stderr.String())
		}
		return r.m
	case <-time.After(completeTimeout):
		p.t.Fatalf("no answer from downex-nmhost\nstderr: %s", p.stderr.String())
		return nil
	}
}

// closeAndWait closes stdin, as a browser closing the port does, and expects a clean
// exit with nothing more on stdout.
func (p *nmhostProc) closeAndWait() {
	p.t.Helper()
	_ = p.stdin.Close()
	rest, _ := io.ReadAll(p.stdout)
	select {
	case <-p.exited:
		if p.err != nil {
			p.t.Fatalf("downex-nmhost exited with %v\nstderr: %s", p.err, p.stderr.String())
		}
	case <-time.After(exitTimeout):
		p.t.Fatal("downex-nmhost did not exit after its stdin closed")
	}
	if len(rest) > 0 {
		p.t.Fatalf("unexpected output after the last response: %q", rest)
	}
}

// setDefaultDir points the daemon's default download directory at dir: the relay's
// adds carry no directory.
func (h *harness) setDefaultDir(dir string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	got, err := h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		h.t.Fatal(err)
	}
	s := got.Msg.GetSettings()
	s.DefaultDir = dir
	if _, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); err != nil {
		h.t.Fatal(err)
	}
}

// The relay, spoken to as a browser does, hands a download to a running downexd.
func TestNativeMessagingAdd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 2<<20 + 5, 301
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"n1"`, Filename: "from-browser.bin"})
	// Installed with the (stand-in) window, which lets the daemon ask for confirmation.
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	st := h.waitReady(d)
	h.setDefaultDir(h.out)

	p := h.startNMHost(h.env)
	p.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	p.send(map[string]any{"id": "2", "type": "add", "url": srv.URL + "/dl?token=secret", "method": "GET",
		"headers":  [][]string{{"Cookie", "session=nm-cookie-secret"}, {"User-Agent", "E2E/1"}},
		"referrer": "https://example.test/page"})
	got := map[string]map[string]any{}
	for range 2 {
		m := p.recv()
		got[fmt.Sprint(m["id"])] = m
	}
	if w := got["1"]; w["ok"] != true || w["type"] != "welcome" || w["protocol"] != 1.0 || w["daemonVersion"] != st.GetVersion() {
		t.Fatalf("hello answered %v", w)
	}
	a := got["2"]
	id, ok := a["downloadId"].(float64)
	if a["ok"] != true || a["type"] != "added" || !ok || id <= 0 || a["awaiting"] != true {
		t.Fatalf("add answered %v", a)
	}
	// A browser hand-off awaits the user's confirmation (docs/DESKTOP-SPEC.md §1).
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	if _, err := h.dl.Confirm(ctx, connect.NewRequest(&downexv1.ConfirmRequest{Id: int64(id), Start: true})); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	dl := h.waitCompleted(int64(id))
	if dl.GetDir() != h.out || dl.GetFilename() != "from-browser.bin" {
		t.Fatalf("saved as %q in %q", dl.GetFilename(), dl.GetDir())
	}
	checkFile(t, filepath.Join(h.out, "from-browser.bin"), seed, size)
	noPartFiles(t, h.out)

	// Status, and an invalid request answered with its id.
	p.send(map[string]any{"id": "3", "type": "status"})
	if m := p.recv(); m["id"] != "3" || m["running"] != true {
		t.Fatalf("status answered %v", m)
	}
	p.send(map[string]any{"id": "4", "type": "add", "url": "file:///etc/passwd"})
	if m := p.recv(); m["id"] != "4" || m["ok"] != false {
		t.Fatalf("invalid add answered %v", m)
	}
	p.closeAndWait()

	// The relay's log records the add without the cookie or the query.
	logPath := h.userLog("nmhost.log")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("relay log: %v", err)
	}
	if log := string(b); !strings.Contains(log, `"type":"add"`) || strings.Contains(log, "nm-cookie-secret") ||
		strings.Contains(log, "token=secret") {
		t.Errorf("relay log:\n%s", log)
	}
}

// autostartEnv returns the environment of a relay whose autostarted downexd opens the
// default database below the user configuration directory: kept, with settings that
// save in h.out without asking for confirmation, in the test.
func (h *harness) autostartEnv() []string {
	h.t.Helper()
	cfg := filepath.Join(h.root, "config")
	settings := filepath.Join(cfg, "Downex", "settings.json")
	if runtime.GOOS == "darwin" { // ~/Library/Application Support, below the harness's HOME
		settings = filepath.Join(h.userDir, "Library", "Application Support", "Downex", "settings.json")
	}
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		h.t.Fatal(err)
	}
	js, _ := json.Marshal(map[string]any{"max_concurrent_downloads": 3, "global_speed_limit": 0,
		"default_dir": h.out, "default_max_connections": 4, "confirm_browser_downloads": false})
	if err := os.WriteFile(settings, js, 0o600); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.status(); err == nil {
		h.t.Fatal("a daemon answers before the test started one")
	}
	return withOverrides(h.env, map[string]string{"APPDATA": cfg, "XDG_CONFIG_HOME": cfg})
}

// An add starts downexd (found next to downex-nmhost) when it is not running.
func TestNativeMessagingAutostart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 1<<20 + 3, 302
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"n2"`})
	env := h.autostartEnv()

	p := h.startNMHost(env)
	p.send(map[string]any{"id": "s", "type": "status"})
	if m := p.recv(); m["running"] != false {
		t.Fatalf("status before the add: %v", m)
	}
	p.send(map[string]any{"id": "a", "type": "add", "url": srv.URL + "/auto.bin"})
	m := p.recv()
	id, ok := m["downloadId"].(float64)
	if m["type"] != "added" || !ok || m["awaiting"] != false {
		t.Fatalf("add answered %v", m)
	}
	h.waitCompleted(int64(id))
	checkFile(t, filepath.Join(h.out, "auto.bin"), seed, size)
	p.closeAndWait()

	// The daemon keeps running after the relay exits.
	if _, err := h.status(); err != nil {
		t.Fatalf("the autostarted daemon is gone: %v", err)
	}
}

// A stream probe (probeStream, a user's click on a stream row: docs/VIDEO-DETECT-SPEC.md
// §3.5) starts downexd when it is not running and answers the stream's MP4 qualities
// without any address; an addMedia with a track, not held for confirmation here, says
// so (awaiting false) and saves that quality.
func TestNativeMessagingStreamAutostart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	_, hi := tsMaster(t, s, "/auto/", 171)
	env := h.autostartEnv()

	// No hello, which would start downexd itself (the relay warms the daemon on it).
	p := h.startNMHost(env)
	p.send(map[string]any{"id": "s", "type": "status"})
	if m := p.recv(); m["running"] != false {
		t.Fatalf("status before the probe: %v", m)
	}
	p.send(map[string]any{"id": "p", "type": "probeStream", "manifestUrl": s.url("/auto/master.m3u8")})
	info := p.recv()
	if info["type"] != "videoInfo" || info["container"] != "mp4" || info["site"] != "" || info["selectedVideo"] != "v1" {
		t.Fatalf("probeStream answered %v\nrelay stderr: %s", info, p.stderr.String())
	}
	noAddresses(t, "probeStream's answer", info)
	if _, err := h.status(); err != nil {
		t.Fatalf("the probe did not start downexd: %v", err)
	}
	p.send(map[string]any{"id": "a", "type": "addMedia", "manifestUrl": s.url("/auto/master.m3u8"), "videoTrack": "v1",
		"title": "Auto"})
	m := p.recv()
	id, ok := m["downloadId"].(float64)
	if m["type"] != "added" || !ok || m["awaiting"] != false {
		t.Fatalf("addMedia answered %v", m)
	}
	p.closeAndWait()
	dl := h.waitCompleted(int64(id))
	if dl.GetFilename() != "Auto.mp4" || dl.GetMedia().GetSelectedVideo() != "v1" {
		t.Fatalf("completed download = %v", dl)
	}
	checkMP4(t, filepath.Join(dl.GetDir(), dl.GetFilename()), tsTracks(hi)...)
}

// withOverrides returns env with the given variables replaced (names compare as the
// system does: case-insensitively on Windows).
func withOverrides(env []string, overrides map[string]string) []string {
	same := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		replaced := false
		for o := range overrides {
			replaced = replaced || same(k, o)
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
