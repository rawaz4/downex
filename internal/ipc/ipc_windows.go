//go:build windows

package ipc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const (
	// pipePrefix is the only namespace accepted for endpoints: local named pipes.
	// Remote (\\server\pipe\...) and NT-native (\\?\pipe\...) forms are refused, and so
	// is any name that could leave the pipe namespace through path normalisation.
	pipePrefix = `\\.\pipe\`
	// maxPipeNameLen is the documented limit for the part after pipePrefix.
	maxPipeNameLen = 256
	// pipeBufferSize is the in/out buffer size requested for every pipe instance.
	pipeBufferSize = 64 << 10
	// lockPrefix places the instance mutex in the caller's session namespace.
	lockPrefix = `Local\downex-`

	// keyDirName and keyFileName locate the per-user endpoint key below the local
	// application data folder.
	keyDirName  = "Downex"
	keyFileName = "endpoint.key"
	// keyBytes is the size of the random endpoint key (hex-encoded in the file).
	keyBytes = 16
)

// Mandatory integrity levels (the RID of the S-1-16-<rid> label SID).
const (
	integrityLow    = 0x1000
	integrityMedium = 0x2000
	integrityHigh   = 0x3000
)

// systemMandatoryLabelACEType is SYSTEM_MANDATORY_LABEL_ACE_TYPE, the ACE type of an
// integrity label in a SACL.
const systemMandatoryLabelACEType = 0x11

// errMalformedKey reports an endpoint key file whose content is not a key.
var errMalformedKey = errors.New("malformed endpoint key")

// Endpoint returns the per-user named pipe \\.\pipe\downex-<key>, where <key> is a
// random 32-hex-digit value created once per user and kept in
// %LOCALAPPDATA%\Downex\endpoint.key. The name is therefore not predictable from
// public facts (such as the user's SID), so no other account can create the pipe
// before the daemon does. The key file is readable only by the current user and
// carries a Medium no-read-up label, so sandboxed (Low integrity) processes of the
// user cannot learn the name either. A non-empty EnvEndpoint overrides it; the
// override must be a local pipe path (\\.\pipe\<name>).
func Endpoint() (string, error) {
	if e := os.Getenv(EnvEndpoint); e != "" {
		if err := checkPipePath(e); err != nil {
			return "", fmt.Errorf("ipc: %s: %w", EnvEndpoint, err)
		}
		return e, nil
	}
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	dir, err := keyDir()
	if err != nil {
		return "", err
	}
	key, err := endpointKey(filepath.Join(dir, keyFileName), sid)
	if err != nil {
		return "", err
	}
	return pipePrefix + "downex-" + key, nil
}

// Listen creates the first and only owner of the named pipe endpoint and returns a
// listener for it.
//
// The pipe is created with the first-instance disposition, so Listen fails with an
// error wrapping ErrAlreadyRunning when any pipe of that name already exists, whoever
// created it (a second daemon or a squatter; Dial tells them apart). Its security
// descriptor is owned by the current user and carries a protected DACL with a single
// ACE granting generic-all to the current user's SID only (no inherited or other
// entries). A process running above Medium integrity also labels the pipe with its own
// level (no-write-up), so that processes of lower integrity cannot drive it. go-winio
// creates every instance with FILE_PIPE_REJECT_REMOTE_CLIENTS, so clients on other
// machines are refused by the pipe file system. Each instance uses 64 KiB input/output
// buffers.
func Listen(endpoint string) (net.Listener, error) {
	if err := checkPipePath(endpoint); err != nil {
		return nil, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	sddl, err := objectSDDL(sid)
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    pipeBufferSize,
		OutputBufferSize:   pipeBufferSize,
	})
	if err != nil {
		// The first-instance create of an existing pipe fails with ACCESS_DENIED (the
		// pipe file system's answer, also when its DACL excludes us) or ALREADY_EXISTS.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, fmt.Errorf("ipc: listen on %s: %w (%w)", endpoint, ErrAlreadyRunning, err)
		}
		return nil, fmt.Errorf("ipc: listen on %s: %w", endpoint, err)
	}
	return l, nil
}

// Dial connects to the daemon's named pipe, waiting while all pipe instances are busy
// until ctx is done. It returns an error wrapping ErrNotRunning when the pipe does not
// exist.
//
// Before returning, Dial verifies the server, so that no request (with its cookies or
// body) ever reaches an impostor that created the pipe first: the pipe must be owned by
// the current user, its mandatory integrity label must not be below the caller's level
// (capped at Medium, the level of the user's normal processes), and the process serving
// it must run as the current user at such a level too. Otherwise the error wraps
// ErrForeignEndpoint and fs.ErrPermission. The client opens the pipe at the anonymous
// impersonation level, so the server cannot impersonate the caller.
func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	if err := checkPipePath(endpoint); err != nil {
		return nil, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	minLevel, err := requiredServerLevel()
	if err != nil {
		return nil, err
	}
	conn, err := winio.DialPipeContext(ctx, endpoint)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, fmt.Errorf("ipc: dial %s: %w", endpoint, ErrNotRunning)
		}
		return nil, fmt.Errorf("ipc: dial %s: %w", endpoint, err)
	}
	if err := verifyConnServer(conn, sid, minLevel); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ipc: dial %s: %w", endpoint, err)
	}
	return conn, nil
}

// AcquireInstanceLock creates the named mutex that marks a running daemon for
// endpoint: Local\downex-<h>, where <h> is a hash of the (case-folded) endpoint. The
// default endpoint embeds the per-user random key, so its mutex name is as
// unpredictable as the pipe name. The mutex is owned by the current user with a
// protected owner-only DACL (and the process's label above Medium integrity).
//
// When the mutex already exists and was created by this user at no lower integrity
// than the caller's (capped at Medium), the error wraps ErrAlreadyRunning. A mutex
// owned by someone else, labelled lower, or whose DACL denies us was squatted: the
// error then wraps ErrForeignEndpoint (and fs.ErrPermission). release closes the mutex
// handle; it is idempotent. The kernel drops the mutex when the process exits, so a
// crash never leaves a stale lock.
func AcquireInstanceLock(endpoint string) (release func(), err error) {
	if err := checkPipePath(endpoint); err != nil {
		return nil, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	minLevel, err := requiredServerLevel()
	if err != nil {
		return nil, err
	}
	name := lockName(endpoint)
	sddl, err := objectSDDL(sid)
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("ipc: build instance lock security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
		InheritHandle:      0,
	}
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("ipc: instance lock name %q: %w", name, err)
	}
	h, err := windows.CreateMutex(sa, false, name16)
	switch {
	case err == nil:
	case errors.Is(err, windows.ERROR_ALREADY_EXISTS):
		if h == 0 {
			return nil, fmt.Errorf("ipc: instance lock %s: %w", name, ErrAlreadyRunning)
		}
		verr := verifyObject(h, windows.SE_KERNEL_OBJECT, sid, minLevel)
		_ = windows.CloseHandle(h)
		if verr != nil {
			return nil, fmt.Errorf("ipc: instance lock %s: %w", name, verr)
		}
		return nil, fmt.Errorf("ipc: instance lock %s: %w", name, ErrAlreadyRunning)
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		// The object exists but its DACL does not admit us: ours always does.
		return nil, fmt.Errorf("ipc: instance lock %s: %w (%w, %w)", name, ErrForeignEndpoint, fs.ErrPermission, err)
	default:
		return nil, fmt.Errorf("ipc: create instance lock %s: %w", name, err)
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = windows.CloseHandle(h) })
	}, nil
}

// checkPipePath accepts only local named pipe paths of the form \\.\pipe\<name>, where
// <name> has no path separators and is not "." or "..": Win32 path normalisation would
// otherwise resolve "\\.\pipe\..\x" outside the pipe namespace (to a file, or through
// \\.\UNC\ to a remote server).
func checkPipePath(p string) error {
	if len(p) <= len(pipePrefix) || !strings.EqualFold(p[:len(pipePrefix)], pipePrefix) {
		return fmt.Errorf("ipc: endpoint %q is not a local named pipe path (%s<name>)", p, pipePrefix)
	}
	name := p[len(pipePrefix):]
	if n := len(name); n > maxPipeNameLen {
		return fmt.Errorf("ipc: endpoint pipe name is %d characters; the limit is %d", n, maxPipeNameLen)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("ipc: endpoint %q contains a NUL character", p)
	}
	if strings.ContainsAny(name, `\/`) || name == "." || name == ".." {
		return fmt.Errorf("ipc: endpoint pipe name %q must not contain path separators or be . or ..", name)
	}
	return nil
}

// currentUserSID returns the user SID of the current process token.
func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("ipc: query current user SID: %w", err)
	}
	sid, err := tu.User.Sid.Copy()
	if err != nil {
		return nil, fmt.Errorf("ipc: copy current user SID: %w", err)
	}
	return sid, nil
}

// tokenIntegrity returns the mandatory integrity level (label RID) of token t.
func tokenIntegrity(t windows.Token) (uint32, error) {
	n := uint32(64)
	for {
		buf := make([]byte, n)
		err := windows.GetTokenInformation(t, windows.TokenIntegrityLevel, &buf[0], n, &n)
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("ipc: query integrity level: %w", err)
		}
		label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
		return labelLevel(label.Label.Sid)
	}
}

// labelLevel returns the RID of a mandatory label SID (S-1-16-<rid>). It reads the
// SID's binary layout (revision, sub-authority count, 6-byte identifier authority,
// 32-bit sub-authorities) itself: the x/sys accessors turn syscall results into
// pointers, which the race detector's pointer checks reject for SIDs in Go memory.
func labelLevel(sid *windows.SID) (uint32, error) {
	if sid == nil || !sid.IsValid() {
		return 0, fmt.Errorf("ipc: %v is not a mandatory label", sid)
	}
	p := unsafe.Pointer(sid)
	count := int(*(*uint8)(unsafe.Add(p, 1)))
	authority := *(*[6]byte)(unsafe.Add(p, 2))
	if count == 0 || authority != windows.SECURITY_MANDATORY_LABEL_AUTHORITY.Value {
		return 0, fmt.Errorf("ipc: %v is not a mandatory label", sid)
	}
	return *(*uint32)(unsafe.Add(p, 8+4*(count-1))), nil
}

// requiredServerLevel is the lowest integrity level a daemon's endpoint may have for
// this process to use it: the caller's own level, capped at Medium so that an elevated
// client still reaches the user's normal daemon.
func requiredServerLevel() (uint32, error) {
	level, err := tokenIntegrity(windows.GetCurrentProcessToken())
	if err != nil {
		return 0, err
	}
	return min(level, integrityMedium), nil
}

// keyDir returns, creating it if needed, the directory of the endpoint key:
// %LOCALAPPDATA%\Downex. When LOCALAPPDATA is unset or not absolute the known folder
// FOLDERID_LocalAppData is used.
func keyDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(base) {
		var err error
		if base, err = windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT); err != nil {
			return "", fmt.Errorf("ipc: locate the local application data folder: %w", err)
		}
	}
	dir := filepath.Join(base, keyDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("ipc: create %s: %w", dir, err)
	}
	return dir, nil
}

// endpointKey returns the key stored at path, creating it (or replacing a malformed
// one) when needed. Concurrent first calls agree on one key: a new key is moved into
// place only if no key exists yet.
func endpointKey(path string, sid *windows.SID) (string, error) {
	for range 3 {
		key, err := readKey(path)
		switch {
		case err == nil:
			return key, nil
		case errors.Is(err, fs.ErrNotExist):
			err = writeKey(path, sid, false)
		case errors.Is(err, errMalformedKey):
			err = writeKey(path, sid, true)
		default:
			return "", fmt.Errorf("ipc: read endpoint key: %w", err)
		}
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("ipc: create endpoint key: %w", err)
		}
	}
	return "", fmt.Errorf("ipc: endpoint key %s keeps changing", path)
}

// readKey reads and validates the key file at path.
func readKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if len(key) != 2*keyBytes || strings.Trim(key, "0123456789abcdef") != "" {
		return "", fmt.Errorf("%s: %w", path, errMalformedKey)
	}
	return key, nil
}

// writeKey writes a new random key to a temporary file (owner-only DACL, Medium
// no-read-up/no-write-up label) and moves it to path. Unless replace is set, an
// existing file at path wins and the error wraps fs.ErrExist.
func writeKey(path string, sid *windows.SID, replace bool) (err error) {
	b := make([]byte, keyBytes)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	key := hex.EncodeToString(b)
	s := sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + s + "D:P(A;;FA;;;" + s + ")S:(ML;;NWNR;;;ME)")
	if err != nil {
		return fmt.Errorf("build key file security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	tmp := path + ".tmp-" + hex.EncodeToString(b[:4])
	tmp16, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(tmp16, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	f := os.NewFile(uintptr(h), tmp)
	_, werr := f.WriteString(key + "\n")
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if werr != nil {
		return fmt.Errorf("write %s: %w", tmp, werr)
	}
	from16, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	if err := windows.MoveFileEx(from16, to16, flags); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return fmt.Errorf("%s: %w", path, fs.ErrExist)
		}
		return fmt.Errorf("move %s into place: %w", tmp, err)
	}
	return nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// lockName derives the instance mutex name from the (case-folded) endpoint.
func lockName(endpoint string) string {
	return lockPrefix + shortHash(strings.ToLower(endpoint))
}

// objectSDDL describes an object owned by sid whose protected DACL grants generic-all
// to sid and to nobody else. Above Medium integrity it also carries a no-write-up label
// of the process's level, so that the user's normal processes cannot drive an elevated
// instance.
func objectSDDL(sid *windows.SID) (string, error) {
	s := sid.String()
	sddl := "O:" + s + "D:P(A;;GA;;;" + s + ")"
	level, err := tokenIntegrity(windows.GetCurrentProcessToken())
	if err != nil {
		return "", err
	}
	if level > integrityMedium {
		sddl += fmt.Sprintf("S:(ML;;NW;;;S-1-16-%d)", level)
	}
	return sddl, nil
}

// handleOf returns the pipe handle of a connection from winio.
func handleOf(conn net.Conn) (windows.Handle, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("ipc: pipe connection %T exposes no handle", conn)
	}
	return windows.Handle(f.Fd()), nil
}

// verifyConnOwner checks that the pipe behind conn is owned by want.
func verifyConnOwner(conn net.Conn, want *windows.SID) error {
	h, err := handleOf(conn)
	if err != nil {
		return err
	}
	return verifyHandleOwner(h, windows.SE_FILE_OBJECT, want)
}

// verifyConnServer checks the pipe behind conn and the process serving it: owned by and
// running as user, at integrity level minLevel or above.
func verifyConnServer(conn net.Conn, user *windows.SID, minLevel uint32) error {
	h, err := handleOf(conn)
	if err != nil {
		return err
	}
	if err := verifyObject(h, windows.SE_FILE_OBJECT, user, minLevel); err != nil {
		return err
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("ipc: query the endpoint's server process: %w", err)
	}
	return verifyProcess(pid, user, minLevel)
}

// verifyObject checks that the object behind h (of type typ) is owned by user and that
// its mandatory label is at least minLevel (no label means Medium).
func verifyObject(h windows.Handle, typ windows.SE_OBJECT_TYPE, user *windows.SID, minLevel uint32) error {
	if err := verifyHandleOwner(h, typ, user); err != nil {
		return err
	}
	level, err := objectIntegrity(h, typ)
	if err != nil {
		return err
	}
	if level < minLevel {
		return foreign("endpoint has integrity level %#x, below the required %#x (created by a sandboxed process)", level, minLevel)
	}
	return nil
}

// objectIntegrity returns the level of the mandatory label of the object behind h, or
// Medium when it has none.
func objectIntegrity(h windows.Handle, typ windows.SE_OBJECT_TYPE) (uint32, error) {
	sd, err := windows.GetSecurityInfo(h, typ, windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return 0, fmt.Errorf("ipc: query endpoint integrity label: %w", err)
	}
	sacl, _, err := sd.SACL()
	if err != nil || sacl == nil {
		return integrityMedium, nil //nolint:nilerr // no SACL: no label, which means Medium
	}
	for i := range uint32(sacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(sacl, i, &ace); err != nil {
			return 0, fmt.Errorf("ipc: read endpoint integrity label: %w", err)
		}
		if ace.Header.AceType != systemMandatoryLabelACEType {
			continue
		}
		return labelLevel((*windows.SID)(unsafe.Pointer(&ace.SidStart)))
	}
	return integrityMedium, nil
}

// verifyProcess checks that process pid runs as user at integrity level minLevel or
// above.
func verifyProcess(pid uint32, user *windows.SID, minLevel uint32) error {
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return foreign("cannot inspect the endpoint's server process %d: %v", pid, err)
	}
	defer windows.CloseHandle(ph)
	var tok windows.Token
	if err := windows.OpenProcessToken(ph, windows.TOKEN_QUERY, &tok); err != nil {
		return foreign("cannot inspect the endpoint's server process %d: %v", pid, err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return fmt.Errorf("ipc: query the user of server process %d: %w", pid, err)
	}
	if !tu.User.Sid.Equals(user) {
		return foreign("endpoint is served by process %d of %s, not the current user %s", pid, tu.User.Sid, user)
	}
	level, err := tokenIntegrity(tok)
	if err != nil {
		return err
	}
	if level < minLevel {
		return foreign("endpoint is served by process %d at integrity level %#x, below the required %#x", pid, level, minLevel)
	}
	return nil
}

// verifyHandleOwner checks that the object behind h is owned by want. The error wraps
// ErrForeignEndpoint and fs.ErrPermission when the owner differs.
func verifyHandleOwner(h windows.Handle, typ windows.SE_OBJECT_TYPE, want *windows.SID) error {
	sd, err := windows.GetSecurityInfo(h, typ, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("ipc: query endpoint owner: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("ipc: read endpoint owner: %w", err)
	}
	if owner == nil {
		return foreign("endpoint has no owner")
	}
	if !owner.Equals(want) {
		return foreign("endpoint is owned by %s, not the current user %s", owner.String(), want.String())
	}
	return nil
}

// foreign formats an error wrapping ErrForeignEndpoint and fs.ErrPermission.
func foreign(format string, args ...any) error {
	return fmt.Errorf("ipc: %s: %w (%w)", fmt.Sprintf(format, args...), ErrForeignEndpoint, fs.ErrPermission)
}
