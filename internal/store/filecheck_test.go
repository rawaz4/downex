package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"downex/internal/model"
)

// TestSQLiteMigratesVersion7 opens a database written by a build that knew migrations 1
// to 7 only: its rows have no served type, no hidden controls and no file check
// (migrations/0008_filecheck.sql); all three round-trip afterwards.
func TestSQLiteMigratesVersion7(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 8 {
		t.Fatalf("%d migrations, want at least 8", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:7]); err != nil {
		t.Fatalf("migrate to version 7: %v", err)
	}
	// A row exactly as the version-7 codec wrote it: a completed file download.
	const v7Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category, kind, media, media_checkpoint, completion_dialog,
		started_at, active_ms, queue_id, queue_pos, replace_file)
		VALUES ('https://example.test/old.mp4', 'GET', NULL, NULL, '', '/tmp/old', 'old.mp4', 42, 1, 4, '', 0,
		'', 4, 0, '', 1000, 2000, 3000, 2, 0, '', '', 0, NULL, NULL, 0, NULL, 0, 1, 1, 0)`
	if _, err := raw.ExecContext(ctx, v7Insert); err != nil {
		t.Fatalf("insert version-7 row: %v", err)
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
		t.Fatalf("ListDownloads = %v, %v; want the version-7 row", list, err)
	}
	old := list[0]
	if old.ServedType != "" || old.NameControls || old.FileCheck != nil || old.State != model.StateCompleted {
		t.Fatalf("migrated row = %+v; want no served type, controls or check", old)
	}
	old.ServedType, old.NameControls = "text/html", true
	if err := s.UpdateDownload(ctx, &old); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	fc := &model.FileCheck{Level: model.FileCheckCaution, Stage: model.FileCheckStageFinal, Reason: "web_page",
		Content: "html", Claim: "mp4", Served: "text/html"}
	if err := s.SetFileCheck(ctx, old.ID, fc); err != nil {
		t.Fatalf("SetFileCheck: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	got := mustGet(t, s2, old.ID)
	if got.ServedType != "text/html" || !got.NameControls || got.FileCheck == nil || *got.FileCheck != *fc {
		t.Fatalf("after reopen = %+v / %+v", got, got.FileCheck)
	}
}

// TestSQLiteInvalidFileCheckReadsAsNone: the check is advisory; a damaged value never
// makes the record unreadable.
func TestSQLiteInvalidFileCheckReadsAsNone(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	d := mustCreate(t, s, fullDownload("bad"))
	if _, err := s.db.ExecContext(ctx, `UPDATE downloads SET file_check = '{not json' WHERE id = ?`, int64(d.ID)); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, d.ID); got.FileCheck != nil {
		t.Fatalf("file check = %+v, want none", got.FileCheck)
	}
}
