package limiter

import (
	"fmt"
	"testing"
)

// TestRateAccuracy checks the long-run rate of chains where either the parent or the
// child is the bottleneck. Each case moves about two seconds of traffic.
func TestRateAccuracy(t *testing.T) {
	t.Parallel()
	const tolerance = 0.10

	type tc struct {
		name  string
		build func() []flow
		want  float64
	}
	var cases []tc
	for _, r := range []int{256 * kib, mib} {
		rate := int64(r)
		cases = append(cases,
			tc{
				name: "single/" + human(rate),
				build: func() []flow {
					return []flow{{New(rate), 2 * r, 32 * kib}}
				},
				want: float64(r),
			},
			tc{
				name: "parent-bottleneck/" + human(rate),
				build: func() []flow {
					parent := New(rate)
					child := parent.Child(4 * rate)
					// 256 KiB slices exceed the 64 KiB burst at 256 KiB/s and are split.
					return []flow{{child, 2 * r, 256 * kib}}
				},
				want: float64(r),
			},
			tc{
				name: "child-bottleneck/" + human(rate),
				build: func() []flow {
					parent := New(4 * rate)
					child := parent.Child(rate)
					return []flow{{child, 2 * r, 64 * kib}}
				},
				want: float64(r),
			},
			tc{
				name: "shared-parent-bottleneck/" + human(rate),
				build: func() []flow {
					global := New(0)
					queue := global.Child(rate)
					var flows []flow
					for i := range 4 {
						download := queue.Child(int64(i%2) * 4 * rate) // unlimited or 4x
						flows = append(flows,
							flow{download, r / 4, 16 * kib},
							flow{download, r / 4, 64 * kib})
					}
					return flows
				},
				want: float64(r),
			},
			tc{
				name: "children-bottleneck/" + human(rate),
				build: func() []flow {
					queue := New(8 * rate)
					a, b := queue.Child(rate/2), queue.Child(rate/2)
					return []flow{
						{a, r / 2, 16 * kib}, {a, r / 2, 32 * kib},
						{b, r / 2, 16 * kib}, {b, r / 2, 32 * kib},
					}
				},
				want: float64(r),
			},
		)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			assertRate(t, measureRate(t, c.build()...), c.want, tolerance)
		})
	}
}

func human(bps int64) string {
	if bps >= mib {
		return fmt.Sprintf("%dMiBps", bps/mib)
	}
	return fmt.Sprintf("%dKiBps", bps/kib)
}
