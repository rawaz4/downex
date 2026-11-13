package media

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// MPD is a parsed static Media Presentation Description (ISO/IEC 23009-1 §5.3). Every
// BaseURL is resolved (inherited across MPD, Period, AdaptationSet and Representation);
// segment addressing (templates, SegmentURL media, sourceURL) is kept as written and
// resolved against Representation.BaseURL by PlanDASH. Attributes that a
// Representation inherits from its AdaptationSet, and SegmentTemplate, SegmentList or
// SegmentBase given at a higher level, are merged into each Representation.
type MPD struct {
	// URL is the base URL the MPD was parsed with ("" when none).
	URL string
	// Type is always "static": dynamic MPDs are refused with ErrLive.
	Type string
	// MediaPresentationDuration is 0 when absent.
	MediaPresentationDuration time.Duration
	BaseURL                   string
	Periods                   []Period
}

// Period is a Period of an MPD.
type Period struct {
	ID string
	// Start is the Period start (explicit, or derived from the previous Period).
	Start time.Duration
	// Duration is Period@duration, else derived from the next Period's start or
	// MPD@mediaPresentationDuration; 0 when unknown.
	Duration       time.Duration
	BaseURL        string
	AdaptationSets []AdaptationSet
}

// AdaptationSet is an AdaptationSet of a Period.
type AdaptationSet struct {
	ID              string
	ContentType     string
	MimeType        string
	Codecs          string
	Lang            string
	Label           string
	Representations []Representation
}

// Representation is one encoded version of a media component, with its inherited
// attributes and effective segment addressing. At most one of Template, List and Base
// is set; with none, the Representation is the single resource at BaseURL.
type Representation struct {
	ID                string
	Bandwidth         int64
	Width, Height     int
	FrameRate         string // as written, e.g. "30000/1001"
	Codecs            string
	MimeType          string
	ContentType       string
	AudioSamplingRate string
	Lang              string
	Label             string
	// BaseURL is the resolved base URL of the Representation's segments.
	BaseURL  string
	Template *SegmentTemplate
	List     *SegmentList
	Base     *SegmentBase
}

// NeedsIndex reports a SegmentBase Representation whose media segments are described
// only by a segment index ('sidx', or Cues for WebM) at Base.IndexRange.
func (r *Representation) NeedsIndex() bool {
	return r.Base != nil && r.Base.IndexRange != nil
}

// SegmentTemplate is effective SegmentTemplate addressing (ISO/IEC 23009-1 §5.3.9.4).
// Slices may be shared between Representations and must not be modified.
type SegmentTemplate struct {
	Media          string    // @media template, as written
	Initialization string    // @initialization template, as written
	Init           *URLRange // Initialization element
	StartNumber    uint64    // default 1
	EndNumber      uint64    // 0 when absent
	Timescale      uint64    // default 1, never 0
	// Duration is the constant segment duration in Timescale units; 0 when absent.
	Duration               uint64
	PresentationTimeOffset uint64
	// Timeline is the SegmentTimeline; nil when absent.
	Timeline []TimelineEntry
}

// TimelineEntry is a SegmentTimeline S element.
type TimelineEntry struct {
	T    uint64 // @t, meaningful when HasT
	HasT bool
	D    uint64 // @d, never 0
	// R is @r: the number of repeats after the first segment. A negative value repeats
	// until the next entry's @t (which that entry must then have) or, for the last
	// entry, the end of the Period. PlanDASH keeps only the segments that overlap the
	// Period on the media timeline (from PresentationTimeOffset for the Period's
	// duration); $Number$ still counts the others.
	R int64
}

// SegmentList is effective SegmentList addressing (ISO/IEC 23009-1 §5.3.9.3).
type SegmentList struct {
	StartNumber            uint64
	Timescale              uint64
	Duration               uint64
	PresentationTimeOffset uint64
	Init                   *URLRange
	Timeline               []TimelineEntry
	Segments               []SegmentURL
}

// SegmentURL is a SegmentList entry.
type SegmentURL struct {
	Media      string // as written; "" means the Representation's BaseURL
	MediaRange *[2]int64
}

// SegmentBase is SegmentBase addressing (ISO/IEC 23009-1 §5.3.9.2): the
// Representation is one resource, optionally with a segment index.
type SegmentBase struct {
	Timescale              uint64
	PresentationTimeOffset uint64
	IndexRange             *[2]int64
	Init                   *URLRange
}

// URLRange is an Initialization element: a URL (as written; "" means the
// Representation's BaseURL) and an optional inclusive byte range.
type URLRange struct {
	SourceURL string
	Range     *[2]int64
}

