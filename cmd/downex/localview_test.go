package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"downex/internal/daemon"
	"downex/internal/model"
	"downex/internal/store"
	"downex/internal/transport"
)

// The CLI's local views (info and info --json, which always read the database, and ls
// and ls --json while no downexd runs) show a stored download as downexd's Get and List
// show it when it is stopped: the same size, the same "~" for an estimate and the same
// bytes done and percent, for every kind (docs/SITE-MEDIA-SPEC.md §3.3, §3.7).

// storedRecord is one download to put into a test database, with the segments of a file
// download. queued downloads go into a queue that is stopped, so that a daemon started on
// the database leaves them as they are.
type storedRecord struct {
	name string
	d    *model.Download
	segs []model.Segment
}

// seedDownloads stores recs in the database at db (created), in order: their ids are 1,
// 2, ...
func seedDownloads(t *testing.T, db string, recs ...storedRecord) {
	t.Helper()
	ctx := context.Background()
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var held []model.DownloadID
	for i, r := range recs {
		if err := st.CreateDownload(ctx, r.d); err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if r.d.FileCheck != nil { // written only by SetFileCheck
			if err := st.SetFileCheck(ctx, r.d.ID, r.d.FileCheck); err != nil {
				t.Fatalf("%s: %v", r.name, err)
			}
		}
		if r.d.ID != model.DownloadID(i+1) {
			t.Fatalf("%s: stored as download %d, want %d", r.name, r.d.ID, i+1)
		}
		if len(r.segs) > 0 {
			if err := st.SaveCheckpoint(ctx, r.d, r.segs); err != nil {
				t.Fatalf("%s: %v", r.name, err)
			}
		}
		if r.d.State == model.StateQueued {
			held = append(held, r.d.ID)
		}
	}
	if len(held) > 0 {
		q := &model.Queue{Name: "Held", Running: false}
		if err := st.CreateQueue(ctx, q); err != nil {
			t.Fatal(err)
		}
		if err := st.MoveDownloads(ctx, held, q.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
}

// daemonOn runs downexd's Manager on the database at db in this process (no media jobs,
// no desktop, nothing that runs) and returns a host whose commands reach it. It is shut
// down, and the database closed, when the test ends.
func daemonOn(t *testing.T, db string) *fakeHost {
	t.Helper()
	st, err := store.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	opts := transport.DefaultHTTPOptions()
	opts.Proxy = "direct"
	tf, err := transport.NewHTTPFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := daemon.New(context.Background(), daemon.Config{
		Store: st, Transport: tf, DownloadsDir: func() (string, error) { return dir, nil },
		Logger: slog.New(slog.DiscardHandler), Version: "test-version", Endpoint: "fake-endpoint",
	})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	srv := httptest.NewServer(daemon.NewHandler(m, nil))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		srv.Close()
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return &fakeHost{srv: srv, up: true, startTimeout: 2 * time.Second, stopTimeout: 2 * time.Second}
}

// infoRow is the value of info's row named key ("" when there is none).
func infoRow(out, key string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// lsLine is the row of download id in ls's table ("" when there is none).
func lsLine(out string, id int) string {
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == strconv.Itoa(id) {
			return line
		}
	}
	return ""
}

// A web page's video paused under 0.8.1, in the shape that version stored it (the record
// of internal/dlview's TestOldSiteCheckpointShowsItsDurableChunks; made-up sizes that keep
// its chunk counts): direct formats, 4K video 401 and audio 140 of exact sizes, a
// checkpoint without durableBytes or bytesTotal whose done chunks are video 0, 4, 5 of 29
// (10 MiB) and audio 0 of 8 (2,162,688 B), and a bytes done that also counted chunks still
// arriving.
const (
	oldSiteTotal   = 300_000_000 + 16_000_000
	oldSiteDurable = 3*10_485_760 + 2_162_688
)

func oldSiteVideo(dir string) *model.Download {
	return &model.Download{Request: model.Request{URL: testPage}, Dir: dir, Filename: "video.mp4", Size: -1,
		State: model.StatePaused, Kind: model.KindMedia, Media: &model.Media{
			Origin: "site", Site: "YouTube", Format: "http", Container: "mp4", SelectedVideo: "401", SelectedAudio: "140",
			Duration: 900 * time.Second,
			Tracks: []model.MediaTrack{
				{ID: "401", Kind: "video", Width: 3840, Height: 2160, Size: 300_000_000, Ext: "mp4", Key: "s1-v"},
				{ID: "140", Kind: "audio", Bandwidth: 128_000, Size: 16_000_000, Ext: "m4a", Key: "s1-a"},
			},
			FragmentsTotal: 37, FragmentsDone: 4, BytesDone: 52_000_000,
			Checkpoint: &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
				{TrackID: "401", Total: 29, Done: []byte{0x31, 0, 0, 0}, Key: "s1-v", ChunkBytes: 10_485_760},
				{TrackID: "140", Total: 8, Done: []byte{0x01}, Key: "s1-a", ChunkBytes: 2_162_688},
			}},
		}}
}

