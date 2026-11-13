package query

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
)

// The cases of ui/frontend/src/lib/query.test.ts.

const MiB = 1 << 20

var (
	stQueued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	stDownloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	stPaused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	stCompleted   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	stFailed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	stProbing     = downexv1.DownloadState_DOWNLOAD_STATE_PROBING
)

func ms(n int64) *timestamppb.Timestamp { return timestamppb.New(time.UnixMilli(n)) }

// makeDownload is testing.ts makeDownload.
func makeDownload(id int64, edit ...func(*downexv1.Download)) *downexv1.Download {
	d := &downexv1.Download{
		Id:        id,
		Url:       fmt.Sprintf("https://downloads.example.com/file-%d.bin", id),
		Dir:       "/home/you/Downloads",
		Filename:  fmt.Sprintf("file-%d.bin", id),
		Size:      10 * MiB,
		State:     stQueued,
		CreatedAt: ms(1_700_000_000_000 + id*1000),
		UpdatedAt: ms(1_700_000_000_000 + id*1000),
		Kind:      downexv1.DownloadKind_DOWNLOAD_KIND_FILE,
		QueueId:   mainQueueID,
	}
	for _, e := range edit {
		e(d)
	}
	return d
}

func row(name string, size int64, state downexv1.DownloadState, received int64, speed float64, created int64) func(*downexv1.Download) {
	return func(d *downexv1.Download) {
		d.Filename, d.Size, d.State, d.Received, d.SpeedBps, d.CreatedAt = name, size, state, received, speed, ms(created)
	}
}

func testList() []*downexv1.Download {
	return []*downexv1.Download{
		makeDownload(1, row("file10.zip", 300, stCompleted, 300, 0, 30)),
		makeDownload(2, row("file2.zip", 100, stDownloading, 50, 10, 10)),
		makeDownload(3, row("Clip.mp4", -1, stDownloading, 5, 50, 20)),
		makeDownload(4, row("notes.pdf", 200, stPaused, 150, 0, 40)),
		makeDownload(5, row("song.mp3", 100, stFailed, 10, 0, 50), func(d *downexv1.Download) { d.Url = "https://media.example.org/x" }),
		makeDownload(6, row("tool.exe", 100, stQueued, 0, 0, 60), func(d *downexv1.Download) { d.QueuePosition = 2 }),
		makeDownload(7, row("tool2.exe", 100, stQueued, 0, 0, 60), func(d *downexv1.Download) { d.QueuePosition = 1 }),
	}
}

// defaultCategories is api.ts DEFAULT_CATEGORIES.
func defaultCategories() []*downexv1.Category {
	return []*downexv1.Category{
		{Name: "Video", Extensions: []string{"mp4", "mkv", "avi", "mov", "webm", "wmv", "flv", "m4v", "mpg", "mpeg", "ts", "3gp"}},
		{Name: "Audio", Extensions: []string{"mp3", "flac", "m4a", "aac", "wav", "ogg", "opus", "wma"}},
		{Name: "Archives", Extensions: []string{"zip", "rar", "7z", "gz", "xz", "bz2", "tar", "tgz", "zst", "iso", "img", "dmg"}},
		{Name: "Documents", Extensions: []string{"pdf", "epub", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "txt", "csv", "md"}},
		{Name: "Programs", Extensions: []string{"exe", "msi", "msix", "appx", "apk", "deb", "rpm", "appimage", "bat", "cmd", "ps1", "jar"}},
	}
}

var index = categories.NewIndex(defaultCategories())

func ids(xs []*downexv1.Download) []int64 {
	out := []int64{}
	for _, d := range xs {
		out = append(out, d.GetId())
	}
	return out
}

func spec(category categories.Filter, status categories.Status, search string) FilterSpec {
	return FilterSpec{Category: category, Status: status, Search: search}
}

func TestFilterDownloads(t *testing.T) {
	list := testList()
	for _, c := range []struct {
		f    FilterSpec
		want []int64
	}{
		{spec(categories.Key("Archives"), categories.AnyStatus, ""), []int64{1, 2}},
		{spec(categories.All, categories.Active, ""), []int64{2, 3}},
		{spec(categories.Key("Archives"), categories.Active, ""), []int64{2}},
		{spec(categories.All, categories.AnyStatus, "  CLIP "), []int64{3}},
		// searches URLs as well as names
		{spec(categories.All, categories.AnyStatus, "media.example"), []int64{5}},
	} {
		if got := ids(FilterDownloads(list, c.f, index)); !slices.Equal(got, c.want) {
			t.Errorf("filter %+v = %v, want %v", c.f, got, c.want)
		}
	}
}

