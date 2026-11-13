package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func idArg(id int64) json.RawMessage { return json.RawMessage(fmt.Sprintf(`{"id":%d}`, id)) }

func TestOpenFileOpensCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "report.pdf"))
	h := &fakeHost{}
	a, sh := newTestApp(t, newFakeDaemon(completedIn(dir, 1, "report.pdf")), h)
	if err := a.OpenFile(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "report.pdf")}; !reflect.DeepEqual(sh.opened, want) {
		t.Errorf("opened %v, want %v", sh.opened, want)
	}
	if len(h.confirms) != 0 {
		t.Errorf("a document was confirmed: %q", h.confirms)
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
	f := newFakeDaemon(
		running,
		completedIn(dir, 2, "gone.zip"),
		completedIn("relative", 3, "a.zip"),
		completedIn(dir, 4, ".."),
		completedIn(dir, 5, `sub`+string(filepath.Separator)+`a.zip`),
		completedIn(dir, 6, "folder.zip"),
		completedIn(dir, 7, ""),
		completedIn(dir, 8, "nul\x00.zip"),
	)
	h := &fakeHost{answer: true}
	a, sh := newTestApp(t, f, h)
	// Open with… makes the same checks as Open before the system's dialog appears.
	for _, open := range []struct {
		name string
		fn   func(json.RawMessage) error
	}{{"OpenFile", a.OpenFile}, {"OpenWith", a.OpenWith}} {
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
			err := open.fn(idArg(tc.id))
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("%s(%d) = %v, want %q", open.name, tc.id, err, tc.want)
			}
		}
		if err := open.fn(json.RawMessage(`{"id":`)); err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: malformed request") {
			t.Errorf("%s with a malformed request: %v", open.name, err)
		}
	}
	if len(sh.opened) != 0 || len(sh.openedWith) != 0 {
		t.Errorf("opened %v, opened with %v", sh.opened, sh.openedWith)
	}
	if len(h.confirms) != 0 {
		t.Errorf("refused files were confirmed: %q", h.confirms)
	}
}

func TestOpenWithChoosesAnAppForCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "report.pdf"))
	h := &fakeHost{}
	a, sh := newTestApp(t, newFakeDaemon(completedIn(dir, 1, "report.pdf")), h)
	if err := a.OpenWith(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "report.pdf")}; !reflect.DeepEqual(sh.openedWith, want) {
		t.Errorf("opened with %v, want %v", sh.openedWith, want)
	}
	if len(sh.opened) != 0 {
		t.Errorf("the default app opened %v", sh.opened)
	}
	if len(h.confirms) != 0 {
		t.Errorf("a document was confirmed: %q", h.confirms)
	}
}