// A web page's video paused under 0.8.1: info shows the probe's exact size and the bytes
// of the checkpoint's done chunks (where a resume starts), never "unknown" and the
// inflated bytes done; ls without downexd shows the percent and size downexd shows.
func TestInfoOldSiteCheckpoint(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	seedDownloads(t, db, storedRecord{name: "old checkpoint", d: oldSiteVideo(t.TempDir())})
	ctx := context.Background()
	sys := offlineSystem()

	r := runWith(ctx, sys, "info", "1", "--db", db)
	if got := infoRow(r.stdout, "Size"); got != "316.0 MB (316000000 bytes)" {
		t.Errorf("info Size = %q, want the exact total 316.0 MB:\n%s", got, r.stdout)
	}
	if got := infoRow(r.stdout, "Downloaded"); got != "33.6 MB (10.6%)" {
		t.Errorf("info Downloaded = %q, want the done chunks, 33.6 MB (10.6%%):\n%s", got, r.stdout)
	}
	if got := infoRow(r.stdout, "Parts"); got != "4/37 parts" {
		t.Errorf("info Parts = %q", got)
	}
	r = runWith(ctx, sys, "info", "1", "--json", "--db", db)
	var v infoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Size != oldSiteTotal || v.SizeApprox ||
		v.Downloaded != oldSiteDurable {
		t.Errorf("info --json: size %d (approx %v), downloaded %d, err %v; want %d, exact, %d",
			v.Size, v.SizeApprox, v.Downloaded, err, oldSiteTotal, oldSiteDurable)
	}
	offline := runWith(ctx, sys, "ls", "--db", db)
	if line := lsLine(offline.stdout, 1); !strings.Contains(line, "10.6%") || !strings.Contains(line, "316.0 MB") ||
		strings.Contains(line, "~") {
		t.Errorf("ls without downexd: %q, want 10.6%% of 316.0 MB:\n%s", line, offline.stdout)
	}
	if live := daemonOn(t, db).run(ctx, "ls"); live.code != exitOK || live.stdout != offline.stdout {
		t.Errorf("ls differs from downexd's list of the same record:\nwithout downexd:\n%s\nfrom downexd: %v",
			offline.stdout, live)
	}
}

// A checkpoint written by 0.8.2 is shown as written: its bytesTotal (the job measured the
// formats, which had no size) and its durableBytes, prefixes included.
func TestInfoDurableCheckpoint(t *testing.T) {
	d := oldSiteVideo(t.TempDir())
	d.Media.Tracks[0].Size, d.Media.Tracks[1].Size = 0, 0
	d.Media.BytesDone = 40_000_000
	d.Media.Checkpoint.DurableBytes, d.Media.Checkpoint.BytesTotal = 40_000_000, oldSiteTotal
	d.Media.Checkpoint.Tracks[0].Partial = map[int]int64{1: 4_000_000}
	db := filepath.Join(t.TempDir(), "downex.db")
	seedDownloads(t, db, storedRecord{name: "0.8.2 checkpoint", d: d})
	ctx := context.Background()
	sys := offlineSystem()

	r := runWith(ctx, sys, "info", "1", "--db", db)
	if got := infoRow(r.stdout, "Size"); got != "316.0 MB (316000000 bytes)" {
		t.Errorf("info Size = %q, want the checkpoint's total:\n%s", got, r.stdout)
	}
	if got := infoRow(r.stdout, "Downloaded"); got != "40.0 MB (12.7%)" {
		t.Errorf("info Downloaded = %q, want the checkpoint's durable bytes, 40.0 MB (12.7%%):\n%s", got, r.stdout)
	}
	r = runWith(ctx, sys, "info", "1", "--json", "--db", db)
	var v infoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Size != oldSiteTotal || v.SizeApprox ||
		v.Downloaded != 40_000_000 {
		t.Errorf("info --json: size %d (approx %v), downloaded %d, err %v", v.Size, v.SizeApprox, v.Downloaded, err)
	}
	r = runWith(ctx, sys, "ls", "--db", db)
	if line := lsLine(r.stdout, 1); !strings.Contains(line, "12.7%") || !strings.Contains(line, "316.0 MB") {
		t.Errorf("ls without downexd: %q", line)
	}
}

