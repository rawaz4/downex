// Package formattest holds the fixtures the ui/core tests share: downloads built as
// ui/frontend/src/lib/testing.ts builds them, and the reader of the 0.9.2 formatter goldens
// (ref-0.9.2/goldens/formatter-goldens.json in the research folder's native-ui, see
// ui/internal/research), which hold every input of the window's vitest tables with Blink's
// output in each locale and zone.
package formattest

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/internal/research"
)

// Byte units of testing.ts.
const (
	KiB = 1024
	MiB = 1024 * KiB
	KB  = 1000
	MB  = 1000 * KB
	GB  = 1000 * MB
)

// Millis is an epoch-milliseconds timestamp as the wire carries it; nil for 0 or less.
func Millis(ms int64) *timestamppb.Timestamp {
	if ms <= 0 {
		return nil
	}
	return timestamppb.New(timeFromMillis(ms))
}

func timeFromMillis(ms int64) time.Time { return time.UnixMilli(ms) }

// Download is testing.ts's makeDownload: a queued 10 MiB file, changed by edit.
func Download(id int64, edit func(d *downexv1.Download)) *downexv1.Download {
	d := &downexv1.Download{
		Id:        id,
		Url:       fmt.Sprintf("https://downloads.example.com/file-%d.bin", id),
		Dir:       "/home/you/Downloads",
		Filename:  fmt.Sprintf("file-%d.bin", id),
		Size:      10 * MiB,
		State:     downexv1.DownloadState_DOWNLOAD_STATE_QUEUED,
		CreatedAt: Millis(1_700_000_000_000 + id*1000),
		UpdatedAt: Millis(1_700_000_000_000 + id*1000),
		Kind:      downexv1.DownloadKind_DOWNLOAD_KIND_FILE,
		QueueId:   1,
	}
	if edit != nil {
		edit(d)
	}
	return d
}

