package remux

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"downex/internal/media"
	"downex/internal/testmedia"
)

func TestCheckTS(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 24, BFrames: true, Seed: 30}, testmedia.AudioConfig{Frames: 38, Seed: 31})
	first := func(m *testmedia.Muxer, units []testmedia.PES) []byte { return segments(m, units, 0.4)[0] }
	muxed := first(avMuxer(false, "eng"), fx.units)
	hevcFx := newTSFixture(testmedia.VideoConfig{Codec: testmedia.HEVC, Frames: 12, Seed: 32}, testmedia.AudioConfig{Frames: 20, Seed: 33})
	withAudio := func(at byte, desc []byte) []byte {
		m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}, {PID: aPID, Type: at, Descriptors: desc}}}
		ac3 := testmedia.PES{PID: aPID, PTS: 0, DTS: -1, Data: bytes.Repeat([]byte{0x0b, 0x77}, 200), Bounded: true}
		return m.Segment(testmedia.Interleave(testmedia.VideoPES(vPID, fx.aus[:12]), []testmedia.PES{ac3}))
	}
	audioOnly := (&testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}).Segment(testmedia.AudioPES(aPID, fx.frames, 4))
	scrambled := func(pid uint16, pes bool) []byte {
		units := slices.Clone(fx.units)
		for i := range units {
			if units[i].PID == pid && i > 3 {
				units[i].TSScrambled, units[i].PESScrambled = !pes, pes
				break
			}
		}
		return avMuxer(false, "").Segment(units)
	}
	twoAUDs := testmedia.VideoPES(vPID, fx.aus[:12])
	twoAUDs[2].Data = slices.Concat(fx.aus[2].AnnexB, fx.aus[3].AnnexB)
	twoAUDsTS := (&testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}).Segment(twoAUDs)
	hiRate := (&testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}).Segment(
		testmedia.AudioPES(aPID, testmedia.AAC(testmedia.AudioConfig{Frames: 8, SampleRate: 88200}), 4))
	// An EXT-X-MAP section with the PAT and PMT, followed by a fragment without them.
	m := avMuxer(false, "")
	mapped := slices.Concat((&testmedia.Muxer{Streams: m.Streams}).PSI(), segments(m, fx.units, 0.4)[1][2*188:])

	for name, tc := range map[string]struct {
		prefix []byte
		kind   Kind
		want   error
	}{
		"muxed":                            {muxed, KindMuxed, nil},
		"muxed as video":                   {muxed, KindVideo, nil},
		"muxed as audio":                   {muxed, KindAudio, nil},
		"HEVC":                             {first(avMuxer(true, ""), hevcFx.units), KindMuxed, nil},
		"map section and fragment":         {mapped, KindMuxed, nil},
		"cut in the middle of a unit":      {muxed[:20*188+77], KindMuxed, nil},
		"audio only":                       {audioOnly, KindAudio, nil},
		"audio only as muxed":              {audioOnly, KindMuxed, ErrUnsupported},
		"audio only as video":              {audioOnly, KindVideo, ErrUnsupported},
		"video only as audio":              {(&testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}).Segment(testmedia.VideoPES(vPID, fx.aus)), KindAudio, ErrUnsupported},
		"AC-3 in muxed":                    {withAudio(testmedia.TypeAC3, nil), KindMuxed, ErrUnsupported},
		"AC-3 descriptor in muxed":         {withAudio(testmedia.TypePrivate, testmedia.AC3Descriptor()), KindMuxed, ErrUnsupported},
		"E-AC-3 ignored for a video track": {withAudio(testmedia.TypeEAC3, nil), KindVideo, nil},
		"AC-3 for an audio track":          {withAudio(testmedia.TypeAC3, nil), KindAudio, ErrUnsupported},
		"subtitles ignored":                {withAudio(testmedia.TypePrivate, []byte{0x59, 0}), KindMuxed, nil},
		"SAMPLE-AES AAC":                   {withAudio(testmedia.TypeSAESAAC, nil), KindMuxed, media.ErrEncrypted},
		"SAMPLE-AES AC-3":                  {withAudio(testmedia.TypeSAESAC3, nil), KindAudio, media.ErrEncrypted},
		"scrambled video packet":           {scrambled(vPID, false), KindMuxed, media.ErrEncrypted},
		"scrambled audio PES":              {scrambled(aPID, true), KindMuxed, media.ErrEncrypted},
		"scrambled audio of a video track": {scrambled(aPID, false), KindVideo, nil},
		"several access units in one PES":  {twoAUDsTS, KindVideo, ErrUnsupported},
		"88.2 kHz AAC":                     {hiRate, KindAudio, ErrUnsupported},
		"filler":                           {testmedia.Filler(3, 100), KindMuxed, ErrMalformed},
		"error page":                       {[]byte("<html><body>Not found</body></html>"), KindMuxed, ErrMalformed},
		"empty":                            {nil, KindVideo, ErrMalformed},
		"fragmented MP4":                   {encodeBox(t, (&fxTrack{video: true, timescale: 90000}).initSegment(t)), KindVideo, ErrMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckTS(tc.prefix, tc.kind)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("CheckTS = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("CheckTS = %v, want %v", err, tc.want)
			}
			t.Log(err)
		})
	}
	if err := CheckTS(muxed, "subtitles"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
}

// TestCheckTSReadsOneMiB: CheckTS looks at the first MaxCheckTSBytes only.
func TestCheckTSReadsOneMiB(t *testing.T) {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 240, SliceBytes: 8000, Seed: 34})
	units := testmedia.VideoPES(vPID, aus)
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	clean := m.Segment(units)
	if len(clean) < 2*MaxCheckTSBytes {
		t.Fatalf("fixture of %d bytes is too small", len(clean))
	}
	late := slices.Clone(units)
	late[len(late)-2].TSScrambled = true
	m = &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	if err := CheckTS(m.Segment(late), KindVideo); err != nil {
		t.Fatalf("a scrambled packet beyond the first MiB: %v", err)
	}
	early := slices.Clone(units)
	early[3].TSScrambled = true
	m = &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	if err := CheckTS(m.Segment(early), KindVideo); !errors.Is(err, media.ErrEncrypted) {
		t.Fatalf("a scrambled packet in the first MiB: %v", err)
	}
}