// The app the user picks may run the file: programs and active content are confirmed
// first, as for Open.
func TestOpenWithConfirmsProgramsAndActiveContent(t *testing.T) {
	dir := t.TempDir()
	names := []string{"setup.exe", "run.ps1", "invoice.rdp", "budget.docm", "invoice‮fdp.exe"}
	var ds []*downexv1.Download
	for i, n := range names {
		writeFile(t, filepath.Join(dir, n))
		ds = append(ds, completedIn(dir, int64(i+1), n))
	}
	h := &fakeHost{answer: false}
	a, sh := newTestApp(t, newFakeDaemon(ds...), h)
	for _, d := range ds {
		if err := a.OpenWith(idArg(d.GetId())); err != nil {
			t.Errorf("declined %q: %v, want no error", d.GetFilename(), err)
		}
	}
	if len(sh.openedWith) != 0 || len(sh.opened) != 0 {
		t.Fatalf("declined files were opened: with %v, default %v", sh.openedWith, sh.opened)
	}
	if len(h.confirms) != len(ds) {
		t.Fatalf("%d confirmations for %d files: %q", len(h.confirms), len(ds), h.confirms)
	}
	if q := h.confirms[0]; !strings.HasPrefix(q, "Run this program?\nsetup.exe is a program or script.") {
		t.Errorf("question about a program = %q", q)
	}
	if q := h.confirms[2]; !strings.HasPrefix(q, "Open this file?\ninvoice.rdp is a type of file that can run code") {
		t.Errorf("question about a remote desktop connection = %q", q)
	}
	if q := h.confirms[4]; !strings.Contains(q, "invoice⟨U+202E⟩fdp.exe") || strings.ContainsRune(q, '‮') {
		t.Errorf("question about a disguised name = %q", q)
	}

	h.mu.Lock()
	h.answer = true
	h.mu.Unlock()
	if err := a.OpenWith(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "setup.exe")}; !reflect.DeepEqual(sh.openedWith, want) {
		t.Errorf("after yes: opened with %v, want %v", sh.openedWith, want)
	}

	h.mu.Lock()
	h.confirmErr = errors.New("no dialog")
	h.mu.Unlock()
	if err := a.OpenWith(idArg(1)); err == nil || !strings.HasPrefix(err.Error(), "internal: confirmation dialog: no dialog") {
		t.Errorf("failed dialog: %v", err)
	}
	if len(sh.openedWith) != 1 {
		t.Errorf("a program was opened without an answer: %v", sh.openedWith)
	}

	// Without a window there is nobody to ask: nothing opens.
	idle := newApp(newFakeDaemon(ds...), sh, nil)
	if err := idle.OpenWith(idArg(1)); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Errorf("OpenWith of a program before start-up: %v", err)
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
	h := &fakeHost{answer: false}
	a, sh := newTestApp(t, newFakeDaemon(completedIn(dir, 1, "notes.txt")), h)
	if err := a.OpenWith(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if len(h.confirms) != 1 || len(sh.openedWith) != 0 {
		t.Fatalf("a link to a program: confirms %q, opened with %v", h.confirms, sh.openedWith)
	}
}

func TestOpenWithShellResults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"))
	a, sh := newTestApp(t, newFakeDaemon(completedIn(dir, 1, "a.txt")), &fakeHost{})
	path := filepath.Join(dir, "a.txt")
	for _, tc := range []struct {
		err  error
		want string
	}{
		// The user closed the dialog: the page stays as it is.
		{connect.NewError(connect.CodeCanceled, errors.New("no app was chosen")), "canceled: no app was chosen"},
		// A system without the dialog (Linux, for now): the page says so.
		{connect.NewError(connect.CodeUnimplemented, errors.New("not available on this system yet")), "unimplemented: not available on this system yet"},
		{errors.New("SHOpenWithDialog: HRESULT 0x80004005"), "internal: cannot open " + path + ": SHOpenWithDialog: HRESULT 0x80004005"},
	} {
		sh.mu.Lock()
		sh.withErr = tc.err
		sh.mu.Unlock()
		if err := a.OpenWith(idArg(1)); err == nil || err.Error() != tc.want {
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
	f := newFakeDaemon(ds...)
	h := &fakeHost{answer: false}
	a, sh := newTestApp(t, f, h)

	for _, d := range ds {
		if err := a.OpenFile(idArg(d.GetId())); err != nil {
			t.Errorf("declined %q: %v, want no error", d.GetFilename(), err)
		}
	}
	if len(sh.opened) != 0 {
		t.Errorf("declined programs were opened: %v", sh.opened)
	}
	if len(h.confirms) != len(ds) {
		t.Fatalf("%d confirmations for %d programs: %q", len(h.confirms), len(ds), h.confirms)
	}
	if q := h.confirms[0]; !strings.HasPrefix(q, "Run this program?\nsetup.exe is a program or script.") || !strings.Contains(q, "Folder: "+dir) {
		t.Errorf("question = %q", q)
	}
	// Bidi controls that disguise the type are shown, not applied.
	last := h.confirms[len(h.confirms)-1]
	if !strings.Contains(last, "invoice⟨U+202E⟩fdp.exe") || strings.ContainsRune(last, '\u202e') {
		t.Errorf("question about a disguised name = %q", last)
	}

	h.mu.Lock()
	h.answer = true
	h.mu.Unlock()
	if err := a.OpenFile(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "setup.exe")}; !reflect.DeepEqual(sh.opened, want) {
		t.Errorf("after yes: opened %v, want %v", sh.opened, want)
	}

	h.mu.Lock()
	h.confirmErr = errors.New("no dialog")
	h.mu.Unlock()
	if err := a.OpenFile(idArg(1)); err == nil || !strings.HasPrefix(err.Error(), "internal: confirmation dialog: no dialog") {
		t.Errorf("failed dialog: %v", err)
	}
	if len(sh.opened) != 1 {
		t.Errorf("a program was opened without an answer: %v", sh.opened)
	}
}