// MediaInfo is testing.ts's makeMediaInfo: an HLS talk with three video and two audio tracks,
// 200 fragments, changed by edit.
func MediaInfo(edit func(m *downexv1.MediaInfo)) *downexv1.MediaInfo {
	m := &downexv1.MediaInfo{
		ManifestUrl: "https://media.example.org/talk/master.m3u8",
		Format:      "hls",
		Tracks: []*downexv1.MediaTrack{
			{Id: "v0", Kind: "video", Bandwidth: 800_000, Width: 640, Height: 360, FrameRate: 25, Codecs: "avc1.4d401e"},
			{Id: "v1", Kind: "video", Bandwidth: 5_000_000, Width: 1920, Height: 1080, FrameRate: 29.97, Codecs: "avc1.640028"},
			{Id: "v2", Kind: "muxed", Bandwidth: 1_200_000, Width: 960, Height: 540, Codecs: "avc1.4d401f,mp4a.40.2"},
			{Id: "r0", Kind: "audio", Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en", Name: "English"},
			{Id: "r1", Kind: "audio", Bandwidth: 96_000, Codecs: "mp4a.40.2", Language: "de", Name: "Deutsch"},
		},
		SelectedVideo:  "v1",
		SelectedAudio:  "r0",
		DurationMs:     600_000,
		FragmentsTotal: 200,
		Container:      "mp4",
		Title:          "Keynote",
		Origin:         downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST,
	}
	if edit != nil {
		edit(m)
	}
	return m
}

// MediaDownload is testing.ts's makeMediaDownload: a stream of unknown size in Video.
func MediaDownload(id int64, edit func(d *downexv1.Download)) *downexv1.Download {
	return Download(id, func(d *downexv1.Download) {
		d.Url = "https://media.example.org/talk/master.m3u8"
		d.Filename = "Keynote.mp4"
		d.Size = -1
		d.Category = "Video"
		d.Kind = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
		d.Media = MediaInfo(nil)
		if edit != nil {
			edit(d)
		}
	})
}

// ---- the goldens ----------------------------------------------------------------------

// DefaultGoldens is where the step-1 goldens are: ref-0.9.2's goldens in the research folder
// ("" when it is not known); $DOWNEX_GOLDENS names another file.
func DefaultGoldens() string {
	return research.Beside(research.NativeUI, "ref-0.9.2", "goldens", "formatter-goldens.json")
}

// Goldens is the formatter goldens file.
type Goldens struct {
	Locales   []string                  `json:"locales"`
	TimeZones []string                  `json:"timeZones"`
	FixedNow  float64                   `json:"fixedNow"`
	Functions map[string]GoldenFunction `json:"functions"`
}

// GoldenFunction is one function's cases.
type GoldenFunction struct {
	Module string `json:"module"`
	Cases  []Case `json:"cases"`
}

// Outcome is what a call returned (Out) or that it threw (Threw).
type Outcome struct {
	Out   json.RawMessage `json:"out"`
	Threw json.RawMessage `json:"threw"`
}

// Case is one input with its outcome, the same everywhere or per locale and/or zone.
type Case struct {
	ID                  int                           `json:"id"`
	Args                []json.RawMessage             `json:"args"`
	Apply               []json.RawMessage             `json:"apply"`
	Out                 json.RawMessage               `json:"out"`
	Threw               json.RawMessage               `json:"threw"`
	ByLocale            map[string]Outcome            `json:"byLocale"`
	ByTimeZone          map[string]Outcome            `json:"byTimeZone"`
	ByLocaleAndTimeZone map[string]map[string]Outcome `json:"byLocaleAndTimeZone"`
}

// Expected is the case's outcome in a locale and zone.
func (c Case) Expected(locale, zone string) Outcome {
	switch {
	case c.ByLocaleAndTimeZone != nil:
		return c.ByLocaleAndTimeZone[locale][zone]
	case c.ByLocale != nil:
		return c.ByLocale[locale]
	case c.ByTimeZone != nil:
		return c.ByTimeZone[zone]
	}
	return Outcome{Out: c.Out, Threw: c.Threw}
}

// LoadGoldens reads the goldens from $DOWNEX_GOLDENS or DefaultGoldens; ok is false when
// neither is there (the caller skips).
func LoadGoldens() (g *Goldens, path string, ok bool, err error) {
	path = os.Getenv("DOWNEX_GOLDENS")
	if path == "" {
		path = DefaultGoldens()
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, path, false, nil
	}
	if err != nil {
		return nil, path, false, err
	}
	g = &Goldens{}
	if err := json.Unmarshal(data, g); err != nil {
		return nil, path, false, fmt.Errorf("%s: %w", path, err)
	}
	return g, path, true, nil
}

// Num reads a number argument; {"$num": "NaN"|"Infinity"|"-Infinity"|"-0"} for the values JSON
// cannot hold, {"$undefined": true} as NaN (what Number(undefined) is).
func Num(raw json.RawMessage) (float64, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var tagged struct {
		Num       string `json:"$num"`
		Undefined bool   `json:"$undefined"`
	}
	if err := json.Unmarshal(raw, &tagged); err != nil {
		return 0, fmt.Errorf("not a number: %s", raw)
	}
	switch {
	case tagged.Undefined:
		return math.NaN(), nil
	case tagged.Num == "NaN":
		return math.NaN(), nil
	case tagged.Num == "Infinity":
		return math.Inf(1), nil
	case tagged.Num == "-Infinity":
		return math.Inf(-1), nil
	case tagged.Num == "-0":
		return math.Copysign(0, -1), nil
	}
	return 0, fmt.Errorf("not a number: %s", raw)
}

// IsUndefined reports an argument the call left out ({"$undefined": true}).
func IsUndefined(raw json.RawMessage) bool {
	return strings.Contains(string(raw), `"$undefined"`)
}

// Str reads a string argument.
func Str(raw json.RawMessage) (string, error) {
	var s string
	err := json.Unmarshal(raw, &s)
	return s, err
}

// Bool reads a boolean argument.
func Bool(raw json.RawMessage) (bool, error) {
	var b bool
	err := json.Unmarshal(raw, &b)
	return b, err
}

// wireDownload is a lib/api.ts Download as the goldens hold it.
type wireDownload struct {
	ID               int64            `json:"id"`
	URL              string           `json:"url"`
	FinalURL         string           `json:"finalUrl"`
	Dir              string           `json:"dir"`
	Filename         string           `json:"filename"`
	Size             JSNum            `json:"size"`
	SizeApprox       bool             `json:"sizeApprox"`
	Received         JSNum            `json:"received"`
	State            string           `json:"state"`
	Mode             string           `json:"mode"`
	MaxConnections   int32            `json:"maxConnections"`
	SpeedLimit       JSNum            `json:"speedLimit"`
	Error            string           `json:"error"`
	ErrorCode        string           `json:"errorCode"`
	CreatedAt        JSNum            `json:"createdAt"`
	UpdatedAt        JSNum            `json:"updatedAt"`
	CompletedAt      JSNum            `json:"completedAt"`
	SpeedBps         JSNum            `json:"speedBps"`
	Connections      int32            `json:"connections"`
	QueuePosition    int32            `json:"queuePosition"`
	Source           string           `json:"source"`
	ExpectedSize     JSNum            `json:"expectedSize"`
	Mime             string           `json:"mime"`
	Category         string           `json:"category"`
	Kind             string           `json:"kind"`
	Media            *wireMedia       `json:"media"`
	ConnectionInfo   []wireConnection `json:"connectionInfo"`
	CompletionDialog bool             `json:"completionDialog"`
	ActiveMs         JSNum            `json:"activeMs"`
	StartedAt        JSNum            `json:"startedAt"`
	QueueID          JSNum            `json:"queueId"`
	Segments         []struct {
		Start    int64 `json:"start"`
		End      int64 `json:"end"`
		Received int64 `json:"received"`
	} `json:"segments"`
}

type wireMedia struct {
	ManifestURL    string      `json:"manifestUrl"`
	Format         string      `json:"format"`
	Tracks         []wireTrack `json:"tracks"`
	SelectedVideo  string      `json:"selectedVideo"`
	SelectedAudio  string      `json:"selectedAudio"`
	DurationMs     int64       `json:"durationMs"`
	FragmentsTotal int32       `json:"fragmentsTotal"`
	FragmentsDone  int32       `json:"fragmentsDone"`
	Container      string      `json:"container"`
	Title          string      `json:"title"`
	Origin         string      `json:"origin"`
	Site           string      `json:"site"`
	PageURL        string      `json:"pageUrl"`
}

type wireTrack struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Bandwidth  int64   `json:"bandwidth"`
	Width      int32   `json:"width"`
	Height     int32   `json:"height"`
	FrameRate  float64 `json:"frameRate"`
	Codecs     string  `json:"codecs"`
	Language   string  `json:"language"`
	Name       string  `json:"name"`
	Size       int64   `json:"size"`
	SizeApprox bool    `json:"sizeApprox"`
	Ext        string  `json:"ext"`
}

