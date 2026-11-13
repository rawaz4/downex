//go:build windows

package ipc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// testEndpoint returns a fresh pipe path that no other test (or daemon) uses.
func testEndpoint(t *testing.T) string {
	t.Helper()
	return pipePrefix + "downex-test-" + strings.ToLower(rand.Text())
}

func mustUserSID(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// TestEndpointDefault: the default pipe name embeds a random per-user key (SEC-5), so
// it cannot be predicted from public facts such as the user's SID; it is stable for the
// profile that holds the key.
func TestEndpointDefault(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	ep, err := Endpoint()
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if !regexp.MustCompile(`^\\\\\.\\pipe\\downex-[0-9a-f]{32}$`).MatchString(ep) {
		t.Fatalf("Endpoint = %q, want \\\\.\\pipe\\downex-<32 hex>", ep)
	}
	sum := sha256.Sum256([]byte(mustUserSID(t).String()))
	if strings.Contains(ep, hex.EncodeToString(sum[:])[:16]) {
		t.Fatalf("Endpoint %q is derived from the user's SID", ep)
	}
	if again, err := Endpoint(); err != nil || again != ep {
		t.Fatalf("second Endpoint = %q, %v; want %q", again, err, ep)
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if other, err := Endpoint(); err != nil || other == ep {
		t.Fatalf("Endpoint of another profile = %q, %v; want a different key", other, err)
	}
}

func TestEndpointOverride(t *testing.T) {
	t.Setenv(EnvEndpoint, `\\.\pipe\downex-profile-a`)
	ep, err := Endpoint()
	if err != nil || ep != `\\.\pipe\downex-profile-a` {
		t.Fatalf("Endpoint = %q, %v; want the override", ep, err)
	}
	t.Setenv(EnvEndpoint, `C:\temp\rpc.sock`)
	if ep, err := Endpoint(); err == nil {
		t.Fatalf("Endpoint accepted a non-pipe override: %q", ep)
	}
}

func TestEndpointValidation(t *testing.T) {
	t.Parallel()
	for _, ep := range []string{
		``,
		`\\.\pipe\`,
		`C:\temp\rpc.sock`,
		`\\server\pipe\downex`,
		`\\?\pipe\downex`,
		`\\.\pipe\` + strings.Repeat("x", maxPipeNameLen+1),
		"\\\\.\\pipe\\a\x00b",
		// Names that Win32 normalisation would resolve outside the pipe namespace.
		`\\.\pipe\..\UNC\localhost\pipe\downex`,
		`\\.\pipe\..\C:\Windows\win.ini`,
		`\\.\pipe\a/../../C:\x`,
		`\\.\pipe\a\b`,
		`\\.\pipe\..`,
		`\\.\pipe\.`,
	} {
		if l, err := Listen(ep); err == nil {
			_ = l.Close()
			t.Errorf("Listen(%q) succeeded", ep)
		}
		if c, err := Dial(t.Context(), ep); err == nil {
			_ = c.Close()
			t.Errorf("Dial(%q) succeeded", ep)
		} else if errors.Is(err, ErrNotRunning) {
			t.Errorf("Dial(%q) = %v; an invalid path is not 'not running'", ep, err)
		}
		if r, err := AcquireInstanceLock(ep); err == nil {
			r()
			t.Errorf("AcquireInstanceLock(%q) succeeded", ep)
		}
	}
	// The prefix is case-insensitive like the pipe namespace itself.
	ep := `\\.\PIPE\` + strings.TrimPrefix(testEndpoint(t), pipePrefix)
	l := listenT(t, ep)
	go serveLineEcho(l)
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatalf("Dial(%q): %v", ep, err)
	}
	_ = c.Close()
}

// TestPipeSecurityDescriptor inspects the created pipe through a client handle: the
// owner must be the current user and the protected DACL must hold exactly one ACE,
// granting full access to the current user's SID.
func TestPipeSecurityDescriptor(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)

	c, err := dialT(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := windows.Handle(c.(interface{ Fd() uintptr }).Fd())
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	t.Logf("pipe security descriptor: %s", sd)
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff // FILE_ALL_ACCESS
	assertOwnerOnly(t, sd, mustUserSID(t), fileAllAccess)
}

// assertOwnerOnly checks owner == user, a protected DACL and a single allow ACE for user
// whose mask is wantMask (GENERIC_ALL mapped for the object type) or GENERIC_ALL itself.
func assertOwnerOnly(t *testing.T, sd *windows.SECURITY_DESCRIPTOR, user *windows.SID, wantMask windows.ACCESS_MASK) {
	t.Helper()
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("Owner: %v", err)
	}
	if owner == nil || !owner.Equals(user) {
		t.Errorf("owner = %v, want %v", owner, user)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if control&windows.SE_DACL_PRESENT == 0 {
		t.Fatal("no DACL present (a NULL DACL would grant everyone access)")
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("DACL is not protected against inheritance (control %#x)", control)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl == nil {
		t.Fatal("NULL DACL")
	}
	if dacl.AceCount != 1 {
		t.Fatalf("DACL has %d ACEs, want exactly 1", dacl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatalf("GetAce: %v", err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Errorf("ACE type = %d, want ACCESS_ALLOWED", ace.Header.AceType)
	}
	if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
		t.Errorf("ACE is inherited (flags %#x)", ace.Header.AceFlags)
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(user) {
		t.Errorf("ACE trustee = %v, want the current user %v", aceSID, user)
	}
	if ace.Mask != wantMask && ace.Mask != windows.GENERIC_ALL {
		t.Errorf("ACE mask = %#x, want %#x (or GENERIC_ALL)", ace.Mask, wantMask)
	}
}

// filePipeLocalInformation mirrors FILE_PIPE_LOCAL_INFORMATION (class 24).
type filePipeLocalInformation struct {
	NamedPipeType          uint32
	NamedPipeConfiguration uint32
	MaximumInstances       uint32
	CurrentInstances       uint32
	InboundQuota           uint32
	ReadDataAvailable      uint32
	OutboundQuota          uint32
	WriteQuotaAvailable    uint32
	NamedPipeState         uint32
	NamedPipeEnd           uint32
}

const filePipeLocalInformationClass = 24

// TestPipeConfiguration queries the server end of an accepted connection: 64 KiB
// quotas, byte mode, and the reject-remote-clients type flag go-winio sets.
func TestPipeConfiguration(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
		close(accepted)
	}()
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sc, ok := <-accepted
	if !ok {
		t.Fatal("Accept failed")
	}
	defer sc.Close()

	var info filePipeLocalInformation
	var iosb windows.IO_STATUS_BLOCK
	h := windows.Handle(sc.(interface{ Fd() uintptr }).Fd())
	if err := windows.NtQueryInformationFile(h, &iosb, (*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)), filePipeLocalInformationClass); err != nil {
		t.Fatalf("NtQueryInformationFile: %v", err)
	}
	t.Logf("pipe info: %+v", info)
	if info.InboundQuota != pipeBufferSize || info.OutboundQuota != pipeBufferSize {
		t.Errorf("quotas in=%d out=%d, want %d", info.InboundQuota, info.OutboundQuota, pipeBufferSize)
	}
	if info.NamedPipeType&windows.FILE_PIPE_REJECT_REMOTE_CLIENTS == 0 {
		t.Errorf("pipe type %#x lacks FILE_PIPE_REJECT_REMOTE_CLIENTS", info.NamedPipeType)
	}
}

// TestRemoteClientRejected dials the pipe through the loopback SMB redirector, which
// the pipe file system treats as a remote client.
func TestRemoteClientRejected(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)

	remote := `\\localhost\pipe\` + strings.TrimPrefix(ep, pipePrefix)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, remote)
	if err == nil {
		_ = c.Close()
		t.Fatalf("remote client %s was accepted", remote)
	}
	t.Logf("remote dial: %v", err)
}

// TestListenRefusesPreexistingPipe simulates a squatter: a pipe of the same name created
// by someone else beforehand (here with the default pipe ACL) must make Listen fail.
func TestListenRefusesPreexistingPipe(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	squatter, err := winio.ListenPipe(ep, nil)
	if err != nil {
		t.Fatalf("create squatter pipe: %v", err)
	}
	defer squatter.Close()

	l, err := Listen(ep)
	if err == nil {
		_ = l.Close()
		t.Fatal("Listen joined a pre-existing pipe; want first-instance failure")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Listen: %v, want ErrAlreadyRunning", err)
	}
	t.Logf("Listen over an existing pipe: %v", err)
}

// TestVerifyOwnerRejectsOtherUser checks the client-side owner verification Dial runs:
// a pipe owned by anyone but the expected SID is refused with fs.ErrPermission.
func TestVerifyOwnerRejectsOtherUser(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := verifyConnOwner(c, mustUserSID(t)); err != nil {
		t.Fatalf("verifyConnOwner(current user): %v", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyConnOwner(c, system)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("verifyConnOwner(SYSTEM) = %v, want fs.ErrPermission", err)
	}
}

// TestDialBusyHonoursDeadline: while the server accepts nothing, every instance is busy,
// so Dial waits and must give up when its context expires.
func TestDialBusyHonoursDeadline(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	_ = listenT(t, ep) // never Accept

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := Dial(ctx, ep)
	if err == nil {
		_ = c.Close()
		t.Fatal("Dial succeeded without a pending Accept")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial: %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial: %v; a busy pipe is running", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Dial took %v after a 200ms deadline", d)
	}
}

func TestInstanceLockName(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	def, err := Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lockName(def), `Local\downex-`+shortHash(strings.ToLower(def)); got != want {
		t.Errorf("lockName(default) = %q, want %q", got, want)
	}
	if lockName(strings.ToUpper(def)) != lockName(def) {
		t.Error("default lock name depends on case")
	}
	a, b := lockName(`\\.\pipe\x`), lockName(`\\.\pipe\y`)
	if a == b || a == lockName(def) {
		t.Errorf("override lock names not distinct: %q %q", a, b)
	}
	if lockName(`\\.\pipe\X`) != a {
		t.Error("override lock name depends on case")
	}
}

// TestInstanceLockSecurityDescriptor opens the mutex by name and checks its owner and
// owner-only protected DACL.
func TestInstanceLockSecurityDescriptor(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	release, err := AcquireInstanceLock(ep)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	sid := mustUserSID(t)
	name16, err := windows.UTF16PtrFromString(lockName(ep))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenMutex(windows.READ_CONTROL, false, name16)
	if err != nil {
		t.Fatalf("OpenMutex: %v", err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	_ = windows.CloseHandle(h) // our extra handle would keep the object alive
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	t.Logf("mutex security descriptor: %s", sd)
	const mutexAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1 // MUTANT_ALL_ACCESS
	assertOwnerOnly(t, sd, sid, mutexAllAccess)

	// After release (the last handle closes) the mutex is gone.
	release()
	h2, err := windows.OpenMutex(windows.READ_CONTROL, false, name16)
	if err == nil {
		_ = windows.CloseHandle(h2)
		t.Fatal("mutex still exists after release")
	}
}
