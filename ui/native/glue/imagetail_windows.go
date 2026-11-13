//go:build windows

package glue

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The zero-filled end of the window's exe image (measured 2026-11-05, ui/native/build.ps1's
// build, QueryWorkingSetEx over the image range of a running window):
//
// The image's writable .data section ends in 32.6 MiB of zero-filled pages (Go's .bss and
// .noptrbss: VirtualSize past SizeOfRawData), 32 MiB of them crypto/internal/fips140/drbg's
// memory (Go 1.26+'s FIPS 140-3 entropy scratch buffer, a [1 << 25]byte touched only in
// FIPS mode). On the first start of each exe file, all of the image is read once, from
// outside the window's own code, 0.5-1 s after the start (.text, .rdata, .reloc and .symtab
// as well as .data, none of which the window reads after loading; later starts of the same
// file are not read so): each zero-filled page read becomes a private page of this
// process (Windows has no shared zero page), 34.1 MB of the cold start's 37-42 MB, held until
// the 20 s trim. The shared pages of the read cost nothing private. Neither the build nor
// the start-up can remove it: the buffer is the standard library's, mapped with the image
// whatever the linker flags (ASLR, DEP and the preferred base change nothing: the image
// was mapped at its ASLR base, its relocated pages stayed shared), and the read is not the
// window's.
//
// So the window watches that range from its start until the start-up trim: whenever a sample
// of its pages shows it read in (tailGrowth), it takes those pages out of its working set at
// once (VirtualUnlock on pages never locked, as documented), and only those: the pages the
// window uses stay, so nothing is drawn slower; a page of the range the window does use
// comes back with a soft fault, its contents kept.

const (
	// TailWatchEvery is how often the watch samples the range.
	TailWatchEvery = 250 * time.Millisecond
	// tailStride samples every 16th page (a 34 MB range: 520 pages, 8 KiB of answers).
	tailStride = 16
	// 1/tailGrowth is the share of the sampled pages that come into the working set for the
	// watch to give them back (about 4 MB of the 34): the first read makes all of them
	// resident at once, then the range is read in again, a few MB a second, for about 10 s
	// (measured: 21-24 MB back 4 s after a release); the window's own use of its .bss is
	// about 2 % of them.
	tailGrowth = 8
)

// pageRange is [start, end) in this process, page-aligned.
type pageRange struct{ start, end uintptr }

func (r pageRange) size() uintptr { return r.end - r.start }

// imageZeroFill returns the zero-filled ends of the exe image's writable sections (the part
// of each past its file data), read from the image's own headers in memory.
func imageZeroFill() ([]pageRange, error) {
	var mod windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &mod); err != nil {
		return nil, fmt.Errorf("the exe's module: %w", err)
	}
	base := uintptr(mod)
	hdr := make([]byte, 4096) // the headers' page: DOS, NT and the section table
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), base, &hdr[0], uintptr(len(hdr)), nil); err != nil {
		return nil, fmt.Errorf("the exe's headers: %w", err)
	}
	return zeroFillOf(base, hdr)
}

// zeroFillOf parses a PE image's headers page (mapped at base) for imageZeroFill.
func zeroFillOf(base uintptr, hdr []byte) ([]pageRange, error) {
	const (
		page      = 4096
		scnWrite  = 0x80000000 // IMAGE_SCN_MEM_WRITE
		secHeader = 40
	)
	if len(hdr) < 0x40 || hdr[0] != 'M' || hdr[1] != 'Z' {
		return nil, errors.New("not a PE image")
	}
	nt := int(binary.LittleEndian.Uint32(hdr[0x3c:]))
	if nt < 0 || nt+24 > len(hdr) || string(hdr[nt:nt+4]) != "PE\x00\x00" {
		return nil, errors.New("no PE signature")
	}
	count := int(binary.LittleEndian.Uint16(hdr[nt+6:]))
	optSize := int(binary.LittleEndian.Uint16(hdr[nt+20:]))
	first := nt + 24 + optSize
	if first+count*secHeader > len(hdr) {
		return nil, errors.New("section table past the headers page")
	}
	up := func(v uintptr) uintptr { return (v + page - 1) &^ (page - 1) }
	var out []pageRange
	for i := 0; i < count; i++ {
		s := hdr[first+i*secHeader:]
		vsize := uintptr(binary.LittleEndian.Uint32(s[8:]))
		rva := uintptr(binary.LittleEndian.Uint32(s[12:]))
		raw := uintptr(binary.LittleEndian.Uint32(s[16:]))
		flags := binary.LittleEndian.Uint32(s[36:])
		if flags&scnWrite == 0 || vsize <= raw {
			continue
		}
		r := pageRange{start: base + up(rva+raw), end: base + up(rva+vsize)}
		if r.end > r.start {
			out = append(out, r)
		}
	}
	return out, nil
}

// wsEx is PSAPI_WORKING_SET_EX_INFORMATION with the address as a number.
type wsEx struct {
	va    uintptr
	attrs uint64
}

// tailWatch samples ranges' pages and gives them back when they are read in.
type tailWatch struct {
	ranges []pageRange
	buf    []wsEx
	base   int // resident samples when the watch began or last released them
}

func newTailWatch(ranges []pageRange) *tailWatch {
	w := &tailWatch{ranges: ranges}
	for _, r := range ranges {
		for a := r.start; a < r.end; a += tailStride * 4096 {
			w.buf = append(w.buf, wsEx{va: a})
		}
	}
	return w
}

// resident counts the sampled pages in this process's working set.
func (w *tailWatch) resident() (int, error) {
	if len(w.buf) == 0 {
		return 0, nil
	}
	for i := range w.buf {
		w.buf[i].attrs = 0
	}
	size := uint32(uintptr(len(w.buf)) * unsafe.Sizeof(w.buf[0]))
	if err := windows.QueryWorkingSetEx(windows.CurrentProcess(), uintptr(unsafe.Pointer(&w.buf[0])), size); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range w.buf {
		n += int(e.attrs & 1) // Valid: in the working set
	}
	return n, nil
}

// release takes the ranges' pages out of the working set. VirtualUnlock does that to pages
// that are not locked (and reports ERROR_NOT_LOCKED, the expected answer); their contents
// stay.
func (w *tailWatch) release() error {
	for _, r := range w.ranges {
		if err := windows.VirtualUnlock(r.start, r.size()); err != nil && !errors.Is(err, windows.ERROR_NOT_LOCKED) {
			return err
		}
	}
	return nil
}

// check samples the ranges and releases them when a share of 1/tailGrowth of the samples
// came in since the watch began or last released them; it reports whether it released them.
func (w *tailWatch) check() (bool, error) {
	n, err := w.resident()
	if err != nil {
		return false, err
	}
	if n-w.base < (len(w.buf)+tailGrowth-1)/tailGrowth {
		return false, nil
	}
	if err := w.release(); err != nil {
		return false, err
	}
	w.base, err = w.resident()
	return true, err
}

// watchImageTail runs the watch every `every` until `until` has passed or ctx ends.
func watchImageTail(ctx context.Context, every, until time.Duration, log *slog.Logger) {
	ranges, err := imageZeroFill()
	if err != nil {
		log.Warn("start-up memory watch", "error", err)
		return
	}
	w := newTailWatch(ranges)
	if w.base, err = w.resident(); err != nil {
		log.Warn("start-up memory watch", "error", err)
		return
	}
	end := time.NewTimer(until)
	defer end.Stop()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-end.C:
			return
		case <-tick.C:
			if _, err := w.check(); err != nil {
				log.Warn("start-up memory watch", "error", err)
				return
			}
		}
	}
}
