package scene

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/media"
	"downex/ui/state"
)

var hosts = []string{"downloads.example.com", "mirror.example.org", "cdn.example.net", "files.example.com", "media.example.org"}

// The demo's names: every video is one MP4 and audio is MP3 or M4A.
var kindOrder = []categories.FileKind{categories.Video, categories.Audio, categories.Archives, categories.Documents, categories.Programs, categories.Other}

var names = map[categories.FileKind][]string{
	categories.Video:     {"city-timelapse-4k.mp4", "conference-keynote.mp4", "drone-footage-coast.mp4", "tutorial-episode-#.mp4", "nature-documentary-part#.mp4"},
	categories.Audio:     {"podcast-episode-#.mp3", "lecture-audio.m4a", "field-recording-forest.m4a", "ambient-loop-#.mp3"},
	categories.Archives:  {"project-assets-#.zip", "dataset-sensors-2026.tar.gz", "source-snapshot.tar.xz", "photo-archive-#.7z", "disk-image-#.iso"},
	categories.Documents: {"annual-report-2025.pdf", "user-guide.pdf", "research-paper-#.pdf", "budget-plan.xlsx", "meeting-notes-#.docx", "novel-draft.epub"},
	categories.Programs:  {"setup-x64-#.exe", "installer-#.msi", "toolkit_#_amd64.deb", "viewer-#.x86_64.rpm", "editor-#.AppImage"},
	categories.Other:     {"firmware-v#.bin", "map-tiles-region-#.mbtiles", "checksums-#.sha256", "telemetry-#.dat"},
}

var sizeRange = map[categories.FileKind][2]float64{
	categories.Video:     {150 * MiB, 4 * GiB},
	categories.Audio:     {3 * MiB, 80 * MiB},
	categories.Archives:  {10 * MiB, 3 * GiB},
	categories.Documents: {120 * KiB, 40 * MiB},
	categories.Programs:  {2 * MiB, 600 * MiB},
	categories.Other:     {200 * KiB, 120 * MiB},
}

// SceneRow is one row of the Studio scene.
type SceneRow struct {
	Name, Host string
	Size       float64
	State      downexv1.DownloadState
	// Ago is the minutes before the start that it was added.
	Ago         float64
	Fraction    float64 // 0: the state's default
	Connections float64
	Speed       float64
	Source      downexv1.AddSource
	Page        string
	Title       string
	Manifest    string
}

const (
	srcBrowser = downexv1.AddSource_ADD_SOURCE_BROWSER
	srcCLI     = downexv1.AddSource_ADD_SOURCE_CLI
)

// studioScene is the Studio scene (DESIGN-SYSTEM.md §7.1), newest first. Sidebar counts:
// Video 11, Audio 3, Archives 6, Documents 3, Programs 4, Other 1; states: 3 downloading, 1
// to confirm, 7 queued, 1 paused, 15 completed, 1 failed.
var studioScene = []SceneRow{
	{Name: "os-image-26.04-amd64.iso", Host: "releases.example.org", Size: 5.9 * GiB, State: state.StateDownloading, Ago: 6, Fraction: 0.67, Connections: 16, Speed: 11.2 * MiB, Source: srcBrowser},
	{Name: "conference-keynote.mp4", Host: "www.youtube.com", State: state.StateDownloading, Ago: 11, Fraction: 0.42, Speed: 5.8 * MiB, Source: srcBrowser,
		// a 38-minute talk offered up to 1440p: about 1.7 GiB in 1080p60 H.264
		Page: "https://www.youtube.com/watch?v=Kn000wdmX26", Title: "Conference keynote 2026: opening session"},
	{Name: "telemetry-183.dat", Host: "files.example.com", Size: 412 * MiB, State: state.StateDownloading, Ago: 17, Fraction: 0.81, Connections: 4, Speed: 0.91 * MiB},
	{Name: "budget-plan.xlsx", Host: "files.example.com", Size: 1.2 * MiB, State: state.StateAwaiting, Ago: 24, Source: srcBrowser},
	{Name: "city-timelapse-4k.mp4", Host: "media.example.org", Size: 1.3 * GiB, State: state.StatePaused, Ago: dayMin + 40, Fraction: 0.18},
	{Name: "setup-x64-312.exe", Host: "downloads.example.com", Size: 543 * MiB, State: state.StateQueued, Ago: dayMin + 95},
	{Name: "annual-report-2025.pdf", Host: "files.example.com", Size: 39.8 * MiB, State: state.StateQueued, Ago: 2*dayMin + 30, Source: srcBrowser},
	{Name: "photo-archive-277.7z", Host: "mirror.example.org", Size: 2.5 * GiB, State: state.StateFailed, Ago: 2*dayMin + 210, Fraction: 0.18},
	{Name: "keynote-2026.mp4", Host: "media.example.org", State: state.StateCompleted, Ago: 3*dayMin + 60, Manifest: "https://media.example.org/talks/keynote-2026/master.m3u8", Source: srcBrowser},
	{Name: "podcast-episode-41.mp3", Host: "cdn.example.net", Size: 74.8 * MiB, State: state.StateCompleted, Ago: 3*dayMin + 300},
	{Name: "drone-footage-coast.mp4", Host: "cdn.example.net", Size: 2.1 * GiB, State: state.StateCompleted, Ago: 4*dayMin + 90, Source: srcBrowser},
	{Name: "viewer-8.x86_64.rpm", Host: "downloads.example.com", Size: 230 * MiB, State: state.StateCompleted, Ago: 4*dayMin + 400, Source: srcCLI},
	{Name: "novel-draft.epub", Host: "files.example.com", Size: 14.3 * MiB, State: state.StateCompleted, Ago: 5*dayMin + 120},
	{Name: "project-assets-362.zip", Host: "mirror.example.org", Size: 2.2 * GiB, State: state.StateCompleted, Ago: 6*dayMin + 60},
	{Name: "nature-documentary-part1.mp4", Host: "media.example.org", Size: 1.6 * GiB, State: state.StateCompleted, Ago: 7*dayMin + 30},
	{Name: "ambient-loop-3.mp3", Host: "cdn.example.net", Size: 8.4 * MiB, State: state.StateCompleted, Ago: 7*dayMin + 500},
	{Name: "source-snapshot.tar.xz", Host: "files.example.com", Size: 412 * MiB, State: state.StateCompleted, Ago: 8*dayMin + 200, Source: srcCLI},
	{Name: "editor-5.AppImage", Host: "downloads.example.com", Size: 118 * MiB, State: state.StateCompleted, Ago: 9*dayMin + 45},
	{Name: "tutorial-episode-9.mp4", Host: "media.example.org", Size: 486 * MiB, State: state.StateCompleted, Ago: 10*dayMin + 80},
	{Name: "tutorial-episode-8.mp4", Host: "media.example.org", Size: 471 * MiB, State: state.StateCompleted, Ago: 11*dayMin + 80},
	{Name: "nature-documentary-part3.mp4", Host: "media.example.org", Size: 1.8 * GiB, State: state.StateCompleted, Ago: 12*dayMin + 300},
	{Name: "tutorial-episode-10.mp4", Host: "media.example.org", Size: 502 * MiB, State: state.StateCompleted, Ago: 13*dayMin + 60},
	{Name: "project-assets-361.zip", Host: "mirror.example.org", Size: 2.1 * GiB, State: state.StateCompleted, Ago: 14*dayMin + 20},
	// Queued #3 to #7: added earlier, waiting for their turn in the main queue.
	{Name: "tutorial-episode-12.mp4", Host: "media.example.org", Size: 512 * MiB, State: state.StateQueued, Ago: 15 * dayMin},
	{Name: "nature-documentary-part2.mp4", Host: "media.example.org", Size: 1.7 * GiB, State: state.StateQueued, Ago: 15*dayMin + 300},
	{Name: "dataset-sensors-2026.tar.gz", Host: "files.example.com", Size: 860 * MiB, State: state.StateQueued, Ago: 16 * dayMin},
	{Name: "installer-27.msi", Host: "downloads.example.com", Size: 96 * MiB, State: state.StateQueued, Ago: 17 * dayMin},
	{Name: "lecture-audio.m4a", Host: "cdn.example.net", Size: 61 * MiB, State: state.StateQueued, Ago: 18 * dayMin},
}

