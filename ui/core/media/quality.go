package media

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
)

// The naming and one-per-class rule of lib/quality.ts over downex.v1 tracks. The names and
// classes are internal/media's (QualityClass, ClassName, ParseName), the one rule in Go;
// lib/quality.ts only repeats it, held to Go's tables by internal/media's quality_sync_test.

// count is a declared quantity, 0 when it is not a positive number.
func count(n float64) float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0
	}
	return n
}

// TrackQuality is a video track's quality: its class (0 unknown), display name ("" unknown) and
// HDR.
type TrackQuality struct {
	Class int
	Name  string
	HDR   bool
}

// QualityOf is a video track's quality as every UI shows it (internal/media TrackName): downexd's
// name when it is a standard one (kept as it is, even where the picture alone would say
// otherwise: downexd may follow the site's own label), unless it is an older downexd's raw height
// (a class that is the picture's height while the picture is of another class: "480p" for
// 1138×480); else the standard name of its picture (HDR when its old name said so); else its
// name as it is.
func QualityOf(t *downexv1.MediaTrack) TrackQuality {
	name := jsTrim(t.GetName())
	width, height := int(count(float64(t.GetWidth()))), int(count(float64(t.GetHeight())))
	cls := imedia.QualityClass(width, height)
	if std, _, hdr, ok := imedia.ParseName(name); ok && (std != height || std == cls) {
		return TrackQuality{Class: std, Name: name, HDR: hdr}
	}
	hdr := hasHDRWord(name)
	if cls > 0 {
		return TrackQuality{Class: cls, Name: imedia.ClassName(cls, count(t.GetFrameRate()), hdr), HDR: hdr}
	}
	return TrackQuality{Name: name, HDR: hdr}
}

// hasHDRWord is /\bHDR\b/i: "HDR" as a word, case ignored.
func hasHDRWord(s string) bool {
	for i := 0; i+3 <= len(s); i++ {
		if !strings.EqualFold(s[i:i+3], "HDR") {
			continue
		}
		if (i == 0 || !wordByte(s[i-1])) && (i+3 == len(s) || !wordByte(s[i+3])) {
			return true
		}
	}
	return false
}

func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// PictureText is "3840×1634", the secondary detail beside a quality's name; "" when unknown.
func PictureText(t *downexv1.MediaTrack) string {
	if count(float64(t.GetWidth())) > 0 && count(float64(t.GetHeight())) > 0 {
		return strconv.Itoa(int(t.GetWidth())) + "×" + strconv.Itoa(int(t.GetHeight()))
	}
	return ""
}

// codecRank: H.264 plays everywhere, then HEVC, then AV1; other codecs after them.
func codecRank(codecs string) int {
	first, _, _ := strings.Cut(codecs, ",")
	family, _, _ := strings.Cut(jsTrim(first), ".")
	switch jsLower(family) {
	case "avc1", "avc3":
		return 0
	case "hvc1", "hev1":
		return 1
	case "av01":
		return 2
	}
	return 3
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CompareInClass orders two tracks of one class, the one to keep first, as downexd does:
// video only before muxed (a muxed format is dropped when a video-only one of its class exists),
// SDR before HDR, the higher frame rate (rounded: 59.94 and 60 are equal), H.264 before HEVC
// before AV1, the higher bitrate, the larger picture, the larger size. 0 keeps the given order
// (downexd's).
func CompareInClass(a, b *downexv1.MediaTrack) int {
	if c := boolInt(a.GetKind() != "video") - boolInt(b.GetKind() != "video"); c != 0 {
		return c
	}
	if c := boolInt(QualityOf(a).HDR) - boolInt(QualityOf(b).HDR); c != 0 {
		return c
	}
	if c := cmp.Compare(jsRound(count(b.GetFrameRate())), jsRound(count(a.GetFrameRate()))); c != 0 {
		return c
	}
	if c := codecRank(a.GetCodecs()) - codecRank(b.GetCodecs()); c != 0 {
		return c
	}
	if c := cmp.Compare(count(float64(b.GetBandwidth())), count(float64(a.GetBandwidth()))); c != 0 {
		return c
	}
	area := func(t *downexv1.MediaTrack) float64 {
		return count(float64(t.GetWidth())) * count(float64(t.GetHeight()))
	}
	if c := cmp.Compare(area(b), area(a)); c != 0 {
		return c
	}
	return cmp.Compare(count(float64(b.GetSize())), count(float64(a.GetSize())))
}

// OnePerClass keeps one video track per quality class, best class first: of each class the
// first by CompareInClass, except that keep (downexd's own pick) is the one kept of its class.
// Tracks of unknown class follow, all of them, in their order. Audio tracks are left out.
func OnePerClass(tracks []*downexv1.MediaTrack, keep string) []*downexv1.MediaTrack {
	best := map[int]*downexv1.MediaTrack{}
	var classes []int
	var unknown []*downexv1.MediaTrack
	for _, t := range tracks {
		if !IsVideoTrack(t) {
			continue
		}
		cls := QualityOf(t).Class
		if cls == 0 {
			unknown = append(unknown, t)
			continue
		}
		kept, ok := best[cls]
		if !ok {
			classes = append(classes, cls)
		}
		if !ok || kept.GetId() != keep && (t.GetId() == keep || CompareInClass(t, kept) < 0) {
			best[cls] = t
		}
	}
	slices.SortFunc(classes, func(a, b int) int { return b - a })
	out := make([]*downexv1.MediaTrack, 0, len(classes)+len(unknown))
	for _, c := range classes {
		out = append(out, best[c])
	}
	return append(out, unknown...)
}
