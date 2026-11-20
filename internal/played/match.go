package played

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"downex/internal/media"
)

// File is a resource with what its head says.
type File struct {
	Resource
	Desc
	// Kind is media.KindMuxed, KindVideo or KindAudio (fileKind).
	Kind media.TrackKind
}

// Offer is the video a source matched: its files as tracks (videos best first, then the
// audio files by size), the default pick, and the files by track id.
type Offer struct {
	Tracks                     []media.Track
	DefaultVideo, DefaultAudio string
	Duration                   time.Duration
	Files                      map[string]File
	// Silent: the default video has no sound and no audio file was matched.
	Silent bool
}

// Output of a pick: OutputCopy saves one file as it is (a muxed file, or a video without
// sound); OutputRemux joins a video file and an audio file.
const (
	OutputCopy  = "copy"
	OutputRemux = "remux"
)

// tolerance is how far a video file's duration may be from the element's:
// max(0.3 s, 1.5 %); audio files max(0.5 s, 2 %) (docs/SITE-MEDIA-SPEC.md §2.9).
func tolerance(d time.Duration, audio bool) time.Duration {
	if audio {
		return max(500*time.Millisecond, d/50)
	}
	return max(300*time.Millisecond, d*15/1000)
}

func nearDur(a, b time.Duration, audio bool) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance(b, audio)
}

// fileKind is what a described file is: muxed (a video and an audio track, or a whole
// progressive file whose head was cut inside its 'moov'), video, audio; "" when the head
// shows no track and the hint does not help, or a fragmented head names no track.
func fileKind(r Resource, d Desc) media.TrackKind {
	switch {
	case d.Video && d.Audio:
		return media.KindMuxed
	case d.Video && !d.Complete && !d.Fragmented:
		// A progressive file cut inside its 'moov': the sound's track may follow.
		return media.KindMuxed
	case d.Audio && !d.Complete && !d.Fragmented && r.Kind == "video":
		// The same with its sound's track first (measured 2026-11-18: a 487 s file whose
		// 'moov' of 392 KiB listed the sound first); the server said video.
		return media.KindMuxed
	case d.Video:
		return media.KindVideo
	case d.Audio:
		return media.KindAudio
	case d.Fragmented:
		// A fragmented file whose head names no track is never taken for a video.
		return ""
	}
	switch r.Kind {
	case "video":
		return media.KindMuxed // a progressive file whose 'moov' follows its data
	case "audio":
		return media.KindAudio
	}
	return ""
}

// MinFileBytes: a known size below this is never a whole video file.
const MinFileBytes = 64 << 10

// partOfStream reports a file that cannot be a whole video: smaller than MinFileBytes; a
// head with no 'moov' that is fragmented or leads with 'moof', 'styp' or 'sidx' (a
// stream's media segment; measured 2026-11-18: a Bluesky HLS segment of 254 KB, 'moof'
// first, was offered as the video); or a fragmented head that is all of its file and
// holds no media (an initialization segment).
func partOfStream(f File) bool {
	if f.Size > 0 && f.Size < MinFileBytes {
		return true
	}
	if !f.Moov && (f.Fragmented || f.Lead == "moof" || f.Lead == "styp" || f.Lead == "sidx") {
		return true
	}
	return f.Fragmented && f.Size > 0 && f.Size <= int64(len(f.Head))
}

// Describe each resource of src; heads that are not MP4, and files on a refused host
// (ErrRefusedHost), are left out. An encrypted file whose duration matches the element
// (or is unknown) makes the source media.ErrEncrypted: Downex never saves protected
// content.
func describeAll(src Source) ([]File, error) {
	var files []File
	for _, r := range src.Resources {
		if CheckURL(r.URL) != nil {
			continue
		}
		d, err := Describe(r.Head)
		if err != nil {
			continue
		}
		f := File{Resource: r, Desc: d, Kind: fileKind(r, d)}
		if partOfStream(f) {
			// A stream's initialization or media segment (an init's 'moov' says the whole
			// duration) or a piece of an ad: never a whole video (measured 2026-11-18: an HLS
			// init segment of 1.7 KB matched a 63 s post).
			continue
		}
		if d.Encrypted && (src.Element.Duration == 0 || d.Duration == 0 || nearDur(d.Duration, src.Element.Duration, d.Audio && !d.Video)) {
			return nil, fmt.Errorf("%w: the video's files are protected", media.ErrEncrypted)
		}
		if f.Kind == "" || d.Encrypted {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// Matched is what Match found of the clicked video: its video (or muxed) files, Anchor
// among them, and the audio files of its sound, Audio among them.
type Matched struct {
	Videos, Audios []File
	// Anchor is the file the clicked element plays (by picture, then length). Pinned: its
	// picture is the element's own, so it is the default whatever else is listed.
	Anchor string
	Pinned bool
	// Audio is the sound paired with Anchor ("" = none).
	Audio string
}

// tieWindow: lengths this close count as equal when choosing among files.
const tieWindow = 50 * time.Millisecond

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// sameLength reports two files of one video: lengths within max(120 ms, 0.5 %) (the
// qualities of one video differ by a frame or two; measured 2026-11-18: a For You item of
// 5.92 s and its preloaded neighbour of 6.1 s).
func sameLength(a, b time.Duration) bool {
	return absDur(a-b) <= max(120*time.Millisecond, b/200)
}

// sameAspect reports two pictures of one shape (within 2 %), or one of them unknown.
func sameAspect(a, b File) bool {
	if a.Width == 0 || a.Height == 0 || b.Width == 0 || b.Height == 0 {
		return true
	}
	x, y := float64(a.Width)/float64(a.Height), float64(b.Width)/float64(b.Height)
	return math.Abs(x-y) <= 0.02*y
}

func isVideo(f File) bool { return f.Kind == media.KindVideo || f.Kind == media.KindMuxed }

// Match finds the files of the clicked video among src's (in the extension's order, the
// most recently played first; docs/SITE-MEDIA-SPEC.md §2.9 "The flow" 5):
//   - an element of unknown duration (not loaded, or live) matches nothing (ErrNotFound);
//   - the anchor is the video file the element plays: among the files whose length is the
//     element's (tolerance), those whose picture is the element's own first, then the
//     closest length, then the newest;
//   - its qualities are the video files of the anchor's own length (sameLength) and shape;
//   - the files another element of the frame claims (ownFiles) are never the anchor or
//     its qualities;
//   - its sound is the audio file closest to the anchor's own length (the audio
//     tolerance). Several sounds of one length are one sound's qualities when their sizes
//     differ (soundQualities): all listed, the largest the default; else two items of one
//     length: the sound fetched in the anchor's turn among the videos of that length, and
//     only the anchor and its sound are offered.
//
// With no file of the element's length, the newest video whose head names its track but
// not its length, and its sound. Nothing: ErrNotFound.
func Match(src Source) (Matched, error) {
	files, err := describeAll(src)
	if err != nil {
		return Matched{}, err
	}
	el := src.Element
	if el.Duration <= 0 {
		return Matched{}, fmt.Errorf("%w (the video has not loaded yet)", ErrNotFound)
	}
	mine := ownFiles(files, el)
	var near []File
	for _, f := range files {
		if isVideo(f) && f.Duration > 0 && nearDur(f.Duration, el.Duration, false) && mine[f.ID] {
			near = append(near, f)
		}
	}
	var m Matched
	var anchor File
	if len(near) > 0 {
		anchor = pickAnchor(near, el)
		for _, f := range files {
			// A file another player claims is never listed as a quality of this one.
			if isVideo(f) && f.Duration > 0 && mine[f.ID] && sameLength(f.Duration, anchor.Duration) && sameAspect(f, anchor) {
				m.Videos = append(m.Videos, f)
			}
		}
	} else {
		for _, f := range files {
			if isVideo(f) && f.Duration == 0 && f.VideoCodec != "" {
				anchor = f
				m.Videos = []File{f}
				break
			}
		}
		if len(m.Videos) == 0 {
			return Matched{}, fmt.Errorf("%w (%d files looked at)", ErrNotFound, len(src.Resources))
		}
	}
	m.Anchor = anchor.ID
	m.Pinned = el.Width > 0 && el.Height > 0 && anchor.Width == el.Width && anchor.Height == el.Height
	pairAudio(&m, files, anchor, el)
	return m, nil
}

// othersFile reports a file whose length is closer to another element of the frame than to
// the clicked one's (measured 2026-11-18, For You: the clicked item's element said 5.92 s and
// its file 5.77 s; the next item's element 6.24 s and its file 6.10 s, preloaded after it).
// An element of the clicked one's own length (a backdrop copy of it) never claims a file.
// It decides only for a file whose picture is unknown (ownFiles).
func othersFile(d time.Duration, el Element) bool {
	own := absDur(d - el.Duration)
	for _, o := range otherPlayers(el) {
		if absDur(d-o) < own {
			return true
		}
	}
	return false
}

// otherPlayers are the lengths of the frame's other elements that may claim files: never
// one of the clicked element's own length (a backdrop copy of it).
func otherPlayers(el Element) []time.Duration {
	var out []time.Duration
	for _, o := range el.Others {
		if absDur(o-el.Duration) > tieWindow {
			out = append(out, o)
		}
	}
	return out
}

// ownFiles reports, by id, the video files that are the clicked element's rather than
// another player's of the frame (docs/SITE-MEDIA-SPEC.md §2.9 "The flow" 5). Files of one
// picture (width x height) are distinct items (a player's qualities differ in picture), so
// within each picture the files and the players are paired as a whole, in order of length,
// one file per player: the most pairs within the tolerance, then the least total distance
// (a player's element can say a length about 0.15 s longer than its file's, so a file can
// lie nearer a neighbour's element than its own and still be its own's; review 2026-11-18).
// A file whose picture is unknown goes to the nearest element (othersFile). With no other
// player every file may be the clicked element's.
func ownFiles(files []File, el Element) map[string]bool {
	mine := make(map[string]bool, len(files))
	others := otherPlayers(el)
	groups := map[[2]int][]File{}
	for _, f := range files {
		switch {
		case !isVideo(f) || f.Duration <= 0:
		case len(others) == 0:
			mine[f.ID] = true
		case f.Width == 0 || f.Height == 0:
			mine[f.ID] = !othersFile(f.Duration, el)
		default:
			k := [2]int{f.Width, f.Height}
			groups[k] = append(groups[k], f)
		}
	}
	players := append([]time.Duration{el.Duration}, others...)
	for _, g := range groups {
		for _, id := range pairInOrder(g, players) {
			mine[id] = true
		}
	}
	return mine
}

// pairInOrder pairs files (one picture) with players (players[0] is the clicked element) in
// order of length, each file and player at most once, a pair only within the video
// tolerance: the most pairs, then the least total distance, then the newer file for the
// clicked element. It returns the ids of the files paired with players[0].
func pairInOrder(files []File, players []time.Duration) []string {
	type pl struct {
		d       time.Duration
		clicked bool
	}
	ps := make([]pl, len(players))
	for i, d := range players {
		ps[i] = pl{d, i == 0}
	}
	slices.SortStableFunc(ps, func(a, b pl) int { return cmp.Compare(a.d, b.d) })
	// files arrive newest first; a stable sort by length keeps the newer first on a tie.
	fs := slices.Clone(files)
	slices.SortStableFunc(fs, func(a, b File) int { return cmp.Compare(a.Duration, b.Duration) })
	type cell struct {
		pairs int
		cost  time.Duration
		ok    bool
	}
	better := func(a, b cell) bool {
		if !b.ok {
			return a.ok
		}
		return a.ok && (a.pairs > b.pairs || a.pairs == b.pairs && a.cost < b.cost)
	}
	n, k := len(fs), len(ps)
	dp := make([][]cell, n+1)
	for i := range dp {
		dp[i] = make([]cell, k+1)
	}
	dp[0][0] = cell{ok: true}
	for i := 0; i <= n; i++ {
		for j := 0; j <= k; j++ {
			c := dp[i][j]
			if !c.ok {
				continue
			}
			if i < n && better(c, dp[i+1][j]) {
				dp[i+1][j] = c
			}
			if j < k && better(c, dp[i][j+1]) {
				dp[i][j+1] = c
			}
			if i < n && j < k && nearDur(fs[i].Duration, ps[j].d, false) {
				next := cell{pairs: c.pairs + 1, cost: c.cost + absDur(fs[i].Duration-ps[j].d), ok: true}
				if better(next, dp[i+1][j+1]) {
					dp[i+1][j+1] = next
				}
			}
		}
	}
	// Walk back from the end to find the clicked player's file.
	var out []string
	for i, j := n, k; i > 0 && j > 0; {
		c := dp[i][j]
		switch {
		case nearDur(fs[i-1].Duration, ps[j-1].d, false) && dp[i-1][j-1].ok &&
			c == (cell{pairs: dp[i-1][j-1].pairs + 1, cost: dp[i-1][j-1].cost + absDur(fs[i-1].Duration-ps[j-1].d), ok: true}):
			if ps[j-1].clicked {
				out = append(out, fs[i-1].ID)
			}
			i, j = i-1, j-1
		case dp[i-1][j] == c:
			i--
		default:
			j--
		}
	}
	return out
}

// pickAnchor is the file among near (the files of the element's length) the element
// plays: those whose picture is the element's first, then the closest length, then the
// newest.
func pickAnchor(near []File, el Element) File {
	pool := near
	if el.Width > 0 && el.Height > 0 {
		var same []File
		for _, f := range near {
			if f.Width == el.Width && f.Height == el.Height {
				same = append(same, f)
			}
		}
		if len(same) > 0 {
			pool = same
		}
	}
	best := pool[0]
	for _, f := range pool[1:] {
		if absDur(f.Duration-el.Duration)+tieWindow < absDur(best.Duration-el.Duration) {
			best = f
		}
	}
	return best
}

// pairAudio sets m's sound: the audio files closest to the anchor's own length.
func pairAudio(m *Matched, files []File, anchor File, el Element) {
	length := cmp.Or(anchor.Duration, el.Duration)
	var auds []File
	for _, f := range files {
		if f.Kind == media.KindAudio && f.Duration > 0 && nearDur(f.Duration, length, true) &&
			!slices.ContainsFunc(auds, func(a File) bool { return sameFile(a, f) }) {
			auds = append(auds, f)
		}
	}
	if len(auds) == 0 {
		// A sound whose head does not say its length (no 'mehd' or 'sidx'): only when it is
		// the only such file of the tab.
		var unknown []File
		for _, f := range files {
			if f.Kind == media.KindAudio && f.Duration == 0 {
				unknown = append(unknown, f)
			}
		}
		if len(unknown) == 1 && el.HasAudio != -1 {
			m.Audios, m.Audio = unknown, unknown[0].ID
		}
		return
	}
	closest := absDur(auds[0].Duration - length)
	for _, f := range auds[1:] {
		closest = min(closest, absDur(f.Duration-length))
	}
	// The sounds of the closest length, in fetch order (auds keeps the order of files).
	var tied []File
	for _, f := range auds {
		if absDur(f.Duration-length) <= closest+tieWindow {
			tied = append(tied, f)
		}
	}
	if len(tied) > 1 && !soundQualities(tied, anchor, el) {
		// Sounds of one length are two items of one length (a length cap): the one fetched in
		// the anchor's turn among the videos of that length (each item's picture and sound are
		// fetched together), and the other item's picture or sound is never listed with it.
		rank := 0
		for _, f := range files {
			if f.ID == anchor.ID {
				break
			}
			if isVideo(f) && f.Duration > 0 && absDur(f.Duration-anchor.Duration) <= tieWindow {
				rank++
			}
		}
		chosen := tied[min(rank, len(tied)-1)]
		m.Videos = slices.DeleteFunc(m.Videos, func(f File) bool { return f.ID != anchor.ID })
		m.Audios, m.Audio = []File{chosen}, chosen.ID
		return
	}
	// One sound, or one sound's qualities: the largest of the closest length is the default.
	chosen := tied[0]
	for _, f := range tied[1:] {
		if f.Size > chosen.Size {
			chosen = f
		}
	}
	for _, f := range auds {
		if sameLength(f.Duration, chosen.Duration) {
			m.Audios = append(m.Audios, f)
		}
	}
	m.Audio = chosen.ID
}

// soundQualities reports sounds of one length that are the qualities of one item's sound,
// not two items' (review 2026-11-18: an adaptive player that fetched its sound at 128 and 64
// kbit/s): no other element of the frame has the anchor's length, and their sizes are known
// and each differs from the others by at least a quarter (two items' sounds at one bit rate
// are about one size, like 250 and 240 KB for two 15 s items).
func soundQualities(tied []File, anchor File, el Element) bool {
	length := cmp.Or(anchor.Duration, el.Duration)
	for _, o := range el.Others {
		if sameLength(o, el.Duration) || sameLength(o, length) {
			return false
		}
	}
	for i, a := range tied {
		if a.Size <= 0 {
			return false
		}
		for _, b := range tied[i+1:] {
			lo, hi := min(a.Size, b.Size), max(a.Size, b.Size)
			if hi*4 < lo*5 {
				return false
			}
		}
	}
	return true
}

// sameFile reports one sound under two addresses (a player that fetched it twice).
func sameFile(a, b File) bool { return a.Size > 0 && a.Size == b.Size && a.AudioCodec == b.AudioCodec }

// codecRank orders video codecs at one height: H.264 plays everywhere, then HEVC, AV1, VP9.
func codecRank(c string) int {
	switch c {
	case "avc1", "avc3":
		return 0
	case "hvc1", "hev1":
		return 1
	case "av01":
		return 2
	case "vp09":
		return 3
	}
	return 4
}

// joinable reports a sample entry the MP4 join and copy carry.
func joinable(c string, audio bool) bool {
	if audio {
		switch c {
		case "mp4a", "Opus", "ac-3", "ec-3", "":
			return true
		}
		return false
	}
	switch c {
	case "avc1", "avc3", "hvc1", "hev1", "av01", "vp09", "":
		return true
	}
	return false
}

// MakeOffer turns matched files into the offer: one video per height (the anchor at its
// own height when it is pinned, else a muxed file before a video-only one, then H.264,
// HEVC, AV1, VP9, then the larger), best first; the audio files by size. The default is
// the anchor when its picture is the element's own, else the best video; with the paired
// sound (else the largest) for a video-only default, none for a muxed file.
func MakeOffer(m Matched, el Element) (*Offer, error) {
	byHeight := map[int]File{}
	var heights []int
	var auds []File
	for _, f := range m.Audios {
		// The same sound under two addresses (a player that fetched it twice) is listed once.
		if joinable(f.AudioCodec, true) && !slices.ContainsFunc(auds, func(a File) bool { return sameFile(a, f) }) {
			auds = append(auds, f)
		}
	}
	for _, f := range m.Videos {
		if !joinable(f.VideoCodec, false) {
			continue
		}
		h := f.Height
		cur, ok := byHeight[h]
		switch {
		case !ok:
			heights = append(heights, h)
			byHeight[h] = f
		case m.Pinned && cur.ID == m.Anchor:
		case m.Pinned && f.ID == m.Anchor, better(f, cur):
			byHeight[h] = f
		}
	}
	if len(heights) == 0 {
		return nil, fmt.Errorf("%w (no file in a format Downex can save)", ErrNotFound)
	}
	slices.SortFunc(heights, func(a, b int) int { return cmp.Compare(b, a) })
	slices.SortStableFunc(auds, func(a, b File) int { return cmp.Compare(b.Size, a.Size) })
	o := &Offer{Files: map[string]File{}, Duration: el.Duration}
	for _, h := range heights {
		f := byHeight[h]
		o.Tracks = append(o.Tracks, track(f))
		o.Files[f.ID] = f
		if o.Duration == 0 {
			o.Duration = f.Duration
		}
	}
	for _, f := range auds {
		o.Tracks = append(o.Tracks, track(f))
		o.Files[f.ID] = f
	}
	best := byHeight[heights[0]]
	if a, ok := o.Files[m.Anchor]; ok && m.Pinned {
		best = a
	}
	o.DefaultVideo = best.ID
	switch {
	case best.Kind == media.KindMuxed:
	case len(auds) > 0:
		o.DefaultAudio = auds[0].ID
		if _, ok := o.Files[m.Audio]; ok {
			o.DefaultAudio = m.Audio
		}
	default:
		o.Silent = true
	}
	return o, nil
}

// better reports whether f is preferred over cur at the same height.
func better(f, cur File) bool {
	if (f.Kind == media.KindMuxed) != (cur.Kind == media.KindMuxed) {
		return f.Kind == media.KindMuxed
	}
	if a, b := codecRank(f.VideoCodec), codecRank(cur.VideoCodec); a != b {
		return a < b
	}
	return f.Size > cur.Size
}

func track(f File) media.Track {
	t := media.Track{ID: f.ID, Key: f.ID, Kind: f.Kind, Size: f.Size, Ext: "mp4"}
	if f.Kind == media.KindAudio {
		t.Ext, t.Codecs = "m4a", f.AudioCodec
	} else {
		t.Width, t.Height, t.Codecs = f.Width, f.Height, f.VideoCodec
		if f.Kind == media.KindMuxed && f.AudioCodec != "" {
			t.Codecs += "," + f.AudioCodec
		}
		if f.Width > 0 && f.Height > 0 {
			t.Name = media.QualityName(f.Width, f.Height, 0, false)
		}
	}
	if f.Size > 0 && f.Duration > 0 {
		t.Bandwidth = int64(float64(f.Size*8) / f.Duration.Seconds())
	}
	return t
}

// Probe describes and matches src and returns its offer (docs/SITE-MEDIA-SPEC.md §2.9).
func Probe(src Source) (*Offer, error) {
	if err := Check(src); err != nil {
		return nil, err
	}
	m, err := Match(src)
	if err != nil {
		return nil, err
	}
	return MakeOffer(m, src.Element)
}

// Output is how a pick is saved: a video file with an audio file is joined, and so is a
// fragmented file alone (into a progressive MP4, as a silent site video,
// docs/SITE-MEDIA-SPEC.md §3.11); a progressive file alone is copied.
func Output(video media.TrackKind, fragmented, withAudio bool) string {
	if withAudio && video == media.KindVideo || fragmented {
		return OutputRemux
	}
	return OutputCopy
}
