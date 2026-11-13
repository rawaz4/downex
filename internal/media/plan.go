package media

import (
	"math"
	"math/bits"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// PlanHLS plans a media playlist: its segments in order, each preceded by its
// EXT-X-MAP initialization section whenever that changes. EXT-X-GAP segments are left
// out and the next fragment is marked as a discontinuity. A master playlist has no
// segments of its own (plan the media playlist of a chosen Track.URL); a live
// playlist yields ErrLive and an I-frame-only playlist ErrUnsupported.
func PlanHLS(media *HLSPlaylist) (*Plan, error) {
	switch {
	case media == nil:
		return nil, errf(ErrNotFound, "no playlist")
	case media.Master:
		return nil, errf(ErrNotFound, "a master playlist has no segments; plan a variant's media playlist")
	case media.Live():
		return nil, errf(ErrLive, "media playlist has no EXT-X-ENDLIST")
	case media.IFramesOnly:
		return nil, errf(ErrUnsupported, "I-frame-only playlists are not downloadable media")
	case len(media.Segments) > MaxSegments:
		return nil, errf(ErrTooLarge, "more than %d media segments", MaxSegments)
	}
	p := &Plan{Fragments: make([]Fragment, 0, len(media.Segments)+1)}
	var curMap *InitSection
	pendingDisc := false
	for i := range media.Segments {
		s := &media.Segments[i]
		disc := pendingDisc || s.Discontinuity
		if s.Gap {
			pendingDisc = true
			continue
		}
		pendingDisc = false
		if s.Map != nil && !sameInit(s.Map, curMap) {
			curMap = s.Map
			p.Fragments = append(p.Fragments, Fragment{URL: s.Map.URI, ByteRange: copyRange(s.Map.ByteRange), Init: true, Discontinuity: disc})
			disc = false
		}
		p.Fragments = append(p.Fragments, Fragment{URL: s.URI, ByteRange: copyRange(s.ByteRange), Duration: s.Duration, Discontinuity: disc})
	}
	if p.MediaFragments() == 0 {
		return nil, errf(ErrNotFound, "media playlist has no segments")
	}
	p.Container = hlsContainer(media)
	if err := p.finish(); err != nil {
		return nil, err
	}
	return p, nil
}

func sameInit(a, b *InitSection) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || a.URI != b.URI {
		return false
	}
	if a.ByteRange == nil || b.ByteRange == nil {
		return a.ByteRange == b.ByteRange
	}
	return *a.ByteRange == *b.ByteRange
}

func copyRange(r *[2]int64) *[2]int64 {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// hlsContainer infers the segment format: EXT-X-MAP means fMP4 unless the map is a TS
// resource; otherwise the first segment's file extension decides.
func hlsContainer(p *HLSPlaylist) Container {
	for i := range p.Segments {
		s := &p.Segments[i]
		if s.Map != nil {
			if c := containerFromExt(s.Map.URI); c == ContainerTS {
				return c
			}
			return ContainerFMP4
		}
	}
	if len(p.Segments) > 0 {
		return containerFromExt(p.Segments[0].URI)
	}
	return ContainerUnknown
}

func containerFromExt(raw string) Container {
	p := raw
	if u, err := url.Parse(raw); err == nil {
		p = u.Path
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".ts", ".m2ts", ".mts", ".m2t", ".tsv", ".tsa":
		return ContainerTS
	case ".mp4", ".m4s", ".m4v", ".m4a", ".mp4a", ".mp4v", ".cmfv", ".cmfa", ".cmft", ".cmfm", ".m4f", ".fmp4", ".ismv", ".isma":
		return ContainerFMP4
	case ".webm", ".mkv", ".mka", ".weba":
		return ContainerWebM
	}
	return ContainerUnknown
}

func containerFromMime(mime string) Container {
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "mp4"):
		return ContainerFMP4
	case strings.Contains(mime, "webm"), strings.Contains(mime, "matroska"):
		return ContainerWebM
	case strings.Contains(mime, "mp2t"):
		return ContainerTS
	}
	return ContainerUnknown
}

// PlanDASH plans the Representation with the given @id: in every Period that has it,
// its initialization data and media segments in order, the first fragment of each later
// Period marked as a discontinuity. A SegmentBase Representation is one whole-resource
// fragment carrying Fragment.IndexRange.
func PlanDASH(mpd *MPD, representationID string) (*Plan, error) {
	if mpd == nil {
		return nil, errf(ErrNotFound, "no MPD")
	}
	if mpd.Type == "dynamic" {
		return nil, errf(ErrLive, "MPD is dynamic")
	}
	p := &Plan{}
	pl := &dashPlanner{plan: p, docURL: mpd.URL}
	found := false
	for pi := range mpd.Periods {
		per := &mpd.Periods[pi]
		rep := per.representation(representationID)
		if rep == nil {
			continue
		}
		if !found {
			found = true
			p.Container = containerFromMime(rep.MimeType)
		}
		start := len(p.Fragments)
		if err := pl.representation(per, rep); err != nil {
			return nil, withContext(err, "Period %d", pi+1)
		}
		if start > 0 && len(p.Fragments) > start {
			p.Fragments[start].Discontinuity = true
		}
	}
	if !found {
		return nil, errf(ErrNotFound, "no Representation with that @id")
	}
	if pl.media == 0 {
		return nil, errf(ErrNotFound, "Representation has no media segments")
	}
	if p.Container == ContainerUnknown {
		for i := range p.Fragments {
			if !p.Fragments[i].Init {
				p.Container = containerFromExt(p.Fragments[i].URL)
				break
			}
		}
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	return p, nil
}

func (per *Period) representation(id string) *Representation {
	for ai := range per.AdaptationSets {
		as := &per.AdaptationSets[ai]
		for ri := range as.Representations {
			if as.Representations[ri].ID == id {
				return &as.Representations[ri]
			}
		}
	}
	return nil
}

type dashPlanner struct {
	plan   *Plan
	docURL string // MPD.URL
	media  int    // media fragments so far, at most MaxSegments
	budget urlBudget
}

func (pl *dashPlanner) reserve(n uint64) error {
	if n > uint64(MaxSegments-pl.media) {
		return errf(ErrTooLarge, "more than %d media segments", MaxSegments)
	}
	pl.media += int(n)
	return nil
}

func (pl *dashPlanner) add(f Fragment) {
	pl.plan.Fragments = append(pl.plan.Fragments, f)
}

// resolve resolves a reference against the Representation's base. "" is the base
// itself, which must come from a BaseURL element: the MPD's own URL is not media.
func (pl *dashPlanner) resolve(base *url.URL, baseStr, raw string) (string, error) {
	if raw == "" {
		if baseStr == "" || baseStr == pl.docURL {
			return "", errf(ErrMalformed, "Representation has no media URL")
		}
		return baseStr, nil
	}
	u, err := resolveRef(base, raw)
	if err != nil {
		return "", err
	}
	return pl.budget.urlString(u)
}

func (pl *dashPlanner) representation(per *Period, rep *Representation) error {
	var base *url.URL
	if rep.BaseURL != "" {
		var err error
		if base, err = url.Parse(rep.BaseURL); err != nil {
			return errf(ErrMalformed, "invalid BaseURL")
		}
	}
	switch {
	case rep.Template != nil:
		return pl.template(per, rep, base, rep.Template)
	case rep.List != nil:
		return pl.list(per, rep, base, rep.List)
	case rep.Base != nil:
		return pl.segmentBase(per, rep, base, rep.Base)
	}
	u, err := pl.resolve(base, rep.BaseURL, "")
	if err != nil {
		return err
	}
	if err := pl.reserve(1); err != nil {
		return err
	}
	pl.add(Fragment{URL: u, Duration: per.Duration})
	return nil
}

func (pl *dashPlanner) initFragment(base *url.URL, baseStr string, init *URLRange) error {
	if init == nil {
		return nil
	}
	u, err := pl.resolve(base, baseStr, init.SourceURL)
	if err != nil {
		return withContext(err, "Initialization")
	}
	pl.add(Fragment{URL: u, ByteRange: copyRange(init.Range), Init: true})
	return nil
}

func (pl *dashPlanner) segmentBase(per *Period, rep *Representation, base *url.URL, sb *SegmentBase) error {
	// The Representation is one resource holding initialization data, the index and
	// all media; only initialization data stored elsewhere is a fragment of its own.
	var initRange *[2]int64
	if sb.Init != nil {
		if sb.Init.SourceURL != "" {
			if err := pl.initFragment(base, rep.BaseURL, sb.Init); err != nil {
				return err
			}
		} else {
			initRange = copyRange(sb.Init.Range)
		}
	}
	u, err := pl.resolve(base, rep.BaseURL, "")
	if err != nil {
		return err
	}
	if err := pl.reserve(1); err != nil {
		return err
	}
	pl.add(Fragment{URL: u, Duration: per.Duration, IndexRange: copyRange(sb.IndexRange), InitRange: initRange})
	return nil
}

func (pl *dashPlanner) list(per *Period, rep *Representation, base *url.URL, l *SegmentList) error {
	if l.Timescale == 0 {
		return errf(ErrMalformed, "@timescale is zero")
	}
	n := len(l.Segments)
	if n == 0 {
		return nil
	}
	if n > MaxSegments {
		return errf(ErrTooLarge, "more than %d media segments", MaxSegments)
	}
	const outside = time.Duration(-1) // an entry of another Period
	durations := make([]time.Duration, n)
	kept := n
	switch {
	case l.Timeline != nil:
		// Entry i takes the timing of timeline segment i; the entries the timeline places
		// outside the Period are left out.
		w, err := periodWindow(per.Duration, l.Timescale, l.PresentationTimeOffset)
		if err != nil {
			return err
		}
		for i := range durations {
			durations[i] = outside
		}
		total, end, err := timelineRuns(l.Timeline, w, func(index, _, d, count uint64) error {
			dur := unitsToDuration(d, l.Timescale)
			for c := uint64(0); c < count && index+c < uint64(n); c++ {
				durations[index+c] = dur
			}
			return nil
		})
		if err != nil {
			return err
		}
		// Entries beyond the timeline have no known timing. They follow its last
		// segment: inside the Period unless the timeline already reaches its end.
		if !w.hasEnd || end < w.end {
			for i := total; i < uint64(n); i++ {
				durations[i] = 0
			}
		}
		kept = 0
		for _, d := range durations {
			if d != outside {
				kept++
			}
		}
	case l.Duration > 0:
		d := unitsToDuration(l.Duration, l.Timescale)
		for i := range durations {
			durations[i] = d
		}
	case n == 1:
		durations[0] = per.Duration
	}
	if kept == 0 {
		return nil
	}
	if err := pl.reserve(uint64(kept)); err != nil {
		return err
	}
	pl.grow(uint64(kept) + 1)
	if err := pl.initFragment(base, rep.BaseURL, l.Init); err != nil {
		return err
	}
	for i := range l.Segments {
		if durations[i] == outside {
			continue
		}
		su := &l.Segments[i]
		u, err := pl.resolve(base, rep.BaseURL, su.Media)
		if err != nil {
			return withContext(err, "SegmentURL %d", i+1)
		}
		pl.add(Fragment{URL: u, ByteRange: copyRange(su.MediaRange), Duration: durations[i]})
	}
	return nil
}

func (pl *dashPlanner) template(per *Period, rep *Representation, base *url.URL, t *SegmentTemplate) error {
	if t.Timescale == 0 {
		return errf(ErrMalformed, "@timescale is zero")
	}
	media, uses, err := compileTemplate(t.Media)
	if err != nil {
		return withContext(err, "SegmentTemplate@media")
	}

	// Count the segments arithmetically before anything is allocated; @endNumber,
	// when given, bounds the segment numbers. span is the number of timeline positions
	// up to the last planned segment, which numbers it.
	limit := numberLimit(t)
	var n, span uint64
	var window mediaWindow
	if t.Timeline != nil {
		if window, err = periodWindow(per.Duration, t.Timescale, t.PresentationTimeOffset); err != nil {
			return err
		}
		_, _, err = timelineRuns(t.Timeline, window, func(index, _, _, count uint64) error {
			if index >= limit {
				return nil
			}
			count = min(count, limit-index)
			if count > uint64(MaxSegments)-n {
				return errf(ErrTooLarge, "SegmentTimeline expands to more than %d segments", MaxSegments)
			}
			n += count
			span = index + count
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		if t.Duration == 0 {
			return errf(ErrMalformed, "SegmentTemplate has neither @duration nor SegmentTimeline")
		}
		switch {
		case per.Duration > 0:
			units, _, err := periodEndUnits(per.Duration, t.Timescale, 0)
			if err != nil {
				return err
			}
			n = ceilDiv(units, t.Duration)
			n = min(n, limit)
		case limit != math.MaxUint64:
			n = limit // @endNumber numbers the segments of a Period of unknown length
		default:
			return errf(ErrMalformed, "Period duration is unknown, so SegmentTemplate@duration gives no segment count")
		}
		if n > uint64(MaxSegments) {
			return errf(ErrTooLarge, "SegmentTemplate expands to more than %d segments", MaxSegments)
		}
		if hi, lo := bits.Mul64(n, t.Duration); hi != 0 || lo > math.MaxUint64-t.PresentationTimeOffset {
			return errf(ErrMalformed, "segment times overflow")
		}
		span = n
	}
	switch {
	case n == 0:
		return nil
	case n > 1 && uses&(useNumber|useTime) == 0:
		return errf(ErrMalformed, "SegmentTemplate@media has neither $Number$ nor $Time$")
	case t.StartNumber > math.MaxUint64-span:
		return errf(ErrMalformed, "segment numbers overflow")
	}
	if err := pl.reserve(n); err != nil {
		return err
	}
	pl.grow(n + 1)

	vars := templateVars{id: rep.ID, bandwidth: rep.Bandwidth}
	switch {
	case t.Initialization != "":
		parts, iuses, err := compileTemplate(t.Initialization)
		if err != nil {
			return withContext(err, "SegmentTemplate@initialization")
		}
		if iuses&(useNumber|useTime) != 0 {
			return errf(ErrMalformed, "SegmentTemplate@initialization uses $Number$ or $Time$")
		}
		s, err := expandTemplate(parts, vars)
		if err != nil {
			return withContext(err, "SegmentTemplate@initialization")
		}
		u, err := pl.resolve(base, rep.BaseURL, s)
		if err != nil {
			return withContext(err, "SegmentTemplate@initialization")
		}
		pl.add(Fragment{URL: u, Init: true})
	case t.Init != nil:
		if err := pl.initFragment(base, rep.BaseURL, t.Init); err != nil {
			return err
		}
	}

	emit := func(number, tm uint64, dur time.Duration) error {
		v := vars
		v.number, v.time = number, tm
		s, err := expandTemplate(media, v)
		if err != nil {
			return withContext(err, "SegmentTemplate@media")
		}
		u, err := pl.resolve(base, rep.BaseURL, s)
		if err != nil {
			return withContext(err, "segment URL")
		}
		pl.add(Fragment{URL: u, Duration: dur})
		return nil
	}

	if t.Timeline != nil {
		var k uint64
		_, _, err := timelineRuns(t.Timeline, window, func(index, start, d, count uint64) error {
			if index >= limit {
				return nil
			}
			count = min(count, limit-index)
			dur := unitsToDuration(d, t.Timescale)
			for c := uint64(0); c < count && k < n; c++ {
				if err := emit(t.StartNumber+index+c, start+c*d, dur); err != nil {
					return err
				}
				k++
			}
			return nil
		})
		return err
	}
	segDur := unitsToDuration(t.Duration, t.Timescale)
	for k := uint64(0); k < n; k++ {
		dur := segDur
		if k == n-1 && segDur > 0 && k <= uint64(maxDuration/segDur) {
			// The last segment ends with the Period.
			if rest := per.Duration - time.Duration(k)*segDur; rest > 0 && rest < segDur {
				dur = rest
			}
		}
		if err := emit(t.StartNumber+k, t.PresentationTimeOffset+k*t.Duration, dur); err != nil {
			return err
		}
	}
	return nil
}

// grow reserves capacity for n more fragments; n is bounded by reserve. Capacity grows
// geometrically, so planning many Periods copies the fragments a bounded number of
// times.
func (pl *dashPlanner) grow(n uint64) {
	pl.plan.Fragments = slices.Grow(pl.plan.Fragments, int(n))
}

// numberLimit is the number of segments SegmentTemplate@endNumber allows, or
// MaxUint64 without one.
func numberLimit(t *SegmentTemplate) uint64 {
	switch {
	case t.EndNumber == 0:
		return math.MaxUint64
	case t.EndNumber < t.StartNumber:
		return 0
	case t.EndNumber-t.StartNumber == math.MaxUint64:
		return math.MaxUint64
	}
	return t.EndNumber - t.StartNumber + 1
}

// periodEndUnits converts a Period duration to the end of the Period on the media
// timeline in timescale units: presentationTimeOffset + ceil(duration × timescale).
// hasEnd is false when the duration is unknown.
func periodEndUnits(d time.Duration, timescale, pto uint64) (end uint64, hasEnd bool, err error) {
	if d <= 0 {
		return 0, false, nil
	}
	hi, lo := bits.Mul64(uint64(d), timescale)
	if hi >= uint64(time.Second) {
		return 0, false, errf(ErrMalformed, "Period end overflows the timescale")
	}
	q, r := bits.Div64(hi, lo, uint64(time.Second))
	if r != 0 {
		if q == math.MaxUint64 {
			return 0, false, errf(ErrMalformed, "Period end overflows the timescale")
		}
		q++
	}
	end, carry := bits.Add64(q, pto, 0)
	if carry != 0 {
		return 0, false, errf(ErrMalformed, "Period end overflows the timescale")
	}
	return end, true, nil
}

// unitsToDuration converts timescale units to a Duration, saturating.
func unitsToDuration(units, timescale uint64) time.Duration {
	if timescale == 0 {
		return 0
	}
	hi, lo := bits.Mul64(units, uint64(time.Second))
	if hi >= timescale {
		return maxDuration
	}
	q, _ := bits.Div64(hi, lo, timescale)
	if q > math.MaxInt64 {
		return maxDuration
	}
	return time.Duration(q)
}

// mediaWindow is the part of the media timeline that a Period presents, in timescale
// units: [start, end), from @presentationTimeOffset to presentationTimeOffset plus the
// Period duration (rounded up). hasEnd is false when the Period duration is unknown.
type mediaWindow struct {
	start, end uint64
	hasEnd     bool
}

func periodWindow(d time.Duration, timescale, pto uint64) (mediaWindow, error) {
	end, hasEnd, err := periodEndUnits(d, timescale, pto)
	return mediaWindow{start: pto, end: end, hasEnd: hasEnd}, err
}

// timelineRuns visits the segments of a SegmentTimeline (ISO/IEC 23009-1 §5.3.9.6) that
// lie in the Period's window w; a segment that ends at or before w.start, or starts at or
// after w.end, belongs to another Period. Each visit is a run of count segments of
// duration d, the first starting at start and being segment index of the timeline
// (counted from 0 with the segments outside the window, as $Number$ counts them).
//
// A missing @t continues the previous entry (0 for the first). A negative @r repeats
// until the next entry's @t or, for the last entry, the end of the window; a next entry
// without @t, or an unknown end, is malformed. Counts are computed arithmetically, so
// callers can bound them before iterating. timelineRuns returns the number of segments
// the timeline describes and the time at which its last entry ends.
func timelineRuns(tl []TimelineEntry, w mediaWindow, visit func(index, start, d, count uint64) error) (total, end uint64, err error) {
	var next, index uint64
	for i, e := range tl {
		if e.D == 0 {
			return 0, 0, errf(ErrMalformed, "SegmentTimeline S %d has no @d", i+1)
		}
		t := next
		if e.HasT {
			t = e.T
		}
		var count uint64
		if e.R >= 0 {
			count = uint64(e.R) + 1
		} else {
			stop, ok := w.end, w.hasEnd
			if i+1 < len(tl) {
				if !tl[i+1].HasT {
					return 0, 0, errf(ErrMalformed, "SegmentTimeline S %d repeats up to an S without @t", i+1)
				}
				stop, ok = tl[i+1].T, true
			}
			if !ok {
				return 0, 0, errf(ErrMalformed, "SegmentTimeline S %d repeats to an unknown Period end", i+1)
			}
			if stop > t {
				count = ceilDiv(stop-t, e.D)
			}
		}
		hi, length := bits.Mul64(count, e.D)
		if hi != 0 {
			return 0, 0, errf(ErrMalformed, "SegmentTimeline S %d overflows the media timeline", i+1)
		}
		after, carry := bits.Add64(t, length, 0)
		if carry != 0 {
			return 0, 0, errf(ErrMalformed, "SegmentTimeline S %d overflows the media timeline", i+1)
		}
		nextIndex, carry := bits.Add64(index, count, 0)
		if carry != 0 {
			return 0, 0, errf(ErrMalformed, "SegmentTimeline S %d overflows the segment numbers", i+1)
		}
		// Segment k of the run spans [t + k·d, t + (k+1)·d).
		first, last := uint64(0), count
		if w.start > t {
			first = min((w.start-t)/e.D, count)
		}
		if w.hasEnd {
			last = 0
			if w.end > t {
				last = min(ceilDiv(w.end-t, e.D), count)
			}
		}
		if first < last {
			if err := visit(index+first, t+first*e.D, e.D, last-first); err != nil {
				return 0, 0, err
			}
		}
		index, next = nextIndex, after
	}
	return index, next, nil
}

func ceilDiv(a, b uint64) uint64 {
	q := a / b
	if a%b != 0 {
		q++
	}
	return q
}

// Template identifiers (ISO/IEC 23009-1 §5.3.9.4.4, Table 16).
const (
	identLiteral = iota
	identRepresentationID
	identNumber
	identBandwidth
	identTime
)

const (
	useNumber = 1 << iota
	useTime
)

// maxTemplateWidth bounds the %0<width>d format width.
const maxTemplateWidth = 64

type templatePart struct {
	ident int
	lit   string
	width int
}

type templateVars struct {
	id           string
	bandwidth    int64
	number, time uint64
}

// compileTemplate parses a SegmentTemplate URL template: $$ is a literal $, and
// $<identifier>[%0<width>d]$ is substituted; the format tag is not allowed on
// $RepresentationID$.
func compileTemplate(s string) ([]templatePart, int, error) {
	var parts []templatePart
	uses := 0
	for len(s) > 0 {
		i := strings.IndexByte(s, '$')
		if i < 0 {
			parts = append(parts, templatePart{lit: s})
			break
		}
		if i > 0 {
			parts = append(parts, templatePart{lit: s[:i]})
		}
		j := strings.IndexByte(s[i+1:], '$')
		if j < 0 {
			return nil, 0, errf(ErrMalformed, "unpaired $ in template")
		}
		tok := s[i+1 : i+1+j]
		s = s[i+2+j:]
		if tok == "" {
			parts = append(parts, templatePart{lit: "$"})
			continue
		}
		name, format, hasFormat := strings.Cut(tok, "%")
		var p templatePart
		switch name {
		case "RepresentationID":
			p.ident = identRepresentationID
			if hasFormat {
				return nil, 0, errf(ErrMalformed, "$RepresentationID$ cannot have a format tag")
			}
		case "Number":
			p.ident, uses = identNumber, uses|useNumber
		case "Bandwidth":
			p.ident = identBandwidth
		case "Time":
			p.ident, uses = identTime, uses|useTime
		case "SubNumber":
			return nil, 0, errf(ErrUnsupported, "$SubNumber$ templates are not supported")
		default:
			return nil, 0, errf(ErrMalformed, "unknown template identifier")
		}
		if hasFormat {
			w, ok := strings.CutSuffix(format, "d")
			if !ok || (w != "" && (w[0] != '0' || (len(w) > 1 && !isDigits(w[1:])))) {
				return nil, 0, errf(ErrMalformed, "template format tag is not %%0<width>d")
			}
			if len(w) > 1 {
				width, err := strconv.Atoi(w[1:])
				if err != nil || width > maxTemplateWidth {
					return nil, 0, errf(ErrMalformed, "template width exceeds %d", maxTemplateWidth)
				}
				p.width = width
			}
		}
		parts = append(parts, p)
	}
	return parts, uses, nil
}

// expandTemplate substitutes vars, refusing results longer than MaxURIBytes before
// they are built.
func expandTemplate(parts []templatePart, v templateVars) (string, error) {
	var b strings.Builder
	var num [20]byte
	for _, p := range parts {
		var piece string
		switch p.ident {
		case identLiteral:
			piece = p.lit
		case identRepresentationID:
			piece = v.id
		case identNumber:
			piece = string(strconv.AppendUint(num[:0], v.number, 10))
		case identBandwidth:
			piece = string(strconv.AppendInt(num[:0], v.bandwidth, 10))
		case identTime:
			piece = string(strconv.AppendUint(num[:0], v.time, 10))
		}
		pad := 0
		if p.width > len(piece) {
			pad = p.width - len(piece)
		}
		if b.Len()+pad+len(piece) > MaxURIBytes {
			return "", errf(ErrTooLarge, "expanded template is longer than %d bytes", MaxURIBytes)
		}
		for ; pad > 0; pad-- {
			b.WriteByte('0')
		}
		b.WriteString(piece)
	}
	return b.String(), nil
}
