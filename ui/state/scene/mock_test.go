package scene

import (
	"context"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
)

const dir = "/home/you/Downloads"

var ctx = context.Background()

func zero() *float64         { v := 0.0; return &v }
func num(v float64) *float64 { return &v }

// mk is mock.test.ts make(): an empty list, seed 42, no timer, no random failures.
func mk(t *testing.T, edit ...func(*Options)) *Mock {
	t.Helper()
	o := Options{Scene: Random, Seed: 42, FailureRate: zero(), DefaultDir: dir}
	for _, e := range edit {
		e(&o)
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func get(t *testing.T, m *Mock, id int64) *downexv1.Download {
	t.Helper()
	l, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range l.GetDownloads() {
		if d.GetId() == id {
			return d
		}
	}
	t.Fatalf("no download %d", id)
	return nil
}

// runUntil ticks one simulated second at a time until done holds.
func runUntil(t *testing.T, m *Mock, id int64, done func(*downexv1.Download) bool) *downexv1.Download {
	t.Helper()
	for range 20_000 {
		d := get(t, m, id)
		if done(d) {
			return d
		}
		m.Tick(1000)
	}
	t.Fatalf("condition not reached for download %d", id)
	return nil
}

func checkSegments(t *testing.T, d *downexv1.Download) {
	t.Helper()
	if len(d.GetSegments()) == 0 {
		return
	}
	next, sum := int64(0), int64(0)
	for _, s := range d.GetSegments() {
		if s.GetStart() != next {
			t.Fatalf("download %d: segment starts at %d, want %d", d.GetId(), s.GetStart(), next)
		}
		length := int64(math.MaxInt64)
		if s.GetEnd() >= 0 {
			length = s.GetEnd() - s.GetStart() + 1
		}
		if s.GetReceived() < 0 || s.GetReceived() > length {
			t.Fatalf("download %d: segment received %d of %d", d.GetId(), s.GetReceived(), length)
		}
		next = s.GetEnd() + 1
		sum += s.GetReceived()
	}
	if d.GetSize() > 0 && d.GetSegments()[len(d.GetSegments())-1].GetEnd() != d.GetSize()-1 {
		t.Fatalf("download %d: last segment ends at %d, size %d", d.GetId(), d.GetSegments()[len(d.GetSegments())-1].GetEnd(), d.GetSize())
	}
	if sum != d.GetReceived() {
		t.Fatalf("download %d: segments hold %d, received %d", d.GetId(), sum, d.GetReceived())
	}
}

func add(t *testing.T, m *Mock, req *downexv1.AddRequest) *downexv1.Download {
	t.Helper()
	r, err := m.Add(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return r.GetDownload()
}

func addMedia(t *testing.T, m *Mock, req *downexv1.AddMediaRequest) *downexv1.Download {
	t.Helper()
	r, err := m.AddMedia(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return r.GetDownload()
}

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil || connect.CodeOf(err) != code {
		t.Fatalf("error %v, want code %v", err, code)
	}
}

func wantMessage(t *testing.T, err error, code connect.Code, re string) {
	t.Helper()
	wantCode(t, err, code)
	var ce *connect.Error
	msg := err.Error()
	if e, ok := err.(*connect.Error); ok {
		ce = e
		msg = ce.Message()
	}
	if !regexp.MustCompile(re).MatchString(msg) {
		t.Fatalf("message %q does not match %s", msg, re)
	}
}

func list(t *testing.T, m *Mock) []*downexv1.Download {
	t.Helper()
	l, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return l.GetDownloads()
}

func byState(l []*downexv1.Download, s downexv1.DownloadState) []*downexv1.Download {
	var out []*downexv1.Download
	for _, d := range l {
		if d.GetState() == s {
			out = append(out, d)
		}
	}
	return out
}

func settingsOf(t *testing.T, m *Mock) *downexv1.Settings {
	t.Helper()
	r, err := m.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return r.GetSettings()
}

func update(m *Mock, s *downexv1.Settings, edit func(*downexv1.Settings)) (*downexv1.Settings, error) {
	c := proto.CloneOf(s)
	edit(c)
	r, err := m.UpdateSettings(ctx, &downexv1.UpdateSettingsRequest{Settings: c})
	return r.GetSettings(), err
}

func withSettings(edit func(*downexv1.Settings)) func(*Options) {
	return func(o *Options) { o.Settings = edit }
}

const (
	sQueued      = state.StateQueued
	sProbing     = state.StateProbing
	sDownloading = state.StateDownloading
	sPaused      = state.StatePaused
	sCompleted   = state.StateCompleted
	sFailed      = state.StateFailed
	sAwaiting    = state.StateAwaiting
	kMedia       = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
	site         = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
)

// ---- lifecycle --------------------------------------------------------------------------

func TestQueuesProbesDownloadsInSegmentsAndCompletes(t *testing.T) {
	m := mk(t)
	added := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/report.pdf?size=20971520"})
	if added.GetState() != sQueued || added.GetFilename() != "report.pdf" || added.GetDir() != dir+"/Documents" || added.GetCategory() != "Documents" || added.GetReceived() != 0 || added.GetSize() <= 0 {
		t.Fatalf("added %v", added)
	}
	m.Tick(100)
	if get(t, m, added.GetId()).GetState() != sProbing {
		t.Fatal("not probing")
	}
	running := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sDownloading && d.GetReceived() > 0 })
	if running.GetMode() != downexv1.Mode_MODE_RANGED || running.GetSize() != 20*MiB || len(running.GetSegments()) < 8 || len(running.GetSegments()) > 64 || running.GetConnections() <= 0 || running.GetSpeedBps() <= 0 {
		t.Fatalf("running %v", running)
	}
	checkSegments(t, running)
	done := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool { return d.GetState() != sDownloading })
	if done.GetState() != sCompleted || done.GetReceived() != done.GetSize() || done.GetCompletedAt() == nil || done.GetConnections() != 0 || done.GetSpeedBps() != 0 {
		t.Fatalf("done %v", done)
	}
}

func TestKeepsSegmentInvariantsWhileSplitting(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/big-movie.mkv", MaxConnections: 4})
	maxSegs := 0
	for range 400 {
		m.Tick(1000)
		cur := get(t, m, d.GetId())
		checkSegments(t, cur)
		if cur.GetConnections() > 4 {
			t.Fatalf("%d connections", cur.GetConnections())
		}
		maxSegs = max(maxSegs, len(cur.GetSegments()))
		if cur.GetState() == sCompleted {
			break
		}
	}
	if maxSegs < 4 {
		t.Fatalf("at most %d segments", maxSegs)
	}
}

func TestStartsQueuedDownloadsFIFO(t *testing.T) {
	m := mk(t, withSettings(func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 2 }))
	var ids []int64
	for i := range 5 {
		ids = append(ids, add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/clip-" + string(rune('0'+i)) + ".mp4"}).GetId())
	}
	m.Tick(100)
	var running []int64
	var positions []int32
	for _, d := range list(t, m) {
		if d.GetState() == sProbing || d.GetState() == sDownloading {
			running = append(running, d.GetId())
		}
		if d.GetState() == sQueued {
			positions = append(positions, d.GetQueuePosition())
		}
	}
	if !slices.Equal(running, ids[:2]) || !slices.Equal(positions, []int32{1, 2, 3}) {
		t.Fatalf("running %v, positions %v", running, positions)
	}
	st, _ := m.Status(ctx)
	if st.GetActive() != 2 || st.GetQueued() != 3 {
		t.Fatalf("status %v", st)
	}
}

func TestPausesWithoutLosingProgress(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/holiday.mp4"})
	before := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetReceived() > 4*MiB })
	p, err := m.Pause(ctx, &downexv1.PauseRequest{Id: d.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	paused := p.GetDownload()
	if paused.GetState() != sPaused || paused.GetSpeedBps() != 0 || paused.GetConnections() != 0 {
		t.Fatalf("paused %v", paused)
	}
	m.Tick(1000)
	m.Tick(1000)
	still := get(t, m, d.GetId())
	if still.GetReceived() != paused.GetReceived() || still.GetReceived() < before.GetReceived() {
		t.Fatal("progress changed while paused")
	}
	r, _ := m.Resume(ctx, &downexv1.ResumeRequest{Id: d.GetId()})
	if r.GetDownload().GetState() != sQueued {
		t.Fatal("not queued")
	}
	again := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool {
		return x.GetState() == sDownloading && x.GetReceived() > paused.GetReceived()
	})
	if len(again.GetSegments()) == 0 {
		t.Fatal("no segments")
	}
}

func TestPausesQueuedAndRejectsInvalidTransitions(t *testing.T) {
	m := mk(t, withSettings(func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 1 }))
	add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/a.zip"})
	second := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/b.zip"})
	if p, _ := m.Pause(ctx, &downexv1.PauseRequest{Id: second.GetId()}); p.GetDownload().GetState() != sPaused {
		t.Fatal("not paused")
	}
	_, err := m.Resume(ctx, &downexv1.ResumeRequest{Id: second.GetId(), MaxConnections: proto.Int32(99)})
	wantCode(t, err, connect.CodeInvalidArgument)
	r, err := m.Resume(ctx, &downexv1.ResumeRequest{Id: second.GetId(), MaxConnections: proto.Int32(4), SpeedLimit: proto.Int64(64 * KiB)})
	if err != nil || r.GetDownload().GetState() != sQueued || r.GetDownload().GetMaxConnections() != 4 || r.GetDownload().GetSpeedLimit() != 64*KiB {
		t.Fatalf("resumed %v %v", r, err)
	}
	_, err = m.Resume(ctx, &downexv1.ResumeRequest{Id: second.GetId()})
	wantCode(t, err, connect.CodeFailedPrecondition)
	_, err = m.Pause(ctx, &downexv1.PauseRequest{Id: 12345})
	wantCode(t, err, connect.CodeNotFound)
}

// ---- failures ---------------------------------------------------------------------------