// Downloads carry no Mark-of-the-Web yet (U6), so neither the system nor Office warns
// before such a file runs its content: Downex asks first.
func TestOpenFileConfirmsActiveContent(t *testing.T) {
	dir := t.TempDir()
	names := []string{"invoice.rdp", "report.theme", "fix.diagcab", "tool.xll", "disk.iso", "budget.docm"}
	var ds []*downexv1.Download
	for i, n := range names {
		writeFile(t, filepath.Join(dir, n))
		ds = append(ds, completedIn(dir, int64(i+1), n))
	}
	h := &fakeHost{answer: false}
	a, sh := newTestApp(t, newFakeDaemon(ds...), h)
	for _, d := range ds {
		if err := a.OpenFile(idArg(d.GetId())); err != nil {
			t.Errorf("declined %q: %v, want no error", d.GetFilename(), err)
		}
	}
	if len(sh.opened) != 0 {
		t.Errorf("declined files were opened: %v", sh.opened)
	}
	if len(h.confirms) != len(names) {
		t.Fatalf("%d confirmations for %d files: %q", len(h.confirms), len(names), h.confirms)
	}
	if q := h.confirms[0]; !strings.HasPrefix(q, "Open this file?\ninvoice.rdp is a type of file that can run code or connect to other computers when it is opened") {
		t.Errorf("question about a remote desktop connection = %q", q)
	}
	// A plug-in that Excel loads as code is a program.
	if q := h.confirms[3]; !strings.HasPrefix(q, "Run this program?\ntool.xll is a program or script.") {
		t.Errorf("question about an Excel add-in = %q", q)
	}
}

func TestOpenFileFollowsLinksToPrograms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tool.exe"))
	if err := os.Symlink(filepath.Join(dir, "tool.exe"), filepath.Join(dir, "notes.txt")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	h := &fakeHost{answer: false}
	a, sh := newTestApp(t, newFakeDaemon(completedIn(dir, 1, "notes.txt")), h)
	if err := a.OpenFile(idArg(1)); err != nil {
		t.Fatal(err)
	}
	if len(h.confirms) != 1 || len(sh.opened) != 0 {
		t.Fatalf("a link to a program: confirms %q, opened %v", h.confirms, sh.opened)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(dir, "tool.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if q := h.confirms[0]; !strings.Contains(q, "notes.txt (a link to "+target+") is a program") {
		t.Errorf("question = %q", q)
	}
}

func TestOpenFileShellError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"))
	a, sh := newTestApp(t, newFakeDaemon(completedIn(dir, 1, "a.txt")), &fakeHost{})
	sh.err = errors.New("access is denied")
	err := a.OpenFile(idArg(1))
	if want := "internal: cannot open " + filepath.Join(dir, "a.txt") + ": access is denied"; err == nil || err.Error() != want {
		t.Errorf("OpenFile = %v, want %q", err, want)
	}
}

