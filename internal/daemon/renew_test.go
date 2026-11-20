package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

const renewSize = 8 << 20

// expiredDownload adds a download of srv's path (token "old"), pauses it midway,
// expires the token and resumes it, so that it fails with link_expired and keeps its
// bytes (docs/ENGINE-SPEC.md §7.2).
func expiredDownload(t *testing.T, h *harness, srv *testserver.Server, path string) *downexv1.Download {
	t.Helper()
	srv.Hold(2 << 20)
	d := h.add(&downexv1.AddRequest{Url: srv.URL + path + "?token=old", Source: downexv1.AddSource_ADD_SOURCE_CLI})
	h.waitReceived(d.GetId(), 1<<20)
	h.pause(d.GetId())
	srv.SetToken("new")
	srv.Release()
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	failed := h.waitState(d.GetId(), downexv1.DownloadState_DOWNLOAD_STATE_FAILED)
	if failed.GetErrorCode() != CodeLinkExpired || failed.GetReceived() <= 0 || failed.GetSize() != renewSize {
		t.Fatalf("expired download: code %q received %d size %d (%s)", failed.GetErrorCode(), failed.GetReceived(),
			failed.GetSize(), failed.GetError())
	}
	return failed
}

func renewServer(t *testing.T, seed uint64) *testserver.Server {
	return serve(t, testserver.Options{Size: renewSize, Seed: seed, ETag: fmt.Sprintf(`"s%d"`, seed), Token: "old"})
}

// noticeRecorder collects the notices of a Manager.
type noticeRecorder struct {
	mu   sync.Mutex
	seen []Notice
}

