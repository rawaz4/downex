package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"strings"
	"time"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// protocolVersion is the native-messaging protocol this relay speaks.
const protocolVersion = 1

// Bounds of fields that are not forwarded to the daemon.
const (
	maxIDBytes         = 256 // request id, echoed in every response
	maxExtVersionBytes = 64
	maxMessageBytes    = 1024 // error messages sent to the browser
)

// Bounds of what the relay sends back for probes of pages and streams
// (docs/SITE-MEDIA-SPEC.md §3.5, docs/VIDEO-DETECT-SPEC.md §3.5).
const (
	maxVideoTracks        = 200       // tracks of a videoInfo
	maxVideoResponseBytes = 256 << 10 // an encoded videoInfo
	maxReasonBytes        = 64        // the daemon's error code, forwarded as reason
)

// Error codes of responses (docs/BROWSER-SPEC.md §2).
const (
	codeInvalidRequest    = "invalid_request"
	codeDaemonUnavailable = "daemon_unavailable"
	codeRejected          = "rejected"
	codeInternal          = "internal"
)

// Request types.
const (
	typeHello       = "hello"
	typeAdd         = "add"
	typeAddMedia    = "addMedia"
	typeProbeVideo  = "probeVideo" // docs/SITE-MEDIA-SPEC.md §3.5
	typeAddVideo    = "addVideo"
	typeProbeStream = "probeStream" // docs/VIDEO-DETECT-SPEC.md §3.5
	typeStatus      = "status"
	typePing        = "ping"
)

// features names what this relay serves beyond the first protocol-1 requests (welcome).
// The extension sends probeVideo and addVideo only to a relay that lists "video", and a
// look-ahead probeVideo (prefetch) only to one that lists "prefetch": an older relay would
// drop the mark, and the daemon would take the look-ahead for a user's probe. It sends
// probeStream, and addMedia with tracks, only to one that lists "streams": an older relay
// refuses the type and ignores the tracks, so the daemon would save its default pick.
var features = []string{"video", "prefetch", "streams"}

// request is a parsed, validated browser message.
type request struct {
	id    string
	typ   string
	hello helloRequest                // typeHello
	add   *downexv1.AddRequest        // typeAdd
	media *downexv1.AddMediaRequest   // typeAddMedia, typeAddVideo (origin SITE)
	probe *downexv1.ProbeMediaRequest // typeProbeVideo (origin SITE), typeProbeStream (origin MANIFEST)
	info  addInfo                     // adds, probes and video requests: logged, not forwarded
	// received is when the relay read the message; an add's deadline counts from it.
	received time.Time
}

// video reports the requests served on the video pool, under their own semaphore
// (maxVideoInFlight) and bound (videoTimeout): the page requests and the stream probes.
// addMedia stays with the adds.
func (r *request) video() bool {
	return r.typ == typeProbeVideo || r.typ == typeAddVideo || r.typ == typeProbeStream
}

type helloRequest struct {
	browser    string
	extVersion string
}

// addInfo is what the log records about an add, a probe or a video request: never header
// values or the body.
type addInfo struct {
	url     string // add: the URL; addMedia, probeStream: the manifest; probeVideo, addVideo: the page
	method  string
	headers int
	body    int
	mime    string
	size    int64 // -1 when not given
	// deadline is how long the extension waits for the answer (deadlineMs, capped at
	// addTimeout, or at videoTimeout for the video pool's requests); 0 when not given.
	deadline time.Duration
	title    int // addMedia, addVideo: characters of the title hint
}

// requestError is a message that cannot be served. id is "" when the message carries
// no usable id: nothing can be answered then, and the relay stops.
type requestError struct {
	id  string
	msg string
}

func (e *requestError) Error() string { return e.msg }

func invalid(id, format string, args ...any) *requestError {
	return &requestError{id: id, msg: fmt.Sprintf(format, args...)}
}

