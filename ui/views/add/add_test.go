package add

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/media"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// The ported cases of ui/frontend/src/lib/components/AddDialog.test.ts, by their names.

type addRig struct {
	h       *viewstest.Harness
	a       *Add
	submit  calls[[]*downexv1.AddRequest]
	probe   calls[*downexv1.ProbeMediaRequest]
	media   calls[*downexv1.AddMediaRequest]
	picks   calls[string]
	closed  int
	setting int
}

type rigOptions struct {
	result      func(reqs []*downexv1.AddRequest) []state.AddOutcome
	probe       func(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error)
	submitMedia func(req *downexv1.AddMediaRequest) *downexv1.Download
	filesOnly   bool
	req         views.AddRequest
	queues      []*downexv1.Queue
	defQueue    int64
	free        func(dir string) (int64, error)
	tools       *downexv1.ToolsStatus
	picked      string
}

// setup is AddDialog.test.ts's setup() (files only, unless probe is given) and its other
// setups.
func setupAdd(t *testing.T, ro rigOptions) *addRig {
	t.Helper()
	h := newHarness(t)
	r := &addRig{h: h}
	picked := ro.picked
	if picked == "" {
		picked = "/picked/folder"
	}
	o := Options{
		Defaults: &Defaults{Dir: "/home/you/Downloads", MaxConnections: 8},
		PickFolder: func(_ context.Context, initial string) (string, error) {
			r.picks.add(initial)
			return picked, nil
		},
		Submit: func(_ context.Context, reqs []*downexv1.AddRequest) []state.AddOutcome {
			r.submit.add(reqs)
			if ro.result != nil {
				return ro.result(reqs)
			}
			return okOutcomes(reqs)
		},
		FilesOnly:      ro.filesOnly,
		OnClose:        func() { r.closed++ },
		OnOpenSettings: func() { r.setting++ },
		Queues:         ro.queues,
		DefaultQueue:   ro.defQueue,
		Go:             asyncGo,
		RetryClock:     retryClock(h),
		NoFreeSpace:    ro.free == nil,
		FreeSpace:      ro.free,
	}
	if o.Queues == nil {
		o.Queues = []*downexv1.Queue{}
	}
	if ro.tools != nil {
		o.Tools = func() *downexv1.ToolsStatus { return ro.tools }
	}
	if !ro.filesOnly {
		probe := ro.probe
		if probe == nil {
			probe = func(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
				return formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.ManifestUrl = req.GetManifestUrl() }), nil
			}
		}
		o.ProbeMedia = func(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			r.probe.add(req)
			return probe(ctx, req)
		}
		o.SubmitMedia = func(_ context.Context, req *downexv1.AddMediaRequest) (*downexv1.Download, error) {
			r.media.add(req)
			if ro.submitMedia != nil {
				return ro.submitMedia(req), nil
			}
			return formattest.MediaDownload(1, nil), nil
		}
	}
	r.a = New(h.App, ro.req, o)
	h.App.Dialogs.Show(r.a)
	h.Settle()
	return r
}

func (r *addRig) settle(t *testing.T, what string, cond func() bool) { waitUntil(t, r.h, what, cond) }

func (r *addRig) typeURLs(t *testing.T, s string)   { typeIn(t, r.h, r.a.urlsArea, s) }
func (r *addRig) typeStream(t *testing.T, s string) { typeIn(t, r.h, r.a.stream, s) }
func (r *addRig) primary()                          { press(r.h, r.a.submit) }
func (r *addRig) openMore()                         { press(r.h, r.a.moreToggle) }
func (r *addRig) toStream()                         { r.a.seg.SetValue(modeStream); r.a.setMode(modeStream); r.h.Settle() }
func (r *addRig) qualities() *qualitySection        { return r.a.Picker.QualitySection() }
func (r *addRig) checked() string                   { return r.qualities().Checked().ID() }
func (r *addRig) chooseQuality(id string)           { press(r.h, r.qualities().Card(id)) }
func (r *addRig) audio() *audioPart {
	if r.a.Picker == nil || len(r.a.Picker.audio) == 0 {
		return nil
	}
	return r.a.Picker.audio[0]
}