// hlsStream is a paused HLS stream of 40 fragments, 10 of them durable (12.5 MB): its
// size is the estimate downexd extrapolates from them, ~50 MB.
func hlsStream(dir string) *model.Download {
	return &model.Download{Request: model.Request{URL: testManifest}, Dir: dir, Filename: "show.mp4", Size: -1,
		State: model.StatePaused, Kind: model.KindMedia, Media: &model.Media{
			Format: "hls", Container: "mp4", Duration: 100 * time.Second, SelectedVideo: "v1", SelectedAudio: "a1",
			Tracks: []model.MediaTrack{
				{ID: "v0", Kind: "video", Bandwidth: 800_000},
				{ID: "v1", Kind: "video", Bandwidth: 4_000_000},
				{ID: "a1", Kind: "audio", Bandwidth: 128_000},
			},
			FragmentsTotal: 40, FragmentsDone: 10, BytesDone: 12_500_000,
			Checkpoint: &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
				{TrackID: "v1", Total: 20, Done: []byte{0x1f, 0}}, {TrackID: "a1", Total: 20, Done: []byte{0x1f, 0}},
			}},
		}}
}

// A stream's size is an estimate until it is assembled: ls and info mark it with "~",
// and its percent counts fragments, as downexd's list does.
func TestStreamEstimateLocal(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	seedDownloads(t, db, storedRecord{name: "hls", d: hlsStream(t.TempDir())})
	ctx := context.Background()
	sys := offlineSystem()

	offline := runWith(ctx, sys, "ls", "--db", db)
	if line := lsLine(offline.stdout, 1); !strings.Contains(line, "25.0%") || !strings.Contains(line, "~50.0 MB") {
		t.Errorf("ls without downexd: %q, want 25.0%% of ~50.0 MB:\n%s", line, offline.stdout)
	}
	r := runWith(ctx, sys, "info", "1", "--db", db)
	if got := infoRow(r.stdout, "Size"); got != "~50.0 MB (estimated)" {
		t.Errorf("info Size = %q, want ~50.0 MB (estimated):\n%s", got, r.stdout)
	}
	if got := infoRow(r.stdout, "Downloaded"); got != "12.5 MB (25.0%)" {
		t.Errorf("info Downloaded = %q, want 12.5 MB (25.0%%):\n%s", got, r.stdout)
	}
	r = runWith(ctx, sys, "info", "1", "--json", "--db", db)
	var v infoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Size != 50_000_000 || !v.SizeApprox ||
		v.Downloaded != 12_500_000 {
		t.Errorf("info --json: size %d (approx %v), downloaded %d, err %v", v.Size, v.SizeApprox, v.Downloaded, err)
	}
	if live := daemonOn(t, db).run(ctx, "ls"); live.code != exitOK || live.stdout != offline.stdout {
		t.Errorf("ls differs from downexd's list of the same record:\nwithout downexd:\n%s\nfrom downexd: %v",
			offline.stdout, live)
	}
}

