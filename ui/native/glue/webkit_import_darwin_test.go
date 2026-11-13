//go:build darwin && cgo

package glue

import (
	"testing"

	"downex/ui/state/prefs"
)

// The first prefs file takes today's Mac window's localStorage from the configured folder,
// or from ~/Library/WebKit for the user's own file only: a test's or another data folder's
// file never reads the user's WebKit data.
func TestWebKitImportOnlyForTheUsersOwnFile(t *testing.T) {
	if webKitImport("", t.TempDir()+"/ui-prefs.json") != nil {
		t.Error("another folder's prefs file reads the user's WebKit data")
	}
	if webKitImport(t.TempDir(), t.TempDir()+"/ui-prefs.json") == nil {
		t.Error("a configured WebKit folder is not read")
	}
	if p := prefs.DefaultPath(); p != "" && webKitImport("", p) == nil {
		t.Error("the user's own prefs file does not take today's preferences")
	}
}
