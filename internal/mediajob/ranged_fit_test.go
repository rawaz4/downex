package mediajob

import (
	"testing"

	"downex/internal/media"
)

// FitChunk lowers a ranged track's chunk size to about two chunks per connection the
// job may use for its host, rounded up to 64 KiB, never below 2 MiB nor above the
// chunk size ChunkSize chose (docs/SITE-MEDIA-SPEC.md §2.4).
func TestFitChunk(t *testing.T) {
	const mib = 1 << 20
	rules := &testRules{
		conns: map[string]int{"rr1.googlevideo.com": 4, "one.example": 1},
		caps:  map[string]int64{"rr1.googlevideo.com": 10 * mib},
	}
	const plain = "https://cdn.example/v.mp4"
	const gv = "https://rr1.googlevideo.com/videoplayback"
	def := int64(media.DefaultChunkBytes)
	for _, tc := range []struct {
		name     string
		url      string
		chunk    int64
		size     int64
		maxConns int
		want     int64
	}{
		{"20 MiB over 8: the 2 MiB floor", plain, def, 20 * mib, 8, 2 * mib},
		{"40 MiB over 8", plain, def, 40 * mib, 8, 5 * mib / 2},
		{"100 MiB over 8", plain, def, 100 * mib, 8, 25 * mib / 4},
		{"100 MiB + 1 over 8: rounded up to 64 KiB", plain, def, 100*mib + 1, 8, 25*mib/4 + 64<<10},
		{"400 MiB over 8: the 16 MiB ceiling", plain, def, 400 * mib, 8, def},
		{"googlevideo, 60 MiB over its 4 connections", gv, 10 * mib, 60 * mib, 8, 15 * mib / 2},
		{"googlevideo, 1 GiB stays at its 10 MiB cap", gv, 10 * mib, 1 << 30, 8, 10 * mib},
		{"googlevideo, the user allows 2", gv, 10 * mib, 60 * mib, 2, 10 * mib},
		{"unknown size", plain, def, 0, 8, def},
		{"negative size", plain, def, -1, 8, def},
		{"one connection", plain, def, 40 * mib, 1, def},
		{"a host of one connection", "https://one.example/v", def, 40 * mib, 8, def},
		{"maxConns 0 is the default 32: the 2 MiB floor", plain, def, 40 * mib, 0, 2 * mib},
		{"maxConns above the hard maximum", plain, def, 40 * mib, 1000, 2 * mib},
		{"a ceiling below the floor", plain, media.MinChunkBytes, 40 * mib, 8, media.MinChunkBytes},
		{"a small ceiling", plain, 3 * mib, 400 * mib, 8, 3 * mib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FitChunk(rules, tc.url, tc.chunk, tc.size, tc.maxConns)
			if got != tc.want {
				t.Fatalf("FitChunk(%s, %d, %d, %d) = %d, want %d", tc.url, tc.chunk, tc.size, tc.maxConns, got, tc.want)
			}
			if got < media.MinChunkBytes || got > tc.chunk {
				t.Fatalf("FitChunk = %d, outside [%d, %d]", got, media.MinChunkBytes, tc.chunk)
			}
		})
	}
	// Without rules, every size and connection count stays within [MinChunkBytes, chunk]
	// and is a multiple of 64 KiB when it was lowered.
	for _, chunk := range []int64{media.MinChunkBytes, 2 * mib, 10 * mib, def, media.MaxChunkBytes} {
		for _, size := range []int64{0, 1, 3 * mib, 17*mib + 5, 999 * mib, media.MaxRangedSize} {
			for _, k := range []int{-1, 0, 1, 2, 3, 8, 16, 32, 64} {
				got := FitChunk(nil, plain, chunk, size, k)
				if got < media.MinChunkBytes || got > chunk {
					t.Fatalf("FitChunk(nil, %d, %d, %d) = %d, outside [%d, %d]", chunk, size, k, got, media.MinChunkBytes, chunk)
				}
				if got != chunk && got%(64<<10) != 0 {
					t.Fatalf("FitChunk(nil, %d, %d, %d) = %d, not a multiple of 64 KiB", chunk, size, k, got)
				}
			}
		}
	}
}
