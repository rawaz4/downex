package display_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
)

// TestGoldens replays every display.ts case of the 0.9.2 formatter goldens (the inputs of the
// window's vitest and component tests, with Blink's output) in each locale and time zone.
func TestGoldens(t *testing.T) {
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s (set DOWNEX_GOLDENS)", path)
	}
	RunGoldens(t, g, "display.", displayCalls(g.FixedNow))
}

func dl(f func(d *downexv1.Download, args []json.RawMessage) (any, error)) Call {
	return func(args []json.RawMessage) (any, error) {
		d, err := DownloadArg(args[0])
		if err != nil {
			return nil, err
		}
		return f(d, args[1:])
	}
}

func numArg(args []json.RawMessage, i int) (float64, error) {
	if i >= len(args) || IsUndefined(args[i]) {
		return math.NaN(), nil
	}
	return Num(args[i])
}

func boolArg(args []json.RawMessage, i int) bool {
	if i >= len(args) {
		return false
	}
	b, _ := Bool(args[i])
	return b
}

func columnJSON(c display.Column) map[string]any {
	m := map[string]any{"key": c.Key, "label": c.Label, "width": c.Width, "min": c.Min, "drop": float64(c.Drop)}
	if c.Title != "" {
		m["title"] = c.Title
	}
	if c.End {
		m["end"] = true
	}
	return m
}

func boxJSON(b display.ConnBox) map[string]any {
	m := map[string]any{"key": b.Key, "fraction": b.Fraction, "tone": string(b.Tone), "speed": b.Speed, "label": b.Label, "title": b.Title}
	if b.More > 0 {
		m["more"] = float64(b.More)
	}
	return m
}

