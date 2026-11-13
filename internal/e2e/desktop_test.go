package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// Desktop integration (docs/DESKTOP-SPEC.md §1, §2, §5 and the bulk actions of §3), with
// real executables: browser hand-offs through downex-nmhost, the confirmation window
// that downexd starts (a stand-in that records its arguments), "downex confirm",
// categories and their folders, PauseAll / ResumeAll and the settings round trip.

// envFakeUI names the file that the stand-in desktop window appends its invocations to;
// TestMain checks it before anything else.
const envFakeUI = "DOWNEX_E2E_FAKE_UI"

// uiCall is one recorded start of the stand-in window.
type uiCall struct {
	Args     []string `json:"args"`
	Endpoint string   `json:"endpoint"` // the daemon endpoint the window would use
}

// fakeUI is the stand-in window: it appends its arguments to record and exits.
func fakeUI(record string, args []string) int {
	b, err := json.Marshal(uiCall{Args: args, Endpoint: os.Getenv(ipc.EnvEndpoint)})
	if err != nil {
		return 1
	}
	f, err := os.OpenFile(record, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 1
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 1
	}
	return 0
}

// desktopInstall is an installation directory holding downexd next to the stand-in
// downex-ui, so that the daemon asks "the window" to confirm browser hand-offs.
type desktopInstall struct {
	downexd string   // the daemon executable in the directory
	record  string   // the stand-in window's record
	env     []string // the harness environment plus envFakeUI
}

func (h *harness) newDesktopInstall() desktopInstall {
	h.t.Helper()
	dir := filepath.Join(h.root, "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		h.t.Fatal(err)
	}
	inst := desktopInstall{
		downexd: filepath.Join(dir, exeName("downexd")),
		record:  filepath.Join(h.root, "ui-calls.jsonl"),
	}
	installFile(h.t, bin.downexd, inst.downexd)
	installFile(h.t, self, filepath.Join(dir, exeName("downex-ui")))
	inst.env = withOverrides(h.env, map[string]string{envFakeUI: inst.record})
	return inst
}

// installFile links src at dst, or copies it where links are not possible.
func installFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.Link(src, dst); err == nil {
		return
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// uiCalls returns the recorded starts of the stand-in window.
func (d desktopInstall) uiCalls(t *testing.T) []uiCall {
	t.Helper()
	f, err := os.Open(d.record)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []uiCall
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c uiCall
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("stand-in window record %q: %v", sc.Text(), err)
		}
		calls = append(calls, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return calls
}

// waitUICall waits for the first recorded start of the stand-in window.
func (d desktopInstall) waitUICall(t *testing.T) uiCall {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		if calls := d.uiCalls(t); len(calls) > 0 {
			return calls[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("downexd did not start the confirmation window within %v", readyTimeout)
		}
		time.Sleep(pollInterval)
	}
}

// handOff sends one browser hand-off (an "add" message) through a new downex-nmhost and
// returns the download id the relay answered with.
func (h *harness) handOff(add map[string]any) int64 {
	h.t.Helper()
	p := h.startNMHost(h.env)
	p.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	if w := p.recv(); w["ok"] != true || w["type"] != "welcome" {
		h.t.Fatalf("hello answered %v", w)
	}
	msg := map[string]any{"id": "2", "type": "add", "method": "GET"}
	for k, v := range add {
		msg[k] = v
	}
	p.send(msg)
	a := p.recv()
	id, ok := a["downloadId"].(float64)
	if a["ok"] != true || a["type"] != "added" || !ok || id <= 0 {
		h.t.Fatalf("add answered %v\nrelay stderr: %s", a, p.stderr.String())
	}
	p.closeAndWait()
	return int64(id)
}

func (h *harness) getSettings() *downexv1.Settings {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	res, err := h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		h.t.Fatalf("get settings: %v", err)
	}
	return res.Msg.GetSettings()
}

func (h *harness) updateSettings(s *downexv1.Settings) (*downexv1.Settings, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	res, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetSettings(), nil
}

func noSuchPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (or cannot be checked: %v); nothing should have been created", path, err)
	}
}

