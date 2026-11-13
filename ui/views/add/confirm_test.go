package add

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views/viewstest"
)

// The ported cases of ui/frontend/src/lib/components/ConfirmDialog.test.ts, by their names.

func confirmSettings() *downexv1.Settings {
	s := state.DefaultSettings()
	s.DefaultDir = "/home/you/Downloads"
	s.Categories = append(s.Categories, &downexv1.Category{Name: "Books", Extensions: []string{"epub"}, Dir: "/data/books"})
	return s
}

type confirmRig struct {
	h        *viewstest.Harness
	c        *Confirm
	confirms calls[*downexv1.ConfirmRequest]
	cancels  calls[int]
	moves    calls[int64]
	order    calls[string]
	picks    calls[string]
	closed   int
}

type confirmSetup struct {
	d         *downexv1.Download
	onconfirm func(*downexv1.ConfirmRequest) error
	onmove    func(int64) error
	requested *bool
	guard     time.Duration
	waiting   int
	queues    []*downexv1.Queue
	move      bool
}

func setupConfirm(t *testing.T, cs confirmSetup) *confirmRig {
	t.Helper()
	h := newHarness(t)
	r := &confirmRig{h: h}
	requested := true
	if cs.requested != nil {
		requested = *cs.requested
	}
	o := ConfirmOptions{
		Settings: confirmSettings(), Waiting: cs.waiting, Requested: requested, Guard: cs.guard,
		PickFolder: func(_ context.Context, initial string) (string, error) {
			r.picks.add(initial)
			return "/picked", nil
		},
		Confirm: func(_ context.Context, req *downexv1.ConfirmRequest) error {
			r.order.add("confirm")
			r.confirms.add(req)
			if cs.onconfirm != nil {
				return cs.onconfirm(req)
			}
			return nil
		},
		Cancel: func(context.Context) error { r.cancels.add(1); return nil },
		Queues: cs.queues, NoMove: !cs.move,
		OnClose: func() { r.closed++ },
		// the guard's clock is the test's (it moves only when the test says, as performance.now
		// mocked)
		Now: h.StoreClock.Now,
	}
	if o.Queues == nil {
		o.Queues = []*downexv1.Queue{}
	}
	if cs.move {
		o.Move = func(_ context.Context, q int64) error {
			r.order.add("move")
			r.moves.add(q)
			if cs.onmove != nil {
				return cs.onmove(q)
			}
			return nil
		}
	}
	r.c = NewConfirm(h.App, cs.d, o)
	h.App.Dialogs.Show(r.c)
	h.Settle()
	return r
}

// fileDownload is ConfirmDialog.test.ts's setup() download.
func fileDownload(edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.Download(4, func(d *downexv1.Download) {
		d.Url = "https://media.example.org/get?id=4"
		d.Filename = "talk.mp4"
		d.Dir = "/home/you/Downloads/Video"
		d.Size = -1
		d.ExpectedSize = 300 * MiB
		d.State = state.StateAwaiting
		d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		d.Category = "Video"
		if edit != nil {
			edit(d)
		}
	})
}

func (r *confirmRig) start()              { press(r.h, r.c.start) }
func (r *confirmRig) later()              { press(r.h, r.c.later) }
func (r *confirmRig) qs() *qualitySection { return r.c.Picker.QualitySection() }

func (r *confirmRig) waitConfirm(t *testing.T) {
	t.Helper()
	waitUntil(t, r.h, "the confirmation", func() bool { return r.confirms.n() > 0 && r.c.busy == "" })
}

func req(start bool, dir, name string) string {
	return (&downexv1.ConfirmRequest{Id: 0, Start: start, Dir: dir, Filename: name}).String()
}

// confirmed is the last confirmation without its id (the test's onconfirm has none).
func (r *confirmRig) confirmed() *downexv1.ConfirmRequest {
	c := r.confirms.last()
	if c == nil {
		return nil
	}
	cp := proto.Clone(c).(*downexv1.ConfirmRequest)
	cp.Id = 0
	return cp
}

