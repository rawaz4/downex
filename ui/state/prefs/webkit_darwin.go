//go:build darwin

package prefs

import (
	"downex/ui/state"
	"downex/ui/state/prefs/webstorage"
)

// WebKitImport reads state.StorageKeys from today's Mac windows' localStorage (WebKit's,
// origin wails://wails, the main window's before the progress windows') under base
// (webstorage.WebKitDefaultDir() when ""), never changing it.
func WebKitImport(base string) Importer {
	return func() (map[string]string, error) {
		if base == "" {
			base = webstorage.WebKitDefaultDir()
		}
		if base == "" {
			return nil, webstorage.ErrNoStorage
		}
		return webstorage.ReadWebKit(base, webstorage.WebKitBundles, state.StorageKeys())
	}
}