func (r *addRig) cardLabels() [][2]string {
	var out [][2]string
	for _, c := range r.qualities().Cards() {
		out = append(out, [2]string{c.ID(), c.Semantics.Name})
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func TestAddDialog(t *testing.T) {
	t.Run("is a labelled modal dialog that focuses the URL field", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		card := r.a.Card()
		eq(t, "name", card.Semantics.Name, "Add downloads")
		if card.Semantics.States&kit.NodeModal == 0 {
			t.Error("not modal")
		}
		if !isFocused(r.h, r.a.urlsArea) {
			t.Errorf("focus on %T", focused(r.h))
		}
		eq(t, "connections", r.a.conns.Text(), "8")
	})
	t.Run("requires a URL and does not submit an empty form", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		r.primary()
		eq(t, "error", r.a.urlsErr.Text(), "Enter at least one URL")
		if r.a.urlsErr.Style.Hidden || !strings.Contains(r.a.urlsArea.Semantics.Description, "Enter at least one URL") {
			t.Error("error not shown or not describing the field")
		}
		if !isFocused(r.h, r.a.urlsArea) {
			t.Error("the URL field lost the focus")
		}
		eq(t, "submits", r.submit.n(), 0)
	})
	t.Run("rejects non-http URLs and out-of-range connections", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		r.typeURLs(t, "ftp://files.example.com/a.zip")
		typeIn(t, r.h, r.a.conns, "40")
		r.primary()
		if !strings.Contains(r.a.urlsErr.Text(), "must start with http") {
			t.Errorf("urls error %q", r.a.urlsErr.Text())
		}
		if !strings.Contains(r.a.connsErr.Text(), "Connections must be from 1 to 32") {
			t.Errorf("connections error %q", r.a.connsErr.Text())
		}
		eq(t, "submits", r.submit.n(), 0)
		// errors clear live once the input is fixed
		r.typeURLs(t, "https://files.example.com/a.zip")
		if !r.a.urlsErr.Style.Hidden {
			t.Error("the error stayed")
		}
	})
	t.Run("submits one request per URL with the chosen options and closes", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		r.typeURLs(t, "https://files.example.com/a.iso\nhttps://files.example.com/b.iso\n")
		r.openMore()
		if !r.a.fileName.Disabled {
			t.Error("file name enabled for two URLs")
		}
		typeIn(t, r.h, r.a.conns, "4")
		typeIn(t, r.h, r.a.speed, "2")
		choose(t, r.h, r.a.unit, "KB/s")
		r.h.Click(r.a.pausedFiles.Box.Base())
		eq(t, "footer", r.a.FooterSummary(), "2 links · 4 connections")
		eq(t, "button", r.a.SubmitLabel(), "Add 2 downloads")
		r.primary()
		r.settle(t, "close", func() bool { return r.closed == 1 })
		eq(t, "submits", r.submit.n(), 1)
		want := []*downexv1.AddRequest{
			{Url: "https://files.example.com/a.iso", MaxConnections: 4, SpeedLimit: 2000, StartPaused: true, Source: downexv1.AddSource_ADD_SOURCE_UI},
			{Url: "https://files.example.com/b.iso", MaxConnections: 4, SpeedLimit: 2000, StartPaused: true, Source: downexv1.AddSource_ADD_SOURCE_UI},
		}
		got := r.submit.last()
		if len(got) != 2 {
			t.Fatalf("requests %v", got)
		}
		for i := range want {
			if got[i].String() != want[i].String() {
				t.Errorf("request %d = %v, want %v", i, got[i], want[i])
			}
		}
	})
	t.Run("keeps the dialog open with only the URLs that failed", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true, result: func(reqs []*downexv1.AddRequest) []state.AddOutcome {
			out := okOutcomes(reqs)
			for i, q := range reqs {
				if strings.HasSuffix(q.GetUrl(), "bad.zip") {
					out[i] = state.AddOutcome{URL: q.GetUrl(), Err: &host.CallError{Code: connect.CodeInvalidArgument, Message: "url is not allowed"}}
				}
			}
			return out
		}})
		r.typeURLs(t, "https://a.example/good.zip https://a.example/bad.zip")
		r.h.Window.Focus(r.a.urlsArea)
		key(r.h, kit.KeyEnter, kit.ModCtrl)
		r.settle(t, "the failures", func() bool { return strings.Contains(r.a.FailureText(), "url is not allowed") })
		eq(t, "closed", r.closed, 0)
		eq(t, "urls", r.a.urlsArea.Text(), "https://a.example/bad.zip")
		eq(t, "alert", r.a.failBox.Semantics.Role, kit.RoleAlert)
	})
	t.Run("fills the folder from the picker and closes on Escape", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		press(r.h, r.a.change)
		r.settle(t, "the folder", func() bool { return r.a.folder.Text() == "/picked/folder" })
		eq(t, "picker opened at", r.picks.all(), []string{"/home/you/Downloads"})
		eq(t, "name", r.a.folder.Semantics.Name, "Save to folder")
		eq(t, "change name", r.a.change.Semantics.Name, "Change folder")
		key(r.h, kit.KeyEscape, 0)
		r.settle(t, "close", func() bool { return r.closed == 1 })
	})
	t.Run("keeps Tab focus inside the dialog", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		r.h.Window.Focus(r.a.CloseButton())
		key(r.h, kit.KeyTab, kit.ModShift)
		if !isFocused(r.h, r.a.submit) {
			t.Errorf("Shift+Tab from Close went to %T", focused(r.h))
		}
		eq(t, "submit", r.a.SubmitLabel(), "Add download")
		key(r.h, kit.KeyTab, 0)
		if !isFocused(r.h, r.a.CloseButton()) {
			t.Errorf("Tab from the last went to %T", focused(r.h))
		}
	})
}

const manifest = "https://media.example.org/talk/master.m3u8"

