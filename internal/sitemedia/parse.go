package sitemedia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// Limits on single fields of yt-dlp's output (docs/SITE-MEDIA-SPEC.md §3.10, §6.2).
const (
	maxIDBytes           = 256
	maxFormatIDBytes     = 64
	maxCodecBytes        = 128
	maxContainerBytes    = 32
	maxDynamicRange      = 16
	maxLanguageBytes     = 35
	maxNoteRunes         = 128
	maxAvailability      = 32
	maxDuration          = 1e7 // seconds
	maxDimension         = 16_384
	maxFPS               = 1_000
	maxBitrate           = 1e6 // kbit/s, and Hz for asr
	maxFormatSize        = 1 << 40
	minHTTPChunk         = 64 << 10
	maxHTTPChunk         = 1 << 30
	maxLanguagePref      = 1_000
	maxHeaderEntries     = 32
	maxHeaderValueBytes  = 4 << 10
	maxXHeaders          = 8
	maxCookiesFieldBytes = 16 << 10
	maxCookies           = 50
	maxCookieBytes       = 8 << 10
	maxQueryDigits       = 20
	// availableAhead is how far ahead available_at may be before a format is dropped;
	// expireAhead how far ahead an expire parameter is believed.
	availableAhead = 10 * time.Minute
	expireAhead    = 7 * 24 * time.Hour
)

var (
	extractorKeyRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	extRE          = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	languageRE     = regexp.MustCompile(`^[A-Za-z0-9-]{1,35}$`)
	availabilityRE = regexp.MustCompile(`^[a-z_]{1,32}$`)
	xHeaderRE      = regexp.MustCompile(`^X-[A-Za-z0-9-]{1,64}$`)
)

// allowedHeaders are the request headers a format may set besides X-* headers
// (docs/SITE-MEDIA-SPEC.md §6.2), canonical.
var allowedHeaders = []string{
	"User-Agent", "Accept", "Accept-Language", "Referer", "Origin",
	"Sec-Fetch-Mode", "Sec-Fetch-Dest", "Sec-Fetch-Site",
}

// rawInfo holds the top-level fields Parse reads, undecoded, so that a field of the
// wrong type only makes that field absent.
type rawInfo struct {
	Type         json.RawMessage `json:"_type"`
	ID           json.RawMessage `json:"id"`
	ExtractorKey json.RawMessage `json:"extractor_key"`
	Title        json.RawMessage `json:"title"`
	Duration     json.RawMessage `json:"duration"`
	IsLive       json.RawMessage `json:"is_live"`
	LiveStatus   json.RawMessage `json:"live_status"`
	HasDRM       json.RawMessage `json:"_has_drm"`
	Availability json.RawMessage `json:"availability"`
	Formats      json.RawMessage `json:"formats"`
}

// rawFormat holds the fields Parse reads of one format, undecoded.
type rawFormat struct {
	FormatID           json.RawMessage `json:"format_id"`
	Ext                json.RawMessage `json:"ext"`
	Protocol           json.RawMessage `json:"protocol"`
	URL                json.RawMessage `json:"url"`
	ManifestURL        json.RawMessage `json:"manifest_url"`
	VCodec             json.RawMessage `json:"vcodec"`
	ACodec             json.RawMessage `json:"acodec"`
	Width              json.RawMessage `json:"width"`
	Height             json.RawMessage `json:"height"`
	FPS                json.RawMessage `json:"fps"`
	TBR                json.RawMessage `json:"tbr"`
	VBR                json.RawMessage `json:"vbr"`
	ABR                json.RawMessage `json:"abr"`
	ASR                json.RawMessage `json:"asr"`
	Filesize           json.RawMessage `json:"filesize"`
	FilesizeApprox     json.RawMessage `json:"filesize_approx"`
	Container          json.RawMessage `json:"container"`
	DynamicRange       json.RawMessage `json:"dynamic_range"`
	Language           json.RawMessage `json:"language"`
	LanguagePreference json.RawMessage `json:"language_preference"`
	FormatNote         json.RawMessage `json:"format_note"`
	HTTPHeaders        json.RawMessage `json:"http_headers"`
	Cookies            json.RawMessage `json:"cookies"`
	DownloaderOptions  json.RawMessage `json:"downloader_options"`
	AvailableAt        json.RawMessage `json:"available_at"`
	HasDRM             json.RawMessage `json:"has_drm"`
	NoResume           json.RawMessage `json:"no_resume"`
	// Present in a usable format only as null, false or empty: each needs a download
	// step Downex does not have.
	RequestData   json.RawMessage `json:"request_data"`
	Impersonate   json.RawMessage `json:"impersonate"`
	HLSAES        json.RawMessage `json:"hls_aes"`
	ExtraSegParam json.RawMessage `json:"extra_param_to_segment_url"`
	ExtraKeyParam json.RawMessage `json:"extra_param_to_key_url"`
	IsFromStart   json.RawMessage `json:"is_from_start"`
	Fragments     json.RawMessage `json:"fragments"`
}

