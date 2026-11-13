package mediajob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/limiter"
	"downex/internal/media"
	"downex/internal/remux"
)

const masterTS = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2"
high/index.m3u8
`

// ProbeMP4 lists the MPEG-TS variants of a master playlist, which are saved as an MP4,
// takes the offer's first video as the default and fetches no segment.
func TestProbeHLSMaster(t *testing.T) {
	s := newServer(t)
	s.add("/m/master.m3u8", []byte(masterTS), hlsType)
	addTSPlaylist(s, "/m/low/", 3, 1)
	addTSPlaylist(s, "/m/high/", 5, 2)

	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/m/master.m3u8")}, byHeight)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != "hls" || len(info.Tracks) != 2 || info.DefaultVideo != "v1" || info.DefaultAudio != "" {
		t.Fatalf("info = %+v", info)
	}
	if info.Container != "mp4" || info.FragmentsTotal != 5 || info.Duration != 20*time.Second {
		t.Fatalf("container %q, fragments %d, duration %v", info.Container, info.FragmentsTotal, info.Duration)
	}
	if s.hitCount("/m/high/seg0.ts") != 0 || s.hitCount("/m/low/seg0.ts") != 0 {
		t.Fatal("ProbeMP4 fetched a segment")
	}

	info, err = ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/m/master.m3u8"), VideoTrack: "v0"}, byHeight)
	if err != nil || info.DefaultVideo != "v0" || info.FragmentsTotal != 3 {
		t.Fatalf("explicit selection: %+v, %v", info, err)
	}
}

func TestRunHLSTransportStream(t *testing.T) {
	s := newServer(t)
	s.add("/m/master.m3u8", []byte(masterTS), hlsType)
	addTSPlaylist(s, "/m/low/", 3, 1)
	want := addTSPlaylist(s, "/m/high/", 9, 2)
	s.setDelay(40 * time.Millisecond)

	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MaxConnections = 2
	var rec recorder
	container, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/m/master.m3u8")}, nil, rec.onProgress, rec.onCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if container != "mp4" {
		t.Fatalf("container = %q", container)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, want %d", len(got), len(want))
	}
	// The part directory is the caller's to remove once it has stored the completion.
	if _, err := os.Stat(filepath.Join(opts.PartDir, outputRecordName)); err != nil {
		t.Fatalf("no output record in the part directory: %v", err)
	}
	if _, err := os.Stat(opts.OutPath + ".dxpart"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("part file left behind: %v", err)
	}
	in := asm.inputs
	if len(in) != 1 || in[0].Kind != remux.Kind("muxed") || in[0].Container != "ts" || in[0].Init != "" || len(in[0].Fragments) != 9 {
		t.Fatalf("inputs = %+v", in)
	}

	// Progress: monotonic, at most 4 Hz, ending complete.
	if len(rec.progress) < 2 {
		t.Fatalf("progress events: %d", len(rec.progress))
	}
	final := rec.progress[len(rec.progress)-1]
	if final.FragmentsDone != 9 || final.FragmentsTotal != 9 || final.BytesDone != int64(len(want)) || final.Connections != 0 {
		t.Fatalf("final progress = %+v", final)
	}
	for i := 1; i < len(rec.progress); i++ {
		if rec.progress[i].FragmentsDone < rec.progress[i-1].FragmentsDone {
			t.Fatalf("progress went backwards: %+v", rec.progress)
		}
		if i < len(rec.progress)-1 && i > 1 {
			if gap := rec.progressAt[i].Sub(rec.progressAt[i-1]); gap < 200*time.Millisecond {
				t.Fatalf("progress events %v apart", gap)
			}
		}
	}
	cp := rec.last()
	if cp == nil || len(cp.Tracks) != 1 || cp.Tracks[0].TrackID != "v1" || cp.Tracks[0].Total != 9 {
		t.Fatalf("checkpoint = %+v", cp)
	}
	for i := range 9 {
		if !bitSet(cp.Tracks[0].Done, i) {
			t.Fatalf("fragment %d not in the final checkpoint", i)
		}
	}
}

const masterAudio = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/index.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Deutsch",LANGUAGE="de",URI="audio-de/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
video/index.m3u8
`

