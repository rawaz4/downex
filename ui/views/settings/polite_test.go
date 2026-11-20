package settings_test

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/validate"
	"downex/ui/views"
)

// Settings → Downloads has the Polite mode switch (docs/POLITE-SPEC.md §8): it shows the
// setting, saves it, and keeps polite_target_ms, which the window does not edit.
func TestPoliteSwitch(t *testing.T) {
	r := setup(t, clone(loaded(), func(s *downexv1.Settings) { s.PoliteTargetMs = 80 }), views.SettingsRequest{Page: validate.PageDownloads})
	sw := r.sw("Polite mode")
	if sw.On {
		t.Fatal("the switch is on for a setting that is off")
	}
	if !hasText(r.root(), "Slow down when downloads make calls, games or streaming lag; speed back up when the line is quiet.") {
		t.Error("no description")
	}
	r.toggle("Polite mode")
	r.save()
	want := clone(loaded(), func(s *downexv1.Settings) { s.PoliteMode, s.PoliteTargetMs = true, 80 })
	if got := r.lastSave(); !equal(got, want) {
		t.Errorf("saved\n%v\nwant\n%v", got, want)
	}

	r = setup(t, clone(loaded(), func(s *downexv1.Settings) { s.PoliteMode = true }), views.SettingsRequest{Page: validate.PageDownloads})
	if !r.sw("Polite mode").On {
		t.Error("the switch is off for a setting that is on")
	}
}
