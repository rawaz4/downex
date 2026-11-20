package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"downex/internal/model"
)

// BenchmarkListDownloads10k measures the read of a 10,000-download history: the daemon
// lists every record when it starts, and the engine lists them to pick a free filename.
func BenchmarkListDownloads10k(b *testing.B) {
	s, err := OpenSQLite(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	for i := range 10_000 {
		d := fullDownload(fmt.Sprint(i))
		d.ID = 0
		if err := s.CreateDownload(ctx, &d); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		list, err := s.ListDownloads(ctx)
		if err != nil || len(list) != 10_000 {
			b.Fatal(len(list), err)
		}
	}
}

// A listing longer than one parsing chunk keeps the row order, and a bad row fails it
// with the first bad row's error, as a row-by-row parse would.
func TestListDownloadsManyChunks(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "many.db"))
	ctx := context.Background()
	const n = 3*listChunk + 7
	for i := range n {
		d := fullDownload(fmt.Sprint(i))
		d.ID = 0
		if err := s.CreateDownload(ctx, &d); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListDownloads(ctx)
	if err != nil || len(list) != n {
		t.Fatalf("list = %d, %v", len(list), err)
	}
	for i, d := range list {
		if d.ID != model.DownloadID(i+1) || d.Media == nil || d.Media.Title != fullDownload(fmt.Sprint(i)).Media.Title {
			t.Fatalf("row %d = id %d, media %v", i, d.ID, d.Media)
		}
	}
	for _, id := range []int{2*listChunk + 3, listChunk + 5} {
		if _, err := s.db.ExecContext(ctx, `UPDATE downloads SET media = '{' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.ListDownloads(ctx)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("download %d:", listChunk+5)) {
		t.Fatalf("err = %v", err)
	}
}