// parseRequest parses and validates one message.
func parseRequest(msg []byte) (*request, *requestError) {
	if !utf8.Valid(msg) {
		// encoding/json replaces invalid UTF-8, so the id may still be readable.
		var env struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(msg, &env) == nil && validID(env.ID) {
			return nil, invalid(env.ID, "message is not valid UTF-8")
		}
		return nil, invalid("", "message is not valid UTF-8 and has no readable id")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(msg, &fields); err != nil {
		return nil, invalid("", "message is not a JSON object: %v", err)
	}
	var id string
	if raw, ok := fields["id"]; !ok || json.Unmarshal(raw, &id) != nil || !validID(id) {
		return nil, invalid("", "message has no id: a non-empty string of at most %d bytes is required", maxIDBytes)
	}
	r := &request{id: id}
	if raw, ok := fields["type"]; !ok || json.Unmarshal(raw, &r.typ) != nil {
		return nil, invalid(id, "type must be a string")
	}
	var err error
	switch r.typ {
	case typeHello:
		r.hello, err = parseHello(msg)
	case typeAdd:
		r.add, r.info, err = parseAdd(msg)
	case typeAddMedia:
		r.media, r.info, err = parseAddMedia(msg)
	case typeProbeVideo:
		r.probe, r.info, err = parseProbeVideo(msg)
	case typeAddVideo:
		r.media, r.info, err = parseAddVideo(msg)
	case typeProbeStream:
		r.probe, r.info, err = parseProbeStream(msg)
	case typeStatus, typePing:
	default:
		return nil, invalid(id, "unknown request type %q", truncate(r.typ, 32))
	}
	if err != nil {
		return nil, invalid(id, "%s: %v", r.typ, err)
	}
	return r, nil
}

func validID(id string) bool { return id != "" && len(id) <= maxIDBytes }

// decodeFields decodes msg into v (a struct of pointer fields), naming the field of a
// type mismatch.
func decodeFields(msg []byte, v any) error {
	err := json.Unmarshal(msg, v)
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		if te.Field != "" {
			return fmt.Errorf("field %s has the wrong type (%s)", te.Field, te.Value)
		}
		return fmt.Errorf("a field has the wrong type (%s)", te.Value)
	}
	return err
}

func parseHello(msg []byte) (helloRequest, error) {
	var f struct {
		Protocol   *int64  `json:"protocol"`
		Browser    *string `json:"browser"`
		ExtVersion *string `json:"extVersion"`
	}
	var h helloRequest
	if err := decodeFields(msg, &f); err != nil {
		return h, err
	}
	switch {
	case f.Protocol == nil:
		return h, errors.New("protocol is required")
	case *f.Protocol != protocolVersion:
		return h, fmt.Errorf("protocol %d is not supported (this relay speaks %d)", *f.Protocol, protocolVersion)
	case f.Browser == nil:
		return h, errors.New("browser is required")
	}
	switch *f.Browser {
	case "chrome", "edge", "firefox", "other":
		h.browser = *f.Browser
	default:
		return h, errors.New(`browser must be "chrome", "edge", "firefox" or "other"`)
	}
	if f.ExtVersion != nil {
		if len(*f.ExtVersion) > maxExtVersionBytes || !printable(*f.ExtVersion) {
			return h, fmt.Errorf("extVersion must be printable text of at most %d bytes", maxExtVersionBytes)
		}
		h.extVersion = *f.ExtVersion
	}
	return h, nil
}

