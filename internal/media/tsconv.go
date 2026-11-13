package media

import "strings"

// TSConvertible reports whether every codec an RFC 6381 CODECS value names is one the
// TS→MP4 remuxer (internal/remux, docs/VIDEO-DETECT-SPEC.md §4) converts: avc1/avc3
// (H.264), hvc1/hev1 (HEVC), mp4a.40.2/mp4a.40.5/mp4a.40.29/mp4a.40.1 (AAC carried as
// ADTS; the object type is a decimal number of at most three digits, so 02 is 2).
// "" (unknown) is true: CheckTS decides after the first fragment (§4.8). Sample entry
// identifiers match without regard to case; empty list entries and the subtitle and
// caption formats a variant may also name (wvtt, stpp, c608, ...: not a converted
// track) are skipped.
func TSConvertible(codecs string) bool {
	for c := range codecList(codecs) {
		if codecKind(c) != KindSubtitles && !tsConvertibleCodec(c) {
			return false
		}
	}
	return true
}

// tsConvertibleCodec reports whether one RFC 6381 codec is in TSConvertible's list.
func tsConvertibleCodec(c string) bool {
	id, rest, _ := strings.Cut(c, ".")
	switch strings.ToLower(id) {
	case "avc1", "avc3", "hvc1", "hev1":
		return true
	case "mp4a":
		// mp4a.<OTI>.<audio object type>: OTI 0x40 is MPEG-4 audio (ISO/IEC 14496-3);
		// object types 1 (AAC Main), 2 (AAC LC), 5 (SBR) and 29 (PS).
		oti, aot, ok := strings.Cut(rest, ".")
		if !ok || oti != "40" || len(aot) > 3 {
			return false
		}
		switch strings.TrimLeft(aot, "0") {
		case "1", "2", "5", "29":
			return true
		}
	}
	return false
}
