package kit

import "strings"

// MacKeys makes shortcut labels use macOS's names (⌘ for Ctrl). The macOS host sets it
// before it builds a window; the keys themselves already accept ⌘ wherever they take Ctrl.
var MacKeys bool

// macKeys is how the Mac names a shortcut: "Ctrl+K" and "Ctrl K" are "⌘K", a lone "Ctrl" cap
// "⌘"; Remove's "Del" is the Mac's delete key, ⌫ (owner decision 2026-11-11), and
// "Shift+Del" ⇧⌫. "Delete" (a word, or the key's accessible name) stays: the replacer tries
// its pairs in order at each place, so it is matched before "Del".
var macKeys = strings.NewReplacer("Ctrl+", "⌘", "Ctrl ", "⌘", "Ctrl", "⌘",
	"Shift+Del", "⇧⌫", "Delete", "Delete", "Del", "⌫")

// KeyLabel is a shortcut, or a text that names one, as this system writes it: unchanged on
// Windows, ⌘ for Ctrl on the Mac (MacKeys).
func KeyLabel(s string) string {
	if !MacKeys {
		return s
	}
	return macKeys.Replace(s)
}
