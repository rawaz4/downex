package locale_test

import (
	"encoding/json"
	"runtime"
	"testing"

	. "downex/ui/core/format/formattest"
	"downex/ui/core/locale"
)

// TestLanguageNameGoldens replays the goldens of media.ts languageName (Intl.DisplayNames in
// English, the tag where Intl has no name or throws) through LanguageName, as ui/core/media's
// LanguageName uses a namer.
func TestLanguageNameGoldens(t *testing.T) {
	if runtime.GOOS != "windows" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "386") {
		t.Skip("no system names here")
	}
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s", path)
	}
	cases := g.Functions["media.languageName"].Cases
	if len(cases) == 0 {
		t.Fatal("no media.languageName cases")
	}
	for _, c := range cases {
		tag, err := Str(c.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		var want string
		if err := json.Unmarshal(c.Expected("en-US", "UTC").Out, &want); err != nil {
			t.Fatal(err)
		}
		got := tag
		if tag != "" {
			if name, ok := locale.LanguageName(tag); ok {
				got = name
			}
		}
		if got != want {
			t.Errorf("LanguageName(%q) = %q, want %q", tag, got, want)
		}
	}
}
