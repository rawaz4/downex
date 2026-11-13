package host

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/engine"
	downexv1 "downex/internal/rpc/downex/v1"
)

func completedIn(dir string, id int64, name string) *downexv1.Download {
	return &downexv1.Download{Id: id, Dir: dir, Filename: name, Size: 3, Received: 3,
		State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestFiles returns the files of a session on d whose window answers with r.
func newTestFiles(t *testing.T, d Daemon, r *recorder) (*Files, *fakeShell) {
	t.Helper()
	s, sh := newTestSession(t, d)
	if r != nil {
		s.Files().SetConfirm(r.confirm)
	}
	return s.Files(), sh
}

func TestOpenFileOpensCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "report.pdf"))
	r := &recorder{}
	f, sh := newTestFiles(t, newFakeDaemon(completedIn(dir, 1, "report.pdf")), r)
	if err := f.OpenFile(callCtx(t), 1); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "report.pdf")}; !reflect.DeepEqual(sh.opened, want) {
		t.Errorf("opened %v, want %v", sh.opened, want)
	}
	if q := r.questions(); len(q) != 0 {
		t.Errorf("a document was confirmed: %q", q)
	}
}

func TestOpenFileRefusals(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "partial.zip"))
	if err := os.Mkdir(filepath.Join(dir, "folder.zip"), 0o755); err != nil {
		t.Fatal(err)
	}
	running := completedIn(dir, 1, "partial.zip")
	running.State = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	d := newFakeDaemon(
		running,
		completedIn(dir, 2, "gone.zip"),
		completedIn("relative", 3, "a.zip"),
		completedIn(dir, 4, ".."),
		completedIn(dir, 5, `sub`+string(filepath.Separator)+`a.zip`),
		completedIn(dir, 6, "folder.zip"),
		completedIn(dir, 7, ""),
		completedIn(dir, 8, "nul\x00.zip"),
	)
	r := &recorder{answer: true}
	f, sh := newTestFiles(t, d, r)
	// Open with… makes the same checks as Open before the system's dialog appears.
	for _, open := range []struct {
		name string
		fn   func(id int64) error
	}{
		{"OpenFile", func(id int64) error { return f.OpenFile(callCtx(t), id) }},
		{"OpenWith", func(id int64) error { return f.OpenWith(callCtx(t), id) }},
	} {
		for _, tc := range []struct {
			id   int64
			want string
		}{
			{1, "failed_precondition: download 1 is not completed (downloading)"},
			{2, "not_found: " + filepath.Join(dir, "gone.zip") + " does not exist"},
			{3, "failed_precondition: download 3 does not name a usable file"},
			{4, "failed_precondition: download 4 does not name a usable file"},
			{5, "failed_precondition: download 5 does not name a usable file"},
			{6, "failed_precondition: " + filepath.Join(dir, "folder.zip") + " is a folder"},
			{7, "failed_precondition: download 7 has no file name yet"},
			{8, "failed_precondition: download 8 does not name a usable file"},
			{42, "not_found: no such download"},
		} {
			if err := open.fn(tc.id); !hasPrefix(err, tc.want) {
				t.Errorf("%s(%d) = %v, want %q", open.name, tc.id, err, tc.want)
			}
		}
	}
	if len(sh.opened) != 0 || len(sh.openedWith) != 0 {
		t.Errorf("opened %v, opened with %v", sh.opened, sh.openedWith)
	}
	if q := r.questions(); len(q) != 0 {
		t.Errorf("refused files were confirmed: %q", q)
	}
}

func TestOpenWithChoosesAnAppForCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "report.pdf"))
	r := &recorder{}
	f, sh := newTestFiles(t, newFakeDaemon(completedIn(dir, 1, "report.pdf")), r)
	if err := f.OpenWith(callCtx(t), 1); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "report.pdf")}; !reflect.DeepEqual(sh.openedWith, want) {
		t.Errorf("opened with %v, want %v", sh.openedWith, want)
	}
	if len(sh.opened) != 0 {
		t.Errorf("the default app opened %v", sh.opened)
	}
	if q := r.questions(); len(q) != 0 {
		t.Errorf("a document was confirmed: %q", q)
	}
}