func dialogText(c *Confirm) string {
	var b strings.Builder
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		switch w := e.Widget().(type) {
		case interface{ Text() string }:
			b.WriteString(w.Text())
			b.WriteString(" ")
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(c.Card())
	b.WriteString(c.Card().Semantics.Description)
	return strings.Join(strings.Fields(b.String()), " ")
}

func TestConfirmDialog(t *testing.T) {
	t.Run("shows the proposal and starts the download as proposed", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil), waiting: 3})
		eq(t, "name", r.c.Card().Semantics.Name, "Confirm download")
		text := dialogText(r.c)
		for _, want := range []string{"https://media.example.org/get?id=4", "Size: 315 MB", "2 more downloads are waiting"} {
			if !strings.Contains(text+" "+r.c.chip.Semantics.Name, want) {
				t.Errorf("no %q in %q", want, text)
			}
		}
		eq(t, "name", r.c.Name.Text(), "talk.mp4")
		if !isFocused(r.h, r.c.Name) {
			t.Errorf("focus on %T", focused(r.h))
		}
		eq(t, "folder", r.c.Folder.Text(), "/home/you/Downloads/Video")
		eq(t, "category", r.c.CategorySelect.Value(), "Video")
		eq(t, "categories", optionLabels(r.c.CategorySelect), []string{"Video", "Audio", "Archives", "Documents", "Programs", "Books", "None"})
		r.start()
		r.waitConfirm(t)
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
		// unchanged values are left to the daemon's proposal
		eq(t, "confirm", r.confirmed().String(), req(true, "", ""))
	})
	t.Run("proposes the folder of the chosen category and downloads later with the edits", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		choose(t, r.h, r.c.CategorySelect, "Books")
		eq(t, "folder", r.c.Folder.Text(), "/data/books")
		choose(t, r.h, r.c.CategorySelect, "Audio")
		eq(t, "folder", r.c.Folder.Text(), "/home/you/Downloads/Audio")
		choose(t, r.h, r.c.CategorySelect, "")
		eq(t, "category", r.c.CategorySelect.Value(), "")
		eq(t, "folder", r.c.Folder.Text(), "/home/you/Downloads")
		typeIn(t, r.h, r.c.Name, " keynote.mp4 ")
		r.later()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), req(false, "/home/you/Downloads", "keynote.mp4"))
	})
	t.Run("browses for a folder", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		press(r.h, r.c.browse)
		waitUntil(t, r.h, "the folder", func() bool { return r.c.Folder.Text() == "/picked" })
		eq(t, "picker", r.picks.all(), []string{"/home/you/Downloads/Video"})
		eq(t, "label", r.c.browse.Label().Text(), "Browse…")
	})
	t.Run("checks the name and folder before asking the daemon", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		typeIn(t, r.h, r.c.Name, "a/b.mp4")
		typeIn(t, r.h, r.c.Folder, "relative")
		r.start()
		if !r.c.Name.InputOptions().Invalid || !r.c.Folder.InputOptions().Invalid {
			t.Error("not both invalid")
		}
		if !isFocused(r.h, r.c.Name) {
			t.Errorf("focus on %T", focused(r.h))
		}
		eq(t, "confirms", r.confirms.n(), 0)
	})
	t.Run("keeps the dialog open with the daemon error", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil), onconfirm: func(*downexv1.ConfirmRequest) error {
			return apiError(connect.CodeInvalidArgument, "dir: not writable")
		}})
		r.start()
		r.waitConfirm(t)
		eq(t, "alert", r.c.failText.Text(), "dir: not writable")
		eq(t, "role", r.c.failBox.Semantics.Role, kit.RoleAlert)
		eq(t, "closed", r.closed, 0)
	})
	t.Run("cancels by removing; closing leaves it waiting", func(t *testing.T) {
		first := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		key(first.h, kit.KeyEscape, 0)
		waitUntil(t, first.h, "close", func() bool { return first.closed == 1 })
		eq(t, "cancels", first.cancels.n(), 0)
		eq(t, "confirms", first.confirms.n(), 0)

		second := setupConfirm(t, confirmSetup{d: fileDownload(func(d *downexv1.Download) { d.ExpectedSize, d.Category = 0, "Gone" })})
		if !strings.Contains(second.c.nameHint.Text(), "Size: unknown") {
			t.Errorf("hint %q", second.c.nameHint.Text())
		}
		eq(t, "category", second.c.CategorySelect.Value(), "") // a category that no longer exists
		press(second.h, second.c.cancel)
		waitUntil(t, second.h, "close", func() bool { return second.closed == 1 })
		eq(t, "cancels", second.cancels.n(), 1)
	})
	t.Run("leaves an unnamed download to the server instead of showing a name that is not used", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(func(d *downexv1.Download) { d.Filename = "" })})
		eq(t, "name", r.c.Name.Text(), "")
		eq(t, "placeholder", r.c.Name.InputOptions().Placeholder, "Named by the server")
		r.start()
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
		if r.c.Name.InputOptions().Invalid {
			t.Error("invalid")
		}
		eq(t, "confirm", r.confirmed().String(), req(true, "", ""))
	})
	t.Run(`never proposes the whole address as the name (path "/")`, func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(func(d *downexv1.Download) { d.Filename, d.Url = "", "https://example.com/?file=7" })})
		eq(t, "name", r.c.Name.Text(), "")
		r.later()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), req(false, "", ""))
		if strings.Contains(r.c.nameErr.Text(), "cannot contain") {
			t.Error("an error")
		}
	})
	t.Run("accepts a cleared name: the daemon keeps its proposal", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		typeIn(t, r.h, r.c.Name, "  ")
		eq(t, "placeholder", r.c.Name.InputOptions().Placeholder, "talk.mp4")
		r.start()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), req(true, "", ""))
		if r.c.nameErr.Text() == "Enter a file name" {
			t.Error("an error")
		}
	})
	t.Run("opened by itself, keeps typing out of the name and ignores activation at first", func(t *testing.T) {
		// the guard reads the window's clock, which the test moves itself
		no := false
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil), requested: &no, guard: 80 * time.Millisecond})
		// focus is on the dialog, not in the form whose Enter starts the download
		if !isFocused(r.h, r.c.Card()) {
			t.Errorf("focus on %T", focused(r.h))
		}
		eq(t, "name", r.c.Name.Text(), "talk.mp4")
		// Enter in the form, and clicks that land right after it appeared, do nothing
		r.c.Submit()
		r.start()
		r.later()
		press(r.h, r.c.cancel)
		r.h.Settle()
		eq(t, "confirms", r.confirms.n(), 0)
		eq(t, "cancels", r.cancels.n(), 0)
		r.h.Advance(100 * time.Millisecond)
		r.start()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), req(true, "", ""))
	})
	t.Run("asked for by the user, puts the cursor in the name and acts at once", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		if !isFocused(r.h, r.c.Name) {
			t.Errorf("focus on %T", focused(r.h))
		}
		key(r.h, kit.KeyEnter, 0) // Enter in the name submits the form
		r.waitConfirm(t)
		eq(t, "confirms", r.confirms.n(), 1)
	})
}

