//go:build perf

package perf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"sync"
)

// contentPeriod is the period of the served content. It is a prime, so a range written
// at a wrong offset (by any shift that is not a multiple of it) fails verification,
// while serving and checking stay as cheap as copying from one block.
const contentPeriod = 4_194_301

// contentBlock is one period of the content: pseudo-random bytes from a fixed seed.
var contentBlock = sync.OnceValue(func() []byte {
	b := make([]byte, contentPeriod+8)
	r := rand.New(rand.NewPCG(0x646f776e, 0x6578))
	for i := 0; i+8 <= len(b); i += 8 {
		binary.LittleEndian.PutUint64(b[i:], r.Uint64())
	}
	return b[:contentPeriod]
})

// writeContent writes content bytes [start, end] to w in writes of at most chunk bytes.
// It returns the bytes written.
func writeContent(w io.Writer, start, end int64, chunk int) (int64, error) {
	block := contentBlock()
	var n int64
	for pos := start; pos <= end; {
		off := pos % contentPeriod
		k := min(int64(chunk), end-pos+1, contentPeriod-off)
		m, err := w.Write(block[off : off+k])
		n += int64(m)
		pos += int64(m)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// verifyContent checks that the file at path holds exactly content bytes [0, size).
func verifyContent(path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != size {
		return fmt.Errorf("%s has %d bytes, want %d", path, fi.Size(), size)
	}
	block := contentBlock()
	buf := make([]byte, 1<<20)
	for pos := int64(0); pos < size; {
		n := int(min(int64(len(buf)), size-pos))
		if _, err := io.ReadFull(f, buf[:n]); err != nil {
			return fmt.Errorf("read at %d: %w", pos, err)
		}
		for i := 0; i < n; {
			off := (pos + int64(i)) % contentPeriod
			k := int(min(int64(n-i), contentPeriod-off))
			if !bytes.Equal(buf[i:i+k], block[off:off+int64(k)]) {
				return fmt.Errorf("content differs in bytes %d-%d", pos+int64(i), pos+int64(i+k)-1)
			}
			i += k
		}
		pos += int64(n)
	}
	return nil
}