func TestRunHLSSeparateAudioFMP4(t *testing.T) {
	s := newServer(t)
	s.add("/a/master.m3u8", []byte(masterAudio), hlsType)
	vInit, vSegs := addFMP4Playlist(s, "/a/video/", 4, 3)
	aInit, aSegs := addFMP4Playlist(s, "/a/audio/", 3, 4)
	addFMP4Playlist(s, "/a/audio-de/", 3, 5)

	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/a/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.DefaultVideo != "v0" || info.DefaultAudio != "r0" || info.Container != "mp4" || info.FragmentsTotal != 9 {
		t.Fatalf("info = %+v", info)
	}
	probed := s.hitCount("/a/audio-de/index.m3u8") // ProbeMP4 plans every rendition

	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	container, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/a/master.m3u8")}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if container != "mp4" {
		t.Fatalf("container = %q", container)
	}
	want := bytes.Join([][]byte{vInit, vSegs, aInit, aSegs}, nil)
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, want %d", len(got), len(want))
	}
	in := asm.inputs
	if len(in) != 2 || in[0].Kind != "video" || in[1].Kind != "audio" || in[0].Container != "fmp4" ||
		in[0].Init == "" || len(in[0].Fragments) != 4 || len(in[1].Fragments) != 3 {
		t.Fatalf("inputs = %+v", in)
	}
	if s.hitCount("/a/audio-de/index.m3u8") != probed || s.hitCount("/a/audio-de/s0.m4s") != 0 {
		t.Fatal("Run fetched an audio rendition that was not selected")
	}

	// An explicit audio selection.
	opts = testOptions(t, asm)
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/a/master.m3u8"), AudioTrack: "r1"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.hitCount("/a/audio-de/s0.m4s") != 1 {
		t.Fatal("the selected audio rendition was not downloaded")
	}
}

const dashMPD = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S" minBufferTime="PT2S">
  <Period id="p0" duration="PT8S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="1000" initialization="video/$RepresentationID$/init.mp4" media="video/$RepresentationID$/$Number%03d$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="2000" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v720" bandwidth="2000000" width="1280" height="720" codecs="avc1.64001f"/>
      <Representation id="v360" bandwidth="600000" width="640" height="360" codecs="avc1.4d401e"/>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">
      <SegmentTemplate timescale="48000" duration="96000" initialization="audio/init.mp4" media="audio/seg-$Number$.m4s" startNumber="1"/>
      <Representation id="a128" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>
