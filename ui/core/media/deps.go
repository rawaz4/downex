package media

import (
	"sync"
	"sync/atomic"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"downex/ui/core/format"
	"downex/ui/core/text"
)

// What media uses of ui/core/format and ui/core/text (the window's JavaScript rules and number
// formats), and the two locale services today's window takes from Intl.

var (
	isJSSpace     = text.IsJSSpace
	jsTrim        = text.TrimJS
	jsLower       = text.LowerJS
	jsRound       = format.Round
	jsNumber      = format.Num
	formatBytes   = format.Bytes
	formatBitrate = format.Bitrate
	formatPercent = format.Percent
)

// schemeAndPath is new URL(s)'s scheme (without ":") and pathname; ok false where it throws.
func schemeAndPath(s string) (scheme, path string, ok bool) {
	u, err := text.ParseURL(s)
	if err != nil {
		return "", "", false
	}
	return u.Scheme(), u.Pathname(), true
}

// LanguageNamer names a BCP 47 language tag in English as Intl.DisplayNames(['en'], {type:
// 'language', fallback: 'code'}) does ("en" → "English", "pt-BR" → "Brazilian Portuguese");
// ok is false when it has no name for the tag.
type LanguageNamer func(tag string) (name string, ok bool)

var namer atomic.Pointer[LanguageNamer]

// SetLanguageNamer sets the names LanguageName gives (the window sets the system's, from ICU,
// at start). Until then, and for a tag it has no name for, a language is named by its tag, as
// Intl's fallback "code" names it.
func SetLanguageNamer(n LanguageNamer) {
	if n == nil {
		namer.Store(nil)
		return
	}
	namer.Store(&n)
}

// LanguageName is "en" → "English"; the tag itself when nothing names it.
func LanguageName(tag string) string {
	if tag == "" {
		return ""
	}
	if n := namer.Load(); n != nil {
		if name, ok := (*n)(tag); ok && name != "" {
			return name
		}
	}
	return tag
}

// localeCompare is String.prototype.localeCompare with its defaults: the Unicode collation's
// root order, accents and case telling strings apart. A collator is not safe for concurrent
// use, so each comparison takes one from a pool.
func localeCompare(a, b string) int {
	if a == b {
		return 0
	}
	c := collators.Get().(*collate.Collator)
	defer collators.Put(c)
	return c.CompareString(a, b)
}

var collators = sync.Pool{New: func() any { return collate.New(language.Und) }}
