package media

import (
	"bytes"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"
)

// HLSPlaylist is a parsed HLS master or media playlist (RFC 8216 §4). URIs are
// resolved against the base URL given to ParseHLS.
type HLSPlaylist struct {
	// URL is the base URL the playlist was parsed with ("" when none).
	URL string
	// Master distinguishes a master playlist (Variants, Renditions) from a media
	// playlist (Segments).
	Master  bool
	Version int
	// IndependentSegments reports EXT-X-INDEPENDENT-SEGMENTS.
	IndependentSegments bool

	// Master playlist.
	Variants   []Variant
	Renditions []Rendition

	// Media playlist.
	TargetDuration        time.Duration
	MediaSequence         uint64
	DiscontinuitySequence uint64
	// PlaylistType is "VOD", "EVENT" or "" (EXT-X-PLAYLIST-TYPE).
	PlaylistType string
	EndList      bool
	IFramesOnly  bool
	Segments     []Segment
}

// Live reports a media playlist that may still change: it has neither
// EXT-X-ENDLIST nor EXT-X-PLAYLIST-TYPE:VOD (RFC 8216 §4.3.3.4, §4.3.3.5).
func (p *HLSPlaylist) Live() bool {
	return !p.Master && !p.EndList && p.PlaylistType != "VOD"
}

// Variant is an EXT-X-STREAM-INF variant stream (RFC 8216 §4.3.4.2).
type Variant struct {
	URI              string
	Bandwidth        int64
	AverageBandwidth int64
	Codecs           string
	Width, Height    int
	FrameRate        float64
	// Rendition groups (GROUP-ID values); ClosedCaptions may be "NONE".
	Audio, Video, Subtitles, ClosedCaptions string
}

// Rendition is an EXT-X-MEDIA alternative rendition (RFC 8216 §4.3.4.1).
type Rendition struct {
	// Type is AUDIO, VIDEO, SUBTITLES or CLOSED-CAPTIONS.
	Type    string
	GroupID string
	Name    string
	// Language and AssocLanguage are RFC 5646 tags.
	Language, AssocLanguage string
	Default, Autoselect     bool
	Forced                  bool
	// URI is "" when the rendition is carried in the variant streams themselves.
	URI             string
	Channels        string
	Characteristics string
	InstreamID      string
}

// Segment is a media segment of a media playlist.
type Segment struct {
	URI string
	// Sequence is the media sequence number (EXT-X-MEDIA-SEQUENCE plus position).
	Sequence uint64
	Duration time.Duration
	Title    string
	// ByteRange is the inclusive sub-range of URI (EXT-X-BYTERANGE), with implicit
	// offsets already resolved; nil for the whole resource.
	ByteRange     *[2]int64
	Discontinuity bool
	// Gap marks an EXT-X-GAP segment, which must not be fetched.
	Gap bool
	// Map is the Media Initialization Section in effect (EXT-X-MAP); segments under
	// one tag share one *InitSection.
	Map *InitSection
}

// InitSection is an EXT-X-MAP Media Initialization Section (RFC 8216 §4.3.2.5).
type InitSection struct {
	URI string
	// ByteRange is the inclusive sub-range of URI; nil for the whole resource.
	ByteRange *[2]int64
}

// Limits private to HLS parsing.
const (
	maxHLSVariables = 1_000
	maxHLSMaps      = MaxSegments
)

// ParseHLS parses a master or media playlist. Relative URIs are resolved against base
// (left relative when base is nil). It returns ErrEncrypted when any EXT-X-KEY or
// EXT-X-SESSION-KEY has a METHOD other than NONE. A live media playlist parses; see
// HLSPlaylist.Live and PlanHLS.
func ParseHLS(base *url.URL, data []byte) (*HLSPlaylist, error) {
	if len(data) > MaxManifestBytes {
		return nil, errf(ErrTooLarge, "playlist is larger than %d bytes", MaxManifestBytes)
	}
	if err := checkBase(base); err != nil {
		return nil, err
	}
	p := &hlsParser{base: base, res: resolver{base: base, budget: &urlBudget{}}}
	if base != nil {
		p.out.URL = base.String()
	}
	return p.parse(data)
}

