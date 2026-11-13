package platform

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// This file holds the OS-independent parts of the macOS implementation
// (docs/MACOS-SPEC.md §2): locations, the LaunchAgent property list and the open(1)
// command line. They are used on darwin only, but kept free of build constraints so
// that every platform runs their tests. Paths are slash-separated (package path), as
// on macOS.

// launchAgentLabel is the launchd label of the login item SetLaunchAtLogin manages;
// its property list is ~/Library/LaunchAgents/<label>.plist.
const launchAgentLabel = "com.downex.agent"

// darwinDirs are the per-user locations Downex uses on macOS.
type darwinDirs struct {
	downloads   string // ~/Downloads
	logs        string // ~/Library/Logs/Downex
	launchAgent string // ~/Library/LaunchAgents/com.downex.agent.plist
}

// darwinPaths returns the locations below the home directory home, which must be an
// absolute path.
func darwinPaths(home string) (darwinDirs, error) {
	if !path.IsAbs(home) {
		return darwinDirs{}, fmt.Errorf("platform: home directory %q is not an absolute path", home)
	}
	home = path.Clean(home)
	return darwinDirs{
		downloads:   path.Join(home, "Downloads"),
		logs:        path.Join(home, "Library", "Logs", "Downex"),
		launchAgent: path.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"),
	}, nil
}

// launchAgentPlist is the LaunchAgent that makes launchd run exe with args when the
// user logs in: Label, ProgramArguments (exe first), RunAtLoad true, KeepAlive false
// (quitting stays quit) and ProcessType Interactive (no background throttling). It is
// an XML property list (Apple's PropertyList-1.0 DTD). exe must be an absolute path;
// strings that are not valid UTF-8 or contain control characters are refused.
func launchAgentPlist(exe string, args []string) ([]byte, error) {
	if !path.IsAbs(exe) {
		return nil, fmt.Errorf("platform: login entry: %q is not an absolute executable path", exe)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("\t<key>Label</key>\n")
	b.WriteString("\t<string>" + launchAgentLabel + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n")
	b.WriteString("\t<array>\n")
	for _, w := range append([]string{exe}, args...) {
		s, err := plistString(w)
		if err != nil {
			return nil, err
		}
		b.WriteString("\t\t<string>" + s + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n")
	b.WriteString("\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n")
	b.WriteString("\t<false/>\n")
	b.WriteString("\t<key>ProcessType</key>\n")
	b.WriteString("\t<string>Interactive</string>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return []byte(b.String()), nil
}

// plistString escapes s as the character data of a <string> element: '&', '<' and '>'
// become the predefined XML entities. It refuses invalid UTF-8, control characters
// (an argument never needs one) and the code points XML 1.0 excludes (U+FFFE, U+FFFF).
func plistString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("platform: login entry: %q is not valid UTF-8", s)
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || r == 0xfffe || r == 0xffff:
			return "", fmt.Errorf("platform: login entry: %q contains the character %U", s, r)
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

// bundleExecutableDir is what separates an application bundle from its main
// executable: <Name>.app/Contents/MacOS/<executable>.
const bundleExecutableDir = ".app/Contents/MacOS/"

// appBundle returns the application bundle whose Contents/MacOS directory holds exe
// directly, the innermost one when bundles are nested
// (".../Downex.app/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex" →
// ".../Downex Progress.app"). ok is false for a relative path and for any other
// executable (a helper tool in Contents/Helpers, a file below Contents/MacOS/<dir>/),
// which open(1) could not start by its bundle.
func appBundle(exe string) (bundle string, ok bool) {
	if !path.IsAbs(exe) {
		return "", false
	}
	exe = path.Clean(exe)
	i := strings.LastIndex(exe, bundleExecutableDir)
	if i < 0 {
		return "", false
	}
	name := exe[i+len(bundleExecutableDir):]
	bundle = exe[:i+len(".app")]
	if name == "" || strings.Contains(name, "/") || path.Base(bundle) == ".app" {
		return "", false
	}
	return bundle, true
}

// openArgs are the arguments of open(1) that start a new instance (-n) of the
// application bundle, with env ("NAME=value" entries) added to the environment Launch
// Services gives it and args as its command-line arguments. Launch Services activates
// the new app (it answers the user's action) unless background is set (-g: a launch
// nobody asked for at that moment must not take the focus).
func openArgs(bundle string, background bool, env, args []string) []string {
	argv := []string{"-n"}
	if background {
		argv = append(argv, "-g")
	}
	for _, e := range env {
		argv = append(argv, "--env", e)
	}
	argv = append(argv, bundle)
	if len(args) > 0 {
		argv = append(append(argv, "--args"), args...)
	}
	return argv
}

// forwardedEnv selects from environ ("NAME=value" entries, as os.Environ returns them)
// what an application started through Launch Services must still see. Such an
// application gets the user session's environment, not the caller's, so the variables
// that point Downex programs at a daemon are handed over: every DOWNEX_* variable (such
// as DOWNEX_ENDPOINT and DOWNEX_DAEMON). The default endpoint needs nothing: it follows
// the user's temporary directory, which Launch Services provides.
func forwardedEnv(environ []string) []string {
	var env []string
	for _, e := range environ {
		name, _, ok := strings.Cut(e, "=")
		if ok && strings.HasPrefix(name, "DOWNEX_") && len(name) > len("DOWNEX_") {
			env = append(env, e)
		}
	}
	return env
}
