//go:build windows

package ipc

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// envLowHelper makes this test binary, started by startLowHelper, block until its
// standard input closes (see TestLowIntegrityHelper).
const envLowHelper = "DOWNEX_IPC_LOW_HELPER"

// lowToken returns a copy of this process's token lowered to Low integrity: a primary
// token (to start a process with) or an impersonation token (for a thread).
func lowToken(t *testing.T, tokenType uint32) windows.Token {
	t.Helper()
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &self); err != nil {
		t.Fatalf("OpenProcessToken: %v", err)
	}
	defer self.Close()
	var dup windows.Token
	access := uint32(windows.TOKEN_QUERY | windows.TOKEN_DUPLICATE | windows.TOKEN_ADJUST_DEFAULT |
		windows.TOKEN_ASSIGN_PRIMARY | windows.TOKEN_IMPERSONATE)
	if err := windows.DuplicateTokenEx(self, access, nil, windows.SecurityImpersonation, tokenType, &dup); err != nil {
		t.Fatalf("DuplicateTokenEx: %v", err)
	}
	t.Cleanup(func() { _ = dup.Close() })
	low, err := windows.CreateWellKnownSid(windows.WinLowLabelSid)
	if err != nil {
		t.Fatal(err)
	}
	tml := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: low, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(dup, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&tml)), tml.Size()); err != nil {
		t.Fatalf("lower the token to Low integrity: %v", err)
	}
	return dup
}

// asLowIntegrity runs fn on a thread that impersonates a Low-integrity copy of this
// process's token, as a sandboxed process of the same user would run. Objects fn
// creates get that token's owner, default DACL and integrity label.
func asLowIntegrity(t *testing.T, fn func()) {
	t.Helper()
	tok := lowToken(t, windows.TokenImpersonation)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		if err := windows.SetThreadToken(nil, tok); err != nil {
			t.Errorf("SetThreadToken: %v", err)
			return
		}
		defer func() { _ = windows.RevertToSelf() }()
		fn()
	}()
	<-done
}

// TestDialRefusesLowIntegritySquatter (SEC-1): a sandboxed (Low integrity) process of
// the same user that creates the endpoint first owns it as that user, but Dial must
// still refuse it, so that no request reaches it.
func TestDialRefusesLowIntegritySquatter(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	var (
		squatter net.Listener
		err      error
	)
	asLowIntegrity(t, func() { squatter, err = winio.ListenPipe(ep, nil) })
	if err != nil {
		t.Fatalf("create the squatter pipe at Low integrity: %v", err)
	}
	defer squatter.Close()
	received := make(chan []byte, 1)
	go func() {
		c, err := squatter.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		received <- b
	}()

	c, err := dialT(t, ep)
	if err == nil {
		_, _ = io.WriteString(c, "POST /downex.v1.DownloadService/Add cookie: session=SECRET\n")
		_ = c.Close()
		t.Fatalf("Dial accepted a pipe created at Low integrity; it received %q", <-received)
	}
	if !errors.Is(err, ErrForeignEndpoint) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Dial = %v, want ErrForeignEndpoint and fs.ErrPermission", err)
	}
	t.Logf("Dial of the squatted pipe: %v", err)
}

// TestInstanceLockRefusesLowIntegritySquatter (SEC-1): a mutex of the lock's name made
// by a sandboxed process is not "already running".
func TestInstanceLockRefusesLowIntegritySquatter(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	name16, err := windows.UTF16PtrFromString(lockName(ep))
	if err != nil {
		t.Fatal(err)
	}
	var h windows.Handle
	asLowIntegrity(t, func() { h, err = windows.CreateMutex(nil, false, name16) })
	if err != nil {
		t.Fatalf("create the squatter mutex at Low integrity: %v", err)
	}
	defer windows.CloseHandle(h)

	release, err := AcquireInstanceLock(ep)
	if err == nil {
		release()
		t.Fatal("AcquireInstanceLock succeeded over an existing mutex")
	}
	if errors.Is(err, ErrAlreadyRunning) || !errors.Is(err, ErrForeignEndpoint) {
		t.Fatalf("AcquireInstanceLock = %v, want ErrForeignEndpoint (squatted), not ErrAlreadyRunning", err)
	}
	t.Logf("AcquireInstanceLock over the squatted mutex: %v", err)
}

