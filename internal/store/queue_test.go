package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"downex/internal/model"
)

// queueStore returns s as a QueueStore (both implementations are).
func queueStore(t *testing.T, s Store) QueueStore {
	t.Helper()
	qs, ok := s.(QueueStore)
	if !ok {
		t.Fatalf("%T does not implement QueueStore", s)
	}
	return qs
}

func mustQueues(t *testing.T, qs QueueStore) []model.Queue {
	t.Helper()
	list, err := qs.ListQueues(context.Background())
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if list == nil {
		t.Fatal("ListQueues returned nil")
	}
	return list
}

// queueByID returns queue id from ListQueues.
func queueByID(t *testing.T, qs QueueStore, id model.QueueID) model.Queue {
	t.Helper()
	for _, q := range mustQueues(t, qs) {
		if q.ID == id {
			return q
		}
	}
	t.Fatalf("queue %d not listed", id)
	return model.Queue{}
}

func assertOrder(t *testing.T, qs QueueStore, id model.QueueID, want ...model.DownloadID) {
	t.Helper()
	got := queueByID(t, qs, id).DownloadIDs
	if want == nil {
		want = []model.DownloadID{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("queue %d order = %v, want %v", id, got, want)
	}
}

// fullQueue returns a queue with every field set to a distinct non-zero value.
func fullQueue(name string) model.Queue {
	return model.Queue{
		ID:            999, // ignored by CreateQueue
		Name:          name,
		Running:       true,
		MaxConcurrent: 7,
		SpeedLimit:    3 << 20,
		Schedule: model.Schedule{
			Enabled: true, StartMinute: 1380, StopEnabled: true, StopMinute: 345, Weekdays: 0b1010101,
			OnceDate: "2026-12-24", WakeComputer: true, RepeatMinutes: 45,
		},
		PostAction:   model.PostActionHibernate,
		ScheduleMark: fixtureTime(3 * time.Hour),
		DownloadIDs:  []model.DownloadID{12345}, // ignored by writes
	}
}

func equalQueue(a, b model.Queue) bool {
	if !a.ScheduleMark.Equal(b.ScheduleMark) {
		return false
	}
	a.ScheduleMark, b.ScheduleMark = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func testMainQueueExists(t *testing.T, s Store) {
	qs := queueStore(t, s)
	list := mustQueues(t, qs)
	want := model.Queue{ID: model.MainQueueID, Name: MainQueueName, Running: true, DownloadIDs: []model.DownloadID{}}
	if len(list) != 1 || !equalQueue(list[0], want) {
		t.Fatalf("queues of a new store = %+v, want only %+v", list, want)
	}
	if err := qs.DeleteQueue(context.Background(), model.MainQueueID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DeleteQueue(main) = %v, want ErrInvalid", err)
	}
}

func testQueueRoundTrip(t *testing.T, s Store) {
	ctx := context.Background()
	qs := queueStore(t, s)
	in := fullQueue("Night")
	assertNoZeroFields(t, in)
	q := in
	if err := qs.CreateQueue(ctx, &q); err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	if q.ID <= model.MainQueueID {
		t.Fatalf("CreateQueue assigned id %d", q.ID)
	}
	want := in
	want.ID, want.DownloadIDs = q.ID, []model.DownloadID{}
	if got := queueByID(t, qs, q.ID); !equalQueue(got, want) {
		t.Fatalf("created queue = %+v, want %+v", got, want)
	}
	// Every field is replaced, including back to zero values.
	q2 := model.Queue{ID: q.ID, Name: "Renamed", DownloadIDs: []model.DownloadID{1}}
	if err := qs.UpdateQueue(ctx, &q2); err != nil {
		t.Fatalf("UpdateQueue: %v", err)
	}
	want = model.Queue{ID: q.ID, Name: "Renamed", DownloadIDs: []model.DownloadID{}}
	if got := queueByID(t, qs, q.ID); !equalQueue(got, want) {
		t.Fatalf("updated queue = %+v, want %+v", got, want)
	}
	missing := model.Queue{ID: 4242, Name: "x"}
	if err := qs.UpdateQueue(ctx, &missing); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("UpdateQueue(missing) = %v", err)
	}
	if err := qs.DeleteQueue(ctx, 4242); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("DeleteQueue(missing) = %v", err)
	}
	bad := fullQueue("bad")
	bad.ScheduleMark = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := qs.CreateQueue(ctx, &bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreateQueue(unstorable mark) = %v", err)
	}
}