// StudioScene is the Studio scene's rows (a copy).
func StudioScene() []SceneRow { return slices.Clone(studioScene) }

var seedFailures = [][2]string{
	{"validator_changed", "The file on the server changed since the download started"},
	{"network", "Connection reset by the server"},
	{"http_status", "Server responded 403 Forbidden"},
	{"disk_full", "Not enough free space on the destination disk"},
	{"tls", "The server certificate is not valid for this host"},
	{"stalled", "No data received for 60 seconds"},
}

func (m *Mock) seedTimes(d *dl, bytes float64) {
	d.activeMs = max(1000, round(bytes/(2*MiB)*1000))
	d.startedAt = d.createdAt + 1500
}

func (m *Mock) planSegments(s *sim) {
	d := s.d
	ranged := !s.noRange && d.size > 0
	if !ranged {
		d.mode = downexv1.Mode_MODE_SINGLE
		end := -1.0
		if d.size > 0 {
			end = d.size - 1
		}
		d.segments = []*segment{{start: 0, end: end}}
		return
	}
	d.mode = downexv1.Mode_MODE_RANGED
	wanted := d.maxConnections
	if wanted == 0 {
		wanted = float64(m.settings.GetDefaultMaxConnections())
	}
	n := max(1, min(wanted, floor(d.size/MiB)))
	step := floor(d.size / n)
	d.segments = nil
	for i := 0.0; i < n; i++ {
		end := (i+1)*step - 1
		if i == n-1 {
			end = d.size - 1
		}
		d.segments = append(d.segments, &segment{start: i * step, end: end})
	}
}

// seedProgress spreads bytes over fresh segments to give seeded downloads a believable map.
func (m *Mock) seedProgress(s *sim, fraction float64) {
	m.planSegments(s)
	d := s.d
	total := 0.0
	for _, seg := range d.segments {
		length := segmentLength(seg, d.size)
		if math.IsInf(length, 0) {
			seg.received = floor(m.between(1, 50) * MiB)
		} else {
			seg.received = floor(length * min(1, max(0, fraction+m.between(-0.25, 0.25))))
		}
		total += seg.received
	}
	d.received = total
}

// seed generates count rows (Scene Random).
func (m *Mock) seed(count int) {
	t0 := m.now()
	span := 30 * 86400_000.0
	if count > 500 {
		span = 365 * 86400_000
	}
	n := float64(count)
	for i := 0; i < count; i++ {
		cat := pick(m, kindOrder)
		template := pick(m, names[cat])
		filename := strings.Replace(template, "#", strconv.Itoa(1+int(floor(m.rng()*400))), 1)
		host := pick(m, hosts)
		// Older entries are mostly finished; the newest ones are still queued.
		fromEnd := count - 1 - i
		unknownSize := fromEnd == 4
		noRange := fromEnd == 6
		r := sizeRange[cat]
		size := floor(m.between(r[0], r[1]))
		createdAt := floor(t0 - span*((n-float64(i))/n) - m.rng()*3600_000)
		dirPart := "pub"
		if unknownSize {
			dirPart = "live/unknown-size"
		}
		rawURL := "https://" + host + "/" + dirPart + "/" + encodeURIComponent(filename)
		category := m.chooseCategory(filename, rawURL, "")
		// Older downloads were saved before category folders existed.
		dir := m.settings.GetDefaultDir()
		if m.rng() < 0.5 {
			dir = m.categoryFolder(category)
		}
		dsize := size
		if unknownSize {
			dsize = -1
		}
		d := m.baseDownload(rawURL, filename, dir, dsize, createdAt)
		d.category = category
		d.source = [3]downexv1.AddSource{downexv1.AddSource_ADD_SOURCE_UI, srcBrowser, srcCLI}[i%3]
		s := m.newSim(d)
		if unknownSize {
			s.hiddenSize = size
		}
		if noRange {
			s.noRange = true
		}
		m.addSim(s)
		q, _ := m.targetQueue(state.MainQueueID)
		m.enqueue(d, q)

		roll := m.rng()
		recent := float64(i)/n >= 0.6
		completedShare := 0.82
		if recent {
			completedShare = 0.25
		}
		switch {
		case unknownSize || noRange || fromEnd <= int(m.settings.GetMaxConcurrentDownloads())+1:
			d.state = state.StateQueued
		case roll < completedShare:
			d.state = state.StateCompleted
			d.received = size
			d.mode = downexv1.Mode_MODE_RANGED
			d.completedAt = min(t0, createdAt+floor(m.between(20_000, 3_600_000)))
			d.updatedAt = d.completedAt
			// Started soon after it was added; paused for part of the time now and then.
			d.startedAt = min(d.completedAt, createdAt+1500)
			d.activeMs = round((d.completedAt - d.startedAt) * (0.6 + float64((d.id*37)%41)/100))
		case roll < completedShare+0.08:
			d.state = state.StatePaused
			m.seedProgress(s, m.rng())
			m.seedTimes(d, d.received)
		case roll < completedShare+0.16:
			f := pick(m, seedFailures)
			d.state = state.StateFailed
			m.seedProgress(s, m.between(0.05, 0.9))
			m.seedTimes(d, d.received)
			d.errCode, d.err = f[0], f[1]
			if f[0] == "validator_changed" {
				s.remoteChanged = true
			}
		default:
			d.state = state.StateQueued
		}
	}
	m.seedMedia()
	// Give the first view some live transfers.
	started := 0
	for _, s := range m.list() {
		if started >= int(m.settings.GetMaxConcurrentDownloads()) {
			break
		}
		if s.d.state != state.StateQueued || s.hiddenSize > 0 {
			continue
		}
		m.start(s)
		s.probeLeftMs = 0
		s.d.state = state.StateDownloading
		m.seedProgress(s, m.between(0.05, 0.6))
		m.seedTimes(s.d, s.d.received)
		s.d.startedAt = max(s.d.createdAt, t0-s.d.activeMs)
		started++
	}
	m.renumberQueue()
}