// A browser hand-off waits for confirmation: the daemon starts the window with
// "--confirm <id>", fetches and creates nothing, keeps the download across a restart,
// and "downex confirm --name" then saves it under the new name in the folder of the
// category chosen at the hand-off.
func TestBrowserHandOffAwaitsConfirmation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 2<<20 + 11, 401
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"c1"`, ContentType: "application/zip"})
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	h.setDefaultDir(h.out)
	if s := h.getSettings(); !s.GetConfirmBrowserDownloads() || !s.GetUseCategoryFolders() {
		t.Fatalf("defaults: confirm_browser_downloads %v, use_category_folders %v", s.GetConfirmBrowserDownloads(),
			s.GetUseCategoryFolders())
	}

	id := h.handOff(map[string]any{"url": srv.URL + "/files/bundle.zip", "mime": "application/zip", "size": size,
		"referrer": "https://example.test/page"})
	archives := filepath.Join(h.out, "Archives")
	dl := h.get(id)
	if dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION ||
		dl.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || dl.GetCategory() != "Archives" ||
		dl.GetExpectedSize() != size || dl.GetMime() != "application/zip" ||
		dl.GetDir() != archives || dl.GetFilename() != "bundle.zip" {
		t.Fatalf("handed-off download = %v", dl)
	}
	call := inst.waitUICall(t)
	if !slices.Equal(call.Args, []string{"--confirm", idArg(id)}) || call.Endpoint != h.endpoint {
		t.Fatalf("confirmation window started with %q on endpoint %q, want [--confirm %d] on %q",
			call.Args, call.Endpoint, id, h.endpoint)
	}
	if e := entry(t, h.ls(), id); e.State != "awaiting" {
		t.Errorf("ls shows state %q, want awaiting", e.State)
	}
	// Pause and Resume do not apply to it; the scheduler never starts it.
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	if _, err := h.dl.Resume(ctx, connect.NewRequest(&downexv1.ResumeRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Resume of an awaiting download: %v, want FailedPrecondition", err)
	}

	// It survives a restart, still unstarted.
	h.stop(d)
	d = h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	if got := h.get(id); got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		t.Fatalf("after a restart: %v", got)
	}
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Fatalf("the server was asked before the confirmation: %+v", reqs)
	}
	noSuchPath(t, archives)

	r := h.cli("confirm", idArg(id), "--name", "x.bin").ok(t)
	want := filepath.Join(archives, "x.bin")
	if !strings.Contains(r.stdout, fmt.Sprintf("Confirmed download %d", id)) || !strings.Contains(r.stdout, want) {
		t.Errorf("confirm: %v", r)
	}
	dl = h.waitCompleted(id)
	if dl.GetDir() != archives || dl.GetFilename() != "x.bin" || dl.GetCategory() != "Archives" {
		t.Fatalf("completed as %q in %q (category %q)", dl.GetFilename(), dl.GetDir(), dl.GetCategory())
	}
	checkFile(t, want, seed, size)
	noPartFiles(t, archives)

	// A second confirmation is refused.
	r = h.cli("confirm", idArg(id))
	if r.code == 0 {
		t.Errorf("confirm of a completed download succeeded: %v", r)
	}
	h.stop(d)
}

// With confirm_browser_downloads off ("downex settings --confirm-browser off") a
// hand-off is queued at once: no confirmation is asked for, and the MIME hint chooses
// the category folder when neither the name nor the URL tells. With show_progress_window
// on, downexd opens the download's progress window ("downex-ui --progress <id>",
// docs/DESKTOP-SPEC.md §7) for such a hand-off, never for a CLI add or with the setting off.
func TestBrowserHandOffWithoutConfirmation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 1<<20 + 7, 402
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"c2"`, Filename: "track.mp3", ContentType: "audio/mpeg"})
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	if s := h.getSettings(); !s.GetShowProgressWindow() || !s.GetShowCompleteDialog() {
		t.Fatalf("defaults: show_progress_window %v, show_complete_dialog %v", s.GetShowProgressWindow(), s.GetShowCompleteDialog())
	}
	r := h.cli("settings", "--confirm-browser", "off", "--progress-window", "off", "--dir", h.out, "--json").ok(t)
	var v struct {
		DefaultDir              string `json:"defaultDir"`
		ConfirmBrowserDownloads bool   `json:"confirmBrowserDownloads"`
		ShowProgressWindow      bool   `json:"showProgressWindow"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.ConfirmBrowserDownloads || v.ShowProgressWindow || v.DefaultDir != h.out {
		t.Fatalf("settings --confirm-browser off --progress-window off: %v: %v", err, r)
	}

	id := h.handOff(map[string]any{"url": srv.URL + "/get?id=7", "mime": "audio/mpeg; charset=binary", "size": size})
	dl := h.waitCompleted(id)
	audio := filepath.Join(h.out, "Audio")
	if dl.GetDir() != audio || dl.GetFilename() != "track.mp3" || dl.GetCategory() != "Audio" ||
		dl.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || dl.GetMime() != "audio/mpeg" || dl.GetExpectedSize() != size {
		t.Fatalf("completed download = %v", dl)
	}
	checkFile(t, filepath.Join(audio, "track.mp3"), seed, size)

	// A CLI add is never held and never opens a window, whatever the settings.
	r = h.cli("settings", "--confirm-browser", "on", "--progress-window", "on").ok(t)
	if !strings.Contains(r.stdout, "Confirm browser downloads:") || !strings.Contains(r.stdout, "Progress window:") {
		t.Errorf("settings: %v", r)
	}
	srv2 := newServer(t, testserver.Options{Size: size, Seed: seed + 1, ETag: `"c3"`})
	cid := h.add(srv2.URL + "/docs/notes.pdf")
	dl = h.waitCompleted(cid)
	if dl.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI || dl.GetDir() != filepath.Join(h.out, "Documents") {
		t.Fatalf("CLI add = %v", dl)
	}
	checkFile(t, filepath.Join(h.out, "Documents", "notes.pdf"), seed+1, size)

	// A hand-off that starts at once while the setting is on opens its progress window.
	// It is the first start of the window: none of the adds above opened one.
	h.cli("settings", "--confirm-browser", "off").ok(t)
	srv3 := newServer(t, testserver.Options{Size: size, Seed: seed + 2, ETag: `"c4"`})
	wid := h.handOff(map[string]any{"url": srv3.URL + "/files/shown.zip", "size": size})
	dl = h.waitCompleted(wid)
	checkFile(t, filepath.Join(h.out, "Archives", "shown.zip"), seed+2, size)
	call := inst.waitUICall(t)
	if !slices.Equal(call.Args, []string{"--progress", idArg(wid)}) || call.Endpoint != h.endpoint {
		t.Fatalf("window started with %q on endpoint %q, want [--progress %d] on %q (earlier downloads: %d, %d)",
			call.Args, call.Endpoint, wid, h.endpoint, id, cid)
	}
	if calls := inst.uiCalls(t); len(calls) != 1 {
		t.Errorf("the window was started %d times: %+v", len(calls), calls)
	}
	h.stop(d)
}

// A daemon that cannot ask the user to confirm (no confirmation window next to it and
// no notification-area icon: Linux for now, --no-tray, a hidden icon) queues browser
// hand-offs as before M4.5, rather than holding them where nobody would ever see them
// (GO-3); its log says why.
func TestBrowserHandOffWithNobodyToAsk(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 1<<20 + 3, 404
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"n1"`})
	d := h.startDaemon() // no downex-ui next to it; a private daemon shows no icon
	h.waitReady(d)
	h.setDefaultDir(h.out)
	if s := h.getSettings(); !s.GetConfirmBrowserDownloads() {
		t.Fatal("confirm_browser_downloads is off by default")
	}

	id := h.handOff(map[string]any{"url": srv.URL + "/files/report.pdf", "size": size})
	if s := h.get(id).GetState(); s == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		t.Fatalf("the hand-off awaits a confirmation that nothing can ask for")
	}
	dl := h.waitCompleted(id)
	docs := filepath.Join(h.out, "Documents")
	if dl.GetDir() != docs || dl.GetCategory() != "Documents" || dl.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER {
		t.Fatalf("completed download = %v", dl)
	}
	checkFile(t, filepath.Join(docs, "report.pdf"), seed, size)
	if log := h.daemonLog(); !strings.Contains(log, "nothing can ask the user") {
		t.Errorf("the daemon log does not say why the hand-off was not held:\n%s", log)
	}
	h.stop(d)
}

