package settings_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

const (
	chromeFolder  = `C:\Program Files\Downex\extension\chrome-mv3`
	firefoxFolder = `C:\Program Files\Downex\extension\firefox-mv3`
	registerName  = "Register with browsers again"
)

func setupIntegration(t *testing.T, mutate ...func(*rig)) *rig {
	t.Helper()
	return setup(t, loaded(), views.SettingsRequest{Page: validate.PageIntegration}, mutate...)
}

// said is the extension steps' polite status ("" when it says nothing).
func said(r *rig) string {
	e := find(r.root(), func(e *kit.Element) bool {
		return e.Semantics.Role == kit.RoleStatus && e.Semantics.Live == kit.LivePolite
	})
	if e == nil {
		r.t.Fatal("no status")
	}
	return e.Semantics.Name
}

// alertText is the page's alert, "" when there is none.
func alertText(r *rig) string {
	e := find(r.root(), func(e *kit.Element) bool { return e.Semantics.Role == kit.RoleAlert && !hiddenUnder(r.root(), e) })
	if e == nil {
		return ""
	}
	return textOf(e)
}

func TestIntegrationShowsTheExtensionFolders(t *testing.T) {
	r := setupIntegration(t)
	for _, s := range []string{chromeFolder, firefoxFolder, "chrome://extensions", "edge://extensions", "about:debugging", "manifest.json"} {
		if !hasText(r.root(), s) {
			t.Errorf("no %q", s)
		}
	}
	// the relay is a button now, not a command to type somewhere
	if hasText(r.root(), "downex-nmhost register") {
		t.Error("the page still says to run downex-nmhost register")
	}

	press(r.h, buttonNamed(t, r.root(), "Copy the path of the extension folder for Chrome and Edge"))
	if !slices.Equal(r.fp.Copied, []string{chromeFolder}) || said(r) != "The path was copied." {
		t.Errorf("copied %q, said %q", r.fp.Copied, said(r))
	}
	press(r.h, buttonNamed(t, r.root(), "Open the extension folder for Firefox"))
	if !slices.Contains(r.fp.Calls, "OpenAppFolder extension-firefox") {
		t.Errorf("calls %v", r.fp.Calls)
	}
	if alertText(r) != "" {
		t.Errorf("alert %q", alertText(r))
	}
	r.h.Advance(5 * time.Second)
	if said(r) != "" {
		t.Errorf("still says %q", said(r))
	}
}

func TestIntegrationRegistersWithTheBrowsersAgain(t *testing.T) {
	r := setupIntegration(t)
	press(r.h, buttonNamed(t, r.root(), registerName))
	if !slices.Contains(r.fp.Calls, "RegisterBrowsers") {
		t.Fatalf("calls %v", r.fp.Calls)
	}
	if got := said(r); got != "Downex is registered with your browsers again." {
		t.Errorf("said %q", got)
	}
	if buttonNamed(t, r.root(), registerName).Disabled {
		t.Error("the button stays unavailable")
	}

	// A failure says why, until the next try.
	r.fp.Err = connect.NewError(connect.CodeInternal, errors.New("write the Edge manifest: access is denied"))
	press(r.h, buttonNamed(t, r.root(), registerName))
	if got := alertText(r); got != "Could not register Downex with the browsers: write the Edge manifest: access is denied" {
		t.Errorf("alert %q", got)
	}
	if said(r) != "" {
		t.Errorf("said %q beside the failure", said(r))
	}
	r.fp.Err = nil
	press(r.h, buttonNamed(t, r.root(), registerName))
	if alertText(r) != "" || said(r) == "" {
		t.Errorf("after a good try: alert %q, said %q", alertText(r), said(r))
	}

	// A folder the installation lacks is named as such.
	r.fp.Err = connect.NewError(connect.CodeFailedPrecondition, errors.New("the extension-chrome folder does not exist yet"))
	press(r.h, buttonNamed(t, r.root(), "Open the extension folder for Chrome and Edge"))
	if got := alertText(r); got != "The extension folder is not there. Install Downex again to put it back." {
		t.Errorf("alert %q", got)
	}
}

// noFolders is a platform that cannot say where this installation is.
type noFolders struct{ *viewstest.FakePlatform }

func (noFolders) ExtensionFolder(host.FolderKind) (string, error) {
	return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot locate this program"))
}

func TestIntegrationWithoutTheInstallationsFolders(t *testing.T) {
	r := setupIntegration(t, func(r *rig) { r.h.App.Platform = noFolders{r.fp} })
	// the folder inside an installation, as the page said before, and nothing to copy or open
	if !hasText(r.root(), "extension/chrome-mv3") && !hasText(r.root(), `extension\chrome-mv3`) {
		t.Error("no folder named")
	}
	for _, name := range []string{"Copy the path of the extension folder for Chrome and Edge", "Open the extension folder for Firefox"} {
		if !buttonNamed(t, r.root(), name).Disabled {
			t.Errorf("%q available", name)
		}
	}
	if buttonNamed(t, r.root(), registerName).Disabled {
		t.Error("register unavailable")
	}
}

// translocated is a Mac platform whose Downex.app runs from App Translocation's temporary copy.
type translocated struct{ *viewstest.FakePlatform }

func (translocated) ExtensionFolder(host.FolderKind) (string, error) {
	return "", connect.NewError(connect.CodeFailedPrecondition, host.ErrTranslocated)
}

// A Downex.app that macOS runs from a temporary copy says so beside the folders, whose buttons
// are off; a host that only cannot locate itself shows no such note.
func TestIntegrationSaysWhenMacRunsATemporaryCopy(t *testing.T) {
	r := setupIntegration(t, func(r *rig) { r.h.App.Platform = translocated{r.fp} })
	if !hasText(r.root(), "move Downex.app to Applications") {
		t.Error("no note about the temporary copy")
	}
	if !buttonNamed(t, r.root(), "Open the extension folder for Chrome and Edge").Disabled {
		t.Error("Open folder available for a temporary copy")
	}
	r2 := setupIntegration(t, func(r *rig) { r.h.App.Platform = noFolders{r.fp} })
	if hasText(r2.root(), "move Downex.app to Applications") || hasText(r2.root(), "cannot locate") {
		t.Error("a note for a host that only cannot locate itself")
	}
}