func TestAddDialogStreams(t *testing.T) {
	t.Run("lists the qualities first, then adds the stream with the chosen tracks", func(t *testing.T) {
		r := setupAdd(t, rigOptions{})
		r.toStream()
		eq(t, "name", r.a.Card().Semantics.Name, "Add video or stream")
		if !r.a.moreFiles.Style.Hidden || !r.a.filesField.Style.Hidden {
			t.Error("files fields shown")
		}
		r.typeStream(t, manifest)
		eq(t, "primary", r.a.SubmitLabel(), "Find qualities")
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		eq(t, "probe", r.probe.last().String(), (&downexv1.ProbeMediaRequest{ManifestUrl: manifest}).String())
		eq(t, "added", r.media.n(), 0)
		// the keyboard goes on with the choice: the chosen quality card has the focus
		if !isFocused(r.h, r.qualities().Checked()) {
			t.Errorf("focus on %T", focused(r.h))
		}
		eq(t, "checked", r.checked(), "v1")
		if r.a.foundMark.Style.Hidden {
			t.Error("no Found")
		}
		eq(t, "primary", r.a.SubmitLabel(), "Add stream")
		eq(t, "placeholder", r.a.streamName.InputOptions().Placeholder, "Keynote")

		r.chooseQuality("v0")
		eq(t, "checked", r.checked(), "v0")
		choose(t, r.h, r.audio().Select, "r1")
		r.openMore()
		typeIn(t, r.h, r.a.streamName, " talk ")
		r.h.Click(r.a.pausedStrm.Box.Base())
		r.primary()
		r.settle(t, "close", func() bool { return r.closed == 1 })
		want := &downexv1.AddMediaRequest{ManifestUrl: manifest, Filename: "talk", Source: downexv1.AddSource_ADD_SOURCE_UI, VideoTrack: "v0", AudioTrack: "r1", StartPaused: true}
		eq(t, "add", r.media.last().String(), want.String())
	})
	t.Run("adds a stream with the daemon's audio-only pick", func(t *testing.T) {
		r := setupAdd(t, rigOptions{req: views.AddRequest{URLs: manifest}, probe: func(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			return formattest.MediaInfo(func(m *downexv1.MediaInfo) {
				m.ManifestUrl, m.SelectedVideo, m.SelectedAudio = req.GetManifestUrl(), "", "r1"
			}), nil
		}})
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		// the audio-only pick is the last card
		eq(t, "checked", r.checked(), "")
		cards := r.qualities().Cards()
		eq(t, "its name", r.qualities().Checked().Semantics.Name, "No video (audio only)")
		if cards[len(cards)-1] != r.qualities().Checked() {
			t.Error("not the last card")
		}
		eq(t, "audio", r.audio().Select.Value(), "r1")
		r.primary()
		r.settle(t, "close", func() bool { return r.closed == 1 })
		eq(t, "tracks", [2]string{r.media.last().GetVideoTrack(), r.media.last().GetAudioTrack()}, [2]string{"", "r1"})
	})
	t.Run("lists the qualities again when the address changes", func(t *testing.T) {
		r := setupAdd(t, rigOptions{req: views.AddRequest{URLs: manifest}})
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		r.typeStream(t, "https://media.example.org/b.mpd")
		if r.a.Picker != nil || !r.a.qualityArea.Style.Hidden {
			t.Error("the old qualities stayed")
		}
		if !r.a.foundMark.Style.Hidden {
			t.Error("Found stayed")
		}
		eq(t, "primary", r.a.SubmitLabel(), "Find qualities")
		r.primary()
		r.settle(t, "second probe", func() bool { return r.probe.n() == 2 })
	})
	t.Run("explains why an encrypted or live stream cannot be added", func(t *testing.T) {
		r := setupAdd(t, rigOptions{req: views.AddRequest{URLs: manifest}, probe: func(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			if strings.Contains(req.GetManifestUrl(), "live") {
				return nil, apiError(connect.CodeInvalidArgument, "live: the presentation is live")
			}
			return nil, apiError(connect.CodeInvalidArgument, "encrypted: DRM")
		}})
		r.primary()
		r.settle(t, "the refusal", func() bool { return strings.Contains(r.a.FailureText(), media.RefusalText(media.Encrypted)) })
		r.typeStream(t, "https://media.example.org/live.m3u8")
		r.primary()
		r.settle(t, "the refusal", func() bool { return strings.Contains(r.a.FailureText(), media.RefusalText(media.Live)) })
		eq(t, "probes", r.probe.n(), 2)
		eq(t, "added", r.media.n(), 0)
		eq(t, "closed", r.closed, 0)
		if r.a.Picker != nil {
			t.Error("qualities shown")
		}
	})
	t.Run("checks the address before asking the daemon", func(t *testing.T) {
		r := setupAdd(t, rigOptions{})
		r.toStream()
		r.primary()
		if !strings.Contains(r.a.streamErr.Text(), "Enter the address of a video page") {
			t.Errorf("error %q", r.a.streamErr.Text())
		}
		if !r.a.stream.InputOptions().Invalid {
			t.Error("not invalid")
		}
		if !isFocused(r.h, r.a.stream) {
			t.Error("not focused")
		}
		r.typeStream(t, "ftp://media.example.org/a.m3u8")
		if !strings.Contains(r.a.streamErr.Text(), "must start with http") {
			t.Errorf("error %q", r.a.streamErr.Text())
		}
		eq(t, "probes", r.probe.n(), 0)
	})
	t.Run("opens in stream mode for a manifest address, and suggests it for one typed as a file", func(t *testing.T) {
		r := setupAdd(t, rigOptions{req: views.AddRequest{URLs: manifest}})
		eq(t, "address", r.a.stream.Text(), manifest)
		if !isFocused(r.h, r.a.stream) {
			t.Error("the address is not focused")
		}
		eq(t, "mode", r.a.seg.Value, modeStream)
		r.a.setMode(modeFiles)
		r.h.Settle()
		eq(t, "urls", r.a.urlsArea.Text(), manifest)
		link := r.a.suggest.Children()[len(r.a.suggest.Children())-1].Widget().(*flow).Link()
		eq(t, "link", link.Semantics.Name, "Download it as a video")
		press(r.h, link)
		eq(t, "name", r.a.Card().Semantics.Name, "Add video or stream")
	})
	t.Run("adds files only without the stream functions", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		if r.a.seg != nil {
			t.Error("a mode control")
		}
	})
}

