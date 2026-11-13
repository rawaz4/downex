package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultInstallation writes the manifests into the browsers' directories below
// ~/Library/Application Support, or with system into their directories below /Library
// (docs/MACOS-SPEC.md §4). The manifests name this executable: inside the app bundle
// Downex.app/Contents/Helpers/downex-nmhost.
func defaultInstallation(system bool) (installation, error) {
	inst := installation{scope: "the current user", samePath: func(a, b string) bool {
		return filepath.Clean(a) == filepath.Clean(b)
	}}
	if system {
		inst.scope = "all users"
		inst.targets = macSystemTargets()
		return inst, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return inst, fmt.Errorf("locate the home directory: %w", err)
	}
	inst.targets = macUserTargets(home)
	return inst, nil
}