// PauseAll pauses running and queued downloads, ResumeAll queues the paused ones again;
// a download awaiting confirmation is left alone by both.
func TestPauseAllResumeAll(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const n, size, seed = 3, 4 << 20, 403
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"p"`, Throttle: 256 << 10})
	// The stand-in window next to downexd lets the daemon ask, so a hand-off is held.
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	h.cli("settings", "--max-concurrent", "2", "--dir", h.out).ok(t)

	ids := make([]int64, n)
	for i := range ids {
		ids[i] = h.add(fmt.Sprintf("%s/bulk-%d.bin", srv.URL, i), "-o", h.out, "-c", "1")
	}
	h.waitReceived(ids[0], 64<<10)
	h.waitReceived(ids[1], 64<<10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*rpcCallTimeout)
	defer cancel()
	aw, err := h.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{Url: srv.URL + "/held.zip",
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER}))
	if err != nil {
		t.Fatal(err)
	}
	held := aw.Msg.GetDownload().GetId()

	pa, err := h.dl.PauseAll(ctx, connect.NewRequest(&downexv1.PauseAllRequest{}))
	if err != nil {
		t.Fatalf("PauseAll: %v", err)
	}
	if pa.Msg.GetPaused() != n {
		t.Errorf("PauseAll paused %d, want %d", pa.Msg.GetPaused(), n)
	}
	for _, id := range ids {
		if s := h.get(id).GetState(); s != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
			t.Errorf("download %d is %v after PauseAll", id, s)
		}
	}
	if s := h.get(held).GetState(); s != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		t.Errorf("the awaiting download is %v after PauseAll", s)
	}
	if e := entry(t, h.ls(), ids[2]); e.State != "paused" {
		t.Errorf("ls: the queued download is %q after PauseAll", e.State)
	}

	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	ra, err := h.dl.ResumeAll(ctx, connect.NewRequest(&downexv1.ResumeAllRequest{}))
	if err != nil {
		t.Fatalf("ResumeAll: %v", err)
	}
	if ra.Msg.GetResumed() != n {
		t.Errorf("ResumeAll resumed %d, want %d", ra.Msg.GetResumed(), n)
	}
	for i, id := range ids {
		h.waitCompleted(id)
		checkFile(t, filepath.Join(h.out, fmt.Sprintf("bulk-%d.bin", i)), seed, size)
	}
	if s := h.get(held).GetState(); s != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		t.Errorf("the awaiting download is %v after ResumeAll", s)
	}
	noPartFiles(t, h.out)
	h.stop(d)
}

// settingsJSON is the part of "downex settings --json" the tests read.
type settingsJSON struct {
	MaxConcurrentDownloads  int    `json:"maxConcurrentDownloads"`
	DefaultDir              string `json:"defaultDir"`
	UseCategoryFolders      bool   `json:"useCategoryFolders"`
	ConfirmBrowserDownloads bool   `json:"confirmBrowserDownloads"`
	NotifyOnComplete        bool   `json:"notifyOnComplete"`
	NotifyOnFailure         bool   `json:"notifyOnFailure"`
	LaunchAtLogin           bool   `json:"launchAtLogin"`
	ShowTrayIcon            bool   `json:"showTrayIcon"`
	Categories              []struct {
		Name       string   `json:"name"`
		Extensions []string `json:"extensions"`
		Dir        string   `json:"dir"`
		Folder     string   `json:"folder"`
	} `json:"categories"`
}

func (h *harness) settingsCLI(args ...string) settingsJSON {
	h.t.Helper()
	r := h.cli(append(append([]string{"settings"}, args...), "--json")...).ok(h.t)
	var v settingsJSON
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		h.t.Fatalf("settings --json: %v: %v", err, r)
	}
	return v
}

// Settings are replaced as a whole: the CLI changes the fields it is given and writes
// back every other one, custom categories included; they survive a restart, an invalid
// category list is refused, and a custom category's folder receives its downloads. A
// private daemon stores launch_at_login without touching the real sign-in entry.
func TestSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 512<<10 + 1, 404
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"s"`})
	d := h.startDaemon()
	h.waitReady(d)

	v := h.settingsCLI("--dir", h.out)
	names := []string{}
	for _, c := range v.Categories {
		names = append(names, c.Name)
		if c.Dir != "" || c.Folder != filepath.Join(h.out, c.Name) {
			t.Errorf("default category %+v: folder should be below %s", c, h.out)
		}
	}
	if !slices.Equal(names, []string{"Video", "Audio", "Archives", "Documents", "Programs"}) ||
		!v.UseCategoryFolders || !v.ConfirmBrowserDownloads || !v.NotifyOnComplete || !v.NotifyOnFailure ||
		v.LaunchAtLogin || !v.ShowTrayIcon {
		t.Fatalf("default settings = %+v", v)
	}

	// A custom category with its own folder, written as the window writes settings.
	pics := filepath.Join(h.root, "pictures")
	s := h.getSettings()
	s.Categories = append(s.Categories, &downexv1.Category{Name: "Images", Extensions: []string{".PNG", "jpg", "png"}, Dir: pics})
	got, err := h.updateSettings(s)
	if err != nil {
		t.Fatalf("add a category: %v", err)
	}
	if c := got.GetCategories()[len(got.GetCategories())-1]; c.GetName() != "Images" ||
		!slices.Equal(c.GetExtensions(), []string{"png", "jpg"}) || c.GetDir() != pics {
		t.Fatalf("stored category = %v", c)
	}

	// The CLI keeps it while changing other fields.
	v = h.settingsCLI("--tray", "off", "--notify-failure", "off", "--max-concurrent", "4")
	if len(v.Categories) != 6 || v.Categories[5].Name != "Images" || v.Categories[5].Folder != pics ||
		v.ShowTrayIcon || v.NotifyOnFailure || !v.NotifyOnComplete || v.MaxConcurrentDownloads != 4 || v.DefaultDir != h.out {
		t.Fatalf("settings after the CLI change = %+v", v)
	}
	before := h.getSettings()

	// Invalid category lists are refused and change nothing.
	for name, cats := range map[string][]*downexv1.Category{
		"duplicate name": {{Name: "Video"}, {Name: "video"}},
		"bad extension":  {{Name: "X", Extensions: []string{"tar.gz"}}},
		"relative dir":   {{Name: "X", Dir: "relative"}},
	} {
		bad := proto.Clone(before).(*downexv1.Settings)
		bad.Categories = cats
		if _, err := h.updateSettings(bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: UpdateSettings answered %v, want InvalidArgument", name, err)
		}
	}
	if after := h.getSettings(); !proto.Equal(after, before) {
		t.Fatalf("a refused update changed the settings:\n%v\nwant\n%v", after, before)
	}

	// launch_at_login of a private daemon is stored only: the sign-in entry belongs
	// to the user's own daemon (internal/platform tests the writer itself under test
	// names).
	entryBefore, err := signInEntry()
	if err != nil {
		t.Fatalf("read the sign-in entry: %v", err)
	}
	if v := h.settingsCLI("--launch-at-login", "on"); !v.LaunchAtLogin {
		t.Fatalf("launch at login not stored: %+v", v)
	}
	if entryAfter, err := signInEntry(); err != nil || entryAfter != entryBefore {
		t.Fatalf("a private daemon changed the sign-in entry: %q -> %q (%v)", entryBefore, entryAfter, err)
	}
	before = h.getSettings()

	// Everything survives a restart.
	h.stop(d)
	d = h.startDaemon()
	h.waitReady(d)
	if after := h.getSettings(); !proto.Equal(after, before) {
		t.Fatalf("settings after a restart:\n%v\nwant\n%v", after, before)
	}
	if v := h.settingsCLI("--launch-at-login", "off"); v.LaunchAtLogin {
		t.Fatalf("launch at login still on: %+v", v)
	}

	// The custom category's folder is created for its first download.
	id := h.add(srv.URL + "/img/photo.png")
	dl := h.waitCompleted(id)
	if dl.GetCategory() != "Images" || dl.GetDir() != pics {
		t.Fatalf("download of a png = %v", dl)
	}
	checkFile(t, filepath.Join(pics, "photo.png"), seed, size)
	h.stop(d)
}
