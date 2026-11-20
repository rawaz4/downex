package settings_test

import (
	"testing"

	"downex/ui/views"
)

// TestSettingsFileCheckSwitch: Settings → Integration → Safety starts with the file check
// (docs/DESKTOP-SPEC.md §9.9): it shows Settings field 27 and saves it.
func TestSettingsFileCheckSwitch(t *testing.T) {
	r := setup(t, m6(), views.SettingsRequest{})
	r.click("integration")
	sw := r.sw("not what its name says")
	if !sw.On {
		t.Fatal("the file check shows off; the default is on")
	}
	if !hasText(r.root(), "asks before opening one that pretends to be another type") {
		t.Error("no hint")
	}
	r.toggle("not what its name says")
	r.save()
	if s := r.lastSave(); s.GetCheckDownloadedFiles() || s.GetMarkDownloadedFiles() || !s.GetClipboardMonitor() {
		t.Errorf("saved %v", s)
	}
}
