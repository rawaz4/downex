//go:build windows

package glue

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The first start of an exe file has its whole image read in from outside (0.5-1 s after the
// start), which made the zero-filled end of .data (Go's .bss, 32 MiB of it the FIPS entropy
// buffer) 34 MB of private pages until the 20 s trim. Read in the same way here (a read of the
// process's memory, as the outside reader's), the watch sees it and gives those pages back,
// and leaves them when nothing came in.
func TestImageTailWatch(t *testing.T) {
	ranges, err := imageZeroFill()
	if err != nil {
		t.Fatal(err)
	}
	var total uintptr
	for _, r := range ranges {
		total += r.size()
	}
	if total < 1<<20 {
		t.Fatalf("zero-filled image ranges %v: %d bytes, want the .bss (MBs)", ranges, total)
	}
	w := newTailWatch(ranges)
	// Earlier tests of the package may have read the range in: start from it given back.
	if err := w.release(); err != nil {
		t.Fatal(err)
	}
	if w.base, err = w.resident(); err != nil {
		t.Fatal(err)
	}
	if released, err := w.check(); err != nil || released {
		t.Fatalf("check with nothing read in: released %v, %v", released, err)
	}

	for i := range tailMark {
		tailMark[i] = byte(i%251 + 1)
	}
	readIn(t, ranges)
	n, _ := w.resident()
	if n < len(w.buf)*3/4 {
		t.Fatalf("after the read %d of %d samples resident, want nearly all", n, len(w.buf))
	}
	released, err := w.check()
	if err != nil || !released {
		t.Fatalf("check after the read: released %v, %v", released, err)
	}
	if w.base > len(w.buf)/tailGrowth {
		t.Fatalf("after the release %d of %d samples resident", w.base, len(w.buf))
	}
	// The contents stay (tailMark is in the .bss, so in the range).
	for i, b := range tailMark {
		if b != byte(i%251+1) {
			t.Fatalf("tailMark[%d] = %d after the release", i, b)
		}
	}
	if released, _ := w.check(); released {
		t.Fatal("a second check released again with nothing read in")
	}

	// After the first read the range is read in again a few MB a second: the watch gives it
	// back once the growth since its release reaches 1/tailGrowth, however slowly it came.
	readPart(t, ranges, 0, 0.5/tailGrowth)
	if released, _ := w.check(); released {
		t.Fatal("released at half the growth")
	}
	readPart(t, ranges, 0.5/tailGrowth, 1.5/tailGrowth)
	if released, err := w.check(); err != nil || !released {
		t.Fatalf("a slow read in steps: released %v, %v", released, err)
	}

	// The watch itself: it finds the read and ends at its deadline.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { watchImageTail(ctx, 10*time.Millisecond, 2*time.Second, log); close(done) }()
	time.Sleep(50 * time.Millisecond)
	readIn(t, ranges)
	probe := newTailWatch(ranges)
	deadline := time.Now().Add(time.Second)
	for {
		n, _ := probe.resident()
		if n <= len(probe.buf)/tailGrowth {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watch left %d of %d samples resident", n, len(probe.buf))
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("the watch ended before its deadline")
	default:
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not end at its deadline")
	}
}

// tailMark is a .bss variable the test writes before a release and reads after it.
var tailMark [3 * 4096]byte

// readIn reads ranges as an outside reader does (ReadProcessMemory), page by page.
func readIn(t *testing.T, ranges []pageRange) { t.Helper(); readPart(t, ranges, 0, 1) }

// readPart reads the share [from, to) of each range so.
func readPart(t *testing.T, ranges []pageRange, from, to float64) {
	t.Helper()
	var b [1]byte
	for _, r := range ranges {
		pages := float64(r.size() / 4096)
		lo, hi := r.start+uintptr(from*pages)*4096, r.start+uintptr(to*pages)*4096
		for a := lo; a < hi && a < r.end; a += 4096 {
			if err := windows.ReadProcessMemory(windows.CurrentProcess(), a, &b[0], 1, nil); err != nil {
				t.Fatalf("read %#x: %v", a, err)
			}
		}
	}
}

// zeroFillOf takes only the writable sections' part past their file data.
func TestZeroFillOf(t *testing.T) {
	hdr := make([]byte, 4096)
	hdr[0], hdr[1] = 'M', 'Z'
	const nt = 0x80
	binary.LittleEndian.PutUint32(hdr[0x3c:], nt)
	copy(hdr[nt:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(hdr[nt+6:], 3)    // sections
	binary.LittleEndian.PutUint16(hdr[nt+20:], 240) // optional header
	sec := func(i int, vsize, rva, raw, flags uint32) {
		s := hdr[nt+24+240+i*40:]
		binary.LittleEndian.PutUint32(s[8:], vsize)
		binary.LittleEndian.PutUint32(s[12:], rva)
		binary.LittleEndian.PutUint32(s[16:], raw)
		binary.LittleEndian.PutUint32(s[36:], flags)
	}
	sec(0, 0x9000, 0x1000, 0x1000, 0x60000020)  // .text: not writable
	sec(1, 0x1800, 0xa000, 0x2000, 0xc0000040)  // writable, all file data
	sec(2, 0x25000, 0xc000, 0x1200, 0xc0000040) // .data with its .bss
	got, err := zeroFillOf(0x140000000, hdr)
	if err != nil {
		t.Fatal(err)
	}
	want := pageRange{start: 0x140000000 + 0xe000, end: 0x140000000 + 0x31000}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("zeroFillOf = %#v, want [%#v]", got, want)
	}
	if _, err := zeroFillOf(0, make([]byte, 64)); err == nil {
		t.Fatal("no error for a page without a PE image")
	}
}
