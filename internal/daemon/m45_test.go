package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

const stAwaiting = downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION

// defaultsFor is DefaultSettings normalised with dir as the Downloads folder.
func defaultsFor(dir string) Settings {
	s := DefaultSettings()
	s.MaxConcurrentDownloads, s.DefaultDir, s.DefaultMaxConnections = DefaultMaxConcurrentDownloads, dir, DefaultMaxConnections
	return s
}

func TestChooseCategory(t *testing.T) {
	cats := DefaultCategories()
	custom := append(DefaultCategories(), Category{Name: "Clips", Extensions: []string{"mp4", "gif"}})
	for _, c := range []struct {
		name                string
		cats                []Category
		filename, url, mime string
		want                string
	}{
		{"filename", cats, "movie.MKV", "https://x.test/get?id=1", "", "Video"},
		{"filename wins over URL", cats, "setup.exe", "https://x.test/files/a.zip", "", "Programs"},
		{"URL when the filename has no extension", cats, "README", "https://x.test/files/a.pdf", "", "Documents"},
		{"URL path, query ignored", cats, "", "https://x.test/dl/song.flac?token=a.zip", "", "Audio"},
		{"percent-encoded URL", cats, "", "https://x.test/my%20file.7z", "", "Archives"},
		{"trailing slash", cats, "", "https://x.test/dir/archive.tar.gz/", "", "Archives"},
		{"MIME when no name matches", cats, "", "https://x.test/download.php", "application/pdf", "Documents"},
		{"unknown extension falls back to MIME", cats, "data.bin", "https://x.test/f", "video/mp4", "Video"},
		{"generic video MIME", cats, "", "https://x.test/stream", "video/x-new-codec", "Video"},
		{"generic audio MIME", cats, "", "https://x.test/stream", "audio/x-unknown", "Audio"},
		{"program MIME", cats, "", "https://x.test/get", "application/x-msdownload", "Programs"},
		{"nothing matches", cats, "notes.xyz", "https://x.test/", "application/octet-stream", ""},
		{"no hints", cats, "", "https://x.test", "", ""},
		{"host is not a name", cats, "", "https://files.example.zip/", "", ""},
		{"dot file", cats, ".zip", "https://x.test/", "", ""},
		{"first matching category wins", custom, "clip.mp4", "", "", "Video"},
		{"later category", custom, "anim.gif", "", "", "Clips"},
		{"no categories", nil, "a.zip", "https://x.test/a.zip", "application/zip", ""},
	} {
		if got := chooseCategory(c.cats, c.filename, c.url, c.mime); got != c.want {
			t.Errorf("%s: chooseCategory(%q, %q, %q) = %q, want %q", c.name, c.filename, c.url, c.mime, got, c.want)
		}
	}
}

