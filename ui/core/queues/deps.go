package queues

import (
	"downex/ui/core/format"
	"downex/ui/core/text"
)

// What queues uses of ui/core/format and ui/core/text: the rate fields and JavaScript's trim
// and toLowerCase, as today's window compares queue names.

var (
	jsTrim  = text.TrimJS
	jsLower = text.LowerJS
)

// RateUnit is the unit of a speed field ("KB/s" or "MB/s").
type RateUnit = format.RateUnit

// parseRate reads a typed speed in unit; loaded is the limit the field was filled from (0:
// none), kept exactly while its text is unchanged.
func parseRate(value string, unit RateUnit, loaded int64) (int64, bool) {
	return format.ParseRate(value, unit, float64(loaded))
}

func splitRate(bytesPerSecond int64) (string, RateUnit) {
	return format.SplitRate(float64(bytesPerSecond))
}
