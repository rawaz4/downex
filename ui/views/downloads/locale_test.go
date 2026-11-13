package downloads

import (
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
	"downex/ui/state"
)

// TestAddedColumnFollowsTheLocale: the Added cell is format.Date in the formatter the window
// sets at start (state.UseUserLocale, today's setFormatLocale(navigator.language)), so the
// day and month come in the user's order. The 0.9.2 references were captured in en-GB
// ("2 Nov", index.json page.language); an en-US formatter gives "Nov 2". The row never formats
// dates itself.
func TestAddedColumnFollowsTheLocale(t *testing.T) {
	t.Logf("this PC's user locale (what the product formats with): %s", locale.UserLocale())
	created := Millis(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC).UnixMilli())
	older := Millis(time.Date(2025, 10, 3, 9, 0, 0, 0, time.UTC).UnixMilli())
	for _, c := range []struct {
		tag, zone          string
		thisYear, lastYear string
	}{
		{"en-GB", "Asia/Baghdad", "2 Nov", "3 Oct 2025"},
		{"en-US", "Asia/Baghdad", "Nov 2", "Oct 3, 2025"},
	} {
		t.Run(c.tag, func(t *testing.T) {
			f, err := locale.New(c.tag, c.zone)
			if err != nil {
				t.Skip(err)
			}
			prev := format.Locale()
			format.SetLocale(f)
			t.Cleanup(func() { format.SetLocale(prev) })
			r, _ := fakeRig(t,
				Download(1, func(d *downexv1.Download) {
					d.State, d.Received, d.CreatedAt = state.StateCompleted, 10*MiB, created
				}),
				Download(2, func(d *downexv1.Download) {
					d.State, d.Received, d.CreatedAt = state.StateCompleted, 10*MiB, older
				}),
			)
			if s := r.cellText(1, "added"); s != c.thisYear {
				t.Errorf("this year: %q, want %q", s, c.thisYear)
			}
			if s := r.cellText(2, "added"); s != c.lastYear {
				t.Errorf("last year: %q, want %q", s, c.lastYear)
			}
		})
	}
}
