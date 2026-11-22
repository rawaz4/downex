package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

const rescueSize = 4 << 20

// writeRescuePartial writes the first n bytes of the content of srv (seed) into
// dir/name, as a browser leaves them.
func writeRescuePartial(t *testing.T, dir, name string, o testserver.Options, n int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, testserver.Content(o)[:n], 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *harness) rescue(r *downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
	h.t.Helper()
	res, err := h.dl.Rescue(ctxT(h.t), connect.NewRequest(r))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// refusalCode is the code a rescue refusal carries (message prefix and metadata, which
// must agree).
func refusalCode(t *testing.T, err error) string {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a Connect error: %v", err)
	}
	code := ce.Meta().Get(ErrorCodeMeta)
	if !strings.HasPrefix(ce.Message(), code+": ") {
		t.Fatalf("message %q does not start with its code %q", ce.Message(), code)
	}
	return code
}

func TestParseRescue(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "a.zip.crdownload")
	target := filepath.Join(filepath.Dir(abs), "a.zip")
	ok := func() *downexv1.RescueRequest {
		return &downexv1.RescueRequest{Url: "https://example.test/a.zip", PartialPath: abs, Source: downexv1.AddSource_ADD_SOURCE_UI}
	}
	for _, tc := range []struct {
		name string
		edit func(r *downexv1.RescueRequest)
		want string // "" = accepted
	}{
		{"ui", func(*downexv1.RescueRequest) {}, ""},
		{"browser with target only", func(r *downexv1.RescueRequest) {
			r.PartialPath, r.TargetPath, r.Source = "", target, downexv1.AddSource_ADD_SOURCE_BROWSER
		}, ""},
		{"no url", func(r *downexv1.RescueRequest) { r.Url = "" }, "url is required"},
		{"ftp", func(r *downexv1.RescueRequest) { r.Url = "ftp://example.test/a" }, "scheme"},
		{"clipboard", func(r *downexv1.RescueRequest) { r.Source = downexv1.AddSource_ADD_SOURCE_CLIPBOARD }, "source"},
		{"ui without partial", func(r *downexv1.RescueRequest) { r.PartialPath, r.TargetPath = "", target }, "partial_path is required"},
		{"browser without paths", func(r *downexv1.RescueRequest) {
			r.PartialPath, r.Source = "", downexv1.AddSource_ADD_SOURCE_BROWSER
		}, "partial_path or target_path"},
		{"relative", func(r *downexv1.RescueRequest) { r.PartialPath = "a.zip.crdownload" }, "absolute"},
		{"NUL", func(r *downexv1.RescueRequest) { r.PartialPath = abs + "\x00" }, "absolute"},
		{"long path", func(r *downexv1.RescueRequest) { r.PartialPath = abs + strings.Repeat("a", MaxRescuePathBytes) }, "longer"},
		{"negative count", func(r *downexv1.RescueRequest) { r.BytesReceived = -1 }, "bytes_received"},
		{"bad total", func(r *downexv1.RescueRequest) { r.ExpectedSize = -2 }, "expected_size"},
		{"negative start", func(r *downexv1.RescueRequest) { r.StartedAtMs = -1 }, "started_at_ms"},
		{"long etag", func(r *downexv1.RescueRequest) { r.Etag = strings.Repeat("e", MaxRescueValidatorBytes+1) }, "etag"},
		{"etag with control", func(r *downexv1.RescueRequest) { r.Etag = "\"a\nb\"" }, "control"},
		{"date with control", func(r *downexv1.RescueRequest) { r.LastModified = "Mon\x01" }, "control"},
		{"connections", func(r *downexv1.RescueRequest) { r.MaxConnections = MaxConnectionsLimit + 1 }, "max_connections"},
		{"speed", func(r *downexv1.RescueRequest) { r.SpeedLimit = -1 }, "speed_limit"},
		{"page", func(r *downexv1.RescueRequest) { r.PageUrl = "javascript:x" }, "page_url"},
		{"forbidden header", func(r *downexv1.RescueRequest) {
			r.Headers = []*downexv1.Header{{Name: "Host", Value: "x"}}
		}, "header"},
	} {
		r := ok()
		tc.edit(r)
		in, err := parseRescue(r)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.want)
		}
		if tc.name == "ui" && (in.req.Method != "GET" || in.source != model.SourceUI || in.partial != abs) {
			t.Errorf("parsed %+v", in)
		}
	}
	in, err := parseRescue(&downexv1.RescueRequest{Url: "https://e.test/a", PartialPath: abs, Source: downexv1.AddSource_ADD_SOURCE_CLI,
		Etag: `W/"x"`, LastModified: "Mon, 02 Jan 2026 10:00:00 GMT", StartedAtMs: 1_700_000_000_000, ExpectedSize: -1})
	if err != nil || in.validators.StrongETag || in.validators.ETag != `W/"x"` || in.startedAt.UnixMilli() != 1_700_000_000_000 || in.expectedSize != 0 {
		t.Fatalf("parsed %+v, %v", in, err)
	}
}