func TestSortDownloads(t *testing.T) {
	list := testList()
	sorted := SortDownloads(list, SortSpec{SortName, Asc}, nil)
	var names []string
	for _, d := range sorted {
		names = append(names, d.GetFilename())
	}
	if !slices.Equal(names, []string{"Clip.mp4", "file2.zip", "file10.zip", "notes.pdf", "song.mp3", "tool.exe", "tool2.exe"}) {
		t.Errorf("names naturally and case-insensitively = %q", names)
	}
	before := slices.Clone(list)
	SortDownloads(list, SortSpec{SortSize, Asc}, nil)
	if !slices.Equal(list, before) {
		t.Error("the input was changed")
	}
	for _, c := range []struct {
		s    SortSpec
		want []int64
	}{
		// ties break on id in the sort direction
		{SortSpec{SortSize, Desc}, []int64{1, 4, 7, 6, 5, 2, 3}},
		{SortSpec{SortSize, Asc}, []int64{3, 2, 5, 6, 7, 4, 1}},
		// activity first, queued by queue position
		{SortSpec{SortStatus, Asc}, []int64{2, 3, 7, 6, 4, 5, 1}},
		{SortSpec{SortAdded, Desc}, []int64{7, 6, 5, 4, 1, 3, 2}},
	} {
		if got := ids(SortDownloads(list, c.s, nil)); !slices.Equal(got, c.want) {
			t.Errorf("sort %+v = %v, want %v", c.s, got, c.want)
		}
	}
	// unknown progress and ETA last in both directions
	for _, dir := range []SortDir{Asc, Desc} {
		if got := ids(SortDownloads(list, SortSpec{SortProgress, dir}, nil)); got[len(got)-1] != 3 {
			t.Errorf("progress %s = %v", dir, got)
		}
		if got := ids(SortDownloads(list, SortSpec{SortETA, dir}, nil)); got[0] != 2 {
			t.Errorf("eta %s = %v", dir, got)
		}
	}
	// only running downloads count for speed
	if got := ids(SortDownloads(list, SortSpec{SortSpeed, Desc}, nil)); !slices.Equal(got[:2], []int64{3, 2}) {
		t.Errorf("speed = %v", got)
	}
}

func TestToggleSort(t *testing.T) {
	for _, c := range []struct {
		cur  SortSpec
		key  SortKey
		want SortSpec
	}{
		{SortSpec{SortSize, Desc}, SortSize, SortSpec{SortSize, Asc}},
		{SortSpec{SortSize, Asc}, SortName, SortSpec{SortName, Asc}},
		{SortSpec{SortName, Asc}, SortAdded, SortSpec{SortAdded, Desc}},
		{SortSpec{SortName, Asc}, SortStatus, SortSpec{SortStatus, Asc}},
	} {
		if got := ToggleSort(c.cur, c.key); got != c.want {
			t.Errorf("ToggleSort(%+v, %s) = %+v", c.cur, c.key, got)
		}
	}
}

func TestCountFacets(t *testing.T) {
	list := testList()
	all := CountFacets(list, categories.All, categories.AnyStatus, index, 0)
	want := map[categories.Filter]int{categories.All: 7, categories.Key("Archives"): 2, categories.Key("Video"): 1, categories.Key("Documents"): 1, categories.Key("Audio"): 1, categories.Key("Programs"): 2, categories.OtherFilter: 0}
	if !maps.Equal(all.Categories, want) {
		t.Errorf("categories = %v", all.Categories)
	}
	for s, n := range map[categories.Status]int{categories.AnyStatus: 7, categories.Active: 2, categories.Queued: 2, categories.Paused: 1, categories.Completed: 1, categories.Failed: 1} {
		if all.Statuses[s] != n {
			t.Errorf("status %s = %d, want %d", s, all.Statuses[s], n)
		}
	}
	active := CountFacets(list, categories.Key("Archives"), categories.Active, index, 0)
	for f, n := range map[categories.Filter]int{categories.All: 2, categories.Key("Archives"): 1, categories.Key("Video"): 1, categories.Key("Programs"): 0} {
		if active.Categories[f] != n {
			t.Errorf("active category %s = %d, want %d", f, active.Categories[f], n)
		}
	}
	for s, n := range map[categories.Status]int{categories.AnyStatus: 2, categories.Active: 1, categories.Completed: 1, categories.Queued: 0} {
		if active.Statuses[s] != n {
			t.Errorf("archives status %s = %d, want %d", s, active.Statuses[s], n)
		}
	}
}