func TestOpenFolder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "done.zip"))
	writeFile(t, filepath.Join(dir, "half.zip"+partSuffix))
	running := completedIn(dir, 2, "half.zip")
	running.State = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	f := newFakeDaemon(
		completedIn(dir, 1, "done.zip"),
		running,
		completedIn(dir, 3, "moved.zip"),
		completedIn(dir, 4, ""), // not probed yet
		completedIn(filepath.Join(dir, "missing"), 5, "a.zip"),
		completedIn("relative", 6, "a.zip"),
	)
	a, sh := newTestApp(t, f, &fakeHost{})
	for id := int64(1); id <= 4; id++ {
		if err := a.OpenFolder(idArg(id)); err != nil {
			t.Errorf("OpenFolder(%d): %v", id, err)
		}
	}
	want := []string{
		"reveal:" + filepath.Join(dir, "done.zip"),
		"reveal:" + filepath.Join(dir, "half.zip"+partSuffix),
		"folder:" + dir,
		"folder:" + dir,
	}
	if !reflect.DeepEqual(sh.shown, want) {
		t.Errorf("shown %q, want %q", sh.shown, want)
	}
	if err := a.OpenFolder(idArg(5)); err == nil || !strings.HasPrefix(err.Error(), "not_found: "+filepath.Join(dir, "missing")+" does not exist") {
		t.Errorf("missing folder: %v", err)
	}
	if err := a.OpenFolder(idArg(6)); err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: download 6 has no absolute folder") {
		t.Errorf("relative folder: %v", err)
	}
}

func TestPickFolder(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHost{picked: `C:\Chosen`}
	a, _ := newTestApp(t, newFakeDaemon(), h)
	got, err := a.PickFolder(json.RawMessage(fmt.Sprintf(`{"initialDir":%q}`, filepath.Join(dir, "not", "yet"))))
	if err != nil || got != `C:\Chosen` {
		t.Fatalf("PickFolder = %q, %v", got, err)
	}
	h.picked = ""
	if got, err := a.PickFolder(json.RawMessage(`{"initialDir":"relative"}`)); err != nil || got != "" {
		t.Fatalf("cancelled PickFolder = %q, %v", got, err)
	}
	if got, err := a.PickFolder(json.RawMessage(`{"initialDir":""}`)); err != nil || got != "" {
		t.Fatalf("PickFolder without a folder = %q, %v", got, err)
	}
	// A folder that does not exist starts at its nearest existing parent.
	if want := []string{dir, "", ""}; !reflect.DeepEqual(h.picks, want) {
		t.Errorf("starting folders %q, want %q", h.picks, want)
	}

	idle := newApp(newFakeDaemon(), &fakeShell{}, nil)
	if _, err := idle.PickFolder(json.RawMessage(`{"initialDir":""}`)); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Errorf("PickFolder before start-up: %v", err)
	}
}

func TestRiskOf(t *testing.T) {
	for name, want := range map[string]risk{
		"a.exe": riskProgram, "A.EXE": riskProgram, "a.exe.": riskProgram, "a.exe . ": riskProgram,
		"x.tar.gz.exe": riskProgram, "a.Msi": riskProgram, "a.ps1": riskProgram, "a.url": riskProgram,
		"a.desktop": riskProgram, "a.appimage": riskProgram, "a.deb": riskProgram,
		"fix.diagcab": riskProgram, "tool.xll": riskProgram, "a.msh1xml": riskProgram, "a.psc2": riskProgram,
		"a.vsto": riskProgram, "a.msu": riskProgram,
		// Types whose application runs code they contain, mounts them or connects out.
		"invoice.rdp": riskContent, "report.theme": riskContent, "a.iso": riskContent, "a.docm": riskContent,
		"a.XLSM": riskContent, "a.wsb": riskContent, "a.searchconnector-ms": riskContent, "a.vhdx": riskContent,
		"a.accde": riskContent, "a.iqy": riskContent, "a.slk": riskContent, "a.ins": riskContent,
		"a.pdf": riskNone, "a.zip": riskNone, "a.exe.txt": riskNone, "exe": riskNone, ".exe.mp4": riskNone,
		"a": riskNone, "a.docx": riskNone, "a.xlsx": riskNone, "a.png": riskNone,
	} {
		if got := riskOf(name, nil); got != want {
			t.Errorf("riskOf(%q) = %v, want %v", name, got, want)
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
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartSuffixMatchesTheEngine(t *testing.T) {
	dir := t.TempDir()
	if got, want := filepath.Join(dir, "a.bin"+partSuffix), engine.PartPath(dir, "a.bin"); got != want {
		t.Errorf("part file %q, the engine writes %q", got, want)
	}
}