// The app the user picks may run the file: programs and active content are confirmed
// first, as for Open.
func TestOpenWithConfirmsProgramsAndActiveContent(t *testing.T) {
	dir := t.TempDir()
	names := []string{"setup.exe", "run.ps1", "invoice.rdp", "budget.docm", "invoice\u202efdp.exe"}
	var ds []*downexv1.Download
	for i, n := range names {
		writeFile(t, filepath.Join(dir, n))
		ds = append(ds, completedIn(dir, int64(i+1), n))
	}
	r := &recorder{answer: false}
	f, sh := newTestFiles(t, newFakeDaemon(ds...), r)
	for _, d := range ds {
		if err := f.OpenWith(callCtx(t), d.GetId()); err != nil {
			t.Errorf("declined %q: %v, want no error", d.GetFilename(), err)
		}
	}
	if len(sh.openedWith) != 0 || len(sh.opened) != 0 {
		t.Fatalf("declined files were opened: with %v, default %v", sh.openedWith, sh.opened)
	}
	q := r.questions()
	if len(q) != len(ds) {
		t.Fatalf("%d confirmations for %d files: %q", len(q), len(ds), q)
	}
	if !strings.HasPrefix(q[0], "Run this program?\nsetup.exe is a program or script.") {
		t.Errorf("question about a program = %q", q[0])
	}
	if !strings.HasPrefix(q[2], "Open this file?\ninvoice.rdp is a type of file that can run code") {
		t.Errorf("question about a remote desktop connection = %q", q[2])
	}
	if !strings.Contains(q[4], "invoice⟨U+202E⟩fdp.exe") || strings.ContainsRune(q[4], '\u202e') {
		t.Errorf("question about a disguised name = %q", q[4])
	}

	r.setAnswer(true, nil)
	if err := f.OpenWith(callCtx(t), 1); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "setup.exe")}; !reflect.DeepEqual(sh.openedWith, want) {
		t.Errorf("after yes: opened with %v, want %v", sh.openedWith, want)
	}

	r.setAnswer(true, errors.New("no dialog"))
	if err := f.OpenWith(callCtx(t), 1); !hasPrefix(err, "internal: confirmation dialog: no dialog") {
		t.Errorf("failed dialog: %v", err)
	}
	if len(sh.openedWith) != 1 {
		t.Errorf("a program was opened without an answer: %v", sh.openedWith)
	}

	// Without a window there is nobody to ask: nothing opens.
	f.SetConfirm(nil)
	if err := f.OpenWith(callCtx(t), 1); !hasPrefix(err, "unavailable:") {
		t.Errorf("OpenWith of a program without a window: %v", err)
	}
	if len(sh.openedWith) != 1 {
		t.Errorf("a program was opened without a window: %v", sh.openedWith)
	}
}

func TestOpenWithFollowsLinksToPrograms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tool.exe"))
	if err := os.Symlink(filepath.Join(dir, "tool.exe"), filepath.Join(dir, "notes.txt")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	r := &recorder{answer: false}
	f, sh := newTestFiles(t, newFakeDaemon(completedIn(dir, 1, "notes.txt")), r)
	if err := f.OpenWith(callCtx(t), 1); err != nil {
		t.Fatal(err)
	}
	if q := r.questions(); len(q) != 1 || len(sh.openedWith) != 0 {
		t.Fatalf("a link to a program: confirms %q, opened with %v", q, sh.openedWith)
	}
}

func TestOpenWithShellResults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"))
	f, sh := newTestFiles(t, newFakeDaemon(completedIn(dir, 1, "a.txt")), &recorder{})
	path := filepath.Join(dir, "a.txt")
	for _, tc := range []struct {
		err  error
		want string
	}{
		// The user closed the dialog: the window stays as it is.
		{connect.NewError(connect.CodeCanceled, errors.New("no app was chosen")), "canceled: no app was chosen"},
		// A system without the dialog (Linux, for now): the window says so.
		{connect.NewError(connect.CodeUnimplemented, errors.New("not available on this system yet")), "unimplemented: not available on this system yet"},
		{errors.New("SHOpenWithDialog: HRESULT 0x80004005"), "internal: cannot open " + path + ": SHOpenWithDialog: HRESULT 0x80004005"},
	} {
		sh.mu.Lock()
		sh.withErr = tc.err
		sh.mu.Unlock()
		if err := f.OpenWith(callCtx(t), 1); errText(err) != tc.want {
			t.Errorf("OpenWith with shell error %v = %v, want %q", tc.err, err, tc.want)
		}
	}
	if len(sh.opened) != 0 {
		t.Errorf("the default app opened %v", sh.opened)
	}
}

