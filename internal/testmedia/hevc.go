package testmedia

import "math/rand"

// HEVC NAL unit types (ISO/IEC 23008-2 Table 7-1).
const (
	HEVCTrailN    = 0
	HEVCTrailR    = 1
	HEVCRASLN     = 8
	HEVCRASLR     = 9
	HEVCBLAWLP    = 16
	HEVCIDRWRADL  = 19
	HEVCIDRNLP    = 20
	HEVCCRA       = 21
	HEVCVPSType   = 32
	HEVCSPSType   = 33
	HEVCPPSType   = 34
	HEVCAUD       = 35
	HEVCEOS       = 36
	HEVCEOB       = 37
	HEVCFD        = 38
	HEVCPrefixSEI = 39
)

// hevcHeader is a two-byte NAL unit header with nuh_layer_id 0 and
// nuh_temporal_id_plus1 1.
func hevcHeader(typ byte) []byte { return []byte{typ << 1, 1} }

// profileTierLevel writes profile_tier_level(1, 0) for the Main profile at level
// 3.1 (ISO/IEC 23008-2 §7.3.3).
func profileTierLevel(w *BitWriter) {
	w.Bits(0, 2)           // general_profile_space
	w.Bit(0)               // general_tier_flag
	w.Bits(1, 5)           // general_profile_idc: Main
	w.Bits(0x60000000, 32) // general_profile_compatibility_flag[1], [2]
	w.Bit(1)               // general_progressive_source_flag
	w.Bit(0)               // general_interlaced_source_flag
	w.Bit(0)               // general_non_packed_constraint_flag
	w.Bit(1)               // general_frame_only_constraint_flag
	w.Bits(0, 43)          // general_reserved_zero_43bits
	w.Bit(0)               // general_reserved_zero_bit
	w.Bits(93, 8)          // general_level_idc
}

// HEVCVPS returns a video parameter set NAL unit (vps_video_parameter_set_id 0).
func HEVCVPS() []byte {
	var w BitWriter
	w.Bits(0, 4)       // vps_video_parameter_set_id
	w.Bit(1)           // vps_base_layer_internal_flag
	w.Bit(1)           // vps_base_layer_available_flag
	w.Bits(0, 6)       // vps_max_layers_minus1
	w.Bits(0, 3)       // vps_max_sub_layers_minus1
	w.Bit(1)           // vps_temporal_id_nesting_flag
	w.Bits(0xffff, 16) // vps_reserved_0xffff_16bits
	profileTierLevel(&w)
	w.Bit(1)     // vps_sub_layer_ordering_info_present_flag
	w.UE(4)      // vps_max_dec_pic_buffering_minus1
	w.UE(2)      // vps_max_num_reorder_pics
	w.UE(0)      // vps_max_latency_increase_plus1
	w.Bits(0, 6) // vps_max_layer_id
	w.UE(0)      // vps_num_layer_sets_minus1
	w.Bit(0)     // vps_timing_info_present_flag
	w.Bit(0)     // vps_extension_flag
	return NAL(hevcHeader(HEVCVPSType), w.RBSP())
}

