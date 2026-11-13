package widgets

import "time"

// The DateTimeField's rules (DateTimeField.svelte's parseParts, formatParts, partAt,
// partRange and stepParts) are ui/core/queues' FieldKind, ParseParts, FormatParts, PartAt,
// PartRange and StepParts: one owner, which the schedule editor's checks share.

// todayParts is today's date in local time [year, month, day].
func todayParts(now time.Time) []int {
	return []int{now.Year(), int(now.Month()), now.Day()}
}
