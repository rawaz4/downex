package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

func (a *app) confirm(ctx context.Context, args []string) error {
	fs := a.flagSet("confirm", "confirm <id> [-o dir] [--name file] [--video ID] [--audio ID] [--from t] [--to t | --chapter N | --whole]\n"+
		"       [--queue q] [--later] [--wait]",
		"Confirm a download handed off by the browser that awaits confirmation (its state is\n"+
			"\"awaiting\" in \"downex ls\"): queue it now, or with --later keep it paused until\n"+
			"\"downex resume <id>\". -o and --name replace the proposed folder and file name; a\n"+
			"name that is taken gets a \" (n)\" suffix. For a stream or a video page, --video and --audio\n"+
			"choose other tracks, e.g. any MP4 quality of the page (see \"downex info <id>\"); a page's\n"+
			"video stays an .mp4. --queue puts it into another queue: it starts when that\n"+
			"queue runs. --from, --to and --chapter save only part of a video (see \"downex media\"),\n"+
			"--whole all of it. To cancel the download, use \"downex rm <id>\".\n"+
			"With --wait, follow the progress until the download completes.")
	t := a.daemonFlags(fs)
	var (
		dir   string
		name  string
		later bool
		wait  bool
		video string
		audio string
		queue string
		clipF clipFlags
	)
	fs.StringVar(&dir, "o", "", "save into `dir` instead of the proposed folder")
	fs.StringVar(&name, "name", "", "save as `file` instead of the proposed name")
	fs.StringVar(&video, "video", "", "streams and video pages: download the video track `ID` instead of the proposed one")
	fs.StringVar(&audio, "audio", "", "streams: download the audio track `ID` instead of the proposed one")
	fs.BoolVar(&later, "later", false, "keep the download paused (\"download later\") instead of queueing it")
	fs.StringVar(&queue, "queue", "", "put the download into `queue` (an id or a name, see \"downex queue ls\"; default: keep its queue)")
	fs.BoolVar(&wait, "wait", false, "follow the download's progress until it completes")
	fs.StringVar(&clipF.from, "from", "", "streams and video pages: save only part of the video, from `time` (e.g. 1:20)")
	fs.StringVar(&clipF.to, "to", "", "streams and video pages: save the part up to `time` (default: to the end)")
	fs.IntVar(&clipF.chapter, "chapter", 0, "video pages: save only chapter `N` (1-based, see \"downex info <id>\")")
	fs.BoolVar(&clipF.whole, "whole", false, "streams and video pages: save the whole video instead of a part")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	mid, err := parseID("confirm", pos)
	if err != nil {
		return err
	}
	if later && wait {
		return usageError{"confirm", "--later and --wait cannot be combined"}
	}
	set := visited(fs)
	clip, err := clipF.parse("confirm", set)
	if err != nil {
		return err
	}
	if set["name"] && name == "" {
		return usageError{"confirm", "--name must not be empty"}
	}
	if dir != "" {
		// The daemon requires an absolute directory; -o is relative to this process.
		if dir, err = filepath.Abs(dir); err != nil {
			return fmt.Errorf("directory: %w", err)
		}
	}
	id := int64(mid)
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	var q *downexv1.Queue
	if set["queue"] {
		if q, err = a.resolveQueueFlag(ctx, c, queue); err != nil {
			return err
		}
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	if set["chapter"] {
		g, err := c.Downloads.Get(cctx, connect.NewRequest(&downexv1.GetRequest{Id: id}))
		if err != nil {
			return downloadRPCError("confirm", id, err)
		}
		if clip, err = chapterClip("confirm", clipF.chapter, protoChapterViews(g.Msg.GetDownload().GetMedia().GetChapters())); err != nil {
			return err
		}
	}
	resp, err := c.Downloads.Confirm(cctx, connect.NewRequest(&downexv1.ConfirmRequest{
		Id: id, Dir: dir, Filename: name, Start: !later, VideoTrack: video, AudioTrack: audio, QueueId: q.GetId(), Clip: clip,
	}))
	if err != nil {
		return downloadRPCError("confirm", id, err)
	}
	d := resp.Msg.GetDownload()
	target := d.GetDir()
	if d.GetFilename() != "" {
		target = filepath.Join(d.GetDir(), d.GetFilename())
	}
	if later {
		fmt.Fprintf(a.stdout, "Confirmed download %d for later: %s\nStart it with: %s\n", id, printable(target),
			commandLine(t, "resume", idArg(id)))
		return nil
	}
	fmt.Fprintf(a.stdout, "Confirmed download %d (%s): %s\n", id, describeState(d), printable(target))
	a.hintStoppedQueue(t, q, d)
	if !wait {
		return nil
	}
	return a.follow(ctx, c, t, d)
}

// onOff is a flag.Value for a setting given as on or off (also true/false, yes/no, 1/0).
type onOff struct{ v *bool }

func (f onOff) String() string {
	if f.v == nil || !*f.v {
		return "off"
	}
	return "on"
}

func (f onOff) Set(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		*f.v = true
	case "off", "false", "no", "0":
		*f.v = false
	default:
		return fmt.Errorf("want on or off, got %q", s)
	}
	return nil
}

func onOffText(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