func mediaDownload(edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.MediaDownload(9, func(d *downexv1.Download) {
		d.Dir = "/home/you/Downloads/Video"
		d.State = state.StateAwaiting
		d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		if edit != nil {
			edit(d)
		}
	})
}

func (r *confirmRig) audioSelect() *audioPart {
	if r.c.Picker == nil || len(r.c.Picker.audio) == 0 {
		return nil
	}
	return r.c.Picker.audio[0]
}

func tracksReq(start bool, video, audio string) string {
	return (&downexv1.ConfirmRequest{Start: start, VideoTrack: video, AudioTrack: audio}).String()
}

func TestConfirmDialogMediaDownloads(t *testing.T) {
	t.Run("shows a quality picker with the daemon pick and sends the chosen tracks", func(t *testing.T) {
		d := mediaDownload(nil)
		r := setupConfirm(t, confirmSetup{d: d})
		if !strings.Contains(r.c.Card().Semantics.Description, "Choose its quality") {
			t.Error(r.c.Card().Semantics.Description)
		}
		eq(t, "group", r.qs().Semantics.Name, "Quality")
		// one card per class, best first, with its codec and the size of the file it makes
		var ids []string
		for _, c := range r.qs().Cards() {
			ids = append(ids, c.ID())
		}
		eq(t, "values", ids, []string{"v1", "v2", "v0"})
		eq(t, "first", r.qs().Cards()[0].Semantics.Name, "1080p (Full HD) · H.264 · ~385 MB")
		// the tooltip: the whole quality name, its picture and bitrate
		eq(t, "tooltip", r.qs().Card("v1").Tooltip, "1080p (Full HD) · 1920×1080 · 29.97 fps · 5.0 Mbit/s")
		eq(t, "checked", r.qs().Checked().ID(), "v1")
		a := r.audioSelect().Select
		eq(t, "audio values", optionValues(a), []string{"r0", "r1"})
		eq(t, "audio labels", optionLabels(a), []string{"English", "Deutsch (German)"})
		eq(t, "audio", a.Value(), "r0")
		// the size is estimated from the bitrate and length (5.128 Mbit/s × 10 min)
		eq(t, "size", r.c.nameHint.Text(), "Size: about 385 MB")
		// the video card: a stream, its address (no field of its own), its length, best class and
		// the MP4 it becomes
		if !strings.Contains(videoCardText(r.c.Video), "HLS stream ·") {
			t.Error(videoCardText(r.c.Video))
		}
		eq(t, "address", r.c.Video.Address.Value(), d.GetUrl())
		eq(t, "address name", r.c.Video.Address.Semantics.Name, "Address")
		if !r.c.Video.Address.ReadOnly() {
			t.Error("not read-only")
		}
		if r.c.address != nil {
			t.Error("an Address field")
		}
		eq(t, "facts", strings.Join(r.c.Video.Facts(), " "), "10:00 Up to 1080p Saved as MP4")

		choose(t, r.h, a, "r1")
		r.h.Click(r.qs().Card("v0").Base())
		r.start()
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
		eq(t, "confirm", r.confirmed().String(), tracksReq(true, "v0", "r1"))
	})
	t.Run("shows the size downexd measured of a bare media playlist, its picture and name", func(t *testing.T) {
		info := formattest.MediaInfo(func(m *downexv1.MediaInfo) {
			m.ManifestUrl = "https://moon.example/vd/abc/index-s1080p-v1-a1.m3u8"
			m.DurationMs = 3_907_000
			m.SelectedVideo, m.SelectedAudio = "v0", ""
			m.Tracks = []*downexv1.MediaTrack{{Id: "v0", Kind: "muxed", Width: 1920, Height: 1080, FrameRate: 25, Codecs: "avc1.640028,mp4a.40.2", Name: "1080p (Full HD)", Size: 2150 * MiB, SizeApprox: true}}
		})
		r := setupConfirm(t, confirmSetup{d: mediaDownload(func(d *downexv1.Download) { d.Media = info })})
		eq(t, "size", r.c.nameHint.Text(), "Size: about 2.3 GB")
		eq(t, "names", r.qs().Cards()[0].Semantics.Name, "1080p (Full HD) · H.264 + AAC · ~2.3 GB")
		eq(t, "cards", len(r.qs().Cards()), 1)
	})
	t.Run("hides the audio picker for a muxed video track", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: mediaDownload(nil)})
		r.h.Click(r.qs().Card("v2").Base())
		eq(t, "audio part", r.audioSelect().Mode(), "static")
		eq(t, "static", r.audioSelect().StaticText(), "This video quality includes its sound.")
		r.later()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), tracksReq(false, "v2", ""))
		// going back to a separate video track brings the audio picker back (a new dialog would be
		// closed by now: the pick is the dialog's state)
		r.c.Picker.Choose("v1")
		r.h.Settle()
		eq(t, "audio", r.audioSelect().Select.Value(), "r0")
	})
	t.Run("starts from a muxed default pick without an audio picker", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: mediaDownload(func(d *downexv1.Download) {
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "v2", "" })
		})})
		eq(t, "checked", r.qs().Checked().ID(), "v2")
		if r.audioSelect().Mode() == "select" {
			t.Error("an audio select")
		}
	})
	t.Run("keeps the daemon's audio-only pick of a stream that also lists videos", func(t *testing.T) {
		// e.g. DASH whose video Representations Downex cannot assemble (WebM, SegmentBase)
		r := setupConfirm(t, confirmSetup{d: mediaDownload(func(d *downexv1.Download) {
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "r1" })
		})})
		eq(t, "checked", r.qs().Checked().ID(), "")
		var ids []string
		for _, c := range r.qs().Cards() {
			ids = append(ids, c.ID())
		}
		eq(t, "values", ids, []string{"v1", "v2", "v0", ""})
		eq(t, "last name", r.qs().Cards()[3].Semantics.Name, "No video (audio only)")
		eq(t, "audio", r.audioSelect().Select.Value(), "r1")
		eq(t, "size", r.c.nameHint.Text(), "Size: about 7.2 MB")
		// a video quality and back to none
		r.h.Click(r.qs().Card("v1").Base())
		eq(t, "audio", r.audioSelect().Select.Value(), "r1")
		r.h.Click(r.qs().Card("").Base())
		eq(t, "checked", r.qs().Checked().ID(), "")
		eq(t, "audio", r.audioSelect().Select.Value(), "r1")
		r.start()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), tracksReq(true, "", "r1"))
	})
	t.Run("offers only audio for an audio-only stream", func(t *testing.T) {
		var tracks []*downexv1.MediaTrack
		for _, tr := range formattest.MediaInfo(nil).GetTracks() {
			if tr.GetKind() == "audio" {
				tracks = append(tracks, tr)
			}
		}
		r := setupConfirm(t, confirmSetup{d: mediaDownload(func(d *downexv1.Download) {
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.Tracks, m.SelectedVideo, m.SelectedAudio = tracks, "", "r1" })
		})})
		if r.c.Picker.QualitySection() != nil {
			t.Error("quality cards")
		}
		eq(t, "audio", r.audioSelect().Select.Value(), "r1")
		r.start()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), tracksReq(true, "", "r1"))
	})
	t.Run("keeps file downloads without a picker or tracks", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: formattest.Download(3, func(d *downexv1.Download) { d.State, d.Filename = state.StateAwaiting, "a.zip" })})
		if r.c.Picker != nil {
			t.Error("a picker")
		}
	})
}

