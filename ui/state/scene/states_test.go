package scene_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/media"
	"downex/ui/host"
	"downex/ui/state"
	"downex/ui/state/scene"
)

// loaded is one fixture state set up headless: the simulated daemon and the stores a window
// would feed from it.
type loaded struct {
	m         *scene.Mock
	downloads *state.DownloadsStore
	queues    *state.QueuesStore
	tools     *state.ToolsStore
	progress  *state.ProgressStore
}

func load(t *testing.T, fx scene.FixtureState) *loaded {
	t.Helper()
	o := fx.Options()
	m, err := scene.New(o)
	if err != nil {
		t.Fatalf("%s: %v", fx.Name, err)
	}
	t.Cleanup(m.Close)
	ctx := context.Background()
	l := &loaded{m: m}
	hub := &state.Hub{}
	if fx.Progress > 0 {
		// A progress window watches its download only, through the host's real bridge.
		l.progress = state.NewProgress(state.ProgressOptions{Daemon: m, Events: hub, ID: fx.Progress, Go: func(f func()) { f() }})
		t.Cleanup(l.progress.Dispose)
		first := make(chan struct{}, 1)
		stop := (&host.Bridge{Daemon: m, IDs: []int64{fx.Progress}, Emit: func(r *downexv1.WatchResponse) {
			hub.Emit(r)
			select {
			case first <- struct{}{}:
			default:
			}
		}}).Start(ctx)
		t.Cleanup(stop)
		l.progress.Start(time.Hour)
		select {
		case <-first:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no Watch batch", fx.Name)
		}
		return l
	}
	l.downloads = state.NewDownloads(state.DownloadsOptions{Daemon: m, Events: m})
	t.Cleanup(l.downloads.Dispose)
	l.queues = state.NewQueues(state.QueuesOptions{Daemon: m, Events: m, Go: func(f func()) { f() }})
	t.Cleanup(l.queues.Dispose)
	l.tools = state.NewTools(state.ToolsOptions{Daemon: m, Events: m, Go: func(f func()) { f() }})
	t.Cleanup(l.tools.Dispose)
	l.queues.Start()
	l.tools.Start()
	l.downloads.Start(ctx)
	l.downloads.RefreshStatus()
	if !l.queues.Load() {
		t.Fatalf("%s: queues: %s", fx.Name, l.queues.LoadError())
	}
	l.tools.Load()
	return l
}

func fixtureValue(fx scene.FixtureState, name string) (string, bool) {
	for _, tok := range strings.Split(fx.Fixture, ",") {
		if v, ok := strings.CutPrefix(tok, name+":"); ok {
			return v, true
		}
	}
	return "", false
}

func byName(list []*downexv1.Download, name string) *downexv1.Download {
	for _, d := range list {
		if d.GetFilename() == name {
			return d
		}
	}
	return nil
}

// TestAllFixtureStatesLoadHeadless sets up each of the 87 states and checks that the
// stores hold what its dialog shows and what its steps act on.
func TestAllFixtureStatesLoadHeadless(t *testing.T) {
	states := scene.Fixtures()
	if len(states) != 87 {
		t.Fatalf("%d states, want 87 (78 dialog states and 9 progress views)", len(states))
	}
	dialogs, progress := 0, 0
	seen := map[string]bool{}
	for _, fx := range states {
		if seen[fx.Name] {
			t.Fatalf("%s twice", fx.Name)
		}
		seen[fx.Name] = true
		if fx.Progress > 0 {
			progress++
		} else {
			dialogs++
		}
		t.Run(fx.Name, func(t *testing.T) {
			l := load(t, fx)
			if fx.Progress > 0 {
				checkProgress(t, fx, l)
				return
			}
			checkMain(t, fx, l)
		})
	}
	if dialogs != 78 || progress != 9 {
		t.Fatalf("%d dialog states and %d progress views", dialogs, progress)
	}
}

