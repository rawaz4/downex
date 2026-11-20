package remux

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/bits"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/av1"
	"github.com/Eyevinn/mp4ff/mp4"
)

// bitWriter writes the RBSP of H.264 parameter sets (ISO/IEC 14496-10 §7.3.2).
type bitWriter struct {
	buf []byte
	n   uint
}

func (w *bitWriter) bit(b uint64) {
	if w.n%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b&1 != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.n % 8)
	}
	w.n++
}

func (w *bitWriter) bits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an unsigned Exp-Golomb code (§9.1).
func (w *bitWriter) ue(v uint64) {
	n := bits.Len64(v + 1)
	w.bits(0, n-1)
	w.bits(v+1, n)
}

func (w *bitWriter) nal(header byte) []byte {
	w.bit(1) // rbsp_stop_one_bit
	for w.n%8 != 0 {
		w.bit(0)
	}
	out := []byte{header}
	zeros := 0
	for _, b := range w.buf { // emulation prevention (§7.4.1)
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// testSPS is a Main profile sequence parameter set for 640x352 frames.
func testSPS() []byte {
	var w bitWriter
	w.bits(77, 8) // profile_idc
	w.bits(0, 8)  // constraint flags
	w.bits(30, 8) // level_idc
	w.ue(0)       // seq_parameter_set_id
	w.ue(0)       // log2_max_frame_num_minus4
	w.ue(0)       // pic_order_cnt_type
	w.ue(2)       // log2_max_pic_order_cnt_lsb_minus4
	w.ue(2)       // max_num_ref_frames
	w.bit(0)      // gaps_in_frame_num_value_allowed_flag
	w.ue(39)      // pic_width_in_mbs_minus1
	w.ue(21)      // pic_height_in_map_units_minus1
	w.bit(1)      // frame_mbs_only_flag
	w.bit(1)      // direct_8x8_inference_flag
	w.bit(0)      // frame_cropping_flag
	w.bit(0)      // vui_parameters_present_flag
	return w.nal(0x67)
}

func testPPS() []byte {
	var w bitWriter
	w.ue(0)      // pic_parameter_set_id
	w.ue(0)      // seq_parameter_set_id
	w.bit(0)     // entropy_coding_mode_flag
	w.bit(0)     // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)      // num_slice_groups_minus1
	w.ue(0)      // num_ref_idx_l0_default_active_minus1
	w.ue(0)      // num_ref_idx_l1_default_active_minus1
	w.bit(0)     // weighted_pred_flag
	w.bits(0, 2) // weighted_bipred_idc
	w.ue(0)      // pic_init_qp_minus26 (se 0)
	w.ue(0)      // pic_init_qs_minus26 (se 0)
	w.ue(0)      // chroma_qp_index_offset (se 0)
	w.bit(1)     // deblocking_filter_control_present_flag
	w.bit(0)     // constrained_intra_pred_flag
	w.bit(0)     // redundant_pic_cnt_present_flag
	return w.nal(0x68)
}

type fxSample struct {
	dur  uint32
	cto  int32
	sync bool
	data []byte
}

// fxTrack describes a synthetic fMP4 track: an initialization segment and media
// fragments generated with mp4ff.
type fxTrack struct {
	video     bool
	timescale uint32
	trackID   uint32   // source track ID (default 1)
	base      uint64   // tfdt of the first fragment
	times     []uint64 // tfdt of each fragment, overriding base
	perFrag   int      // samples per fragment
	elst      int64    // media_time of an edit list in the init; 0 = none
	optimize  bool     // let mp4ff move common sample values into tfhd defaults
	// codec is the video sample entry: "" (avc1), "av01" or "vp09".
	codec   string
	samples []fxSample
}

func (fx *fxTrack) id() uint32 {
	if fx.trackID == 0 {
		return 1
	}
	return fx.trackID
}

// videoSamples returns n H.264-like samples of duration d: a sync sample every
// 12 samples and an I P B B composition pattern.
func videoSamples(n int, d uint32, seed int64) []fxSample {
	rng := rand.New(rand.NewSource(seed))
	out := make([]fxSample, n)
	for i := range out {
		pos := i % 12
		s := fxSample{dur: d}
		switch {
		case pos == 0:
			s.sync, s.cto = true, int32(d)
		case (pos-1)%3 == 0:
			s.cto = 3 * int32(d)
		}
		size := 200 + rng.Intn(800)
		if s.sync {
			size += 3000
		}
		s.data = make([]byte, size)
		rng.Read(s.data)
		out[i] = s
	}
	return out
}

// audioSamples returns n AAC-like samples of 1024 ticks, all sync.
func audioSamples(n int, seed int64) []fxSample {
	rng := rand.New(rand.NewSource(seed))
	out := make([]fxSample, n)
	for i := range out {
		out[i] = fxSample{dur: 1024, sync: true, data: make([]byte, 100+rng.Intn(300))}
		rng.Read(out[i].data)
	}
	return out
}

func (fx *fxTrack) initSegment(t testing.TB) *mp4.InitSegment {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	kind := "audio"
	if fx.video {
		kind = "video"
	}
	trak := init.AddEmptyTrack(fx.timescale, kind, "und")
	if fx.video && fx.codec != "" {
		// An AV1 or VP9 sample entry with its configuration box (ISO/IEC 14496-12 §12.1.3).
		var conf mp4.Box = &mp4.VppCBox{Version: 1, Level: 31, BitDepth: 8, ChromaSubsampling: 1}
		if fx.codec == "av01" {
			conf = &mp4.Av1CBox{CodecConfRec: av1.CodecConfRec{Version: 1, SeqLevelIdx0: 8, ChromaSubsamplingX: 1, ChromaSubsamplingY: 1}}
		}
		stsd := trak.Mdia.Minf.Stbl.Stsd
		stsd.Children, stsd.SampleCount = nil, 0
		stsd.AddChild(mp4.CreateVisualSampleEntryBox(fx.codec, 1280, 720, conf))
	} else if fx.video {
		if err := trak.SetAVCDescriptor("avc1", [][]byte{testSPS()}, [][]byte{testPPS()}, true); err != nil {
			t.Fatal(err)
		}
	} else if err := trak.SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	trak.Tkhd.TrackID = fx.id()
	init.Moov.Mvex.Trex.TrackID = fx.id()
	if fx.elst != 0 {
		edts := &mp4.EdtsBox{}
		edts.AddChild(&mp4.ElstBox{Entries: []mp4.ElstEntry{{SegmentDuration: 0, MediaTime: fx.elst, MediaRateInteger: 1}}})
		trak.AddChild(edts)
	}
	return init
}

func encodeBox(t testing.TB, b interface{ Encode(w io.Writer) error }) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := b.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sampleFlags(s fxSample) uint32 {
	if s.sync {
		return mp4.SyncSampleFlags
	}
	return mp4.NonSyncSampleFlags | mp4.SampleDependsOn1
}

func (fx *fxTrack) fragmentBytes(t testing.TB) [][]byte {
	t.Helper()
	var out [][]byte
	dt := fx.base
	for i, seq := 0, 0; i < len(fx.samples); i, seq = i+fx.perFrag, seq+1 {
		if fx.times != nil {
			dt = fx.times[seq]
		}
		frag, err := mp4.CreateFragment(uint32(seq+1), fx.id())
		if err != nil {
			t.Fatal(err)
		}
		if fx.optimize {
			frag.EncOptimize = mp4.OptimizeTrun
		}
		for _, s := range fx.samples[i:min(i+fx.perFrag, len(fx.samples))] {
			frag.AddFullSample(mp4.FullSample{
				Sample:     mp4.Sample{Flags: sampleFlags(s), Dur: s.dur, Size: uint32(len(s.data)), CompositionTimeOffset: s.cto},
				DecodeTime: dt,
				Data:       s.data,
			})
			dt += uint64(s.dur)
		}
		out = append(out, encodeBox(t, frag))
	}
	return out
}

func writeFile(t testing.TB, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeInput writes the track's files into dir and returns its Input.
func (fx *fxTrack) writeInput(t testing.TB, dir, name string) Input {
	t.Helper()
	kind := KindAudio
	if fx.video {
		kind = KindVideo
	}
	in := Input{Kind: kind, Container: ContainerFMP4}
	in.Init = writeFile(t, dir, name+"-init.mp4", encodeBox(t, fx.initSegment(t)))
	for i, f := range fx.fragmentBytes(t) {
		in.Fragments = append(in.Fragments, writeFile(t, dir, name+"-"+itoa(i)+".m4s", f))
	}
	return in
}

func itoa(i int) string { return string(rune('a'+i/26)) + string(rune('a'+i%26)) }

// expTrack is what an output track must contain.
type expTrack struct {
	handler   string
	timescale uint32
	samples   []fxSample
	durs      []uint32 // expected durations; nil = those of samples
	shift     int32    // expected addition to every composition offset
	elst      []mp4.ElstEntry
	stsd      []byte // encoded sample description box of the source
}

func expect(t testing.TB, fx *fxTrack) expTrack {
	h := "soun"
	if fx.video {
		h = "vide"
	}
	init := fx.initSegment(t)
	return expTrack{handler: h, timescale: fx.timescale, samples: fx.samples, stsd: encodeBox(t, init.Moov.Trak.Mdia.Minf.Stbl.Stsd)}
}

// checkProgressive decodes a progressive MP4 with mp4ff and compares it with exp.
func checkProgressive(t *testing.T, path string, exp []expTrack) *mp4.File {
	t.Helper()
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if f.IsFragmented() {
		t.Fatal("output is fragmented")
	}
	var types []string
	for _, c := range f.Children {
		types = append(types, c.Type())
	}
	if !slices.Equal(types, []string{"ftyp", "moov", "mdat"}) {
		t.Fatalf("top-level boxes = %v, want ftyp moov mdat", types)
	}
	if got := len(f.Moov.Traks); got != len(exp) {
		t.Fatalf("%d tracks, want %d", got, len(exp))
	}
	if f.Moov.Mvhd.NextTrackID != uint32(len(exp)+1) {
		t.Errorf("next_track_ID = %d", f.Moov.Mvhd.NextTrackID)
	}
	var maxDur uint64
	for i, e := range exp {
		trak := f.Moov.Traks[i]
		if trak.Tkhd.TrackID != uint32(i+1) {
			t.Errorf("track %d: ID %d", i, trak.Tkhd.TrackID)
		}
		if trak.Tkhd.Flags&3 != 3 {
			t.Errorf("track %d: tkhd flags %#x", i, trak.Tkhd.Flags)
		}
		if h := trak.Mdia.Hdlr.HandlerType; h != e.handler {
			t.Errorf("track %d: handler %q, want %q", i, h, e.handler)
		}
		if ts := trak.Mdia.Mdhd.Timescale; ts != e.timescale {
			t.Errorf("track %d: timescale %d, want %d", i, ts, e.timescale)
		}
		stbl := trak.Mdia.Minf.Stbl
		n := stbl.Stsz.GetNrSamples()
		if int(n) != len(e.samples) {
			t.Fatalf("track %d: %d samples, want %d", i, n, len(e.samples))
		}
		if e.stsd != nil {
			if got := encodeBox(t, stbl.Stsd); !bytes.Equal(got, e.stsd) {
				t.Errorf("track %d: sample description differs from the source", i)
			}
		}
		wantDurs := e.durs
		if wantDurs == nil {
			for _, s := range e.samples {
				wantDurs = append(wantDurs, s.dur)
			}
		}
		durs, err := stbl.Stts.SampleDurations(n)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(durs, wantDurs) {
			t.Errorf("track %d: durations differ\n got %v\nwant %v", i, durs, wantDurs)
		}
		var total uint64
		for _, d := range wantDurs {
			total += uint64(d)
		}
		if trak.Mdia.Mdhd.Duration != total {
			t.Errorf("track %d: mdhd duration %d, want %d", i, trak.Mdia.Mdhd.Duration, total)
		}
		maxDur = max(maxDur, trak.Tkhd.Duration)

		var wantCTO []int32
		anyCTO := false
		for _, s := range e.samples {
			wantCTO = append(wantCTO, s.cto+e.shift)
			anyCTO = anyCTO || s.cto+e.shift != 0
		}
		switch {
		case anyCTO && stbl.Ctts == nil:
			t.Errorf("track %d: no ctts", i)
		case anyCTO:
			if stbl.Ctts.Version != 0 {
				t.Errorf("track %d: ctts version %d", i, stbl.Ctts.Version)
			}
			got, err := stbl.Ctts.CompositionTimeOffsets(n)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, wantCTO) {
				t.Errorf("track %d: composition offsets differ\n got %v\nwant %v", i, got, wantCTO)
			}
		case stbl.Ctts != nil:
			t.Errorf("track %d: unexpected ctts", i)
		}

		var wantSync []uint32
		allSync := true
		for j, s := range e.samples {
			if s.sync {
				wantSync = append(wantSync, uint32(j+1))
			} else {
				allSync = false
			}
		}
		switch {
		case allSync && stbl.Stss != nil:
			t.Errorf("track %d: stss although every sample is sync", i)
		case !allSync && stbl.Stss == nil:
			t.Errorf("track %d: no stss", i)
		case !allSync && !slices.Equal(stbl.Stss.SampleNumber, wantSync):
			t.Errorf("track %d: sync samples %v, want %v", i, stbl.Stss.SampleNumber, wantSync)
		}

		for j, s := range e.samples {
			if got := stbl.Stsz.GetSampleSize(j + 1); got != uint32(len(s.data)) {
				t.Fatalf("track %d sample %d: size %d, want %d", i, j+1, got, len(s.data))
			}
		}
		data, err := f.ReadSampleData(trak, 1, n, fd, nil)
		if err != nil {
			t.Fatal(err)
		}
		var want []byte
		for _, s := range e.samples {
			want = append(want, s.data...)
		}
		if !bytes.Equal(data, want) {
			t.Errorf("track %d: sample data differs", i)
		}

		var elst []mp4.ElstEntry
		if trak.Edts != nil {
			for _, b := range trak.Edts.Elst {
				elst = append(elst, b.Entries...)
			}
		}
		if !slices.Equal(elst, e.elst) {
			t.Errorf("track %d: edit list %+v, want %+v", i, elst, e.elst)
		}
	}
	if f.Moov.Mvhd.Duration != maxDur {
		t.Errorf("movie duration %d, want %d", f.Moov.Mvhd.Duration, maxDur)
	}
	return f
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func be64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// rawBox builds a box from parts.
func rawBox(typ string, parts ...[]byte) []byte {
	body := slices.Concat(parts...)
	return slices.Concat(be32(uint32(8+len(body))), []byte(typ), body)
}

func fullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	return rawBox(typ, append([][]byte{be32(uint32(version)<<24 | flags)}, parts...)...)
}
