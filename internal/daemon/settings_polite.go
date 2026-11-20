package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"downex/internal/polite"
	downexv1 "downex/internal/rpc/downex/v1"
)

// PoliteSettings are Polite mode's settings (docs/POLITE-SPEC.md §7; Settings fields
// 40-41, kept in the settings file as the "polite" object).
type PoliteSettings struct {
	// Enabled switches Polite mode on (default off: speed first).
	Enabled bool `json:"enabled"`
	// TargetMs is the extra line delay kept under, in milliseconds: 0 (the default 30)
	// or 10..200.
	TargetMs int `json:"target_ms"`
}

// DefaultPoliteSettings are the settings of a new installation and of a settings file
// of an older daemon: off, the default target.
func DefaultPoliteSettings() PoliteSettings { return PoliteSettings{} }

// UnmarshalJSON reads the settings file's "polite" object: fields it lacks take their
// DefaultPoliteSettings values.
func (p *PoliteSettings) UnmarshalJSON(data []byte) error {
	var f struct {
		Enabled  *bool `json:"enabled"`
		TargetMs *int  `json:"target_ms"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*p = DefaultPoliteSettings()
	if f.Enabled != nil {
		p.Enabled = *f.Enabled
	}
	if f.TargetMs != nil {
		p.TargetMs = *f.TargetMs
	}
	return nil
}

// normalize validates p as part of a full replacement from a client (rule 19).
func (p PoliteSettings) normalize() (PoliteSettings, error) {
	if !polite.ValidTargetMs(p.TargetMs) {
		return p, fmt.Errorf("polite_target_ms must be 0 or between %d and %d, got %d",
			polite.MinTarget/time.Millisecond, polite.MaxTarget/time.Millisecond, p.TargetMs)
	}
	return p, nil
}

// repair replaces an invalid target read from disk with the default and describes it.
func (p PoliteSettings) repair() (PoliteSettings, []string) {
	if _, err := p.normalize(); err != nil {
		p.TargetMs = 0
		return p, []string{fmt.Sprintf("polite.target_ms: %v; using the default %d ms", err, polite.DefaultTarget/time.Millisecond)}
	}
	return p, nil
}

// target returns the target delay (0 selects polite.DefaultTarget).
func (p PoliteSettings) target() time.Duration { return polite.TargetOf(p.TargetMs) }

// toProto writes p into the API settings (fields 40-41).
func (p PoliteSettings) toProto(s *downexv1.Settings) {
	s.PoliteMode = p.Enabled
	s.PoliteTargetMs = int32(p.TargetMs)
}

// politeSettingsFromProto reads Polite mode's settings of API settings (a full
// replacement; normalize validates).
func politeSettingsFromProto(s *downexv1.Settings) PoliteSettings {
	return PoliteSettings{Enabled: s.GetPoliteMode(), TargetMs: int(s.GetPoliteTargetMs())}
}