// ParseDASH parses a static MPD. Relative URLs are resolved against base (left
// relative when base is nil). Any ContentProtection element yields ErrEncrypted; an
// MPD with type="dynamic" yields ErrLive.
func ParseDASH(base *url.URL, data []byte) (*MPD, error) {
	if len(data) > MaxManifestBytes {
		return nil, errf(ErrTooLarge, "MPD is larger than %d bytes", MaxManifestBytes)
	}
	if err := checkBase(base); err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if err := scanMPD(data); err != nil {
		return nil, err
	}
	var x xmlMPD
	if err := xml.NewDecoder(bytes.NewReader(data)).Decode(&x); err != nil {
		return nil, errf(ErrMalformed, "MPD: %s", xmlError(err))
	}
	switch x.Type {
	case "", "static":
	case "dynamic":
		return nil, errf(ErrLive, "MPD is dynamic")
	default:
		return nil, errf(ErrMalformed, "MPD@type is neither static nor dynamic")
	}
	b := &dashBuilder{
		timelines: make(map[*xmlTimeline][]TimelineEntry),
		lists:     make(map[*xmlSegmentList][]SegmentURL),
	}
	return b.build(base, &x)
}

// scanMPD checks the limits of docs/MEDIA-SPEC.md §3 on the raw token stream, before
// the document is decoded into memory, and detects ContentProtection anywhere and a
// dynamic MPD.
func scanMPD(data []byte) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	var stack [MaxXMLDepth]string
	depth, elements := 0, 0
	periods, sets, reps := 0, 0, 0
	timelineS, listURLs := 0, 0
	root, encrypted, live := false, false, false
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errf(ErrMalformed, "MPD: %s", xmlError(err))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == MaxXMLDepth {
				return errf(ErrTooLarge, "MPD nests elements deeper than %d", MaxXMLDepth)
			}
			if elements++; elements > MaxXMLElements {
				return errf(ErrTooLarge, "MPD has more than %d elements", MaxXMLElements)
			}
			name := t.Name.Local
			parent := ""
			if depth > 0 {
				parent = stack[depth-1]
			} else if root {
				return errf(ErrMalformed, "MPD has more than one root element")
			} else {
				root = true
				if name != "MPD" {
					return errf(ErrMalformed, "root element is not MPD")
				}
				for _, a := range t.Attr {
					if a.Name.Space == "" && a.Name.Local == "type" && strings.TrimSpace(a.Value) == "dynamic" {
						live = true
					}
				}
			}
			stack[depth] = name
			depth++
			switch name {
			case "ContentProtection":
				encrypted = true
			case "Period":
				if periods++; periods > MaxVariants {
					return errf(ErrTooLarge, "MPD has more than %d Periods", MaxVariants)
				}
			case "AdaptationSet":
				if sets++; sets > MaxVariants {
					return errf(ErrTooLarge, "MPD has more than %d AdaptationSets", MaxVariants)
				}
			case "Representation":
				if reps++; reps > MaxVariants {
					return errf(ErrTooLarge, "MPD has more than %d Representations", MaxVariants)
				}
			case "SegmentTimeline":
				timelineS = 0
			case "S":
				if parent == "SegmentTimeline" {
					if timelineS++; timelineS > MaxSegments {
						return errf(ErrTooLarge, "SegmentTimeline has more than %d entries", MaxSegments)
					}
				}
			case "SegmentList":
				listURLs = 0
			case "SegmentURL":
				if parent == "SegmentList" {
					if listURLs++; listURLs > MaxSegments {
						return errf(ErrTooLarge, "SegmentList has more than %d entries", MaxSegments)
					}
				}
			}
		case xml.EndElement:
			if depth == 0 {
				return errf(ErrMalformed, "MPD has an unbalanced end tag")
			}
			depth--
		}
	}
	switch {
	case !root:
		return errf(ErrMalformed, "MPD has no root element")
	case depth != 0:
		return errf(ErrMalformed, "MPD ends inside an element")
	case encrypted:
		return errf(ErrEncrypted, "MPD declares ContentProtection")
	case live:
		return errf(ErrLive, "MPD is dynamic")
	}
	return nil
}

// xmlError formats a decoder error, cut short: it may quote document text.
func xmlError(err error) string {
	s := err.Error()
	if n := 200; len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "..."
	}
	return s
}

