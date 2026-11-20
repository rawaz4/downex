package host

import (
	"io/fs"
	"testing"

	"downex/internal/filecheck"
)

// TestRunExtsAreRisky is the drift guard between the file check's run extensions
// (docs/DESKTOP-SPEC.md §9.4) and the open gate: every extension the check treats as a
// program is one the gate asks about (RiskProgram or RiskContent), on every platform's
// rules, except those listed here with the reason.
func TestRunExtsAreRisky(t *testing.T) {
	exceptions := map[string]string{
		// macOS opens these as programs or follows them (macos.go judges them there); on
		// Windows and Linux they are plain files without an application that runs them.
		"mpkg":        "a macOS installer bundle (macos.go)",
		"tool":        "a macOS shell script (macos.go)",
		"terminal":    "a macOS Terminal profile (macos.go)",
		"workflow":    "a macOS Automator workflow (macos.go)",
		"scpt":        "a macOS AppleScript (macos.go)",
		"scptd":       "a macOS AppleScript bundle (macos.go)",
		"applescript": "a macOS AppleScript (macos.go)",
		"webloc":      "a macOS link (macos.go)",
		"fileloc":     "a macOS link (macos.go)",
		"inetloc":     "a macOS link (macos.go)",
	}
	none := func(string, fs.FileInfo) Risk { return RiskNone }
	for ext := range exceptions {
		if Classify("file."+ext, nil, none) != RiskNone {
			t.Errorf("the exception for .%s is stale: the gate asks about it", ext)
		}
	}
	for _, ext := range filecheck.RunExts() {
		if Classify("file."+ext, nil, none) != RiskNone {
			continue
		}
		if _, ok := exceptions[ext]; !ok {
			t.Errorf("the file check calls .%s a program, but the open gate does not ask before it opens", ext)
		}
	}
}