func checkMain(t *testing.T, fx scene.FixtureState, l *loaded) {
	ctx := context.Background()
	d := l.downloads
	if !d.Ready() || d.Stale() || d.LoadError() != "" || len(d.All()) < 28 {
		t.Fatalf("list: ready %v, stale %v, %q, %d rows", d.Ready(), d.Stale(), d.LoadError(), len(d.All()))
	}
	if l.tools.Status() == nil || !l.queues.Loaded() {
		t.Fatal("tools or queues not loaded")
	}
	// The steps' rows are there (the script finds them by name, after a search when it searches).
	for _, s := range fx.Steps {
		switch s.Kind {
		case scene.StepSelectRow, scene.StepCtrlRow, scene.StepContextRow, scene.StepDoubleRow:
			if byName(d.All(), s.Text) == nil {
				t.Fatalf("no row %q", s.Text)
			}
		case scene.StepSearch:
			d.SetSearch(s.Text)
			if len(d.Visible()) == 0 {
				t.Fatalf("search %q finds nothing", s.Text)
			}
		case scene.StepClick:
			if s.Selector == "[role=dialog] .queue" {
				found := false
				for _, q := range l.queues.List() {
					found = found || q.GetName() == s.Text
				}
				if !found {
					t.Fatalf("no queue %q", s.Text)
				}
			}
		case scene.StepFindMedia:
			checkFindMedia(t, l, s)
		}
	}
	d.SetSearch("")
	// The queues the state names.
	if n, ok := fixtureValue(fx, "queues"); ok {
		if want, _ := strconv.Atoi(n); len(l.queues.List()) != want {
			t.Fatalf("%d queues, want %d", len(l.queues.List()), want)
		}
	} else if len(l.queues.List()) != 2 {
		t.Fatalf("%d queues, want the main queue and Night", len(l.queues.List()))
	}
	if n, ok := fixtureValue(fx, "night"); ok {
		want, _ := strconv.Atoi(n)
		if c := d.QueueCounts()[2]; c != want {
			t.Fatalf("Night holds %d, want %d", c, want)
		}
	}
	if fx.Name == "queues-main" && d.QueueCounts()[state.MainQueueID] != 13 {
		t.Fatalf("the main queue holds %d to do, want 13", d.QueueCounts()[state.MainQueueID])
	}
	if name, ok := fixtureValue(fx, "gone"); ok {
		row := byName(d.All(), name)
		if exists, err := l.m.FileExists(ctx, row.GetId()); err != nil || exists {
			t.Fatalf("%s still there", name)
		}
	}
	if v, _ := fixtureValue(fx, "settings"); v == "full" {
		s, _ := l.m.GetSettings(ctx)
		st := s.GetSettings()
		if st.GetProxy().GetMode() != downexv1.ProxyMode_PROXY_MODE_MANUAL || !st.GetProxy().GetHasPassword() || len(st.GetHostRules()) != 3 || len(st.GetSiteLogins()) != 2 {
			t.Fatal(st)
		}
	}
	if v, _ := fixtureValue(fx, "tools"); v == "ready" && !l.tools.Status().GetReady() {
		t.Fatal("tools not ready")
	}
	if fx.Confirm {
		checkConfirm(t, fx, l)
	}
}

