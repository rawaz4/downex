//go:build windows || (darwin && cgo)

package main

import (
	"reflect"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/state/scene"
)

// The measuring flags come out of the command line; the product's arguments stay, in their
// order, for the launch contract (and for a later start's hand-off, unchanged).
func TestParseArgs(t *testing.T) {
	dark := true
	for _, c := range []struct {
		name     string
		args     []string
		rest     []string
		remember bool
		check    func(t *testing.T, o options)
	}{
		{name: "none", args: nil, remember: true},
		{name: "the product's own", args: []string{"--confirm", "12", "--progress", "7"}, rest: []string{"--confirm", "12", "--progress", "7"}, remember: true},
		{name: "a measuring run of the main window", args: []string{"-size", "1200x760", "-noactivate", "-report", "r.json", "-exit-after=30s"}, remember: false,
			check: func(t *testing.T, o options) {
				if o.size != (kit.Size{W: 1200, H: 760}) || !o.noActivate || o.report != "r.json" || o.exitAfter != 30*time.Second {
					t.Errorf("%+v", o)
				}
			}},
		{name: "-dpi leaves the stored size alone", args: []string{"-dpi", "96"}, remember: false},
		{name: "themes and the renderer", args: []string{"--theme=dark", "-system-theme", "dark", "-render", "sw", "--confirm", "3"}, rest: []string{"--confirm", "3"}, remember: true,
			check: func(t *testing.T, o options) {
				if o.fixed == nil || *o.fixed != tokens.ChoiceDark || !reflect.DeepEqual(o.systemDark, &dark) || !o.software {
					t.Errorf("%+v", o)
				}
			}},
		{name: "a fixture run stores nothing", args: []string{"-fixture", "random", "-rows", "20", "-live", "--progress", "4"}, rest: []string{"--progress", "4"}, remember: false,
			check: func(t *testing.T, o options) {
				if !o.fixture || o.scene != scene.Random || o.rows != 20 || !o.live {
					t.Errorf("%+v", o)
				}
			}},
		{name: "-noactivate=false is no flag", args: []string{"-noactivate=false"}, remember: true,
			check: func(t *testing.T, o options) {
				if o.noActivate {
					t.Error("noactivate")
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			o, rest, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rest, c.rest) {
				t.Errorf("product arguments %q, want %q", rest, c.rest)
			}
			if o.remember != c.remember {
				t.Errorf("remember %v, want %v", o.remember, c.remember)
			}
			if c.check != nil {
				c.check(t, o)
			}
		})
	}
	for _, bad := range [][]string{{"-size", "12"}, {"-dpi", "10"}, {"-theme", "blue"}, {"-fixture", "x"}, {"-size"}, {"-render", "gpu"}, {"-exit-after", "soon"}} {
		if _, _, err := parseArgs(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// The flags of the start reach every window the process opens (how they draw, and none takes
// the focus under -noactivate); a later window opens at its own size, writes its -report and
// -stats beside the first's, and is not closed by -exit-after.
func TestFlagsForLaterWindows(t *testing.T) {
	o, _, err := parseArgs([]string{"-noactivate", "-render", "sw", "-dpi", "120", "-size", "900x600", "-report", "r.json", "-stats", "s.jsonl", "-exit-after", "3s", "-theme", "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if first := o.forWindow(1); !reflect.DeepEqual(first, o) {
		t.Fatalf("the first window's flags %+v, want the start's %+v", first, o)
	}
	l := o.forWindow(3)
	if !l.noActivate || !l.software || l.dpi != 120 || l.fixed == nil || *l.fixed != *o.fixed {
		t.Fatalf("a later window draws otherwise: %+v", l)
	}
	if l.size != (kit.Size{}) || l.exitAfter != 0 || l.report != "r.json.3" || l.stats != "s.jsonl.3" || l.remember {
		t.Fatalf("a later window: size %v exit-after %v report %q stats %q remember %v", l.size, l.exitAfter, l.report, l.stats, l.remember)
	}
	if n := (options{remember: true}).forWindow(2); n.report != "" || n.stats != "" || !n.remember {
		t.Fatalf("no flags: %+v", n)
	}
}
