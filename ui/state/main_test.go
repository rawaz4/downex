package state_test

import (
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMainReadsSettingsAndQueuesWhileDownexdAnswers(t *testing.T) {
	f := statetest.NewDaemon(dl(1, nil))
	f.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" }))
	s := f.Settings()
	s.Categories = []*downexv1.Category{{Name: "Clips", Extensions: []string{"bin"}}}
	c := statetest.NewClock(1_800_000_000_000)
	m := state.NewMain(state.MainOptions{Daemon: f, Events: &f.Events, Clock: c, Go: statetest.Sync})
	t.Cleanup(m.Dispose)
	m.Start()
	c.Advance(0) // the first status check
	if _, loaded := m.Settings(); !loaded || !m.Queues.Loaded() || !m.Downloads.Ready() {
		t.Fatal("not loaded")
	}
	// the categories of the settings group the list
	if m.Downloads.Counts().Categories[categories.Key("Clips")] != 1 {
		t.Fatal(m.Downloads.Counts().Categories)
	}
	m.Downloads.SetQueueFilter(2)

	// downexd stops: the list is stale, the queues and settings are read again later
	f.SetDown(true)
	c.Advance(state.StatusInterval)
	if _, loaded := m.Settings(); loaded || m.Queues.Loaded() || !m.Downloads.Stale() {
		t.Fatal("not invalidated")
	}
	// it is back without Night: read again, and the filter on Night shows every queue
	f.SetDown(false)
	f.DeleteQueueItem(2)
	reads := f.Count("getSettings")
	c.Advance(state.StatusInterval)
	eventually(t, "the reads", func() bool { _, ok := m.Settings(); return ok && m.Queues.Loaded() })
	if f.Count("getSettings") != reads+1 || m.Downloads.QueueFilter() != 0 || len(m.Queues.List()) != 1 {
		t.Fatal(f.Count("getSettings"), m.Downloads.QueueFilter(), len(m.Queues.List()))
	}
	// the focus reads the settings again
	m.Focus()
	if f.Count("getSettings") != reads+2 {
		t.Fatal(f.Count("getSettings"))
	}
}