// mediaCategory is the category the daemon gives a stream: Video, or Audio when it has no
// video.
func (m *Mock) mediaCategory(info *downexv1.MediaInfo) string {
	kind := categories.Audio
	if media.TrackByID(info, info.GetSelectedVideo()) != nil {
		kind = categories.Video
	}
	for _, c := range m.settings.GetCategories() {
		if categories.CategoryKind(c.GetName(), c.GetExtensions()) == kind {
			return c.GetName()
		}
	}
	return ""
}

// seedAsMedia makes a seeded download a stream in the same state.
func (m *Mock) seedAsMedia(s *sim, manifestURL string) {
	d := s.d
	info, err := probeStream(manifestURL)
	if err != nil {
		return
	}
	total := mediaBytes(info)
	fraction := 0.35
	switch d.state {
	case state.StateCompleted:
		fraction = 1
	case state.StateQueued:
		fraction = 0
	}
	d.url, d.finalURL = manifestURL, ""
	d.filename = mediaFilename(info.GetTitle())
	d.kind = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
	d.category = m.mediaCategory(info)
	d.mode = downexv1.Mode_MODE_UNSPECIFIED
	d.segments = nil
	d.received = floor(total * fraction)
	d.size = -1
	if d.state == state.StateCompleted {
		d.size = total
	}
	mi := cloneMedia(info)
	if fraction == 1 {
		mi.FragmentsDone = info.GetFragmentsTotal()
	} else {
		mi.FragmentsDone = int32(floor(float64(info.GetFragmentsTotal()) * fraction))
	}
	d.media = mi
	if d.state == state.StateFailed {
		d.errCode, d.err = "network", "The connection was reset while fetching a fragment"
	}
	s.media = &mediaSim{probed: info, totalBytes: total}
	s.hiddenSize, s.noRange, s.remoteChanged = -1, false, false
}

func (m *Mock) seedMedia() {
	manifests := []string{
		"https://media.example.org/talks/keynote-2026/master.m3u8",
		"https://media.example.org/films/coastline/manifest.mpd",
		"https://media.example.org/shows/evening-news/muxed/index.m3u8",
	}
	list := m.list()
	next := 0
	// Older entries that are not about to start (the newest ones are queued).
	for i := len(list) - 8; i >= 0 && next < len(manifests); i -= 3 {
		s := list[i]
		if s.d.state == state.StateQueued || s.hiddenSize > 0 || s.noRange {
			continue
		}
		m.seedAsMedia(s, manifests[next])
		next++
	}
}

// spreadProgress spreads fraction over fresh segments, each a little ahead or behind (the
// mean kept).
func (m *Mock) spreadProgress(s *sim, fraction float64) {
	m.planSegments(s)
	d := s.d
	jitter := make([]float64, len(d.segments))
	sum := 0.0
	for i := range jitter {
		jitter[i] = m.between(-0.22, 0.22)
		sum += jitter[i]
	}
	mean := sum / max(1, float64(len(jitter)))
	total := 0.0
	for i, seg := range d.segments {
		length := segmentLength(seg, d.size)
		share := min(1, max(0.02, fraction+jitter[i]-mean))
		seg.received = floor(length * share)
		total += seg.received
	}
	d.received = total
}

// slotWeight is a connection's weight in its download's speed (0.85–1.15).
func slotWeight(index float64) float64 { return 0.85 + float64((int(index)*37)%31)/100 }

func newSlot(index float64) *slot {
	return &slot{index: index, state: connConnecting, phaseMs: 300, last: segment{start: -1, end: -1}}
}

const (
	connConnecting = downexv1.ConnectionState_CONNECTION_STATE_CONNECTING
	connRequesting = downexv1.ConnectionState_CONNECTION_STATE_REQUESTING
	connReceiving  = downexv1.ConnectionState_CONNECTION_STATE_RECEIVING
	connRetrying   = downexv1.ConnectionState_CONNECTION_STATE_RETRYING
	connIdle       = downexv1.ConnectionState_CONNECTION_STATE_IDLE
	connDone       = downexv1.ConnectionState_CONNECTION_STATE_DONE
)

