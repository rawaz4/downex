package scene

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"downex/internal/filecheck"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/media"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/state"
)

var _ host.Daemon = (*Mock)(nil)
var _ state.Events = (*Mock)(nil)

func (m *Mock) get(id int64) (*sim, error) {
	s := m.sims[id]
	if s == nil {
		return nil, apiErr(notFoundCode, "download "+strconv.FormatInt(id, 10)+" not found")
	}
	return s, nil
}

func checkConnections(n *int32) error {
	if n != nil && (*n < 0 || *n > limitMaxConnections) {
		return apiErr(invalidArgument, "max_connections must be 0-"+strconv.Itoa(limitMaxConnections))
	}
	return nil
}

func checkSpeed(n *int64) error {
	if n != nil && (*n < 0 || *n > 1<<53-1) {
		return apiErr(invalidArgument, "speed_limit must be >= 0")
	}
	return nil
}

func i32(v int32) *int32 { return &v }
func i64(v int64) *int64 { return &v }

// run is one call: the simulated latency, then fn under the lock; the confirmation
// requests it raised are told after unlocking.
func call[T any](m *Mock, ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if err := m.respond(ctx); err != nil {
		return zero, connect.NewError(connect.CodeCanceled, err)
	}
	m.mu.Lock()
	v, err := fn()
	asks := m.pendingConfirms
	m.pendingConfirms = nil
	subs := m.confirmListeners()
	m.mu.Unlock()
	for _, id := range asks {
		for _, f := range subs {
			f(id)
		}
	}
	return v, err
}

// ---- DownloadService ----------------------------------------------------------------------

// List implements host.Daemon.
func (m *Mock) List(ctx context.Context) (*downexv1.ListResponse, error) {
	return call(m, ctx, func() (*downexv1.ListResponse, error) {
		out := &downexv1.ListResponse{}
		for _, s := range m.list() {
			out.Downloads = append(out.Downloads, s.d.clone())
		}
		return out, nil
	})
}

// Get implements host.Daemon.
func (m *Mock) Get(ctx context.Context, id int64) (*downexv1.Download, error) {
	return call(m, ctx, func() (*downexv1.Download, error) {
		s, err := m.get(id)
		if err != nil {
			return nil, err
		}
		return s.d.clone(), nil
	})
}

var sizeMarker = regexp.MustCompile(`[?&]size=(\d+)`)

func (m *Mock) addNow(req *downexv1.AddRequest) (*downexv1.Download, error) {
	if reason, ok := checkURL(req.GetUrl()); !ok {
		return nil, apiErr(invalidArgument, "url "+reason)
	}
	if method := req.GetMethod(); method != "" && method != "GET" && method != "POST" {
		return nil, apiErr(invalidArgument, "method must be GET or POST")
	}
	if err := checkConnections(i32(req.GetMaxConnections())); err != nil {
		return nil, err
	}
	if err := checkSpeed(i64(req.GetSpeedLimit())); err != nil {
		return nil, err
	}
	q, err := m.targetQueue(req.GetQueueId())
	if err != nil {
		return nil, err
	}
	filename := nameFromURL(req.GetUrl())
	if req.GetFilename() != "" {
		filename = sanitizeFilename(req.GetFilename())
	}
	category := m.chooseCategory(req.GetFilename(), req.GetUrl(), req.GetMime())
	dir := req.GetDir()
	if dir == "" {
		dir = m.categoryFolder(category)
	}
	if e := validate.CheckDir(dir); e != "" {
		return nil, apiErr(invalidArgument, "dir: "+e)
	}
	r := sizeRange[categories.ClassifyName(filename)]
	unknown := strings.Contains(req.GetUrl(), "unknown-size")
	marker := sizeMarker.FindStringSubmatch(req.GetUrl())
	hint := 0.0
	if req.GetExpectedSize() > 0 {
		hint = float64(req.GetExpectedSize())
	}
	var size float64
	switch {
	case marker != nil:
		size, _ = strconv.ParseFloat(marker[1], 64)
	case hint > 0:
		size = hint
	default:
		size = floor(m.between(r[0], r[1]))
	}
	awaiting := req.GetSource() == srcBrowser && m.settings.GetConfirmBrowserDownloads()
	dsize := size
	if unknown || awaiting {
		dsize = -1
	}
	d := m.baseDownload(req.GetUrl(), filename, dir, dsize, m.now())
	d.maxConnections = float64(req.GetMaxConnections())
	d.speedLimit = float64(req.GetSpeedLimit())
	switch {
	case awaiting:
		d.state = state.StateAwaiting
	case req.GetStartPaused():
		d.state = state.StatePaused
	default:
		d.state = state.StateQueued
	}
	d.source = req.GetSource()
	d.expectedSize = hint
	d.mime = req.GetMime()
	d.category = category
	s := m.newSim(d)
	if unknown && marker != nil {
		s.hiddenSize = size
	}
	if awaiting {
		s.pendingSize = size
		if unknown {
			s.pendingSize = -1
		}
	}
	m.addSim(s)
	m.enqueue(d, q)
	m.queueDirty = true
	m.mark(d.id)
	if awaiting {
		m.pendingConfirms = append(m.pendingConfirms, d.id)
	}
	return d.clone(), nil
}

// Add implements host.Daemon.
func (m *Mock) Add(ctx context.Context, req *downexv1.AddRequest) (*downexv1.AddResponse, error) {
	return call(m, ctx, func() (*downexv1.AddResponse, error) {
		d, err := m.addNow(req)
		if err != nil {
			return nil, err
		}
		return &downexv1.AddResponse{Download: d}, nil
	})
}

// addMediaDownload makes the download of a probed stream or page with the chosen tracks.
func (m *Mock) addMediaDownload(req *downexv1.AddMediaRequest, probed, info *downexv1.MediaInfo, rawURL, base string) (*downexv1.Download, error) {
	if req.GetFilename() != "" {
		if e := validate.CheckFilename(req.GetFilename()); e != "" {
			return nil, apiErr(invalidArgument, "filename: "+e)
		}
	}
	q, err := m.targetQueue(req.GetQueueId())
	if err != nil {
		return nil, err
	}
	category := m.mediaCategory(info)
	dir := req.GetDir()
	if dir == "" {
		dir = m.categoryFolder(category)
	}
	if e := validate.CheckDir(dir); e != "" {
		return nil, apiErr(invalidArgument, "dir: "+e)
	}
	awaiting := req.GetSource() == srcBrowser && m.settings.GetConfirmBrowserDownloads()
	d := m.baseDownload(rawURL, mediaFilename(base), dir, -1, m.now())
	d.kind = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
	d.media = info
	d.category = category
	d.source = req.GetSource()
	switch {
	case awaiting:
		d.state = state.StateAwaiting
	case req.GetStartPaused():
		d.state = state.StatePaused
	default:
		d.state = state.StateQueued
	}
	s := m.newSim(d)
	s.media = &mediaSim{probed: probed, totalBytes: mediaBytes(info)}
	if media.IsSite(info) {
		d.expectedSize = s.media.totalBytes
	}
	m.addSim(s)
	m.enqueue(d, q)
	m.queueDirty = true
	m.mark(d.id)
	if awaiting {
		m.pendingConfirms = append(m.pendingConfirms, d.id)
	}
	return d.clone(), nil
}

