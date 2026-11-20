package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"net/url"
	"path/filepath"
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
	maxVideoItems         = 20        // items of a videoInfo (docs/SITE-MEDIA-SPEC.md §3.12)
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
	typeRescue      = "rescue" // docs/BROWSER-SPEC.md §6
	typeResume      = "resume"
	typeFetchTake   = "fetchTake" // docs/SITE-MEDIA-SPEC.md §2.8
	typeFetchPut    = "fetchPut"
	typeProbePlayed = "probePlayed" // docs/SITE-MEDIA-SPEC.md §2.9, §3.5
	typeAddPlayed   = "addPlayed"
)

// Bounds of browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8; the daemon's
// internal/browserfetch checks them again).
const (
	maxFetchTickets = 64
	maxFetchWaitMs  = 25_000
	defFetchWaitMs  = 20_000
	maxFetchPiece   = 256 << 10
	maxFetchError   = 256
	maxFetchIDBytes = 32
)

// Bounds of rescue (docs/BROWSER-SPEC.md §2, docs/DAEMON-SPEC.md §4).
const (
	maxRescuePathBytes      = 32 << 10
	maxRescueValidatorBytes = 1024
	maxJSONInteger          = 1<<53 - 1 // the largest integer a JavaScript number holds exactly
)

// features names what this relay serves beyond the first protocol-1 requests (welcome).
// The extension sends probeVideo and addVideo only to a relay that lists "video", and a
// look-ahead probeVideo (prefetch) only to one that lists "prefetch": an older relay would
// drop the mark, and the daemon would take the look-ahead for a user's probe. It sends
// probeStream, and addMedia with tracks, only to one that lists "streams": an older relay
// refuses the type and ignores the tracks, so the daemon would save its default pick.
// "renew": add takes pageUrl and added says renewCheck (docs/BROWSER-SPEC.md §2); both
// are additive, an older relay ignores the page. "rescue": the rescue and resume requests
// (docs/BROWSER-SPEC.md §6), which an older relay refuses as unknown. "clips": addMedia
// and addVideo take clipStartMs/clipEndMs and videoInfo answers carry chapters
// (docs/MEDIA-SPEC.md §8); an older relay would drop the part and save the whole video,
// so the extension sends one only to a relay that lists it.
// "items": probeVideo and addVideo take entryId, one video of a page with several, and
// videoInfo answers carry entryId, items and itemsLeftOut (docs/SITE-MEDIA-SPEC.md §3.12);
// an older relay would drop entryId and the daemon would take the page's default item.
// "signIn": probeVideo and addVideo take cookieDomain, the domain the Cookie header's
// sign-in is for (docs/SITE-MEDIA-SPEC.md §6.3); an older relay would drop it and the
// daemon would write the cookies for the page's exact host only.
// "browserFetch": addVideo takes browserFetch and added carries fetchTicket, videoInfo
// says browserFetch, and fetchTake/fetchPut move a download's requests to the browser
// and its bytes back (docs/SITE-MEDIA-SPEC.md §2.8); an older relay refuses the types.
// "played": probePlayed and addPlayed save the video the browser played
// (docs/SITE-MEDIA-SPEC.md §2.9); an older relay refuses the types.
var features = []string{"video", "prefetch", "streams", "renew", "rescue", "clips", "items", "signIn", "browserFetch", "played"}

// request is a parsed, validated browser message.
type request struct {
	id    string
	typ   string
	hello helloRequest                // typeHello
	add   *downexv1.AddRequest        // typeAdd
	media *downexv1.AddMediaRequest   // typeAddMedia, typeAddVideo (origin SITE), typeAddPlayed (origin PLAYED)
	probe *downexv1.ProbeMediaRequest // typeProbeVideo (origin SITE), typeProbeStream (origin MANIFEST), typeProbePlayed (origin PLAYED)
	// rescue is typeRescue's request, resume typeResume's download id.
	rescue *downexv1.RescueRequest
	resume int64
	// take and put are typeFetchTake's and typeFetchPut's requests.
	take *downexv1.TakeBrowserFetchRequest
	put  *downexv1.PutBrowserFetchRequest
	info addInfo // adds, probes and video requests: logged, not forwarded
	// received is when the relay read the message; an add's deadline counts from it.
	received time.Time
}