// fakePartialFS is a partialFS over a fixed listing.
type fakePartialFS struct {
	files map[string]fakeFile
	dirN  int   // entries ReadDir reports beyond files (a huge folder)
	err   error // returned by every call
}

type fakeFile struct {
	size  int64
	mtime time.Time
	dir   bool
}

func (f fakeFile) info(name string) fs.FileInfo { return fakeInfo{name: name, f: f} }

type fakeInfo struct {
	name string
	f    fakeFile
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return i.f.size }
func (i fakeInfo) ModTime() time.Time { return i.f.mtime }
func (i fakeInfo) IsDir() bool        { return i.f.dir }
func (i fakeInfo) Sys() any           { return nil }
func (i fakeInfo) Mode() fs.FileMode {
	if i.f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

func (p *fakePartialFS) Lstat(name string) (fs.FileInfo, error) {
	if p.err != nil {
		return nil, p.err
	}
	f, ok := p.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return f.info(filepath.Base(name)), nil
}

func (p *fakePartialFS) ReadDir(dir string, n int) ([]fs.DirEntry, error) {
	if p.err != nil {
		return nil, p.err
	}
	var out []fs.DirEntry
	for name, f := range p.files {
		if filepath.Dir(name) == dir {
			out = append(out, fs.FileInfoToDirEntry(f.info(filepath.Base(name))))
		}
	}
	for i := 0; i < p.dirN && len(out) < n; i++ {
		out = append(out, fs.FileInfoToDirEntry(fakeFile{size: 1}.info(fmt.Sprintf("other %d", i))))
	}
	return out[:min(len(out), n)], nil
}

func TestFindPartial(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dl")
	at := func(n string) string { return filepath.Join(dir, n) }
	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	browser := func(edit func(*rescueInput)) rescueInput {
		in := rescueInput{source: model.SourceBrowser, target: at("movie.mkv"), trusted: 1000, startedAt: start}
		if edit != nil {
			edit(&in)
		}
		return in
	}
	fresh := start.Add(time.Minute)
	for _, tc := range []struct {
		name        string
		files       map[string]fakeFile
		in          rescueInput
		dirN        int
		err         error
		want        string
		placeholder string
		code        string // the refusal's code ("" with want, "invalid" for a plain error)
	}{
		{name: "crdownload", files: map[string]fakeFile{at("movie.mkv.crdownload"): {size: 1000, mtime: fresh}},
			in: browser(nil), want: at("movie.mkv.crdownload")},
		{name: "part with placeholder", files: map[string]fakeFile{at("movie.mkv.part"): {size: 1000, mtime: fresh}, at("movie.mkv"): {mtime: fresh}},
			in: browser(nil), want: at("movie.mkv.part"), placeholder: at("movie.mkv")},
		{name: "old placeholder is no placeholder", files: map[string]fakeFile{at("movie.mkv.part"): {size: 1000, mtime: fresh}, at("movie.mkv"): {mtime: start.Add(-time.Hour)}},
			in: browser(nil), want: at("movie.mkv.part")},
		{name: "non-empty file is no placeholder", files: map[string]fakeFile{at("movie.mkv.part"): {size: 1000, mtime: fresh}, at("movie.mkv"): {size: 1, mtime: fresh}},
			in: browser(nil), want: at("movie.mkv.part")},
		{name: "no start time, no placeholder", files: map[string]fakeFile{at("movie.mkv.part"): {size: 1000, mtime: fresh}, at("movie.mkv"): {mtime: fresh}},
			in: browser(func(in *rescueInput) { in.startedAt = time.Time{} }), want: at("movie.mkv.part")},
		{name: "unconfirmed", files: map[string]fakeFile{
			at("Unconfirmed 123.crdownload"): {size: 1500, mtime: fresh},
			at("Unconfirmed 9.crdownload"):   {size: 500, mtime: fresh},                  // too small
			at("Unconfirmed 7.crdownload"):   {size: 1000 + 17<<20, mtime: fresh},        // too large
			at("Unconfirmed 8.crdownload"):   {size: 1500, mtime: start.Add(-time.Hour)}, // older
			at("Unconfirmed x.crdownload"):   {size: 1500, mtime: fresh},                 // not the pattern
		}, in: browser(nil), want: at("Unconfirmed 123.crdownload")},
		{name: "two unconfirmed", files: map[string]fakeFile{
			at("Unconfirmed 1.crdownload"): {size: 1500, mtime: fresh}, at("Unconfirmed 2.crdownload"): {size: 1600, mtime: fresh},
		}, in: browser(nil), code: CodeRescueAmbiguous},
		{name: "none", files: map[string]fakeFile{at("other.bin"): {size: 5}}, in: browser(nil), code: CodeRescueNotFound},
		{name: "huge folder", files: map[string]fakeFile{at("Unconfirmed 1.crdownload"): {size: 1500, mtime: fresh}}, dirN: MaxRescueListing,
			in: browser(nil), code: CodeRescueAmbiguous},
		{name: "no access", err: fs.ErrPermission, in: browser(nil), code: CodeRescueNoAccess},
		{name: "browser partial without suffix", files: map[string]fakeFile{at("movie.txt"): {size: 1}},
			in: browser(func(in *rescueInput) { in.partial = at("movie.txt") }), code: "invalid"},
		{name: "browser partial elsewhere", files: map[string]fakeFile{filepath.Join(t.TempDir(), "x.part"): {size: 1}},
			in: browser(func(in *rescueInput) { in.partial = filepath.Join(filepath.Dir(dir), "x.part") }), code: "invalid"},
		{name: "ui any file", files: map[string]fakeFile{at("movie.txt"): {size: 1}},
			in: rescueInput{source: model.SourceUI, partial: at("movie.txt")}, want: at("movie.txt")},
		{name: "ui folder", files: map[string]fakeFile{at("sub"): {dir: true}},
			in: rescueInput{source: model.SourceUI, partial: at("sub")}, code: CodeRescueNotFound},
		{name: "ui gone", in: rescueInput{source: model.SourceUI, partial: at("gone.part")}, code: CodeRescueNotFound},
	} {
		fsys := &fakePartialFS{files: tc.files, dirN: tc.dirN, err: tc.err}
		loc, err := findPartial(fsys, tc.in)
		if tc.code != "" {
			var c *rescueCoded
			switch {
			case err == nil:
				t.Errorf("%s: found %+v, want a refusal", tc.name, loc)
			case tc.code == "invalid" && errors.As(err, &c):
				t.Errorf("%s: %v is coded %s, want a plain error", tc.name, err, c.code)
			case tc.code != "invalid" && (!errors.As(err, &c) || c.code != tc.code):
				t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
			}
			continue
		}
		if err != nil || loc.partial != tc.want || loc.placeholder != tc.placeholder {
			t.Errorf("%s: %+v, %v; want %s (placeholder %q)", tc.name, loc, err, tc.want, tc.placeholder)
		}
	}
}

// TestRescueRPC is the whole browser flow: a paused rescue (never held for
// confirmation), the relay's first start, which opens the progress window once, and a
// byte-exact file. rescued_bytes is in Get, List and the response.
func TestRescueRPC(t *testing.T) {
	windows := &launcher{}
	h := newHarness(t, harnessOptions{openProgress: windows.open, settings: func(s *downexv1.Settings) {
		s.ConfirmBrowserDownloads = true
	}})
	o := testserver.Options{Size: rescueSize, Seed: 501, ETag: `"r"`}
	srv := serve(t, o)
	const kept = 1<<20 + 77
	partial := writeRescuePartial(t, h.dir, "big.iso.crdownload", o, kept)
	res, err := h.rescue(&downexv1.RescueRequest{
		Url: srv.URL + "/big.iso", TargetPath: filepath.Join(h.dir, "big.iso"), BytesReceived: kept,
		ExpectedSize: rescueSize, Etag: `"r"`, Source: downexv1.AddSource_ADD_SOURCE_BROWSER, StartPaused: true,
	})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	d := res.GetDownload()
	if res.GetRescuedBytes() != kept || d.GetRescuedBytes() != kept || d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED ||
		d.GetFilename() != "big.iso" || d.GetSize() != rescueSize || d.GetReceived() != kept ||
		d.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER {
		t.Fatalf("rescued %+v", res)
	}
	if _, err := os.Stat(partial); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial still there: %v", err)
	}
	if got := h.get(d.GetId()); got.GetRescuedBytes() != kept {
		t.Fatalf("Get: rescued %d", got.GetRescuedBytes())
	}
	found := false
	for _, l := range h.list() {
		found = found || (l.GetId() == d.GetId() && l.GetRescuedBytes() == kept)
	}
	if !found {
		t.Fatal("List does not show the rescue")
	}
	if got := windows.launched(); len(got) != 0 {
		t.Fatalf("windows opened before the start: %v", got)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), RescueStart: true})
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, 501, rescueSize)
	// Its progress window as it starts, then the download complete dialog in it: the run
	// that downexd opened the window for is windowed (docs/DESKTOP-SPEC.md §8).
	waitLaunches(t, windows, d.GetId(), d.GetId())
	if done.GetRescuedBytes() != kept {
		t.Fatalf("completed rescued %d", done.GetRescuedBytes())
	}
	// The first start is spent.
	_, err = h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId(), RescueStart: true}))
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second rescue_start = %v", err)
	}
	if st := storedState(t, h.st, d.GetId()); st.RescuedBytes != kept {
		t.Fatalf("stored rescued %d", st.RescuedBytes)
	}
}