// storedTable is one stopped record of every kind whose size or bytes done downexd
// derives when it reads it: web pages' direct formats (checkpoints of 0.8.1 and 0.8.2,
// exact, estimated and unknown sizes, nothing received), streams of manifests and of
// sites (extrapolated, every fragment, none yet), completed media, file downloads.
func storedTable(dir string) []storedRecord {
	site := func(name string, edit func(d *model.Download)) storedRecord {
		d := oldSiteVideo(dir)
		d.Filename = name + ".mp4"
		edit(d)
		return storedRecord{name: name, d: d}
	}
	stream := func(name string, edit func(d *model.Download)) storedRecord {
		d := hlsStream(dir)
		d.Filename = name + ".mp4"
		edit(d)
		return storedRecord{name: name, d: d}
	}
	durable := func(d *model.Download) {
		d.Media.BytesDone = 40_000_000
		d.Media.Checkpoint.DurableBytes, d.Media.Checkpoint.BytesTotal = 40_000_000, oldSiteTotal
		d.Media.Checkpoint.Tracks[0].Partial = map[int]int64{1: 4_000_000}
	}
	completed := func(size int64) func(d *model.Download) {
		return func(d *model.Download) {
			d.State, d.Size, d.CompletedAt = model.StateCompleted, size, time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
			d.Media.FragmentsDone, d.Media.BytesDone, d.Media.Checkpoint = d.Media.FragmentsTotal, size, nil
		}
	}
	return []storedRecord{
		site("site-old-paused", func(*model.Download) {}),
		site("site-old-failed", func(d *model.Download) { d.State, d.Error = model.StateFailed, "download failed" }),
		site("site-old-queued", func(d *model.Download) { d.State = model.StateQueued }),
		site("site-old-estimated", func(d *model.Download) { d.Media.Tracks[0].SizeApprox = true }),
		site("site-old-estimated-few", func(d *model.Download) {
			d.Media.Tracks[0].SizeApprox = true
			d.Media.BytesDone = 1000
		}),
		site("site-old-short-last-chunk", func(d *model.Download) { d.Media.Checkpoint.Tracks[1].Done = []byte{0x81} }),
		site("site-old-unsized", func(d *model.Download) {
			d.Media.Tracks[0].Size, d.Media.Tracks[1].Size, d.Media.Tracks[1].Bandwidth = 0, 0, 0
		}),
		site("site-durable", durable),
		site("site-durable-measured", func(d *model.Download) {
			durable(d)
			d.Media.Tracks[0].Size, d.Media.Tracks[1].Size = 0, 0
		}),
		site("site-nothing-yet", func(d *model.Download) {
			d.Media.FragmentsTotal, d.Media.FragmentsDone, d.Media.BytesDone, d.Media.Checkpoint = 0, 0, 0, nil
		}),
		site("site-completed", completed(320_000_000)),
		stream("hls-paused", func(*model.Download) {}),
		stream("hls-failed", func(d *model.Download) { d.State, d.Error = model.StateFailed, "download failed" }),
		stream("hls-queued", func(d *model.Download) { d.State = model.StateQueued }),
		stream("hls-no-fragment-yet", func(d *model.Download) {
			d.Media.FragmentsTotal, d.Media.FragmentsDone, d.Media.BytesDone, d.Media.Checkpoint = 1816, 0, 0, nil
		}),
		stream("dash-every-fragment", func(d *model.Download) {
			d.Media.Format, d.Media.FragmentsDone, d.Media.BytesDone = "dash", 40, 49_000_000
		}),
		stream("site-stream-sized", func(d *model.Download) {
			d.Request.URL, d.Media.Origin, d.Media.Site, d.Media.PageURL = testPage, "site", "YouTube", testPage
			d.Media.Tracks[1].Size, d.Media.Tracks[2].Size = 50_000_000, 1_600_000
		}),
		stream("hls-completed", completed(48_500_000)),
		{name: "file-paused", d: &model.Download{Request: model.Request{URL: "https://example.test/f.iso"}, Dir: dir,
			Filename: "f.iso", Size: 1000, State: model.StatePaused, Mode: model.ModeRanged, Kind: model.KindFile},
			segs: []model.Segment{{ID: 0, Start: 0, End: 499, Durable: 200}, {ID: 1, Start: 500, End: 999, Durable: 100}}},
		{name: "file-completed", d: &model.Download{Request: model.Request{URL: "https://example.test/g.iso"}, Dir: dir,
			Filename: "g.iso", Size: 100, State: model.StateCompleted, Mode: model.ModeRanged, Kind: model.KindFile,
			CompletedAt: time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)}},
		// The file check (docs/DESKTOP-SPEC.md §9): a stored final result and a name stage.
		{name: "file-disguised", d: &model.Download{Request: model.Request{URL: "https://example.test/m.mp4"}, Dir: dir,
			Filename: "movie.mp4", Size: 100, State: model.StateCompleted, Mode: model.ModeRanged, Kind: model.KindFile,
			CompletedAt: time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC), ServedType: "video/mp4",
			FileCheck: &model.FileCheck{Level: model.FileCheckDanger, Stage: model.FileCheckStageFinal,
				Reason: "program_disguised", Content: "pe", Claim: "mp4", Served: "video/mp4"}}},
		{name: "file-double-extension", d: &model.Download{Request: model.Request{URL: "https://example.test/i"}, Dir: dir,
			Filename: "invoice.pdf.exe", Size: 100, State: model.StatePaused, Mode: model.ModeRanged, Kind: model.KindFile},
			segs: []model.Segment{{ID: 0, Start: 0, End: 99, Durable: 10}}},
	}
}

