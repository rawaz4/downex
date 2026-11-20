package daemon

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/avscan"
	"downex/internal/filecheck"
	"downex/internal/filemark"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// The file check of docs/DESKTOP-SPEC.md §9 in the daemon.

func peHead() []byte {
	b := make([]byte, 1024)
	copy(b, "MZ")
	b[0x3C] = 0x80
	copy(b[0x80:], "PE\x00\x00")
	return b
}

// orderedPost records the order of the post-processing steps around the real check.
type orderedPost struct {
	mu    sync.Mutex
	calls []string
}

func (o *orderedPost) note(s string) {
	o.mu.Lock()
	o.calls = append(o.calls, s)
	o.mu.Unlock()
}

func (o *orderedPost) processor() *PostProcessor {
	p := NewPostProcessor(nil)
	check := p.Check
	p.Check = func(ctx context.Context, path string, d *model.Download) (*model.FileCheck, error) {
		o.note("check")
		return check(ctx, path, d)
	}
	p.Mark = func(string, filemark.Origin) error { o.note("mark"); return nil }
	p.Scan = func(context.Context, string, string, string) (avscan.Result, error) {
		o.note("scan")
		return avscan.Result{}, nil
	}
	return p
}

// TestFileCheckEarlyThenFinal: a "movie.mp4" that is a Windows program shows the EARLY
// DANGER while it downloads, then the stored FINAL result; the check runs before the
// mark and the scan, and the tray hears of it.
func TestFileCheckEarlyThenFinal(t *testing.T) {
	const size = 2 << 20
	ts := serve(t, testserver.Options{Size: size, Seed: 3, ETag: `"pe"`, Prefix: peHead(), Filename: "movie.mp4", ContentType: "video/mp4"})
	post := &orderedPost{}
	scanner := filepath.Join(t.TempDir(), "scan.exe")
	h := newHarness(t, harnessOptions{post: post.processor(), settings: func(s *downexv1.Settings) {
		s.AvScanEnabled, s.AvScanCommand = false, scanner
	}})
	// The scan is configured directly: normalize would require the scanner to exist.
	h.m.mu.Lock()
	h.m.settings.Desktop.AVScanEnabled = true
	h.m.mu.Unlock()
	notices, cancel := h.m.Subscribe()
	defer cancel()

	ts.Hold(256 << 10)
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/get"})
	early := h.waitFor(d.GetId(), "early file check", func(d *downexv1.Download) bool {
		return d.GetFileCheck().GetStage() == downexv1.FileCheckStage_FILE_CHECK_STAGE_EARLY
	})
	fc := early.GetFileCheck()
	if fc.GetLevel() != downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER || fc.GetReason() != "program_disguised" ||
		fc.GetSummary() != "This file is a Windows program, not an MP4 video." ||
		fc.GetDetail() != "Files that pretend to be another type are a common way to spread malware. Don’t rename or run it unless you trust where it came from. Downex checks it again when the download completes." {
		t.Fatalf("early check = %v", fc)
	}
	if early.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		t.Fatal("completed during the hold")
	}
	ts.Release()

	done := h.waitCompleted(d.GetId())
	fc = done.GetFileCheck()
	if fc.GetStage() != downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL || fc.GetLevel() != downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER ||
		fc.GetContent() != "pe" || fc.GetContentLabel() != "Windows program" || fc.GetClaimLabel() != "MP4 video" ||
		fc.GetServedType() != "video/mp4" {
		t.Fatalf("final check = %v", fc)
	}
	if got := storedState(t, h.st, d.GetId()).FileCheck; got == nil || got.Stage != model.FileCheckStageFinal || got.CheckedAt.IsZero() {
		t.Fatalf("stored check = %+v", got)
	}
	post.mu.Lock()
	calls := append([]string(nil), post.calls...)
	post.mu.Unlock()
	if want := []string{"check", "mark", "scan"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("post-processing order = %q, want %q", calls, want)
	}
	n := nextNotice(t, notices, NoticeFileCheck)
	if n.Level != model.FileCheckDanger || n.Text != "movie.mp4 is not what its name says." || n.Download.GetId() != d.GetId() {
		t.Fatalf("notice = %+v", n)
	}

	// Turned off: nothing is reported, but the stored result stays.
	h.m.mu.Lock()
	h.m.settings.Desktop.AVScanEnabled = false // the stand-in scanner does not exist
	h.m.mu.Unlock()
	h.setCheckFiles(false)
	if g := h.get(d.GetId()); g.GetFileCheck() != nil {
		t.Fatalf("check while off = %v", g.GetFileCheck())
	}
	h.setCheckFiles(true)
	if g := h.get(d.GetId()); g.GetFileCheck().GetStage() != downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL {
		t.Fatalf("check after turning it on again = %v", g.GetFileCheck())
	}
}

