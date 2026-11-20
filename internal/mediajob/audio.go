package mediajob

import (
	"context"
	"fmt"
	"time"

	"downex/internal/media"
	"downex/internal/transport"
)

// Audio downloads of web pages without a video (docs/SITE-MEDIA-SPEC.md §3.13): one
// sound track, saved as an MP4 with one sound track (.m4a) or as the MP3 it is.

// containerMP3 is the container RunRanged returns for a copied MP3.
const containerMP3 = "mp3"

// mp3HeadBytes is what the check of an MP3's head reads: an ID3v2 header is 10 bytes, an
// MPEG audio frame header 4.
const mp3HeadBytes = 10

// errAudioPart refuses a part of an audio download (docs/SITE-MEDIA-SPEC.md §3.13).
var errAudioPart = fmt.Errorf("%w: parts can be saved of videos only", ErrInvalidSelection)

// checkMP3Head accepts the head of an MP3 file (at most mp3HeadBytes): an ID3v2 tag
// header ("ID3", major version 2 to 4, a revision below 0xFF and a syncsafe size), or
// an MPEG audio frame header (11 sync bits, no reserved version or layer, a bit rate
// index other than 15 and a sampling rate index other than 3). Anything else, such as
// a web page served in its place, is media.ErrUnsupported.
func checkMP3Head(b []byte) error {
	if len(b) >= 10 && string(b[:3]) == "ID3" && b[3] >= 2 && b[3] <= 4 && b[4] != 0xff &&
		(b[6]|b[7]|b[8]|b[9])&0x80 == 0 {
		return nil
	}
	if len(b) >= 4 && b[0] == 0xff && b[1]&0xe0 == 0xe0 && (b[1]>>3)&3 != 1 && (b[1]>>1)&3 != 0 &&
		b[2]>>4 != 0xf && (b[2]>>2)&3 != 3 {
		return nil
	}
	return fmt.Errorf("%w: the file is not an MP3 file", media.ErrUnsupported)
}

// checkNoVideo refuses the 'moov' (or initialization segment) of an audio download's
// track id when it has a video track: an audio download never saves a picture.
func checkNoVideo(id string, moov []byte) error {
	video, err := media.InitHasVideo(moov)
	if err != nil {
		return fmt.Errorf("mediajob: track %q: %w", id, err)
	}
	if video {
		return fmt.Errorf("mediajob: track %q: %w: the audio file carries a video track", id, media.ErrUnsupported)
	}
	return nil
}

// audioOnly makes the selection of an audio download (Options.AudioOnly) its one track's
// sound: exactly one track, which is not video only, downloaded as an audio track (a
// media playlist's track has no kind; as audio, a transport stream is converted with its
// audio stream only).
func audioOnly(res *resolved) error {
	if len(res.tracks) != 1 || res.tracks[0].kind == media.KindVideo {
		return fmt.Errorf("%w: an audio download is one track with sound", ErrInvalidSelection)
	}
	res.tracks[0].kind = media.KindAudio
	return nil
}

// probeAudio finishes ProbeAudio once the offered tracks are planned: it lists the
// planned tracks that are not video only (offer's order), resolves the selection among
// them (pickAudio) and plans it as the one track of the download.
func probeAudio(ctx context.Context, tf transport.Factory, hp headerPolicy, sel Selection, format string,
	offered []media.Track, planned map[string]candidate, fallback time.Duration, describe bool) (*Info, error) {
	var (
		tracks []media.Track
		reason error
	)
	byID := make(map[string]*track)
	for _, t := range offered {
		c := planned[t.ID]
		if reason == nil {
			reason = c.err
		}
		if c.plan == nil || t.Kind != media.KindAudio && t.Kind != media.KindMuxed {
			continue
		}
		tracks = append(tracks, t)
		byID[t.ID] = c.track
	}
	if len(tracks) == 0 {
		if reason != nil {
			return nil, reason
		}
		return nil, ErrNoMP4Audio
	}
	chosen, err := pickAudio(tracks, sel)
	if err != nil {
		return nil, err
	}
	res := &resolved{info: Info{Tracks: tracks}, tracks: []*track{byID[chosen.ID]}, fallbackDuration: fallback}
	if err := res.finish(format, false); err != nil {
		return nil, err
	}
	if describe && format == "hls" {
		describeTracks(ctx, tf, hp, &res.info, byID)
	}
	return &res.info, nil
}

// pickAudio resolves the selection of an audio download (docs/SITE-MEDIA-SPEC.md §3.13)
// among tracks: the one track sel names, as its video or as its audio (a media
// playlist's track has no kind; an MPD's or a master's audio is an audio track), found
// as pick finds a chosen track; else the default pair's audio, or its track when it has
// no audio (a muxed one). A video-only track is refused. The result is a copy.
func pickAudio(tracks []media.Track, sel Selection) (*media.Track, error) {
	var (
		t   *media.Track
		err error
	)
	switch {
	case sel.VideoTrack != "" || sel.VideoKey != "":
		t, err = find(tracks, "audio", sel.VideoTrack, sel.VideoKey)
	case sel.AudioTrack != "" || sel.AudioKey != "":
		t, err = find(tracks, "audio", sel.AudioTrack, sel.AudioKey)
	default:
		v, a := media.DefaultPair(tracks)
		if t = clone(a); t == nil {
			t = clone(v)
		}
	}
	switch {
	case err != nil:
		return nil, err
	case t == nil:
		return nil, fmt.Errorf("mediajob: %w: the manifest has no audio track", media.ErrNotFound)
	case t.Kind == media.KindVideo:
		return nil, fmt.Errorf("%w: track %q is not an audio track", ErrInvalidSelection, t.ID)
	}
	return t, nil
}