// addSiteNow is AddMedia of a web page (origin site): the offer of its page (or a
// fixture's offer), the chosen MP4 quality, one .mp4.
func (m *Mock) addSiteNow(req *downexv1.AddMediaRequest, offer *downexv1.MediaInfo) (*downexv1.Download, error) {
	if req.GetManifestUrl() != "" {
		return nil, apiErr(invalidArgument, "manifest_url: must be empty for a web page")
	}
	if _, err := m.targetQueue(req.GetQueueId()); err != nil {
		return nil, err
	}
	// add:slow: the page's offer left the cache, and yt-dlp reads the page again
	if offer == nil && m.now() < m.addSlowUntil {
		return nil, apiErr(failedPrecondition, "site_resolving: still getting the video info")
	}
	probed := offer
	if probed == nil {
		var err error
		if probed, err = m.resolvePage(req.GetPageUrl()); err != nil {
			return nil, err
		}
	}
	p, err := choosePick(probed, req.GetVideoTrack(), req.GetAudioTrack())
	if err != nil {
		return nil, err
	}
	info := withPick(probed, p)
	base := req.GetFilename()
	for _, b := range []string{probed.GetTitle(), req.GetTitle(), "video"} {
		if base != "" {
			break
		}
		base = b
	}
	return m.addMediaDownload(req, probed, info, probed.GetPageUrl(), base)
}

func (m *Mock) addMediaNow(req *downexv1.AddMediaRequest) (*downexv1.Download, error) {
	if req.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE {
		return m.addSiteNow(req, nil)
	}
	if _, err := m.targetQueue(req.GetQueueId()); err != nil {
		return nil, err
	}
	probed, err := probeStream(req.GetManifestUrl())
	if err != nil {
		return nil, err
	}
	p, err := choosePick(probed, req.GetVideoTrack(), req.GetAudioTrack())
	if err != nil {
		return nil, err
	}
	info := withPick(probed, p)
	if req.GetTitle() != "" {
		info.Title = req.GetTitle()
	}
	base := req.GetFilename()
	if base == "" {
		base = info.GetTitle()
	}
	return m.addMediaDownload(req, probed, info, req.GetManifestUrl(), base)
}

// AddMedia implements host.Daemon.
func (m *Mock) AddMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.AddMediaResponse, error) {
	return call(m, ctx, func() (*downexv1.AddMediaResponse, error) {
		d, err := m.addMediaNow(req)
		if err != nil {
			return nil, err
		}
		return &downexv1.AddMediaResponse{Download: d}, nil
	})
}

// ProbeMedia implements host.Daemon.
func (m *Mock) ProbeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.ProbeMediaResponse, error) {
	return call(m, ctx, func() (*downexv1.ProbeMediaResponse, error) {
		var info *downexv1.MediaInfo
		var err error
		switch {
		case req.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE:
			info, err = probeStream(req.GetManifestUrl())
		case req.GetManifestUrl() != "":
			err = apiErr(invalidArgument, "manifest_url: must be empty for a web page")
		default:
			info, err = m.resolvePage(req.GetPageUrl())
		}
		if err != nil {
			return nil, err
		}
		return &downexv1.ProbeMediaResponse{Media: proto.CloneOf(info)}, nil
	})
}

// Confirm implements host.Daemon.
func (m *Mock) Confirm(ctx context.Context, req *downexv1.ConfirmRequest) (*downexv1.ConfirmResponse, error) {
	return call(m, ctx, func() (*downexv1.ConfirmResponse, error) {
		s, err := m.get(req.GetId())
		if err != nil {
			return nil, err
		}
		d := s.d
		if d.state != state.StateAwaiting {
			return nil, apiErr(failedPrecondition, fmt.Sprintf("download %d is not awaiting confirmation", d.id))
		}
		if m.confirmRefusal != nil {
			return nil, m.confirmRefusal
		}
		if err := checkConnections(req.MaxConnections); err != nil {
			return nil, err
		}
		if err := checkSpeed(req.SpeedLimit); err != nil {
			return nil, err
		}
		dir := req.GetDir()
		if dir == "" {
			dir = d.dir
		}
		e := validate.CheckDir(dir)
		if e == "" && dir == "" {
			e = "must be absolute"
		}
		if e != "" {
			return nil, apiErr(invalidArgument, "dir: "+e)
		}
		if req.GetFilename() != "" {
			if e := validate.CheckFilename(req.GetFilename()); e != "" {
				return nil, apiErr(invalidArgument, "filename: "+e)
			}
			if name := sanitizeFilename(req.GetFilename()); name != d.filename && d.fileCheck != nil {
				// downexd judges the user's own name again (DESKTOP-SPEC §9.5)
				d.fileCheck = protoCheck(filecheck.CheckName(name, false, d.mime), name)
			}
			d.filename = sanitizeFilename(req.GetFilename())
		}
		switch {
		case s.media != nil && d.media != nil:
			// Empty = keep the daemon's pick (a muxed video then takes no audio track). A web
			// page's video stays one .mp4 whatever is chosen: its name keeps its extension.
			video, audio := req.GetVideoTrack(), req.GetAudioTrack()
			if video == "" {
				video = d.media.GetSelectedVideo()
			}
			if audio == "" {
				audio = d.media.GetSelectedAudio()
			}
			p, err := choosePick(s.media.probed, video, audio)
			if err != nil {
				return nil, err
			}
			title := d.media.GetTitle()
			d.media = withPick(s.media.probed, p)
			d.media.Title = title
			if d.category == "" {
				d.category = m.mediaCategory(d.media)
			}
			s.media.totalBytes = mediaBytes(d.media)
			if media.IsSite(d.media) {
				d.expectedSize = s.media.totalBytes
			}
		case req.GetVideoTrack() != "" || req.GetAudioTrack() != "":
			return nil, apiErr(invalidArgument, fmt.Sprintf("download %d is not a stream: it has no tracks", d.id))
		}
		var q *mqueue
		if req.GetQueueId() != 0 {
			if q, err = m.getQueue(req.GetQueueId()); err != nil {
				return nil, err
			}
		}
		d.dir = dir
		if req.MaxConnections != nil {
			d.maxConnections = float64(req.GetMaxConnections())
		}
		if req.SpeedLimit != nil {
			d.speedLimit = float64(req.GetSpeedLimit())
		}
		d.size = s.pendingSize
		if q != nil {
			m.enqueue(d, q)
		}
		// As downexd: a named queue takes it queued (it waits there until the queue runs).
		d.state = state.StatePaused
		if req.GetStart() || q != nil {
			d.state = state.StateQueued
		}
		m.queueDirty = true
		m.touch(d)
		return &downexv1.ConfirmResponse{Download: d.clone()}, nil
	})
}

// PauseAll implements host.Daemon.
func (m *Mock) PauseAll(ctx context.Context) (*downexv1.PauseAllResponse, error) {
	return call(m, ctx, func() (*downexv1.PauseAllResponse, error) {
		var n int32
		for _, s := range m.list() {
			d := s.d
			if d.state != state.StateQueued && d.state != state.StateProbing && d.state != state.StateDownloading {
				continue
			}
			d.state, d.speedBps, d.connections, d.queuePosition = state.StatePaused, 0, 0, 0
			m.touch(d)
			n++
		}
		m.queueDirty = true
		return &downexv1.PauseAllResponse{Paused: n}, nil
	})
}