`

// addDASH serves dashMPD and its segments; protected makes the video init segments
// carry a 'pssh' box. It returns the expected output of the default pick.
func addDASH(s *server, protected bool) []byte {
	s.add("/d/manifest.mpd", []byte(dashMPD), "application/dash+xml")
	var want []byte
	for k, rep := range []string{"v720", "v360"} {
		init := initSegment(rep, protected)
		s.add("/d/video/"+rep+"/init.mp4", init, "video/mp4")
		if k == 0 {
			want = append(want, init...)
		}
		for n := 1; n <= 4; n++ {
			seg := mediaSegment(uint64(100*k+n), 700+n)
			s.add(fmt.Sprintf("/d/video/%s/%03d.m4s", rep, n), seg, "video/iso.segment")
			if k == 0 {
				want = append(want, seg...)
			}
		}
	}
	init := initSegment("audio", false)
	s.add("/d/audio/init.mp4", init, "audio/mp4")
	want = append(want, init...)
	for n := 1; n <= 4; n++ {
		seg := mediaSegment(uint64(1000+n), 300+n)
		s.add(fmt.Sprintf("/d/audio/seg-%d.m4s", n), seg, "audio/iso.segment")
		want = append(want, seg...)
	}
	return want
}

func TestRunDASH(t *testing.T) {
	s := newServer(t)
	want := addDASH(s, false)
	sel := Selection{ManifestURL: s.url("/d/manifest.mpd")}

	info, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != "dash" || len(info.Tracks) != 3 || info.DefaultVideo != "v720" || info.DefaultAudio != "a128" ||
		info.Container != "mp4" || info.FragmentsTotal != 10 || info.Duration != 8*time.Second {
		t.Fatalf("info = %+v", info)
	}

	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	var rec recorder
	container, err := Run(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if container != "mp4" {
		t.Fatalf("container = %q", container)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, want %d", len(got), len(want))
	}
	if s.hitCount("/d/video/v360/001.m4s") != 0 {
		t.Fatal("downloaded a Representation that was not selected")
	}
	cp := rec.last()
	if cp == nil || len(cp.Tracks) != 2 || cp.Tracks[0].TrackID != "v720" || cp.Tracks[1].TrackID != "a128" {
		t.Fatalf("checkpoint = %+v", cp)
	}
	// The checkpoint survives a JSON round trip.
	b, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	var back Checkpoint
	if err := json.Unmarshal(b, &back); err != nil || !bytes.Equal(back.Tracks[1].Done, cp.Tracks[1].Done) {
		t.Fatalf("JSON round trip: %v", err)
	}

	// The lower video quality.
	opts = testOptions(t, asm)
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: sel.ManifestURL, VideoTrack: "v360"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.hitCount("/d/video/v360/004.m4s") != 1 || asm.inputs[1].Kind != "audio" {
		t.Fatalf("explicit selection: inputs %+v", asm.inputs)
	}
}

func TestEncryptedInitRefused(t *testing.T) {
	s := newServer(t)
	addDASH(s, true)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/d/manifest.mpd")}, nil, nil, nil)
	if !errors.Is(err, media.ErrEncrypted) {
		t.Fatalf("err = %v, want ErrEncrypted", err)
	}
	for n := 1; n <= 4; n++ {
		if s.hitCount(fmt.Sprintf("/d/video/v720/%03d.m4s", n)) != 0 || s.hitCount(fmt.Sprintf("/d/audio/seg-%d.m4s", n)) != 0 {
			t.Fatal("media fragments were requested for protected content")
		}
	}
	if asm.callCount() != 0 {
		t.Fatal("protected content was assembled")
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("an output file exists")
	}
}

func TestPauseResume(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/p/", 12, 7)
	sel := Selection{ManifestURL: s.url("/p/index.m3u8")}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var once sync.Once
	s.setHook(func(r *http.Request) {
		if r.URL.Path == "/p/seg6.ts" {
			once.Do(func() {
				cancel() // pause while fragment 6 is in flight
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			})
		}
	})

	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MaxConnections = 1
	var rec recorder
	_, err := Run(ctx, opts, sel, nil, rec.onProgress, rec.onCheckpoint)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	cp := rec.last()
	if cp == nil || len(cp.Tracks) != 1 || cp.Tracks[0].Total != 12 {
		t.Fatalf("checkpoint = %+v", cp)
	}
	for i := range 12 {
		if got, want := bitSet(cp.Tracks[0].Done, i), i < 6; got != want {
			t.Fatalf("fragment %d done = %v, want %v", i, got, want)
		}
	}
	if asm.callCount() != 0 {
		t.Fatal("assembled after a pause")
	}
	if p := rec.progress[len(rec.progress)-1]; p.FragmentsDone != 6 {
		t.Fatalf("final progress = %+v", p)
	}
	// The checkpoint counts the bytes of its durable fragments, which the final progress
	// reports too (the daemon stores them as the bytes done).
	durable := int64(0)
	for i := range 6 {
		s.mu.Lock()
		durable += int64(len(s.files[fmt.Sprintf("/p/seg%d.ts", i)].data))
		s.mu.Unlock()
	}
	if p := rec.progress[len(rec.progress)-1]; cp.DurableBytes != durable || p.BytesDone != durable || cp.BytesTotal != 0 {
		t.Fatalf("checkpoint has %d durable bytes (total %d), final progress %d bytes; want %d", cp.DurableBytes, cp.BytesTotal, p.BytesDone, durable)
	}

	// A fragment whose file vanished is fetched again; the others are not.
	if err := os.Remove(filepath.Join(opts.PartDir, "muxed-v0", "3.frag")); err != nil {
		t.Fatal(err)
	}
	s.setHook(nil)
	var rec2 recorder
	container, err := Run(testContext(t), opts, sel, cp, rec2.onProgress, rec2.onCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if container != "mp4" {
		t.Fatalf("container = %q", container)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, want %d", len(got), len(want))
	}
	for i := range 12 {
		want := 1
		if i == 3 || i == 6 {
			want = 2
		}
		if got := s.hitCount(fmt.Sprintf("/p/seg%d.ts", i)); got != want {
			t.Fatalf("segment %d requested %d times, want %d", i, got, want)
		}
	}
	if first := rec2.progress[0]; first.FragmentsDone != 5 || first.FragmentsTotal != 12 {
		t.Fatalf("resumed progress starts at %+v", first)
	}
}

func TestRetryTransient(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/r/", 5, 9)
	s.failNext("/r/seg1.ts", 2, http.StatusServiceUnavailable, "")
	s.failNext("/r/seg3.ts", 1, http.StatusServiceUnavailable, "1")
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	start := time.Now()
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/r/index.m3u8")}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("finished after %v: Retry-After not honoured", el)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output differs")
	}
	if s.hitCount("/r/seg1.ts") != 3 || s.hitCount("/r/seg3.ts") != 2 {
		t.Fatalf("hits: seg1 %d, seg3 %d", s.hitCount("/r/seg1.ts"), s.hitCount("/r/seg3.ts"))
	}
}

func TestPermanentFailure(t *testing.T) {
	s := newServer(t)
	addTSPlaylist(s, "/f/", 6, 9)
	s.failNext("/f/seg2.ts", 100, http.StatusForbidden, "")
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MaxConnections = 1
	var rec recorder
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/f/index.m3u8")}, nil, nil, rec.onCheckpoint)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Fatalf("err = %v, want status 403", err)
	}
	if s.hitCount("/f/seg2.ts") != 1 {
		t.Fatalf("a permanent failure was retried %d times", s.hitCount("/f/seg2.ts"))
	}
	if asm.callCount() != 0 {
		t.Fatal("assembled after a failure")
	}
	if cp := rec.last(); cp == nil || !bitSet(cp.Tracks[0].Done, 1) || bitSet(cp.Tracks[0].Done, 2) {
		t.Fatalf("checkpoint after failure = %+v", cp)
	}

	// Transient failures exhaust the retries.
	s2 := newServer(t)
	addTSPlaylist(s2, "/f/", 2, 9)
	s2.failNext("/f/seg1.ts", 100, http.StatusServiceUnavailable, "")
	opts = testOptions(t, asm)
	opts.MaxRetries = 3
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s2.url("/f/index.m3u8")}, nil, nil, nil); !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("err = %v", err)
	}
	if got := s2.hitCount("/f/seg1.ts"); got != 3 {
		t.Fatalf("seg1 requested %d times, want 3", got)
	}
}

// byteRangePlaylist addresses consecutive parts of one TS file, the map included.
func addByteRangePlaylist(s *server, dir string) []byte {
	all := tsSegment(42, 40)
	s.add(dir+"all.ts", all, "video/mp2t")
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-MAP:URI=\"all.ts\",BYTERANGE=\"376@0\"\n")
	off := 376
	for _, n := range []int{5, 9, 7, 11, 6} {
		fmt.Fprintf(&b, "#EXTINF:4.0,\n#EXT-X-BYTERANGE:%d@%d\nall.ts\n", n*188, off)
		off += n * 188
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	s.add(dir+"index.m3u8", []byte(b.String()), hlsType)
	return all[:off]
}

func TestByteRangeFragments(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(fmt.Sprintf("ignoreRange=%v", ignore), func(t *testing.T) {
			s := newServer(t)
			want := addByteRangePlaylist(s, "/b/")
			s.setIgnoreRange("/b/all.ts", ignore)
			asm := &fakeAssembler{}
			opts := testOptions(t, asm)
			container, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/b/index.m3u8")}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if container != "mp4" {
				t.Fatalf("container = %q", container)
			}
			if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
				t.Fatalf("output: %d bytes, want %d", len(got), len(want))
			}
			if in := asm.inputs[0]; in.Init != "" || len(in.Fragments) != 6 {
				t.Fatalf("inputs = %+v", in)
			}
			ranges := s.seen("/b/all.ts").ranges

			if len(ranges) != 6 {
				t.Fatalf("all.ts requested %d times: %v", len(ranges), ranges)
			}
			for _, want := range []string{"bytes=0-375", "bytes=376-1315"} {
				found := false
				for _, r := range ranges {
					found = found || r == want
				}
				if !found {
					t.Fatalf("no request for %s: %v", want, ranges)
				}
			}
		})
	}
}

func TestConcurrencyBound(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/c/", 24, 11)
	s.setDelay(30 * time.Millisecond)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MaxConnections = 3
	var rec recorder
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/c/index.m3u8")}, nil, rec.onProgress, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output differs")
	}
	s.mu.Lock()
	peak := s.maxActive
	s.mu.Unlock()
	if peak > 3 || peak < 2 {
		t.Fatalf("peak concurrency %d, want 2..3", peak)
	}
	for _, p := range rec.progress {
		if p.Connections > 3 {
			t.Fatalf("progress reports %d connections", p.Connections)
		}
	}
}

// TestCheckpointDurable checks, while the job runs, that every fragment a checkpoint
// reports is complete on disk and that reported fragments stay reported.
func TestCheckpointDurable(t *testing.T) {
	s := newServer(t)
	s.add("/a/master.m3u8", []byte(masterAudio), hlsType)
	addFMP4Playlist(s, "/a/video/", 30, 21)
	addFMP4Playlist(s, "/a/audio/", 25, 22)
	addFMP4Playlist(s, "/a/audio-de/", 1, 23)
	s.mu.Lock()
	files := make(map[string][]byte, len(s.files))
	for p, f := range s.files {
		files[p] = f.data
	}
	s.mu.Unlock()

	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MaxConnections = 6
	opts.CheckpointInterval = time.Nanosecond
	dirs := map[string]string{"v0": "video", "r0": "audio"}
	prev := map[string][]byte{}
	calls := 0
	check := func(cp Checkpoint) error {
		calls++
		durable := int64(0)
		for _, tc := range cp.Tracks {
			for i := range tc.Total {
				if !bitSet(tc.Done, i) {
					if p := prev[tc.TrackID]; p != nil && bitSet(p, i) {
						return fmt.Errorf("track %s fragment %d was reported and then dropped", tc.TrackID, i)
					}
					continue
				}
				name := fmt.Sprintf("/a/%s/s%d.m4s", dirs[tc.TrackID], i-1)
				if i == 0 {
					name = fmt.Sprintf("/a/%s/init.mp4", dirs[tc.TrackID])
				}
				got, err := os.ReadFile(filepath.Join(opts.PartDir, dirs[tc.TrackID]+"-"+tc.TrackID, fmt.Sprintf("%d.frag", i)))
				if err != nil || !bytes.Equal(got, files[name]) {
					return fmt.Errorf("track %s fragment %d reported done but its file is incomplete: %v", tc.TrackID, i, err)
				}
				durable += int64(len(got))
			}
			prev[tc.TrackID] = bytes.Clone(tc.Done)
		}
		if cp.DurableBytes != durable {
			return fmt.Errorf("checkpoint counts %d durable bytes, its fragments have %d", cp.DurableBytes, durable)
		}
		return nil
	}
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/a/master.m3u8")}, nil, nil, check); err != nil {
		t.Fatal(err)
	}
	if calls < 3 {
		t.Fatalf("only %d checkpoints", calls)
	}
}

func TestCheckpointErrorStops(t *testing.T) {
	s := newServer(t)
	addTSPlaylist(s, "/e/", 6, 3)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.CheckpointInterval = time.Nanosecond
	boom := errors.New("disk on fire")
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/e/index.m3u8")}, nil, nil, func(Checkpoint) error { return boom })
	if !errors.Is(err, boom) || asm.callCount() != 0 {
		t.Fatalf("err = %v, assembled %d times", err, asm.callCount())
	}
}

func TestCredentialsStayWithTheManifestOrigin(t *testing.T) {
	a, b := newServer(t), newServer(t)
	segA, segB := tsSegment(1, 3), tsSegment(2, 4)
	a.add("/s/a.ts", segA, "video/mp2t")
	b.add("/s/b.ts", segB, "video/mp2t")
	a.add("/s/index.m3u8", []byte(mediaPlaylist("", []string{"a.ts", b.url("/s/b.ts")})), hlsType)
	h := http.Header{}
	h.Set("Cookie", "sid=secret")
	h.Set("Authorization", "Bearer secret")
	h.Set("Referer", "https://page.example/watch")
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: a.url("/s/index.m3u8"), Headers: h}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, append(append([]byte(nil), segA...), segB...)) {
		t.Fatal("output differs")
	}
	if c := a.seen("/s/a.ts").cookies; len(c) != 1 || c[0] != "sid=secret" {
		t.Fatalf("same-origin segment cookies = %q", c)
	}
	if c := b.seen("/s/b.ts").cookies; len(c) != 1 || c[0] != "" {
		t.Fatalf("cross-origin segment cookies = %q", c)
	}
	if r := b.seen("/s/b.ts").referers; len(r) != 1 || r[0] != "https://page.example/watch" {
		t.Fatalf("cross-origin segment referer = %q", r)
	}
}

func TestProbeRefusals(t *testing.T) {
	s := newServer(t)
	s.add("/live.m3u8", []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\na.ts\n"), hlsType)
	s.add("/key.m3u8", []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:4,\na.ts\n#EXT-X-ENDLIST\n"), hlsType)
	s.add("/page.html", []byte("<!DOCTYPE html><html><body>sign in</body></html>"), "text/html")
	s.add("/drm.mpd", []byte(strings.Replace(dashMPD, `<AdaptationSet mimeType="video/mp4" contentType="video">`,
		`<AdaptationSet mimeType="video/mp4" contentType="video"><ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>`, 1)), "application/dash+xml")
	s.add("/base.mpd", []byte(`<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
<Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1" width="2" height="2" codecs="avc1.64001f">
<BaseURL>v.mp4</BaseURL><SegmentBase indexRange="800-1000"><Initialization range="0-799"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`), "application/dash+xml")
	s.add("/ac3.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="en",URI="a.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=640x360,CODECS="avc1.4d401e,ac-3",AUDIO="aud"
v.m3u8
`), hlsType)
	addTSPlaylist(s, "/", 2, 1) // index.m3u8 for v.m3u8 and a.m3u8 below
	s.add("/v.m3u8", []byte(mediaPlaylist("", []string{"seg0.ts"})), hlsType)
	s.add("/a.m3u8", []byte(mediaPlaylist("", []string{"seg1.ts"})), hlsType)
	s.add("/master.m3u8", []byte(masterTS), hlsType)
	addTSPlaylist(s, "/low/", 2, 2)
	addTSPlaylist(s, "/high/", 2, 3)

	for _, tc := range []struct {
		path string
		sel  Selection
		want []error
	}{
		{"/live.m3u8", Selection{}, []error{media.ErrLive}},
		{"/key.m3u8", Selection{}, []error{media.ErrEncrypted}},
		{"/page.html", Selection{}, []error{ErrNotManifest}},
		{"/drm.mpd", Selection{}, []error{media.ErrEncrypted}},
		{"/base.mpd", Selection{}, []error{media.ErrUnsupported}},
		{"/ac3.m3u8", Selection{}, []error{media.ErrUnsupported}},
		{"/missing.m3u8", Selection{}, nil},
		{"/master.m3u8", Selection{VideoTrack: "v9"}, []error{ErrInvalidSelection, media.ErrNotFound}},
		{"/master.m3u8", Selection{AudioTrack: "v0"}, []error{ErrInvalidSelection}},
	} {
		sel := tc.sel
		sel.ManifestURL = s.url(tc.path)
		_, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
		if err == nil {
			t.Errorf("%s %+v: no error", tc.path, tc.sel)
			continue
		}
		for _, w := range tc.want {
			if !errors.Is(err, w) {
				t.Errorf("%s %+v: err = %v, want %v", tc.path, tc.sel, err, w)
			}
		}
		if tc.path == "/missing.m3u8" {
			var se *StatusError
			if !errors.As(err, &se) || se.Code != http.StatusNotFound {
				t.Errorf("missing manifest: err = %v", err)
			}
		}
		if strings.Contains(err.Error(), s.ts.URL) {
			t.Errorf("error repeats the URL: %v", err)
		}
	}
	if _, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: "file:///etc/passwd"}, nil); !errors.Is(err, ErrInvalidSelection) {
		t.Errorf("file URL: err = %v", err)
	}
	// Network errors do not repeat the URL either.
	gone := newServer(t)
	u := gone.url("/token=secret/index.m3u8")
	gone.ts.Close()
	if _, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: u}, nil); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("unreachable server: err = %v", err)
	}
}

