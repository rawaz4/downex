//go:build !windows && !darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Manifest directories (Linux): per user below $XDG_CONFIG_HOME (default ~/.config) for
// the Chromium browsers and ~/.mozilla for Firefox; for all users below /etc/opt,
// /etc and /usr/lib.
var (
	userDirs = map[string]string{ // relative to the config directory, or home for firefox
		"chrome":   "google-chrome",
		"chromium": "chromium",
		"edge":     "microsoft-edge",
		"brave":    filepath.Join("BraveSoftware", "Brave-Browser"),
		"firefox":  ".mozilla",
	}
	systemDirs = map[string]string{
		"chrome":   "/etc/opt/chrome/native-messaging-hosts",
		"chromium": "/etc/chromium/native-messaging-hosts",
		"edge":     "/etc/opt/edge/native-messaging-hosts",
		"brave":    "/etc/opt/brave.com/brave/native-messaging-hosts",
		"firefox":  "/usr/lib/mozilla/native-messaging-hosts",
	}
)

// defaultInstallation writes the manifests into the browsers' per-user directories, or
// with system into their system-wide directories.
func defaultInstallation(system bool) (installation, error) {
	inst := installation{scope: "the current user", samePath: func(a, b string) bool {
		return filepath.Clean(a) == filepath.Clean(b)
	}}
	if system {
		inst.scope = "all users"
		inst.targets = make(map[string]target, len(allBrowsers))
		for _, b := range allBrowsers {
			inst.targets[b] = target{browser: b, file: filepath.Join(systemDirs[b], hostName+".json")}
		}
		return inst, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return inst, fmt.Errorf("locate the home directory: %w", err)
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(cfg) {
		cfg = filepath.Join(home, ".config")
	}
	inst.targets = unixTargets(home, cfg)
	return inst, nil
}

// unixTargets returns the per-user manifest locations below home and cfg.
func unixTargets(home, cfg string) map[string]target {
	t := make(map[string]target, len(allBrowsers))
	for _, b := range allBrowsers {
		var profile, dir string
		if b == "firefox" {
			profile = filepath.Join(home, userDirs[b])
			dir = filepath.Join(profile, "native-messaging-hosts")
		} else {
			profile = filepath.Join(cfg, userDirs[b])
			dir = filepath.Join(profile, "NativeMessagingHosts")
		}
		t[b] = target{browser: b, file: filepath.Join(dir, hostName+".json"), profile: profile}
	}
	return t
}
