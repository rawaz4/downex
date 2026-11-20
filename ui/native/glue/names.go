package glue

import (
	"path/filepath"
	"strconv"

	"downex/ui/host"
	"downex/ui/state/prefs"
)

// PreviewLockID is the native preview's own single-instance id: its locks never meet the
// shipped window's (host.MainInstanceID), so both run side by side until the switch.
const PreviewLockID = "downex-native-preview-3b9d2f64-8f4e-4c1a-b0a7-6e5d4c3b2a19"

// lockID is this build's single-instance id. A build of the product sets it to today's
// window's id at link time (build.ps1 -Product:
// -X downex/ui/native/glue.lockID=downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904); every
// other build keeps the preview's.
var lockID = PreviewLockID

// LockID is this build's single-instance id.
func LockID() string { return lockID }

// IsPreview reports a build whose locks and files are the preview's own: any id but today's
// window's.
func IsPreview(id string) bool { return id != host.MainInstanceID }

// InstanceID is the single-instance id of view v in a build whose id is base: base itself
// for the main window, "<base>-progress-<id>" for the progress window of download id (the
// rule of host.ProgressInstanceID, which it equals for today's id).
func InstanceID(base string, v host.View) string {
	if v.IsProgress() {
		return base + "-progress-" + strconv.FormatInt(v.Progress, 10)
	}
	if v.IsConfirm() {
		return base + "-confirm"
	}
	return base
}

// HandoffNames are the lock names of view v in a build whose id is base: today's
// (host.HandoffNamesOf) for today's id.
func HandoffNames(base string, v host.View) host.HandoffNames {
	return host.HandoffNamesOf(InstanceID(base, v))
}

// Files are the per-user files of a window process.
type Files struct {
	// Theme is the theme choice the windows read before they exist (ui-theme.json).
	Theme string
	// Prefs is the per-viewer conveniences (ui-prefs.json).
	Prefs string
	// WindowState is the main window's size (ui-window.json).
	WindowState string
	// LogDir and LogName are ui.log's folder (downexd's log folder) and name.
	LogDir, LogName string
}

// The preview's own files, beside today's: it never changes what today's window reads.
const (
	PreviewThemeFile       = "ui-theme-native-preview.json"
	PreviewPrefsFile       = prefs.PreviewFileName // ui-prefs-native-preview.json
	PreviewWindowStateFile = "ui-window-native-preview.json"
	PreviewLogName         = "ui-native-preview.log"
)

// FilesOf are the files of a build (preview or the product) whose ui-theme.json belongs in
// dir (the folder of host.DefaultThemePath: %LOCALAPPDATA%\Downex on Windows) and whose log
// belongs in logDir (platform.LogDir). An empty dir gives no theme, prefs or window-state
// file (each then lasts for the session); an empty logDir no log.
func FilesOf(preview bool, dir, logDir string) Files {
	f := Files{LogDir: logDir, LogName: host.LogName}
	theme, conv, state := host.ThemeFileName, prefs.FileName, host.WindowStateFileName
	if preview {
		theme, conv, state = PreviewThemeFile, PreviewPrefsFile, PreviewWindowStateFile
		f.LogName = PreviewLogName
	}
	if dir != "" {
		f.Theme = filepath.Join(dir, theme)
		f.Prefs = filepath.Join(dir, conv)
		f.WindowState = filepath.Join(dir, state)
	}
	return f
}
