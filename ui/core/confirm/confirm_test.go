package confirm

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The cases of ui/frontend/src/lib/confirm.test.ts.

const (
	queued   = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	awaiting = downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION
)

func dl(id int64, state downexv1.DownloadState, createdMs int64) *downexv1.Download {
	return &downexv1.Download{Id: id, State: state, CreatedAt: timestamppb.New(time.UnixMilli(createdMs))}
}

var list = []*downexv1.Download{
	dl(1, queued, 10),
	dl(2, awaiting, 30),
	dl(3, awaiting, 20),
	dl(4, awaiting, 20),
}

func set(ids ...int64) map[int64]struct{} {
	m := map[int64]struct{}{}
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

func TestNextConfirmation(t *testing.T) {
	var ids []int64
	for _, d := range AwaitingInOrder(list) {
		ids = append(ids, d.GetId())
	}
	if !slices.Equal(ids, []int64{3, 4, 2}) {
		t.Fatalf("awaiting in order = %v, want the oldest first, ties by id", ids)
	}
	cases := []struct {
		name      string
		list      []*downexv1.Download
		dismissed map[int64]struct{}
		preferred int64
		want      int64
		ok        bool
	}{
		{"oldest", list, set(), 0, 3, true},
		{"skips a closed dialog", list, set(3), 0, 4, true},
		{"all closed", list, set(2, 3, 4), 0, 0, false},
		{"the one the user asked for", list, set(2, 3, 4), 2, 2, true},
		{"asked for one that is not waiting: ignored", list, set(), 1, 3, true},
		{"nothing waiting", nil, set(), 5, 0, false},
	}
	for _, c := range cases {
		got, ok := Next(c.list, c.dismissed, c.preferred)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Next = %d, %v; want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCategoryFolder(t *testing.T) {
	settings := &downexv1.Settings{
		DefaultDir:         `C:\Users\you\Downloads`,
		UseCategoryFolders: true,
		Categories: []*downexv1.Category{
			{Name: "Video", Extensions: []string{"mp4"}},
			{Name: "Books", Extensions: []string{"epub"}, Dir: `D:\Books`},
		},
	}
	off := &downexv1.Settings{DefaultDir: settings.DefaultDir, Categories: settings.Categories}
	cases := []struct {
		s    *downexv1.Settings
		name string
		want string
	}{
		{settings, "Video", `C:\Users\you\Downloads\Video`},
		{settings, "Books", `D:\Books`},
		{settings, "", `C:\Users\you\Downloads`},
		{settings, "Gone", `C:\Users\you\Downloads`},
		{off, "Books", `C:\Users\you\Downloads`},
	}
	for _, c := range cases {
		if got := CategoryFolder(c.s, c.name); got != c.want {
			t.Errorf("CategoryFolder(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestJoinDir(t *testing.T) {
	for _, c := range [][3]string{
		{"/home/you/Downloads", "Audio", "/home/you/Downloads/Audio"},
		{"/home/you/Downloads/", "Audio", "/home/you/Downloads/Audio"},
		{`C:\`, "Audio", `C:\Audio`},
		{"", "Audio", "Audio"},
	} {
		if got := JoinDir(c[0], c[1]); got != c[2] {
			t.Errorf("JoinDir(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	if Guard != 600*time.Millisecond {
		t.Errorf("Guard = %v", Guard)
	}
}
