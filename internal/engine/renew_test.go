package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"downex/internal/model"
	"downex/internal/testserver"
)

// TestProofWindows is the window rule of docs/ENGINE-SPEC.md §7.3 P2.
func TestProofWindows(t *testing.T) {
	const K = ProofWindowBytes
	rnd := func() *bytes.Reader {
		return bytes.NewReader(bytes.Repeat([]byte{0x5a, 0x13, 0xc7, 0x02, 0x9e, 0x41, 0x77, 0x3b}, 64))
	}
	check := func(t *testing.T, runs []byteRun, n int, ws []byteRun) {
		t.Helper()
		if len(ws) == 0 || len(ws) > n {
			t.Fatalf("%d windows, want 1..%d", len(ws), n)
		}
		for i, w := range ws {
			if w.len() <= 0 || w.len() > K {
				t.Fatalf("window %d = %v: length %d", i, w, w.len())
			}
			inside := false
			for _, r := range runs {
				inside = inside || r.contains(w)
			}
			if !inside {
				t.Fatalf("window %d = %v lies outside the runs %v", i, w, runs)
			}
			for j := range i {
				if ws[j].contains(w) {
					t.Fatalf("window %d = %v is inside window %d = %v", i, w, j, ws[j])
				}
			}
		}
	}

	one := []byteRun{{0, 10<<20 - 1}}
	ws := proofWindows(one, 8, rnd())
	check(t, one, 8, ws)
	if ws[0] != one[0].tail(K) || ws[1] != one[0].head(K) {
		t.Fatalf("one run: first windows %v, want the boundary then the head", ws[:2])
	}
	for _, w := range ws[2:] {
		if w.start%proofAlign != 0 {
			t.Fatalf("random window %v is not aligned to %d", w, proofAlign)
		}
	}

	many := []byteRun{{0, 300_000}, {1 << 20, 1<<20 + 50_000}, {5 << 20, 9 << 20}}
	ws = proofWindows(many, 8, rnd())
	check(t, many, 8, ws)
	if ws[0] != many[0].tail(K) || ws[1] != many[2].tail(K) || ws[2] != many[0].head(K) {
		t.Fatalf("many runs: deterministic windows %v", ws[:3])
	}

	// No run at 0 (the head segment had no durable bytes): no boundary or head window.
	late := []byteRun{{4096 * 10, 4096*10 + 3*K}}
	ws = proofWindows(late, 4, rnd())
	check(t, late, 4, ws)
	if ws[0] != late[0].tail(K) {
		t.Fatalf("late run: first window %v", ws[0])
	}

	// A tiny run: every window is inside the first, so only that one stays.
	tiny := []byteRun{{0, 999}}
	if ws = proofWindows(tiny, 8, rnd()); len(ws) != 1 || ws[0] != tiny[0] {
		t.Fatalf("tiny run: %v", ws)
	}
	if ws = proofWindows(one, 8, bytes.NewReader(nil)); len(ws) != 2 {
		t.Fatalf("without random bytes: %v, want the deterministic windows", ws)
	}

	runs := durableRuns([]model.Segment{
		{Start: 100, End: 199, Durable: 100}, {Start: 0, End: 99, Durable: 100}, {Start: 200, End: 299, Durable: 0},
		{Start: 300, End: 399, Durable: 10},
	})
	if len(runs) != 2 || runs[0] != (byteRun{0, 199}) || runs[1] != (byteRun{300, 309}) {
		t.Fatalf("durableRuns = %v", runs)
	}
}

func TestStripVariant(t *testing.T) {
	for in, want := range map[string]string{
		"a.zip": "a.zip", "a (1).zip": "a.zip", "a (12).tar.gz": "a (12).tar.gz", "a (12).gz": "a.gz",
		"(1).zip": "(1).zip", "a (x).zip": "a (x).zip", "noext (3)": "noext", ".profile": ".profile",
	} {
		if got := StripVariant(in); got != want {
			t.Errorf("StripVariant(%q) = %q, want %q", in, got, want)
		}
	}
}