// seedRunning makes a seeded download run now: its connections are already receiving.
func (m *Mock) seedRunning(s *sim, speed float64) {
	d := s.d
	d.state = state.StateDownloading
	d.speedBps = speed
	s.baseSpeed = speed
	m.seedTimes(d, d.received)
	d.startedAt = max(d.createdAt+1500, m.now()-d.activeMs)
	if s.media != nil {
		wanted := d.maxConnections
		if wanted == 0 {
			wanted = float64(m.settings.GetDefaultMaxConnections())
		}
		d.connections = min(wanted, max(1, float64(d.media.GetFragmentsTotal()-d.media.GetFragmentsDone())))
		s.slots = nil
		for i := 0.0; i < d.connections; i++ {
			sl := newSlot(i + 1)
			sl.state, sl.phaseMs = connReceiving, 0
			s.slots = append(s.slots, sl)
		}
		return
	}
	var open []*segment
	for _, seg := range d.segments {
		if seg.received < segmentLength(seg, d.size) {
			open = append(open, seg)
		}
	}
	s.slots = nil
	for i, seg := range open {
		idx := float64(i + 1)
		s.slots = append(s.slots, &slot{index: idx, state: connReceiving, seg: seg, last: *seg, speedBps: speed * slotWeight(idx) / float64(len(open))})
	}
	d.connections = float64(len(s.slots))
	d.connInfo = nil
	for _, sl := range s.slots {
		d.connInfo = append(d.connInfo, slotInfo(sl, -1))
	}
}

// seedCompleted is the finished history of a seeded download: it came at 2 to 12 MiB/s
// soon after it was added.
func (m *Mock) seedCompleted(d *dl) {
	d.state = state.StateCompleted
	d.received = d.size
	d.startedAt = d.createdAt + 1500
	seconds := max(2, d.size/(m.between(2, 12)*MiB))
	d.completedAt = min(m.now(), d.startedAt+round(seconds*1000))
	d.updatedAt = d.completedAt
	d.activeMs = max(1000, d.completedAt-d.startedAt)
}

func (m *Mock) seedStudio() {
	t0 := m.now()
	for _, row := range studioScene {
		if n, ok := m.opts.Rename[row.Name]; ok {
			row.Name = n
		}
		createdAt := t0 - row.Ago*60_000
		rawURL := row.Page
		if rawURL == "" {
			rawURL = row.Manifest
		}
		if rawURL == "" {
			rawURL = "https://" + row.Host + "/pub/" + encodeURIComponent(row.Name)
		}
		size := round(row.Size)
		category := m.chooseCategory(row.Name, rawURL, "")
		d := m.baseDownload(rawURL, row.Name, m.categoryFolder(category), size, createdAt)
		d.category = category
		d.source = row.Source
		if d.source == downexv1.AddSource_ADD_SOURCE_UNSPECIFIED {
			d.source = downexv1.AddSource_ADD_SOURCE_UI
		}
		d.maxConnections = row.Connections
		s := m.newSim(d)
		m.addSim(s)
		q, _ := m.targetQueue(state.MainQueueID)
		m.enqueue(d, q)
		switch row.State {
		case state.StateCompleted:
			if row.Manifest != "" {
				d.state = state.StateCompleted
				m.seedAsMedia(s, row.Manifest)
			} else {
				d.mode = downexv1.Mode_MODE_RANGED
			}
			m.seedCompleted(d)
		case state.StateAwaiting:
			// As a browser hand-off: the size is the browser's hint until it is confirmed.
			d.state = state.StateAwaiting
			d.size = -1
			d.expectedSize = size
			s.pendingSize = size
		case state.StatePaused, state.StateFailed:
			d.state = row.State
			fraction := row.Fraction
			if fraction == 0 {
				fraction = 0.2
			}
			m.spreadProgress(s, fraction)
			m.seedTimes(d, d.received)
			if row.State == state.StateFailed {
				d.errCode, d.err = "http_status", "HTTP 503 Service Unavailable"
			}
		case state.StateDownloading:
			if row.Page != "" {
				// A YouTube video in 1080p (H.264 where the page has it), saved as one MP4.
				offer, err := siteOffer(row.Page)
				if err != nil {
					continue
				}
				var hd *track
				for _, t := range offer.GetTracks() {
					if t.GetKind() == "video" && t.GetHeight() == 1080 && strings.HasPrefix(strings.ToLower(t.GetCodecs()), "avc1") {
						hd = t
						break
					}
				}
				if hd == nil {
					for _, t := range offer.GetTracks() {
						if t.GetKind() == "video" && t.GetHeight() == 1080 {
							hd = t
							break
						}
					}
				}
				p, _ := choosePick(offer, hd.GetId(), "")
				info := withPick(offer, p)
				if row.Title != "" {
					info.Title = row.Title
				}
				total := mediaBytes(info)
				fraction := row.Fraction
				if fraction == 0 {
					fraction = 0.4
				}
				d.kind = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
				info.FragmentsDone = int32(floor(float64(info.GetFragmentsTotal()) * fraction))
				d.media = info
				d.category = m.mediaCategory(info)
				d.dir = m.categoryFolder(d.category)
				d.size, d.expectedSize = total, total
				d.received = floor(total * fraction)
				s.media = &mediaSim{probed: offer, totalBytes: total}
			} else {
				fraction := row.Fraction
				if fraction == 0 {
					fraction = 0.5
				}
				m.spreadProgress(s, fraction)
			}
			speed := row.Speed
			if speed == 0 {
				speed = 2 * MiB
			}
			m.seedRunning(s, speed)
		case state.StateQueued:
			d.state = state.StateQueued
		}
	}
	m.renumberQueue()
}

var (
	reFlaky   = regexp.MustCompile(`flaky`)
	reNoRange = regexp.MustCompile(`norange|unknown-size`)
)

func (m *Mock) start(s *sim) {
	d := s.d
	d.state = state.StateProbing
	d.queuePosition = 0
	d.err, d.errCode = "", ""
	d.speedBps = 0
	if d.startedAt == 0 {
		d.startedAt = m.now()
	}
	// A web page's video is read again by yt-dlp first ("Getting video info…").
	if media.IsSite(d.media) {
		s.probeLeftMs = m.between(1200, 2400)
	} else {
		s.probeLeftMs = m.between(250, 900)
	}
	s.slots = nil
	s.assembleLeftMs = -1
	d.connInfo = nil
	// A stream's size is what its fragments add up to.
	total := d.size
	if s.media != nil {
		total = s.media.totalBytes
	}
	if strings.Contains(d.url, "404") {
		s.probeError = "Server responded 404 Not Found"
	}
	if strings.Contains(d.url, "changed") && !s.scenarioDone && d.received == 0 && s.media == nil {
		s.changeAt = floor(d.size * 0.3)
	}
	if reFlaky.MatchString(d.url) && !s.scenarioDone && d.received == 0 {
		s.failAt = floor(total * 0.5)
		s.failCode = "network"
		s.failMessage = "Connection reset by the server"
		if s.media != nil {
			s.failMessage = "The connection was reset while fetching a fragment"
		}
	} else if s.failAt < 0 && d.received == 0 && total > 0 && m.rng() < m.failRate {
		s.failAt = floor(total * m.between(0.1, 0.9))
		s.failCode = "network"
		s.failMessage = "The connection timed out"
	}
	if reNoRange.MatchString(d.url) {
		s.noRange = true
	}
	if strings.Contains(d.url, "unknown-size") && d.size < 0 && s.hiddenSize < 0 {
		s.hiddenSize = floor(m.between(20*MiB, 300*MiB))
	}
	m.queueDirty = true
	m.touch(d)
}

