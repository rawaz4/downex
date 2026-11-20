package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// TestUpdateSettingsKeepsTheFileCheck: the frontend has no control for the file check, so a
// save that does not name it keeps the daemon's value; one that names it sets it.
func TestUpdateSettingsKeepsTheFileCheck(t *testing.T) {
	f := newFakeDaemon()
	f.settings = &downexv1.Settings{CheckDownloadedFiles: true, MarkDownloadedFiles: true}
	a, _ := newTestApp(t, f, nil)
	if _, err := a.UpdateSettings(json.RawMessage(`{"settings":{"markDownloadedFiles":false}}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings(); !got.GetCheckDownloadedFiles() || got.GetMarkDownloadedFiles() {
		t.Errorf("a save without the field: %v", got)
	}
	if _, err := a.UpdateSettings(json.RawMessage(`{"settings":{"checkDownloadedFiles":false}}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings(); got.GetCheckDownloadedFiles() {
		t.Errorf("a save that turns it off: %v", got)
	}
}

// TestOpenFileAsksBeforeDisguisedFiles: the Wails window, whose pages show no file check,
// still never opens a file that is not what its name says unasked (docs/DESKTOP-SPEC.md
// §9.7); declining opens nothing, and a yes opens it.
func TestOpenFileAsksBeforeDisguisedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "movie.mp4"))
	d := completedIn(dir, 1, "movie.mp4")
	d.FileCheck = &downexv1.FileCheck{Level: downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER,
		Summary: "This file is a Windows program, not an MP4 video.", Detail: "Files that pretend to be another type are a common way to spread malware."}
	h := &fakeHost{answer: false}
	a, sh := newTestApp(t, newFakeDaemon(d), h)
	if err := a.OpenFile(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if len(sh.opened) != 0 || len(h.confirms) != 1 ||
		h.confirms[0] != "This file is not what its name says\nThis file is a Windows program, not an MP4 video.\n\nFiles that pretend to be another type are a common way to spread malware." {
		t.Fatalf("declined: opened %v, asked %q", sh.opened, h.confirms)
	}
	h.answer = true
	if err := a.OpenFile(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if len(sh.opened) != 1 || !strings.HasSuffix(sh.opened[0], "movie.mp4") {
		t.Fatalf("confirmed: opened %v", sh.opened)
	}
}

// TestUpdateSettingsKeepsPoliteAndRenewLinks: the frontend has no controls for Polite mode
// or renewing links either, so a save that does not name them keeps the daemon's values;
// one that names them sets them.
func TestUpdateSettingsKeepsPoliteAndRenewLinks(t *testing.T) {
	f := newFakeDaemon()
	f.settings = &downexv1.Settings{PoliteMode: true, PoliteTargetMs: 80, RenewLinks: downexv1.RenewLinks_RENEW_LINKS_OFF}
	a, _ := newTestApp(t, f, nil)
	if _, err := a.UpdateSettings(json.RawMessage(`{"settings":{"markDownloadedFiles":false}}`)); err != nil {
		t.Fatal(err)
	}
	got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	if !got.GetPoliteMode() || got.GetPoliteTargetMs() != 80 || got.GetRenewLinks() != downexv1.RenewLinks_RENEW_LINKS_OFF {
		t.Errorf("a save without the fields: %v", got)
	}
	if _, err := a.UpdateSettings(json.RawMessage(`{"settings":{"politeMode":false,"polite_target_ms":0,"renewLinks":"RENEW_LINKS_ASK"}}`)); err != nil {
		t.Fatal(err)
	}
	got = f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	if got.GetPoliteMode() || got.GetPoliteTargetMs() != 0 || got.GetRenewLinks() != downexv1.RenewLinks_RENEW_LINKS_ASK {
		t.Errorf("a save that names them: %v", got)
	}
}
