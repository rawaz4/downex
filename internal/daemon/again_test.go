package daemon

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// Downloading a completed download again (docs/ENGINE-SPEC.md §7.1): ResumeRequest with
// restart and existing_file.

const (
	keepBoth    = downexv1.ExistingFile_EXISTING_FILE_KEEP_BOTH
	replaceFile = downexv1.ExistingFile_EXISTING_FILE_REPLACE
)

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func folderNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestDownloadAgainRules: a completed download is downloaded again only with restart;
// existing_file without restart is refused.
func TestDownloadAgainRules(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 1, ETag: `"a"`})
	h := newHarness(t, harnessOptions{})
	d := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/rules.bin"}).GetId())

	_, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId()}))
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("resume of a completed download without restart: %v, want failed_precondition", err)
	}
	for _, ef := range []downexv1.ExistingFile{keepBoth, replaceFile} {
		_, err = h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId(), ExistingFile: ef}))
		if connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("existing_file %v without restart: %v, want invalid_argument", ef, err)
		}
	}
	if got := h.get(d.GetId()); got.GetState() != stCompleted || got.GetFilename() != "rules.bin" {
		t.Fatalf("after the refusals = %v", got)
	}
}

// TestDownloadAgainKeepBoth: the new copy is saved as "name (1).ext"; the file stays
// byte for byte.
func TestDownloadAgainKeepBoth(t *testing.T) {
	const size = 256 << 10
	ts := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"a"`})
	h := newHarness(t, harnessOptions{})
	done := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/again.bin"}).GetId())
	path := filepath.Join(done.GetDir(), done.GetFilename())
	original := readBytes(t, path)

	ts.Update(func(o *testserver.Options) { o.Seed, o.ETag = 2, `"b"` })
	r := h.resume(&downexv1.ResumeRequest{Id: done.GetId(), Restart: true}) // unspecified = keep both
	if r.GetFilename() != "again (1).bin" || r.GetState() == stCompleted || r.GetCompletedAt() != nil {
		t.Fatalf("restarted = %v", r)
	}
	again := h.waitCompleted(done.GetId())
	if again.GetFilename() != "again (1).bin" {
		t.Fatalf("completed again as %q", again.GetFilename())
	}
	checkFile(t, again, 2, size)
	if got := readBytes(t, path); !bytes.Equal(got, original) {
		t.Fatal("keep both changed the original file")
	}

	r = h.resume(&downexv1.ResumeRequest{Id: done.GetId(), Restart: true, ExistingFile: keepBoth})
	if r.GetFilename() != "again (1) (1).bin" {
		t.Fatalf("second keep both = %q", r.GetFilename())
	}
	h.waitCompleted(done.GetId())
	if names := folderNames(t, done.GetDir()); !slices.Equal(names, []string{"again (1) (1).bin", "again (1).bin", "again.bin"}) {
		t.Fatalf("folder = %q", names)
	}
}

// TestDownloadAgainReplace: the file stays until the new copy is complete, which then
// takes its name; nothing else is left in the folder.
func TestDownloadAgainReplace(t *testing.T) {
	const size = 512 << 10
	ts := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"a"`})
	h := newHarness(t, harnessOptions{})
	done := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/movie.bin"}).GetId())
	path := filepath.Join(done.GetDir(), done.GetFilename())
	original := readBytes(t, path)

	ts.Update(func(o *testserver.Options) { o.Seed, o.ETag, o.Throttle = 2, `"b"`, 128<<10 })
	r := h.resume(&downexv1.ResumeRequest{Id: done.GetId(), Restart: true, ExistingFile: replaceFile})
	if r.GetFilename() != "movie.bin" || r.GetCompletedAt() != nil {
		t.Fatalf("restarted = %v", r)
	}
	if rec := storedState(t, h.st, done.GetId()); !rec.ReplaceFile {
		t.Fatal("the stored record does not replace its file")
	}
	h.waitReceived(done.GetId(), 32<<10)
	if got := readBytes(t, path); !bytes.Equal(got, original) {
		t.Fatal("the file changed before its replacement was complete")
	}
	ts.Update(func(o *testserver.Options) { o.Throttle = 0 })
	again := h.waitCompleted(done.GetId())
	if again.GetFilename() != "movie.bin" {
		t.Fatalf("completed as %q", again.GetFilename())
	}
	checkFile(t, again, 2, size)
	if rec := storedState(t, h.st, done.GetId()); rec.ReplaceFile {
		t.Fatal("the completed record still replaces its file")
	}
	if names := folderNames(t, done.GetDir()); !slices.Equal(names, []string{"movie.bin"}) {
		t.Fatalf("folder = %q", names)
	}
}