// TestRescueStartOnlyForRescues: rescue_start is refused for any other download, and a
// UI rescue starts at once without a window from downexd.
func TestRescueStartOnlyForRescues(t *testing.T) {
	windows := &launcher{}
	h := newHarness(t, harnessOptions{openProgress: windows.open})
	o := testserver.Options{Size: rescueSize, Seed: 502}
	srv := serve(t, o)
	d := h.add(&downexv1.AddRequest{Url: srv.URL + "/x", StartPaused: true, Source: downexv1.AddSource_ADD_SOURCE_CLI})
	_, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId(), RescueStart: true}))
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("rescue_start of an add = %v", err)
	}
	_, err = h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId(), RescueStart: true, Restart: true}))
	if connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("rescue_start with restart = %v", err)
	}
	partial := writeRescuePartial(t, h.dir, "ui.bin.part", o, 1<<20+1)
	res, err := h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/ui.bin", PartialPath: partial, Source: downexv1.AddSource_ADD_SOURCE_UI})
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, h.waitCompleted(res.GetDownload().GetId()), 502, rescueSize)
	time.Sleep(150 * time.Millisecond)
	if got := windows.launched(); len(got) != 0 {
		t.Fatalf("downexd opened windows for a UI rescue: %v", got)
	}
}