// XML schema subset (ISO/IEC 23009-1 Annex B). Attributes are strings, parsed and
// validated while building, except the numerous S attributes.
type xmlMPD struct {
	XMLName                   xml.Name    `xml:"MPD"`
	Type                      string      `xml:"type,attr"`
	MediaPresentationDuration string      `xml:"mediaPresentationDuration,attr"`
	BaseURLs                  []xmlText   `xml:"BaseURL"`
	Periods                   []xmlPeriod `xml:"Period"`
}

type xmlText struct {
	Value string `xml:",chardata"`
}

type xmlSegmentInfo struct {
	SegmentBase     *xmlSegmentBase     `xml:"SegmentBase"`
	SegmentList     *xmlSegmentList     `xml:"SegmentList"`
	SegmentTemplate *xmlSegmentTemplate `xml:"SegmentTemplate"`
}

type xmlCommon struct {
	MimeType          string `xml:"mimeType,attr"`
	Codecs            string `xml:"codecs,attr"`
	Width             string `xml:"width,attr"`
	Height            string `xml:"height,attr"`
	FrameRate         string `xml:"frameRate,attr"`
	AudioSamplingRate string `xml:"audioSamplingRate,attr"`
}

type xmlPeriod struct {
	ID       string    `xml:"id,attr"`
	Start    string    `xml:"start,attr"`
	Duration string    `xml:"duration,attr"`
	Href     string    `xml:"http://www.w3.org/1999/xlink href,attr"`
	BaseURLs []xmlText `xml:"BaseURL"`
	xmlSegmentInfo
	AdaptationSets []xmlAdaptationSet `xml:"AdaptationSet"`
}

type xmlAdaptationSet struct {
	ID          string `xml:"id,attr"`
	ContentType string `xml:"contentType,attr"`
	Lang        string `xml:"lang,attr"`
	Href        string `xml:"http://www.w3.org/1999/xlink href,attr"`
	xmlCommon
	Labels   []xmlText `xml:"Label"`
	BaseURLs []xmlText `xml:"BaseURL"`
	xmlSegmentInfo
	Representations []xmlRepresentation `xml:"Representation"`
}

type xmlRepresentation struct {
	ID        string `xml:"id,attr"`
	Bandwidth string `xml:"bandwidth,attr"`
	Lang      string `xml:"lang,attr"`
	xmlCommon
	Labels   []xmlText `xml:"Label"`
	BaseURLs []xmlText `xml:"BaseURL"`
	xmlSegmentInfo
}

type xmlSegmentTemplate struct {
	Media                  string       `xml:"media,attr"`
	Initialization         string       `xml:"initialization,attr"`
	StartNumber            string       `xml:"startNumber,attr"`
	EndNumber              string       `xml:"endNumber,attr"`
	Timescale              string       `xml:"timescale,attr"`
	Duration               string       `xml:"duration,attr"`
	PresentationTimeOffset string       `xml:"presentationTimeOffset,attr"`
	InitElem               *xmlURL      `xml:"Initialization"`
	Timeline               *xmlTimeline `xml:"SegmentTimeline"`
}

type xmlSegmentList struct {
	Href                   string          `xml:"http://www.w3.org/1999/xlink href,attr"`
	StartNumber            string          `xml:"startNumber,attr"`
	Timescale              string          `xml:"timescale,attr"`
	Duration               string          `xml:"duration,attr"`
	PresentationTimeOffset string          `xml:"presentationTimeOffset,attr"`
	InitElem               *xmlURL         `xml:"Initialization"`
	Timeline               *xmlTimeline    `xml:"SegmentTimeline"`
	SegmentURLs            []xmlSegmentURL `xml:"SegmentURL"`
}

type xmlSegmentBase struct {
	Timescale              string  `xml:"timescale,attr"`
	PresentationTimeOffset string  `xml:"presentationTimeOffset,attr"`
	IndexRange             string  `xml:"indexRange,attr"`
	InitElem               *xmlURL `xml:"Initialization"`
}

type xmlURL struct {
	SourceURL string `xml:"sourceURL,attr"`
	Range     string `xml:"range,attr"`
}

type xmlSegmentURL struct {
	Media      string `xml:"media,attr"`
	MediaRange string `xml:"mediaRange,attr"`
}

type xmlTimeline struct {
	S []xmlS `xml:"S"`
}

type xmlS struct {
	T *uint64 `xml:"t,attr"`
	D uint64  `xml:"d,attr"`
	R int64   `xml:"r,attr"`
}

// resolveToZero is the xlink:href value that removes the element (ISO/IEC 23009-1
// §5.5.3).
const resolveToZero = "urn:mpeg:dash:resolve-to-zero:2013"

