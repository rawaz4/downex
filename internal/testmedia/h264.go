package testmedia

import (
	"math/rand"
)

// Codec is a video coding format.
type Codec int

// Video codecs.
const (
	H264 Codec = iota // ISO/IEC 14496-10
	HEVC              // ISO/IEC 23008-2
)

// H.264 NAL unit types (ISO/IEC 14496-10 Table 7-1).
const (
	H264Slice    = 1
	H264IDR      = 5
	H264SEI      = 6
	H264SPSType  = 7
	H264PPSType  = 8
	H264AUD      = 9
	H264EndSeq   = 10
	H264EndStrm  = 11
	H264Filler   = 12
	H264SPSExt   = 13
	h264NALRefHi = 0x60 // nal_ref_idc 3
)

// H264SPS returns a Main profile sequence parameter set NAL unit (header
// included) with the given seq_parameter_set_id for frames of width × height
// luma samples. Sizes that are not multiples of 16 use frame cropping.
func H264SPS(id, width, height int) []byte {
	mbW, mbH := (width+15)/16, (height+15)/16
	var w BitWriter
	w.Bits(77, 8) // profile_idc: Main
	w.Bits(0, 8)  // constraint flags
	w.Bits(31, 8) // level_idc
	w.UE(uint64(id))
	w.UE(0)  // log2_max_frame_num_minus4
	w.UE(0)  // pic_order_cnt_type
	w.UE(2)  // log2_max_pic_order_cnt_lsb_minus4
	w.UE(3)  // max_num_ref_frames
	w.Bit(0) // gaps_in_frame_num_value_allowed_flag
	w.UE(uint64(mbW - 1))
	w.UE(uint64(mbH - 1))
	w.Bit(1) // frame_mbs_only_flag
	w.Bit(1) // direct_8x8_inference_flag
	cropR, cropB := (mbW*16-width)/2, (mbH*16-height)/2
	if cropR > 0 || cropB > 0 {
		w.Bit(1) // frame_cropping_flag (4:2:0: units of 2 samples)
		w.UE(0)
		w.UE(uint64(cropR))
		w.UE(0)
		w.UE(uint64(cropB))
	} else {
		w.Bit(0)
	}
	w.Bit(0) // vui_parameters_present_flag
	return NAL([]byte{h264NALRefHi | H264SPSType}, w.RBSP())
}

// H264PPS returns a picture parameter set NAL unit with the given ids.
func H264PPS(id, spsID int) []byte {
	var w BitWriter
	w.UE(uint64(id))
	w.UE(uint64(spsID))
	w.Bit(0)     // entropy_coding_mode_flag
	w.Bit(0)     // bottom_field_pic_order_in_frame_present_flag
	w.UE(0)      // num_slice_groups_minus1
	w.UE(0)      // num_ref_idx_l0_default_active_minus1
	w.UE(0)      // num_ref_idx_l1_default_active_minus1
	w.Bit(0)     // weighted_pred_flag
	w.Bits(0, 2) // weighted_bipred_idc
	w.SE(0)      // pic_init_qp_minus26
	w.SE(0)      // pic_init_qs_minus26
	w.SE(0)      // chroma_qp_index_offset
	w.Bit(1)     // deblocking_filter_control_present_flag
	w.Bit(0)     // constrained_intra_pred_flag
	w.Bit(0)     // redundant_pic_cnt_present_flag
	return NAL([]byte{h264NALRefHi | H264PPSType}, w.RBSP())
}

// H264AUDUnit is an access unit delimiter (primary_pic_type 7: any slice type).
func H264AUDUnit() []byte { return []byte{H264AUD, 0xf0} }

