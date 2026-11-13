package format_test

import (
	"testing"

	"downex/ui/core/format"
	"downex/ui/core/locale"
)

func benchLocale(b *testing.B) {
	f, err := locale.New("de-DE", "Europe/Berlin")
	if err != nil {
		b.Fatal(err)
	}
	prev := format.Locale()
	format.SetLocale(f)
	b.Cleanup(func() { format.SetLocale(prev) })
}

func BenchmarkBytes(b *testing.B) {
	benchLocale(b)
	b.ReportAllocs()
	for i := range b.N {
		_ = format.Bytes(float64(1_429_658_896 + i))
	}
}

func BenchmarkDate(b *testing.B) {
	benchLocale(b)
	b.ReportAllocs()
	for i := range b.N {
		_ = format.Date(1792508400000-float64(i%40)*86_400_000, 1792508400000)
	}
}

func BenchmarkEta(b *testing.B) {
	b.ReportAllocs()
	for i := range b.N {
		_ = format.Eta(float64(i % 100000))
	}
}
