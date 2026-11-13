package locale

import (
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStyleNames(t *testing.T) {
	for s, want := range map[Style]string{Time: "time", Clock: "clock", MonthDay: "monthDay", Date: "date", YMD: "ymd", Full: "full", styleCount: "Style(6)"} {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}

func TestCheckTag(t *testing.T) {
	for _, tag := range []string{"en", "en-US", "de-DE", "ar", "ja", "zh-Hans-CN", "und", "en-u-nu-latn", "ar-001", "sr-Latn-RS"} {
		if err := checkTag(tag); err != nil {
			t.Errorf("checkTag(%q): %v", tag, err)
		}
	}
	for _, tag := range []string{"", "e", "abcd", "de_DE", "en--US", "en-", "1x", "en-toolongsubtag", "ü"} {
		if err := checkTag(tag); err == nil {
			t.Errorf("checkTag(%q) accepted", tag)
		}
	}
}

func TestBestAvailable(t *testing.T) {
	avail := map[string]bool{"en": true, "en-US": true, "en-GB": true, "de": true, "de-DE": true, "ar": true, "ar-IQ": true, "zh-Hans": true}
	has := func(s string) bool { return avail[s] }
	for in, want := range map[string]string{
		"en-US":      "en-US",
		"en-AU":      "en",
		"de-DE-1996": "de-DE",
		"ar-Arab-IQ": "ar",
		"zh-Hans-CN": "zh-Hans",
		"de-x-abc":   "de",
	} {
		got, ok := bestAvailable(in, has)
		if !ok || got != want {
			t.Errorf("bestAvailable(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	if got, ok := bestAvailable("xx-YY", has); ok {
		t.Errorf("bestAvailable(xx-YY) = %q", got)
	}
}

func TestCanonicalCaseAndExtensions(t *testing.T) {
	for in, want := range map[string]string{"EN-us": "en-US", "zh-hans-cn": "zh-Hans-CN", "ES-419": "es-419", "de-DE-1996": "de-DE-1996"} {
		if got := canonicalCase(in); got != want {
			t.Errorf("canonicalCase(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string][2]string{
		"ar":                 {"ar", ""},
		"ar-u-nu-arab":       {"ar", "-u-nu-arab"},
		"en-US-u-hc-h23-x-a": {"en-US", "-u-hc-h23-x-a"},
	} {
		if base, ext := splitExtensions(in); base != want[0] || ext != want[1] {
			t.Errorf("splitExtensions(%q) = %q %q", in, base, ext)
		}
	}
	if !hasUnicodeKey("-u-nu-arab", "nu") || hasUnicodeKey("-u-hc-h23", "nu") || hasUnicodeKey("-x-nu", "nu") {
		t.Error("hasUnicodeKey is wrong")
	}
	for in, want := range map[string]string{"": "-u-nu-latn", "-u-hc-h23": "-u-nu-latn-hc-h23", "-x-priv": "-u-nu-latn-x-priv"} {
		if got := withLatinDigits(in); got != want {
			t.Errorf("withLatinDigits(%q) = %q, want %q", in, got, want)
		}
	}
	if !latnByDefault("ar") || !latnByDefault("ar-001") || latnByDefault("ar-IQ") || latnByDefault("en") {
		t.Error("latnByDefault is wrong")
	}
}

func TestHourCycleAndSpace(t *testing.T) {
	for in, want := range map[string]string{"h:mm a": "H:mm a", "HH:mm": "HH:mm", "K:mm 'h' k": "H:mm 'h' H", "d MMM y, HH:mm:ss": "d MMM y, HH:mm:ss"} {
		if got := withHourCycle(in); got != want {
			t.Errorf("withHourCycle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := spaceBeforeDayPeriod("11:59\u202fAM"); got != "11:59 AM" {
		t.Errorf("spaceBeforeDayPeriod = %q", got)
	}
}

func TestTimeClip(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want float64
		ok   bool
	}{{1.9, 1, true}, {-1.9, -1, true}, {MaxTime, MaxTime, true}, {MaxTime + 1, 0, false}, {math.NaN(), 0, false}, {math.Inf(1), 0, false}} {
		got, ok := timeClip(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("timeClip(%v) = %v %v", c.in, got, ok)
		}
	}
}

func TestFallbackNumber(t *testing.T) {
	for _, c := range []struct {
		v      float64
		digits int
		want   string
	}{
		{0, 0, "0"}, {1234567, 0, "1,234,567"}, {12.5, 0, "13"}, {0.5, 0, "1"}, {-5, 0, "-5"}, {1e21, 0, "1,000,000,000,000,000,000,000"},
		{32, 1, "32.0"}, {99.95, 1, "100.0"}, {1.05, 1, "1.1"}, {math.Copysign(0, -1), 0, "-0"}, {math.NaN(), 0, "NaN"},
		{math.Inf(1), 1, "∞"}, {math.Inf(-1), 0, "-∞"}, {999.5, 0, "1,000"}, {0.04, 1, "0.0"},
	} {
		if got := fallbackNumber(c.v, c.digits); got != c.want {
			t.Errorf("fallbackNumber(%v, %d) = %q, want %q", c.v, c.digits, got, c.want)
		}
	}
}

func TestFallbackDates(t *testing.T) {
	f, err := NewFallback("en-US", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	ms := float64(time.Date(2026, 10, 20, 9, 5, 7, 0, time.UTC).UnixMilli())
	for s, want := range map[Style]string{Time: "09:05", Clock: "9:05 AM", MonthDay: "Oct 20", Date: "Oct 20, 2026", YMD: "10/20/2026", Full: "Oct 20, 2026, 09:05:07"} {
		if got := f.Date(ms, s); got != want {
			t.Errorf("fallback %s = %q, want %q", s, got, want)
		}
	}
	if got := f.Year(ms); got != "2026" {
		t.Errorf("fallback Year = %q", got)
	}
	if got := f.Date(math.NaN(), Time); got != "" {
		t.Errorf("fallback Date(NaN) = %q", got)
	}
	if _, err := NewFallback("en-US", "Not/AZone"); err == nil {
		t.Error("NewFallback accepted an unknown zone")
	}
}

// TestSystemMatchesTheFallbackInEnglish checks the two formatters against each other where
// they must agree: en-US in UTC.
func TestSystemMatchesTheFallbackInEnglish(t *testing.T) {
	sys, err := New("en-US", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	fb, _ := NewFallback("en-US", "UTC")
	if _, isFallback := sys.(*fallbackFormatter); isFallback {
		t.Skipf("no system formatter on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	for _, v := range []float64{0, 1, 999.5, 1234567.891, -42.25, 1e21, 0.05, 99.95, math.NaN(), math.Inf(1)} {
		for _, d := range []int{0, 1, 2} {
			if a, b := sys.Number(v, d), fb.Number(v, d); a != b {
				t.Errorf("Number(%v, %d): system %q, fallback %q", v, d, a, b)
			}
		}
	}
	for _, ms := range []float64{1, 1700000004000, 1792508400000, 4102444799000, -62198755200000} {
		for s := Time; s < styleCount; s++ {
			b := fb.Date(ms, s)
			if runtime.GOOS == "darwin" && s == Full {
				// WebKit on macOS (its ICU's en-US pattern) joins the time with " at "
				// ("Oct 20, 2026 at 09:05:07"), where Blink writes ", "
				i := strings.LastIndex(b, ", ")
				b = b[:i] + " at " + b[i+2:]
			}
			if a := sys.Date(ms, s); a != b {
				t.Errorf("Date(%v, %s): system %q, fallback %q", ms, s, a, b)
			}
		}
		if a, b := sys.Year(ms), fb.Year(ms); a != b {
			t.Errorf("Year(%v): system %q, fallback %q", ms, a, b)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	if _, err := New("de_DE", ""); err == nil {
		t.Error("New accepted de_DE")
	}
	if _, err := New("en-US", "Mars/Olympus"); err == nil {
		t.Error("New accepted an unknown zone")
	}
	f, err := New("en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Tag() != "en-US" || f.TimeZone() != "" {
		t.Errorf("Tag %q TimeZone %q", f.Tag(), f.TimeZone())
	}
	if got := f.Date(MaxTime*2, Full); got != "" {
		t.Errorf("Date out of range = %q", got)
	}
}

func TestUserLocale(t *testing.T) {
	tag := UserLocale()
	if err := checkTag(tag); err != nil {
		t.Errorf("UserLocale() = %q: %v", tag, err)
	}
	if _, err := New(tag, ""); err != nil {
		t.Errorf("New(UserLocale() = %q): %v", tag, err)
	}
}

func TestConcurrentUse(t *testing.T) {
	f, err := New("de-DE", "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 8)
	for i := range 8 {
		go func() {
			s := ""
			for j := range 200 {
				s = f.Number(float64(i*1000+j)+0.25, 1) + " " + f.Date(1792508400000+float64(j)*86400000, Full)
			}
			done <- s
		}()
	}
	for range 8 {
		if s := <-done; s == "" {
			t.Error("empty result")
		}
	}
}
