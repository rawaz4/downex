package app_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/palette"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
)

// Polite mode in the main window (docs/POLITE-SPEC.md §8): the limit menu's checkbox and the
// palette command each do one settings round-trip that keeps polite_target_ms, toast the
// result, and the status bar follows the status downexd streams.
func TestPoliteMode(t *testing.T) {
	t.Run("turns on from the limit menu and shows the status", func(t *testing.T) {
		f := newFixture(t, setup{settings: func(s *downexv1.Settings) { s.PoliteTargetMs = 80 }}, dl(1, state.StateQueued, nil))
		lm := f.m.Shell.StatusBar().LimitMenu()
		f.h.Click(lm.Trigger.Base())
		clickMenuItem(t, f.h, lm.Menu(), "Polite mode")
		f.h.Settle()
		if f.d.Count("updateSettings") != 1 || !f.d.Settings().GetPoliteMode() || f.d.Settings().GetPoliteTargetMs() != 80 {
			t.Fatalf("writes %d, written %v", f.d.Count("updateSettings"), f.d.Settings())
		}
		if !f.hasToast("Polite mode on") || !strings.Contains(lm.Trigger.Base().Semantics.Name, "Unlimited · Polite") {
			t.Errorf("toasts %q, trigger %q", f.toasts(), lm.Trigger.Base().Semantics.Name)
		}
		f.d.Emit(&downexv1.Event{Kind: &downexv1.Event_Polite{Polite: &downexv1.PoliteStatus{
			State: downexv1.PoliteState_POLITE_STATE_SLOWED, RateLimit: 2_400_000, DelayMs: 41, BaseMs: 20}}})
		f.h.Settle()
		if name := lm.Trigger.Base().Semantics.Name; !strings.HasPrefix(name, "Global speed limit: Polite 2.4 MB/s. Polite: slowed") {
			t.Errorf("slowed: trigger %q", name)
		}
		// The palette turns it off again.
		cmd := findCommand(f, "polite")
		if cmd.Label != "Turn off Polite mode" || cmd.Description != "Now slowed to 2.4 MB/s to keep the line smooth" {
			t.Errorf("palette command %+v", cmd)
		}
		if !f.h.App.Commands.Run("polite") {
			t.Fatal("the palette command did not run")
		}
		f.h.Settle()
		if f.d.Count("updateSettings") != 2 || f.d.Settings().GetPoliteMode() || f.d.Settings().GetPoliteTargetMs() != 80 {
			t.Fatalf("writes %d, written %v", f.d.Count("updateSettings"), f.d.Settings())
		}
		if !f.hasToast("Polite mode off") {
			t.Errorf("toasts %q", f.toasts())
		}
	})
	t.Run("says why it cannot while downexd is unreachable", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		f.d.SetDown(true)
		f.advance(3 * time.Second)
		if !f.h.App.Stale() {
			t.Fatal("not stale")
		}
		f.h.App.Act.SetPolite(true)
		f.h.Settle()
		if f.d.Count("updateSettings") != 0 || !f.hasToast(views.UnavailableText) {
			t.Errorf("writes %d, toasts %q", f.d.Count("updateSettings"), f.toasts())
		}
	})
	t.Run("reports a refused change", func(t *testing.T) {
		f := newFixture(t, setup{daemon: func(d *statetest.Daemon) {
			d.Before = func(name string, _ ...any) error {
				if name == "updateSettings" {
					return statetest.Err(connect.CodeInvalidArgument, "polite_target_ms must be 0 or between 10 and 200, got 5")
				}
				return nil
			}
		}}, dl(1, state.StateQueued, nil))
		f.h.App.Act.SetPolite(true)
		f.h.Settle()
		if !f.hasToast("Could not change Polite mode: ") || f.d.Settings().GetPoliteMode() {
			t.Errorf("toasts %q", f.toasts())
		}
	})
}

// findCommand is the palette's command id as the window lists it.
func findCommand(f *fixture, id string) palette.Command {
	f.t.Helper()
	for _, c := range f.h.App.Commands.List() {
		if c.ID == id {
			return c
		}
	}
	f.t.Fatalf("no command %q", id)
	return palette.Command{}
}