// dropRemote reports whether an element with this xlink:href is removed, and refuses
// remote elements, which would need a network fetch.
func dropRemote(href string) (bool, error) {
	switch strings.TrimSpace(href) {
	case "":
		return false, nil
	case resolveToZero:
		return true, nil
	}
	return false, errf(ErrUnsupported, "remote (xlink) elements are not supported")
}

// baseRef is a resolved base URL and its string form.
type baseRef struct {
	u *url.URL
	s string
}

type dashBuilder struct {
	budget urlBudget
	// Converted timelines and segment lists, shared by every Representation that
	// inherits them so that inheritance never multiplies memory.
	timelines map[*xmlTimeline][]TimelineEntry
	lists     map[*xmlSegmentList][]SegmentURL
}

// baseURL applies the first BaseURL element of a level (further ones are alternative
// locations) to the inherited base.
func (b *dashBuilder) baseURL(parent baseRef, elems []xmlText) (baseRef, error) {
	if len(elems) == 0 {
		return parent, nil
	}
	raw := strings.TrimSpace(elems[0].Value)
	if raw == "" {
		return parent, nil
	}
	u, err := resolveRef(parent.u, raw)
	if err != nil {
		return baseRef{}, withContext(err, "BaseURL")
	}
	s, err := b.budget.urlString(u)
	if err != nil {
		return baseRef{}, err
	}
	return baseRef{u: u, s: s}, nil
}

func (b *dashBuilder) build(base *url.URL, x *xmlMPD) (*MPD, error) {
	m := &MPD{Type: "static"}
	if x.MediaPresentationDuration != "" {
		d, err := parseISODuration(x.MediaPresentationDuration)
		if err != nil {
			return nil, errf(ErrMalformed, "MPD@mediaPresentationDuration: %v", err)
		}
		m.MediaPresentationDuration = d
	}
	top := baseRef{u: base}
	if base != nil {
		top.s = base.String()
	}
	m.URL = top.s
	mb, err := b.baseURL(top, x.BaseURLs)
	if err != nil {
		return nil, withContext(err, "MPD")
	}
	m.BaseURL = mb.s

	periods := make([]*xmlPeriod, 0, len(x.Periods))
	for i := range x.Periods {
		drop, err := dropRemote(x.Periods[i].Href)
		if err != nil {
			return nil, withContext(err, "Period %d", i+1)
		}
		if !drop {
			periods = append(periods, &x.Periods[i])
		}
	}
	if len(periods) == 0 {
		return nil, errf(ErrMalformed, "MPD has no Period")
	}
	timing, err := periodTiming(periods, m.MediaPresentationDuration)
	if err != nil {
		return nil, err
	}
	m.Periods = make([]Period, len(periods))
	for pi, xp := range periods {
		per := &m.Periods[pi]
		per.ID = xp.ID
		per.Start, per.Duration = timing[pi].start, timing[pi].duration
		pb, err := b.baseURL(mb, xp.BaseURLs)
		if err != nil {
			return nil, withContext(err, "Period %d", pi+1)
		}
		per.BaseURL = pb.s
		if err := normalizeSegmentInfo(&xp.xmlSegmentInfo); err != nil {
			return nil, withContext(err, "Period %d", pi+1)
		}
		for ai := range xp.AdaptationSets {
			xa := &xp.AdaptationSets[ai]
			drop, err := dropRemote(xa.Href)
			if err != nil {
				return nil, withContext(err, "Period %d AdaptationSet %d", pi+1, ai+1)
			}
			if drop {
				continue
			}
			// What the Representations inherit is normalized here, once.
			set, err := readAttrs(&xa.xmlCommon, xa.ContentType, xa.Lang, xa.Labels)
			if err == nil {
				err = normalizeSegmentInfo(&xa.xmlSegmentInfo)
			}
			if err != nil {
				return nil, withContext(err, "Period %d AdaptationSet %d", pi+1, ai+1)
			}
			as := AdaptationSet{
				ID:          xa.ID,
				ContentType: set.contentType,
				MimeType:    set.mimeType,
				Codecs:      set.codecs,
				Lang:        set.lang,
				Label:       set.label,
			}
			ab, err := b.baseURL(pb, xa.BaseURLs)
			if err != nil {
				return nil, withContext(err, "Period %d AdaptationSet %d", pi+1, ai+1)
			}
			as.Representations = make([]Representation, 0, len(xa.Representations))
			for ri := range xa.Representations {
				rep, err := b.representation(xp, xa, &set, &xa.Representations[ri], ab)
				if err != nil {
					return nil, withContext(err, "Period %d AdaptationSet %d Representation %d", pi+1, ai+1, ri+1)
				}
				as.Representations = append(as.Representations, rep)
			}
			per.AdaptationSets = append(per.AdaptationSets, as)
		}
	}
	return m, nil
}

