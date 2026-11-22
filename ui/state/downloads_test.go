package state_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format/formattest"
	"downex/ui/core/query"
	"downex/ui/core/selection"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/state/statetest"
)

var ctx = context.Background()

const MiB = statetest.MiB

func dl(id int64, edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.Download(id, edit)
}

func zero() *float64 { v := 0.0; return &v }

// mock is downloads.test.ts mock(count): the simulated daemon without its timer.
func mock(t *testing.T, count int) *scene.Mock {
	t.Helper()
	m, err := scene.New(scene.Options{Scene: scene.Random, Count: count, Seed: 3, FailureRate: zero(), DefaultDir: "/home/you/Downloads"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

type opt func(*state.DownloadsOptions)

func started(t *testing.T, d interface {
	state.Events
}, daemon any, opts ...opt) *state.DownloadsStore {
	t.Helper()
	o := state.DownloadsOptions{Events: d}
	switch x := daemon.(type) {
	case *scene.Mock:
		o.Daemon = x
	case *statetest.Daemon:
		o.Daemon = x
	}
	for _, f := range opts {
		f(&o)
	}
	s := state.NewDownloads(o)
	t.Cleanup(s.Dispose)
	s.Start(ctx)
	return s
}

func fake(t *testing.T, initial ...*downexv1.Download) (*statetest.Daemon, *state.DownloadsStore) {
	t.Helper()
	f := statetest.NewDaemon(initial...)
	return f, started(t, &f.Events, f)
}

func ids(list []*downexv1.Download) []int64 {
	out := []int64{}
	for _, d := range list {
		out = append(out, d.GetId())
	}
	return out
}

func sorted(v []int64) []int64 { v = slices.Clone(v); slices.Sort(v); return v }

func at(ms int64) func(*downexv1.Download) {
	return func(d *downexv1.Download) { d.UpdatedAt = statetest.Ms(ms) }
}

func withClock(c *statetest.Clock) opt { return func(o *state.DownloadsOptions) { o.Clock = c } }

// ---- loading and events ---------------------------------------------------------------

func TestDownloadsLoadsAndDerives(t *testing.T) {
	m := mock(t, 40)
	s := started(t, m, m)
	if !s.Ready() || len(s.All()) != 40 || len(s.Visible()) != 40 {
		t.Fatal(s.Ready(), len(s.All()), len(s.Visible()))
	}
	var created []int64
	for _, d := range s.Visible() {
		created = append(created, d.GetCreatedAt().AsTime().UnixMilli())
	}
	want := slices.Clone(created)
	slices.Sort(want)
	slices.Reverse(want)
	if !slices.Equal(created, want) {
		t.Fatal("not newest first")
	}
	if s.Counts().Categories[categories.All] != 40 {
		t.Fatal(s.Counts().Categories)
	}
}

func TestDownloadsAppliesUpdatesIgnoringStaleOnes(t *testing.T) {
	f, s := fake(t, dl(1, at(100)), dl(2, nil))
	f.Emit(statetest.Updated(dl(1, func(d *downexv1.Download) { d.State = state.StateDownloading; at(200)(d) })))
	if d, _ := s.Get(1); d.GetState() != state.StateDownloading {
		t.Fatal(d)
	}
	f.Emit(statetest.Updated(dl(1, func(d *downexv1.Download) { d.State = state.StateQueued; at(150)(d) })))
	if d, _ := s.Get(1); d.GetState() != state.StateDownloading {
		t.Fatal("a stale update won")
	}
	f.Emit(statetest.Removed(2), statetest.Snapshot(dl(3, nil)))
	if got := sorted(ids(s.All())); !slices.Equal(got, []int64{1, 3}) {
		t.Fatal(got)
	}
}

func TestDownloadsReplaysEventsThatArriveWhileLoading(t *testing.T) {
	f := statetest.NewDaemon(dl(1, func(d *downexv1.Download) { d.State = state.StateDownloading; at(1)(d) }))
	release := make(chan struct{})
	entered := make(chan struct{})
	f.Before = func(name string, _ ...any) error {
		if name == "list" {
			close(entered)
			<-release
		}
		return nil
	}
	s := state.NewDownloads(state.DownloadsOptions{Daemon: f, Events: &f.Events})
	t.Cleanup(s.Dispose)
	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()
	<-entered
	f.Emit(statetest.Updated(dl(1, func(d *downexv1.Download) { d.State = state.StateCompleted; at(9e12)(d) })))
	close(release)
	<-done
	if d, _ := s.Get(1); d.GetState() != state.StateCompleted {
		t.Fatal(d)
	}
}

func values(h []state.SpeedSample) []float64 {
	out := []float64{}
	for _, x := range h {
		out = append(out, x.V)
	}
	return out
}

func TestDownloadsRecordsASpeedHistory(t *testing.T) {
	c := statetest.NewClock(1000)
	f := statetest.NewDaemon()
	s := started(t, &f.Events, f, withClock(c))
	for _, v := range []float64{100, 200, 300} {
		c.Advance(250 * time.Millisecond)
		f.Emit(statetest.Updated(dl(1, func(d *downexv1.Download) { d.State, d.SpeedBps = state.StateDownloading, v })))
	}
	if got := values(s.HistoryOf(1)); !slices.Equal(got, []float64{100, 200, 300}) {
		t.Fatal(got)
	}
	c.Advance(120 * time.Second)
	f.Emit(statetest.Updated(dl(1, func(d *downexv1.Download) { d.State, d.SpeedBps = state.StateDownloading, 5 })))
	if got := values(s.HistoryOf(1)); !slices.Equal(got, []float64{5}) {
		t.Fatal(got)
	}
	f.Emit(statetest.Removed(1))
	if len(s.HistoryOf(1)) != 0 {
		t.Fatal("history kept")
	}
}

func TestDownloadsRecordsTheTotalSpeed(t *testing.T) {
	c := statetest.NewClock(1000)
	f := statetest.NewDaemon(dl(1, at(1)), dl(2, at(1)))
	s := started(t, &f.Events, f, withClock(c))
	if len(s.TotalHistory()) != 0 {
		t.Fatal(s.TotalHistory())
	}
	run := func(id int64, st downexv1.DownloadState, v float64, ms int64) *downexv1.Event {
		return statetest.Updated(dl(id, func(d *downexv1.Download) { d.State, d.SpeedBps = st, v; at(ms)(d) }))
	}
	c.Advance(250 * time.Millisecond)
	f.Emit(run(1, state.StateDownloading, 100, 1250))
	c.Advance(250 * time.Millisecond)
	f.Emit(run(1, state.StateDownloading, 300, 1500), run(2, state.StateDownloading, 200, 1500))
	c.Advance(250 * time.Millisecond)
	// a paused download no longer counts, and a stale speed of a stopped one never does
	f.Emit(run(2, state.StatePaused, 999, 1750))
	want := []state.SpeedSample{{T: 1250, V: 100}, {T: 1500, V: 500}, {T: 1750, V: 300}}
	if !slices.Equal(s.TotalHistory(), want) {
		t.Fatal(s.TotalHistory())
	}
	// a batch that changes nothing adds no sample
	f.Emit(run(2, state.StatePaused, 0, 1))
	if len(s.TotalHistory()) != 3 {
		t.Fatal(len(s.TotalHistory()))
	}
	// samples older than the window fall out
	c.Advance(120 * time.Second)
	now := c.Now().UnixMilli()
	f.Emit(run(1, state.StateDownloading, 50, now))
	if !slices.Equal(s.TotalHistory(), []state.SpeedSample{{T: float64(now), V: 50}}) {
		t.Fatal(s.TotalHistory())
	}
}

// ---- filters and selection -------------------------------------------------------------

func rows() []*downexv1.Download {
	return []*downexv1.Download{
		dl(1, func(d *downexv1.Download) { d.Filename, d.State = "a.mp4", state.StateDownloading }),
		dl(2, func(d *downexv1.Download) { d.Filename, d.State = "b.zip", state.StateCompleted }),
		dl(3, func(d *downexv1.Download) { d.Filename, d.State = "c.mp4", state.StatePaused }),
		dl(4, func(d *downexv1.Download) { d.Filename, d.State = "d.pdf", state.StateFailed }),
	}
}

func TestDownloadsFiltersByCategoryStatusAndSearch(t *testing.T) {
	_, s := fake(t, rows()...)
	s.SetCategory(categories.Key("Video"))
	if got := sorted(s.VisibleIDs()); !slices.Equal(got, []int64{1, 3}) {
		t.Fatal(got)
	}
	s.SetStatusFilter(categories.Paused)
	if !slices.Equal(s.VisibleIDs(), []int64{3}) {
		t.Fatal(s.VisibleIDs())
	}
	st := s.Counts().Statuses
	if st[categories.AnyStatus] != 2 || st[categories.Active] != 1 || st[categories.Paused] != 1 {
		t.Fatal(st)
	}
	s.SetStatusFilter(categories.AnyStatus)
	s.SetSearch("A.MP4")
	if !slices.Equal(s.VisibleIDs(), []int64{1}) {
		t.Fatal(s.VisibleIDs())
	}
}

func TestDownloadsGroupsByTheConfiguredCategories(t *testing.T) {
	_, s := fake(t, rows()...)
	s.SetCategories([]*downexv1.Category{{Name: "Clips", Extensions: []string{"mp4"}}, {Name: "Docs", Extensions: []string{"pdf"}}})
	c := s.Counts().Categories
	want := map[categories.Filter]int{categories.All: 4, categories.Key("Clips"): 2, categories.Key("Docs"): 1, categories.OtherFilter: 1}
	if len(c) != len(want) {
		t.Fatal(c)
	}
	for k, v := range want {
		if c[k] != v {
			t.Fatal(c)
		}
	}
	s.SetCategory(categories.Key("Docs"))
	if !slices.Equal(s.VisibleIDs(), []int64{4}) {
		t.Fatal(s.VisibleIDs())
	}
	s.SetCategory(categories.OtherFilter)
	if !slices.Equal(s.VisibleIDs(), []int64{2}) {
		t.Fatal(s.VisibleIDs())
	}
	s.SetCategory(categories.Key("Docs"))
	s.SetCategories([]*downexv1.Category{{Name: "Clips", Extensions: []string{"mp4"}}})
	if s.Category() != categories.All || len(s.VisibleIDs()) != 4 {
		t.Fatal(s.Category(), s.VisibleIDs())
	}
}

func TestDownloadsConfirmsAWaitingDownload(t *testing.T) {
	awaiting := func(id, created int64) *downexv1.Download {
		return dl(id, func(d *downexv1.Download) { d.State, d.CreatedAt = state.StateAwaiting, statetest.Ms(created) })
	}
	f, s := fake(t, awaiting(1, 50), awaiting(2, 40), dl(3, nil))
	if !slices.Equal(ids(s.Awaiting()), []int64{2, 1}) || s.Counts().Statuses[categories.Awaiting] != 2 {
		t.Fatal(ids(s.Awaiting()))
	}
	if _, opening, err := s.Confirm(ctx, &downexv1.ConfirmRequest{Id: 2, Dir: "/data"}); err != nil || opening {
		t.Fatal(opening, err)
	}
	if d, _ := s.Get(2); d.GetState() != state.StatePaused || d.GetDir() != "/data" {
		t.Fatal(d)
	}
	if !slices.Equal(ids(s.Awaiting()), []int64{1}) {
		t.Fatal(ids(s.Awaiting()))
	}
	if _, _, err := s.Confirm(ctx, &downexv1.ConfirmRequest{Id: 2, Start: true}); err == nil || !strings.Contains(err.Error(), "not awaiting") {
		t.Fatal(err)
	}
	// restart never applies to a download that has not started
	if out := s.Restart(ctx, []int64{1}, 0); len(out) != 0 {
		t.Fatal(out)
	}
	// downexd says it opens the progress window of a Start itself: the store passes it on.
	f.SetOpensProgressWindows(true)
	d, opening, err := s.Confirm(ctx, &downexv1.ConfirmRequest{Id: 1, Start: true, ProgressWindow: true})
	if err != nil || !opening || d.GetState() != state.StateQueued {
		t.Fatal(d, opening, err)
	}
}

func TestDownloadsActsOnlyOnVisibleSelectedRows(t *testing.T) {
	f, s := fake(t, rows()...)
	s.SelectAll()
	if len(s.Selected()) != 4 {
		t.Fatal(len(s.Selected()))
	}
	s.SetCategory(categories.Key("Video"))
	if got := s.Selection().IDs(); !slices.Equal(got, []int64{1, 3}) {
		t.Fatal(got)
	}
	s.SetCategory(categories.All)
	s.SetStatusFilter(categories.Active)
	s.SelectAll()
	// the download completes and leaves the "Active" view: nothing visible stays selected
	f.Emit(statetest.Updated(dl(1, func(d *downexv1.Download) { d.Filename, d.State = "a.mp4", state.StateCompleted; at(9e12)(d) })))
	if len(s.Selected()) != 0 {
		t.Fatal(ids(s.Selected()))
	}
	f.Emit(statetest.Removed(1))
	if s.Selection().Has(1) {
		t.Fatal("removed id still selected")
	}
}

func TestDownloadsDrivesSelectionInDisplayOrder(t *testing.T) {
	_, s := fake(t, rows()...)
	s.SortBy(query.SortName)
	if s.Sort() != (query.SortSpec{Key: query.SortName, Dir: query.Asc}) {
		t.Fatal(s.Sort())
	}
	s.Click(1, selection.Modifiers{})
	s.Click(3, selection.Modifiers{Shift: true})
	if !slices.Equal(ids(s.Selected()), []int64{1, 2, 3}) {
		t.Fatal(ids(s.Selected()))
	}
	s.Move(selection.By(1), selection.Modifiers{})
	if !slices.Equal(ids(s.Selected()), []int64{4}) {
		t.Fatal(ids(s.Selected()))
	}
	if d, ok := s.Detail(); !ok || d.GetId() != 4 {
		t.Fatal(d)
	}
	s.ClearSelection()
	if _, ok := s.Detail(); ok {
		t.Fatal("a detail without a selection")
	}
	// SetCursor moves only the cursor (today's {...selection, focus: id})
	s.Click(2, selection.Modifiers{})
	s.SetCursor(4)
	if f, _ := s.Selection().Focus(); f != 4 || !slices.Equal(s.Selection().IDs(), []int64{2}) {
		t.Fatal(f, s.Selection().IDs())
	}
	if a, _ := s.Selection().Anchor(); a != 2 {
		t.Fatal(a)
	}
}

// ---- actions ----------------------------------------------------------------------------

func outcomes(out []state.Outcome) []string {
	var s []string
	for _, o := range out {
		if o.OK() {
			s = append(s, "ok")
		} else {
			s = append(s, "fail:"+o.Err.Message)
		}
	}
	return s
}

func TestDownloadsPausesResumesRestartsAndRemoves(t *testing.T) {
	m := mock(t, 0)
	s := started(t, m, m)
	added := s.Add(ctx, []*downexv1.AddRequest{{Url: "https://downloads.example.com/pub/a.mkv"}, {Url: "ftp://downloads.example.com/b.mkv"}})
	if !added[0].OK() || added[1].OK() || !strings.Contains(added[1].Err.Message, "http") {
		t.Fatal(added)
	}
	id := added[0].Download.GetId()
	if d, _ := s.Get(id); d.GetState() != state.StateQueued {
		t.Fatal(d)
	}
	if out := s.Pause(ctx, []int64{id}); len(out) != 1 || out[0].ID != id || !out[0].OK() {
		t.Fatal(out)
	}
	if d, _ := s.Get(id); d.GetState() != state.StatePaused {
		t.Fatal(d)
	}
	// nothing to pause any more: filtered out instead of failing
	if out := s.Pause(ctx, []int64{id}); len(out) != 0 {
		t.Fatal(out)
	}
	s.Resume(ctx, []int64{id}, state.ResumeOptions{})
	if d, _ := s.Get(id); d.GetState() != state.StateQueued {
		t.Fatal(d)
	}
	for range 5 {
		m.Tick(1000)
	}
	s.Restart(ctx, []int64{id}, 0)
	if d, _ := s.Get(id); d.GetState() != state.StateQueued || d.GetReceived() != 0 {
		t.Fatal(d)
	}
	s.SelectOnly(id)
	if out := s.Remove(ctx, []int64{id}, true); len(out) != 1 || !out[0].OK() {
		t.Fatal(out)
	}
	if _, ok := s.Get(id); ok || s.Selection().Len() != 0 {
		t.Fatal("still there or selected")
	}
	if out := s.Remove(ctx, []int64{id}, false); out[0].OK() {
		t.Fatal(out)
	}
}

func TestDownloadsDownloadsCompletedOnesAgainWithTheChoice(t *testing.T) {
	f := statetest.NewDaemon(
		dl(1, func(d *downexv1.Download) {
			d.Filename, d.State, d.Received = "report.pdf", state.StateCompleted, 10*MiB
		}),
		dl(2, func(d *downexv1.Download) {
			d.Filename, d.State, d.Received = "video.mp4", state.StateCompleted, 10*MiB
		}),
		dl(3, func(d *downexv1.Download) { d.State, d.Received = state.StateDownloading, MiB }),
		dl(4, func(d *downexv1.Download) { d.State = state.StateAwaiting }),
	)
	type resume struct {
		id      int64
		restart bool
		ef      downexv1.ExistingFile
	}
	var mu sync.Mutex
	var resumes []resume
	f.Before = func(name string, args ...any) error {
		if name == "resume" {
			r := args[1].(*downexv1.ResumeRequest)
			mu.Lock()
			resumes = append(resumes, resume{r.GetId(), r.GetRestart(), r.GetExistingFile()})
			mu.Unlock()
		}
		return nil
	}
	s := started(t, &f.Events, f)
	out := s.Restart(ctx, []int64{1, 3, 4}, state.Replace)
	if len(out) != 2 || out[0].ID != 1 || out[1].ID != 3 || !out[0].OK() || !out[1].OK() {
		t.Fatal(out)
	}
	slices.SortFunc(resumes, func(a, b resume) int { return int(a.id - b.id) })
	// existing_file only for the completed one (downexd ignores it for others, never without restart)
	if !slices.Equal(resumes, []resume{{1, true, state.Replace}, {3, true, 0}}) {
		t.Fatal(resumes)
	}
	if d, _ := s.Get(1); d.GetState() != state.StateQueued || d.GetFilename() != "report.pdf" {
		t.Fatal(d)
	}
	// the running one was paused first, as before
	if f.Count("pause") != 1 {
		t.Fatal(f.Count("pause"))
	}
	// Keep both is the default: the new copy gets a numbered name.
	resumes = nil
	if out := s.Restart(ctx, []int64{2}, 0); len(out) != 1 || !out[0].OK() {
		t.Fatal(out)
	}
	if !slices.Equal(resumes, []resume{{2, true, state.KeepBoth}}) {
		t.Fatal(resumes)
	}
	if d, _ := s.Get(2); d.GetFilename() != "video (1).mp4" {
		t.Fatal(d.GetFilename())
	}
}

func TestDownloadsCopiesURLs(t *testing.T) {
	f := statetest.NewDaemon(dl(1, func(d *downexv1.Download) { d.Url = "https://a.example/1" }), dl(2, func(d *downexv1.Download) { d.Url = "https://a.example/2" }))
	var copied []string
	s := started(t, &f.Events, f, func(o *state.DownloadsOptions) {
		o.CopyText = func(_ context.Context, text string) error { copied = append(copied, text); return nil }
	})
	if n, err := s.CopyURLs(ctx, []int64{2, 1, 99}); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if !slices.Equal(copied, []string{"https://a.example/2\nhttps://a.example/1"}) {
		t.Fatal(copied)
	}
}

func TestMapLimitBoundsConcurrencyAndKeepsOrder(t *testing.T) {
	var running, peak atomic.Int32
	out := state.MapLimit([]int{5, 1, 4, 2, 3, 0}, 2, func(n int) int {
		r := running.Add(1)
		for {
			p := peak.Load()
			if r <= p || peak.CompareAndSwap(p, r) {
				break
			}
		}
		time.Sleep(time.Duration(n) * time.Millisecond)
		running.Add(-1)
		return n * 2
	})
	if !slices.Equal(out, []int{10, 2, 8, 4, 6, 0}) || peak.Load() != 2 {
		t.Fatal(out, peak.Load())
	}
}

// ---- daemon unavailable -----------------------------------------------------------------

func TestDownloadsMarksTheListStaleAndReloadsAfterReconnecting(t *testing.T) {
	f, s := fake(t,
		dl(1, func(d *downexv1.Download) { d.State, d.SpeedBps, d.Received = state.StateDownloading, 5*MiB, MiB }),
		dl(2, func(d *downexv1.Download) { d.State = state.StatePaused }))
	s.RefreshStatus()
	if s.Stale() || s.Totals().Speed != 5*MiB {
		t.Fatal(s.Stale(), s.Totals())
	}
	f.SetDown(true)
	s.RefreshStatus()
	if d := s.Daemon(); d == nil || d.OK || !strings.Contains(d.Error, "connection refused") {
		t.Fatal(d)
	}
	if !s.Stale() || s.Totals().Speed != 0 || len(s.All()) != 2 {
		t.Fatal("not stale")
	}
	f.SetDown(false)
	f.DeleteItem(2)
	f.ResetCalls()
	s.RefreshStatus()
	if s.Stale() || !slices.Equal(f.Calls(), []string{"status", "list"}) || !slices.Equal(ids(s.All()), []int64{1}) {
		t.Fatal(s.Stale(), f.Calls(), ids(s.All()))
	}
}

func TestDownloadsIsStaleWhenTheFirstLoadFails(t *testing.T) {
	f := statetest.NewDaemon(dl(1, nil))
	f.SetDown(true)
	s := started(t, &f.Events, f)
	if s.Ready() || !strings.Contains(s.LoadError(), "connection refused") || !s.Stale() {
		t.Fatal(s.Ready(), s.LoadError())
	}
	f.SetDown(false)
	s.RetryNow()
	if !s.Ready() || s.Stale() {
		t.Fatal("not recovered")
	}
}

func TestDownloadsSchedulesTheNextCheckAndRetriesNow(t *testing.T) {
	c := statetest.NewClock(10_000)
	f := statetest.NewDaemon()
	f.SetDown(true)
	s := state.NewDownloads(state.DownloadsOptions{Daemon: f, Events: &f.Events, Clock: c})
	t.Cleanup(s.Dispose)
	s.StartStatusPolling(3 * time.Second)
	c.Advance(0)
	if d := s.Daemon(); d == nil || d.OK || s.NextCheckAt().UnixMilli() != 13_000 {
		t.Fatal(s.Daemon(), s.NextCheckAt())
	}
	before := f.Count("status")
	c.Set(11_000)
	f.SetDown(false)
	s.RetryNow()
	if f.Count("status") != before+1 || !s.Daemon().OK || s.NextCheckAt().UnixMilli() != 14_000 {
		t.Fatal(f.Count("status"), s.Daemon(), s.NextCheckAt())
	}
	// the earlier timer was replaced, not doubled
	c.Advance(3 * time.Second)
	if f.Count("status") != before+2 {
		t.Fatal(f.Count("status"))
	}
}

// ---- keyboard position after removing ---------------------------------------------------

func ten() []*downexv1.Download {
	var out []*downexv1.Download
	for i := int64(1); i <= 10; i++ {
		out = append(out, dl(i, func(d *downexv1.Download) { d.CreatedAt = statetest.Ms(1000 - i) }))
	}
	return out
}

func TestDownloadsSelectsTheNextSurvivingRow(t *testing.T) {
	_, s := fake(t, ten()...)
	// default sort: added, newest first → ids 1..10 in order
	if !slices.Equal(s.VisibleIDs(), []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Fatal(s.VisibleIDs())
	}
	s.Click(4, selection.Modifiers{})
	s.Click(6, selection.Modifiers{Shift: true})
	s.Remove(ctx, []int64{4, 5, 6}, false)
	if f, _ := s.Selection().Focus(); !slices.Equal(s.Selection().IDs(), []int64{7}) || f != 7 {
		t.Fatal(s.Selection().IDs(), f)
	}
	s.Click(10, selection.Modifiers{})
	s.Remove(ctx, []int64{10}, false)
	if f, _ := s.Selection().Focus(); f != 9 {
		t.Fatal(f)
	}
}

func TestDownloadsLeavesTheSelectionWhenSomeRemovalsFail(t *testing.T) {
	f, s := fake(t, ten()...)
	s.Click(2, selection.Modifiers{})
	s.Click(3, selection.Modifiers{Toggle: true})
	f.DeleteItem(3) // the daemon no longer knows it: removing fails
	out := s.Remove(ctx, []int64{2, 3}, false)
	if out[0].OK() != true || out[1].OK() != false || !slices.Equal(s.Selection().IDs(), []int64{3}) {
		t.Fatal(outcomes(out), s.Selection().IDs())
	}
}

// ---- bulk responses ----------------------------------------------------------------------

func TestDownloadsCoalescesBulkRevisionsIntoFrames(t *testing.T) {
	var list []*downexv1.Download
	for i := int64(1); i <= 40; i++ {
		list = append(list, dl(i, func(d *downexv1.Download) { d.State = state.StateDownloading }))
	}
	f := statetest.NewDaemon(list...)
	frames := &statetest.Frames{}
	s := started(t, &f.Events, f, func(o *state.DownloadsOptions) { o.Frames = frames })
	var changes atomic.Int32
	s.Changes.Subscribe(func() { changes.Add(1) })
	out := s.Pause(ctx, s.VisibleIDs())
	if len(out) != 40 || frames.Count() > 2 {
		t.Fatal(len(out), frames.Count())
	}
	// settled at once when the action returns
	for _, d := range s.Visible() {
		if d.GetState() != state.StatePaused {
			t.Fatal(d)
		}
	}
	if changes.Load() > 2 {
		t.Fatal("a change per answer", changes.Load())
	}
}

// TestDownloadsKeepsUpWithTenThousandRows applies the simulation's batches (4 a second)
// to a list of 10,000 and reads what a frame shows: well inside today's 60 ms per batch.
func TestDownloadsKeepsUpWithTenThousandRows(t *testing.T) {
	m, err := scene.New(scene.Options{Scene: scene.Random, Count: 10_000, Seed: 3, FailureRate: zero()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	s := started(t, m, m)
	if len(s.Visible()) != 10_000 {
		t.Fatal(len(s.Visible()))
	}
	s.SetSearch("mp4")
	bound := 60 * time.Millisecond * raceSlowdown // today's 60 ms (race_test.go scales it under -race)
	// A round is 40 batches and counts its worst. Alone a batch takes about 9 ms; with every
	// package of the module tested at once on this PC one round's worst read 70.5 ms, a
	// moment the scheduler gave the test's thread away, so a round over the bound is run
	// again, up to three rounds: a slow batch fails all of them.
	var worst time.Duration
	for round := 1; round <= 3; round++ {
		worst = 0
		for range 40 {
			start := time.Now()
			m.Tick(250)
			_ = s.Visible()
			_ = s.Counts()
			_ = s.Totals()
			worst = max(worst, time.Since(start))
		}
		t.Logf("round %d: worst batch over 10,000 rows (simulation step included): %v (bound %v)", round, worst, bound)
		if worst <= bound {
			return
		}
	}
	t.Fatalf("a batch took %v in each of 3 rounds", worst)
}
