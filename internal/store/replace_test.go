package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"downex/internal/model"
)

// TestSQLiteMigratesVersion6 opens a database written by a build that knew migrations 1
// to 6 only: its rows do not replace their file, and the flag round-trips afterwards
// (migrations/0007_replace.sql).
func TestSQLiteMigratesVersion6(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 7 {
		t.Fatalf("%d migrations, want at least 7", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:6]); err != nil {
		t.Fatalf("migrate to version 6: %v", err)
	}
	// A row exactly as the version-6 codec wrote it: a failed file download.
	const v6Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category, kind, media, media_checkpoint, completion_dialog,
		started_at, active_ms, queue_id, queue_pos)
		VALUES ('https://example.test/old.bin', 'GET', NULL, NULL, '', '/tmp/old', 'old.bin', 42, 1, 5, '', 0,
		'', 4, 0, 'boom', 1000, 2000, NULL, 2, 0, '', '', 0, NULL, NULL, 0, NULL, 0, 1, 1)`
	if _, err := raw.ExecContext(ctx, v6Insert); err != nil {
		t.Fatalf("insert version-6 row: %v", err)
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
		t.Fatalf("ListDownloads = %v, %v; want the version-6 row", list, err)
	}
	old := list[0]
	if old.Filename != "old.bin" || old.State != model.StateFailed || old.ReplaceFile {
		t.Fatalf("migrated row = %+v; want old.bin, failed, not replacing", old)
	}
	old.ReplaceFile = true
	if err := s.UpdateDownload(ctx, &old); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	var stored int64
	if err := s.db.QueryRowContext(ctx, `SELECT replace_file FROM downloads WHERE id = ?`, int64(old.ID)).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("replace_file = %d, %v; want 1", stored, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	assertDownload(t, mustGet(t, s2, old.ID), old)
	old.ReplaceFile = false
	if err := s2.SaveCheckpoint(ctx, &old, nil); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	assertDownload(t, mustGet(t, s2, old.ID), old)
}