type periodTimes struct {
	start, duration time.Duration
}

// periodTiming derives Period starts and durations (ISO/IEC 23009-1 §5.3.2.1): a
// missing @start follows the previous Period; a missing @duration ends at the next
// Period's @start or, for the last Period, at MPD@mediaPresentationDuration.
func periodTiming(periods []*xmlPeriod, total time.Duration) ([]periodTimes, error) {
	n := len(periods)
	out := make([]periodTimes, n)
	startKnown := make([]bool, n)
	startExplicit := make([]bool, n)
	durKnown := make([]bool, n)
	for i, xp := range periods {
		switch {
		case xp.Start != "":
			d, err := parseISODuration(xp.Start)
			if err != nil {
				return nil, errf(ErrMalformed, "Period %d @start: %v", i+1, err)
			}
			out[i].start, startKnown[i], startExplicit[i] = d, true, true
		case i == 0:
			startKnown[i] = true
		case startKnown[i-1] && durKnown[i-1]:
			out[i].start, startKnown[i] = addDuration(out[i-1].start, out[i-1].duration), true
		}
		if xp.Duration != "" {
			d, err := parseISODuration(xp.Duration)
			if err != nil {
				return nil, errf(ErrMalformed, "Period %d @duration: %v", i+1, err)
			}
			out[i].duration, durKnown[i] = d, true
		}
	}
	for i := range out {
		if durKnown[i] || !startKnown[i] {
			continue
		}
		switch {
		case i+1 < n && startExplicit[i+1]:
			if next := out[i+1].start; next >= out[i].start {
				out[i].duration = next - out[i].start
			}
		case i == n-1 && total > 0 && total >= out[i].start:
			out[i].duration = total - out[i].start
		}
	}
	return out, nil
}

func firstText(elems []xmlText) string {
	for _, e := range elems {
		if v := strings.TrimSpace(e.Value); v != "" {
			return v
		}
	}
	return ""
}

// attrs are the attributes a Representation may inherit from its AdaptationSet
// (ISO/IEC 23009-1 §5.3.7), trimmed, bounded and parsed. An AdaptationSet's are read
// once, so that inheriting them costs nothing per Representation.
type attrs struct {
	contentType, mimeType, codecs, frameRate string
	audioSamplingRate, lang, label           string
	width, height                            int
	hasWidth, hasHeight                      bool
}

func readAttrs(c *xmlCommon, contentType, lang string, labels []xmlText) (attrs, error) {
	a := attrs{
		audioSamplingRate: strings.TrimSpace(c.AudioSamplingRate),
		lang:              strings.TrimSpace(lang),
		label:             firstText(labels),
	}
	err := normTokens(
		field{&contentType, "contentType"}, field{&c.MimeType, "mimeType"},
		field{&c.Codecs, "codecs"}, field{&c.FrameRate, "frameRate"})
	if err != nil {
		return a, err
	}
	a.contentType, a.mimeType, a.codecs, a.frameRate = contentType, c.MimeType, c.Codecs, c.FrameRate
	if a.width, a.hasWidth, err = dimension(c.Width, "width"); err != nil {
		return a, err
	}
	a.height, a.hasHeight, err = dimension(c.Height, "height")
	return a, err
}

// inherit fills the attributes a leaves unset from those of its AdaptationSet.
func (a *attrs) inherit(set *attrs) {
	for _, f := range [...]struct{ dst, v *string }{
		{&a.contentType, &set.contentType}, {&a.mimeType, &set.mimeType}, {&a.codecs, &set.codecs},
		{&a.frameRate, &set.frameRate}, {&a.audioSamplingRate, &set.audioSamplingRate},
		{&a.lang, &set.lang}, {&a.label, &set.label},
	} {
		if *f.dst == "" {
			*f.dst = *f.v
		}
	}
	if !a.hasWidth {
		a.width, a.hasWidth = set.width, set.hasWidth
	}
	if !a.hasHeight {
		a.height, a.hasHeight = set.height, set.hasHeight
	}
}

func dimension(s, name string) (int, bool, error) {
	v, ok, err := optUint(s, name)
	if err == nil && v > math.MaxInt32 {
		err = errf(ErrMalformed, "@%s is out of range", name)
	}
	return int(v), ok, err
}