// video reports the requests served on the video pool, under their own semaphore
// (maxVideoInFlight) and bound (videoTimeout): the page requests and the stream probes.
// addMedia stays with the adds.
func (r *request) video() bool {
	return r.typ == typeProbeVideo || r.typ == typeAddVideo || r.typ == typeProbeStream || r.typ == typeProbePlayed ||
		r.typ == typeAddPlayed
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
	title    int    // addMedia, addVideo: characters of the title hint
	pageHost string // add: the host of pageUrl ("" without one); never more is logged
	// rescue: the base name of the target only, and the browser's count.
	target   string
	received int64
	// probePlayed, addPlayed: the counts of files, of their hosts and of head bytes.
	files, hosts, headBytes int
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
	case typeRescue:
		r.rescue, r.info, err = parseRescue(msg)
	case typeResume:
		r.resume, err = parseResume(msg)
	case typeFetchTake:
		r.take, err = parseFetchTake(msg)
	case typeFetchPut:
		r.put, err = parseFetchPut(msg)
	case typeProbePlayed:
		r.probe, r.info, err = parseProbePlayed(msg)
	case typeAddPlayed:
		r.media, r.info, err = parseAddPlayed(msg)
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
		PageURL     *string    `json:"pageUrl"`
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
	if p := deref(f.PageURL); p != "" {
		// The page the link was found on: "Open page" of a download whose link expires
		// (docs/DESKTOP-SPEC.md §10). Validated like the referrer.
		u, err := url.Parse(p)
		if verr := validate.URL(p); verr != nil || err != nil || u.User != nil {
			return nil, info, errors.New("pageUrl must be an http or https URL without a user name")
		}
		req.PageUrl, info.pageHost = p, u.Host
	}
	if info.deadline, err = deadline(f.DeadlineMs, addTimeout); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// parseRescue validates a rescue request (docs/BROWSER-SPEC.md §2, §6) with the daemon's
// limits and converts it to the daemon's RescueRequest with source BROWSER: downexd finds
// the browser's partial file next to target (its <target>.crdownload or .part, else a
// provisional "Unconfirmed N.crdownload"). Messages never repeat header values.
func parseRescue(msg []byte) (*downexv1.RescueRequest, addInfo, error) {
	var f struct {
		URL           *string    `json:"url"`
		Headers       [][]string `json:"headers"`
		Target        *string    `json:"target"`
		BytesReceived *int64     `json:"bytesReceived"`
		TotalBytes    *int64     `json:"totalBytes"`
		StartTime     *int64     `json:"startTime"`
		ETag          *string    `json:"etag"`
		LastModified  *string    `json:"lastModified"`
		Filename      *string    `json:"filename"`
		StartPaused   *bool      `json:"startPaused"`
		PageURL       *string    `json:"pageUrl"`
		DeadlineMs    *int64     `json:"deadlineMs"`
	}
	info := addInfo{size: -1, method: "GET"}
	if err := decodeFields(msg, &f); err != nil {
		return nil, info, err
	}
	if f.URL == nil {
		return nil, info, errors.New("url is required")
	}
	if err := validate.URL(*f.URL); err != nil {
		return nil, info, err
	}
	info.url = *f.URL
	req := &downexv1.RescueRequest{Url: *f.URL, Source: downexv1.AddSource_ADD_SOURCE_BROWSER}
	var err error
	if req.Headers, err = validHeaders(f.Headers); err != nil {
		return nil, info, err
	}
	info.headers = len(req.Headers)
	switch t := deref(f.Target); {
	case t == "":
		return nil, info, errors.New("target is required")
	case len(t) > maxRescuePathBytes:
		return nil, info, fmt.Errorf("target is longer than %d bytes", maxRescuePathBytes)
	case strings.ContainsRune(t, 0) || !filepath.IsAbs(t):
		return nil, info, errors.New("target must be an absolute path")
	default:
		req.TargetPath, info.target = t, filepath.Base(t)
	}
	if f.BytesReceived == nil || *f.BytesReceived < 0 || *f.BytesReceived > maxJSONInteger {
		return nil, info, errors.New("bytesReceived must be a number of bytes")
	}
	req.BytesReceived, info.received = *f.BytesReceived, *f.BytesReceived
	if f.TotalBytes != nil {
		if *f.TotalBytes < -1 || *f.TotalBytes > maxJSONInteger {
			return nil, info, errors.New("totalBytes must be -1 (unknown) or a number of bytes")
		}
		req.ExpectedSize, info.size = *f.TotalBytes, *f.TotalBytes
	}
	if f.StartTime != nil {
		if *f.StartTime < 0 || *f.StartTime > maxJSONInteger {
			return nil, info, errors.New("startTime must be a time in milliseconds")
		}
		req.StartedAtMs = *f.StartTime
	}
	for _, v := range []struct {
		name string
		val  *string
		dst  *string
	}{{"etag", f.ETag, &req.Etag}, {"lastModified", f.LastModified, &req.LastModified}} {
		s := deref(v.val)
		if len(s) > maxRescueValidatorBytes || !printable(s) {
			return nil, info, fmt.Errorf("%s must be printable text of at most %d bytes", v.name, maxRescueValidatorBytes)
		}
		*v.dst = s
	}
	if f.Filename != nil {
		if _, err := validate.Filename(*f.Filename); err != nil {
			return nil, info, err
		}
		req.Filename = *f.Filename // the daemon sanitises it
	}
	if f.StartPaused == nil {
		return nil, info, errors.New("startPaused is required")
	}
	req.StartPaused = *f.StartPaused
	if p := deref(f.PageURL); p != "" {
		u, err := url.Parse(p)
		if verr := validate.URL(p); verr != nil || err != nil || u.User != nil {
			return nil, info, errors.New("pageUrl must be an http or https URL without a user name")
		}
		req.PageUrl, info.pageHost = p, u.Host
	}
	if info.deadline, err = deadline(f.DeadlineMs, rescueTimeout); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// parseResume validates a resume request: the first start of a download that a rescue
// stored paused (docs/BROWSER-SPEC.md §6). The daemon refuses any other download.
func parseResume(msg []byte) (int64, error) {
	var f struct {
		DownloadID *int64 `json:"downloadId"`
	}
	if err := decodeFields(msg, &f); err != nil {
		return 0, err
	}
	if f.DownloadID == nil || *f.DownloadID <= 0 || *f.DownloadID > maxJSONInteger {
		return 0, errors.New("downloadId must be a positive integer")
	}
	return *f.DownloadID, nil
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
		clipFields
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
	if req.Clip, err = f.clipFields.parse(); err != nil {
		return nil, info, err
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
	EntryID    *string    `json:"entryId"`
	// CookieDomain is the domain of the Cookie header's sign-in (docs/SITE-MEDIA-SPEC.md
	// §6.3), checked for its syntax here and against the page by the daemon.
	CookieDomain *string `json:"cookieDomain"`
}

// parseVideo validates what both video requests carry: the page (required), the
// headers, the deadline, the item and the sign-in's domain. Of the headers only Cookie
// (the user's sign-in, docs/SITE-MEDIA-SPEC.md §6.3) and Referer (the embedding page)
// mean something to the daemon, which ignores the others.
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
	if err := validate.EntryID(deref(f.EntryID)); err != nil {
		return f, nil, info, fmt.Errorf("entryId: %w", err)
	}
	if err := validate.CookieDomainName(deref(f.CookieDomain)); err != nil {
		return f, nil, info, fmt.Errorf("cookieDomain: %w", err)
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
		Prefetch: p.Prefetch != nil && *p.Prefetch, EntryId: deref(f.EntryID), CookieDomain: deref(f.CookieDomain),
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
		Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, EntryId: deref(f.EntryID), CookieDomain: deref(f.CookieDomain),
	}
	// The browser can fetch the bytes when the site's video servers answer only browsers
	// (docs/SITE-MEDIA-SPEC.md §2.8).
	var bf struct {
		BrowserFetch *bool `json:"browserFetch"`
	}
	if err := decodeFields(msg, &bf); err != nil {
		return nil, info, err
	}
	req.BrowserFetch = bf.BrowserFetch != nil && *bf.BrowserFetch
	var c clipFields
	if err := decodeFields(msg, &c); err != nil {
		return nil, info, err
	}
	if req.Clip, err = c.parse(); err != nil {
		return nil, info, err
	}
	if req.VideoTrack, req.AudioTrack, err = tracks(f.VideoTrack, f.AudioTrack); err != nil {
		return nil, info, err
	}
	if req.Title, info.title, err = title(f.Title); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// clipFields are the part of a video addMedia and addVideo may ask for
// (docs/MEDIA-SPEC.md §8): integers of milliseconds; only a start means to the end.
type clipFields struct {
	ClipStartMs *int64 `json:"clipStartMs"`
	ClipEndMs   *int64 `json:"clipEndMs"`
}

// parse checks the part with the daemon's bounds (validate.ClipMs); nil: none. An end
// without a start is refused.
func (c clipFields) parse() (*downexv1.MediaClip, error) {
	switch {
	case c.ClipStartMs == nil && c.ClipEndMs == nil:
		return nil, nil
	case c.ClipStartMs == nil:
		return nil, errors.New("clipEndMs needs clipStartMs")
	}
	start, end := *c.ClipStartMs, int64(0)
	if c.ClipEndMs != nil {
		end = *c.ClipEndMs
	}
	if err := validate.ClipMs(start, end); err != nil {
		return nil, fmt.Errorf("clip: %w", err)
	}
	return &downexv1.MediaClip{StartMs: start, EndMs: end}, nil
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

// added is the answer to add, addMedia and addVideo: the download, whether the daemon
// holds it for the user's confirmation, and whether it checks first that its link
// continues a download whose link expired (renewCheck; docs/BROWSER-SPEC.md §2).
func added(id string, d *downexv1.Download) response {
	return success(id, "added", map[string]any{
		"downloadId": d.GetId(),
		"awaiting":   d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION,
		"renewCheck": d.GetRenewCheck(),
	})
}

// addedVideo is the answer to addVideo: added's fields and, when downexd made one, the
// download's fetch ticket (docs/SITE-MEDIA-SPEC.md §2.8).
func addedVideo(id string, d *downexv1.Download, ticket string) response {
	r := added(id, d)
	if ticket != "" {
		r["fetchTicket"] = ticket
	}
	return r
}

// rescued is the answer to rescue: the new download and the bytes it kept from the
// browser's partial file.
func rescued(id string, d *downexv1.Download, kept int64) response {
	return success(id, "rescued", map[string]any{"downloadId": d.GetId(), "keptBytes": kept})
}

// resumed is the answer to resume.
func resumed(id string, d *downexv1.Download) response {
	return success(id, "resumed", map[string]any{"downloadId": d.GetId()})
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
	chapters := make([]map[string]any, 0, len(m.GetChapters()))
	if len(m.GetChapters()) <= validate.MaxChapters {
		for _, c := range m.GetChapters() {
			chapters = append(chapters, map[string]any{"startMs": c.GetStartMs(), "endMs": c.GetEndMs(), "title": c.GetTitle()})
		}
	}
	items := make([]map[string]any, 0, len(m.GetItems()))
	for _, it := range m.GetItems() {
		if len(items) == maxVideoItems {
			break
		}
		items = append(items, map[string]any{"id": it.GetId(), "title": it.GetTitle(), "durationMs": it.GetDurationMs(),
			"thumbnail": it.GetThumbnail()})
	}
	fields := map[string]any{
		"site": m.GetSite(), "title": m.GetTitle(), "durationMs": m.GetDurationMs(), "format": m.GetFormat(),
		"container": m.GetContainer(), "selectedVideo": m.GetSelectedVideo(), "selectedAudio": m.GetSelectedAudio(),
		"tracks": tracks, "chapters": chapters,
	}
	// A video saved from what the browser played (docs/SITE-MEDIA-SPEC.md §2.9).
	if m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED {
		fields["origin"] = "played"
	}
	// A page whose video servers answer only browsers (docs/SITE-MEDIA-SPEC.md §2.8).
	if m.GetBrowserFetch() {
		fields["browserFetch"] = true
	}
	// A page with several videos only (docs/SITE-MEDIA-SPEC.md §3.12).
	if left := max(int(m.GetItemsLeftOut()), 0) + len(m.GetItems()) - len(items); m.GetEntryId() != "" || len(items) > 0 || left > 0 {
		fields["entryId"], fields["items"], fields["itemsLeftOut"] = m.GetEntryId(), items, left
	}
	return fields, nil
}

// withoutThumbnails is a videoInfo's fields with its items' thumbnails left out: what an
// answer drops after the chapters when it would be too large (docs/SITE-MEDIA-SPEC.md
// §3.5, §3.12).
func withoutThumbnails(fields map[string]any) {
	items, _ := fields["items"].([]map[string]any)
	for _, it := range items {
		it["thumbnail"] = ""
	}
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

// validTicket reports a fetch ticket's syntax: 32 lowercase hex characters.
func validTicket(t string) bool {
	return len(t) == 32 && !strings.ContainsFunc(t, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') })
}

// parseFetchTake validates a fetchTake (docs/SITE-MEDIA-SPEC.md §2.8): 1 to 64 tickets and
// a wait of 0 to 25,000 ms (20,000 when not given).
func parseFetchTake(msg []byte) (*downexv1.TakeBrowserFetchRequest, error) {
	var f struct {
		Tickets []string `json:"tickets"`
		WaitMs  *int64   `json:"waitMs"`
	}
	if err := decodeFields(msg, &f); err != nil {
		return nil, err
	}
	if len(f.Tickets) == 0 || len(f.Tickets) > maxFetchTickets {
		return nil, fmt.Errorf("tickets must hold 1 to %d tickets", maxFetchTickets)
	}
	for _, t := range f.Tickets {
		if !validTicket(t) {
			return nil, errors.New("a ticket is 32 lowercase hex characters")
		}
	}
	wait := int64(defFetchWaitMs)
	if f.WaitMs != nil {
		if *f.WaitMs < 0 || *f.WaitMs > maxFetchWaitMs {
			return nil, fmt.Errorf("waitMs must be 0 to %d", maxFetchWaitMs)
		}
		wait = *f.WaitMs
	}
	return &downexv1.TakeBrowserFetchRequest{Tickets: f.Tickets, WaitMs: int32(wait)}, nil
}

// fetchHeadFields is the head of a fetchPut.
type fetchHeadFields struct {
	Status        *int64  `json:"status"`
	ContentType   *string `json:"contentType"`
	ContentRange  *string `json:"contentRange"`
	ContentLength *int64  `json:"contentLength"`
}

// parseFetchPut validates a fetchPut (docs/SITE-MEDIA-SPEC.md §2.8): the ticket, the fetch
// id, seq, an optional head (status 100-599, header values of printable text up to 1 KiB,
// a length of -1 or more), data as base64 of at most 256 KiB, end, and an error of at most
// 256 bytes of printable text.
func parseFetchPut(msg []byte) (*downexv1.PutBrowserFetchRequest, error) {
	var f struct {
		Ticket  *string          `json:"ticket"`
		FetchID *string          `json:"fetchId"`
		Seq     *int64           `json:"seq"`
		Head    *fetchHeadFields `json:"head"`
		Data    *string          `json:"data"`
		End     *bool            `json:"end"`
		Error   *string          `json:"error"`
	}
	if err := decodeFields(msg, &f); err != nil {
		return nil, err
	}
	switch {
	case f.Ticket == nil || !validTicket(*f.Ticket):
		return nil, errors.New("ticket must be 32 lowercase hex characters")
	case f.FetchID == nil || *f.FetchID == "" || len(*f.FetchID) > maxFetchIDBytes || !printable(*f.FetchID):
		return nil, fmt.Errorf("fetchId must be 1 to %d printable bytes", maxFetchIDBytes)
	case f.Seq == nil || *f.Seq < 0 || *f.Seq > maxJSONInteger:
		return nil, errors.New("seq must be an integer of 0 or more")
	case f.Error != nil && (len(*f.Error) > maxFetchError || !printable(*f.Error)):
		return nil, fmt.Errorf("error must be at most %d printable bytes", maxFetchError)
	}
	req := &downexv1.PutBrowserFetchRequest{Ticket: *f.Ticket, FetchId: *f.FetchID, Seq: *f.Seq, End: f.End != nil && *f.End,
		Error: deref(f.Error)}
	if h := f.Head; h != nil {
		if h.Status == nil || *h.Status < 100 || *h.Status > 599 {
			return nil, errors.New("head.status must be 100 to 599")
		}
		for _, v := range []*string{h.ContentType, h.ContentRange} {
			if v != nil && (len(*v) > 1024 || !printable(*v)) {
				return nil, errors.New("head values must be at most 1,024 printable bytes")
			}
		}
		length := int64(-1)
		if h.ContentLength != nil {
			if *h.ContentLength < -1 || *h.ContentLength > maxJSONInteger {
				return nil, errors.New("head.contentLength must be -1 or more")
			}
			length = *h.ContentLength
		}
		req.Head = &downexv1.BrowserFetchHead{Status: int32(*h.Status), ContentType: deref(h.ContentType),
			ContentRange: deref(h.ContentRange), ContentLength: length}
	}
	if f.Data != nil {
		if base64.StdEncoding.DecodedLen(len(*f.Data)) > maxFetchPiece+2 {
			return nil, fmt.Errorf("data must be at most %d bytes", maxFetchPiece)
		}
		data, err := base64.StdEncoding.DecodeString(*f.Data)
		if err != nil {
			return nil, errors.New("data must be base64")
		}
		if len(data) > maxFetchPiece {
			return nil, fmt.Errorf("data must be at most %d bytes", maxFetchPiece)
		}
		req.Data = data
	}
	return req, nil
}

// fetchTask is the answer to fetchTake: the request the browser makes now, if any, and the
// tickets downexd no longer knows.
func fetchTask(id string, r *downexv1.TakeBrowserFetchResponse) response {
	fields := map[string]any{"ended": append([]string{}, r.GetEndedTickets()...)}
	if f := r.GetFetch(); f != nil {
		fields["fetch"] = map[string]any{"ticket": f.GetTicket(), "fetchId": f.GetFetchId(), "url": f.GetUrl(),
			"start": f.GetStart(), "end": f.GetEnd(), "total": f.GetTotal()}
	}
	return success(id, "fetchTask", fields)
}

// Bounds of played sources the relay checks before the daemon (docs/SITE-MEDIA-SPEC.md
// §2.9 "Limits"; internal/played checks them again).
const (
	maxPlayedFiles     = 16
	maxPlayedURLBytes  = 8 << 10
	maxPlayedHeadBytes = 256 << 10
	maxPlayedHeads     = 1 << 20
	maxPlayedSize      = 1 << 40
	maxPlayedMs        = 24 * 3600 * 1000
	maxPlayedOthers    = 8
)

// playedFile is one file of a played source as the extension sends it.
type playedFile struct {
	ID      *string `json:"id"`
	URL     *string `json:"url"`
	Size    *int64  `json:"size"`
	Kind    *string `json:"kind"`
	Head    *string `json:"head"`
	Browser *bool   `json:"browser"`
}

// playedFields are the fields of probePlayed and addPlayed.
type playedFields struct {
	PageURL    *string `json:"pageUrl"`
	DeadlineMs *int64  `json:"deadlineMs"`
	VideoTrack *string `json:"videoTrack"`
	AudioTrack *string `json:"audioTrack"`
	Title      *string `json:"title"`
	Played     *struct {
		Resources   []playedFile `json:"resources"`
		DurationMs  *int64       `json:"durationMs"`
		VideoWidth  *int64       `json:"videoWidth"`
		VideoHeight *int64       `json:"videoHeight"`
		HasAudio    *int64       `json:"hasAudio"`
		// OtherDurationsMs are the frame's other <video> elements' durations.
		OtherDurationsMs []int64 `json:"otherDurationsMs"`
	} `json:"played"`
}

// parsePlayedFile checks one file: an id of 1 to 8 printable bytes, an http(s) address of
// at most 8 KiB, a size of 0 to 2^40, a kind of "video", "audio" or "", and a head of
// base64 of 1 byte to 256 KiB.
func parsePlayedFile(i int, r playedFile) (*downexv1.PlayedResource, error) {
	switch {
	case r.ID == nil || *r.ID == "" || len(*r.ID) > 8 || !printable(*r.ID):
		return nil, fmt.Errorf("file %d: id must be 1 to 8 printable bytes", i+1)
	case r.URL == nil || len(*r.URL) > maxPlayedURLBytes:
		return nil, fmt.Errorf("file %d: url must be at most %d bytes", i+1, maxPlayedURLBytes)
	case r.Size != nil && (*r.Size < 0 || *r.Size > maxPlayedSize):
		return nil, fmt.Errorf("file %d: size out of range", i+1)
	case r.Kind != nil && *r.Kind != "" && *r.Kind != "video" && *r.Kind != "audio":
		return nil, fmt.Errorf("file %d: kind must be video, audio or empty", i+1)
	case r.Head == nil || *r.Head == "" || base64.StdEncoding.DecodedLen(len(*r.Head)) > maxPlayedHeadBytes+2:
		return nil, fmt.Errorf("file %d: head must be base64 of 1 to %d bytes", i+1, maxPlayedHeadBytes)
	}
	if err := validate.URL(*r.URL); err != nil {
		return nil, fmt.Errorf("file %d: url: %w", i+1, err)
	}
	head, err := base64.StdEncoding.DecodeString(*r.Head)
	if err != nil || len(head) == 0 || len(head) > maxPlayedHeadBytes {
		return nil, fmt.Errorf("file %d: head must be base64 of 1 to %d bytes", i+1, maxPlayedHeadBytes)
	}
	res := &downexv1.PlayedResource{Id: *r.ID, Url: *r.URL, Head: head, Kind: deref(r.Kind), Browser: r.Browser != nil && *r.Browser}
	if r.Size != nil {
		res.Size = *r.Size
	}
	return res, nil
}

// parsePlayed validates what both played requests carry: the page (the frame's address, a
// hint), the deadline and the source: 1 to 16 files (parsePlayedFile), 1 MiB of heads in
// all, and the element's duration (0 to 24 h), picture (0 to 16384) and sound (-1, 0 or
// 1). The log keeps the counts, never an address or a byte.
func parsePlayed(msg []byte) (playedFields, *downexv1.PlayedSource, addInfo, error) {
	var f playedFields
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
	var err error
	if info.deadline, err = deadline(f.DeadlineMs, videoTimeout); err != nil {
		return f, nil, info, err
	}
	p := f.Played
	if p == nil {
		return f, nil, info, errors.New("played is required")
	}
	if n := len(p.Resources); n == 0 || n > maxPlayedFiles {
		return f, nil, info, fmt.Errorf("played.resources must hold 1 to %d files", maxPlayedFiles)
	}
	src := &downexv1.PlayedSource{}
	hosts := map[string]bool{}
	for i, r := range p.Resources {
		res, err := parsePlayedFile(i, r)
		if err != nil {
			return f, nil, info, err
		}
		if info.headBytes += len(res.Head); info.headBytes > maxPlayedHeads {
			return f, nil, info, fmt.Errorf("the heads must be at most %d bytes in all", maxPlayedHeads)
		}
		if u, err := url.Parse(res.Url); err == nil {
			hosts[u.Host] = true
		}
		src.Resources = append(src.Resources, res)
	}
	info.files, info.hosts = len(src.Resources), len(hosts)
	if p.DurationMs != nil {
		if *p.DurationMs < 0 || *p.DurationMs > maxPlayedMs {
			return f, nil, info, errors.New("played.durationMs out of range")
		}
		src.DurationMs = *p.DurationMs
	}
	for _, v := range []struct {
		n      *int64
		dst    *int32
		lo, hi int64
	}{{p.VideoWidth, &src.VideoWidth, 0, 16384}, {p.VideoHeight, &src.VideoHeight, 0, 16384}, {p.HasAudio, &src.HasAudio, -1, 1}} {
		if v.n == nil {
			continue
		}
		if *v.n < v.lo || *v.n > v.hi {
			return f, nil, info, errors.New("played: the element's picture or sound is out of range")
		}
		*v.dst = int32(*v.n)
	}
	if len(p.OtherDurationsMs) > maxPlayedOthers {
		return f, nil, info, fmt.Errorf("played.otherDurationsMs must hold at most %d durations", maxPlayedOthers)
	}
	for _, ms := range p.OtherDurationsMs {
		if ms <= 0 || ms > maxPlayedMs {
			return f, nil, info, errors.New("played.otherDurationsMs out of range")
		}
		src.OtherDurationsMs = append(src.OtherDurationsMs, ms)
	}
	return f, src, info, nil
}

// parseProbePlayed converts a probePlayed request (no side effects) to the daemon's
// ProbeMediaRequest with origin PLAYED; track and title fields are ignored.
func parseProbePlayed(msg []byte) (*downexv1.ProbeMediaRequest, addInfo, error) {
	f, src, info, err := parsePlayed(msg)
	if err != nil {
		return nil, info, err
	}
	return &downexv1.ProbeMediaRequest{PageUrl: *f.PageURL, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED, Played: src}, info, nil
}

// parseAddPlayed converts an addPlayed request to the daemon's AddMediaRequest with origin
// PLAYED and source BROWSER; empty tracks ask for the daemon's default pick.
func parseAddPlayed(msg []byte) (*downexv1.AddMediaRequest, addInfo, error) {
	f, src, info, err := parsePlayed(msg)
	if err != nil {
		return nil, info, err
	}
	req := &downexv1.AddMediaRequest{PageUrl: *f.PageURL, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED, Played: src,
		Source: downexv1.AddSource_ADD_SOURCE_BROWSER}
	if req.VideoTrack, req.AudioTrack, err = tracks(f.VideoTrack, f.AudioTrack); err != nil {
		return nil, info, err
	}
	if req.Title, info.title, err = title(f.Title); err != nil {
		return nil, info, err
	}
	return req, info, nil
}

// playedHost is what the log keeps of a played video's page: its host only.
func playedHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return truncate(u.Hostname(), 64)
}