// loginCovers reports whether a site login's pattern covers a host.
func loginCovers(pattern, host string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if i := strings.Index(p, "://"); i > 0 && strings.Trim(p[:i], "abcdefghijklmnopqrstuvwxyz") == "" {
		p = p[i+3:]
	}
	if i := strings.IndexAny(p, "/:"); i >= 0 {
		p = p[:i]
	}
	h := strings.ToLower(host)
	if strings.HasPrefix(p, "*.") {
		return h == p[2:] || strings.HasSuffix(h, p[1:])
	}
	return p != "" && p == h
}

// hasLogin: credentials for an address are its user info, or a site login with a password
// for its host.
func (m *Mock) hasLogin(raw string) bool {
	u, ok := parseJSURL(raw)
	if !ok {
		return false
	}
	if u.username != "" {
		return true
	}
	for _, l := range m.settings.GetSiteLogins() {
		if m.loginPasswords[l.GetId()] && loginCovers(l.GetUrlPattern(), u.hostname) {
			return true
		}
	}
	return false
}

func (m *Mock) fail(s *sim, code, message string) {
	d := s.d
	d.state = state.StateFailed
	d.errCode, d.err = code, message
	d.speedBps, d.connections = 0, 0
	m.touch(d)
}

var reAuth = regexp.MustCompile(`private|auth-required`)
var reHost = regexp.MustCompile(`^https?://[^/]+`)

func (m *Mock) finishProbe(s *sim) {
	d := s.d
	if s.media != nil && media.IsSite(d.media) {
		// The size is known from the formats (Download.size = MediaProgress.BytesTotal).
		d.size = s.media.totalBytes
	}
	switch {
	case s.probeError != "":
		m.fail(s, "http_status", s.probeError)
		return
	case s.remoteChanged && d.received > 0:
		m.fail(s, "validator_changed", "The file on the server changed since the download started")
		return
	case s.media != nil && strings.Contains(d.url, "init-protected"):
		// MEDIA-SPEC §6.4: CheckInit runs on every init segment before any media fragment.
		m.fail(s, "encrypted", "An initialization segment is encrypted (it declares content protection)")
		return
	case reAuth.MatchString(d.url) && !m.hasLogin(d.url):
		// M6-SPEC §2: no credentials match the server's challenge.
		m.fail(s, "auth_required", "The server asks for a user name and password (HTTP 401)")
		return
	}
	if strings.Contains(d.url, "redirect") {
		d.finalURL = reHost.ReplaceAllString(d.url, "https://edge.example.net")
	}
	d.state = state.StateDownloading
	if len(d.segments) == 0 && s.media == nil {
		m.planSegments(s)
	}
	m.touch(d)
}

func (m *Mock) wantedConnections(d *dl) float64 {
	if d.maxConnections != 0 {
		return d.maxConnections
	}
	return float64(m.settings.GetDefaultMaxConnections())
}

func (m *Mock) split(d *dl) {
	var active []*segment
	for _, seg := range d.segments {
		if seg.received < segmentLength(seg, d.size) {
			active = append(active, seg)
		}
	}
	if d.mode != downexv1.Mode_MODE_RANGED || float64(len(active)) >= m.wantedConnections(d) || len(d.segments) >= maxSegments {
		return
	}
	var best *segment
	bestLeft := 0.0
	for _, seg := range active {
		if left := segmentLength(seg, d.size) - seg.received; left > bestLeft {
			best, bestLeft = seg, left
		}
	}
	if best == nil || bestLeft < minSplit {
		return
	}
	mid := best.start + best.received + floor(bestLeft/2)
	tail := &segment{start: mid, end: best.end}
	best.end = mid - 1
	i := slices.Index(d.segments, best)
	d.segments = slices.Insert(d.segments, i+1, tail)
}

var retryReasons = []string{"HTTP 503", "connection reset", "timed out"}

// hiccup: sometimes a receiving connection has to wait and try again.
func (m *Mock) hiccup(sl *slot, dtMs float64) {
	if sl.state != connReceiving || m.retryRate <= 0 {
		return
	}
	if m.slotRng() >= m.retryRate*(dtMs/1000) {
		return
	}
	sl.state = connRetrying
	sl.phaseMs = 2000 + floor(m.slotRng()*3)*1000
	sl.reason = retryReasons[int(floor(m.slotRng()*float64(len(retryReasons))))]
}

// advanceSlot moves a slot through its timed phases: connecting, requesting, retrying,
// then receiving.
func advanceSlot(sl *slot, dtMs float64) {
	left := dtMs
	for left > 0 && (sl.state == connConnecting || sl.state == connRequesting || sl.state == connRetrying) {
		if left < sl.phaseMs {
			sl.phaseMs -= left
			return
		}
		left -= sl.phaseMs
		switch sl.state {
		case connRequesting:
			sl.state, sl.phaseMs = connReceiving, 0
		case connConnecting:
			sl.state, sl.phaseMs = connRequesting, 200
		default:
			sl.state, sl.phaseMs, sl.reason = connConnecting, 300, ""
		}
	}
}

func slotInfo(sl *slot, fragment int32) *downexv1.ConnectionInfo {
	r := sl.last
	if sl.seg != nil {
		r = *sl.seg
	}
	info := &downexv1.ConnectionInfo{Index: int32(sl.index), State: sl.state, Start: int64(r.start), End: int64(r.end), Received: int64(r.received), Fragment: fragment}
	if sl.state == connReceiving {
		info.SpeedBps = round(sl.speedBps)
	}
	if sl.state == connRetrying {
		// downexd's wording: "<reason>, retrying in <n> s".
		info.Detail = sl.reason + ", retrying in " + strconv.Itoa(int(max(1, math.Ceil(sl.phaseMs/1000)))) + " s"
	}
	return info
}