func recordNotices(t *testing.T, m *Manager) *noticeRecorder {
	r := &noticeRecorder{}
	ch, cancel := m.Subscribe()
	t.Cleanup(cancel)
	go func() {
		for n := range ch {
			r.mu.Lock()
			r.seen = append(r.seen, n)
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *noticeRecorder) has(kind NoticeKind, id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.seen {
		if n.Kind == kind && n.Download.GetId() == id {
			return true
		}
	}
	return false
}

func (h *harness) gone(id int64) bool {
	_, err := h.dl.Get(ctxT(h.t), connect.NewRequest(&downexv1.GetRequest{Id: id}))
	return connectCode(err) == connect.CodeNotFound
}

// TestRenewAutoMatch: a browser hand-off of a fresh link to the same file is held while
// it is checked, then removed, and the download whose link expired continues with its
// link, opens its progress window and is announced (A4, A5, A7).
func TestRenewAutoMatch(t *testing.T) {
	t.Parallel()
	var opened sync.Map
	h := newHarness(t, harnessOptions{
		settings:     func(s *downexv1.Settings) { s.ConfirmBrowserDownloads = false },
		openProgress: func(id int64) error { opened.Store(id, true); return nil },
	})
	notices := recordNotices(t, h.m)
	srv := renewServer(t, 1)
	old := expiredDownload(t, h, srv, "/data.bin")

	n := h.add(&downexv1.AddRequest{Url: srv.URL + "/data.bin?token=new", Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
		ExpectedSize: renewSize, PageUrl: "https://pages.example/download?id=1"})
	if !n.GetRenewCheck() {
		t.Fatalf("new add = %v, want held with renew_check", n)
	}
	waitUntil(t, "the new add removed", func() bool { return h.gone(n.GetId()) })
	done := h.waitCompleted(old.GetId())
	checkFile(t, done, 1, renewSize)
	if done.GetPageUrl() != "https://pages.example/download?id=1" || done.GetUrl() != srv.URL+"/data.bin?token=new" {
		t.Fatalf("renewed download: page %q url %q", done.GetPageUrl(), done.GetUrl())
	}
	waitUntil(t, "the renewal notice", func() bool { return notices.has(NoticeLinkRenewed, old.GetId()) })
	if _, ok := opened.Load(old.GetId()); !ok {
		t.Fatal("the continued download's progress window was not opened")
	}
	if _, ok := opened.Load(n.GetId()); ok {
		t.Fatal("the held add's progress window was opened")
	}
}

// TestRenewAskMode: with renew_links ask, a matching hand-off awaits confirmation with
// an offer; RenewLink from it continues the stopped download and removes it (A8, R2).
func TestRenewAskMode(t *testing.T) {
	t.Parallel()
	var asked sync.Map
	h := newHarness(t, harnessOptions{
		settings:         func(s *downexv1.Settings) { s.RenewLinks = downexv1.RenewLinks_RENEW_LINKS_ASK },
		openConfirmation: func(id int64) error { asked.Store(id, true); return nil },
	})
	srv := renewServer(t, 2)
	old := expiredDownload(t, h, srv, "/movie.mkv")

	n := h.add(&downexv1.AddRequest{Url: srv.URL + "/movie.mkv?token=new", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	offered := h.waitFor(n.GetId(), "an offer", func(d *downexv1.Download) bool { return d.GetRenewOffer() != nil })
	o := offered.GetRenewOffer()
	if offered.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION || offered.GetRenewCheck() ||
		o.GetDownloadId() != old.GetId() || o.GetSize() != renewSize || o.GetReceived() != old.GetReceived() ||
		o.GetFilename() != old.GetFilename() || o.GetNameDiffers() {
		t.Fatalf("offer = %v on %v", o, offered.GetState())
	}
	waitUntil(t, "the confirmation asked", func() bool { _, ok := asked.Load(n.GetId()); return ok })

	res, err := h.dl.RenewLink(ctxT(t), connect.NewRequest(&downexv1.RenewLinkRequest{
		Id: old.GetId(), Link: &downexv1.RenewLinkRequest_FromDownload{FromDownload: n.GetId()}, Start: true,
		AllowNameChange: true,
	}))
	if err != nil {
		t.Fatalf("RenewLink(from_download) = %v", err)
	}
	if res.Msg.GetRestarted() || res.Msg.GetDownload().GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED {
		t.Fatalf("response = %v", res.Msg)
	}
	if !h.gone(n.GetId()) {
		t.Fatal("the awaiting download was not removed")
	}
	checkFile(t, h.waitCompleted(old.GetId()), 2, renewSize)
}

// TestRenewNoMatch: another file under the same name and size is downloaded on its own;
// the download whose link expired stays as it is (A9).
func TestRenewNoMatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	srv := renewServer(t, 3)
	old := expiredDownload(t, h, srv, "/setup.exe")
	other := serve(t, testserver.Options{Size: renewSize, Seed: 4})

	n := h.add(&downexv1.AddRequest{Url: other.URL + "/setup.exe", Source: downexv1.AddSource_ADD_SOURCE_UI})
	if !n.GetRenewCheck() {
		t.Fatal("the add was not checked")
	}
	checkFile(t, h.waitCompleted(n.GetId()), 4, renewSize)
	if got := h.get(old.GetId()); got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_FAILED ||
		got.GetErrorCode() != CodeLinkExpired || got.GetUrl() != old.GetUrl() {
		t.Fatalf("the stopped download changed: %v", got)
	}
}

// TestRenewNotMatched: CLI adds and renew_links off are never held (A1).
func TestRenewNotMatched(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		source downexv1.AddSource
		mode   downexv1.RenewLinks
	}{
		"cli": {downexv1.AddSource_ADD_SOURCE_CLI, downexv1.RenewLinks_RENEW_LINKS_AUTO},
		"off": {downexv1.AddSource_ADD_SOURCE_UI, downexv1.RenewLinks_RENEW_LINKS_OFF},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.RenewLinks = tc.mode }})
			srv := renewServer(t, 5)
			old := expiredDownload(t, h, srv, "/a.iso")
			n := h.add(&downexv1.AddRequest{Url: srv.URL + "/a.iso?token=new", Source: tc.source})
			if n.GetRenewCheck() {
				t.Fatal("the add was held")
			}
			checkFile(t, h.waitCompleted(n.GetId()), 5, renewSize)
			if got := h.get(old.GetId()); got.GetErrorCode() != CodeLinkExpired {
				t.Fatalf("the stopped download changed: %v", got)
			}
		})
	}
}

// TestRenewHoldEndedByPause: a user operation on a held add ends its check without a
// renewal (A6).
func TestRenewHoldEndedByPause(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	srv := renewServer(t, 6)
	old := expiredDownload(t, h, srv, "/b.bin")
	srv.Hold(srv.BytesSent()) // the proof's answers wait
	defer srv.Release()
	n := h.add(&downexv1.AddRequest{Url: srv.URL + "/b.bin?token=new", Source: downexv1.AddSource_ADD_SOURCE_UI})
	if !h.get(n.GetId()).GetRenewCheck() {
		t.Fatal("the add is not held")
	}
	paused := h.pause(n.GetId())
	if paused.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED || paused.GetRenewCheck() {
		t.Fatalf("paused = %v", paused)
	}
	srv.Release()
	if got := h.get(old.GetId()); got.GetErrorCode() != CodeLinkExpired || got.GetUrl() != old.GetUrl() {
		t.Fatalf("the stopped download changed: %v", got)
	}
}