// TestInstanceLockOfRunningDaemon: a mutex this user created at its own level is a
// running daemon.
func TestInstanceLockOfRunningDaemon(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	release, err := AcquireInstanceLock(ep)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if r2, err := AcquireInstanceLock(ep); err == nil {
		r2()
		t.Fatal("second AcquireInstanceLock succeeded")
	} else if !errors.Is(err, ErrAlreadyRunning) || errors.Is(err, ErrForeignEndpoint) {
		t.Fatalf("second AcquireInstanceLock = %v, want ErrAlreadyRunning", err)
	}
}

// TestLowIntegrityHelper is not a test: started by startLowHelper with envLowHelper
// set, it blocks until its standard input closes.
func TestLowIntegrityHelper(t *testing.T) {
	if os.Getenv(envLowHelper) == "" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// startLowHelper starts this test binary at Low integrity and returns its pid; it exits
// when the test ends.
func startLowHelper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLowIntegrityHelper$")
	cmd.Env = append(os.Environ(), envLowHelper+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(lowToken(t, windows.TokenPrimary))}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a Low integrity process: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// TestVerifyProcessIntegrity (SEC-1): the server-process check accepts this process
// and refuses a process of the same user running at Low integrity.
func TestVerifyProcessIntegrity(t *testing.T) {
	t.Parallel()
	user := mustUserSID(t)
	if err := verifyProcess(uint32(os.Getpid()), user, integrityMedium); err != nil {
		t.Fatalf("verifyProcess(self): %v", err)
	}
	pid := startLowHelper(t)
	err := verifyProcess(uint32(pid), user, integrityMedium)
	if !errors.Is(err, ErrForeignEndpoint) {
		t.Fatalf("verifyProcess(Low integrity process) = %v, want ErrForeignEndpoint", err)
	}
	if err := verifyProcess(uint32(pid), user, integrityLow); err != nil {
		t.Fatalf("verifyProcess(Low integrity process, Low required) = %v", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyProcess(uint32(os.Getpid()), system, integrityMedium); !errors.Is(err, ErrForeignEndpoint) {
		t.Fatalf("verifyProcess(self as SYSTEM) = %v, want ErrForeignEndpoint", err)
	}
}

// TestEndpointKeyFile (SEC-5): the key file is private to the user (protected
// owner-only DACL) and labelled Medium with no-read-up, so that sandboxed processes of
// the user cannot read the pipe name; a malformed key is replaced.
func TestEndpointKeyFile(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	ep, err := Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("LOCALAPPDATA"), keyDirName, keyFileName)
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	t.Logf("key file security descriptor: %s", sd)
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	assertOwnerOnly(t, sd, mustUserSID(t), fileAllAccess)
	sacl, _, err := sd.SACL()
	if err != nil || sacl == nil || sacl.AceCount != 1 {
		t.Fatalf("key file SACL = %v, %v; want one mandatory label", sacl, err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(sacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	level, err := labelLevel((*windows.SID)(unsafe.Pointer(&ace.SidStart)))
	const noWriteUp, noReadUp = 0x1, 0x2
	if ace.Header.AceType != systemMandatoryLabelACEType || err != nil || level != integrityMedium ||
		ace.Mask&(noWriteUp|noReadUp) != noWriteUp|noReadUp {
		t.Fatalf("key file label: type %#x level %#x (%v) policy %#x; want Medium no-write-up no-read-up",
			ace.Header.AceType, level, err, ace.Mask)
	}

	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := Endpoint()
	if err != nil || fresh == ep || !regexp.MustCompile(`^\\\\\.\\pipe\\downex-[0-9a-f]{32}$`).MatchString(fresh) {
		t.Fatalf("Endpoint after the key was damaged = %q, %v; want a new valid key", fresh, err)
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
		t.Fatalf("key directory holds %v (%v); want only the key file", entries, err)
	}
}
