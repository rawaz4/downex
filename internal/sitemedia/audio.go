package sitemedia

import (
	"cmp"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"

	"downex/internal/media"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.13, amendment 2026-11-18): a page without any
// video offers its audio as one file, AAC as an .m4a (joined or copied, never
// re-encoded) or a direct MP3 as the .mp3 it is.

// The groups of usable audio, in the order a tie of their best bit rates is broken.
const (
	groupDirectAAC = iota
	groupStreamedAAC
	groupMP3
)

// audioOffer is the offer of an audio page (Info.AudioOnly): of the usable audio groups
// (direct AAC, streamed AAC, direct MP3) the one whose best bit rate is the highest, a
// tie going to the earlier group, so an offer has one file type. The other usable
// groups follow it in that order (Offer.Next), for a caller whose probe of a stream
// fails. Direct groups list every format of the group as an audio track, best first;
// streamed AAC is the manifest of its best format, probed by the caller with
// mediajob.ProbeAudio and ManifestAudioTracks. A format marked as a preview is used
// only when the page has no whole track. A page with no usable audio is refused as
// Offers refuses a page without video, and then as media.ErrUnsupported naming a
// preview, Opus or Vorbis, or raw AAC when the page has those.
func audioOffer(info *Info, host string) (*Offer, error) {
	if off := audioGroups(info, host, false); off != nil {
		return off, nil
	}
	if audioGroups(info, host, true) != nil {
		// Only previews: the page's whole track is not public (§3.13).
		return nil, fmt.Errorf("%w: only a preview of this track is public, not the whole track", media.ErrUnsupported)
	}
	if err := noOfferError(info); err != nil {
		return nil, err
	}
	var opus, raw bool
	for i := range info.Formats {
		f := &info.Formats[i]
		switch {
		case f.Kind != media.KindAudio:
		case opusOrVorbis(f):
			opus = true
		case rawAAC(f):
			raw = true
		}
	}
	switch {
	case opus:
		return nil, fmt.Errorf("%w: the audio is Opus or Vorbis, which Downex cannot save without converting it",
			media.ErrUnsupported)
	case raw:
		return nil, fmt.Errorf("%w: the audio is a raw AAC stream, which Downex cannot save without converting it",
			media.ErrUnsupported)
	}
	return nil, fmt.Errorf("%w: no AAC or MP3 audio on this page", media.ErrUnsupported)
}

// audioGroups returns the offers of the usable audio groups of an audio page, the best
// first, each linked to the next (Offer.Next); nil when there is none. Formats marked
// as a preview are counted only with previews, and then only they.
func audioGroups(info *Info, host string, previews bool) *Offer {
	var aac, mp3 []*Format
	var streamed *Format
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.Kind != media.KindAudio || f.Preview != previews || opusOrVorbis(f) {
			continue
		}
		switch {
		case f.direct() && !allowedURL(f.URL, host), !f.direct() && !allowedURL(f.ManifestURL, host):
		case f.direct() && directAAC(f):
			aac = append(aac, f)
		case f.direct() && directMP3(f):
			mp3 = append(mp3, f)
		case !f.direct() && streamedAAC(f):
			if streamed == nil || audioRate(f) > audioRate(streamed) {
				streamed = f
			}
		}
	}
	// The groups in their tie order, then by their best rate: a later group goes first
	// only with a higher rate.
	type group struct {
		g    int
		rate float64
	}
	var groups []group
	for g, fs := range [][]*Format{groupDirectAAC: aac, groupStreamedAAC: {streamed}, groupMP3: mp3} {
		best := -1.0
		for _, f := range fs {
			if f != nil {
				best = max(best, audioRate(f))
			}
		}
		if best >= 0 {
			groups = append(groups, group{g, best})
		}
	}
	slices.SortStableFunc(groups, func(a, b group) int { return cmp.Compare(b.rate, a.rate) })
	var first, last *Offer
	for _, g := range groups {
		var off *Offer
		switch g.g {
		case groupDirectAAC:
			off = directAudioOffer(aac, ContainerM4A)
		case groupMP3:
			off = directAudioOffer(mp3, ContainerMP3)
		case groupStreamedAAC:
			off = streamedAudioOffer(streamed, host)
		}
		if first == nil {
			first = off
		} else {
			last.next = off
		}
		last = off
	}
	return first
}

