package mediajob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stepper holds back the fragment requests of a job with one connection: request q is
// answered only once a progress event has reported q fragments done, so a test sees an
// event after every fragment. Playlist requests pass at once.
type stepper struct {
	mu    sync.Mutex
	gates []chan struct{}
	asked int // fragment requests received
	open  int // fragment requests let through
}

func (st *stepper) gate(q int) chan struct{} {
	for len(st.gates) <= q {
		st.gates = append(st.gates, make(chan struct{}))
	}
	return st.gates[q]
}

func (st *stepper) hook(r *http.Request) {
	if strings.HasSuffix(r.URL.Path, ".m3u8") {
		return
	}
	st.mu.Lock()
	g := st.gate(st.asked)
	st.asked++
	st.mu.Unlock()
	select {
	case <-g:
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
}

// release lets the first n fragment requests through.
func (st *stepper) release(n int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for ; st.open < n; st.open++ {
		close(st.gate(st.open))
	}
}

// steppedRun runs sel with one connection, releasing one fragment request per progress
// event, and returns the events.
func steppedRun(t *testing.T, s *server, opts Options, sel Selection, resume *Checkpoint) ([]Progress, *recorder, error) {
	t.Helper()
	st := &stepper{}
	s.setHook(st.hook)
	t.Cleanup(func() { s.setHook(nil) })
	opts.MaxConnections = 1
	rec := &recorder{}
	_, err := Run(testContext(t), opts, sel, resume, func(p Progress) {
		rec.onProgress(p)
		st.release(p.FragmentsDone + 1)
	}, rec.onCheckpoint)
	return rec.progress, rec, err
}

// timedPlaylist renders a VOD media playlist whose segment i lasts durs[i] seconds.
func timedPlaylist(mapURI string, segs []string, durs []int) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:8\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	if mapURI != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", mapURI)
	}
	for i, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%d.000,\n%s\n", durs[i], s)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// addSteadyTS serves a media playlist of TS segments of the given durations whose sizes
// are proportional to them (564 bytes per second of media), and returns their
// concatenation.
func addSteadyTS(s *server, dir string, durs []int) []byte {
	var segs []string
	var all []byte
	for i, d := range durs {
		name := fmt.Sprintf("seg%d.ts", i)
		data := tsSegment(uint64(900+i), 3*d)
		s.add(dir+name, data, "video/mp2t")
		segs = append(segs, name)
		all = append(all, data...)
	}
	s.add(dir+"index.m3u8", []byte(timedPlaylist("", segs, durs)), hlsType)
	return all
}

