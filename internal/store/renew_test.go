package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"downex/internal/model"
)

// TestSQLiteMigratesVersion8 opens a database written by a build that knew migrations 1
// to 8 only: its rows have no page address and no remote name
// (migrations/0009_renew.sql); both round-trip afterwards.
func TestSQLiteMigratesVersion8(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 9 {
		t.Fatalf("%d migrations, want at least 9", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:8]); err != nil {
		t.Fatalf("migrate to version 8: %v", err)
	}
	// A row exactly as the version-8 codec wrote it: a failed ranged file download.
	const v8Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category, kind, media, media_checkpoint, completion_dialog,
		started_at, active_ms, queue_id, queue_pos, replace_file, served_type, name_controls, file_check)
		VALUES ('https://example.test/old.zip', 'GET', NULL, NULL, '', '/tmp/old', 'old.zip', 42, 1, 5, '', 0,
		'', 4, 0, 'HTTP 403', 1000, 2000, NULL, 2, 0, '', '', 0, NULL, NULL, 0, NULL, 0, 1, 1, 0, '', 0, NULL)`
	if _, err := raw.ExecContext(ctx, v8Insert); err != nil {
		t.Fatalf("insert version-8 row: %v", err)
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
		t.Fatalf("ListDownloads = %v, %v; want the version-8 row", list, err)
	}
	old := list[0]
	if old.PageURL != "" || old.RemoteName != "" || old.State != model.StateFailed {
		t.Fatalf("migrated row = %+v; want no page and no remote name", old)
	}
	old.PageURL, old.RemoteName = "https://example.test/files", "old.zip"
	if err := s.UpdateDownload(ctx, &old); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	got := mustGet(t, s2, old.ID)
	if got.PageURL != "https://example.test/files" || got.RemoteName != "old.zip" {
		t.Fatalf("after reopen = %+v", got)
	}
}

// TestExpiredLinkFieldLimits: both stores refuse a page address that is not http(s) or
// longer than 32 KiB, and a remote name that is no sanitised file name.
func TestExpiredLinkFieldLimits(t *testing.T) {
	ctx := context.Background()
	for name, s := range map[string]Store{
		"memory": NewMemory(),
		"sqlite": openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db")),
	} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				page     string
				remote   string
				accepted bool
			}{
				{"none", "", "", true},
				{"http page", "http://example.test/a", "a.zip", true},
				{"https page, upper case", "HTTPS://example.test/a", "a b (1).zip", true},
				{"longest page", "https://example.test/" + strings.Repeat("a", MaxPageURLBytes-len("https://example.test/")), "", true},
				{"page too long", "https://example.test/" + strings.Repeat("a", MaxPageURLBytes), "", false},
				{"file page", "file:///etc/passwd", "", false},
				{"script page", "javascript:alert(1)", "", false},
				{"remote name with a slash", "", "a/b.zip", false},
				{"remote name with a backslash", "", `a\b.zip`, false},
				{"remote name with NUL", "", "a\x00.zip", false},
				{"remote name too long", "", strings.Repeat("n", MaxRemoteNameBytes+1), false},
			} {
				d := fullDownload("limits")
				d.PageURL, d.RemoteName = tc.page, tc.remote
				err := s.CreateDownload(ctx, &d)
				if tc.accepted && err != nil {
					t.Errorf("%s: CreateDownload = %v, want accepted", tc.name, err)
				}
				if !tc.accepted && !errors.Is(err, ErrInvalid) {
					t.Errorf("%s: CreateDownload = %v, want ErrInvalid", tc.name, err)
				}
			}
		})
	}
}