func siteConfirm(edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.SiteDownload(11, func(d *downexv1.Download) {
		d.Dir = "/home/you/Downloads/Video"
		d.State = state.StateAwaiting
		d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		if edit != nil {
			edit(d)
		}
	})
}

func TestConfirmDialogVideoPages(t *testing.T) {
	t.Run("shows the page, its title and site, and every MP4 quality with its size", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
		if !strings.Contains(r.c.Card().Semantics.Description, "A video from the browser is waiting. Choose its quality") {
			t.Error(r.c.Card().Semantics.Description)
		}
		// the page's address is the card's, after the site's name (its host is not repeated): a
		// read-only field named "Page", reached with Tab, the whole address as its tooltip
		page := r.c.Video.Address
		eq(t, "page", page.Value(), formattest.Page)
		eq(t, "name", page.Semantics.Name, "Page")
		if !page.ReadOnly() {
			t.Error("not read-only")
		}
		eq(t, "tooltip", page.Tooltip, formattest.Page)
		eq(t, "tab", page.Focus, kit.FocusTab)
		if r.c.address != nil {
			t.Error("a Page field of its own")
		}
		eq(t, "title", r.c.Video.Title.Text(), "A talk")
		// the card names the site and what is true here: a hand-off may carry cookies
		text := videoCardText(r.c.Video)
		if !strings.Contains(text, "YouTube ·") || strings.Contains(strings.ReplaceAll(text, formattest.Page, ""), "youtube.com") {
			t.Errorf("card %q", text)
		}
		eq(t, "facts", strings.Join(r.c.Video.Facts(), " "), "10:00 Up to 1080p Saved as MP4")
		// each quality with the size of the file it makes (the video with its audio)
		var names []string
		for _, c := range r.qs().Cards() {
			names = append(names, c.Semantics.Name)
		}
		eq(t, "names", names, []string{"1080p (Full HD) 60fps · H.264 · 482 MB", "720p (HD) · H.264 · 126 MB", "480p · AV1 · ~168 MB", "360p · H.264 + AAC · ~45.0 MB"})
		eq(t, "checked", r.qs().Checked().ID(), "299")
		eq(t, "size", r.c.nameHint.Text(), "Size: 482 MB · saved as MP4")
		r.h.Click(r.qs().Card("397").Base())
		eq(t, "size", r.c.nameHint.Text(), "Size: ~168 MB · saved as MP4")
	})
	t.Run("confirms another quality under the same .mp4 name; a new name keeps the extension", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
		eq(t, "name", r.c.Name.Text(), "A talk.mp4")
		r.h.Click(r.qs().Card("136").Base())
		r.start()
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
		eq(t, "confirm", r.confirmed().String(), tracksReq(true, "136", "140"))
	})
	t.Run("adds .mp4 to a name typed without it", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
		typeIn(t, r.h, r.c.Name, " The talk ")
		r.later()
		r.waitConfirm(t)
		eq(t, "start", r.confirmed().GetStart(), false)
		eq(t, "name", r.confirmed().GetFilename(), "The talk.mp4")
	})
	t.Run("keeps a typed .MP4 name as it is", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
		typeIn(t, r.h, r.c.Name, "Talk.MP4")
		r.start()
		r.waitConfirm(t)
		eq(t, "name", r.confirmed().GetFilename(), "Talk.MP4")
	})
	t.Run("shows the one audio track instead of a choice", func(t *testing.T) {
		info := formattest.SiteMediaInfo(nil)
		var tracks []*downexv1.MediaTrack
		for _, tr := range info.GetTracks() {
			if tr.GetId() != "140-1" {
				tracks = append(tracks, tr)
			}
		}
		info.Tracks = tracks
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) { d.Media = info })})
		eq(t, "mode", r.audioSelect().Mode(), "static")
		// the one track as it is (the full label in its tooltip)
		eq(t, "text", r.audioSelect().StaticText(), "English original · AAC 129 kbit/s")
		eq(t, "title", r.audioSelect().StaticTitle(), "English original · AAC · 129 kbit/s · 10.5 MB")
	})
}

