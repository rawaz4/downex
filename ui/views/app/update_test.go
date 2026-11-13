package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/confirm"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/viewstest"
)

// updateChecked is the update event of an automatic check that found latest at day.
func updateChecked(latest string, available bool, day time.Time) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Update{Update: &downexv1.UpdateInfo{
		CurrentVersion: "1.0.0", LatestVersion: latest, Available: available,
		Notes: "Downloads start faster.", CheckedAt: timestamppb.New(day),
	}}}
}

func (f *fixture) popup() *dialogs.Update { return f.m.UpdatePopup() }

func (f *fixture) emitUpdate(ev *downexv1.Event) {
	f.d.Emit(ev)
	f.h.Settle()
}

// TestUpdatePopup: an automatic check's update event opens the pop-up (the notice → pop-up
// state); Later, Escape and the close button put that check's offer off, per version and
// check, until the next automatic check finds it again; Update now runs InstallUpdate with
// its progress, failure and installing texts.
func TestUpdatePopup(t *testing.T) {
	day := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

	t.Run("offers an available version, Later puts this check's offer off", func(t *testing.T) {
		f := newFixture(t, setup{})
		f.emitUpdate(updateChecked("1.0.0", false, day))
		if f.popup() != nil {
			t.Fatal("a pop-up for an up-to-date check")
		}
		f.emitUpdate(updateChecked("1.0.1", true, day))
		p := f.popup()
		if p == nil || f.h.App.Dialogs.Kind() != views.ModalUpdate {
			t.Fatalf("no pop-up: dialog %v", f.h.App.Dialogs.Kind())
		}
		if txt := dialogs.UpdateTexts(p.Info()); txt.Title != "Downex 1.0.1 is available" ||
			!strings.HasPrefix(txt.Description, "You have version 1.0.0.") || txt.Notes != "Downloads start faster." {
			t.Errorf("texts %+v", txt)
		}
		// It opened by itself: it takes no focus from what had it.
		if _, now := p.Buttons(); f.h.Window.Focused() == &now.Element {
			t.Error("Update now has the focus")
		}
		later, _ := p.Buttons()
		f.h.Click(&later.Element)
		if f.popup() != nil || f.h.App.Dialogs.DialogOpen() {
			t.Fatal("the pop-up stayed after Later")
		}
		if v, _, _ := f.h.Storage.Get(state.UpdateLaterKey); !strings.HasPrefix(v, "1.0.1@") {
			t.Errorf("the offer put off is stored as %q", v)
		}
		// The same check's result again (a new Watch stream): nothing.
		f.emitUpdate(updateChecked("1.0.1", true, day))
		if f.popup() != nil {
			t.Error("the offer put off came back before the next check")
		}
		// The next automatic check finds it again.
		f.emitUpdate(updateChecked("1.0.1", true, day.Add(24*time.Hour)))
		if f.popup() == nil {
			t.Fatal("the next check's offer did not show")
		}
		// Escape is Later too.
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		if f.popup() != nil {
			t.Fatal("the pop-up stayed after Escape")
		}
		f.emitUpdate(updateChecked("1.0.1", true, day.Add(24*time.Hour)))
		if f.popup() != nil {
			t.Error("Escape did not put the offer off")
		}
		if f.calls("installUpdate") > 0 {
			t.Error("Later installed something")
		}
	})

	t.Run("waits while downexd is unreachable", func(t *testing.T) {
		f := newFixture(t, setup{daemon: func(d *statetest.Daemon) { d.SetDown(true) }})
		f.emitUpdate(updateChecked("1.0.1", true, day))
		if !f.ds().Stale() || f.popup() != nil {
			t.Fatalf("stale %v, pop-up %v: want none while downexd is unreachable", f.ds().Stale(), f.popup())
		}
		f.d.SetDown(false)
		f.advance(10 * time.Second)
		if f.ds().Stale() || f.popup() == nil {
			t.Errorf("stale %v, pop-up %v: want the pop-up once downexd answers again", f.ds().Stale(), f.popup())
		}
	})

	t.Run("waits for another dialog", func(t *testing.T) {
		f := newFixture(t, setup{})
		f.h.App.Open.Settings(views.SettingsRequest{})
		f.h.Settle()
		f.emitUpdate(updateChecked("1.0.1", true, day))
		if f.popup() != nil || f.h.App.Dialogs.Kind() != views.ModalSettings {
			t.Fatal("the pop-up replaced an open dialog")
		}
		f.last().Dlg.Remove()
		f.h.Settle()
		if f.popup() == nil {
			t.Fatal("the pop-up did not show once the dialog had closed")
		}
		// A later check that offers nothing takes it away (without putting it off).
		f.emitUpdate(updateChecked("1.0.1", false, day.Add(24*time.Hour)))
		if f.popup() != nil || f.h.App.Dialogs.DialogOpen() {
			t.Error("the pop-up stayed for an up-to-date check")
		}
		if _, ok, _ := f.h.Storage.Get(state.UpdateLaterKey); ok {
			t.Error("taken away is not put off")
		}
	})

	t.Run("gives way to a confirmation that arrives while it is open", func(t *testing.T) {
		f := newFixture(t, setup{})
		f.emitUpdate(updateChecked("1.0.1", true, day))
		if f.popup() == nil {
			t.Fatal("no pop-up")
		}
		// The browser hands over a download that waits for its confirmation.
		f.d.Emit(statetest.Updated(awaiting(9, 1, "handoff.zip")))
		f.h.Settle()
		f.h.Settle()
		if f.popup() != nil || f.confirming() != 9 || f.h.App.Dialogs.Kind() != views.ModalConfirm {
			t.Fatalf("pop-up %v, confirming %d, dialog %v: want the confirmation", f.popup(), f.confirming(), f.h.App.Dialogs.Kind())
		}
		if _, ok, _ := f.h.Storage.Get(state.UpdateLaterKey); ok {
			t.Error("giving way put the offer off")
		}
		// Once the download is confirmed (elsewhere, here) the pop-up comes back.
		f.d.Emit(statetest.Updated(dl(9, state.StateQueued, func(d *downexv1.Download) { d.Filename = "handoff.zip" })))
		f.h.Settle()
		f.h.Settle()
		if f.confirming() != 0 || f.popup() == nil {
			t.Fatalf("confirming %d, pop-up %v: want the pop-up again", f.confirming(), f.popup())
		}
	})

	t.Run("Update now: guard, failure, then installing", func(t *testing.T) {
		f := newFixture(t, setup{})
		f.emitUpdate(updateChecked("1.0.1", true, day))
		p := f.popup()
		if p == nil {
			t.Fatal("no pop-up")
		}
		later, now := p.Buttons()
		// Right after it appeared by itself a click is not a decision about it.
		f.h.Click(&now.Element)
		if f.calls("installUpdate") > 0 {
			t.Fatal("Update now ran right after the pop-up appeared")
		}
		f.advance(confirm.Guard + 50*time.Millisecond)
		// The fake downexd refuses: the reason shows, the pop-up stays to try again.
		f.h.Click(&now.Element)
		if !(f.calls("installUpdate") > 0) {
			t.Fatal("Update now did not call InstallUpdate")
		}
		n := p.Status()
		if n == nil || n.Tone != views.NoteFail || n.Text.Text() != "The update could not be installed: no update is available" {
			t.Fatalf("failure note %v", n)
		}
		if p.Busy() || later.Disabled || now.Disabled || f.popup() != p {
			t.Error("after a failure the pop-up is not ready again")
		}
		// downexd starts the installation (it exits for it).
		f.d.SetInstallUpdate(&downexv1.UpdateInfo{CurrentVersion: "1.0.0", LatestVersion: "1.0.1", Available: true})
		f.h.Click(&now.Element)
		n = p.Status()
		if n == nil || n.Tone != views.NoteInfo || !strings.HasPrefix(n.Text.Text(), "Downex is installing version 1.0.1") {
			t.Fatalf("installing note %v", n)
		}
		if p.CloseButton() == nil || f.h.App.Main.Update.Installed() == nil {
			t.Error("no Close button or the store does not know")
		}
		// downexd goes away for the installation: the pop-up stays as it is.
		f.emitUpdate(updateChecked("1.0.1", false, day.Add(time.Hour)))
		if f.popup() != p {
			t.Error("the installing pop-up was taken away")
		}
		f.h.Click(&p.CloseButton().Element)
		if f.popup() != nil {
			t.Error("Close left the pop-up")
		}
		if _, ok, _ := f.h.Storage.Get(state.UpdateLaterKey); ok {
			t.Error("an installed update was put off")
		}
	})
}