func TestReportsValidatorChangedUntilRestart(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/changed/movie.mp4?size=31457280"})
	failed := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sFailed })
	if failed.GetErrorCode() != "validator_changed" || failed.GetError() == "" || failed.GetReceived() <= 0 {
		t.Fatalf("failed %v", failed)
	}
	_, _ = m.Resume(ctx, &downexv1.ResumeRequest{Id: d.GetId()})
	if again := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sFailed }); again.GetErrorCode() != "validator_changed" {
		t.Fatal(again)
	}
	r, _ := m.Resume(ctx, &downexv1.ResumeRequest{Id: d.GetId(), Restart: true})
	if x := r.GetDownload(); x.GetState() != sQueued || x.GetReceived() != 0 || x.GetErrorCode() != "" || x.GetError() != "" || len(x.GetSegments()) != 0 {
		t.Fatalf("restarted %v", x)
	}
	done := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted || x.GetState() == sFailed })
	if done.GetState() != sCompleted {
		t.Fatal(done)
	}
}

func TestFailsWhileProbingOnHTTPError(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/missing/404/file.zip"})
	failed := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sFailed })
	if failed.GetErrorCode() != "http_status" || failed.GetReceived() != 0 {
		t.Fatal(failed)
	}
}

func TestDownloadsUnknownSizeOnOneConnection(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/live/unknown-size/capture.ts?size=10485760"})
	if d.GetSize() != -1 {
		t.Fatal(d.GetSize())
	}
	running := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sDownloading && x.GetReceived() > 0 })
	if running.GetMode() != downexv1.Mode_MODE_SINGLE || len(running.GetSegments()) != 1 || running.GetSegments()[0].GetEnd() != -1 || running.GetSegments()[0].GetReceived() != running.GetReceived() {
		t.Fatal(running)
	}
	done := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	if done.GetSize() != 10*MiB || done.GetReceived() != done.GetSize() || done.GetSegments()[0].GetEnd() != done.GetSize()-1 {
		t.Fatal(done)
	}
}

func TestValidatesInputLikeTheDaemon(t *testing.T) {
	m := mk(t)
	for _, req := range []*downexv1.AddRequest{
		{Url: "ftp://example.com/a"},
		{Url: "https://example.com/a", MaxConnections: 33},
		{Url: "https://example.com/a", SpeedLimit: -1},
		{Url: "https://example.com/a", Dir: "relative"},
	} {
		_, err := m.Add(ctx, req)
		wantCode(t, err, connect.CodeInvalidArgument)
	}
	d := add(t, m, &downexv1.AddRequest{Url: "https://example.com/a.zip"})
	wantCode(t, m.OpenFile(ctx, d.GetId()), connect.CodeFailedPrecondition)
}

// ---- limits -----------------------------------------------------------------------------

func TestRespectsThePerDownloadSpeedLimit(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/limited.mkv", SpeedLimit: 256 * KiB})
	start := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sDownloading })
	for range 4 {
		m.Tick(1000)
	}
	got := get(t, m, d.GetId()).GetReceived() - start.GetReceived()
	if got > 4*256*KiB || got <= 0 {
		t.Fatal(got)
	}
}

func TestSharesTheGlobalSpeedLimit(t *testing.T) {
	m := mk(t, withSettings(func(s *downexv1.Settings) { s.GlobalSpeedLimit = 512 * KiB; s.MaxConcurrentDownloads = 3 }))
	for _, n := range []string{"m0", "m1", "m2"} {
		add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/" + n + ".mkv"})
	}
	m.Tick(1000)
	m.Tick(1000)
	total := func() (sum int64) {
		for _, d := range list(t, m) {
			sum += d.GetReceived()
		}
		return sum
	}
	t0 := total()
	for range 5 {
		m.Tick(1000)
	}
	if got := total() - t0; got > 5*512*KiB {
		t.Fatal(got)
	}
}

func TestValidatesAndStoresSettings(t *testing.T) {
	m := mk(t)
	cur := settingsOf(t, m)
	var names []string
	for _, c := range cur.GetCategories() {
		names = append(names, c.GetName())
	}
	if !slices.Equal(names, []string{"Video", "Audio", "Archives", "Documents", "Programs"}) {
		t.Fatal(names)
	}
	for _, edit := range []func(*downexv1.Settings){
		func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 0 },
		func(s *downexv1.Settings) { s.DefaultDir = "nope" },
		func(s *downexv1.Settings) {
			s.Categories = append(s.Categories, &downexv1.Category{Name: "video", Extensions: []string{"x"}})
		},
		func(s *downexv1.Settings) {
			s.Categories = []*downexv1.Category{{Name: "Bad", Extensions: []string{".MP4"}}}
		},
	} {
		_, err := update(m, cur, edit)
		wantCode(t, err, connect.CodeInvalidArgument)
	}
	next := proto.CloneOf(cur)
	next.MaxConcurrentDownloads, next.GlobalSpeedLimit, next.DefaultDir, next.DefaultMaxConnections = 5, MiB, "/data", 16
	saved, err := update(m, next, func(*downexv1.Settings) {})
	if err != nil || !proto.Equal(saved, next) || !proto.Equal(settingsOf(t, m), next) {
		t.Fatalf("saved %v %v", saved, err)
	}
	if d := add(t, m, &downexv1.AddRequest{Url: "https://example.com/x.zip"}); d.GetDir() != "/data/Archives" {
		t.Fatal(d.GetDir())
	}
	_, _ = update(m, next, func(s *downexv1.Settings) {
		s.Categories = []*downexv1.Category{{Name: "Video", Extensions: []string{"mp4"}, Dir: "/films"}}
	})
	if d := add(t, m, &downexv1.AddRequest{Url: "https://example.com/clip.mp4"}); d.GetDir() != "/films" {
		t.Fatal(d.GetDir())
	}
	_, _ = update(m, next, func(s *downexv1.Settings) { s.UseCategoryFolders = false })
	if d := add(t, m, &downexv1.AddRequest{Url: "https://example.com/y.zip"}); d.GetDir() != "/data" || d.GetCategory() != "Archives" {
		t.Fatal(d)
	}
}

// ---- categories and browser hand-offs -----------------------------------------------------

func TestChoosesTheCategory(t *testing.T) {
	m := mk(t)
	for _, c := range []struct {
		req  *downexv1.AddRequest
		want string
	}{
		{&downexv1.AddRequest{Url: "https://example.com/get?id=1", Filename: "talk.MKV"}, "Video"},
		{&downexv1.AddRequest{Url: "https://example.com/files/notes.pdf"}, "Documents"},
		{&downexv1.AddRequest{Url: "https://example.com/get?id=2", Mime: "audio/ogg"}, "Audio"},
		{&downexv1.AddRequest{Url: "https://example.com/get?id=3"}, ""},
	} {
		if got := add(t, m, c.req).GetCategory(); got != c.want {
			t.Errorf("%s: %q, want %q", c.req.GetUrl(), got, c.want)
		}
	}
}

func TestKeepsBrowserHandoffsWaiting(t *testing.T) {
	m := mk(t)
	var asked []int64
	off := m.OnConfirmRequest(func(id int64) { asked = append(asked, id) })
	d, err := m.BrowserHandoff(func(r *downexv1.AddRequest) {
		r.Url, r.Filename, r.ExpectedSize = "https://example.com/pub/movie.mp4", "movie.mp4", 5*MiB
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.GetState() != sAwaiting || d.GetSource() != srcBrowser || d.GetCategory() != "Video" || d.GetDir() != dir+"/Video" || d.GetExpectedSize() != 5*MiB || d.GetSize() != -1 {
		t.Fatal(d)
	}
	if !slices.Equal(asked, []int64{d.GetId()}) {
		t.Fatal(asked)
	}
	for range 5 {
		m.Tick(1000)
	}
	if get(t, m, d.GetId()).GetState() != sAwaiting {
		t.Fatal("started by the scheduler")
	}
	if add(t, m, &downexv1.AddRequest{Url: "https://example.com/a.zip", Source: downexv1.AddSource_ADD_SOURCE_UI}).GetState() != sQueued {
		t.Fatal("not queued")
	}
	off()
	_, _ = m.BrowserHandoff(nil)
	if len(asked) != 1 {
		t.Fatal(asked)
	}
}

func TestConfirmsStartLaterFolderAndName(t *testing.T) {
	m := mk(t)
	first, _ := m.BrowserHandoff(func(r *downexv1.AddRequest) {
		r.Url, r.Filename = "https://example.com/pub/a.zip?size=4194304", "a.zip"
	})
	c, err := m.Confirm(ctx, &downexv1.ConfirmRequest{Id: first.GetId(), Start: true, Dir: "/data/in", Filename: "renamed.zip"})
	if x := c.GetDownload(); err != nil || x.GetState() != sQueued || x.GetDir() != "/data/in" || x.GetFilename() != "renamed.zip" || x.GetSize() != 4*MiB {
		t.Fatal(x, err)
	}
	_, err = m.Confirm(ctx, &downexv1.ConfirmRequest{Id: first.GetId(), Start: true})
	wantCode(t, err, connect.CodeFailedPrecondition)
	runUntil(t, m, first.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sCompleted })
	second, _ := m.BrowserHandoff(nil)
	_, err = m.Confirm(ctx, &downexv1.ConfirmRequest{Id: second.GetId(), Start: true, Dir: "relative"})
	wantCode(t, err, connect.CodeInvalidArgument)
	c, _ = m.Confirm(ctx, &downexv1.ConfirmRequest{Id: second.GetId()})
	if c.GetDownload().GetState() != sPaused || c.GetDownload().GetDir() != second.GetDir() {
		t.Fatal(c.GetDownload())
	}
}

func TestStartsBrowserDownloadsWhenConfirmationIsOff(t *testing.T) {
	m := mk(t, withSettings(func(s *downexv1.Settings) { s.ConfirmBrowserDownloads = false }))
	if d, _ := m.BrowserHandoff(nil); d.GetState() != sQueued {
		t.Fatal(d.GetState())
	}
}

func TestPausesAndResumesAll(t *testing.T) {
	m := mk(t, withSettings(func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 2 }))
	for _, n := range []string{"f0", "f1", "f2"} {
		add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/" + n + ".iso"})
	}
	waiting, _ := m.BrowserHandoff(nil)
	m.Tick(1000)
	if r, _ := m.PauseAll(ctx); r.GetPaused() != 3 {
		t.Fatal(r)
	}
	l := list(t, m)
	if len(byState(l, sPaused)) != 3 || get(t, m, waiting.GetId()).GetState() != sAwaiting {
		t.Fatal("pause all")
	}
	if r, _ := m.PauseAll(ctx); r.GetPaused() != 0 {
		t.Fatal(r)
	}
	if r, _ := m.ResumeAll(ctx); r.GetResumed() != 3 {
		t.Fatal(r)
	}
	n := 0
	for _, d := range list(t, m) {
		if d.GetState() == sQueued || d.GetState() == sProbing {
			n++
		}
	}
	if n != 3 {
		t.Fatal(n)
	}
}