// TestRenewHoldEndedByOptions: an operation that ends a held add's check without
// scheduling anything itself (SetDownloadOptions changing nothing) still lets the add
// start: it is queued and the scheduler runs (A6, A9).
func TestRenewHoldEndedByOptions(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	srv := renewServer(t, 9)
	old := expiredDownload(t, h, srv, "/e.bin")
	srv.Hold(srv.BytesSent()) // the proof's answers wait
	defer srv.Release()
	n := h.add(&downexv1.AddRequest{Url: srv.URL + "/e.bin?token=new", Source: downexv1.AddSource_ADD_SOURCE_UI})
	if !n.GetRenewCheck() {
		t.Fatal("the add is not held")
	}
	if _, err := h.dl.SetDownloadOptions(ctxT(t), connect.NewRequest(&downexv1.SetDownloadOptionsRequest{Id: n.GetId()})); err != nil {
		t.Fatalf("SetDownloadOptions = %v", err)
	}
	srv.Release()
	checkFile(t, h.waitCompleted(n.GetId()), 9, renewSize)
	if got := h.get(old.GetId()); got.GetErrorCode() != CodeLinkExpired || got.GetUrl() != old.GetUrl() {
		t.Fatalf("the stopped download changed: %v", got)
	}
}

// TestRenewHoldPauseAll: Pause all pauses a queued add held for its check too, which
// would otherwise start once the check ends.
func TestRenewHoldPauseAll(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	srv := renewServer(t, 10)
	expiredDownload(t, h, srv, "/p.bin")
	srv.Hold(srv.BytesSent()) // the proof's answers wait
	defer srv.Release()
	n := h.add(&downexv1.AddRequest{Url: srv.URL + "/p.bin?token=new", Source: downexv1.AddSource_ADD_SOURCE_UI})
	if !n.GetRenewCheck() {
		t.Fatal("the add is not held")
	}
	if _, err := h.dl.PauseAll(ctxT(t), connect.NewRequest(&downexv1.PauseAllRequest{})); err != nil {
		t.Fatalf("PauseAll = %v", err)
	}
	srv.Release()
	if got := h.get(n.GetId()); got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED || got.GetRenewCheck() {
		t.Fatalf("after Pause all: %v", got)
	}
}

