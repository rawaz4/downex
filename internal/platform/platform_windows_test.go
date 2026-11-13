//go:build windows

package platform

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wantDetachFacts are the facts a helper started by StartDetached must report: no
// console, and the Windows directory (not the starter's) as working directory (L4).
func wantDetachFacts(t *testing.T) []string {
	t.Helper()
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"console=none", "cwd=" + strings.ToLower(filepath.Clean(dir))}
}

// detachFacts describes the calling process: whether it is attached to a console at all
// (GetConsoleProcessList fails without one; a console window may be hidden or, under a
// pseudo console, absent, so GetConsoleWindow cannot tell), and its working directory.
func detachFacts() []string {
	var pids [4]uint32
	n, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList").
		Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	facts := []string{"console=attached"}
	if n == 0 {
		facts = []string{"console=none"}
	}
	if wd, err := os.Getwd(); err == nil {
		facts = append(facts, "cwd="+strings.ToLower(filepath.Clean(wd)))
	}
	return facts
}

// TestElevatedDecision (SEC-2): an elevated split token ("Run as administrator") and
// the System level of a service are elevated; a normal or Low process, and an
// administrator without UAC (no split token, so no weaker process to protect), are not.
func TestElevatedDecision(t *testing.T) {
	const (
		typeDefault, typeFull, typeLimited = 1, 2, 3
		low, medium, high, system          = 0x1000, 0x2000, 0x3000, 0x4000
	)
	for _, c := range []struct {
		typ, level uint32
		want       bool
	}{
		{typeLimited, medium, false},
		{typeDefault, medium, false},
		{typeLimited, low, false},
		{typeDefault, high, false}, // UAC off: every process of the user is High
		{typeFull, high, true},
		{typeDefault, system, true},
	} {
		if got := elevated(c.typ, c.level); got != c.want {
			t.Errorf("elevated(type %d, level %#x) = %v, want %v", c.typ, c.level, got, c.want)
		}
	}
	got, err := Elevated()
	if err != nil {
		t.Fatalf("Elevated: %v", err)
	}
	t.Logf("this test process is elevated: %v", got)
}

// newJob creates a job object with the given limit flags that kills its processes when
// its last handle closes; the returned func closes it.
func newJob(t *testing.T, limits uint32) (windows.Handle, func()) {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | limits
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		t.Fatal(err)
	}
	var once bool
	closeJob := func() {
		if !once {
			once = true
			_ = windows.CloseHandle(job)
		}
	}
	t.Cleanup(closeJob)
	return job, closeJob
}

// startInJob starts a "spawn-when" helper inside job and returns the file that lets it
// call StartDetached, and a func that waits for the helper to exit.
func startInJob(t *testing.T, job windows.Handle, goFile, marker string) (startFile string, wait func() error) {
	t.Helper()
	startFile = filepath.Join(filepath.Dir(goFile), "start")
	cmd := exec.Command(os.Args[0], helperCommand("spawn-when", startFile, goFile, marker)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, ph)
		_ = windows.CloseHandle(ph)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("assign the helper to the job: %v", err)
	}
	return startFile, func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("%w: %s", err, out.String())
		}
		return nil
	}
}

// TestStartDetachedBreaksAwayFromJob (L5): a daemon started from inside a job that
// kills its processes on close (an SSH session, a CI step) survives the job.
func TestStartDetachedBreaksAwayFromJob(t *testing.T) {
	goFile, marker := helperFiles(t)
	job, closeJob := newJob(t, windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK)
	startFile, wait := startInJob(t, job, goFile, marker)
	if err := os.WriteFile(startFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("starter: %v", err)
	}
	closeJob() // kills every process still in the job
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	checkFacts(t, awaitMarker(t, marker))
}

// TestStartDetachedInJobWithoutBreakaway: a job that forbids breakaway still lets
// StartDetached start the process (as a member of the job).
func TestStartDetachedInJobWithoutBreakaway(t *testing.T) {
	goFile, marker := helperFiles(t)
	job, _ := newJob(t, 0)
	startFile, wait := startInJob(t, job, goFile, marker)
	if err := os.WriteFile(startFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("starter: %v", err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	checkFacts(t, awaitMarker(t, marker))
}

// diskUsage returns the bytes the file at path occupies on disk (GetCompressedFileSizeW,
// which counts only allocated ranges of sparse files).
func diskUsage(t *testing.T, path string) int64 {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	var high uint32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCompressedFileSizeW")
	low, _, callErr := proc.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&high)))
	if uint32(low) == 0xFFFFFFFF && callErr != windows.ERROR_SUCCESS {
		t.Fatalf("GetCompressedFileSizeW(%s): %v", path, callErr)
	}
	return int64(high)<<32 | int64(uint32(low))
}

