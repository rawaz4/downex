package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
)

// writePartial writes the first n bytes of the server's content into dir/name, as a
// browser leaves them, after edit (if any) changed them, and returns the path.
func writePartial(t *testing.T, h *harness, name string, n int64, edit func([]byte)) string {
	t.Helper()
	data := testserver.Content(h.srv.Options())[:n]
	data = bytes.Clone(data)
	if edit != nil {
		edit(data)
	}
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// rescueReader is a fixed source for the random windows.
func rescueReader() io.Reader {
	return rand.NewChaCha8([32]byte{'r', 'e', 's', 'c', 'u', 'e'})
}

func (h *harness) rescue(in RescueInput) (*RescueResult, error) {
	h.t.Helper()
	if in.Request.URL == "" {
		in.Request.URL = h.srv.URL + "/file"
	}
	if in.rand == nil {
		in.rand = rescueReader()
	}
	ctx, cancel := runContext(h.t)
	defer cancel()
	return h.eng.Rescue(ctx, in)
}

// ranges returns the Range headers of the requests the server got from index from on.
func ranges(srv *testserver.Server, from int) []string {
	var out []string
	for _, r := range srv.Requests()[from:] {
		out = append(out, r.Range)
	}
	return out
}

func rangeStart(t *testing.T, rng string) int64 {
	t.Helper()
	spec, ok := strings.CutPrefix(rng, "bytes=")
	a, _, ok2 := strings.Cut(spec, "-")
	n, err := strconv.ParseInt(a, 10, 64)
	if !ok || !ok2 || err != nil {
		t.Fatalf("range %q", rng)
	}
	return n
}

// fileState is what a refused rescue must leave unchanged.
type fileState struct {
	size  int64
	mtime time.Time
	data  []byte
}

func stateOf(t *testing.T, path string) fileState {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileState{fi.Size(), fi.ModTime(), data}
}

// checkUntouched asserts that a refused rescue changed nothing: the partial, the store
// and the folder (no part file).
func checkUntouched(t *testing.T, h *harness, partial string, was fileState) {
	t.Helper()
	now := stateOf(t, partial)
	if now.size != was.size || !now.mtime.Equal(was.mtime) || !bytes.Equal(now.data, was.data) {
		t.Fatalf("the partial changed: %d bytes, mtime %v (was %d, %v)", now.size, now.mtime, was.size, was.mtime)
	}
	if list, _ := h.st.ListDownloads(context.Background()); len(list) != 0 {
		t.Fatalf("records left: %+v", list)
	}
	parts, _ := filepath.Glob(filepath.Join(h.dir, "*"+partExt))
	if len(parts) != 0 {
		t.Fatalf("part files left: %v", parts)
	}
}

// TestRescueHappyPath is §7.4 with a strong entity tag known: the windows lie inside
// the partial (the last one first, no If-Range), the run fetches only the rest, and the
// file is byte-exact.
func TestRescueHappyPath(t *testing.T) {
	t.Parallel()
	const size = 8 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 71, ETag: `"r1"`, Filename: "server.iso"}, resumeOpts())
	kept := int64(size * 4 / 10)
	partial := writePartial(t, h, "disk.iso.crdownload", kept, nil)

	res, err := h.rescue(RescueInput{Partial: partial, ExpectedSize: size, Validators: model.Validators{ETag: `"r1"`, StrongETag: true}})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	reqs := h.srv.Requests()
	if len(reqs) == 0 || len(reqs) > ProofWindowsVouched {
		t.Fatalf("%d verification requests, want 1..%d (vouched)", len(reqs), ProofWindowsVouched)
	}
	if want := "bytes=" + strconv.FormatInt(kept-ProofWindowBytes, 10) + "-" + strconv.FormatInt(kept-1, 10); reqs[0].Range != want {
		t.Fatalf("first request %q, want the last window %q", reqs[0].Range, want)
	}
	for _, r := range reqs {
		if r.IfRange != "" {
			t.Fatalf("verification sent If-Range %q", r.IfRange)
		}
		if rangeStart(t, r.Range) >= kept {
			t.Fatalf("window %q lies beyond the partial's %d bytes", r.Range, kept)
		}
	}
	d := res.Download
	if res.Kept != kept || d.RescuedBytes != kept || d.State != model.StateQueued || d.Mode != model.ModeRanged ||
		d.Size != size || d.Filename != "disk.iso" || d.Dir != h.dir || !res.ValidatorsMatched || d.RemoteName != "server.iso" {
		t.Fatalf("result %+v, download %+v", res, d)
	}
	if _, err := os.Lstat(partial); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the partial is still there: %v", err)
	}
	segs := h.st.segs(d.ID)
	if len(segs) != 1 || segs[0].Start != 0 || segs[0].End != size-1 || segs[0].Durable != kept {
		t.Fatalf("segments %+v", segs)
	}

	before := len(h.srv.Requests())
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := h.verify(d.ID)
	if got.RescuedBytes != kept {
		t.Fatalf("RescuedBytes after completion = %d", got.RescuedBytes)
	}
	for _, rng := range ranges(h.srv, before) {
		if rangeStart(t, rng) < kept {
			t.Fatalf("the run asked for %q, below the %d kept bytes", rng, kept)
		}
	}
}