func TestOpenFileConfirmsPrograms(t *testing.T) {
	dir := t.TempDir()
	names := []string{"setup.exe", "SETUP.EXE", "install.msi", "run.ps1", "a.bat", "b.cmd", "c.vbs", "d.js",
		"e.lnk", "f.scr", "g.jar", "h.sh", "i.AppImage", "setup.exe. ", "invoice\u202efdp.exe"}
	var ds []*downexv1.Download
	for i, n := range names {
		if strings.HasSuffix(n, " ") && os.PathSeparator == '\\' {
			continue // Windows cannot create such a name; TestRiskOf covers it
		}
		writeFile(t, filepath.Join(dir, n))
		ds = append(ds, completedIn(dir, int64(i+1), n))
	}
	r := &recorder{answer: false}
	f, sh := newTestFiles(t, newFakeDaemon(ds...), r)

	for _, d := range ds {
		if err := f.OpenFile(callCtx(t), d.GetId()); err != nil {
			t.Errorf("declined %q: %v, want no error", d.GetFilename(), err)
		}
	}
	if len(sh.opened) != 0 {
		t.Errorf("declined programs were opened: %v", sh.opened)
	}
	q := r.questions()
	if len(q) != len(ds) {
		t.Fatalf("%d confirmations for %d programs: %q", len(q), len(ds), q)
	}
	if !strings.HasPrefix(q[0], "Run this program?\nsetup.exe is a program or script.") || !strings.Contains(q[0], "Folder: "+dir) {
		t.Errorf("question = %q", q[0])
	}
	// The whole question, as the dialog shows it.
	want := "Run this program?\nsetup.exe is a program or script. Opening it runs it on this computer, with your rights.\n\n" +
		"Programs from the internet can harm your computer. Open it only if you trust where it came from.\n\n" +
		"Folder: " + dir
	if q[0] != want {
		t.Errorf("question =\n%q\nwant\n%q", q[0], want)
	}
	// Bidi controls that disguise the type are shown, not applied.
	last := q[len(q)-1]
	if !strings.Contains(last, "invoice⟨U+202E⟩fdp.exe") || strings.ContainsRune(last, '\u202e') {
		t.Errorf("question about a disguised name = %q", last)
	}

	r.setAnswer(true, nil)
	if err := f.OpenFile(callCtx(t), 1); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "setup.exe")}; !reflect.DeepEqual(sh.opened, want) {
		t.Errorf("after yes: opened %v, want %v", sh.opened, want)
	}

	r.setAnswer(true, errors.New("no dialog"))
	if err := f.OpenFile(callCtx(t), 1); !hasPrefix(err, "internal: confirmation dialog: no dialog") {
		t.Errorf("failed dialog: %v", err)
	}
	if len(sh.opened) != 1 {
		t.Errorf("a program was opened without an answer: %v", sh.opened)
	}
}