// TestUpdatePopupShots writes the pop-up's pictures (offered, failed, installing; light and
// dark) into $UPDATE_SHOTS for a look. It asserts nothing.
func TestUpdatePopupShots(t *testing.T) {
	dir := os.Getenv("UPDATE_SHOTS")
	if dir == "" {
		t.Skip("UPDATE_SHOTS not set")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	day := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for _, theme := range []string{"light", "dark"} {
		for _, step := range []string{"offer", "failed", "installing"} {
			d := statetest.NewDaemon(
				dl(1, state.StateDownloading, func(d *downexv1.Download) { d.Filename = "holiday-video.mp4" }),
				dl(2, state.StateCompleted, func(d *downexv1.Download) { d.Filename = "setup-tools.zip" }),
			)
			h, m := mainWindow(t, viewstest.Options{Daemon: d, Real: true, Theme: theme, Scale: 1.25})
			shotTheme(h, theme)
			ev := updateChecked("1.0.1", true, day)
			ev.GetUpdate().Notes = "Downloads start faster. Video pages show their qualities sooner. Fixes for resuming paused downloads."
			d.Emit(ev)
			h.Settle()
			p := m.UpdatePopup()
			if p == nil {
				t.Fatal("no pop-up")
			}
			p.Guard = -1
			_, now := p.Buttons()
			switch step {
			case "failed":
				h.Click(&now.Element)
			case "installing":
				d.SetInstallUpdate(ev.GetUpdate())
				h.Click(&now.Element)
			}
			h.Advance(time.Second)
			img, err := h.Render()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "update-"+step+"-"+theme+".png")
			if err := viewstest.WritePNG(path, img); err != nil {
				t.Fatal(err)
			}
			t.Log(path)
		}
	}
}