// HEVCSPS returns a Main profile sequence parameter set NAL unit for pictures of
// width × height luma samples (multiples of 8 are coded as is; other sizes use the
// conformance window).
func HEVCSPS(id, width, height int) []byte {
	cw, ch := (width+7)/8*8, (height+7)/8*8
	var w BitWriter
	w.Bits(0, 4) // sps_video_parameter_set_id
	w.Bits(0, 3) // sps_max_sub_layers_minus1
	w.Bit(1)     // sps_temporal_id_nesting_flag
	profileTierLevel(&w)
	w.UE(uint64(id)) // sps_seq_parameter_set_id
	w.UE(1)          // chroma_format_idc: 4:2:0
	w.UE(uint64(cw)) // pic_width_in_luma_samples
	w.UE(uint64(ch)) // pic_height_in_luma_samples
	if cw != width || ch != height {
		w.Bit(1) // conformance_window_flag (units of 2 samples)
		w.UE(0)
		w.UE(uint64((cw - width) / 2))
		w.UE(0)
		w.UE(uint64((ch - height) / 2))
	} else {
		w.Bit(0)
	}
	w.UE(0)  // bit_depth_luma_minus8
	w.UE(0)  // bit_depth_chroma_minus8
	w.UE(4)  // log2_max_pic_order_cnt_lsb_minus4
	w.Bit(1) // sps_sub_layer_ordering_info_present_flag
	w.UE(4)  // sps_max_dec_pic_buffering_minus1
	w.UE(2)  // sps_max_num_reorder_pics
	w.UE(0)  // sps_max_latency_increase_plus1
	w.UE(0)  // log2_min_luma_coding_block_size_minus3
	w.UE(3)  // log2_diff_max_min_luma_coding_block_size
	w.UE(0)  // log2_min_luma_transform_block_size_minus2
	w.UE(3)  // log2_diff_max_min_luma_transform_block_size
	w.UE(0)  // max_transform_hierarchy_depth_inter
	w.UE(0)  // max_transform_hierarchy_depth_intra
	w.Bit(0) // scaling_list_enabled_flag
	w.Bit(0) // amp_enabled_flag
	w.Bit(0) // sample_adaptive_offset_enabled_flag
	w.Bit(0) // pcm_enabled_flag
	w.UE(0)  // num_short_term_ref_pic_sets
	w.Bit(0) // long_term_ref_pics_present_flag
	w.Bit(0) // sps_temporal_mvp_enabled_flag
	w.Bit(0) // strong_intra_smoothing_enabled_flag
	w.Bit(0) // vui_parameters_present_flag
	w.Bit(0) // sps_extension_present_flag
	return NAL(hevcHeader(HEVCSPSType), w.RBSP())
}

// HEVCPPS returns a picture parameter set NAL unit with the given ids.
func HEVCPPS(id, spsID int) []byte {
	var w BitWriter
	w.UE(uint64(id))    // pps_pic_parameter_set_id
	w.UE(uint64(spsID)) // pps_seq_parameter_set_id
	w.Bit(0)            // dependent_slice_segments_enabled_flag
	w.Bit(0)            // output_flag_present_flag
	w.Bits(0, 3)        // num_extra_slice_header_bits
	w.Bit(0)            // sign_data_hiding_enabled_flag
	w.Bit(0)            // cabac_init_present_flag
	w.UE(0)             // num_ref_idx_l0_default_active_minus1
	w.UE(0)             // num_ref_idx_l1_default_active_minus1
	w.SE(0)             // init_qp_minus26
	w.Bit(0)            // constrained_intra_pred_flag
	w.Bit(0)            // transform_skip_enabled_flag
	w.Bit(0)            // cu_qp_delta_enabled_flag
	w.SE(0)             // pps_cb_qp_offset
	w.SE(0)             // pps_cr_qp_offset
	w.Bit(0)            // pps_slice_chroma_qp_offsets_present_flag
	w.Bit(0)            // weighted_pred_flag
	w.Bit(0)            // weighted_bipred_flag
	w.Bit(0)            // transquant_bypass_enabled_flag
	w.Bit(0)            // tiles_enabled_flag
	w.Bit(0)            // entropy_coding_sync_enabled_flag
	w.Bit(0)            // pps_loop_filter_across_slices_enabled_flag
	w.Bit(0)            // deblocking_filter_control_present_flag
	w.Bit(0)            // pps_scaling_list_data_present_flag
	w.Bit(0)            // lists_modification_present_flag
	w.UE(0)             // log2_parallel_merge_level_minus2
	w.Bit(0)            // slice_segment_header_extension_present_flag
	w.Bit(0)            // pps_extension_present_flag
	return NAL(hevcHeader(HEVCPPSType), w.RBSP())
}

// HEVCAUDUnit is an access unit delimiter (pic_type 2: any slice type).
func HEVCAUDUnit() []byte { return []byte{HEVCAUD << 1, 1, 0x50} }

// HEVCSlice returns a slice segment NAL unit of the given type with a random
// payload of about size bytes and no start code in it.
func HEVCSlice(rng *rand.Rand, typ byte, first bool, size int) []byte {
	return NAL(hevcHeader(typ), slicePayload(rng, first, size))
}
