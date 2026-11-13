package kit

import "testing"

func TestKeyLabel(t *testing.T) {
	defer func(old bool) { MacKeys = old }(MacKeys)
	cases := []struct{ in, win, mac string }{
		{"Ctrl+K", "Ctrl+K", "⌘K"},
		{"Ctrl K", "Ctrl K", "⌘K"},
		{"Ctrl+,", "Ctrl+,", "⌘,"},
		{"Ctrl", "Ctrl", "⌘"},
		{"Commands (Ctrl+K)", "Commands (Ctrl+K)", "Commands (⌘K)"},
		{"One URL per line. Ctrl+Enter adds.", "One URL per line. Ctrl+Enter adds.", "One URL per line. ⌘Enter adds."},
		{"Shift+Del", "Shift+Del", "⇧⌫"},
		{"Del", "Del", "⌫"},
		{"Remove (Del)", "Remove (Del)", "Remove (⌫)"},
		{"Delete", "Delete", "Delete"},
		{"Delete the files too", "Delete the files too", "Delete the files too"},
		{"Enter", "Enter", "Enter"},
	}
	for _, c := range cases {
		MacKeys = false
		if got := KeyLabel(c.in); got != c.win {
			t.Errorf("Windows KeyLabel(%q) = %q, want %q", c.in, got, c.win)
		}
		MacKeys = true
		if got := KeyLabel(c.in); got != c.mac {
			t.Errorf("Mac KeyLabel(%q) = %q, want %q", c.in, got, c.mac)
		}
	}
}