func TestConfirmDialogQueues(t *testing.T) {
	two := func(second *downexv1.Queue) []*downexv1.Queue {
		return []*downexv1.Queue{statetest.Queue(1, nil), second}
	}
	night := statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" })
	daytime := statetest.Queue(3, func(q *downexv1.Queue) { q.Name, q.Running = "Daytime", true })
	qd := func(edit func(d *downexv1.Download)) *downexv1.Download {
		return formattest.Download(4, func(d *downexv1.Download) {
			d.Filename, d.Dir, d.State = "talk.mp4", "/home/you/Downloads", state.StateAwaiting
			if edit != nil {
				edit(d)
			}
		})
	}
	t.Run("keeps a download for later paused in a running queue, as the hint says", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: qd(nil), queues: two(daytime), move: true})
		choose(t, r.h, r.c.QueueSelect, "3")
		if !strings.Contains(r.c.queueHint.Text(), "Download later keeps it paused") {
			t.Error(r.c.queueHint.Text())
		}
		r.later()
		r.waitConfirm(t)
		// a queue_id would queue it there, and a running queue would start it at once
		eq(t, "confirm", r.confirmed().String(), req(false, "", ""))
		eq(t, "moves", r.moves.all(), []int64{3})
		eq(t, "order", r.order.all(), []string{"move", "confirm"})
	})
	t.Run("puts a download for later into its own queue when that queue is stopped", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: qd(nil), queues: []*downexv1.Queue{statetest.Queue(1, func(q *downexv1.Queue) { q.Running = false }), night}, move: true})
		eq(t, "queue", r.c.QueueSelect.Value(), "1")
		if !strings.Contains(r.c.queueHint.Text(), "starts when the queue runs") {
			t.Error(r.c.queueHint.Text())
		}
		r.later()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), (&downexv1.ConfirmRequest{QueueId: 1}).String())
		eq(t, "moves", r.moves.n(), 0)
	})
	t.Run("starts a download in another running queue without moving it first", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: qd(nil), queues: two(daytime), move: true})
		choose(t, r.h, r.c.QueueSelect, "3")
		r.start()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), (&downexv1.ConfirmRequest{Start: true, QueueId: 3}).String())
		eq(t, "moves", r.moves.n(), 0)
	})
	t.Run("reports a move that fails and confirms nothing", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: qd(nil), queues: two(daytime), move: true, onmove: func(int64) error {
			return textError("not_found: queue 3 not found")
		}})
		choose(t, r.h, r.c.QueueSelect, "3")
		r.later()
		waitUntil(t, r.h, "the alert", func() bool { return strings.Contains(r.c.failText.Text(), "queue 3 not found") })
		eq(t, "confirms", r.confirms.n(), 0)
	})
	t.Run("puts a download for later into the chosen queue", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: qd(nil), queues: two(night), move: true})
		eq(t, "queue", r.c.QueueSelect.Value(), "1")
		eq(t, "labels", optionLabels(r.c.QueueSelect), []string{"Main queue", "Night (stopped)"})
		choose(t, r.h, r.c.QueueSelect, "2")
		if !strings.Contains(r.c.queueHint.Text(), "starts when the queue runs") {
			t.Error(r.c.queueHint.Text())
		}
		r.later()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), (&downexv1.ConfirmRequest{QueueId: 2}).String())
	})
	t.Run("keeps the queue it is in unless another is chosen", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: qd(func(d *downexv1.Download) { d.QueueId = 2 }), queues: two(night), move: true})
		eq(t, "queue", r.c.QueueSelect.Value(), "2")
		r.start()
		r.waitConfirm(t)
		eq(t, "confirm", r.confirmed().String(), req(true, "", ""))
	})
	t.Run("offers no queue without queues", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		if r.c.QueueSelect != nil {
			t.Error("a queue")
		}
	})
}