// parseAdd validates an add request with the daemon's limits (docs/DAEMON-SPEC.md §4)
// and converts it to the daemon's AddRequest. Messages never repeat header values.
func parseAdd(msg []byte) (*downexv1.AddRequest, addInfo, error) {
	var f struct {
		URL         *string    `json:"url"`
		Method      *string    `json:"method"`
		Headers     [][]string `json:"headers"`
		Body        *string    `json:"body"`
		Filename    *string    `json:"filename"`
		Referrer    *string    `json:"referrer"`
		MIME        *string    `json:"mime"`
		Size        *int64     `json:"size"`
		StartPaused *bool      `json:"startPaused"`
		DeadlineMs  *int64     `json:"deadlineMs"`
	}
	info := addInfo{size: -1}
	if err := decodeFields(msg, &f); err != nil {
		return nil, info, err
	}
	if f.URL == nil {
		return nil, info, errors.New("url is required")
	}
	if err := validate.URL(*f.URL); err != nil {
		return nil, info, err
	}
	// Browser hand-offs may need the user's confirmation (docs/DESKTOP-SPEC.md §1).
	req := &downexv1.AddRequest{Url: *f.URL, Source: downexv1.AddSource_ADD_SOURCE_BROWSER}
	info.url = *f.URL

	method, err := validate.Method(deref(f.Method))
	if err != nil {
		return nil, info, err
	}
	req.Method, info.method = method, method

	if req.Headers, err = headerPairs(f.Headers); err != nil {
		return nil, info, err
	}
	hasReferer := false
	for _, h := range req.Headers {
		hasReferer = hasReferer || textproto.CanonicalMIMEHeaderKey(h.GetName()) == "Referer"
	}
	if r := deref(f.Referrer); r != "" {
		if err := validate.URL(r); err != nil {
			return nil, info, fmt.Errorf("referrer: %w", err)
		}
		if !hasReferer {
			req.Headers = append(req.Headers, &downexv1.Header{Name: "Referer", Value: r})
		}
	}
	if _, err := validate.Headers(req.Headers); err != nil {
		return nil, info, err
	}
	info.headers = len(req.Headers)

	if f.Body != nil && *f.Body != "" {
		b64 := *f.Body
		if len(b64) > base64.StdEncoding.EncodedLen(validate.MaxBodyBytes) {
			return nil, info, fmt.Errorf("body is larger than %d bytes", validate.MaxBodyBytes)
		}
		body, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, info, errors.New("body is not valid base64")
		}
		req.Body = body
	}
	if err := validate.Body(len(req.Body), method); err != nil {
		return nil, info, err
	}
	info.body = len(req.Body)

	if f.Filename != nil {
		if _, err := validate.Filename(*f.Filename); err != nil {
			return nil, info, err
		}
		req.Filename = *f.Filename // the daemon sanitises it
	}
	if f.MIME != nil {
		// The daemon uses the hint to choose a category (docs/DESKTOP-SPEC.md §2).
		mime, err := validate.MIME(*f.MIME)
		if err != nil {
			return nil, info, err
		}
		req.Mime, info.mime = mime, mime
	}
	if f.Size != nil {
		if *f.Size < -1 {
			return nil, info, errors.New("size must not be negative")
		}
		// Shown in the confirmation dialog; -1 (unknown) is sent as 0.
		if req.ExpectedSize, err = validate.ExpectedSize(*f.Size); err != nil {
			return nil, info, err
		}
		info.size = *f.Size
	}
	req.StartPaused = f.StartPaused != nil && *f.StartPaused
	if info.deadline, err = deadline(f.DeadlineMs, addTimeout); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// streamFields are the fields addMedia and probeStream share (docs/BROWSER-SPEC.md §2,
// docs/VIDEO-DETECT-SPEC.md §3.5).
type streamFields struct {
	ManifestURL *string    `json:"manifestUrl"`
	Headers     [][]string `json:"headers"`
	PageURL     *string    `json:"pageUrl"`
	DeadlineMs  *int64     `json:"deadlineMs"`
}

// stream is a validated manifest, the headers the page sent for it and the page ("" when
// not given).
type stream struct {
	manifest string
	headers  []*downexv1.Header
	page     string
}

