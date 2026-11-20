package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/limiter"
	downexv1 "downex/internal/rpc/downex/v1"
)

func TestPoliteSettings(t *testing.T) {
	if d := DefaultPoliteSettings(); d != (PoliteSettings{}) {
		t.Errorf("DefaultPoliteSettings = %+v, want off with the default target", d)
	}
	for data, want := range map[string]PoliteSettings{
		`{}`:                                {},
		`{"enabled":true}`:                  {Enabled: true},
		`{"target_ms":50,"z":1}`:            {TargetMs: 50},
		`{"enabled":false,"target_ms":200}`: {TargetMs: 200},
	} {
		var s PoliteSettings
		if err := json.Unmarshal([]byte(data), &s); err != nil || s != want {
			t.Errorf("unmarshal %s = %+v, %v; want %+v", data, s, err, want)
		}
	}
	for _, ok := range []int{0, 10, 30, 200} {
		if _, err := (PoliteSettings{TargetMs: ok}).normalize(); err != nil {
			t.Errorf("normalize(%d): %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 1, 9, 201, 5000} {
		if _, err := (PoliteSettings{TargetMs: bad}).normalize(); err == nil {
			t.Errorf("normalize(%d) accepted", bad)
		}
	}
	if _, err := (PoliteSettings{TargetMs: 5}).normalize(); err == nil ||
		err.Error() != "polite_target_ms must be 0 or between 10 and 200, got 5" {
		t.Errorf("error text: %v", err)
	}
	if got, problems := (PoliteSettings{Enabled: true, TargetMs: 5}).repair(); got != (PoliteSettings{Enabled: true}) ||
		len(problems) != 1 || !strings.HasSuffix(problems[0], "; using the default 30 ms") {
		t.Errorf("repair = %+v, %q", got, problems)
	}
	if got := (PoliteSettings{}).target(); got != 30*time.Millisecond {
		t.Errorf("default target %v", got)
	}
	if got := (PoliteSettings{TargetMs: 80}).target(); got != 80*time.Millisecond {
		t.Errorf("target %v", got)
	}
	// Proto round trip (fields 40-41).
	p := &downexv1.Settings{}
	PoliteSettings{Enabled: true, TargetMs: 45}.toProto(p)
	if !p.GetPoliteMode() || p.GetPoliteTargetMs() != 45 {
		t.Errorf("toProto = %v", p)
	}
	if got := politeSettingsFromProto(p); got != (PoliteSettings{Enabled: true, TargetMs: 45}) {
		t.Errorf("politeSettingsFromProto = %+v", got)
	}
	// Settings compare and copy the area like the others.
	a := DefaultSettings()
	b := a.Clone()
	b.Polite.Enabled = true
	if a.Equal(b) || !b.Equal(b.Clone()) {
		t.Error("Equal ignores Polite mode")
	}
}

// The setting goes through UpdateSettings into the "polite" object of the settings file
// and back; an older file gets the defaults, an invalid target on disk is repaired and
// refused from clients with the exact message.
func TestPoliteSettingsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFileName)
	if err := os.WriteFile(path, []byte(`{"max_concurrent_downloads": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOptions{settingsPath: path})
	if s := h.m.Settings(); s.Polite != DefaultPoliteSettings() || s.MaxConcurrentDownloads != 2 {
		t.Fatalf("settings of an older file = %+v", s)
	}
	got, err := h.dm.GetSettings(ctxT(t), connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Msg.GetSettings()
	s.PoliteMode, s.PoliteTargetMs = true, 60
	res, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Msg.GetSettings(); !r.GetPoliteMode() || r.GetPoliteTargetMs() != 60 {
		t.Fatalf("updated = %v", r)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file["polite"]), `"enabled": true`) || !strings.Contains(string(file["polite"]), `"target_ms": 60`) {
		t.Errorf("the settings file's polite object: %s", data)
	}
	want := h.m.Settings()
	if h2 := newHarness(t, harnessOptions{settingsPath: path}); !h2.m.Settings().Equal(want) {
		t.Errorf("reloaded = %+v, want %+v", h2.m.Settings(), want)
	}

	s.PoliteTargetMs = 5
	_, err = h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument ||
		!strings.Contains(err.Error(), "polite_target_ms must be 0 or between 10 and 200, got 5") {
		t.Errorf("target 5: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"polite": {"enabled": true, "target_ms": 7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	fixed, problems := raw.repair(func() (string, error) { return dir, nil })
	if fixed.Polite != (PoliteSettings{Enabled: true}) || len(problems) != 1 {
		t.Errorf("repaired = %+v, %q", fixed.Polite, problems)
	}
}

// A run's link counts every byte asked of it into the meter, also on an unlimited chain,
// and keeps counting after it moves to another parent; links without a meter count
// nothing (no byte is counted twice along the chain).
func TestChainLinkMeter(t *testing.T) {
	root := limiter.New(0)
	queue := newChainLink(0, root)
	run := newChainLink(0, queue)
	var meter atomic.Int64
	run.setMeter(&meter)
	ctx := context.Background()
	for range 3 {
		if err := run.WaitN(ctx, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if meter.Load() != 3000 {
		t.Fatalf("meter %d, want 3000", meter.Load())
	}
	run.setParent(newChainLink(0, root))
	if err := run.WaitN(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if meter.Load() != 3500 {
		t.Fatalf("meter %d after a move, want 3500", meter.Load())
	}
	if err := queue.WaitN(ctx, 100); err != nil || meter.Load() != 3500 {
		t.Fatalf("a link without a meter counted: %d, %v", meter.Load(), err)
	}
	// The chain's limit is the tightest level, the polite one included.
	politeLevel := newChainLink(0, root)
	q2 := newChainLink(4<<20, politeLevel)
	r2 := newChainLink(0, q2)
	politeLevel.SetLimit(1 << 20)
	if got := r2.Limit(); got != 1<<20 {
		t.Fatalf("chain limit %d, want the polite level's", got)
	}
}
