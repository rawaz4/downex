package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

// storeFactory returns a fresh, empty store. The factory registers Close as a
// test cleanup; closing twice must be harmless.
type storeFactory func(t *testing.T) Store

// runConformance runs the behaviour every Store implementation must share.
func runConformance(t *testing.T, newStore storeFactory) {
	tests := []struct {
		name string
		fn   func(t *testing.T, s Store)
	}{
		{"CreateGetRoundTripsEveryField", testCreateGetRoundTrip},
		{"CreateStampsZeroTimes", testCreateStampsZeroTimes},
		{"CreateAssignsIncreasingNeverReusedIDs", testCreateAssignsIDs},
		{"ListOrderedByID", testListOrderedByID},
		{"UpdateReplacesRecord", testUpdateReplacesRecord},
		{"DeleteCascadesSegments", testDeleteCascades},
		{"CheckpointReplacesSegmentsAndRecord", testCheckpointReplaces},
		{"CheckpointRejectsDuplicateSegmentIDs", testCheckpointRejectsDuplicates},
		{"NotFound", testNotFound},
		{"NilAndEmptyPreserved", testNilAndEmptyPreserved},
		{"NoAliasing", testNoAliasing},
		{"InvalidInput", testInvalidInput},
		{"CanceledContext", testCanceledContext},
		{"Closed", testClosed},
		{"ConcurrentCheckpoints", testConcurrentCheckpoints},
		{"MediaRoundTrips", testMediaRoundTrip},
		{"MainQueueExists", testMainQueueExists},
		{"QueueRoundTripsEveryField", testQueueRoundTrip},
		{"CreateDownloadAppendsToQueue", testCreateAppendsToQueue},
		{"MoveDownloadsOrdersQueues", testMoveDownloads},
		{"UpdateKeepsQueueMembership", testUpdateKeepsQueue},
		{"DeleteQueueMovesDownloadsToMain", testDeleteQueue},
		{"QueueIDsNeverReused", testQueueIDsNeverReused},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStore(t))
		})
	}
}

// fixtureTime returns a timestamp in the canonical form both stores read back.
func fixtureTime(offset time.Duration) time.Time {
	return time.Date(2026, 10, 19, 12, 30, 45, 123456789, time.UTC).Add(offset)
}