// field names a string attribute to normalize in place.
type field struct {
	p    *string
	name string
}

// normTokens trims short attributes in place and bounds their length (maxTokenBytes).
func normTokens(fields ...field) error {
	for _, f := range fields {
		*f.p = strings.TrimSpace(*f.p)
		if len(*f.p) > maxTokenBytes {
			return errf(ErrTooLarge, "@%s is longer than %d bytes", f.name, maxTokenBytes)
		}
	}
	return nil
}

// normalizeSegmentInfo trims the segment addressing attributes of one level in place
// and bounds the short ones. Each element is normalized once; the Representations that
// inherit it then merge its values without examining their text again.
func normalizeSegmentInfo(si *xmlSegmentInfo) error {
	if x := si.SegmentTemplate; x != nil {
		x.Media, x.Initialization = strings.TrimSpace(x.Media), strings.TrimSpace(x.Initialization)
		err := normTokens(
			field{&x.StartNumber, "startNumber"}, field{&x.EndNumber, "endNumber"}, field{&x.Timescale, "timescale"},
			field{&x.Duration, "duration"}, field{&x.PresentationTimeOffset, "presentationTimeOffset"})
		if err == nil {
			err = normalizeURLRange(x.InitElem)
		}
		if err != nil {
			return withContext(err, "SegmentTemplate")
		}
	}
	if x := si.SegmentList; x != nil {
		x.Href = strings.TrimSpace(x.Href)
		err := normTokens(
			field{&x.StartNumber, "startNumber"}, field{&x.Timescale, "timescale"},
			field{&x.Duration, "duration"}, field{&x.PresentationTimeOffset, "presentationTimeOffset"})
		if err == nil {
			err = normalizeURLRange(x.InitElem)
		}
		if err != nil {
			return withContext(err, "SegmentList")
		}
	}
	if x := si.SegmentBase; x != nil {
		err := normTokens(
			field{&x.Timescale, "timescale"}, field{&x.PresentationTimeOffset, "presentationTimeOffset"},
			field{&x.IndexRange, "indexRange"})
		if err == nil {
			err = normalizeURLRange(x.InitElem)
		}
		if err != nil {
			return withContext(err, "SegmentBase")
		}
	}
	return nil
}

func normalizeURLRange(x *xmlURL) error {
	if x == nil {
		return nil
	}
	x.SourceURL = strings.TrimSpace(x.SourceURL)
	if err := normTokens(field{&x.Range, "range"}); err != nil {
		return withContext(err, "Initialization")
	}
	return nil
}

// optUint parses an optional xs:unsignedInt/unsignedLong attribute.
func optUint(s, name string) (uint64, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	v, err := parseDecimalInt(s)
	if err != nil {
		return 0, false, errf(ErrMalformed, "@%s: %v", name, err)
	}
	return v, true, nil
}

func optInt(s, name string, max uint64) (uint64, error) {
	v, _, err := optUint(s, name)
	if err == nil && v > max {
		err = errf(ErrMalformed, "@%s is out of range", name)
	}
	return v, err
}

func checkURILength(s, name string) error {
	if len(s) > MaxURIBytes {
		return errf(ErrTooLarge, "%s is longer than %d bytes", name, MaxURIBytes)
	}
	return nil
}

// representation builds a Representation of AdaptationSet xa, whose own attributes are
// set and whose segment addressing (and that of Period xp) is already normalized.
func (b *dashBuilder) representation(xp *xmlPeriod, xa *xmlAdaptationSet, set *attrs, xr *xmlRepresentation, parent baseRef) (Representation, error) {
	r := Representation{ID: strings.TrimSpace(xr.ID)}
	if r.ID == "" {
		return r, errf(ErrMalformed, "Representation has no @id")
	}
	a, err := readAttrs(&xr.xmlCommon, "", xr.Lang, xr.Labels)
	if err != nil {
		return r, err
	}
	a.inherit(set)
	r.ContentType, r.MimeType, r.Codecs, r.FrameRate = a.contentType, a.mimeType, a.codecs, a.frameRate
	r.AudioSamplingRate, r.Lang, r.Label = a.audioSamplingRate, a.lang, a.label
	r.Width, r.Height = a.width, a.height
	bw, err := optInt(xr.Bandwidth, "bandwidth", math.MaxInt64)
	if err != nil {
		return r, err
	}
	r.Bandwidth = int64(bw)
	if err := normalizeSegmentInfo(&xr.xmlSegmentInfo); err != nil {
		return r, err
	}
	rb, err := b.baseURL(parent, xr.BaseURLs)
	if err != nil {
		return r, err
	}
	r.BaseURL = rb.s

	levels := [3]*xmlSegmentInfo{&xp.xmlSegmentInfo, &xa.xmlSegmentInfo, &xr.xmlSegmentInfo}
	kind, err := segmentKind(levels)
	if err != nil {
		return r, err
	}
	switch kind {
	case kindTemplate:
		r.Template, err = b.template(levels)
	case kindList:
		r.List, err = b.list(levels)
	case kindBase:
		r.Base, err = segmentBase(levels)
	}
	return r, err
}

type segKind int

const (
	kindNone segKind = iota
	kindTemplate
	kindList
	kindBase
)

// segmentKind picks the addressing of the lowest level that has any; the same kind at
// higher levels supplies defaults (ISO/IEC 23009-1 §5.3.9.1).
func segmentKind(levels [3]*xmlSegmentInfo) (segKind, error) {
	for i := len(levels) - 1; i >= 0; i-- {
		l := levels[i]
		if l.SegmentList != nil {
			drop, err := dropRemote(l.SegmentList.Href)
			if err != nil {
				return kindNone, err
			}
			if drop {
				l.SegmentList = nil
			}
		}
		switch {
		case l.SegmentTemplate != nil:
			return kindTemplate, nil
		case l.SegmentList != nil:
			return kindList, nil
		case l.SegmentBase != nil:
			return kindBase, nil
		}
	}
	return kindNone, nil
}

// pickString takes a level's value when it has one; levels are normalized (trimmed)
// before they are merged.
func pickString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func (b *dashBuilder) template(levels [3]*xmlSegmentInfo) (*SegmentTemplate, error) {
	var m xmlSegmentTemplate
	for _, l := range levels {
		x := l.SegmentTemplate
		if x == nil {
			continue
		}
		pickString(&m.Media, x.Media)
		pickString(&m.Initialization, x.Initialization)
		pickString(&m.StartNumber, x.StartNumber)
		pickString(&m.EndNumber, x.EndNumber)
		pickString(&m.Timescale, x.Timescale)
		pickString(&m.Duration, x.Duration)
		pickString(&m.PresentationTimeOffset, x.PresentationTimeOffset)
		if x.InitElem != nil {
			m.InitElem = x.InitElem
		}
		if x.Timeline != nil {
			m.Timeline = x.Timeline
		}
	}
	t := &SegmentTemplate{Media: m.Media, Initialization: m.Initialization, StartNumber: 1, Timescale: 1}
	if t.Media == "" {
		return nil, errf(ErrMalformed, "SegmentTemplate has no @media")
	}
	if err := checkURILength(t.Media, "SegmentTemplate@media"); err != nil {
		return nil, err
	}
	if err := checkURILength(t.Initialization, "SegmentTemplate@initialization"); err != nil {
		return nil, err
	}
	var err error
	if t.Init, err = urlRange(m.InitElem); err != nil {
		return nil, err
	}
	if v, ok, err := optUint(m.StartNumber, "startNumber"); err != nil {
		return nil, err
	} else if ok {
		t.StartNumber = v
	}
	if t.EndNumber, _, err = optUint(m.EndNumber, "endNumber"); err != nil {
		return nil, err
	}
	if t.Timescale, err = timescale(m.Timescale); err != nil {
		return nil, err
	}
	if t.Duration, _, err = optUint(m.Duration, "duration"); err != nil {
		return nil, err
	}
	if t.PresentationTimeOffset, _, err = optUint(m.PresentationTimeOffset, "presentationTimeOffset"); err != nil {
		return nil, err
	}
	if t.Timeline, err = b.timeline(m.Timeline); err != nil {
		return nil, err
	}
	return t, nil
}

func timescale(s string) (uint64, error) {
	v, ok, err := optUint(s, "timescale")
	switch {
	case err != nil:
		return 0, err
	case !ok:
		return 1, nil
	case v == 0:
		return 0, errf(ErrMalformed, "@timescale is zero")
	}
	return v, nil
}

