//go:build darwin

package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// wantDetachFacts are the facts a helper started by StartDetached must report: its own
// session, and "/" (not the starter's directory) as working directory.
func wantDetachFacts(*testing.T) []string { return []string{"session=leader", "cwd=/"} }

// detachFacts describes the calling process: whether it leads its own session, its
// working directory, and the file behind each of its descriptors above standard error
// ("fd=<device>:<inode>"; /dev/fd entries are not symbolic links on macOS).
func detachFacts() []string {
	facts := []string{"session=inherited"}
	if sid, err := unix.Getsid(0); err == nil && sid == os.Getpid() {
		facts = []string{"session=leader"}
	}
	if wd, err := os.Getwd(); err == nil {
		facts = append(facts, "cwd="+wd)
	}
	entries, _ := os.ReadDir("/dev/fd")
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > 2 {
			var st unix.Stat_t
			if unix.Fstat(n, &st) == nil {
				facts = append(facts, fmt.Sprintf("fd=%d:%d", st.Dev, st.Ino))
			}
		}
	}
	return facts
}

// TestStartDetachedInheritsNoDescriptors: a descriptor the caller inherited without
// close-on-exec does not reach the detached process.
func TestStartDetachedInheritsNoDescriptors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inherited")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY, 0) // no O_CLOEXEC, like an inherited descriptor
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	inherited := fmt.Sprintf("fd=%d:%d", st.Dev, st.Ino)
	goFile, marker := helperFiles(t)
	if err := StartDetached(os.Args[0], helperCommand("detached", goFile, marker)...); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	facts := awaitMarker(t, marker)
	checkFacts(t, facts)
	if slices.Contains(facts, inherited) {
		t.Fatalf("the detached process inherited the caller's descriptor %d: %q", fd, facts)
	}
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor flags = %#x, %v; want close-on-exec set in the caller", flags, err)
	}
}

// TestStartAppOpensBundle: an application bundle's executable is started through
// open(1) with -n (and -g in the background), the bundle, the caller's DOWNEX_*
// variables and --args.
func TestStartAppOpensBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Downex Progress.app")
	exe := filepath.Join(bundle, "Contents", "MacOS", "Downex")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "open-args")
	prev := openTool
	t.Cleanup(func() { openTool = prev })
	openTool = append([]string{os.Args[0]}, helperCommand("record", marker)...)
	t.Setenv("DOWNEX_ENDPOINT", "/tmp/dx/rpc.sock")

	for _, tc := range []struct {
		start func(string, ...string) error
		flags []string
	}{
		{StartApp, []string{"-n"}},
		{StartAppInBackground, []string{"-n", "-g"}},
	} {
		_ = os.Remove(marker)
		if err := tc.start(exe, "--progress", "7"); err != nil {
			t.Fatalf("start %q: %v", tc.flags, err)
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("open(1) was not run: %v", err)
		}
		got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		want := slices.Clone(tc.flags)
		for _, e := range forwardedEnv(os.Environ()) {
			want = append(want, "--env", e)
		}
		want = append(want, bundle, "--args", "--progress", "7")
		if !slices.Equal(got, want) || !slices.Contains(got, "DOWNEX_ENDPOINT=/tmp/dx/rpc.sock") {
			t.Fatalf("open arguments = %q, want %q", got, want)
		}
	}

	// A missing executable is reported before open(1) runs.
	if err := StartApp(filepath.Join(dir, "Missing.app", "Contents", "MacOS", "Missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("StartApp(missing bundle) = %v, want fs.ErrNotExist", err)
	}
	// open(1) failing is an error.
	openTool = []string{"/usr/bin/false"}
	if err := StartApp(exe); err == nil {
		t.Fatal("StartApp succeeded although open(1) failed")
	}
}

// TestStartAppPlainExecutable: an executable outside a bundle is started detached.
func TestStartAppPlainExecutable(t *testing.T) {
	prev := openTool
	t.Cleanup(func() { openTool = prev })
	openTool = []string{"/usr/bin/false"} // must not be used
	goFile, marker := helperFiles(t)
	if err := StartApp(os.Args[0], helperCommand("detached", goFile, marker)...); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	checkFacts(t, awaitMarker(t, marker))
}

