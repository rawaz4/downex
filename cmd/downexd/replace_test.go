package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/daemon"
)

// Download again → Replace of a completed stream or web page video: the daemon's
// MediaRunOptions.ReplaceExisting reaches the real jobs, and the new copy replaces the
// regular file at OutPath once it is complete (docs/ENGINE-SPEC.md §7.1).
func TestMediaJobsReplaceExisting(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		srv := newFileServer(t)
		tsMaster(srv, "/t/")
		sel := daemon.MediaSelection{ManifestURL: srv.url("/t/master.m3u8")}
		p, err := mediaJobs{}.Probe(siteContext(t), siteFactory(t), sel)
		if err != nil {
			t.Fatal(err)
		}
		sel.VideoTrack = p.DefaultVideo
		for _, tr := range p.Tracks {
			if tr.ID == p.DefaultVideo {
				sel.VideoKey = tr.Key
			}
		}
		want := tsPlaylist(srv, "/t/aac/", 3)
		var f jobFakes
		opts := runOptions(t, daemon.MediaRunOptions{ReplaceExisting: true})
		if err := os.WriteFile(opts.OutPath, []byte("the old copy"), 0o644); err != nil {
			t.Fatal(err)
		}
		if c, err := f.jobs().Run(siteContext(t), opts, sel, nil, nil, nil); err != nil || c != "mp4" {
			t.Fatalf("Run with ReplaceExisting = %q, %v", c, err)
		}
		if got, _ := os.ReadFile(opts.OutPath); !bytes.Equal(got, want) {
			t.Fatalf("the file was not replaced: %d bytes", len(got))
		}
	})
	t.Run("site", func(t *testing.T) {
		srv := newFileServer(t)
		init := mp4.CreateEmptyInit()
		if err := init.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte("downex"), 300_000)
		mdat := append([]byte{0, 0, 0, 0, 'm', 'd', 'a', 't'}, payload...)
		n := len(mdat)
		mdat[0], mdat[1], mdat[2], mdat[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
		file := append(encodeBox(t, init), mdat...)
		srv.add("/m.mp4", file)
		page := pageJSON(t, format{"format_id": "18", "protocol": "http", "url": srv.url("/m.mp4"), "ext": "mp4",
			"vcodec": "avc1.42001E", "acodec": "mp4a.40.2", "height": 360, "filesize": len(file)})
		s := newSiteMedia(&fakeResolver{pages: []string{page}})
		sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "18",
			VideoKey: keyOf(t, page, "18")}
		opts := runOptions(t, daemon.MediaRunOptions{HostRules: testRules{maxBytes: 1 << 20}, ReplaceExisting: true})
		if err := os.WriteFile(opts.OutPath, []byte("the old copy"), 0o644); err != nil {
			t.Fatal(err)
		}
		if c, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); err != nil || c != "mp4" {
			t.Fatalf("Run with ReplaceExisting = %q, %v", c, err)
		}
		if got, _ := os.ReadFile(opts.OutPath); !bytes.Equal(got, file) {
			t.Fatalf("the file was not replaced: %d bytes", len(got))
		}
	})
}
