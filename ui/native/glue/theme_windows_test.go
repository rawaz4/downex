//go:build windows

package glue

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/prefs"
)

// themeWriter is another window process changing the theme: "json <path> <choice>" as
// today's window does (ui-theme.json only), "prefs <path> <choice>" as a native window's
// prefs file does.
func themeWriter(_ string, args []string) int {
	if len(args) != 3 {
		return 20
	}
	c, ok := host.ParseThemeChoice(args[2])
	if !ok {
		return 21
	}
	switch args[0] {
	case "json":
		if err := host.SaveThemeChoice(args[1], c); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 22
		}
	case "prefs":
		f, err := prefs.Open(args[1], prefs.Options{Poll: -1})
		if err != nil {
			return 23
		}
		if c == host.ThemeSystem {
			err = f.Remove(state.ThemeKey)
		} else {
			err = f.Set(state.ThemeKey, string(c))
		}
		if err == nil {
			err = f.Flush()
		}
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 24
		}
	default:
		return 25
	}
	fmt.Println(time.Now().UnixNano()) // when the change was on disk
	return 0
}

// A theme change another process makes reaches an open window within a second: one of
// today's windows writing ui-theme.json, a native window writing ui-prefs.json. The window
// keeps a followed choice in its prefs and never writes it back to ui-theme.json; the user's
// own choice in the window is written to both.
func TestThemeFollowsOtherProcesses(t *testing.T) {
	desk := onTestDesktop(t)
	dir := t.TempDir()
	p, _, err := start(t, testLock(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.h.Close()
	pf, err := p.OpenPrefs()
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	theme := p.NewTheme(pf)
	defer theme.Close()
	tw := openWindow(t, kit.WindowOptions{})
	g, err := p.Attach(tw.w, tw.kw, AttachOptions{Theme: theme, Prefs: pf})
	if err != nil {
		t.Fatal(err)
	}
	if theme.Choice() != host.ThemeSystem || host.LoadThemeChoice(p.Files.Theme) != host.ThemeSystem {
		t.Fatalf("start: %q, file %q", theme.Choice(), host.LoadThemeChoice(p.Files.Theme))
	}
	type step struct {
		name, how string
		choice    host.ThemeChoice
	}
	steps := []step{
		{"today's window chooses Dark", "json", host.ThemeDark},
		{"a native window chooses Light", "prefs", host.ThemeLight},
		{"today's window chooses System", "json", host.ThemeSystem},
	}
	// When the window took each choice (on the UI thread, as it happened).
	var seen sync.Map // host.ThemeChoice -> time.Time
	stopSeen := theme.Changes.Subscribe(func() { seen.LoadOrStore(theme.Choice(), time.Now()) })
	defer stopSeen()
	results := make(chan string, 8)
	go func() {
		defer tw.close()
		for _, s := range steps {
			path := p.Files.Theme
			if s.how == "prefs" {
				path = p.Files.Prefs
			}
			before, _ := os.Stat(p.Files.Theme)
			ch := startChild(t, "theme-writer", dir, map[string]string{
				envDesktop: desk, envArgs: argsJSON(t, s.how, path, string(s.choice)),
			})
			var at int64
			select {
			case l := <-ch.lines:
				_, _ = fmt.Sscan(l, &at)
			case <-time.After(20 * time.Second):
			}
			if code := ch.exitCode(20 * time.Second); code != 0 || at == 0 {
				results <- fmt.Sprintf("%s: the writer exited %d", s.name, code)
				return
			}
			wrote := time.Unix(0, at)
			for theme.Choice() != s.choice && time.Since(wrote) < 3*time.Second {
				time.Sleep(time.Millisecond)
			}
			took := time.Since(wrote)
			if v, ok := seen.LoadAndDelete(s.choice); ok {
				took = max(v.(time.Time).Sub(wrote), 0)
			}
			seen.Clear()
			if theme.Choice() != s.choice || took > time.Second {
				results <- fmt.Sprintf("%s: the window shows %q after %v", s.name, theme.Choice(), took)
				return
			}
			_ = pf.Flush()
			v, ok, _ := pf.Get(state.ThemeKey)
			if s.choice == host.ThemeSystem && ok || s.choice != host.ThemeSystem && v != string(s.choice) {
				results <- fmt.Sprintf("%s: the prefs hold %q", s.name, v)
				return
			}
			after, _ := os.Stat(p.Files.Theme)
			if s.how == "prefs" && before != nil && after != nil && !after.ModTime().Equal(before.ModTime()) {
				results <- fmt.Sprintf("%s: the window wrote a followed choice back to ui-theme.json", s.name)
				return
			}
			results <- fmt.Sprintf("ok %s: followed in %v", s.name, took.Round(time.Millisecond))
		}
		// The user's own choice in this window goes to both files.
		done := make(chan struct{})
		_ = tw.kw.Post(func() { theme.Set(host.ThemeDark); close(done) })
		<-done
		_ = pf.Flush()
		if v, _, _ := pf.Get(state.ThemeKey); v != "dark" || host.LoadThemeChoice(p.Files.Theme) != host.ThemeDark {
			results <- fmt.Sprintf("the user's choice: prefs %q, ui-theme.json %q", v, host.LoadThemeChoice(p.Files.Theme))
			return
		}
		results <- "ok the user's choice is written to both files"
	}()
	tw.run(t, 60*time.Second)
	close(results)
	n := 0
	for r := range results {
		if len(r) > 3 && r[:3] == "ok " {
			t.Log(r[3:])
			n++
			continue
		}
		t.Error(r)
	}
	if n != len(steps)+1 {
		t.Fatalf("%d of %d steps passed", n, len(steps)+1)
	}
	_ = g
}

// The System theme follows the system's light or dark apps through the window's
// environment (WM_SETTINGCHANGE reaches kit.WindowOptions.OnEnvironment; ui/win's
// TestSettingChangeFollowsTheSystem drives that part with synthetic messages).
func TestSystemDarkFollowsTheEnvironment(t *testing.T) {
	_ = onTestDesktop(t)
	p, _, err := start(t, testLock(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.h.Close()
	theme := p.NewTheme(nil)
	tw := openWindow(t, kit.WindowOptions{})
	g, err := p.Attach(tw.w, tw.kw, AttachOptions{Theme: theme})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	stop := theme.Changes.Subscribe(func() { n++ })
	defer stop()
	first := !theme.Dark() // the system's own mode at start (host.SystemPrefersDark), then flips
	for _, dark := range []bool{first, !first, first} {
		g.Environment(kit.Environment{Dark: dark})
		if theme.Dark() != dark {
			t.Fatalf("system dark %v: the System theme is dark %v", dark, theme.Dark())
		}
	}
	g.Environment(kit.Environment{Dark: first}) // no change: nothing told
	if n != 3 {
		t.Fatalf("%d theme changes told, want 3", n)
	}
	theme.Set(host.ThemeLight)
	g.Environment(kit.Environment{Dark: !first})
	g.Environment(kit.Environment{Dark: true})
	if theme.Dark() {
		t.Fatal("a Light choice followed the system")
	}
	(*Glue)(nil).Environment(kit.Environment{}) // a window without glue: nothing
	tw.close()
	tw.run(t, 10*time.Second)
}
