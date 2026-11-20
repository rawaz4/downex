package scene

import (
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/state"
)

// applyFixture sets up a layout fixture (Options.Fixture): comma-separated tokens on top of
// the scene, for the dialog states scripts/measure-dialogs.mjs lays out.
//   - confirm:<file|site|hls|dash|muxed|audio>: one download awaits confirmation and its
//     dialog opens by itself; file: the scene's budget-plan.xlsx; otherwise a browser
//     hand-off (the only one waiting): a YouTube page's video with q:<1|3|6|8> qualities (6),
//     audio:<1-3> tracks (2), title:<plain|long|ku|ar|none> and url:long, or an HLS or DASH
//     stream (muxed: HLS variants with their sound; audio: an audio-only stream); with
//     confirm:file, url:long gives the file a signed storage address (about 600 characters);
//   - waiting:<n>: n downloads await confirmation in all (newer file hand-offs);
//   - fail: downexd refuses every confirmation (not enough disk space);
//   - queues:<n>: n queues in all (1: the main queue only; more: stopped ones);
//   - main:<n>, night:<n>: n more queued downloads in the main queue or the second queue;
//   - gone:<name>: the file of that completed download is no longer in its folder;
//   - settings:full: a manual proxy with a password, host rules and site logins;
//   - tools:ready: yt-dlp and Deno are installed; tools:slow: their download goes on for ten
//     minutes;
//   - resolve:slow: yt-dlp reads each page for ten minutes ("Getting video info…");
//   - add:slow: adding a page's video waits for yt-dlp for ten minutes;
//   - check: file checks on some of the scene's downloads and one more hand-off
//     (filecheck.go, docs/DESKTOP-SPEC.md §9);
//   - renew: a download whose link expired, a hand-off whose link is being checked and one
//     that offers to continue the first (renew.go, docs/DESKTOP-SPEC.md §10);
//   - polite:<on|slowed>: Polite mode is on, watching a smooth line or slowing every
//     download to 2.4 MB/s (docs/POLITE-SPEC.md §8).
func (m *Mock) applyFixture(spec string) error {
	var tokens []string
	for _, t := range strings.Split(spec, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tokens = append(tokens, t)
		}
	}
	known := []string{"confirm", "q", "audio", "title", "url", "waiting", "fail", "queues", "main", "night", "gone", "settings", "tools", "resolve", "add", "check", "renew", "polite"}
	for _, t := range tokens {
		name, _, _ := strings.Cut(t, ":")
		if !slices.Contains(known, name) {
			return fixtureError(`unknown token "` + t + `"`)
		}
	}
	values := func(name string) []string {
		var out []string
		for _, t := range tokens {
			if v, ok := strings.CutPrefix(t, name+":"); ok {
				out = append(out, v)
			}
		}
		return out
	}
	value := func(name string) (string, bool) {
		if v := values(name); len(v) > 0 {
			return v[0], true
		}
		return "", false
	}
	count := func(name string, fallback int) (int, error) {
		v, ok := value(name)
		if !ok {
			return fallback, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, fixtureError(name + " needs a count")
		}
		return n, nil
	}

	if v, _ := value("settings"); v == "full" {
		m.settings.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy.corp.example:8080",
			HttpsProxy: "proxy.corp.example:8443", Bypass: "<local>;*.corp.example;10.*", Username: "downex"}
		m.settings.HostRules = []*downexv1.HostRule{
			{Pattern: "*.googlevideo.com", MaxConnections: 4, MaxRequestBytes: 10 * MiB},
			{Pattern: "mirror.example.org", MaxConnections: 16, SpeedLimit: 8_000_000},
			{Pattern: "*.example.net", MaxConnections: 2, SpeedLimit: 512_000},
		}
		m.settings.SiteLogins = []*downexv1.SiteLogin{
			{Id: "login-1", UrlPattern: "https://files.example.com/*", Username: "zana"},
			{Id: "login-2", UrlPattern: "*.intranet.example", Username: "build-bot"},
		}
		m.proxyPassword = true
		m.loginPasswords["login-1"], m.loginPasswords["login-2"] = true, true
		m.nextLogin = 3
	}
	switch v, _ := value("tools"); v {
	case "ready":
		m.toolsPhase = "ready"
		m.toolsCheckedAt = m.now()
	case "slow":
		m.toolsInstallMs = 600_000
	}
	if v, _ := value("resolve"); v == "slow" {
		m.resolveMs = 600_000
	}
	if v, _ := value("add"); v == "slow" {
		m.addSlowUntil = m.now() + 600_000
	}
	switch v, ok := value("polite"); {
	case !ok:
	case v == "on":
		m.settings.PoliteMode = true
	case v == "slowed":
		m.settings.PoliteMode = true
		m.politeFixed = &downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_SLOWED, RateLimit: 2_400_000,
			DelayMs: 42, BaseMs: 18, Since: stamp(m.startedAt)}
	default:
		return fixtureError("polite needs on or slowed")
	}

	queueCount, err := count("queues", 2)
	if err != nil {
		return err
	}
	if queueCount < 1 {
		return fixtureError("queues needs at least 1")
	}
	if queueCount == 1 {
		for id, q := range m.queues {
			if !q.isMain {
				delete(m.queues, id)
			}
		}
	}
	extra := []string{"Weekend", "Videos to watch", "Work files", "Overnight ISOs", "دروس الفيديو", "Курсы", "Podcasts", "Photo backups", "Big files (night only)", "Game updates", "Linux images", "Research papers"}
	for i := len(m.queues); i < queueCount; i++ {
		m.newQueue(extra[(i-2)%len(extra)], 0, 0, &downexv1.Schedule{}, downexv1.PostAction_POST_ACTION_UNSPECIFIED, false, false)
	}

	// fill puts more queued downloads at the end of a queue.
	fill := func(q *mqueue, n int) {
		if q == nil {
			return
		}
		kinds := []categories.FileKind{categories.Video, categories.Archives, categories.Programs, categories.Documents, categories.Audio}
		for i := 0; i < n; i++ {
			kind := kinds[i%len(kinds)]
			filename := strings.Replace(pick(m, names[kind]), "#", strconv.Itoa(100+i), 1)
			r := sizeRange[kind]
			u := "https://" + pick(m, hosts) + "/pub/" + encodeURIComponent(filename)
			category := m.chooseCategory(filename, u, "")
			d := m.baseDownload(u, filename, m.categoryFolder(category), floor(m.between(r[0], r[1])), m.now()-float64(n-i)*60_000)
			d.category = category
			m.addSim(m.newSim(d))
			m.enqueue(d, q)
		}
	}
	mainN, err := count("main", 0)
	if err != nil {
		return err
	}
	fill(m.queues[state.MainQueueID], mainN)
	nightN, err := count("night", 0)
	if err != nil {
		return err
	}
	var second *mqueue
	for _, q := range m.queuesInOrder() {
		if !q.isMain {
			second = q
			break
		}
	}
	fill(second, nightN)

	for _, name := range values("gone") {
		found := false
		for _, s := range m.list() {
			if s.d.filename == name && s.d.state == state.StateCompleted {
				m.gone[s.d.id] = true
				found = true
				break
			}
		}
		if !found {
			return fixtureError(`no completed download "` + name + `"`)
		}
	}
	if slices.Contains(tokens, "fail") {
		m.confirmRefusal = apiErr(connect.CodeResourceExhausted,
			"disk_full: not enough free space on the destination disk for this download (it needs 1.5 GB, 327 MB are free)")
	}

	kind, hasKind := value("confirm")
	urlKind, _ := value("url")
	if kind == "file" && urlKind == "long" {
		// a signed storage link, as Drive, S3 and CDNs hand them to the browser
		var s *sim
		for _, x := range m.list() {
			if x.d.state == state.StateAwaiting {
				s = x
				break
			}
		}
		if s == nil {
			return fixtureError("no download awaits confirmation")
		}
		signature := strings.Repeat("a3f9c27e8b4d6015f2e9ac7b38d4e6f1", 8)
		name := encodeURIComponent(s.d.filename)
		s.d.url = "https://storage.example-cdn.net/prod-eu-west-3/uploads/2026/10/29/" + name +
			"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAEXAMPLE7Q3NV2XZD%2F20261029%2Feu-west-3%2Fs3%2Faws4_request" +
			"&X-Amz-Date=20261029T081512Z&X-Amz-Expires=3600&X-Amz-SignedHeaders=host" +
			"&response-content-disposition=attachment%3B%20filename%3D%22" + name + "%22" +
			"&X-Amz-Signature=" + signature
	}
	if hasKind && kind != "file" {
		// the hand-off is the only download waiting: the scene's own goes on as queued
		for _, s := range m.list() {
			if s.d.state == state.StateAwaiting {
				s.d.state = state.StateQueued
				s.d.size = s.pendingSize
			}
		}
		if kind == "site" {
			page := "https://www.youtube.com/watch?v=Xb7mQ2kLp9w"
			if urlKind == "long" {
				page = "https://www.youtube.com/watch?v=Xb7mQ2kLp9w&list=PLx0sYbCqOb8TBPRdmBHs5Iftvv9TPboYG&index=7&pp=iAQBsAgCiAQB&t=42s"
			}
			key, ok := value("title")
			if !ok {
				key = "plain"
			}
			title, ok := FixtureTitles[key]
			if !ok {
				return fixtureError(`no title "` + key + `"`)
			}
			q, err := count("q", 6)
			if err != nil {
				return err
			}
			audio, err := count("audio", 2)
			if err != nil {
				return err
			}
			offer, err := fixtureOffer(fixtureVideo{pageURL: page, qualities: q, audio: audio, title: title})
			if err != nil {
				return err
			}
			if _, err := m.addSiteNow(&downexv1.AddMediaRequest{PageUrl: page, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Source: srcBrowser}, offer); err != nil {
				return err
			}
		} else {
			manifest, ok := map[string]string{
				"hls":   "https://media.example.org/talks/platform-engineering-day-2026/master.m3u8",
				"dash":  "https://media.example.org/films/coastline/manifest.mpd",
				"muxed": "https://media.example.org/shows/evening-news/muxed/index.m3u8",
				"audio": "https://media.example.org/podcasts/episode-212/audio.m3u8",
			}[kind]
			if !ok {
				return fixtureError(`no confirm kind "` + kind + `"`)
			}
			if _, err := m.addMediaNow(&downexv1.AddMediaRequest{ManifestUrl: manifest, PageUrl: "https://media.example.org/watch",
				Headers: []*downexv1.Header{{Name: "Referer", Value: "https://media.example.org/watch"}}, Source: srcBrowser}); err != nil {
				return err
			}
		}
	}
	waiting := 0
	for _, s := range m.list() {
		if s.d.state == state.StateAwaiting {
			waiting++
		}
	}
	want, err := count("waiting", 0)
	if err != nil {
		return err
	}
	for i := waiting; i < want; i++ {
		if _, err := m.addNow(m.handoffRequest()); err != nil {
			return err
		}
	}
	if slices.Contains(tokens, "check") {
		if err := m.applyCheckFixture(); err != nil {
			return err
		}
	}
	if slices.Contains(tokens, "renew") {
		if err := m.applyRenewFixture(); err != nil {
			return err
		}
	}
	m.pendingConfirms = nil // the fixture's dialog opens by itself (FixtureState.Confirm)
	m.renumberQueue()
	return nil
}