// TestRescueWindowsWithoutValidators: no validator known, so the sampled proof compares
// its full set of windows: the last one, the first one, then random ones.
func TestRescueWindowsWithoutValidators(t *testing.T) {
	t.Parallel()
	const size = 8 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 72}, resumeOpts())
	kept := int64(3 << 20)
	partial := writePartial(t, h, "a.bin.crdownload", kept, nil)
	res, err := h.rescue(RescueInput{Partial: partial})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	reqs := ranges(h.srv, 0)
	if len(reqs) != ProofWindowsUnvouched || res.Windows != ProofWindowsUnvouched || res.ValidatorsMatched {
		t.Fatalf("%d requests (%v), result %+v; want %d windows", len(reqs), reqs, res, ProofWindowsUnvouched)
	}
	if reqs[0] != "bytes=3080192-3145727" || reqs[1] != "bytes=0-65535" {
		t.Fatalf("first windows %v, want the last then the first", reqs[:2])
	}
	if err := h.run(res.Download.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.verify(res.Download.ID)
}

// TestRescueSmallPartialFullProof: a partial of at most 1 MiB is compared in full, in
// one request.
func TestRescueSmallPartialFullProof(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 3 << 20, Seed: 73}, resumeOpts())
	partial := writePartial(t, h, "s.bin.part", 700_000, nil)
	res, err := h.rescue(RescueInput{Partial: partial})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	if reqs := ranges(h.srv, 0); len(reqs) != 1 || reqs[0] != "bytes=0-699999" || res.Compared != 700_000 {
		t.Fatalf("requests %v, compared %d", reqs, res.Compared)
	}
	if res.Download.Filename != "s.bin" {
		t.Fatalf("name %q", res.Download.Filename)
	}
	if err := h.run(res.Download.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.verify(res.Download.ID)
}

