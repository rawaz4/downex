package media

import (
	"strings"
	"testing"
)

func TestTSConvertible(t *testing.T) {
	for _, tc := range []struct {
		codecs string
		want   bool
	}{
		// Unknown: CheckTS decides after the first fragment.
		{"", true},
		{" ", true},
		{",", true},
		{" , ,", true},

		// H.264 and HEVC, any profile, with or without the suffix.
		{"avc1.64001f", true},
		{"avc3.4d401e", true},
		{"avc1", true},
		{"AVC1.640028", true},
		{"hvc1.1.6.L93.B0", true},
		{"hev1.2.4.L120.90", true},
		{"HVC1.1.6.L93.B0", true},

		// AAC carried as ADTS: Main, LC, SBR (HE-AAC), PS (HE-AAC v2).
		{"mp4a.40.2", true},
		{"mp4a.40.5", true},
		{"mp4a.40.29", true},
		{"mp4a.40.1", true},
		{"MP4A.40.2", true},
		{"mp4a.40.02", true}, // the object type is a decimal number
		{"mp4a.40.005", true},
		{"mp4a.40.029", true},

		// Lists: every codec must be convertible.
		{"avc1.64001f,mp4a.40.2", true},
		{" avc1.64001f , mp4a.40.5 ", true},
		{"hvc1.1.6.L93.B0,mp4a.40.29", true},
		{"avc1.64001f,,mp4a.40.2", true},
		{"avc1.64001f,ac-3", false},
		{"ec-3,avc1.64001f", false},

		// Subtitle and caption formats a variant may also name are not converted
		// tracks: they are skipped.
		{"avc1.64001f,mp4a.40.2,wvtt", true},
		{"avc1.4d401e,mp4a.40.2,stpp.ttml.im1t", true},
		{"wvtt,avc1.64001f,c608", true},
		{"avc1.64001f,ac-3,wvtt", false},

		// Other AAC object types and other MPEG-4 audio.
		{"mp4a.40.3", false},
		{"mp4a.40.4", false},
		{"mp4a.40.23", false},
		{"mp4a.40.34", false}, // MPEG-1/2 Layer 3 through the MPEG-4 OTI
		{"mp4a.40.", false},
		{"mp4a.40", false},
		{"mp4a", false},
		{"mp4a.40.2x", false},
		{"mp4a.40.2.1", false},
		{"mp4a.40.-2", false},
		{"mp4a.40.0", false},
		{"mp4a.40.00", false},
		{"mp4a.40.+2", false},
		{"mp4a.40.0002", false}, // at most three digits
		{"mp4a.69", false},      // MPEG-2 audio (MP3)
		{"mp4a.6B", false},      // MPEG-1 audio (MP3)
		{"mp4a.67", false},      // MPEG-2 AAC LC OTI: not what ADTS in HLS is declared as
		{"mp4a.41.2", false},    // not OTI 0x40

		// Codecs the remuxer does not convert.
		{"ac-3", false},
		{"ec-3", false},
		{"mp3", false},
		{"opus", false},
		{"avc2.64001f", false},
		{"avc4.64001f", false},
		{"dvh1.05.06", false},
		{"av01.0.08M.08", false},
		{"vp09.00.10.08", false},
		{"mp4v.20.9", false},
		{"avc", false},
		{"avc10.64001f", false},
		{"x", false},
	} {
		if got := TSConvertible(tc.codecs); got != tc.want {
			t.Errorf("TSConvertible(%q) = %v, want %v", tc.codecs, got, tc.want)
		}
	}
}

func TestTSConvertibleLongList(t *testing.T) {
	ok := strings.Repeat("avc1.64001f,mp4a.40.2,", 1000)
	if !TSConvertible(ok) {
		t.Error("a long list of convertible codecs is not convertible")
	}
	if TSConvertible(ok + "ac-3") {
		t.Error("a long list ending in AC-3 is convertible")
	}
	if TSConvertible("mp4a.40." + strings.Repeat("0", 1000) + "2") {
		t.Error("an object type with 1000 leading zeros is convertible")
	}
}