// TestRescueRefusalCodes: refusals carry their code; messages never name the address.
func TestRescueRefusalCodes(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	o := testserver.Options{Size: rescueSize, Seed: 503}
	changed := serve(t, testserver.Options{Size: rescueSize, Seed: 504})
	noRanges := serve(t, testserver.Options{Size: rescueSize, Seed: 503, NoRanges: true})
	unknown := serve(t, testserver.Options{Size: rescueSize, Seed: 503, UnknownTotal: true})
	expired := serve(t, testserver.Options{Size: rescueSize, Seed: 503, Token: "x"})
	for _, tc := range []struct {
		name    string
		url     string
		partial string
		n       int64
		target  bool // send target_path only
		code    string
		conn    connect.Code
	}{
		{"changed", changed.URL + "/secret-token/a", "a.bin.crdownload", 2 << 20, false, CodeRescueChanged, connect.CodeFailedPrecondition},
		{"no ranges", noRanges.URL + "/b", "b.bin.crdownload", 2 << 20, false, CodeRescueNoRanges, connect.CodeFailedPrecondition},
		{"size unknown", unknown.URL + "/c", "c.bin.crdownload", 2 << 20, false, CodeRescueSizeUnknown, connect.CodeFailedPrecondition},
		{"expired", expired.URL + "/d", "d.bin.crdownload", 2 << 20, false, CodeLinkExpired, connect.CodeFailedPrecondition},
		{"empty", changed.URL + "/e", "e.bin.crdownload", 0, false, CodeRescueEmpty, connect.CodeInvalidArgument},
		{"not found", changed.URL + "/f", "", 0, true, CodeRescueNotFound, connect.CodeFailedPrecondition},
	} {
		r := &downexv1.RescueRequest{Url: tc.url, Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
			Headers: []*downexv1.Header{{Name: "Cookie", Value: "session=hunter2"}}}
		if tc.target {
			r.TargetPath = filepath.Join(h.dir, "nothing.bin")
		} else {
			r.PartialPath = writeRescuePartial(t, h.dir, tc.partial, o, tc.n)
		}
		_, err := h.rescue(r)
		if code := refusalCode(t, err); code != tc.code || connectCode(err) != tc.conn {
			t.Errorf("%s: %v (code %s), want %s / %v", tc.name, err, code, tc.code, tc.conn)
		}
		if msg := err.Error(); strings.Contains(msg, "127.0.0.1") || strings.Contains(msg, "secret-token") || strings.Contains(msg, "hunter2") {
			t.Errorf("%s: message names the address or a header: %s", tc.name, msg)
		}
	}
	if list := h.list(); len(list) != 0 {
		t.Fatalf("refused rescues left downloads: %+v", list)
	}
}