// VideoConfig describes a generated video elementary stream.
type VideoConfig struct {
	Codec         Codec
	Width, Height int   // default 640×360
	Frames        int   // access units, leading ones included
	GOP           int   // frames per closed GOP, each starting with a sync picture (default 12)
	BFrames       bool  // two B pictures between anchors: PTS ≠ DTS
	FrameTicks    int64 // 90 kHz ticks per frame (default 3000: 30 fps)
	StartDTS      int64 // decode time of the first unit in 90 kHz ticks (unwrapped)
	Seed          int64
	SEI           bool // an SEI NAL unit in every access unit
	// LeadingNonSync access units come before the first sync picture (a stream
	// cut mid-GOP); a converter drops them.
	LeadingNonSync int
	// SPSID and PPSID are the parameter set ids (H.264: SPS 0-31, PPS 0-255; HEVC:
	// 0-15 and 0-63).
	SPSID, PPSID int
	// SliceBytes is the mean slice payload size (default 600).
	SliceBytes int
}

// AccessUnit is one generated picture.
type AccessUnit struct {
	DTS, PTS int64 // 90 kHz ticks, unwrapped
	Sync     bool  // IDR (H.264) or IDR_W_RADL (HEVC)
	Leading  bool  // one of LeadingNonSync
	// AnnexB is the PES payload: an access unit delimiter, the parameter sets
	// (sync pictures only), SEI and the slices, with start codes.
	AnnexB []byte
	// Sample is what an MP4 sample of this unit holds: the SEI and slice NAL units
	// (parameter sets and delimiters removed) with 4-byte lengths.
	Sample []byte
	// ParamSets are the parameter set NAL units this unit carries.
	ParamSets [][]byte
}

func (c *VideoConfig) defaults() {
	if c.Width == 0 {
		c.Width, c.Height = 640, 360
	}
	if c.GOP == 0 {
		c.GOP = 12
	}
	if c.FrameTicks == 0 {
		c.FrameTicks = 3000
	}
	if c.SliceBytes == 0 {
		c.SliceBytes = 600
	}
}

// ParamSets returns the parameter sets of the configuration in decoder order
// (HEVC: VPS, SPS, PPS; H.264: SPS, PPS).
func (c VideoConfig) ParamSets() [][]byte {
	c.defaults()
	if c.Codec == HEVC {
		return [][]byte{HEVCVPS(), HEVCSPS(c.SPSID, c.Width, c.Height), HEVCPPS(c.PPSID, c.SPSID)}
	}
	return [][]byte{H264SPS(c.SPSID, c.Width, c.Height), H264PPS(c.PPSID, c.SPSID)}
}

// pictureOrder returns, for a GOP of n frames, the display index of each
// picture in decode order and whether it is an anchor (I or P). With B frames,
// anchors are at display 0, 3, 6, … and n-1, each followed in decode order by the
// B pictures displayed before it.
func pictureOrder(n int, bframes bool) (display []int, anchor []bool) {
	if !bframes {
		for i := range n {
			display, anchor = append(display, i), append(anchor, true)
		}
		return display, anchor
	}
	prev := 0
	display, anchor = append(display, 0), append(anchor, true)
	for a := 3; prev < n-1; a += 3 {
		if a > n-1 {
			a = n - 1
		}
		display, anchor = append(display, a), append(anchor, true)
		for b := prev + 1; b < a; b++ {
			display, anchor = append(display, b), append(anchor, false)
		}
		prev = a
	}
	return display, anchor
}

// Video generates the access units of c in decode order. With BFrames, each
// picture's PTS is StartDTS + (display index + 1) × FrameTicks, so the first
// presented picture is shown one frame after it is decoded.
func Video(c VideoConfig) []AccessUnit {
	c.defaults()
	rng := rand.New(rand.NewSource(c.Seed))
	ps := c.ParamSets()
	var out []AccessUnit
	delay := int64(0)
	if c.BFrames {
		delay = c.FrameTicks
	}
	// Leading pictures: non-sync P pictures of a GOP whose start is missing.
	for i := range c.LeadingNonSync {
		dts := c.StartDTS + int64(i)*c.FrameTicks
		au := c.unit(rng, ps, false, true, i)
		au.DTS, au.PTS, au.Leading = dts, dts+delay, true
		out = append(out, au)
	}
	base := len(out)
	for g := 0; base+g*c.GOP < c.Frames; g++ {
		n := min(c.GOP, c.Frames-base-g*c.GOP)
		display, anchor := pictureOrder(n, c.BFrames)
		for i := range n {
			first := base + g*c.GOP
			dts := c.StartDTS + int64(first+i)*c.FrameTicks
			pts := c.StartDTS + int64(first+display[i])*c.FrameTicks + delay
			au := c.unit(rng, ps, i == 0, anchor[i], first+i)
			au.DTS, au.PTS = dts, pts
			out = append(out, au)
		}
	}
	return out
}

