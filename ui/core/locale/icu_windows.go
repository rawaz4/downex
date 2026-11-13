//go:build windows && (amd64 || 386)

package locale

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The ICU C API of Windows' icu.dll (Windows 10 1903 and later), from the Windows SDK's
// icu.h. Every function returns its failure in a UErrorCode: > 0 is an error, < 0 a warning.

var (
	modICU = windows.NewLazySystemDLL("icu.dll")

	procULocForLanguageTag            = modICU.NewProc("uloc_forLanguageTag")
	procULocToLanguageTag             = modICU.NewProc("uloc_toLanguageTag")
	procULocCountAvailable            = modICU.NewProc("uloc_countAvailable")
	procULocGetAvailable              = modICU.NewProc("uloc_getAvailable")
	procUCalGetCanonicalTimeZoneID    = modICU.NewProc("ucal_getCanonicalTimeZoneID")
	procUCalClone                     = modICU.NewProc("ucal_clone")
	procUCalClose                     = modICU.NewProc("ucal_close")
	procUCalGetType                   = modICU.NewProc("ucal_getType")
	procUCalSetGregorianChange        = modICU.NewProc("ucal_setGregorianChange")
	procUDatPGOpen                    = modICU.NewProc("udatpg_open")
	procUDatPGClose                   = modICU.NewProc("udatpg_close")
	procUDatPGGetBestPatternWithOpts  = modICU.NewProc("udatpg_getBestPatternWithOptions")
	procUDatOpen                      = modICU.NewProc("udat_open")
	procUDatClose                     = modICU.NewProc("udat_close")
	procUDatFormat                    = modICU.NewProc("udat_format")
	procUDatFormatForFields           = modICU.NewProc("udat_formatForFields")
	procUDatGetCalendar               = modICU.NewProc("udat_getCalendar")
	procUDatSetCalendar               = modICU.NewProc("udat_setCalendar")
	procUFieldPosIterOpen             = modICU.NewProc("ufieldpositer_open")
	procUFieldPosIterClose            = modICU.NewProc("ufieldpositer_close")
	procUFieldPosIterNext             = modICU.NewProc("ufieldpositer_next")
	procUNumFOpenForSkeletonAndLocale = modICU.NewProc("unumf_openForSkeletonAndLocale")
	procUNumFOpenResult               = modICU.NewProc("unumf_openResult")
	procUNumFFormatDecimal            = modICU.NewProc("unumf_formatDecimal")
	procUNumFResultToString           = modICU.NewProc("unumf_resultToString")
	procUNumFClose                    = modICU.NewProc("unumf_close")
	procUNumFCloseResult              = modICU.NewProc("unumf_closeResult")
)

const (
	uBufferOverflowError = 15 // U_BUFFER_OVERFLOW_ERROR

	// udatPattern is UDAT_PATTERN (-2) as an int32 argument: the pattern decides the format.
	udatPattern                = uintptr(0xfffffffe)
	udatYearField              = 1    // UDAT_YEAR_FIELD
	udatExtendedYearField      = 20   // UDAT_EXTENDED_YEAR_FIELD
	udatpgMatchHourFieldLength = 2048 // UDATPG_MATCH_HOUR_FIELD_LENGTH = 1 << UDATPG_HOUR_FIELD

	// startOfTime is where V8 moves the Gregorian change of a Gregorian calendar: -(2^53) ms,
	// so every date is proleptic Gregorian as ECMAScript's are.
	startOfTime = -9007199254740992.0
)

// icuAvailable loads icu.dll and finds every function once.
var icuAvailable = sync.OnceValue(func() error {
	if err := modICU.Load(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	for _, p := range []*windows.LazyProc{
		procULocForLanguageTag, procULocToLanguageTag, procULocCountAvailable, procULocGetAvailable, procUCalGetCanonicalTimeZoneID, procUCalClone, procUCalClose,
		procUCalGetType, procUCalSetGregorianChange, procUDatPGOpen, procUDatPGClose,
		procUDatPGGetBestPatternWithOpts, procUDatOpen, procUDatClose, procUDatFormat,
		procUDatFormatForFields, procUDatGetCalendar, procUDatSetCalendar, procUFieldPosIterOpen,
		procUFieldPosIterClose, procUFieldPosIterNext, procUNumFOpenForSkeletonAndLocale,
		procUNumFOpenResult, procUNumFFormatDecimal, procUNumFResultToString, procUNumFClose,
		procUNumFCloseResult,
	} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
	}
	return nil
})

// icuError is a failed ICU call.
type icuError struct {
	call string
	code int32
}

func (e *icuError) Error() string {
	return fmt.Sprintf("locale: %s failed (UErrorCode %d)", e.call, e.code)
}

func failed(st int32) bool { return st > 0 }

// ret reads an int32_t return value.
func ret(r uintptr) int32 { return int32(uint32(r)) }

// callUDate calls an ICU function whose second argument is a UDate (double): one 64-bit word
// on amd64 (the callee reads the XMM register Go's call loads alongside it), two 32-bit stack
// words on 386. Pointers converted to uintptr in the call expression stay put and alive until
// it returns.
//
//go:uintptrescapes
func callUDate(p *windows.LazyProc, first uintptr, ms float64, rest ...uintptr) uintptr {
	bits := math.Float64bits(ms)
	args := make([]uintptr, 0, 3+len(rest))
	args = append(args, first)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		args = append(args, uintptr(bits))
	} else {
		args = append(args, uintptr(uint32(bits)), uintptr(uint32(bits>>32)))
	}
	args = append(args, rest...)
	r, _, _ := p.Call(args...)
	return r
}

// cPointer reads a const char* ICU returned (memory ICU owns, never Go's).
func cPointer(p uintptr) *byte { return *(**byte)(unsafe.Pointer(&p)) }

// call calls an ICU function. Arguments that are pointers converted to uintptr in the call
// expression stay where they are and alive until it returns (as for syscall.Syscall).
//
//go:uintptrescapes
func call(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

// utf16z is s as a NUL-terminated UTF-16 string.
func utf16z(s string) []uint16 { return append(utf16.Encode([]rune(s)), 0) }

// cstr is s as a NUL-terminated byte string.
func cstr(s string) []byte { return append([]byte(s), 0) }

// readUTF16 calls fill with a buffer until it fits, as ICU's preflighting asks: fill returns
// the length ICU needed; a U_BUFFER_OVERFLOW_ERROR asks again with that length.
func readUTF16(name string, fill func(buf []uint16, st *int32) int32) (string, error) {
	var small [128]uint16
	buf := small[:]
	for range 2 {
		var st int32
		n := fill(buf, &st)
		if st == uBufferOverflowError && int(n) > len(buf) {
			buf = make([]uint16, n+1)
			continue
		}
		if failed(st) {
			return "", &icuError{name, st}
		}
		if n < 0 || int(n) > len(buf) {
			return "", &icuError{name, -1}
		}
		return string(utf16.Decode(buf[:n])), nil
	}
	return "", &icuError{name, uBufferOverflowError}
}

// icuFormatter is a Formatter over ICU. Its handles are made on first use and freed when
// the formatter is unreachable; mu guards them (ICU's date formats are not thread-safe).
type icuFormatter struct {
	tag, zone string
	h         *icuHandles
}

type icuHandles struct {
	mu      sync.Mutex
	locale  []byte   // the ICU locale id, NUL-terminated
	zone    []uint16 // the zone, NUL-terminated; nil for the system's
	numbers map[int]uintptr
	result  uintptr // UFormattedNumber*
	dtpg    uintptr // UDateTimePatternGenerator*
	dates   [styleCount]uintptr
	fields  uintptr // UFieldPositionIterator*
}

func newSystem(tag, timeZone string) (Formatter, error) {
	if err := icuAvailable(); err != nil {
		return nil, err
	}
	loc, err := resolveICULocale(tag)
	if err != nil {
		return nil, err
	}
	h := &icuHandles{locale: cstr(loc), numbers: map[int]uintptr{}}
	if timeZone != "" {
		if err := checkZone(timeZone); err != nil {
			return nil, err
		}
		h.zone = utf16z(timeZone)
	}
	f := &icuFormatter{tag: tag, zone: timeZone, h: h}
	runtime.AddCleanup(f, func(h *icuHandles) { h.close() }, h)
	return f, nil
}

// icuLocale turns a BCP 47 tag into an ICU locale id ("de-DE" → "de_DE").
func icuLocale(tag string) (string, error) {
	in := cstr(tag)
	var out [160]byte
	var parsed int32
	var st int32
	n := ret(call(procULocForLanguageTag, uintptr(unsafe.Pointer(&in[0])), uintptr(unsafe.Pointer(&out[0])),
		uintptr(len(out)), uintptr(unsafe.Pointer(&parsed)), uintptr(unsafe.Pointer(&st))))
	if failed(st) || n < 0 || int(n) >= len(out) {
		return "", fmt.Errorf("locale: invalid language tag %q: %w", tag, &icuError{"uloc_forLanguageTag", st})
	}
	if int(parsed) != len(tag) {
		return "", fmt.Errorf("locale: invalid language tag %q", tag)
	}
	return string(out[:n]), nil
}

// availableLocales is the set of BCP 47 tags ICU has locale data for.
var availableLocales = sync.OnceValue(func() map[string]bool {
	set := map[string]bool{}
	n := int(ret(call(procULocCountAvailable)))
	for i := range n {
		id := call(procULocGetAvailable, uintptr(i))
		if id == 0 {
			continue
		}
		in := cstr(windows.BytePtrToString(cPointer(id)))
		var out [160]byte
		var st int32
		m := ret(call(procULocToLanguageTag, uintptr(unsafe.Pointer(&in[0])), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), 1,
			uintptr(unsafe.Pointer(&st))))
		if !failed(st) && m > 0 && int(m) < len(out) {
			set[string(out[:m])] = true
		}
	}
	return set
})

// resolveICULocale resolves a tag as V8 does before it makes its ICU formatters: the best
// available locale for it (else the user's locale, else en-US), its extensions kept; and
// Latin digits where CLDR 48 made them the default (latnByDefault).
func resolveICULocale(tag string) (string, error) {
	base, ext := splitExtensions(tag)
	avail := availableLocales()
	has := func(t string) bool { return avail[t] }
	resolved, ok := bestAvailable(canonicalCase(base), has)
	if !ok {
		user, _ := splitExtensions(UserLocale())
		if resolved, ok = bestAvailable(canonicalCase(user), has); !ok {
			resolved = "en-US"
		}
	}
	if latnByDefault(resolved) && !hasUnicodeKey(ext, "nu") {
		ext = withLatinDigits(ext)
	}
	return icuLocale(resolved + ext)
}

// checkZone refuses a zone ICU does not know (Intl's "Invalid time zone specified").
func checkZone(zone string) error {
	id := utf16z(zone)
	var system int8
	_, err := readUTF16("ucal_getCanonicalTimeZoneID", func(buf []uint16, st *int32) int32 {
		return ret(call(procUCalGetCanonicalTimeZoneID, uintptr(unsafe.Pointer(&id[0])), uintptr(len(id)-1),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&system)), uintptr(unsafe.Pointer(st))))
	})
	if err != nil {
		return fmt.Errorf("locale: invalid time zone %q: %w", zone, err)
	}
	return nil
}

func (h *icuHandles) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for d, nf := range h.numbers {
		call(procUNumFClose, nf)
		delete(h.numbers, d)
	}
	if h.result != 0 {
		call(procUNumFCloseResult, h.result)
		h.result = 0
	}
	for i, df := range h.dates {
		if df != 0 {
			call(procUDatClose, df)
			h.dates[i] = 0
		}
	}
	if h.dtpg != 0 {
		call(procUDatPGClose, h.dtpg)
		h.dtpg = 0
	}
	if h.fields != 0 {
		call(procUFieldPosIterClose, h.fields)
		h.fields = 0
	}
}

func (f *icuFormatter) Tag() string      { return f.tag }
func (f *icuFormatter) TimeZone() string { return f.zone }

func (f *icuFormatter) Number(v float64, digits int) string {
	digits = max(0, min(digits, 20))
	s, err := f.h.number(v, digits)
	if err != nil {
		return fallbackNumber(v, digits)
	}
	return s
}

func (f *icuFormatter) Date(ms float64, style Style) string {
	t, ok := timeClip(ms)
	if !ok || style >= styleCount {
		return ""
	}
	s, err := f.h.date(t, style)
	if err != nil {
		return ""
	}
	return s
}

func (f *icuFormatter) Year(ms float64) string {
	t, ok := timeClip(ms)
	if !ok {
		return ""
	}
	s, err := f.h.year(t)
	if err != nil {
		return ""
	}
	return s
}

// decimal writes v as the shortest decimal that reads back as v, the digits ICU's
// formatDouble starts from; NaN and the infinities in the spelling unumf_formatDecimal reads.
func decimal(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func (h *icuHandles) number(v float64, digits int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	nf, err := h.numberFormat(digits)
	if err != nil {
		return "", err
	}
	if h.result == 0 {
		var st int32
		r := call(procUNumFOpenResult, uintptr(unsafe.Pointer(&st)))
		if failed(st) || r == 0 {
			return "", &icuError{"unumf_openResult", st}
		}
		h.result = r
	}
	in := cstr(decimal(v))
	var st int32
	call(procUNumFFormatDecimal, nf, uintptr(unsafe.Pointer(&in[0])), uintptr(len(in)-1), h.result, uintptr(unsafe.Pointer(&st)))
	if failed(st) {
		return "", &icuError{"unumf_formatDecimal", st}
	}
	return readUTF16("unumf_resultToString", func(buf []uint16, st *int32) int32 {
		return ret(call(procUNumFResultToString, h.result, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(st))))
	})
}

// numberFormat is the number formatter for a fraction length: V8's Intl.NumberFormat with
// minimumFractionDigits = maximumFractionDigits = digits (precision, half-expand rounding,
// the locale's grouping, numbering system and signs).
func (h *icuHandles) numberFormat(digits int) (uintptr, error) {
	if nf, ok := h.numbers[digits]; ok {
		return nf, nil
	}
	precision := "precision-integer"
	if digits > 0 {
		precision = "." + zeros(digits)
	}
	skel := utf16z(precision + " rounding-mode-half-up")
	var st int32
	nf := call(procUNumFOpenForSkeletonAndLocale, uintptr(unsafe.Pointer(&skel[0])), uintptr(len(skel)-1),
		uintptr(unsafe.Pointer(&h.locale[0])), uintptr(unsafe.Pointer(&st)))
	if failed(st) || nf == 0 {
		return 0, &icuError{"unumf_openForSkeletonAndLocale", st}
	}
	h.numbers[digits] = nf
	return nf, nil
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

// dateFormat is the date format of a style, made as V8 makes Intl.DateTimeFormat's: the
// locale's best pattern for the style's skeleton (hour fields as long as asked), the hour
// cycle of the options, the zone, and a proleptic Gregorian calendar.
func (h *icuHandles) dateFormat(style Style) (uintptr, error) {
	if df := h.dates[style]; df != 0 {
		return df, nil
	}
	if h.dtpg == 0 {
		var st int32
		g := call(procUDatPGOpen, uintptr(unsafe.Pointer(&h.locale[0])), uintptr(unsafe.Pointer(&st)))
		if failed(st) || g == 0 {
			return 0, &icuError{"udatpg_open", st}
		}
		h.dtpg = g
	}
	skeleton, h23 := style.skeleton()
	skel := utf16z(skeleton)
	pattern, err := readUTF16("udatpg_getBestPatternWithOptions", func(buf []uint16, st *int32) int32 {
		return ret(call(procUDatPGGetBestPatternWithOpts, h.dtpg, uintptr(unsafe.Pointer(&skel[0])), uintptr(len(skel)-1),
			udatpgMatchHourFieldLength, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(st))))
	})
	if err != nil {
		return 0, err
	}
	if h23 {
		pattern = withHourCycle(pattern)
	}
	pat := utf16z(pattern)
	var zone *uint16 // nil: the system's zone
	zoneLen := 0
	if h.zone != nil {
		zone, zoneLen = &h.zone[0], len(h.zone)-1
	}
	var st int32
	df := call(procUDatOpen, udatPattern, udatPattern, uintptr(unsafe.Pointer(&h.locale[0])),
		uintptr(unsafe.Pointer(zone)), uintptr(zoneLen), uintptr(unsafe.Pointer(&pat[0])), uintptr(len(pat)-1), uintptr(unsafe.Pointer(&st)))
	if failed(st) || df == 0 {
		return 0, &icuError{"udat_open", st}
	}
	if err := prolepticGregorian(df); err != nil {
		call(procUDatClose, df)
		return 0, err
	}
	h.dates[style] = df
	return df, nil
}