// fullDownload returns a download with every field set to a distinct non-zero
// value, so a column mix-up or a dropped field fails the round trip.
func fullDownload(tag string) model.Download {
	return model.Download{
		ID: 999, // ignored by CreateDownload
		Request: model.Request{
			URL:    "https://example.test/files/" + tag + ".zip",
			Method: "POST",
			Headers: http.Header{
				"Cookie":     {"a=1", "b=2"},
				"Referer":    {"https://example.test/page"},
				"User-Agent": {"Downex-Test/" + tag},
			},
			Body: []byte("q=" + tag + "\x00\xff"),
		},
		FinalURL: "https://cdn.example.test/" + tag + ".zip",
		Dir:      `C:\Downloads\` + tag,
		Filename: tag + " (1).zip",
		Size:     1<<40 + 7,
		Mode:     model.ModeRanged,
		State:    model.StatePaused,
		Validators: model.Validators{
			ETag:         `"etag-` + tag + `"`,
			StrongETag:   true,
			LastModified: "Mon, 19 Oct 2026 12:00:00 GMT",
		},
		MaxConnections: 16,
		SpeedLimit:     512 * 1024,
		Error:          "previous attempt: connection reset (" + tag + ")",
		CreatedAt:      fixtureTime(0),
		UpdatedAt:      fixtureTime(time.Minute),
		CompletedAt:    fixtureTime(time.Hour),
		Source:         model.SourceBrowser,
		ExpectedSize:   1<<40 + 5,
		MIME:           "application/zip",
		Category:       "Archives (" + tag + ")",
		Kind:           model.KindMedia,
		Media:          fullMedia(tag),
		// Migration 4: the download complete dialog.
		CompletionDialog: true,
		StartedAt:        fixtureTime(2 * time.Second),
		ActiveMS:         3_600_000*25 + 1,
		// Migration 5: the only queue every store has.
		QueueID: model.MainQueueID,
		// Migration 7: a restart that replaces the completed file.
		ReplaceFile: true,
	}
}

// fullMedia returns a media description with every field set to a distinct non-zero
// value, including a checkpoint.
func fullMedia(tag string) *model.Media {
	return &model.Media{
		Origin: "site",
		Site:   "Site " + tag,
		Format: "hls",
		Tracks: []model.MediaTrack{
			{ID: "v0-" + tag, Kind: "video", Bandwidth: 5_000_000, Width: 1920, Height: 1080, FrameRate: 29.97,
				Codecs: "avc1.640028", Language: "und", Name: "1080p " + tag, Key: "hkey-" + tag, Size: 412_000_000,
				Ext: "mp4"},
			{ID: "r1", Kind: "audio", Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en", Name: "English",
				Size: 4_000_000, SizeApprox: true, Ext: "m4a"},
		},
		SelectedVideo:  "v0-" + tag,
		SelectedAudio:  "r1",
		Duration:       95*time.Minute + 1500*time.Millisecond,
		Container:      "mp4",
		Title:          "Title " + tag + " ü",
		PageURL:        "https://example.test/watch/" + tag,
		FragmentsTotal: 1200,
		FragmentsDone:  321,
		BytesDone:      987_654_321,
		Checkpoint: &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
			{TrackID: "v0-" + tag, Total: 600, Done: []byte{0xff, 0x0f, 0x00, 0x81}, Key: "hkey-" + tag,
				ChunkBytes: 10 << 20, Partial: map[int]int64{4: 1 << 20, 30: 77}},
			{TrackID: "r1", Total: 600, Done: []byte{0x01}},
		}, DurableBytes: 123_456_789, BytesTotal: 2_000_000_001},
	}
}

func fixtureSegments(n int, durable int64) []model.Segment {
	segs := make([]model.Segment, n)
	for i := range segs {
		start := int64(i) * 1_000_000
		segs[i] = model.Segment{ID: i + 1, Start: start, End: start + 999_999, Durable: durable}
	}
	return segs
}

func mustCreate(t *testing.T, s Store, d model.Download) model.Download {
	t.Helper()
	if err := s.CreateDownload(context.Background(), &d); err != nil {
		t.Fatalf("CreateDownload: %v", err)
	}
	return d
}

func mustGet(t *testing.T, s Store, id model.DownloadID) model.Download {
	t.Helper()
	d, err := s.GetDownload(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDownload(%d): %v", id, err)
	}
	return *d
}

func mustLoad(t *testing.T, s Store, id model.DownloadID) []model.Segment {
	t.Helper()
	segs, err := s.LoadSegments(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadSegments(%d): %v", id, err)
	}
	return segs
}

// assertDownload compares records, treating timestamps as instants.
func assertDownload(t *testing.T, got, want model.Download) {
	t.Helper()
	if !equalDownload(got, want) {
		t.Fatalf("download mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func equalDownload(a, b model.Download) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) || !a.CompletedAt.Equal(b.CompletedAt) ||
		!a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	a.CreatedAt, a.UpdatedAt, a.CompletedAt, a.StartedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt, b.CompletedAt, b.StartedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func assertSegments(t *testing.T, got, want []model.Segment) {
	t.Helper()
	if got == nil {
		t.Fatalf("segments: got nil slice, want non-nil")
	}
	if !slices.Equal(got, want) {
		t.Fatalf("segments mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func assertErrorIs(t *testing.T, op string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got error %v, want errors.Is(%v)", op, err, target)
	}
}

// assertNoZeroFields fails when a fixture leaves a field at its zero value, so
// a field added to the model later cannot silently escape the round-trip test.
func assertNoZeroFields(t *testing.T, v any) {
	t.Helper()
	timeType := reflect.TypeFor[time.Time]()
	var walk func(path string, rv reflect.Value)
	walk = func(path string, rv reflect.Value) {
		if rv.Kind() == reflect.Pointer && !rv.IsNil() && rv.Elem().Kind() == reflect.Struct {
			walk(path, rv.Elem())
			return
		}
		if rv.Kind() == reflect.Struct && rv.Type() != timeType {
			for i := range rv.NumField() {
				walk(path+"."+rv.Type().Field(i).Name, rv.Field(i))
			}
			return
		}
		if rv.IsZero() {
			t.Errorf("fixture field %s is zero; give it a distinct value", path)
		}
	}
	walk(reflect.TypeOf(v).Name(), reflect.ValueOf(v))
}

func testCreateGetRoundTrip(t *testing.T, s Store) {
	in := fullDownload("roundtrip")
	assertNoZeroFields(t, in)
	assertNoZeroFields(t, fixtureSegments(2, 1)[1])

	created := mustCreate(t, s, in)
	if created.ID <= 0 || created.ID == in.ID {
		t.Fatalf("CreateDownload assigned ID %d; want a new positive ID", created.ID)
	}
	want := in
	want.ID = created.ID
	assertDownload(t, created, want) // non-zero timestamps are kept as given
	assertDownload(t, mustGet(t, s, created.ID), want)
	assertSegments(t, mustLoad(t, s, created.ID), []model.Segment{})
}

func testCreateStampsZeroTimes(t *testing.T, s Store) {
	in := fullDownload("stamp")
	in.CreatedAt, in.UpdatedAt, in.CompletedAt = time.Time{}, time.Time{}, time.Time{}

	before := time.Now()
	d := mustCreate(t, s, in)
	after := time.Now()

	for name, ts := range map[string]time.Time{"CreatedAt": d.CreatedAt, "UpdatedAt": d.UpdatedAt} {
		if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
			t.Errorf("%s = %v, want between %v and %v", name, ts, before, after)
		}
	}
	if !d.CompletedAt.IsZero() {
		t.Errorf("CompletedAt = %v, want zero", d.CompletedAt)
	}
	assertDownload(t, mustGet(t, s, d.ID), d)

	// Only zero timestamps are filled in.
	in2 := fullDownload("stamp2")
	in2.UpdatedAt = time.Time{}
	d2 := mustCreate(t, s, in2)
	if !d2.CreatedAt.Equal(in2.CreatedAt) || d2.UpdatedAt.IsZero() {
		t.Errorf("CreatedAt=%v UpdatedAt=%v; want CreatedAt kept and UpdatedAt stamped", d2.CreatedAt, d2.UpdatedAt)
	}
}

func testCreateAssignsIDs(t *testing.T, s Store) {
	ctx := context.Background()
	var ids []model.DownloadID
	for i := range 3 {
		ids = append(ids, mustCreate(t, s, fullDownload(fmt.Sprint("id", i))).ID)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("IDs not increasing: %v", ids)
		}
	}
	last := ids[len(ids)-1]
	if err := s.DeleteDownload(ctx, last); err != nil {
		t.Fatalf("DeleteDownload: %v", err)
	}
	next := mustCreate(t, s, fullDownload("after-delete")).ID
	if next <= last {
		t.Fatalf("ID %d reused or decreased after deleting %d", next, last)
	}
}

func testListOrderedByID(t *testing.T, s Store) {
	ctx := context.Background()
	got, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty store: ListDownloads = %#v, want empty non-nil slice", got)
	}

	var want []model.Download
	for i := range 5 {
		want = append(want, mustCreate(t, s, fullDownload(fmt.Sprint("list", i))))
	}
	got, err = s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ListDownloads returned %d records, want %d", len(got), len(want))
	}
	for i := range want {
		assertDownload(t, got[i], want[i])
	}
}

func testUpdateReplacesRecord(t *testing.T, s Store) {
	ctx := context.Background()
	d := mustCreate(t, s, fullDownload("update"))
	segs := fixtureSegments(2, 10)
	if err := s.SaveCheckpoint(ctx, &d, segs); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	d.State = model.StateCompleted
	d.Mode = model.ModeSingle
	d.Size = -1
	d.Filename = "renamed.zip"
	d.Request.Headers = http.Header{"X-Only": {"one"}}
	d.Request.Body = nil
	d.Validators = model.Validators{LastModified: "Tue, 20 Oct 2026 08:00:00 GMT"}
	d.Error = ""
	d.UpdatedAt = fixtureTime(2 * time.Hour)
	d.CompletedAt = time.Time{}
	if err := s.UpdateDownload(ctx, &d); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	assertDownload(t, mustGet(t, s, d.ID), d)
	assertSegments(t, mustLoad(t, s, d.ID), segs) // segments untouched by UpdateDownload

	// The M4.5 fields and state are replaced like every other field.
	d.State = model.StateAwaitingConfirmation
	d.Source, d.ExpectedSize, d.MIME, d.Category = model.SourceCLI, 0, "", ""
	if err := s.UpdateDownload(ctx, &d); err != nil {
		t.Fatalf("UpdateDownload: %v", err)
	}
	assertDownload(t, mustGet(t, s, d.ID), d)
}

func testDeleteCascades(t *testing.T, s Store) {
	ctx := context.Background()
	keep := mustCreate(t, s, fullDownload("keep"))
	gone := mustCreate(t, s, fullDownload("gone"))
	keepSegs := fixtureSegments(2, 5)
	if err := s.SaveCheckpoint(ctx, &keep, keepSegs); err != nil {
		t.Fatalf("SaveCheckpoint keep: %v", err)
	}
	if err := s.SaveCheckpoint(ctx, &gone, fixtureSegments(3, 7)); err != nil {
		t.Fatalf("SaveCheckpoint gone: %v", err)
	}

	if err := s.DeleteDownload(ctx, gone.ID); err != nil {
		t.Fatalf("DeleteDownload: %v", err)
	}
	_, err := s.GetDownload(ctx, gone.ID)
	assertErrorIs(t, "GetDownload after delete", err, ErrNotFound)
	_, err = s.LoadSegments(ctx, gone.ID)
	assertErrorIs(t, "LoadSegments after delete", err, ErrNotFound)
	assertErrorIs(t, "second DeleteDownload", s.DeleteDownload(ctx, gone.ID), ErrNotFound)

	list, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("ListDownloads after delete = %+v, want only %d", list, keep.ID)
	}
	assertSegments(t, mustLoad(t, s, keep.ID), keepSegs)
}

func testCheckpointReplaces(t *testing.T, s Store) {
	ctx := context.Background()
	d := mustCreate(t, s, fullDownload("checkpoint"))

	first := fixtureSegments(3, 100)
	d.State = model.StateDownloading
	if err := s.SaveCheckpoint(ctx, &d, first); err != nil {
		t.Fatalf("SaveCheckpoint 1: %v", err)
	}
	assertSegments(t, mustLoad(t, s, d.ID), first)
	assertDownload(t, mustGet(t, s, d.ID), d)

	// A smaller, unordered set with new IDs replaces the previous one entirely
	// and comes back ordered by segment ID.
	second := []model.Segment{
		{ID: 9, Start: 500, End: -1, Durable: 0},
		{ID: 4, Start: 0, End: 499, Durable: 499},
	}
	d.State = model.StatePaused
	d.UpdatedAt = fixtureTime(3 * time.Hour)
	if err := s.SaveCheckpoint(ctx, &d, second); err != nil {
		t.Fatalf("SaveCheckpoint 2: %v", err)
	}
	assertSegments(t, mustLoad(t, s, d.ID), []model.Segment{second[1], second[0]})
	assertDownload(t, mustGet(t, s, d.ID), d)

	if err := s.SaveCheckpoint(ctx, &d, nil); err != nil {
		t.Fatalf("SaveCheckpoint empty: %v", err)
	}
	assertSegments(t, mustLoad(t, s, d.ID), []model.Segment{})
}

func testCheckpointRejectsDuplicates(t *testing.T, s Store) {
	ctx := context.Background()
	d := mustCreate(t, s, fullDownload("dup"))
	segs := fixtureSegments(2, 3)
	if err := s.SaveCheckpoint(ctx, &d, segs); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	stored := d

	d.State = model.StateFailed
	bad := []model.Segment{{ID: 1, Start: 0, End: 9}, {ID: 1, Start: 10, End: 19}}
	assertErrorIs(t, "SaveCheckpoint duplicate IDs", s.SaveCheckpoint(ctx, &d, bad), ErrInvalid)
	assertDownload(t, mustGet(t, s, d.ID), stored)
	assertSegments(t, mustLoad(t, s, d.ID), segs)
}

func testNotFound(t *testing.T, s Store) {
	ctx := context.Background()
	existing := mustCreate(t, s, fullDownload("exists"))
	missing := existing.ID + 1000

	_, err := s.GetDownload(ctx, missing)
	assertErrorIs(t, "GetDownload", err, ErrNotFound)
	_, err = s.LoadSegments(ctx, missing)
	assertErrorIs(t, "LoadSegments", err, ErrNotFound)
	assertErrorIs(t, "DeleteDownload", s.DeleteDownload(ctx, missing), ErrNotFound)

	ghost := fullDownload("ghost")
	ghost.ID = missing
	assertErrorIs(t, "UpdateDownload", s.UpdateDownload(ctx, &ghost), ErrNotFound)
	assertErrorIs(t, "SaveCheckpoint", s.SaveCheckpoint(ctx, &ghost, fixtureSegments(2, 1)), ErrNotFound)
	_, err = s.LoadSegments(ctx, missing)
	assertErrorIs(t, "LoadSegments after failed checkpoint", err, ErrNotFound)

	list, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("failed writes created records: %+v", list)
	}
	assertDownload(t, list[0], existing)
}

func testNilAndEmptyPreserved(t *testing.T, s Store) {
	cases := map[string]model.Request{
		"nil headers and body":   {URL: "https://example.test/a"},
		"empty headers and body": {URL: "https://example.test/b", Headers: http.Header{}, Body: []byte{}},
		"header with nil and empty values": {URL: "https://example.test/c", Headers: http.Header{
			"X-Nil":   nil,
			"X-Empty": {},
			"x-raw":   {""}, // non-canonical key kept verbatim
		}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			d := model.Download{Request: req, Size: -1}
			d = mustCreate(t, s, d)
			got := mustGet(t, s, d.ID)
			if (got.Request.Headers == nil) != (req.Headers == nil) || (got.Request.Body == nil) != (req.Body == nil) {
				t.Fatalf("nil-ness changed: got headers=%#v body=%#v, want headers=%#v body=%#v",
					got.Request.Headers, got.Request.Body, req.Headers, req.Body)
			}
			assertDownload(t, got, d)
		})
	}
}

func testNoAliasing(t *testing.T, s Store) {
	ctx := context.Background()
	in := fullDownload("alias")
	d := in
	d.Request.Headers = in.Request.Headers.Clone()
	d.Request.Body = slices.Clone(in.Request.Body)
	if err := s.CreateDownload(ctx, &d); err != nil {
		t.Fatalf("CreateDownload: %v", err)
	}
	want := d
	want.Request.Headers = d.Request.Headers.Clone()
	want.Request.Body = slices.Clone(d.Request.Body)

	// Mutating the caller's value after the write must not reach the store.
	d.Request.Headers["Cookie"][0] = "mutated"
	d.Request.Headers.Set("X-Added", "1")
	d.Request.Body[0] = '!'
	assertDownload(t, mustGet(t, s, want.ID), want)

	// Mutating values returned by reads must not reach the store either.
	got, err := s.GetDownload(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	got.Request.Headers["Cookie"][0] = "mutated"
	got.Request.Body[0] = '!'
	list, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	list[0].Request.Headers.Del("Referer")
	list[0].Request.Body[1] = '!'
	assertDownload(t, mustGet(t, s, want.ID), want)

	segs := fixtureSegments(3, 42)
	wantSegs := slices.Clone(segs)
	if err := s.SaveCheckpoint(ctx, &want, segs); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	segs[0].Durable = -5
	loaded := mustLoad(t, s, want.ID)
	loaded[1].Durable = -6
	assertSegments(t, mustLoad(t, s, want.ID), wantSegs)
}

func testInvalidInput(t *testing.T, s Store) {
	ctx := context.Background()
	assertErrorIs(t, "CreateDownload(nil)", s.CreateDownload(ctx, nil), ErrInvalid)
	assertErrorIs(t, "UpdateDownload(nil)", s.UpdateDownload(ctx, nil), ErrInvalid)
	assertErrorIs(t, "SaveCheckpoint(nil)", s.SaveCheckpoint(ctx, nil, nil), ErrInvalid)

	ancient := fullDownload("ancient")
	ancient.CreatedAt = time.Date(1500, 1, 1, 0, 0, 0, 0, time.UTC)
	assertErrorIs(t, "CreateDownload(year 1500)", s.CreateDownload(ctx, &ancient), ErrInvalid)
	if ancient.ID != 999 {
		t.Fatalf("failed CreateDownload modified the caller's ID to %d", ancient.ID)
	}

	d := mustCreate(t, s, fullDownload("valid"))
	stored := d
	d.CompletedAt = time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)
	assertErrorIs(t, "UpdateDownload(year 2300)", s.UpdateDownload(ctx, &d), ErrInvalid)
	assertErrorIs(t, "SaveCheckpoint(year 2300)", s.SaveCheckpoint(ctx, &d, nil), ErrInvalid)
	d.CompletedAt = stored.CompletedAt
	d.StartedAt = time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)
	assertErrorIs(t, "UpdateDownload(started in 1600)", s.UpdateDownload(ctx, &d), ErrInvalid)
	assertDownload(t, mustGet(t, s, d.ID), stored)

	list, err := s.ListDownloads(ctx)
	if err != nil {
		t.Fatalf("ListDownloads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("invalid writes created records: %+v", list)
	}
}

func testCanceledContext(t *testing.T, s Store) {
	d := mustCreate(t, s, fullDownload("ctx"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fresh := fullDownload("ctx2")
	assertErrorIs(t, "CreateDownload", s.CreateDownload(ctx, &fresh), context.Canceled)
	assertErrorIs(t, "UpdateDownload", s.UpdateDownload(ctx, &d), context.Canceled)
	assertErrorIs(t, "SaveCheckpoint", s.SaveCheckpoint(ctx, &d, fixtureSegments(1, 1)), context.Canceled)
	assertErrorIs(t, "DeleteDownload", s.DeleteDownload(ctx, d.ID), context.Canceled)
	_, err := s.GetDownload(ctx, d.ID)
	assertErrorIs(t, "GetDownload", err, context.Canceled)
	_, err = s.ListDownloads(ctx)
	assertErrorIs(t, "ListDownloads", err, context.Canceled)
	_, err = s.LoadSegments(ctx, d.ID)
	assertErrorIs(t, "LoadSegments", err, context.Canceled)

	// Nothing changed.
	assertDownload(t, mustGet(t, s, d.ID), d)
	assertSegments(t, mustLoad(t, s, d.ID), []model.Segment{})
}

func testClosed(t *testing.T, s Store) {
	ctx := context.Background()
	d := mustCreate(t, s, fullDownload("closed"))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	fresh := fullDownload("closed2")
	assertErrorIs(t, "CreateDownload", s.CreateDownload(ctx, &fresh), ErrClosed)
	assertErrorIs(t, "UpdateDownload", s.UpdateDownload(ctx, &d), ErrClosed)
	assertErrorIs(t, "SaveCheckpoint", s.SaveCheckpoint(ctx, &d, nil), ErrClosed)
	assertErrorIs(t, "DeleteDownload", s.DeleteDownload(ctx, d.ID), ErrClosed)
	_, err := s.GetDownload(ctx, d.ID)
	assertErrorIs(t, "GetDownload", err, ErrClosed)
	_, err = s.ListDownloads(ctx)
	assertErrorIs(t, "ListDownloads", err, ErrClosed)
	_, err = s.LoadSegments(ctx, d.ID)
	assertErrorIs(t, "LoadSegments", err, ErrClosed)
}

// generationSegments encodes gen into every segment so a reader can detect a
// torn checkpoint: all segments must carry the same Durable value and their
// count is a function of it.
func generationSegments(gen int) []model.Segment {
	return fixtureSegments(1+gen%4, int64(gen))
}

func testConcurrentCheckpoints(t *testing.T, s Store) {
	const (
		writers = 8
		rounds  = 20
		readers = 4
	)
	ctx := context.Background()
	ids := make([]model.DownloadID, writers)
	for i := range ids {
		ids[i] = mustCreate(t, s, fullDownload(fmt.Sprint("conc", i))).ID
	}

	stop := make(chan struct{})
	var readersWG sync.WaitGroup
	for range readers {
		readersWG.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range ids {
					segs, err := s.LoadSegments(ctx, id)
					if err != nil {
						t.Errorf("LoadSegments(%d): %v", id, err)
						return
					}
					if len(segs) == 0 {
						continue
					}
					gen := int(segs[0].Durable)
					if want := generationSegments(gen); !slices.Equal(segs, want) {
						t.Errorf("torn checkpoint for %d: got %+v, want %+v", id, segs, want)
						return
					}
				}
				if _, err := s.ListDownloads(ctx); err != nil {
					t.Errorf("ListDownloads: %v", err)
					return
				}
			}
		})
	}

	var writersWG sync.WaitGroup
	for _, id := range ids {
		writersWG.Go(func() {
			for gen := 1; gen <= rounds; gen++ {
				d, err := s.GetDownload(ctx, id)
				if err != nil {
					t.Errorf("GetDownload(%d): %v", id, err)
					return
				}
				d.Size = int64(gen)
				d.State = model.StateDownloading
				if err := s.SaveCheckpoint(ctx, d, generationSegments(gen)); err != nil {
					t.Errorf("SaveCheckpoint(%d, gen %d): %v", id, gen, err)
					return
				}
			}
		})
	}
	writersWG.Wait()
	close(stop)
	readersWG.Wait()
	if t.Failed() {
		return
	}

	for _, id := range ids {
		d := mustGet(t, s, id)
		if d.Size != rounds || d.State != model.StateDownloading {
			t.Errorf("download %d: size=%d state=%v, want size=%d state=downloading", id, d.Size, d.State, rounds)
		}
		assertSegments(t, mustLoad(t, s, id), generationSegments(rounds))
	}
}