func TestAddDialogQueues(t *testing.T) {
	queues := []*downexv1.Queue{
		statetest.Queue(1, nil),
		statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" }),
		statetest.Queue(3, func(q *downexv1.Queue) { q.Name, q.Running = "Work", true }),
	}
	t.Run("adds into the chosen queue; the main queue is the default", func(t *testing.T) {
		r := setupAdd(t, rigOptions{queues: queues})
		eq(t, "queue", r.a.queueFiles.sel.Value(), "1")
		r.typeURLs(t, "https://example.com/a.zip")
		r.primary()
		r.settle(t, "submit", func() bool { return r.submit.n() == 1 })
		eq(t, "queue id", r.submit.last()[0].GetQueueId(), int64(0))
	})
	t.Run("proposes the queue shown in the list, and says when it is stopped", func(t *testing.T) {
		r := setupAdd(t, rigOptions{queues: queues, defQueue: 2})
		eq(t, "queue", r.a.queueFiles.sel.Value(), "2")
		// More options sums up the queue while it is closed
		if r.a.moreToggle.Open() {
			t.Error("open")
		}
		if !strings.Contains(r.a.moreToggle.Summary(), "· Night ·") {
			t.Errorf("summary %q", r.a.moreToggle.Summary())
		}
		r.openMore()
		eq(t, "values", optionValues(r.a.queueFiles.sel), []string{"1", "2", "3"})
		eq(t, "labels", optionLabels(r.a.queueFiles.sel), []string{"Main queue", "Night (stopped)", "Work"})
		if !strings.Contains(r.a.queueFiles.hint.Text(), "This queue is stopped") {
			t.Errorf("hint %q", r.a.queueFiles.hint.Text())
		}
		choose(t, r.h, r.a.queueFiles.sel, "3")
		if strings.Contains(r.a.queueFiles.hint.Text(), "This queue is stopped") {
			t.Error("still stopped")
		}
		r.typeURLs(t, "https://example.com/a.zip\nhttps://example.com/b.zip")
		eq(t, "button", r.a.SubmitLabel(), "Add 2 downloads")
		r.primary()
		r.settle(t, "submit", func() bool { return r.submit.n() == 1 })
		var ids []int64
		for _, q := range r.submit.last() {
			ids = append(ids, q.GetQueueId())
		}
		eq(t, "queue ids", ids, []int64{3, 3})
	})
	t.Run("adds a stream into the chosen queue", func(t *testing.T) {
		r := setupAdd(t, rigOptions{queues: queues, defQueue: 2})
		r.toStream()
		r.typeStream(t, "https://media.example.org/a/master.m3u8")
		eq(t, "primary", r.a.SubmitLabel(), "Find qualities")
		r.primary()
		r.settle(t, "Add stream", func() bool { return r.a.SubmitLabel() == "Add stream" })
		eq(t, "queue", r.a.queueStream.sel.Value(), "2")
		r.primary()
		r.settle(t, "added", func() bool { return r.media.n() == 1 })
		eq(t, "queue id", r.media.last().GetQueueId(), int64(2))
	})
}

func siteProbe(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	return formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.PageUrl = req.GetPageUrl() }), nil
}

func siteAdd(req *downexv1.AddMediaRequest) *downexv1.Download {
	return formattest.SiteDownload(1, func(d *downexv1.Download) { d.Url = req.GetPageUrl() })
}

func (r *addRig) enterPage(t *testing.T, url string) {
	r.toStream()
	r.typeStream(t, url)
}