// volumeSupportsSparse reports whether f's volume supports sparse files.
func volumeSupportsSparse(t *testing.T, f *os.File) bool {
	t.Helper()
	var flags uint32
	err := control(f, func(fd uintptr) error {
		return windows.GetVolumeInformationByHandle(windows.Handle(fd), nil, 0, nil, nil, &flags, nil, 0)
	})
	if err != nil {
		t.Fatalf("GetVolumeInformationByHandle: %v", err)
	}
	return flags&windows.FILE_SUPPORTS_SPARSE_FILES != 0
}

func isSparse(t *testing.T, path string) bool {
	t.Helper()
	sparse, err := IsSparse(path)
	if err != nil {
		t.Fatalf("IsSparse: %v", err)
	}
	return sparse
}

// TestPreallocateSparse4GiB: on a volume with sparse files (NTFS) preallocating 4 GiB is
// near-instant, marks the file sparse and takes almost no disk space, even after a write
// at its tail, which reads back.
func TestPreallocateSparse4GiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.dxpart")
	f := createFile(t, path)
	if !volumeSupportsSparse(t, f) {
		t.Skip("the temporary directory's volume does not support sparse files")
	}
	const size = 4 << 30
	start := time.Now()
	if err := Preallocate(f, size); err != nil {
		t.Fatalf("Preallocate: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Preallocate(4 GiB) took %v, want near-instant", took)
	}
	if got := fileSize(t, f); got != size {
		t.Fatalf("size = %d, want %d", got, size)
	}
	if !isSparse(t, path) {
		t.Fatal("preallocated file is not sparse")
	}
	if used := diskUsage(t, path); used > 1<<20 {
		t.Fatalf("preallocated file occupies %d bytes on disk, want almost none", used)
	}

	tail := pattern(64 << 10)
	start = time.Now()
	if _, err := f.WriteAt(tail, size-int64(len(tail))); err != nil {
		t.Fatalf("write tail: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("writing the tail took %v: the gap was zero-filled", took)
	}
	if got := readAt(t, f, size-int64(len(tail)), len(tail)); !bytes.Equal(got, tail) {
		t.Fatal("tail does not read back")
	}
	if got := readAt(t, f, size/2, 4096); !bytes.Equal(got, make([]byte, 4096)) {
		t.Fatal("unwritten middle does not read as zeros")
	}
	if got := fileSize(t, f); got != size {
		t.Fatalf("size after tail write = %d, want %d", got, size)
	}
	if used := diskUsage(t, path); used > 2<<20 {
		t.Fatalf("file occupies %d bytes on disk after a 64 KiB write, want about 64 KiB", used)
	}
}

// TestPreallocateSparseKeepsExistingData: marking a file with data sparse and extending
// it keeps its data allocated and intact.
func TestPreallocateSparseKeepsExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.dxpart")
	f := createFile(t, path)
	if !volumeSupportsSparse(t, f) {
		t.Skip("the temporary directory's volume does not support sparse files")
	}
	data := pattern(1 << 20)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := Preallocate(f, 1<<30); err != nil {
		t.Fatalf("Preallocate: %v", err)
	}
	if !isSparse(t, path) {
		t.Fatal("file is not sparse")
	}
	if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
		t.Fatal("existing data changed")
	}
	if used := diskUsage(t, path); used < int64(len(data)) || used > 4<<20 {
		t.Fatalf("file occupies %d bytes on disk, want about %d", used, len(data))
	}
}

// TestPreallocateWithoutSparseSupport takes the path of a FAT32/exFAT volume: the file
// is extended, not marked sparse, and keeps its data.
func TestPreallocateWithoutSparseSupport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fat.dxpart")
	f := createFile(t, path)
	data := pattern(64 << 10)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	const size = 32 << 20
	if err := preallocate(f, size, false); err != nil {
		t.Fatalf("preallocate: %v", err)
	}
	if got := fileSize(t, f); got != size {
		t.Fatalf("size = %d, want %d", got, size)
	}
	if isSparse(t, path) {
		t.Fatal("file was marked sparse")
	}
	if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
		t.Fatal("existing data changed")
	}
	tail := pattern(4096)
	if _, err := f.WriteAt(tail, size-int64(len(tail))); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, f, size-int64(len(tail)), len(tail)); !bytes.Equal(got, tail) {
		t.Fatal("tail does not read back")
	}
}

func TestIsSparsePlainFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(path, pattern(4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if isSparse(t, path) {
		t.Fatal("a plain file is reported sparse")
	}
}

func TestDownloadsDirIsKnownFolder(t *testing.T) {
	want, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if !strings.EqualFold(got, filepath.Clean(want)) {
		t.Fatalf("DownloadsDir = %q, want %q", got, want)
	}
}

func TestLogDirUnderLocalAppData(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	dir, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	if want := filepath.Join(base, "Downex", "logs"); dir != want {
		t.Fatalf("LogDir = %q, want %q", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("LogDir %q was not created (%v)", dir, err)
	}
	again, err := LogDir()
	if err != nil || again != dir {
		t.Fatalf("second LogDir = %q, %v; want %q", again, err, dir)
	}
}
