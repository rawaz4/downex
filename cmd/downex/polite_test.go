package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// settings --polite and --polite-target (docs/POLITE-SPEC.md §8): one full round trip,
// the line and the JSON fields, and the usage errors.
func TestSettingsPolite(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	if r := h.run(ctx, "settings"); !strings.Contains(r.stdout, "Polite mode:                off") {
		t.Errorf("settings lacks the Polite mode line:\n%s", r.stdout)
	}
	r := h.run(ctx, "settings", "--polite", "on", "--json")
	if r.code != exitOK {
		t.Fatalf("settings --polite on: %v", r)
	}
	upd := f.rec().updates
	if len(upd) != 1 || !upd[0].GetPoliteMode() || upd[0].GetPoliteTargetMs() != 0 {
		t.Fatalf("update = %v", upd)
	}
	var v settingsView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || !v.PoliteMode || v.PoliteTargetMs != 0 ||
		!strings.Contains(r.stdout, `"politeMode": true`) || !strings.Contains(r.stdout, `"politeTargetMs": 0`) {
		t.Errorf("--json: %+v %v\n%s", v, err, r.stdout)
	}
	r = h.run(ctx, "settings", "--polite-target", "50")
	if r.code != exitOK || !strings.Contains(r.stdout, "Polite mode:                on (target +50 ms)") {
		t.Errorf("--polite-target 50: %v", r)
	}
	if upd := f.rec().updates; len(upd) != 2 || !upd[1].GetPoliteMode() || upd[1].GetPoliteTargetMs() != 50 {
		t.Errorf("updates = %v", upd)
	}
	r = h.run(ctx, "settings", "--polite-target", "0")
	if !strings.Contains(r.stdout, "on (target +30 ms)") {
		t.Errorf("the default target is not shown as 30 ms:\n%s", r.stdout)
	}
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"settings", "--polite-target", "5"}, "--polite-target must be between 10 and 200 (or 0 for the default), got 5"},
		{[]string{"settings", "--polite-target", "201"}, "--polite-target must be between 10 and 200 (or 0 for the default), got 201"},
		{[]string{"settings", "--polite", "maybe"}, "on"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if n := len(f.rec().updates); n != 3 {
		t.Errorf("%d updates, want 3 (refused flags send none)", n)
	}
}

// daemon status prints a Polite mode line for every state, none for an older daemon.
func TestDaemonStatusPolite(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	for _, tc := range []struct {
		st   *downexv1.PoliteStatus
		line string
	}{
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_OFF, DelayMs: -1, BaseMs: -1}, "off"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_IDLE, DelayMs: -1, BaseMs: -1}, "on (nothing downloading)"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_WATCHING, DelayMs: 4, BaseMs: 21}, "on, full speed (line delay +4 ms)"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_WATCHING, DelayMs: -1, BaseMs: -1}, "on, full speed"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_SLOWED, RateLimit: 2_400_000, DelayMs: 42, BaseMs: 20},
			"slowed to 2.4 MB/s to keep the line smooth (line delay +42 ms)"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_MEASURING, RateLimit: 65536, DelayMs: -1}, "measuring the line"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_NO_SIGNAL, DelayMs: -1}, "on, but this connection can't be measured: full speed"},
		{&downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_UNAVAILABLE, DelayMs: -1}, "not available in this downexd"},
	} {
		f.do(func() { f.status.Polite = tc.st })
		r := h.run(ctx, "daemon", "status")
		if want := "Polite mode:  " + tc.line + "\n"; r.code != exitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("%v: want the line %q in\n%s", tc.st.GetState(), want, r.stdout)
		}
	}
	f.do(func() {
		f.status.Polite = &downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_SLOWED, RateLimit: 2_400_000, DelayMs: 42, BaseMs: 20}
	})
	r := h.run(ctx, "daemon", "status", "--json")
	var st statusView
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil || st.Polite == nil ||
		*st.Polite != (politeView{State: "slowed", RateLimit: 2_400_000, DelayMs: 42, BaseMs: 20}) {
		t.Errorf("status --json: %+v %v\n%s", st.Polite, err, r.stdout)
	}
	// An older daemon (no status, or state UNSPECIFIED) gets no line and no object.
	for _, old := range []*downexv1.PoliteStatus{nil, {}} {
		f.do(func() { f.status.Polite = old })
		if r := h.run(ctx, "daemon", "status"); strings.Contains(r.stdout, "Polite") {
			t.Errorf("an older daemon's status mentions Polite mode:\n%s", r.stdout)
		}
		if r := h.run(ctx, "daemon", "status", "--json"); strings.Contains(r.stdout, "polite") {
			t.Errorf("an older daemon's JSON status has polite:\n%s", r.stdout)
		}
	}
}