// TestDownloadAgainReplaceFailureKeepsFile: a replacement that fails half way (the server
// answers 404) leaves the file untouched and keeps the choice for the next resume.
func TestDownloadAgainReplaceFailureKeepsFile(t *testing.T) {
	const size = 512 << 10
	ts := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"a"`})
	h := newHarness(t, harnessOptions{})
	done := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/keep.bin", MaxConnections: 2}).GetId())
	path := filepath.Join(done.GetDir(), done.GetFilename())
	original := readBytes(t, path)

	ts.Update(func(o *testserver.Options) { o.Seed, o.ETag, o.Throttle = 2, `"b"`, 64<<10 })
	h.resume(&downexv1.ResumeRequest{Id: done.GetId(), Restart: true, ExistingFile: replaceFile})
	h.waitReceived(done.GetId(), 16<<10)
	h.pause(done.GetId())
	ts.Update(func(o *testserver.Options) { o.FailFirst, o.FailStatus, o.Throttle = 1<<30, 404, 0 })
	h.resume(&downexv1.ResumeRequest{Id: done.GetId()})
	failed := h.waitState(done.GetId(), stFailed)
	if failed.GetFilename() != "keep.bin" {
		t.Fatalf("failed = %v", failed)
	}
	if got := readBytes(t, path); !bytes.Equal(got, original) {
		t.Fatal("a failed replacement changed the file")
	}
	if rec := storedState(t, h.st, done.GetId()); !rec.ReplaceFile {
		t.Fatal("the failed record forgot that it replaces its file")
	}

	ts.Update(func(o *testserver.Options) { o.FailFirst = 0 })
	h.resume(&downexv1.ResumeRequest{Id: done.GetId()})
	again := h.waitCompleted(done.GetId())
	if again.GetFilename() != "keep.bin" {
		t.Fatalf("completed as %q", again.GetFilename())
	}
	checkFile(t, again, 2, size)
}

// TestDownloadAgainReplaceNeverOverwritesOther: when the file became a folder or a link
// while its replacement downloaded, the new copy takes another name and the folder or
// link stays as it is.
func TestDownloadAgainReplaceNeverOverwritesOther(t *testing.T) {
	for _, kind := range []string{"folder", "link"} {
		t.Run(kind, func(t *testing.T) {
			const size = 256 << 10
			ts := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"a"`})
			h := newHarness(t, harnessOptions{})
			done := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/x.bin"}).GetId())
			path := filepath.Join(done.GetDir(), done.GetFilename())

			ts.Update(func(o *testserver.Options) { o.Seed, o.ETag, o.Throttle = 2, `"b"`, 64<<10 })
			h.resume(&downexv1.ResumeRequest{Id: done.GetId(), Restart: true, ExistingFile: replaceFile})
			h.waitReceived(done.GetId(), 16<<10)
			h.pause(done.GetId())

			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target.bin")
			if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "folder":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "inside.txt"), []byte("inside"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("no symbolic links here: %v", err)
				}
			}
			ts.Update(func(o *testserver.Options) { o.Throttle = 0 })
			h.resume(&downexv1.ResumeRequest{Id: done.GetId()})
			again := h.waitCompleted(done.GetId())
			if again.GetFilename() != "x (1).bin" {
				t.Fatalf("completed as %q, want x (1).bin", again.GetFilename())
			}
			checkFile(t, again, 2, size)
			fi, err := os.Lstat(path)
			switch {
			case err != nil:
				t.Fatalf("the %s is gone: %v", kind, err)
			case kind == "folder" && !fi.IsDir():
				t.Fatal("the folder was replaced")
			case kind == "link" && fi.Mode()&os.ModeSymlink == 0:
				t.Fatal("the link was replaced")
			}
			if got := readBytes(t, target); string(got) != "target" {
				t.Fatalf("the link's target = %q", got)
			}
		})
	}
}

