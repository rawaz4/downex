package pageurl

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for raw, want := range map[string]string{
		"https://pages.example/files?id=7#top": "https://pages.example/files?id=7#top",
		"HTTP://pages.example/a":               "http://pages.example/a",
		"http://pages.example":                 "http://pages.example",
	} {
		if got, err := Check(raw); err != nil || got != want {
			t.Errorf("Check(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "file:///C:/Windows/System32/calc.exe", "shell:startup", "ms-settings:privacy", "javascript:alert(1)",
		"https://u:p@pages.example/", "https:///path", "https://pages.example/a\nb", "https://pages.example/a b",
		"ftp://pages.example/", "//pages.example/", "https://pages.example/" + strings.Repeat("a", MaxBytes),
		"mailto:a@example.test", "http:opaque",
	} {
		if got, err := Check(raw); err == nil {
			t.Errorf("Check(%q) = %q, want ErrNotPage", raw, got)
		}
	}
}