type hlsParser struct {
	base *url.URL
	res  resolver
	line int
	out  HLSPlaylist

	masterTags, mediaTags bool
	versionSeen           bool

	vars map[string]string // EXT-X-DEFINE

	// Master playlist state.
	pendingVariant *Variant

	// Media playlist state: tags that apply to the next URI line.
	hasInf        bool
	infDuration   time.Duration
	infTitle      string
	hasRange      bool
	rangeN        uint64
	rangeO        uint64
	rangeHasO     bool
	discontinuity bool
	gap           bool
	curMap        *InitSection
	maps          int

	// The previous segment's sub-range, for implicit EXT-X-BYTERANGE offsets.
	prevRangeURI string
	prevRangeEnd uint64
	hasPrevRange bool
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func (p *hlsParser) fail(err error) error {
	return withContext(err, "line %d", p.line)
}

func (p *hlsParser) parse(data []byte) (*HLSPlaylist, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	header := false
	for len(data) > 0 {
		var raw []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			raw, data = data[:i], data[i+1:]
		} else {
			raw, data = data, nil
		}
		p.line++
		// RFC 8216 §4.1: a line ends with LF or CRLF.
		raw = bytes.TrimSuffix(raw, []byte{'\r'})
		line := bytes.TrimSpace(raw)
		// Encryption is refused first (docs/MEDIA-SPEC.md §2), also when the key tag
		// is hidden where this parser would not look for it.
		if err := hiddenKey(line); err != nil {
			return nil, p.fail(err)
		}
		if bytes.IndexByte(raw, '\r') >= 0 {
			// Readers that also end lines at a bare CR would see more lines than this
			// parser does.
			return nil, p.fail(errf(ErrMalformed, "line has a carriage return that does not end it"))
		}
		raw = line
		if !header {
			// RFC 8216 §4.3.1.1: the first line is #EXTM3U.
			if !bytes.Equal(raw, []byte("#EXTM3U")) && !bytes.HasPrefix(raw, []byte("#EXTM3U ")) {
				return nil, p.fail(errf(ErrMalformed, "playlist does not start with #EXTM3U"))
			}
			header = true
			continue
		}
		if len(raw) == 0 {
			continue
		}
		var err error
		switch {
		case raw[0] != '#':
			err = p.uriLine(string(raw))
		case bytes.HasPrefix(raw, []byte("#EXT")):
			err = p.tag(string(raw))
		}
		if err != nil {
			return nil, p.fail(err)
		}
	}
	if !header {
		return nil, errf(ErrMalformed, "playlist does not start with #EXTM3U")
	}
	if p.pendingVariant != nil {
		return nil, p.fail(errf(ErrMalformed, "EXT-X-STREAM-INF is not followed by a URI"))
	}
	if p.hasInf {
		return nil, p.fail(errf(ErrMalformed, "EXTINF is not followed by a URI"))
	}
	out := &p.out
	if p.masterTags {
		out.Master = true
		if len(out.Variants) == 0 {
			return nil, errf(ErrMalformed, "master playlist has no EXT-X-STREAM-INF variant")
		}
		return out, nil
	}
	for i := range out.Segments {
		if uint64(i) > math.MaxUint64-out.MediaSequence {
			return nil, errf(ErrMalformed, "media sequence numbers overflow")
		}
		out.Segments[i].Sequence = out.MediaSequence + uint64(i)
	}
	return out, nil
}

// master and media record which kind of playlist a tag belongs to; RFC 8216 §4.1
// forbids mixing them.
func (p *hlsParser) master() error {
	p.masterTags = true
	if p.mediaTags {
		return errf(ErrMalformed, "playlist mixes master and media playlist tags")
	}
	return nil
}

func (p *hlsParser) media() error {
	p.mediaTags = true
	if p.masterTags {
		return errf(ErrMalformed, "playlist mixes master and media playlist tags")
	}
	return nil
}

