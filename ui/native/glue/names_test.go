package glue

import (
	"os"
	"path/filepath"
	"testing"

	"downex/ui/host"
)

// The product's locks are today's window's, name for name (Wails v2.17's pattern over
// host.MainInstanceID and its progress ids), so an old and a new window meet on them.
func TestProductLocksAreTodays(t *testing.T) {
	for _, c := range []struct {
		name string
		view host.View
		want host.HandoffNames
	}{
		{"main window", host.View{}, host.HandoffNames{
			Mutex:  "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904sim",
			Class:  "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-sic",
			Window: "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-siw",
		}},
		{"progress window 42", host.View{Progress: 42}, host.HandoffNames{
			Mutex:  "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-progress-42sim",
			Class:  "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-progress-42-sic",
			Window: "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-progress-42-siw",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := HandoffNames(host.MainInstanceID, c.view); got != c.want {
				t.Fatalf("names %+v, want %+v", got, c.want)
			}
			if got, want := InstanceID(host.MainInstanceID, c.view), c.view.InstanceID(); got != want {
				t.Fatalf("instance id %q, want the host's %q", got, want)
			}
			if got := HandoffNames(host.MainInstanceID, c.view); got != host.HandoffNamesOf(c.view.InstanceID()) {
				t.Fatalf("names %+v differ from the host's", got)
			}
		})
	}
	if IsPreview(host.MainInstanceID) {
		t.Fatal("today's id taken for a preview")
	}
}

// A build that is not given today's id at link time keeps the preview's locks and files: it
// never hands over to (or takes over from) the shipped window, and never writes its files.
func TestPreviewKeepsItsOwnLocksAndFiles(t *testing.T) {
	if want := os.Getenv("GLUE_WANT_LOCK"); want != "" { // a run linked as build.ps1 -Product links
		if LockID() != want || IsPreview(LockID()) != (want != host.MainInstanceID) {
			t.Fatalf("the linked lock id is %q, want %q", LockID(), want)
		}
		t.Skip("linked with its own lock id")
	}
	if LockID() != PreviewLockID || !IsPreview(LockID()) {
		t.Fatalf("the default lock id is %q, want the preview's", LockID())
	}
	for _, v := range []host.View{{}, {Progress: 7}} {
		p, product := HandoffNames(PreviewLockID, v), HandoffNames(host.MainInstanceID, v)
		if p.Mutex == product.Mutex || p.Class == product.Class || p.Window == product.Window {
			t.Fatalf("preview names %+v meet the product's %+v", p, product)
		}
	}
	if got, want := InstanceID(PreviewLockID, host.View{Progress: 7}), PreviewLockID+"-progress-7"; got != want {
		t.Fatalf("progress id %q, want %q", got, want)
	}
	dir, logs := filepath.FromSlash("C:/Users/u/AppData/Local/Downex"), filepath.FromSlash("C:/Users/u/AppData/Local/Downex/logs")
	for _, c := range []struct {
		name    string
		preview bool
		want    Files
	}{
		{"product", false, Files{
			Theme: filepath.Join(dir, "ui-theme.json"), Prefs: filepath.Join(dir, "ui-prefs.json"),
			WindowState: filepath.Join(dir, "ui-window.json"), LogDir: logs, LogName: "ui.log",
		}},
		{"preview", true, Files{
			Theme: filepath.Join(dir, "ui-theme-native-preview.json"), Prefs: filepath.Join(dir, "ui-prefs-native-preview.json"),
			WindowState: filepath.Join(dir, "ui-window-native-preview.json"), LogDir: logs, LogName: "ui-native-preview.log",
		}},
	} {
		if got := FilesOf(c.preview, dir, logs); got != c.want {
			t.Errorf("%s files %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := FilesOf(false, "", ""); got != (Files{LogName: "ui.log"}) {
		t.Errorf("files without folders %+v", got)
	}
	if got := FilesOf(false, dir, logs); got.WindowState != host.WindowStatePath(filepath.Join(dir, host.ThemeFileName)) {
		t.Errorf("window state %q is not the host's", got.WindowState)
	}
}
