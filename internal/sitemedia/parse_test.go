package sitemedia

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/validate"
)

// testNow is the time the fixtures were written for: their expire parameters are 6 h
// later, and available_at values are relative to it.
var testNow = time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseFixture(t testing.TB, name string) *Info {
	t.Helper()
	info, err := Parse(fixture(t, name), testNow)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return info
}

// page wraps format objects (JSON text) into a minimal yt-dlp output.
func page(formats ...string) []byte {
	return []byte(`{"id": "vid", "extractor_key": "Generic", "title": "t", "formats": [` + strings.Join(formats, ",") + `]}`)
}

// muxedFormat is a usable direct muxed MP4 with extra members (JSON text, "" for none).
func muxedFormat(id, extra string) string {
	if extra != "" {
		extra = ", " + extra
	}
	return fmt.Sprintf(`{"format_id": %q, "ext": "mp4", "protocol": "https", "url": "https://cdn.example.com/v.mp4?x=1",`+
		` "vcodec": "avc1.4d401f", "acodec": "mp4a.40.2", "height": 360%s}`, id, extra)
}

// The YouTube-like fixture: top-level fields cleaned, unusable formats dropped, and
// the kept ones with their validated fields, headers and URL hints.
func TestParseYouTube(t *testing.T) {
	info := parseFixture(t, "youtube.json")
	if info.ID != "aBcDeFgHiJk" || info.ExtractorKey != "Youtube" || info.Site != "YouTube" {
		t.Errorf("id %q, extractor %q, site %q", info.ID, info.ExtractorKey, info.Site)
	}
	if info.Title != "A sample video title" {
		t.Errorf("title %q, want bidi controls removed and space collapsed", info.Title)
	}
	if info.Duration != 213*time.Second || info.HasDRM || info.Protected != 0 || info.Availability != "public" {
		t.Errorf("duration %v, drm %v, protected %d, availability %q", info.Duration, info.HasDRM, info.Protected, info.Availability)
	}
	for _, id := range []string{"sb0", "135", "402"} { // mhtml, request_data, available_at in an hour
		if info.Format(id) != nil {
			t.Errorf("format %s kept, want dropped", id)
		}
	}
	if len(info.Formats) != 31 {
		t.Errorf("%d formats, want 31", len(info.Formats))
	}

	f := info.Format("137")
	if f == nil {
		t.Fatal("format 137 missing")
	}
	const lmt = "1790000000001080"
	want := Format{
		ID: "137", Key: Key("Youtube", "aBcDeFgHiJk", "137", "", 117000000, lmt), Kind: media.KindVideo,
		Ext: "mp4", Protocol: "https", URL: f.URL, VCodec: "avc1.640028", Width: 1920, Height: 1080, FPS: 30,
		TBR: 4400, VBR: 4400, Size: 117000000, Container: "mp4_dash", LanguagePreference: -1, Note: "1080p",
		Headers: http.Header{
			"User-Agent":        {f.Headers.Get("User-Agent")},
			"Accept":            {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			"Accept-Language":   {"en-us,en;q=0.5"},
			"Sec-Fetch-Mode":    {"navigate"},
			"X-Goog-Visitor-Id": {"Cgt2aXNpdG9y"},
			"Origin":            {"https://www.youtube.com"},
			"Cookie":            {"VISITOR_INFO1_LIVE=abc123"},
		},
		ChunkSize: 10 << 20, AvailableAt: testNow.Add(5 * time.Second), Expires: testNow.Add(6 * time.Hour),
		Network: "tcp4", LMT: lmt,
	}
	if !strings.HasPrefix(f.URL, "https://rr3---sn-4g5e6nzz.googlevideo.com/videoplayback?") || !strings.HasPrefix(want.Headers.Get("User-Agent"), "Mozilla/5.0") {
		t.Errorf("URL %q, User-Agent %q", f.URL, f.Headers.Get("User-Agent"))
	}
	if !reflect.DeepEqual(*f, want) {
		t.Errorf("format 137:\n got %+v\nwant %+v", *f, want)
	}

	if f := info.Format("18"); f == nil || f.Kind != media.KindMuxed || f.Size != 0 || f.ApproxSize != 13300000 ||
		f.Container != "" || f.VCodec != "avc1.42001E" || f.ACodec != "mp4a.40.2" || f.ChunkSize != 0 {
		t.Errorf("format 18 = %+v, want muxed with an estimated size", f)
	}
	if f := info.Format("233"); f == nil || f.Kind != media.KindAudio || f.URL != "" ||
		!strings.Contains(f.ManifestURL, "/hls_variant/") || f.Expires != (time.Time{}) || f.LMT != "" {
		t.Errorf("format 233 = %+v, want an HLS audio format of the master playlist", f)
	}
	if f := info.Format("93"); f == nil || f.Kind != media.KindMuxed || f.Protocol != protoHLS {
		t.Errorf("format 93 = %+v, want a muxed HLS format", f)
	}
	if f := info.Format("140-1"); f == nil || f.Language != "de" || f.LanguagePreference != -1 || f.Note != "German, medium" ||
		f.ABR != 129.4 || f.ASR != 44100 || f.Kind != media.KindAudio || f.Ext != "m4a" {
		t.Errorf("format 140-1 = %+v", f)
	}
	if f := info.Format("701"); f == nil || !f.HDR {
		t.Errorf("format 701 = %+v, want HDR", f)
	}
	if f := info.Format("401"); f == nil || f.HDR {
		t.Errorf("format 401 = %+v, want SDR", f)
	}
	if f := info.Format("251-0"); f == nil || f.Ext != "webm" || f.Kind != media.KindAudio {
		t.Errorf("format 251-0 = %+v, want a WebM audio (parsed, never offered)", f)
	}
}

// Refused pages: playlists, links, live streams, malformed and oversized output.
func TestParseRefusals(t *testing.T) {
	huge := append([]byte(`{"id": "x", "extractor_key": "Generic", "pad": "`), make([]byte, MaxOutputBytes)...)
	for name, c := range map[string]struct {
		data []byte
		want error
	}{
		"playlist":        {fixture(t, "playlist.json"), media.ErrUnsupported},
		"multi video":     {[]byte(`{"_type": "multi_video", "id": "x", "extractor_key": "Generic"}`), media.ErrUnsupported},
		"other type":      {[]byte(`{"_type": "compat_list", "id": "x", "extractor_key": "Generic"}`), media.ErrUnsupported},
		"url":             {[]byte(`{"_type": "url", "url": "https://example.com/v", "id": "x"}`), ErrSiteUnsupported},
		"url transparent": {[]byte(`{"_type": "url_transparent", "url": "https://example.com/v"}`), ErrSiteUnsupported},
		"live":            {fixture(t, "live.json"), media.ErrLive},
		"is_live only":    {[]byte(`{"id": "x", "extractor_key": "Generic", "is_live": true}`), media.ErrLive},
		"upcoming":        {[]byte(`{"id": "x", "extractor_key": "Generic", "live_status": "is_upcoming"}`), media.ErrLive},
		"post live":       {[]byte(`{"id": "x", "extractor_key": "Generic", "live_status": "post_live"}`), media.ErrLive},
		"malformed":       {fixture(t, "malformed.json"), media.ErrMalformed},
		"oversized":       {fixture(t, "oversized.json"), media.ErrTooLarge},
		"over 32 MiB":     {huge, media.ErrTooLarge},
		"array":           {[]byte(`[{"id": "x", "extractor_key": "Generic"}]`), media.ErrMalformed},
		"string":          {[]byte(`"x"`), media.ErrMalformed},
		"null":            {[]byte(" null\n"), ErrSiteUnsupported},
		"empty":           {nil, media.ErrMalformed},
		"trailing data":   {[]byte(`{"id": "x", "extractor_key": "Generic"} {}`), media.ErrMalformed},
		"no id":           {[]byte(`{"extractor_key": "Generic"}`), media.ErrMalformed},
		"empty id":        {[]byte(`{"id": "", "extractor_key": "Generic"}`), media.ErrMalformed},
		"numeric id":      {[]byte(`{"id": 5, "extractor_key": "Generic"}`), media.ErrMalformed},
		"control in id":   {[]byte(`{"id": "a\nb", "extractor_key": "Generic"}`), media.ErrMalformed},
		"long id":         {[]byte(`{"id": "` + strings.Repeat("a", 257) + `", "extractor_key": "Generic"}`), media.ErrMalformed},
		"bad extractor":   {[]byte(`{"id": "x", "extractor_key": "Gen eric"}`), media.ErrMalformed},
		"long extractor":  {[]byte(`{"id": "x", "extractor_key": "` + strings.Repeat("A", 65) + `"}`), media.ErrMalformed},
	} {
		info, err := Parse(c.data, testNow)
		if !errors.Is(err, c.want) || info != nil {
			t.Errorf("%s: Parse = %v, %v; want %v", name, info, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "://") {
			t.Errorf("%s: error %q repeats a URL", name, err)
		}
	}
}

// Finished live streams, unknown live states and other harmless top-level values are
// ordinary pages; wrong types count as absent.
func TestParseTopLevelLeniency(t *testing.T) {
	for name, c := range map[string]struct {
		json      string
		title     string
		duration  time.Duration
		drm       bool
		available string
	}{
		"was live":         {`"live_status": "was_live", "is_live": false`, "t", 0, false, ""},
		"unknown status":   {`"live_status": "something_new"`, "t", 0, false, ""},
		"wrong types":      {`"is_live": "yes", "_has_drm": 1, "duration": "60", "availability": 5`, "t", 0, false, ""},
		"duration":         {`"duration": 61.5`, "t", 61500 * time.Millisecond, false, ""},
		"long duration":    {`"duration": 10000001`, "t", 0, false, ""},
		"negative":         {`"duration": -1`, "t", 0, false, ""},
		"nan duration":     {`"duration": NaN`, "t", 0, false, ""},
		"has drm":          {`"_has_drm": true`, "t", 0, true, ""},
		"needs auth":       {`"availability": "needs_auth"`, "t", 0, false, "needs_auth"},
		"bad availability": {`"availability": "Needs Auth"`, "t", 0, false, ""},
		"empty title":      {`"title": " \t "`, "x", 0, false, ""},
		"no title":         {`"title": null`, "x", 0, false, ""},
		"long title":       {`"title": "` + strings.Repeat("é", 600) + `"`, strings.Repeat("é", 512), 0, false, ""},
	} {
		data := `{"id": "x", "extractor_key": "Generic", "title": "t", ` + c.json + `}`
		if strings.Contains(c.json, `"title"`) {
			data = `{"id": "x", "extractor_key": "Generic", ` + c.json + `}`
		}
		info, err := Parse([]byte(data), testNow)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if info.Title != c.title || info.Duration != c.duration || info.HasDRM != c.drm || info.Availability != c.available {
			t.Errorf("%s: title %q, duration %v, drm %v, availability %q; want %q, %v, %v, %q", name,
				info.Title, info.Duration, info.HasDRM, info.Availability, c.title, c.duration, c.drm, c.available)
		}
	}
}

// parseOne parses a page with the given formats and returns the format with id (nil
// when it was dropped) and the page.
func parseOne(t *testing.T, id string, formats ...string) (*Format, *Info) {
	t.Helper()
	info, err := Parse(page(formats...), testNow)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return info.Format(id), info
}

// Formats with an unusable value are dropped; fields of the wrong type or out of
// range count as absent.
func TestParseFormatFields(t *testing.T) {
	long := strings.Repeat("a", maxHeaderValueBytes+1)
	for name, c := range map[string]struct {
		format string
		keep   bool
		check  func(f *Format) bool
	}{
		"plain":                {muxedFormat("f", ""), true, func(f *Format) bool { return f.Kind == media.KindMuxed && f.Ext == "mp4" && f.Height == 360 }},
		"width as text":        {muxedFormat("f", `"width": "1920"`), true, func(f *Format) bool { return f.Width == 0 }},
		"height too large":     {muxedFormat("f", `"height": 16385`), true, func(f *Format) bool { return f.Height == 0 }},
		"height fractional":    {muxedFormat("f", `"height": 360.5`), true, func(f *Format) bool { return f.Height == 0 }},
		"fps too high":         {muxedFormat("f", `"fps": 1001`), true, func(f *Format) bool { return f.FPS == 0 }},
		"fps NaN":              {muxedFormat("f", `"fps": NaN`), true, func(f *Format) bool { return f.FPS == 0 }},
		"tbr Infinity":         {muxedFormat("f", `"tbr": Infinity, "vbr": -Infinity`), true, func(f *Format) bool { return f.TBR == 0 && f.VBR == 0 }},
		"tbr huge exponent":    {muxedFormat("f", `"tbr": 1e400`), true, func(f *Format) bool { return f.TBR == 0 }},
		"asr fractional":       {muxedFormat("f", `"asr": 44100.7`), true, func(f *Format) bool { return f.ASR == 44100 }},
		"filesize zero":        {muxedFormat("f", `"filesize": 0, "filesize_approx": 10`), true, func(f *Format) bool { return f.Size == 0 && f.ApproxSize == 10 }},
		"filesize over 1 TiB":  {muxedFormat("f", `"filesize": 1099511627777`), true, func(f *Format) bool { return f.Size == 0 }},
		"filesize fractional":  {muxedFormat("f", `"filesize": 1.5`), true, func(f *Format) bool { return f.Size == 0 }},
		"approx fractional":    {muxedFormat("f", `"filesize_approx": 1234.7`), true, func(f *Format) bool { return f.ApproxSize == 1234 }},
		"exact wins":           {muxedFormat("f", `"filesize": 5, "filesize_approx": 10`), true, func(f *Format) bool { return f.Size == 5 && f.ApproxSize == 0 }},
		"ext upper case":       {muxedFormat("f", `"ext": "MP4"`), true, func(f *Format) bool { return f.Ext == "" }},
		"language invalid":     {muxedFormat("f", `"language": "en_US"`), true, func(f *Format) bool { return f.Language == "" }},
		"language kept":        {muxedFormat("f", `"language": "pt-BR"`), true, func(f *Format) bool { return f.Language == "pt-BR" }},
		"language too long":    {muxedFormat("f", `"language": "`+strings.Repeat("a", 36)+`"`), true, func(f *Format) bool { return f.Language == "" }},
		"preference range":     {muxedFormat("f", `"language_preference": 1001`), true, func(f *Format) bool { return f.LanguagePreference == -1 }},
		"preference kept":      {muxedFormat("f", `"language_preference": -10`), true, func(f *Format) bool { return f.LanguagePreference == -10 }},
		"hdr HLG":              {muxedFormat("f", `"dynamic_range": "HLG"`), true, func(f *Format) bool { return f.HDR }},
		"sdr":                  {muxedFormat("f", `"dynamic_range": "SDR"`), true, func(f *Format) bool { return !f.HDR }},
		"dynamic range long":   {muxedFormat("f", `"dynamic_range": "`+strings.Repeat("H", 17)+`"`), true, func(f *Format) bool { return !f.HDR }},
		"container long":       {muxedFormat("f", `"container": "`+strings.Repeat("c", 33)+`"`), true, func(f *Format) bool { return f.Container == "" }},
		"note cleaned":         {muxedFormat("f", `"format_note": " a\u0000b‮  c "`), true, func(f *Format) bool { return f.Note == "a b c" }},
		"note truncated":       {muxedFormat("f", `"format_note": "`+strings.Repeat("n", 200)+`"`), true, func(f *Format) bool { return f.Note == strings.Repeat("n", 128) }},
		"chunk size small":     {muxedFormat("f", `"downloader_options": {"http_chunk_size": 1000}`), true, func(f *Format) bool { return f.ChunkSize == 0 }},
		"chunk size large":     {muxedFormat("f", `"downloader_options": {"http_chunk_size": 2147483648}`), true, func(f *Format) bool { return f.ChunkSize == 0 }},
		"chunk size kept":      {muxedFormat("f", `"downloader_options": {"http_chunk_size": 65536}`), true, func(f *Format) bool { return f.ChunkSize == 65536 }},
		"options not object":   {muxedFormat("f", `"downloader_options": [1]`), true, func(f *Format) bool { return f.ChunkSize == 0 }},
		"available soon":       {muxedFormat("f", fmt.Sprintf(`"available_at": %d`, testNow.Add(9*time.Minute).Unix())), true, func(f *Format) bool { return f.AvailableAt.Equal(testNow.Add(9 * time.Minute)) }},
		"available already":    {muxedFormat("f", fmt.Sprintf(`"available_at": %d`, testNow.Add(-time.Hour).Unix())), true, func(f *Format) bool { return f.AvailableAt.IsZero() }},
		"available later":      {muxedFormat("f", fmt.Sprintf(`"available_at": %d`, testNow.Add(11*time.Minute).Unix())), false, nil},
		"no resume":            {muxedFormat("f", `"no_resume": true`), true, func(f *Format) bool { return f.Unranged }},
		"no resume text":       {muxedFormat("f", `"no_resume": "true"`), true, func(f *Format) bool { return !f.Unranged }},
		"drm false":            {muxedFormat("f", `"has_drm": false`), true, nil},
		"empty extras":         {muxedFormat("f", `"request_data": "", "impersonate": false, "is_from_start": null, "hls_aes": {}, "fragments": []`), true, nil},
		"request data":         {muxedFormat("f", `"request_data": "a=b"`), false, nil},
		"impersonate":          {muxedFormat("f", `"impersonate": true`), false, nil},
		"impersonate target":   {muxedFormat("f", `"impersonate": "chrome"`), false, nil},
		"hls aes":              {muxedFormat("f", `"hls_aes": {"uri": "k"}`), false, nil},
		"segment param":        {muxedFormat("f", `"extra_param_to_segment_url": "a=b"`), false, nil},
		"key param":            {muxedFormat("f", `"extra_param_to_key_url": "a=b"`), false, nil},
		"from start":           {muxedFormat("f", `"is_from_start": true`), false, nil},
		"direct fragments":     {muxedFormat("f", `"fragments": [{"url": "https://cdn.example.com/1"}]`), false, nil},
		"id with space":        {muxedFormat("f g", ""), false, nil},
		"id too long":          {muxedFormat(strings.Repeat("f", 65), ""), false, nil},
		"protocol m3u8":        {muxedFormat("f", `"protocol": "m3u8"`), false, nil},
		"protocol missing":     {muxedFormat("f", `"protocol": null`), false, nil},
		"url ftp":              {muxedFormat("f", `"url": "ftp://cdn.example.com/v"`), false, nil},
		"url relative":         {muxedFormat("f", `"url": "/v.mp4"`), false, nil},
		"url userinfo":         {muxedFormat("f", `"url": "https://user:pw@cdn.example.com/v"`), false, nil},
		"url too long":         {muxedFormat("f", `"url": "https://cdn.example.com/`+strings.Repeat("a", 32<<10)+`"`), false, nil},
		"no streams":           {muxedFormat("f", `"vcodec": "none", "acodec": "none"`), false, nil},
		"codecs unknown mp4":   {muxedFormat("f", `"vcodec": null, "acodec": null`), true, func(f *Format) bool { return f.Kind == media.KindMuxed && f.VCodec == "" && f.ACodec == "" }},
		"codecs unknown m4a":   {muxedFormat("f", `"ext": "m4a", "vcodec": null, "acodec": null`), true, func(f *Format) bool { return f.Kind == media.KindAudio }},
		"codecs unknown other": {muxedFormat("f", `"ext": "bin", "vcodec": null, "acodec": null`), false, nil},
		"audio unknown":        {muxedFormat("f", `"acodec": null`), true, func(f *Format) bool { return f.Kind == media.KindMuxed && f.ACodec == "" }},
		"video only":           {muxedFormat("f", `"acodec": "none"`), true, func(f *Format) bool { return f.Kind == media.KindVideo && f.ACodec == "" }},
		"codec too long":       {muxedFormat("f", `"vcodec": "`+strings.Repeat("v", 129)+`", "acodec": "none"`), true, func(f *Format) bool { return f.Kind == media.KindVideo && f.VCodec == "" }},
		"headers renamed":      {muxedFormat("f", `"http_headers": {"user-agent": "UA", "x-a": "1"}`), true, func(f *Format) bool { return f.Headers.Get("User-Agent") == "UA" && f.Headers.Get("X-A") == "1" }},
		"headers dropped":      {muxedFormat("f", `"http_headers": {"Cookie": "a=b", "Authorization": "x", "Proxy-Authorization": "x", "Host": "h", "Range": "bytes=0-", "If-Range": "x", "Content-Type": "x", "Accept-Encoding": "gzip", "Connection": "close", "Transfer-Encoding": "chunked", "Te": "x"}`), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"header value long":    {muxedFormat("f", `"http_headers": {"Referer": "`+long+`", "Accept": "*/*"}`), true, func(f *Format) bool { return len(f.Headers) == 1 && f.Headers.Get("Accept") == "*/*" }},
		"header not ascii":     {muxedFormat("f", `"http_headers": {"Referer": "https://example.com/é"}`), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"header control":       {muxedFormat("f", `"http_headers": {"Referer": "a\r\nX-Evil: 1"}`), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"header not string":    {muxedFormat("f", `"http_headers": {"Referer": 5}`), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"header first wins":    {muxedFormat("f", `"http_headers": {"Accept": "a", "accept": "b"}`), true, func(f *Format) bool { return reflect.DeepEqual(f.Headers["Accept"], []string{"a"}) }},
		"headers not object":   {muxedFormat("f", `"http_headers": ["Accept", "a"]`), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"bad x header":         {muxedFormat("f", `"http_headers": {"X-": "a", "X-a_b": "b", "X-`+strings.Repeat("a", 65)+`": "c"}`), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"x header cap": {muxedFormat("f", xHeaders(9)), true, func(f *Format) bool {
			return len(f.Headers) == 8 && f.Headers.Get("X-H8") == "" && f.Headers.Get("X-H7") == "7"
		}},
		"too many headers":  {muxedFormat("f", tooManyHeaders()), true, func(f *Format) bool { return len(f.Headers) == 0 }},
		"headers too large": {muxedFormat("f", largeHeaders()), false, nil},
		"cookies too long":  {muxedFormat("f", `"cookies": "a=b; Domain=cdn.example.com; x=`+strings.Repeat("y", 16<<10)+`"`), true, func(f *Format) bool { return f.Headers.Get("Cookie") == "" }},
		"cookie built":      {muxedFormat("f", `"cookies": "a=b; Domain=.example.com; Path=/; Secure", "http_headers": {"Cookie": "c=d"}`), true, func(f *Format) bool { return reflect.DeepEqual(f.Headers["Cookie"], []string{"a=b"}) }},
	} {
		f, info := parseOne(t, "f", c.format)
		switch {
		case (f != nil) != c.keep:
			t.Errorf("%s: kept = %v, want %v", name, f != nil, c.keep)
		case f != nil && c.check != nil && !c.check(f):
			t.Errorf("%s: format = %+v", name, *f)
		case info.Protected != 0:
			t.Errorf("%s: %d protected formats", name, info.Protected)
		}
	}
}

func xHeaders(n int) string {
	var b strings.Builder
	b.WriteString(`"http_headers": {`)
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, `"X-H%d": "%d"`, i, i)
	}
	b.WriteString("}")
	return b.String()
}

func tooManyHeaders() string {
	var b strings.Builder
	b.WriteString(`"http_headers": {"Accept": "*/*"`)
	for i := range maxHeaderEntries {
		fmt.Fprintf(&b, `, "Other-%d": "x"`, i)
	}
	b.WriteString("}")
	return b.String()
}

// largeHeaders are allowed headers of 4 KiB each, together above validate.Headers'
// 64 KiB.
func largeHeaders() string {
	v := strings.Repeat("v", maxHeaderValueBytes)
	var b strings.Builder
	b.WriteString(`"http_headers": {`)
	for i, name := range allowedHeaders {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q: %q", name, v)
	}
	for i := range maxXHeaders {
		fmt.Fprintf(&b, `, "X-L%d": %q`, i, v)
	}
	b.WriteString("}")
	return b.String()
}

// DRM-protected formats are dropped and counted; a later format with an ID seen
// before is dropped; a manifest format falls back from manifest_url to url.
func TestParseFormatList(t *testing.T) {
	_, info := parseOne(t, "x",
		muxedFormat("a", `"has_drm": true`),
		muxedFormat("b", `"has_drm": "maybe"`),
		muxedFormat("c", ""),
		muxedFormat("c", `"height": 720`),
		`5`, `"text"`, `null`, `[]`,
		`{"format_id": "m", "protocol": "m3u8_native", "url": "https://cdn.example.com/m.m3u8", "manifest_url": "nope", "ext": "mp4", "fragments": [{"path": "a"}]}`,
		`{"format_id": "n", "protocol": "http_dash_segments", "manifest_url": "https://cdn.example.com/n.mpd"}`,
		`{"format_id": "o", "protocol": "http_dash_segments", "manifest_url": "nope", "url": "ftp://x"}`,
	)
	if info.Protected != 2 {
		t.Errorf("protected = %d, want 2", info.Protected)
	}
	var ids []string
	for _, f := range info.Formats {
		ids = append(ids, f.ID)
	}
	if !reflect.DeepEqual(ids, []string{"c", "m", "n"}) {
		t.Fatalf("formats %v, want [c m n]", ids)
	}
	if f := info.Format("c"); f.Height != 360 {
		t.Errorf("duplicate replaced the first format: height %d", f.Height)
	}
	if f := info.Format("m"); f.ManifestURL != "https://cdn.example.com/m.m3u8" || f.URL != "" || f.Kind != media.KindMuxed {
		t.Errorf("manifest format m = %+v", f)
	}
	if f := info.Format("n"); f.ManifestURL != "https://cdn.example.com/n.mpd" || f.Kind != media.KindMuxed {
		t.Errorf("manifest format n = %+v, want unknown content as muxed", f)
	}
}

// URL hints of direct formats: expire within 7 days, the ip family, lmt digits.
func TestParseURLHints(t *testing.T) {
	soon := testNow.Add(time.Hour).Unix()
	for name, c := range map[string]struct {
		query   string
		expires time.Time
		network string
		lmt     string
	}{
		"all":           {fmt.Sprintf("expire=%d&ip=198.51.100.4&lmt=1700000000000001", soon), time.Unix(soon, 0), "tcp4", "1700000000000001"},
		"ipv6":          {"ip=2001:db8::1", time.Time{}, "tcp6", ""},
		"mapped ipv4":   {"ip=::ffff:198.51.100.4", time.Time{}, "tcp4", ""},
		"not an ip":     {"ip=example.com&lmt=12a", time.Time{}, "", ""},
		"expired":       {fmt.Sprintf("expire=%d", testNow.Add(-time.Minute).Unix()), time.Time{}, "", ""},
		"too far":       {fmt.Sprintf("expire=%d", testNow.Add(8*24*time.Hour).Unix()), time.Time{}, "", ""},
		"signed expire": {fmt.Sprintf("expire=+%d", soon), time.Time{}, "", ""},
		"long lmt":      {"lmt=" + strings.Repeat("1", 21), time.Time{}, "", ""},
		"semicolons":    {fmt.Sprintf("a=b;c&expire=%d", soon), time.Unix(soon, 0), "", ""},
	} {
		f, _ := parseOne(t, "f", muxedFormat("f", `"url": "https://cdn.example.com/v?`+c.query+`"`))
		if f == nil {
			t.Errorf("%s: dropped", name)
			continue
		}
		if !f.Expires.Equal(c.expires) || f.Network != c.network || f.LMT != c.lmt {
			t.Errorf("%s: expires %v, network %q, lmt %q; want %v, %q, %q", name, f.Expires, f.Network, f.LMT, c.expires, c.network, c.lmt)
		}
	}
	// Manifest formats take no hints.
	f, _ := parseOne(t, "m", `{"format_id": "m", "protocol": "m3u8_native", "url": "https://cdn.example.com/m.m3u8?expire=`+
		fmt.Sprint(soon)+`&ip=1.2.3.4&lmt=5", "vcodec": "avc1", "acodec": "none"}`)
	if f == nil || !f.Expires.IsZero() || f.Network != "" || f.LMT != "" {
		t.Errorf("manifest format = %+v, want no URL hints", f)
	}
}

// The Cookie header holds only the cookies for the request URL (RFC 6265 domain and
// path matching), never expired ones or Secure ones over http, within its limits.
func TestCookieHeader(t *testing.T) {
	many := make([]string, 60)
	for i := range many {
		many[i] = fmt.Sprintf("c%d=v; Domain=example.com", i)
	}
	wantMany := make([]string, maxCookies)
	for i := range wantMany {
		wantMany[i] = fmt.Sprintf("c%d=v", i)
	}
	big := strings.Repeat("v", 3000)
	for name, c := range map[string]struct {
		cookies, target, want string
	}{
		"exact domain":      {"a=1; Domain=cdn.example.com", "https://cdn.example.com/v", "a=1"},
		"parent domain":     {"a=1; Domain=.example.com; b=2; Domain=example.com", "https://a.b.example.com/v", "a=1; b=2"},
		"other domain":      {"a=1; Domain=example.net; b=2; Domain=ample.com", "https://cdn.example.com/v", ""},
		"case and dot":      {"a=1; DOMAIN=Example.COM.", "https://CDN.example.com./v", "a=1"},
		"no domain":         {"a=1; Path=/", "https://cdn.example.com/v", ""},
		"ip host suffix":    {"a=1; Domain=0.2.1", "https://192.0.2.1/v", ""},
		"ip host exact":     {"a=1; Domain=192.0.2.1", "https://192.0.2.1/v", "a=1"},
		"path prefix":       {"a=1; Domain=example.com; Path=/videos; b=2; Domain=example.com; Path=/videos/", "https://example.com/videos/1", "a=1; b=2"},
		"path not prefix":   {"a=1; Domain=example.com; Path=/vid; b=2; Domain=example.com; Path=/other", "https://example.com/videos", ""},
		"empty path":        {"a=1; Domain=example.com; Path=/x", "https://example.com", ""},
		"secure over http":  {"a=1; Domain=example.com; Secure; b=2; Domain=example.com", "http://example.com/", "b=2"},
		"expired":           {fmt.Sprintf("a=1; Domain=example.com; Expires=%d; b=2; Domain=example.com; Expires=%d", testNow.Unix(), testNow.Unix()+1), "https://example.com/", "b=2"},
		"bad expires":       {"a=1; Domain=example.com; Expires=Thu, 01 Jan", "https://example.com/", "a=1"},
		"bad name":          {"a b=1; Domain=example.com; c=2; Domain=example.com", "https://example.com/", "c=2"},
		"no value":          {"flag; Domain=example.com", "https://example.com/", ""},
		"attribute first":   {"Domain=example.com; a=1; Domain=example.com", "https://example.com/", "a=1"},
		"value not ascii":   {"a=é; Domain=example.com", "https://example.com/", ""},
		"other attributes":  {"a=1; Domain=example.com; HttpOnly; SameSite=Lax; Max-Age=5; Version=0", "https://example.com/", "a=1"},
		"quoted value":      {`a="x y"; Domain=example.com`, "https://example.com/", `a="x y"`},
		"count cap":         {strings.Join(many, "; "), "https://example.com/", strings.Join(wantMany, "; ")},
		"size cap":          {fmt.Sprintf("a=%s; Domain=example.com; b=%s; Domain=example.com; c=%s; Domain=example.com", big, big, big), "https://example.com/", "a=" + big + "; b=" + big},
		"unparsable target": {"a=1; Domain=example.com", "://", ""},
	} {
		if got := cookieHeader(c.cookies, c.target, testNow); got != c.want {
			t.Errorf("%s: cookieHeader = %q, want %q", name, got, c.want)
		}
	}
}

// Python's non-finite numbers outside strings read as null; strings keep them.
func TestFiniteJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": NaN, "b": [Infinity, -Infinity]}`: `{"a": null, "b": [null, null]}`,
		`{"a": "NaN \" Infinity", "b": 1}`:       `{"a": "NaN \" Infinity", "b": 1}`,
		`{"a": "\\", "b": NaN}`:                  `{"a": "\\", "b": null}`,
		`{"a": -1.5e3}`:                          `{"a": -1.5e3}`,
	} {
		if got := string(finiteJSON([]byte(in))); got != want {
			t.Errorf("finiteJSON(%s) = %s, want %s", in, got, want)
		}
	}
	in := []byte(`{"a": 1}`)
	if out := finiteJSON(in); &out[0] != &in[0] {
		t.Error("finiteJSON copied input without non-finite numbers")
	}
}

// Chapters for choosing a part (docs/MEDIA-SPEC.md §8 C14): invalid entries are dropped,
// titles cleaned, overlaps dropped; a list that is not one, or too long, means none.
func TestParseChapters(t *testing.T) {
	data := `{"id": "x", "extractor_key": "Generic", "title": "t", "duration": 754, "chapters": [
		{"start_time": 80, "end_time": 165.5, "title": "Results\nand more"},
		{"start_time": 0, "end_time": 80, "title": "Intro"},
		{"start_time": 100, "end_time": 120, "title": "overlap"},
		{"start_time": "1", "end_time": 2, "title": "wrong type"},
		{"start_time": 700, "end_time": 900, "title": "past the end"},
		{"start_time": 165.5, "end_time": 754, "title": null}]}`
	info, err := Parse([]byte(data), testNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []validate.Chapter{{StartMs: 0, EndMs: 80_000, Title: "Intro"}, {StartMs: 80_000, EndMs: 165_500, Title: "Results and more"}, {StartMs: 165_500, EndMs: 754_000}}
	if !reflect.DeepEqual(info.Chapters, want) {
		t.Errorf("chapters %+v, want %+v", info.Chapters, want)
	}
	for _, v := range []string{`"chapters": {}`, `"chapters": "x"`, `"chapters": [` + strings.Repeat(`{"start_time":0,"end_time":1},`, validate.MaxChapters) + `{"start_time":1,"end_time":2}]`} {
		info, err := Parse([]byte(`{"id": "x", "extractor_key": "Generic", `+v+`}`), testNow)
		if err != nil || info.Chapters != nil {
			t.Errorf("%.40s: %v %+v", v, err, info.Chapters)
		}
	}
}

// Facebook's titles lead with the post's counts; the video's own text is its title
// (measured 2026-11-18). Other sites' titles keep their numbers.
func TestParseFacebookCounts(t *testing.T) {
	for _, c := range []struct{ key, title, want string }{
		{"Facebook", "4.6K views · 38K reactions | The trend | Example Page", "The trend | Example Page"},
		{"FacebookReel", "1,204 views · 9 reactions | A clip", "A clip"},
		{"Facebook", "12 views | ", "12 views |"},
		{"Facebook", "Morning walk | Example", "Morning walk | Example"},
		{"Youtube", "10 things | You missed", "10 things | You missed"},
	} {
		data := `{"id": "x", "extractor_key": "` + c.key + `", "title": "` + c.title + `"}`
		info, err := Parse([]byte(data), testNow)
		if err != nil {
			t.Fatal(err)
		}
		if info.Title != c.want {
			t.Errorf("%s %q: title %q, want %q", c.key, c.title, info.Title, c.want)
		}
	}
}