// TestRescueRefusals: every refusal leaves the partial, the store and the folder as they
// were.
func TestRescueRefusals(t *testing.T) {
	t.Parallel()
	const size = 4 << 20
	lm := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		opts  testserver.Options
		in    RescueInput
		edit  func([]byte)
		kept  int64
		want  error
		other error // must not match
	}{
		{name: "other content", opts: testserver.Options{Seed: 99}, want: ErrLinkMismatch},
		{name: "other entity tag", opts: testserver.Options{ETag: `"v2"`},
			in: RescueInput{Validators: model.Validators{ETag: `"v1"`, StrongETag: true}}, want: ErrLinkChanged},
		{name: "other date", opts: testserver.Options{LastModified: lm},
			in: RescueInput{Validators: model.Validators{LastModified: lm.Add(time.Hour).Format(http.TimeFormat)}}, want: ErrLinkChanged},
		{name: "no ranges", opts: testserver.Options{NoRanges: true}, want: ErrLinkNoRanges, other: ErrSizeUnknown},
		{name: "accept-ranges none", opts: testserver.Options{AcceptRangesNone: true}, want: ErrLinkNoRanges},
		{name: "416", opts: testserver.Options{Always416: true}, want: ErrLinkSize},
		{name: "unknown total", opts: testserver.Options{UnknownTotal: true}, want: ErrSizeUnknown},
		{name: "browser's total differs", in: RescueInput{ExpectedSize: size + 1}, want: ErrLinkSize},
		{name: "server's file shorter", opts: testserver.Options{Size: 1 << 20}, kept: 2 << 20, want: ErrLinkSize},
		{name: "expired 403", opts: testserver.Options{FailFirst: 10, FailStatus: http.StatusForbidden}, want: ErrLinkExpired},
		{name: "expired 401 without challenge", opts: testserver.Options{FailFirst: 10, FailStatus: http.StatusUnauthorized}, want: ErrLinkExpired},
		{name: "web page instead of the range", opts: testserver.Options{NoRanges: true, ContentType: "text/html"}, want: ErrLinkExpired},
		{name: "zeros prove nothing", edit: func(b []byte) { clear(b) }, opts: testserver.Options{Prefix: make([]byte, 2<<20)}, want: ErrLinkUnproven},
		{name: "server error", opts: testserver.Options{FailFirst: 10, FailStatus: http.StatusNotImplemented}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			so := tc.opts
			if so.Size == 0 {
				so.Size = size
			}
			partOpts := so
			partOpts.Seed, partOpts.Size = 74, size
			h := newHarness(t, partOpts, fastOpts())
			kept := cmp.Or(tc.kept, 2<<20)
			partial := writePartial(t, h, "f.bin.crdownload", kept, tc.edit)
			h.srv.Update(func(o *testserver.Options) { *o = so; o.Seed = cmp.Or(so.Seed, 74) })
			was := stateOf(t, partial)
			in := tc.in
			in.Partial = partial
			_, err := h.rescue(in)
			switch {
			case tc.want == nil && err == nil:
				t.Fatal("Rescue succeeded")
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("Rescue = %v, want %v", err, tc.want)
			case tc.other != nil && errors.Is(err, tc.other):
				t.Fatalf("Rescue = %v, also %v", err, tc.other)
			}
			if isTransient(err) {
				t.Fatalf("refusal %v is transient", err)
			}
			checkUntouched(t, h, partial, was)
		})
	}
}

// TestRescueWeakTagMatches: a weak and a strong form of one opaque tag are the same
// version (RFC 9110 weak comparison).
func TestRescueWeakTagMatches(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 75, ETag: `"v1"`}, resumeOpts())
	partial := writePartial(t, h, "w.bin.crdownload", 1<<20+5, nil)
	res, err := h.rescue(RescueInput{Partial: partial, Validators: model.Validators{ETag: `W/"v1"`}})
	if err != nil || !res.ValidatorsMatched {
		t.Fatalf("Rescue = %+v, %v", res, err)
	}
}

// TestRescueRetriesTransient: a 503 before the first window is retried.
func TestRescueRetriesTransient(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 76, FailFirst: 1}, fastOpts())
	partial := writePartial(t, h, "t.bin.crdownload", 1<<20+7, nil)
	if _, err := h.rescue(RescueInput{Partial: partial}); err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	if reqs := h.srv.Requests(); reqs[0].Status != http.StatusServiceUnavailable {
		t.Fatalf("first answer %d", reqs[0].Status)
	}
}

// TestRescueTornTail: when only the last window differs, the proof tries once more
// 4 MiB lower (aligned), keeping only what that proves.
func TestRescueTornTail(t *testing.T) {
	t.Parallel()
	const size = 16 << 20
	const kept = 9<<20 + 123
	t.Run("last MiB lost", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 77}, resumeOpts())
		partial := writePartial(t, h, "torn.bin.crdownload", kept, func(b []byte) { clear(b[len(b)-(1<<20):]) })
		res, err := h.rescue(RescueInput{Partial: partial})
		if err != nil {
			t.Fatalf("Rescue: %v", err)
		}
		want := int64((kept - RescueTornStep) / proofAlign * proofAlign)
		if res.Kept != want || res.Download.RescuedBytes != want {
			t.Fatalf("kept %d, want %d", res.Kept, want)
		}
		if err := h.run(res.Download.ID, nil, nil); err != nil {
			t.Fatal(err)
		}
		h.verify(res.Download.ID)
	})
	t.Run("5 MiB lost", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 78}, resumeOpts())
		partial := writePartial(t, h, "torn.bin.crdownload", kept, func(b []byte) { clear(b[len(b)-(5<<20):]) })
		was := stateOf(t, partial)
		if _, err := h.rescue(RescueInput{Partial: partial}); !errors.Is(err, ErrLinkMismatch) {
			t.Fatalf("Rescue = %v, want ErrLinkMismatch", err)
		}
		checkUntouched(t, h, partial, was)
	})
}

