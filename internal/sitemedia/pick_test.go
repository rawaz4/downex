package sitemedia

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/mediajob"
)

func trackKey(t *testing.T, off *Offer, id string) string {
	t.Helper()
	tr := trackByID(off, id)
	if tr == nil {
		t.Fatalf("track %s not offered", id)
	}
	return tr.Key
}

// The default pick is the best video with the best audio, remuxed; its ranged tracks
// carry the format's URL, headers, exact size and URL hints, and plan once the caller
// sets the chunk size.
func TestSelectDefault(t *testing.T) {
	info, off := offerFixture(t, "youtube.json", "www.youtube.com")
	pick, err := Select(info, off, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if pick.Output != OutputRemux || pick.Container != ContainerMP4 || !pick.NotBefore.IsZero() || len(pick.Tracks) != 2 {
		t.Fatalf("pick output %q, container %q, not before %v, %d tracks", pick.Output, pick.Container, pick.NotBefore, len(pick.Tracks))
	}
	f := info.Format("401")
	want := media.RangedTrack{ID: "401", Key: f.Key, Kind: media.KindVideo, Ext: "mp4", URL: f.URL, Headers: f.Headers,
		Size: 478000000, Expires: testNow.Add(6 * time.Hour), Network: "tcp4"}
	if !reflect.DeepEqual(pick.Tracks[0], want) {
		t.Errorf("video track\n got %+v\nwant %+v", pick.Tracks[0], want)
	}
	if a := pick.Tracks[1]; a.ID != "140-0" || a.Kind != media.KindAudio || a.Ext != "m4a" || a.Size != 3446000 || a.ChunkBytes != 0 {
		t.Errorf("audio track = %+v", a)
	}
	pick.Tracks[0].Headers.Set("User-Agent", "changed")
	if info.Format("401").Headers.Get("User-Agent") == "changed" {
		t.Error("the pick shares its headers with the Info")
	}
	for _, tr := range pick.Tracks {
		tr.ChunkBytes = mediajob.ChunkSize(nil, tr.URL, info.Format(tr.ID).ChunkSize)
		if p, err := media.PlanRanged(tr); err != nil || !p.SingleResource || p.Container != media.ContainerFMP4 {
			t.Errorf("PlanRanged(%s) = %+v, %v", tr.ID, p, err)
		}
	}
}

// A pick by key keeps the chosen quality, its cookie and its available_at.
func TestSelectByKey(t *testing.T) {
	info, off := offerFixture(t, "youtube.json", "www.youtube.com")
	pick, err := Select(info, off, "137", "140-1", trackKey(t, off, "137"), trackKey(t, off, "140-1"))
	if err != nil {
		t.Fatal(err)
	}
	if pick.Tracks[0].ID != "137" || pick.Tracks[1].ID != "140-1" || !pick.NotBefore.Equal(testNow.Add(5*time.Second)) {
		t.Errorf("pick %s + %s, not before %v", pick.Tracks[0].ID, pick.Tracks[1].ID, pick.NotBefore)
	}
	if got := pick.Tracks[0].Headers.Get("Cookie"); got != "VISITOR_INFO1_LIVE=abc123" {
		t.Errorf("cookie %q", got)
	}
	// Keys alone select too; an ID alone selects within the same resolution.
	if p, err := Select(info, off, "", "", trackKey(t, off, "298"), ""); err != nil || p.Tracks[0].ID != "298" || p.Tracks[1].ID != "140-0" {
		t.Errorf("Select(key 298) = %+v, %v", p, err)
	}
	if p, err := Select(info, off, "hevc-1080", "328-1", "", ""); err != nil || p.Tracks[0].ID != "hevc-1080" || p.Tracks[1].ID != "328-1" {
		t.Errorf("Select(hevc-1080, 328-1) = %+v, %v", p, err)
	}
}

// Keys stay the same when the page is resolved again with new URLs, so a run finds the
// chosen tracks in a fresh resolution; a re-encoded format (another size) is not
// found again.
func TestSelectAcrossResolutions(t *testing.T) {
	first, off1 := offerFixture(t, "youtube.json", "www.youtube.com")
	key137, keyAudio := trackKey(t, off1, "137"), trackKey(t, off1, "140-0")

	fresh := bytes.ReplaceAll(fixture(t, "youtube.json"), []byte("sig=AJfQdSswRQIh"), []byte("sig=Fresh"))
	fresh = bytes.ReplaceAll(fresh, []byte("expire=1792648800"), []byte("expire=1792652400"))
	second, err := Parse(fresh, testNow)
	if err != nil {
		t.Fatal(err)
	}
	off2, err := Offers(second, "www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	for i := range off1.Tracks {
		if off1.Tracks[i].Key != off2.Tracks[i].Key {
			t.Errorf("track %s changed its key with its URL", off1.Tracks[i].ID)
		}
	}
	pick, err := Select(second, off2, "137", "140-0", key137, keyAudio)
	if err != nil {
		t.Fatal(err)
	}
	if u := pick.Tracks[0].URL; !strings.Contains(u, "sig=Fresh137") || u == first.Format("137").URL {
		t.Errorf("pick URL %q, want the fresh one", u)
	}
	if !pick.Tracks[0].Expires.Equal(testNow.Add(7 * time.Hour)) {
		t.Errorf("expires %v", pick.Tracks[0].Expires)
	}

	resized, err := Parse(bytes.Replace(fixture(t, "youtube.json"), []byte(`"filesize": 117000000`), []byte(`"filesize": 117000001`), 1), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off3, err := Offers(resized, "www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	if trackKey(t, off3, "137") == key137 {
		t.Error("a size change kept the key")
	}
	if _, err := Select(resized, off3, "137", "", key137, ""); !errors.Is(err, media.ErrNotFound) {
		t.Errorf("Select(old key) = %v, want media.ErrNotFound", err)
	}
}

// A muxed pick takes no audio: a progressive MP4 is copied, a fragmented one remuxed.
func TestSelectMuxed(t *testing.T) {
	info, off := offerFixture(t, "youtube.json", "www.youtube.com")
	pick, err := Select(info, off, "18", "", trackKey(t, off, "18"), "")
	if err != nil {
		t.Fatal(err)
	}
	if pick.Output != OutputCopy || len(pick.Tracks) != 1 || pick.Tracks[0].Kind != media.KindMuxed || pick.Tracks[0].Size != 0 {
		t.Errorf("pick = %+v, want one muxed track copied, size unknown (an estimate only)", pick)
	}
	if _, err := Select(info, off, "18", "140-0", "", ""); !errors.Is(err, media.ErrMalformed) {
		t.Errorf("Select(muxed + audio) = %v, want media.ErrMalformed", err)
	}

	minfo, moff := offerFixture(t, "muxed.json", "vimeo.com")
	for id, want := range map[string]string{"http-1080p": OutputCopy, "dash-480p": OutputRemux, "": OutputCopy} {
		pick, err := Select(minfo, moff, id, "", "", "")
		if err != nil || pick.Output != want || len(pick.Tracks) != 1 {
			t.Errorf("Select(%q) = %+v, %v; want output %s", id, pick, err, want)
		}
	}
	if pick, _ := Select(minfo, moff, "http-720p", "", "", ""); pick == nil || !pick.Tracks[0].Unranged || pick.Tracks[0].Size != 15000000 {
		t.Errorf("Select(http-720p) = %+v, want an unranged track", pick)
	}
}

// Selections the offer cannot satisfy fail without falling back to another quality.
func TestSelectErrors(t *testing.T) {
	info, off := offerFixture(t, "youtube.json", "www.youtube.com")
	key137 := trackKey(t, off, "137")
	_, hls := offerFixture(t, "hls.json", "example.org")
	for name, c := range map[string]struct {
		info                 *Info
		off                  *Offer
		video, audio, vk, ak string
		want                 error
	}{
		"unknown id":        {info, off, "999", "", "", "", media.ErrNotFound},
		"webm id":           {info, off, "248", "", "", "", media.ErrNotFound},
		"unknown key":       {info, off, "137", "", "s1-00000000000000000000000000000000", "", media.ErrNotFound},
		"key of other id":   {info, off, "136", "", key137, "", media.ErrNotFound},
		"unknown audio key": {info, off, "", "", "", "s1-0", media.ErrNotFound},
		"audio as video":    {info, off, "140-0", "", "", "", media.ErrMalformed},
		"video as audio":    {info, off, "137", "136", "", "", media.ErrMalformed},
		"manifest offer":    {info, hls, "", "", "", "", media.ErrNotFound},
		"other info":        {parseFixture(t, "muxed.json"), off, "137", "", "", "", media.ErrNotFound},
		"no info":           {nil, off, "", "", "", "", media.ErrMalformed},
		"no offer":          {info, nil, "", "", "", "", media.ErrMalformed},
	} {
		if pick, err := Select(c.info, c.off, c.video, c.audio, c.vk, c.ak); !errors.Is(err, c.want) || pick != nil {
			t.Errorf("%s: Select = %+v, %v; want %v", name, pick, err, c.want)
		}
	}
}