func TestAddDialogVideoPages(t *testing.T) {
	t.Run("widens once the qualities are found, and gives them a column beside the fields", func(t *testing.T) {
		r := setupAdd(t, rigOptions{probe: siteProbe, submitMedia: siteAdd})
		r.enterPage(t, formattest.Page)
		s := r.h.Window.Environment().Scale
		eq(t, "width before", r.a.Card().Size().W, s.Rem(38))
		if r.a.Found() {
			t.Error("split before")
		}
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		// 56rem, as Confirm download: the qualities (start) beside the audio track, the folder and
		// More options (end), in the order Tab takes them
		eq(t, "width", r.a.Card().Size().W, min(s.Rem(56), r.h.Window.Size().W-32))
		order := r.a.form.Children()
		idx := func(e *kit.Element) int {
			for i, c := range order {
				if c == e {
					return i
				}
			}
			return -1
		}
		if !(idx(r.a.qualityArea) < idx(r.a.pair) && idx(r.a.pair) < idx(r.a.more)) {
			t.Error("the qualities, the pair and More options are out of order")
		}
		q, p := r.a.qualityArea.WindowBounds(), r.a.pair.WindowBounds()
		if !(q.Max.X <= p.Min.X && q.Min.Y == p.Min.Y) {
			t.Errorf("the qualities %v are not beside the fields %v", q, p)
		}
	})
	t.Run("lists every MP4 quality of a page with its size, and adds the chosen one as an .mp4", func(t *testing.T) {
		r := setupAdd(t, rigOptions{probe: siteProbe, submitMedia: siteAdd})
		r.enterPage(t, formattest.Page)
		eq(t, "hint", r.a.streamName.Semantics.Description, "The extension (.mp4) is added for you.")
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		eq(t, "probe", r.probe.last().String(), (&downexv1.ProbeMediaRequest{PageUrl: formattest.Page, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE}).String())
		// downexd's order (best first), one card per class, no "audio only"; each with the size of
		// the file it makes (the video with its audio), "~" where it is an estimate
		eq(t, "cards", r.cardLabels(), [][2]string{
			{"299", "1080p (Full HD) 60fps · H.264 · 482 MB"},
			{"136", "720p (HD) · H.264 · 126 MB"},
			{"397", "480p · AV1 · ~168 MB"},
			{"18", "360p · H.264 + AAC · ~45.0 MB"},
		})
		eq(t, "checked", r.checked(), "299")
		eq(t, "hint", QualityHint(len(r.a.Picker.Videos())), "One MP4 per resolution, best first")
		// H.264 mixed with AV1: the cards explain the difference
		eq(t, "note", r.qualities().note.Text(), "AV1 is smaller; H.264 plays everywhere.")
		audio := r.audio().Select
		eq(t, "audio values", optionValues(audio), []string{"140", "140-1"})
		eq(t, "audio labels", optionLabels(audio), []string{"English original", "Deutsch (German)"})
		// the trigger shows the name and what the track is ("English original · AAC 129 kbit/s")
		eq(t, "chosen label", audio.Options[0].Label, "English original")
		if !strings.Contains(audio.Options[0].Detail, "AAC 129 kbit/s") {
			t.Errorf("detail %q", audio.Options[0].Detail)
		}
		// the video card: the title, the site, and what is true of this download
		eq(t, "title", r.a.Video.Title.Text(), "A talk")
		if !strings.Contains(videoCardText(r.a.Video), "YouTube · youtube.com") {
			t.Errorf("card %q", videoCardText(r.a.Video))
		}
		eq(t, "facts", strings.Join(r.a.Video.Facts(), " "), "10:00 Up to 1080p Saved as MP4 No cookies sent")
		// the chosen quality's picture is the card's tooltip, never its name
		eq(t, "tooltip", r.a.Video.Tooltip, "1920×1080")
		eq(t, "footer", r.a.FooterSummary(), "1080p · H.264 + English audio · 482 MB")
		eq(t, "primary", r.a.SubmitLabel(), "Add video")
		eq(t, "placeholder", r.a.streamName.InputOptions().Placeholder, "A talk")

		// an estimated size says so; a muxed quality brings its own sound
		r.chooseQuality("397")
		eq(t, "footer", r.a.FooterSummary(), "480p · AV1 + English audio · ~168 MB")
		r.chooseQuality("18")
		eq(t, "audio part", r.audio().Mode(), "static")
		eq(t, "static", r.audio().StaticText(), "This video quality includes its sound.")
		// no declared size: 600 kbit/s × 10 min
		eq(t, "footer", r.a.FooterSummary(), "360p · H.264 with its sound · about 45.0 MB")
		r.primary()
		r.settle(t, "close", func() bool { return r.closed == 1 })
		want := &downexv1.AddMediaRequest{PageUrl: formattest.Page, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Source: downexv1.AddSource_ADD_SOURCE_UI, VideoTrack: "18"}
		eq(t, "add", r.media.last().String(), want.String())
	})
	t.Run("saves part of the video: a chapter fills the times, an error blocks the add", func(t *testing.T) {
		probe := func(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			return formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) {
				m.PageUrl = req.GetPageUrl()
				m.Chapters = []*downexv1.Chapter{{StartMs: 0, EndMs: 80_000, Title: "Intro"}, {StartMs: 80_000, EndMs: 165_000, Title: "Results"}}
			}), nil
		}
		r := setupAdd(t, rigOptions{probe: probe, submitMedia: siteAdd})
		r.enterPage(t, formattest.Page)
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		c := r.a.Clip
		if c == nil || c.Check.Checked() || c.Chapter == nil {
			t.Fatal("no part section, or it starts checked, or without its chapters")
		}
		eq(t, "footer", r.a.FooterSummary(), "1080p · H.264 + English audio · 482 MB")
		r.h.Click(c.Check.Box.Base())
		r.h.Settle()
		eq(t, "chapters", optionLabels(c.Chapter), []string{media.ClipCustom, "1. Intro (0:00–1:20)", "2. Results (1:20–2:45)"})
		c.chooseChapter("2")
		r.h.Settle()
		eq(t, "times", c.Form().From+" "+c.Form().To, "1:20 2:45")
		// the size × 90 s ÷ 600 s (the part and 5 s), an estimate
		eq(t, "footer", r.a.FooterSummary(), "1080p · H.264 + English audio · ~72.4 MB · part 1:20 – 2:45")
		// an edited time is custom again; a bad one blocks the add and takes the keyboard
		c.From.SetText("1:2")
		c.form.From = "1:2"
		c.custom()
		r.h.Settle()
		eq(t, "chapter", c.Chapter.Value(), "0")
		r.primary()
		r.h.Settle()
		eq(t, "error", c.ErrorText(), media.ClipBadTime)
		eq(t, "adds", r.media.n(), 0)
		if w := r.h.Window; w.Focused() != c.From.Base() {
			t.Error("the keyboard is not on From")
		}
		c.From.SetText("1:20")
		c.form.From = "1:20"
		c.custom()
		r.h.Settle()
		r.primary()
		r.settle(t, "close", func() bool { return r.closed == 1 })
		got := r.media.last()
		if got.GetClip().GetStartMs() != 80_000 || got.GetClip().GetEndMs() != 165_000 || got.GetClip().GetChapter() != "" || got.GetFilename() != "" {
			t.Errorf("add = %v", got)
		}
	})
	t.Run("asks again while yt-dlp is set up and the page is read, showing how far it is", func(t *testing.T) {
		answers := []any{
			textError("failed_precondition: tools_installing: downloading yt-dlp and Deno (34%)"),
			textError("failed_precondition: site_resolving: still getting the video info"),
			formattest.SiteMediaInfo(nil),
		}
		var mu syncMutex
		r := setupAdd(t, rigOptions{probe: func(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			mu.Lock()
			next := answers[0]
			answers = answers[1:]
			mu.Unlock()
			if err, ok := next.(error); ok {
				return nil, err
			}
			return next.(*downexv1.MediaInfo), nil
		}})
		r.enterPage(t, formattest.Page)
		r.primary()
		r.settle(t, "the install", func() bool { return r.a.waiting != nil })
		eq(t, "probes", r.probe.n(), 1)
		eq(t, "primary", r.a.SubmitLabel(), "Finding qualities…")
		eq(t, "bar", r.a.setupBar.Label, "Downloading yt-dlp and Deno")
		eq(t, "bar value", math.Round(r.a.setupFraction()*100), 34.0)
		// the percentage once: the bar's; the line names what downloads
		eq(t, "line", r.a.setupText.Text(), "Downloading yt-dlp and Deno…")
		if r.a.setupPart.Style.Hidden {
			t.Error("the first-use hint is hidden")
		}

		r.h.Advance(2 * time.Second) // every 2 s
		r.settle(t, "the second ask", func() bool { return r.probe.n() == 2 && r.a.waiting != nil && r.a.waiting.Kind == media.SiteResolving })
		if r.a.gettingRow.Style.Hidden {
			t.Error("no Getting video info…")
		}
		r.h.Advance(time.Second)
		eq(t, "elapsed", r.a.elapsed.Text(), "3 s")
		eq(t, "probes", r.probe.n(), 2)
		r.h.Advance(time.Second)
		r.settle(t, "the third ask", func() bool { return r.probe.n() == 3 && r.a.Info() != nil })
		if !r.a.waitBox.Style.Hidden {
			t.Error("still waiting")
		}
		eq(t, "alert", r.a.FailureText(), "")
	})
	t.Run("asks again at once after an answer that took downexd its whole wait budget", func(t *testing.T) {
		var r *addRig
		var n calls[int]
		r = setupAdd(t, rigOptions{probe: func(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			n.add(1)
			if n.n() == 1 {
				sleepOn(r.h, 25*time.Second)
				return nil, textError("failed_precondition: site_resolving: still getting the video info")
			}
			return formattest.SiteMediaInfo(nil), nil
		}})
		r.enterPage(t, formattest.Page)
		r.primary()
		r.settle(t, "the first ask", func() bool { return n.n() == 1 && r.h.StoreClock.Pending() > 0 })
		r.h.Advance(25 * time.Second)
		r.settle(t, "the qualities", func() bool { return r.a.Info() != nil })
		eq(t, "calls", n.n(), 2)
	})
	t.Run("shows the install progress the tools events report", func(t *testing.T) {
		installing := statetest.Tools(func(s *downexv1.ToolsStatus) {
			s.Ready = false
			for _, tl := range s.Tools {
				tl.State, tl.Version, tl.BytesDone, tl.BytesTotal = downexv1.ToolState_TOOL_STATE_INSTALLING, "", 5*MiB, 20*MiB
			}
		})
		r := setupAdd(t, rigOptions{tools: installing, probe: func(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			return nil, textError("failed_precondition: tools_installing: downloading yt-dlp and Deno (10%)")
		}})
		r.enterPage(t, formattest.Page)
		r.primary()
		r.settle(t, "the install", func() bool { return r.a.waiting != nil })
		eq(t, "bar value", math.Round(r.a.setupFraction()*100), 25.0)
		eq(t, "line", r.a.setupText.Text(), "Downloading yt-dlp and Deno…")
	})
	t.Run("stops asking on Cancel, and stays open to try again", func(t *testing.T) {
		r := setupAdd(t, rigOptions{probe: func(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			return nil, textError("failed_precondition: site_resolving: still getting the video info")
		}})
		r.enterPage(t, formattest.Page)
		r.primary()
		r.settle(t, "the wait", func() bool { return r.a.waiting != nil })
		r.h.Advance(2 * time.Second)
		r.settle(t, "the second ask", func() bool { return r.probe.n() == 2 })
		eq(t, "name", r.a.Card().Semantics.Name, "Add video or stream")
		if r.a.Busy() {
			t.Error("aria-busy: Escape would not close it")
		}
		press(r.h, r.a.cancel)
		r.h.Advance(10 * time.Second)
		eq(t, "probes", r.probe.n(), 2)
		eq(t, "closed", r.closed, 0)
		eq(t, "primary", r.a.SubmitLabel(), "Find qualities")
		if !r.a.waitBox.Style.Hidden {
			t.Error("Getting video info… stayed")
		}
		eq(t, "alert", r.a.FailureText(), "")
		// a changed address also ends the wait for the old one
		r.primary()
		r.settle(t, "the third ask", func() bool { return r.probe.n() == 3 })
		r.typeStream(t, formattest.Page+"x")
		r.h.Advance(10 * time.Second)
		eq(t, "probes", r.probe.n(), 3)
	})
	t.Run("explains why a page cannot be added, and offers the settings when yt-dlp is missing", func(t *testing.T) {
		answers := []error{
			apiError(connect.CodeFailedPrecondition, "tools_unavailable: yt-dlp and Deno are not set up: could not download yt-dlp (network)"),
			apiError(connect.CodeFailedPrecondition, "site_unavailable: this video is not available: Private video"),
			apiError(connect.CodeInvalidArgument, "media_unsupported: a playlist: open a single video"),
			apiError(connect.CodeInvalidArgument, "media_unsupported: no MP4 video on this page"),
			apiError(connect.CodeFailedPrecondition, "site_error: yt-dlp could not read this page: HTTP Error 500"),
			apiError(connect.CodeUnimplemented, "downex.v1.DaemonService.ProbeMedia is not implemented"),
		}
		var mu syncMutex
		r := setupAdd(t, rigOptions{probe: func(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			mu.Lock()
			defer mu.Unlock()
			e := answers[0]
			answers = answers[1:]
			return nil, e
		}})
		r.enterPage(t, formattest.Page)
		expect := func(text string) {
			t.Helper()
			r.primary()
			r.settle(t, text, func() bool { return strings.Contains(r.a.FailureText(), text) && r.a.busy == idle })
		}
		expect("Downex could not set up yt-dlp/Deno: could not download yt-dlp (network).")
		settings := findButton(r.a.failBox.Base(), "Open settings")
		if settings == nil {
			t.Fatal("no Open settings")
		}
		press(r.h, settings)
		eq(t, "settings", r.setting, 1)
		expect(media.SiteTexts["site_unavailable"] + " Private video.")
		if findButton(r.a.failBox.Base(), "Open settings") != nil {
			t.Error("Open settings stayed")
		}
		expect(media.SiteTexts["playlist"])
		expect(media.SiteTexts["media_unsupported"])
		expect("yt-dlp could not read this page: HTTP Error 500.")
		expect("Update Downex to download videos.")
		eq(t, "probes", r.probe.n(), 6)
		eq(t, "added", r.media.n(), 0)
	})
	t.Run("asks for a manifest once (no retries) and names the .mp4 extension", func(t *testing.T) {
		r := setupAdd(t, rigOptions{probe: func(context.Context, *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
			return nil, apiError(connect.CodeFailedPrecondition, "tools_installing: downloading yt-dlp and Deno (34%)")
		}})
		r.enterPage(t, "https://media.example.org/a/master.m3u8")
		eq(t, "hint", r.a.streamName.Semantics.Description, "The extension (.mp4) is added for you.")
		r.primary()
		r.settle(t, "the alert", func() bool { return r.a.FailureText() != "" })
		eq(t, "probes", r.probe.n(), 1)
		eq(t, "probe", r.probe.last().String(), (&downexv1.ProbeMediaRequest{ManifestUrl: "https://media.example.org/a/master.m3u8"}).String())
	})
}