func allocated(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

// TestPreallocateReservesBlocks: F_PREALLOCATE reserves the whole range up front where
// the temporary directory's file system supports it (APFS, HFS+).
func TestPreallocateReservesBlocks(t *testing.T) {
	dir := t.TempDir()
	probe := createFile(t, filepath.Join(dir, "probe"))
	if err := fstore(int(probe.Fd()), 1<<20, false); preallocateUnsupported(err) {
		t.Skipf("the temporary directory's file system has no F_PREALLOCATE: %v", err)
	} else if err != nil {
		t.Fatalf("F_PREALLOCATE probe: %v", err)
	}
	if err := probe.Truncate(1 << 20); err != nil {
		t.Fatal(err)
	}
	if allocated(t, probe.Name()) < 1<<20 {
		t.Skip("F_PREALLOCATE does not show as allocated blocks on the temporary directory's file system")
	}
	path := filepath.Join(dir, "alloc.dxpart")
	f := createFile(t, path)
	data := pattern(1 << 20)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	const size = 64 << 20
	if err := Preallocate(f, size); err != nil {
		t.Fatalf("Preallocate: %v", err)
	}
	if got := fileSize(t, f); got != size {
		t.Fatalf("size = %d, want %d", got, size)
	}
	if got := allocated(t, path); got < size {
		t.Fatalf("allocated %d bytes, want at least %d", got, size)
	}
	if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
		t.Fatal("existing data changed")
	}
	if got := readAt(t, f, size-4096, 4096); !bytes.Equal(got, make([]byte, 4096)) {
		t.Fatal("reserved range does not read as zeros")
	}
}

// allocCall is one call of a preallocate test double.
type allocCall struct {
	length     int64
	contiguous bool
}

// TestPreallocateContiguousFirst: a contiguous run is asked for first, then any space;
// only the bytes not yet allocated are requested.
func TestPreallocateContiguousFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contig.dxpart")
	f := createFile(t, path)
	var calls []allocCall
	err := preallocate(f, 8<<20, func(fd int, length int64, contiguous bool) error {
		calls = append(calls, allocCall{length, contiguous})
		if contiguous {
			return unix.ENOSPC // fragmented free space
		}
		return nil
	})
	if err != nil {
		t.Fatalf("preallocate: %v", err)
	}
	if want := []allocCall{{8 << 20, true}, {8 << 20, false}}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if got := fileSize(t, f); got != 8<<20 {
		t.Fatalf("size = %d, want %d", got, 8<<20)
	}

	// Growing a file that has data asks for the rest only.
	g := createFile(t, filepath.Join(t.TempDir(), "grow.dxpart"))
	if _, err := g.WriteAt(pattern(1<<20), 0); err != nil {
		t.Fatal(err)
	}
	if err := g.Sync(); err != nil {
		t.Fatal(err)
	}
	have := allocated(t, g.Name())
	calls = nil
	if err := preallocate(g, 8<<20, func(fd int, length int64, contiguous bool) error {
		calls = append(calls, allocCall{length, contiguous})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].length != 8<<20-have {
		t.Fatalf("calls = %v, want one for %d bytes", calls, 8<<20-have)
	}
}

// TestPreallocateFallsBackToTruncate: without F_PREALLOCATE the file is extended by
// ftruncate and keeps its data.
func TestPreallocateFallsBackToTruncate(t *testing.T) {
	for _, errno := range []error{unix.ENOTSUP, unix.EOPNOTSUPP, unix.ENOSYS, unix.EINVAL} {
		t.Run(errno.Error(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "noalloc.dxpart")
			f := createFile(t, path)
			data := pattern(64 << 10)
			if _, err := f.WriteAt(data, 0); err != nil {
				t.Fatal(err)
			}
			const size = 64 << 20
			calls := 0
			if err := preallocate(f, size, func(int, int64, bool) error { calls++; return errno }); err != nil {
				t.Fatalf("preallocate: %v", err)
			}
			if calls != 2 {
				t.Fatalf("alloc called %d times, want 2 (contiguous, then any)", calls)
			}
			if got := fileSize(t, f); got != size {
				t.Fatalf("size = %d, want %d", got, size)
			}
			if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
				t.Fatal("existing data changed")
			}
		})
	}
}

// TestPreallocateRetriesEINTR: an allocation interrupted by a signal is restarted.
func TestPreallocateRetriesEINTR(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "eintr.dxpart"))
	calls := 0
	err := preallocate(f, 1<<20, func(int, int64, bool) error {
		if calls++; calls < 3 {
			return unix.EINTR
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("preallocate = %v after %d calls, want success after 3", err, calls)
	}
	if got := fileSize(t, f); got != 1<<20 {
		t.Fatalf("size = %d, want %d", got, 1<<20)
	}
}

// TestPreallocateReportsDiskFull: ENOSPC is returned (matching syscall.ENOSPC), not
// papered over with a sparse extension, and the file keeps its length.
func TestPreallocateReportsDiskFull(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "full.dxpart"))
	err := preallocate(f, 1<<20, func(int, int64, bool) error { return unix.ENOSPC })
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("preallocate error = %v, want ENOSPC", err)
	}
	if got := fileSize(t, f); got != 0 {
		t.Fatalf("size = %d after a full disk, want 0", got)
	}
}