// TestRescueTrustedCount: bytes beyond the browser's count are discarded and fetched
// again; a partial larger than that count may have holes, and is trusted only up to its
// first aligned block of zeros.
func TestRescueTrustedCount(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	t.Run("extra bytes", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 79}, resumeOpts())
		partial := writePartial(t, h, "x.bin.crdownload", 3<<20, func(b []byte) { b[len(b)-1] ^= 0xff })
		res, err := h.rescue(RescueInput{Partial: partial, Trusted: 2 << 20})
		if err != nil || res.Kept != 2<<20 {
			t.Fatalf("Rescue = %+v, %v", res, err)
		}
		if err := h.run(res.Download.ID, nil, nil); err != nil {
			t.Fatal(err)
		}
		h.verify(res.Download.ID)
	})
	t.Run("hole", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 80}, resumeOpts())
		hole := int64(1<<20 + 3*ProofWindowBytes)
		partial := writePartial(t, h, "h.bin.crdownload", 4<<20, func(b []byte) { clear(b[hole : hole+2*ProofWindowBytes]) })
		res, err := h.rescue(RescueInput{Partial: partial, Trusted: 3 << 20})
		if err != nil || res.Kept != hole {
			t.Fatalf("Rescue = %+v, %v; want %d kept", res, err, hole)
		}
		if err := h.run(res.Download.ID, nil, nil); err != nil {
			t.Fatal(err)
		}
		h.verify(res.Download.ID)
	})
}

// TestRescueComplete: a partial that already holds the whole file completes on the
// first run without another request.
func TestRescueComplete(t *testing.T) {
	t.Parallel()
	const size = 3 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 81}, resumeOpts())
	partial := writePartial(t, h, "all.bin.crdownload", size, nil)
	res, err := h.rescue(RescueInput{Partial: partial})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	n := len(h.srv.Requests())
	if err := h.run(res.Download.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.verify(res.Download.ID)
	if got := len(h.srv.Requests()); got != n {
		t.Fatalf("the run sent %d requests", got-n)
	}
}

// TestRescueEmpty: a 0-byte partial is refused before any request.
func TestRescueEmpty(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 82}, fastOpts())
	partial := writePartial(t, h, "e.bin.crdownload", 0, nil)
	if _, err := h.rescue(RescueInput{Partial: partial}); !errors.Is(err, ErrPartialEmpty) {
		t.Fatalf("Rescue = %v, want ErrPartialEmpty", err)
	}
	if n := len(h.srv.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

// TestRescueNames: the placeholder a browser made is removed and the name kept; any
// other file there gives the rescued file a variant; a provisional name gives way to the
// target's, else the server's.
func TestRescueNames(t *testing.T) {
	t.Parallel()
	const size = 2 << 20
	t.Run("placeholder", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 83}, resumeOpts())
		target := filepath.Join(h.dir, "movie.mkv")
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		partial := writePartial(t, h, "movie.mkv.part", 1<<20+1, nil)
		res, err := h.rescue(RescueInput{Partial: partial, Target: target, Placeholder: target})
		if err != nil || res.Download.Filename != "movie.mkv" {
			t.Fatalf("Rescue = %+v, %v", res, err)
		}
	})
	t.Run("taken name", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 84}, resumeOpts())
		target := filepath.Join(h.dir, "movie.mkv")
		if err := os.WriteFile(target, []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		partial := writePartial(t, h, "movie.mkv.part", 1<<20+1, nil)
		res, err := h.rescue(RescueInput{Partial: partial, Target: target, Placeholder: target})
		if err != nil || res.Download.Filename != "movie (1).mkv" {
			t.Fatalf("Rescue = %+v, %v", res, err)
		}
		if b, _ := os.ReadFile(target); string(b) != "mine" {
			t.Fatal("the file at the name changed")
		}
	})
	t.Run("provisional", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, testserver.Options{Size: size, Seed: 85, Filename: "from server.zip"}, resumeOpts())
		partial := writePartial(t, h, "Unconfirmed 4711.crdownload", 1<<20+1, nil)
		res, err := h.rescue(RescueInput{Partial: partial})
		if err != nil || res.Download.Filename != "from server.zip" {
			t.Fatalf("Rescue = %+v, %v", res, err)
		}
	})
	for _, tc := range []struct{ filename, partial, target, remote, want string }{
		{"", "/d/a.zip.crdownload", "", "", "a.zip"},
		{"", "/d/a.zip.CRDOWNLOAD", "", "", "a.zip"},
		{"", "/d/a.zip.part", "", "", "a.zip"},
		{"", "/d/a.zip", "", "", "a.zip"},
		{"chosen.iso", "/d/a.zip.part", "/d/t.zip", "r.zip", "chosen.iso"},
		{"", "/d/Unconfirmed 12.crdownload", "/d/t.zip", "r.zip", "t.zip"},
		{"", "/d/Unconfirmed 12.crdownload", "", "r.zip", "r.zip"},
		{"", "/d/Unconfirmed 12.crdownload", "", "", ""},
		{"", "/d/.crdownload", "", "r.zip", "r.zip"},
	} {
		if got := RescueName(tc.filename, filepath.FromSlash(tc.partial), filepath.FromSlash(tc.target), tc.remote); got != tc.want {
			t.Errorf("RescueName(%q, %q, %q, %q) = %q, want %q", tc.filename, tc.partial, tc.target, tc.remote, got, tc.want)
		}
	}
}