func (h *harness) setCheckFiles(on bool) {
	h.t.Helper()
	s := protoSettings(h.m.Settings())
	s.CheckDownloadedFiles = on
	if _, err := h.m.UpdateSettings(context.Background(), s); err != nil {
		h.t.Fatal(err)
	}
}

// TestFileCheckOffComputesNothing: with the setting off a completed download gets no
// stored result, and turning it on later shows none (only `downex check` checks it).
func TestFileCheckOffComputesNothing(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 100 << 10, Seed: 4, Prefix: peHead(), Filename: "movie.mp4"})
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.CheckDownloadedFiles = false }})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/get"})
	if done := h.waitCompleted(d.GetId()); done.GetFileCheck() != nil {
		t.Fatalf("check while off = %v", done.GetFileCheck())
	}
	if got := storedState(t, h.st, d.GetId()).FileCheck; got != nil {
		t.Fatalf("stored check while off = %+v", got)
	}
	h.setCheckFiles(true)
	if g := h.get(d.GetId()); g.GetFileCheck() != nil {
		t.Fatalf("check of a download completed while off = %v", g.GetFileCheck())
	}
}

// TestFileCheckSettingRepublishes: turning check_downloaded_files off or on changes what
// every download reports (§9.9), so Watch sends each one again: a window does not keep
// showing "Check file" after the user turned the check off.
func TestFileCheckSettingRepublishes(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 100 << 10, Seed: 6, Prefix: peHead(), Filename: "movie.mp4"})
	h := newHarness(t, harnessOptions{})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/get"})
	if done := h.waitCompleted(d.GetId()); done.GetFileCheck().GetLevel() != downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER {
		t.Fatalf("final check = %v", done.GetFileCheck())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan *downexv1.Download, 16)
	snapshot := make(chan struct{})
	go func() {
		first := true
		_ = h.m.Watch(ctx, []int64{d.GetId()}, func(r *downexv1.WatchResponse) error {
			if first {
				first = false
				close(snapshot)
				return nil
			}
			for _, ev := range r.GetEvents() {
				if u := ev.GetUpdated(); u != nil {
					events <- u
				}
			}
			return nil
		})
	}()
	<-snapshot
	next := func(what string, want bool) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case u := <-events:
				if (u.GetFileCheck() != nil) == want {
					return
				}
			case <-timeout:
				t.Fatalf("no update %s", what)
			}
		}
	}
	h.setCheckFiles(false)
	next("without the check after turning it off", false)
	h.setCheckFiles(true)
	next("with the check after turning it on", true)
}

// TestFileCheckNameStage: an awaiting browser hand-off is judged by its name before any
// byte arrives; a confirmation under the user's own name drops the hidden controls.
func TestFileCheckNameStage(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 1000, Seed: 5})
	h := newHarness(t, harnessOptions{})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/a", Filename: "invoice.pdf.exe", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	fc := h.get(d.GetId()).GetFileCheck()
	if d.GetState() != stAwaiting || fc.GetStage() != downexv1.FileCheckStage_FILE_CHECK_STAGE_NAME ||
		fc.GetLevel() != downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER || fc.GetReason() != "double_extension" ||
		fc.GetSummary() != "“invoice.pdf.exe” is a Windows program whose name imitates a PDF document." {
		t.Fatalf("awaiting %v check %v", d.GetState(), fc)
	}

	hid := h.add(&downexv1.AddRequest{Url: ts.URL + "/b", Filename: "report‮fdp.exe", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if !storedState(t, h.st, hid.GetId()).NameControls || h.get(hid.GetId()).GetFileCheck().GetReason() != "name_hid_direction" {
		t.Fatalf("hidden controls not noted: %v", h.get(hid.GetId()).GetFileCheck())
	}
	if _, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: hid.GetId(), Filename: "report.exe"})); err != nil {
		t.Fatal(err)
	}
	if rec := storedState(t, h.st, hid.GetId()); rec.NameControls || rec.Filename != "report.exe" {
		t.Fatalf("after confirming another name: %+v", rec)
	}
	if fc := h.get(hid.GetId()).GetFileCheck(); fc != nil {
		t.Fatalf("check after the rename = %v", fc)
	}
	// A CLI add with such a name keeps the note.
	cli := h.add(&downexv1.AddRequest{Url: ts.URL + "/c", Filename: "x‮txt.scr", StartPaused: true})
	if !storedState(t, h.st, cli.GetId()).NameControls {
		t.Fatal("a requested name's controls are not stored")
	}
}