// TestRenewLinkRPC: a pasted link continues the download; refusals carry their code in
// the message and as Downex-Error-Code, and change nothing (R1-R4, C2).
func TestRenewLinkRPC(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	srv := renewServer(t, 7)
	old := expiredDownload(t, h, srv, "/c.bin")
	renew := func(r *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
		r.Id = old.GetId()
		res, err := h.dl.RenewLink(ctxT(t), connect.NewRequest(r))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	link := func(u string) *downexv1.RenewLinkRequest_Url { return &downexv1.RenewLinkRequest_Url{Url: u} }
	other := serve(t, testserver.Options{Size: renewSize, Seed: 70})
	bigger := serve(t, testserver.Options{Size: renewSize + 1, Seed: 7})
	renamed := serve(t, testserver.Options{Size: renewSize, Seed: 7, Filename: "d.bin"})
	for _, tc := range []struct {
		name string
		url  string
		code string
	}{
		{"other bytes", other.URL + "/c.bin", CodeRenewMismatch},
		{"other size", bigger.URL + "/c.bin", CodeRenewSize},
		{"other name", renamed.URL + "/x", CodeRenewName},
		{"expired too", srv.URL + "/c.bin?token=old", CodeLinkExpired},
	} {
		_, err := renew(&downexv1.RenewLinkRequest{Link: link(tc.url)})
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition || ce.Meta().Get(ErrorCodeMeta) != tc.code ||
			!strings.HasPrefix(ce.Message(), tc.code+": ") {
			t.Fatalf("%s: RenewLink = %v (meta %q)", tc.name, err, ce.Meta().Get(ErrorCodeMeta))
		}
		if got := h.get(old.GetId()); got.GetUrl() != old.GetUrl() || got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_FAILED {
			t.Fatalf("%s: the download changed: %v", tc.name, got)
		}
	}
	for _, tc := range []struct {
		name string
		r    *downexv1.RenewLinkRequest
		code connect.Code
	}{
		{"no link", &downexv1.RenewLinkRequest{}, connect.CodeInvalidArgument},
		{"ftp", &downexv1.RenewLinkRequest{Link: link("ftp://example.test/c.bin")}, connect.CodeInvalidArgument},
		{"bad page", &downexv1.RenewLinkRequest{Link: link(srv.URL), PageUrl: "file:///etc"}, connect.CodeInvalidArgument},
		{"self", &downexv1.RenewLinkRequest{Link: &downexv1.RenewLinkRequest_FromDownload{FromDownload: old.GetId()}}, connect.CodeInvalidArgument},
		{"not awaiting", &downexv1.RenewLinkRequest{Link: &downexv1.RenewLinkRequest_FromDownload{FromDownload: 999}}, connect.CodeNotFound},
	} {
		if _, err := renew(tc.r); connectCode(err) != tc.code {
			t.Fatalf("%s: RenewLink = %v, want %v", tc.name, err, tc.code)
		}
	}

	res, err := renew(&downexv1.RenewLinkRequest{Link: link(renamed.URL + "/x"), AllowNameChange: true, Start: true,
		PageUrl: "https://pages.example/c"})
	if err != nil || !res.GetNameDiffers() || res.GetRestarted() {
		t.Fatalf("RenewLink(name ok) = %v, %v", res, err)
	}
	done := h.waitCompleted(old.GetId())
	checkFile(t, done, 7, renewSize)
	if done.GetFilename() != old.GetFilename() || done.GetPageUrl() != "https://pages.example/c" {
		t.Fatalf("renewed: name %q page %q", done.GetFilename(), done.GetPageUrl())
	}
	if _, err := renew(&downexv1.RenewLinkRequest{Link: link(srv.URL + "/c.bin?token=new")}); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("RenewLink(completed) = %v", err)
	}
}

// TestRenewLinkRunning: a running download is refused.
func TestRenewLinkRunning(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	srv := serve(t, testserver.Options{Size: renewSize, Seed: 8})
	srv.Hold(1 << 20)
	defer srv.Release()
	d := h.add(&downexv1.AddRequest{Url: srv.URL + "/r.bin", Source: downexv1.AddSource_ADD_SOURCE_CLI})
	h.waitReceived(d.GetId(), 1)
	_, err := h.dl.RenewLink(ctxT(t), connect.NewRequest(&downexv1.RenewLinkRequest{Id: d.GetId(),
		Link: &downexv1.RenewLinkRequest_Url{Url: srv.URL + "/r2.bin"}}))
	if connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "pause it first") {
		t.Fatalf("RenewLink(running) = %v", err)
	}
}

func TestRenewHeaders(t *testing.T) {
	stored := model.Request{URL: "https://a.example/f", Headers: http.Header{
		"Cookie": {"c=1"}, "Authorization": {"Bearer x"}, "Referer": {"https://a.example/"}, "User-Agent": {"ua"},
	}}
	same := engine.RenewHeaders(stored, "https://a.example:443/g?t=2")
	if same.Get("Cookie") != "c=1" || same.Get("Authorization") == "" {
		t.Fatalf("same origin: %v", same)
	}
	cross := engine.RenewHeaders(stored, "https://b.example/f")
	if cross.Get("Cookie") != "" || cross.Get("Authorization") != "" || cross.Get("Referer") == "" || cross.Get("User-Agent") != "ua" {
		t.Fatalf("other origin: %v", cross)
	}
	if stored.Headers.Get("Cookie") == "" {
		t.Fatal("the stored headers were changed")
	}
}