func (p *hlsParser) tag(s string) error {
	name, value, _ := strings.Cut(s, ":")
	// Encryption is refused before anything else about the tag is examined
	// (docs/MEDIA-SPEC.md §2).
	switch name {
	case "#EXT-X-KEY", "#EXT-X-SESSION-KEY":
		if err := checkKey(value); err != nil {
			return err
		}
	}
	switch name {
	case "#EXTM3U", "#EXT-X-START", "#EXT-X-INDEPENDENT-SEGMENTS":
		if name == "#EXT-X-INDEPENDENT-SEGMENTS" {
			p.out.IndependentSegments = true
		}
		return nil
	case "#EXT-X-VERSION":
		if p.versionSeen {
			return errf(ErrMalformed, "EXT-X-VERSION appears twice")
		}
		p.versionSeen = true
		v, err := parseDecimalInt(value)
		if err != nil || v > math.MaxInt32 {
			return errf(ErrMalformed, "EXT-X-VERSION is not a small decimal integer")
		}
		p.out.Version = int(v)
		return nil
	case "#EXT-X-DEFINE":
		return p.define(value)

	// Master playlist tags (RFC 8216 §4.3.4).
	case "#EXT-X-STREAM-INF":
		if err := p.master(); err != nil {
			return err
		}
		return p.streamInf(value)
	case "#EXT-X-MEDIA":
		if err := p.master(); err != nil {
			return err
		}
		return p.rendition(value)
	case "#EXT-X-I-FRAME-STREAM-INF", "#EXT-X-SESSION-DATA", "#EXT-X-SESSION-KEY", "#EXT-X-CONTENT-STEERING":
		// I-frame (trick play) variants are not offered as download options.
		return p.master()

	// Media playlist tags (RFC 8216 §4.3.2, §4.3.3).
	case "#EXTINF":
		if err := p.media(); err != nil {
			return err
		}
		durStr, title, _ := strings.Cut(value, ",")
		d, err := parseSeconds(strings.TrimSpace(durStr))
		if err != nil {
			return errf(ErrMalformed, "EXTINF: %v", err)
		}
		p.hasInf, p.infDuration, p.infTitle = true, d, title
		return nil
	case "#EXT-X-BYTERANGE":
		if err := p.media(); err != nil {
			return err
		}
		n, o, hasO, err := parseHLSByteRange(value)
		if err != nil {
			return errf(ErrMalformed, "EXT-X-BYTERANGE: %v", err)
		}
		p.hasRange, p.rangeN, p.rangeO, p.rangeHasO = true, n, o, hasO
		return nil
	case "#EXT-X-DISCONTINUITY":
		p.discontinuity = true
		return p.media()
	case "#EXT-X-GAP":
		p.gap = true
		return p.media()
	case "#EXT-X-MAP":
		if err := p.media(); err != nil {
			return err
		}
		return p.mapTag(value)
	case "#EXT-X-KEY":
		return p.media() // METHOD=NONE: no encryption from here on.
	case "#EXT-X-TARGETDURATION":
		if err := p.media(); err != nil {
			return err
		}
		v, err := parseDecimalInt(value)
		if err != nil || v > uint64(maxSeconds) {
			return errf(ErrMalformed, "EXT-X-TARGETDURATION is not a decimal integer")
		}
		p.out.TargetDuration = time.Duration(v) * time.Second
		return nil
	case "#EXT-X-MEDIA-SEQUENCE":
		if err := p.media(); err != nil {
			return err
		}
		v, err := parseDecimalInt(value)
		if err != nil {
			return errf(ErrMalformed, "EXT-X-MEDIA-SEQUENCE: %v", err)
		}
		p.out.MediaSequence = v
		return nil
	case "#EXT-X-DISCONTINUITY-SEQUENCE":
		if err := p.media(); err != nil {
			return err
		}
		v, err := parseDecimalInt(value)
		if err != nil {
			return errf(ErrMalformed, "EXT-X-DISCONTINUITY-SEQUENCE: %v", err)
		}
		p.out.DiscontinuitySequence = v
		return nil
	case "#EXT-X-PLAYLIST-TYPE":
		if err := p.media(); err != nil {
			return err
		}
		if value != "VOD" && value != "EVENT" {
			return errf(ErrMalformed, "EXT-X-PLAYLIST-TYPE is neither VOD nor EVENT")
		}
		p.out.PlaylistType = value
		return nil
	case "#EXT-X-ENDLIST":
		p.out.EndList = true
		return p.media()
	case "#EXT-X-I-FRAMES-ONLY":
		p.out.IFramesOnly = true
		return p.media()
	case "#EXT-X-SKIP":
		if err := p.media(); err != nil {
			return err
		}
		return errf(ErrUnsupported, "playlist delta updates (EXT-X-SKIP) are not supported")
	case "#EXT-X-PROGRAM-DATE-TIME", "#EXT-X-DATERANGE", "#EXT-X-BITRATE", "#EXT-X-PART",
		"#EXT-X-PART-INF", "#EXT-X-SERVER-CONTROL", "#EXT-X-PRELOAD-HINT", "#EXT-X-RENDITION-REPORT":
		return p.media()
	}
	// RFC 8216 §6.3.1: unrecognized tags are ignored.
	return nil
}