func testCreateAppendsToQueue(t *testing.T, s Store) {
	ctx := context.Background()
	qs := queueStore(t, s)
	night := fullQueue("Night")
	if err := qs.CreateQueue(ctx, &night); err != nil {
		t.Fatal(err)
	}
	a := fullDownload("a")
	a.QueueID = 0 // the main queue
	a = mustCreate(t, s, a)
	if a.QueueID != model.MainQueueID {
		t.Fatalf("download created without a queue has queue %d", a.QueueID)
	}
	b := fullDownload("b")
	b.QueueID = night.ID
	b = mustCreate(t, s, b)
	c := mustCreate(t, s, fullDownload("c"))
	d := fullDownload("d")
	d.QueueID = night.ID
	d = mustCreate(t, s, d)
	assertOrder(t, qs, model.MainQueueID, a.ID, c.ID)
	assertOrder(t, qs, night.ID, b.ID, d.ID)
	if got := mustGet(t, s, b.ID); got.QueueID != night.ID {
		t.Fatalf("stored queue of b = %d", got.QueueID)
	}

	before, _ := s.ListDownloads(ctx)
	e := fullDownload("e")
	e.QueueID = 4242
	if err := s.CreateDownload(ctx, &e); !errors.Is(err, ErrQueueNotFound) || e.ID != 999 {
		t.Fatalf("CreateDownload into a missing queue = %v (id %d)", err, e.ID)
	}
	if after, _ := s.ListDownloads(ctx); len(after) != len(before) {
		t.Fatal("a download was created in a missing queue")
	}
	// Deleted downloads leave their queue.
	if err := s.DeleteDownload(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, qs, model.MainQueueID, c.ID)
}

func testMoveDownloads(t *testing.T, s Store) {
	ctx := context.Background()
	qs := queueStore(t, s)
	night := fullQueue("Night")
	if err := qs.CreateQueue(ctx, &night); err != nil {
		t.Fatal(err)
	}
	var ids []model.DownloadID
	for _, tag := range []string{"1", "2", "3", "4", "5"} {
		ids = append(ids, mustCreate(t, s, fullDownload(tag)).ID)
	}
	main := model.MainQueueID
	// Reorder within a queue: [5, 1] to the front (duplicates count once).
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[4], ids[0], ids[4]}, main, 0); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, qs, main, ids[4], ids[0], ids[1], ids[2], ids[3])
	// Into another queue (appended), then before its first.
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[1]}, night.ID, -1); err != nil {
		t.Fatal(err)
	}
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[3], ids[2]}, night.ID, 0); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, qs, main, ids[4], ids[0])
	assertOrder(t, qs, night.ID, ids[3], ids[2], ids[1])
	// A position past the end appends; an index in the middle inserts.
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[4]}, night.ID, 99); err != nil {
		t.Fatal(err)
	}
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[0]}, night.ID, 2); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, qs, main)
	assertOrder(t, qs, night.ID, ids[3], ids[2], ids[0], ids[1], ids[4])
	if got := mustGet(t, s, ids[0]); got.QueueID != night.ID {
		t.Fatalf("moved download is in queue %d", got.QueueID)
	}
	// Missing downloads or queues change nothing.
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[0], 4242}, main, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MoveDownloads(missing download) = %v", err)
	}
	if err := qs.MoveDownloads(ctx, []model.DownloadID{ids[0]}, 4242, 0); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("MoveDownloads(missing queue) = %v", err)
	}
	assertOrder(t, qs, night.ID, ids[3], ids[2], ids[0], ids[1], ids[4])
}