// parseStream validates the fields both stream requests carry with the daemon's limits:
// the manifest (required), the headers, the page and the deadline, capped at limit.
func parseStream(f streamFields, limit time.Duration) (stream, addInfo, error) {
	var s stream
	info := addInfo{size: -1}
	if f.ManifestURL == nil {
		return s, info, errors.New("manifestUrl is required")
	}
	if err := validate.URL(*f.ManifestURL); err != nil {
		return s, info, fmt.Errorf("manifestUrl: %w", err)
	}
	s.manifest = *f.ManifestURL
	info.url, info.method = s.manifest, "GET"
	var err error
	if s.headers, err = validHeaders(f.Headers); err != nil {
		return s, info, err
	}
	info.headers = len(s.headers)
	if p := deref(f.PageURL); p != "" {
		if err := validate.URL(p); err != nil {
			return s, info, fmt.Errorf("pageUrl: %w", err)
		}
		s.page = p
	}
	if info.deadline, err = deadline(f.DeadlineMs, limit); err != nil {
		return s, info, err
	}
	return s, info, nil
}

// parseAddMedia validates an addMedia request (docs/BROWSER-SPEC.md §2) with the
// daemon's limits and converts it to the daemon's AddMediaRequest (source BROWSER).
// The optional tracks are ids of a probeStream answer; missing or empty ask for the
// daemon's default pick. Messages never repeat header values.
func parseAddMedia(msg []byte) (*downexv1.AddMediaRequest, addInfo, error) {
	var f struct {
		streamFields
		Title      *string `json:"title"`
		VideoTrack *string `json:"videoTrack"`
		AudioTrack *string `json:"audioTrack"`
	}
	if err := decodeFields(msg, &f); err != nil {
		return nil, addInfo{size: -1}, err
	}
	s, info, err := parseStream(f.streamFields, addTimeout)
	if err != nil {
		return nil, info, err
	}
	req := &downexv1.AddMediaRequest{
		ManifestUrl: s.manifest, Headers: s.headers, PageUrl: s.page, Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
	}
	if req.VideoTrack, req.AudioTrack, err = tracks(f.VideoTrack, f.AudioTrack); err != nil {
		return nil, info, err
	}
	if req.Title, info.title, err = title(f.Title); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// parseProbeStream converts a probeStream request (no side effects) to the daemon's
// ProbeMediaRequest with origin MANIFEST. It is always a user's click: never a look-ahead,
// so it starts downexd when none runs. Fields of other requests (prefetch, tracks, title)
// are ignored like any unknown field.
func parseProbeStream(msg []byte) (*downexv1.ProbeMediaRequest, addInfo, error) {
	var f streamFields
	if err := decodeFields(msg, &f); err != nil {
		return nil, addInfo{size: -1}, err
	}
	s, info, err := parseStream(f, videoTimeout)
	if err != nil {
		return nil, info, err
	}
	return &downexv1.ProbeMediaRequest{
		ManifestUrl: s.manifest, Headers: s.headers, PageUrl: s.page, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST,
	}, info, nil
}

// videoFields are the fields of probeVideo and addVideo (docs/SITE-MEDIA-SPEC.md §3.5).
type videoFields struct {
	PageURL    *string    `json:"pageUrl"`
	Headers    [][]string `json:"headers"`
	VideoTrack *string    `json:"videoTrack"`
	AudioTrack *string    `json:"audioTrack"`
	Title      *string    `json:"title"`
	DeadlineMs *int64     `json:"deadlineMs"`
}

// parseVideo validates what both video requests carry: the page (required), the
// headers and the deadline. Of the headers only Cookie (the user's opt-in) and Referer
// (the embedding page) mean something to the daemon, which ignores the others.
func parseVideo(msg []byte) (videoFields, []*downexv1.Header, addInfo, error) {
	var f videoFields
	info := addInfo{size: -1, method: "GET"}
	if err := decodeFields(msg, &f); err != nil {
		return f, nil, info, err
	}
	if f.PageURL == nil {
		return f, nil, info, errors.New("pageUrl is required")
	}
	if err := validate.URL(*f.PageURL); err != nil {
		return f, nil, info, fmt.Errorf("pageUrl: %w", err)
	}
	info.url = *f.PageURL
	headers, err := validHeaders(f.Headers)
	if err != nil {
		return f, nil, info, err
	}
	info.headers = len(headers)
	if info.deadline, err = deadline(f.DeadlineMs, videoTimeout); err != nil {
		return f, nil, info, err
	}
	return f, headers, info, nil
}

// parseProbeVideo converts a probeVideo request (no side effects) to the daemon's
// ProbeMediaRequest with origin SITE; track and title fields are ignored. The optional
// boolean prefetch marks the extension's look-ahead while the user watches the video
// (docs/BROWSER-SPEC.md §4): the daemon never installs tools for it and never lets it wait
// behind or delay a user's probe.
func parseProbeVideo(msg []byte) (*downexv1.ProbeMediaRequest, addInfo, error) {
	f, headers, info, err := parseVideo(msg)
	if err != nil {
		return nil, info, err
	}
	var p struct {
		Prefetch *bool `json:"prefetch"`
	}
	if err := decodeFields(msg, &p); err != nil {
		return nil, info, err
	}
	return &downexv1.ProbeMediaRequest{
		PageUrl: *f.PageURL, Headers: headers, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE,
		Prefetch: p.Prefetch != nil && *p.Prefetch,
	}, info, nil
}

// parseAddVideo converts an addVideo request to the daemon's AddMediaRequest with
// origin SITE and source BROWSER; empty tracks ask for the daemon's default pick.
func parseAddVideo(msg []byte) (*downexv1.AddMediaRequest, addInfo, error) {
	f, headers, info, err := parseVideo(msg)
	if err != nil {
		return nil, info, err
	}
	req := &downexv1.AddMediaRequest{
		PageUrl: *f.PageURL, Headers: headers, Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
		Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE,
	}
	if req.VideoTrack, req.AudioTrack, err = tracks(f.VideoTrack, f.AudioTrack); err != nil {
		return nil, info, err
	}
	if req.Title, info.title, err = title(f.Title); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// tracks checks the optional track ids of addMedia and addVideo; missing or "" is the
// daemon's default pick.
func tracks(video, audio *string) (string, string, error) {
	if err := validate.TrackID(deref(video)); err != nil {
		return "", "", fmt.Errorf("videoTrack: %w", err)
	}
	if err := validate.TrackID(deref(audio)); err != nil {
		return "", "", fmt.Errorf("audioTrack: %w", err)
	}
	return deref(video), deref(audio), nil
}

// headerPairs converts [name, value] pairs, bounding their number; the caller checks
// them with validate.Headers. Messages never repeat header values.
func headerPairs(kv [][]string) ([]*downexv1.Header, error) {
	if len(kv) > validate.MaxHeaderCount {
		return nil, fmt.Errorf("at most %d headers are allowed", validate.MaxHeaderCount)
	}
	var hs []*downexv1.Header
	for i, p := range kv {
		if len(p) != 2 {
			return nil, fmt.Errorf("header %d must be a [name, value] pair", i+1)
		}
		hs = append(hs, &downexv1.Header{Name: p[0], Value: p[1]})
	}
	return hs, nil
}

// validHeaders converts header pairs and checks them with the daemon's limits.
func validHeaders(kv [][]string) ([]*downexv1.Header, error) {
	hs, err := headerPairs(kv)
	if err != nil {
		return nil, err
	}
	if _, err := validate.Headers(hs); err != nil {
		return nil, err
	}
	return hs, nil
}

// title checks an optional title hint and returns it cleaned, with its length in
// characters for the log.
func title(raw *string) (string, int, error) {
	if raw == nil {
		return "", 0, nil
	}
	t, err := validate.Title(*raw)
	if err != nil {
		return "", 0, err
	}
	return t, utf8.RuneCountInString(t), nil
}

// deadline reads an optional deadlineMs, capped at limit; 0 when not given.
func deadline(ms *int64, limit time.Duration) (time.Duration, error) {
	if ms == nil {
		return 0, nil
	}
	if *ms <= 0 {
		return 0, errors.New("deadlineMs must be a positive number of milliseconds")
	}
	return time.Duration(min(*ms, limit.Milliseconds())) * time.Millisecond, nil
}

// printable reports text without control characters.
func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- responses ------------------------------------------------------------------

// response is one host → browser message.
type response map[string]any

func success(id, typ string, fields map[string]any) response {
	r := response{"id": id, "ok": true, "type": typ}
	for k, v := range fields {
		r[k] = v
	}
	return r
}

func failure(id, code, msg string) response {
	return response{"id": id, "ok": false, "error": map[string]string{"code": code, "message": clip(msg)}}
}

// withReason adds the daemon's stable error code (its Downex-Error-Code metadata) to a
// failure as "reason"; anything but [a-z_]{1,64} is left out.
func (r response) withReason(reason string) response {
	if e, ok := r["error"].(map[string]string); ok && validReason(reason) {
		e["reason"] = reason
	}
	return r
}

func validReason(s string) bool {
	return s != "" && len(s) <= maxReasonBytes &&
		!strings.ContainsFunc(s, func(c rune) bool { return (c < 'a' || c > 'z') && c != '_' })
}

// added is the answer to add, addMedia and addVideo: the download, and whether the
// daemon holds it for the user's confirmation (docs/BROWSER-SPEC.md §2).
func added(id string, d *downexv1.Download) response {
	return success(id, "added", map[string]any{
		"downloadId": d.GetId(),
		"awaiting":   d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION,
	})
}

// videoInfo is the answer to a probeVideo (docs/SITE-MEDIA-SPEC.md §3.5) and to a
// probeStream (docs/VIDEO-DETECT-SPEC.md §3.5): the offered tracks and the daemon's
// pick. It never carries a URL: not the manifest or page the daemon's MediaInfo echoes,
// and no format URLs or yt-dlp headers, which MediaInfo does not have.
func videoInfo(m *downexv1.MediaInfo) (map[string]any, error) {
	if n := len(m.GetTracks()); n > maxVideoTracks {
		return nil, fmt.Errorf("downexd offered %d tracks, more than %d", n, maxVideoTracks)
	}
	tracks := make([]map[string]any, 0, len(m.GetTracks()))
	for _, t := range m.GetTracks() {
		tracks = append(tracks, map[string]any{
			"id": t.GetId(), "kind": t.GetKind(), "ext": t.GetExt(), "codecs": t.GetCodecs(),
			"width": t.GetWidth(), "height": t.GetHeight(), "frameRate": t.GetFrameRate(),
			"bandwidth": t.GetBandwidth(), "size": t.GetSize(), "sizeApprox": t.GetSizeApprox(),
			"language": t.GetLanguage(), "name": t.GetName(),
		})
	}
	return map[string]any{
		"site": m.GetSite(), "title": m.GetTitle(), "durationMs": m.GetDurationMs(), "format": m.GetFormat(),
		"container": m.GetContainer(), "selectedVideo": m.GetSelectedVideo(), "selectedAudio": m.GetSelectedAudio(),
		"tracks": tracks,
	}, nil
}

// fits reports whether r encodes to at most limit bytes.
func (r response) fits(limit int) bool {
	b, err := json.Marshal(r)
	return err == nil && len(b) <= limit
}

// encode returns the message bytes of r; a response that would exceed the browser's
// limit becomes an internal error.
func (r response) encode() []byte {
	b, err := json.Marshal(r)
	if err == nil && len(b) <= maxResponseBytes {
		return b
	}
	id, _ := r["id"].(string)
	b, _ = json.Marshal(failure(id, codeInternal, "the response could not be encoded"))
	return b
}

// clip bounds a message for the browser, keeping it valid UTF-8.
func clip(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxMessageBytes {
		return s
	}
	cut := maxMessageBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// truncate shortens s to at most n bytes (on a rune boundary) for messages and logs.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
