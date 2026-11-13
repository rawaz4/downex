//go:build darwin && cgo

package glue

import (
	"testing"

	"downex/internal/client"
	"downex/ui/host"
)

// TestEachBundleHostsItsOwnWindows: Downex.app's executable hosts the main window under
// today's main lock, the progress app's every progress window under a lock of its own, and
// a development build every window.
func TestEachBundleHostsItsOwnWindows(t *testing.T) {
	const app = "/Applications/Downex.app/Contents"
	tests := []struct {
		exe  string
		role Role
		lock string
	}{
		{app + "/MacOS/Downex", RoleMain, host.MainInstanceID},
		{app + "/Helpers/Downex Progress.app/Contents/MacOS/Downex", RoleProgress, host.MainInstanceID + "-progress-host"},
		{"/Users/me/src/downex/downex-ui", RoleAll, host.MainInstanceID},
	}
	for _, tc := range tests {
		l := client.LayoutOf("darwin", tc.exe)
		r := RoleOf(l)
		if r != tc.role {
			t.Errorf("%s: role %d, want %d", tc.exe, r, tc.role)
		}
		if got := hostLockID(host.MainInstanceID, r); got != tc.lock {
			t.Errorf("%s: lock %q, want %q", tc.exe, got, tc.lock)
		}
	}
	if !RoleMain.hostsMain() || RoleMain.hostsProgress() || RoleProgress.hostsMain() || !RoleProgress.hostsProgress() ||
		!RoleAll.hostsMain() || !RoleAll.hostsProgress() {
		t.Error("the roles' windows")
	}
	// A progress window's own lock is today's Wails progress lock.
	if got := InstanceID(host.MainInstanceID, host.View{Progress: 49}); got != host.ProgressInstanceID(49) {
		t.Errorf("progress lock %q", got)
	}
}
