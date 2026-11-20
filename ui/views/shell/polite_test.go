package shell_test

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/shell"
)

func politeStatus(s downexv1.PoliteState, rate int64) *downexv1.PoliteStatus {
	return &downexv1.PoliteStatus{State: s, RateLimit: rate, DelayMs: -1, BaseMs: -1}
}

// The limit control's text and tooltip line with Polite mode (docs/POLITE-SPEC.md §8).
func TestPoliteTexts(t *testing.T) {
	const (
		slowed    = downexv1.PoliteState_POLITE_STATE_SLOWED
		measuring = downexv1.PoliteState_POLITE_STATE_MEASURING
		watching  = downexv1.PoliteState_POLITE_STATE_WATCHING
	)
	for _, tc := range []struct {
		limit int64
		on    bool
		state downexv1.PoliteState
		rate  int64
		want  string
	}{
		{0, false, 0, 0, "Unlimited"},
		{10 * mb, false, 0, 0, "Limit 10 MB/s"},
		{0, true, 0, 0, "Unlimited · Polite"},
		{0, true, watching, 0, "Unlimited · Polite"},
		{10 * mb, true, watching, 0, "Limit 10 MB/s · Polite"},
		{0, true, slowed, 2_400_000, "Polite 2.4 MB/s"},
		{10 * mb, true, slowed, 2_400_000, "Polite 2.4 MB/s"},
		{1 * mb, true, slowed, 2_400_000, "Limit 1 MB/s · Polite"}, // the user's limit is tighter
		{0, true, measuring, 65536, "Polite: measuring…"},
	} {
		if got := shell.PoliteLimitText(tc.limit, true, tc.on, tc.state, tc.rate); got != tc.want {
			t.Errorf("PoliteLimitText(%d, %v, %v, %d) = %q, want %q", tc.limit, tc.on, tc.state, tc.rate, got, tc.want)
		}
	}
	for _, tc := range []struct {
		on    bool
		state downexv1.PoliteState
		want  string
	}{
		{false, slowed, ""},
		{true, 0, "Polite mode is on: Downex slows down if its downloads make the line laggy."},
		{true, downexv1.PoliteState_POLITE_STATE_IDLE, "Polite mode is on: Downex slows down if its downloads make the line laggy."},
		{true, watching, "Polite mode is on: Downex slows down if its downloads make the line laggy."},
		{true, slowed, "Polite: slowed to keep the line smooth. Downloads are held to 2.4 MB/s while the line is busy."},
		{true, measuring, "Polite: measuring the line…"},
		{true, downexv1.PoliteState_POLITE_STATE_NO_SIGNAL, "Polite mode can't measure this connection, so downloads run at full speed."},
		{true, downexv1.PoliteState_POLITE_STATE_UNAVAILABLE, "Polite mode isn't available with this downexd."},
	} {
		if got := shell.PoliteLine(tc.on, tc.state, 2_400_000); got != tc.want {
			t.Errorf("PoliteLine(%v, %v) = %q, want %q", tc.on, tc.state, got, tc.want)
		}
	}
	it := shell.PoliteItem(true)
	if it.ID != "polite" || it.Label != "Polite mode" || !it.Checked || !it.Separated ||
		it.Title != "Slow down when downloads make calls, games or streaming lag" {
		t.Errorf("PoliteItem = %+v", it)
	}
}