// partialDownload makes a paused ranged download of srv's file with some durable bytes.
func partialDownload(t *testing.T, h *harness, path string) *model.Download {
	t.Helper()
	d := h.add(path, "", 4)
	if err := pauseMidway(t, h, d.ID, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	rec := h.st.get(t, d.ID)
	if rec.State != model.StatePaused || rec.Mode != model.ModeRanged {
		t.Fatalf("partial download %+v", rec)
	}
	return rec
}

func prove(t *testing.T, h *harness, id model.DownloadID, rawURL string, o ProveOptions) (LinkProof, error) {
	t.Helper()
	ctx, cancel := runContext(t)
	defer cancel()
	return h.eng.ProveLink(ctx, id, model.Request{URL: rawURL}, o)
}

// TestRenewOnAnotherOrigin: a mirror serving the same bytes with another entity tag is
// proven and continues the download; its validators replace the old ones (P4, P8).
func TestRenewOnAnotherOrigin(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	so := testserver.Options{Size: size, Seed: 21, ETag: `"a"`, Filename: "data.bin"}
	h := newHarness(t, so, resumeOpts())
	d := partialDownload(t, h, "/old")
	mirror := testserver.New(testserver.Options{Size: size, Seed: 21, ETag: `"b"`, Filename: "data.bin"})
	t.Cleanup(mirror.Close)

	p, err := prove(t, h, d.ID, mirror.URL+"/new", ProveOptions{})
	if err != nil {
		t.Fatalf("ProveLink = %v", err)
	}
	if p.Fresh || p.NameDiffers || p.Validators.ETag != `"b"` || p.Compared == 0 || p.RemoteName != "data.bin" {
		t.Fatalf("proof = %+v", p)
	}
	if n := len(mirror.Requests()); n == 0 || n > ProofWindowsUnvouched {
		t.Fatalf("%d proof requests", n)
	}
	for _, r := range mirror.Requests() {
		if r.IfRange != "" {
			t.Fatalf("a proof request sent If-Range %q", r.IfRange)
		}
	}
	before := h.st.segs(d.ID)
	if err := h.eng.Renew(context.Background(), d.ID, p, true); err != nil {
		t.Fatalf("Renew = %v", err)
	}
	rec := h.st.get(t, d.ID)
	if rec.State != model.StateQueued || rec.Request.URL != mirror.URL+"/new" || rec.Validators.ETag != `"b"` ||
		rec.Size != size || rec.Filename != d.Filename {
		t.Fatalf("renewed record %+v", rec)
	}
	if after := h.st.segs(d.ID); len(after) != len(before) {
		t.Fatalf("segments changed: %v → %v", before, after)
	}
	served := len(mirror.Requests())
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run after renewal = %v", err)
	}
	h.verify(d.ID)
	if len(mirror.Requests()) == served {
		t.Fatal("the rest did not come from the new link")
	}
}

// TestProveLinkVouched: a new server whose first answer carries the stored strong entity
// tag vouches for the file: a sampled proof then compares ProofWindowsVouched windows, else
// ProofWindowsUnvouched (P2).
func TestProveLinkVouched(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 22, ETag: `"v"`}, resumeOpts())
	d := partialDownload(t, h, "/v.bin")
	if runs := durableRuns(h.st.segs(d.ID)); runsTotal(runs) <= FullProofMaxBytes && len(runs) <= FullProofMaxRuns {
		t.Skip("a full proof")
	}
	for _, tc := range []struct {
		etag string
		want int
	}{{`"v"`, ProofWindowsVouched}, {`"w"`, ProofWindowsUnvouched}} {
		mirror := testserver.New(testserver.Options{Size: size, Seed: 22, ETag: tc.etag})
		t.Cleanup(mirror.Close)
		if _, err := prove(t, h, d.ID, mirror.URL+"/v.bin", ProveOptions{}); err != nil {
			t.Fatalf("ProveLink(%s) = %v", tc.etag, err)
		}
		if n := len(mirror.Requests()); n != tc.want {
			t.Fatalf("ETag %s: %d proof requests, want %d", tc.etag, n, tc.want)
		}
	}
}

