package testmedia

import (
	"fmt"
	"math"
	"strings"
)

// Segment is one entry of a media playlist.
type Segment struct {
	URI      string
	Duration float64 // seconds
	// Discontinuity writes EXT-X-DISCONTINUITY before the segment.
	Discontinuity bool
	// Map writes EXT-X-MAP with this URI before the segment.
	Map string
}

// MediaPlaylist returns a VOD media playlist (RFC 8216 §4.3) of segs, ended with
// EXT-X-ENDLIST.
func MediaPlaylist(segs []Segment) string {
	target := 1.0
	for _, s := range segs {
		target = max(target, math.Ceil(s.Duration))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", int(target))
	for _, s := range segs {
		if s.Discontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if s.Map != "" {
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", s.Map)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", s.Duration, s.URI)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// Variant is one EXT-X-STREAM-INF entry of a master playlist.
type Variant struct {
	URI        string
	Bandwidth  int
	Codecs     string // "" = no CODECS attribute
	Resolution string // "WxH"; "" = none
	Audio      string // audio group id; "" = none
}

// Rendition is one EXT-X-MEDIA audio entry of a master playlist.
type Rendition struct {
	Group, Name, Language, URI string
	Default                    bool
}

// MasterPlaylist returns a master playlist (RFC 8216 §4.3.4) of the audio
// renditions and variants.
func MasterPlaylist(variants []Variant, audio []Rendition) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	for _, r := range audio {
		def := "NO"
		if r.Default {
			def = "YES"
		}
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q", r.Group, r.Name)
		if r.Language != "" {
			fmt.Fprintf(&b, ",LANGUAGE=%q", r.Language)
		}
		fmt.Fprintf(&b, ",DEFAULT=%s,AUTOSELECT=YES,URI=%q\n", def, r.URI)
	}
	for _, v := range variants {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d", v.Bandwidth)
		if v.Codecs != "" {
			fmt.Fprintf(&b, ",CODECS=%q", v.Codecs)
		}
		if v.Resolution != "" {
			fmt.Fprintf(&b, ",RESOLUTION=%s", v.Resolution)
		}
		if v.Audio != "" {
			fmt.Fprintf(&b, ",AUDIO=%q", v.Audio)
		}
		fmt.Fprintf(&b, "\n%s\n", v.URI)
	}
	return b.String()
}

// SyncCut returns a Muxer.Segments cut function that starts a segment at every
// random access unit of pid at least every seconds (measured in decode time). The
// function keeps state: use a fresh one for each call of Segments or Durations.
func SyncCut(pid uint16, seconds float64) func(i int, p PES) bool {
	last := int64(math.MinInt64)
	return func(i int, p PES) bool {
		if p.PID != pid || !p.RandomAccess {
			return false
		}
		t := p.decodeTime()
		if last == math.MinInt64 {
			last = t // the first segment starts at the first unit anyway
			return false
		}
		if float64(t-last) >= seconds*90000 {
			last = t
			return true
		}
		return false
	}
}

// Durations returns the duration in seconds of each segment that cut makes of
// units (see Muxer.Segments): the decode time from its first unit to the next
// segment's, the last one ending at end (90 kHz ticks).
func Durations(units []PES, cut func(i int, p PES) bool, end int64) []float64 {
	var starts []int64
	for i, u := range units {
		if c := cut(i, u); i == 0 || c {
			starts = append(starts, u.decodeTime())
		}
	}
	out := make([]float64, len(starts))
	for i, s := range starts {
		next := end
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		out[i] = float64(next-s) / 90000
	}
	return out
}
