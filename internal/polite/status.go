package polite

import "time"

// State is Polite mode's state (docs/POLITE-SPEC.md rule 10).
type State uint8

const (
	// Off: the setting is off. No cap, no probes.
	Off State = iota + 1
	// Idle: on, and nothing downloads. No cap, no probes.
	Idle
	// Watching: on, downloads at full speed, the line is smooth.
	Watching
	// Slowed: on, every download together is held to Status.RateLimit.
	Slowed
	// Measuring: a calibration holds downloads at the floor for at most 4 s to learn the
	// line's base delay.
	Measuring
	// NoSignal: on, but the line cannot be measured; downloads run at full speed.
	NoSignal
	// Unavailable: this daemon has no prober (test daemons, unsupported systems).
	Unavailable
)

// String returns the state's name as the relay and logs spell it.
func (s State) String() string {
	switch s {
	case Off:
		return "off"
	case Idle:
		return "idle"
	case Watching:
		return "watching"
	case Slowed:
		return "slowed"
	case Measuring:
		return "measuring"
	case NoSignal:
		return "no_signal"
	case Unavailable:
		return "unavailable"
	}
	return "unknown"
}

// Status is what Polite mode reports.
type Status struct {
	State State
	// RateLimit is the cap applied now (Slowed, Measuring), else 0.
	RateLimit int64
	// DelayMs is the latest line delay over the base, BaseMs the base RTT of the anchor
	// that gave it; -1 when unknown.
	DelayMs, BaseMs int32
	// Since is when this state began.
	Since time.Time
}

// OffStatus is the status of a daemon whose setting is off.
func OffStatus(since time.Time) Status {
	return Status{State: Off, DelayMs: -1, BaseMs: -1, Since: since}
}

// UnavailableStatus is the status of a daemon without a prober whose setting is on.
func UnavailableStatus(since time.Time) Status {
	return Status{State: Unavailable, DelayMs: -1, BaseMs: -1, Since: since}
}

// millis converts a duration to whole milliseconds for a Status (rounded to nearest).
func millis(d time.Duration) int32 {
	return int32((d + time.Millisecond/2) / time.Millisecond)
}