type wireConnection struct {
	Index    int32   `json:"index"`
	State    string  `json:"state"`
	Start    int64   `json:"start"`
	End      int64   `json:"end"`
	Received int64   `json:"received"`
	SpeedBps float64 `json:"speedBps"`
	Detail   string  `json:"detail"`
	Fragment int32   `json:"fragment"`
}

func enumOf(prefix string, values map[string]int32, name string) int32 {
	return values[prefix+strings.ToUpper(name)]
}

// DownloadArg reads a lib/api.ts Download argument into the proto message the native window
// gets (lib/wails.ts parseDownload in reverse).
func DownloadArg(raw json.RawMessage) (*downexv1.Download, error) {
	var w wireDownload
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	if !w.ints() {
		return nil, ErrUnrepresentable
	}
	return w.proto(), nil
}

// ErrUnrepresentable is an argument the proto message cannot hold (NaN or a fraction in an
// int64 field): the case only exists in TypeScript.
var ErrUnrepresentable = errors.New("formattest: not representable in downex.v1")

// JSNum is a JSON number argument, or a tagged NaN, infinity or -0.
type JSNum float64

func (n *JSNum) UnmarshalJSON(b []byte) error {
	v, err := Num(b)
	*n = JSNum(v)
	return err
}

func (w *wireDownload) ints() bool {
	// A timestamp that is not a time reaches the window as 0 (wails.ts timestamp()).
	for _, ts := range []*JSNum{&w.CreatedAt, &w.UpdatedAt, &w.CompletedAt, &w.StartedAt} {
		if f := float64(*ts); math.IsNaN(f) || math.IsInf(f, 0) {
			*ts = 0
		}
	}
	for _, v := range []JSNum{w.Size, w.Received, w.SpeedLimit, w.ExpectedSize, w.CreatedAt, w.UpdatedAt, w.CompletedAt, w.ActiveMs, w.StartedAt, w.QueueID} {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<62 {
			return false
		}
	}
	return true
}