// TestDownloadAgainMedia: a completed media download downloads again from no checkpoint;
// keep both runs under a free name, replace under its own with ReplaceExisting, which
// the job honours (MediaRunOptions).
func TestDownloadAgainMedia(t *testing.T) {
	fm := newFakeMedia(3)
	h := newHarness(t, harnessOptions{media: fm})
	dir := t.TempDir()
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "movie"})
	h.waitCompleted(d.GetId())
	out := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(out, []byte("the first copy"), 0o644); err != nil { // tell it apart from the next copies
		t.Fatal(err)
	}

	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true, ExistingFile: keepBoth})
	if r.GetFilename() != "movie (1).mp4" || r.GetReceived() != 0 || r.GetMedia().GetFragmentsDone() != 0 {
		t.Fatalf("restarted (keep both) = %v", r)
	}
	both := h.waitCompleted(d.GetId())
	if both.GetFilename() != "movie (1).mp4" {
		t.Fatalf("completed as %q", both.GetFilename())
	}
	if got := readBytes(t, out); string(got) != "the first copy" {
		t.Fatalf("keep both changed the file: %q", got)
	}
	_, runs := fm.calls()
	last := runs[len(runs)-1]
	if last.resume != nil || last.opts.OutPath != filepath.Join(dir, "movie (1).mp4") || last.opts.ReplaceExisting {
		t.Fatalf("keep-both run = %+v", last)
	}

	kept := filepath.Join(dir, "movie (1).mp4")
	// Its own file (the size it completed with), told apart from the new copy.
	if err := os.WriteFile(kept, bytes.Repeat([]byte{'x'}, len(wantOutput(3))), 0o644); err != nil {
		t.Fatal(err)
	}
	r = h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true, ExistingFile: replaceFile})
	if r.GetFilename() != "movie (1).mp4" {
		t.Fatalf("restarted (replace) = %v", r)
	}
	replaced := h.waitCompleted(d.GetId())
	if replaced.GetFilename() != "movie (1).mp4" {
		t.Fatalf("completed as %q", replaced.GetFilename())
	}
	if got := readBytes(t, kept); !bytes.Equal(got, wantOutput(3)) {
		t.Fatalf("the file was not replaced: %q", got)
	}
	_, runs = fm.calls()
	last = runs[len(runs)-1]
	if last.resume != nil || last.opts.OutPath != kept || !last.opts.ReplaceExisting {
		t.Fatalf("replace run = %+v", last)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.ReplaceFile {
		t.Fatal("the completed record still replaces its file")
	}
	if names := folderNames(t, dir); !slices.Equal(names, []string{"movie (1).mp4", "movie.mp4"}) {
		t.Fatalf("folder = %q", names)
	}
}

// TestDownloadAgainSite: a completed video of a web page runs again from the page (the
// job resolves it afresh, with fresh addresses), without a checkpoint.
func TestDownloadAgainSite(t *testing.T) {
	fs := newFakeSite(3)
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	d := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage})
	done := h.waitCompleted(d.GetId())
	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true})
	if r.GetFilename() != "A video title (1).mp4" {
		t.Fatalf("restarted = %v", r)
	}
	again := h.waitCompleted(d.GetId())
	_, runs := fs.calls()
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2", len(runs))
	}
	if runs[1].sel.PageURL != testPage || runs[1].resume != nil ||
		runs[1].opts.OutPath != filepath.Join(again.GetDir(), "A video title (1).mp4") {
		t.Fatalf("second run = %+v", runs[1])
	}
	if _, err := os.Stat(filepath.Join(done.GetDir(), done.GetFilename())); err != nil {
		t.Fatalf("the first copy: %v", err)
	}
}

// TestStatusFolders: Status names the daemon's folders (absolute) and whether this build
// can install updates.
func TestStatusFolders(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "data", SettingsFileName)
	h := newHarness(t, harnessOptions{settingsPath: settingsPath})
	s := h.status()
	if s.GetDataDir() != filepath.Join(dir, "data") || s.GetLogDir() != "" || s.GetToolsDir() != "" || s.GetUpdatesSupported() {
		t.Fatalf("status = %v", s)
	}
	if s.GetEndpoint() != "test-endpoint" || s.GetVersion() != "test-version" {
		t.Fatalf("status = %v", s)
	}

	h.m.cfg.LogDir, h.m.cfg.ToolsDir = filepath.Join(dir, "logs"), filepath.Join(dir, "tools")
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := StartUpdates(h.m, UpdatesConfig{PublicKey: base64.StdEncoding.EncodeToString(pub)})
	defer stop()
	s = h.status()
	if s.GetLogDir() != filepath.Join(dir, "logs") || s.GetToolsDir() != filepath.Join(dir, "tools") || !s.GetUpdatesSupported() {
		t.Fatalf("status = %v", s)
	}
	for _, p := range []string{s.GetDataDir(), s.GetLogDir(), s.GetToolsDir()} {
		if !filepath.IsAbs(p) {
			t.Errorf("%q is not absolute", p)
		}
	}

	// A relative folder is never reported (only an absolute path can be opened safely).
	h.m.cfg.LogDir = "logs"
	if s = h.status(); s.GetLogDir() != "" {
		t.Fatalf("a relative log folder is reported: %q", s.GetLogDir())
	}
}