// Parse reads what yt-dlp -J printed for a page (docs/SITE-MEDIA-SPEC.md §3.10) at the
// time now, which judges available_at, expire parameters and cookie expiry.
//
// Unknown fields are ignored, a field of the wrong type or out of range counts as
// absent, and a format that is then unusable is dropped. Parse refuses output larger
// than MaxOutputBytes or with more than MaxFormats formats (media.ErrTooLarge),
// output that is not one JSON object or lacks a valid id or extractor_key
// (media.ErrMalformed), a playlist (media.ErrUnsupported), a link to another page
// (ErrSiteUnsupported) and a live, upcoming or still processed stream
// (media.ErrLive). Whether anything usable remains is for Offers to say. Errors never
// repeat the output.
func Parse(data []byte, now time.Time) (*Info, error) {
	if len(data) > MaxOutputBytes {
		return nil, fmt.Errorf("%w: yt-dlp output is larger than %d bytes", media.ErrTooLarge, MaxOutputBytes)
	}
	data = finiteJSON(data)
	var raw rawInfo
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: yt-dlp output is not a JSON object", media.ErrMalformed)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: yt-dlp output is not valid JSON", media.ErrMalformed)
	}
	typ, _ := jsonString(raw.Type)
	switch typ {
	case "", "video":
	case "playlist", "multi_video":
		return nil, fmt.Errorf("%w: the page is a playlist; open a single video of it", media.ErrUnsupported)
	case "url", "url_transparent":
		return nil, fmt.Errorf("%w: yt-dlp found only a link to another page", ErrSiteUnsupported)
	default:
		return nil, fmt.Errorf("%w: yt-dlp returned an unknown kind of result", media.ErrUnsupported)
	}
	if live, _ := jsonBool(raw.IsLive); live {
		return nil, fmt.Errorf("%w: the stream is live", media.ErrLive)
	}
	switch status, _ := jsonString(raw.LiveStatus); status {
	case "is_live":
		return nil, fmt.Errorf("%w: the stream is live", media.ErrLive)
	case "is_upcoming":
		return nil, fmt.Errorf("%w: the stream has not started yet", media.ErrLive)
	case "post_live":
		return nil, fmt.Errorf("%w: the stream has ended but is still being processed", media.ErrLive)
	}
	info := &Info{}
	if id, ok := jsonString(raw.ID); ok && id != "" && len(id) <= maxIDBytes && printable(id, true) {
		info.ID = id
	} else {
		return nil, fmt.Errorf("%w: yt-dlp output has no valid id", media.ErrMalformed)
	}
	if key, ok := jsonString(raw.ExtractorKey); ok && extractorKeyRE.MatchString(key) {
		info.ExtractorKey = key
		info.Site = SiteName(key)
	} else {
		return nil, fmt.Errorf("%w: yt-dlp output has no valid extractor_key", media.ErrMalformed)
	}
	title, _ := jsonString(raw.Title)
	if info.Title = cleanText(title, validate.MaxTitleRunes); info.Title == "" {
		info.Title = info.ID
	}
	if d, ok := floatIn(raw.Duration, 0, maxDuration); ok {
		info.Duration = time.Duration(d * float64(time.Second))
	}
	info.HasDRM, _ = jsonBool(raw.HasDRM)
	if a, ok := jsonString(raw.Availability); ok && availabilityRE.MatchString(a) {
		info.Availability = a
	}
	seen := make(map[string]bool)
	err := eachElement(raw.Formats, MaxFormats, func(el json.RawMessage) {
		f, protected, ok := parseFormat(el, info, seen, now)
		switch {
		case protected:
			info.Protected++
		case ok:
			info.Formats = append(info.Formats, f)
		}
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// parseFormat validates one element of formats. ok reports a format to keep;
// protected one dropped because it is DRM-protected. seen collects the format IDs
// met so far: a later format with the same ID is dropped.
func parseFormat(el json.RawMessage, info *Info, seen map[string]bool, now time.Time) (f Format, protected, ok bool) {
	var raw rawFormat
	if len(el) == 0 || el[0] != '{' || json.Unmarshal(el, &raw) != nil {
		return f, false, false
	}
	id, ok := jsonString(raw.FormatID)
	if !ok || id == "" || len(id) > maxFormatIDBytes || !printable(id, false) || seen[id] {
		return f, false, false
	}
	seen[id] = true
	f.ID = id
	if b, _ := jsonBool(raw.HasDRM); b {
		return f, true, false
	}
	if s, _ := jsonString(raw.HasDRM); s == "maybe" {
		return f, true, false
	}
	for _, v := range []json.RawMessage{raw.RequestData, raw.Impersonate, raw.HLSAES, raw.ExtraSegParam,
		raw.ExtraKeyParam, raw.IsFromStart} {
		if present(v) {
			return f, false, false
		}
	}
	f.Protocol, _ = jsonString(raw.Protocol)
	cookieURL := ""
	switch f.Protocol {
	case protoHTTPS, protoHTTP:
		if f.URL, ok = formatURL(raw.URL); !ok || present(raw.Fragments) {
			return f, false, false
		}
		cookieURL = f.URL
	case protoHLS, protoDASH:
		if f.ManifestURL, ok = formatURL(raw.ManifestURL); !ok {
			if f.ManifestURL, ok = formatURL(raw.URL); !ok {
				return f, false, false
			}
		}
		cookieURL = f.ManifestURL
	default:
		return f, false, false
	}
	if ext, ok := jsonString(raw.Ext); ok && extRE.MatchString(ext) {
		f.Ext = ext
	}
	vcodec, hasVideo := codecField(raw.VCodec, f.Ext, videoExts)
	acodec, hasAudio := codecField(raw.ACodec, f.Ext, videoExts, audioExts)
	f.VCodec, f.ACodec = vcodec, acodec
	switch {
	case hasVideo && hasAudio:
		f.Kind = media.KindMuxed
	case hasVideo:
		f.Kind = media.KindVideo
	case hasAudio:
		f.Kind = media.KindAudio
	case !f.direct():
		f.Kind = media.KindMuxed // a manifest track of unknown content: the probe tells
	default:
		return f, false, false
	}
	if v, ok := intIn(raw.Width, 0, maxDimension); ok {
		f.Width = int(v)
	}
	if v, ok := intIn(raw.Height, 0, maxDimension); ok {
		f.Height = int(v)
	}
	f.FPS, _ = floatIn(raw.FPS, 0, maxFPS)
	f.TBR, _ = floatIn(raw.TBR, 0, maxBitrate)
	f.VBR, _ = floatIn(raw.VBR, 0, maxBitrate)
	f.ABR, _ = floatIn(raw.ABR, 0, maxBitrate)
	if v, ok := floatIn(raw.ASR, 0, maxBitrate); ok {
		f.ASR = int(v)
	}
	if v, ok := intIn(raw.Filesize, 1, maxFormatSize); ok {
		f.Size = v
	} else if v, ok := floatIn(raw.FilesizeApprox, 1, maxFormatSize); ok {
		f.ApproxSize = int64(v)
	}
	if s, ok := jsonString(raw.Container); ok && len(s) <= maxContainerBytes && printable(s, false) {
		f.Container = s
	}
	if s, ok := jsonString(raw.DynamicRange); ok && len(s) <= maxDynamicRange && s != "" && s != "SDR" {
		f.HDR = true
	}
	if s, ok := jsonString(raw.Language); ok && languageRE.MatchString(s) {
		f.Language = s
	}
	f.LanguagePreference = -1
	if v, ok := intIn(raw.LanguagePreference, -maxLanguagePref, maxLanguagePref); ok {
		f.LanguagePreference = int(v)
	}
	note, _ := jsonString(raw.FormatNote)
	f.Note = cleanText(note, maxNoteRunes)
	var opts struct {
		HTTPChunkSize json.RawMessage `json:"http_chunk_size"`
	}
	if len(raw.DownloaderOptions) > 0 && raw.DownloaderOptions[0] == '{' && json.Unmarshal(raw.DownloaderOptions, &opts) == nil {
		f.ChunkSize, _ = intIn(opts.HTTPChunkSize, minHTTPChunk, maxHTTPChunk)
	}
	if v, ok := intIn(raw.AvailableAt, 0, maxFormatSize); ok {
		switch at := time.Unix(v, 0).UTC(); {
		case at.After(now.Add(availableAhead)):
			return f, false, false
		case at.After(now):
			f.AvailableAt = at
		}
	}
	f.Unranged, _ = jsonBool(raw.NoResume)
	if f.direct() {
		f.Expires, f.Network, f.LMT = urlHints(f.URL, now)
	}
	if f.Headers, ok = formatHeaders(raw.HTTPHeaders, raw.Cookies, cookieURL, now); !ok {
		return f, false, false
	}
	f.Key = Key(info.ExtractorKey, info.ID, f.ID, f.Language, f.Size, f.LMT)
	return f, false, true
}

// codecField reads a vcodec or acodec field: the codec ("" when none or unknown) and
// whether the format carries that kind of stream. A codec is known unless the field
// is absent or invalid; "none" says the stream is absent; an unknown codec is taken
// to be present when the file type is one of exts.
func codecField(raw json.RawMessage, ext string, exts ...[]string) (codec string, has bool) {
	s, ok := jsonString(raw)
	switch {
	case ok && s == "none":
		return "", false
	case ok && s != "" && len(s) <= maxCodecBytes && printable(s, true):
		return s, true
	}
	for _, list := range exts {
		if slices.Contains(list, ext) {
			return "", true
		}
	}
	return "", false
}

// formatURL reads a format or manifest URL: validate.URL (absolute http(s) with a
// host, at most 32 KiB) and no user information, which Downex never sends.
func formatURL(raw json.RawMessage) (string, bool) {
	s, ok := jsonString(raw)
	if !ok || validate.URL(s) != nil {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil {
		return "", false
	}
	return s, true
}

// urlHints reads the hints of a direct format's URL query (docs/SITE-MEDIA-SPEC.md
// §3.10): expire (decimal Unix seconds within the next 7 days), ip (an IP literal: the
// address family to dial) and lmt (at most 20 decimal digits).
func urlHints(raw string, now time.Time) (expires time.Time, network, lmt string) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	q, _ := url.ParseQuery(u.RawQuery) // keeps what parses
	if v := q.Get("expire"); decimal(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			if t := time.Unix(n, 0).UTC(); t.After(now) && !t.After(now.Add(expireAhead)) {
				expires = t
			}
		}
	}
	if addr, err := netip.ParseAddr(q.Get("ip")); err == nil {
		network = "tcp6"
		if addr.Unmap().Is4() {
			network = "tcp4"
		}
	}
	if v := q.Get("lmt"); decimal(v) {
		lmt = v
	}
	return
}

// decimal reports 1 to 20 decimal digits.
func decimal(s string) bool {
	if s == "" || len(s) > maxQueryDigits {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// formatHeaders builds the request headers of a format (docs/SITE-MEDIA-SPEC.md §6.2):
// the allow-listed entries of http_headers, at most maxXHeaders X-* headers, values of
// printable ASCII up to 4 KiB (others are left out), plus a Cookie built from the
// cookies field for target. ok is false when the set fails validate.Headers, which
// makes the format unusable. An http_headers that is not an object of at most
// maxHeaderEntries entries counts as absent.
func formatHeaders(rawHeaders, rawCookies json.RawMessage, target string, now time.Time) (http.Header, bool) {
	var list []*downexv1.Header
	have := make(map[string]bool)
	xHeaders := 0
	entries, _ := objectEntries(rawHeaders, maxHeaderEntries)
	for _, e := range entries {
		v, ok := jsonString(e.value)
		if !ok || len(v) > maxHeaderValueBytes || !printableASCII(v) || !isToken(e.key) {
			continue
		}
		key := textproto.CanonicalMIMEHeaderKey(e.key)
		switch {
		case have[key]:
			continue
		case slices.Contains(allowedHeaders, key):
		case xHeaderRE.MatchString(key) && xHeaders < maxXHeaders:
			xHeaders++
		default:
			continue
		}
		have[key] = true
		list = append(list, &downexv1.Header{Name: key, Value: v})
	}
	if s, ok := jsonString(rawCookies); ok && len(s) <= maxCookiesFieldBytes {
		if c := cookieHeader(s, target, now); c != "" {
			list = append(list, &downexv1.Header{Name: "Cookie", Value: c})
		}
	}
	h, err := validate.Headers(list)
	return h, err == nil
}

// cookie is one cookie of a format's cookies field with its attributes.
type cookie struct {
	name, value  string
	domain, path string
	secure       bool
	expires      int64 // Unix seconds; 0 = a session cookie
}

// parseCookies reads yt-dlp's cookies field: "name=value" pairs, each followed by its
// attributes (Domain, Path, Secure, Expires in Unix seconds, ...), all separated by
// "; ". A pair with a name that is not a token, or a value that is not printable
// ASCII, is left out together with its attributes.
func parseCookies(s string) []cookie {
	var out []cookie
	var cur *cookie
	for item := range strings.SplitSeq(s, ";") {
		item = strings.TrimSpace(item)
		name, value, hasValue := strings.Cut(item, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		switch strings.ToLower(name) {
		case "domain":
			if cur != nil {
				cur.domain = strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(value), "."), ".")
			}
			continue
		case "path":
			if cur != nil {
				cur.path = value
			}
			continue
		case "secure":
			if cur != nil {
				cur.secure = true
			}
			continue
		case "expires":
			if cur != nil {
				if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
					cur.expires = n
				}
			}
			continue
		case "version", "max-age", "httponly", "samesite", "comment", "commenturl", "discard", "port", "":
			continue
		}
		if !hasValue || !isToken(name) || !printableASCII(value) {
			cur = nil
			continue
		}
		out = append(out, cookie{name: name, value: value})
		cur = &out[len(out)-1]
	}
	return out
}

// cookieHeader is the Cookie header value for a request to target: the cookies of s
// whose Domain domain-matches the URL's host and whose Path path-matches its path
// (RFC 6265 §5.1.3, §5.1.4), Secure ones only over https, none expired at now; at
// most maxCookies cookies of at most maxCookieBytes in total. A cookie without Domain
// is not sent: nothing says which host it belongs to.
func cookieHeader(s, target string, now time.Time) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	reqPath := u.EscapedPath()
	if reqPath == "" {
		reqPath = "/"
	}
	_, ipErr := netip.ParseAddr(host)
	var b strings.Builder
	n := 0
	for _, c := range parseCookies(s) {
		switch {
		case c.domain == "",
			host != c.domain && (ipErr == nil || !strings.HasSuffix(host, "."+c.domain)),
			!pathMatch(reqPath, c.path),
			c.secure && u.Scheme != "https",
			c.expires != 0 && c.expires <= now.Unix():
			continue
		}
		pair := c.name + "=" + c.value
		sep := 0
		if n > 0 {
			sep = 2
		}
		if n == maxCookies || b.Len()+sep+len(pair) > maxCookieBytes {
			break
		}
		if n > 0 {
			b.WriteString("; ")
		}
		b.WriteString(pair)
		n++
	}
	return b.String()
}

