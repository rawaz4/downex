package model

import (
	"slices"
	"time"
)

// QueueID identifies a download queue (docs/M6-SPEC.md §1).
type QueueID int64

// MainQueueID is the main queue: it exists in every store, cannot be deleted, runs by
// default and receives every download added without a queue.
const MainQueueID QueueID = 1

// PostAction is what happens once a queue has finished its downloads. The values match
// the API's PostAction.
type PostAction int

const (
	PostActionNone      PostAction = iota // nothing
	PostActionQuit                        // stop the daemon (and its windows)
	PostActionSleep                       // put the computer to sleep
	PostActionHibernate                   // hibernate the computer
	PostActionShutdown                    // shut the computer down

	// PostActionLast is the highest known action; stored values above it come from a newer build.
	PostActionLast = PostActionShutdown
)

func (a PostAction) String() string {
	switch a {
	case PostActionNone:
		return "none"
	case PostActionQuit:
		return "quit"
	case PostActionSleep:
		return "sleep"
	case PostActionHibernate:
		return "hibernate"
	case PostActionShutdown:
		return "shutdown"
	default:
		return "unknown"
	}
}

// Schedule is when a queue runs, in local time (docs/M6-SPEC.md §1).
type Schedule struct {
	Enabled bool
	// StartMinute and StopMinute are minutes after local midnight (0..1439). A stop
	// minute at or before the start minute ends the window on the next day.
	StartMinute int
	StopEnabled bool
	StopMinute  int
	// Weekdays has bit 0 for Monday up to bit 6 for Sunday (see WeekdayBit); 0 runs once,
	// on OnceDate ("YYYY-MM-DD", local).
	Weekdays      int
	OnceDate      string
	WakeComputer  bool // wake the computer from sleep for the start (Windows)
	RepeatMinutes int  // > 0: inside the window, queue unfinished downloads again every N minutes
}

// AllWeekdays is the Weekdays value of a schedule that runs every day.
const AllWeekdays = 1<<7 - 1

// WeekdayBit returns the bit of d in Schedule.Weekdays (bit 0 = Monday).
func WeekdayBit(d time.Weekday) int {
	return 1 << ((int(d) + 6) % 7)
}

// Queue is a persisted download queue.
type Queue struct {
	ID            QueueID
	Name          string
	Running       bool  // processing its downloads
	MaxConcurrent int   // 0 = the max_concurrent_downloads setting
	SpeedLimit    int64 // bytes/s, 0 = unlimited (a child of the global limit)
	Schedule      Schedule
	PostAction    PostAction
	// ScheduleMark is the time of the latest schedule event (start or stop) the daemon
	// has applied; events up to it are never applied again (zero: none).
	ScheduleMark time.Time
	// DownloadIDs are the queue's downloads in queue order, as QueueStore.ListQueues
	// reads them; queue writes ignore it (see QueueStore.MoveDownloads).
	DownloadIDs []DownloadID
}

// Clone returns a deep copy of q.
func (q Queue) Clone() Queue {
	q.DownloadIDs = slices.Clone(q.DownloadIDs)
	return q
}

// IsMain reports whether q is the main queue.
func (q Queue) IsMain() bool { return q.ID == MainQueueID }

// UniqueDownloadIDs returns ids without repeats, keeping the first occurrence of each.
func UniqueDownloadIDs(ids []DownloadID) []DownloadID {
	seen := make(map[DownloadID]bool, len(ids))
	out := make([]DownloadID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// PlaceDownloads returns the queue order that moving the downloads moved (without
// repeats) into a queue whose order is current yields: moved are taken out of current
// and inserted, in their order, at position, the 0-based index among the downloads that
// remain; a negative position or one past the end appends them. current is not modified.
func PlaceDownloads(current, moved []DownloadID, position int) []DownloadID {
	out := make([]DownloadID, 0, len(current)+len(moved))
	skip := make(map[DownloadID]bool, len(moved))
	for _, id := range moved {
		skip[id] = true
	}
	for _, id := range current {
		if !skip[id] {
			out = append(out, id)
		}
	}
	if position < 0 || position > len(out) {
		position = len(out)
	}
	return slices.Insert(out, position, moved...)
}