// runSlots gives each unfinished segment a worker slot (at most the wanted connections),
// moves the slots through their phases and returns the segments that receive data now.
func (m *Mock) runSlots(s *sim, active []*segment, dtMs float64, lengthOf func(*segment) float64) []*segment {
	d := s.d
	wanted := 1.0
	if d.mode == downexv1.Mode_MODE_RANGED {
		wanted = m.wantedConnections(d)
	}
	for _, sl := range s.slots {
		if sl.seg != nil && sl.seg.received >= lengthOf(sl.seg) {
			sl.last = *sl.seg
			sl.seg = nil
			sl.state = connDone
			sl.speedBps = 0
		}
	}
	busy := map[*segment]bool{}
	for _, sl := range s.slots {
		if sl.seg != nil {
			busy[sl.seg] = true
		}
	}
	for _, seg := range active {
		if busy[seg] {
			continue
		}
		var free *slot
		for _, sl := range s.slots {
			if sl.seg == nil && sl.index <= wanted {
				free = sl
				break
			}
		}
		if free == nil && float64(len(s.slots)) < wanted {
			free = newSlot(float64(len(s.slots) + 1))
			s.slots = append(s.slots, free)
		}
		if free == nil {
			break
		}
		free.seg = seg
		free.state, free.phaseMs = connConnecting, 300
		busy[seg] = true
	}
	// A lowered limit stops the extra slots at once (their ranges wait for a free slot).
	for _, sl := range s.slots {
		if sl.index > wanted && sl.seg != nil {
			sl.last = *sl.seg
			sl.seg = nil
			sl.state = connIdle
			sl.speedBps = 0
		}
	}
	s.slots = slices.DeleteFunc(s.slots, func(sl *slot) bool { return !(sl.index <= wanted || sl.seg != nil) })
	for _, sl := range s.slots {
		advanceSlot(sl, dtMs)
		m.hiccup(sl, dtMs)
		if sl.seg == nil && sl.state != connDone {
			sl.state = connIdle
		}
	}
	var out []*segment
	for _, seg := range active {
		if slices.ContainsFunc(s.slots, func(sl *slot) bool { return sl.seg == seg && sl.state == connReceiving }) {
			out = append(out, seg)
		}
	}
	return out
}

func (m *Mock) addCompleted(s *sim) {
	m.completedBytes += s.d.size - max(0, s.replaceBytes)
	s.replaceBytes = -1
}

// transferMedia: fragments arrive in parallel; when all are done the stream is assembled
// into one file.
func (m *Mock) transferMedia(s *sim, ms *mediaSim, budget, dtMs float64) {
	d := s.d
	info := d.media
	if info == nil {
		return
	}
	if s.assembleLeftMs >= 0 {
		// All fragments are there: the remux takes a moment, without connections.
		s.assembleLeftMs -= dtMs
		if s.assembleLeftMs <= 0 {
			s.assembleLeftMs = -1
			d.size = d.received
			d.state = state.StateCompleted
			d.completedAt = m.now()
			m.addCompleted(s)
			m.touch(d)
		}
		return
	}
	before := d.received
	d.received = min(ms.totalBytes, d.received+floor(budget*dtMs/1000))
	instant := (d.received - before) * 1000 / dtMs
	d.speedBps = d.speedBps*0.5 + instant*0.5
	total := float64(info.GetFragmentsTotal())
	finished := d.received >= ms.totalBytes
	done := total
	if !finished {
		done = min(total-1, floor(total*d.received/ms.totalBytes))
	}
	if int32(done) != info.GetFragmentsDone() {
		next := cloneMedia(info)
		next.FragmentsDone = int32(done)
		d.media = next
	}
	d.connections = max(0, min(total-done, m.wantedConnections(d)))
	// One slot per connection, each on the next fragment that is not done.
	for float64(len(s.slots)) < d.connections {
		s.slots = append(s.slots, newSlot(float64(len(s.slots)+1)))
	}
	s.slots = s.slots[:int(d.connections)]
	fragmentBytes := 0.0
	if total > 0 {
		fragmentBytes = ms.totalBytes / total
	}
	for _, sl := range s.slots {
		advanceSlot(sl, dtMs)
		m.hiccup(sl, dtMs)
	}
	// The receiving connections share the download's speed, so their boxes add up to it.
	weights := 0.0
	for _, sl := range s.slots {
		if sl.state == connReceiving {
			weights += slotWeight(sl.index)
		}
	}
	d.connInfo = nil
	for i, sl := range s.slots {
		sl.speedBps = 0
		if weights > 0 && sl.state == connReceiving {
			sl.speedBps = d.speedBps * slotWeight(sl.index) / weights
		}
		fragment := min(total-1, done+float64(i))
		received := floor(fragmentBytes * math.Mod(d.received/max(1, fragmentBytes)+float64(i)*0.37, 1))
		sl.last = segment{start: 0, end: max(0, round(fragmentBytes)-1), received: received}
		d.connInfo = append(d.connInfo, slotInfo(sl, int32(fragment)))
	}
	m.touch(d)
	if s.failAt >= 0 && d.received >= s.failAt {
		s.failAt = -1
		s.scenarioDone = true
		m.fail(s, s.failCode, s.failMessage)
		return
	}
	if finished {
		s.assembleLeftMs = m.assembleMs
		s.slots = nil
		d.connInfo = nil
		d.speedBps, d.connections = 0, 0
		m.touch(d)
		if m.assembleMs <= 0 {
			m.transferMedia(s, ms, 0, 0)
		}
	}
}