// TestRescueQueueAndConfirmation: a rescue joins its queue's end and is never held for
// confirmation, even with confirm_browser_downloads on.
func TestRescueQueueAndConfirmation(t *testing.T) {
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.ConfirmBrowserDownloads = true }})
	o := testserver.Options{Size: rescueSize, Seed: 505}
	srv := serve(t, o)
	q := h.createQueue(&downexv1.Queue{Name: "Later"})
	partial := writeRescuePartial(t, h.dir, "q.bin.crdownload", o, 1<<20+5)
	res, err := h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/q.bin", PartialPath: partial, QueueId: q.GetId(),
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if err != nil {
		t.Fatal(err)
	}
	d := res.GetDownload()
	if d.GetQueueId() != q.GetId() || d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		t.Fatalf("rescued into queue %d, state %v", d.GetQueueId(), d.GetState())
	}
	if _, err := h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/q.bin", PartialPath: partial, QueueId: 999,
		Source: downexv1.AddSource_ADD_SOURCE_UI}); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown queue = %v", err)
	}
}

// TestRescueSamePathOnce: two rescues of one partial at once: one adopts it, the other
// finds it gone.
func TestRescueSamePathOnce(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	o := testserver.Options{Size: rescueSize, Seed: 506}
	srv := serve(t, o)
	partial := writeRescuePartial(t, h.dir, "twice.bin.crdownload", o, 1<<20+5)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/twice.bin", PartialPath: partial,
				Source: downexv1.AddSource_ADD_SOURCE_UI, StartPaused: true})
		})
	}
	wg.Wait()
	ok, gone := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case refusalCode(t, err) == CodeRescueNotFound:
			gone++
		default:
			t.Errorf("rescue: %v", err)
		}
	}
	if ok != 1 || gone != 1 {
		t.Fatalf("%d adopted, %d not found: %v", ok, gone, errs)
	}
	if n := len(h.list()); n != 1 {
		t.Fatalf("%d downloads", n)
	}
}