// Downloads may carry no Mark-of-the-Web, so neither the system nor Office warns before
// such a file runs its content: Downex asks first.
func TestOpenFileConfirmsActiveContent(t *testing.T) {
	dir := t.TempDir()
	names := []string{"invoice.rdp", "report.theme", "fix.diagcab", "tool.xll", "disk.iso", "budget.docm"}
	var ds []*downexv1.Download
	for i, n := range names {
		writeFile(t, filepath.Join(dir, n))
		ds = append(ds, completedIn(dir, int64(i+1), n))
	}
	r := &recorder{answer: false}
	f, sh := newTestFiles(t, newFakeDaemon(ds...), r)
	for _, d := range ds {
		if err := f.OpenFile(callCtx(t), d.GetId()); err != nil {
			t.Errorf("declined %q: %v, want no error", d.GetFilename(), err)
		}
	}
	if len(sh.opened) != 0 {
		t.Errorf("declined files were opened: %v", sh.opened)
	}
	q := r.questions()
	if len(q) != len(names) {
		t.Fatalf("%d confirmations for %d files: %q", len(q), len(names), q)
	}
	want := "Open this file?\ninvoice.rdp is a type of file that can run code or connect to other computers when it is opened, " +
		"with your rights (such as a document with macros, a disk image or a remote desktop connection).\n\n" +
		"Files from the internet can harm your computer. Open it only if you trust where it came from.\n\n" +
		"Folder: " + dir
	if q[0] != want {
		t.Errorf("question about a remote desktop connection =\n%q\nwant\n%q", q[0], want)
	}
	// A plug-in that Excel loads as code is a program.
	if !strings.HasPrefix(q[3], "Run this program?\ntool.xll is a program or script.") {
		t.Errorf("question about an Excel add-in = %q", q[3])
	}
}

func TestOpenFileFollowsLinksToPrograms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tool.exe"))
	if err := os.Symlink(filepath.Join(dir, "tool.exe"), filepath.Join(dir, "notes.txt")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	r := &recorder{answer: false}
	f, sh := newTestFiles(t, newFakeDaemon(completedIn(dir, 1, "notes.txt")), r)
	if err := f.OpenFile(callCtx(t), 1); err != nil {
		t.Fatal(err)
	}
	q := r.questions()
	if len(q) != 1 || len(sh.opened) != 0 {
		t.Fatalf("a link to a program: confirms %q, opened %v", q, sh.opened)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(dir, "tool.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q[0], "notes.txt (a link to "+target+") is a program") {
		t.Errorf("question = %q", q[0])
	}
}

func TestOpenFileShellError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"))
	f, sh := newTestFiles(t, newFakeDaemon(completedIn(dir, 1, "a.txt")), &recorder{})
	sh.err = errors.New("access is denied")
	err := f.OpenFile(callCtx(t), 1)
	if want := "internal: cannot open " + filepath.Join(dir, "a.txt") + ": access is denied"; errText(err) != want {
		t.Errorf("OpenFile = %v, want %q", err, want)
	}
}

func TestOpenFolder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "done.zip"))
	writeFile(t, filepath.Join(dir, "half.zip"+PartSuffix))
	running := completedIn(dir, 2, "half.zip")
	running.State = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	d := newFakeDaemon(
		completedIn(dir, 1, "done.zip"),
		running,
		completedIn(dir, 3, "moved.zip"),
		completedIn(dir, 4, ""), // not probed yet
		completedIn(filepath.Join(dir, "missing"), 5, "a.zip"),
		completedIn("relative", 6, "a.zip"),
	)
	f, sh := newTestFiles(t, d, nil)
	for id := int64(1); id <= 4; id++ {
		if err := f.OpenFolder(callCtx(t), id); err != nil {
			t.Errorf("OpenFolder(%d): %v", id, err)
		}
	}
	want := []string{
		"reveal:" + filepath.Join(dir, "done.zip"),
		"reveal:" + filepath.Join(dir, "half.zip"+PartSuffix),
		"folder:" + dir,
		"folder:" + dir,
	}
	if !reflect.DeepEqual(sh.shown, want) {
		t.Errorf("shown %q, want %q", sh.shown, want)
	}
	if err := f.OpenFolder(callCtx(t), 5); !hasPrefix(err, "not_found: "+filepath.Join(dir, "missing")+" does not exist") {
		t.Errorf("missing folder: %v", err)
	}
	if err := f.OpenFolder(callCtx(t), 6); !hasPrefix(err, "failed_precondition: download 6 has no absolute folder") {
		t.Errorf("relative folder: %v", err)
	}
	if err := f.OpenFolder(callCtx(t), 42); !hasPrefix(err, "not_found: no such download") {
		t.Errorf("unknown download: %v", err)
	}
	sh.err = errors.New("access is denied")
	if err := f.OpenFolder(callCtx(t), 1); errText(err) != "internal: cannot show "+filepath.Join(dir, "done.zip")+": access is denied" {
		t.Errorf("OpenFolder with a shell error = %v", err)
	}
}