// A stream without byte ranges reports no total until a media fragment is durable, then
// an estimate from the bytes per second of media so far (the segments differ in length:
// a count of fragments would be wrong), and the total of its fragments once all are
// durable: still an estimate of the file, an MP4 written anew from them.
func TestProgressEstimatesTheStreamSize(t *testing.T) {
	s := newServer(t)
	durs := []int{2, 4, 6, 4, 2, 6, 4, 4}
	want := addSteadyTS(s, "/e/", durs)
	total := int64(len(want))

	events, _, err := steppedRun(t, s, testOptions(t, &fakeAssembler{}), Selection{ManifestURL: s.url("/e/index.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]bool)
	for _, p := range events {
		seen[p.FragmentsDone] = true
		switch done := p.FragmentsDone; {
		case done == 0:
			if p.BytesTotal != 0 || p.BytesTotalApprox {
				t.Errorf("before any fragment: total %d (approx %v), want unknown", p.BytesTotal, p.BytesTotalApprox)
			}
		case done < len(durs):
			if p.BytesTotal != total || !p.BytesTotalApprox {
				t.Errorf("%d of %d fragments: total %d (approx %v), want the estimate %d", done, len(durs), p.BytesTotal,
					p.BytesTotalApprox, total)
			}
		default:
			if p.BytesTotal != total || !p.BytesTotalApprox || p.BytesDone != total {
				t.Errorf("all fragments: total %d (approx %v), done %d, want the estimate %d", p.BytesTotal, p.BytesTotalApprox,
					p.BytesDone, total)
			}
		}
	}
	for k := range len(durs) + 1 {
		if !seen[k] {
			t.Errorf("no progress event with %d fragments done: %+v", k, events)
		}
	}
}

// Every fragment with a byte range (EXT-X-BYTERANGE) gives the total of the fragments
// from the first event on, marked approximate all the same: the file is an MP4 written
// anew, whose size differs.
func TestProgressTotalOfByteRanges(t *testing.T) {
	s := newServer(t)
	want := addByteRangePlaylist(s, "/b/")
	events, _, err := steppedRun(t, s, testOptions(t, &fakeAssembler{}), Selection{ManifestURL: s.url("/b/index.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range events {
		if p.BytesTotal != int64(len(want)) || !p.BytesTotalApprox {
			t.Fatalf("%d fragments done: total %d (approx %v), want the estimate %d", p.FragmentsDone, p.BytesTotal, p.BytesTotalApprox, len(want))
		}
	}
}

// Each track is estimated on its own: a video of 1000 bytes per second in eight
// fragments and an audio of 100 bytes per second in two, which finishes first. The total
// stays unknown until both tracks have a durable media fragment.
func TestProgressEstimatesEachTrack(t *testing.T) {
	s := newServer(t)
	s.add("/t/master.m3u8", []byte(masterAudio), hlsType)
	addSteady := func(dir string, durs []int, rate int, seed uint64) []byte {
		init := initSegment(dir, false)
		s.add(dir+"init.mp4", init, "video/mp4")
		all := bytes.Clone(init)
		var names []string
		for i, d := range durs {
			name := fmt.Sprintf("s%d.m4s", i)
			seg := mediaSegment(seed+uint64(i), rate*d-32) // moof+mdat headers are 32 bytes
			s.add(dir+name, seg, "video/iso.segment")
			names = append(names, name)
			all = append(all, seg...)
		}
		s.add(dir+"index.m3u8", []byte(timedPlaylist("init.mp4", names, durs)), hlsType)
		return all
	}
	video := addSteady("/t/video/", []int{2, 2, 2, 2, 2, 2, 2, 2}, 1000, 10)
	audio := addSteady("/t/audio/", []int{8, 8}, 100, 20)
	addSteady("/t/audio-de/", []int{8, 8}, 100, 30)
	total := int64(len(video) + len(audio))

	events, _, err := steppedRun(t, s, testOptions(t, &fakeAssembler{}), Selection{ManifestURL: s.url("/t/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Fetched in order: both initialization segments, then video 0, audio 0, video 1,
	// audio 1, video 2 … 7.
	for _, p := range events {
		switch done := p.FragmentsDone; {
		case done <= 3:
			if p.BytesTotal != 0 || p.BytesTotalApprox {
				t.Errorf("%d fragments done (no audio fragment yet): total %d (approx %v), want unknown", done, p.BytesTotal, p.BytesTotalApprox)
			}
		case done < 12:
			if p.BytesTotal != total || !p.BytesTotalApprox {
				t.Errorf("%d fragments done: total %d (approx %v), want the estimate %d", done, p.BytesTotal, p.BytesTotalApprox, total)
			}
		default:
			if p.BytesTotal != total || !p.BytesTotalApprox {
				t.Errorf("all fragments: total %d (approx %v), want the estimate %d", p.BytesTotal, p.BytesTotalApprox, total)
			}
		}
	}
}

// A resumed job estimates from the fragments it takes over at once, and a Run that finds
// the job assembled reports the output's size as its total, exactly.
func TestProgressEstimateAfterResume(t *testing.T) {
	s := newServer(t)
	durs := []int{2, 4, 6, 4, 2, 6}
	want := addSteadyTS(s, "/r/", durs)
	sel := Selection{ManifestURL: s.url("/r/index.m3u8")}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var once sync.Once
	s.setHook(func(r *http.Request) {
		if r.URL.Path == "/r/seg3.ts" {
			once.Do(func() {
				cancel()
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			})
		}
	})
	opts := testOptions(t, &fakeAssembler{})
	opts.MaxConnections = 1
	var rec recorder
	if _, err := Run(ctx, opts, sel, nil, rec.onProgress, rec.onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	s.setHook(nil)

	var rec2 recorder
	if _, err := Run(testContext(t), opts, sel, rec.last(), rec2.onProgress, rec2.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	if first := rec2.progress[0]; first.FragmentsDone != 3 || first.BytesTotal != int64(len(want)) || !first.BytesTotalApprox {
		t.Fatalf("resumed progress starts at %+v, want 3 fragments and the estimate %d", first, len(want))
	}

	var rec3 recorder
	if _, err := Run(testContext(t), opts, sel, rec2.last(), rec3.onProgress, rec3.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	if len(rec3.progress) != 1 || rec3.progress[0].BytesTotal != int64(len(want)) || rec3.progress[0].BytesTotalApprox {
		t.Fatalf("assembled job's progress = %+v, want the output's size %d", rec3.progress, len(want))
	}
}

// When Run stops, its last checkpoint follows a progress event without the bytes of the
// requests it gave up: a caller that stores the bytes done with each checkpoint (the
// daemon, which extrapolates a paused stream's size from them) stores those of the
// durable fragments, not of attempts thrown away.
func TestFinalCheckpointFollowsDurableBytes(t *testing.T) {
	const n, segBytes = 8, 100 * 188
	segs := make([][]byte, n)
	names := make([]string, n)
	for i := range segs {
		segs[i] = tsSegment(uint64(700+i), segBytes/188)
		names[i] = fmt.Sprintf("seg%d.ts", i)
	}
	playlist := mediaPlaylist("", names)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.m3u8" {
			w.Header().Set("Content-Type", hlsType)
			_, _ = w.Write([]byte(playlist))
			return
		}
		var i int
		if _, err := fmt.Sscanf(r.URL.Path, "/seg%d.ts", &i); err != nil || i >= n {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Length", fmt.Sprint(segBytes))
		if i < 2 {
			_, _ = w.Write(segs[i])
			return
		}
		// Half of the segment, then nothing until the job gives up.
		_, _ = w.Write(segs[i][:segBytes/2])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var (
		last  Progress
		saved []Progress // the last event before each checkpoint
		cps   []Checkpoint
	)
	opts := testOptions(t, &fakeAssembler{})
	_, err := Run(ctx, opts, Selection{ManifestURL: srv.URL + "/index.m3u8"}, nil, func(p Progress) {
		last = p
		if p.FragmentsDone == 2 && p.BytesDone > 2*segBytes {
			cancel() // two fragments durable, others half received
		}
	}, func(cp Checkpoint) error {
		saved, cps = append(saved, last), append(cps, cp)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(cps) == 0 {
		t.Fatal("no checkpoint")
	}
	cp, p := cps[len(cps)-1], saved[len(saved)-1]
	durable := 0
	for i := range n {
		if bitSet(cp.Tracks[0].Done, i) {
			durable++
		}
	}
	if durable != 2 || p.FragmentsDone != durable || p.BytesDone != int64(durable*segBytes) {
		t.Errorf("last checkpoint (%d durable fragments) follows %d fragments and %d bytes done, want %d bytes", durable,
			p.FragmentsDone, p.BytesDone, durable*segBytes)
	}
}
