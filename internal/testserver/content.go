package testserver

import (
	"crypto/sha256"
	"encoding/binary"
)

// word returns the 64-bit content word with index i: a splitmix64 hash, so misplaced
// or repeated ranges are detected by byte comparison.
func word(seed, i uint64) uint64 {
	z := seed + (i+1)*0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Byte returns byte i of the content generated from seed.
func Byte(seed uint64, i int64) byte {
	u := uint64(i)
	return byte(word(seed, u>>3) >> (8 * (u & 7)))
}

// Fill writes the content bytes [off, off+len(p)) generated from seed into p.
func Fill(seed uint64, p []byte, off int64) {
	for i := 0; i < len(p); {
		pos := uint64(off) + uint64(i)
		if pos&7 == 0 && len(p)-i >= 8 {
			binary.LittleEndian.PutUint64(p[i:], word(seed, pos>>3))
			i += 8
			continue
		}
		p[i] = byte(word(seed, pos>>3) >> (8 * (pos & 7)))
		i++
	}
}

// Bytes returns the content bytes [off, off+n) generated from seed.
func Bytes(seed uint64, off, n int64) []byte {
	p := make([]byte, n)
	Fill(seed, p, off)
	return p
}

// Sum256 returns the SHA-256 of the first size content bytes generated from seed.
func Sum256(seed uint64, size int64) [32]byte {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for off := int64(0); off < size; {
		n := min(int64(len(buf)), size-off)
		Fill(seed, buf[:n], off)
		h.Write(buf[:n])
		off += n
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}
