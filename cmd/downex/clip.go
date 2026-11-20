package main

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Parts of a video (clips, docs/MEDIA-SPEC.md §8): --from, --to and --chapter of
// "media" and "confirm", the "Chapters:" block of --list and the "Part:" line of info.

// clipView is the JSON form of a part of a video (mediaView.Clip).
type clipView struct {
	StartMs      int64  `json:"startMs"`
	EndMs        int64  `json:"endMs,omitempty"` // 0 = to the end
	Chapter      string `json:"chapter,omitempty"`
	KeyframeMs   int64  `json:"keyframeMs,omitempty"`
	FetchStartMs int64  `json:"fetchStartMs,omitempty"`
	FetchEndMs   int64  `json:"fetchEndMs,omitempty"`
	FetchedWhole bool   `json:"fetchedWhole,omitempty"`
}

// chapterView is one chapter of a video (mediaView.Chapters).
type chapterView struct {
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Title   string `json:"title,omitempty"`
}

func protoClipView(c *downexv1.MediaClip) *clipView {
	if c == nil {
		return nil
	}
	return &clipView{StartMs: c.GetStartMs(), EndMs: c.GetEndMs(), Chapter: c.GetChapter(), KeyframeMs: c.GetKeyframeMs(),
		FetchStartMs: c.GetFetchStartMs(), FetchEndMs: c.GetFetchEndMs(), FetchedWhole: c.GetFetchedWhole()}
}

func storedClipView(c *model.MediaClip) *clipView {
	if c == nil {
		return nil
	}
	return &clipView{StartMs: c.StartMs, EndMs: c.EndMs, Chapter: c.Chapter, KeyframeMs: c.KeyframeMs,
		FetchStartMs: c.FetchStartMs, FetchEndMs: c.FetchEndMs, FetchedWhole: c.FetchedWhole}
}

func protoChapterViews(cs []*downexv1.Chapter) []chapterView {
	var out []chapterView
	for _, c := range cs {
		out = append(out, chapterView{StartMs: c.GetStartMs(), EndMs: c.GetEndMs(), Title: c.GetTitle()})
	}
	return out
}

func storedChapterViews(cs []model.Chapter) []chapterView {
	var out []chapterView
	for _, c := range cs {
		out = append(out, chapterView{StartMs: c.StartMs, EndMs: c.EndMs, Title: c.Title})
	}
	return out
}

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

// done reports a part whose download completed (the daemon then records what it became).
func (c *clipView) done() bool { return c.KeyframeMs > 0 || c.FetchEndMs > 0 || c.FetchedWhole }

// partText is the "Part:" line of info (docs/SITE-MEDIA-SPEC.md §5.4, B6): "1:20 – 2:45",
// with the keyframe once complete ("(keyframe 1:17.4)"), or the visible start when the
// first keyframe came after the part's start ("1:23 – 2:45 (first keyframe after 1:20)").
func partText(c *clipView, duration time.Duration) string {
	clip := media.Clip{Start: ms(c.StartMs), End: ms(c.EndMs)}
	switch {
	case c.done() && c.KeyframeMs > c.StartMs:
		late := media.Clip{Start: ms(c.KeyframeMs), End: clip.End}
		return media.FormatClipRange(late, duration) + " (first keyframe after " + media.FormatClipTime(clip.Start) + ")"
	case c.done():
		return media.FormatClipRange(clip, duration) + " (keyframe " + media.FormatClipTime(ms(c.KeyframeMs)) + ")"
	}
	return media.FormatClipRange(clip, duration)
}

// writeChapters prints the "Chapters:" block of --list: number, title and range.
func writeChapters(w io.Writer, cs []chapterView) error {
	if len(cs) == 0 {
		return nil
	}
	fmt.Fprintln(w, "Chapters:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, c := range cs {
		fmt.Fprintf(tw, "  %d\t%s\t%s\n", i+1, printable(dash(c.Title)),
			media.FormatClipRange(media.Clip{Start: ms(c.StartMs), End: ms(c.EndMs)}, 0))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "Save one with --chapter N, or a part with --from and --to.")
	return nil
}

// clipFlags are the part flags of a command.
type clipFlags struct {
	from, to string
	chapter  int
	whole    bool
}

// set reports whether any part flag was given.
func (f clipFlags) set(visited map[string]bool) bool {
	return visited["from"] || visited["to"] || visited["chapter"] || visited["whole"]
}

// parse checks the flags' combinations and times (media.ParseClipTime); chapter is
// resolved by resolve. cmd names the command for usage errors.
func (f clipFlags) parse(cmd string, visited map[string]bool) (*downexv1.MediaClip, error) {
	switch {
	case visited["chapter"] && (visited["from"] || visited["to"]):
		return nil, usageError{cmd, "--chapter cannot be combined with --from or --to"}
	case visited["whole"] && (visited["from"] || visited["to"] || visited["chapter"]):
		return nil, usageError{cmd, "--whole cannot be combined with --from, --to or --chapter"}
	case visited["chapter"] && f.chapter < 1:
		return nil, usageError{cmd, "--chapter wants a chapter number from 1 (see --list)"}
	}
	if !visited["from"] && !visited["to"] {
		if visited["whole"] {
			return &downexv1.MediaClip{}, nil
		}
		return nil, nil
	}
	c := &downexv1.MediaClip{}
	for _, p := range []struct {
		name string
		v    string
		dst  *int64
	}{{"from", f.from, &c.StartMs}, {"to", f.to, &c.EndMs}} {
		if !visited[p.name] {
			continue
		}
		d, err := media.ParseClipTime(p.v)
		if err != nil {
			return nil, usageError{cmd, "--" + p.name + ": Enter a time like 1:20 or 1:02:03."}
		}
		*p.dst = d.Milliseconds()
	}
	return c, nil
}

// chapterClip is the part of chapter n (1-based) of cs.
func chapterClip(cmd string, n int, cs []chapterView) (*downexv1.MediaClip, error) {
	if len(cs) == 0 {
		return nil, usageError{cmd, "--chapter: this video has no chapters"}
	}
	if n > len(cs) {
		return nil, usageError{cmd, "--chapter: this video has " + strconv.Itoa(len(cs)) + " chapters"}
	}
	c := cs[n-1]
	return &downexv1.MediaClip{StartMs: c.StartMs, EndMs: c.EndMs, Chapter: c.Title}, nil
}
