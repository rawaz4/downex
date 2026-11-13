package formattest

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"

	"downex/ui/core/format"
	"downex/ui/core/locale"
)

// Call runs one golden case's function on its arguments; the result is compared as JSON.
type Call func(args []json.RawMessage) (any, error)

// RunGoldens replays the cases of every function whose name starts with prefix in every
// locale and zone of the goldens with locale.New's formatter, failing on any difference and
// on a function without a Call.
func RunGoldens(t *testing.T, g *Goldens, prefix string, calls map[string]Call) {
	t.Helper()
	RunGoldensWith(t, g, prefix, calls, g.Locales, locale.New)
}

// RunGoldensWith is RunGoldens in the given locales with the formatters newFormatter makes.
func RunGoldensWith(t *testing.T, g *Goldens, prefix string, calls map[string]Call, locales []string,
	newFormatter func(tag, zone string) (locale.Formatter, error)) {
	t.Helper()
	for name := range g.Functions {
		if strings.HasPrefix(name, prefix) {
			if _, ok := calls[name]; !ok {
				t.Errorf("%s: no Go call for its cases", name)
			}
		}
	}
	unrepresentable := map[string]bool{}
	defer func() {
		if len(unrepresentable) > 0 {
			t.Logf("cases whose arguments downex.v1 cannot hold (a fraction or NaN in an int64 field): %v", slices.Sorted(maps.Keys(unrepresentable)))
		}
	}()
	for _, tag := range locales {
		for _, zone := range g.TimeZones {
			f, err := newFormatter(tag, zone)
			if err != nil {
				t.Fatalf("locale.New(%q, %q): %v", tag, zone, err)
			}
			prev := format.Locale()
			format.SetLocale(f)
			compared, failed, skipped := 0, 0, 0
			for name, fn := range g.Functions {
				call, ok := calls[name]
				if !ok {
					continue
				}
				for _, c := range fn.Cases {
					want := c.Expected(tag, zone)
					msg := compare(call, c, want)
					if msg == skip {
						skipped++
						unrepresentable[fmt.Sprintf("%s#%d", name, c.ID)] = true
						continue
					}
					compared++
					if msg != "" {
						failed++
						if failed <= 40 {
							t.Errorf("%s %s %s(%s): %s", tag, zone, name, joinArgs(c.Args), msg)
						}
					}
				}
			}
			format.SetLocale(prev)
			t.Logf("%s %s: %d cases compared, %d differ, %d not representable in Go", tag, zone, compared, failed, skipped)
		}
	}
}

func joinArgs(args []json.RawMessage) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = string(a)
		if len(parts[i]) > 120 {
			parts[i] = parts[i][:120] + "…"
		}
	}
	return strings.Join(parts, ", ")
}

// ApplyResult is a Call's result that is a function of a number (speedFormatter): the goldens
// hold its outputs for their apply inputs.
type ApplyResult func(float64) string

const skip = "skip-unrepresentable"

func compare(call Call, c Case, want Outcome) string {
	got, err := call(c.Args)
	if errors.Is(err, ErrUnrepresentable) {
		return skip
	}
	if len(want.Threw) > 0 {
		if err == nil {
			return fmt.Sprintf("got %s, want a throw %s", encode(got), want.Threw)
		}
		return ""
	}
	if err != nil {
		return "error: " + err.Error()
	}
	if f, ok := got.(ApplyResult); ok {
		if len(c.Apply) == 0 {
			got = map[string]any{"$function": "anonymous"}
		} else {
			outs := make([]any, len(c.Apply))
			for i, raw := range c.Apply {
				x, err := Num(raw)
				if err != nil {
					return "error: " + err.Error()
				}
				outs[i] = f(x)
			}
			got = outs
		}
	}
	g, w := encode(got), normalize(want.Out)
	if g != w {
		return fmt.Sprintf("got %s, want %s", g, w)
	}
	return ""
}

// encode writes a result as the goldens hold it: NaN and the infinities tagged.
func encode(v any) string {
	b, err := json.Marshal(tagNumbers(v))
	if err != nil {
		return "!" + err.Error()
	}
	return string(b)
}

func tagNumbers(v any) any {
	switch x := v.(type) {
	case float64:
		switch {
		case math.IsNaN(x):
			return map[string]string{"$num": "NaN"}
		case math.IsInf(x, 1):
			return map[string]string{"$num": "Infinity"}
		case math.IsInf(x, -1):
			return map[string]string{"$num": "-Infinity"}
		case x == 0 && math.Signbit(x):
			return map[string]string{"$num": "-0"}
		}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = tagNumbers(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = tagNumbers(e)
		}
		return out
	}
	return v
}

func normalize(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "undefined"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "!" + err.Error()
	}
	b, _ := json.Marshal(v)
	return string(b)
}
