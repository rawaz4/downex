//go:build darwin && cgo

package locale

import "testing"

// TestWebKitLanguage: the user's language is WKWebView's navigator.language, which equals
// Intl's default locale there. The table is what a WKWebView reported on macOS 26.5 for a
// user whose only preferred language was the key (run with -AppleLanguages "(key)").
func TestWebKitLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"en-IQ": "en-GB", "en-DE": "en-GB", "en-001": "en-GB", "en-IE": "en-GB", "en-AE": "en-US", "en": "en-US",
		"en-US": "en-US", "en-GB": "en-GB", "en-AU": "en-AU", "en-IN": "en-IN", "en-NZ": "en-NZ", "en-SG": "en-SG",
		"en-CA": "en-CA", "de-DE": "de-DE", "de-IQ": "de-DE", "de-CH": "de-DE", "de": "de-DE", "fr-BE": "fr-FR",
		"fr-CA": "fr-CA", "nl-BE": "nl-NL", "sv-SE": "sv-SE", "pt-BR": "pt-BR", "pt": "pt-BR", "es": "es-ES",
		"es-419": "es-419", "es-AR": "es-419", "es-MX": "es-MX", "es-US": "es-US", "ar-IQ": "ar", "ckb-IQ": "ckb",
		"ja-JP": "ja", "zh-Hans-CN": "zh-CN", "zh-Hant-TW": "zh-TW", "zh-Hant-HK": "zh-HK", "sr-Latn-RS": "sr-Latn",
		"sr": "sr-RS", "ru-RU": "ru", "ko": "ko-KR", "no": "nb-NO", "lt-LT": "lt", "fil-PH": "fil", "xx-YY": "en-US",
	} {
		if got := webkitLanguage([]string{in}); got != want {
			t.Errorf("webkitLanguage(%q) = %q, WebKit %q", in, got, want)
		}
	}
	if got := webkitLanguage([]string{"en-IQ", "ar-IQ"}); got != "en-GB" {
		t.Errorf("webkitLanguage(en-IQ, ar-IQ) = %q, WebKit en-GB", got)
	}
	if got := webkitLanguage([]string{"xx-YY", "de-DE"}); got != "de-DE" {
		t.Errorf("webkitLanguage(xx-YY, de-DE) = %q", got)
	}
	if tag := UserLocale(); checkTag(tag) != nil {
		t.Errorf("UserLocale() = %q", tag)
	}
}

func TestWebKitTag(t *testing.T) {
	for in, want := range map[string]string{"en_GB": "en-GB", "zh-CN": "zh-CN", "ja": "ja", "": "en-US", "de_DE.UTF-8": "en-US"} {
		if got := webkitTag(in); got != want {
			t.Errorf("webkitTag(%q) = %q, want %q", in, got, want)
		}
	}
}