func TestPageURL(t *testing.T) {
	withRef := func(ref string) *model.Download {
		return &model.Download{Request: model.Request{Headers: http.Header{"Referer": {ref}}}}
	}
	for _, tc := range []struct {
		d    *model.Download
		want string
	}{
		{&model.Download{PageURL: "https://p.example/x"}, "https://p.example/x"},
		{withRef("https://p.example/watch?v=1"), "https://p.example/watch?v=1"},
		{withRef("https://p.example/files/"), "https://p.example/files/"},
		{withRef("https://p.example/"), ""},
		{withRef("https://p.example"), ""},
		{withRef("https://p.example/?q=1"), "https://p.example/?q=1"},
		{withRef("ftp://p.example/x"), ""},
		{withRef("https://u:p@p.example/x"), ""},
		{&model.Download{}, ""},
	} {
		if got := dlview.PageURL(tc.d); got != tc.want {
			t.Errorf("pageURL(%+v) = %q, want %q", tc.d.Request.Headers, got, tc.want)
		}
	}
	for raw, ok := range map[string]bool{"": true, "https://p.example/a": true, "http://p.example": true,
		"javascript:alert(1)": false, "https://u:p@p.example/": false, "file:///c:/x": false} {
		if _, err := parsePageURL(raw); (err == nil) != ok {
			t.Errorf("parsePageURL(%q) = %v", raw, err)
		}
	}
}

func TestLinkExpiredCodes(t *testing.T) {
	live := fmt.Errorf("connection 2: bytes 0-9: HTTP 403 Forbidden: %w", engine.ErrLinkExpired)
	if got := ErrorCode(live); got != CodeLinkExpired {
		t.Fatalf("ErrorCode = %q", got)
	}
	if got := codeFromMessage(live.Error()); got != CodeLinkExpired {
		t.Fatalf("codeFromMessage = %q", got)
	}
	if got := codeFromMessage("probe: unexpected HTTP status 404 Not Found"); got != CodeHTTPStatus {
		t.Fatalf("plain 404 = %q", got)
	}
	for err, want := range map[error]string{
		fmt.Errorf("x: %w", engine.ErrLinkMismatch):  CodeRenewMismatch,
		fmt.Errorf("x: %w", engine.ErrLinkSize):      CodeRenewSize,
		fmt.Errorf("x: %w", engine.ErrLinkChanged):   CodeRenewChanged,
		fmt.Errorf("x: %w", engine.ErrLinkNoRanges):  CodeRenewNoRanges,
		fmt.Errorf("x: %w", engine.ErrLinkUnproven):  CodeRenewUnproven,
		&engine.NameError{ID: 1, New: "b", Old: "a"}: CodeRenewName,
		live: CodeLinkExpired,
	} {
		if got := renewErrorCode(err); got != want {
			t.Errorf("renewErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
	stale := renewRefusal(context.Background(), 3, fmt.Errorf("x: %w", engine.ErrLinkStale))
	if connectCode(stale) != connect.CodeFailedPrecondition || !strings.Contains(stale.Error(), "changed during the check") {
		t.Fatalf("stale = %v", stale)
	}
}

// TestRenewLinksSetting: the setting round-trips, an unknown value is refused, and a
// settings file with an unknown value is repaired to auto.
func TestRenewLinksSetting(t *testing.T) {
	p := protoSettings(DefaultSettings())
	if p.GetRenewLinks() != downexv1.RenewLinks_RENEW_LINKS_AUTO {
		t.Fatalf("default = %v", p.GetRenewLinks())
	}
	for _, v := range []downexv1.RenewLinks{downexv1.RenewLinks_RENEW_LINKS_ASK, downexv1.RenewLinks_RENEW_LINKS_OFF} {
		p.RenewLinks = v
		d, err := desktopFromProto(p).normalize()
		if err != nil {
			t.Fatal(err)
		}
		var q downexv1.Settings
		d.toProto(&q)
		if q.GetRenewLinks() != v {
			t.Fatalf("round trip of %v = %v", v, q.GetRenewLinks())
		}
	}
	p.RenewLinks = downexv1.RenewLinks_RENEW_LINKS_UNSPECIFIED
	if d, _ := desktopFromProto(p).normalize(); d.RenewMode() != RenewLinksAuto {
		t.Fatalf("unspecified = %q", d.RenewLinks)
	}
	p.RenewLinks = 9
	if _, err := desktopFromProto(p).normalize(); err == nil || !strings.Contains(err.Error(), "renew_links must be auto, ask or off") {
		t.Fatalf("unknown value: %v", err)
	}
	bad := DefaultDesktopSettings()
	bad.RenewLinks = "sometimes"
	fixed, problems := bad.repair()
	if fixed.RenewMode() != RenewLinksAuto || len(problems) != 1 {
		t.Fatalf("repair = %q, %v", fixed.RenewLinks, problems)
	}
}