func TestIsSparseDarwin(t *testing.T) {
	dir := t.TempDir()
	holey := filepath.Join(dir, "holey")
	if err := os.WriteFile(holey, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(holey, 64<<20); err != nil {
		t.Fatal(err)
	}
	if allocated(t, holey) >= 64<<20 {
		t.Skip("the temporary directory's file system does not support holes (HFS+)")
	}
	if sparse, err := IsSparse(holey); err != nil || !sparse {
		t.Fatalf("IsSparse(truncated file) = %v, %v; want true", sparse, err)
	}
	full := filepath.Join(dir, "full")
	if err := os.WriteFile(full, pattern(64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if sparse, err := IsSparse(full); err != nil || sparse {
		t.Fatalf("IsSparse(written file) = %v, %v; want false", sparse, err)
	}
}

func TestElevatedDarwin(t *testing.T) {
	got, err := Elevated()
	if err != nil {
		t.Fatal(err)
	}
	if want := os.Geteuid() == 0; got != want {
		t.Fatalf("Elevated = %v, want %v for euid %d", got, want, os.Geteuid())
	}
}

func TestUserDirsDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dl, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if want := filepath.Join(home, "Downloads"); dl != want {
		t.Fatalf("DownloadsDir = %q, want %q", dl, want)
	}
	logs, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	if want := filepath.Join(home, "Library", "Logs", "Downex"); logs != want {
		t.Fatalf("LogDir = %q, want %q", logs, want)
	}
	for _, d := range []string{dl, logs} {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s: %v, %v; want a directory without group or other access", d, fi, err)
		}
	}
	t.Setenv("HOME", "")
	if _, err := LogDir(); err == nil {
		t.Fatal("LogDir succeeded without a home directory")
	}
}

// TestLaunchAgent: SetLaunchAtLogin writes the LaunchAgent (0644, the user's, valid for
// plutil when it is installed), leaves an up-to-date one alone and removes it.
func TestLaunchAgent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: SetLaunchAtLogin refuses to write a LaunchAgent")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "Library", "LaunchAgents", "com.downex.agent.plist")
	if err := SetLaunchAtLogin(false, ""); err != nil {
		t.Fatalf("disable without an entry: %v", err)
	}
	exe := "/Applications/Down & <ex>.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent"
	if err := SetLaunchAtLogin(true, exe, "--background"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := launchAgentPlist(exe, []string{"--background"})
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("LaunchAgent =\n%s\nwant\n%s (%v)", data, want, err)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode() != 0o644 || int(fi.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		t.Fatalf("LaunchAgent: %v, %v; want a regular 0644 file of uid %d", fi, err, os.Getuid())
	}
	if plutil, err := exec.LookPath("plutil"); err == nil {
		if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
			t.Fatalf("plutil -lint: %v: %s", err, out)
		}
		out, err := exec.Command(plutil, "-convert", "json", "-o", "-", path).Output()
		if err != nil {
			t.Fatalf("plutil -convert json: %v", err)
		}
		var job struct {
			Label            string
			ProgramArguments []string
			RunAtLoad        bool
			KeepAlive        *bool
			ProcessType      string
		}
		if err := json.Unmarshal(out, &job); err != nil {
			t.Fatalf("plutil output %s: %v", out, err)
		}
		if job.Label != "com.downex.agent" || !slices.Equal(job.ProgramArguments, []string{exe, "--background"}) ||
			!job.RunAtLoad || job.KeepAlive == nil || *job.KeepAlive || job.ProcessType != "Interactive" {
			t.Fatalf("LaunchAgent as read by plutil = %+v", job)
		}
	}

	// Up to date: not rewritten.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := SetLaunchAtLogin(true, exe, "--background"); err != nil {
		t.Fatalf("enable again: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(old) {
		t.Fatalf("an up-to-date LaunchAgent was rewritten (%v, %v)", fi, err)
	}
	// A wrong mode is repaired.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := SetLaunchAtLogin(true, exe, "--background"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode after repair: %v, %v", fi, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".downex-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if err := SetLaunchAtLogin(true, "relative/downexd"); err == nil {
		t.Fatal("a relative executable was accepted")
	}
	if err := SetLaunchAtLogin(true, "/x/downexd", "a\nb"); err == nil {
		t.Fatal("a newline in an argument was accepted")
	}
	if err := SetLaunchAtLogin(false, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LaunchAgent still exists: %v", err)
	}
}