func TestConfirmDialogStudioLayout(t *testing.T) {
	t.Run("heads the dialog with the tile of its file kind", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		eq(t, "kind", tileOf(r.c).Kind(), "video")
		r2 := setupConfirm(t, confirmSetup{d: fileDownload(func(d *downexv1.Download) {
			d.Filename, d.Url = "setup-x64.exe", "https://downloads.example.com/setup-x64.exe"
		})})
		eq(t, "kind", tileOf(r2.c).Kind(), "programs")
	})
	t.Run("chooses a category from the keyboard, which proposes its folder", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		trig := r.c.CategorySelect
		r.h.Window.Focus(trig)
		key(r.h, kit.KeyArrowDown, 0)
		if !trig.IsOpen() {
			t.Fatal("did not open")
		}
		// Video → Audio
		key(r.h, kit.KeyArrowDown, 0)
		key(r.h, kit.KeyEnter, 0)
		if trig.IsOpen() {
			t.Error("still open")
		}
		eq(t, "value", trig.Value(), "Audio")
		eq(t, "folder", r.c.Folder.Text(), "/home/you/Downloads/Audio")
		// Escape closes the list without a change, and leaves the dialog open
		key(r.h, kit.KeyArrowDown, 0)
		key(r.h, kit.KeyEnd, 0)
		key(r.h, kit.KeyEscape, 0)
		eq(t, "value", trig.Value(), "Audio")
		if trig.IsOpen() {
			t.Error("still open")
		}
		eq(t, "closed", r.closed, 0)
		// closed, Escape is the dialog's again
		key(r.h, kit.KeyEscape, 0)
		waitUntil(t, r.h, "close", func() bool { return r.closed == 1 })
	})
	t.Run("moves between quality cards like one radio group", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
		for _, c := range r.qs().Cards() {
			if c.Disabled || c.Semantics.Role != kit.RoleRadioButton {
				t.Errorf("card %s", c.ID())
			}
		}
		// the chosen card is Mint, with its tick
		on := r.qs().Checked()
		if !on.On() || !on.HasTick() {
			t.Error("the chosen card's look")
		}
		// Space on another card chooses it
		r.h.Window.Focus(r.qs().Card("136"))
		key(r.h, kit.KeySpace, 0)
		eq(t, "checked", r.qs().Checked().ID(), "136")
		if on.On() {
			t.Error("the old card is still on")
		}
		r.start()
		r.waitConfirm(t)
		c := r.confirmed()
		eq(t, "tracks", [2]string{c.GetVideoTrack(), c.GetAudioTrack()}, [2]string{"136", "140"})
	})
}