// A picker starts in an existing folder: the one asked for, else its nearest existing
// parent, else wherever the system starts it ("").
func TestExistingDir(t *testing.T) {
	dir := t.TempDir()
	for in, want := range map[string]string{
		filepath.Join(dir, "not", "yet"): dir,
		dir:                              dir,
		"relative":                       "",
		"":                               "",
	} {
		if got := ExistingDir(in); got != want {
			t.Errorf("ExistingDir(%q) = %q, want %q", in, got, want)
		}
	}
	writeFile(t, filepath.Join(dir, "file"))
	if got := ExistingDir(filepath.Join(dir, "file")); got != dir {
		t.Errorf("ExistingDir(a file) = %q, want its folder %q", got, dir)
	}
}

func TestPickerTitles(t *testing.T) {
	if FolderPickerTitle != "Choose a folder" {
		t.Errorf("folder picker title %q", FolderPickerTitle)
	}
	for in, want := range map[string]string{
		"":                       "Choose a file",
		"Choose the scanner":     "Choose the scanner",
		strings.Repeat("x", 200): strings.Repeat("x", 200),
		strings.Repeat("x", 201): "Choose a file",
	} {
		if got := FilePickerTitleOf(in); got != want {
			t.Errorf("FilePickerTitleOf(%d bytes) = %q, want %q", len(in), got, want)
		}
	}
}

func TestRiskOf(t *testing.T) {
	for name, want := range map[string]Risk{
		"a.exe": RiskProgram, "A.EXE": RiskProgram, "a.exe.": RiskProgram, "a.exe . ": RiskProgram,
		"x.tar.gz.exe": RiskProgram, "a.Msi": RiskProgram, "a.ps1": RiskProgram, "a.url": RiskProgram,
		"a.desktop": RiskProgram, "a.appimage": RiskProgram, "a.deb": RiskProgram,
		"fix.diagcab": RiskProgram, "tool.xll": RiskProgram, "a.msh1xml": RiskProgram, "a.psc2": RiskProgram,
		"a.vsto": RiskProgram, "a.msu": RiskProgram,
		// Types whose application runs code they contain, mounts them or connects out.
		"invoice.rdp": RiskContent, "report.theme": RiskContent, "a.iso": RiskContent, "a.docm": RiskContent,
		"a.XLSM": RiskContent, "a.wsb": RiskContent, "a.searchconnector-ms": RiskContent, "a.vhdx": RiskContent,
		"a.accde": RiskContent, "a.iqy": RiskContent, "a.slk": RiskContent, "a.ins": RiskContent,
		"a.pdf": RiskNone, "a.zip": RiskNone, "a.exe.txt": RiskNone, "exe": RiskNone, ".exe.mp4": RiskNone,
		"a": RiskNone, "a.docx": RiskNone, "a.xlsx": RiskNone, "a.png": RiskNone,
	} {
		if got := RiskOf(name, nil); got != want {
			t.Errorf("RiskOf(%q) = %v, want %v", name, got, want)
		}
	}
}

