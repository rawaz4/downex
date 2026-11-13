package dialogs_test

import (
	"context"
	"slices"
	"testing"

	"downex/ui/core/media"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/viewstest"
)

// The dialog states of measure-dialogs.mjs that this package draws (ui/state/scene's
// Fixtures), and how each is opened from its scene: the rows its steps select and the opener
// call they end in.
var dialogStates = []string{"again-keep", "again-missing", "again-several", "restart-mixed", "restart", "remove", "remove-several", "open-program", "move-queue", "move-queue-many"}

// several are severalSteps' downloads (states.go).
var several = []string{"novel-draft.epub", "podcast-episode-41.mp3", "drone-footage-coast.mp4", "viewer-8.x86_64.rpm", "project-assets-362.zip", "nature-documentary-part1.mp4"}

// idsOf are the ids of the downloads named, in the list's order (the selection's).
func idsOf(t testing.TB, h *viewstest.Harness, names ...string) []int64 {
	t.Helper()
	s := h.Main.Downloads
	var ids []int64
	for _, id := range s.VisibleIDs() {
		if slices.Contains(names, s.NameOf(id)) {
			ids = append(ids, id)
		}
	}
	if len(ids) != len(names) {
		t.Fatalf("found %d of the downloads %v", len(ids), names)
	}
	return ids
}

// againOf is App.svelte's requestRestart for ids: the restartable ones, the completed ones'
// files asked for, and the reason.
func againOf(h *viewstest.Harness, ids []int64, reason views.RestartReason) views.RestartRequest {
	s := h.Main.Downloads
	var again []views.AgainFile
	for _, id := range ids {
		d, _ := s.Get(id)
		if d.GetState() != state.StateCompleted {
			continue
		}
		exists, err := h.Platform.FileExists(context.Background(), id)
		if err != nil {
			exists = true
		}
		again = append(again, views.AgainFile{Name: s.NameOf(id), Exists: exists, Site: media.IsSiteDownload(d)})
	}
	if len(again) == len(ids) {
		reason = views.RestartCompleted
	}
	return views.RestartRequest{IDs: ids, Reason: reason, Again: again}
}

// openState opens the dialog of a state in h, as its steps do.
func openState(t testing.TB, h *viewstest.Harness, name string) views.Modal {
	t.Helper()
	a := h.App
	var m views.Modal
	switch name {
	case "again-keep", "again-missing":
		m = dialogs.NewRestart(a, againOf(h, idsOf(t, h, "novel-draft.epub"), views.RestartManual), nil)
	case "again-several":
		m = dialogs.NewRestart(a, againOf(h, idsOf(t, h, several...), views.RestartManual), nil)
	case "restart-mixed":
		m = dialogs.NewRestart(a, againOf(h, idsOf(t, h, "photo-archive-277.7z", "novel-draft.epub"), views.RestartManual), nil)
	case "restart":
		m = dialogs.NewRestart(a, againOf(h, idsOf(t, h, "photo-archive-277.7z"), views.RestartManual), nil)
	case "remove":
		m = dialogs.NewRemove(a, views.RemoveRequest{IDs: idsOf(t, h, "podcast-episode-41.mp3")}, nil)
	case "remove-several":
		m = dialogs.NewRemove(a, views.RemoveRequest{IDs: idsOf(t, h, several...)}, nil)
	case "open-program":
		m = dialogs.NewOpen(a, views.OpenRequest{ID: idsOf(t, h, "editor-5.AppImage")[0]}, nil)
	case "move-queue", "move-queue-many":
		m = dialogs.NewMove(a, views.MoveRequest{IDs: idsOf(t, h, "setup-x64-312.exe")}, nil)
	default:
		t.Fatalf("no state %q", name)
	}
	// The references show the focus ring: the script opened every dialog from the keyboard or
	// with a click that Chromium follows with a visible focus.
	h.Window.SetFocusVisible(true)
	a.Dialogs.Show(m)
	h.Settle()
	return m
}
