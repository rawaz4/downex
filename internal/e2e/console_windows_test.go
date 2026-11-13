//go:build windows

package e2e

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	downexv1 "downex/internal/rpc/downex/v1"
)

// envConsoleOf makes this test executable, started as a helper, report whether the
// process with that id is attached to a console (consoleProbe); TestMain checks it
// before anything else.
const envConsoleOf = "DOWNEX_E2E_CONSOLE_OF"

// Exit codes of the console probe, distinct from those of a test run.
const (
	probeHasConsole = 10
	probeNoConsole  = 11
	probeFailed     = 12
)

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole   = kernel32.NewProc("FreeConsole")
	procAttachConsole = kernel32.NewProc("AttachConsole")
)

// consoleProbe runs the helper when envConsoleOf is set: it leaves its own console and
// attaches to the console of the process named there, which fails with
// ERROR_INVALID_HANDLE exactly when that process has no console.
func consoleProbe() (code int, ok bool) {
	v := os.Getenv(envConsoleOf)
	if v == "" {
		return 0, false
	}
	pid, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		fmt.Fprintln(os.Stderr, "console probe:", err)
		return probeFailed, true
	}
	procFreeConsole.Call() //nolint:errcheck // it may have none
	r, _, err := procAttachConsole.Call(uintptr(pid))
	switch {
	case r != 0:
		procFreeConsole.Call() //nolint:errcheck // leaving it again
		return probeHasConsole, true
	case errors.Is(err, windows.ERROR_INVALID_HANDLE):
		return probeNoConsole, true
	}
	fmt.Fprintln(os.Stderr, "console probe: AttachConsole:", err)
	return probeFailed, true
}

// hasConsole reports whether process pid is attached to a console: a console window
// (perhaps hidden) that stays open while it runs, and that stops it when closed.
func hasConsole(t *testing.T, pid int) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envConsoleOf+"="+strconv.Itoa(pid))
	// The helper is a console program too: its own console gets no window.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case probeHasConsole:
			return true
		case probeNoConsole:
			return false
		}
	}
	t.Fatalf("console probe of pid %d: %v\n%s", pid, err, out)
	return false
}

// buildRelease builds downexd, downex and downex-nmhost as releases are built
// (scripts/build-windows.ps1, without the window and the extension) into a new
// directory and returns it.
func buildRelease(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "build-windows.ps1")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the release build script: %v", err)
	}
	out := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script, "-SkipExtension", "-SkipUI", "-Out", out)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, b)
	}
	return out
}

// subsystem returns the PE subsystem of the executable exe.
func subsystem(t *testing.T, exe string) uint16 {
	t.Helper()
	f, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		return oh.Subsystem
	case *pe.OptionalHeader32:
		return oh.Subsystem
	}
	t.Fatalf("%s has no optional header", exe)
	return 0
}

// The sign-in entry ("<dir>\downexd.exe" --background in HKCU\...\Run, docs/DESKTOP-SPEC.md
// §5) is started like any program the user opens: Windows gives a console program a
// console window of its own, which would stay open as long as the daemon runs and stop
// it when closed (GO-1). The released downexd is a GUI-subsystem program and gets
// none; a console build (go build, as the other tests use) hands over to a detached
// copy of itself at once. Either way the daemon that serves has no console.
func TestSignInStartHasNoConsole(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("end-to-end test: builds and runs downexd")
	}
	if bin.err != nil {
		t.Fatalf("cannot build the executables: %v", bin.err)
	}
	release := filepath.Join(buildRelease(t), "downexd.exe")
	if got := subsystem(t, release); got != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		t.Errorf("the released downexd is built for PE subsystem %d, want %d (Windows GUI): Windows gives it a "+
			"console window at sign-in", got, pe.IMAGE_SUBSYSTEM_WINDOWS_GUI)
	}
	// The installer reads --help through pipes to learn whether --background is known.
	if out, err := exec.Command(release, "--help").CombinedOutput(); err != nil || !strings.Contains(string(out), "-background") {
		t.Errorf("released downexd --help: %v\n%s", err, out)
	}

	for _, c := range []struct {
		name      string
		exe       string
		handsOver bool
	}{
		{"release build", release, false},
		{"console build", bin.downexd, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			cmd := exec.Command(c.exe, "--background", "--db", h.db, "--log-dir", h.logDir, "--log-level", "debug")
			cmd.Env, cmd.Dir = h.env, h.root
			// As Explorer starts a Run entry: a console program gets a new console. It is
			// hidden, so that a regression does not show it on the desktop.
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_CONSOLE}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var waitErr error
			go func() { waitErr = cmd.Wait(); close(done) }()
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					_ = cmd.Process.Kill()
					<-done
				}
			})

			var st *downexv1.StatusResponse
			deadline := time.Now().Add(readyTimeout)
			for {
				var err error
				if st, err = h.status(); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("downexd did not answer within %v: %v", readyTimeout, err)
				}
				time.Sleep(pollInterval)
			}
			pid := int(st.GetPid())
			switch {
			case pid == cmd.Process.Pid && c.handsOver:
				t.Errorf("the console build kept running in its console (pid %d)", pid)
			case pid != cmd.Process.Pid && !c.handsOver:
				t.Errorf("the release build handed over to pid %d although it needs no console", pid)
			case pid != cmd.Process.Pid:
				select {
				case <-done:
					if waitErr != nil {
						t.Errorf("the started downexd exited with %v after handing over to pid %d", waitErr, pid)
					}
				case <-time.After(exitTimeout):
					t.Fatalf("the started downexd (pid %d) still runs after handing over to pid %d", cmd.Process.Pid, pid)
				}
			}
			if hasConsole(t, pid) {
				t.Errorf("the serving downexd (pid %d) has a console: its window stays open while it runs, and "+
					"closing it stops the daemon", pid)
			}
			h.stopLeftoverDaemon()
			for deadline := time.Now().Add(exitTimeout); processAlive(pid); time.Sleep(pollInterval) {
				if time.Now().After(deadline) {
					t.Fatalf("downexd pid %d did not exit after Shutdown", pid)
				}
			}
		})
	}
}