func TestNormalizeCategories(t *testing.T) {
	dir := t.TempDir()
	got, err := normalizeCategories([]Category{
		{Name: "Video", Extensions: []string{" .MP4", "mkv", "mp4"}},
		{Name: "Books", Extensions: nil, Dir: filepath.Join(dir, "b", "..", "books")},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Category{{Name: "Video", Extensions: []string{"mp4", "mkv"}}, {Name: "Books", Extensions: []string{}, Dir: filepath.Join(dir, "books")}}
	if !slices.EqualFunc(got, want, Category.equal) {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}
	if got, err := normalizeCategories(nil, dir); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no categories = %#v, %v", got, err)
	}

	many := make([]Category, MaxCategories+1)
	for i := range many {
		many[i] = Category{Name: "c" + string(rune('A'+i%26)) + string(rune('a'+i/26))}
	}
	exts := make([]string, MaxCategoryExtensions+1)
	for i := range exts {
		exts[i] = "e" + strings.Repeat("x", i%10) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for name, cs := range map[string][]Category{
		"too many categories":    many,
		"too many extensions":    {{Name: "X", Extensions: exts}},
		"empty name":             {{Name: ""}},
		"long name":              {{Name: strings.Repeat("n", MaxCategoryNameRunes+1)}},
		"duplicate name":         {{Name: "Video"}, {Name: "video"}},
		"name with separator":    {{Name: `a\b`}},
		"name with slash":        {{Name: "a/b"}},
		"dot dot":                {{Name: ".."}},
		"reserved device name":   {{Name: "CON"}},
		"trailing dot":           {{Name: "Video."}},
		"leading space":          {{Name: " Video"}},
		"control character":      {{Name: "Vid\neo"}},
		"empty extension":        {{Name: "X", Extensions: []string{""}}},
		"extension with a dot":   {{Name: "X", Extensions: []string{"tar.gz"}}},
		"extension with a space": {{Name: "X", Extensions: []string{"m p4"}}},
		"long extension":         {{Name: "X", Extensions: []string{strings.Repeat("a", 17)}}},
		"non-ASCII extension":    {{Name: "X", Extensions: []string{"mp4é"}}},
		"relative dir":           {{Name: "X", Dir: "relative/dir"}},
		"dir with NUL":           {{Name: "X", Dir: dir + "\x00"}},
	} {
		if _, err := normalizeCategories(cs, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// 32 categories and 200 extensions are allowed.
	if _, err := normalizeCategories(many[:MaxCategories], dir); err != nil {
		t.Errorf("%d categories: %v", MaxCategories, err)
	}
	if _, err := normalizeCategories([]Category{{Name: "X", Extensions: exts[:MaxCategoryExtensions]}}, dir); err != nil {
		t.Errorf("%d extensions: %v", MaxCategoryExtensions, err)
	}
}

func TestCategorySettingsValidationRPC(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)
	for name, cats := range map[string][]*downexv1.Category{
		"duplicate names": {{Name: "A"}, {Name: "a"}},
		"bad extension":   {{Name: "A", Extensions: []string{"a*b"}}},
		"relative dir":    {{Name: "A", Dir: "rel"}},
		"bad name":        {{Name: "x:y"}},
	} {
		s := protoSettings(h.m.Settings())
		s.Categories = cats
		if _, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s := h.m.Settings(); !s.Equal(defaultsFor(h.dir)) {
		t.Fatalf("rejected updates changed the settings: %+v", s)
	}
	// A valid update is normalised and read back in full.
	s := protoSettings(h.m.Settings())
	s.Categories = []*downexv1.Category{{Name: "Books", Extensions: []string{".EPUB", "mobi"}}}
	s.UseCategoryFolders, s.NotifyOnComplete, s.ShowTrayIcon = false, false, false
	upd, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []*downexv1.Settings{upd.Msg.GetSettings(), got.Msg.GetSettings()} {
		if c := g.GetCategories(); len(c) != 1 || c[0].GetName() != "Books" || !slices.Equal(c[0].GetExtensions(), []string{"epub", "mobi"}) ||
			g.GetUseCategoryFolders() || g.GetNotifyOnComplete() || g.GetShowTrayIcon() || !g.GetConfirmBrowserDownloads() ||
			!g.GetNotifyOnFailure() || g.GetLaunchAtLogin() {
			t.Fatalf("settings = %v", g)
		}
	}
}

// A settings file written by an older daemon (only the M2 fields) gets the defaults of
// the M4.5 fields; one that sets them keeps its values.
func TestSettingsFileOfOlderDaemon(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFileName)
	old := `{"max_concurrent_downloads": 2, "global_speed_limit": 5, "default_dir": ` + jsonString(dir) +
		`, "default_max_connections": 4, "some_future_field": 1}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultSettings()
	want.MaxConcurrentDownloads, want.GlobalSpeedLimit, want.DefaultDir, want.DefaultMaxConnections = 2, 5, dir, 4
	if !s.Equal(want) || !s.UseCategoryFolders || !s.ConfirmBrowserDownloads || !s.NotifyOnComplete ||
		!s.NotifyOnFailure || !s.ShowTrayIcon || s.LaunchAtLogin || len(s.Categories) != 5 {
		t.Fatalf("old file = %+v", s)
	}

	explicit := `{"max_concurrent_downloads": 2, "categories": [], "use_category_folders": false,
		"confirm_browser_downloads": false, "notify_on_complete": false, "notify_on_failure": false,
		"launch_at_login": true, "show_tray_icon": false}`
	if err := os.WriteFile(path, []byte(explicit), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Categories) != 0 || s.UseCategoryFolders || s.ConfirmBrowserDownloads || s.NotifyOnComplete ||
		s.NotifyOnFailure || !s.LaunchAtLogin || s.ShowTrayIcon {
		t.Fatalf("explicit file = %+v", s)
	}

	// Invalid categories in a file are replaced by the defaults when the daemon starts.
	bad := `{"categories": [{"name": "A/B", "extensions": ["x"]}], "confirm_browser_downloads": false}`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOptions{settingsPath: path, dir: dir})
	got := h.m.Settings()
	if len(got.Categories) != 5 || got.Categories[0].Name != "Video" || got.ConfirmBrowserDownloads {
		t.Fatalf("settings from a file with bad categories = %+v", got)
	}
}

func jsonString(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func TestAddChoosesCategoryFolder(t *testing.T) {
	const size, seed = 300 << 10, 61
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"c"`})
	booksDir := filepath.Join(t.TempDir(), "deep", "books") // its parent does not exist either
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) {
		s.Categories = append(s.Categories, &downexv1.Category{Name: "Books", Extensions: []string{"mobi"}, Dir: booksDir})
	}})
	archives := filepath.Join(h.dir, "Archives")

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/files/pack.zip", Source: downexv1.AddSource_ADD_SOURCE_CLI})
	if d.GetCategory() != "Archives" || d.GetDir() != archives || d.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI {
		t.Fatalf("added = %v", d)
	}
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size) // the folder was created when the download started

	// A configured category folder, created with its missing parents.
	b := h.add(&downexv1.AddRequest{Url: ts.URL + "/get?id=7", Filename: "novel.mobi"})
	if b.GetCategory() != "Books" || b.GetDir() != booksDir {
		t.Fatalf("added = %v", b)
	}
	checkFile(t, h.waitCompleted(b.GetId()), seed, size)

	// The MIME hint, and an explicit dir that keeps the category but not its folder.
	own := t.TempDir()
	c := h.add(&downexv1.AddRequest{Url: ts.URL + "/stream", Dir: own, Mime: "Video/MP4; codecs=x", ExpectedSize: 12345,
		StartPaused: true})
	if c.GetCategory() != "Video" || c.GetDir() != own || c.GetMime() != "video/mp4" || c.GetExpectedSize() != 12345 ||
		c.GetState() != stPaused {
		t.Fatalf("added = %v", c)
	}
	if rec := storedState(t, h.st, c.GetId()); rec.Category != "Video" || rec.MIME != "video/mp4" || rec.ExpectedSize != 12345 {
		t.Fatalf("stored = %+v", rec)
	}
	// Nothing matches: the default directory.
	if e := h.add(&downexv1.AddRequest{Url: ts.URL + "/x.unknownext", StartPaused: true}); e.GetCategory() != "" || e.GetDir() != h.dir {
		t.Fatalf("added = %v", e)
	}
	// Bad hints.
	for name, req := range map[string]*downexv1.AddRequest{
		"negative size": {Url: ts.URL + "/a.zip", ExpectedSize: -2},
		"mime with CR":  {Url: ts.URL + "/a.zip", Mime: "video/mp4\r\n"},
	} {
		if _, err := h.dl.Add(ctxT(t), connect.NewRequest(req)); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Without use_category_folders everything goes into the default directory.
	s := protoSettings(h.m.Settings())
	s.UseCategoryFolders = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	if f := h.add(&downexv1.AddRequest{Url: ts.URL + "/b.zip", StartPaused: true}); f.GetCategory() != "Archives" || f.GetDir() != h.dir {
		t.Fatalf("added without category folders = %v", f)
	}
}