// The limit menu's "Polite mode" checkbox toggles the setting; the control, its tooltip and its
// accessible name follow the state, in the warning ink while it slows downloads.
func TestStatusBarPolite(t *testing.T) {
	var toggles []bool
	b := shell.NewStatusBar(shell.StatusOptions{
		OnLimit:  func(views.LimitChoice) {},
		OnPolite: func(on bool) { toggles = append(toggles, on) },
	})
	h := rig(t, b)
	b.Update(barState(nil))
	h.Settle()
	lm := b.LimitMenu()
	trigger := lm.Trigger.Base()
	if trigger.Semantics.Name != "Global speed limit: Unlimited" || b.Parts()["limit"].Tooltip != "Global speed limit" || b.LimitWarns() {
		t.Errorf("off: %q / %q / warn %v", trigger.Semantics.Name, b.Parts()["limit"].Tooltip, b.LimitWarns())
	}
	h.Click(trigger)
	m := lm.Menu()
	if m == nil {
		t.Fatal("no menu")
	}
	boxes := nodes(m.Root(), kit.RoleMenuItemCheckBox)
	if len(boxes) != 1 || boxes[0].Name != "Polite mode" || boxes[0].Toggle != kit.ToggleOff {
		t.Fatalf("checkboxes %+v", boxes)
	}
	clickItem(t, h, m, "Polite mode")
	if len(toggles) != 1 || !toggles[0] {
		t.Fatalf("toggles %v", toggles)
	}

	// On, slowed by Polite mode: "Polite 2.4 MB/s" in the warning ink.
	b.Update(barState(func(s *shell.StatusState) {
		s.PoliteOn, s.Polite = true, politeStatus(downexv1.PoliteState_POLITE_STATE_SLOWED, 2_400_000)
	}))
	h.Settle()
	line := "Polite: slowed to keep the line smooth. Downloads are held to 2.4 MB/s while the line is busy."
	if !strings.Contains(strings.Join(b.Texts(), " | "), "Polite 2.4 MB/s") || !b.LimitWarns() ||
		trigger.Semantics.Name != "Global speed limit: Polite 2.4 MB/s. "+line ||
		b.Parts()["limit"].Tooltip != "Global speed limit. "+line {
		t.Errorf("slowed: %q / %q / %q / warn %v", b.Texts(), trigger.Semantics.Name, b.Parts()["limit"].Tooltip, b.LimitWarns())
	}
	h.Click(trigger)
	if boxes := nodes(lm.Menu().Root(), kit.RoleMenuItemCheckBox); len(boxes) != 1 || boxes[0].Toggle != kit.ToggleOn {
		t.Fatalf("checked box %+v", boxes)
	}
	clickItem(t, h, lm.Menu(), "Polite mode")
	if len(toggles) != 2 || toggles[1] {
		t.Fatalf("toggles %v", toggles)
	}

	// On and watching: the limit's text with " · Polite", not a warning.
	b.Update(barState(func(s *shell.StatusState) {
		s.PoliteOn, s.Polite = true, politeStatus(downexv1.PoliteState_POLITE_STATE_WATCHING, 0)
	}))
	h.Settle()
	if !strings.Contains(strings.Join(b.Texts(), " | "), "Unlimited · Polite") || b.LimitWarns() {
		t.Errorf("watching: %q, warn %v", b.Texts(), b.LimitWarns())
	}
	// The setting off wins over a status that has not caught up.
	b.Update(barState(func(s *shell.StatusState) {
		s.PoliteOn, s.Polite = false, politeStatus(downexv1.PoliteState_POLITE_STATE_SLOWED, 2_400_000)
	}))
	h.Settle()
	if txt := strings.Join(b.Texts(), " | "); strings.Contains(txt, "Polite") || b.LimitWarns() {
		t.Errorf("off with a stale status: %q", txt)
	}
	// While downexd is unreachable the tooltip says so.
	b.Update(barState(func(s *shell.StatusState) { s.Stale, s.PoliteOn = true, true }))
	if b.Parts()["limit"].Tooltip != "Unavailable while downexd is unreachable" {
		t.Errorf("stale tooltip %q", b.Parts()["limit"].Tooltip)
	}
}

// Without OnPolite the menu has no Polite mode item and the control ignores the status.
func TestStatusBarWithoutPolite(t *testing.T) {
	b := shell.NewStatusBar(shell.StatusOptions{OnLimit: func(views.LimitChoice) {}})
	h := rig(t, b)
	b.Update(barState(func(s *shell.StatusState) {
		s.PoliteOn, s.Polite = true, politeStatus(downexv1.PoliteState_POLITE_STATE_SLOWED, 2_400_000)
	}))
	h.Settle()
	h.Click(b.LimitMenu().Trigger.Base())
	if boxes := nodes(b.LimitMenu().Menu().Root(), kit.RoleMenuItemCheckBox); len(boxes) != 0 {
		t.Errorf("checkboxes %+v", boxes)
	}
	if strings.Contains(strings.Join(b.Texts(), " | "), "Polite") {
		t.Errorf("texts %q", b.Texts())
	}
}