func tileOf(c *Confirm) *fileTile {
	var out *fileTile
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if t, ok := e.Widget().(*fileTile); ok {
			out = t
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(c.Card())
	return out
}

// tabOrder is the form's controls in Tab order (the card's address, the qualities and the
// footer left out), named as assistive technology names them.
func tabOrder(c *Confirm) []string {
	var out []string
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		if e.Focus == kit.FocusTab && !e.Disabled {
			name := e.Semantics.Name
			if b, ok := e.Widget().(interface{ Label() *kit.Label }); ok && name == "" {
				name = b.Label().Text()
			}
			out = append(out, name)
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(c.fields)
	return out
}

func TestConfirmDialogFitRule(t *testing.T) {
	renderSite := func(t *testing.T, waiting int) *confirmRig {
		return setupConfirm(t, confirmSetup{d: siteConfirm(nil), waiting: waiting})
	}
	t.Run("widens a video to 56rem and gives its qualities a column before the fields, in Tab order", func(t *testing.T) {
		r := renderSite(t, 1)
		s := r.h.Window.Environment().Scale
		eq(t, "width", r.c.Card().Size().W, min(s.Rem(56), r.h.Window.Size().W-32))
		var parts []*kit.Element
		for _, k := range r.c.form.Children() {
			if !k.Style.Hidden {
				parts = append(parts, k)
			}
		}
		eq(t, "parts", parts, []*kit.Element{r.c.cardArea, r.c.qualityArea, r.c.fields})
		q, f := r.c.qualityArea.WindowBounds(), r.c.fields.WindowBounds()
		if !(q.Max.X <= f.Min.X) {
			t.Error("the qualities are not before the fields")
		}
		// Tab: the card's address, the qualities, the audio track, then the file name and the folder
		order := []kit.Widget{r.c.Video.Address, r.qs().Checked(), r.audioSelect().Select, r.c.Name, r.c.Folder}
		var at []int
		for _, w := range order {
			at = append(at, tabIndex(r.c.Card(), w.Base()))
		}
		for i := 1; i < len(at); i++ {
			if !(at[i-1] < at[i]) {
				t.Errorf("Tab order %v", at)
			}
		}
	})
	t.Run("keeps a file in one column at 44rem, with its address as a field", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		s := r.h.Window.Environment().Scale
		eq(t, "width", r.c.Card().Size().W, s.Rem(44))
		if r.c.qualities {
			t.Error("split")
		}
		if r.c.address == nil || r.c.address.Style.Hidden {
			t.Error("no Address field")
		}
	})
	t.Run("puts the description beside the icon, under the title, and the waiting count in the header", func(t *testing.T) {
		r := renderSite(t, 3)
		if !strings.Contains(r.c.Card().Semantics.Description, "A video from the browser is waiting.") {
			t.Error(r.c.Card().Semantics.Description)
		}
		eq(t, "chip", r.c.chip.Text(), "2 more waiting")
		eq(t, "chip title", r.c.chip.Tooltip, "2 more downloads are waiting for confirmation.")
		eq(t, "chip name", r.c.chip.Semantics.Name, "2 more downloads are waiting for confirmation.")
		if strings.Contains(dialogText(r.c), "waiting for confirmation") {
			t.Error("the body says it")
		}
		// the description sits under the title beside the icon: the header holds it
		if !(r.c.chip.WindowBounds().Max.Y <= r.c.Body.WindowBounds().Min.Y) {
			t.Error("the chip is not in the header")
		}
	})
	t.Run("says the category proposes the folder in the folder’s label row, still describing the category", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		eq(t, "description", r.c.CategorySelect.Semantics.Description, "Choosing a category proposes its folder.")
		row := r.c.categoryHint.Parent()
		if row.Parent() != r.c.dirField.Base() {
			t.Error("the hint is not in the folder's label row")
		}
	})
	t.Run("shows the duration once (a fact), not also on the poster", func(t *testing.T) {
		r := renderSite(t, 1)
		eq(t, "facts", strings.Join(r.c.Video.Facts(), " "), "10:00 Up to 1080p Saved as MP4")
		if p := r.c.Video.Children()[0]; len(p.Children()) != 0 {
			t.Error("the poster has content")
		}
	})
}

// tabIndex is where e comes in root's Tab order (-1: not in it).
func tabIndex(root, e *kit.Element) int {
	i, found := 0, -1
	var walk func(x *kit.Element)
	walk = func(x *kit.Element) {
		if x.Style.Hidden || found >= 0 {
			return
		}
		if x == e {
			found = i
		}
		if x.Focus != kit.FocusNone {
			i++
		}
		for _, k := range x.Children() {
			walk(k)
		}
	}
	walk(root)
	return found
}

func TestConfirmDialogRightToLeftTitle(t *testing.T) {
	t.Run("takes its direction from the title itself, so an Arabic title is right-aligned", func(t *testing.T) {
		title := "أطيب كباب في السليمانية_ أكلات شعبية وخبز التنور الحار"
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) {
			d.Id, d.Media = 12, formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.Title = title })
		})})
		el := r.c.Video.Title
		eq(t, "title", el.Text(), title)
		// dir="auto" on the block that holds the text
		eq(t, "direction", el.Options().Direction, kit.DirAuto)
		eq(t, "align", el.Options().Align, kit.TextAlignStart)
	})
}