func (w *wireDownload) proto() *downexv1.Download {
	d := &downexv1.Download{
		Id:               w.ID,
		Url:              w.URL,
		FinalUrl:         w.FinalURL,
		Dir:              w.Dir,
		Filename:         w.Filename,
		Size:             int64(w.Size),
		SizeApprox:       w.SizeApprox,
		Received:         int64(w.Received),
		State:            downexv1.DownloadState(enumOf("DOWNLOAD_STATE_", downexv1.DownloadState_value, w.State)),
		Mode:             downexv1.Mode(enumOf("MODE_", downexv1.Mode_value, w.Mode)),
		MaxConnections:   w.MaxConnections,
		SpeedLimit:       int64(w.SpeedLimit),
		Error:            w.Error,
		ErrorCode:        w.ErrorCode,
		CreatedAt:        Millis(int64(w.CreatedAt)),
		UpdatedAt:        Millis(int64(w.UpdatedAt)),
		CompletedAt:      Millis(int64(w.CompletedAt)),
		SpeedBps:         float64(w.SpeedBps),
		Connections:      w.Connections,
		QueuePosition:    w.QueuePosition,
		Source:           downexv1.AddSource(enumOf("ADD_SOURCE_", downexv1.AddSource_value, w.Source)),
		ExpectedSize:     int64(w.ExpectedSize),
		Mime:             w.Mime,
		Category:         w.Category,
		Kind:             downexv1.DownloadKind(enumOf("DOWNLOAD_KIND_", downexv1.DownloadKind_value, w.Kind)),
		CompletionDialog: w.CompletionDialog,
		ActiveMs:         int64(w.ActiveMs),
		StartedAt:        Millis(int64(w.StartedAt)),
		QueueId:          int64(w.QueueID),
	}
	for _, s := range w.Segments {
		d.Segments = append(d.Segments, &downexv1.Segment{Start: s.Start, End: s.End, Received: s.Received})
	}
	if w.Media != nil {
		d.Media = w.Media.proto()
	}
	for _, c := range w.ConnectionInfo {
		d.ConnectionInfo = append(d.ConnectionInfo, c.proto())
	}
	return d
}

func (m *wireMedia) proto() *downexv1.MediaInfo {
	out := &downexv1.MediaInfo{
		ManifestUrl:    m.ManifestURL,
		Format:         m.Format,
		SelectedVideo:  m.SelectedVideo,
		SelectedAudio:  m.SelectedAudio,
		DurationMs:     m.DurationMs,
		FragmentsTotal: m.FragmentsTotal,
		FragmentsDone:  m.FragmentsDone,
		Container:      m.Container,
		Title:          m.Title,
		Origin:         downexv1.MediaOrigin(enumOf("MEDIA_ORIGIN_", downexv1.MediaOrigin_value, m.Origin)),
		Site:           m.Site,
		PageUrl:        m.PageURL,
	}
	for _, t := range m.Tracks {
		out.Tracks = append(out.Tracks, t.proto())
	}
	return out
}

