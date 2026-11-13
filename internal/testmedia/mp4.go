package testmedia

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// ProgressiveMP4 returns a progressive MP4 file (ftyp, moov, mdat; ISO/IEC
// 14496-12/14) with a video track of aus (H.264 as avc1, HEVC as hvc1, with the
// parameter sets of v) and, when frames is not empty, an AAC track of a. Each
// track's samples form one chunk; video sample durations are the DTS
// differences, the last one repeating the one before.
func ProgressiveMP4(v VideoConfig, aus []AccessUnit, a AudioConfig, frames []AudioFrame) ([]byte, error) {
	v.defaults()
	a.defaults()
	if len(aus) == 0 {
		return nil, fmt.Errorf("testmedia: no video")
	}
	moov := mp4.NewMoovBox()
	mvhd := mp4.CreateMvhd()
	mvhd.Timescale = 1000
	moov.AddChild(mvhd)

	vt := mp4.CreateEmptyTrak(1, 90000, "video", "und")
	ps := v.ParamSets()
	var err error
	if v.Codec == HEVC {
		err = vt.SetHEVCDescriptor("hvc1", ps[:1], ps[1:2], ps[2:], nil, true)
	} else {
		err = vt.SetAVCDescriptor("avc1", ps[:1], ps[1:], true)
	}
	if err != nil {
		return nil, err
	}
	var vdata []byte
	var vsizes, vdurs []uint32
	var ctos []int32
	var syncs []uint32
	for i, au := range aus {
		vdata = append(vdata, au.Sample...)
		vsizes = append(vsizes, uint32(len(au.Sample)))
		d := uint32(v.FrameTicks)
		if i+1 < len(aus) {
			d = uint32(aus[i+1].DTS - au.DTS)
		}
		vdurs = append(vdurs, d)
		ctos = append(ctos, int32(au.PTS-au.DTS))
		if au.Sync {
			syncs = append(syncs, uint32(i+1))
		}
	}
	vdur := uint64(0)
	for _, d := range vdurs {
		vdur += uint64(d)
	}
	tracks := []*progTrack{{trak: vt, data: vdata, sizes: vsizes, durs: vdurs, ctos: ctos, syncs: syncs, dur: vdur, timescale: 90000}}

	if len(frames) > 0 {
		at := mp4.CreateEmptyTrak(2, uint32(a.SampleRate), "audio", "und")
		esds := mp4.CreateEsdsBox(AudioSpecificConfig(a))
		ch := a.Channels
		if ch == 7 {
			ch = 8
		}
		at.Mdia.Minf.Stbl.Stsd.AddChild(mp4.CreateAudioSampleEntryBox("mp4a", uint16(ch), 16, uint16(a.SampleRate), esds))
		var adata []byte
		var asizes, adurs []uint32
		for _, f := range frames {
			adata = append(adata, f.Raw...)
			asizes = append(asizes, uint32(len(f.Raw)))
			adurs = append(adurs, 1024)
		}
		tracks = append(tracks, &progTrack{trak: at, data: adata, sizes: asizes, durs: adurs, dur: uint64(len(frames)) * 1024, timescale: uint32(a.SampleRate)})
	}
	mvhd.NextTrackID = uint32(len(tracks) + 1)
	for _, t := range tracks {
		t.fill()
		ms := t.dur * 1000 / uint64(t.timescale)
		t.trak.Tkhd.Duration = ms
		mvhd.Duration = max(mvhd.Duration, ms)
		moov.AddChild(t.trak)
	}

	ftyp := mp4.NewFtyp("isom", 0x200, []string{"isom", "iso2", "avc1", "mp41"})
	offset := ftyp.Size() + moov.Size() + 8
	for _, t := range tracks {
		t.stco.ChunkOffset[0] = uint32(offset)
		offset += uint64(len(t.data))
	}
	var out bytes.Buffer
	if err := ftyp.Encode(&out); err != nil {
		return nil, err
	}
	if err := moov.Encode(&out); err != nil {
		return nil, err
	}
	payload := 0
	for _, t := range tracks {
		payload += len(t.data)
	}
	out.Write([]byte{byte((payload + 8) >> 24), byte((payload + 8) >> 16), byte((payload + 8) >> 8), byte(payload + 8), 'm', 'd', 'a', 't'})
	for _, t := range tracks {
		out.Write(t.data)
	}
	return out.Bytes(), nil
}

type progTrack struct {
	trak      *mp4.TrakBox
	data      []byte
	sizes     []uint32
	durs      []uint32
	ctos      []int32
	syncs     []uint32
	dur       uint64
	timescale uint32
	stco      *mp4.StcoBox
}

// fill replaces the empty sample tables of the track with complete ones (one
// chunk).
func (t *progTrack) fill() {
	stbl := t.trak.Mdia.Minf.Stbl
	stsd := stbl.Stsd
	nb := mp4.NewStblBox()
	nb.AddChild(stsd)
	stts := &mp4.SttsBox{}
	for i := 0; i < len(t.durs); {
		j := i + 1
		for j < len(t.durs) && t.durs[j] == t.durs[i] {
			j++
		}
		stts.SampleCount = append(stts.SampleCount, uint32(j-i))
		stts.SampleTimeDelta = append(stts.SampleTimeDelta, t.durs[i])
		i = j
	}
	nb.AddChild(stts)
	if len(t.ctos) > 0 {
		ctts := &mp4.CttsBox{}
		var counts []uint32
		var offs []int32
		for i := 0; i < len(t.ctos); {
			j := i + 1
			for j < len(t.ctos) && t.ctos[j] == t.ctos[i] {
				j++
			}
			counts, offs = append(counts, uint32(j-i)), append(offs, t.ctos[i])
			i = j
		}
		_ = ctts.AddSampleCountsAndOffset(counts, offs)
		nb.AddChild(ctts)
	}
	stsc := &mp4.StscBox{}
	_ = stsc.AddEntry(1, uint32(len(t.sizes)), 1)
	nb.AddChild(stsc)
	nb.AddChild(&mp4.StszBox{SampleNumber: uint32(len(t.sizes)), SampleSize: t.sizes})
	t.stco = &mp4.StcoBox{ChunkOffset: []uint32{0}}
	nb.AddChild(t.stco)
	if t.syncs != nil && len(t.syncs) < len(t.sizes) {
		nb.AddChild(&mp4.StssBox{SampleNumber: t.syncs})
	}
	minf := t.trak.Mdia.Minf
	for i, c := range minf.Children {
		if c == stbl {
			minf.Children[i] = nb
		}
	}
	minf.Stbl = nb
	t.trak.Mdia.Mdhd.Duration = t.dur
}