// TestRescueStability: a partial that changes between the check and the rename is
// still being written: nothing is adopted.
func TestRescueStability(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 86}, fastOpts())
	partial := writePartial(t, h, "g.bin.crdownload", 1<<20+9, nil)
	var created model.DownloadID
	_, err := h.rescue(RescueInput{Partial: partial, Created: func(d *model.Download) { created = d.ID },
		beforeRename: func() {
			f, err := os.OpenFile(partial, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = f.Write([]byte{1})
			_ = f.Close()
		}})
	if !errors.Is(err, ErrPartialLocked) {
		t.Fatalf("Rescue = %v, want ErrPartialLocked", err)
	}
	if created == 0 {
		t.Fatal("no record was created before the check")
	}
	if list, _ := h.st.ListDownloads(context.Background()); len(list) != 0 {
		t.Fatalf("records left: %+v", list)
	}
	if fi, err := os.Stat(partial); err != nil || fi.Size() != 1<<20+10 {
		t.Fatalf("partial %v, %v", fi, err)
	}
	if parts, _ := filepath.Glob(filepath.Join(h.dir, "*"+partExt)); len(parts) != 0 {
		t.Fatalf("part files left: %v", parts)
	}
}

// TestRescueHeldDuringAdoption: the new record is held (a Run is refused) from its
// creation until the rescue returns.
func TestRescueHeldDuringAdoption(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 87}, resumeOpts())
	partial := writePartial(t, h, "held.bin.crdownload", 1<<20+3, nil)
	var runErr error
	res, err := h.rescue(RescueInput{Partial: partial, Created: func(d *model.Download) {
		if d.State != model.StateProbing || d.RescuedBytes != 0 {
			t.Errorf("created record %+v", d)
		}
		runErr = h.eng.Run(context.Background(), d.ID, nil, nil)
	}})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	if !errors.Is(runErr, ErrAlreadyRunning) {
		t.Fatalf("Run during the adoption = %v, want ErrAlreadyRunning", runErr)
	}
	if err := h.run(res.Download.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.verify(res.Download.ID)
}

