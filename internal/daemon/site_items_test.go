package daemon

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Pages with several videos (docs/SITE-MEDIA-SPEC.md §3.12): the item a request names
// reaches the resolver, the answer lists the items (with thumbnails only in probes), the
// record keeps its item for every run, and Confirm can take another item of the page.

func itemsSite() *fakeSite {
	fs := newFakeSite(2)
	fs.set(func(f *fakeSite) {
		f.probe.Items = []model.MediaItem{
			{ID: "itemA", Title: "Video by example", Duration: 12 * time.Second, Thumbnail: "https://img.example/a.jpg"},
			{ID: "itemB", Title: "Video by example", Duration: 7 * time.Second, Thumbnail: "https://img.example/b.jpg"},
			{ID: "itemC", Title: "The third one"},
		}
		f.probe.ItemsLeftOut = 2
		f.probe.Site = "Instagram"
	})
	return fs
}

func TestSiteItemsProbe(t *testing.T) {
	fs := itemsSite()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	mi, err := h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage, EntryId: "itemB"})
	if err != nil {
		t.Fatal(err)
	}
	if mi.GetEntryId() != "itemB" || mi.GetTitle() != "Video by example" || mi.GetItemsLeftOut() != 2 || len(mi.GetItems()) != 3 {
		t.Fatalf("probe = %v", mi)
	}
	if it := mi.GetItems()[1]; it.GetId() != "itemB" || it.GetDurationMs() != 7000 || it.GetThumbnail() != "https://img.example/b.jpg" {
		t.Errorf("item = %v", it)
	}
	if probes, _ := fs.calls(); len(probes) != 1 || probes[0].EntryID != "itemB" {
		t.Errorf("probes = %+v", probes)
	}
	// A page with one video has no items.
	plain := newFakeSite(1)
	h2 := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: plain, tools: newFakeTools()})
	if mi, err := h2.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage}); err != nil ||
		mi.GetEntryId() != "" || len(mi.GetItems()) != 0 || mi.GetItemsLeftOut() != 0 {
		t.Errorf("one video: %v, %v", mi, err)
	}
	// An item the page no longer has is site_unavailable.
	_, err = h.probeMedia(&downexv1.ProbeMediaRequest{Origin: originSite, PageUrl: testPage, EntryId: "gone"})
	if mediaCode(t, err) != CodeSiteUnavailable {
		t.Errorf("gone item: %v", err)
	}
	// Bad requests: an item for a manifest, an item id that is not text.
	for name, req := range map[string]*downexv1.ProbeMediaRequest{
		"manifest": {ManifestUrl: "https://media.example/master.m3u8", EntryId: "itemA"},
		"control":  {Origin: originSite, PageUrl: testPage, EntryId: "a\nb"},
	} {
		if _, err := h.probeMedia(req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
	if _, err := h.addMediaReq(&downexv1.AddMediaRequest{ManifestUrl: "https://media.example/master.m3u8", EntryId: "itemA"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("add of a manifest with an item: %v", err)
	}
}

func TestSiteItemsAddAndRun(t *testing.T) {
	fs := itemsSite()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	d := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, EntryId: "itemB", Source: sourceBrowser})
	// Two items share the title: the name adds the item's position.
	if d.GetFilename() != "Video by example 2.mp4" || d.GetMedia().GetEntryId() != "itemB" || len(d.GetMedia().GetItems()) != 3 {
		t.Fatalf("added = %v", d)
	}
	for _, it := range d.GetMedia().GetItems() {
		if it.GetThumbnail() != "" {
			t.Errorf("a recorded item keeps its thumbnail: %v", it)
		}
	}
	rec := storedState(t, h.st, d.GetId())
	if rec.Media.EntryID != "itemB" || len(rec.Media.Items) != 3 || rec.Media.ItemsLeftOut != 2 {
		t.Errorf("stored media = %+v", rec.Media)
	}
	if b, _ := json.Marshal(rec.Media); bytes.Contains(b, []byte("img.example")) {
		t.Errorf("a thumbnail is stored: %s", b)
	}
	// A title no other item has is the name as it is.
	d3 := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, EntryId: "itemC", StartPaused: true})
	if d3.GetFilename() != "The third one.mp4" {
		t.Errorf("unique title: %q", d3.GetFilename())
	}

	// Confirm takes another item: resolved again, its tracks and name replace the old ones.
	c, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, EntryId: "itemA", VideoTrack: "399"})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetMedia().GetEntryId() != "itemA" || c.GetFilename() != "Video by example 1.mp4" || c.GetMedia().GetSelectedVideo() != "399" {
		t.Fatalf("confirmed = %v", c)
	}
	probes, _ := fs.calls()
	if last := probes[len(probes)-1]; last.EntryID != "itemA" || last.PageURL != testPage {
		t.Errorf("Confirm's probe = %+v", last)
	}
	h.waitCompleted(d.GetId())
	_, runs := fs.calls()
	if len(runs) != 1 || runs[0].sel.EntryID != "itemA" || runs[0].sel.VideoTrack != "399" {
		t.Errorf("runs = %+v", runs)
	}

	// Confirm of an item the page no longer has fails and changes nothing.
	d4 := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, EntryId: "itemC", Source: sourceBrowser})
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d4.GetId(), EntryId: "gone"}); mediaCode(t, err) != CodeSiteUnavailable {
		t.Errorf("Confirm of a gone item: %v", err)
	}
	if rec := storedState(t, h.st, d4.GetId()); rec.Media.EntryID != "itemC" || rec.State != model.StateAwaitingConfirmation {
		t.Errorf("after a failed Confirm: %+v", rec)
	}
	// The recorded item again is no change.
	if c, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d4.GetId(), EntryId: "itemC"}); err != nil || c.GetFilename() != "The third one (1).mp4" || c.GetMedia().GetEntryId() != "itemC" {
		t.Errorf("Confirm of the same item: %v, %v", c, err)
	}
}

func TestSiteItemsConfirmRefusals(t *testing.T) {
	h := newHarness(t, harnessOptions{media: newFakeMedia(2)})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: sourceBrowser})
	if d.GetState() != stAwaiting {
		t.Fatalf("added = %v", d)
	}
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), EntryId: "itemA"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Confirm of a manifest with an item: %v", err)
	}
}