// launcher records the downloads the confirmation UI was opened for.
type launcher struct {
	mu  sync.Mutex
	ids []int64
	err error
}

func (l *launcher) open(id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ids = append(l.ids, id)
	return l.err
}

func (l *launcher) launched() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ids)
}

// nextNotice waits for a notice of kind.
func nextNotice(t *testing.T, ch <-chan Notice, kind NoticeKind) Notice {
	t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case n, ok := <-ch:
			if !ok {
				t.Fatalf("notices closed while waiting for kind %d", kind)
			}
			if n.Kind == kind {
				return n
			}
		case <-timeout:
			t.Fatalf("no notice of kind %d", kind)
		}
	}
}

func TestBrowserAddAwaitsConfirmation(t *testing.T) {
	const size, seed = 256 << 10, 62
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"a"`})
	l := &launcher{}
	h := newHarness(t, harnessOptions{openConfirmation: l.open})
	notices, cancel := h.m.Subscribe()
	defer cancel()

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/v/movie.mp4", Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
		ExpectedSize: size, Mime: "video/mp4", StartPaused: true})
	video := filepath.Join(h.dir, "Video")
	if d.GetState() != stAwaiting || d.GetCategory() != "Video" || d.GetDir() != video || d.GetFilename() != "movie.mp4" ||
		d.GetExpectedSize() != size || d.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || d.GetQueuePosition() != 0 {
		t.Fatalf("added = %v", d)
	}
	n := nextNotice(t, notices, NoticeAwaiting)
	if !n.UILaunched || n.Download.GetId() != d.GetId() {
		t.Fatalf("notice = %+v", n)
	}
	if got := l.launched(); !slices.Equal(got, []int64{d.GetId()}) {
		t.Fatalf("UI opened for %v", got)
	}
	// Nothing runs it and nothing is created on disk before the confirmation.
	time.Sleep(300 * time.Millisecond)
	if g := h.get(d.GetId()); g.GetState() != stAwaiting || len(ts.Requests()) != 0 {
		t.Fatalf("awaiting download = %v, %d requests", g.GetState(), len(ts.Requests()))
	}
	if _, err := os.Stat(video); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("category folder created before confirmation: %v", err)
	}
	if a := h.m.Activity(); a.Awaiting != 1 || a.Active != 0 || a.Queued != 0 {
		t.Fatalf("activity = %+v", a)
	}
	if st := h.status(); st.GetQueued() != 0 || st.GetActive() != 0 {
		t.Fatalf("status = %v", st)
	}
	// Pause and Resume do not apply.
	if _, err := h.dl.Pause(ctxT(t), connect.NewRequest(&downexv1.PauseRequest{Id: d.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("pause awaiting: %v", err)
	}
	if _, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume awaiting: %v", err)
	}

	res, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true}))
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Msg.GetDownload(); c.GetState() == stAwaiting || c.GetDir() != video || c.GetFilename() != "movie.mp4" {
		t.Fatalf("confirmed = %v", c)
	}
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size)
	if n := nextNotice(t, notices, NoticeCompleted); n.Download.GetId() != d.GetId() || n.Download.GetState() != stCompleted {
		t.Fatalf("completion notice = %+v", n)
	}
	// Confirm applies only to downloads awaiting confirmation.
	_, err = h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true}))
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("confirm a completed download: %v", err)
	}
	p := h.add(&downexv1.AddRequest{Url: ts.URL + "/p.bin", StartPaused: true})
	if _, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: p.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("confirm a paused download: %v", err)
	}
	if _, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: 999})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("confirm a missing download: %v", err)
	}
}

func TestConfirmOptions(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 63})
	h := newHarness(t, harnessOptions{}) // no UI: notices only
	notices, cancel := h.m.Subscribe()
	defer cancel()
	browser := func(path, name string) *downexv1.Download {
		t.Helper()
		d := h.add(&downexv1.AddRequest{Url: ts.URL + path, Filename: name, Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
		if d.GetState() != stAwaiting {
			t.Fatalf("added = %v", d)
		}
		return d
	}
	confirm := func(r *downexv1.ConfirmRequest) (*downexv1.Download, error) {
		res, err := h.dl.Confirm(ctxT(t), connect.NewRequest(r))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetDownload(), nil
	}

	a := browser("/r/report.pdf", "")
	if n := nextNotice(t, notices, NoticeAwaiting); n.UILaunched || n.Download.GetId() != a.GetId() {
		t.Fatalf("notice without a UI = %+v", n)
	}
	if a.GetFilename() != "report.pdf" || a.GetCategory() != "Documents" {
		t.Fatalf("proposal = %v", a)
	}
	// Invalid input is refused and leaves the download awaiting.
	for name, r := range map[string]*downexv1.ConfirmRequest{
		"relative dir":      {Id: a.GetId(), Dir: "relative"},
		"dir is a file":     {Id: a.GetId(), Dir: os.Args[0]},
		"missing parent":    {Id: a.GetId(), Dir: filepath.Join(t.TempDir(), "no", "such")},
		"name with a slash": {Id: a.GetId(), Filename: "../x.pdf"},
		"dot dot":           {Id: a.GetId(), Filename: ".."},
		"bad connections":   {Id: a.GetId(), MaxConnections: ptr(int32(MaxConnectionsLimit + 1))},
		"negative limit":    {Id: a.GetId(), SpeedLimit: ptr(int64(-1))},
	} {
		if _, err := confirm(r); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if g := h.get(a.GetId()); g.GetState() != stAwaiting || g.GetFilename() != "report.pdf" {
		t.Fatalf("after refused confirmations: %v", g)
	}

	// Download later, into another folder (created below its existing parent) under a
	// new name, which is reserved: a file of that name exists, so "(1)" is chosen.
	other := filepath.Join(t.TempDir(), "picked")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "final.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := confirm(&downexv1.ConfirmRequest{Id: a.GetId(), Dir: other, Filename: "final.pdf", Start: false,
		MaxConnections: ptr(int32(2)), SpeedLimit: ptr(int64(1000))})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetState() != stPaused || c.GetDir() != other || c.GetFilename() != "final (1).pdf" || c.GetMaxConnections() != 2 ||
		c.GetSpeedLimit() != 1000 {
		t.Fatalf("confirmed later = %v", c)
	}
	if rec := storedState(t, h.st, a.GetId()); rec.State != model.StatePaused || rec.Filename != "final (1).pdf" || rec.Dir != other {
		t.Fatalf("stored = %+v", rec)
	}

	// Two awaiting downloads proposing the same name get different ones.
	b1, b2 := browser("/1/same.zip", ""), browser("/2/same.zip", "")
	if b1.GetFilename() != "same.zip" || b2.GetFilename() != "same (1).zip" {
		t.Fatalf("proposals = %q, %q", b1.GetFilename(), b2.GetFilename())
	}
	// Choosing another category's folder changes the category.
	c2, err := confirm(&downexv1.ConfirmRequest{Id: b2.GetId(), Dir: filepath.Join(h.dir, "Video"), Filename: "clip.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if c2.GetCategory() != "Video" || c2.GetState() != stPaused {
		t.Fatalf("confirmed into the Video folder = %v", c2)
	}
	// A name the server chooses: no proposal for a script-like URL.
	s := browser("/download.php?id=4", "")
	if s.GetFilename() != "" || s.GetCategory() != "" {
		t.Fatalf("proposal for a script URL = %v", s)
	}

	// Remove cancels an awaiting download and deletes no file (none exists yet).
	if _, err := h.dl.Remove(ctxT(t), connect.NewRequest(&downexv1.RemoveRequest{Id: b1.GetId(), DeleteFile: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dl.Get(ctxT(t), connect.NewRequest(&downexv1.GetRequest{Id: b1.GetId()})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("removed awaiting download: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// Downloads awaiting confirmation stay so across a restart: never started, not queued.
func TestAwaitingSurvivesRestart(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 64})
	st := store.NewMemory()
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{st: st, dir: dir})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if err := h.m.Shutdown(ctxT(t)); err != nil {
		t.Fatal(err)
	}

	h2 := newHarness(t, harnessOptions{st: st, dir: dir})
	time.Sleep(200 * time.Millisecond)
	if g := h2.get(d.GetId()); g.GetState() != stAwaiting || len(ts.Requests()) != 0 {
		t.Fatalf("after restart = %v", g)
	}
	if a := h2.m.Activity(); a.Awaiting != 1 || a.Queued != 0 {
		t.Fatalf("activity after restart = %+v", a)
	}
	// ResumeAll leaves it alone; Confirm still works.
	if n, err := h2.m.ResumeAll(ctxT(t)); err != nil || n != 0 {
		t.Fatalf("ResumeAll = %d, %v", n, err)
	}
	if _, err := h2.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true})); err != nil {
		t.Fatal(err)
	}
	h2.waitCompleted(d.GetId())
}

func TestConfirmationDisabled(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 65})
	l := &launcher{}
	h := newHarness(t, harnessOptions{openConfirmation: l.open, settings: func(s *downexv1.Settings) {
		s.ConfirmBrowserDownloads = false
	}})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if d.GetState() == stAwaiting {
		t.Fatalf("added = %v", d)
	}
	h.waitCompleted(d.GetId())
	// Other sources never await confirmation.
	h2 := newHarness(t, harnessOptions{openConfirmation: l.open})
	for _, src := range []downexv1.AddSource{downexv1.AddSource_ADD_SOURCE_UNSPECIFIED, downexv1.AddSource_ADD_SOURCE_CLI,
		downexv1.AddSource_ADD_SOURCE_UI, downexv1.AddSource(42)} {
		if d := h2.add(&downexv1.AddRequest{Url: ts.URL + "/b.zip", Source: src, StartPaused: true}); d.GetState() != stPaused {
			t.Errorf("source %v: added = %v", src, d)
		}
	}
	if got := l.launched(); len(got) != 0 {
		t.Fatalf("UI opened for %v", got)
	}
}

// A burst of hand-offs opens the confirmation UI once at once and once more for the
// rest, with the oldest of them; a failed launch is reported in the notices.
func TestConfirmationLaunchesCoalesce(t *testing.T) {
	l := &launcher{}
	h := newHarness(t, harnessOptions{openConfirmation: l.open})
	h.m.confirm.interval = 300 * time.Millisecond // before any Add
	notices, cancel := h.m.Subscribe()
	defer cancel()
	var ids []int64
	for i := range 4 {
		ids = append(ids, h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/f" + string(rune('a'+i)) + ".zip",
			Source: downexv1.AddSource_ADD_SOURCE_BROWSER}).GetId())
	}
	for range ids {
		if n := nextNotice(t, notices, NoticeAwaiting); !n.UILaunched {
			t.Fatalf("notice = %+v", n)
		}
	}
	got := l.launched()
	if len(got) != 2 || got[0] != ids[0] || !slices.Contains(ids[1:], got[1]) {
		t.Fatalf("UI opened for %v (added %v)", got, ids)
	}

	l.mu.Lock()
	l.err = errors.New("no window")
	l.mu.Unlock()
	time.Sleep(400 * time.Millisecond)
	h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/g.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if n := nextNotice(t, notices, NoticeAwaiting); n.UILaunched {
		t.Fatalf("notice after a failed launch = %+v", n)
	}
}

func TestPauseAllResumeAll(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 66})
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 2 }})
	ctx := ctxT(t)
	var ids []int64
	for i := range 3 {
		ids = append(ids, h.add(&downexv1.AddRequest{Url: ts.URL + "/p" + string(rune('0'+i)), SpeedLimit: 64 << 10}).GetId())
	}
	h.waitReceived(ids[0], 1)
	h.waitReceived(ids[1], 1)
	if g := h.get(ids[2]); g.GetState() != stQueued {
		t.Fatalf("third download = %v", g.GetState())
	}
	done := h.add(&downexv1.AddRequest{Url: ts.URL + "/small", Filename: "small.bin", StartPaused: true})
	awaiting := h.add(&downexv1.AddRequest{Url: ts.URL + "/w.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})

	res, err := h.dl.PauseAll(ctx, connect.NewRequest(&downexv1.PauseAllRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetPaused() != 3 {
		t.Fatalf("PauseAll paused %d, want 3", res.Msg.GetPaused())
	}
	for _, id := range ids {
		if g := h.get(id); g.GetState() != stPaused {
			t.Fatalf("download %d = %v after PauseAll", id, g.GetState())
		}
	}
	if a := h.m.Activity(); a.Active != 0 || a.Queued != 0 || a.Awaiting != 1 {
		t.Fatalf("activity = %+v", a)
	}
	if n, err := h.m.PauseAll(ctx); err != nil || n != 0 {
		t.Fatalf("second PauseAll = %d, %v", n, err)
	}

	rres, err := h.dl.ResumeAll(ctx, connect.NewRequest(&downexv1.ResumeAllRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if rres.Msg.GetResumed() != 4 { // the three, and the download added paused
		t.Fatalf("ResumeAll resumed %d, want 4", rres.Msg.GetResumed())
	}
	for _, id := range append(ids, done.GetId()) {
		if g := h.get(id); g.GetState() == stPaused || g.GetState() == stAwaiting {
			t.Fatalf("download %d = %v after ResumeAll", id, g.GetState())
		}
	}
	if g := h.get(awaiting.GetId()); g.GetState() != stAwaiting {
		t.Fatalf("awaiting download after ResumeAll = %v", g.GetState())
	}
	waitUntil(t, "two running", func() bool { a := h.m.Activity(); return a.Active == 2 && a.Queued == 2 })
	if n, err := h.m.ResumeAll(ctx); err != nil || n != 0 {
		t.Fatalf("second ResumeAll = %d, %v", n, err)
	}
	// Failed downloads are not resumed by ResumeAll.
	if n, err := h.m.PauseAll(ctx); err != nil || n != 4 {
		t.Fatalf("PauseAll = %d, %v", n, err)
	}
	missing := serve(t, testserver.Options{Size: 1, FailFirst: 1000, FailStatus: 404})
	bad := h.add(&downexv1.AddRequest{Url: missing.URL + "/gone"})
	h.waitState(bad.GetId(), stFailed)
	if n, err := h.m.ResumeAll(ctx); err != nil || n != 4 {
		t.Fatalf("ResumeAll with a failed download = %d, %v", n, err)
	}
	if g := h.get(bad.GetId()); g.GetState() != stFailed {
		t.Fatalf("failed download after ResumeAll = %v", g.GetState())
	}
}

func TestLaunchAtLoginApplied(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []bool
		fail  error
	)
	apply := func(on bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, on)
		return fail
	}
	seen := func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
	path := filepath.Join(t.TempDir(), SettingsFileName)
	h := newHarness(t, harnessOptions{settingsPath: path, launchAtLogin: apply})
	if got := seen(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("calls at start = %v, want the setting verified once", got)
	}
	set := func(on bool) error {
		s := protoSettings(h.m.Settings())
		s.LaunchAtLogin = on
		_, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
		return err
	}
	if err := set(true); err != nil {
		t.Fatal(err)
	}
	if err := set(true); err != nil { // unchanged: not applied again
		t.Fatal(err)
	}
	if got := seen(); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("calls = %v", got)
	}
	mu.Lock()
	fail = errors.New("registry is read-only")
	mu.Unlock()
	if err := set(false); connectCode(err) != connect.CodeInternal {
		t.Fatalf("failed apply: %v", err)
	}
	if !h.m.Settings().LaunchAtLogin {
		t.Fatal("a failed apply changed the setting")
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), `"launch_at_login": true`) {
		t.Fatalf("settings file = %s, %v", data, err)
	}
	mu.Lock()
	fail = errors.ErrUnsupported
	mu.Unlock()
	if err := set(false); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unsupported apply: %v", err)
	}

	// The restarted daemon verifies the stored value.
	mu.Lock()
	fail, calls = nil, nil
	mu.Unlock()
	newHarness(t, harnessOptions{settingsPath: path, launchAtLogin: apply})
	if got := seen(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("calls at restart = %v", got)
	}
}

func TestSubscribeNotices(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 67})
	h := newHarness(t, harnessOptions{})
	ch, cancel := h.m.Subscribe()
	other, cancelOther := h.m.Subscribe()

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/ok.bin"})
	if n := nextNotice(t, ch, NoticeCompleted); n.Download.GetId() != d.GetId() || n.Download.GetFilename() == "" {
		t.Fatalf("completed = %+v", n)
	}
	missing := serve(t, testserver.Options{Size: 1, FailFirst: 1000, FailStatus: 404})
	bad := h.add(&downexv1.AddRequest{Url: missing.URL + "/gone.bin"})
	if n := nextNotice(t, ch, NoticeFailed); n.Download.GetId() != bad.GetId() || n.Download.GetErrorCode() != CodeHTTPStatus {
		t.Fatalf("failed = %+v", n)
	}
	s := protoSettings(h.m.Settings())
	s.ShowTrayIcon = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	if n := nextNotice(t, ch, NoticeSettings); n.Settings.ShowTrayIcon {
		t.Fatalf("settings notice = %+v", n)
	}
	cancel()
	cancel() // idempotent
	if _, ok := <-ch; ok {
		for range ch { // drain what was buffered before the cancel
		}
	}
	// Shutdown closes the remaining subscriptions; later ones are closed at once.
	cancelOther()
	other2, cancel2 := h.m.Subscribe()
	defer cancel2()
	if err := h.m.Shutdown(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	for range other2 {
	}
	for range other {
	}
	late, cancelLate := h.m.Subscribe()
	defer cancelLate()
	if _, ok := <-late; ok {
		t.Fatal("subscription after Shutdown is open")
	}
}

// Downloads awaiting confirmation keep their proposed name reserved: the engine does
// not give it to another download.
func TestAwaitingNameIsReserved(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 32 << 10, Seed: 68, Filename: "taken.zip"})
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.UseCategoryFolders = false }})
	a := h.add(&downexv1.AddRequest{Url: ts.URL + "/x/taken.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	b := h.add(&downexv1.AddRequest{Url: ts.URL + "/y"}) // named "taken.zip" by the server
	done := h.waitCompleted(b.GetId())
	if a.GetFilename() != "taken.zip" || done.GetFilename() != "taken (1).zip" {
		t.Fatalf("names = %q, %q", a.GetFilename(), done.GetFilename())
	}
	if _, err := os.Stat(engine.PartPath(h.dir, "taken.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part file of the awaiting download: %v", err)
	}
}

func TestSettingsCloneAndEqual(t *testing.T) {
	a := DefaultSettings()
	b := a.Clone()
	b.Categories[0].Extensions[0] = "changed"
	if a.Categories[0].Extensions[0] == "changed" || a.Equal(b) {
		t.Fatal("Clone shares the categories")
	}
	if !(Settings{}).Equal(Settings{Categories: []Category{}}) {
		t.Fatal("nil and empty categories differ")
	}
}

// A browser hand-off with its own dir is validated like Add's but creates nothing.
func TestAwaitingWithRequestedDir(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	base := t.TempDir()
	missing := filepath.Join(base, "new")
	d := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/a.zip", Dir: missing, Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if d.GetState() != stAwaiting || d.GetDir() != missing {
		t.Fatalf("added = %v", d)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dir created before confirmation: %v", err)
	}
	for name, dir := range map[string]string{
		"missing parent": filepath.Join(base, "no", "such"),
		"a file":         os.Args[0],
		"relative":       "rel",
	} {
		_, err := h.dl.Add(ctxT(t), connect.NewRequest(&downexv1.AddRequest{Url: "http://127.0.0.1:9/a.zip", Dir: dir,
			Source: downexv1.AddSource_ADD_SOURCE_BROWSER}))
		if connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Confirm accepts every folder the daemon itself would choose (GO-2): a configured
// category's folder and the proposed folder are trusted like the settings they come
// from, and created with their parents when the download starts; only a folder that is
// a file is refused. Other folders keep Add's checks (TestConfirmOptions).
func TestConfirmIntoCategoryFolder(t *testing.T) {
	const size, seed = 64 << 10, 69
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	base := t.TempDir()
	films := filepath.Join(base, "Media", "Films", "New") // neither Media nor Films exists
	h := newHarness(t, harnessOptions{openConfirmation: (&launcher{}).open, settings: func(s *downexv1.Settings) {
		s.Categories = append(s.Categories, &downexv1.Category{Name: "Films", Extensions: []string{"film"}, Dir: films})
	}})
	confirm := func(r *downexv1.ConfirmRequest) *downexv1.Download {
		t.Helper()
		res, err := h.dl.Confirm(ctxT(t), connect.NewRequest(r))
		if err != nil {
			t.Fatalf("confirm %v: %v", r, err)
		}
		return res.Msg.GetDownload()
	}

	// The dialog's category selector sends the chosen category's folder.
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/v/clip.mp4", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if d.GetState() != stAwaiting || d.GetCategory() != "Video" {
		t.Fatalf("added = %v", d)
	}
	c := confirm(&downexv1.ConfirmRequest{Id: d.GetId(), Dir: films, Start: true})
	if c.GetDir() != films || c.GetCategory() != "Films" || c.GetFilename() != "clip.mp4" {
		t.Fatalf("confirmed into the Films folder = %v", c)
	}
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)

	// The proposed folder is accepted unchanged although the settings moved it since.
	p := h.add(&downexv1.AddRequest{Url: ts.URL + "/f/show.film", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if p.GetDir() != films || p.GetCategory() != "Films" {
		t.Fatalf("added = %v", p)
	}
	if err := os.RemoveAll(filepath.Join(base, "Media")); err != nil {
		t.Fatal(err)
	}
	s := protoSettings(h.m.Settings())
	for _, cat := range s.GetCategories() {
		if cat.GetName() == "Films" {
			cat.Dir = filepath.Join(base, "Elsewhere")
		}
	}
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	if c := confirm(&downexv1.ConfirmRequest{Id: p.GetId(), Dir: films}); c.GetDir() != films || c.GetState() != stPaused {
		t.Fatalf("confirmed into the proposed folder = %v", c)
	}

	// A category folder that is a file is still refused.
	file := filepath.Join(base, "Elsewhere")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := h.add(&downexv1.AddRequest{Url: ts.URL + "/g/a.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	_, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: f.GetId(), Dir: file}))
	if connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("confirm into a category folder that is a file: %v", err)
	}
}

// A browser hand-off is held for confirmation only while the daemon can ask the user
// (GO-3): it can open the confirmation window, or a subscriber shows the user its
// notices (the tray while its icon is visible). Otherwise nothing would ever show it,
// so it is queued as before (paused with start_paused).
func TestBrowserAddWithNobodyToAsk(t *testing.T) {
	const size, seed = 64 << 10, 71
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	h := newHarness(t, harnessOptions{nobodyAsks: true})
	browser := func(path string, paused bool) *downexv1.Download {
		t.Helper()
		return h.add(&downexv1.AddRequest{Url: ts.URL + path, Source: downexv1.AddSource_ADD_SOURCE_BROWSER, StartPaused: paused})
	}
	d := browser("/a/song.mp3", false)
	if d.GetState() == stAwaiting || d.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || d.GetCategory() != "Audio" ||
		d.GetDir() != filepath.Join(h.dir, "Audio") {
		t.Fatalf("added with nobody to ask = %v", d)
	}
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
	if p := browser("/b/later.zip", true); p.GetState() != stPaused {
		t.Fatalf("added paused with nobody to ask = %v", p)
	}

	// A subscriber that shows its notices can ask: the hand-off awaits confirmation.
	notices, cancel := h.m.Subscribe()
	defer cancel()
	h.m.ShowNotices(notices, true)
	a := browser("/c/held.zip", true)
	if a.GetState() != stAwaiting {
		t.Fatalf("added with a subscriber that shows notices = %v", a)
	}
	if n := nextNotice(t, notices, NoticeAwaiting); n.UILaunched || n.Download.GetId() != a.GetId() {
		t.Fatalf("notice = %+v", n)
	}
	// It stops showing them (the icon was hidden), or its subscription ends.
	h.m.ShowNotices(notices, false)
	if p := browser("/d/hidden.zip", true); p.GetState() != stPaused {
		t.Fatalf("added after the subscriber stopped showing notices = %v", p)
	}
	h.m.ShowNotices(notices, true)
	cancel()
	h.m.ShowNotices(notices, true) // ignored: the subscription has ended
	if p := browser("/e/ended.zip", true); p.GetState() != stPaused {
		t.Fatalf("added after the subscription ended = %v", p)
	}
	if g := h.get(a.GetId()); g.GetState() != stAwaiting {
		t.Fatalf("the held hand-off = %v", g)
	}
}