// rng is the test's linear congruential generator.
func rng(seed uint32) func() float64 {
	s := seed
	return func() float64 {
		s = s*1664525 + 1013904223
		return float64(s) / (1 << 32)
	}
}

var states = []downexv1.DownloadState{stQueued, stDownloading, stPaused, stCompleted, stFailed, stProbing}

func randomDownload(id int64, r func() float64) *downexv1.Download {
	size := int64(r() * 1000)
	if r() < 0.1 {
		size = -1
	}
	filename := ""
	if r() >= 0.3 {
		filename = fmt.Sprintf("file %d.zip", int(r()*50))
	}
	return makeDownload(id, func(d *downexv1.Download) {
		d.Filename = filename
		d.Url = fmt.Sprintf("https://example.com/d/%d.bin", int(r()*40))
		d.Size = size
		d.Received = int64(r() * 1000)
		d.State = states[int(r()*float64(len(states)))]
		d.SpeedBps = float64(int(r()*5) * 100)
		d.Connections = int32(r() * 4)
		d.QueuePosition = int32(r() * 5)
		d.CreatedAt = ms(int64(r() * 30))
	})
}

func values(items map[int64]*downexv1.Download) []*downexv1.Download {
	return slices.Collect(maps.Values(items))
}

func TestSortedViewMatchesAFullSort(t *testing.T) {
	keys := []SortKey{SortName, SortSize, SortProgress, SortSpeed, SortETA, SortConnections, SortStatus, SortAdded}
	for _, key := range keys {
		for _, dir := range []SortDir{Asc, Desc} {
			t.Run(fmt.Sprintf("%s %s", key, dir), func(t *testing.T) {
				r := rng(uint32(len(key)*31 + len(dir)))
				items := map[int64]*downexv1.Download{}
				for id := int64(1); id <= 300; id++ {
					items[id] = randomDownload(id, r)
				}
				var view SortedView
				s := SortSpec{key, dir}
				first, _ := view.Update(items, s)
				if !slices.Equal(ids(first), ids(SortDownloads(values(items), s, nil))) {
					t.Fatal("the first update is not a full sort")
				}
				nextID := int64(301)
				for batch := 0; batch < 40; batch++ {
					for range 6 {
						roll := r()
						id := 1 + int64(r()*float64(nextID-1))
						prev := items[id]
						switch {
						case roll < 0.15 && prev != nil:
							view.Touch(id, prev)
							delete(items, id)
						case roll < 0.3:
							d := randomDownload(nextID, r)
							nextID++
							view.Touch(d.GetId(), nil)
							items[d.GetId()] = d
						case prev != nil:
							// live update: often only the volatile fields change
							var next *downexv1.Download
							if r() < 0.5 {
								next = proto.CloneOf(prev)
								next.Received++
								next.UpdatedAt = ms(prev.GetUpdatedAt().AsTime().UnixMilli() + 1)
							} else {
								next = randomDownload(id, r)
								next.CreatedAt = prev.GetCreatedAt()
							}
							view.Touch(id, prev)
							items[id] = next
						}
					}
					got, _ := view.Update(items, s)
					if want := ids(SortDownloads(values(items), s, nil)); !slices.Equal(ids(got), want) {
						t.Fatalf("batch %d: %v, want %v", batch, ids(got), want)
					}
				}
			})
		}
	}
}

