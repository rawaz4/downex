//go:build !windows && !(darwin && cgo)

package locale

import "testing"

// [program] A process's own language (macOS: NSLocale's first preferred language) wins over
// the environment, without being written to it: the processes the window starts keep theirs.
func TestSetUserLocale(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "fr_FR.UTF-8")
	t.Cleanup(func() { SetUserLocale("") })
	if got := UserLocale(); got != "fr-FR" {
		t.Fatalf("UserLocale from $LANG = %q, want fr-FR", got)
	}
	for _, c := range []struct{ in, want string }{
		{"de-DE", "de-DE"},
		{"zh-Hans-CN", "zh-Hans-CN"},
		{"en", "en"},
		{"pt_BR", "pt-BR"},
		{"", "fr-FR"},     // cleared: the environment again
		{"#bad", "fr-FR"}, // not a tag: ignored
	} {
		SetUserLocale(c.in)
		if got := UserLocale(); got != c.want {
			t.Errorf("SetUserLocale(%q): UserLocale = %q, want %q", c.in, got, c.want)
		}
	}
}
