package engine

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"downex/internal/model"
	"downex/internal/testserver"
)

// TestReservationOutlivesDeletedPartFile: a paused download keeps its name reserved
// even when its part file was deleted, so a second download of the same name gets a
// variant instead of sharing (and corrupting) the first one's part file.
func TestReservationOutlivesDeletedPartFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 81, ETag: `"p"`, Throttle: 1 << 20}, resumeOpts())
	d1 := h.add("/f.bin", "f.bin", 2)
	if err := pauseMidway(t, h, d1.ID, 512<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	if err := os.Remove(partPath(h.dir, "f.bin")); err != nil {
		t.Fatal(err)
	}
	d2 := h.add("/f.bin", "f.bin", 2)
	if d2.Filename == d1.Filename {
		t.Fatalf("second download reserved %q, the name of a paused download", d2.Filename)
	}
	h.srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	if err := h.run(d2.ID, nil, nil); err != nil {
		t.Fatalf("Run d2: %v", err)
	}
	h.verify(d2.ID)
	if err := h.run(d1.ID, nil, nil); err != nil {
		t.Fatalf("Run d1: %v", err)
	}
	if got := h.verify(d1.ID); got.Filename != "f.bin" {
		t.Fatalf("d1 filename %q", got.Filename)
	}
}

// TestMissingPartFileIsReservedAgain: a stored name whose part file disappeared is
// reserved again (never recreated blindly), so a name now taken on disk is skipped.
func TestMissingPartFileIsReservedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 83, ETag: `"q"`, Throttle: 1 << 20}, resumeOpts())
	d := h.add("/g.bin", "g.bin", 2)
	if err := pauseMidway(t, h, d.ID, 256<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	if err := os.Remove(partPath(h.dir, "g.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "g.bin"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.verify(d.ID); got.Filename != "g (1).bin" {
		t.Fatalf("filename %q", got.Filename)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "g.bin")); string(b) != "user" {
		t.Fatal("the user's file was changed")
	}
}