// TestRescueBrowserPathRules: a browser's partial must be a .crdownload or .part file in the
// target's folder (docs/DAEMON-SPEC.md §4): anything else is InvalidArgument, never internal.
func TestRescueBrowserPathRules(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	o := testserver.Options{Size: rescueSize, Seed: 507}
	srv := serve(t, o)
	plain := writeRescuePartial(t, h.dir, "notes.txt", o, 1<<20+5)
	_, err := h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/notes.txt", PartialPath: plain,
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("a browser rescue of a .txt file = %v", err)
	}
	part := writeRescuePartial(t, h.dir, "x.bin.part", o, 1<<20+5)
	_, err = h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/x.bin", PartialPath: part,
		TargetPath: filepath.Join(t.TempDir(), "x.bin"), Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("a browser rescue outside the target's folder = %v", err)
	}
	if fi, err := os.Stat(part); err != nil || fi.Size() != 1<<20+5 {
		t.Fatalf("the refused partial: %v, %v", fi, err)
	}
	// Another download's own part file (ENGINE-SPEC §7.4 A1), even for the window or the CLI.
	d := h.add(&downexv1.AddRequest{Url: srv.URL + "/own.bin", Filename: "own.bin", StartPaused: true,
		Source: downexv1.AddSource_ADD_SOURCE_CLI})
	own := filepath.Join(h.dir, d.GetFilename()+".dxpart")
	_, err = h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/own.bin", PartialPath: own,
		Source: downexv1.AddSource_ADD_SOURCE_UI})
	if connectCode(err) != connect.CodeInvalidArgument || refusalCode(t, err) != CodeInvalidRequest {
		t.Fatalf("a rescue of download %d's part file = %v", d.GetId(), err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("the download's part file: %v", err)
	}
}

// TestRescueStartWindowRules: a browser rescue's first start opens no window when it waits
// in a stopped queue (DESKTOP-SPEC §7: only a start at once does), and rescue_start is
// refused once RescueStartWindow has passed since the rescue.
func TestRescueStartWindowRules(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	windows := &launcher{}
	h := newHarness(t, harnessOptions{openProgress: windows.open, now: clock})
	o := testserver.Options{Size: rescueSize, Seed: 508}
	srv := serve(t, o)
	q := h.createQueue(&downexv1.Queue{Name: "Stopped"})
	if q.GetRunning() {
		t.Fatalf("a new queue runs: %+v", q)
	}
	rescue := func(name string, queue int64) *downexv1.Download {
		t.Helper()
		partial := writeRescuePartial(t, h.dir, name+".crdownload", o, 1<<20+9)
		res, err := h.rescue(&downexv1.RescueRequest{Url: srv.URL + "/" + name, PartialPath: partial, QueueId: queue,
			TargetPath: filepath.Join(h.dir, name), Source: downexv1.AddSource_ADD_SOURCE_BROWSER, StartPaused: true})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetDownload()
	}
	waiting := rescue("waits.bin", q.GetId())
	if d := h.resume(&downexv1.ResumeRequest{Id: waiting.GetId(), RescueStart: true}); d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		t.Fatalf("the start in a stopped queue: %v", d.GetState())
	}
	late := rescue("late.bin", 0)
	mu.Lock()
	now = now.Add(RescueStartWindow + time.Second)
	mu.Unlock()
	_, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: late.GetId(), RescueStart: true}))
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("rescue_start after %v = %v", RescueStartWindow, err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := windows.launched(); len(got) != 0 {
		t.Fatalf("windows opened: %v", got)
	}
	// The late rescue is an ordinary paused download: a plain resume starts it.
	h.resume(&downexv1.ResumeRequest{Id: late.GetId()})
	checkFile(t, h.waitCompleted(late.GetId()), 508, rescueSize)
}
