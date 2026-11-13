package state_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

// checked is an automatic check's result: version latest found at t.
func checked(latest string, available bool, t time.Time) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Update{Update: &downexv1.UpdateInfo{
		CurrentVersion: "1.0.0", LatestVersion: latest, Available: available, Notes: "Faster starts.",
		CheckedAt: timestamppb.New(t),
	}}}
}

// TestUpdateOffer: the update events offer an available version until Later, which lasts
// until the next automatic check finds it again (per check, kept in the prefs file); a check
// that found nothing newer offers nothing.
func TestUpdateOffer(t *testing.T) {
	d := statetest.NewDaemon()
	storage := statetest.NewMemory(nil)
	u := state.NewUpdate(state.UpdateOptions{Daemon: d, Events: &d.Events, Storage: storage})
	u.Start()
	defer u.Dispose()
	changes := 0
	defer u.Changes.Subscribe(func() { changes++ })()

	if _, ok := u.Offer(); ok {
		t.Fatal("an offer before any check")
	}
	day := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	d.Emit(checked("1.0.0", false, day)) // up to date
	if _, ok := u.Offer(); ok || u.Info() == nil || changes != 1 {
		t.Fatalf("an up-to-date check: offer %v, info %v, %d changes", ok, u.Info(), changes)
	}
	d.Emit(checked("1.0.1", true, day))
	info, ok := u.Offer()
	if !ok || info.GetLatestVersion() != "1.0.1" {
		t.Fatalf("offer %v, %v", info, ok)
	}

	// Later: put off, remembered per version and check.
	u.Later(info)
	if _, ok := u.Offer(); ok {
		t.Error("offered after Later")
	}
	if v, _, _ := storage.Get(state.UpdateLaterKey); v != state.UpdateOfferKey(info) || v != "1.0.1@"+strconv.FormatInt(day.UnixMilli(), 10) {
		t.Errorf("stored %q, want the offer's key", v)
	}
	// The same check's result again (a window's new Watch stream): still put off.
	d.Emit(checked("1.0.1", true, day))
	if _, ok := u.Offer(); ok {
		t.Error("the same check's offer came back")
	}
	// A window opened later reads the choice.
	u2 := state.NewUpdate(state.UpdateOptions{Daemon: d, Storage: storage})
	u2.Apply([]*downexv1.Event{checked("1.0.1", true, day)})
	if _, ok := u2.Offer(); ok {
		t.Error("a later window offered what was put off")
	}
	// The next automatic check (the next day, or after downexd started) finds it again.
	d.Emit(checked("1.0.1", true, day.Add(24*time.Hour)))
	if info, ok := u.Offer(); !ok || info.GetLatestVersion() != "1.0.1" {
		t.Errorf("the next check: offer %v, %v", info, ok)
	}
	// Without storage Later lasts for the session.
	u3 := state.NewUpdate(state.UpdateOptions{Daemon: d, Storage: statetest.Broken{}})
	u3.Apply([]*downexv1.Event{checked("1.0.2", true, day)})
	info3, _ := u3.Offer()
	u3.Later(info3)
	if _, ok := u3.Offer(); ok {
		t.Error("offered after Later with a broken storage")
	}
}

// TestUpdateInstall: Install runs InstallUpdate once at a time; a failure keeps the offer and
// says why, a success ends it and runs Installed (the macOS window's restart).
func TestUpdateInstall(t *testing.T) {
	d := statetest.NewDaemon()
	var installed []*downexv1.UpdateInfo
	u := state.NewUpdate(state.UpdateOptions{Daemon: d, Events: &d.Events,
		Installed: func(i *downexv1.UpdateInfo) { installed = append(installed, i) }})
	u.Start()
	defer u.Dispose()
	d.Emit(checked("1.0.1", true, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)))

	if u.Install(context.Background()) {
		t.Fatal("Install succeeded with nothing to install")
	}
	if u.Failure() != "no update is available" || u.Busy() || u.Installed() != nil || len(installed) != 0 {
		t.Errorf("after a failure: %q, busy %v, installed %v", u.Failure(), u.Busy(), u.Installed())
	}
	if _, ok := u.Offer(); !ok {
		t.Error("a failed install took the offer away")
	}

	d.SetInstallUpdate(&downexv1.UpdateInfo{LatestVersion: "1.0.1", Available: true})
	busy := false
	d.Before = func(name string, _ ...any) error {
		if name == "installUpdate" {
			busy = u.Busy()
		}
		return nil
	}
	if !u.Install(context.Background()) {
		t.Fatalf("Install failed: %q", u.Failure())
	}
	if !busy {
		t.Error("not busy while InstallUpdate ran")
	}
	if u.Failure() != "" || u.Installed().GetLatestVersion() != "1.0.1" || len(installed) != 1 {
		t.Errorf("after the install: %q, %v, hook %v", u.Failure(), u.Installed(), installed)
	}
	if _, ok := u.Offer(); ok {
		t.Error("offered after the install started")
	}
	if u.Install(context.Background()) {
		t.Error("a second Install ran")
	}

	// Settings → Updates installs through its own call and tells the store.
	hooks := 0
	s := state.NewUpdate(state.UpdateOptions{Daemon: d, Installed: func(*downexv1.UpdateInfo) { hooks++ }})
	s.Apply([]*downexv1.Event{checked("1.0.1", true, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))})
	s.MarkInstalled(&downexv1.UpdateInfo{LatestVersion: "1.0.1"})
	s.MarkInstalled(&downexv1.UpdateInfo{LatestVersion: "1.0.1"})
	if _, ok := s.Offer(); ok || s.Installed() == nil || hooks != 1 {
		t.Errorf("after MarkInstalled: offer %v, installed %v, %d hook runs", ok, s.Installed(), hooks)
	}
}