// hiddenKey refuses a key tag name anywhere in a line except as the line's own tag: in
// a comment, an unknown tag, a URI or an attribute value, after a character that some
// readers take as a line end, or as the start of a longer tag name. A reader that splits
// lines differently, or searches the text for tags, would apply such a key. The result
// is ErrEncrypted when what follows the hidden tag declares encryption, else
// ErrMalformed. It examines each line once.
func hiddenKey(line []byte) error {
	const prefix = "#EXT-X-"
	for off := 0; ; {
		i := bytes.Index(line[off:], []byte(prefix))
		if i < 0 {
			return nil
		}
		i += off
		off = i + 1
		var name string
		switch rest := line[i+len(prefix):]; {
		case bytes.HasPrefix(rest, []byte("KEY")):
			name = "#EXT-X-KEY"
		case bytes.HasPrefix(rest, []byte("SESSION-KEY")):
			name = "#EXT-X-SESSION-KEY"
		default:
			continue
		}
		after := line[i+len(name):]
		if i == 0 && (len(after) == 0 || after[0] == ':') {
			continue // the line's own tag, examined by tag
		}
		if value, ok := bytes.CutPrefix(after, []byte(":")); ok {
			if err := checkKey(string(value)); errors.Is(err, ErrEncrypted) {
				return err
			}
		}
		return errf(ErrMalformed, "line hides a key tag")
	}
}

// checkKey refuses EXT-X-KEY and EXT-X-SESSION-KEY whose METHOD is anything but NONE,
// including a missing METHOD.
func checkKey(value string) error {
	attrs, err := parseAttrList(value)
	if err != nil {
		return withContext(err, "key tag")
	}
	switch m, _ := attrs.get("METHOD"); {
	case m == "NONE":
		return nil
	case m == "" || len(m) > 32:
		return errf(ErrEncrypted, "playlist declares encryption")
	default:
		return errf(ErrEncrypted, "playlist declares encryption METHOD %q", m)
	}
}

// attrs parses an attribute list and applies EXT-X-DEFINE variable substitution to
// quoted-string and hexadecimal-sequence values (draft-pantos-hls-rfc8216bis §4.3).
func (p *hlsParser) attrs(tag, value string) (attrList, error) {
	l, err := parseAttrList(value)
	if err != nil {
		return nil, withContext(err, "%s", tag)
	}
	if p.vars != nil {
		for i := range l {
			if l[i].quoted || isHexSequence(l[i].value) {
				if l[i].value, err = p.substitute(l[i].value); err != nil {
					return nil, withContext(err, "%s %s", tag, l[i].name)
				}
			}
		}
	}
	return l, nil
}

func (p *hlsParser) streamInf(value string) error {
	if p.pendingVariant != nil {
		return errf(ErrMalformed, "EXT-X-STREAM-INF is not followed by a URI")
	}
	if len(p.out.Variants) >= MaxVariants {
		return errf(ErrTooLarge, "more than %d variant streams", MaxVariants)
	}
	l, err := p.attrs("EXT-X-STREAM-INF", value)
	if err != nil {
		return err
	}
	var v Variant
	for _, a := range l {
		switch a.name {
		case "BANDWIDTH", "AVERAGE-BANDWIDTH":
			n, err := parseInt63(a.value)
			if err != nil {
				return errf(ErrMalformed, "EXT-X-STREAM-INF %s: %v", a.name, err)
			}
			if a.name == "BANDWIDTH" {
				v.Bandwidth = n
			} else {
				v.AverageBandwidth = n
			}
		case "CODECS":
			if len(a.value) > maxTokenBytes {
				return errf(ErrTooLarge, "EXT-X-STREAM-INF CODECS is longer than %d bytes", maxTokenBytes)
			}
			v.Codecs = a.value
		case "RESOLUTION":
			if v.Width, v.Height, err = parseResolution(a.value); err != nil {
				return errf(ErrMalformed, "EXT-X-STREAM-INF RESOLUTION: %v", err)
			}
		case "FRAME-RATE":
			if v.FrameRate, err = parseDecimalFloat(a.value); err != nil {
				return errf(ErrMalformed, "EXT-X-STREAM-INF FRAME-RATE: %v", err)
			}
		case "AUDIO":
			v.Audio = a.value
		case "VIDEO":
			v.Video = a.value
		case "SUBTITLES":
			v.Subtitles = a.value
		case "CLOSED-CAPTIONS":
			v.ClosedCaptions = a.value
		}
	}
	p.pendingVariant = &v
	return nil
}

