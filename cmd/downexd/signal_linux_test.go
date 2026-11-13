//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGTERM pauses everything and exits 0; the log and settings files are private.
func TestSIGTERMStopsGracefully(t *testing.T) { testSignalStopsGracefully(t, syscall.SIGTERM) }

// SIGHUP (the terminal of a --foreground daemon was closed) stops it gracefully too,
// instead of Go's default of exiting at once without pausing (L7).
func TestSIGHUPStopsGracefully(t *testing.T) { testSignalStopsGracefully(t, syscall.SIGHUP) }

func testSignalStopsGracefully(t *testing.T, sig syscall.Signal) {
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
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	c := dial(t, endpoint)
	waitReady(t, c, &daemonRun{done: make(chan int), stderr: &syncBuffer{}})
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon exited with %v: %s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon ignored %v", sig)
	}
	logs, err := os.ReadFile(filepath.Join(logDir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logs, []byte(`"msg":"stopping on signal"`)) {
		t.Fatalf("log lacks the signal stop:\n%s", logs)
	}
	for _, p := range []string{filepath.Join(logDir, logFileName)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s has mode %v, want private", p, perm)
		}
	}
	if fi, err := os.Stat(logDir); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("log directory mode: %v %v", fi.Mode(), err)
	}
}