// TestLocalViewMatchesDaemon is the guard against drift: for every stored record of
// storedTable, the CLI's local view (ls --json and info --json without downexd) and
// downexd's conversion of the same record (its List, through ls --json) agree on size,
// approx and bytes done, and ls prints the same table either way.
func TestLocalViewMatchesDaemon(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	table := storedTable(t.TempDir())
	seedDownloads(t, db, table...)
	ctx := context.Background()
	sys := offlineSystem()

	views := func(r cliResult) map[model.DownloadID]downloadView {
		t.Helper()
		var list []downloadView
		if r.code != exitOK {
			t.Fatalf("ls --json: %v", r)
		}
		if err := json.Unmarshal([]byte(r.stdout), &list); err != nil {
			t.Fatalf("ls --json: %v\n%s", err, r.stdout)
		}
		out := make(map[model.DownloadID]downloadView, len(list))
		for _, v := range list {
			out[v.ID] = v
		}
		return out
	}
	offlineText := runWith(ctx, sys, "ls", "--db", db)
	local := views(runWith(ctx, sys, "ls", "--json", "--db", db))
	info := make(map[model.DownloadID]infoView)
	for i := range table {
		r := runWith(ctx, sys, "info", strconv.Itoa(i+1), "--json", "--db", db)
		var v infoView
		if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
			t.Fatalf("info %d --json: %v\n%v", i+1, err, r)
		}
		info[v.ID] = v
	}

	h := daemonOn(t, db)
	remote := views(h.run(ctx, "ls", "--json"))
	var exact, approx, unknown int
	for i, rec := range table {
		id := model.DownloadID(i + 1)
		want, ok := remote[id]
		if !ok {
			t.Errorf("%s: downexd does not list download %d", rec.name, id)
			continue
		}
		if want.State != stateName(rec.d.State) {
			t.Errorf("%s: downexd shows state %s, stored %s (it must not change stopped records)", rec.name, want.State,
				stateName(rec.d.State))
		}
		for _, got := range []struct {
			from string
			v    downloadView
		}{{"ls", local[id]}, {"info", info[id].downloadView}} {
			if !reflect.DeepEqual(got.v.FileCheck, want.FileCheck) {
				t.Errorf("%s: %s without downexd shows file check %+v; downexd: %+v", rec.name, got.from, got.v.FileCheck, want.FileCheck)
			}
			if got.v.Size != want.Size || got.v.SizeApprox != want.SizeApprox || got.v.Downloaded != want.Downloaded {
				t.Errorf("%s: %s without downexd shows size %d (approx %v), downloaded %d; downexd: size %d (approx %v), downloaded %d",
					rec.name, got.from, got.v.Size, got.v.SizeApprox, got.v.Downloaded, want.Size, want.SizeApprox, want.Downloaded)
			}
		}
		switch {
		case want.Size < 0:
			unknown++
		case want.SizeApprox:
			approx++
		default:
			exact++
		}
	}
	if exact < 3 || approx < 3 || unknown < 2 {
		t.Errorf("the table covers %d exact, %d estimated and %d unknown sizes; it must cover each kind", exact, approx, unknown)
	}
	if live := h.run(ctx, "ls"); live.stdout != offlineText.stdout {
		t.Errorf("ls differs from downexd's list:\nwithout downexd:\n%s\nfrom downexd:\n%s", offlineText.stdout, live.stdout)
	}
}