func (m *Mock) transfer(s *sim, budget, dtMs float64) {
	if s.media != nil {
		m.transferMedia(s, s.media, budget, dtMs)
		return
	}
	d := s.d
	bytes := floor(budget * dtMs / 1000)
	// An open-ended segment still ends where the simulated server stops sending.
	lengthOf := func(seg *segment) float64 {
		length := segmentLength(seg, d.size)
		if !math.IsInf(length, 0) || s.hiddenSize < 0 {
			return length
		}
		return s.hiddenSize - seg.start
	}
	var active []*segment
	for _, seg := range d.segments {
		if seg.received < lengthOf(seg) {
			active = append(active, seg)
		}
	}
	flowing := m.runSlots(s, active, dtMs, lengthOf)
	if len(flowing) > 0 && bytes > 0 {
		slots := make([]*slot, len(flowing))
		weights := make([]float64, len(flowing))
		sum := 0.0
		for i, seg := range flowing {
			for _, sl := range s.slots {
				if sl.seg == seg {
					slots[i] = sl
					break
				}
			}
			idx := 1.0
			if slots[i] != nil {
				idx = slots[i].index
			}
			weights[i] = slotWeight(idx)
			sum += weights[i]
		}
		budgetBytes := bytes
		for i, seg := range flowing {
			share := math.Ceil(budgetBytes * weights[i] / sum)
			take := min(share, lengthOf(seg)-seg.received, bytes)
			seg.received += take
			bytes -= take
			if sl := slots[i]; sl != nil {
				sl.speedBps = sl.speedBps*0.5 + take*1000/dtMs*0.5
			}
		}
	}
	d.connInfo = nil
	for _, sl := range s.slots {
		d.connInfo = append(d.connInfo, slotInfo(sl, -1))
	}
	before := d.received
	d.received = 0
	for _, seg := range d.segments {
		d.received += seg.received
	}
	instant := (d.received - before) * 1000 / dtMs
	d.speedBps = d.speedBps*0.5 + instant*0.5
	m.split(d)
	n := 0.0
	for _, sl := range s.slots {
		if sl.seg != nil {
			n++
		}
	}
	d.connections = n
	m.touch(d)

	if s.changeAt >= 0 && d.received >= s.changeAt {
		s.changeAt = -1
		s.scenarioDone = true
		s.remoteChanged = true
		m.fail(s, "validator_changed", "The file on the server changed while downloading")
		return
	}
	if s.failAt >= 0 && d.received >= s.failAt {
		s.failAt = -1
		s.scenarioDone = true
		m.fail(s, s.failCode, s.failMessage)
		return
	}
	if d.size < 0 && s.hiddenSize > 0 && d.received >= s.hiddenSize {
		d.size = d.received
		if len(d.segments) > 0 {
			d.segments[0].end = d.size - 1
		}
	}
	if d.size >= 0 && d.received >= d.size {
		d.received = d.size
		d.state = state.StateCompleted
		d.completedAt = m.now()
		m.addCompleted(s)
		d.speedBps, d.connections = 0, 0
		s.slots = nil
		d.connInfo = nil
		m.touch(d)
	}
}

// schedule is admission (M6-SPEC §1): a queued download starts when its queue runs, the
// queue has fewer than its limit (0 = the global one) running, and the global limit
// allows it; in the queue's order.
func (m *Mock) schedule() {
	running := 0
	for _, s := range m.list() {
		if isRunningState(s.d) {
			running++
		}
	}
	limit := int(m.settings.GetMaxConcurrentDownloads())
	for _, q := range m.queuesInOrder() {
		if !q.running || running >= limit {
			continue
		}
		capacity := int(q.maxConcurrent)
		if capacity == 0 {
			capacity = limit
		}
		inQueue := 0
		for _, id := range q.order {
			if s := m.sims[id]; s != nil && isRunningState(s.d) {
				inQueue++
			}
		}
		for _, id := range q.order {
			if running >= limit || inQueue >= capacity {
				break
			}
			if s := m.sims[id]; s != nil && s.d.state == state.StateQueued {
				m.start(s)
				running++
				inQueue++
			}
		}
	}
	if m.queueDirty {
		m.renumberQueue()
	}
}

// siteReady: runMedia of a web page waits in PROBING for the tools (SITE-MEDIA-SPEC
// §3.3); it fails when they cannot be set up or video pages were turned off.
func (m *Mock) siteReady(s *sim) bool {
	if !m.settings.GetSiteMedia() {
		m.fail(s, "site_media_off", "video downloads from web pages are turned off (Settings → Video sites)")
		return false
	}
	switch m.toolsPhase {
	case "ready":
		return true
	case "failed":
		m.fail(s, "tools_unavailable", "yt-dlp and Deno are not set up: could not download yt-dlp (network)")
		return false
	}
	m.startInstall()
	return false
}

func (m *Mock) step(dtMs float64) {
	m.stepTools(dtMs)
	m.schedule()
	var transferring []*sim
	for _, s := range m.list() {
		// The running time excludes pauses and the wait in the queue (Download.active_ms).
		if s.d.state == state.StateProbing || s.d.state == state.StateDownloading {
			s.d.activeMs += dtMs
		}
		if s.d.state == state.StateProbing {
			if media.IsSite(s.d.media) && !m.siteReady(s) {
				continue
			}
			s.probeLeftMs -= dtMs
			if s.probeLeftMs <= 0 {
				m.finishProbe(s)
			}
		} else if s.d.state == state.StateDownloading {
			transferring = append(transferring, s)
		}
	}
	wants := make([]float64, len(transferring))
	for i, s := range transferring {
		s.wobble = min(1.35, max(0.55, s.wobble+(m.rng()-0.5)*0.25))
		target := s.baseSpeed * s.wobble
		if s.d.speedLimit > 0 {
			target = min(target, s.d.speedLimit)
		}
		wants[i] = target
	}
	// Limits nest: download → queue → global (M6-SPEC §1).
	perQueue := map[int64]float64{}
	for i, s := range transferring {
		perQueue[s.d.queueID] += wants[i]
	}
	for i, s := range transferring {
		limit := 0.0
		if q := m.queues[s.d.queueID]; q != nil {
			limit = float64(q.speedLimit)
		}
		if sum := perQueue[s.d.queueID]; limit > 0 && sum > limit {
			wants[i] = wants[i] * limit / sum
		}
	}
	total := 0.0
	for _, w := range wants {
		total += w
	}
	scale := 1.0
	if g := float64(m.settings.GetGlobalSpeedLimit()); g > 0 && total > g {
		scale = g / total
	}
	for i, s := range transferring {
		m.transfer(s, wants[i]*scale, dtMs)
	}
	for _, s := range transferring {
		if s.d.state != state.StateDownloading {
			m.queueDirty = true
		}
		if q := m.queues[s.d.queueID]; s.d.state == state.StateCompleted && q != nil && q.running {
			q.completedThisRun = true
		}
	}
	if m.queueDirty {
		m.schedule()
	}
	m.checkFinishedQueues()
}

// ---- yt-dlp and Deno, and the pages they read (SITE-MEDIA-SPEC §3.3, §4) ----------------

