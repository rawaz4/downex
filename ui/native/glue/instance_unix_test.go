//go:build darwin || linux

package glue

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/host"
)

// shortTemp is a temporary folder whose socket paths fit sun_path (macOS's $TMPDIR is long).
func shortTemp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "dx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestTheLockExcludesASecondHolder(t *testing.T) {
	dir := shortTemp(t)
	p := lockPath(dir, host.MainInstanceID)
	if filepath.Base(p) != host.MainInstanceID+".lock" {
		t.Fatalf("lock file %q is not Wails' name", p)
	}
	l, held, err := claimLock(p)
	if err != nil || held || l == nil {
		t.Fatalf("first claim: %v %v %v", l, held, err)
	}
	if _, held, err := claimLock(p); err != nil || !held {
		t.Fatalf("second claim in the same process: held %v, %v", held, err)
	}
	l.Close()
	l2, held, err := claimLock(p)
	if err != nil || held {
		t.Fatalf("claim after Close: held %v, %v", held, err)
	}
	l2.Close()
}

// TestTheLockExcludesAnotherProcess: flock(1)-style holder in another process (what Wails'
// window is to the native one) keeps the lock until it exits.
func TestTheLockExcludesAnotherProcess(t *testing.T) {
	if os.Getenv("GLUE_HOLD_LOCK") != "" {
		l, _, err := claimLock(os.Getenv("GLUE_HOLD_LOCK"))
		if err != nil || l == nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("held\n")
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	dir := shortTemp(t)
	p := lockPath(dir, "x")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheLockExcludesAnotherProcess$")
	cmd.Env = append(os.Environ(), "GLUE_HOLD_LOCK="+p)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || !strings.HasPrefix(string(buf), "held") {
		t.Fatalf("holder: %q %v", buf, err)
	}
	if _, held, err := claimLock(p); err != nil || !held {
		t.Fatalf("claim while another process holds it: held %v, %v", held, err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	l, held, err := claimLock(p)
	if err != nil || held {
		t.Fatalf("claim after the holder died: held %v, %v", held, err)
	}
	l.Close()
}

func TestTheSocketFolderIsTheUsersOwn(t *testing.T) {
	dir := shortTemp(t)
	d, err := socketDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(d)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := socketDir(dir); err == nil {
		t.Error("a folder others may enter is used")
	}
	if p := socketPath(d, host.MainInstanceID+"-progress-host"); len(p) > 103 {
		t.Errorf("socket path %d bytes", len(p))
	}
	// The product's real folder (/var/folders/…/T/) leaves room too.
	if p := socketPath(filepath.Join("/var/folders/1d/f_2fvcm96dn2f7xh1cnns0t40000gn/T", "dxui-501"), "x"); len(p) > 103 {
		t.Errorf("socket path in the user's temporary folder: %d bytes", len(p))
	}
}

func TestTheHostAnswersAHandoff(t *testing.T) {
	dir := shortTemp(t)
	path := socketPath(dir, "id")
	var got atomic.Value
	answer := uint32(host.HostAnswer)
	l, err := listenHandoffs(path, func(kind uint32, payload []byte) uint32 {
		if kind != host.HostHandoffKind {
			return 1
		}
		h, err := parseHandoffJSON(payload)
		if err != nil {
			return 1
		}
		got.Store(h)
		return answer
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := handoffJSON([]string{"--progress", "7"}, "/Applications")
	if err != nil {
		t.Fatal(err)
	}
	a, err := sendHandoff(path, host.HostHandoffKind, b, time.Second)
	if err != nil || hostReply(a, err) != replyTaken {
		t.Fatalf("answer %x %v", a, err)
	}
	h := got.Load().(host.Handoff)
	if strings.Join(h.Args, " ") != "--progress 7" || h.WorkingDirectory != "/Applications" {
		t.Errorf("handed over %+v", h)
	}
	answer = host.HostClosing
	if a, err := sendHandoff(path, host.HostHandoffKind, b, time.Second); hostReply(a, err) != replyRetry {
		t.Errorf("a closing host: %x %v", a, err)
	}
	l.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket stays: %v", err)
	}
	if a, err := sendHandoff(path, host.HostHandoffKind, b, time.Second); !errors.Is(err, errNoListener) || hostReply(a, err) != replyRetry {
		t.Errorf("no host: %x %v", a, err)
	}
}

func TestHandoffJSONIsWailsMacFormat(t *testing.T) {
	b, err := handoffJSON(nil, "/x")
	if err != nil || string(b) != `{"Args":[],"WorkingDirectory":"/x"}` {
		t.Errorf("%s %v", b, err)
	}
	h, err := parseHandoffJSON([]byte(`{"Args":["--confirm","3"],"WorkingDirectory":"/","Extra":1}`))
	if err != nil || len(h.Args) != 2 {
		t.Errorf("%+v %v", h, err)
	}
	if hostReply(0, nil) != replyOlder || hostReply(1, nil) != replyOlder {
		t.Error("an unknown answer is not an older build's")
	}
}