// TestRescueKillPoints: a commit that fails moves the partial back and deletes the
// record; a crash before the commit (a PROBING record, no segments) downloads the file
// again from byte 0, byte-exact.
func TestRescueKillPoints(t *testing.T) {
	t.Parallel()
	const kept = 1<<20 + 11
	h := newHarness(t, testserver.Options{Size: 3 << 20, Seed: 88}, resumeOpts())
	partial := writePartial(t, h, "k.bin.crdownload", kept, nil)
	was := stateOf(t, partial)
	var snapshot model.Download
	h.st.saveErr = errors.New("disk unplugged")
	_, err := h.rescue(RescueInput{Partial: partial, Created: func(d *model.Download) { snapshot = *d }})
	if err == nil || !strings.Contains(err.Error(), "disk unplugged") {
		t.Fatalf("Rescue = %v", err)
	}
	now := stateOf(t, partial)
	if now.size != kept || !bytes.Equal(now.data, was.data) {
		t.Fatalf("the partial came back with %d bytes", now.size)
	}
	if list, _ := h.st.ListDownloads(context.Background()); len(list) != 0 {
		t.Fatalf("records left: %+v", list)
	}

	// The crash: the record as created, the part file renamed, no checkpoint.
	h.st.saveErr = nil
	h.st.mu.Lock()
	h.st.downloads[snapshot.ID] = snapshot
	h.st.mu.Unlock()
	if err := os.Rename(partial, partPath(h.dir, snapshot.Filename)); err != nil {
		t.Fatal(err)
	}
	if err := h.run(snapshot.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := h.verify(snapshot.ID); d.RescuedBytes != 0 {
		t.Fatalf("a download started over keeps %d rescued bytes", d.RescuedBytes)
	}
}

// TestRescueRestartClears: Restart forgets the rescued bytes.
func TestRescueRestartClears(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 89}, resumeOpts())
	partial := writePartial(t, h, "r.bin.crdownload", 1<<20+1, nil)
	res, err := h.rescue(RescueInput{Partial: partial, Paused: true})
	if err != nil || res.Download.State != model.StatePaused {
		t.Fatalf("Rescue = %+v, %v", res, err)
	}
	if err := h.eng.Restart(context.Background(), res.Download.ID, RestartOptions{}); err != nil {
		t.Fatal(err)
	}
	if d := h.st.get(t, res.Download.ID); d.RescuedBytes != 0 {
		t.Fatalf("RescuedBytes after Restart = %d", d.RescuedBytes)
	}
}

// TestRescueInput: refusals before anything is read or sent.
func TestRescueInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 90}, fastOpts())
	partial := writePartial(t, h, "i.bin.crdownload", 5, nil)
	for name, in := range map[string]RescueInput{
		"relative":  {Partial: "i.bin.crdownload"},
		"post":      {Partial: partial, Request: model.Request{URL: h.srv.URL, Method: http.MethodPost}},
		"ftp":       {Partial: partial, Request: model.Request{URL: "ftp://example.test/a"}},
		"directory": {Partial: h.dir},
	} {
		if _, err := h.rescue(in); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: Rescue = %v, want ErrInvalidRequest", name, err)
		}
	}
	if _, err := h.rescue(RescueInput{Partial: filepath.Join(h.dir, "gone.crdownload")}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: Rescue = %v, want fs.ErrNotExist", err)
	}
	if n := len(h.srv.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestTrustedLength(t *testing.T) {
	for _, tc := range []struct {
		size, trusted, want int64
		sparse              bool
	}{
		{100, 0, 100, false}, {100, 100, 100, false}, {100, 150, 100, false}, {100, 40, 40, true}, {0, 0, 0, false},
	} {
		if got, sparse := trustedLength(tc.size, tc.trusted); got != tc.want || sparse != tc.sparse {
			t.Errorf("trustedLength(%d, %d) = %d, %v", tc.size, tc.trusted, got, sparse)
		}
	}
}

// TestRescueRefusesOwnFiles: a partial that is a stored download's part file or file
// (a Downex download picked by mistake) is refused before any request, and stays where it
// is: adopting it would take the file from under the other download (§7.4 A1).
func TestRescueRefusesOwnFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 91}, fastOpts())
	ctx, cancel := runContext(t)
	defer cancel()
	d, err := h.eng.Add(ctx, model.Request{URL: h.srv.URL + "/own.bin"}, h.dir, "own.bin", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	part := PartPath(d.Dir, d.Filename)
	if err := os.WriteFile(part, testserver.Content(h.srv.Options())[:2<<20], 0o644); err != nil {
		t.Fatal(err)
	}
	done := writePartial(t, h, "own.bin", 2<<20, nil) // as if the download had completed
	for _, p := range []string{part, done, strings.ToUpper(part[:len(part)-len(filepath.Base(part))]) + filepath.Base(part)} {
		if p != part && p != done && !caseInsensitiveFS() {
			continue
		}
		was := stateOf(t, p)
		if _, err := h.rescue(RescueInput{Partial: p}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("rescue of %s = %v, want ErrInvalidRequest", filepath.Base(p), err)
		}
		if now := stateOf(t, p); now.size != was.size || !bytes.Equal(now.data, was.data) {
			t.Fatalf("%s changed", p)
		}
	}
	if n := len(h.srv.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
	if list, _ := h.st.ListDownloads(context.Background()); len(list) != 1 {
		t.Fatalf("%d records", len(list))
	}
}

// caseInsensitiveFS reports the file systems whose names ignore case by default.
func caseInsensitiveFS() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }
