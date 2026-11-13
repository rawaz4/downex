package platform

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
)

// ToolsDir returns the folder where the user's own daemon keeps the yt-dlp and Deno
// copies it downloads (docs/SITE-MEDIA-SPEC.md §3.9): ToolsDirFor with the running
// system and its environment. It does not create the folder.
func ToolsDir() (string, error) { return ToolsDirFor(runtime.GOOS, os.Getenv) }

// ToolsDirFor returns the tools folder of system goos, reading the environment through
// env:
//
//   - windows: %LOCALAPPDATA%\Downex\tools
//   - darwin: $HOME/Library/Application Support/Downex/tools
//   - others: ${XDG_DATA_HOME:-$HOME/.local/share}/downex/tools (an XDG_DATA_HOME that
//     is not absolute is ignored, as the XDG Base Directory specification says)
//
// The base must be an absolute path. It is kept free of build constraints so that every
// platform runs its tests: Windows paths use backslashes, the others slashes.
func ToolsDirFor(goos string, env func(string) string) (string, error) {
	switch goos {
	case "windows":
		base := env("LOCALAPPDATA")
		if !windowsAbs(base) {
			return "", fmt.Errorf("platform: LOCALAPPDATA %q is not an absolute path", base)
		}
		return strings.TrimRight(base, `\/`) + `\Downex\tools`, nil
	case "darwin":
		home := env("HOME")
		if !path.IsAbs(home) {
			return "", fmt.Errorf("platform: home directory %q is not an absolute path", home)
		}
		return path.Join(home, "Library", "Application Support", "Downex", "tools"), nil
	}
	base := env("XDG_DATA_HOME")
	if !path.IsAbs(base) {
		home := env("HOME")
		if !path.IsAbs(home) {
			return "", fmt.Errorf("platform: home directory %q is not an absolute path", home)
		}
		base = path.Join(home, ".local", "share")
	}
	return path.Join(base, "downex", "tools"), nil
}

// windowsAbs reports whether p is an absolute Windows path: a drive letter followed by
// a separator, or a UNC path (\\server\share).
func windowsAbs(p string) bool {
	switch {
	case len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'):
		c := p[0] | 0x20
		return 'a' <= c && c <= 'z'
	case len(p) > 2 && (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/'):
		return strings.TrimLeft(p, `\/`) != ""
	}
	return false
}
