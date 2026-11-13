package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindDaemonInBundle checks that every executable of a Downex.app, and a CLI linked
// into another directory, autostarts the login item (the daemon with the menu-bar icon)
// when it is installed, else the downexd in Contents/Helpers.
func TestFindDaemonInBundle(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir()) // /var is a link to /private/var
	if err != nil {
		t.Fatal(err)
	}
	contents := filepath.Join(root, "Downex.app", "Contents")
	helpers := filepath.Join(contents, "Helpers")
	agent := filepath.Join(contents, "Library", "LoginItems", MacAgentApp, "Contents", "MacOS", MacAgentName)
	exes := []string{
		filepath.Join(helpers, "downex"),
		filepath.Join(contents, "MacOS", MacWindowName),
		filepath.Join(helpers, MacProgressApp, "Contents", "MacOS", MacWindowName),
	}
	create := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range exes {
		create(p)
	}
	noEnv := func(string) string { return "" }
	daemon := filepath.Join(helpers, "downexd")
	if _, err := FindDaemon(noEnv, func() (string, error) { return exes[1], nil }); err == nil ||
		!strings.Contains(err.Error(), "in "+helpers) {
		t.Errorf("missing daemon: %v", err)
	}
	create(daemon)
	link := filepath.Join(t.TempDir(), "downex")
	if err := os.Symlink(exes[0], link); err != nil {
		t.Fatal(err)
	}
	from := append(exes, link)
	for _, exe := range from {
		got, err := FindDaemon(noEnv, func() (string, error) { return exe, nil })
		if err != nil || got != daemon {
			t.Errorf("FindDaemon from %s without the login item = %q, %v; want %q", exe, got, err, daemon)
		}
	}
	if err := os.MkdirAll(agent, 0o755); err != nil { // a folder where the executable belongs
		t.Fatal(err)
	}
	if got, err := FindDaemon(noEnv, func() (string, error) { return exes[0], nil }); err != nil || got != daemon {
		t.Errorf("FindDaemon with a folder for the login item = %q, %v; want %q", got, err, daemon)
	}
	if err := os.Remove(agent); err != nil {
		t.Fatal(err)
	}
	create(agent)
	for _, exe := range append(from, agent) {
		got, err := FindDaemon(noEnv, func() (string, error) { return exe, nil })
		if err != nil || got != agent {
			t.Errorf("FindDaemon from %s = %q, %v; want the login item %q", exe, got, err, agent)
		}
	}
	l, err := FindLayout(func() (string, error) { return link, nil })
	if err != nil || l.Bundle != filepath.Join(root, "Downex.app") || l.Window != exes[1] || l.Progress != exes[2] ||
		l.Agent != agent {
		t.Errorf("FindLayout via a link: %+v %v", l, err)
	}
}
