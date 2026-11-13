// Package research finds the research folder, where the native window's tests read the
// step-1 references, goldens and WebView2 copies. These are not part of the repository; a
// test whose files are missing skips.
//
// The folder is $DOWNEX_RESEARCH when it is set, else the folder beside the module's root
// named after it with "-research" (for a checkout in a folder "src", "src-research" next
// to it). It is found at run time, never from a path compiled in, so that no machine's
// own folders end up in the code. Each test also takes its own environment variable that
// names another place for its files: $DOWNEX_REF_092, $DOWNEX_GOLDENS,
// $DOWNEX_WEBSTORAGE_COPIES and $DOWNEX_VIEWS_SHOTS.
package research

import (
	"os"
	"path/filepath"
	"sync"
)

// NativeUI is the native window's folder, relative to the research folder.
var NativeUI = filepath.Join("design-2026-10", "native-ui")

// Beside is rel (joined) in the research folder: $DOWNEX_RESEARCH, else the folder beside the
// module's root (the go.mod above the current folder, which go test makes the package's)
// named after it with "-research". It is "" when neither is known.
func Beside(rel ...string) string {
	root := Root()
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, rel...)...)
}

// Root is the research folder (see Beside), or "" when neither $DOWNEX_RESEARCH nor the
// module's root is known.
func Root() string {
	if d := os.Getenv("DOWNEX_RESEARCH"); d != "" {
		return d
	}
	root := moduleRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"-research")
}

var moduleRoot = sync.OnceValue(func() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
})
