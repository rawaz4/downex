//go:build windows || (darwin && cgo)

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/state/scene"
)

// options are the measuring flags of a run (the product is started with none of them). They
// are taken out of the command line; what is left is the product's own arguments
// ("--progress <id>", "--confirm <id>"), which the launch contract reads and a later start
// hands over unchanged.
type options struct {
	// fixed is the theme drawn whatever the stored choice says (-theme); systemDark stands
	// in for the system's app mode (-system-theme).
	fixed      *tokens.Choice
	systemDark *bool
	software   bool
	noActivate bool
	dpi        uint32
	size       kit.Size // zero: the view's own size
	report     string
	stats      string
	exitAfter  time.Duration
	// fixture shows the simulated daemon (ui/state/scene) instead of downexd: no lock, no
	// per-user file, nothing stored.
	fixture bool
	scene   scene.Kind
	rows    int
	live    bool
	// remember: the main window opens at the size it was left at and stores it when it
	// closes (not when -size or -dpi chose how it looks, nor in a fixture run).
	remember bool
}

// flagNames are the measuring flags and whether each takes a value. Each is written -name or
// --name, with its value after "=" or as the next argument.
var flagNames = map[string]bool{
	"theme": true, "system-theme": true, "render": true, "dpi": true, "size": true,
	"report": true, "stats": true, "exit-after": true, "fixture": true, "rows": true,
	"noactivate": false, "live": false,
}

// parseArgs takes the measuring flags out of args (the command line without the program
// name) and returns them with the product's arguments, in their order.
func parseArgs(args []string) (options, []string, error) {
	o := options{remember: true}
	var rest []string
	vals := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := flagOf(a)
		takes, ok := flagNames[name]
		if !ok {
			rest = append(rest, a)
			continue
		}
		if takes && !hasValue {
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("-%s needs a value", name)
			}
			i++
			value = args[i]
		}
		if !takes && hasValue {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return o, nil, fmt.Errorf("-%s=%q: want true or false", name, value)
			}
			if !b {
				continue
			}
		}
		vals[name] = value
	}
	if v, ok := vals["theme"]; ok {
		c, ok := tokens.ParseChoice(v)
		if !ok {
			return o, nil, fmt.Errorf("-theme %q: want system, dark or light", v)
		}
		o.fixed = &c
	}
	if v, ok := vals["system-theme"]; ok {
		switch v {
		case "auto":
		case "light", "dark":
			d := v == "dark"
			o.systemDark = &d
		default:
			return o, nil, fmt.Errorf("-system-theme %q: want auto, light or dark", v)
		}
	}
	if v, ok := vals["render"]; ok {
		switch v {
		case "hw":
		case "sw":
			o.software = true
		default:
			return o, nil, fmt.Errorf("-render %q: want hw or sw", v)
		}
	}
	if v, ok := vals["dpi"]; ok {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n < 72 || n > 480 {
			return o, nil, fmt.Errorf("-dpi %q: want 72 to 480", v)
		}
		o.dpi, o.remember = uint32(n), false
	}
	if v, ok := vals["size"]; ok {
		var w, h float32
		if _, err := fmt.Sscanf(v, "%gx%g", &w, &h); err != nil || w < 300 || h < 200 || w > 8000 || h > 8000 {
			return o, nil, fmt.Errorf("-size %q: want WxH in DIPs", v)
		}
		o.size, o.remember = kit.Size{W: w, H: h}, false
	}
	o.report, o.stats = vals["report"], vals["stats"]
	if v, ok := vals["exit-after"]; ok {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return o, nil, fmt.Errorf("-exit-after %q: want a duration such as 30s", v)
		}
		o.exitAfter = d
	}
	if v, ok := vals["fixture"]; ok {
		switch v {
		case "studio":
			o.scene = scene.Studio
		case "random":
			o.scene = scene.Random
		default:
			return o, nil, fmt.Errorf("-fixture %q: want studio or random", v)
		}
		o.fixture, o.remember = true, false
	}
	o.rows = 10000
	if v, ok := vals["rows"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1_000_000 {
			return o, nil, fmt.Errorf("-rows %q: want 0 to 1000000", v)
		}
		o.rows = n
	}
	_, o.noActivate = vals["noactivate"]
	_, o.live = vals["live"]
	return o, rest, nil
}

// forWindow is o for the n-th window the process opens (1: its start's own). Every window
// draws as the flags say (-theme, -system-theme, -render, -dpi, -noactivate); a later
// window opens at its own size, and writes its -report and -stats to the file's name with
// ".<n>" added; only the first is closed by -exit-after.
func (o options) forWindow(n int) options {
	if n <= 1 {
		return o
	}
	o.size, o.exitAfter = kit.Size{}, 0
	if o.report != "" {
		o.report += "." + strconv.Itoa(n)
	}
	if o.stats != "" {
		o.stats += "." + strconv.Itoa(n)
	}
	return o
}

// flagOf splits "-name", "--name" and "-name=value"; anything else has no name.
func flagOf(a string) (name, value string, hasValue bool) {
	switch {
	case strings.HasPrefix(a, "--"):
		a = a[2:]
	case strings.HasPrefix(a, "-"):
		a = a[1:]
	default:
		return "", "", false
	}
	if a == "" {
		return "", "", false
	}
	name, value, hasValue = strings.Cut(a, "=")
	return name, value, hasValue
}
