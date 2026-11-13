package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

func openTestSQLite(t *testing.T, path string) *SQLite {
	t.Helper()
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteConformance(t *testing.T) {
	runConformance(t, func(t *testing.T) Store {
		return openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	})
}

// TestSQLitePragmas checks the settings on two distinct pooled connections,
// since pragmas are per connection.
func TestSQLitePragmas(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	ctx := context.Background()
	var conns []*sql.Conn
	for range 2 {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var journal string
		var sync, fk, busy int
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("conn %d journal_mode: %v", i, err)
		}
		for _, p := range []struct {
			name string
			dst  *int
		}{{"synchronous", &sync}, {"foreign_keys", &fk}, {"busy_timeout", &busy}} {
			if err := c.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(p.dst); err != nil {
				t.Fatalf("conn %d %s: %v", i, p.name, err)
			}
		}
		if !strings.EqualFold(journal, "wal") || sync != 2 || fk != 1 || busy != 5000 {
			t.Errorf("conn %d: journal_mode=%s synchronous=%d foreign_keys=%d busy_timeout=%d; want wal 2 1 5000",
				i, journal, sync, fk, busy)
		}
		_ = c.Close()
	}
}

func TestSQLiteReopenPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	a := mustCreate(t, s, fullDownload("a"))
	b := mustCreate(t, s, fullDownload("b"))
	c := mustCreate(t, s, fullDownload("c"))
	segsA := fixtureSegments(4, 1234)
	a.State = model.StateDownloading
	if err := s.SaveCheckpoint(ctx, &a, segsA); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	b.State = model.StateCompleted
	b.Request.Body = []byte{}
	if err := s.UpdateDownload(ctx, &b); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	if err := s.DeleteDownload(ctx, c.ID); err != nil {
		t.Fatalf("DeleteDownload: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := openTestSQLite(t, path)
	list, err := s2.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads after reopen: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListDownloads after reopen returned %d records, want 2: %+v", len(list), list)
	}
	assertDownload(t, list[0], a)
	assertDownload(t, list[1], b)
	assertSegments(t, mustLoad(t, s2, a.ID), segsA)
	assertSegments(t, mustLoad(t, s2, b.ID), []model.Segment{})

	// IDs keep increasing across reopen, past the deleted one.
	if d := mustCreate(t, s2, fullDownload("d")); d.ID <= c.ID {
		t.Fatalf("ID after reopen = %d, want > %d", d.ID, c.ID)
	}
}

func TestSQLiteMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	s := openTestSQLite(t, path)
	d := mustCreate(t, s, fullDownload("migrate"))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen sequentially, then from several goroutines at once (each a separate
	// pool, like separate processes); every open must succeed and leave one
	// schema at the latest version with the data intact.
	s2 := openTestSQLite(t, path)
	_ = s2.Close()
	var wg sync.WaitGroup
	stores := make([]*SQLite, 4)
	errs := make([]error, len(stores))
	for i := range stores {
		wg.Go(func() { stores[i], errs[i] = OpenSQLite(path) })
	}
	wg.Wait()
	for _, st := range stores {
		if st != nil {
			t.Cleanup(func() { _ = st.Close() })
		}
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent OpenSQLite %d: %v", i, err)
		}
	}

	s3 := stores[0]
	v, err := readSchemaVersion(ctx, s3.db)
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	if v != len(ms) {
		t.Fatalf("schema version = %d, want %d", v, len(ms))
	}
	var rows int
	if err := s3.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_version").Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_version has %d rows, want 1", rows)
	}
	assertDownload(t, mustGet(t, s3, d.ID), d)
}