// TestDownloadAgainReplaceOnlyItsOwnFile: Replace replaces only a file that is plausibly
// the download's own: no other download (completed ones included) names it, and it has
// the size the download completed with. Otherwise the new copy keeps both, and the file
// there stays as it is.
func TestDownloadAgainReplaceOnlyItsOwnFile(t *testing.T) {
	const size = 128 << 10
	t.Run("another download's file", func(t *testing.T) {
		tsA := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"a"`})
		tsB := serve(t, testserver.Options{Size: size, Seed: 7, ETag: `"b"`})
		h := newHarness(t, harnessOptions{})
		a := h.waitCompleted(h.add(&downexv1.AddRequest{Url: tsA.URL + "/setup.bin"}).GetId())
		if err := os.Remove(filepath.Join(a.GetDir(), a.GetFilename())); err != nil {
			t.Fatal(err)
		}
		// B (another address) completes under the name A's missing file left free.
		b := h.waitCompleted(h.add(&downexv1.AddRequest{Url: tsB.URL + "/x/setup.bin"}).GetId())
		if b.GetFilename() != a.GetFilename() || b.GetDir() != a.GetDir() {
			t.Fatalf("setup: B = %s in %s, A = %s in %s", b.GetFilename(), b.GetDir(), a.GetFilename(), a.GetDir())
		}
		r := h.resume(&downexv1.ResumeRequest{Id: a.GetId(), Restart: true, ExistingFile: replaceFile})
		if r.GetFilename() != "setup (1).bin" {
			t.Errorf("restarted as %q, want setup (1).bin (keep both)", r.GetFilename())
		}
		if rec := storedState(t, h.st, a.GetId()); rec.ReplaceFile {
			t.Error("A replaces B's file")
		}
		again := h.waitCompleted(a.GetId())
		checkFile(t, again, 1, size)
		checkFile(t, h.get(b.GetId()), 7, size) // B's file is still B's
	})
	t.Run("a file of another size", func(t *testing.T) {
		ts := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"a"`})
		h := newHarness(t, harnessOptions{})
		a := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/notes.bin"}).GetId())
		path := filepath.Join(a.GetDir(), a.GetFilename())
		if err := os.WriteFile(path, []byte("the user's own notes"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := h.resume(&downexv1.ResumeRequest{Id: a.GetId(), Restart: true, ExistingFile: replaceFile})
		if r.GetFilename() != "notes (1).bin" {
			t.Errorf("restarted as %q, want notes (1).bin (keep both)", r.GetFilename())
		}
		h.waitCompleted(a.GetId())
		if got := readBytes(t, path); string(got) != "the user's own notes" {
			t.Fatalf("the file was replaced (%d bytes)", len(got))
		}
	})
	t.Run("media, another download's file", func(t *testing.T) {
		fm := newFakeMedia(3)
		h := newHarness(t, harnessOptions{media: fm})
		dir := t.TempDir()
		a := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "movie"})
		h.waitCompleted(a.GetId())
		out := filepath.Join(dir, "movie.mp4")
		if err := os.Remove(out); err != nil {
			t.Fatal(err)
		}
		b := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest + "?b", Dir: dir, Filename: "movie"})
		if b = h.waitCompleted(b.GetId()); b.GetFilename() != "movie.mp4" {
			t.Fatalf("setup: B completed as %q", b.GetFilename())
		}
		other := bytes.Repeat([]byte{'b'}, len(wantOutput(3))) // B's file, A's size
		if err := os.WriteFile(out, other, 0o644); err != nil {
			t.Fatal(err)
		}
		r := h.resume(&downexv1.ResumeRequest{Id: a.GetId(), Restart: true, ExistingFile: replaceFile})
		if r.GetFilename() != "movie (1).mp4" {
			t.Errorf("restarted as %q, want movie (1).mp4 (keep both)", r.GetFilename())
		}
		h.waitCompleted(a.GetId())
		if got := readBytes(t, out); !bytes.Equal(got, other) {
			t.Fatal("A's new copy replaced B's file")
		}
	})
}