func (p *hlsParser) rendition(value string) error {
	if len(p.out.Renditions) >= MaxVariants {
		return errf(ErrTooLarge, "more than %d renditions", MaxVariants)
	}
	l, err := p.attrs("EXT-X-MEDIA", value)
	if err != nil {
		return err
	}
	var r Rendition
	for _, a := range l {
		switch a.name {
		case "TYPE":
			r.Type = a.value
		case "GROUP-ID":
			r.GroupID = a.value
		case "NAME":
			r.Name = a.value
		case "LANGUAGE":
			r.Language = a.value
		case "ASSOC-LANGUAGE":
			r.AssocLanguage = a.value
		case "DEFAULT":
			r.Default = a.value == "YES"
		case "AUTOSELECT":
			r.Autoselect = a.value == "YES"
		case "FORCED":
			r.Forced = a.value == "YES"
		case "CHANNELS":
			r.Channels = a.value
		case "CHARACTERISTICS":
			r.Characteristics = a.value
		case "INSTREAM-ID":
			r.InstreamID = a.value
		case "URI":
			if r.URI, err = p.res.resolve(a.value); err != nil {
				return withContext(err, "EXT-X-MEDIA URI")
			}
		}
	}
	if r.Type == "" || r.GroupID == "" {
		return errf(ErrMalformed, "EXT-X-MEDIA lacks TYPE or GROUP-ID")
	}
	switch r.Type {
	case "AUDIO", "VIDEO", "SUBTITLES":
	case "CLOSED-CAPTIONS":
		r.URI = "" // carried in the video; RFC 8216 forbids a URI here.
	default:
		return nil // Unknown rendition types are ignored.
	}
	p.out.Renditions = append(p.out.Renditions, r)
	return nil
}

func (p *hlsParser) mapTag(value string) error {
	if p.maps >= maxHLSMaps {
		return errf(ErrTooLarge, "more than %d EXT-X-MAP tags", maxHLSMaps)
	}
	l, err := p.attrs("EXT-X-MAP", value)
	if err != nil {
		return err
	}
	raw, ok := l.get("URI")
	if !ok || raw == "" {
		return errf(ErrMalformed, "EXT-X-MAP has no URI")
	}
	m := &InitSection{}
	if m.URI, err = p.res.resolve(raw); err != nil {
		return withContext(err, "EXT-X-MAP URI")
	}
	if br, ok := l.get("BYTERANGE"); ok {
		// A missing offset starts the range at the beginning of the resource.
		n, o, _, err := parseHLSByteRange(br)
		if err == nil {
			m.ByteRange, err = inclusiveRange(n, o)
		}
		if err != nil {
			return errf(ErrMalformed, "EXT-X-MAP BYTERANGE: %v", err)
		}
	}
	p.maps++
	p.curMap = m
	return nil
}

func (p *hlsParser) define(value string) error {
	l, err := parseAttrList(value)
	if err != nil {
		return withContext(err, "EXT-X-DEFINE")
	}
	if len(p.vars) >= maxHLSVariables {
		return errf(ErrTooLarge, "more than %d EXT-X-DEFINE variables", maxHLSVariables)
	}
	var name, val string
	if n, ok := l.get("NAME"); ok {
		v, ok := l.get("VALUE")
		if !ok {
			return errf(ErrMalformed, "EXT-X-DEFINE NAME has no VALUE")
		}
		name, val = n, v
	} else if q, ok := l.get("QUERYPARAM"); ok {
		var values url.Values
		if p.base != nil {
			values = p.base.Query()
		}
		if !values.Has(q) {
			return errf(ErrMalformed, "EXT-X-DEFINE QUERYPARAM is not in the playlist URL")
		}
		name, val = q, values.Get(q)
	} else if _, ok := l.get("IMPORT"); ok {
		return errf(ErrUnsupported, "EXT-X-DEFINE IMPORT needs the master playlist's variables")
	} else {
		return errf(ErrMalformed, "EXT-X-DEFINE has no NAME, QUERYPARAM or IMPORT")
	}
	if !isVariableName(name) {
		return errf(ErrMalformed, "EXT-X-DEFINE variable name is invalid")
	}
	if len(val) > MaxURIBytes {
		return errf(ErrTooLarge, "EXT-X-DEFINE value is longer than %d bytes", MaxURIBytes)
	}
	if _, dup := p.vars[name]; dup {
		return errf(ErrMalformed, "EXT-X-DEFINE variable is defined twice")
	}
	if p.vars == nil {
		p.vars = make(map[string]string)
	}
	p.vars[name] = val
	return nil
}

