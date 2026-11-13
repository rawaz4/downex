//go:build perf

package perf

import (
	"fmt"

	"downex/internal/testmedia"
)

// hlsAsset is a generated VOD HLS presentation: one H.264 transport stream track cut
// into equal segments.
type hlsAsset struct {
	files    filesHandler
	segments int
	frames   int
	bytes    int64 // the segments' bytes
}

const (
	hlsVideoPID   = 0x100
	hlsFPS        = 30
	hlsSegSeconds = 2
	hlsFrameTicks = 90000 / hlsFPS
	hlsBatch      = 10 // segments generated at once (bounds the generator's memory)
)

// genHLS returns segments transport stream segments of about segBytes each under
// /hls/ (index.m3u8, seg-<n>.ts). The video is 640×360 H.264 at 30 fps with a sync
// picture every 12 frames, cut every 2 s; its slices are random bytes, so the remuxer
// does its full work on them.
func genHLS(segments, segBytes int) (*hlsAsset, error) {
	framesPerSeg := hlsFPS * hlsSegSeconds
	// A frame averages 1.5 slices of SliceBytes, and a sync picture's slices are 4×
	// larger (1 in 12): 1.875 × SliceBytes; the TS packets add 188/184 and PES headers.
	sliceBytes := int(float64(segBytes) * 184 / 188 / float64(framesPerSeg) / 1.875)
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: hlsVideoPID, Type: testmedia.TypeH264}}}
	a := &hlsAsset{files: filesHandler{}}
	var list []testmedia.Segment
	for b := 0; b < segments; b += hlsBatch {
		n := min(hlsBatch, segments-b)
		aus := testmedia.Video(testmedia.VideoConfig{
			Codec:      testmedia.H264,
			Frames:     n * framesPerSeg,
			StartDTS:   90000 + int64(b*framesPerSeg)*hlsFrameTicks,
			Seed:       int64(b + 1),
			SliceBytes: sliceBytes,
		})
		units := testmedia.VideoPES(hlsVideoPID, aus)
		segs := m.Segments(units, testmedia.SyncCut(hlsVideoPID, hlsSegSeconds))
		if len(segs) != n {
			return nil, fmt.Errorf("batch %d: %d segments, want %d", b, len(segs), n)
		}
		for _, seg := range segs {
			a.segments++
			uri := fmt.Sprintf("seg-%d.ts", a.segments)
			a.files["/hls/"+uri] = servedFile{data: seg, mime: "video/mp2t"}
			a.bytes += int64(len(seg))
			list = append(list, testmedia.Segment{URI: uri, Duration: hlsSegSeconds})
		}
		a.frames += len(aus)
	}
	a.files["/hls/index.m3u8"] = servedFile{data: []byte(testmedia.MediaPlaylist(list)), mime: "application/vnd.apple.mpegurl"}
	return a, nil
}