// pathMatch reports whether the request path reqPath path-matches the cookie path
// cookiePath (RFC 6265 §5.1.4); a cookie without Path belongs to "/".
func pathMatch(reqPath, cookiePath string) bool {
	if cookiePath == "" || cookiePath[0] != '/' {
		cookiePath = "/"
	}
	switch {
	case reqPath == cookiePath:
		return true
	case !strings.HasPrefix(reqPath, cookiePath):
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || reqPath[len(cookiePath)] == '/'
}

// entry is one member of a JSON object, in input order.
type entry struct {
	key   string
	value json.RawMessage
}

// objectEntries returns the members of a JSON object in order; ok is false when raw
// is not an object or has more than max members.
func objectEntries(raw json.RawMessage, max int) (out []entry, ok bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	for dec.More() {
		tok, err := dec.Token()
		key, isKey := tok.(string)
		if err != nil || !isKey || len(out) == max {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		out = append(out, entry{key, v})
	}
	return out, true
}

// eachElement calls fn with each element of a JSON array; a value that is not an
// array counts as an empty one. More than max elements are media.ErrTooLarge,
// counted before any is kept.
func eachElement(raw json.RawMessage, max int, fn func(json.RawMessage)) error {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("%w: yt-dlp formats are not valid JSON", media.ErrMalformed)
	}
	for n := 0; dec.More(); n++ {
		if n == max {
			return fmt.Errorf("%w: yt-dlp lists more than %d formats", media.ErrTooLarge, max)
		}
		var el json.RawMessage
		if err := dec.Decode(&el); err != nil {
			return fmt.Errorf("%w: yt-dlp formats are not valid JSON", media.ErrMalformed)
		}
		fn(el)
	}
	return nil
}

// finiteJSON replaces the non-finite numbers that Python's JSON encoder writes (NaN,
// Infinity, -Infinity) outside strings with null, so that such a field reads as
// absent instead of failing the whole output. It returns data itself when there are
// none.
func finiteJSON(data []byte) []byte {
	var out []byte
	last := 0
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		var n int
		switch {
		case c == '"':
			inString = true
			continue
		case c == 'N' && bytes.HasPrefix(data[i:], []byte("NaN")):
			n = len("NaN")
		case c == 'I' && bytes.HasPrefix(data[i:], []byte("Infinity")):
			n = len("Infinity")
		case c == '-' && bytes.HasPrefix(data[i:], []byte("-Infinity")):
			n = len("-Infinity")
		default:
			continue
		}
		if out == nil {
			out = make([]byte, 0, len(data)+len(data)/8)
		}
		out = append(out, data[last:i]...)
		out = append(out, "null"...)
		i += n - 1
		last = i + 1
	}
	if out == nil {
		return data
	}
	return append(out, data[last:]...)
}

