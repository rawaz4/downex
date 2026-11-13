package widgets_test

import (
	"slices"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The toasts' and notices' rules (toasts.svelte.ts, Toasts.svelte's Notices), which today's
// suites check through the App: 5 s, 10 s for an error, at most 4, a 140 ms exit, nothing to
// wait for under reduced motion; notices 9 s, at most 3, held while hovered or focused.

func messages(ts []widgets.Toast) []string {
	var out []string
	for _, t := range ts {
		m := t.Message
		if t.Leaving {
			m += " (leaving)"
		}
		out = append(out, m)
	}
	return out
}

func TestToastsStore(t *testing.T) {
	r := newRig(t, kit.Size{})
	s := widgets.NewToasts(r.win)
	s.Show("Nothing to pause", widgets.ToastInfo)
	s.Error("Could not reach downexd")
	r.wait(5 * time.Second)
	if got := messages(s.Items()); !slices.Equal(got, []string{"Nothing to pause (leaving)", "Could not reach downexd"}) {
		t.Fatalf("after 5 s: %q", got)
	}
	r.wait(140 * time.Millisecond)
	if got := messages(s.Items()); !slices.Equal(got, []string{"Could not reach downexd"}) {
		t.Fatalf("after the exit: %q", got)
	}
	r.wait(4861 * time.Millisecond) // an error lasts 10 s: now 10.001 s
	if got := messages(s.Items()); len(got) != 1 || got[0] != "Could not reach downexd (leaving)" {
		t.Errorf("after 10 s: %q", got)
	}
	r.wait(time.Second)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		s.Success(m)
	}
	if got := messages(s.Items()); !slices.Equal(got, []string{"b", "c", "d", "e"}) {
		t.Errorf("at most 4: %q", got)
	}
	s.Dismiss(s.Items()[0].ID)
	s.Success("f") // a leaving toast does not count towards the 4
	if got := messages(s.Items()); !slices.Equal(got, []string{"b (leaving)", "c", "d", "e", "f"}) {
		t.Errorf("with one leaving: %q", got)
	}
}

func TestToastsWithoutMotion(t *testing.T) {
	r := newRig(t, kit.Size{})
	env := r.win.Environment()
	env.ReducedMotion = true
	r.loop.Send(kit.EnvironmentEvent{Env: env})
	s := widgets.NewToasts(r.win)
	s.Show("Settings saved", widgets.ToastSuccess)
	s.Dismiss(s.Items()[0].ID)
	if len(s.Items()) != 0 {
		t.Errorf("under reduced motion a toast leaves at once: %q", messages(s.Items()))
	}
}

func TestNoticesStore(t *testing.T) {
	r := newRig(t, kit.Size{})
	n := widgets.NewNotices(r.win)
	keys := func() []string {
		var out []string
		for _, x := range n.Items() {
			k := x.Key
			if n.Leaving(k) {
				k += " (leaving)"
			}
			out = append(out, k)
		}
		return out
	}
	for _, k := range []string{"1", "2", "3", "4"} {
		n.Show(widgets.Notice{Key: k, Title: "From your browser"})
	}
	if got := keys(); !slices.Equal(got, []string{"2", "3", "4"}) {
		t.Fatalf("at most 3: %q", got)
	}
	n.Show(widgets.Notice{Key: "3", Title: "Again"}) // the same key comes back at the end
	if got := keys(); !slices.Equal(got, []string{"2", "4", "3"}) {
		t.Fatalf("again: %q", got)
	}
	r.wait(5 * time.Second)
	n.Hold("2")
	r.wait(10 * time.Second)
	if got := keys(); !slices.Equal(got, []string{"2"}) {
		t.Fatalf("held: %q", got)
	}
	if left := n.Remaining("2"); left != 4*time.Second {
		t.Fatalf("held with %v left, want 4s", left)
	}
	n.Release("2")
	r.wait(3999 * time.Millisecond)
	if got := keys(); !slices.Equal(got, []string{"2"}) {
		t.Fatalf("released early: %q", got)
	}
	r.wait(2 * time.Millisecond)
	if got := keys(); !slices.Equal(got, []string{"2 (leaving)"}) {
		t.Fatalf("after its time left: %q", got)
	}
	r.wait(140 * time.Millisecond)
	if len(n.Items()) != 0 {
		t.Errorf("after the exit: %q", keys())
	}
	n.Show(widgets.Notice{Key: "x"})
	n.Clear()
	if len(n.Items()) != 0 {
		t.Error("Clear left notices")
	}
}

func TestToastLayer(t *testing.T) {
	r := newRig(t, kit.Size{W: 1200, H: 760})
	toasts := widgets.NewToasts(r.win)
	notices := widgets.NewNotices(r.win)
	l := widgets.NewToastLayer(toasts, notices)
	l.Push(r.win)
	ran := 0
	toasts.Success("2 downloads paused")
	notices.Show(widgets.Notice{Key: "k", Title: "From your browser", Detail: "ubuntu.iso", Actions: []widgets.NoticeAction{
		{Label: "Later"}, {Label: "Confirm…", Primary: true, Run: func() { ran++ }},
	}})
	r.settle()
	if got := r.host.SemanticCount; got == 0 && len(r.host.Semantics) == 0 {
		// the tree was never read: no events are sent (kit's rule); read it and announce again
		r.win.Semantics().Root()
	}
	status := byRole(t, r.win, kit.RoleStatus)
	if len(status) != 2 {
		t.Fatalf("%d live regions", len(status))
	}
	groups := byRole(t, r.win, kit.RoleGroup)
	if len(groups) != 1 || groups[0].Name != "From your browser" {
		t.Fatalf("notice %v", groups)
	}
	// the plain toast sits 16 px above the status bar and from the end, 360 px wide
	var plain kit.SemanticNode
	for _, b := range byRole(t, r.win, kit.RoleButton) {
		if b.Name == "Dismiss" {
			plain = b
		}
	}
	if plain.ID == 0 {
		t.Fatal("no Dismiss button")
	}
	if plain.Bounds.Max.X > 1200-16 || plain.Bounds.Max.Y > 760-32-16 {
		t.Errorf("dismiss at %v", plain.Bounds)
	}
	var confirm kit.SemanticNode
	for _, b := range byRole(t, r.win, kit.RoleButton) {
		if b.Name == "Confirm…" {
			confirm = b
		}
	}
	if confirm.Bounds.Max.Y >= plain.Bounds.Min.Y {
		t.Errorf("the notice (%v) is not above the plain toast (%v)", confirm.Bounds, plain.Bounds)
	}
	r.clickAt(confirm.Bounds.Center())
	if ran != 1 || !notices.Leaving("k") {
		t.Errorf("ran %d, leaving %v", ran, notices.Leaving("k"))
	}
	r.clickAt(plain.Bounds.Center())
	if !toasts.Items()[0].Leaving {
		t.Error("Dismiss did not dismiss")
	}
}
