package format_test

import (
	"math"
	"regexp"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
)

// The tables of ui/frontend/src/lib/format.test.ts, case for case.

func setLocale(t *testing.T, tag, zone string) {
	t.Helper()
	f, err := locale.New(tag, zone)
	if err != nil {
		t.Fatalf("locale.New(%q, %q): %v", tag, zone, err)
	}
	prev := format.Locale()
	format.SetLocale(f)
	t.Cleanup(func() { format.SetLocale(prev) })
}

func enUS(t *testing.T) { setLocale(t, "en-US", "UTC") }

func utc(y int, mo time.Month, d, h, mi, s int) float64 {
	return float64(time.Date(y, mo, d, h, mi, s, 0, time.UTC).UnixMilli())
}

var nan = math.NaN()

func TestBytes(t *testing.T) {
	enUS(t)
	for _, c := range []struct {
		bytes float64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{999, "999 B"},
		{1000, "1.0 KB"},
		{1024, "1.0 KB"},
		{1500, "1.5 KB"},
		{10 * MB, "10.0 MB"},
		{10 * MiB, "10.5 MB"},
		{99.94 * MB, "99.9 MB"},
		{150 * MB, "150 MB"},
		// 630 MiB
		{660_602_880, "661 MB"},
		{1_429_658_896, "1.4 GB"},
		{5 * GB, "5.0 GB"},
		{2 * 1000 * GB, "2.0 TB"},
	} {
		if got := format.Bytes(c.bytes); got != c.want {
			t.Errorf("Bytes(%v) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

func TestBytesStepsAt1000AndPromotes(t *testing.T) {
	enUS(t)
	for in, want := range map[float64]string{999_999: "1.0 MB", 999_499: "999 KB", 999_500: "1.0 MB", 999_999_999: "1.0 GB", 1_000_000: "1.0 MB"} {
		if got := format.Bytes(in); got != want {
			t.Errorf("Bytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBytesDropsTheDecimalAt100(t *testing.T) {
	enUS(t)
	for in, want := range map[float64]string{99_950: "100 KB", 99_949: "99.9 KB", 99_950_000: "100 MB"} {
		if got := format.Bytes(in); got != want {
			t.Errorf("Bytes(%v) = %q, want %q", in, got, want)
		}
	}
	if got := format.Speed(99_950); got != "100 KB/s" {
		t.Errorf("Speed(99950) = %q", got)
	}
}

func TestBytesNeverBinary(t *testing.T) {
	enUS(t)
	binary := regexp.MustCompile(`iB`)
	for _, b := range []float64{999, 1000, 1024, MiB, 630 * MiB, 3 * 1024 * MiB, 1e13} {
		if s := format.Bytes(b); binary.MatchString(s) {
			t.Errorf("Bytes(%v) = %q", b, s)
		}
		if s := format.Speed(b); binary.MatchString(s) {
			t.Errorf("Speed(%v) = %q", b, s)
		}
	}
}

func TestBytesUnknown(t *testing.T) {
	enUS(t)
	if got := format.Bytes(-1); got != "—" {
		t.Errorf("Bytes(-1) = %q", got)
	}
	if got := format.Bytes(nan); got != "—" {
		t.Errorf("Bytes(NaN) = %q", got)
	}
}

func TestSpeed(t *testing.T) {
	enUS(t)
	for _, c := range []struct {
		bps  float64
		want string
	}{
		{0, "0 B/s"}, {-5, "0 B/s"}, {999, "999 B/s"}, {1000, "1.0 KB/s"}, {2048, "2.0 KB/s"},
		{12.34 * MB, "12.3 MB/s"},
		// 9.67 MiB/s
		{10_139_730, "10.1 MB/s"},
		{999_999, "1.0 MB/s"},
	} {
		if got := format.Speed(c.bps); got != c.want {
			t.Errorf("Speed(%v) = %q, want %q", c.bps, got, c.want)
		}
	}
}

func TestEta(t *testing.T) {
	for _, c := range []struct {
		s    float64
		want string
	}{
		{nan, "—"}, {-1, "—"}, {math.Inf(1), "∞"}, {0, "<1s"}, {0.4, "1s"}, {45, "45s"}, {185, "3m 05s"},
		{7620, "2h 07m"}, {3*86400 + 4*3600, "3d 4h"}, {200 * 86400, ">99d"},
	} {
		if got := format.Eta(c.s); got != c.want {
			t.Errorf("Eta(%v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func with(d *downexv1.Download, edit func(*downexv1.Download)) *downexv1.Download {
	c := proto.Clone(d).(*downexv1.Download)
	edit(c)
	return c
}

const (
	downloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	paused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	completed   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	site        = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
)

func isNaN(t *testing.T, what string, v float64) {
	t.Helper()
	if !math.IsNaN(v) {
		t.Errorf("%s = %v, want NaN", what, v)
	}
}

func equal(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func close9(t *testing.T, what string, got, want float64, digits int) {
	t.Helper()
	// vitest's toBeCloseTo(want, digits): |got - want| < 10^-digits / 2
	if !(math.Abs(got-want) < math.Pow(10, -float64(digits))/2) {
		t.Errorf("%s = %v, want ≈ %v", what, got, want)
	}
}

func TestEtaSecondsOnlyWhileDownloadingWithAKnownSize(t *testing.T) {
	d := Download(1, func(d *downexv1.Download) { d.State = downloading; d.Size = 1000; d.Received = 400; d.SpeedBps = 100 })
	equal(t, "eta", format.EtaSeconds(d), 6)
	isNaN(t, "paused", format.EtaSeconds(with(d, func(d *downexv1.Download) { d.State = paused })))
	isNaN(t, "size -1", format.EtaSeconds(with(d, func(d *downexv1.Download) { d.Size = -1 })))
	isNaN(t, "speed 0", format.EtaSeconds(with(d, func(d *downexv1.Download) { d.SpeedBps = 0 })))
}

func TestEtaSecondsOfAStream(t *testing.T) {
	d := MediaDownload(1, func(d *downexv1.Download) {
		d.State = downloading
		d.Size = 300 * MiB
		d.SizeApprox = true
		d.Received = 100 * MiB
		d.SpeedBps = 10 * MiB
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 100 })
	})
	equal(t, "estimate", format.EtaSeconds(d), 20)
	// 100 MiB were half the fragments.
	equal(t, "fragments", format.EtaSeconds(with(d, func(d *downexv1.Download) { d.Size = -1; d.SizeApprox = false })), 10)
	// An estimate already passed: the fragments decide.
	equal(t, "passed", format.EtaSeconds(with(d, func(d *downexv1.Download) { d.Size = 90 * MiB })), 10)
}

func TestASitesVideoIsMeasuredByItsBytes(t *testing.T) {
	// Paused at 4 of 37 parts (10 MiB video chunks, 2 MiB audio ones): 10.8% by parts,
	// 16.5% by the bytes kept.
	s := MediaDownload(1, func(d *downexv1.Download) {
		d.State = paused
		d.Size = 316_000_000
		d.SizeApprox = false
		d.Received = 52_000_000
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) {
			m.Origin = site
			m.Format = "http"
			m.ManifestUrl = ""
			m.FragmentsTotal = 37
			m.FragmentsDone = 4
		})
	})
	bytes := 52_000_000.0 / 316_000_000
	close9(t, "paused", format.ProgressFraction(s), bytes, 9)
	close9(t, "downloading", format.ProgressFraction(with(s, func(d *downexv1.Download) { d.State = downloading })), bytes, 9)
	// Also when the size is the site's estimate, and indeterminate while it is unknown.
	close9(t, "estimate", format.ProgressFraction(with(s, func(d *downexv1.Download) { d.SizeApprox = true })), bytes, 9)
	isNaN(t, "unknown size", format.ProgressFraction(with(s, func(d *downexv1.Download) { d.Size = -1 })))
	isNaN(t, "eta unknown size", format.EtaSeconds(with(s, func(d *downexv1.Download) { d.State = downloading; d.SpeedBps = 1_000_000; d.Size = -1 })))
	close9(t, "eta", format.EtaSeconds(with(s, func(d *downexv1.Download) { d.State = downloading; d.SpeedBps = 1_000_000 })), 264, 6)
	// A site's stream (HLS or DASH through yt-dlp) is measured like any stream.
	stream := with(s, func(d *downexv1.Download) {
		d.SizeApprox = true
		d.Media = MediaInfo(func(m *downexv1.MediaInfo) {
			m.Origin = site
			m.Format = "hls"
			m.FragmentsTotal = 40
			m.FragmentsDone = 10
		})
	})
	equal(t, "stream", format.ProgressFraction(stream), 0.25)
}

func TestSizeMarksEstimates(t *testing.T) {
	enUS(t)
	for _, c := range []struct {
		size   int64
		approx bool
		want   string
	}{
		{1500, false, "1.5 KB"}, {2100 * MB, true, "~2.1 GB"}, {1_429_658_896, true, "~1.4 GB"}, {-1, false, "—"}, {-1, true, "—"},
	} {
		d := Download(1, func(d *downexv1.Download) { d.Size = c.size; d.SizeApprox = c.approx })
		if got := format.Size(d); got != c.want {
			t.Errorf("Size(%d, approx %v) = %q, want %q", c.size, c.approx, got, c.want)
		}
	}
}

func TestProgressFraction(t *testing.T) {
	equal(t, "completed", format.ProgressFraction(Download(1, func(d *downexv1.Download) { d.State = completed; d.Size = -1 })), 1)
	isNaN(t, "unknown", format.ProgressFraction(Download(1, func(d *downexv1.Download) { d.Size = -1; d.Received = 50 })))
	equal(t, "empty", format.ProgressFraction(Download(1, func(d *downexv1.Download) { d.Size = 0 })), 0)
	equal(t, "quarter", format.ProgressFraction(Download(1, func(d *downexv1.Download) { d.Size = 200; d.Received = 50 })), 0.25)
	equal(t, "over", format.ProgressFraction(Download(1, func(d *downexv1.Download) { d.Size = 200; d.Received = 500 })), 1)
}

func TestPercentFloors(t *testing.T) {
	for in, want := range map[float64]string{0.999: "99%", 1: "100%", 0: "0%"} {
		if got := format.Percent(in); got != want {
			t.Errorf("Percent(%v) = %q, want %q", in, got, want)
		}
	}
	if got := format.Percent(nan); got != "" {
		t.Errorf("Percent(NaN) = %q", got)
	}
}

func TestDates(t *testing.T) {
	enUS(t)
	now := utc(2026, 10, 20, 15, 0, 0)
	for _, c := range []struct {
		ms   float64
		want string
	}{
		{utc(2026, 10, 20, 9, 5, 0), "09:05"},
		{utc(2026, 10, 3, 23, 59, 0), "Oct 3"},
		{utc(2024, 3, 1, 12, 0, 0), "Mar 1, 2024"},
		{0, "—"},
	} {
		if got := format.Date(c.ms, now); got != c.want {
			t.Errorf("Date(%v) = %q, want %q", c.ms, got, c.want)
		}
	}
	want := "Oct 20, 2026, 09:05:07"
	if macWebKit {
		// today's Mac window (WebKit's Intl over macOS's ICU) joins the time with " at "
		want = "Oct 20, 2026 at 09:05:07"
	}
	if got := format.DateTime(utc(2026, 10, 20, 9, 5, 7)); got != want {
		t.Errorf("DateTime = %q, want %q", got, want)
	}
	if got := format.DateTime(0); got != "—" {
		t.Errorf("DateTime(0) = %q", got)
	}
}

func TestRates(t *testing.T) {
	if got := format.RateUnits(); len(got) != 2 || got[0] != format.KBps || got[1] != format.MBps {
		t.Errorf("RateUnits = %v", got)
	}
	type rc struct {
		value  string
		unit   format.RateUnit
		loaded float64
		want   int64 // -1: null
	}
	cases := []rc{
		{"", format.MBps, 0, 0}, {" 2 ", format.KBps, 0, 2000}, {"1.5", format.MBps, 0, 1_500_000}, {"1,5", format.MBps, 0, 1_500_000},
		{"abc", format.MBps, 0, -1}, {"-1", format.MBps, 0, -1}, {"1e3", format.MBps, 0, -1},
		// a typed unit: K, M, G decimal, KiB, MiB, GiB binary
		{"500K", format.MBps, 0, 500_000}, {"500 KB", format.MBps, 0, 500_000}, {"500 kb/s", format.MBps, 0, 500_000},
		{"2M", format.KBps, 0, 2_000_000}, {"2 MB/s", format.KBps, 0, 2_000_000}, {"1G", format.KBps, 0, 1_000_000_000},
		{"1 GB", format.KBps, 0, 1_000_000_000}, {"500 KiB", format.MBps, 0, 500 * KiB}, {"1.5 MiB/s", format.KBps, 0, 1.5 * MiB},
		{"1 GiB", format.KBps, 0, 1024 * MiB}, {"300 B/s", format.MBps, 0, 300}, {"5 Mi", format.MBps, 0, -1},
		{"5 X", format.MBps, 0, -1}, {"MB", format.MBps, 0, -1},
		// a speed above 0 that rounds to 0 bytes per second is refused (0 is no limit)
		{"0.4 B/s", format.MBps, 0, -1}, {"0.0001", format.KBps, 0, -1}, {"0", format.KBps, 0, 0}, {"0.5 B/s", format.MBps, 0, 1},
	}
	for _, c := range cases {
		got, ok := format.ParseRate(c.value, c.unit, c.loaded)
		if c.want < 0 {
			if ok {
				t.Errorf("ParseRate(%q, %s) = %d, want null", c.value, c.unit, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("ParseRate(%q, %s) = %d, %v, want %d", c.value, c.unit, got, ok, c.want)
		}
	}
}

func TestSplitRate(t *testing.T) {
	for _, c := range []struct {
		bps   float64
		value string
		unit  format.RateUnit
	}{
		{0, "", format.MBps}, {2 * MB, "2", format.MBps}, {1.5 * MB, "1.5", format.MBps}, {1500, "1.5", format.KBps},
		// never "1000" KB/s: MB/s once the KB/s text would round to 1000
		{999_999, "1", format.MBps}, {999_995, "1", format.MBps}, {999_994, "999.99", format.KBps},
		// a limit set in MiB/s before shows rounded
		{10 * MiB, "10.49", format.MBps}, {512 * KiB, "524.29", format.KBps},
	} {
		value, unit := format.SplitRate(c.bps)
		if value != c.value || unit != c.unit {
			t.Errorf("SplitRate(%v) = %q %s, want %q %s", c.bps, value, unit, c.value, c.unit)
		}
	}
}

func TestRatesRoundTrip(t *testing.T) {
	for _, b := range []float64{512 * 1000, 3 * MB, 2.25 * MB, 100 * 1000, 1230} {
		value, unit := format.SplitRate(b)
		if got, ok := format.ParseRate(value, unit, 0); !ok || float64(got) != b {
			t.Errorf("round trip of %v: %q %s → %d %v", b, value, unit, got, ok)
		}
	}
}

func TestLoadedLimitKeptWhileUnchanged(t *testing.T) {
	for _, b := range []float64{10 * MiB, 512 * KiB, MiB + 1, 999_999} {
		value, unit := format.SplitRate(b)
		if got, _ := format.ParseRate(value, unit, 0); float64(got) == b {
			t.Errorf("ParseRate(%q, %s) = %d, want another value", value, unit, got)
		}
		if got, ok := format.ParseRate(value, unit, b); !ok || float64(got) != b {
			t.Errorf("ParseRate(%q, %s, %v) = %d %v", value, unit, b, got, ok)
		}
		// edited: what is typed
		want := int64(10 * 1000)
		if unit == format.MBps {
			want = 10 * MB
		}
		if got, ok := format.ParseRate("10", unit, b); !ok || got != want {
			t.Errorf("ParseRate(10, %s, %v) = %d %v, want %d", unit, b, got, ok, want)
		}
	}
	// the same number in the other unit is an edit
	if got, ok := format.ParseRate("10.49", format.KBps, 10*MiB); !ok || got != 10_490 {
		t.Errorf("ParseRate(10.49, KB/s, 10 MiB) = %d %v", got, ok)
	}
}

func TestCountsAndHosts(t *testing.T) {
	enUS(t)
	if got := format.Count(10000); got != "10,000" {
		t.Errorf("Count = %q", got)
	}
	if got := format.HostOf("https://cdn.example.net:8443/a"); got != "cdn.example.net:8443" {
		t.Errorf("HostOf = %q", got)
	}
	if got := format.HostOf("not a url"); got != "not a url" {
		t.Errorf("HostOf = %q", got)
	}
}

func TestClockOnTheLocalesClock(t *testing.T) {
	enUS(t)
	at := utc(2026, 10, 28, 11, 59, 30)
	space := regexp.MustCompile(`^11:59\sAM$`)
	if got := format.Clock(at); !space.MatchString(got) {
		t.Errorf("Clock = %q", got)
	}
	if got := format.Clock(utc(2026, 10, 28, 23, 5, 0)); !regexp.MustCompile(`^11:05\sPM$`).MatchString(got) {
		t.Errorf("Clock = %q", got)
	}
	t.Run("en-GB", func(t *testing.T) {
		setLocale(t, "en-GB", "UTC")
		if got := format.Clock(at); got != "11:59" {
			t.Errorf("Clock = %q", got)
		}
		if got := format.Clock(utc(2026, 10, 28, 23, 5, 0)); got != "23:05" {
			t.Errorf("Clock = %q", got)
		}
	})
}

func TestClockFollowsTheZone(t *testing.T) {
	t.Run("de-DE", func(t *testing.T) {
		setLocale(t, "de-DE", "Europe/Berlin")
		if got := format.Clock(utc(2026, 10, 28, 11, 59, 30)); got != "12:59" {
			t.Errorf("Clock = %q", got)
		}
	})
	enUS(t)
	for _, v := range []float64{0, -5, nan, math.Inf(1)} {
		if got := format.Clock(v); got != "—" {
			t.Errorf("Clock(%v) = %q", v, got)
		}
	}
}

func TestDriveLabel(t *testing.T) {
	for _, c := range [][2]string{
		{`D:\Downloads\Video`, "D:"},
		{`c:\Users\you\Downloads`, "C:"},
		{"E:/media", "E:"},
		{"D:", "D:"},
		{`\\nas\share\downloads`, "Network folder"},
		{"//nas/share", "Network folder"},
		{"/home/you/Downloads", "Downloads"},
		{"/home/you/Downloads/", "Downloads"},
		{"/", "/"},
		{"", ""},
		{"   ", ""},
	} {
		if got := format.DriveLabel(c[0]); got != c[1] {
			t.Errorf("DriveLabel(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// IsDefault: true until SetLocale chooses another formatter, and again once the default is
// put back (a test's cleanup).
func TestIsDefaultLocale(t *testing.T) {
	prev := format.Locale()
	if !format.IsDefault() {
		t.Skip("another test left a locale set")
	}
	f, err := locale.New("en-GB", "Asia/Baghdad")
	if err != nil {
		t.Skip(err)
	}
	format.SetLocale(f)
	if format.IsDefault() {
		t.Error("IsDefault after SetLocale(en-GB)")
	}
	format.SetLocale(prev)
	if !format.IsDefault() {
		t.Error("not default once the default formatter is put back")
	}
}