func isVariableName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// substitute replaces "{$name}" references with defined values; the result is at most
// MaxURIBytes long, checked before it is built.
func (p *hlsParser) substitute(s string) (string, error) {
	if !strings.Contains(s, "{$") {
		return s, nil
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "{$")
		if i < 0 {
			if b.Len()+len(s) > MaxURIBytes {
				return "", errf(ErrTooLarge, "substituted value is longer than %d bytes", MaxURIBytes)
			}
			b.WriteString(s)
			return b.String(), nil
		}
		end := strings.IndexByte(s[i+2:], '}')
		var name string
		if end >= 0 {
			name = s[i+2 : i+2+end]
		}
		if end < 0 || !isVariableName(name) {
			// Not a variable reference: keep the text.
			if b.Len()+i+2 > MaxURIBytes {
				return "", errf(ErrTooLarge, "substituted value is longer than %d bytes", MaxURIBytes)
			}
			b.WriteString(s[:i+2])
			s = s[i+2:]
			continue
		}
		v, ok := p.vars[name]
		if !ok {
			return "", errf(ErrMalformed, "reference to an undefined variable")
		}
		if b.Len()+i+len(v) > MaxURIBytes {
			return "", errf(ErrTooLarge, "substituted value is longer than %d bytes", MaxURIBytes)
		}
		b.WriteString(s[:i])
		b.WriteString(v)
		s = s[i+3+end:]
	}
}

func (p *hlsParser) uriLine(raw string) error {
	if p.vars != nil {
		var err error
		if raw, err = p.substitute(raw); err != nil {
			return err
		}
	}
	if p.pendingVariant != nil {
		v := p.pendingVariant
		p.pendingVariant = nil
		var err error
		if v.URI, err = p.res.resolve(raw); err != nil {
			return withContext(err, "variant URI")
		}
		p.out.Variants = append(p.out.Variants, *v)
		return nil
	}
	if p.masterTags {
		return errf(ErrMalformed, "URI line without EXT-X-STREAM-INF")
	}
	if !p.hasInf {
		return errf(ErrMalformed, "URI line without EXTINF")
	}
	if len(p.out.Segments) >= MaxSegments {
		return errf(ErrTooLarge, "more than %d media segments", MaxSegments)
	}
	p.mediaTags = true
	uri, err := p.res.resolve(raw)
	if err != nil {
		return withContext(err, "segment URI")
	}
	seg := Segment{
		URI:           uri,
		Duration:      p.infDuration,
		Title:         p.infTitle,
		Discontinuity: p.discontinuity,
		Gap:           p.gap,
		Map:           p.curMap,
	}
	if p.hasRange {
		o := p.rangeO
		if !p.rangeHasO {
			// RFC 8216 §4.3.2.2: without an offset the range continues the previous
			// segment's sub-range of the same resource.
			if !p.hasPrevRange || p.prevRangeURI != uri {
				return errf(ErrMalformed, "EXT-X-BYTERANGE without offset does not follow a sub-range of the same resource")
			}
			o = p.prevRangeEnd
		}
		if seg.ByteRange, err = inclusiveRange(p.rangeN, o); err != nil {
			return errf(ErrMalformed, "EXT-X-BYTERANGE: %v", err)
		}
		p.prevRangeURI, p.prevRangeEnd, p.hasPrevRange = uri, uint64(seg.ByteRange[1])+1, true
	} else {
		p.hasPrevRange = false
	}
	p.out.Segments = append(p.out.Segments, seg)
	p.hasInf, p.infDuration, p.infTitle = false, 0, ""
	p.hasRange, p.discontinuity, p.gap = false, false, false
	return nil
}