// The two lists do not overlap: a type is either a program or a file with content.
func TestExtensionListsAreDisjoint(t *testing.T) {
	for ext := range contentExtensions {
		if programExtensions[ext] {
			t.Errorf("%s is in both lists", ext)
		}
	}
	for _, set := range []map[string]bool{programExtensions, contentExtensions} {
		for ext := range set {
			if ext != strings.ToLower(ext) || strings.Contains(ext, ".") {
				t.Errorf("%q is not a lower-case extension without its dot", ext)
			}
		}
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"plain name.zip":        "plain name.zip",
		"invoice\u202efdp.exe":  "invoice⟨U+202E⟩fdp.exe",
		"a\u200bb\u2066c\u2069": "a⟨U+200B⟩b⟨U+2066⟩c⟨U+2069⟩",
		"line\nbreak\u2028":     "line⟨U+000A⟩break⟨U+2028⟩",
		"日本語.txt":               "日本語.txt",
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartSuffixMatchesTheEngine(t *testing.T) {
	dir := t.TempDir()
	if got, want := filepath.Join(dir, "a.bin"+PartSuffix), engine.PartPath(dir, "a.bin"); got != want {
		t.Errorf("part file %q, the engine writes %q", got, want)
	}
}

func TestStateName(t *testing.T) {
	for s, want := range map[downexv1.DownloadState]string{
		downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:                "paused",
		downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION: "awaiting_confirmation",
		downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:             "completed",
	} {
		if got := StateName(s); got != want {
			t.Errorf("StateName(%v) = %q, want %q", s, got, want)
		}
	}
}

// TestFileExists: the Download again dialog asks whether a download's file is on disk.
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "here.bin"))
	completed := downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	d := newFakeDaemon(
		&downexv1.Download{Id: 1, Dir: dir, Filename: "here.bin", State: completed},
		&downexv1.Download{Id: 2, Dir: dir, Filename: "gone.bin", State: completed},
		&downexv1.Download{Id: 3, Dir: "relative", Filename: "here.bin", State: completed},
		&downexv1.Download{Id: 4, Dir: dir, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED},
	)
	f, _ := newTestFiles(t, d, nil)
	for id, want := range map[int64]bool{1: true, 2: false, 3: false, 4: false} {
		got, err := f.FileExists(callCtx(t), id)
		if err != nil || got != want {
			t.Errorf("download %d: FileExists = %v, %v; want %v", id, got, err, want)
		}
	}
	if _, err := f.FileExists(callCtx(t), 9); !hasPrefix(err, "not_found: ") {
		t.Errorf("unknown download: %v", err)
	}
}

func TestFreeSpaceReadsTheVolume(t *testing.T) {
	f, _ := newTestFiles(t, newFakeDaemon(), nil)
	dir := t.TempDir()
	n, err := f.FreeSpace(dir)
	if err != nil || n <= 0 {
		t.Fatalf("FreeSpace(temp dir) = %d, %v; want a positive count", n, err)
	}
	// A folder that does not exist yet is measured at its nearest existing parent.
	if _, err := f.FreeSpace(filepath.Join(dir, "not", "yet")); err != nil {
		t.Errorf("FreeSpace(missing folder) = %v", err)
	}
}

func TestFreeSpaceAsksForTheFolder(t *testing.T) {
	f, _ := newTestFiles(t, newFakeDaemon(), nil)
	var asked string
	f.FreeSpaceOf = func(dir string) (int64, error) {
		asked = dir
		return 229_780_750_336_123, nil
	}
	dir := t.TempDir()
	if n, err := f.FreeSpace(dir); err != nil || n != 229_780_750_336_123 {
		t.Errorf("FreeSpace = %d, %v", n, err)
	}
	if asked != dir {
		t.Errorf("read %q, want the folder asked for", asked)
	}
}