// TestProveLinkRefusals: each kind of new link that cannot continue the download is
// refused, and the download is left as it was (P3-P7).
func TestProveLinkRefusals(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	so := testserver.Options{Size: size, Seed: 31, ETag: `"same"`, Filename: "movie.mkv"}
	h := newHarness(t, so, resumeOpts())
	d := partialDownload(t, h, "/movie.mkv")
	before := *h.st.get(t, d.ID)

	other := func(o testserver.Options) string {
		s := testserver.New(o)
		t.Cleanup(s.Close)
		return s.URL + "/movie.mkv"
	}
	for _, tc := range []struct {
		name string
		url  string
		want error
	}{
		{"other bytes", other(testserver.Options{Size: size, Seed: 32, Filename: "movie.mkv"}), ErrLinkMismatch},
		{"other size", other(testserver.Options{Size: size + 1, Seed: 31, Filename: "movie.mkv"}), ErrLinkSize},
		{"no ranges", other(testserver.Options{Size: size, Seed: 31, NoRanges: true}), ErrLinkNoRanges},
		{"expired", other(testserver.Options{Size: size, Seed: 31, Token: "x"}), ErrLinkExpired},
		{"same origin, new version", h.srv.URL + "/v2", ErrLinkChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == ErrLinkChanged {
				h.srv.SetETag(`"other"`)
				defer h.srv.SetETag(`"same"`)
			}
			_, err := prove(t, h, d.ID, tc.url, ProveOptions{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("ProveLink = %v, want %v", err, tc.want)
			}
			if got := *h.st.get(t, d.ID); got.Request.URL != before.Request.URL || got.UpdatedAt != before.UpdatedAt {
				t.Fatalf("the download changed: %+v", got)
			}
		})
	}
	// The status of a server that answers otherwise is kept.
	_, err := prove(t, h, d.ID, other(testserver.Options{Size: size, FailFirst: 1, FailStatus: http.StatusNotFound}), ProveOptions{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Fatalf("ProveLink(404) = %v", err)
	}
	// Another name is reported, not refused.
	p, err := prove(t, h, d.ID, other(testserver.Options{Size: size, Seed: 31, Filename: "Other.mkv"}), ProveOptions{})
	if err != nil || !p.NameDiffers || p.RemoteName != "Other.mkv" {
		t.Fatalf("ProveLink(another name) = %+v, %v", p, err)
	}
	if _, err := prove(t, h, d.ID, "ftp://example.test/x", ProveOptions{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ProveLink(ftp) = %v", err)
	}
	ctx := context.Background()
	if _, err := h.eng.ProveLink(ctx, d.ID, model.Request{URL: h.srv.URL, Method: http.MethodPost}, ProveOptions{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ProveLink(POST) = %v", err)
	}
}

// TestProveLinkUnproven: durable bytes of one repeated value prove nothing; with
// RestartIfUnproven the renewal downloads again from the start (P7).
func TestProveLinkUnproven(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	so := testserver.Options{Size: size, Seed: 41, Prefix: make([]byte, size)} // all zeros
	h := newHarness(t, so, resumeOpts())
	d := partialDownload(t, h, "/zeros.bin")
	durable := int64(0)
	for _, s := range h.st.segs(d.ID) {
		durable += s.Durable
	}
	if durable <= FullProofMaxBytes {
		t.Skipf("only %d durable bytes: a full proof", durable)
	}
	if _, err := prove(t, h, d.ID, h.srv.URL+"/again", ProveOptions{}); !errors.Is(err, ErrLinkUnproven) {
		t.Fatalf("ProveLink = %v, want ErrLinkUnproven", err)
	}
	p, err := prove(t, h, d.ID, h.srv.URL+"/again", ProveOptions{RestartIfUnproven: true})
	if err != nil || !p.Fresh {
		t.Fatalf("ProveLink(restart) = %+v, %v", p, err)
	}
	if err := h.eng.Renew(context.Background(), d.ID, p, false); err != nil {
		t.Fatalf("Renew(fresh) = %v", err)
	}
	rec := h.st.get(t, d.ID)
	if rec.State != model.StatePaused || rec.Mode != model.ModeUnknown || rec.Size != -1 || rec.RemoteName != "" ||
		rec.Filename != d.Filename || len(h.st.segs(d.ID)) != 0 || rec.Request.URL != h.srv.URL+"/again" {
		t.Fatalf("fresh renewal = %+v, segments %v", rec, h.st.segs(d.ID))
	}
	if fi, err := os.Stat(partPath(rec.Dir, rec.Filename)); err != nil || fi.Size() != 0 {
		t.Fatalf("part file after a fresh renewal: %v, %v", fi, err)
	}
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.verify(d.ID)
}

// TestProveLinkFullFlat: a full proof whose durable bytes are all one repeated value (an
// image's zeroed system area) proves nothing either: another file of the same size that
// starts with the same zeros is refused (P7), never spliced.
func TestProveLinkFullFlat(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 45, Prefix: make([]byte, size)}, resumeOpts())
	d := partialDownload(t, h, "/disk.img")
	// Keep at most 32 KiB of each segment durable: a full proof (≤ 1 MiB in ≤ 8 runs).
	segs := h.st.segs(d.ID)
	if len(segs) > FullProofMaxRuns {
		t.Skipf("%d segments", len(segs))
	}
	var durable int64
	for i := range segs {
		segs[i].Durable = min(segs[i].Durable, 32<<10)
		durable += segs[i].Durable
	}
	if durable == 0 {
		t.Fatal("no durable bytes")
	}
	if err := h.st.SaveCheckpoint(context.Background(), h.st.get(t, d.ID), segs); err != nil {
		t.Fatal(err)
	}
	// The same zeros, then another last byte: another file.
	other := testserver.New(testserver.Options{Size: size, Seed: 46, Prefix: make([]byte, size-1)})
	t.Cleanup(other.Close)
	if other.Sum256() == h.srv.Sum256() {
		t.Skip("the generated last byte is a zero too")
	}
	if _, err := prove(t, h, d.ID, other.URL+"/disk.img", ProveOptions{}); !errors.Is(err, ErrLinkUnproven) {
		t.Fatalf("ProveLink(full proof of zeros) = %v, want ErrLinkUnproven", err)
	}
	if p, err := prove(t, h, d.ID, other.URL+"/disk.img", ProveOptions{RestartIfUnproven: true}); err != nil || !p.Fresh {
		t.Fatalf("ProveLink(full proof of zeros, restart) = %+v, %v", p, err)
	}
}

// TestProveLinkFresh: a download with nothing to splice is renewed from the start
// without any request (P1).
func TestProveLinkFresh(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 3 << 20, Seed: 51}, resumeOpts())
	d := partialDownload(t, h, "/f.bin")
	if err := os.Remove(partPath(d.Dir, d.Filename)); err != nil {
		t.Fatal(err)
	}
	served := len(h.srv.Requests())
	p, err := prove(t, h, d.ID, h.srv.URL+"/g.bin", ProveOptions{})
	if err != nil || !p.Fresh {
		t.Fatalf("ProveLink = %+v, %v", p, err)
	}
	if len(h.srv.Requests()) != served {
		t.Fatal("a proof without a part file sent requests")
	}
	queued := h.add("/q.bin", "", 2)
	if p, err := prove(t, h, queued.ID, h.srv.URL+"/q2.bin", ProveOptions{}); err != nil || !p.Fresh {
		t.Fatalf("ProveLink(never started) = %+v, %v", p, err)
	}
}