func checkConfirm(t *testing.T, fx scene.FixtureState, l *loaded) {
	ctx := context.Background()
	aw := l.downloads.Awaiting()
	want := 1
	if n, ok := fixtureValue(fx, "waiting"); ok {
		want, _ = strconv.Atoi(n)
	}
	if len(aw) != want {
		t.Fatalf("%d awaiting confirmation, want %d", len(aw), want)
	}
	first := aw[0]
	kind, _ := fixtureValue(fx, "confirm")
	urlKind, _ := fixtureValue(fx, "url")
	switch kind {
	case "file":
		if first.GetFilename() != "budget-plan.xlsx" {
			t.Fatal(first.GetFilename())
		}
		if urlKind == "long" && len(first.GetUrl()) < 550 {
			t.Fatalf("address of %d characters", len(first.GetUrl()))
		}
	case "site":
		info := first.GetMedia()
		q := 6
		if v, ok := fixtureValue(fx, "q"); ok {
			q, _ = strconv.Atoi(v)
		}
		audio := 2
		if v, ok := fixtureValue(fx, "audio"); ok {
			audio, _ = strconv.Atoi(v)
		}
		if !media.IsSite(info) || len(media.VideoTracks(info.GetTracks(), true, "")) != q || len(media.AudioTracks(info.GetTracks(), true)) != audio {
			t.Fatalf("site offer: %d videos, %d audio", len(media.VideoTracks(info.GetTracks(), true, "")), len(media.AudioTracks(info.GetTracks(), true)))
		}
		key, ok := fixtureValue(fx, "title")
		if !ok {
			key = "plain"
		}
		if info.GetTitle() != scene.FixtureTitles[key] {
			t.Fatal(info.GetTitle())
		}
		if urlKind == "long" && !strings.Contains(first.GetUrl(), "&list=") {
			t.Fatal(first.GetUrl())
		}
	case "hls", "dash", "muxed", "audio":
		info := first.GetMedia()
		if first.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA || media.IsSite(info) {
			t.Fatal(first)
		}
		wantFormat := map[string]string{"hls": "hls", "dash": "dash", "muxed": "hls", "audio": "hls"}[kind]
		if info.GetFormat() != wantFormat || kind == "audio" && info.GetSelectedVideo() != "" || kind == "muxed" && info.GetSelectedAudio() != "" {
			t.Fatal(info)
		}
	}
	// The one the dialog shows is the only scene download waiting unless the fixture is a file.
	if kind != "file" && byName(l.downloads.Awaiting(), "budget-plan.xlsx") != nil {
		t.Fatal("the scene's own still waits")
	}
	_, err := l.m.Confirm(ctx, &downexv1.ConfirmRequest{Id: first.GetId(), Start: true})
	if _, refused := fixtureValue(fx, "fail"); refused || strings.Contains(","+fx.Fixture+",", ",fail,") {
		if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "disk_full") {
			t.Fatalf("not refused: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}

// checkFindMedia probes the address as the Add dialog does, giving the simulation the 16 s
// the script waits.
func checkFindMedia(t *testing.T, l *loaded, s scene.Step) {
	ctx := context.Background()
	req := &downexv1.ProbeMediaRequest{ManifestUrl: s.Text}
	if !media.LooksLikeManifest(s.Text) {
		req = &downexv1.ProbeMediaRequest{PageUrl: s.Text, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE}
	}
	var err error
	for range 40 {
		if _, err = l.m.ProbeMedia(ctx, req); err == nil {
			break
		}
		code := state.ErrorOf(err)
		switch {
		case s.Selector == ".waiting" && strings.HasPrefix(code.Message, "tools_installing:"):
			return
		case s.Selector == ".getting" && strings.HasPrefix(code.Message, "site_resolving:"):
			return
		case s.Selector == ".failures" && !strings.HasPrefix(code.Message, "tools_installing:") && !strings.HasPrefix(code.Message, "site_resolving:"):
			return
		}
		l.m.Tick(400)
	}
	if s.Selector != ".found-mark" || err != nil {
		t.Fatalf("find %s: until %s, got %v", s.Text, s.Selector, err)
	}
}

func checkProgress(t *testing.T, fx scene.FixtureState, l *loaded) {
	p := l.progress
	d := p.Download()
	if !p.Ready() || d == nil || p.Missing() || p.Removed() || p.LoadError() != "" || p.Settings() == nil {
		t.Fatalf("ready %v, %v, %q", p.Ready(), d, p.LoadError())
	}
	switch fx.Progress {
	case 1:
		if d.GetState() != state.StateDownloading || d.GetConnections() != 16 || len(d.GetConnectionInfo()) != 16 {
			t.Fatal(d.GetState(), d.GetConnections(), len(d.GetConnectionInfo()))
		}
	case 2:
		if d.GetState() != state.StateDownloading || !media.IsSiteDownload(d) {
			t.Fatal(d)
		}
	case 5:
		if d.GetState() != state.StatePaused || d.GetFilename() != "city-timelapse-4k.mp4" {
			t.Fatal(d)
		}
	case 8:
		if d.GetState() != state.StateFailed || d.GetErrorCode() != "http_status" || !strings.Contains(d.GetError(), "HTTP 503") {
			t.Fatal(d)
		}
	case 9:
		if d.GetState() != state.StateCompleted || !media.IsMedia(d) || d.GetFilename() != "keynote-2026.mp4" {
			t.Fatal(d)
		}
	case 11:
		if d.GetState() != state.StateCompleted || media.IsMedia(d) || categories.ExtensionOf(d.GetFilename()) != "mp4" {
			t.Fatal(d)
		}
	default:
		t.Fatalf("no check for download %d", fx.Progress)
	}
	if v := l.m.Startup(); v.Progress != fx.Progress {
		t.Fatal(v)
	}
}
