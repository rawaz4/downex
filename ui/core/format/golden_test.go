package format_test

import (
	"encoding/json"
	"math"
	"testing"

	"downex/ui/core/format"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
)

// TestGoldens replays every format.ts case of the 0.9.2 formatter goldens (the vitest tables'
// inputs and the edge cases, with Blink's output) in each locale and time zone, byte for byte.
func TestGoldens(t *testing.T) {
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s (set DOWNEX_GOLDENS)", path)
	}
	RunGoldens(t, g, "format.", formatCalls(g.FixedNow))
}

func num(args []json.RawMessage, i int) (float64, error) {
	if i >= len(args) {
		return math.NaN(), nil
	}
	return Num(args[i])
}

func numCall(f func(float64) string) Call {
	return func(args []json.RawMessage) (any, error) {
		n, err := num(args, 0)
		if err != nil {
			return nil, err
		}
		return f(n), nil
	}
}

func formatCalls(fixedNow float64) map[string]Call {
	dl := func(f func(args []json.RawMessage) (any, error)) Call { return f }
	return map[string]Call{
		"format.formatBytes":      numCall(format.Bytes),
		"format.formatCount":      numCall(format.Count),
		"format.formatSpeed":      numCall(format.Speed),
		"format.formatEta":        numCall(format.Eta),
		"format.formatBitrate":    numCall(format.Bitrate),
		"format.formatDuration":   numCall(format.Duration),
		"format.formatPercent":    numCall(format.Percent),
		"format.formatDateTime":   numCall(format.DateTime),
		"format.formatClock":      numCall(format.Clock),
		"format.formatDriveLabel": strCall(format.DriveLabel),
		"format.hostOf":           strCall(format.HostOf),
		"format.formatDate": func(args []json.RawMessage) (any, error) {
			ms, err := num(args, 0)
			if err != nil {
				return nil, err
			}
			now := fixedNow
			if len(args) > 1 && !IsUndefined(args[1]) {
				if now, err = Num(args[1]); err != nil {
					return nil, err
				}
			}
			return format.Date(ms, now), nil
		},
		"format.formatSize": dl(func(args []json.RawMessage) (any, error) {
			d, err := DownloadArg(args[0])
			if err != nil {
				return nil, err
			}
			return format.Size(d), nil
		}),
		"format.etaSeconds": dl(func(args []json.RawMessage) (any, error) {
			d, err := DownloadArg(args[0])
			if err != nil {
				return nil, err
			}
			return format.EtaSeconds(d), nil
		}),
		"format.progressFraction": dl(func(args []json.RawMessage) (any, error) {
			d, err := DownloadArg(args[0])
			if err != nil {
				return nil, err
			}
			return format.ProgressFraction(d), nil
		}),
		"format.parseRate": func(args []json.RawMessage) (any, error) {
			value, err := Str(args[0])
			if err != nil {
				return nil, err
			}
			unit, err := Str(args[1])
			if err != nil {
				return nil, err
			}
			loaded := 0.0
			if len(args) > 2 && !IsUndefined(args[2]) {
				if loaded, err = Num(args[2]); err != nil {
					return nil, err
				}
			}
			n, ok := format.ParseRate(value, format.RateUnit(unit), loaded)
			if !ok {
				return nil, nil
			}
			return float64(n), nil
		},
		"format.splitRate": numCall2(func(n float64) any {
			value, unit := format.SplitRate(n)
			return map[string]any{"value": value, "unit": string(unit)}
		}),
	}
}

func numCall2(f func(float64) any) Call {
	return func(args []json.RawMessage) (any, error) {
		n, err := num(args, 0)
		if err != nil {
			return nil, err
		}
		return f(n), nil
	}
}

func strCall(f func(string) string) Call {
	return func(args []json.RawMessage) (any, error) {
		s, err := Str(args[0])
		if err != nil {
			return nil, err
		}
		return f(s), nil
	}
}

// TestGoldensFallback replays the en-US cases with the pure Go formatter of the platforms
// without ICU: it writes en-US forms, so it must reproduce them.
func TestGoldensFallback(t *testing.T) {
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s (set DOWNEX_GOLDENS)", path)
	}
	RunGoldensWith(t, g, "format.", formatCalls(g.FixedNow), []string{"en-US"}, locale.NewFallback)
}