// ResumeAll implements host.Daemon.
func (m *Mock) ResumeAll(ctx context.Context) (*downexv1.ResumeAllResponse, error) {
	return call(m, ctx, func() (*downexv1.ResumeAllResponse, error) {
		var n int32
		for _, s := range m.list() {
			if s.d.state != state.StatePaused {
				continue // as the daemon: paused downloads only
			}
			s.d.state = state.StateQueued
			m.touch(s.d)
			n++
		}
		m.queueDirty = true
		return &downexv1.ResumeAllResponse{Resumed: n}, nil
	})
}

// Pause implements host.Daemon.
func (m *Mock) Pause(ctx context.Context, req *downexv1.PauseRequest) (*downexv1.PauseResponse, error) {
	return call(m, ctx, func() (*downexv1.PauseResponse, error) {
		s, err := m.get(req.GetId())
		if err != nil {
			return nil, err
		}
		d := s.d
		if d.state == state.StatePaused {
			return &downexv1.PauseResponse{Download: d.clone()}, nil
		}
		if d.state != state.StateQueued && d.state != state.StateProbing && d.state != state.StateDownloading {
			return nil, apiErr(failedPrecondition, fmt.Sprintf("download %d is %s", d.id, stateName(d.state)))
		}
		d.state, d.speedBps, d.connections, d.queuePosition = state.StatePaused, 0, 0, 0
		m.queueDirty = true
		m.touch(d)
		return &downexv1.PauseResponse{Download: d.clone()}, nil
	})
}

// freeVariant is the first "name (n).ext" no download in the same folder holds.
func (m *Mock) freeVariant(d *dl) string {
	taken := map[string]bool{}
	for _, s := range m.list() {
		if s.d.dir == d.dir {
			taken[strings.ToLower(s.d.filename)] = true
		}
	}
	for n := 1; ; n++ {
		if c := state.NumberedName(d.filename, n); !taken[strings.ToLower(c)] {
			return c
		}
	}
}

// Resume implements host.Daemon.
func (m *Mock) Resume(ctx context.Context, req *downexv1.ResumeRequest) (*downexv1.ResumeResponse, error) {
	return call(m, ctx, func() (*downexv1.ResumeResponse, error) {
		s, err := m.get(req.GetId())
		if err != nil {
			return nil, err
		}
		d := s.d
		if req.GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_UNSPECIFIED && !req.GetRestart() {
			return nil, apiErr(invalidArgument, "existing_file is only for a restart")
		}
		again := d.state == state.StateCompleted && req.GetRestart()
		if d.state == state.StateCompleted && !again {
			return nil, apiErr(failedPrecondition, fmt.Sprintf("download %d is already completed", d.id))
		}
		if !again && !state.IsResumable(d.state) {
			return nil, apiErr(failedPrecondition, fmt.Sprintf("download %d is %s", d.id, stateName(d.state)))
		}
		if err := checkConnections(req.MaxConnections); err != nil {
			return nil, err
		}
		if err := checkSpeed(req.SpeedLimit); err != nil {
			return nil, err
		}
		if req.MaxConnections != nil {
			d.maxConnections = float64(req.GetMaxConnections())
		}
		if req.SpeedLimit != nil {
			d.speedLimit = float64(req.GetSpeedLimit())
		}
		if again {
			// As downexd (ENGINE-SPEC §7.1): replace keeps the name and frees the old copy's
			// space at completion; keep both saves under a free name; a file that is gone is
			// downloaded again under its own name.
			there := !m.gone[d.id]
			delete(m.gone, d.id)
			replace := there && req.GetExistingFile() == downexv1.ExistingFile_EXISTING_FILE_REPLACE
			s.replaceBytes = -1
			if replace {
				s.replaceBytes = max(0, d.size)
			}
			if there && !replace {
				d.filename = m.freeVariant(d)
			}
			d.completedAt = 0
			s.scenarioDone = false
		}
		if req.GetRestart() {
			d.received = 0
			d.segments = nil
			d.mode = downexv1.Mode_MODE_UNSPECIFIED
			if d.media != nil {
				next := cloneMedia(d.media)
				next.FragmentsDone = 0
				d.media = next
				d.size = -1
			}
			s.remoteChanged = false
			s.changeAt = -1
			if d.size >= 0 && s.hiddenSize > 0 {
				d.size = -1
			}
		}
		d.state = state.StateQueued
		d.err, d.errCode = "", ""
		m.queueDirty = true
		m.touch(d)
		return &downexv1.ResumeResponse{Download: d.clone()}, nil
	})
}

// Remove implements host.Daemon.
func (m *Mock) Remove(ctx context.Context, req *downexv1.RemoveRequest) (*downexv1.RemoveResponse, error) {
	return call(m, ctx, func() (*downexv1.RemoveResponse, error) {
		s, err := m.get(req.GetId())
		if err != nil {
			return nil, err
		}
		id := s.d.id
		if q := m.queues[s.d.queueID]; q != nil {
			q.order = slices.DeleteFunc(slices.Clone(q.order), func(x int64) bool { return x == id })
			m.markQueue(q.id)
		}
		m.deleteSim(id)
		if m.changedS[id] {
			delete(m.changedS, id)
			m.changed = slices.DeleteFunc(m.changed, func(x int64) bool { return x == id })
		}
		m.removed = append(m.removed, id)
		m.queueDirty = true
		return &downexv1.RemoveResponse{}, nil
	})
}