func TestConfirmDialogReviewOfTheFitChange(t *testing.T) {
	queues := []*downexv1.Queue{statetest.Queue(1, nil), statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" })}
	t.Run("keeps an audio-only stream in one column at 44rem: it has no quality cards", func(t *testing.T) {
		var tracks []*downexv1.MediaTrack
		for _, tr := range formattest.MediaInfo(nil).GetTracks() {
			if tr.GetKind() == "audio" {
				tracks = append(tracks, tr)
			}
		}
		r := setupConfirm(t, confirmSetup{d: mediaDownload(func(d *downexv1.Download) {
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.Tracks, m.SelectedVideo, m.SelectedAudio = tracks, "", "r1" })
		})})
		s := r.h.Window.Environment().Scale
		eq(t, "width", r.c.Card().Size().W, s.Rem(44))
		if r.c.qualities || len(r.c.qualityArea.Children()) != 0 {
			t.Error("a quality column")
		}
		if r.audioSelect().Select == nil {
			t.Error("no audio select")
		}
	})
	t.Run("gives a video’s folder a row of its own after the category and the queue, in Tab order", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: siteConfirm(nil), queues: queues, move: true})
		eq(t, "tab order", tabOrder(r.c), []string{"Audio track", "File name", "Category", "Queue", "Save to folder", "Browse…"})
		// the folder is not in a row beside the category
		d, cat := r.c.dirField.WindowBounds(), r.c.CategorySelect.WindowBounds()
		if !(d.Min.Y >= cat.Max.Y) {
			t.Error("the folder is beside the category")
		}
		// the queue's hint is the row's, under both selects
		h := r.c.queueHint.WindowBounds()
		if !(h.Width() > cat.Width()*1.5) {
			t.Error("the queue's hint is not the row's")
		}
	})
	t.Run("shows the whole folder as its field’s tooltip", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: fileDownload(nil)})
		eq(t, "tooltip", r.c.Folder.Tooltip, "/home/you/Downloads/Video")
		typeIn(t, r.h, r.c.Folder, "/data/media/very/long/folder")
		eq(t, "tooltip", r.c.Folder.Tooltip, "/data/media/very/long/folder")
	})
	t.Run("keeps a file’s category beside its folder without queues, and puts both selects first with them", func(t *testing.T) {
		r := setupConfirm(t, confirmSetup{d: formattest.Download(4, func(d *downexv1.Download) {
			d.Filename, d.Dir, d.State = "a.zip", "/home/you/Downloads", state.StateAwaiting
		})})
		eq(t, "tab order", tabOrder(r.c), []string{"File name", "Category", "Save to folder", "Browse…"})
		d, cat := r.c.dirField.WindowBounds(), r.c.CategorySelect.WindowBounds()
		if !(d.Min.X >= cat.Max.X) {
			t.Error("the folder is not beside the category")
		}
	})
	t.Run("clamps a long file address to a few lines, the whole address as its tooltip, without a scroller of its own", func(t *testing.T) {
		url := "https://storage.example-cdn.net/uploads/budget-plan.xlsx?X-Amz-Signature=" + strings.Repeat("a3f9c27e", 60)
		r := setupConfirm(t, confirmSetup{d: formattest.Download(4, func(d *downexv1.Download) {
			d.Url, d.Filename, d.State = url, "budget-plan.xlsx", state.StateAwaiting
		})})
		eq(t, "text", r.c.url.Text(), url)
		eq(t, "tooltip", r.c.url.Tooltip, url)
		eq(t, "lines", r.c.url.label.Options().MaxLines, 3)
	})
	t.Run("has the whole title as the tooltip of the title (clamped to two lines), over the card’s picture", func(t *testing.T) {
		title := "How We Moved Forty Services From a Single VPS to a Staging and Production Setup With Dokploy, Traefik and Postgres Replicas"
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) {
			d.Id, d.Media = 12, formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.Title = title })
		})})
		eq(t, "tooltip", r.c.Video.Title.Tooltip, title)
		eq(t, "lines", r.c.Video.Title.Options().MaxLines, 2)
		eq(t, "card tooltip", r.c.Video.Tooltip, "1920×1080")
	})
	t.Run("shows a page address with a bidi control escaped, in the field and its tooltip", func(t *testing.T) {
		const rlo = "‮"
		url := "https://www.youtube.com/watch?v=Xb7mQ2kLp9w&t=" + rlo + "4pm.exe"
		r := setupConfirm(t, confirmSetup{d: siteConfirm(func(d *downexv1.Download) { d.Id, d.Url = 7, url })})
		v := r.c.Video.Address.Value()
		if strings.Contains(v, rlo) || !strings.Contains(v, "⟨U+202E⟩") {
			t.Errorf("value %q", v)
		}
		eq(t, "tooltip", r.c.Video.Address.Tooltip, v)
	})
}