// TestRenewStale: a download that changed between the proof and Renew is refused (P8).
func TestRenewStale(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 6 << 20, Seed: 61}, resumeOpts())
	d := partialDownload(t, h, "/f.bin")
	p, err := prove(t, h, d.ID, h.srv.URL+"/again", ProveOptions{})
	if err != nil || p.Fresh {
		t.Fatalf("ProveLink = %+v, %v", p, err)
	}
	segs := h.st.segs(d.ID)
	segs[0].Durable /= 2
	rec := h.st.get(t, d.ID)
	if err := h.st.SaveCheckpoint(context.Background(), rec, segs); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Renew(context.Background(), d.ID, p, true); !errors.Is(err, ErrLinkStale) {
		t.Fatalf("Renew = %v, want ErrLinkStale", err)
	}
	if got := h.st.get(t, d.ID); got.Request.URL == h.srv.URL+"/again" {
		t.Fatal("a stale proof was applied")
	}
}

// TestRenewCycleIntegrity: expire → renew → resume is byte-exact for many layouts.
func TestRenewCycleIntegrity(t *testing.T) {
	t.Parallel()
	renewed := 0
	for seed := uint64(100); seed < 120; seed++ {
		const size = 3<<20 + 12345
		so := testserver.Options{Size: size, Seed: seed, ETag: `"s"`, Token: "old"}
		opts := resumeOpts()
		opts.HostRules = &capRules{conns: 4, caps: map[string]int64{}}
		h := newHarness(t, so, opts)
		opts.HostRules.(*capRules).caps[hostKey(h.srv.URL)] = 128<<10 + int64(seed)*977
		d := h.add("/f.bin?token=old", "", 4)
		if err := expireMidway(t, h, d.ID, 512<<10+int64(seed)*4099); !errors.Is(err, ErrLinkExpired) {
			if err == nil {
				continue // every range was out already: it completed
			}
			t.Fatalf("seed %d: Run = %v", seed, err)
		}
		p, err := prove(t, h, d.ID, h.srv.URL+"/f.bin?token=new", ProveOptions{})
		if err != nil {
			t.Fatalf("seed %d: ProveLink = %v", seed, err)
		}
		if err := h.eng.Renew(context.Background(), d.ID, p, true); err != nil {
			t.Fatalf("seed %d: Renew = %v", seed, err)
		}
		if err := h.run(d.ID, nil, nil); err != nil {
			t.Fatalf("seed %d: Run = %v", seed, err)
		}
		h.verify(d.ID)
		renewed++
	}
	if renewed < 15 {
		t.Fatalf("only %d of 20 downloads expired midway", renewed)
	}
}