// Rescue implements host.Daemon for the demo's rescue of a partial file
// (docs/DESKTOP-SPEC.md §11): an add of its address named after the partial (without
// .crdownload or .part) that keeps 40 % of its size.
func (m *Mock) Rescue(ctx context.Context, req *downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
	return call(m, ctx, func() (*downexv1.RescueResponse, error) {
		p := req.GetPartialPath()
		if p == "" {
			return nil, apiErr(invalidArgument, "partial_path is required")
		}
		name := req.GetFilename()
		if name == "" {
			name = path.Base(strings.ReplaceAll(p, `\`, "/"))
			for _, suf := range []string{".crdownload", ".part"} {
				name = strings.TrimSuffix(name, suf)
			}
		}
		d, err := m.addNow(&downexv1.AddRequest{Url: req.GetUrl(), Headers: req.GetHeaders(), Filename: name,
			MaxConnections: req.GetMaxConnections(), SpeedLimit: req.GetSpeedLimit(), StartPaused: req.GetStartPaused(),
			QueueId: req.GetQueueId(), Source: downexv1.AddSource_ADD_SOURCE_UI})
		if err != nil {
			return nil, err
		}
		kept := max(d.GetSize(), 0) * 2 / 5
		d.RescuedBytes = kept
		return &downexv1.RescueResponse{Download: d, RescuedBytes: kept}, nil
	})
}

// RenewLink implements host.Daemon as downexd does for a new link that serves the same
// file (docs/DESKTOP-SPEC.md §10): the download continues from where it stopped; with
// from_download that download is removed. A link containing "other" serves another file.
func (m *Mock) RenewLink(ctx context.Context, req *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
	return call(m, ctx, func() (*downexv1.RenewLinkResponse, error) {
		s, err := m.get(req.GetId())
		if err != nil {
			return nil, err
		}
		d := s.d
		switch {
		case d.kind == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA:
			return nil, apiErr(invalidArgument, fmt.Sprintf("download %d is a stream or web page video: it cannot take a new link", d.id))
		case d.state != state.StatePaused && d.state != state.StateFailed && d.state != state.StateQueued:
			return nil, apiErr(failedPrecondition, fmt.Sprintf("download %d is %s", d.id, stateName(d.state)))
		}
		u := req.GetUrl()
		var from *sim
		if id := req.GetFromDownload(); id != 0 {
			if from, err = m.get(id); err != nil {
				return nil, err
			}
			u = from.d.url
		} else if _, ok := checkURL(u); !ok {
			return nil, apiErr(invalidArgument, "url scheme must be http or https")
		}
		if strings.Contains(u, "other") {
			ce := connect.NewError(failedPrecondition, errors.New("renew_mismatch: bytes 0-65535 differ: engine: the new link serves different data"))
			ce.Meta().Set("Downex-Error-Code", "renew_mismatch")
			return nil, ce
		}
		d.url, d.err, d.errCode = u, "", ""
		d.state = state.StatePaused
		if req.GetStart() {
			d.state = state.StateQueued
		}
		if req.GetPageUrl() != "" {
			d.pageURL = req.GetPageUrl()
		}
		m.queueDirty = true
		m.touch(d)
		if from != nil {
			id := from.d.id
			if q := m.queues[from.d.queueID]; q != nil {
				q.order = slices.DeleteFunc(slices.Clone(q.order), func(x int64) bool { return x == id })
				m.markQueue(q.id)
			}
			m.deleteSim(id)
			m.removed = append(m.removed, id)
		}
		return &downexv1.RenewLinkResponse{Download: d.clone()}, nil
	})
}

// SetDownloadOptions implements host.Daemon.
func (m *Mock) SetDownloadOptions(ctx context.Context, req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error) {
	return call(m, ctx, func() (*downexv1.SetDownloadOptionsResponse, error) {
		s, err := m.get(req.GetId())
		if err != nil {
			return nil, err
		}
		d := s.d
		if err := checkConnections(req.MaxConnections); err != nil {
			return nil, err
		}
		if err := checkSpeed(req.SpeedLimit); err != nil {
			return nil, err
		}
		// As downexd: a completed download takes no options.
		if d.state == state.StateCompleted {
			return nil, apiErr(failedPrecondition, fmt.Sprintf("download %d is already completed", d.id))
		}
		if req.SpeedLimit != nil {
			d.speedLimit = float64(req.GetSpeedLimit())
		}
		if req.MaxConnections != nil {
			d.maxConnections = float64(req.GetMaxConnections())
		}
		// Only ever set: a download whose progress window was opened keeps the dialog.
		if req.GetCompletionDialog() {
			d.completionDialog = true
		}
		m.touch(d)
		return &downexv1.SetDownloadOptionsResponse{Download: d.clone()}, nil
	})
}

// settingsView is the settings as GetSettings reports them: with whether passwords are
// stored.
func (m *Mock) settingsView() *downexv1.Settings {
	s := proto.CloneOf(m.settings)
	if s.Proxy == nil {
		s.Proxy = &downexv1.ProxySettings{}
	}
	s.Proxy.HasPassword = m.proxyPassword
	for _, l := range s.SiteLogins {
		l.HasPassword = m.loginPasswords[l.GetId()]
	}
	return s
}

// GetSettings implements host.Daemon.
func (m *Mock) GetSettings(ctx context.Context) (*downexv1.GetSettingsResponse, error) {
	return call(m, ctx, func() (*downexv1.GetSettingsResponse, error) {
		return &downexv1.GetSettingsResponse{Settings: m.settingsView()}, nil
	})
}

var (
	reHTTPS   = regexp.MustCompile(`(?i)^https://[^/]`)
	reAbsPath = regexp.MustCompile(`^([A-Za-z]:[\\/]|/)`)
)

func checkCategories(next *downexv1.Settings) error {
	var drafts []validate.CategoryDraft
	for i, c := range next.GetCategories() {
		drafts = append(drafts, validate.CategoryDraft{Key: i, Name: c.GetName(), Extensions: strings.Join(c.GetExtensions(), " "), Dir: c.GetDir()})
	}
	res := validate.ValidateCategories(drafts)
	if res.OK && len(res.Categories) == len(next.GetCategories()) {
		same := true
		for i, c := range res.Categories {
			n := next.GetCategories()[i]
			if c.GetName() != n.GetName() || strings.Join(c.GetExtensions(), " ") != strings.Join(n.GetExtensions(), " ") {
				same = false
			}
		}
		if same {
			return nil
		}
	}
	return apiErr(invalidArgument, "categories: names must be unique and non-empty, extensions [a-z0-9]{1,16}, folders absolute")
}

// UpdateSettings implements host.Daemon.
func (m *Mock) UpdateSettings(ctx context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	return call(m, ctx, func() (*downexv1.UpdateSettingsResponse, error) {
		next := req.GetSettings()
		if c := next.GetMaxConcurrentDownloads(); c < limitMinConcurrent || c > limitMaxConcurrent {
			return nil, apiErr(invalidArgument, "max_concurrent_downloads must be 1-16")
		}
		if c := next.GetDefaultMaxConnections(); c < limitMinConnections || c > limitMaxConnections {
			return nil, apiErr(invalidArgument, "default_max_connections must be 1-32")
		}
		if err := checkSpeed(i64(next.GetGlobalSpeedLimit())); err != nil {
			return nil, err
		}
		e := "must be absolute"
		if next.GetDefaultDir() != "" {
			e = validate.CheckDir(next.GetDefaultDir())
		}
		if e != "" {
			return nil, apiErr(invalidArgument, "default_dir: "+e)
		}
		if err := checkCategories(next); err != nil {
			return nil, err
		}
		if u := next.GetUpdateFeedUrl(); u != "" && !reHTTPS.MatchString(u) {
			return nil, apiErr(invalidArgument, "update_feed_url: must be an https address")
		}
		if next.GetAvScanEnabled() && !reAbsPath.MatchString(next.GetAvScanCommand()) {
			return nil, apiErr(invalidArgument, "av_scan_command: must be an absolute path")
		}
		s := proto.CloneOf(next)
		// Site logins get their ids here; passwords of removed ones are dropped.
		for _, l := range s.GetSiteLogins() {
			if l.GetId() == "" {
				l.Id = "login-" + strconv.Itoa(m.nextLogin)
				m.nextLogin++
			}
		}
		for id := range m.loginPasswords {
			if !slices.ContainsFunc(s.GetSiteLogins(), func(l *downexv1.SiteLogin) bool { return l.GetId() == id }) {
				delete(m.loginPasswords, id)
			}
		}
		if s.GetPoliteMode() != m.settings.GetPoliteMode() {
			m.politeChanged = true
			if !s.GetPoliteMode() {
				m.politeFixed = nil // switched off: the fixture's status no longer applies
			}
		}
		m.settings = s
		return &downexv1.UpdateSettingsResponse{Settings: m.settingsView()}, nil
	})
}

// Status implements host.Daemon.
func (m *Mock) Status(ctx context.Context) (*downexv1.StatusResponse, error) {
	return call(m, ctx, func() (*downexv1.StatusResponse, error) {
		var active, queued int32
		for _, s := range m.list() {
			switch s.d.state {
			case state.StateDownloading, state.StateProbing:
				active++
			case state.StateQueued:
				queued++
			}
		}
		f := m.folders()
		return &downexv1.StatusResponse{Version: "0.0.0-mock", Pid: 4242, StartedAt: stamp(m.startedAt), Active: active, Queued: queued,
			Endpoint: "mock://downexd", DataDir: f.data, LogDir: f.logs, ToolsDir: f.tools, Polite: m.politeStatusLocked()}, nil
	})
}

// Watch implements host.Daemon: a snapshot of the downloads ids (none: all, and the
// tools' state), then the batches, filtered to those downloads when ids name some. An id
// that names no download is refused with not_found, as downexd does.
func (m *Mock) Watch(ctx context.Context, ids []int64) (host.WatchStream, error) {
	if err := m.respond(ctx); err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	m.mu.Lock()
	for _, id := range ids {
		if m.sims[id] == nil {
			m.mu.Unlock()
			return nil, apiErr(notFoundCode, "download "+strconv.FormatInt(id, 10)+" not found")
		}
	}
	m.mu.Unlock()
	st := newStream(ctx)
	keep := func(b state.Batch) state.Batch {
		if len(ids) == 0 {
			return b
		}
		var out state.Batch
		for _, ev := range b {
			var id int64
			switch k := ev.GetKind().(type) {
			case *downexv1.Event_Snapshot:
				id = k.Snapshot.GetId()
			case *downexv1.Event_Updated:
				id = k.Updated.GetId()
			case *downexv1.Event_Removed:
				id = k.Removed
			default:
				continue
			}
			if slices.Contains(ids, id) {
				out = append(out, ev)
			}
		}
		return out
	}
	unsub := m.Subscribe(func(b state.Batch) {
		if b = keep(b); len(b) > 0 {
			st.push(&downexv1.WatchResponse{Events: b})
		}
	})
	st.onClose(unsub)
	return st, nil
}

// ---- QueueService and the rest --------------------------------------------------------

func checkQueueSpec(m *Mock, spec *downexv1.Queue, self int64) error {
	name := strings.TrimSpace(spec.GetName())
	if name == "" || utf8.RuneCountInString(name) > limitQueueName {
		return apiErr(invalidArgument, "name: must be 1-"+strconv.Itoa(limitQueueName)+" characters")
	}
	for _, q := range m.queuesInOrder() {
		if q.id != self && strings.EqualFold(q.name, name) {
			return apiErr(connect.CodeAlreadyExists, `a queue named "`+name+`" exists`)
		}
	}
	if c := spec.GetMaxConcurrent(); c < 0 || c > limitQueueConcurr {
		return apiErr(invalidArgument, "max_concurrent must be 0-"+strconv.Itoa(limitQueueConcurr))
	}
	if err := checkSpeed(i64(spec.GetSpeedLimit())); err != nil {
		return err
	}
	s := spec.GetSchedule()
	minute := func(v int32) bool { return v >= 0 && v < 1440 }
	if s.GetEnabled() && (!minute(s.GetStartMinute()) || s.GetStopEnabled() && !minute(s.GetStopMinute())) {
		return apiErr(invalidArgument, "schedule: times must be 0-1439 minutes")
	}
	if s.GetEnabled() && s.GetWeekdays() == 0 && !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(s.GetOnceDate()) {
		return apiErr(invalidArgument, "schedule: once_date must be YYYY-MM-DD")
	}
	return nil
}

// ListQueues implements host.Daemon.
func (m *Mock) ListQueues(ctx context.Context) (*downexv1.ListQueuesResponse, error) {
	return call(m, ctx, func() (*downexv1.ListQueuesResponse, error) {
		out := &downexv1.ListQueuesResponse{}
		for _, q := range m.queuesInOrder() {
			out.Queues = append(out.Queues, q.clone())
		}
		return out, nil
	})
}

// CreateQueue implements host.Daemon.
func (m *Mock) CreateQueue(ctx context.Context, req *downexv1.CreateQueueRequest) (*downexv1.CreateQueueResponse, error) {
	return call(m, ctx, func() (*downexv1.CreateQueueResponse, error) {
		spec := req.GetQueue()
		if err := checkQueueSpec(m, spec, 0); err != nil {
			return nil, err
		}
		q := m.newQueue(strings.TrimSpace(spec.GetName()), spec.GetMaxConcurrent(), spec.GetSpeedLimit(), spec.GetSchedule(), spec.GetPostAction(), false, false)
		return &downexv1.CreateQueueResponse{Queue: q.clone()}, nil
	})
}

// UpdateQueue implements host.Daemon.
func (m *Mock) UpdateQueue(ctx context.Context, req *downexv1.UpdateQueueRequest) (*downexv1.UpdateQueueResponse, error) {
	return call(m, ctx, func() (*downexv1.UpdateQueueResponse, error) {
		spec := req.GetQueue()
		q, err := m.getQueue(spec.GetId())
		if err != nil {
			return nil, err
		}
		if err := checkQueueSpec(m, spec, q.id); err != nil {
			return nil, err
		}
		q.name = strings.TrimSpace(spec.GetName())
		q.maxConcurrent, q.speedLimit = spec.GetMaxConcurrent(), spec.GetSpeedLimit()
		q.schedule, q.postAction = cloneSchedule(spec.GetSchedule()), spec.GetPostAction()
		m.markQueue(q.id)
		m.queueDirty = true
		return &downexv1.UpdateQueueResponse{Queue: q.clone()}, nil
	})
}

// DeleteQueue implements host.Daemon.
func (m *Mock) DeleteQueue(ctx context.Context, req *downexv1.DeleteQueueRequest) (*downexv1.DeleteQueueResponse, error) {
	return call(m, ctx, func() (*downexv1.DeleteQueueResponse, error) {
		q, err := m.getQueue(req.GetId())
		if err != nil {
			return nil, err
		}
		if q.isMain {
			return nil, apiErr(failedPrecondition, "the main queue cannot be deleted")
		}
		main, _ := m.getQueue(state.MainQueueID)
		for _, did := range q.order {
			s := m.sims[did]
			if s == nil {
				continue
			}
			main.order = append(main.order, did)
			s.d.queueID = main.id
			m.touch(s.d)
		}
		delete(m.queues, q.id)
		delete(m.countdowns, q.id)
		m.unmarkQueue(q.id)
		m.markQueue(main.id)
		m.removedQueues = append(m.removedQueues, q.id)
		m.queueDirty = true
		return &downexv1.DeleteQueueResponse{}, nil
	})
}

// StartQueue implements host.Daemon.
func (m *Mock) StartQueue(ctx context.Context, req *downexv1.StartQueueRequest) (*downexv1.StartQueueResponse, error) {
	return call(m, ctx, func() (*downexv1.StartQueueResponse, error) {
		q, err := m.getQueue(req.GetId())
		if err != nil {
			return nil, err
		}
		if !q.running {
			q.running, q.completedThisRun = true, false
			m.markQueue(q.id)
			m.queueDirty = true
		}
		return &downexv1.StartQueueResponse{Queue: q.clone()}, nil
	})
}

// StopQueue implements host.Daemon.
func (m *Mock) StopQueue(ctx context.Context, req *downexv1.StopQueueRequest) (*downexv1.StopQueueResponse, error) {
	return call(m, ctx, func() (*downexv1.StopQueueResponse, error) {
		q, err := m.getQueue(req.GetId())
		if err != nil {
			return nil, err
		}
		q.running = false
		// Its running downloads stop (with what they received) and wait in the queue.
		for _, did := range q.order {
			s := m.sims[did]
			if s == nil || !isRunningState(s.d) {
				continue
			}
			s.d.state, s.d.speedBps, s.d.connections = state.StateQueued, 0, 0
			s.slots, s.d.connInfo = nil, nil
			m.touch(s.d)
		}
		m.markQueue(q.id)
		m.queueDirty = true
		return &downexv1.StopQueueResponse{Queue: q.clone()}, nil
	})
}

// MoveDownloads implements host.Daemon.
func (m *Mock) MoveDownloads(ctx context.Context, req *downexv1.MoveDownloadsRequest) (*downexv1.MoveDownloadsResponse, error) {
	return call(m, ctx, func() (*downexv1.MoveDownloadsResponse, error) {
		target, err := m.getQueue(req.GetQueueId())
		if err != nil {
			return nil, err
		}
		var moving []int64
		for _, id := range req.GetIds() {
			if m.sims[id] != nil {
				moving = append(moving, id)
			}
		}
		if len(moving) != len(req.GetIds()) {
			return nil, apiErr(notFoundCode, "a download to move does not exist")
		}
		for _, q := range m.queuesInOrder() {
			kept := slices.DeleteFunc(slices.Clone(q.order), func(x int64) bool { return slices.Contains(moving, x) })
			if len(kept) != len(q.order) {
				q.order = kept
				m.markQueue(q.id)
			}
		}
		at := int(req.GetPosition())
		if at < 0 || at > len(target.order) {
			at = len(target.order)
		}
		target.order = slices.Insert(target.order, at, moving...)
		for _, id := range moving {
			if d := m.sims[id].d; d.queueID != target.id {
				d.queueID = target.id
				m.touch(d)
			}
		}
		m.markQueue(target.id)
		m.queueDirty = true
		return &downexv1.MoveDownloadsResponse{Queue: target.clone()}, nil
	})
}

// CancelPostAction implements host.Daemon.
func (m *Mock) CancelPostAction(ctx context.Context, req *downexv1.CancelPostActionRequest) (*downexv1.CancelPostActionResponse, error) {
	return call(m, ctx, func() (*downexv1.CancelPostActionResponse, error) {
		if _, err := m.getQueue(req.GetQueueId()); err != nil {
			return nil, err
		}
		if c := m.countdowns[req.GetQueueId()]; c != nil {
			delete(m.countdowns, req.GetQueueId())
			m.postEvents = append(m.postEvents, &downexv1.PostActionPending{QueueId: req.GetQueueId(), Action: c.action, Deadline: stamp(c.deadline), Cancelled: true})
		}
		return &downexv1.CancelPostActionResponse{}, nil
	})
}

// SetSecret implements host.Daemon (only whether one is stored is kept).
func (m *Mock) SetSecret(ctx context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error) {
	return call(m, ctx, func() (*downexv1.SetSecretResponse, error) {
		if req.GetPassword() == "" {
			return nil, apiErr(invalidArgument, "password: must not be empty")
		}
		switch {
		case req.GetKind() == "proxy":
			m.proxyPassword = true
		case slices.ContainsFunc(m.settings.GetSiteLogins(), func(l *downexv1.SiteLogin) bool { return l.GetId() == req.GetId() }):
			m.loginPasswords[req.GetId()] = true
		default:
			return nil, apiErr(notFoundCode, "site login "+req.GetId()+" not found")
		}
		return &downexv1.SetSecretResponse{}, nil
	})
}

// ClearSecret implements host.Daemon.
func (m *Mock) ClearSecret(ctx context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error) {
	return call(m, ctx, func() (*downexv1.ClearSecretResponse, error) {
		if req.GetKind() == "proxy" {
			m.proxyPassword = false
		} else {
			delete(m.loginPasswords, req.GetId())
		}
		return &downexv1.ClearSecretResponse{}, nil
	})
}

func (m *Mock) updateInfo() (*downexv1.UpdateInfo, error) {
	if m.settings.GetUpdateFeedUrl() == "" {
		return nil, apiErr(failedPrecondition, "updates are off: set the address of an update feed first")
	}
	return &downexv1.UpdateInfo{CurrentVersion: "0.0.0-mock", LatestVersion: "0.0.1-mock", Available: true,
		Notes: "A simulated update: queues, schedules and site logins.", PublishedAt: stamp(m.startedAt - 86_400_000), CheckedAt: stamp(m.now())}, nil
}

// CheckForUpdate implements host.Daemon: the feed always offers a newer version.
func (m *Mock) CheckForUpdate(ctx context.Context) (*downexv1.CheckForUpdateResponse, error) {
	return call(m, ctx, func() (*downexv1.CheckForUpdateResponse, error) {
		info, err := m.updateInfo()
		if err != nil {
			return nil, err
		}
		return &downexv1.CheckForUpdateResponse{Info: info}, nil
	})
}

// InstallUpdate implements host.Daemon: nothing to install, answered as if the installer
// started.
func (m *Mock) InstallUpdate(ctx context.Context) (*downexv1.InstallUpdateResponse, error) {
	return call(m, ctx, func() (*downexv1.InstallUpdateResponse, error) {
		info, err := m.updateInfo()
		if err != nil {
			return nil, err
		}
		return &downexv1.InstallUpdateResponse{Info: info}, nil
	})
}

// GetTools implements host.Daemon.
func (m *Mock) GetTools(ctx context.Context) (*downexv1.GetToolsResponse, error) {
	return call(m, ctx, func() (*downexv1.GetToolsResponse, error) {
		return &downexv1.GetToolsResponse{Status: m.toolsStatus()}, nil
	})
}

// InstallTools implements host.Daemon. The user's install ignores the backoff of automatic
// ones; an update of ready copies only looks for newer releases (there are none). It
// answers when the install ends (the simulation's ticks run it), or when ctx does.
func (m *Mock) InstallTools(ctx context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error) {
	if err := m.respond(ctx); err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	m.mu.Lock()
	if !m.settings.GetSiteMedia() {
		m.mu.Unlock()
		return nil, siteMediaOff()
	}
	if m.toolsPhase == "ready" {
		if req.GetUpdate() {
			m.toolsCheckedAt = m.now()
			m.toolsChanged = true
		}
		st := m.toolsStatus()
		m.mu.Unlock()
		return &downexv1.InstallToolsResponse{Status: st}, nil
	}
	if m.toolsPhase == "failed" {
		m.toolsPhase = "missing"
	}
	switch m.startInstall() {
	case "ready":
		st := m.toolsStatus()
		m.mu.Unlock()
		return &downexv1.InstallToolsResponse{Status: st}, nil
	case "failed":
		m.mu.Unlock()
		return nil, toolsUnavailable()
	}
	w := make(toolsWaiter, 1)
	m.toolsWaiters = append(m.toolsWaiters, w)
	m.mu.Unlock()
	select {
	case err, ok := <-w:
		if ok && err != nil {
			return nil, err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.toolsPhase != "ready" {
			return nil, toolsUnavailable()
		}
		return &downexv1.InstallToolsResponse{Status: m.toolsStatus()}, nil
	case <-ctx.Done():
		m.mu.Lock()
		defer m.mu.Unlock()
		return &downexv1.InstallToolsResponse{Status: m.toolsStatus()}, nil
	}
}

// ---- the host's part of the demo --------------------------------------------------------

type folderSet struct{ data, logs, tools string }

var reDownloads = regexp.MustCompile(`(?i)[\\/]Downloads[\\/]?$`)

func (m *Mock) home() string {
	h := reDownloads.ReplaceAllString(m.settings.GetDefaultDir(), "")
	if h != "" {
		return h
	}
	if m.sep == `\` {
		return `C:\Users\you`
	}
	return "/home/you"
}

func (m *Mock) folders() folderSet {
	h := m.home()
	if m.sep == `\` {
		return folderSet{h + `\AppData\Roaming\Downex`, h + `\AppData\Local\Downex\logs`, h + `\AppData\Local\Downex\tools`}
	}
	return folderSet{h + "/.config/Downex", h + "/.local/state/Downex", h + "/.local/share/Downex/tools"}
}

var reAbsFolder = regexp.MustCompile(`^([A-Za-z]:[\\/]|[\\/])`)

// FreeSpace answers 214 GiB (Options.FreeBytes) less what completed in this run, for an
// absolute folder (as the host: never echoed).
func (m *Mock) FreeSpace(dir string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !reAbsFolder.MatchString(strings.TrimSpace(dir)) {
		return 0, apiErr(invalidArgument, "dir must be an absolute path")
	}
	return int64(max(0, m.freeAtStart-m.completedBytes)), nil
}

// Startup is what the window's start-up reports, with the demo's own build also under a
// caller's view that names none.
func (m *Mock) Startup() Startup {
	v := Startup{OpenWith: true, Hibernate: true, WakeTimers: true}
	if m.opts.View != nil {
		v = *m.opts.View
	}
	if v.Build == nil {
		osName := "linux"
		if m.sep == `\` {
			osName = "windows"
		}
		// the native window's build: it links no Wails (host.BuildInfoFrom leaves WailsVersion "")
		v.Build = &host.BuildInfo{Version: "0.0.0-mock", OS: osName, Arch: "amd64", GoVersion: "go1.27.0"}
	}
	return v
}

// OpenProgressWindow records the request (and calls Options.OnOpenProgressWindow).
func (m *Mock) OpenProgressWindow(id int64) error {
	m.mu.Lock()
	if _, err := m.get(id); err != nil {
		m.mu.Unlock()
		return err
	}
	m.window.Opened = append(m.window.Opened, id)
	m.mu.Unlock()
	if m.opts.OnOpenProgressWindow != nil {
		m.opts.OnOpenProgressWindow(id)
	}
	return nil
}

// SetTheme records the theme the window gave its host.
func (m *Mock) SetTheme(c host.ThemeChoice) error {
	if _, ok := host.ParseThemeChoice(string(c)); !ok {
		return apiErr(invalidArgument, `theme must be "system", "light" or "dark"`)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window.Themes = append(m.window.Themes, string(c))
	return nil
}

// FileExists reports whether a download's file is there: once it completed, also while
// it is downloaded again to replace it.
func (m *Mock) FileExists(ctx context.Context, id int64) (bool, error) {
	return call(m, ctx, func() (bool, error) {
		s, err := m.get(id)
		if err != nil {
			return false, err
		}
		return !m.gone[id] && (s.d.state == state.StateCompleted || s.replaceBytes >= 0), nil
	})
}

// OpenAppFolder records the folder the page asked for.
func (m *Mock) OpenAppFolder(kind string) error {
	if _, ok := host.ParseFolderKind(kind); !ok {
		return apiErr(invalidArgument, `kind must be "data", "logs", "tools", "downloads", "extension-chrome" or "extension-firefox"`)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window.Folders = append(m.window.Folders, kind)
	return nil
}

// ExtensionFolder is where the demo's installation keeps the extension of kind: Setup's
// per-user folder on Windows, Downex.app's resources elsewhere.
func (m *Mock) ExtensionFolder(kind string) (string, error) {
	target := map[string]string{"extension-chrome": "chrome-mv3", "extension-firefox": "firefox-mv3"}[kind]
	if target == "" {
		return "", apiErr(invalidArgument, `kind must be "extension-chrome" or "extension-firefox"`)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sep == `\` {
		return m.home() + `\AppData\Local\Programs\Downex\extension\` + target, nil
	}
	return "/Applications/Downex.app/Contents/Resources/extension/" + target, nil
}

// RegisterBrowsers records that the page registered Downex with the browsers again.
func (m *Mock) RegisterBrowsers() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window.Registered++
	return nil
}

var reWebview = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// Diagnostics are the lines the host writes: no endpoint, address, file name or profile
// path.
func (m *Mock) Diagnostics(webview string) (string, error) {
	if webview != "" && (len(webview) > 32 || !reWebview.MatchString(webview)) {
		return "", apiErr(invalidArgument, "webview must be a version such as 141.0.3537.71")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.home()
	hide := func(p string) string {
		if m.sep == `\` {
			p = strings.Replace(p, h+`\AppData\Local`, "%LOCALAPPDATA%", 1)
			p = strings.Replace(p, h+`\AppData\Roaming`, "%APPDATA%", 1)
			return strings.Replace(p, h, "%USERPROFILE%", 1)
		}
		return strings.Replace(p, h, "~", 1)
	}
	f := m.folders()
	counts := map[string]int{}
	for _, s := range m.list() {
		counts[stateName(s.d.state)]++
	}
	var parts []string
	for k, n := range counts {
		parts = append(parts, k+": "+strconv.Itoa(n))
	}
	sort.Strings(parts)
	osName := "linux"
	if m.sep == `\` {
		osName = "windows"
	}
	updates := "not set"
	if m.settings.GetUpdateFeedUrl() != "" {
		updates = "set"
	}
	video := "off"
	if m.settings.GetSiteMedia() {
		video = "on"
	}
	lines := []string{"Downex window 0.0.0-mock (demo, " + osName + "/amd64, Go 1.27.0)"}
	if webview != "" {
		lines = append(lines, "WebView2 "+webview)
	}
	lines = append(lines,
		"downexd 0.0.0-mock (simulated)",
		"Updates supported: no",
		"Data folder: "+hide(f.data),
		"Log folder: "+hide(f.logs),
		"Tools folder: "+hide(f.tools),
		fmt.Sprintf("Settings: %d at once, %d connections", m.settings.GetMaxConcurrentDownloads(), m.settings.GetDefaultMaxConnections()),
		"Update address: "+updates,
		"Proxy: "+proxyModeName(m.settings.GetProxy().GetMode()),
		fmt.Sprintf("Host rules: %d; Site logins: %d", len(m.settings.GetHostRules()), len(m.settings.GetSiteLogins())),
		"Video sites: "+video+"; tools "+m.toolsPhase,
		fmt.Sprintf("Downloads: %d; %s", len(m.sims), strings.Join(parts, ", ")),
	)
	return strings.Join(lines, "\n") + "\n", nil
}

func proxyModeName(mode downexv1.ProxyMode) string {
	switch mode {
	case downexv1.ProxyMode_PROXY_MODE_NONE:
		return "none"
	case downexv1.ProxyMode_PROXY_MODE_MANUAL:
		return "manual"
	case downexv1.ProxyMode_PROXY_MODE_PAC:
		return "pac"
	}
	return "system"
}

// ThirdPartyNotices is the demo's notices text.
func (m *Mock) ThirdPartyNotices() string {
	return "THIRD-PARTY NOTICES (demo)\n\nThe desktop window shows the licences of every Go module and npm package that " +
		"Downex is built from here (THIRD-PARTY-NOTICES.txt). Programs Downex downloads separately: yt-dlp " +
		"(The Unlicense) and Deno (MIT License).\n"
}

func (m *Mock) completedOnly(ctx context.Context, id int64) error {
	_, err := call(m, ctx, func() (struct{}, error) {
		s, err := m.get(id)
		if err != nil {
			return struct{}{}, err
		}
		if s.d.state != state.StateCompleted {
			return struct{}{}, apiErr(failedPrecondition, fmt.Sprintf("download %d is not completed", id))
		}
		return struct{}{}, nil
	})
	return err
}

// OpenFile checks as the host does and opens nothing.
func (m *Mock) OpenFile(ctx context.Context, id int64) error { return m.completedOnly(ctx, id) }

// OpenWith checks as the host does and opens nothing (a demo has no "Open with" dialog).
func (m *Mock) OpenWith(ctx context.Context, id int64) error { return m.completedOnly(ctx, id) }

// OpenFolder checks that the download exists.
func (m *Mock) OpenFolder(ctx context.Context, id int64) error {
	_, err := call(m, ctx, func() (struct{}, error) { _, err := m.get(id); return struct{}{}, err })
	return err
}

// PickFolder answers "<initial or default folder><sep>Picked".
func (m *Mock) PickFolder(initial string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if initial == "" {
		initial = m.settings.GetDefaultDir()
	}
	return initial + m.sep + "Picked"
}

// PickFile answers "<initial or default folder><sep>scanner[.exe]".
func (m *Mock) PickFile(initial string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if initial == "" {
		initial = m.settings.GetDefaultDir()
	}
	exe := ""
	if m.sep == `\` {
		exe = ".exe"
	}
	return initial + m.sep + "scanner" + exe
}

// OpenExternal accepts an https address only (it opens nothing).
func (m *Mock) OpenExternal(u string) error {
	if !reHTTPS.MatchString(u) {
		return apiErr(invalidArgument, "not an https address")
	}
	return nil
}

// SetTitle, SetSize, CloseWindow, Minimise and ToggleMaximise record what the page asked
// of its simulated window.
func (m *Mock) SetTitle(title string) { m.mu.Lock(); m.window.Title = title; m.mu.Unlock() }

// SetSize records a size request.
func (m *Mock) SetSize(w, h float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window.Width, m.window.Height = w, h
	m.window.Sizes = append(m.window.Sizes, [2]float64{w, h})
}

// CloseWindow records a close (and calls Options.OnCloseWindow).
func (m *Mock) CloseWindow() {
	m.mu.Lock()
	m.window.Closed = true
	m.mu.Unlock()
	if m.opts.OnCloseWindow != nil {
		m.opts.OnCloseWindow()
	}
}

// Minimise records a minimise.
func (m *Mock) Minimise() { m.mu.Lock(); m.window.Minimised++; m.mu.Unlock() }

// ToggleMaximise records a maximise or restore.
func (m *Mock) ToggleMaximise() { m.mu.Lock(); m.window.Maximised = !m.window.Maximised; m.mu.Unlock() }

// DeleteFile deletes a download's file on disk (FileExists answers false until it is
// downloaded again).
func (m *Mock) DeleteFile(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(id); err != nil {
		return err
	}
	m.gone[id] = true
	return nil
}

// ---- the simulated browser -------------------------------------------------------------

func (m *Mock) handoffRequest() *downexv1.AddRequest {
	kind := pick(m, []categories.FileKind{categories.Video, categories.Audio, categories.Archives, categories.Documents, categories.Programs})
	filename := strings.Replace(pick(m, names[kind]), "#", strconv.Itoa(1+int(floor(m.rng()*400))), 1)
	r := sizeRange[kind]
	mime := map[categories.FileKind]string{categories.Video: "video/mp4", categories.Audio: "audio/mpeg", categories.Archives: "application/zip",
		categories.Documents: "application/pdf", categories.Programs: "application/x-msdownload"}[kind]
	u := "https://" + pick(m, hosts) + "/get/" + encodeURIComponent(filename) + "?token=" + strconv.Itoa(int(floor(m.rng()*1e9)))
	ref := "https://" + pick(m, hosts) + "/"
	return &downexv1.AddRequest{Url: u, Headers: []*downexv1.Header{{Name: "Referer", Value: ref}}, Filename: filename, Source: srcBrowser,
		ExpectedSize: int64(floor(m.between(r[0], r[1]))), Mime: mime}
}

func (m *Mock) mediaHandoffRequest() *downexv1.AddMediaRequest {
	n := strconv.Itoa(1 + int(floor(m.rng()*400)))
	manifest := pick(m, []string{
		"https://media.example.org/talks/session-" + n + "/master.m3u8",
		"https://media.example.org/films/feature-" + n + "/manifest.mpd",
		"https://media.example.org/podcasts/episode-" + n + "/audio.m3u8",
	})
	return &downexv1.AddMediaRequest{ManifestUrl: manifest, Headers: []*downexv1.Header{{Name: "Referer", Value: "https://media.example.org/watch"}},
		PageUrl: "https://media.example.org/watch", Source: srcBrowser}
}

// BrowserHandoff is a browser handing a download over (an Add with source browser), edit
// changing the request; when it then awaits confirmation, the window is asked to show its
// dialog.
func (m *Mock) BrowserHandoff(edit func(*downexv1.AddRequest)) (*downexv1.Download, error) {
	return call(m, context.Background(), func() (*downexv1.Download, error) {
		req := m.handoffRequest()
		if edit != nil {
			edit(req)
		}
		return m.addNow(req)
	})
}

// BrowserMediaHandoff is a browser handing a stream over.
func (m *Mock) BrowserMediaHandoff(edit func(*downexv1.AddMediaRequest)) (*downexv1.Download, error) {
	return call(m, context.Background(), func() (*downexv1.Download, error) {
		req := m.mediaHandoffRequest()
		if edit != nil {
			edit(req)
		}
		return m.addMediaNow(req)
	})
}

// BrowserVideoHandoff is a browser handing a web page's video over (yt-dlp must be ready).
func (m *Mock) BrowserVideoHandoff(edit func(*downexv1.AddMediaRequest)) (*downexv1.Download, error) {
	return call(m, context.Background(), func() (*downexv1.Download, error) {
		n := 1 + int(floor(m.rng()*400))
		req := &downexv1.AddMediaRequest{PageUrl: "https://www.youtube.com/watch?v=dQw4w9WgX" + strconv.Itoa(n%10) + "Q",
			Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Source: srcBrowser}
		if edit != nil {
			edit(req)
		}
		return m.addMediaNow(req)
	})
}
