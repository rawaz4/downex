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

	folderDisclosure = "Advanced: install from the folder"
	folderSummary    = "Load unpacked, Copy path, Open folder"
)

func setupIntegration(t *testing.T, mutate ...func(*rig)) *rig {
	t.Helper()
	return setup(t, loaded(), views.SettingsRequest{Page: validate.PageIntegration}, mutate...)
}

// openFolderSteps opens "Advanced: install from the folder".
func openFolderSteps(r *rig) {
	r.t.Helper()
	press(r.h, buttonNamed(r.t, r.root(), folderDisclosure+" "+folderSummary))
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

// textFont is the font of the visible text s.
func textFont(t *testing.T, root *kit.Element, s string) kit.Font {
	t.Helper()
	e := find(root, func(e *kit.Element) bool {
		tx, ok := e.Widget().(*views.Text)
		return ok && tx.Text() == s && !hiddenUnder(root, e)
	})
	if e == nil {
		t.Fatalf("no text %q", s)
	}
	return e.Widget().(*views.Text).Font()
}

// sizeOf is a font's size in DIPs at the default text size (1rem = 16).
func sizeOf(f kit.Font) kit.DIP {
	d, _ := f.Size.Resolve(kit.Scale{}, -1)
	return d
}

// folderSteps is the list of steps that load the extension from Downex's folder.
func folderSteps(r *rig) *kit.Element {
	e := find(r.root(), func(e *kit.Element) bool {
		return e.Semantics.Role == kit.RoleList && e.Semantics.Name == folderDisclosure
	})
	if e == nil {
		r.t.Fatal("no folder steps")
	}
	return e
}

// The stores come first: Add to Chrome and Add to Firefox open their listings, the link under
// them the website's guide to the Chrome copy with the YouTube button; the folder's steps are
// folded until the disclosure opens them.
func TestIntegrationOffersTheStoresFirst(t *testing.T) {
	r := setupIntegration(t)
	for _, c := range []struct{ name, url string }{
		{"Add to Chrome", "https://chromewebstore.google.com/detail/llnofffhagcfejjcephcilcbndnhnbeg"},
		{"Add to Firefox", "https://addons.mozilla.org/firefox/addon/downex/"},
		{"Want the YouTube button in Chrome?", "https://downex.app/chrome-youtube"},
	} {
		b := buttonNamed(t, r.root(), c.name)
		if b.Disabled || hiddenUnder(r.root(), b) {
			t.Errorf("%q not available", c.name)
		}
		press(r.h, b)
		if got := r.fp.Calls[len(r.fp.Calls)-1]; got != "OpenExternal "+c.url {
			t.Errorf("%q: last call %q", c.name, got)
		}
	}
	// The browsers Add to Chrome serves are those whose downloads arrive (the relay registers
	// itself with them): the two hints name the same ones.
	for _, s := range []string{"Add to Chrome also works in Edge, Brave and Chromium.", "Downloads you start in Chrome, Edge, Brave, Chromium or Firefox arrive here.",
		"Store copies update themselves", "Keep one copy per browser"} {
		if !hasText(r.root(), s) {
			t.Errorf("no %q", s)
		}
	}
	// The extension's part of the card is a group named after it (a name on a plain box would
	// reach no screen reader).
	if find(r.root(), func(e *kit.Element) bool {
		return e.Semantics.Role == kit.RoleGroup && e.Semantics.Name == "The Downex browser extension"
	}) == nil {
		t.Error("no group named The Downex browser extension")
	}
	// The nested disclosure's label is no larger and no bolder than the card's own title.
	title, label := textFont(t, r.root(), "The Downex browser extension"), textFont(t, r.root(), folderDisclosure)
	if ts, ls := sizeOf(title), sizeOf(label); ls >= ts || label.Weight > title.Weight {
		t.Errorf("disclosure label %v DIP weight %v, card title %v DIP weight %v", ls, label.Weight, ts, title.Weight)
	}

	// Folded: the disclosure says what it hides, and the steps are hidden.
	toggle := buttonNamed(t, r.root(), folderDisclosure+" "+folderSummary)
	if toggle.Semantics.Expand != kit.ExpandClosed || !hiddenUnder(r.root(), folderSteps(r)) {
		t.Fatalf("expand %v, steps hidden %v", toggle.Semantics.Expand, hiddenUnder(r.root(), folderSteps(r)))
	}
	press(r.h, toggle)
	if toggle.Semantics.Expand != kit.ExpandOpen || toggle.Semantics.Name != folderDisclosure || hiddenUnder(r.root(), folderSteps(r)) {
		t.Errorf("opened: expand %v, name %q, steps hidden %v", toggle.Semantics.Expand, toggle.Semantics.Name, hiddenUnder(r.root(), folderSteps(r)))
	}
	if !hasText(r.root(), "remove the store copy first") {
		t.Error("the folder steps do not say to remove the store copy")
	}
	// Firefox Add-ons has the YouTube button already: its copy is the one to keep.
	if !hasText(r.root(), "the Firefox Add-ons copy already has the YouTube button and is the one to keep") ||
		!hasText(r.root(), "remove that one first") {
		t.Error("the Firefox step does not keep the Firefox Add-ons copy")
	}
	press(r.h, toggle)
	if toggle.Semantics.Expand != kit.ExpandClosed || !hiddenUnder(r.root(), folderSteps(r)) {
		t.Error("did not fold again")
	}
	// the relay's button stays outside the fold
	if hiddenUnder(r.root(), buttonNamed(t, r.root(), registerName)) {
		t.Error("register is folded away")
	}

	// A page that does not open says why.
	r.fp.Err = connect.NewError(connect.CodeInternal, errors.New("no browser"))
	press(r.h, buttonNamed(t, r.root(), "Add to Firefox"))
	if got := alertText(r); got != "Could not open https://addons.mozilla.org/firefox/addon/downex/: no browser" {
		t.Errorf("alert %q", got)
	}
}

func TestIntegrationShowsTheExtensionFolders(t *testing.T) {
	r := setupIntegration(t)
	openFolderSteps(r)
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
	openFolderSteps(r)
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
	openFolderSteps(r)
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

// A Downex.app that macOS runs from a temporary copy says so, also while the folders' steps are
// folded, and their buttons are off; a host that only cannot locate itself shows no such note.
func TestIntegrationSaysWhenMacRunsATemporaryCopy(t *testing.T) {
	r := setupIntegration(t, func(r *rig) { r.h.App.Platform = translocated{r.fp} })
	if !hasText(r.root(), "move Downex.app to Applications") {
		t.Error("no note about the temporary copy while the folder steps are folded")
	}
	openFolderSteps(r)
	if !hasText(r.root(), "move Downex.app to Applications") {
		t.Error("no note about the temporary copy")
	}
	if !buttonNamed(t, r.root(), "Open the extension folder for Chrome and Edge").Disabled {
		t.Error("Open folder available for a temporary copy")
	}
	r2 := setupIntegration(t, func(r *rig) { r.h.App.Platform = noFolders{r.fp} })
	openFolderSteps(r2)
	if hasText(r2.root(), "move Downex.app to Applications") || hasText(r2.root(), "cannot locate") {
		t.Error("a note for a host that only cannot locate itself")
	}
}
