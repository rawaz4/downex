package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"downex/internal/model"
)

// testMediaRoundTrip covers the media fields of migration 3: a description with and
// without checkpoint, nil and empty slices kept apart, replacement and removal by
// UpdateDownload and SaveCheckpoint, deep copies, and non-finite numbers refused.
func testMediaRoundTrip(t *testing.T, s Store) {
	ctx := context.Background()

	// A file download has no media description.
	file := fullDownload("file")
	file.Kind, file.Media = model.KindFile, nil
	file = mustCreate(t, s, file)
	assertDownload(t, mustGet(t, s, file.ID), file)

	// Nil and empty are preserved: tracks, checkpoint tracks and bitmaps.
	for name, m := range map[string]*model.Media{
		"no checkpoint":    {Format: "dash", Tracks: []model.MediaTrack{{ID: "a", Kind: "audio"}}},
		"nil tracks":       {Format: "hls"},
		"empty tracks":     {Format: "hls", Tracks: []model.MediaTrack{}},
		"empty checkpoint": {Format: "hls", Checkpoint: &model.MediaCheckpoint{}},
		"empty bitmaps": {Format: "hls", Checkpoint: &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
			{TrackID: "x", Total: 0, Done: []byte{}}, {TrackID: "y", Total: 3, Done: nil},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			d := mustCreate(t, s, model.Download{Request: model.Request{URL: "https://example.test/m.m3u8"}, Size: -1,
				Kind: model.KindMedia, Media: m})
			got := mustGet(t, s, d.ID)
			assertDownload(t, got, d)
			if (got.Media.Tracks == nil) != (m.Tracks == nil) || (got.Media.Checkpoint == nil) != (m.Checkpoint == nil) {
				t.Fatalf("nil-ness changed: got %+v, want %+v", got.Media, m)
			}
		})
	}

	d := mustCreate(t, s, fullDownload("media"))
	want := d
	want.Media = d.Media.Clone()

	// Writes and reads never alias the caller's media.
	d.Media.Tracks[0].Name = "mutated"
	d.Media.Checkpoint.Tracks[0].Done[0] = 0
	assertDownload(t, mustGet(t, s, want.ID), want)
	got := mustGet(t, s, want.ID)
	got.Media.Tracks[1].ID = "mutated"
	got.Media.Checkpoint.Tracks[1].Done[0] = 0xee
	assertDownload(t, mustGet(t, s, want.ID), want)

	// UpdateDownload and SaveCheckpoint replace the description and the checkpoint.
	upd := want
	upd.Media = want.Media.Clone()
	upd.Media.FragmentsDone, upd.Media.BytesDone = 1200, 1<<33
	upd.Media.Checkpoint.Tracks[0].Done = []byte{0xff, 0xff, 0xff, 0xff}
	upd.Media.SelectedAudio = ""
	if err := s.UpdateDownload(ctx, &upd); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	assertDownload(t, mustGet(t, s, upd.ID), upd)
	upd.Media = upd.Media.Clone()
	upd.Media.Checkpoint = nil
	upd.State = model.StateCompleted
	if err := s.SaveCheckpoint(ctx, &upd, nil); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	assertDownload(t, mustGet(t, s, upd.ID), upd)
	upd.Kind, upd.Media = model.KindFile, nil
	if err := s.UpdateDownload(ctx, &upd); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	assertDownload(t, mustGet(t, s, upd.ID), upd)

	// JSON cannot hold NaN or infinities: refused by both stores alike.
	base := mustCreate(t, s, fullDownload("finite"))
	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		bad := fullDownload("nan")
		bad.Media.Tracks[0].FrameRate = v
		assertErrorIs(t, "CreateDownload(non-finite frame rate)", s.CreateDownload(ctx, &bad), ErrInvalid)
		stored := mustGet(t, s, base.ID)
		stored.Media.Tracks[0].FrameRate = v
		assertErrorIs(t, "UpdateDownload(non-finite frame rate)", s.UpdateDownload(ctx, &stored), ErrInvalid)
	}
	assertDownload(t, mustGet(t, s, base.ID), base)
}

