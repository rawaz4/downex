package media

import (
	"errors"
	"strings"
	"testing"
)

const rangedURL = "https://media.example/videoplayback?id=1&expire=1700000000"

func rangedTrack(size, chunk int64) RangedTrack {
	return RangedTrack{ID: "137", Key: "s1-0123", Kind: KindVideo, Ext: "mp4", URL: rangedURL, Size: size, ChunkBytes: chunk}
}

// A ranged track is cut into consecutive ranges of ChunkBytes, the last one shorter;
// the first is marked Init and every range belongs to the one resource.
func TestPlanRangedCutsChunks(t *testing.T) {
	const mib = 1 << 20
	for name, c := range map[string]struct {
		size, chunk int64
		want        []wantFragment
	}{
		"short last chunk": {10*mib + 1, 4 * mib, []wantFragment{
			{url: rangedURL, rng: rng(0, 4*mib-1), init: true},
			{url: rangedURL, rng: rng(4*mib, 8*mib-1)},
			{url: rangedURL, rng: rng(8*mib, 10*mib)},
		}},
		"exact multiple": {8 * mib, 4 * mib, []wantFragment{
			{url: rangedURL, rng: rng(0, 4*mib-1), init: true},
			{url: rangedURL, rng: rng(4*mib, 8*mib-1)},
		}},
		"smaller than a chunk": {12345, MinChunkBytes, []wantFragment{{url: rangedURL, rng: rng(0, 12344), init: true}}},
		"one byte":             {1, MaxChunkBytes, []wantFragment{{url: rangedURL, rng: rng(0, 0), init: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := PlanRanged(rangedTrack(c.size, c.chunk))
			if err != nil {
				t.Fatal(err)
			}
			comparePlan(t, p, c.want)
			if !p.SingleResource || p.Container != ContainerFMP4 || p.Duration != 0 {
				t.Errorf("plan single=%v container=%q duration=%v, want true, fmp4, 0", p.SingleResource, p.Container, p.Duration)
			}
			if got := p.MediaFragments(); got != len(c.want) {
				t.Errorf("MediaFragments = %d, want %d (every range is media)", got, len(c.want))
			}
		})
	}
}

// A track of unknown size, or one the server cannot resume, is one whole-resource
// request; its chunk size is not used.
func TestPlanRangedWholeResource(t *testing.T) {
	for name, tr := range map[string]RangedTrack{
		"unknown size":          rangedTrack(0, 0),
		"negative size":         rangedTrack(-1, 0),
		"unranged":              {Kind: KindMuxed, Ext: "mp4", URL: rangedURL, Size: 5 << 20, ChunkBytes: MinChunkBytes, Unranged: true},
		"unranged without size": {Kind: KindAudio, Ext: "m4a", URL: rangedURL, Unranged: true},
	} {
		p, err := PlanRanged(tr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		comparePlan(t, p, []wantFragment{{url: rangedURL, init: true}})
		if !p.SingleResource {
			t.Errorf("%s: plan is not SingleResource", name)
		}
	}
}

// The largest track at the largest chunk size, and the most ranges a plan may have,
// are planned; one byte or one range more is refused.
func TestPlanRangedBounds(t *testing.T) {
	p, err := PlanRanged(rangedTrack(MaxRangedSize, MaxChunkBytes))
	if err != nil {
		t.Fatal(err)
	}
	checkPlan(t, p)
	if n := len(p.Fragments); n != MaxRangedSize/MaxChunkBytes {
		t.Fatalf("%d fragments, want %d", n, MaxRangedSize/MaxChunkBytes)
	}
	if last := p.Fragments[len(p.Fragments)-1].ByteRange; last[1] != MaxRangedSize-1 {
		t.Errorf("last range ends at %d, want %d", last[1], int64(MaxRangedSize-1))
	}
	p, err = PlanRanged(rangedTrack(MaxSegments*MinChunkBytes, MinChunkBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fragments) != MaxSegments {
		t.Fatalf("%d fragments, want %d", len(p.Fragments), MaxSegments)
	}
	for name, tr := range map[string]RangedTrack{
		"too large":             rangedTrack(MaxRangedSize+1, MaxChunkBytes),
		"too large and unknown": {Kind: KindVideo, URL: rangedURL, Size: MaxRangedSize + 1, Unranged: true},
		"too many ranges":       rangedTrack(MaxSegments*MinChunkBytes+1, MinChunkBytes),
	} {
		_, err := PlanRanged(tr)
		checkClassified(t, name, err)
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v, want ErrTooLarge", name, err)
		}
	}
	if _, err := PlanRanged(rangedTrack(MaxRangedSize, DefaultChunkBytes)); err != nil {
		t.Errorf("DefaultChunkBytes: %v", err)
	}
}

// Unusable tracks are refused with a classified error that never repeats the URL.
func TestPlanRangedRefuses(t *testing.T) {
	const secret = "s3cr3t-t0ken"
	with := func(f func(*RangedTrack)) RangedTrack {
		tr := rangedTrack(10<<20, MinChunkBytes)
		tr.URL += "&sig=" + secret
		f(&tr)
		return tr
	}
	for name, c := range map[string]struct {
		tr   RangedTrack
		want error
	}{
		"no URL":            {with(func(tr *RangedTrack) { tr.URL = "" }), ErrMalformed},
		"relative URL":      {with(func(tr *RangedTrack) { tr.URL = "videoplayback?" + secret }), ErrMalformed},
		"other scheme":      {with(func(tr *RangedTrack) { tr.URL = "ftp://media.example/" + secret }), ErrMalformed},
		"no host":           {with(func(tr *RangedTrack) { tr.URL = "https:///" + secret }), ErrMalformed},
		"port only":         {with(func(tr *RangedTrack) { tr.URL = "https://:443/" + secret }), ErrMalformed},
		"unparsable":        {with(func(tr *RangedTrack) { tr.URL = "https://[" + secret + "/" }), ErrMalformed},
		"URL too long":      {with(func(tr *RangedTrack) { tr.URL += strings.Repeat("a", MaxURIBytes) }), ErrTooLarge},
		"subtitles":         {with(func(tr *RangedTrack) { tr.Kind = KindSubtitles }), ErrMalformed},
		"no kind":           {with(func(tr *RangedTrack) { tr.Kind = "" }), ErrMalformed},
		"bad network":       {with(func(tr *RangedTrack) { tr.Network = "udp" }), ErrMalformed},
		"no chunk size":     {with(func(tr *RangedTrack) { tr.ChunkBytes = 0 }), ErrMalformed},
		"chunk too small":   {with(func(tr *RangedTrack) { tr.ChunkBytes = MinChunkBytes - 1 }), ErrMalformed},
		"chunk too large":   {with(func(tr *RangedTrack) { tr.ChunkBytes = MaxChunkBytes + 1 }), ErrMalformed},
		"negative chunk":    {with(func(tr *RangedTrack) { tr.ChunkBytes = -MinChunkBytes }), ErrMalformed},
		"too large, secret": {with(func(tr *RangedTrack) { tr.Size = MaxRangedSize + 1 }), ErrTooLarge},
	} {
		_, err := PlanRanged(c.tr)
		checkClassified(t, name, err)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error repeats the URL: %v", name, err)
		}
	}
}

// The fields a plan does not use are carried by the track, not checked.
func TestPlanRangedAcceptsTrackFields(t *testing.T) {
	for _, network := range []string{"", "tcp4", "tcp6"} {
		tr := rangedTrack(3<<20, MinChunkBytes)
		tr.Network = network
		tr.Headers = map[string][]string{"User-Agent": {"x"}}
		if _, err := PlanRanged(tr); err != nil {
			t.Errorf("network %q: %v", network, err)
		}
	}
	for ext, want := range map[string]Container{"mp4": ContainerFMP4, "m4a": ContainerFMP4, "": ContainerUnknown} {
		tr := rangedTrack(3<<20, MinChunkBytes)
		tr.Ext = ext
		p, err := PlanRanged(tr)
		if err != nil {
			t.Fatalf("ext %q: %v", ext, err)
		}
		if p.Container != want {
			t.Errorf("ext %q: container %q, want %q", ext, p.Container, want)
		}
	}
}
