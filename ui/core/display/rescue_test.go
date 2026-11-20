package display

import "testing"

// TestRescueTexts: docs/DESKTOP-SPEC.md §11 and the shared table of BROWSER-SPEC §6 R-E7.
func TestRescueTexts(t *testing.T) {
	for _, c := range []struct{ code, goos, want string }{
		{"rescue_changed", "windows", "The file on the server is not the one the browser started, so the saved part cannot be used."},
		{"rescue_no_access", "windows", "Downex is not allowed to open the partial file."},
		{"rescue_no_access", "darwin", "Downex is not allowed to open the partial file. Allow Downex Agent in System Settings > Privacy & Security > Files and Folders."},
		{"rescue_locked", "darwin", "The browser is still using the partial file. Try again in a few seconds."},
		{"disk_full", "windows", ""},
	} {
		if got := RescueErrorText(c.code, c.goos); got != c.want {
			t.Errorf("RescueErrorText(%s, %s) = %q", c.code, c.goos, got)
		}
	}
	if !RescueFileError("rescue_not_found") || !RescueFileError("invalid_request") || RescueFileError("rescue_changed") || RescueFileError("network") {
		t.Error("RescueFileError")
	}
	if s := RescuedText(412_000_000); s != "412 MB from a partial file" {
		t.Errorf("RescuedText = %q", s)
	}
	if s := RescuedToast("disk.iso", 412_000_000); s != "Continuing “⁨disk.iso⁩” from 412 MB" {
		t.Errorf("RescuedToast = %q", s)
	}
	for name, want := range map[string][2]string{
		"disk.iso.crdownload":         {"Chrome partial file (.crdownload)", "disk.iso"},
		"disk.iso.CRDOWNLOAD":         {"Chrome partial file (.crdownload)", "disk.iso"},
		"movie.mkv.part":              {"Firefox partial file (.part)", "movie.mkv"},
		"Unconfirmed 4711.crdownload": {"Chrome partial file (.crdownload)", ""},
		"half.bin":                    {"partial file", "half.bin"},
	} {
		if k, s := PartialKind(name), RescueSaveAs(name); k != want[0] || s != want[1] {
			t.Errorf("%s: kind %q, save as %q", name, k, s)
		}
	}
}