func TestFreeSpaceRefusesWithoutEchoingThePath(t *testing.T) {
	f, _ := newTestFiles(t, newFakeDaemon(), nil)
	reads := 0
	f.FreeSpaceOf = func(string) (int64, error) {
		reads++
		return 1, nil
	}
	secret := "Users/someone/secret-folder"
	bad := []string{"", secret, "./" + secret, "relative"}
	if runtime.GOOS == "windows" {
		// Rooted but without a drive: relative to the current drive.
		bad = append(bad, `\`+strings.ReplaceAll(secret, "/", `\`))
	}
	for _, dir := range bad {
		if _, err := f.FreeSpace(dir); errText(err) != "invalid_argument: dir must be an absolute path" {
			t.Errorf("FreeSpace(%q) = %v, want invalid_argument: dir must be an absolute path", dir, err)
		}
	}
	if reads != 0 {
		t.Errorf("refused requests read the free space %d times", reads)
	}
}

func TestFreeSpaceUnavailableHidesTheError(t *testing.T) {
	f, _ := newTestFiles(t, newFakeDaemon(), nil)
	dir := filepath.Join(t.TempDir(), "private-name")
	for _, fail := range []func(string) (int64, error){
		func(d string) (int64, error) {
			return 0, errors.New("platform: free space of " + d + ": access is denied")
		},
		func(string) (int64, error) { return -1, nil },
	} {
		f.FreeSpaceOf = fail
		_, err := f.FreeSpace(dir)
		if want := "unavailable: could not read the free space"; errText(err) != want {
			t.Errorf("FreeSpace = %v, want %q", err, want)
		}
		if strings.Contains(errText(err), "private-name") {
			t.Errorf("the error repeats the folder: %v", err)
		}
	}
}

func TestOpenAppFolder(t *testing.T) {
	root := t.TempDir()
	data, logs, tools, downloads := filepath.Join(root, "data"), filepath.Join(root, "logs"), filepath.Join(root, "tools"), filepath.Join(root, "dl")
	for _, d := range []string{data, logs, downloads} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := newFakeDaemon()
	d.status = &downexv1.StatusResponse{Version: "1", DataDir: data, LogDir: logs, ToolsDir: tools}
	d.settings = &downexv1.Settings{DefaultDir: downloads}
	f, sh := newTestFiles(t, d, nil)

	for kind, want := range map[FolderKind]string{FolderData: data, FolderLogs: logs, FolderDownloads: downloads} {
		if err := f.OpenAppFolder(callCtx(t), kind); err != nil {
			t.Fatalf("OpenAppFolder(%s): %v", kind, err)
		}
		if got := sh.shown[len(sh.shown)-1]; got != "folder:"+want {
			t.Errorf("%s: shown %q, want folder:%s", kind, got, want)
		}
	}
	// The tools folder does not exist yet: nothing opens.
	n := len(sh.shown)
	if err := f.OpenAppFolder(callCtx(t), FolderTools); errText(err) != "failed_precondition: the tools folder does not exist yet" || len(sh.shown) != n {
		t.Errorf("OpenAppFolder(tools) = %v (shown %v)", err, sh.shown)
	}

	// The window names a kind, never a path: anything else is refused before anything opens.
	for _, bad := range []string{`C:\Windows`, "../data", "", "Data"} {
		if _, ok := ParseFolderKind(bad); ok {
			t.Errorf("ParseFolderKind(%q) accepted", bad)
		}
		err := f.OpenAppFolder(callCtx(t), FolderKind(bad))
		if errText(err) != `invalid_argument: kind must be "data", "logs", "tools", "downloads", "extension-chrome" or "extension-firefox"` {
			t.Errorf("OpenAppFolder(%q) = %v, want invalid_argument", bad, err)
		}
	}
	if len(sh.shown) != n {
		t.Errorf("refused requests opened %v", sh.shown[n:])
	}

	// A daemon that reports a relative folder is not trusted with it either.
	d.status = &downexv1.StatusResponse{DataDir: "data"}
	d.settings = &downexv1.Settings{DefaultDir: "relative"}
	for _, kind := range []FolderKind{FolderData, FolderDownloads} {
		if err := f.OpenAppFolder(callCtx(t), kind); !hasPrefix(err, "failed_precondition: the "+string(kind)+" folder is not an absolute path") {
			t.Errorf("%s: %v, want a refusal of the relative folder", kind, err)
		}
	}
	if len(sh.shown) != n {
		t.Errorf("a relative folder was opened: %v", sh.shown[n:])
	}

	// A daemon that cannot be asked: the user's default folder (here a stand-in profile:
	// platform.LogDir creates its folder). The Downloads known folder cannot be redirected,
	// so the test leaves it alone.
	profile := t.TempDir()
	for _, v := range []string{"LOCALAPPDATA", "APPDATA", "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(v, filepath.Join(profile, v))
	}
	d.setErr(connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running")))
	for _, kind := range []FolderKind{FolderData, FolderLogs, FolderTools} {
		want, err := DefaultFolder(kind)
		if err != nil {
			t.Fatalf("DefaultFolder(%s): %v", kind, err)
		}
		if got, err := f.AppFolder(callCtx(t), kind); err != nil || got != filepath.Clean(want) {
			t.Errorf("AppFolder(%s) with the daemon down = %q, %v; want %q", kind, got, err, want)
		}
		if !strings.HasPrefix(want, profile) {
			t.Errorf("DefaultFolder(%s) = %q, outside the stand-in profile", kind, want)
		}
	}
}