type toolDef struct {
	name, version string
	bytes         float64
	verified      string
	file          string
}

func (m *Mock) toolDefs() []toolDef {
	ytdlp, deno := "yt-dlp", "deno"
	if m.sep == `\` {
		ytdlp, deno = "yt-dlp.exe", "deno.exe"
	}
	return []toolDef{
		{"yt-dlp", "2026.10.10.233141", 18 * MiB, "openpgp+sha256", ytdlp},
		{"deno", "2.5.4", 45 * MiB, "sha256+authenticode", deno},
	}
}

func (m *Mock) toolsStatus() *downexv1.ToolsStatus {
	enabled := m.settings.GetSiteMedia()
	done := 0.0
	if m.toolsPhase == "installing" {
		done = 1 - m.toolsLeftMs/max(1, m.toolsInstallMs)
	}
	st := map[string]downexv1.ToolState{
		"missing": downexv1.ToolState_TOOL_STATE_MISSING, "installing": downexv1.ToolState_TOOL_STATE_INSTALLING,
		"ready": downexv1.ToolState_TOOL_STATE_READY, "failed": downexv1.ToolState_TOOL_STATE_FAILED,
	}[m.toolsPhase]
	if !enabled {
		st = downexv1.ToolState_TOOL_STATE_DISABLED
	}
	out := &downexv1.ToolsStatus{Enabled: enabled, Ready: enabled && m.toolsPhase == "ready"}
	for _, t := range m.toolDefs() {
		info := &downexv1.ToolInfo{Name: t.name, State: st, Managed: true, CheckedAt: stamp(m.toolsCheckedAt)}
		if m.toolsPhase == "ready" {
			info.Version = t.version
			info.Path = m.settings.GetDefaultDir() + m.sep + ".downex-tools" + m.sep + t.name + m.sep + t.version + m.sep + t.file
			info.Verified = t.verified
		}
		if m.toolsCheckedAt > 0 {
			info.LatestVersion = t.version
		}
		if m.toolsPhase == "installing" {
			info.BytesDone = int64(floor(t.bytes * done))
			info.BytesTotal = int64(t.bytes)
		}
		if m.toolsPhase == "failed" {
			info.Error = "could not download " + t.name + " (network)"
			info.ErrorCode = "network"
		}
		out.Tools = append(out.Tools, info)
	}
	return out
}

func (m *Mock) toolsPercent() int {
	if m.toolsPhase != "installing" {
		return 0
	}
	return int(floor((1 - m.toolsLeftMs/max(1, m.toolsInstallMs)) * 100))
}

// startInstall starts the install unless it runs; it reports how it stands.
func (m *Mock) startInstall() string {
	if m.toolsPhase != "installing" {
		m.toolsPhase = "installing"
		m.toolsLeftMs = m.toolsInstallMs
		m.toolsChanged = true
		if m.toolsInstallMs <= 0 {
			m.finishInstall()
		}
	}
	if m.toolsPhase == "missing" {
		return "installing"
	}
	return m.toolsPhase
}

func toolsUnavailable() error {
	return apiErr(failedPrecondition, "tools_unavailable: yt-dlp and Deno are not set up: could not download yt-dlp (network)")
}

func siteMediaOff() error {
	return apiErr(failedPrecondition, "site_media_off: video downloads from web pages are turned off (Settings → Video sites)")
}

func (m *Mock) finishInstall() {
	m.toolsPhase = "ready"
	if m.opts.ToolsFail {
		m.toolsPhase = "failed"
	}
	m.toolsCheckedAt = m.now()
	m.toolsChanged = true
	waiters := m.toolsWaiters
	m.toolsWaiters = nil
	for _, w := range waiters {
		if m.toolsPhase != "ready" {
			w <- toolsUnavailable()
		}
		close(w)
	}
}

// ensureTools is Tools.Ensure: ready, or the install starts (or goes on) and the caller is
// told to come back.
func (m *Mock) ensureTools() error {
	if !m.settings.GetSiteMedia() {
		return siteMediaOff()
	}
	switch m.toolsPhase {
	case "ready":
		return nil
	case "failed":
		return toolsUnavailable()
	}
	switch m.startInstall() {
	case "ready":
		return nil
	case "failed":
		return toolsUnavailable()
	}
	return apiErr(failedPrecondition, "tools_installing: downloading yt-dlp and Deno ("+strconv.Itoa(m.toolsPercent())+"%)")
}

// resolvePage is a page's offer: from the 15-minute cache, else yt-dlp reads it
// (site_resolving meanwhile).
func (m *Mock) resolvePage(pageURL string) (*downexv1.MediaInfo, error) {
	if reason, ok := checkURL(pageURL); !ok {
		return nil, apiErr(invalidArgument, "page_url "+reason)
	}
	if err := m.ensureTools(); err != nil {
		return nil, err
	}
	if hit, ok := m.resolved[pageURL]; ok && m.now()-hit.at < 15*60_000 {
		if hit.err != nil {
			return nil, hit.err
		}
		if hit.info != nil {
			return hit.info, nil
		}
	}
	if _, ok := m.resolving[pageURL]; !ok {
		m.resolving[pageURL] = m.resolveMs
		m.resolveOrder = append(m.resolveOrder, pageURL)
	}
	if m.resolveMs <= 0 {
		return m.finishResolve(pageURL)
	}
	return nil, apiErr(failedPrecondition, "site_resolving: still getting the video info")
}

func (m *Mock) finishResolve(pageURL string) (*downexv1.MediaInfo, error) {
	delete(m.resolving, pageURL)
	m.resolveOrder = slices.DeleteFunc(m.resolveOrder, func(p string) bool { return p == pageURL })
	info, err := siteOffer(pageURL)
	m.resolved[pageURL] = resolvedPage{at: m.now(), info: info, err: err}
	return info, err
}

// stepTools: the install and the page reads go on with the simulated time.
func (m *Mock) stepTools(dtMs float64) {
	if m.toolsPhase == "installing" {
		m.toolsLeftMs -= dtMs
		m.toolsChanged = true
		if m.toolsLeftMs <= 0 {
			m.finishInstall()
		}
	}
	for _, page := range slices.Clone(m.resolveOrder) {
		left := m.resolving[page]
		if left-dtMs > 0 {
			m.resolving[page] = left - dtMs
		} else {
			_, _ = m.finishResolve(page) // kept as the page's answer
		}
	}
}