// ---- events -----------------------------------------------------------------------------

func kindsOf(b state.Batch, f func(*downexv1.Event) bool) int {
	n := 0
	for _, e := range b {
		if f(e) {
			n++
		}
	}
	return n
}

func TestSendsASnapshotThenCoalescedUpdates(t *testing.T) {
	m := mk(t, func(o *Options) { o.Count = 6 })
	var batches []state.Batch
	off := m.Subscribe(func(b state.Batch) { batches = append(batches, b) })
	if len(batches) != 1 || len(batches[0]) != 7 || kindsOf(batches[0], func(e *downexv1.Event) bool { return e.GetSnapshot() != nil }) != 6 {
		t.Fatalf("first batch %v", batches)
	}
	if tl := batches[0][6].GetTools(); tl == nil || !tl.GetEnabled() || tl.GetReady() {
		t.Fatal(batches[0][6])
	}
	add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/new.zip"})
	m.Tick(250)
	m.Tick(250)
	if len(batches) != 3 {
		t.Fatal(len(batches))
	}
	for _, b := range batches[1:] {
		seen := map[int64]bool{}
		for _, e := range b {
			var id int64
			switch {
			case e.GetUpdated() != nil:
				id = e.GetUpdated().GetId()
			case e.GetKind() != nil && e.GetSnapshot() != nil:
				t.Fatal("snapshot after the first batch")
			default:
				if r, ok := e.GetKind().(*downexv1.Event_Removed); ok {
					id = r.Removed
				} else {
					continue
				}
			}
			if seen[id] {
				t.Fatal("a download twice in a batch")
			}
			seen[id] = true
		}
	}
	if kindsOf(batches[1], func(e *downexv1.Event) bool { return e.GetQueueUpdated() != nil }) != 1 ||
		kindsOf(batches[2], func(e *downexv1.Event) bool { return e.GetQueueUpdated() != nil }) != 0 {
		t.Fatal("queue updates")
	}
	off()
	m.Tick(250)
	if len(batches) != 3 {
		t.Fatal("batch after unsubscribing")
	}
}

func TestEmitsRemovedEvents(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/gone.zip"})
	var batches []state.Batch
	m.Subscribe(func(b state.Batch) { batches = append(batches, b) })
	if _, err := m.Remove(ctx, &downexv1.RemoveRequest{Id: d.GetId(), DeleteFile: true}); err != nil {
		t.Fatal(err)
	}
	m.Tick(250)
	last := batches[len(batches)-1]
	if !slices.ContainsFunc(last, func(e *downexv1.Event) bool {
		r, ok := e.GetKind().(*downexv1.Event_Removed)
		return ok && r.Removed == d.GetId()
	}) {
		t.Fatal(last)
	}
	if len(list(t, m)) != 0 {
		t.Fatal("still listed")
	}
	_, err := m.Remove(ctx, &downexv1.RemoveRequest{Id: d.GetId()})
	wantCode(t, err, connect.CodeNotFound)
}

func TestSeedsARealisticList(t *testing.T) {
	m := mk(t, func(o *Options) { o.Count, o.Seed = 300, 7 })
	l := list(t, m)
	if len(l) != 300 {
		t.Fatal(len(l))
	}
	for _, s := range []downexv1.DownloadState{sCompleted, sPaused, sFailed, sQueued, sDownloading} {
		if len(byState(l, s)) == 0 {
			t.Errorf("no %v", s)
		}
	}
	if len(byState(l, sDownloading))+len(byState(l, sProbing)) > 3 {
		t.Fatal("too many running")
	}
	ids := map[int64]bool{}
	unknown := false
	for _, d := range l {
		if d.GetState() == sCompleted && d.GetReceived() != d.GetSize() {
			t.Fatal(d)
		}
		if d.GetState() == sFailed && d.GetErrorCode() == "" {
			t.Fatal(d)
		}
		checkSegments(t, d)
		unknown = unknown || d.GetSize() == -1
		ids[d.GetId()] = true
	}
	if !unknown || len(ids) != 300 {
		t.Fatal("sizes or ids")
	}
}

// ---- streaming media ----------------------------------------------------------------------

const (
	hlsURL  = "https://media.example.org/talks/keynote/master.m3u8"
	dashURL = "https://media.example.org/films/coast/manifest.mpd"
)

func probe(m *Mock, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	r, err := m.ProbeMedia(ctx, req)
	return r.GetMedia(), err
}

func TestProbesHLSAndDASH(t *testing.T) {
	m := mk(t)
	hls, err := probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: hlsURL})
	if err != nil || hls.GetFormat() != "hls" || hls.GetContainer() != "mp4" || hls.GetSelectedVideo() != "v2" || hls.GetSelectedAudio() != "r0" || hls.GetTitle() != "keynote" {
		t.Fatal(hls, err)
	}
	var kinds []string
	for _, tr := range hls.GetTracks() {
		kinds = append(kinds, tr.GetKind())
	}
	if !slices.Equal(kinds, []string{"video", "video", "video", "audio", "audio", "audio"}) || hls.GetDurationMs() <= 0 ||
		hls.GetFragmentsTotal() != int32(2*math.Ceil(float64(hls.GetDurationMs())/6000)) {
		t.Fatal(kinds, hls.GetFragmentsTotal())
	}
	again, _ := probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: hlsURL})
	if !proto.Equal(again, hls) {
		t.Fatal("not stable")
	}
	dash, _ := probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: dashURL})
	if dash.GetFormat() != "dash" || dash.GetSelectedVideo() != "video-1080p" || dash.GetSelectedAudio() != "audio-en" || dash.GetTitle() != "coast" {
		t.Fatal(dash)
	}
	muxed, _ := probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: "https://media.example.org/news/muxed/index.m3u8"})
	if muxed.GetContainer() != "mp4" || muxed.GetSelectedAudio() != "" {
		t.Fatal(muxed)
	}
	for _, tr := range muxed.GetTracks() {
		if tr.GetKind() != "muxed" {
			t.Fatal(tr)
		}
	}
	if len(list(t, m)) != 0 {
		t.Fatal("probing added")
	}
}

func TestRefusesEncryptedAndLiveStreams(t *testing.T) {
	m := mk(t)
	_, err := probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: "https://media.example.org/drm/master.m3u8"})
	wantMessage(t, err, connect.CodeInvalidArgument, `^encrypted: `)
	_, err = m.AddMedia(ctx, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/encrypted.mpd"})
	wantMessage(t, err, connect.CodeInvalidArgument, `^encrypted: `)
	_, err = m.AddMedia(ctx, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/live/channel.m3u8"})
	wantMessage(t, err, connect.CodeInvalidArgument, `^live: `)
	_, err = probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: "https://media.example.org/video.mp4"})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = probe(m, &downexv1.ProbeMediaRequest{ManifestUrl: "ftp://media.example.org/a.m3u8"})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = m.AddMedia(ctx, &downexv1.AddMediaRequest{ManifestUrl: hlsURL, VideoTrack: "r0"})
	wantCode(t, err, connect.CodeInvalidArgument)
	if len(list(t, m)) != 0 {
		t.Fatal("added")
	}
}

func TestDownloadsTheFragmentsAndAssembles(t *testing.T) {
	m := mk(t)
	added := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: hlsURL, Source: downexv1.AddSource_ADD_SOURCE_UI, VideoTrack: "v0"})
	if added.GetKind() != kMedia || added.GetState() != sQueued || added.GetFilename() != "keynote.mp4" || added.GetCategory() != "Video" ||
		added.GetDir() != dir+"/Video" || added.GetSize() != -1 || added.GetSource() != downexv1.AddSource_ADD_SOURCE_UI {
		t.Fatal(added)
	}
	if mi := added.GetMedia(); mi.GetSelectedVideo() != "v0" || mi.GetSelectedAudio() != "r0" || mi.GetFragmentsDone() != 0 {
		t.Fatal(mi)
	}
	total := added.GetMedia().GetFragmentsTotal()
	running := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool {
		return d.GetState() == sDownloading && d.GetMedia().GetFragmentsDone() > 0
	})
	if len(running.GetSegments()) != 0 || running.GetConnections() <= 0 || running.GetMedia().GetFragmentsDone() >= total {
		t.Fatal(running)
	}
	done := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sCompleted })
	if done.GetMedia().GetFragmentsDone() != total || done.GetSize() != done.GetReceived() || done.GetSize() <= 0 {
		t.Fatal(done)
	}
}

func TestNamesStreamsByTitle(t *testing.T) {
	m := mk(t)
	if d := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: hlsURL, Filename: "My talk", StartPaused: true}); d.GetFilename() != "My talk.mp4" || d.GetState() != sPaused {
		t.Fatal(d)
	}
	if d := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: hlsURL, Title: "Opening keynote"}); d.GetFilename() != "Opening keynote.mp4" {
		t.Fatal(d.GetFilename())
	}
	podcast := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/podcasts/ep-12/audio.m3u8"})
	if podcast.GetCategory() != "Audio" || podcast.GetDir() != dir+"/Audio" || podcast.GetFilename() != "ep-12.mp4" ||
		podcast.GetMedia().GetSelectedVideo() != "" || podcast.GetMedia().GetSelectedAudio() != "r0" {
		t.Fatal(podcast)
	}
	news := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/news/evening/muxed.m3u8"})
	if news.GetFilename() != "muxed.mp4" || news.GetCategory() != "Video" || news.GetMedia().GetSelectedAudio() != "" || news.GetMedia().GetContainer() != "mp4" {
		t.Fatal(news)
	}
}