func TestAddDialogStudioLayout(t *testing.T) {
	layout := func(t *testing.T, ro rigOptions) *addRig {
		ro.probe, ro.submitMedia = siteProbe, siteAdd
		return setupAdd(t, ro)
	}
	t.Run(`shows "Add" with the mode as a segmented control, and keeps the dialog names`, func(t *testing.T) {
		r := layout(t, rigOptions{})
		eq(t, "name", r.a.Card().Semantics.Name, "Add downloads")
		var values []string
		for _, o := range r.a.seg.Options {
			values = append(values, o.Value)
		}
		eq(t, "modes", values, []string{"files", "stream"})
		r.toStream()
		eq(t, "name", r.a.Card().Semantics.Name, "Add video or stream")
	})
	t.Run("opens in the mode it is asked for, with a page address in the stream field", func(t *testing.T) {
		r := layout(t, rigOptions{req: views.AddRequest{URLs: formattest.Page, Mode: views.AddStream}})
		eq(t, "name", r.a.Card().Semantics.Name, "Add video or stream")
		eq(t, "address", r.a.stream.Text(), formattest.Page)
		if !isFocused(r.h, r.a.stream) {
			t.Error("not focused")
		}
	})
	t.Run("closes only an open list on Escape, keeping the dialog and what was typed", func(t *testing.T) {
		r := setupAdd(t, rigOptions{filesOnly: true})
		r.typeURLs(t, "https://example.com/a.zip")
		r.openMore()
		unit := r.a.unit
		r.h.Window.Focus(unit)
		key(r.h, kit.KeyArrowDown, 0)
		if !unit.IsOpen() {
			t.Fatal("the list did not open")
		}
		key(r.h, kit.KeyEscape, 0)
		if unit.IsOpen() {
			t.Error("the list stayed open")
		}
		eq(t, "closed", r.closed, 0)
		eq(t, "urls", r.a.urlsArea.Text(), "https://example.com/a.zip")
		key(r.h, kit.KeyEscape, 0)
		r.settle(t, "close", func() bool { return r.closed == 1 })
	})
	t.Run("keeps More options closed with a summary of what it holds, and opens on demand", func(t *testing.T) {
		r := layout(t, rigOptions{})
		r.typeURLs(t, "https://example.com/a.zip")
		m := r.a.moreToggle
		if m.Open() {
			t.Error("open")
		}
		body := r.a.more.Children()[1]
		if !body.Style.Hidden {
			t.Error("body shown")
		}
		// the summary describes the button; it is not part of its name
		eq(t, "summary", m.Semantics.Description, "Name from the server · 8 connections · No speed limit · Start now")
		eq(t, "name", m.Semantics.Name, "More options")
		r.openMore()
		if !m.Open() || body.Style.Hidden {
			t.Error("did not open")
		}
		eq(t, "description", m.Semantics.Description, "")
		typeIn(t, r.h, r.a.fileName, "b.zip")
		typeIn(t, r.h, r.a.speed, "500")
		choose(t, r.h, r.a.unit, "KB/s")
		r.h.Click(r.a.pausedFiles.Box.Base())
		r.openMore()
		if !body.Style.Hidden {
			t.Error("did not close")
		}
		eq(t, "summary", m.Summary(), "b.zip · 8 connections · limit 500 KB/s · Add paused")
		eq(t, "footer", r.a.FooterSummary(), "1 link · 8 connections")
		// a unit typed with the number is read back in decimal units
		r.openMore()
		typeIn(t, r.h, r.a.speed, "2 MiB")
		r.openMore()
		eq(t, "summary", m.Summary(), "b.zip · 8 connections · limit 2.1 MB/s · Add paused")
	})
	t.Run("opens More options by itself when a field inside it is wrong, and focuses that field", func(t *testing.T) {
		r := layout(t, rigOptions{})
		r.typeURLs(t, "https://example.com/a.zip")
		typeIn(t, r.h, r.a.conns, "99")
		if r.a.moreToggle.Open() {
			t.Error("open before")
		}
		r.primary()
		if !r.a.moreToggle.Open() {
			t.Error("did not open")
		}
		if !isFocused(r.h, r.a.conns) {
			t.Errorf("focus on %T", focused(r.h))
		}
		if !strings.Contains(r.a.connsErr.Text(), "Connections must be from 1 to 32") {
			t.Errorf("error %q", r.a.connsErr.Text())
		}
		eq(t, "submits", r.submit.n(), 0)
	})
	t.Run("opens More options for a wrong video file name too", func(t *testing.T) {
		r := layout(t, rigOptions{req: views.AddRequest{URLs: formattest.Page, Mode: views.AddStream}})
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		// typed while More options was open, then closed again
		r.openMore()
		typeIn(t, r.h, r.a.streamName, "a/b")
		r.openMore()
		if r.a.moreToggle.Open() {
			t.Error("open")
		}
		r.primary()
		if !r.a.moreToggle.Open() {
			t.Error("did not open")
		}
		if !isFocused(r.h, r.a.streamName) {
			t.Errorf("focus on %T", focused(r.h))
		}
		eq(t, "added", r.media.n(), 0)
	})
	t.Run("sums up the video in More options: whether it starts now, then its file name (cut first), whole as its tooltip", func(t *testing.T) {
		r := layout(t, rigOptions{req: views.AddRequest{URLs: formattest.Page, Mode: views.AddStream}})
		eq(t, "summary", r.a.moreToggle.Summary(), "Start now · Name from the video title")
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		eq(t, "summary", r.a.moreToggle.Summary(), "Start now · A talk.mp4")
		eq(t, "tooltip", r.a.moreToggle.summary.Tooltip, "Start now · A talk.mp4")
	})
	t.Run(`says "No cookies sent" on the video card: the Add dialog never sends cookies`, func(t *testing.T) {
		r := layout(t, rigOptions{req: views.AddRequest{URLs: formattest.Page, Mode: views.AddStream}})
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		if !strings.Contains(strings.Join(r.a.Video.Facts(), "|"), "No cookies sent") {
			t.Error(r.a.Video.Facts())
		}
	})
	t.Run("shows the free space of the folder, and nothing when it cannot be read", func(t *testing.T) {
		var asked calls[string]
		r := layout(t, rigOptions{free: func(dir string) (int64, error) {
			asked.add(dir)
			if strings.HasPrefix(dir, "/mnt") {
				return 0, textError("unavailable: could not read the free space")
			}
			return 214 * 1024 * MiB, nil
		}})
		r.settle(t, "free space", func() bool { return !r.a.freeText.Style.Hidden && r.a.freeText.Text() == "230 GB free" })
		eq(t, "asked", asked.all(), []string{"/home/you/Downloads"})
		typeIn(t, r.h, r.a.folder, "/mnt/share")
		r.h.Advance(300 * time.Millisecond)
		r.settle(t, "asked again", func() bool { return asked.last() == "/mnt/share" })
		r.settle(t, "no free space", func() bool { return r.a.freeText.Style.Hidden })
		// a relative folder is not asked about
		n := asked.n()
		typeIn(t, r.h, r.a.folder, "relative")
		r.h.Advance(350 * time.Millisecond)
		eq(t, "asks", asked.n(), n)
	})
	t.Run("warns when the chosen video is larger than the free space", func(t *testing.T) {
		r := layout(t, rigOptions{req: views.AddRequest{URLs: formattest.Page, Mode: views.AddStream}, free: func(string) (int64, error) { return 200 * MiB, nil }})
		r.primary()
		r.settle(t, "qualities", func() bool { return r.a.Info() != nil })
		r.settle(t, "free", func() bool { return r.a.freeText.Text() == "210 MB free" })
		// 1080p is 460 MiB; 720p is 120 MiB
		if !r.a.tight() || r.a.freeText.Font().Weight != 600 || r.a.freeText.Tooltip != "Not enough free space for this video" {
			t.Error("not a warning")
		}
		r.chooseQuality("136")
		if r.a.tight() || r.a.freeText.Tooltip != "" {
			t.Error("still a warning")
		}
	})
}

// videoCardText is the card's text as textContent reads it.
func videoCardText(c *VideoCard) string {
	var b strings.Builder
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		switch w := e.Widget().(type) {
		case *views.Text:
			b.WriteString(w.Text())
			b.WriteString(" ")
		case *kit.Label:
			b.WriteString(w.Text())
			b.WriteString(" ")
		case *addressText:
			b.WriteString(w.Value())
			b.WriteString(" ")
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(c.Base())
	return strings.Join(strings.Fields(b.String()), " ")
}

// findButton is the button with that text inside e.
func findButton(e *kit.Element, text string) interface{ Press() } {
	var out interface{ Press() }
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden || out != nil {
			return
		}
		if b, ok := e.Widget().(interface {
			Press()
			Label() *kit.Label
		}); ok && b.Label().Text() == text {
			out = b
			return
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(e)
	return out
}
