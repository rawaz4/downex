package scene

import (
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/text"
)

// Byte units of the simulation.
const (
	KiB = 1024.0
	MiB = 1024 * KiB
	GiB = 1024 * MiB
)

const (
	minSplit    = 2 * MiB
	maxSegments = 64
	dayMin      = 24 * 60
	// partBytes: a web page's video downloads in parts of this size (the googlevideo cap).
	partBytes = 10 * MiB
)

// Limits the simulated daemon checks (api.ts LIMITS).
const (
	limitMinConnections = 1
	limitMaxConnections = 32
	limitMinConcurrent  = 1
	limitMaxConcurrent  = 16
	limitQueueName      = 64
	limitQueueConcurr   = 16
)

type segment struct{ start, end, received float64 }

// dl is a download as the simulation changes it (today's MDownload): numbers are float64
// as in JavaScript, times epoch milliseconds (0 = none).
type dl struct {
	id                                int64
	url, finalURL, dir, filename      string
	size                              float64
	sizeApprox                        bool
	received                          float64
	state                             downexv1.DownloadState
	mode                              downexv1.Mode
	maxConnections                    float64
	speedLimit                        float64
	err, errCode                      string
	createdAt, updatedAt, completedAt float64
	speedBps                          float64
	connections                       float64
	segments                          []*segment
	queuePosition                     float64
	source                            downexv1.AddSource
	expectedSize                      float64
	mime, category                    string
	kind                              downexv1.DownloadKind
	media                             *downexv1.MediaInfo
	connInfo                          []*downexv1.ConnectionInfo
	completionDialog                  bool
	activeMs, startedAt               float64
	queueID                           int64
}

func stamp(ms float64) *timestamppb.Timestamp {
	if ms <= 0 {
		return nil
	}
	return timestamppb.New(time.UnixMilli(int64(ms)))
}

// clone is the download as the daemon reports it; connections are live: only a running
// download has them.
func (d *dl) clone() *downexv1.Download {
	out := &downexv1.Download{
		Id: d.id, Url: d.url, FinalUrl: d.finalURL, Dir: d.dir, Filename: d.filename,
		Size: int64(d.size), SizeApprox: d.sizeApprox, Received: int64(d.received), State: d.state, Mode: d.mode,
		MaxConnections: int32(d.maxConnections), SpeedLimit: int64(d.speedLimit), Error: d.err, ErrorCode: d.errCode,
		CreatedAt: stamp(d.createdAt), UpdatedAt: stamp(d.updatedAt), CompletedAt: stamp(d.completedAt),
		SpeedBps: d.speedBps, Connections: int32(d.connections), QueuePosition: int32(d.queuePosition),
		Source: d.source, ExpectedSize: int64(d.expectedSize), Mime: d.mime, Category: d.category, Kind: d.kind,
		CompletionDialog: d.completionDialog, ActiveMs: int64(d.activeMs), StartedAt: stamp(d.startedAt), QueueId: d.queueID,
	}
	for _, s := range d.segments {
		out.Segments = append(out.Segments, &downexv1.Segment{Start: int64(s.start), End: int64(s.end), Received: int64(s.received)})
	}
	if d.media != nil {
		out.Media = proto.CloneOf(d.media)
	}
	if d.state == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING {
		for _, c := range d.connInfo {
			out.ConnectionInfo = append(out.ConnectionInfo, proto.CloneOf(c))
		}
	}
	return out
}

func segmentLength(s *segment, size float64) float64 {
	end := s.end
	if s.end < 0 {
		if size > 0 {
			end = size - 1
		} else {
			return math.Inf(1)
		}
	}
	return end - s.start + 1
}

// sanitizeFilename is a rough stand-in for the daemon's filename sanitiser.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		// charCodeAt(0) < 32 of the code point's first unit
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	cleaned := strings.TrimRight(b.String(), ". ")
	if cleaned == "" {
		return "download"
	}
	return cleaned
}

func nameFromURL(raw string) string {
	u, ok := parseJSURL(raw)
	if !ok {
		return "download"
	}
	path := u.pathname
	last := path[strings.LastIndex(path, "/")+1:]
	dec, ok := text.DecodeURIComponent(last)
	if !ok {
		return "download"
	}
	if dec == "" {
		dec = "index.html"
	}
	return sanitizeFilename(dec)
}

// kindOfMime is the kind of file a MIME type names, for category selection without a
// usable name.
func kindOfMime(mime string) (categories.FileKind, bool) {
	m := strings.ToLower(mime)
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimSpace(m)
	switch {
	case strings.HasPrefix(m, "video/"):
		return categories.Video, true
	case strings.HasPrefix(m, "audio/"):
		return categories.Audio, true
	}
	switch m {
	case "application/zip", "application/x-7z-compressed", "application/vnd.rar", "application/x-rar-compressed", "application/gzip",
		"application/x-tar", "application/x-xz", "application/x-bzip2", "application/zstd", "application/x-iso9660-image":
		return categories.Archives, true
	case "application/pdf", "application/epub+zip", "application/msword",
		"application/x-msdownload", "application/x-msi", "application/vnd.microsoft.portable-executable",
		"application/vnd.android.package-archive", "application/vnd.debian.binary-package", "application/x-rpm", "application/java-archive":
		if m == "application/pdf" || m == "application/epub+zip" || m == "application/msword" {
			return categories.Documents, true
		}
		return categories.Programs, true
	}
	if strings.HasPrefix(m, "text/") && len(m) > len("text/") ||
		strings.HasPrefix(m, "application/vnd.openxmlformats-officedocument.") && len(m) > len("application/vnd.openxmlformats-officedocument.") ||
		strings.HasPrefix(m, "application/vnd.oasis.opendocument.") && len(m) > len("application/vnd.oasis.opendocument.") {
		return categories.Documents, true
	}
	return "", false
}

func stateName(s downexv1.DownloadState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "DOWNLOAD_STATE_"))
}