func (c wireConnection) proto() *downexv1.ConnectionInfo {
	return &downexv1.ConnectionInfo{
		Index:    c.Index,
		State:    downexv1.ConnectionState(enumOf("CONNECTION_STATE_", downexv1.ConnectionState_value, c.State)),
		Start:    c.Start,
		End:      c.End,
		Received: c.Received,
		SpeedBps: c.SpeedBps,
		Detail:   c.Detail,
		Fragment: c.Fragment,
	}
}

// ConnectionArg reads a lib/api.ts ConnectionInfo argument.
func ConnectionArg(raw json.RawMessage) (*downexv1.ConnectionInfo, error) {
	var c wireConnection
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return c.proto(), nil
}

// Connection is testing.ts's makeConnection: a receiving connection on the first MiB.
func Connection(index int32, edit func(c *downexv1.ConnectionInfo)) *downexv1.ConnectionInfo {
	c := &downexv1.ConnectionInfo{
		Index:    index,
		State:    downexv1.ConnectionState_CONNECTION_STATE_RECEIVING,
		End:      MiB - 1,
		Fragment: -1,
	}
	if edit != nil {
		edit(c)
	}
	return c
}

// Page is testing.ts's PAGE: the web page of the site fixtures.
const Page = "https://www.youtube.com/watch?v=aqz-KE-bpKQ"

// SiteMediaInfo is testing.ts's makeSiteMediaInfo: a web page's video as downexd offers it
// (SITE-MEDIA-SPEC §3.11): one MP4 quality per class, best first, a muxed one last, and two
// MP4 audio tracks.
func SiteMediaInfo(edit func(m *downexv1.MediaInfo)) *downexv1.MediaInfo {
	return MediaInfo(func(m *downexv1.MediaInfo) {
		m.ManifestUrl = ""
		m.Format = "http"
		m.Tracks = []*downexv1.MediaTrack{
			{Id: "299", Kind: "video", Bandwidth: 6_000_000, Width: 1920, Height: 1080, FrameRate: 60, Codecs: "avc1.64002a", Name: "1080p (Full HD) 60fps", Size: 450 * MiB, Ext: "mp4"},
			{Id: "136", Kind: "video", Bandwidth: 1_500_000, Width: 1280, Height: 720, FrameRate: 30, Codecs: "avc1.4d401f", Name: "720p (HD)", Size: 110 * MiB, Ext: "mp4"},
			{Id: "397", Kind: "video", Bandwidth: 2_000_000, Width: 854, Height: 480, FrameRate: 30, Codecs: "av01.0.04M.08", Name: "480p", Size: 150 * MiB, SizeApprox: true, Ext: "mp4"},
			{Id: "18", Kind: "muxed", Bandwidth: 600_000, Width: 640, Height: 360, FrameRate: 30, Codecs: "avc1.42001E, mp4a.40.2", Name: "360p", Ext: "mp4"},
			{Id: "140", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "en", Name: "English original", Size: 10 * MiB, Ext: "m4a"},
			{Id: "140-1", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "de", Name: "Deutsch", Size: 10 * MiB, Ext: "m4a"},
		}
		m.SelectedVideo = "299"
		m.SelectedAudio = "140"
		m.FragmentsTotal = 0
		m.Title = "A talk"
		m.Origin = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
		m.Site = "YouTube"
		m.PageUrl = Page
		if edit != nil {
			edit(m)
		}
	})
}

// SiteDownload is testing.ts's makeSiteDownload: a web page's video of unknown size in Video.
func SiteDownload(id int64, edit func(d *downexv1.Download)) *downexv1.Download {
	return Download(id, func(d *downexv1.Download) {
		d.Url = Page
		d.Filename = "A talk.mp4"
		d.Size = -1
		d.Category = "Video"
		d.Kind = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
		d.Media = SiteMediaInfo(nil)
		if edit != nil {
			edit(d)
		}
	})
}