func TestHoldsStreamHandoffsAndTakesConfirmedTracks(t *testing.T) {
	m := mk(t)
	var asked []int64
	m.OnConfirmRequest(func(id int64) { asked = append(asked, id) })
	held, err := m.BrowserMediaHandoff(func(r *downexv1.AddMediaRequest) { r.ManifestUrl = dashURL })
	if err != nil || held.GetState() != sAwaiting || held.GetKind() != kMedia || held.GetSource() != srcBrowser || !slices.Equal(asked, []int64{held.GetId()}) {
		t.Fatal(held, err, asked)
	}
	c, _ := m.Confirm(ctx, &downexv1.ConfirmRequest{Id: held.GetId(), Start: true, VideoTrack: "video-360p", AudioTrack: "audio-es"})
	if x := c.GetDownload(); x.GetState() != sQueued || x.GetMedia().GetSelectedVideo() != "video-360p" || x.GetMedia().GetSelectedAudio() != "audio-es" ||
		x.GetMedia().GetFragmentsTotal() != held.GetMedia().GetFragmentsTotal() {
		t.Fatal(x)
	}
	other, _ := m.BrowserMediaHandoff(func(r *downexv1.AddMediaRequest) { r.ManifestUrl = "https://media.example.org/news/muxed/index.m3u8" })
	c, _ = m.Confirm(ctx, &downexv1.ConfirmRequest{Id: other.GetId()})
	if mi := c.GetDownload().GetMedia(); mi.GetSelectedVideo() != "v2" || mi.GetSelectedAudio() != "" {
		t.Fatal(mi)
	}
	file, _ := m.BrowserHandoff(nil)
	_, err = m.Confirm(ctx, &downexv1.ConfirmRequest{Id: file.GetId(), Start: true, VideoTrack: "v0"})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestFailsAnEncryptedInitAndRestartsStreams(t *testing.T) {
	m := mk(t)
	sealed := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/init-protected/master.m3u8"})
	if f := runUntil(t, m, sealed.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sFailed }); f.GetErrorCode() != "encrypted" || f.GetReceived() != 0 {
		t.Fatal(f)
	}
	flaky := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/flaky/talk/master.m3u8"})
	broken := runUntil(t, m, flaky.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sFailed })
	if broken.GetErrorCode() != "network" || broken.GetMedia().GetFragmentsDone() <= 0 {
		t.Fatal(broken)
	}
	r, _ := m.Resume(ctx, &downexv1.ResumeRequest{Id: flaky.GetId(), Restart: true})
	if x := r.GetDownload(); x.GetState() != sQueued || x.GetReceived() != 0 || x.GetSize() != -1 || x.GetMedia().GetFragmentsDone() != 0 {
		t.Fatal(x)
	}
	done := runUntil(t, m, flaky.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sCompleted })
	if done.GetMedia().GetFragmentsDone() != done.GetMedia().GetFragmentsTotal() {
		t.Fatal(done)
	}
}

func TestSeedsConsistentMediaDownloads(t *testing.T) {
	m := mk(t, func(o *Options) { o.Count, o.Seed = 60, 5 })
	n := 0
	for _, d := range list(t, m) {
		if d.GetKind() != kMedia {
			continue
		}
		n++
		mi := d.GetMedia()
		if mi.GetFragmentsDone() > mi.GetFragmentsTotal() || len(d.GetSegments()) != 0 {
			t.Fatal(d)
		}
		if d.GetState() == sCompleted {
			if mi.GetFragmentsDone() != mi.GetFragmentsTotal() || d.GetSize() != d.GetReceived() {
				t.Fatal(d)
			}
		} else if d.GetSize() != -1 {
			t.Fatal(d)
		}
	}
	if n == 0 {
		t.Fatal("no media")
	}
}

// ---- video pages --------------------------------------------------------------------------

const page = "https://www.youtube.com/watch?v=aqz-KE-bpKQ"

func siteReq(u string) *downexv1.ProbeMediaRequest {
	return &downexv1.ProbeMediaRequest{PageUrl: u, Origin: site}
}

func TestSetsUpToolsThenOffersOnePerClass(t *testing.T) {
	m := mk(t)
	var events []state.Batch
	m.Subscribe(func(b state.Batch) { events = append(events, b) })
	_, err := probe(m, siteReq(page))
	wantMessage(t, err, connect.CodeFailedPrecondition, `^tools_installing: downloading yt-dlp and Deno \(0%\)$`)
	tl, _ := m.GetTools(ctx)
	for _, x := range tl.GetStatus().GetTools() {
		if x.GetState() != downexv1.ToolState_TOOL_STATE_INSTALLING {
			t.Fatal(x)
		}
	}
	m.Tick(2000)
	_, err = probe(m, siteReq(page))
	wantMessage(t, err, connect.CodeFailedPrecondition, `^tools_installing: downloading yt-dlp and Deno \(50%\)$`)
	last := events[len(events)-1]
	ts := last[len(last)-1].GetTools()
	if ts == nil || ts.GetReady() || ts.GetTools()[0].GetBytesDone() != 9*MiB || ts.GetTools()[0].GetBytesTotal() != 18*MiB {
		t.Fatal(ts)
	}
	m.Tick(2000)
	last = events[len(events)-1]
	if ts = last[len(last)-1].GetTools(); !ts.GetReady() || ts.GetTools()[0].GetVerified() != "openpgp+sha256" {
		t.Fatal(ts)
	}
	_, err = probe(m, siteReq(page))
	wantMessage(t, err, connect.CodeFailedPrecondition, `^site_resolving: still getting the video info$`)
	m.Tick(1200)
	info, err := probe(m, siteReq(page))
	if err != nil || info.GetOrigin() != site || info.GetSite() != "YouTube" || info.GetPageUrl() != page || info.GetFormat() != "http" ||
		info.GetContainer() != "mp4" || info.GetManifestUrl() != "" || info.GetFragmentsTotal() != 0 {
		t.Fatal(info, err)
	}
	var videos []*downexv1.MediaTrack
	for _, tr := range info.GetTracks() {
		if !regexp.MustCompile(`^(mp4|m4a)$`).MatchString(tr.GetExt()) || !regexp.MustCompile(`^(avc1|av01|mp4a)`).MatchString(tr.GetCodecs()) || tr.GetSize() <= 0 {
			t.Fatal(tr)
		}
		if tr.GetKind() != "audio" {
			videos = append(videos, tr)
		}
	}
	if len(videos) < 5 {
		t.Fatal(len(videos))
	}
	var classes []int
	for _, v := range videos {
		c, _, _, ok := imedia.ParseName(v.GetName())
		if !ok || c <= 0 {
			t.Fatal(v.GetName())
		}
		classes = append(classes, c)
		if v.GetHeight() <= 1080 && !strings.HasPrefix(v.GetCodecs(), "avc1") || v.GetKind() == "muxed" {
			t.Fatal(v)
		}
	}
	if !sort.SliceIsSorted(classes, func(i, j int) bool { return classes[i] > classes[j] }) || classes[len(classes)-1] != 144 {
		t.Fatal(classes)
	}
	for i := 1; i < len(classes); i++ {
		if classes[i] == classes[i-1] {
			t.Fatal("a class twice")
		}
	}
	if info.GetSelectedVideo() != videos[0].GetId() || info.GetSelectedAudio() != "140" {
		t.Fatal(info.GetSelectedVideo(), info.GetSelectedAudio())
	}
	cached, _ := probe(m, siteReq(page))
	if !proto.Equal(cached, info) || len(list(t, m)) != 0 {
		t.Fatal("cache or list")
	}
}

func TestNamesAFilmsQualitiesByClass(t *testing.T) {
	m := mk(t, func(o *Options) { o.ToolsReady, o.ResolveMs = true, zero() })
	for _, id := range []string{"film-1", "film-2", "film-3", "movie-4", "movie-5"} {
		info, err := probe(m, siteReq("https://www.youtube.com/watch?v="+id))
		if err != nil {
			t.Fatal(err)
		}
		var videos []*downexv1.MediaTrack
		for _, tr := range info.GetTracks() {
			if tr.GetKind() != "audio" {
				videos = append(videos, tr)
			}
		}
		for _, v := range videos {
			if float64(v.GetWidth())/float64(v.GetHeight()) <= 2.3 || v.GetName() != standardName(v.GetWidth(), v.GetHeight(), v.GetFrameRate(), false) ||
				strings.Contains(v.GetName(), itoa(v.GetHeight())+"p") {
				t.Fatal(v)
			}
		}
		if videos[len(videos)-1].GetName() != "144p" {
			t.Fatal(videos[len(videos)-1].GetName())
		}
	}
	var top *downexv1.MediaTrack
	for i := 1; i <= 30 && top.GetWidth() != 3840; i++ {
		info, _ := probe(m, siteReq("https://www.youtube.com/watch?v=film-"+itoa(int32(i))))
		top = info.GetTracks()[0]
	}
	if top.GetWidth() != 3840 || top.GetHeight() != 1634 || !regexp.MustCompile(`^4K \(2160p\)( 60fps)?$`).MatchString(top.GetName()) {
		t.Fatal(top)
	}
}

func itoa(n int32) string { return strconv.Itoa(int(n)) }