func (b *dashBuilder) timeline(x *xmlTimeline) ([]TimelineEntry, error) {
	if x == nil {
		return nil, nil
	}
	if tl, ok := b.timelines[x]; ok {
		return tl, nil
	}
	tl := make([]TimelineEntry, len(x.S)) // at most MaxSegments, checked by scanMPD
	for i, s := range x.S {
		if s.D == 0 {
			return nil, errf(ErrMalformed, "SegmentTimeline S %d has no @d", i+1)
		}
		tl[i] = TimelineEntry{D: s.D, R: s.R}
		if s.T != nil {
			tl[i].T, tl[i].HasT = *s.T, true
		}
	}
	b.timelines[x] = tl
	return tl, nil
}

func urlRange(x *xmlURL) (*URLRange, error) {
	if x == nil {
		return nil, nil
	}
	u := &URLRange{SourceURL: x.SourceURL} // normalized by normalizeURLRange
	if err := checkURILength(u.SourceURL, "Initialization@sourceURL"); err != nil {
		return nil, err
	}
	if r := x.Range; r != "" {
		var err error
		if u.Range, err = parseDASHRange(r); err != nil {
			return nil, errf(ErrMalformed, "Initialization@range: %v", err)
		}
	}
	return u, nil
}

func (b *dashBuilder) list(levels [3]*xmlSegmentInfo) (*SegmentList, error) {
	var m xmlSegmentList
	var src *xmlSegmentList
	for _, l := range levels {
		x := l.SegmentList
		if x == nil {
			continue
		}
		pickString(&m.StartNumber, x.StartNumber)
		pickString(&m.Timescale, x.Timescale)
		pickString(&m.Duration, x.Duration)
		pickString(&m.PresentationTimeOffset, x.PresentationTimeOffset)
		if x.InitElem != nil {
			m.InitElem = x.InitElem
		}
		if x.Timeline != nil {
			m.Timeline = x.Timeline
		}
		if len(x.SegmentURLs) > 0 {
			src = x
		}
	}
	l := &SegmentList{StartNumber: 1}
	var err error
	if v, ok, err := optUint(m.StartNumber, "startNumber"); err != nil {
		return nil, err
	} else if ok {
		l.StartNumber = v
	}
	if l.Timescale, err = timescale(m.Timescale); err != nil {
		return nil, err
	}
	if l.Duration, _, err = optUint(m.Duration, "duration"); err != nil {
		return nil, err
	}
	if l.PresentationTimeOffset, _, err = optUint(m.PresentationTimeOffset, "presentationTimeOffset"); err != nil {
		return nil, err
	}
	if l.Init, err = urlRange(m.InitElem); err != nil {
		return nil, err
	}
	if l.Timeline, err = b.timeline(m.Timeline); err != nil {
		return nil, err
	}
	if src != nil {
		if l.Segments, err = b.segmentURLs(src); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (b *dashBuilder) segmentURLs(x *xmlSegmentList) ([]SegmentURL, error) {
	if s, ok := b.lists[x]; ok {
		return s, nil
	}
	out := make([]SegmentURL, len(x.SegmentURLs)) // at most MaxSegments, checked by scanMPD
	for i, su := range x.SegmentURLs {
		out[i].Media = strings.TrimSpace(su.Media)
		if err := checkURILength(out[i].Media, "SegmentURL@media"); err != nil {
			return nil, err
		}
		if r := strings.TrimSpace(su.MediaRange); r != "" {
			var err error
			if out[i].MediaRange, err = parseDASHRange(r); err != nil {
				return nil, errf(ErrMalformed, "SegmentURL %d @mediaRange: %v", i+1, err)
			}
		}
	}
	b.lists[x] = out
	return out, nil
}

func segmentBase(levels [3]*xmlSegmentInfo) (*SegmentBase, error) {
	var m xmlSegmentBase
	for _, l := range levels {
		x := l.SegmentBase
		if x == nil {
			continue
		}
		pickString(&m.Timescale, x.Timescale)
		pickString(&m.PresentationTimeOffset, x.PresentationTimeOffset)
		pickString(&m.IndexRange, x.IndexRange)
		if x.InitElem != nil {
			m.InitElem = x.InitElem
		}
	}
	sb := &SegmentBase{}
	var err error
	if sb.Timescale, err = timescale(m.Timescale); err != nil {
		return nil, err
	}
	if sb.PresentationTimeOffset, _, err = optUint(m.PresentationTimeOffset, "presentationTimeOffset"); err != nil {
		return nil, err
	}
	if m.IndexRange != "" {
		if sb.IndexRange, err = parseDASHRange(m.IndexRange); err != nil {
			return nil, errf(ErrMalformed, "SegmentBase@indexRange: %v", err)
		}
	}
	if sb.Init, err = urlRange(m.InitElem); err != nil {
		return nil, err
	}
	return sb, nil
}