func TestOutputIsNeverReplaced(t *testing.T) {
	s := newServer(t)
	addTSPlaylist(s, "/o/", 2, 5)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	if err := os.WriteFile(opts.OutPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rec recorder
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/o/index.m3u8")}, nil, nil, rec.onCheckpoint)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, opts.OutPath); string(got) != "keep" {
		t.Fatal("an existing file was replaced")
	}
	// The fragments are kept, so a retry with a free name only assembles.
	opts.OutPath += ".2"
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/o/index.m3u8")}, rec.last(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.hitCount("/o/seg0.ts") != 1 || s.hitCount("/o/seg1.ts") != 1 {
		t.Fatal("fragments were fetched again")
	}
}

func TestLimiterThrottles(t *testing.T) {
	s := newServer(t)
	var segs []string
	var want []byte
	for i := range 8 {
		data := tsSegment(uint64(500+i), 88) // 16,544 bytes
		s.add(fmt.Sprintf("/l/seg%d.ts", i), data, "video/mp2t")
		segs = append(segs, fmt.Sprintf("seg%d.ts", i))
		want = append(want, data...)
	}
	s.add("/l/index.m3u8", []byte(mediaPlaylist("", segs)), hlsType)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.Limiter = limiter.New(256 << 10) // starts with an empty bucket
	start := time.Now()
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/l/index.m3u8")}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 132 KiB at 256 KiB/s takes about half a second.
	if el := time.Since(start); el < 350*time.Millisecond {
		t.Fatalf("finished after %v: the limiter was not applied", el)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output differs")
	}
}

