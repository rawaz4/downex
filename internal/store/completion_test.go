package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"downex/internal/model"
)

// TestSQLiteMigratesVersion3 opens a database written by a build that knew migrations
// 1 to 3 only: its rows have no completion dialog, no recorded start and no active
// time, and the new fields round-trip afterwards.
func TestSQLiteMigratesVersion3(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 4 {
		t.Fatalf("%d migrations, want at least 4", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:3]); err != nil {
		t.Fatalf("migrate to version 3: %v", err)
	}
	// A row exactly as the version-3 codec wrote it: a completed media download.
	const v3Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category, kind, media, media_checkpoint)
		VALUES ('https://example.test/old.m3u8', 'GET', NULL, NULL, '', '/tmp/old', 'old.mp4', 42, 0, 4, '', 0,
		'', 4, 0, '', 1000, 2000, 3000, 2, 0, '', 'Video', 1, '{"format":"hls","tracks":[]}', NULL)`
	if _, err := raw.ExecContext(ctx, v3Insert); err != nil {
		t.Fatalf("insert version-3 row: %v", err)
	}
	if v, err := readSchemaVersion(ctx, raw); err != nil || v != 3 {
		t.Fatalf("schema version = %d, %v; want 3", v, err)
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
		t.Fatalf("ListDownloads = %v, %v; want the version-3 row", list, err)
	}
	old := list[0]
	if old.Request.URL != "https://example.test/old.m3u8" || old.State != model.StateCompleted || old.Kind != model.KindMedia ||
		old.Media == nil || old.Media.Format != "hls" || !old.CompletedAt.Equal(time.Unix(0, 3000)) {
		t.Fatalf("migrated row = %+v", old)
	}
	if old.CompletionDialog || !old.StartedAt.IsZero() || old.ActiveMS != 0 {
		t.Fatalf("migrated row has completion fields: dialog %v, started %v, active %d ms",
			old.CompletionDialog, old.StartedAt, old.ActiveMS)
	}
	old.CompletionDialog, old.StartedAt, old.ActiveMS = true, fixtureTime(0), 61_500
	if err := s.UpdateDownload(ctx, &old); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	assertDownload(t, mustGet(t, s, old.ID), old)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	assertDownload(t, mustGet(t, s2, old.ID), old)
}

// TestSQLiteCompletionEncoding pins the column encodings documented in
// migrations/0004_completion.sql.
func TestSQLiteCompletionEncoding(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	full := mustCreate(t, s, fullDownload("dialog"))
	never := fullDownload("never")
	never.CompletionDialog, never.StartedAt, never.ActiveMS = false, time.Time{}, 0
	never = mustCreate(t, s, never)

	for _, tc := range []struct {
		id                model.DownloadID
		dialog            int64
		startedType       string
		started, activeMS int64
	}{
		{full.ID, 1, "integer", full.StartedAt.UnixNano(), full.ActiveMS},
		{never.ID, 0, "null", 0, 0},
	} {
		var (
			dialog, activeMS int64
			startedType      string
			started          sql.NullInt64
		)
		err := s.db.QueryRowContext(ctx, `SELECT completion_dialog, typeof(started_at), started_at, active_ms
			FROM downloads WHERE id = ?`, int64(tc.id)).Scan(&dialog, &startedType, &started, &activeMS)
		if err != nil {
			t.Fatalf("query encodings: %v", err)
		}
		if dialog != tc.dialog || startedType != tc.startedType || started.Int64 != tc.started || activeMS != tc.activeMS {
			t.Errorf("download %d: completion_dialog=%d started_at:%s=%d active_ms=%d; want %d %s=%d %d", tc.id, dialog,
				startedType, started.Int64, activeMS, tc.dialog, tc.startedType, tc.started, tc.activeMS)
		}
	}
	assertDownload(t, mustGet(t, s, never.ID), never)
}