// unit builds one access unit.
func (c VideoConfig) unit(rng *rand.Rand, ps [][]byte, sync, ref bool, n int) AccessUnit {
	var nalus, sample [][]byte
	long := map[int]bool{}
	if c.Codec == HEVC {
		nalus = append(nalus, HEVCAUDUnit())
	} else {
		nalus = append(nalus, H264AUDUnit())
	}
	var carried [][]byte
	if sync {
		for _, p := range ps {
			long[len(nalus)] = true
			nalus = append(nalus, p)
		}
		carried = ps
	}
	if c.SEI {
		var sei []byte
		if c.Codec == HEVC {
			sei = NAL([]byte{HEVCPrefixSEI << 1, 1}, seiPayload(rng, n))
		} else {
			sei = NAL([]byte{H264SEI}, seiPayload(rng, n))
		}
		nalus, sample = append(nalus, sei), append(sample, sei)
	}
	slices := 1 + rng.Intn(2)
	for s := range slices {
		size := c.SliceBytes/2 + rng.Intn(c.SliceBytes)
		if sync {
			size *= 4
		}
		var slice []byte
		if c.Codec == HEVC {
			typ := byte(HEVCTrailN)
			switch {
			case sync:
				typ = HEVCIDRWRADL
			case ref:
				typ = HEVCTrailR
			}
			slice = HEVCSlice(rng, typ, s == 0, size)
		} else {
			slice = H264SliceUnit(rng, sync, ref, s == 0, size)
		}
		nalus, sample = append(nalus, slice), append(sample, slice)
	}
	return AccessUnit{
		Sync:      sync,
		AnnexB:    AnnexB(nalus, func(i int) bool { return long[i] }),
		Sample:    LengthPrefixed(sample),
		ParamSets: carried,
	}
}

// seiPayload is a user_data_unregistered SEI message (payloadType 5) of 16 + 8
// bytes, then rbsp_trailing_bits.
func seiPayload(rng *rand.Rand, n int) []byte {
	b := []byte{5, 24}
	uuid := make([]byte, 16)
	rng.Read(uuid)
	b = append(b, uuid...)
	b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 'd', 'x', 't', 'm')
	return append(b, 0x80)
}

// H264SliceUnit returns a coded slice NAL unit with a random payload of about size
// bytes: an IDR slice (type 5) for sync pictures, else a non-IDR slice (type 1),
// with nal_ref_idc 0 for non-reference pictures. The payload starts like a slice
// header (first_mb_in_slice 0 for the first slice) and contains no start code.
func H264SliceUnit(rng *rand.Rand, sync, ref, first bool, size int) []byte {
	typ := byte(H264Slice)
	if sync {
		typ = H264IDR
	}
	hdr := typ
	if ref || sync {
		hdr |= 0x40
	}
	return NAL([]byte{hdr}, slicePayload(rng, first, size))
}

func slicePayload(rng *rand.Rand, first bool, size int) []byte {
	p := make([]byte, max(size, 2))
	rng.Read(p)
	if first {
		p[0] |= 0x80 // first_mb_in_slice ue(v) 0 / first_slice_segment_in_pic_flag 1
	} else {
		p[0] &^= 0x80
		p[0] |= 0x40
	}
	if p[len(p)-1] == 0 {
		p[len(p)-1] = 0x80
	}
	return p
}
