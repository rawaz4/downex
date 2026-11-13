package e2e

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cryptorand "crypto/rand"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/tools"
)

// Site media (docs/SITE-MEDIA-SPEC.md §8.2), with real executables: downexd, built with
// the e2etest hooks, resolves web pages with a stand-in yt-dlp (this test binary installed
// as yt-dlp(.exe): it prints the JSON a test configured for the page and records its
// arguments, its environment's names and its cookie file) found on PATH next to a
// stand-in Deno, or installed from a local TLS release server, and downloads the formats
// from a local site that checks the length and the number of requests.

// ---- stand-in tools ------------------------------------------------------------------

const (
	// envFakeYtDlp names the stand-in yt-dlp's pages: a JSON object of fakePage by URL path.
	envFakeYtDlp = "DOWNEX_E2E_FAKE_YTDLP"
	// envFakeLog names the file the stand-in yt-dlp appends one fakeRun to per extraction.
	envFakeLog = "DOWNEX_E2E_FAKE_LOG"
	// fakeDenoVersion is what the stand-in Deno found on PATH says (yt-dlp needs >= 2.3.0).
	fakeDenoVersion = "2.5.4"
)

// Version folders of managed copies (<tools>/<tool>/<version>/): a stand-in installed
// there says that version, as the smoke test expects.
var (
	ytdlpVersionDir = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(\.\d{1,9})?$`)
	denoVersionDir  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// fakePage is what the stand-in yt-dlp answers for one page.
type fakePage struct {
	// Stdout is printed with "{{run}}" replaced by the number of this extraction of the
	// page (1, 2, ...): a site's format addresses change with every extraction.
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

// fakeRun is one recorded extraction.
type fakeRun struct {
	Nonce   string   `json:"nonce"`
	Page    string   `json:"page"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`     // the names of the variables it was given
	Dir     string   `json:"dir"`     // its working directory
	Cookies string   `json:"cookies"` // the content of its --cookies file
}

// fakeTool runs this executable as a stand-in tool when it was installed as one,
// which its file name tells: ok is false otherwise.
func fakeTool() (code int, ok bool) {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	dir := filepath.Base(filepath.Dir(os.Args[0]))
	args := os.Args[1:]
	switch name {
	case "deno":
		if len(args) == 1 && args[0] == "--version" {
			v := fakeDenoVersion
			if denoVersionDir.MatchString(dir) {
				v = dir
			}
			fmt.Printf("deno %s (stable, release, e2e)\nv8 13.0\ntypescript 5.9\n", v)
			return 0, true
		}
		return 1, true
	case "yt-dlp":
		if len(args) == 1 && args[0] == "--version" {
			v := time.Now().UTC().Format("2006.01.02") // a PATH copy must be recent
			if ytdlpVersionDir.MatchString(dir) {
				v = dir
			}
			fmt.Println(v)
			return 0, true
		}
		return fakeExtract(args), true
	}
	return 0, false
}

// fakeExtract is the stand-in yt-dlp's extraction: it records the run and answers what
// the configuration holds for the page (the argument after "--"), else an extractor
// error as yt-dlp prints it.
func fakeExtract(args []string) int {
	cfgPath, logPath := os.Getenv(envFakeYtDlp), os.Getenv(envFakeLog)
	if cfgPath == "" || logPath == "" || len(args) < 2 || args[len(args)-2] != "--" {
		fmt.Fprintln(os.Stderr, "ERROR: the stand-in yt-dlp has no configuration")
		return 1
	}
	page := args[len(args)-1]
	nonce := make([]byte, 8)
	_, _ = cryptorand.Read(nonce)
	run := fakeRun{Nonce: hex.EncodeToString(nonce), Page: page, Args: args}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k != "" {
			run.Env = append(run.Env, k)
		}
	}
	run.Dir, _ = os.Getwd()
	if i := slices.Index(args, "--cookies"); i >= 0 && i+1 < len(args) {
		b, _ := os.ReadFile(args[i+1])
		run.Cookies = string(b)
	}
	n, err := appendFakeRun(logPath, run)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: stand-in log:", err)
		return 1
	}
	var pages map[string]fakePage
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		err = json.Unmarshal(data, &pages)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: stand-in configuration:", err)
		return 1
	}
	u, err := url.Parse(page)
	p, found := fakePage{}, false
	if err == nil {
		p, found = pages[u.Path]
	}
	if !found {
		fmt.Fprintf(os.Stderr, "ERROR: Unsupported URL: %s\n", page)
		return 1
	}
	_, _ = os.Stdout.WriteString(strings.ReplaceAll(p.Stdout, "{{run}}", strconv.Itoa(n)))
	_, _ = os.Stderr.WriteString(p.Stderr)
	return p.Exit
}

// appendFakeRun appends run to the log at path and returns its number among the runs of
// its page, from 1.
func appendFakeRun(path string, run fakeRun) (int, error) {
	b, err := json.Marshal(run)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	runs, err := readFakeRuns(path)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.Page == run.Page {
			n++
		}
		if r.Nonce == run.Nonce {
			return n, nil
		}
	}
	return 0, errors.New("the run is not in the log")
}

func readFakeRuns(path string) ([]fakeRun, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []fakeRun
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r fakeRun
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("log line %q: %w", line, err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// ---- the site ------------------------------------------------------------------------

// siteServer serves the formats of the stand-in pages at /media/<format>-<run>.<ext> (any
// run: the stand-in names the addresses of each extraction differently, as a site does),
// honouring Range, and records every request and how many ran at once.
type siteServer struct {
	srv       *httptest.Server
	mu        sync.Mutex
	files     map[string][]byte // format id → content
	reqs      []siteRequest
	active    int
	maxActive int
	hold      func(siteRequest) bool  // held back while it reports true (setHold)
	forbid    func(siteRequest) bool  // answered with 403 Forbidden (an expired address)
	cut       func(siteRequest) int64 // sends only this many bytes of the range, then stalls (setCut)
	wake      chan struct{}           // closed when hold or cut changes
}

// siteRequest is one request for a format.
type siteRequest struct {
	Format     string
	Run        int
	Start, End int64 // the requested bytes, End included; -1, -1 without a Range
	Status     int   // the answer; 0 while it runs, -1 when the client gave up
}

func newSiteServer(t *testing.T) *siteServer {
	t.Helper()
	s := &siteServer{files: map[string][]byte{}, wake: make(chan struct{})}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	t.Cleanup(func() { s.setHold(nil); s.setCut(nil) }) // first: Close waits for held requests
	return s
}

func (s *siteServer) add(format string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[format] = data
}

// mediaPath splits "/media/<format>-<run>.<ext>".
func mediaPath(p string) (format string, run int, ok bool) {
	name, found := strings.CutPrefix(p, "/media/")
	dot := strings.LastIndexByte(name, '.')
	dash := strings.LastIndexByte(name, '-')
	if !found || dot < 0 || dash < 1 || dash > dot {
		return "", 0, false
	}
	run, err := strconv.Atoi(name[dash+1 : dot])
	return name[:dash], run, err == nil
}

func (s *siteServer) serve(w http.ResponseWriter, r *http.Request) {
	format, run, ok := mediaPath(r.URL.Path)
	s.mu.Lock()
	data, found := s.files[format]
	s.mu.Unlock()
	if !ok || !found || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	req := siteRequest{Format: format, Run: run, Start: -1, End: -1}
	if v := r.Header.Get("Range"); v != "" {
		if req.Start, req.End, ok = parseRange(v); !ok {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	i := len(s.reqs)
	s.reqs = append(s.reqs, req)
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	s.mu.Unlock()
	status := -1
	defer func() {
		s.mu.Lock()
		s.active--
		s.reqs[i].Status = status
		s.mu.Unlock()
	}()
	if !s.pass(r, req) {
		return
	}
	s.mu.Lock()
	forbid := s.forbid
	s.mu.Unlock()
	if forbid != nil && forbid(req) {
		status = http.StatusForbidden
		http.Error(w, "the address has expired", status)
		return
	}
	if n := s.cutAt(req); n > 0 && req.Start >= 0 && req.Start < int64(len(data)) {
		// A response that stops after n bytes, until the client gives up or the cut is
		// lifted.
		end := min(req.End, int64(len(data))-1)
		n = min(n, end-req.Start+1)
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", req.Start, end, len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-req.Start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[req.Start : req.Start+n])
		w.(http.Flusher).Flush()
		if !s.passCut(r, req) {
			return
		}
		if _, err := w.Write(data[req.Start+n : end+1]); err == nil {
			status = http.StatusPartialContent
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
	http.ServeContent(sw, r, "", time.Time{}, bytes.NewReader(data))
	if r.Context().Err() == nil {
		status = sw.code
	}
}

// statusWriter records the status of a response.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// pass waits while hold holds req back; false when the client gave up.
func (s *siteServer) pass(r *http.Request, req siteRequest) bool {
	for {
		s.mu.Lock()
		hold, wake := s.hold, s.wake
		s.mu.Unlock()
		if hold == nil || !hold(req) {
			return true
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return false
		}
	}
}

// cutAt is the number of bytes the cut lets through for req (0: all of them).
func (s *siteServer) cutAt(req siteRequest) int64 {
	s.mu.Lock()
	cut := s.cut
	s.mu.Unlock()
	if cut == nil {
		return 0
	}
	return cut(req)
}

// passCut waits while the cut stalls req; false when the client gave up.
func (s *siteServer) passCut(r *http.Request, req siteRequest) bool {
	for {
		s.mu.Lock()
		wake := s.wake
		s.mu.Unlock()
		if s.cutAt(req) == 0 {
			return true
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return false
		}
	}
}

// setCut makes the requests cut approves stop after the bytes it returns (nil: none).
func (s *siteServer) setCut(cut func(siteRequest) int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cut = cut
	close(s.wake)
	s.wake = make(chan struct{})
}

// setHold holds back the requests hold approves (nil: none).
func (s *siteServer) setHold(hold func(siteRequest) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hold = hold
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *siteServer) setForbid(forbid func(siteRequest) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forbid = forbid
}

// requests returns the requests so far (from the index from on).
func (s *siteServer) requests(from int) []siteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs[min(from, len(s.reqs)):])
}

// served counts the complete answers (200 or 206) for format of extraction run.
func (s *siteServer) served(format string, run int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.reqs {
		if r.Format == format && r.Run == run && (r.Status == http.StatusOK || r.Status == http.StatusPartialContent) {
			n++
		}
	}
	return n
}

func (s *siteServer) maxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}

// file is the track as one fragmented MP4 file: its initialization segment, then every
// media segment (ftyp, moov with mvex, then moof/mdat pairs), as a site serves a format.
func (g *genTrack) file() []byte {
	return slices.Concat(append([][]byte{g.init}, g.segs...)...)
}

// siteSetup is one test's site: the server, the stand-in's pages and log, the stand-in
// tools on PATH and the environment of downexd.
type siteSetup struct {
	h        *harness
	srv      *siteServer
	config   string // the stand-in's pages
	log      string // the stand-in's runs
	pathDir  string // the PATH folder of the stand-in tools
	toolsDir string // the daemon's tools folder: next to its database
	pages    map[string]fakePage
	env      []string // the harness environment plus PATH and the stand-in's variables
}

// newSite sets up a site whose pages the stand-in yt-dlp describes. The stand-in Deno is
// on downexd's PATH, and the stand-in yt-dlp with withYtDlp; PATH keeps the folders of
// this process's PATH that hold neither tool (the race detector's runtime may need them).
// extra holds more variables for downexd.
func (h *harness) newSite(withYtDlp bool, extra map[string]string) *siteSetup {
	h.t.Helper()
	s := &siteSetup{
		h: h, srv: newSiteServer(h.t), config: filepath.Join(h.root, "fake-pages.json"),
		log: filepath.Join(h.root, "fake-runs.jsonl"), pathDir: filepath.Join(h.root, "path"),
		toolsDir: filepath.Join(filepath.Dir(h.db), "tools"), pages: map[string]fakePage{},
	}
	if err := os.MkdirAll(s.pathDir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		h.t.Fatal(err)
	}
	if withYtDlp {
		installFile(h.t, self, filepath.Join(s.pathDir, exeName("yt-dlp")))
	}
	installFile(h.t, self, filepath.Join(s.pathDir, exeName("deno")))
	path := []string{s.pathDir}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" && !hasFile(filepath.Join(dir, exeName("yt-dlp"))) && !hasFile(filepath.Join(dir, exeName("deno"))) {
			path = append(path, dir)
		}
	}
	overrides := map[string]string{
		"PATH": strings.Join(path, string(os.PathListSeparator)), envFakeYtDlp: s.config, envFakeLog: s.log,
	}
	for k, v := range extra {
		overrides[k] = v
	}
	s.env = withOverrides(h.env, overrides)
	s.writePages()
	return s
}

func hasFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func (s *siteSetup) writePages() {
	s.h.t.Helper()
	b, err := json.Marshal(s.pages)
	if err != nil {
		s.h.t.Fatal(err)
	}
	if err := os.WriteFile(s.config, b, 0o600); err != nil {
		s.h.t.Fatal(err)
	}
}

// page adds a page at path (on the site's host) and returns its URL.
func (s *siteSetup) page(path string, p fakePage) string {
	s.h.t.Helper()
	s.pages[path] = p
	s.writePages()
	return s.srv.srv.URL + path
}

// videoPage adds a page with a video titled title, offering formats (format), and
// returns its URL.
func (s *siteSetup) videoPage(path, title string, formats ...map[string]any) string {
	s.h.t.Helper()
	b, err := json.Marshal(map[string]any{
		"id": "e2e" + strings.ReplaceAll(path, "/", "-"), "title": title, "extractor_key": "Youtube", "duration": 10,
		"live_status": "not_live", "formats": formats,
	})
	if err != nil {
		s.h.t.Fatal(err)
	}
	return s.page(path, fakePage{Stdout: string(b) + "\n"})
}

// format serves g's single file as format id and describes it as yt-dlp does
// (docs/SITE-MEDIA-SPEC.md §3.10): a DASH video-only H.264 format, or an AAC audio one.
func (s *siteSetup) format(id string, g *genTrack, bandwidthKbps int) map[string]any {
	data := g.file()
	s.srv.add(id, data)
	f := map[string]any{"format_id": id, "protocol": "http", "filesize": len(data), "tbr": bandwidthKbps}
	if g.video {
		f["url"] = s.srv.srv.URL + "/media/" + id + "-{{run}}.mp4"
		f["ext"], f["container"], f["vcodec"], f["acodec"] = "mp4", "mp4_dash", "avc1.4d401e", "none"
		f["width"], f["height"], f["fps"], f["vbr"] = g.width, g.height, 25, bandwidthKbps
		f["format_note"] = strconv.Itoa(g.height) + "p"
	} else {
		f["url"] = s.srv.srv.URL + "/media/" + id + "-{{run}}.m4a"
		f["ext"], f["container"], f["vcodec"], f["acodec"] = "m4a", "m4a_dash", "none", "mp4a.40.2"
		f["abr"], f["asr"], f["language"], f["language_preference"] = bandwidthKbps, audioTimescale, "en", 10
		f["format_note"] = "English original"
	}
	return f
}

// runs returns the stand-in yt-dlp's extractions of page.
func (s *siteSetup) runs(page string) []fakeRun {
	s.h.t.Helper()
	runs, err := readFakeRuns(s.log)
	if err != nil {
		s.h.t.Fatal(err)
	}
	// The tool records its working folder as getcwd returns it, with symbolic links
	// resolved (macOS: /var -> /private/var); give it back the spelling downexd used.
	if real, err := filepath.EvalSymlinks(s.h.root); err == nil && real != s.h.root {
		for i := range runs {
			if rel, err := filepath.Rel(real, runs[i].Dir); err == nil && !strings.HasPrefix(rel, "..") {
				runs[i].Dir = filepath.Join(s.h.root, rel)
			}
		}
	}
	return slices.DeleteFunc(runs, func(r fakeRun) bool { return r.Page != page })
}

// siteNetwork makes downexd connect directly (yt-dlp gets --proxy "") and adds the host
// rule of the site: at most 2 connections and requests of at most 1 MiB.
func (h *harness) siteNetwork() {
	h.t.Helper()
	s := h.getSettings()
	s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_NONE}
	s.HostRules = append(s.HostRules, &downexv1.HostRule{Pattern: "127.0.0.1", MaxConnections: 2, MaxRequestBytes: 1 << 20})
	if _, err := h.updateSettings(s); err != nil {
		h.t.Fatalf("update settings: %v", err)
	}
}

// probeSite asks downexd about a page (ProbeMedia with origin SITE) within timeout.
func (h *harness) probeSite(page string, timeout time.Duration) (*downexv1.MediaInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := h.dl.ProbeMedia(ctx, connect.NewRequest(&downexv1.ProbeMediaRequest{
		PageUrl: page, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE,
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetMedia(), nil
}

// refusalCode is the Downex-Error-Code of a refused call ("" for none).
func refusalCode(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Meta().Get("Downex-Error-Code")
	}
	return ""
}

// siteMediaView is the part of "downex media --list --json" the tests read.
type siteMediaView struct {
	Origin        string `json:"origin"`
	Site          string `json:"site"`
	Format        string `json:"format"`
	Container     string `json:"container"`
	SelectedVideo string `json:"selectedVideo"`
	SelectedAudio string `json:"selectedAudio"`
	Tracks        []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Height int    `json:"height"`
		Size   int64  `json:"size"`
		Ext    string `json:"ext"`
	} `json:"tracks"`
}

// toolsStatusView is the part of "downex tools --json" the tests read.
type toolsStatusView struct {
	Ready bool `json:"ready"`
	Tools []struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		Version   string `json:"version"`
		Path      string `json:"path"`
		Managed   bool   `json:"managed"`
		ErrorCode string `json:"errorCode"`
		Verified  string `json:"verified"`
	} `json:"tools"`
}

func (h *harness) toolsCLI() toolsStatusView {
	h.t.Helper()
	r := h.cli("tools", "--json").ok(h.t)
	var v toolsStatusView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		h.t.Fatalf("tools --json: %v: %v", err, r)
	}
	return v
}

// chunk is the number of the 1 MiB chunk a request asks for.
func chunk(r siteRequest) int64 { return r.Start / (1 << 20) }

// checkRequests checks the requests for the site's formats: each asks for a byte range
// within one 1 MiB chunk (the host rule's max_request_bytes), and at most 2 ran at once
// (its max_connections).
func (s *siteSetup) checkRequests(t *testing.T) {
	t.Helper()
	for _, r := range s.srv.requests(0) {
		if r.Start < 0 || r.End-r.Start+1 > 1<<20 || chunk(r) != r.End/(1<<20) {
			t.Errorf("request %+v is not a range of one 1 MiB chunk", r)
		}
	}
	if n := s.srv.maxConcurrent(); n > 2 {
		t.Errorf("%d requests ran at once, more than the host rule's 2", n)
	}
}

// ---- generated video -------------------------------------------------------------------

// siteTracks are the formats of the stand-in video pages: a 320x240 video of about 5 MiB
// (format 137), a 176x144 one of about 2 MiB (136) and an AAC audio of about 1 MiB (140),
// 10 seconds each.
type siteTracks struct {
	video, small, audio *genTrack
}

var (
	siteTracksOnce sync.Once
	siteTracksMade siteTracks
)

func newSiteTracks(t *testing.T) siteTracks {
	t.Helper()
	siteTracksOnce.Do(func() {
		siteTracksMade = siteTracks{
			video: newSizedTrack(t, true, 320, 240, 10, 701, false, 16<<10, 8<<10),
			small: newSizedTrack(t, true, 176, 144, 10, 702, false, 6<<10, 4<<10),
			audio: newSizedTrack(t, false, 0, 0, 10, 703, false, 1536, 1024),
		}
	})
	if siteTracksMade.video == nil {
		t.Fatal("the site's tracks could not be generated")
	}
	return siteTracksMade
}

// talkPage adds the page of a video with all three formats, and a fourth that yt-dlp
// calls AV1 in the class of 137 (240p), which the one-per-class rule never offers
// (docs/SITE-MEDIA-SPEC.md amendment 2026-10-23: H.264 wins) and nothing fetches.
func (s *siteSetup) talkPage(path string, st siteTracks) string {
	av1 := s.format("395", st.video, 6000)
	av1["vcodec"] = "av01.0.00M.08"
	return s.videoPage(path, "An e2e talk", s.format("137", st.video, 4000), av1, s.format("136", st.small, 1500),
		s.format("140", st.audio, 128))
}

// ---- tests -----------------------------------------------------------------------------

// A page whose yt-dlp and Deno are on PATH (docs/SITE-MEDIA-SPEC.md §8.2 steps 2, 3 and
// 9): the release server is never asked; "media --site --list" lists one MP4 video per
// quality class best first, by its standard name, with the default pick (the AV1 format
// of 137's class is not listed); the download with a sign-in cookie respects the host
// rule (1 MiB requests, 2 connections) and becomes one progressive MP4 of the chosen
// video and audio; yt-dlp got exactly the arguments of §4.7, the cookie only in its
// cookie file and an environment from the allow-list.
func TestSiteVideoFromPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	release := newReleaseServer(t)
	s := h.newSite(true, map[string]string{"DOWNEX_E2E_TOOLS_BASE": release.srv.URL})
	st := newSiteTracks(t)
	page := s.talkPage("/watch/talk", st)
	d := h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.siteNetwork()

	r := h.cli("media", "--site", page, "--list", "--json").ok(t)
	var v siteMediaView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("media --list --json: %v: %v", err, r)
	}
	var ids []string
	for _, tr := range v.Tracks {
		ids = append(ids, tr.ID)
	}
	if v.Origin != "site" || v.Site != "YouTube" || v.Format != "http" || v.Container != "mp4" ||
		!slices.Equal(ids, []string{"137", "136", "140"}) || v.SelectedVideo != "137" || v.SelectedAudio != "140" ||
		v.Tracks[0].Size != int64(len(st.video.file())) || v.Tracks[0].Ext != "mp4" || v.Tracks[2].Ext != "m4a" {
		t.Fatalf("media --list --json = %+v", v)
	}
	r = h.cli("media", "--site", page, "--list").ok(t)
	if !strings.Contains(r.stdout, "240p") || !strings.Contains(r.stdout, "144p") || !strings.Contains(r.stdout, "H.264") ||
		!strings.Contains(r.stdout, "*") || !strings.Contains(r.stdout, "320x240 25fps") || strings.Contains(r.stdout, "AV1") ||
		strings.Contains(r.stdout, "395") {
		t.Errorf("media --list:\n%s", r.stdout)
	}

	const secret = "e2e-secret-cookie-value"
	r = h.cli("media", "--site", page, "-H", "Cookie: sid="+secret, "-o", h.out).ok(t)
	id := r.id(t)
	dl := h.waitCompleted(id)
	if dl.GetFilename() != "An e2e talk.mp4" || dl.GetUrl() != page || dl.GetMedia().GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE ||
		dl.GetMedia().GetSelectedVideo() != "137" || dl.GetMedia().GetSelectedAudio() != "140" {
		t.Fatalf("completed download = %v", dl)
	}
	checkMP4(t, filepath.Join(h.out, "An e2e talk.mp4"), st.video, st.audio)
	noMediaLeftovers(t, h.out)
	s.checkRequests(t)

	// Two extractions: the list's, and the add's with the cookie (another cache entry),
	// which the run reused.
	runs := s.runs(page)
	if len(runs) != 2 {
		t.Fatalf("%d extractions of the page, want 2: %+v", len(runs), runs)
	}
	run := runs[1]
	cookies := filepath.Join(run.Dir, "cookies.txt")
	want := []string{"--ignore-config", "--no-plugin-dirs", "--no-update", "--color", "no_color", "--encoding", "utf-8",
		"--compat-options", "no-certifi", "--no-js-runtimes", "--js-runtimes", "deno:" + filepath.Join(s.pathDir, exeName("deno")),
		"--cache-dir", filepath.Join(s.toolsDir, "cache", "yt-dlp"), "--socket-timeout", "30", "--proxy", "",
		"--cookies", cookies, "--no-playlist", "--flat-playlist", "--ignore-no-formats-error",
		"--extractor-args", "youtube:skip=translated_subs,hls,dash", "-J", "--", page}
	if !slices.Equal(run.Args, want) {
		t.Errorf("yt-dlp arguments\n got %q\nwant %q", run.Args, want)
	}
	if !slices.Equal(runs[0].Args, slices.Delete(slices.Clone(want), slices.Index(want, "--cookies"), slices.Index(want, "--cookies")+2)) {
		t.Errorf("yt-dlp arguments without a cookie: %q", runs[0].Args)
	}
	if rel, err := filepath.Rel(filepath.Join(s.toolsDir, "run"), run.Dir); err != nil || strings.Contains(rel, "..") || rel == "." {
		t.Errorf("yt-dlp ran in %s, not in a folder of its own below %s", run.Dir, filepath.Join(s.toolsDir, "run"))
	}
	if !strings.Contains(run.Cookies, "sid\t"+secret) {
		t.Errorf("cookie file:\n%s", run.Cookies)
	}
	for _, r := range runs {
		if slices.ContainsFunc(r.Args, func(a string) bool { return strings.Contains(a, secret) }) {
			t.Errorf("the cookie's value is in yt-dlp's arguments: %q", r.Args)
		}
		checkToolEnv(t, r.Env)
	}
	if left, _ := filepath.Glob(filepath.Join(s.toolsDir, "run", "*")); len(left) > 0 {
		t.Errorf("work folders left after the runs: %v", left)
	}

	tv := h.toolsCLI()
	if !tv.Ready || len(tv.Tools) != 2 || tv.Tools[0].Name != "yt-dlp" || tv.Tools[0].Managed || tv.Tools[0].Verified != "" ||
		!samePath(tv.Tools[0].Path, filepath.Join(s.pathDir, exeName("yt-dlp"))) || tv.Tools[1].Name != "deno" ||
		tv.Tools[1].Version != fakeDenoVersion || tv.Tools[1].Managed {
		t.Errorf("tools --json = %+v", tv)
	}
	if n := release.total(); n != 0 {
		t.Errorf("the release server was asked %d times although both tools are on PATH", n)
	}
	h.stop(d)
}

// samePath compares paths as the system does (case-insensitively on Windows).
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// checkToolEnv checks that a tool got only the variables of the allow-list
// (docs/SITE-MEDIA-SPEC.md §4.7), the ones Downex sets and the e2etest build's own.
func checkToolEnv(t *testing.T, names []string) {
	t.Helper()
	allowed := []string{"SystemRoot", "SystemDrive", "windir", "PATHEXT", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
		"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "PATH", "TEMP", "TMP", "TMPDIR", "DENO_DIR", "DENO_NO_UPDATE_CHECK", "NO_COLOR",
		"PYTHONUTF8"}
	for _, n := range names {
		if strings.HasPrefix(n, "DOWNEX_E2E_") {
			continue
		}
		if !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, n) }) {
			t.Errorf("yt-dlp was given the variable %s, which is not on the allow-list", n)
		}
	}
	for _, need := range []string{"PATH", "TEMP", "NO_COLOR", "PYTHONUTF8", "DENO_NO_UPDATE_CHECK"} {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, need) }) {
			t.Errorf("yt-dlp was not given %s: %q", need, names)
		}
	}
}

// Crash and resume (docs/SITE-MEDIA-SPEC.md §8.2 step 4): downexd is killed while later
// chunks are held back; the restarted daemon resolves the page again (new addresses, same
// keys) and fetches only the chunks whose bits were stored, and the result is byte for
// byte the file of an uninterrupted download of the same page.
func TestSiteResumeAfterKill(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newSite(true, nil)
	st := newSiteTracks(t)
	page := s.talkPage("/watch/resume", st)
	d := h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.siteNetwork()

	const kept = 3 // video chunks served before the kill
	s.srv.setHold(func(r siteRequest) bool { return r.Format == "137" && chunk(r) >= kept })
	id := h.cli("media", "--site", page, "-o", h.out).ok(t).id(t)
	// The stored checkpoint holds the bits of video chunks 0-2: their fsync and the next
	// checkpoint came after they arrived. The audio's one chunk waits behind the held
	// video chunks (chunks go video first, then audio).
	deadline := time.Now().Add(progressTimeout)
	for stored := 0; stored < kept; {
		var v struct {
			Media struct {
				FragmentsDone int `json:"fragmentsDone"`
			} `json:"media"`
		}
		r := h.cli("info", idArg(id), "--json").ok(t)
		if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
			t.Fatalf("info --json: %v: %v", err, r)
		}
		stored = v.Media.FragmentsDone
		if time.Now().After(deadline) {
			t.Fatalf("only %d chunks stored within %v: %v", stored, progressTimeout, h.get(id))
		}
		time.Sleep(10 * pollInterval)
	}
	d.kill(t)
	before := len(s.srv.requests(0))
	s.srv.setHold(nil)

	d = h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.waitCompleted(id)
	path := filepath.Join(h.out, "An e2e talk.mp4")
	checkMP4(t, path, st.video, st.audio)
	for _, r := range s.srv.requests(before) {
		if r.Run != 2 || (r.Format != "137" || chunk(r) < kept) && r.Format != "140" {
			t.Errorf("after the restart: %+v (want only video chunks %d- and the audio, of the second extraction)", r, kept)
		}
	}
	if runs := s.runs(page); len(runs) != 2 {
		t.Errorf("%d extractions, want 2 (the add's, and the restarted run's)", len(runs))
	}

	// The same page downloaded without interruption gives the same bytes.
	out2 := filepath.Join(h.root, "out2")
	id2 := h.cli("media", "--site", page, "-o", out2).ok(t).id(t)
	h.waitCompleted(id2)
	a, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out2, "An e2e talk.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("the resumed download (%d bytes) differs from an uninterrupted one (%d bytes)", len(a), len(b))
	}
	s.checkRequests(t)
	h.stop(d)
}

// Pause inside a chunk (docs/SITE-MEDIA-SPEC.md §2.5): a page's video paused while a
// chunk is half received keeps that half, shows the bytes it kept and its exact size
// while paused, never shows less once resumed, and the resumed run asks only for the rest
// of that chunk; the result is the page's video, byte for byte.
func TestSitePauseMidChunk(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newSite(true, nil)
	st := newSiteTracks(t)
	page := s.talkPage("/watch/pause", st)
	d := h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.siteNetwork()

	// Video chunks 0 and 1 arrive; chunk 2 sends half its bytes and stops; the rest waits.
	const half = 512 << 10
	s.srv.setCut(func(r siteRequest) int64 {
		if r.Format == "137" && r.Start == 2<<20 {
			return half
		}
		return 0
	})
	s.srv.setHold(func(r siteRequest) bool { return r.Format == "137" && chunk(r) > 2 || r.Format == "140" })
	id := h.cli("media", "--site", page, "-o", h.out).ok(t).id(t)
	h.waitReceived(id, 2<<20+half)
	h.cli("pause", idArg(id)).ok(t)
	p := h.waitState(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED)
	total := int64(len(st.video.file()) + len(st.audio.file()))
	if p.GetReceived() != 2<<20+half || p.GetSize() != total || p.GetSizeApprox() {
		t.Fatalf("paused: received %d of %d (approx %v), want %d of exactly %d", p.GetReceived(), p.GetSize(), p.GetSizeApprox(),
			2<<20+half, total)
	}
	before := len(s.srv.requests(0))
	s.srv.setCut(nil)
	s.srv.setHold(nil)

	h.cli("resume", idArg(id)).ok(t)
	deadline := time.Now().Add(completeTimeout)
	for {
		g := h.get(id)
		if g.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			break
		}
		if g.GetReceived() < p.GetReceived() {
			t.Errorf("resumed: %d bytes received, less than the %d it was paused with (%v)", g.GetReceived(), p.GetReceived(), g.GetState())
		}
		if !running(g.GetState()) && g.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED || time.Now().After(deadline) {
			t.Fatalf("download %d stopped or did not complete: %v", id, g)
		}
		time.Sleep(pollInterval)
	}
	checkMP4(t, filepath.Join(h.out, "An e2e talk.mp4"), st.video, st.audio)
	var rest []siteRequest
	for _, r := range s.srv.requests(before) {
		if r.Format == "137" && chunk(r) <= 2 {
			rest = append(rest, r)
		}
	}
	if len(rest) != 1 || rest[0].Start != 2<<20+half || rest[0].End != 3<<20-1 {
		t.Errorf("resumed requests for video chunks 0-2: %+v, want only the rest of chunk 2", rest)
	}
	s.checkRequests(t)
	h.stop(d)
}

// Expiry (docs/SITE-MEDIA-SPEC.md §8.2 step 5): the first extraction's addresses answer
// 403 after two video chunks; the run resolves the page again past the cache (exactly
// one more extraction, whose addresses differ) and completes with them.
func TestSiteExpiredAddresses(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newSite(true, nil)
	st := newSiteTracks(t)
	page := s.talkPage("/watch/expiry", st)
	d := h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.siteNetwork()
	s.srv.setForbid(func(r siteRequest) bool { return r.Run == 1 && s.srv.served("137", 1) >= 2 })

	id := h.cli("media", "--site", page, "-o", h.out).ok(t).id(t)
	h.waitCompleted(id)
	checkMP4(t, filepath.Join(h.out, "An e2e talk.mp4"), st.video, st.audio)
	if runs := s.runs(page); len(runs) != 2 {
		t.Fatalf("%d extractions, want 2 (the add's and one renewal): %+v", len(runs), runs)
	}
	forbidden, renewed := 0, 0
	for _, r := range s.srv.requests(0) {
		switch {
		case r.Status == http.StatusForbidden:
			forbidden++
		case r.Run == 2:
			renewed++
		}
	}
	if forbidden == 0 || renewed == 0 {
		t.Errorf("%d requests refused and %d made with the renewed addresses; want both", forbidden, renewed)
	}
	s.checkRequests(t)
	h.stop(d)
}

// Refusals (docs/SITE-MEDIA-SPEC.md §8.2 steps 6 and 8): DRM, live, a playlist and a
// sign-in wall are refused with their codes; a protected init in the served video fails
// the run as encrypted after the first chunks only; with site media turned off every
// page is site_media_off, and the setting survives the settings round trip.
func TestSiteRefusals(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newSite(true, nil)
	pages := map[string]string{
		"encrypted": s.page("/watch/drm", fakePage{
			Stdout: `{"id":"drm1","title":"Protected","extractor_key":"Youtube","_has_drm":true,"formats":[]}` + "\n"}),
		"live": s.page("/watch/live", fakePage{
			Stdout: `{"id":"live1","title":"Live","extractor_key":"Youtube","is_live":true,"live_status":"is_live","formats":[]}` + "\n"}),
		"media_unsupported": s.page("/watch/list", fakePage{
			Stdout: `{"_type":"playlist","id":"pl1","title":"A list","extractor_key":"YoutubeTab","entries":[]}` + "\n"}),
		"auth_required": s.page("/watch/age", fakePage{Exit: 1,
			Stderr: "WARNING: [youtube] age1: a warning\nERROR: [youtube] age1: Sign in to confirm your age. This video may be inappropriate for some users.\n"}),
		"site_unsupported": s.srv.srv.URL + "/not-configured",
	}
	protected := newSizedTrack(t, true, 320, 240, 4, 704, true, 16<<10, 8<<10)
	audio := newSiteTracks(t).audio
	psshPage := s.videoPage("/watch/pssh", "Protected init", s.format("p137", protected, 4000), s.format("p140", audio, 128))
	d := h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	h.siteNetwork()

	for code, page := range pages {
		_, err := h.probeSite(page, 30*time.Second)
		if got := refusalCode(err); got != code || !strings.Contains(err.Error(), code+": ") {
			t.Errorf("probe of %s: %v (code %q), want %s", page, err, got, code)
		}
	}
	r := h.cli("media", "--site", pages["auth_required"], "--list")
	if r.code == 0 || !strings.Contains(r.stderr, "sign in") {
		t.Errorf("media --list of a sign-in page: %v", r)
	}

	// The protected init is found in the first chunk of the video; nothing past the first
	// chunk of any track is asked for.
	id := h.cli("media", "--site", psshPage, "-o", h.out).ok(t).id(t)
	if dl := h.waitFailed(id); dl.GetErrorCode() != "encrypted" {
		t.Errorf("download of a protected video: %v", dl)
	}
	for _, r := range s.srv.requests(0) {
		if chunk(r) != 0 {
			t.Errorf("request %+v past the first chunk of a protected video", r)
		}
	}

	h.settingsCLI("--site-media", "off")
	if _, err := h.probeSite(psshPage, 30*time.Second); refusalCode(err) != "site_media_off" {
		t.Errorf("probe with site media off: %v", err)
	}
	r = h.cli("media", "--site", psshPage, "--list")
	if r.code == 0 || !strings.Contains(r.stderr, "--site-media on") {
		t.Errorf("media --list with site media off: %v", r)
	}
	var v struct {
		SiteMedia    bool   `json:"siteMedia"`
		ToolsChannel string `json:"toolsChannel"`
	}
	r = h.cli("settings", "--json").ok(t)
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.SiteMedia {
		t.Errorf("settings --json after --site-media off: %v %s", err, r.stdout)
	}
	h.stop(d)
	d = h.startDaemonFrom(bin.downexdHooks, s.env)
	h.waitReady(d)
	if set := h.getSettings(); set.GetSiteMedia() || !set.GetToolsAutoUpdate() || len(set.GetHostRules()) != 1 ||
		set.GetHostRules()[0].GetMaxRequestBytes() != 1<<20 {
		t.Errorf("settings after a restart: site_media %v, tools_auto_update %v, host rules %v", set.GetSiteMedia(),
			set.GetToolsAutoUpdate(), set.GetHostRules())
	}
	h.stop(d)
}

// The browser hand-off (docs/SITE-MEDIA-SPEC.md §8.2 step 7): the relay names the video
// feature; probeVideo answers the offer without any format address; addVideo adds the
// pick awaiting confirmation (downexd starts the window with --confirm); "downex confirm
// --video" switches to another MP4 quality, which is downloaded as the .mp4.
func TestSiteBrowserHandOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newSite(true, nil)
	st := newSiteTracks(t)
	page := s.talkPage("/watch/browser", st)
	// downexd next to the stand-in window, so that hand-offs await confirmation.
	dir := filepath.Join(h.root, "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	installFile(t, bin.downexdHooks, filepath.Join(dir, exeName("downexd")))
	installFile(t, self, filepath.Join(dir, exeName("downex-ui")))
	ui := desktopInstall{downexd: filepath.Join(dir, exeName("downexd")), record: filepath.Join(h.root, "ui-calls.jsonl")}
	env := withOverrides(s.env, map[string]string{envFakeUI: ui.record})
	d := h.startDaemonFrom(ui.downexd, env)
	h.waitReady(d)
	h.setDefaultDir(h.out)
	h.siteNetwork()

	p := h.startNMHost(h.env)
	p.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	w := p.recv()
	features, _ := w["features"].([]any)
	if w["type"] != "welcome" || !slices.Contains(features, any("video")) {
		t.Fatalf("hello answered %v", w)
	}
	p.send(map[string]any{"id": "2", "type": "probeVideo", "pageUrl": page})
	info := p.recv()
	raw, _ := json.Marshal(info)
	tracks, _ := info["tracks"].([]any)
	if info["type"] != "videoInfo" || info["site"] != "YouTube" || info["selectedVideo"] != "137" || len(tracks) != 3 ||
		bytes.Contains(raw, []byte("/media/")) {
		t.Fatalf("probeVideo answered %s", raw)
	}
	p.send(map[string]any{"id": "3", "type": "addVideo", "pageUrl": page, "videoTrack": "137", "audioTrack": "140",
		"title": "From the browser"})
	a := p.recv()
	fid, _ := a["downloadId"].(float64)
	id := int64(fid)
	if a["type"] != "added" || a["awaiting"] != true || id <= 0 {
		t.Fatalf("addVideo answered %v", a)
	}
	p.closeAndWait()
	call := ui.waitUICall(t)
	if !slices.Equal(call.Args, []string{"--confirm", idArg(id)}) {
		t.Errorf("confirmation window started with %q", call.Args)
	}
	dl := h.get(id)
	if dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION || dl.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER ||
		!strings.HasSuffix(dl.GetFilename(), ".mp4") || dl.GetExpectedSize() != int64(len(st.video.file())+len(st.audio.file())) {
		t.Fatalf("handed-off video = %v", dl)
	}

	h.cli("confirm", idArg(id), "--video", "136").ok(t)
	dl = h.waitCompleted(id)
	if dl.GetMedia().GetSelectedVideo() != "136" || !strings.HasSuffix(dl.GetFilename(), ".mp4") {
		t.Fatalf("confirmed with another quality: %v", dl)
	}
	checkMP4(t, filepath.Join(dl.GetDir(), dl.GetFilename()), st.small, st.audio)
	for _, r := range s.srv.requests(0) {
		if r.Format == "137" {
			t.Errorf("the quality not chosen was downloaded: %+v", r)
		}
	}
	s.checkRequests(t)
	h.stop(d)
}

// ---- first-use install ------------------------------------------------------------------

// releaseServer stands in for GitHub releases over TLS: releases/latest redirects to the
// tag of setLatest, files come from releases/download/<tag>/<asset>, and gate holds a
// file's second half back.
type releaseServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	tags  map[string]string // repo → latest tag
	files map[string][]byte // URL path → content
	hits  map[string]int
	gates map[string]chan struct{}
}

func newReleaseServer(t *testing.T) *releaseServer {
	t.Helper()
	rs := &releaseServer{tags: map[string]string{}, files: map[string][]byte{}, hits: map[string]int{},
		gates: map[string]chan struct{}{}}
	rs.srv = httptest.NewTLSServer(http.HandlerFunc(rs.serve))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *releaseServer) serve(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	rs.hits[r.URL.Path]++
	if repo, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/releases/latest"); ok {
		tag, found := rs.tags[repo]
		rs.mu.Unlock()
		if !found {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "https://"+r.Host+"/"+repo+"/releases/tag/"+tag, http.StatusFound)
		return
	}
	data, found := rs.files[r.URL.Path]
	gate := rs.gates[r.URL.Path]
	rs.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if gate != nil {
		_, _ = w.Write(data[:len(data)/2])
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		data = data[len(data)/2:]
	}
	_, _ = w.Write(data)
}

func (rs *releaseServer) put(repo, tag, asset string, data []byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.files["/"+repo+"/releases/download/"+tag+"/"+asset] = data
}

func (rs *releaseServer) setLatest(repo, tag string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.tags[repo] = tag
}

// gate holds back the second half of repo/tag/asset until open is called.
func (rs *releaseServer) gate(t *testing.T, repo, tag, asset string) (open func()) {
	ch := make(chan struct{})
	rs.mu.Lock()
	rs.gates["/"+repo+"/releases/download/"+tag+"/"+asset] = ch
	rs.mu.Unlock()
	open = sync.OnceFunc(func() { close(ch) })
	t.Cleanup(open)
	return open
}

func (rs *releaseServer) total() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, h := range rs.hits {
		n += h
	}
	return n
}

// requestedRepo reports whether any URL below repo was asked for.
func (rs *releaseServer) requestedRepo(repo string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for p := range rs.hits {
		if strings.HasPrefix(p, "/"+repo+"/") {
			return true
		}
	}
	return false
}

// rootCA writes the server's certificate as a PEM file for DOWNEX_E2E_ROOT_CA.
func (rs *releaseServer) rootCA(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "release-ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rs.srv.Certificate().Raw})
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// pgpSigner is an OpenPGP v4 RSA key that signs as yt-dlp's release key does: binary
// detached signatures of class 0x00 with SHA-512 and an issuer fingerprint (RFC 9580).
type pgpSigner struct {
	key  *rsa.PrivateKey
	body []byte // the public key packet's body
	fp   [20]byte
}

func newPGPSigner(t *testing.T) *pgpSigner {
	t.Helper()
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &pgpSigner{key: key}
	var b bytes.Buffer
	b.WriteByte(4)
	_ = binary.Write(&b, binary.BigEndian, uint32(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()))
	b.WriteByte(1) // RSA
	b.Write(pgpMPI(key.N))
	b.Write(pgpMPI(big.NewInt(int64(key.E))))
	s.body = b.Bytes()
	h := sha1.New()
	h.Write([]byte{0x99, byte(len(s.body) >> 8), byte(len(s.body))})
	h.Write(s.body)
	copy(s.fp[:], h.Sum(nil))
	return s
}

func pgpMPI(n *big.Int) []byte {
	return append([]byte{byte(n.BitLen() >> 8), byte(n.BitLen())}, n.Bytes()...)
}

// pgpPacket encodes a new-format packet.
func pgpPacket(tag byte, body []byte) []byte {
	out := []byte{0xc0 | tag}
	switch n := len(body); {
	case n < 192:
		out = append(out, byte(n))
	case n < 8384:
		n -= 192
		out = append(out, byte(n>>8)+192, byte(n))
	default:
		out = append(out, 255, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, body...)
}

// armoredPublic is the public key (and a user ID) as an ASCII-armored key block.
func (s *pgpSigner) armoredPublic() string {
	data := append(pgpPacket(6, s.body), pgpPacket(13, []byte("E2E release key <e2e@example.invalid>"))...)
	var b strings.Builder
	b.WriteString("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n")
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 64 {
		b.WriteString(enc[:64] + "\n")
		enc = enc[64:]
	}
	b.WriteString(enc + "\n")
	crc := uint32(0xB704CE) // CRC-24 (RFC 9580 §6.1)
	for _, c := range data {
		crc ^= uint32(c) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	crc &= 0xFFFFFF
	b.WriteString("=" + base64.StdEncoding.EncodeToString([]byte{byte(crc >> 16), byte(crc >> 8), byte(crc)}) + "\n")
	b.WriteString("-----END PGP PUBLIC KEY BLOCK-----\n")
	return b.String()
}

// sign returns a detached signature of data made now.
func (s *pgpSigner) sign(t *testing.T, data []byte) []byte {
	t.Helper()
	var hashed bytes.Buffer
	hashed.Write([]byte{5, 0x80 | 2}) // creation time, critical
	_ = binary.Write(&hashed, binary.BigEndian, uint32(time.Now().Unix()))
	hashed.Write([]byte{22, 33, 4}) // issuer fingerprint
	hashed.Write(s.fp[:])
	var body bytes.Buffer
	body.Write([]byte{4, 0x00, 1, 10}) // v4, binary document, RSA, SHA-512
	_ = binary.Write(&body, binary.BigEndian, uint16(hashed.Len()))
	body.Write(hashed.Bytes())
	hashedPart := bytes.Clone(body.Bytes())
	unhashed := append([]byte{9, 16}, s.fp[12:]...) // issuer key ID
	_ = binary.Write(&body, binary.BigEndian, uint16(len(unhashed)))
	body.Write(unhashed)
	h := sha512.New()
	h.Write(data)
	h.Write(hashedPart)
	h.Write([]byte{4, 0xff})
	_ = binary.Write(h, binary.BigEndian, uint32(len(hashedPart)))
	digest := h.Sum(nil)
	sig, err := rsa.SignPKCS1v15(cryptorand.Reader, s.key, crypto.SHA512, digest)
	if err != nil {
		t.Fatal(err)
	}
	body.Write(digest[:2])
	body.Write(pgpMPI(new(big.Int).SetBytes(sig)))
	return pgpPacket(2, body.Bytes())
}

// ytdlpRelease publishes this test binary as today's nightly yt-dlp on rs, with
// SHA2-256SUMS signed by s (or, with tampered, a signature of other contents), and
// returns the repository, tag and asset name.
func ytdlpRelease(t *testing.T, rs *releaseServer, s *pgpSigner, tampered bool) (repo, tag, asset string) {
	t.Helper()
	musl, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	asset, _ = tools.AssetNames(runtime.GOOS, runtime.GOARCH, len(musl) > 0)
	if asset == "" {
		t.Skipf("yt-dlp publishes no build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	repo, tag = "yt-dlp/yt-dlp-nightly-builds", time.Now().UTC().Format("2006.01.02")
	sum := sha256.Sum256(exe)
	sums := []byte(fmt.Sprintf("%x  yt-dlp.tar.gz\n%s  %s\n", sha256.Sum256([]byte("other")), hex.EncodeToString(sum[:]), asset))
	signed := sums
	if tampered {
		signed = bytes.Replace(sums, []byte("yt-dlp.tar.gz"), []byte("yt-dlp.tar.xz"), 1)
	}
	rs.put(repo, tag, "SHA2-256SUMS", sums)
	rs.put(repo, tag, "SHA2-256SUMS.sig", s.sign(t, signed))
	rs.put(repo, tag, asset, exe)
	rs.setLatest(repo, tag)
	return repo, tag, asset
}

// First use (docs/SITE-MEDIA-SPEC.md §8.2 step 1): with only Deno on PATH, the first
// probe of a page starts the install of yt-dlp from the release server and answers
// tools_installing with its progress while the download is held back; once it is let
// through, "media --site --list" lists the page, and "downex tools" shows the managed
// copy verified with OpenPGP and SHA-256. Deno, found on PATH, is never downloaded. A
// checksum list whose signature does not match is refused: tools_unavailable, with the
// tool's error code signature.
func TestSiteFirstUseInstall(t *testing.T) {
	t.Parallel()
	signer := newPGPSigner(t)
	start := func(t *testing.T, tampered bool) (*harness, *siteSetup, *releaseServer, func()) {
		h := newHarness(t)
		rs := newReleaseServer(t)
		repo, tag, asset := ytdlpRelease(t, rs, signer, tampered)
		open := rs.gate(t, repo, tag, asset)
		s := h.newSite(false, map[string]string{
			"DOWNEX_E2E_TOOLS_BASE": rs.srv.URL, "DOWNEX_E2E_ROOT_CA": rs.rootCA(t, h.root),
			"DOWNEX_E2E_YTDLP_KEY": signer.armoredPublic(),
		})
		d := h.startDaemonFrom(bin.downexdHooks, s.env)
		h.waitReady(d)
		h.siteNetwork()
		t.Cleanup(func() {
			if !d.exited() {
				h.stop(d)
			}
		})
		return h, s, rs, open
	}

	t.Run("install", func(t *testing.T) {
		t.Parallel()
		h, s, rs, open := start(t, false)
		st := newSiteTracks(t)
		page := s.talkPage("/watch/first", st)
		_, err := h.probeSite(page, 12*time.Second)
		if refusalCode(err) != "tools_installing" || !regexp.MustCompile(`\(\d{1,3}%\)`).MatchString(err.Error()) {
			t.Fatalf("first probe: %v, want tools_installing with its progress", err)
		}
		open()
		r := h.cli("media", "--site", page, "--list", "--json").ok(t)
		var v siteMediaView
		if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.SelectedVideo != "137" || len(v.Tracks) != 3 {
			t.Fatalf("media --list --json after the install: %v: %v", err, r)
		}
		tv := h.toolsCLI()
		tag := time.Now().UTC().Format("2006.01.02")
		if !tv.Ready || len(tv.Tools) != 2 || tv.Tools[0].Name != "yt-dlp" || !tv.Tools[0].Managed ||
			tv.Tools[0].Verified != "openpgp+sha256" || tv.Tools[0].Version != tag ||
			!strings.HasPrefix(tv.Tools[0].Path, filepath.Join(s.toolsDir, "yt-dlp", tag)) || tv.Tools[1].Managed {
			t.Errorf("tools --json = %+v", tv)
		}
		if runs := s.runs(page); len(runs) != 1 || !strings.HasPrefix(runs[0].Dir, s.toolsDir) {
			t.Errorf("extractions with the installed copy: %+v", runs)
		}
		if rs.requestedRepo("denoland/deno") {
			t.Error("Deno was downloaded although a usable copy is on PATH")
		}
	})

	t.Run("bad signature", func(t *testing.T) {
		t.Parallel()
		h, s, _, open := start(t, true)
		open()
		page := s.page("/watch/unsigned", fakePage{Stdout: "{}"})
		r := h.cli("media", "--site", page, "--list")
		if r.code == 0 || !strings.Contains(r.stderr, "could not set up yt-dlp") {
			t.Errorf("media --list with a bad signature: %v", r)
		}
		if _, err := h.probeSite(page, 30*time.Second); refusalCode(err) != "tools_unavailable" {
			t.Errorf("probe with a bad signature: %v", err)
		}
		tv := h.toolsCLI()
		if len(tv.Tools) == 0 || tv.Tools[0].ErrorCode != "signature" || tv.Tools[0].State != "failed" || tv.Ready {
			t.Errorf("tools --json = %+v", tv)
		}
		if runs := s.runs(page); len(runs) != 0 {
			t.Errorf("the unverified yt-dlp ran: %+v", runs)
		}
	})
}
