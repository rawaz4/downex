package polite

import "time"

// The parameters of docs/POLITE-SPEC.md (rules 3-11 and the §4 table). Rates are in
// bytes per second.
const (
	// Tick is how often the controller decides (rule 9).
	Tick = 500 * time.Millisecond
	// ProbeSpacing is how often one probe may be sent, round robin over the anchors
	// (rule 4): at most 1/ProbeSpacing = 4 probes per second in total.
	ProbeSpacing = 250 * time.Millisecond
	// ProbeTimeout bounds one probe; a later answer is no sample (rule 4).
	ProbeTimeout = time.Second
	// MaxAnchors is the most anchors probed at once (rule 3).
	MaxAnchors = 3
	// AnchorKeep is how long an anchor that dropped out of the live connections keeps its
	// history (rule 3).
	AnchorKeep = 10 * time.Minute

	// ICMPFailuresToTCP consecutive ICMP probes without an answer, from an anchor that
	// never answered one, switch it to TCP connect probes (rule 4).
	ICMPFailuresToTCP = 3
	// ICMPRetryAfter is how long an anchor stays on TCP probes before ICMP is tried
	// again (rule 4).
	ICMPRetryAfter = 5 * time.Minute
	// TCPProbeSpacing is the least time between two TCP probes of one anchor (rule 4;
	// at most one connection attempt every 2 s, well under one per second per server).
	TCPProbeSpacing = 2 * time.Second

	// BaseBuckets one-minute buckets of minimum RTTs make an anchor's base (rule 6).
	BaseBuckets = 10
	// BaseBucket is the length of one base bucket.
	BaseBucket = time.Minute
	// FilterWindow is the decision window's length: an anchor's extra delay is its
	// minimum over it (rule 8). Long enough for about 6 samples across the anchors, short
	// enough that a growing queue is seen before it is large (§9: a 3 s window let the
	// queue reach 150-200 ms before the first cut).
	FilterWindow = 1500 * time.Millisecond
	// TCPFilterWindow is the decision window of TCP probe samples (rule 8): one anchor
	// sends at most one every TCPProbeSpacing, so FilterWindow never held the 2 samples d
	// needs and an anchor that does not answer ICMP never gave a d (NO_SIGNAL on every
	// such server). It holds an anchor's latest two: two spacings plus the round trip,
	// which ProbeTimeout bounds.
	TCPFilterWindow = 2*TCPProbeSpacing + ProbeTimeout

	// Floor is the lowest cap Polite mode ever sets (rule 9): downloads never stop.
	Floor int64 = 64 << 10
	// Quiet is the receive rate under which the line counts as quiet for trust and
	// calibration (rule 7).
	Quiet int64 = 128 << 10

	// DefaultTarget is the extra line delay kept under when polite_target_ms is 0.
	DefaultTarget = 30 * time.Millisecond
	// MinTarget and MaxTarget bound polite_target_ms (rule 19).
	MinTarget = 10 * time.Millisecond
	MaxTarget = 200 * time.Millisecond

	// EngageSettle is how long after an engage no decision is taken (rule 8): the
	// receive buffers drain before the first cut is judged.
	EngageSettle = 3 * time.Second
	// DecreaseSettle is how long after a decrease the next one waits for fresh samples.
	DecreaseSettle = 500 * time.Millisecond
	// GapReset is the tick gap that counts as sleep or resume (rule 20).
	GapReset = 5 * time.Second
	// EngageTicks consecutive ticks over the target engage (rule 10).
	EngageTicks = 2

	// CutGain sets the decrease factor max(0.5, 1 - (d-T)/(CutGain*T)): 0.75 at twice the
	// target, halving from three times it (rule 10).
	CutGain = 4.0
	// IncreaseDiv: an increase adds up to max(Floor/4, R/IncreaseDiv) per tick; within
	// [NearLow, NearHigh] of the rate that last overloaded the line only
	// max(Floor/16, R/ProbeDiv) (rule 10).
	IncreaseDiv int64 = 16
	ProbeDiv    int64 = 1024
	NearLow           = 0.9
	NearHigh          = 1.05
	// Binding: the cap binds when Downex received at least this share of it over the last
	// tick; only then is it raised (rule 10).
	Binding = 0.95
	// SteadyBand: Downex's rate is steady when its rate over the last tick is within this
	// share of its 10 s rate; steady under the cap, the cap counts as free (rule 10).
	SteadyBand = 0.05

	// NoTrustedAfter is how long WATCHING or NO_SIGNAL go without a trusted live anchor,
	// while downloads are not quiet, before a calibration (rule 10).
	NoTrustedAfter = 5 * time.Second
	// NoSignalAfter is how long d may stay unknown before fail-open (rule 10).
	NoSignalAfter = 10 * time.Second
	// CalibrateSpacing is the least time between two calibrations, and
	// CalibrateSpacingIdle the least time after one that moved no anchor's base by
	// BaseMoved or more (rule 10).
	CalibrateSpacing     = time.Minute
	CalibrateSpacingIdle = 10 * time.Minute
	BaseMoved            = 5 * time.Millisecond
	// CalibrateMin and CalibrateMax bound a calibration; samples sent CalibrateTrust
	// after its start make their anchor trusted (rules 7b and 10).
	CalibrateMin   = 1500 * time.Millisecond
	CalibrateMax   = 4 * time.Second
	CalibrateTrust = time.Second
	// CalibrateNeeds samples sent after CalibrateTrust from every selected anchor end a
	// calibration early.
	CalibrateNeeds = 2
	// AnsweredWithin: a calibration is started only when some selected anchor answered a
	// probe this recently (calibrating cannot help when none answers).
	AnsweredWithin = 5 * time.Second

	// ReleaseHoldMin is how long the cap must not bind, with a quiet line, before it is
	// removed; ReleaseHoldMax bounds its doubling when the cap re-engages within
	// ReengageWithin of a release; ReleaseReset without an engage resets it (rule 10).
	ReleaseHoldMin = 10 * time.Second
	ReleaseHoldMax = 5 * time.Minute
	ReengageWithin = 30 * time.Second
	ReleaseReset   = 5 * time.Minute
	// StarveAfter with the cap under StarveBelow raises the floor to a tenth of the line's
	// capacity (rule 10).
	StarveAfter       = 15 * time.Minute
	StarveBelow int64 = 4 * Floor
	// CapacityWindow is how long the highest rate seen while WATCHING counts as the
	// line's capacity C.
	CapacityWindow = 24 * time.Hour

	// ApplyChange is the least relative change of the cap passed on to the limiter;
	// PublishChange and PublishGap throttle the published status while SLOWED (rule 11).
	ApplyChange   = 0.02
	PublishChange = 0.10
	PublishGap    = time.Second
)

// TargetOf returns the target of a polite_target_ms setting: DefaultTarget for 0, else
// that many milliseconds. Validation (rule 19) is the settings' concern.
func TargetOf(ms int) time.Duration {
	if ms <= 0 {
		return DefaultTarget
	}
	return time.Duration(ms) * time.Millisecond
}

// ValidTargetMs reports whether ms is an accepted polite_target_ms: 0 or 10..200.
func ValidTargetMs(ms int) bool {
	return ms == 0 || (ms >= int(MinTarget/time.Millisecond) && ms <= int(MaxTarget/time.Millisecond))
}