// TestSQLiteMigratesVersion2 opens a database written by a build that knew migrations
// 1 and 2 only: its rows become file downloads without media, and the media fields
// round-trip afterwards.
func TestSQLiteMigratesVersion2(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) < 3 {
		t.Fatalf("%d migrations, want at least 3", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:2]); err != nil {
		t.Fatalf("migrate to version 2: %v", err)
	}
	// A row exactly as the version-2 codec wrote it.
	const v2Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category) VALUES ('https://example.test/old.iso', 'GET',
		'{"Cookie":["a=1"]}', NULL, '', '/tmp/old', 'old.iso', 42, 1, 3, '"e"', 1, '', 4, 0, '', 1000, 2000, NULL,
		3, 40, 'application/x-iso9660-image', 'Archives')`
	if _, err := raw.ExecContext(ctx, v2Insert); err != nil {
		t.Fatalf("insert version-2 row: %v", err)
	}
	if v, err := readSchemaVersion(ctx, raw); err != nil || v != 2 {
		t.Fatalf("schema version = %d, %v; want 2", v, err)
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
		t.Fatalf("ListDownloads = %v, %v; want the version-2 row", list, err)
	}
	old := list[0]
	if old.Request.URL != "https://example.test/old.iso" || old.State != model.StatePaused || old.Size != 42 ||
		old.Source != model.SourceBrowser || old.Category != "Archives" || old.Request.Headers.Get("Cookie") != "a=1" ||
		old.Kind != model.KindFile || old.Media != nil {
		t.Fatalf("migrated row = %+v", old)
	}
	old.Kind, old.Media = model.KindMedia, fullMedia("migrated")
	if err := s.SaveCheckpoint(ctx, &old, nil); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	assertDownload(t, mustGet(t, s, old.ID), old)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	assertDownload(t, mustGet(t, s2, old.ID), old)
}

// TestSQLiteMediaEncoding pins the column encodings documented in
// migrations/0003_media.sql.
func TestSQLiteMediaEncoding(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	media := mustCreate(t, s, fullDownload("media"))
	file := fullDownload("file")
	file.Kind, file.Media = model.KindFile, nil
	file = mustCreate(t, s, file)
	noCP := fullDownload("nocp")
	noCP.Media.Checkpoint = nil
	noCP = mustCreate(t, s, noCP)

	for _, tc := range []struct {
		id                       model.DownloadID
		kind                     int64
		mediaType, checkpointTyp string
	}{
		{media.ID, 1, "text", "blob"},
		{file.ID, 0, "null", "null"},
		{noCP.ID, 1, "text", "null"},
	} {
		var (
			kind                int64
			mediaType, cpType   string
			mediaJSON           sql.NullString
			checkpoint          []byte
			selectedVideo, fmtS string
		)
		err := s.db.QueryRowContext(ctx, `SELECT kind, typeof(media), typeof(media_checkpoint), media, media_checkpoint
			FROM downloads WHERE id = ?`, int64(tc.id)).Scan(&kind, &mediaType, &cpType, &mediaJSON, &checkpoint)
		if err != nil {
			t.Fatalf("query encodings: %v", err)
		}
		if kind != tc.kind || mediaType != tc.mediaType || cpType != tc.checkpointTyp {
			t.Errorf("download %d: kind=%d media:%s media_checkpoint:%s; want %d %s %s", tc.id, kind, mediaType, cpType,
				tc.kind, tc.mediaType, tc.checkpointTyp)
		}
		if !mediaJSON.Valid {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(mediaJSON.String), &m); err != nil {
			t.Fatalf("media column is not JSON: %v", err)
		}
		if _, ok := m["checkpoint"]; ok {
			t.Errorf("media column holds the checkpoint too: %s", mediaJSON.String)
		}
		selectedVideo, _ = m["selectedVideo"].(string)
		fmtS, _ = m["format"].(string)
		if fmtS != "hls" || selectedVideo == "" {
			t.Errorf("media column = %s", mediaJSON.String)
		}
		if checkpoint != nil {
			var cp model.MediaCheckpoint
			if err := json.Unmarshal(checkpoint, &cp); err != nil || len(cp.Tracks) != 2 {
				t.Errorf("media_checkpoint = %s (%v)", checkpoint, err)
			}
		}
	}

	// A corrupt description is an error, not a silently missing one.
	if _, err := s.db.ExecContext(ctx, "UPDATE downloads SET media = '{nope' WHERE id = ?", int64(media.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDownload(ctx, media.ID); err == nil {
		t.Fatal("GetDownload on a corrupt media column succeeded")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE downloads SET media = NULL WHERE id = ?", int64(media.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDownload(ctx, media.ID); err == nil {
		t.Fatal("GetDownload of a checkpoint without media succeeded")
	}
}
