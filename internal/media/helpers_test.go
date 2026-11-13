package media

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", s, err)
	}
	return u
}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sentinels lists every error kind the package returns.
var sentinels = []error{ErrEncrypted, ErrLive, ErrTooLarge, ErrMalformed, ErrUnsupported, ErrNotFound}

// checkClassified fails unless err wraps exactly one sentinel.
func checkClassified(t testing.TB, what string, err error) {
	t.Helper()
	n := 0
	for _, s := range sentinels {
		if errors.Is(err, s) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s: error %q wraps %d sentinel errors, want 1", what, err, n)
	}
}

// checkPlan verifies the invariants every plan satisfies.
func checkPlan(t testing.TB, p *Plan) {
	t.Helper()
	switch p.Container {
	case ContainerTS, ContainerFMP4, ContainerWebM, ContainerUnknown:
	default:
		t.Fatalf("plan container %q", p.Container)
	}
	media := p.MediaFragments()
	if media == 0 || media > MaxSegments {
		t.Fatalf("plan has %d media fragments, want 1..%d", media, MaxSegments)
	}
	if inits := len(p.Fragments) - media; inits > media {
		t.Fatalf("plan has %d init fragments for %d media fragments", inits, media)
	}
	if p.Duration < 0 {
		t.Fatalf("plan duration %v", p.Duration)
	}
	for i, f := range p.Fragments {
		if f.Index != i {
			t.Fatalf("fragment %d has Index %d", i, f.Index)
		}
		if f.URL == "" || len(f.URL) > MaxURIBytes {
			t.Fatalf("fragment %d URL length %d", i, len(f.URL))
		}
		if f.Duration < 0 || (f.Init && f.Duration != 0) {
			t.Fatalf("fragment %d duration %v (init %v)", i, f.Duration, f.Init)
		}
		for _, r := range []*[2]int64{f.ByteRange, f.IndexRange, f.InitRange} {
			if r != nil && (r[0] < 0 || r[1] < r[0]) {
				t.Fatalf("fragment %d range %v", i, *r)
			}
		}
	}
}

type wantFragment struct {
	url   string
	rng   *[2]int64
	init  bool
	disc  bool
	durMS int64
}

func rng(a, b int64) *[2]int64 { return &[2]int64{a, b} }

func comparePlan(t *testing.T, p *Plan, want []wantFragment) {
	t.Helper()
	checkPlan(t, p)
	if len(p.Fragments) != len(want) {
		for i, f := range p.Fragments {
			t.Logf("fragment %d: %+v", i, f)
		}
		t.Fatalf("plan has %d fragments, want %d", len(p.Fragments), len(want))
	}
	for i, w := range want {
		checkFragment(t, p, i, w)
	}
}

// checkFragment compares fragment i of p with w.
func checkFragment(t *testing.T, p *Plan, i int, w wantFragment) {
	t.Helper()
	if i < 0 || i >= len(p.Fragments) {
		t.Fatalf("plan has no fragment %d (%d fragments)", i, len(p.Fragments))
	}
	f := p.Fragments[i]
	if f.URL != w.url {
		t.Errorf("fragment %d URL = %q, want %q", i, f.URL, w.url)
	}
	if (f.ByteRange == nil) != (w.rng == nil) || (f.ByteRange != nil && *f.ByteRange != *w.rng) {
		t.Errorf("fragment %d range = %v, want %v", i, f.ByteRange, w.rng)
	}
	if f.Init != w.init || f.Discontinuity != w.disc {
		t.Errorf("fragment %d init/disc = %v/%v, want %v/%v", i, f.Init, f.Discontinuity, w.init, w.disc)
	}
	if got := f.Duration.Milliseconds(); got != w.durMS {
		t.Errorf("fragment %d duration = %dms, want %dms", i, got, w.durMS)
	}
}
