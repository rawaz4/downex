package state

import (
	"downex/ui/core/format"
	"downex/ui/core/locale"
	"downex/ui/core/media"
	"downex/ui/core/query"
)

// UseUserLocale is what today's main.ts does before mounting: numbers, sizes and dates in
// the user's locale (setFormatLocale(navigator.language), en-US when the tag is not
// supported), and audio languages named by the system (Intl.DisplayNames). It returns
// the collator for names in that locale (DownloadsOptions.Collator). Call it once, at
// start, before the first store is read.
func UseUserLocale() query.Collator {
	tag := locale.UserLocale()
	if f, err := locale.New(tag, ""); err == nil {
		format.SetLocale(f)
	} else if f, err := locale.New("en-US", ""); err == nil {
		// unsupported locale tag: keep the default
		format.SetLocale(f)
		tag = "en-US"
	}
	media.SetLanguageNamer(locale.LanguageName)
	return query.NewCollator(tag)
}