func source(raw json.RawMessage) downexv1.AddSource {
	s, _ := Str(raw)
	return downexv1.AddSource(downexv1.AddSource_value["ADD_SOURCE_"+upper(s)])
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func displayCalls(fixedNow float64) map[string]Call {
	now := func(args []json.RawMessage, i int) (float64, error) {
		if i >= len(args) || IsUndefined(args[i]) {
			return fixedNow, nil
		}
		return Num(args[i])
	}
	return map[string]Call{
		"display.statusText": dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) { return display.StatusText(d), nil }),
		"display.sizeHint":   dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) { return float64(display.SizeHint(d)), nil }),
		"display.sourceText": func(args []json.RawMessage) (any, error) { return display.SourceText(source(args[0])), nil },
		"display.sourcePhrase": func(args []json.RawMessage) (any, error) {
			return display.SourcePhrase(source(args[0])), nil
		},
		"display.errorHint": func(args []json.RawMessage) (any, error) {
			s, err := Str(args[0])
			return display.ErrorHint(s), err
		},
		"display.joinPath": func(args []json.RawMessage) (any, error) {
			dir, _ := Str(args[0])
			name, _ := Str(args[1])
			return display.JoinPath(dir, name), nil
		},
		"display.rawJoinPath": func(args []json.RawMessage) (any, error) {
			dir, _ := Str(args[0])
			name, _ := Str(args[1])
			return display.RawJoinPath(dir, name), nil
		},
		"display.progressSummary": dl(func(d *downexv1.Download, rest []json.RawMessage) (any, error) {
			return display.ProgressSummary(d, boolArg(rest, 0)), nil
		}),
		"display.rowDescription": dl(func(d *downexv1.Download, rest []json.RawMessage) (any, error) {
			return display.RowDescription(d, fixedNow), nil
		}),
		"display.mediaBadge": dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) { return display.MediaBadge(d), nil }),
		"display.kindLabel": func(args []json.RawMessage) (any, error) {
			k, err := Str(args[0])
			return display.KindLabel(categories.FileKind(k)), err
		},
		"display.rowHost":     dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) { return display.RowHost(d), nil }),
		"display.rowCategory": dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) { return display.RowCategory(d), nil }),
		"display.rowSubline":  dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) { return display.RowSubline(d), nil }),
		"display.addedText": dl(func(d *downexv1.Download, rest []json.RawMessage) (any, error) {
			n, err := now(rest, 0)
			return display.AddedText(d, n), err
		}),
		"display.dayTimeText": func(args []json.RawMessage) (any, error) {
			ms, err := numArg(args, 0)
			if err != nil {
				return nil, err
			}
			n, err := now(args, 1)
			return display.DayTimeText(ms, n), err
		},
		"display.statePill": dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) {
			p := display.StatePill(d)
			return map[string]any{"tone": string(p.Tone), "text": p.Text}, nil
		}),
		"display.receivedOf": func(args []json.RawMessage) (any, error) {
			r, err := numArg(args, 0)
			if err != nil {
				return nil, err
			}
			s, err := numArg(args, 1)
			if err != nil {
				return nil, err
			}
			return display.ReceivedOf(r, s, boolArg(args, 2)), nil
		},
		"display.selectionSummary": func(args []json.RawMessage) (any, error) {
			var raws []json.RawMessage
			if err := json.Unmarshal(args[0], &raws); err != nil {
				return nil, err
			}
			ds := make([]*downexv1.Download, len(raws))
			for i, r := range raws {
				d, err := DownloadArg(r)
				if err != nil {
					return nil, err
				}
				ds[i] = d
			}
			return display.SelectionSummary(ds), nil
		},
		"display.rowHeightFor": func(args []json.RawMessage) (any, error) {
			rem, err := numArg(args, 0)
			return display.RowHeightFor(rem, boolArg(args, 1)), err
		},
		"display.fitColumns": func(args []json.RawMessage) (any, error) {
			available, err := numArg(args, 0)
			if err != nil {
				return nil, err
			}
			rem, err := numArg(args, 1)
			if err != nil {
				return nil, err
			}
			var widthOf struct {
				WidthOf string `json:"$widthOf"`
			}
			if err := json.Unmarshal(args[2], &widthOf); err != nil || widthOf.WidthOf == "" {
				return nil, fmt.Errorf("fitColumns: widthOf %s", args[2])
			}
			cols := display.FitColumns(available, rem, func(c display.Column) float64 { return c.Width * rem })
			out := make([]any, len(cols))
			for i, c := range cols {
				out[i] = columnJSON(c)
			}
			return out, nil
		},
		"display.progressCell": dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) {
			return string(display.ProgressCellOf(d)), nil
		}),
		"display.inlineAction": dl(func(d *downexv1.Download, _ []json.RawMessage) (any, error) {
			a, ok := display.InlineAction(d)
			if !ok {
				return nil, nil
			}
			return map[string]any{"id": a.ID, "label": a.Label}, nil
		}),
		"display.speedAxis": func(args []json.RawMessage) (any, error) {
			peak, err := numArg(args, 0)
			a := display.SpeedAxisFor(peak)
			return map[string]any{"max": a.Max, "labels": []any{a.Labels[0], a.Labels[1], a.Labels[2]}}, err
		},
		"display.speedStats": func(args []json.RawMessage) (any, error) {
			var raw []struct{ T, V JSNum }
			if err := json.Unmarshal(args[0], &raw); err != nil {
				return nil, err
			}
			samples := make([]display.SpeedSample, len(raw))
			for i, s := range raw {
				samples[i] = display.SpeedSample{T: float64(s.T), V: float64(s.V)}
			}
			st := display.SpeedStatsOf(samples)
			shown := make([]any, len(st.Shown))
			for i, s := range st.Shown {
				shown[i] = map[string]any{"t": s.T, "v": s.V}
			}
			return map[string]any{"avg": st.Avg, "peak": st.Peak, "shown": shown}, nil
		},
		"display.speedUnit": func(args []json.RawMessage) (any, error) {
			var raw []JSNum
			if err := json.Unmarshal(args[0], &raw); err != nil {
				return nil, err
			}
			speeds := make([]float64, len(raw))
			for i, v := range raw {
				speeds[i] = float64(v)
			}
			return display.SpeedUnit(speeds), nil
		},
		"display.speedFormatter": func(args []json.RawMessage) (any, error) {
			unit, err := Str(args[0])
			if err != nil {
				return nil, err
			}
			return speedFormatterResult(display.SpeedFormatter(unit)), nil
		},
		"display.connectionBoxes": func(args []json.RawMessage) (any, error) {
			var raws []json.RawMessage
			if err := json.Unmarshal(args[0], &raws); err != nil {
				return nil, err
			}
			infos := make([]*downexv1.ConnectionInfo, len(raws))
			for i, r := range raws {
				c, err := ConnectionArg(r)
				if err != nil {
					return nil, err
				}
				infos[i] = c
			}
			var opts display.BoxOptions
			if len(args) > 1 {
				var o struct {
					Media bool   `json:"media"`
					Piece string `json:"piece"`
					Max   *int   `json:"max"`
				}
				if err := json.Unmarshal(args[1], &o); err != nil {
					return nil, err
				}
				opts.Media, opts.Piece = o.Media, o.Piece
				if o.Max != nil {
					opts.Max = *o.Max
				}
			}
			s := display.ConnectionBoxes(infos, opts)
			boxes := make([]any, len(s.Boxes))
			for i, b := range s.Boxes {
				boxes[i] = boxJSON(b)
			}
			return map[string]any{"boxes": boxes, "together": s.Together, "unit": s.Unit}, nil
		},
		"display.showsConnections": dl(func(d *downexv1.Download, rest []json.RawMessage) (any, error) {
			return display.ShowsConnections(d, boolArg(rest, 0)), nil
		}),
		"display.limitOptions": func(args []json.RawMessage) (any, error) {
			current, err := numArg(args, 0)
			opts := display.LimitOptions(current)
			out := make([]any, len(opts))
			for i, o := range opts {
				out[i] = map[string]any{"value": o.Value, "label": o.Label}
			}
			return out, err
		},
		"display.speedParts": func(args []json.RawMessage) (any, error) {
			bps, err := numArg(args, 0)
			n, unit := display.SpeedParts(bps)
			return map[string]any{"n": n, "unit": unit}, err
		},
	}
}

// speedFormatterResult is what the goldens hold for a returned function: a marker, or (with
// apply) the outputs for the inputs; ApplyResult carries the function to the runner.
func speedFormatterResult(f func(float64) string) any { return ApplyResult(f) }

// TestGoldensFallback replays the en-US cases with the pure Go formatter of the platforms
// without ICU.
func TestGoldensFallback(t *testing.T) {
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s (set DOWNEX_GOLDENS)", path)
	}
	RunGoldensWith(t, g, "display.", displayCalls(g.FixedNow), []string{"en-US"}, locale.NewFallback)
}
