package rescuetext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtensionRepeatsTexts: the extension words a refused rescue as the desktop window
// and the CLI do (docs/BROWSER-SPEC.md §6, R-E7): every text of the table, and the macOS
// addition, is in ext/src/lib/rescue.ts under its code.
func TestExtensionRepeatsTexts(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "ext", "src", "lib", "rescue.ts"))
	if err != nil {
		t.Skipf("the extension's sources: %v", err)
	}
	src := string(b)
	for _, code := range Codes() {
		text := Text(code, "linux")
		if text == "" {
			t.Errorf("%s has no text", code)
			continue
		}
		var line string
		for _, l := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), code+":") {
				line = l
				break
			}
		}
		if !strings.Contains(line, text) {
			t.Errorf("rescue.ts words %s as %q, want %q", code, strings.TrimSpace(line), text)
		}
	}
	if !strings.Contains(src, MacNoAccess) {
		t.Errorf("rescue.ts lacks the macOS text %q", MacNoAccess)
	}
}

func TestTable(t *testing.T) {
	if got := Text(NoAccess, "darwin"); got != texts[NoAccess]+MacNoAccess {
		t.Errorf("darwin no_access = %q", got)
	}
	if got := Text(NoAccess, "windows"); got != texts[NoAccess] {
		t.Errorf("windows no_access = %q", got)
	}
	if Text("disk_full", "linux") != "" {
		t.Error("a code outside the table has a text")
	}
	for _, c := range []string{NoRanges, SizeUnknown, Changed, Unproven} {
		if !FromStart(c) || Retryable(c) {
			t.Errorf("%s: from start %v, retryable %v", c, FromStart(c), Retryable(c))
		}
	}
	for _, c := range []string{Locked, Network} {
		if FromStart(c) || !Retryable(c) {
			t.Errorf("%s: from start %v, retryable %v", c, FromStart(c), Retryable(c))
		}
	}
	if len(Codes()) != len(texts) {
		t.Errorf("Codes lists %d of %d texts", len(Codes()), len(texts))
	}
}