// TestRunHostRules applies Options.HostRules to a manifest job: the host's connection
// cap bounds the parallel fragment requests and its shared limiter sees every byte.
func TestRunHostRules(t *testing.T) {
	s := newServer(t)
	init, segs := addFMP4Playlist(s, "/h/", 8, 31)
	s.setDelay(15 * time.Millisecond)
	opts := testOptions(t, &fakeAssembler{})
	opts.MaxConnections = 6
	host := strings.TrimPrefix(s.ts.URL, "http://")
	lim := &countLimiter{}
	opts.HostRules = &testRules{conns: map[string]int{host: 1}, lims: map[string]limiter.Limiter{host: lim}}
	var log liveLog
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/h/index.m3u8")}, nil, log.on, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, append(init, segs...)) {
		t.Fatal("output differs")
	}
	s.mu.Lock()
	peak := s.maxActive
	s.mu.Unlock()
	if peak != 1 {
		t.Errorf("%d parallel requests to a host capped at 1", peak)
	}
	if got, want := lim.n.Load(), int64(len(init)+len(segs)); got != want {
		t.Errorf("the host's limiter saw %d bytes, want %d", got, want)
	}
	events, _ := log.snapshot()
	for _, p := range events {
		if len(p.Workers) > 1 {
			t.Fatalf("%d workers for a host capped at 1", len(p.Workers))
		}
	}
}