func testUpdateKeepsQueue(t *testing.T, s Store) {
	ctx := context.Background()
	qs := queueStore(t, s)
	night := fullQueue("Night")
	if err := qs.CreateQueue(ctx, &night); err != nil {
		t.Fatal(err)
	}
	d := mustCreate(t, s, fullDownload("keep"))
	stale := d // a run's copy read before the move
	if err := qs.MoveDownloads(ctx, []model.DownloadID{d.ID}, night.ID, -1); err != nil {
		t.Fatal(err)
	}
	stale.Filename = "written by a run.zip"
	if err := s.UpdateDownload(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckpoint(ctx, &stale, fixtureSegments(1, 5)); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, s, d.ID)
	if got.QueueID != night.ID || got.Filename != stale.Filename {
		t.Fatalf("after writes of a stale copy: queue %d, name %q", got.QueueID, got.Filename)
	}
	assertOrder(t, qs, night.ID, d.ID)
}

func testDeleteQueue(t *testing.T, s Store) {
	ctx := context.Background()
	qs := queueStore(t, s)
	night := fullQueue("Night")
	if err := qs.CreateQueue(ctx, &night); err != nil {
		t.Fatal(err)
	}
	m1 := mustCreate(t, s, fullDownload("m1"))
	var nightIDs []model.DownloadID
	for _, tag := range []string{"n1", "n2", "n3"} {
		d := fullDownload(tag)
		d.QueueID = night.ID
		nightIDs = append(nightIDs, mustCreate(t, s, d).ID)
	}
	m2 := mustCreate(t, s, fullDownload("m2"))
	if err := qs.MoveDownloads(ctx, []model.DownloadID{nightIDs[2]}, night.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := qs.DeleteQueue(ctx, night.ID); err != nil {
		t.Fatalf("DeleteQueue: %v", err)
	}
	assertOrder(t, qs, model.MainQueueID, m1.ID, m2.ID, nightIDs[2], nightIDs[0], nightIDs[1])
	if len(mustQueues(t, qs)) != 1 {
		t.Fatal("deleted queue is still listed")
	}
	if got := mustGet(t, s, nightIDs[0]); got.QueueID != model.MainQueueID {
		t.Fatalf("download of the deleted queue is in queue %d", got.QueueID)
	}
	// Appending to the main queue still goes after them.
	m3 := mustCreate(t, s, fullDownload("m3"))
	assertOrder(t, qs, model.MainQueueID, m1.ID, m2.ID, nightIDs[2], nightIDs[0], nightIDs[1], m3.ID)
}

func testQueueIDsNeverReused(t *testing.T, s Store) {
	ctx := context.Background()
	qs := queueStore(t, s)
	a, b := fullQueue("a"), fullQueue("b")
	if err := qs.CreateQueue(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if err := qs.DeleteQueue(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := qs.CreateQueue(ctx, &b); err != nil {
		t.Fatal(err)
	}
	if b.ID <= a.ID {
		t.Fatalf("queue id %d reused or decreased after deleting %d", b.ID, a.ID)
	}
}

// TestSQLiteMigratesVersion4 opens a database written by a build that knew migrations 1
// to 4 only (Downex 0.3.x): its downloads all join the main queue in their id order, the
// main queue runs, and queues work afterwards.
func TestSQLiteMigratesVersion4(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downex.db")
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 5 {
		t.Fatalf("%d migrations, want at least 5", len(ms))
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw, ms[:4]); err != nil {
		t.Fatalf("migrate to version 4: %v", err)
	}
	// Rows exactly as the version-4 codec wrote them, one of them deleted (ids keep
	// increasing) so that ids have a gap.
	const v4Insert = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename, size, mode,
		state, etag, etag_strong, last_modified, max_connections, speed_limit, error, created_at, updated_at,
		completed_at, source, expected_size, mime, category, kind, media, media_checkpoint, completion_dialog,
		started_at, active_ms)
		VALUES (?, 'GET', NULL, NULL, '', '/tmp/old', ?, 42, 1, ?, '', 0, '', 4, 0, '', 1000, 2000, NULL, 1, 0,
		'', '', 0, NULL, NULL, 0, NULL, 0)`
	for i, st := range []model.State{model.StateQueued, model.StatePaused, model.StateCompleted, model.StateQueued} {
		if _, err := raw.ExecContext(ctx, v4Insert, "https://example.test/"+string(rune('a'+i)), "f"+string(rune('a'+i)), int64(st)); err != nil {
			t.Fatalf("insert version-4 row: %v", err)
		}
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM downloads WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s := openTestSQLite(t, path)
	if v, err := readSchemaVersion(ctx, s.db); err != nil || v != len(ms) {
		t.Fatalf("schema version after open = %d, %v; want %d", v, err, len(ms))
	}
	list := mustQueues(t, s)
	want := model.Queue{ID: model.MainQueueID, Name: MainQueueName, Running: true, DownloadIDs: []model.DownloadID{1, 3, 4}}
	if len(list) != 1 || !equalQueue(list[0], want) {
		t.Fatalf("queues after the migration = %+v, want %+v", list, want)
	}
	for _, id := range want.DownloadIDs {
		if d := mustGet(t, s, id); d.QueueID != model.MainQueueID || d.Request.URL == "" {
			t.Fatalf("migrated download %d = %+v", id, d)
		}
	}
	// New downloads and queues follow.
	n := mustCreate(t, s, fullDownload("new"))
	assertOrder(t, s, model.MainQueueID, 1, 3, 4, n.ID)
	q := fullQueue("Night")
	if err := s.CreateQueue(ctx, &q); err != nil || q.ID != 2 {
		t.Fatalf("CreateQueue after the migration: id %d, %v", q.ID, err)
	}
	if err := s.MoveDownloads(ctx, []model.DownloadID{4, 1}, q.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openTestSQLite(t, path)
	assertOrder(t, s2, model.MainQueueID, 3, n.ID)
	assertOrder(t, s2, q.ID, 4, 1)
}

// TestSQLiteQueueEncoding pins the column encodings documented in
// migrations/0005_queues.sql.
func TestSQLiteQueueEncoding(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "downex.db"))
	q := fullQueue("Night")
	if err := s.CreateQueue(ctx, &q); err != nil {
		t.Fatal(err)
	}
	var (
		running, enabled, stop, wake, action, days int64
		markType                                   string
		mark                                       int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT running, schedule_enabled, stop_enabled, wake_computer, post_action,
		weekdays, typeof(schedule_mark), schedule_mark FROM queues WHERE id = ?`, int64(q.ID)).Scan(
		&running, &enabled, &stop, &wake, &action, &days, &markType, &mark)
	if err != nil {
		t.Fatal(err)
	}
	if running != 1 || enabled != 1 || stop != 1 || wake != 1 || action != int64(model.PostActionHibernate) ||
		days != 0b1010101 || markType != "integer" || mark != q.ScheduleMark.UnixNano() {
		t.Fatalf("encodings: running=%d enabled=%d stop=%d wake=%d action=%d weekdays=%d mark=%s:%d", running, enabled,
			stop, wake, action, days, markType, mark)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT typeof(schedule_mark) FROM queues WHERE id = 1`).Scan(&markType); err != nil || markType != "null" {
		t.Fatalf("main queue schedule_mark is %s (%v), want null", markType, err)
	}
}

func TestPlaceDownloads(t *testing.T) {
	ids := func(v ...model.DownloadID) []model.DownloadID { return v }
	for _, tc := range []struct {
		current, moved []model.DownloadID
		pos            int
		want           []model.DownloadID
	}{
		{ids(1, 2, 3), ids(3), 0, ids(3, 1, 2)},
		{ids(1, 2, 3), ids(1), -1, ids(2, 3, 1)},
		{ids(1, 2, 3), ids(1), 1, ids(2, 1, 3)},
		{ids(1, 2, 3), ids(9, 8), 1, ids(1, 9, 8, 2, 3)},
		{ids(1, 2, 3), ids(2), 5, ids(1, 3, 2)},
		{nil, ids(4), 0, ids(4)},
	} {
		if got := model.PlaceDownloads(tc.current, tc.moved, tc.pos); !slices.Equal(got, tc.want) {
			t.Errorf("PlaceDownloads(%v, %v, %d) = %v, want %v", tc.current, tc.moved, tc.pos, got, tc.want)
		}
	}
}
