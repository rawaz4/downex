package add

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/widgets"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// asyncGo runs background work on goroutines of its own, as the window does (the harness's Go
// is synchronous, which a web page's retry loop would block). inFlight counts the ones running.
func asyncGo(f func()) {
	inFlight.Add(1)
	go func() {
		defer inFlight.Add(-1)
		f()
	}()
}

var inFlight atomic.Int64

// settleAsync settles the harness until no background work is running and what it posted back
// has run, failing after 30 s: an answer (the folder's free space) can otherwise land after
// the harness settled, under a full parallel run, and show as a change of an idle dialog.
func settleAsync(t testing.TB, h *viewstest.Harness) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		h.Settle()
		if inFlight.Load() == 0 {
			h.Settle() // what the last of it posted
			if inFlight.Load() == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d background calls still running after 30 s", inFlight.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitUntil settles the harness until cond holds (background work posts back to the UI
// thread), failing after 5 s.
func waitUntil(t testing.TB, h *viewstest.Harness, what string, cond func() bool) {
	t.Helper()
	waitTicking(t, h, what, 0, cond)
}

// waitTicking is waitUntil that moves the scene and the clocks on by step between looks (the
// demo's ticking daemon: yt-dlp reads a page, the retry loop waits 2 s).
func waitTicking(t testing.TB, h *viewstest.Harness, what string, step time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.Settle()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
		if step > 0 {
			h.Settle()
			if cond() {
				return
			}
			if h.Mock != nil {
				h.Mock.Tick(float64(step.Milliseconds()))
			}
			h.Advance(step)
		}
	}
}

// fieldOf is the host's fake field behind an input or text area (nil before it is attached).
func fieldOf(w kit.Widget) *hostcaptest.Field {
	switch x := w.(type) {
	case *widgets.Input:
		f, _ := x.Native().(*hostcaptest.Field)
		return f
	case *widgets.TextArea:
		f, _ := x.Native().(*hostcaptest.Field)
		return f
	}
	return nil
}

// typeIn replaces a field's text as the user's typing does (fireEvent.input).
func typeIn(t testing.TB, h *viewstest.Harness, w kit.Widget, s string) {
	t.Helper()
	f := fieldOf(w)
	if f == nil {
		t.Fatalf("no native field for %T", w)
	}
	f.Replace(s)
	h.Settle()
}

// stateOf opens the dialog of an Add or Confirm reference state as the reference's script
// reached it (scene.FixtureState.Steps), and settles.
func stateOf(t testing.TB, h *viewstest.Harness, name string) views.Modal {
	t.Helper()
	if strings.HasPrefix(name, "confirm-") {
		return confirmState(t, h, name)
	}
	return addState(t, h, name)
}

func addState(t testing.TB, h *viewstest.Harness, name string) *Add {
	t.Helper()
	a := New(h.App, views.AddRequest{}, Options{Go: asyncGo})
	h.App.Dialogs.Show(a)
	h.Settle()
	fx, ok := scene.Fixture(name)
	if !ok {
		t.Fatalf("no fixture %q", name)
	}
	for _, st := range fx.Steps {
		switch st.Kind {
		case scene.StepKey: // Ctrl+N: the dialog is open already
		case scene.StepClick:
			switch st.Text {
			case "Video or stream":
				a.setMode(modeStream)
			case "Add video":
				h.Window.Focus(a.submit) // a click focuses the button (the script's el.focus(); el.click())
				a.onSubmit()
			}
		case scene.StepClickSelector:
			if strings.Contains(st.Selector, "more-toggle") {
				a.moreToggle.Press()
			}
		case scene.StepTypeIn:
			typeIn(t, h, a.urlsArea, st.Text)
			h.Window.SetFocusVisible(true) // the script typed with the keyboard (:focus-visible)
		case scene.StepFindMedia:
			typeIn(t, h, a.stream, st.Text)
			h.Window.SetFocusVisible(true) // the script typed with the keyboard (:focus-visible)
			h.Window.Focus(a.submit)       // "Find qualities" clicked: the button has the focus
			a.onSubmit()
			until := st.Selector
			waitTicking(t, h, name+" "+until+" (busy "+strconv.Itoa(int(a.busy))+")", 250*time.Millisecond, func() bool {
				if a.streamFailure != "" && until != ".failures" {
					t.Fatalf("%s: %s", name, a.streamFailure)
				}
				switch until {
				case ".found-mark":
					return a.Info() != nil && a.busy == idle
				case ".waiting":
					return a.busy != idle && !a.waitBox.Style.Hidden && a.settingUp()
				case ".getting":
					return a.busy != idle && !a.gettingRow.Style.Hidden && a.waiting != nil && a.elapsedSeconds() >= 3
				case ".failures":
					return !a.failBox.Style.Hidden
				}
				return true
			})
		case scene.StepWaitFor:
			waitTicking(t, h, name+" "+st.Selector, 250*time.Millisecond, func() bool {
				return a.busy != idle && a.waiting != nil && a.elapsedSeconds() >= 2
			})
		}
	}
	h.Settle()
	return a
}

func confirmState(t testing.TB, h *viewstest.Harness, name string) *Confirm {
	t.Helper()
	awaiting := h.Main.Downloads.Awaiting()
	if len(awaiting) == 0 {
		t.Fatalf("%s: nothing awaits confirmation", name)
	}
	d := awaiting[0]
	c := NewConfirm(h.App, d, ConfirmOptions{Waiting: len(awaiting), Go: asyncGo})
	h.App.Dialogs.Show(c)
	h.Settle()
	fx, _ := scene.Fixture(name)
	for _, st := range fx.Steps {
		switch st.Kind {
		case scene.StepClearFolder:
			typeIn(t, h, c.Folder, "")
		case scene.StepTypeIn:
			typeIn(t, h, c.Name, st.Text)
			h.Window.SetFocusVisible(true) // the script typed with the keyboard (:focus-visible)
		case scene.StepWait:
			h.Advance(time.Duration(st.Ms) * time.Millisecond)
		case scene.StepClick:
			if st.Text == "Start download" {
				c.confirm(true)
				waitUntil(t, h, name+" start", func() bool { return c.busy == "" })
			}
		}
	}
	h.Settle()
	return c
}

// cardOf is a modal's dialog card.
func cardOf(m views.Modal) *kit.Element {
	switch d := m.(type) {
	case *Add:
		return d.Card()
	case *Confirm:
		return d.Card()
	}
	return nil
}

var _ = downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED
