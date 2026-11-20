package downloads

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/state"
)

// TestDetailsRescued (docs/DESKTOP-SPEC.md §11.3): the "Rescued" row while rescued_bytes > 0,
// also after completion, and none for other downloads.
func TestDetailsRescued(t *testing.T) {
	p := showPane(t, Download(7, func(d *downexv1.Download) {
		d.State, d.Received, d.RescuedBytes = state.StatePaused, 4_120_000, 4_120_000
	}), nil)
	if got, ok := p.fact("Rescued"); !ok || got != "4.1 MB from a partial file" {
		t.Errorf("Rescued %q, %v", got, ok)
	}
	done := showPane(t, Download(8, func(d *downexv1.Download) {
		d.State, d.RescuedBytes = state.StateCompleted, 1_000
	}), nil)
	if got, ok := done.fact("Rescued"); !ok || got != "1.0 KB from a partial file" {
		t.Errorf("completed: Rescued %q, %v", got, ok)
	}
	plain := showPane(t, Download(9, func(d *downexv1.Download) { d.State = state.StatePaused }), nil)
	if _, ok := plain.fact("Rescued"); ok {
		t.Error("a download that rescued nothing has the row")
	}
}
