package downloads

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/kit/icons"
	"downex/ui/state"
	"downex/ui/views"
)

// The file check in the list and the details pane (docs/DESKTOP-SPEC.md §9.7).

func withCheck(level downexv1.FileCheckLevel, st downexv1.DownloadState) func(d *downexv1.Download) {
	return func(d *downexv1.Download) {
		d.Filename, d.State, d.Size, d.Received = "movie.mp4", st, 4*MiB, 4*MiB
		d.FileCheck = &downexv1.FileCheck{Level: level, Stage: downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL,
			Reason: "program_disguised", ContentLabel: "Windows program", ClaimLabel: "MP4 video",
			Summary: "This file is a Windows program, not an MP4 video.",
			Detail:  "Files that pretend to be another type are a common way to spread malware. Don’t rename or run it unless you trust where it came from."}
	}
}

func TestRowShowsFileCheck(t *testing.T) {
	r, _ := fakeRig(t,
		Download(1, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, state.StateCompleted)),
		Download(2, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_CAUTION, state.StateCompleted)),
		Download(3, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_NOTE, state.StateCompleted)),
		Download(4, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, state.StateDownloading)),
	)
	for _, c := range []struct {
		id      int64
		status  string
		mark    icons.MarkKind
		tooltip string
	}{
		{1, "Check file", icons.MarkFailed, "This file is a Windows program, not an MP4 video."},
		{2, "Check file", icons.MarkCheckWarn, "This file is a Windows program, not an MP4 video."},
		{3, "Completed", icons.MarkCompleted, "Completed"},
		{4, "Downloading", icons.MarkFailed, "This file is a Windows program, not an MP4 video."},
	} {
		w := r.row(c.id)
		if w == nil {
			t.Fatalf("row %d", c.id)
		}
		if w.status.Text() != c.status || w.mark.kind != c.mark || w.cells[colStatus].Tooltip != c.tooltip {
			t.Errorf("row %d: status %q mark %v tooltip %q", c.id, w.status.Text(), w.mark.kind, w.cells[colStatus].Tooltip)
		}
	}
}

func TestDetailsFileCheck(t *testing.T) {
	t.Run("DANGER: the note, Contents and Remove and delete file…", func(t *testing.T) {
		p := showPane(t, Download(5, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, state.StateCompleted)), nil)
		n, ok := find[*views.FileCheckNote](p.root())
		if !ok || n.Tone != views.NoteFail || n.Heading.Text() != "This file is not what its name says" ||
			n.Summary.Text() != "This file is a Windows program, not an MP4 video." || n.Detail.Style.Hidden {
			t.Fatalf("note %+v", n)
		}
		if got, _ := p.fact("Contents"); got != "Windows program · named as MP4 video" {
			t.Errorf("Contents %q", got)
		}
		b := p.buttonNamed("Remove and delete file…")
		if b == nil {
			t.Fatal("no Remove and delete file…")
		}
		p.Click(b)
		call, ok := p.Calls.Last("Remove")
		if !ok || len(call.Args) != 1 {
			t.Fatalf("calls %v", p.Calls.List)
		}
		if req := call.Args[0].(views.RemoveRequest); !req.DeleteFile || len(req.IDs) != 1 || req.IDs[0] != 5 {
			t.Errorf("remove %+v", req)
		}
	})
	t.Run("CAUTION: a warn note without the button", func(t *testing.T) {
		p := showPane(t, Download(6, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_CAUTION, state.StateCompleted)), nil)
		n, ok := find[*views.FileCheckNote](p.root())
		if !ok || n.Tone != views.NoteWarn || n.Heading.Text() != "This may not be the file you wanted" {
			t.Fatalf("note %+v", n)
		}
		if p.buttonNamed("Remove and delete file…") != nil {
			t.Error("CAUTION offers to delete")
		}
	})
	t.Run("NOTE: Contents only", func(t *testing.T) {
		p := showPane(t, Download(7, withCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_NOTE, state.StateCompleted)), nil)
		if _, ok := find[*views.FileCheckNote](p.root()); ok {
			t.Error("a NOTE shows a note")
		}
		if got, _ := p.fact("Contents"); got != "Windows program · named as MP4 video" {
			t.Errorf("Contents %q", got)
		}
	})
}
