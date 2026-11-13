//go:build windows

package win

import (
	"downex/ui/kit/widgets"
	"runtime"
	"strings"
	"testing"
	"time"

	"downex/ui/win/uia"
	"downex/ui/win/uia/uiaclient"
)

func collectEdits(wk *uiaclient.TreeWalker, e *uiaclient.Element, depth int, path string, out *strings.Builder) {
	n, _ := e.Name()
	ct, _ := e.ControlType()
	p := path + "/" + n
	if ct == uia.UIA_EditControlTypeId {
		out.WriteString(p + "\n")
	}
	if depth > 12 {
		return
	}
	c, err := wk.GetFirstChildElement(e)
	for err == nil && c != nil {
		collectEdits(wk, c, depth+1, p, out)
		nx, nerr := wk.GetNextSiblingElement(c)
		c.Release()
		c, err = nx, nerr
	}
}

// Each hosted native field appears once to UI Automation, at its node, also while a layer
// covers the place of one of them (a dialog's backdrop over the title bar's search): before,
// that one showed again as a bare nameless Edit under the window (three in the Queues
// dialog's drive trees), since the hit test at its centre found the backdrop. While the title
// bar is modal its search's native field hides (the kit draws its text, faded): the search is
// then its node alone, and the hidden window shows nowhere, nameless or not.
func TestHostedFieldsAppearOnce(t *testing.T) {
	a, w, pg := openUIA(t)
	_ = onKit(pg.win, func() {
		in1 := widgets.NewInput("Night", widgets.InputOptions{Name: "Name"}, nil)
		in2 := widgets.NewInput("3", widgets.InputOptions{Name: "Downloads at once"}, nil)
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Queues"})
		d.Body.Add(in1, in2)
		d.Show(pg.win)
	})
	pump(t, a, w, "settle", 2*time.Second, func() bool { return len(w.fields) >= 3 })
	time.Sleep(200 * time.Millisecond)
	want := []string{"/Downex//Search downloads by name or URL", "/Downex/Queues/Name", "/Downex/Queues/Downloads at once"}
	edits := func(state string) {
		t.Helper()
		clientRun(t, a, w, 30*time.Second, func(c *uiaclient.Automation5) error {
			root, err := c.ElementFromHandle(uintptr(w.hwnd))
			if err != nil {
				return err
			}
			defer root.Release()
			for _, name := range []string{"raw", "control"} {
				var wk *uiaclient.TreeWalker
				if name == "raw" {
					wk, err = c.RawViewWalker()
				} else {
					wk, err = c.ControlViewWalker()
				}
				if err != nil {
					return err
				}
				var b strings.Builder
				collectEdits(wk, root, 0, "", &b)
				wk.Release()
				got := strings.FieldsFunc(b.String(), func(r rune) bool { return r == '\n' })
				if strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("%s, %s view: the Edit elements are %q, want %q", state, name, got, want)
				}
			}
			return nil
		})
	}
	edits("search shown")

	_ = onKit(pg.win, func() { pg.tb.SetModal(true) })
	shown := func() int {
		n := 0
		for _, f := range w.fields {
			if f.shown {
				n++
			}
		}
		return n
	}
	// The dialog's two fields stay; only the search's hides.
	pump(t, a, w, "the search's native field hidden", 5*time.Second, func() bool {
		return shown() == 2 && pg.tb.Search().StandIn()
	})
	time.Sleep(200 * time.Millisecond)
	edits("search stood in for")

	_ = onKit(pg.win, func() { pg.tb.SetModal(false) })
	pump(t, a, w, "the search's native field back", 5*time.Second, func() bool {
		return shown() == 3 && !pg.tb.Search().StandIn()
	})
	edits("search back")

	// The window goes, and with it every provider UI Automation held of it, before the next
	// test counts what is alive (TestUIAutomationTree).
	w.Close()
	if !pumpFor(t, a, w, 10*time.Second, func() bool { runtime.GC(); return w.closed && uia.Live() == 0 }) {
		t.Errorf("%d providers alive after the window closed", uia.Live())
	}
}
