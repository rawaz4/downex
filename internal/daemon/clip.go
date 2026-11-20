package daemon

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"downex/internal/dlview"
	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
	"downex/internal/validate"
)

// Parts of a video (clips, docs/MEDIA-SPEC.md §8). A part is chosen at AddMedia or
// Confirm, stored in model.Media.Clip, kept by Pause, Resume and Download again, passed
// to the job, and completed with what the job reports (keyframe, fetched span).

// Clip refusals (docs/MEDIA-SPEC.md §8 C2).
var (
	errClipLegacyTS = fmt.Errorf("%w: a part cannot be saved from a download stored as .ts", ErrMediaUnsupported)
	errClipFile     = "a part can be chosen only for a video or stream"
	// errClipAudio: an audio page's download (docs/SITE-MEDIA-SPEC.md §3.13).
	errClipAudio = "parts can be saved of videos only"
)

// clipError is a refused part: InvalidArgument with code clip_invalid.
func clipError(reason string) error {
	return mediaError(CodeClipInvalid, connect.CodeInvalidArgument, errors.New(reason))
}

// requestedClip is a part as a client asked for it: its bounds checked
// (validate.ClipMs), its chapter title cleaned.
type requestedClip struct {
	clip    media.Clip
	chapter string
}

// parseClip validates a client's part (nil: none). The duration is checked by applyClip.
func parseClip(c *downexv1.MediaClip) (*requestedClip, error) {
	if c == nil {
		return nil, nil
	}
	if err := validate.ClipMs(c.GetStartMs(), c.GetEndMs()); err != nil {
		return nil, clipError(err.Error())
	}
	title, err := validate.Title(c.GetChapter())
	if err != nil {
		title = ""
	}
	return &requestedClip{
		clip:    media.Clip{Start: time.Duration(c.GetStartMs()) * time.Millisecond, End: time.Duration(c.GetEndMs()) * time.Millisecond},
		chapter: cutUTF8(title, validate.MaxChapterTitleBytes),
	}, nil
}

// applyClip stores the part rc on med (a copy the caller owns), or none for nil or the
// whole video (C1), after the checks of C2: a legacy ".ts" record is media_unsupported,
// times past the video's known duration clip_invalid. It reports whether the stored
// part changed.
func applyClip(med *model.Media, rc *requestedClip) (bool, error) {
	var clip media.Clip
	chapter := ""
	if rc != nil {
		clip, chapter = rc.clip.Normalize(med.Duration), rc.chapter
	}
	if !clip.IsZero() && med.Container == "ts" && med.Checkpoint != nil {
		return false, mediaError(CodeMediaUnsupported, connect.CodeInvalidArgument, errClipLegacyTS)
	}
	if !clip.IsZero() && isAudioMedia(med) {
		return false, clipError(errClipAudio)
	}
	if err := clip.Validate(med.Duration); err != nil {
		var ce *media.ClipError
		if errors.As(err, &ce) {
			return false, clipError(ce.Reason)
		}
		return false, clipError(err.Error())
	}
	old := clipOf(med)
	if clip.IsZero() {
		med.Clip = nil
		return !old.IsZero(), nil
	}
	if chapter == "" && med.Clip != nil && old == clip {
		chapter = med.Clip.Chapter
	}
	med.Clip = &model.MediaClip{StartMs: clip.Start.Milliseconds(), EndMs: clip.End.Milliseconds(), Chapter: chapter}
	return old != clip, nil
}

// clipOf is the part stored on med (the zero Clip: the whole video).
func clipOf(med *model.Media) media.Clip {
	if med == nil || med.Clip == nil {
		return media.Clip{}
	}
	return media.Clip{Start: time.Duration(med.Clip.StartMs) * time.Millisecond, End: time.Duration(med.Clip.EndMs) * time.Millisecond}
}

// clipFileName is name with the part's suffix (media.ClipName, C19), within the file-name
// limit: the stem is shortened when the suffix would not fit.
func clipFileName(name string, c media.Clip) string {
	out := media.ClipName(name, c)
	if limit := transport.MaxFilenameBytes - 12; len(out) > limit {
		stem := media.StripClipSuffix(name)
		ext := ""
		if i := strings.LastIndexByte(stem, '.'); i > 0 && len(stem)-i <= 8 {
			stem, ext = stem[:i], stem[i:]
		}
		stem = cutUTF8(stem, max(len(stem)-(len(out)-limit), 1))
		out = media.ClipName(strings.TrimRight(stem, ". ")+ext, c)
	}
	return transport.SanitizeFilename(out)
}

// completeClip copies what the job reports about the assembled part onto med's part
// (C20).
func completeClip(med *model.Media, got *model.MediaClip) {
	if med == nil || med.Clip == nil || got == nil {
		return
	}
	med.Clip.KeyframeMs, med.Clip.FetchStartMs, med.Clip.FetchEndMs, med.Clip.FetchedWhole =
		got.KeyframeMs, got.FetchStartMs, got.FetchEndMs, got.FetchedWhole
}

// resetClip forgets what a completed run reported about the part (Download again).
func resetClip(med *model.Media) {
	if med != nil && med.Clip != nil {
		med.Clip.KeyframeMs, med.Clip.FetchStartMs, med.Clip.FetchEndMs, med.Clip.FetchedWhole = 0, 0, 0, false
	}
}

// protoClip renders a stored part (nil: none).
func protoClip(c *model.MediaClip) *downexv1.MediaClip {
	if c == nil {
		return nil
	}
	return &downexv1.MediaClip{StartMs: c.StartMs, EndMs: c.EndMs, Chapter: c.Chapter, KeyframeMs: c.KeyframeMs,
		FetchStartMs: c.FetchStartMs, FetchEndMs: c.FetchEndMs, FetchedWhole: c.FetchedWhole}
}

// protoChapters renders a video's chapters.
func protoChapters(cs []model.Chapter) []*downexv1.Chapter {
	if len(cs) == 0 {
		return nil
	}
	out := make([]*downexv1.Chapter, len(cs))
	for i, c := range cs {
		out[i] = &downexv1.Chapter{StartMs: c.StartMs, EndMs: c.EndMs, Title: c.Title}
	}
	return out
}

// clipExpectedSize scales a site video's size to its part (dlview.ClipFraction): an
// estimate, 0 when the size is unknown.
func clipExpectedSize(med *model.Media, size int64) int64 {
	if size <= 0 {
		return size
	}
	f := dlview.ClipFraction(med)
	if f >= 1 {
		return size
	}
	return int64(math.Max(1, math.Round(float64(size)*f)))
}

// cutUTF8 cuts s to at most n bytes at a character boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// checkChapters checks a resolved video's chapters (validate.Chapters, C14).
func checkChapters(cs []model.Chapter, duration time.Duration) []model.Chapter {
	if len(cs) == 0 {
		return nil
	}
	in := make([]validate.Chapter, len(cs))
	for i, c := range cs {
		in[i] = validate.Chapter{StartMs: c.StartMs, EndMs: c.EndMs, Title: c.Title}
	}
	var out []model.Chapter
	for _, c := range validate.Chapters(in, duration.Milliseconds()) {
		out = append(out, model.Chapter{StartMs: c.StartMs, EndMs: c.EndMs, Title: c.Title})
	}
	return out
}