// jsonString decodes a JSON string; ok is false for any other value.
func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// jsonBool decodes a JSON boolean; ok is false for any other value.
func jsonBool(raw json.RawMessage) (v, ok bool) {
	switch string(raw) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// floatIn decodes a finite JSON number within lo..hi.
func floatIn(raw json.RawMessage, lo, hi float64) (float64, bool) {
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	v, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

// intIn decodes a JSON number with an integral value within lo..hi.
func intIn(raw json.RawMessage, lo, hi int64) (int64, bool) {
	v, ok := floatIn(raw, float64(lo), float64(hi))
	if !ok || v != math.Trunc(v) {
		return 0, false
	}
	return int64(v), true
}

// present reports a field that is set: anything but absent, null, false, "", [] and
// {}.
func present(raw json.RawMessage) bool {
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, string(raw))
	switch s {
	case "", "null", "false", `""`, "[]", "{}":
		return false
	}
	return true
}

// printable reports valid UTF-8 of printable characters (unicode.IsPrint: no control,
// format or bidi characters); space is allowed only with space.
func printable(s string, space bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || (r == ' ' && !space) {
			return false
		}
	}
	return true
}

// printableASCII reports bytes 0x20..0x7E only.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// isToken reports a non-empty RFC 9110 token (a header or cookie name).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
