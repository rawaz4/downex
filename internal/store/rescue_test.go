package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestSQLiteMigratesVersion9 opens a database written by a build that knew migrations 1
// to 9 only: its rows read no rescued bytes (migrations/0010_rescue.sql), and the field
// round-trips afterwards.
func TestSQLiteMigratesVersion9(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 10 {
		t.Fatalf("%d migrations, want at least 10", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:9]); err != nil {
		t.Fatalf("migrate to version 9: %v", err)
	}
	// A row exactly as the version-9 codec wrote it: a paused ranged file download.
	const v9Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category, kind, media, media_checkpoint, completion_dialog,
		started_at, active_ms, queue_id, queue_pos, replace_file, served_type, name_controls, file_check,
		page_url, remote_name)
		VALUES ('https://example.test/old.iso', 'GET', NULL, NULL, '', '/tmp/old', 'old.iso', 4096, 1, 4, '', 0,
		'', 4, 0, '', 1000, 2000, NULL, 2, 0, '', '', 0, NULL, NULL, 0, NULL, 0, 1, 1, 0, '', 0, NULL, '', '')`
	if _, err := raw.ExecContext(ctx, v9Insert); err != nil {
		t.Fatalf("insert version-9 row: %v", err)
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
		t.Fatalf("ListDownloads = %v, %v; want the version-9 row", list, err)
	}
	old := list[0]
	if old.RescuedBytes != 0 {
		t.Fatalf("migrated row has %d rescued bytes, want 0", old.RescuedBytes)
	}
	old.RescuedBytes = 1024
	if err := s.UpdateDownload(ctx, &old); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	if got := mustGet(t, s2, old.ID); got.RescuedBytes != 1024 {
		t.Fatalf("after reopen RescuedBytes = %d, want 1024", got.RescuedBytes)
	}
}

// TestRescuedBytesLimits: both stores refuse negative rescued bytes and more rescued
// bytes than a known size.
func TestRescuedBytesLimits(t *testing.T) {
	ctx := context.Background()
	for name, s := range map[string]Store{
		"memory": NewMemory(),
		"sqlite": openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db")),
	} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				size     int64
				rescued  int64
				accepted bool
			}{
				{"none", 100, 0, true},
				{"part", 100, 40, true},
				{"all", 100, 100, true},
				{"size unknown", -1, 40, true},
				{"negative", 100, -1, false},
				{"beyond the size", 100, 101, false},
			} {
				d := fullDownload("rescue")
				d.Size, d.RescuedBytes = tc.size, tc.rescued
				err := s.CreateDownload(ctx, &d)
				if tc.accepted && err != nil {
					t.Errorf("%s: CreateDownload = %v, want accepted", tc.name, err)
				}
				if !tc.accepted && !errors.Is(err, ErrInvalid) {
					t.Errorf("%s: CreateDownload = %v, want ErrInvalid", tc.name, err)
				}
			}
			// UpdateDownload validates as well.
			d := fullDownload("rescue-update")
			d.Size = 10
			if err := s.CreateDownload(ctx, &d); err != nil {
				t.Fatal(err)
			}
			d.RescuedBytes = 11
			if err := s.UpdateDownload(ctx, &d); !errors.Is(err, ErrInvalid) {
				t.Errorf("UpdateDownload with 11 of 10 bytes rescued = %v, want ErrInvalid", err)
			}
		})
	}
}
