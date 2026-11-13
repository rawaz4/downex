package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
)

func TestPostActionBalloon(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	pending := func(a downexv1.PostAction, cancelled bool) *downexv1.PostActionPending {
		return &downexv1.PostActionPending{QueueId: 2, Action: a, Deadline: timestamppb.New(now.Add(60 * time.Second)), Cancelled: cancelled}
	}
	for _, c := range []struct {
		p     *downexv1.PostActionPending
		title string
	}{
		{pending(downexv1.PostAction_POST_ACTION_SHUTDOWN, false), "Shutting down in 60 s"},
		{pending(downexv1.PostAction_POST_ACTION_SLEEP, false), "Sleeping in 60 s"},
		{pending(downexv1.PostAction_POST_ACTION_HIBERNATE, false), "Hibernating in 60 s"},
		{pending(downexv1.PostAction_POST_ACTION_QUIT_DOWNEX, false), "Quitting Downex in 60 s"},
		{pending(downexv1.PostAction_POST_ACTION_SHUTDOWN, true), "Shutdown cancelled"},
	} {
		b, ok := postActionBalloon(c.p, now)
		if !ok || b.Title != c.title || b.Text == "" || b.Error {
			t.Errorf("%v: balloon %+v, %v; want %q", c.p, b, ok, c.title)
		}
	}
	if _, ok := postActionBalloon(pending(downexv1.PostAction_POST_ACTION_UNSPECIFIED, false), now); ok {
		t.Error("a balloon for no action")
	}
	// A countdown seen late shows the time that is left.
	if b, _ := postActionBalloon(pending(downexv1.PostAction_POST_ACTION_SLEEP, false), now.Add(45*time.Second)); b.Title != "Sleeping in 15 s" {
		t.Errorf("late balloon %q", b.Title)
	}
	if b := updateBalloon(&downexv1.UpdateInfo{LatestVersion: "0.5.0"}); b.Title != "Downex 0.5.0 is available" ||
		b.Text != "Click to open Downex and install it." {
		t.Errorf("update balloon %+v", b)
	}
}

// TestOpenKeyring: every daemon keeps its passwords with its own database (Windows:
// DPAPI ciphertext in the store; elsewhere files next to it) except the user's own
// daemon on macOS, which uses the keychain (not exercised here).
func TestOpenKeyring(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "downex.db")
	st, err := store.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k, err := openKeyring(false, db, st, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	const name = "site_login:downextest"
	if err := k.Set(name, []byte("pw-e2e")); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get(name); err != nil || string(got) != "pw-e2e" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if runtime.GOOS == "windows" {
		blob, ok, err := st.GetBlob(name)
		if err != nil || !ok || string(blob) == "pw-e2e" {
			t.Fatalf("blob in the store: %v %v (want ciphertext)", ok, err)
		}
	} else if _, err := os.Stat(filepath.Join(dir, secretsDirName)); err != nil {
		t.Fatalf("no secrets folder next to the database: %v", err)
	}
	if err := k.Delete(name); err != nil {
		t.Fatal(err)
	}
}
