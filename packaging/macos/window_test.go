package macos

import (
	"regexp"
	"strings"
	"testing"

	"downex/ui/host"
)

// TestScriptBuildsTheNativeWindow: the window is the native one (ui/native with cgo for both
// architectures, joined by lipo, with today's single-instance id so that it takes today's
// locks and files), and -w keeps today's Wails build.
func TestScriptBuildsTheNativeWindow(t *testing.T) {
	src := readScript(t)
	for _, want := range []string{
		"go build -trimpath -ldflags \"$(window_ldflags)\" -o \"$WORK/window-$arch/Downex\" ./ui/native",
		"CGO_ENABLED=1",
		"run lipo -create -output \"$WORK/window/Downex\" \"$WORK/window-arm64/Downex\" \"$WORK/window-amd64/Downex\"",
		"-X downex/ui/native/glue.lockID=" + host.MainInstanceID,
		"getopts ':v:o:wrh' opt",
		"w) WEBVIEW=1 ;;",
		"wails build -clean -trimpath -m -nosyncgomod -platform darwin/universal",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("scripts/build-macos.sh does not contain %q", want)
		}
	}
	// The window binary goes into both bundles, as before.
	for _, re := range []string{
		`ditto "\$WORK/window/Downex" "\$contents/MacOS/Downex"`,
		`ditto "\$WORK/window/Downex" "\$progress/Contents/MacOS/Downex"`,
	} {
		if !regexp.MustCompile(re).MatchString(src) {
			t.Errorf("scripts/build-macos.sh does not match %s", re)
		}
	}
}