// TestSQLiteMigratesVersion1 opens a database written by a build that knew only
// migration 1: its rows gain the M4.5 columns with their "unknown" defaults, and the
// new fields round-trip afterwards.
func TestSQLiteMigratesVersion1(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 2 {
		t.Fatalf("%d migrations, want at least 2", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:1]); err != nil {
		t.Fatalf("migrate to version 1: %v", err)
	}
	// A row exactly as the version-1 codec wrote it.
	const v1Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at) VALUES ('https://example.test/old.zip', 'GET', NULL, NULL, '', '/tmp/old', 'old.zip', 42, 1,
		3, '"e"', 1, '', 4, 0, '', 1000, 2000, NULL)`
	if _, err := raw.ExecContext(ctx, v1Insert); err != nil {
		t.Fatalf("insert version-1 row: %v", err)
	}
	if v, err := readSchemaVersion(ctx, raw); err != nil || v != 1 {
		t.Fatalf("schema version = %d, %v; want 1", v, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s := openTestSQLite(t, path)
	if v, err := readSchemaVersion(ctx, s.db); err != nil || v != len(ms) {
		t.Fatalf("schema version after open = %d, %v; want %d", v, err, len(ms))
	}
	list, err := s.ListDownloads(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListDownloads = %v, %v; want the version-1 row", list, err)
	}
	old := list[0]
	if old.Request.URL != "https://example.test/old.zip" || old.State != model.StatePaused || old.Size != 42 ||
		old.Source != model.SourceUnknown || old.ExpectedSize != 0 || old.MIME != "" || old.Category != "" {
		t.Fatalf("migrated row = %+v", old)
	}
	old.State = model.StateAwaitingConfirmation
	old.Source, old.ExpectedSize, old.MIME, old.Category = model.SourceBrowser, 7, "video/mp4", "Video"
	if err := s.UpdateDownload(ctx, &old); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	assertDownload(t, mustGet(t, s, old.ID), old)

	// A database migrated by this build is opened again unchanged.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	assertDownload(t, mustGet(t, s2, old.ID), old)
}

func TestSQLiteRejectsNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	s := openTestSQLite(t, path)
	if _, err := s.db.ExecContext(ctx, "UPDATE schema_version SET version = 999"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := OpenSQLite(path)
	if err == nil {
		_ = s2.Close()
		t.Fatal("OpenSQLite on a newer schema succeeded, want ErrUnsupportedSchema")
	}
	assertErrorIs(t, "OpenSQLite newer schema", err, ErrUnsupportedSchema)

	raw, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	v, err := readSchemaVersion(ctx, raw)
	if err != nil || v != 999 {
		t.Fatalf("schema version after refused open = %d, %v; want 999 untouched", v, err)
	}
}

// TestSQLiteCheckpointRollsBack injects a failure after the record update and
// some segment inserts have run, and checks that nothing of the checkpoint
// remains.
func TestSQLiteCheckpointRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	d := mustCreate(t, s, fullDownload("rollback"))
	segs := fixtureSegments(2, 50)
	if err := s.SaveCheckpoint(ctx, &d, segs); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	stored := d

	const trigger = `CREATE TRIGGER fail_seg_99 BEFORE INSERT ON segments
		WHEN NEW.seg_id = 99 BEGIN SELECT RAISE(ABORT, 'injected failure'); END`
	if _, err := s.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	d.State = model.StateFailed
	d.Size = 1
	bad := []model.Segment{{ID: 1, Start: 0, End: 9}, {ID: 2, Start: 10, End: 19}, {ID: 99, Start: 20, End: 29}}
	err := s.SaveCheckpoint(ctx, &d, bad)
	if err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("SaveCheckpoint with injected failure: got %v", err)
	}
	assertDownload(t, mustGet(t, s, d.ID), stored)
	assertSegments(t, mustLoad(t, s, d.ID), segs)

	// The store stays usable after a rolled-back checkpoint.
	good := fixtureSegments(1, 60)
	if err := s.SaveCheckpoint(ctx, &d, good); err != nil {
		t.Fatalf("SaveCheckpoint after rollback: %v", err)
	}
	assertSegments(t, mustLoad(t, s, d.ID), good)
}

// TestSQLiteDeleteLeavesNoOrphans looks at the segments table directly:
// LoadSegments joins from downloads, so it could not see orphaned rows.
func TestSQLiteDeleteLeavesNoOrphans(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	d := mustCreate(t, s, fullDownload("orphans"))
	if err := s.SaveCheckpoint(ctx, &d, fixtureSegments(5, 1)); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	if err := s.DeleteDownload(ctx, d.ID); err != nil {
		t.Fatalf("DeleteDownload: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM segments").Scan(&n); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d orphaned segment rows after delete", n)
	}
}

func TestSQLiteCorruptRowIsAnError(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	d := mustCreate(t, s, fullDownload("corrupt"))
	if _, err := s.db.ExecContext(ctx, "UPDATE downloads SET headers = '{not json' WHERE id = ?", int64(d.ID)); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	if _, err := s.GetDownload(ctx, d.ID); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDownload on corrupt row: got %v, want a decode error", err)
	}
	if _, err := s.ListDownloads(ctx); err == nil {
		t.Fatal("ListDownloads on corrupt row succeeded, want a decode error")
	}
}

func TestOpenSQLiteInvalidPath(t *testing.T) {
	_, err := OpenSQLite("")
	assertErrorIs(t, "OpenSQLite(\"\")", err, ErrInvalid)
	_, err = OpenSQLite(filepath.Join(t.TempDir(), "what?.db"))
	assertErrorIs(t, "OpenSQLite(path with ?)", err, ErrInvalid)

	missingDir := filepath.Join(t.TempDir(), "no", "such", "dir", "downex.db")
	if s, err := OpenSQLite(missingDir); err == nil {
		_ = s.Close()
		t.Fatal("OpenSQLite in a missing directory succeeded, want an error")
	}
}

// TestSQLiteWriteWaitIsCancellable holds the write semaphore and checks that a
// queued writer gives up on context expiry and on Close.
func TestSQLiteWriteWaitIsCancellable(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	d := mustCreate(t, s, fullDownload("wait"))

	s.writeSem <- struct{}{} // simulate a long-running writer
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	assertErrorIs(t, "UpdateDownload while blocked", s.UpdateDownload(ctx, &d), context.DeadlineExceeded)

	done := make(chan error, 1)
	go func() { done <- s.SaveCheckpoint(context.Background(), &d, nil) }()
	time.Sleep(20 * time.Millisecond) // let the writer queue up
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		assertErrorIs(t, "SaveCheckpoint queued across Close", err, ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("queued writer did not return after Close")
	}
	<-s.writeSem
}

// TestSQLiteOnDiskEncoding pins the column encodings documented in
// migrations/0001_init.sql.
func TestSQLiteOnDiskEncoding(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	in := fullDownload("encoding")
	in.CompletedAt = time.Time{}
	d := mustCreate(t, s, in)

	var (
		headersType, bodyType, createdType, completedType string
		created                                           int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT typeof(headers), typeof(body), typeof(created_at),
		typeof(completed_at), created_at FROM downloads WHERE id = ?`, int64(d.ID)).
		Scan(&headersType, &bodyType, &createdType, &completedType, &created)
	if err != nil {
		t.Fatalf("query encodings: %v", err)
	}
	if headersType != "text" || bodyType != "blob" || createdType != "integer" || completedType != "null" {
		t.Errorf("column types = headers:%s body:%s created_at:%s completed_at:%s; want text blob integer null",
			headersType, bodyType, createdType, completedType)
	}
	if created != in.CreatedAt.UnixNano() {
		t.Errorf("created_at = %d, want Unix nanoseconds %d", created, in.CreatedAt.UnixNano())
	}
}