func TestRefusesPagesAsYtDlpWould(t *testing.T) {
	m := mk(t, func(o *Options) { o.ToolsReady, o.ResolveMs = true, zero() })
	for _, c := range []struct {
		url  string
		code connect.Code
		re   string
	}{
		{"https://video.example.org/drm/1", connect.CodeInvalidArgument, `^encrypted: `},
		{"https://video.example.org/live/1", connect.CodeInvalidArgument, `^live: `},
		{"https://www.youtube.com/playlist?list=PL1", connect.CodeInvalidArgument, `^media_unsupported: a playlist`},
		{"https://podcasts.example.org/ep-1", connect.CodeInvalidArgument, `^media_unsupported: no MP4 video`},
		{"https://video.example.org/private/1", connect.CodeFailedPrecondition, `^site_unavailable: `},
		{"https://video.example.org/signin/1", connect.CodeFailedPrecondition, `^auth_required: `},
		{"https://video.example.org/unsupported", connect.CodeInvalidArgument, `^site_unsupported: `},
		{"https://video.example.org/broken", connect.CodeFailedPrecondition, `^site_error: yt-dlp could not read this page: `},
	} {
		_, err := probe(m, siteReq(c.url))
		wantMessage(t, err, c.code, c.re)
	}
	_, err := probe(m, &downexv1.ProbeMediaRequest{PageUrl: page, Origin: site, ManifestUrl: "https://media.example.org/a.m3u8"})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = probe(m, siteReq("ftp://video.example.org/a"))
	wantCode(t, err, connect.CodeInvalidArgument)
	_, _ = update(m, settingsOf(t, m), func(s *downexv1.Settings) { s.SiteMedia = false })
	_, err = probe(m, siteReq(page))
	wantMessage(t, err, connect.CodeFailedPrecondition, `^site_media_off: `)
	tl, _ := m.GetTools(ctx)
	for _, x := range tl.GetStatus().GetTools() {
		if x.GetState() != downexv1.ToolState_TOOL_STATE_DISABLED {
			t.Fatal(x)
		}
	}
	_, err = m.InstallTools(ctx, &downexv1.InstallToolsRequest{})
	wantMessage(t, err, connect.CodeFailedPrecondition, `^site_media_off: `)
}

func TestAddsAVideoAsOneMP4(t *testing.T) {
	m := mk(t, func(o *Options) { o.ToolsReady, o.ResolveMs = true, zero() })
	held, err := m.BrowserVideoHandoff(func(r *downexv1.AddMediaRequest) { r.PageUrl = page })
	if err != nil || held.GetState() != sAwaiting || held.GetKind() != kMedia || held.GetUrl() != page || held.GetCategory() != "Video" ||
		held.GetFilename() != "Video aqz-KE-bpKQ.mp4" || held.GetMedia().GetOrigin() != site || held.GetMedia().GetContainer() != "mp4" {
		t.Fatal(held, err)
	}
	offer, _ := probe(m, siteReq(page))
	var videos []*downexv1.MediaTrack
	var audio *downexv1.MediaTrack
	for _, tr := range offer.GetTracks() {
		if tr.GetKind() == "video" {
			videos = append(videos, tr)
		}
		if tr.GetId() == "140" {
			audio = tr
		}
	}
	if held.GetExpectedSize() != videos[0].GetSize()+audio.GetSize() {
		t.Fatal(held.GetExpectedSize())
	}
	low := videos[len(videos)-1]
	c, _ := m.Confirm(ctx, &downexv1.ConfirmRequest{Id: held.GetId(), VideoTrack: low.GetId(), AudioTrack: "140"})
	other := c.GetDownload()
	parts := func(v *downexv1.MediaTrack) int32 {
		return int32(math.Ceil(float64(v.GetSize())/(10*MiB)) + math.Ceil(float64(audio.GetSize())/(10*MiB)))
	}
	if other.GetFilename() != "Video aqz-KE-bpKQ.mp4" || other.GetState() != sPaused || other.GetMedia().GetSelectedVideo() != low.GetId() ||
		other.GetMedia().GetSelectedAudio() != "140" || other.GetMedia().GetFragmentsTotal() != parts(low) || other.GetExpectedSize() != low.GetSize()+audio.GetSize() {
		t.Fatal(other)
	}
	v := videos[1]
	added := addMedia(t, m, &downexv1.AddMediaRequest{PageUrl: page, Origin: site, VideoTrack: v.GetId(), Source: downexv1.AddSource_ADD_SOURCE_UI})
	if added.GetState() != sQueued || added.GetFilename() != "Video aqz-KE-bpKQ.mp4" || added.GetDir() != dir+"/Video" || added.GetSize() != -1 || added.GetMedia().GetFragmentsTotal() != parts(v) {
		t.Fatal(added)
	}
	if p := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sProbing }); p.GetSize() != -1 {
		t.Fatal(p.GetSize())
	}
	if r := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sDownloading }); r.GetSize() != v.GetSize()+audio.GetSize() {
		t.Fatal(r.GetSize())
	}
	done := runUntil(t, m, added.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sCompleted })
	if done.GetMedia().GetFragmentsDone() != parts(v) || done.GetSize() != done.GetReceived() {
		t.Fatal(done)
	}
}

func TestInstallsOnRequestAndReportsAFailedInstall(t *testing.T) {
	m := mk(t)
	got := make(chan *downexv1.InstallToolsResponse, 1)
	go func() {
		r, _ := m.InstallTools(ctx, &downexv1.InstallToolsRequest{})
		got <- r
	}()
	waitUntil(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.toolsWaiters) == 1 })
	m.Tick(2000)
	m.Tick(2000)
	if r := <-got; !r.GetStatus().GetReady() || !r.GetStatus().GetEnabled() {
		t.Fatal(r)
	}
	if r, _ := m.InstallTools(ctx, &downexv1.InstallToolsRequest{Update: true}); !r.GetStatus().GetReady() {
		t.Fatal(r)
	}

	b := mk(t, func(o *Options) { o.ToolsFail, o.ToolsInstallMs = true, num(1000) })
	errc := make(chan error, 1)
	go func() {
		_, err := b.InstallTools(ctx, &downexv1.InstallToolsRequest{})
		errc <- err
	}()
	waitUntil(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.toolsWaiters) == 1 })
	b.Tick(1000)
	wantMessage(t, <-errc, connect.CodeFailedPrecondition, `^tools_unavailable: `)
	tl, _ := b.GetTools(ctx)
	if x := tl.GetStatus().GetTools()[0]; x.GetState() != downexv1.ToolState_TOOL_STATE_FAILED || x.GetErrorCode() != "network" || x.GetError() != "could not download yt-dlp (network)" {
		t.Fatal(x)
	}
	_, err := probe(b, siteReq(page))
	wantMessage(t, err, connect.CodeFailedPrecondition, `^tools_unavailable: `)
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for range 2000 {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// ---- progress windows ---------------------------------------------------------------------

func indexes(d *downexv1.Download) []int32 {
	var out []int32
	for _, c := range d.GetConnectionInfo() {
		out = append(out, c.GetIndex())
	}
	return out
}

func TestReportsOneConnectionPerWorkerSlot(t *testing.T) {
	m := mk(t, func(o *Options) { o.RetryRate = zero() })
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/big.iso?size=67108864", MaxConnections: 4})
	m.Tick(100)
	runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sDownloading })
	m.Tick(250)
	cur := get(t, m, d.GetId())
	if !slices.Equal(indexes(cur), []int32{1, 2, 3, 4}) {
		t.Fatal(indexes(cur))
	}
	for _, c := range cur.GetConnectionInfo() {
		if c.GetState() != connConnecting {
			t.Fatal(c)
		}
	}
	m.Tick(1000)
	cur = get(t, m, d.GetId())
	for _, c := range cur.GetConnectionInfo() {
		if c.GetState() != connReceiving || c.GetFragment() != -1 || c.GetStart() < 0 || c.GetEnd() < c.GetStart() || c.GetReceived() <= 0 || c.GetReceived() > c.GetEnd()-c.GetStart()+1 ||
			!slices.ContainsFunc(cur.GetSegments(), func(s *downexv1.Segment) bool { return s.GetStart() == c.GetStart() }) {
			t.Fatal(c)
		}
	}
	if cur.GetConnections() != 4 {
		t.Fatal(cur.GetConnections())
	}
	for range 2000 {
		m.Tick(1000)
		cur = get(t, m, d.GetId())
		if len(cur.GetConnectionInfo()) > 4 {
			t.Fatal("too many connections")
		}
		if cur.GetState() != sDownloading {
			break
		}
	}
	if cur.GetState() != sCompleted || len(cur.GetConnectionInfo()) != 0 {
		t.Fatal(cur)
	}
}

func TestLetsConnectionsWaitAndRetry(t *testing.T) {
	m := mk(t, func(o *Options) { o.RetryRate = num(5) })
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/flaky-server.bin?size=16777216"})
	runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sDownloading })
	m.Tick(1000)
	n := 0
	for _, c := range get(t, m, d.GetId()).GetConnectionInfo() {
		if c.GetState() == connRetrying {
			n++
			if !regexp.MustCompile(`^(HTTP 503|connection reset|timed out), retrying in \d s$`).MatchString(c.GetDetail()) {
				t.Fatal(c.GetDetail())
			}
		}
	}
	if n == 0 {
		t.Fatal("none retrying")
	}
	p, _ := m.Pause(ctx, &downexv1.PauseRequest{Id: d.GetId()})
	if len(p.GetDownload().GetConnectionInfo()) != 0 {
		t.Fatal("connections while paused")
	}
	m.Tick(1000)
	if len(get(t, m, d.GetId()).GetConnectionInfo()) != 0 {
		t.Fatal("connections while paused")
	}
}

func TestChangesTheLimitsOfARunningDownload(t *testing.T) {
	m := mk(t, func(o *Options) { o.RetryRate = zero() })
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/limits.mkv?size=2147483648", MaxConnections: 8})
	runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sDownloading && x.GetReceived() > 0 })
	opt := func(req *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
		req.Id = d.GetId()
		r, err := m.SetDownloadOptions(ctx, req)
		return r.GetDownload(), err
	}
	limited, _ := opt(&downexv1.SetDownloadOptionsRequest{SpeedLimit: proto.Int64(128 * KiB)})
	if limited.GetSpeedLimit() != 128*KiB || limited.GetMaxConnections() != 8 || limited.GetState() != sDownloading {
		t.Fatal(limited)
	}
	m.Tick(1000)
	start := get(t, m, d.GetId())
	for range 4 {
		m.Tick(1000)
	}
	if got := get(t, m, d.GetId()).GetReceived() - start.GetReceived(); got > 4*128*KiB {
		t.Fatal(got)
	}
	if x, _ := opt(&downexv1.SetDownloadOptionsRequest{MaxConnections: proto.Int32(2)}); x.GetMaxConnections() != 2 {
		t.Fatal(x)
	}
	m.Tick(1000)
	fewer := get(t, m, d.GetId())
	if !slices.Equal(indexes(fewer), []int32{1, 2}) || fewer.GetConnections() > 2 {
		t.Fatal(indexes(fewer))
	}
	if x, _ := opt(&downexv1.SetDownloadOptionsRequest{SpeedLimit: proto.Int64(0), MaxConnections: proto.Int32(0)}); x.GetSpeedLimit() != 0 {
		t.Fatal(x)
	}
	_, err := opt(&downexv1.SetDownloadOptionsRequest{MaxConnections: proto.Int32(33)})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = opt(&downexv1.SetDownloadOptionsRequest{SpeedLimit: proto.Int64(-1)})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = m.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: 999, SpeedLimit: proto.Int64(1)})
	wantCode(t, err, connect.CodeNotFound)
}

