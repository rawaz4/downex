package mediajob

import (
	"context"
	"net/url"
	"path"
	"strings"
	"time"

	"downex/internal/media"
	"downex/internal/transport"
)

// masterTimeout bounds the one request for a bare media playlist's master (masterOf).
const masterTimeout = 8 * time.Second

// masterOf returns the master playlist of bare HLS media playlist pl when the player's
// recording held only that level (docs/MEDIA-SPEC.md §7, amendment 2026-11-19: a
// Pinterest level playlist carries the picture only, its sound is in the master's audio
// rendition). The master is looked for once, without retries, in the level's folder
// under the level's name up to its first "_" ("<id>_720w.m3u8" → "<id>.m3u8", the
// naming that groups a master with its levels; the level's query is kept), and used only
// when it is a master playlist that lists this level as a variant whose AUDIO group has
// a rendition of its own: then the level carries no sound and the master adds it. nil
// otherwise (any failure is only the absence of a better choice).
func masterOf(ctx context.Context, f transport.Fetcher, rt retrier, hp headerPolicy, pl *media.HLSPlaylist) *media.HLSPlaylist {
	if pl == nil || pl.Master || pl.URL == "" {
		return nil
	}
	level, err := url.Parse(pl.URL)
	if err != nil || (level.Scheme != "https" && level.Scheme != "http") {
		return nil
	}
	dir, name := path.Split(level.Path)
	stem, _, found := strings.Cut(name, "_")
	if !found || stem == "" || !strings.EqualFold(path.Ext(name), ".m3u8") {
		return nil
	}
	cand := *level
	cand.Path, cand.RawPath = dir+stem+".m3u8", ""
	cand.Fragment = ""
	if err := checkManifestURL(cand.String()); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, masterTimeout)
	defer cancel()
	once := retrier{clock: rt.clock, max: 1, base: rt.base, maxDelay: rt.maxDelay}
	body, base, _, err := fetchManifest(ctx, f, once, hp, cand.String())
	if err != nil {
		return nil
	}
	m, err := media.ParseHLS(base, body)
	if err != nil || !m.Master {
		return nil
	}
	separate := make(map[string]bool) // AUDIO groups with a rendition of their own
	for _, r := range m.Renditions {
		if r.Type == "AUDIO" && r.URI != "" {
			separate[r.GroupID] = true
		}
	}
	for _, v := range m.Variants {
		if v.Audio != "" && separate[v.Audio] && samePlaylist(v.URI, level) {
			return m
		}
	}
	return nil
}

// samePlaylist reports whether variant URI raw names the playlist at level (host and
// path; the query may differ, as signed addresses do).
func samePlaylist(raw string, level *url.URL) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Host, level.Host) && u.Path == level.Path
}