func TestSortedViewKeepsTheOrder(t *testing.T) {
	items := map[int64]*downexv1.Download{}
	for id := int64(1); id <= 1000; id++ {
		items[id] = makeDownload(id, func(d *downexv1.Download) {
			d.Filename, d.Url, d.State, d.SpeedBps = "", fmt.Sprintf("https://example.com/q/%d.iso", id), stDownloading, 10
		})
	}
	var view SortedView
	s := SortSpec{SortName, Asc}
	first, _ := view.Update(items, s)
	if view.FullSorts != 1 {
		t.Fatalf("full sorts = %d", view.FullSorts)
	}
	if again, changed := view.Update(items, s); changed || &again[0] != &first[0] {
		t.Error("nothing changed, yet a new list")
	}
	for range 20 {
		for _, id := range []int64{3, 500, 999} {
			prev := items[id]
			view.Touch(id, prev)
			next := proto.CloneOf(prev)
			next.Received += 1000
			next.SpeedBps++
			items[id] = next
		}
		next, changed := view.Update(items, s)
		if !changed || &next[0] == &first[0] {
			t.Error("a change kept the old list")
		}
		i := slices.IndexFunc(next, func(d *downexv1.Download) bool { return d.GetId() == 500 })
		if next[i] != items[500] {
			t.Error("the list holds an old message")
		}
	}
	if view.FullSorts != 1 {
		t.Errorf("full sorts = %d", view.FullSorts)
	}
	view.Update(items, SortSpec{SortName, Desc})
	if view.FullSorts != 2 {
		t.Errorf("full sorts after a new direction = %d", view.FullSorts)
	}
}

func TestSortedViewTenThousandRows(t *testing.T) {
	items := map[int64]*downexv1.Download{}
	for id := int64(1); id <= 10_000; id++ {
		items[id] = makeDownload(id, func(d *downexv1.Download) {
			d.Filename, d.State, d.SpeedBps = "", stDownloading, float64(id)
			d.Url = fmt.Sprintf("https://cdn.example.com/files/archive%%20%d.zip?x=%d", id, id)
		})
	}
	var view SortedView
	var search Searcher
	s := SortSpec{SortName, Asc}
	view.Update(items, s)
	t0 := time.Now()
	for batch := range 20 {
		for k := range 16 {
			id := int64(1 + (batch*97+k*613)%10_000)
			prev := items[id]
			view.Touch(id, prev)
			next := proto.CloneOf(prev)
			next.Received++
			next.SpeedBps++
			items[id] = next
		}
		sorted, _ := view.Update(items, s)
		found := search.Filter(sorted, spec(categories.All, categories.AnyStatus, "archive 99"), index)
		if len(found) == 0 {
			t.Fatal("no archive 99")
		}
	}
	// today's bound: 60 ms a batch, loose so that slow machines pass
	per := time.Since(t0) / 20
	t.Logf("%v a batch of 16 updates, a sort and a search of 10,000 rows", per)
	if per > 60*time.Millisecond {
		t.Errorf("%v a batch", per)
	}
	if view.FullSorts != 1 {
		t.Errorf("full sorts = %d", view.FullSorts)
	}
}

func TestQueues(t *testing.T) {
	queued := []*downexv1.Download{
		makeDownload(1, func(d *downexv1.Download) { d.Filename, d.State, d.QueueId = "a.zip", stQueued, 2 }),
		makeDownload(2, func(d *downexv1.Download) { d.Filename, d.State, d.QueueId = "b.zip", stCompleted, 2 }),
		makeDownload(3, func(d *downexv1.Download) { d.Filename, d.State, d.QueueId = "c.mp4", stPaused, 1 }),
		// An older daemon sends no queue: the main queue.
		makeDownload(4, func(d *downexv1.Download) { d.Filename, d.State, d.QueueId = "d.mp4", stDownloading, 0 }),
	}
	for _, c := range []struct {
		f    FilterSpec
		want []int64
	}{
		{FilterSpec{Category: categories.All, Status: categories.AnyStatus, Queue: 2}, []int64{1, 2}},
		{FilterSpec{Category: categories.All, Status: categories.AnyStatus, Queue: 1}, []int64{3, 4}},
		{FilterSpec{Category: categories.Key("Video"), Status: categories.Active, Queue: 1}, []int64{4}},
		{FilterSpec{Category: categories.All, Status: categories.AnyStatus}, []int64{1, 2, 3, 4}},
	} {
		if got := ids(FilterDownloads(queued, c.f, index)); !slices.Equal(got, c.want) {
			t.Errorf("filter %+v = %v", c.f, got)
		}
	}
	counts := CountFacets(queued, categories.All, categories.AnyStatus, index, 2)
	if counts.Categories[categories.All] != 2 || counts.Statuses[categories.Completed] != 1 {
		t.Errorf("counts = %+v", counts)
	}
	if got := CountQueues(queued); !maps.Equal(got, map[int64]int{2: 1, 1: 2}) {
		t.Errorf("queues = %v", got)
	}
}