func TestReportsTheFragmentOfEachMediaConnection(t *testing.T) {
	m := mk(t, func(o *Options) { o.RetryRate, o.AssembleMs = zero(), num(1500) })
	added := addMedia(t, m, &downexv1.AddMediaRequest{ManifestUrl: hlsURL, VideoTrack: "v0"})
	running := runUntil(t, m, added.GetId(), func(x *downexv1.Download) bool {
		return x.GetState() == sDownloading && x.GetMedia().GetFragmentsDone() > 0
	})
	if int32(len(running.GetConnectionInfo())) != running.GetConnections() {
		t.Fatal(len(running.GetConnectionInfo()), running.GetConnections())
	}
	done := running.GetMedia().GetFragmentsDone()
	for i, c := range running.GetConnectionInfo() {
		if c.GetFragment() != done+int32(i) {
			t.Fatal(i, c.GetFragment())
		}
	}
	total := running.GetMedia().GetFragmentsTotal()
	asm := runUntil(t, m, added.GetId(), func(x *downexv1.Download) bool { return x.GetMedia().GetFragmentsDone() == total })
	if asm.GetState() != sDownloading || asm.GetConnections() != 0 || len(asm.GetConnectionInfo()) != 0 || asm.GetSpeedBps() != 0 {
		t.Fatal(asm)
	}
	m.Tick(1000)
	if get(t, m, added.GetId()).GetState() != sDownloading {
		t.Fatal("completed too soon")
	}
	m.Tick(1000)
	if c := get(t, m, added.GetId()); c.GetState() != sCompleted || c.GetSize() != c.GetReceived() {
		t.Fatal(c)
	}
}

func TestReadsOneDownloadAndRecordsTheWindow(t *testing.T) {
	m := mk(t, func(o *Options) { o.View = &Startup{Progress: 3, OpenWith: true} })
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/one.zip"})
	if g, _ := m.Get(ctx, d.GetId()); g.GetFilename() != "one.zip" {
		t.Fatal(g)
	}
	_, err := m.Get(ctx, 999)
	wantCode(t, err, connect.CodeNotFound)
	v := m.Startup()
	if v.Progress != 3 || !v.OpenWith || v.Hibernate || v.WakeTimers || v.Build.Version != "0.0.0-mock" || v.Build.Arch != "amd64" {
		t.Fatal(v)
	}
	if v := mk(t).Startup(); v.Progress != 0 || !v.OpenWith || !v.Hibernate || !v.WakeTimers {
		t.Fatal(v)
	}
	b := mk(t)
	added := add(t, b, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/two.zip"})
	if err := b.OpenProgressWindow(added.GetId()); err != nil {
		t.Fatal(err)
	}
	wantCode(t, b.OpenProgressWindow(999), connect.CodeNotFound)
	b.SetTitle("3% two.zip")
	b.SetSize(520, 560)
	b.CloseWindow()
	w := b.Window()
	if !slices.Equal(w.Opened, []int64{added.GetId()}) || w.Title != "3% two.zip" || !w.Closed || len(w.Sizes) != 1 || w.Width != 520 || w.Height != 560 {
		t.Fatal(w)
	}
}

// ---- download complete dialog ---------------------------------------------------------------

func TestMarksForTheDialogAndCountsRunningTime(t *testing.T) {
	clock := int64(1_800_000_000_000)
	m := mk(t, func(o *Options) {
		o.RetryRate = zero()
		o.Now = func() time.Time { return time.UnixMilli(clock) }
	})
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/marked.iso?size=67108864"})
	if d.GetCompletionDialog() || d.GetActiveMs() != 0 || d.GetStartedAt() != nil {
		t.Fatal(d)
	}
	opt := func(req *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
		req.Id = d.GetId()
		r, err := m.SetDownloadOptions(ctx, req)
		return r.GetDownload(), err
	}
	if x, _ := opt(&downexv1.SetDownloadOptionsRequest{CompletionDialog: proto.Bool(true)}); !x.GetCompletionDialog() {
		t.Fatal(x)
	}
	if x, _ := opt(&downexv1.SetDownloadOptionsRequest{SpeedLimit: proto.Int64(0)}); !x.GetCompletionDialog() {
		t.Fatal(x)
	}
	clock += 1000
	m.Tick(1000)
	started := get(t, m, d.GetId())
	if started.GetStartedAt().AsTime().UnixMilli() != clock || started.GetActiveMs() != 1000 {
		t.Fatal(started)
	}
	_, _ = m.Pause(ctx, &downexv1.PauseRequest{Id: d.GetId()})
	for range 5 {
		m.Tick(1000)
	}
	if get(t, m, d.GetId()).GetActiveMs() != 1000 {
		t.Fatal("counted a pause")
	}
	_, _ = m.Resume(ctx, &downexv1.ResumeRequest{Id: d.GetId()})
	clock += 5000
	done := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	if !proto.Equal(done.GetStartedAt(), started.GetStartedAt()) || done.GetActiveMs() <= 1000 || !done.GetCompletionDialog() {
		t.Fatal(done)
	}
	_, err := opt(&downexv1.SetDownloadOptionsRequest{CompletionDialog: proto.Bool(true)})
	wantCode(t, err, connect.CodeFailedPrecondition)
}

func TestOpensCompletedDownloadsWithAnotherAppOnly(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/report.pdf?size=1048576"})
	wantCode(t, m.OpenWith(ctx, d.GetId()), connect.CodeFailedPrecondition)
	runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	if err := m.OpenWith(ctx, d.GetId()); err != nil {
		t.Fatal(err)
	}
	wantCode(t, m.OpenWith(ctx, 999), connect.CodeNotFound)
}

func TestGivesSeededHistoryItsRunningTime(t *testing.T) {
	m := mk(t, func(o *Options) { o.Count = 40 })
	for _, d := range list(t, m) {
		started, completed := d.GetStartedAt().AsTime().UnixMilli(), d.GetCompletedAt().AsTime().UnixMilli()
		if d.GetState() == sCompleted && d.GetKind() != kMedia {
			if d.GetStartedAt() == nil || completed < started || d.GetActiveMs() <= 0 || d.GetActiveMs() > completed-started {
				t.Fatal(d)
			}
		}
		if d.GetState() == sQueued && d.GetStartedAt() == nil && d.GetActiveMs() != 0 {
			t.Fatal(d)
		}
	}
}

// ---- queues ---------------------------------------------------------------------------------

func queueSpec(name string) *downexv1.Queue {
	return &downexv1.Queue{Name: name, MaxConcurrent: 1, Schedule: &downexv1.Schedule{}}
}

func queues(t *testing.T, m *Mock) []*downexv1.Queue {
	t.Helper()
	r, err := m.ListQueues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return r.GetQueues()
}

func TestHasAMainAndANightQueue(t *testing.T) {
	m := mk(t)
	qs := queues(t, m)
	if qs[0].GetId() != 1 || !qs[0].GetIsMain() || !qs[0].GetRunning() || qs[1].GetName() != "Night" || qs[1].GetRunning() ||
		!qs[1].GetSchedule().GetEnabled() || qs[1].GetSchedule().GetStartMinute() != 60 || qs[1].GetSchedule().GetStopMinute() != 420 {
		t.Fatal(qs)
	}
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/a.zip"})
	if d.GetQueueId() != 1 || !slices.Equal(queues(t, m)[0].GetDownloadIds(), []int64{d.GetId()}) {
		t.Fatal(d)
	}
}

func TestStartsAQueueOnlyWhileItRuns(t *testing.T) {
	m := mk(t, withSettings(func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 3 }))
	c, _ := m.CreateQueue(ctx, &downexv1.CreateQueueRequest{Queue: queueSpec("Later")})
	q := c.GetQueue()
	var ids []int64
	for _, n := range []string{"q0", "q1", "q2"} {
		ids = append(ids, add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/" + n + ".iso", QueueId: q.GetId()}).GetId())
	}
	m.Tick(250)
	for _, d := range list(t, m) {
		if d.GetState() != sQueued {
			t.Fatal(d)
		}
	}
	mv, _ := m.MoveDownloads(ctx, &downexv1.MoveDownloadsRequest{Ids: []int64{ids[2]}, QueueId: q.GetId(), Position: 0})
	if !slices.Equal(mv.GetQueue().GetDownloadIds(), []int64{ids[2], ids[0], ids[1]}) {
		t.Fatal(mv.GetQueue().GetDownloadIds())
	}
	_, _ = m.StartQueue(ctx, &downexv1.StartQueueRequest{Id: q.GetId()})
	m.Tick(250)
	l := list(t, m)
	var probing []int64
	for _, d := range byState(l, sProbing) {
		probing = append(probing, d.GetId())
	}
	if !slices.Equal(probing, []int64{ids[2]}) || get(t, m, ids[0]).GetQueuePosition() != 1 {
		t.Fatal(probing)
	}
	_, _ = m.StopQueue(ctx, &downexv1.StopQueueRequest{Id: q.GetId()})
	if get(t, m, ids[2]).GetState() != sQueued {
		t.Fatal("still running")
	}
	_, err := m.CreateQueue(ctx, &downexv1.CreateQueueRequest{Queue: queueSpec("later")})
	wantCode(t, err, connect.CodeAlreadyExists)
	bad := queueSpec("Other")
	bad.MaxConcurrent = 17
	_, err = m.CreateQueue(ctx, &downexv1.CreateQueueRequest{Queue: bad})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestMovesDownloadsBetweenQueues(t *testing.T) {
	m := mk(t)
	d1 := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/1.zip"})
	d2 := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/2.zip"})
	c, _ := m.CreateQueue(ctx, &downexv1.CreateQueueRequest{Queue: queueSpec("Later")})
	q := c.GetQueue()
	_, _ = m.MoveDownloads(ctx, &downexv1.MoveDownloadsRequest{Ids: []int64{d2.GetId(), d1.GetId()}, QueueId: q.GetId(), Position: -1})
	if get(t, m, d1.GetId()).GetQueueId() != q.GetId() || len(queues(t, m)[0].GetDownloadIds()) != 0 {
		t.Fatal("not moved")
	}
	_, _ = m.DeleteQueue(ctx, &downexv1.DeleteQueueRequest{Id: q.GetId()})
	if get(t, m, d1.GetId()).GetQueueId() != 1 || !slices.Equal(queues(t, m)[0].GetDownloadIds(), []int64{d2.GetId(), d1.GetId()}) {
		t.Fatal("not back in the main queue")
	}
	_, err := m.DeleteQueue(ctx, &downexv1.DeleteQueueRequest{Id: 1})
	wantCode(t, err, connect.CodeFailedPrecondition)
	_, err = m.MoveDownloads(ctx, &downexv1.MoveDownloadsRequest{Ids: []int64{999}, QueueId: 1})
	wantCode(t, err, connect.CodeNotFound)
}

func TestConfirmsDownloadLaterIntoAStoppedQueue(t *testing.T) {
	m := mk(t)
	night := queues(t, m)[1]
	waiting, _ := m.BrowserHandoff(nil)
	c, _ := m.Confirm(ctx, &downexv1.ConfirmRequest{Id: waiting.GetId(), QueueId: night.GetId()})
	if d := c.GetDownload(); d.GetState() != sQueued || d.GetQueueId() != night.GetId() {
		t.Fatal(d)
	}
	m.Tick(250)
	if get(t, m, waiting.GetId()).GetState() != sQueued {
		t.Fatal("started")
	}
	other, _ := m.BrowserHandoff(nil)
	c, _ = m.Confirm(ctx, &downexv1.ConfirmRequest{Id: other.GetId(), QueueId: 1})
	if d := c.GetDownload(); d.GetState() != sQueued || d.GetQueueId() != 1 {
		t.Fatal(d)
	}
}

func TestCountsDownTheActionOfAFinishedQueue(t *testing.T) {
	now := int64(1_000_000)
	m := mk(t, func(o *Options) { o.Now = func() time.Time { return time.UnixMilli(now) } })
	spec := queueSpec("Later")
	spec.PostAction = downexv1.PostAction_POST_ACTION_SHUTDOWN
	c, _ := m.CreateQueue(ctx, &downexv1.CreateQueueRequest{Queue: spec})
	q := c.GetQueue()
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/small.zip?size=1048576", QueueId: q.GetId()})
	var posts []*downexv1.PostActionPending
	m.Subscribe(func(b state.Batch) {
		for _, e := range b {
			if p := e.GetPostAction(); p != nil {
				posts = append(posts, p)
			}
		}
	})
	_, _ = m.StartQueue(ctx, &downexv1.StartQueueRequest{Id: q.GetId()})
	runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	m.Tick(250)
	if len(posts) == 0 || posts[0].GetQueueId() != q.GetId() || posts[0].GetAction() != downexv1.PostAction_POST_ACTION_SHUTDOWN || posts[0].GetCancelled() {
		t.Fatal(posts)
	}
	_, _ = m.CancelPostAction(ctx, &downexv1.CancelPostActionRequest{QueueId: q.GetId()})
	m.Tick(250)
	if !posts[len(posts)-1].GetCancelled() {
		t.Fatal(posts)
	}
	now += 120_000
	m.Tick(250)
	if len(posts) != 2 {
		t.Fatal(len(posts))
	}
}

func TestAsksForCredentialsUntilALoginMatches(t *testing.T) {
	m := mk(t)
	d := add(t, m, &downexv1.AddRequest{Url: "https://private.example.net/files/report.pdf"})
	if f := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sFailed }); f.GetErrorCode() != "auth_required" {
		t.Fatal(f)
	}
	saved, _ := update(m, settingsOf(t, m), func(s *downexv1.Settings) {
		s.SiteLogins = []*downexv1.SiteLogin{{UrlPattern: "https://private.example.net/*", Username: "me"}}
	})
	id := saved.GetSiteLogins()[0].GetId()
	if id == "" {
		t.Fatal("no id")
	}
	_, _ = m.SetSecret(ctx, &downexv1.SetSecretRequest{Kind: "site_login", Id: id, Password: "pw"})
	if !settingsOf(t, m).GetSiteLogins()[0].GetHasPassword() {
		t.Fatal("no password")
	}
	_, _ = m.Resume(ctx, &downexv1.ResumeRequest{Id: d.GetId()})
	if x := runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted || x.GetState() == sFailed }); x.GetState() != sCompleted {
		t.Fatal(x)
	}
	_, _ = m.SetSecret(ctx, &downexv1.SetSecretRequest{Kind: "proxy", Password: "x"})
	if !settingsOf(t, m).GetProxy().GetHasPassword() {
		t.Fatal("proxy")
	}
	_, _ = m.ClearSecret(ctx, &downexv1.ClearSecretRequest{Kind: "proxy"})
	if settingsOf(t, m).GetProxy().GetHasPassword() {
		t.Fatal("proxy")
	}
	_, err := m.SetSecret(ctx, &downexv1.SetSecretRequest{Kind: "site_login", Id: "nope", Password: "x"})
	wantCode(t, err, connect.CodeNotFound)
}