// TestFetchIntoRequestCap splits a byte range into requests of at most maxReq bytes.
func TestFetchIntoRequestCap(t *testing.T) {
	s := newServer(t)
	data := tsSegment(77, 6) // 1,128 bytes
	s.add("/c.ts", data, "video/mp2t")
	f := testFactory(t).NewSlot()
	defer f.Close()
	var buf bytes.Buffer
	_, n, err := fetchInto(testContext(t), f, fetchSpec{url: s.url("/c.ts"), rng: &[2]int64{10, 909}, maxReq: 300}, &buf, newCopier(nil, nil), nil)
	if err != nil || n != 900 || !bytes.Equal(buf.Bytes(), data[10:910]) {
		t.Fatalf("fetchInto = %d bytes, %v", n, err)
	}
	want := []string{"bytes=10-309", "bytes=310-609", "bytes=610-909"}
	if got := s.seen("/c.ts").ranges; !slices.Equal(got, want) {
		t.Fatalf("ranges %v, want %v", got, want)
	}
}

// TestShortRanges serves at most 100 bytes per range response: the job asks for the
// rest until each fragment is complete.
func TestShortRanges(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/q/", 3, 17)
	for i := range 3 {
		s.setChunk(fmt.Sprintf("/q/seg%d.ts", i), 100)
	}
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/q/index.m3u8")}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, want %d", len(got), len(want))
	}
	size := len(tsSegment(17000, 3)) // segment 0 has 3 packets
	if got, want := s.hitCount("/q/seg0.ts"), (size+99)/100; got != want {
		t.Fatalf("segment 0 took %d requests, want %d", got, want)
	}
}

func TestDirName(t *testing.T) {
	for _, tc := range []struct {
		id   string
		kind media.TrackKind
		want string
	}{
		{"v0", media.KindMuxed, "muxed-v0"},
		{"a_128-k", media.KindAudio, "audio-a_128-k"},
	} {
		if got := dirName(tc.kind, tc.id); got != tc.want {
			t.Errorf("dirName(%q) = %q", tc.id, got)
		}
	}
	for _, id := range []string{"../x", "con.", "a/b", "", "é", strings.Repeat("x", 65)} {
		got := dirName(media.KindVideo, id)
		if !strings.HasPrefix(got, "video-h") || len(got) != len("video-h")+16 {
			t.Errorf("dirName(%q) = %q", id, got)
		}
	}
}
