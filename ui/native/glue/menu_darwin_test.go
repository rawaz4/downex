//go:build darwin && cgo

package glue

import (
	"slices"
	"testing"

	"downex/ui/mac"
)

// ⌘Q and Close Window end the key window as its own close does (today each window was a
// process of its own, which both items ended). With no key, main or visible window (every
// window minimised) they still end what today's process would have: the main window, else,
// for Quit, every progress window and, for Close Window, the latest one.
func TestMenuCloseTargets(t *testing.T) {
	main := &Window{}
	p1, p2 := &Window{}, &Window{}
	for _, c := range []struct {
		name    string
		cmd     mac.MenuCommand
		key     *Window
		main    *Window
		windows []*Window
		want    []*Window
	}{
		{"quit the key window", mac.MenuQuit, p1, main, []*Window{main, p1, p2}, []*Window{p1}},
		{"close the key window", mac.MenuCloseWindow, main, main, []*Window{main, p1}, []*Window{main}},
		{"quit, all minimised, main host", mac.MenuQuit, nil, main, []*Window{main}, []*Window{main}},
		{"close, all minimised, main host", mac.MenuCloseWindow, nil, main, []*Window{main}, []*Window{main}},
		{"quit, all minimised, progress host", mac.MenuQuit, nil, nil, []*Window{p1, p2}, []*Window{p1, p2}},
		{"close, all minimised, progress host", mac.MenuCloseWindow, nil, nil, []*Window{p1, p2}, []*Window{p2}},
		{"nothing open", mac.MenuQuit, nil, nil, nil, nil},
		{"settings closes nothing", mac.MenuSettings, nil, main, []*Window{main}, nil},
	} {
		got := menuCloseTargets(c.cmd, c.key, c.main, c.windows)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %d windows closed, want %d (%v vs %v)", c.name, len(got), len(c.want), got, c.want)
		}
	}
}