func TestChecksForUpdatesOnlyWithAnHTTPSFeed(t *testing.T) {
	m := mk(t)
	_, err := m.CheckForUpdate(ctx)
	wantCode(t, err, connect.CodeFailedPrecondition)
	_, err = update(m, settingsOf(t, m), func(s *downexv1.Settings) { s.UpdateFeedUrl = "http://x.example/feed" })
	wantCode(t, err, connect.CodeInvalidArgument)
	_, _ = update(m, settingsOf(t, m), func(s *downexv1.Settings) { s.UpdateFeedUrl = "https://updates.example.com/feed.json" })
	if r, _ := m.CheckForUpdate(ctx); !r.GetInfo().GetAvailable() || r.GetInfo().GetLatestVersion() != "0.0.1-mock" {
		t.Fatal(r)
	}
	if got := m.PickFile("/opt"); got != "/opt/scanner" {
		t.Fatal(got)
	}
}

// ---- the Studio scene ------------------------------------------------------------------------

func TestStartsAsTheDesignShowsTheMainWindow(t *testing.T) {
	m := mk(t, func(o *Options) { o.Scene = Studio })
	l := list(t, m)
	if len(l) != 28 {
		t.Fatal(len(l))
	}
	running := byState(l, sDownloading)
	var conns []int32
	for _, d := range running {
		conns = append(conns, d.GetConnections())
	}
	slices.Sort(conns)
	slices.Reverse(conns)
	if !slices.Equal(conns, []int32{32, 16, 4}) {
		t.Fatal(conns)
	}
	for _, d := range running {
		if d.GetKind() == kMedia {
			if d.GetFilename() != "conference-keynote.mp4" || d.GetCategory() != "Video" || d.GetMedia().GetOrigin() != site ||
				d.GetMedia().GetSite() != "YouTube" || d.GetMedia().GetContainer() != "mp4" || d.GetSize() <= 0 {
				t.Fatal(d)
			}
			continue
		}
		if int32(len(d.GetConnectionInfo())) != d.GetConnections() {
			t.Fatal(d)
		}
		speeds := map[float64]bool{}
		for _, c := range d.GetConnectionInfo() {
			if c.GetState() != connReceiving || c.GetSpeedBps() <= 0 {
				t.Fatal(c)
			}
			speeds[c.GetSpeedBps()] = true
		}
		if len(speeds) <= 1 {
			t.Fatal("equal speeds")
		}
	}
	aw := byState(l, sAwaiting)
	if len(aw) != 1 || aw[0].GetFilename() != "budget-plan.xlsx" || aw[0].GetSource() != srcBrowser || aw[0].GetSize() != -1 || aw[0].GetExpectedSize() <= 0 {
		t.Fatal(aw)
	}
	if len(byState(l, sPaused)) != 1 {
		t.Fatal("paused")
	}
	queued := byState(l, sQueued)
	slices.SortFunc(queued, func(a, b *downexv1.Download) int { return int(a.GetQueuePosition() - b.GetQueuePosition()) })
	var pos []int32
	for _, d := range queued {
		pos = append(pos, d.GetQueuePosition())
	}
	if !slices.Equal(pos, []int32{1, 2, 3, 4, 5, 6, 7}) || queued[0].GetFilename() != "setup-x64-312.exe" || queued[1].GetFilename() != "annual-report-2025.pdf" {
		t.Fatal(pos)
	}
	f := byState(l, sFailed)
	if len(f) != 1 || f[0].GetErrorCode() != "http_status" || !strings.Contains(f[0].GetError(), "HTTP 503") {
		t.Fatal(f)
	}
	completed := byState(l, sCompleted)
	if len(completed) != 15 {
		t.Fatal(len(completed))
	}
	media := slices.IndexFunc(completed, func(d *downexv1.Download) bool { return d.GetKind() == kMedia })
	if media < 0 || completed[media].GetFilename() != "keynote-2026.mp4" || completed[media].GetMedia().GetFormat() != "hls" {
		t.Fatal("completed stream")
	}
	counts := map[string]int{}
	for _, d := range l {
		if d.GetState() == sCompleted && (d.GetReceived() != d.GetSize() || d.GetCompletedAt().AsTime().Before(d.GetStartedAt().AsTime())) {
			t.Fatal(d)
		}
		checkSegments(t, d)
		c := d.GetCategory()
		if c == "" {
			c = "Other"
		}
		counts[c]++
	}
	want := map[string]int{"Video": 11, "Audio": 3, "Archives": 6, "Documents": 3, "Programs": 4, "Other": 1}
	if len(counts) != len(want) {
		t.Fatal(counts)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Fatal(counts)
		}
	}
}

func TestStudioIsTheDefaultAndStaysConsistent(t *testing.T) {
	m, err := New(Options{FailureRate: zero(), DefaultDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if len(list(t, m)) != 28 {
		t.Fatal("not the Studio scene")
	}
	for range 40 {
		m.Tick(250)
	}
	l := list(t, m)
	if len(byState(l, sDownloading))+len(byState(l, sProbing)) > 3 {
		t.Fatal("too many running")
	}
	for _, d := range l {
		checkSegments(t, d)
	}
	i := slices.IndexFunc(l, func(d *downexv1.Download) bool { return d.GetKind() == kMedia && d.GetState() == sDownloading })
	if i < 0 || int32(len(l[i].GetConnectionInfo())) != l[i].GetConnections() {
		t.Fatal("media connections")
	}
}

func TestNamesVideosMP4AndAudioMP3OrM4A(t *testing.T) {
	for _, m := range []*Mock{mk(t, func(o *Options) { o.Scene = Studio }), mk(t, func(o *Options) { o.Count, o.Seed = 400, 7 })} {
		for _, d := range list(t, m) {
			n := d.GetFilename()
			if regexp.MustCompile(`(?i)\.(webm|mkv|mov|flac|ogg)$`).MatchString(n) ||
				d.GetCategory() == "Video" && !strings.HasSuffix(n, ".mp4") ||
				d.GetCategory() == "Audio" && !regexp.MustCompile(`\.(mp3|m4a)$`).MatchString(n) {
				t.Fatal(n)
			}
		}
	}
}

// ---- free space and the window ------------------------------------------------------------------

func TestAnswersTheFreeSpace(t *testing.T) {
	m := mk(t, func(o *Options) { o.RetryRate = zero() })
	for _, d := range []string{dir, "C:/Users/you/Downloads"} {
		if n, _ := m.FreeSpace(d); n != 214*1024*MiB {
			t.Fatal(n)
		}
	}
	_, err := m.FreeSpace("Downloads")
	wantMessage(t, err, connect.CodeInvalidArgument, `^dir must be an absolute path$`)
	_, err = m.FreeSpace("")
	wantCode(t, err, connect.CodeInvalidArgument)
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/fill.iso?size=8388608"})
	runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	if n, _ := m.FreeSpace(dir); n != 214*1024*MiB-8*MiB {
		t.Fatal(n)
	}
	if n, _ := mk(t, func(o *Options) { o.FreeBytes = 5 }).FreeSpace("/"); n != 5 {
		t.Fatal(n)
	}
}

func TestRecordsWhatThePageAsksOfItsWindow(t *testing.T) {
	m := mk(t)
	if m.Window().Maximised {
		t.Fatal("maximised")
	}
	m.ToggleMaximise()
	if !m.Window().Maximised {
		t.Fatal("not maximised")
	}
	m.Minimise()
	m.ToggleMaximise()
	if w := m.Window(); w.Minimised != 1 || w.Maximised {
		t.Fatal(w)
	}
}

// ---- download again, theme and About ------------------------------------------------------------

func completedFile(t *testing.T, m *Mock, name string) *downexv1.Download {
	t.Helper()
	d := add(t, m, &downexv1.AddRequest{Url: "https://downloads.example.com/pub/" + name + "?size=4194304"})
	return runUntil(t, m, d.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
}

func resume(m *Mock, id int64, restart bool, ef downexv1.ExistingFile) (*downexv1.Download, error) {
	r, err := m.Resume(ctx, &downexv1.ResumeRequest{Id: id, Restart: restart, ExistingFile: ef})
	return r.GetDownload(), err
}

func TestDownloadsACompletedFileAgainKeepingBoth(t *testing.T) {
	m := mk(t)
	done := completedFile(t, m, "report.pdf")
	_, err := resume(m, done.GetId(), false, 0)
	wantCode(t, err, connect.CodeFailedPrecondition)
	_, err = resume(m, done.GetId(), false, state.KeepBoth)
	wantCode(t, err, connect.CodeInvalidArgument)
	if ok, _ := m.FileExists(ctx, done.GetId()); !ok {
		t.Fatal("file missing")
	}
	again, _ := resume(m, done.GetId(), true, 0)
	if again.GetState() != sQueued || again.GetFilename() != "report (1).pdf" || again.GetReceived() != 0 || again.GetCompletedAt() != nil || again.GetError() != "" || len(again.GetSegments()) != 0 {
		t.Fatal(again)
	}
	second := runUntil(t, m, done.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	if second.GetFilename() != "report (1).pdf" || second.GetReceived() != second.GetSize() {
		t.Fatal(second)
	}
	if third, _ := resume(m, done.GetId(), true, state.KeepBoth); third.GetFilename() != "report (1) (1).pdf" {
		t.Fatal(third.GetFilename())
	}
}

func TestReplacesTheFileOnceTheNewCopyIsComplete(t *testing.T) {
	m := mk(t)
	done := completedFile(t, m, "movie.mp4")
	free, _ := m.FreeSpace(dir)
	if again, _ := resume(m, done.GetId(), true, state.Replace); again.GetState() != sQueued || again.GetFilename() != "movie.mp4" || again.GetReceived() != 0 {
		t.Fatal(again)
	}
	if r := runUntil(t, m, done.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted }); r.GetFilename() != "movie.mp4" {
		t.Fatal(r)
	}
	if n, _ := m.FreeSpace(dir); n != free {
		t.Fatal(n, free)
	}
}

func TestDownloadsAGoneFileAgainUnderItsOwnName(t *testing.T) {
	m := mk(t)
	done := completedFile(t, m, "gone.zip")
	_ = m.DeleteFile(done.GetId())
	if ok, _ := m.FileExists(ctx, done.GetId()); ok {
		t.Fatal("still there")
	}
	if x, _ := resume(m, done.GetId(), true, 0); x.GetFilename() != "gone.zip" {
		t.Fatal(x.GetFilename())
	}
	runUntil(t, m, done.GetId(), func(x *downexv1.Download) bool { return x.GetState() == sCompleted })
	if ok, _ := m.FileExists(ctx, done.GetId()); !ok {
		t.Fatal("not there")
	}
	_ = m.DeleteFile(done.GetId())
	if x, _ := resume(m, done.GetId(), true, state.Replace); x.GetFilename() != "gone.zip" {
		t.Fatal(x.GetFilename())
	}
}

func TestRestartsACompletedVideoOfAWebPage(t *testing.T) {
	m := mk(t, func(o *Options) { o.ToolsReady, o.ResolveMs = true, zero() })
	v, err := m.BrowserVideoHandoff(func(r *downexv1.AddMediaRequest) { r.Source = downexv1.AddSource_ADD_SOURCE_UI })
	if err != nil {
		t.Fatal(err)
	}
	done := runUntil(t, m, v.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sCompleted })
	again, _ := resume(m, done.GetId(), true, 0)
	if !regexp.MustCompile(` \(1\)\.mp4$`).MatchString(again.GetFilename()) || again.GetMedia().GetFragmentsDone() != 0 {
		t.Fatal(again)
	}
	if x := runUntil(t, m, done.GetId(), func(d *downexv1.Download) bool { return d.GetState() == sProbing || d.GetState() == sDownloading }); x.GetState() == sCompleted {
		t.Fatal(x)
	}
}

func TestRecordsTheThemeAndTheFolders(t *testing.T) {
	m := mk(t)
	_ = m.SetTheme("dark")
	_ = m.SetTheme("system")
	if !slices.Equal(m.Window().Themes, []string{"dark", "system"}) {
		t.Fatal(m.Window().Themes)
	}
	wantCode(t, m.SetTheme("purple"), connect.CodeInvalidArgument)
	_ = m.OpenAppFolder("logs")
	_ = m.OpenAppFolder("downloads")
	if !slices.Equal(m.Window().Folders, []string{"logs", "downloads"}) {
		t.Fatal(m.Window().Folders)
	}
	wantCode(t, m.OpenAppFolder(`C:\Windows`), connect.CodeInvalidArgument)
	_ = m.OpenAppFolder("extension-chrome")
	if got := m.Window().Folders; len(got) != 3 || got[2] != "extension-chrome" {
		t.Fatal(got)
	}
	if _, err := m.ExtensionFolder("data"); err == nil {
		t.Error("ExtensionFolder(data) answered")
	}
	if err := m.RegisterBrowsers(); err != nil || m.Window().Registered != 1 {
		t.Errorf("RegisterBrowsers = %v, registered %d", err, m.Window().Registered)
	}
}

func TestAnswersTheAboutPage(t *testing.T) {
	m := mk(t, func(o *Options) { o.DefaultDir = `C:\Users\you\Downloads` })
	if b := m.Startup().Build; b.Version != "0.0.0-mock" || b.OS != "windows" || b.Arch != "amd64" || b.WailsVersion != "" {
		t.Fatal(b) // the native window's build: no Wails
	}
	if dir, err := m.ExtensionFolder("extension-firefox"); err != nil || dir != `C:\Users\you\AppData\Local\Programs\Downex\extension\firefox-mv3` {
		t.Errorf("ExtensionFolder = %q %v", dir, err)
	}
	s, _ := m.Status(ctx)
	if s.GetDataDir() != `C:\Users\you\AppData\Roaming\Downex` || s.GetLogDir() != `C:\Users\you\AppData\Local\Downex\logs` ||
		s.GetToolsDir() != `C:\Users\you\AppData\Local\Downex\tools` || s.GetUpdatesSupported() {
		t.Fatal(s)
	}
	add(t, m, &downexv1.AddRequest{Url: "https://secret.example.com/private-name.iso"})
	text, err := m.Diagnostics("141.0.3537.71")
	if err != nil || !strings.Contains(text, "WebView2 141.0.3537.71") || !strings.Contains(text, `%LOCALAPPDATA%\Downex\logs`) {
		t.Fatal(text, err)
	}
	for _, secret := range []string{s.GetEndpoint(), "mock://", "secret", "private", `C:\Users\you`} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics contain %q", secret)
		}
	}
	_, err = m.Diagnostics("<b>")
	wantCode(t, err, connect.CodeInvalidArgument)
	if !strings.Contains(m.ThirdPartyNotices(), "THIRD-PARTY NOTICES") {
		t.Fatal("notices")
	}
	unix := mk(t)
	if st, _ := unix.Status(ctx); st.GetDataDir() != "/home/you/.config/Downex" || unix.Startup().Build.OS != "linux" {
		t.Fatal(st)
	}
}