// TestRenameNeverReplaces: completing a download must never replace a file that
// appeared at the destination (§2.5, F9), even between a check and the rename.
func TestRenameNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.bin"+partExt), filepath.Join(dir, "a.bin")
	if err := os.WriteFile(src, []byte("download"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(newMemStore(), nil, Options{})
	if err := e.renameWithRetry(context.Background(), src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rename onto an existing file = %v, want fs.ErrExist", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "user" {
		t.Fatalf("destination replaced: %q", b)
	}
	if b, _ := os.ReadFile(src); string(b) != "download" {
		t.Fatalf("source lost: %q", b)
	}
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if err := e.renameWithRetry(context.Background(), src, dst); err != nil {
		t.Fatalf("rename to a free name: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "download" {
		t.Fatalf("destination %q", b)
	}
}

// TestLinkRename covers the hard-link fallback used where an atomic no-replace rename
// is unavailable, including the state a crash between its two steps leaves behind.
func TestLinkRename(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "b.bin"+partExt), filepath.Join(dir, "b.bin")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(src, filepath.Join(dir, "probe-link")); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	if err := os.WriteFile(dst, []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := linkRename(src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("linkRename onto an existing file = %v", err)
	}
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if err := linkRename(src, dst); err != nil {
		t.Fatalf("linkRename: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}
	// A crash between link and remove leaves both names: promote finishes the move.
	if err := os.Link(dst, src); err != nil {
		t.Fatal(err)
	}
	e := New(newMemStore(), nil, Options{})
	name, err := e.promote(context.Background(), dir, "b.bin", 0, false, nil)
	if err != nil || name != "b.bin" {
		t.Fatalf("promote = %q, %v", name, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("part file still present: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "data" {
		t.Fatalf("final file %q", b)
	}
}

// crashStore loses every commit that marks a download completed, as if the process
// were killed right after the final rename.
type crashStore struct {
	*memStore
	crash atomic.Bool
}

var errSimulatedCrash = errors.New("simulated crash")

func (c *crashStore) SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error {
	if c.crash.Load() && d.State == model.StateCompleted {
		return errSimulatedCrash
	}
	return c.memStore.SaveCheckpoint(ctx, d, segs)
}

func (c *crashStore) UpdateDownload(ctx context.Context, d *model.Download) error {
	if c.crash.Load() && d.State == model.StateCompleted {
		return errSimulatedCrash
	}
	return c.memStore.UpdateDownload(ctx, d)
}

// TestRecoverAfterRenameCrashWithVariant: the reserved name was taken during the
// download, so the file was renamed to a variant; the process stopped before the
// completion commit. Recovery must find the variant, not adopt the other file.
func TestRecoverAfterRenameCrashWithVariant(t *testing.T) {
	t.Parallel()
	const size = 200_000
	srv := testserver.New(testserver.Options{Size: size, Seed: 84, ETag: `"c"`})
	t.Cleanup(srv.Close)
	st := &crashStore{memStore: newMemStore()}
	st.crash.Store(true)
	eng := New(st, &httpFactory{newTransport: srv.NewTransport}, fastOpts())
	dir := t.TempDir()
	d, err := eng.Add(context.Background(), model.Request{URL: srv.URL + "/report.pdf"}, dir, "report.pdf", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	user := bytes.Repeat([]byte{'u'}, size) // another report.pdf of the same size
	if err := os.WriteFile(filepath.Join(dir, "report.pdf"), user, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := eng.Run(context.Background(), d.ID, nil, nil); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("Run = %v, want the simulated crash", err)
	}
	st.crash.Store(false)
	before := len(srv.Requests())
	if err := eng.Run(context.Background(), d.ID, nil, nil); err != nil {
		t.Fatalf("Run after crash: %v", err)
	}
	r := st.get(t, d.ID)
	if r.State != model.StateCompleted || r.Filename != "report (1).pdf" {
		t.Fatalf("record %v %q, want completed \"report (1).pdf\"", r.State, r.Filename)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, r.Filename)); !bytes.Equal(got, testserver.Bytes(84, 0, size)) {
		t.Fatal("variant content mismatch")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "report.pdf")); !bytes.Equal(got, user) {
		t.Fatal("the other report.pdf was changed")
	}
	if n := len(srv.Requests()) - before; n != 0 {
		t.Fatalf("%d new requests after the crash", n)
	}
}

// TestRecoverSingleModeAfterRenameCrash: single-mode downloads cannot resume, but one
// that was already renamed must not be downloaded again into a duplicate.
func TestRecoverSingleModeAfterRenameCrash(t *testing.T) {
	t.Parallel()
	const size = 150_000
	srv := testserver.New(testserver.Options{Size: size, Seed: 85, NoRanges: true})
	t.Cleanup(srv.Close)
	st := &crashStore{memStore: newMemStore()}
	st.crash.Store(true)
	eng := New(st, &httpFactory{newTransport: srv.NewTransport}, fastOpts())
	dir := t.TempDir()
	d, err := eng.Add(context.Background(), model.Request{URL: srv.URL + "/single.dat"}, dir, "", 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Run(context.Background(), d.ID, nil, nil); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("Run = %v, want the simulated crash", err)
	}
	st.crash.Store(false)
	before := len(srv.Requests())
	if err := eng.Run(context.Background(), d.ID, nil, nil); err != nil {
		t.Fatalf("Run after crash: %v", err)
	}
	r := st.get(t, d.ID)
	if r.State != model.StateCompleted || r.Filename != "single.dat" || r.Size != size {
		t.Fatalf("record %+v", r)
	}
	if n := len(srv.Requests()) - before; n != 0 {
		t.Fatalf("%d new requests after the crash", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "single (1).dat")); !os.IsNotExist(err) {
		t.Fatalf("a duplicate was downloaded: %v", err)
	}
}