// prolepticGregorian moves the Gregorian change of a Gregorian format's calendar to the start
// of ECMAScript time, as V8 does; other calendars are left as they are.
func prolepticGregorian(df uintptr) error {
	var st int32
	cal := call(procUCalClone, call(procUDatGetCalendar, df), uintptr(unsafe.Pointer(&st)))
	if failed(st) || cal == 0 {
		return &icuError{"ucal_clone", st}
	}
	defer call(procUCalClose, cal)
	st = 0
	typ := call(procUCalGetType, cal, uintptr(unsafe.Pointer(&st)))
	if failed(st) || typ == 0 || windows.BytePtrToString(cPointer(typ)) != "gregorian" {
		return nil
	}
	st = 0
	callUDate(procUCalSetGregorianChange, cal, startOfTime, uintptr(unsafe.Pointer(&st)))
	if failed(st) {
		return &icuError{"ucal_setGregorianChange", st}
	}
	call(procUDatSetCalendar, df, cal) // the format keeps its own copy
	return nil
}

func (h *icuHandles) date(ms float64, style Style) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	df, err := h.dateFormat(style)
	if err != nil {
		return "", err
	}
	s, err := readUTF16("udat_format", func(buf []uint16, st *int32) int32 {
		return ret(callUDate(procUDatFormat, df, ms, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, uintptr(unsafe.Pointer(st))))
	})
	if err != nil {
		return "", err
	}
	return spaceBeforeDayPeriod(s), nil
}

// year is the first year part of the YMD format (Intl's formatToParts type "year": ICU's
// year or extended year field).
func (h *icuHandles) year(ms float64) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	df, err := h.dateFormat(YMD)
	if err != nil {
		return "", err
	}
	if h.fields == 0 {
		var st int32
		it := call(procUFieldPosIterOpen, uintptr(unsafe.Pointer(&st)))
		if failed(st) || it == 0 {
			return "", &icuError{"ufieldpositer_open", st}
		}
		h.fields = it
	}
	var units []uint16
	_, err = readUTF16("udat_formatForFields", func(buf []uint16, st *int32) int32 {
		n := ret(callUDate(procUDatFormatForFields, df, ms, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), h.fields, uintptr(unsafe.Pointer(st))))
		if *st <= 0 && n >= 0 && int(n) <= len(buf) {
			units = append(units[:0], buf[:n]...)
		}
		return n
	})
	if err != nil {
		return "", err
	}
	year := ""
	found := false
	for {
		var begin, end int32
		field := ret(call(procUFieldPosIterNext, h.fields, uintptr(unsafe.Pointer(&begin)), uintptr(unsafe.Pointer(&end))))
		if field < 0 {
			break
		}
		if !found && (field == udatYearField || field == udatExtendedYearField) && begin >= 0 && end >= begin && int(end) <= len(units) {
			year = string(utf16.Decode(units[begin:end]))
			found = true
		}
	}
	if !found {
		return "", errors.New("locale: no year in the date")
	}
	return spaceBeforeDayPeriod(year), nil
}

// ---- language names (Intl.DisplayNames) ----

var (
	procULdnOpen              = modICU.NewProc("uldn_open")
	procULdnClose             = modICU.NewProc("uldn_close")
	procULdnLocaleDisplayName = modICU.NewProc("uldn_localeDisplayName")
)

// englishNames is ICU's English locale display names (uldn_open("en", ULDN_STANDARD_NAMES));
// 0 when ICU cannot give them. It lives as long as the process.
var englishNames = sync.OnceValue(func() uintptr {
	if icuAvailable() != nil {
		return 0
	}
	for _, p := range []*windows.LazyProc{procULdnOpen, procULdnClose, procULdnLocaleDisplayName} {
		if p.Find() != nil {
			return 0
		}
	}
	en := cstr("en")
	var st int32
	ldn := call(procULdnOpen, uintptr(unsafe.Pointer(&en[0])), 0, uintptr(unsafe.Pointer(&st)))
	if failed(st) {
		return 0
	}
	return ldn
})

var namesMu sync.Mutex

func systemLanguageName(id string) (string, bool) {
	ldn := englishNames()
	if ldn == 0 {
		return "", false
	}
	in := cstr(id)
	namesMu.Lock()
	defer namesMu.Unlock()
	name, err := readUTF16("uldn_localeDisplayName", func(buf []uint16, st *int32) int32 {
		return ret(call(procULdnLocaleDisplayName, ldn, uintptr(unsafe.Pointer(&in[0])), uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)), uintptr(unsafe.Pointer(st))))
	})
	if err != nil || name == "" {
		return "", false
	}
	return name, true
}
