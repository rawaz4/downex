// Package confirm holds the rules of the browser hand-off confirmation (DESKTOP-SPEC §1),
// ported from ui/frontend/src/lib/confirm.ts: which download awaiting confirmation the dialog
// shows next, and the folder a category proposes.
package confirm

import (
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Guard is how long a confirmation dialog that opened by itself (a browser hand-off, not the
// user's request) ignores its buttons and Enter: input meant for what was in front before it
// appeared must not start, defer or cancel the download.
const Guard = 600 * time.Millisecond

// millis is a timestamp as the window orders by it: epoch milliseconds, 0 when unset or not
// after the epoch (wails.ts timestamp()).
func millis(ts *timestamppb.Timestamp) int64 {
	if ts == nil {
		return 0
	}
	if ms := ts.AsTime().UnixMilli(); ms > 0 {
		return ms
	}
	return 0
}

// AwaitingInOrder returns the downloads awaiting confirmation, oldest first (ties by id).
func AwaitingInOrder(list []*downexv1.Download) []*downexv1.Download {
	var out []*downexv1.Download
	for _, d := range list {
		// A held add (renew_check) is asked about only once downexd has checked its link
		// (docs/DESKTOP-SPEC.md §10).
		if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION && !d.GetRenewCheck() {
			out = append(out, d)
		}
	}
	slices.SortStableFunc(out, func(a, b *downexv1.Download) int {
		if c := millis(a.GetCreatedAt()) - millis(b.GetCreatedAt()); c != 0 {
			return sign(c)
		}
		return sign(a.GetId() - b.GetId())
	})
	return out
}

func sign(n int64) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Next is the download whose confirmation dialog comes next: preferred (the user asked for it)
// while it awaits confirmation, else the oldest one whose dialog the user has not closed
// (dismissed), else none (ok false). preferred 0 asks for none: download ids start at 1.
func Next(list []*downexv1.Download, dismissed map[int64]struct{}, preferred int64) (id int64, ok bool) {
	awaiting := AwaitingInOrder(list)
	if preferred != 0 {
		for _, d := range awaiting {
			if d.GetId() == preferred {
				return preferred, true
			}
		}
	}
	for _, d := range awaiting {
		if _, closed := dismissed[d.GetId()]; !closed {
			return d.GetId(), true
		}
	}
	return 0, false
}

// JoinDir is dir/name with the separator that dir already uses.
func JoinDir(dir, name string) string {
	if dir == "" {
		return name
	}
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	if strings.HasSuffix(dir, sep) {
		return dir + name
	}
	return dir + sep + name
}

// CategoryFolder is the folder for a download of category name ("" = none) that names no
// folder of its own (DESKTOP-SPEC §2): with category folders on, the category's folder or
// <defaultDir>/<name>; otherwise the default folder.
func CategoryFolder(s *downexv1.Settings, name string) string {
	var category *downexv1.Category
	if name != "" {
		for _, c := range s.GetCategories() {
			if c.GetName() == name {
				category = c
				break
			}
		}
	}
	if !s.GetUseCategoryFolders() || category == nil {
		return s.GetDefaultDir()
	}
	if category.GetDir() != "" {
		return category.GetDir()
	}
	return JoinDir(s.GetDefaultDir(), category.GetName())
}
