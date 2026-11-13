//go:build darwin && cgo

package locale

// The macOS backend: the system's ICU (libicucore, which WebKit's JavaScriptCore formats
// Intl with in today's Mac window) through the same steps JavaScriptCore's
// Intl.NumberFormat, Intl.DateTimeFormat and Intl.DisplayNames take. Core Foundation's
// CFDateFormatter and CFNumberFormatter sit on the same ICU but add steps of their own (user
// overrides, their own template matching), so the formats are made with ICU directly, as
// JavaScriptCore makes them; the macOS SDK has no headers for these ICU functions, so their
// C declarations are below (ICU's stable C API, unversioned names in libicucore).

/*
#cgo LDFLAGS: -licucore
#include <stdint.h>
#include <stdlib.h>

typedef uint16_t UChar;
typedef int32_t UErrorCode;
typedef int8_t UBool;
typedef double UDate;

int32_t uloc_forLanguageTag(const char *langtag, char *localeID, int32_t localeIDCapacity, int32_t *parsedLength, UErrorCode *err);
int32_t uloc_toLanguageTag(const char *localeID, char *langtag, int32_t langtagCapacity, UBool strict, UErrorCode *err);
int32_t uloc_canonicalize(const char *localeID, char *name, int32_t nameCapacity, UErrorCode *err);

// canonicalForm is Apple's ualoc_canonicalForm (it knows the aliases uloc_canonicalize
// leaves), looked up when first used: older systems than the SDK's may not have it, and
// then uloc_canonicalize stands in.
#include <dlfcn.h>
#include <pthread.h>
typedef int32_t (*canonicalFormFn)(const char *, char *, int32_t, UErrorCode *);
static canonicalFormFn canonicalFormPtr;
static pthread_once_t canonicalFormOnce = PTHREAD_ONCE_INIT;
static void findCanonicalForm(void) { canonicalFormPtr = (canonicalFormFn)dlsym(RTLD_DEFAULT, "ualoc_canonicalForm"); }
static int32_t canonicalForm(const char *localeID, char *canonical, int32_t capacity, UErrorCode *err) {
	pthread_once(&canonicalFormOnce, findCanonicalForm);
	return canonicalFormPtr ? canonicalFormPtr(localeID, canonical, capacity, err) : uloc_canonicalize(localeID, canonical, capacity, err);
}
int32_t uloc_countAvailable(void);
const char *uloc_getAvailable(int32_t n);

int32_t ucal_getCanonicalTimeZoneID(const UChar *id, int32_t len, UChar *result, int32_t resultCapacity, UBool *isSystemID, UErrorCode *status);
void *ucal_openTimeZoneIDEnumeration(int32_t zoneType, const char *region, const int32_t *rawOffset, UErrorCode *ec);
const char *uenum_next(void *en, int32_t *resultLength, UErrorCode *status);
void uenum_close(void *en);
void *ucal_clone(const void *cal, UErrorCode *status);
void ucal_close(void *cal);
const char *ucal_getType(const void *cal, UErrorCode *status);
void ucal_setGregorianChange(void *cal, UDate date, UErrorCode *pErrorCode);

void *udatpg_open(const char *locale, UErrorCode *pErrorCode);
void udatpg_close(void *dtpg);
int32_t udatpg_getBestPatternWithOptions(void *dtpg, const UChar *skeleton, int32_t length, int32_t options, UChar *bestPattern, int32_t capacity, UErrorCode *pErrorCode);

void *udat_open(int32_t timeStyle, int32_t dateStyle, const char *locale, const UChar *tzID, int32_t tzIDLength, const UChar *pattern, int32_t patternLength, UErrorCode *status);
void udat_close(void *format);
int32_t udat_format(const void *format, UDate dateToFormat, UChar *result, int32_t resultLength, void *position, UErrorCode *status);
int32_t udat_formatForFields(const void *format, UDate dateToFormat, UChar *result, int32_t resultLength, void *fpositer, UErrorCode *status);
const void *udat_getCalendar(const void *fmt);
void udat_setCalendar(void *fmt, const void *calendarToSet);

void *ufieldpositer_open(UErrorCode *status);
void ufieldpositer_close(void *fpositer);
int32_t ufieldpositer_next(void *fpositer, int32_t *beginIndex, int32_t *endIndex);

void *unumf_openForSkeletonAndLocale(const UChar *skeleton, int32_t skeletonLen, const char *locale, UErrorCode *ec);
void *unumf_openResult(UErrorCode *ec);
void unumf_formatDouble(const void *uformatter, double value, void *uresult, UErrorCode *ec);
int32_t unumf_resultToString(const void *uresult, UChar *buffer, int32_t bufferCapacity, UErrorCode *ec);
void unumf_close(void *uformatter);
void unumf_closeResult(void *uresult);

void *uldn_openForContext(const char *locale, int32_t *contexts, int32_t length, UErrorCode *pErrorCode);
int32_t uldn_localeDisplayName(const void *ldn, const char *locale, UChar *result, int32_t maxResultSize, UErrorCode *pErrorCode);
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"
)

const (
	uBufferOverflowError = 15 // U_BUFFER_OVERFLOW_ERROR

	udatPattern                = -2   // UDAT_PATTERN: the pattern decides the format
	udatYearField              = 1    // UDAT_YEAR_FIELD
	udatExtendedYearField      = 20   // UDAT_EXTENDED_YEAR_FIELD
	udatpgMatchHourFieldLength = 2048 // UDATPG_MATCH_HOUR_FIELD_LENGTH = 1 << UDATPG_HOUR_FIELD
	ucalZoneTypeAny            = 0    // UCAL_ZONE_TYPE_ANY

	// The UDisplayContext values JavaScriptCore opens its language names with
	// (languageDisplay 'dialect', style 'long', fallback by the caller).
	udispctxDialectNames = 1     // UDISPCTX_DIALECT_NAMES
	udispctxLengthFull   = 0x200 // UDISPCTX_LENGTH_FULL
	udispctxNoSubstitute = 0x301 // UDISPCTX_NO_SUBSTITUTE

	// startOfTime is where JavaScriptCore moves the Gregorian change of a Gregorian
	// calendar: the start of ECMAScript time, so every date is proleptic Gregorian.
	startOfTime = -MaxTime
)

// icuError is a failed ICU call.
type icuError struct {
	call string
	code int32
}

func (e *icuError) Error() string {
	return fmt.Sprintf("locale: %s failed (UErrorCode %d)", e.call, e.code)
}

func failed(st C.UErrorCode) bool { return st > 0 }

// uchars is s as UTF-16 in C memory, NUL-terminated; the caller frees it.
func uchars(s string) (*C.UChar, C.int32_t) {
	u := utf16.Encode([]rune(s))
	p := (*C.UChar)(C.malloc(C.size_t(2 * (len(u) + 1))))
	buf := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)+1)
	copy(buf, u)
	buf[len(u)] = 0
	return p, C.int32_t(len(u))
}

// readUChars calls fill with a C buffer until it fits, as ICU's preflighting asks: fill
// returns the length ICU needed; a U_BUFFER_OVERFLOW_ERROR asks again with that length.
func readUChars(name string, fill func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t) (string, error) {
	size := 128
	for range 2 {
		buf := (*C.UChar)(C.malloc(C.size_t(2 * size)))
		var st C.UErrorCode
		n := int(fill(buf, C.int32_t(size), &st))
		if st == uBufferOverflowError && n > size {
			C.free(unsafe.Pointer(buf))
			size = n + 1
			continue
		}
		if failed(st) || n < 0 || n > size {
			C.free(unsafe.Pointer(buf))
			if !failed(st) {
				st = -1
			}
			return "", &icuError{name, int32(st)}
		}
		s := string(utf16.Decode(unsafe.Slice((*uint16)(unsafe.Pointer(buf)), n)))
		C.free(unsafe.Pointer(buf))
		return s, nil
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
	locale  *C.char  // the ICU locale id
	zone    *C.UChar // the zone's ICU id, NUL-terminated; nil for the system's
	zoneLen C.int32_t
	numbers map[int]unsafe.Pointer
	result  unsafe.Pointer // UFormattedNumber*
	dtpg    unsafe.Pointer // UDateTimePatternGenerator*
	dates   [styleCount]unsafe.Pointer
	fields  unsafe.Pointer // UFieldPositionIterator*
}

func newSystem(tag, timeZone string) (Formatter, error) {
	loc, err := resolveICULocale(tag)
	if err != nil {
		return nil, err
	}
	h := &icuHandles{numbers: map[int]unsafe.Pointer{}}
	if timeZone != "" {
		id, err := zoneID(timeZone)
		if err != nil {
			return nil, err
		}
		h.zone, h.zoneLen = uchars(id)
	}
	h.locale = C.CString(loc)
	f := &icuFormatter{tag: tag, zone: timeZone, h: h}
	runtime.AddCleanup(f, func(h *icuHandles) { h.close() }, h)
	return f, nil
}

// icuLocale turns a BCP 47 tag into an ICU locale id ("de-DE" → "de_DE").
func icuLocale(tag string) (string, error) {
	in := C.CString(tag)
	defer C.free(unsafe.Pointer(in))
	var out [160]C.char
	var parsed C.int32_t
	var st C.UErrorCode
	n := int(C.uloc_forLanguageTag(in, &out[0], C.int32_t(len(out)), &parsed, &st))
	if failed(st) || n < 0 || n >= len(out) {
		return "", fmt.Errorf("locale: invalid language tag %q: %w", tag, &icuError{"uloc_forLanguageTag", int32(st)})
	}
	if int(parsed) != len(tag) {
		return "", fmt.Errorf("locale: invalid language tag %q", tag)
	}
	return C.GoStringN(&out[0], C.int(n)), nil
}

// languageTag turns an ICU locale id back into a BCP 47 tag ("de_DE" → "de-DE").
func languageTag(id string) (string, bool) {
	in := C.CString(id)
	defer C.free(unsafe.Pointer(in))
	var out [160]C.char
	var st C.UErrorCode
	n := int(C.uloc_toLanguageTag(in, &out[0], C.int32_t(len(out)), 1, &st))
	if failed(st) || n <= 0 || n >= len(out) {
		return "", false
	}
	return C.GoStringN(&out[0], C.int(n)), true
}

// availableLocales is the set of BCP 47 tags ICU has locale data for.
var availableLocales = sync.OnceValue(func() map[string]bool {
	set := map[string]bool{}
	n := int(C.uloc_countAvailable())
	for i := range n {
		id := C.uloc_getAvailable(C.int32_t(i))
		if id == nil {
			continue
		}
		if tag, ok := languageTag(C.GoString(id)); ok {
			set[tag] = true
			if short, ok := scriptless(tag); ok {
				set[short] = true
			}
		}
	}
	return set
})

// scriptless is a language-script-region tag without its script ("zh-Hant-TW" → "zh-TW"),
// which JavaScriptCore counts as available too (addScriptlessLocaleIfNeeded).
func scriptless(tag string) (string, bool) {
	if len(tag) < 10 {
		return "", false
	}
	parts := strings.Split(tag, "-")
	if len(parts) != 3 || len(parts[1]) != 4 || len(parts[2]) > 3 {
		return "", false
	}
	return parts[0] + "-" + parts[2], true
}

// resolveICULocale resolves a tag as JavaScriptCore does before it makes its ICU formatters:
// the best available locale for it (else the user's locale, else en-US), its extensions
// kept. The digits are the locale's own in this ICU (no latnByDefault: WebKit formats with
// the system's ICU data, whose CLDR already has Latin digits for ar where it is new enough).
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
	return icuLocale(resolved + ext)
}

// zoneIDs is every zone ICU knows, by its lower-case name: JavaScriptCore matches a
// timeZone option without regard to case ("asia/baghdad").
var zoneIDs = sync.OnceValue(func() map[string]string {
	ids := map[string]string{}
	var st C.UErrorCode
	en := C.ucal_openTimeZoneIDEnumeration(ucalZoneTypeAny, nil, nil, &st)
	if failed(st) || en == nil {
		return ids
	}
	defer C.uenum_close(en)
	for {
		var n C.int32_t
		st = 0
		p := C.uenum_next(en, &n, &st)
		if failed(st) || p == nil {
			break
		}
		id := C.GoStringN(p, C.int(n))
		ids[strings.ToLower(id)] = id
	}
	return ids
})

// zoneID is the ICU zone a timeZone option names, as JavaScriptCore accepts it (any case);
// an unknown zone is an error (Intl's RangeError).
func zoneID(zone string) (string, error) {
	id := zone
	if !knownZone(id) {
		var ok bool
		if id, ok = zoneIDs()[strings.ToLower(zone)]; !ok || !knownZone(id) {
			return "", fmt.Errorf("locale: invalid time zone %q", zone)
		}
	}
	return id, nil
}

func knownZone(id string) bool {
	in, n := uchars(id)
	defer C.free(unsafe.Pointer(in))
	var system C.UBool
	_, err := readUChars("ucal_getCanonicalTimeZoneID", func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t {
		return C.ucal_getCanonicalTimeZoneID(in, n, buf, capacity, &system, st)
	})
	return err == nil && system != 0
}

func (h *icuHandles) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for d, nf := range h.numbers {
		C.unumf_close(nf)
		delete(h.numbers, d)
	}
	if h.result != nil {
		C.unumf_closeResult(h.result)
		h.result = nil
	}
	for i, df := range h.dates {
		if df != nil {
			C.udat_close(df)
			h.dates[i] = nil
		}
	}
	if h.dtpg != nil {
		C.udatpg_close(h.dtpg)
		h.dtpg = nil
	}
	if h.fields != nil {
		C.ufieldpositer_close(h.fields)
		h.fields = nil
	}
	if h.zone != nil {
		C.free(unsafe.Pointer(h.zone))
		h.zone = nil
	}
	if h.locale != nil {
		C.free(unsafe.Pointer(h.locale))
		h.locale = nil
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

func (h *icuHandles) number(v float64, digits int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	nf, err := h.numberFormat(digits)
	if err != nil {
		return "", err
	}
	if h.result == nil {
		var st C.UErrorCode
		r := C.unumf_openResult(&st)
		if failed(st) || r == nil {
			return "", &icuError{"unumf_openResult", int32(st)}
		}
		h.result = r
	}
	var st C.UErrorCode
	C.unumf_formatDouble(nf, C.double(v), h.result, &st)
	if failed(st) {
		return "", &icuError{"unumf_formatDouble", int32(st)}
	}
	return readUChars("unumf_resultToString", func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t {
		return C.unumf_resultToString(h.result, buf, capacity, st)
	})
}

// numberFormat is the number formatter for a fraction length: JavaScriptCore's
// Intl.NumberFormat with minimumFractionDigits = maximumFractionDigits = digits (precision,
// half-expand rounding, the locale's grouping, numbering system and signs).
func (h *icuHandles) numberFormat(digits int) (unsafe.Pointer, error) {
	if nf, ok := h.numbers[digits]; ok {
		return nf, nil
	}
	precision := "precision-integer"
	if digits > 0 {
		precision = "." + strings.Repeat("0", digits)
	}
	skel, n := uchars(precision + " rounding-mode-half-up")
	defer C.free(unsafe.Pointer(skel))
	var st C.UErrorCode
	nf := C.unumf_openForSkeletonAndLocale(skel, n, h.locale, &st)
	if failed(st) || nf == nil {
		return nil, &icuError{"unumf_openForSkeletonAndLocale", int32(st)}
	}
	h.numbers[digits] = nf
	return nf, nil
}

// dateFormat is the date format of a style, made as JavaScriptCore makes
// Intl.DateTimeFormat's: the locale's best pattern for the style's skeleton (hour fields as
// long as asked), the hour cycle of the options, the zone, and a proleptic Gregorian
// calendar.
func (h *icuHandles) dateFormat(style Style) (unsafe.Pointer, error) {
	if df := h.dates[style]; df != nil {
		return df, nil
	}
	if h.dtpg == nil {
		var st C.UErrorCode
		g := C.udatpg_open(h.locale, &st)
		if failed(st) || g == nil {
			return nil, &icuError{"udatpg_open", int32(st)}
		}
		h.dtpg = g
	}
	skeleton, h23 := style.skeleton()
	skel, n := uchars(skeleton)
	defer C.free(unsafe.Pointer(skel))
	pattern, err := readUChars("udatpg_getBestPatternWithOptions", func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t {
		return C.udatpg_getBestPatternWithOptions(h.dtpg, skel, n, udatpgMatchHourFieldLength, buf, capacity, st)
	})
	if err != nil {
		return nil, err
	}
	if h23 {
		pattern = withHourCycle(pattern)
	}
	pat, patLen := uchars(pattern)
	defer C.free(unsafe.Pointer(pat))
	var st C.UErrorCode
	df := C.udat_open(udatPattern, udatPattern, h.locale, h.zone, h.zoneLen, pat, patLen, &st)
	if failed(st) || df == nil {
		return nil, &icuError{"udat_open", int32(st)}
	}
	if err := prolepticGregorian(df); err != nil {
		C.udat_close(df)
		return nil, err
	}
	h.dates[style] = df
	return df, nil
}

// prolepticGregorian moves the Gregorian change of a Gregorian format's calendar to the start
// of ECMAScript time, as JavaScriptCore does; other calendars are left as they are.
func prolepticGregorian(df unsafe.Pointer) error {
	var st C.UErrorCode
	cal := C.ucal_clone(C.udat_getCalendar(df), &st)
	if failed(st) || cal == nil {
		return &icuError{"ucal_clone", int32(st)}
	}
	defer C.ucal_close(cal)
	st = 0
	typ := C.ucal_getType(cal, &st)
	if failed(st) || typ == nil || C.GoString(typ) != "gregorian" {
		return nil
	}
	st = 0
	C.ucal_setGregorianChange(cal, startOfTime, &st)
	if failed(st) {
		return &icuError{"ucal_setGregorianChange", int32(st)}
	}
	C.udat_setCalendar(df, cal) // the format keeps its own copy
	return nil
}

func (h *icuHandles) date(ms float64, style Style) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	df, err := h.dateFormat(style)
	if err != nil {
		return "", err
	}
	s, err := readUChars("udat_format", func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t {
		return C.udat_format(df, C.UDate(ms), buf, capacity, nil, st)
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
	if h.fields == nil {
		var st C.UErrorCode
		it := C.ufieldpositer_open(&st)
		if failed(st) || it == nil {
			return "", &icuError{"ufieldpositer_open", int32(st)}
		}
		h.fields = it
	}
	var units []uint16
	_, err = readUChars("udat_formatForFields", func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t {
		n := C.udat_formatForFields(df, C.UDate(ms), buf, capacity, h.fields, st)
		if *st <= 0 && n >= 0 && n <= capacity {
			units = append(units[:0], unsafe.Slice((*uint16)(unsafe.Pointer(buf)), int(n))...)
		}
		return n
	})
	if err != nil {
		return "", err
	}
	year := ""
	found := false
	for {
		var begin, end C.int32_t
		field := C.ufieldpositer_next(h.fields, &begin, &end)
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

// englishNames is ICU's English locale display names as JavaScriptCore opens them for
// Intl.DisplayNames(['en'], {type: 'language'}): dialect names ("British English"), full
// length, no substitute; nil when ICU cannot give them. It lives as long as the process.
var englishNames = sync.OnceValue(func() unsafe.Pointer {
	en := C.CString("en")
	defer C.free(unsafe.Pointer(en))
	contexts := (*C.int32_t)(C.malloc(3 * 4))
	defer C.free(unsafe.Pointer(contexts))
	copy(unsafe.Slice((*int32)(unsafe.Pointer(contexts)), 3), []int32{udispctxDialectNames, udispctxLengthFull, udispctxNoSubstitute})
	var st C.UErrorCode
	ldn := C.uldn_openForContext(en, contexts, 3, &st)
	if failed(st) {
		return nil
	}
	return ldn
})

var namesMu sync.Mutex

// systemLanguageName names an ICU locale id as JavaScriptCore's DisplayNames.of does: the
// locale canonicalized (aliases replaced: "sh" → "sr-Latn", "tl" → "fil") and named by its
// BCP 47 tag ("und" is "Unknown language", not ICU's root), else (no name in ICU) that
// canonical tag, as fallback 'code' gives it.
func systemLanguageName(id string) (string, bool) {
	ldn := englishNames()
	if ldn == nil {
		return "", false
	}
	tag, ok := canonicalTag(id)
	if !ok {
		return "", false
	}
	in := C.CString(tag)
	defer C.free(unsafe.Pointer(in))
	namesMu.Lock()
	name, err := readUChars("uldn_localeDisplayName", func(buf *C.UChar, capacity C.int32_t, st *C.UErrorCode) C.int32_t {
		return C.uldn_localeDisplayName(ldn, in, buf, capacity, st)
	})
	namesMu.Unlock()
	if err != nil || name == "" {
		return tag, true
	}
	return name, true
}

// canonicalTag is the canonical form of a locale id as a BCP 47 tag ("sh" → "sr-Latn"),
// from Apple's ualoc_canonicalForm, which JavaScriptCore canonicalizes with (it knows the
// aliases uloc_canonicalize leaves: "tl" → "fil").
func canonicalTag(id string) (string, bool) {
	in := C.CString(id)
	defer C.free(unsafe.Pointer(in))
	var out [160]C.char
	var st C.UErrorCode
	n := int(C.canonicalForm(in, &out[0], C.int32_t(len(out)), &st))
	if failed(st) || n < 0 || n >= len(out) {
		return "", false
	}
	return languageTag(C.GoStringN(&out[0], C.int(n)))
}
