//go:build unix

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// processAlive reports whether the process pid still exists (zombies are reaped by init
// quickly, as the stand-in's parent is gone).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TestExecTextBusy: a program that is still open for writing when it is started (Linux
// refuses it with ETXTBSY, as when a child forked meanwhile holds a copy of the writer's
// descriptor) starts once the writer has closed it.
func TestExecTextBusy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "standin")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(testExeBytes(t)); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, func() { _ = f.Close() })
	res, err := Exec(context.Background(), ExecSpec{Path: p, Args: []string{"exit", "0"}, Timeout: 30 * time.Second})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("Exec of a program closed after 100 ms: %+v, %v", res, err)
	}
}