// streamedAudioOffer is the offer of an audio page's streamed AAC: the manifest of its
// best format f.
func streamedAudioOffer(f *Format, host string) *Offer {
	off := &Offer{Format: FormatHLS, Container: ContainerM4A, Audio: true, ManifestURL: f.ManifestURL,
		Headers: f.Headers.Clone(), Expires: f.Expires, streamed: f}
	if f.Protocol == protoDASH {
		off.Format = FormatDASH
	}
	if p := f.PlaylistURL; p != "" && sameHost(p, f.ManifestURL) && allowedURL(p, host) {
		// The format's own media playlist: the one track of the format, whatever the
		// master lists. Its cookie is the manifest host's, the same.
		off.ManifestURL = p
	}
	return off
}

// Next is the offer of the next usable audio group of an audio page (docs/SITE-MEDIA-SPEC.md
// §3.13: by best bit rate, ties in the group order), for a caller whose probe of this
// offer's stream failed; nil after the last and for a video page's offer.
func (off *Offer) Next() *Offer { return off.next }

// DescribeAudio completes the probed tracks of an audio page's stream (an Audio offer of
// format hls or dash) with what yt-dlp said of the format the manifest was chosen from,
// when the manifest is one track that says nothing of itself (a media playlist: no
// codecs, no bandwidth): its codec, its bit rate, its note and the file type m4a. Any
// other list is returned as it is. tracks is not changed.
func (off *Offer) DescribeAudio(tracks []media.Track) []media.Track {
	f := off.streamed
	if f == nil || len(tracks) != 1 {
		return tracks
	}
	t := tracks[0]
	if t.Codecs == "" {
		t.Codecs = trackCodec(f.ACodec)
	}
	if t.Bandwidth == 0 {
		t.Bandwidth = int64(math.Round(audioRate(f) * 1000))
	}
	if t.Name == "" {
		t.Name = f.Note
	}
	if t.Ext == "" {
		t.Ext = ContainerM4A
	}
	return []media.Track{t}
}

// sameHost reports two URLs on the same host (case and a final dot ignored).
func sameHost(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && normalizeHost(ua.Host) == normalizeHost(ub.Host)
}

// directAudioOffer lists the direct audio formats of one group as the tracks of an
// offer saved as container, best first (compareAudio), at most MaxOffered.
func directAudioOffer(fs []*Format, container string) *Offer {
	slices.SortFunc(fs, compareAudio)
	if len(fs) > MaxOffered {
		fs = fs[:MaxOffered]
	}
	off := &Offer{Format: FormatHTTP, Container: container, Audio: true, Tracks: make([]media.Track, 0, len(fs))}
	for _, f := range fs {
		off.Tracks = append(off.Tracks, track(f, 0))
	}
	off.DefaultAudio = fs[0].ID
	return off
}

// directAAC reports a direct audio-only MP4 (m4a or mp4, fragmented or without a
// container) of AAC, AC-3 or E-AC-3, or of a codec yt-dlp does not name: its bytes
// decide.
func directAAC(f *Format) bool {
	mp4 := f.Ext == "m4a" || f.Ext == "mp4"
	return mp4 && (f.fragmented() || f.Container == "") && (mp4Audio(f.ACodec) || f.ACodec == "")
}

// directMP3 reports a direct MP3 file.
func directMP3(f *Format) bool {
	return f.Ext == "mp3" && (f.ACodec == "" || strings.EqualFold(f.ACodec, "mp3"))
}

// streamedAAC reports an HLS or DASH audio format of AAC, AC-3 or E-AC-3, or of an
// unnamed codec in an MP4 file type: its data decides.
func streamedAAC(f *Format) bool {
	return mp4Audio(f.ACodec) || f.ACodec == "" && (f.Ext == "m4a" || f.Ext == "mp4")
}

// rawAAC reports a direct file of raw AAC (ADTS, file type aac), which Downex cannot
// save without converting it (docs/SITE-MEDIA-SPEC.md §3.13).
func rawAAC(f *Format) bool { return f.direct() && f.Ext == "aac" }

// opusOrVorbis reports an audio format Downex cannot save without converting it: Opus
// or Vorbis by its codec, or an Opus, Ogg or WebM file.
func opusOrVorbis(f *Format) bool {
	c := strings.ToLower(f.ACodec)
	return strings.Contains(c, "opus") || strings.Contains(c, "vorbis") ||
		f.Ext == "opus" || f.Ext == "ogg" || f.Ext == "webm"
}

// noOfferError is the refusal of a page that offers nothing, for a reason other than
// its formats: DRM, or yt-dlp's availability saying it needs an account; nil otherwise.
func noOfferError(info *Info) error {
	switch {
	case info.Protected > 0 || info.HasDRM:
		return fmt.Errorf("%w: the video is DRM-protected", media.ErrEncrypted)
	case info.Availability == "needs_auth":
		return fmt.Errorf("%w: the video needs a signed-in account", ErrAuthRequired)
	case info.Availability == "premium_only", info.Availability == "subscriber_only":
		return fmt.Errorf("%w: the video is for members only", ErrAuthRequired)
	}
	return nil
}

// ManifestAudioTracks is the offer function of an audio page's streamed AAC
// (docs/SITE-MEDIA-SPEC.md §3.13; mediajob.ProbeMP4): of a probed manifest's tracks it
// keeps those that may carry the page's sound alone: audio and muxed tracks without a
// picture whose codecs are all MP4 audio ones (AAC, AC-3, E-AC-3) or unnamed (a media
// playlist is its own one track, of unknown content); the higher bandwidth first, ties in
// the manifest's order; at most MaxOffered. The download refuses data with a picture
// (mediajob.Options.AudioOnly). tracks is not changed.
func ManifestAudioTracks(tracks []media.Track) []media.Track {
	var out []media.Track
	for _, t := range tracks {
		if t.Kind != media.KindAudio && t.Kind != media.KindMuxed || t.Width > 0 || t.Height > 0 || !audioCodecs(t.Codecs) {
			continue
		}
		out = append(out, t)
	}
	slices.SortStableFunc(out, func(a, b media.Track) int { return cmp.Compare(b.Bandwidth, a.Bandwidth) })
	if len(out) > MaxOffered {
		out = out[:MaxOffered]
	}
	return out
}

// audioCodecs reports a codec list whose every named codec is an MP4 audio one.
func audioCodecs(codecs string) bool {
	for c := range strings.SplitSeq(codecs, ",") {
		if c = strings.TrimSpace(c); c != "" && !mp4Audio(c) {
			return false
		}
	}
	return true
}

// IsAudioContainer reports the container of an audio page's download (ContainerM4A or
// ContainerMP3, docs/SITE-MEDIA-SPEC.md §3.13).
func IsAudioContainer(c string) bool { return c == ContainerM4A || c == ContainerMP3 }

// selectAudio is Select of an audio offer: no video (a named one is refused as a track
// of the wrong kind, or as one the page does not offer), the audio that audio or
// audioKey names, else the default; a fragmented MP4 is joined, any other file copied.
func selectAudio(info *Info, off *Offer, video, audio, videoKey, audioKey string) (*Pick, error) {
	if _, err := find(off, video, videoKey, false); err != nil {
		return nil, err
	}
	a, err := find(off, audio, audioKey, true)
	if err != nil {
		return nil, err
	}
	if a == nil {
		if a = trackByID(off, off.DefaultAudio); a == nil {
			return nil, fmt.Errorf("%w: the page offers no audio", media.ErrNotFound)
		}
	}
	f := info.Format(a.ID)
	if f == nil || f.Key != a.Key {
		return nil, fmt.Errorf("%w: format %s is no longer offered", media.ErrNotFound, a.ID)
	}
	pick := &Pick{Container: off.Container, Output: OutputCopy, NotBefore: f.AvailableAt}
	if f.fragmented() {
		pick.Output = OutputRemux
	}
	pick.Tracks = []media.RangedTrack{rangedTrack(f)}
	return pick, nil
}