func TestFileCheckNotice(t *testing.T) {
	danger := PostResult{Check: &model.FileCheck{Level: model.FileCheckDanger}}
	caution := PostResult{Check: &model.FileCheck{Level: model.FileCheckCaution}}
	note := PostResult{Check: &model.FileCheck{Level: model.FileCheckNote}}
	for _, tc := range []struct {
		r      PostResult
		notify bool
		want   string
	}{
		{danger, false, "a.mp4 is not what its name says."},
		{danger, true, "a.mp4 is not what its name says."},
		{caution, true, "a.mp4 may not be the file you wanted."},
		{caution, false, ""},
		{note, true, ""},
		{PostResult{}, true, ""},
	} {
		text, ok := tc.r.FileCheckNotice("a.mp4", tc.notify)
		if text != tc.want || ok != (tc.want != "") {
			t.Errorf("FileCheckNotice(%v, %v) = %q %v, want %q", tc.r.Check, tc.notify, text, ok, tc.want)
		}
	}
}

// TestPostProcessChecksFilesOnly: a media download (an MP4 Downex assembled) gets no
// content check; with the setting off nothing is checked.
func TestPostProcessChecksFilesOnly(t *testing.T) {
	dir := t.TempDir()
	o := &orderedPost{}
	p := o.processor()
	d := completedDownload(dir)
	d.Kind = model.KindMedia
	res := p.Process(context.Background(), d, DesktopSettings{CheckFiles: true})
	if res.Checked || len(o.calls) != 0 {
		t.Fatalf("media: %+v calls %q", res, o.calls)
	}
	d.Kind = model.KindFile
	res = p.Process(context.Background(), d, DesktopSettings{CheckFiles: false, MarkDownloadedFiles: true})
	if res.Checked || !reflect.DeepEqual(o.calls, []string{"mark"}) {
		t.Fatalf("off: %+v calls %q", res, o.calls)
	}
	// A file that cannot be read gives no result and never fails the download.
	res = p.Process(context.Background(), d, DesktopSettings{CheckFiles: true})
	if res.Checked || res.Check != nil {
		t.Fatalf("missing file: %+v", res)
	}
}

// TestFileCheckLogsNoNames: a check that cannot read the file logs why, never the file's
// name (§9.1 F1).
func TestFileCheckLogsNoNames(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "secret-name.mp4")
	_, err := filecheck.CheckPath(context.Background(), missing, "secret-name.mp4", false, "")
	if err == nil {
		t.Fatal("no error for a missing file")
	}
	if got := withoutPath(err).Error(); strings.Contains(got, "secret-name") {
		t.Fatalf("logged error %q names the file", got)
	}
	_, err = filecheck.CheckPath(context.Background(), dir, "secret-name.mp4", false, "")
	if err == nil || strings.Contains(withoutPath(err).Error(), filepath.Base(dir)) {
		t.Fatalf("folder: %v", err)
	}
}

// TestFileCheckDefaultOn: old settings files (without check_files) keep the check on.
func TestFileCheckDefaultOn(t *testing.T) {
	var d DesktopSettings
	if err := d.UnmarshalJSON([]byte(`{"mark_downloaded_files":true}`)); err != nil || !d.CheckFiles {
		t.Fatalf("old desktop object: %+v %v", d, err)
	}
	if !DefaultDesktopSettings().CheckFiles || !protoSettings(DefaultSettings()).GetCheckDownloadedFiles() {
		t.Fatal("default off")
	}
	if filecheck.HeadBytes != 64<<10 {
		t.Fatal("head size")
	}
}
