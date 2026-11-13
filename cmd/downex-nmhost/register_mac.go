package main

import "path"

// Manifest directories on macOS (docs/MACOS-SPEC.md §4). The tables are not limited to
// darwin builds so that tests check them everywhere; paths use package path (macOS
// paths are slash-separated).
var (
	// macUserDirs are below ~/Library/Application Support: the manifest directory and
	// the browser's own data directory, which exists once this user ran it.
	macUserDirs = map[string]struct{ hosts, profile string }{
		"chrome":   {"Google/Chrome/NativeMessagingHosts", "Google/Chrome"},
		"chromium": {"Chromium/NativeMessagingHosts", "Chromium"},
		"edge":     {"Microsoft Edge/NativeMessagingHosts", "Microsoft Edge"},
		"brave":    {"BraveSoftware/Brave-Browser/NativeMessagingHosts", "BraveSoftware/Brave-Browser"},
		// Firefox reads hosts below Mozilla and keeps its profiles below Firefox.
		"firefox": {"Mozilla/NativeMessagingHosts", "Firefox"},
	}
	// macSystemDirs are read for every user. Brave has none in the specification, so it
	// cannot be registered for all users on macOS.
	macSystemDirs = map[string]string{
		"chrome":   "/Library/Google/Chrome/NativeMessagingHosts",
		"edge":     "/Library/Microsoft/Edge/NativeMessagingHosts",
		"chromium": "/Library/Application Support/Chromium/NativeMessagingHosts",
		"firefox":  "/Library/Application Support/Mozilla/NativeMessagingHosts",
	}
)

// macUserTargets returns the per-user manifest locations of the user whose home
// directory is home.
func macUserTargets(home string) map[string]target {
	support := path.Join(home, "Library", "Application Support")
	t := make(map[string]target, len(allBrowsers))
	for _, b := range allBrowsers {
		d := macUserDirs[b]
		t[b] = target{browser: b, file: path.Join(support, d.hosts, hostName+".json"), profile: path.Join(support, d.profile)}
	}
	return t
}

// macSystemTargets returns the manifest locations for all users; browsers without one
// are missing.
func macSystemTargets() map[string]target {
	t := make(map[string]target, len(macSystemDirs))
	for _, b := range allBrowsers {
		if dir, ok := macSystemDirs[b]; ok {
			t[b] = target{browser: b, file: path.Join(dir, hostName+".json")}
		}
	}
	return t
}
