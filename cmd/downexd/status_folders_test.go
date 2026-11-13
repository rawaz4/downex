package main

import (
	"path/filepath"
	"testing"
)

// TestStatusNamesFolders: downexd reports its data, log and tools folders in Status
// (Settings → About shows, copies and opens them; its diagnostics name them).
func TestStatusNamesFolders(t *testing.T) {
	endpoint := testEndpoint(t)
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	toolsDir := filepath.Join(dir, "tools")
	args := []string{"--db", filepath.Join(dir, "state", "downex.db"), "--endpoint", endpoint,
		"--log-dir", logDir, "--tools-dir", toolsDir}
	d := startDaemon(t, args)
	st := waitReady(t, dial(t, endpoint), d)
	if st.GetDataDir() != filepath.Join(dir, "state") {
		t.Errorf("data_dir = %q, want %q", st.GetDataDir(), filepath.Join(dir, "state"))
	}
	if st.GetLogDir() != logDir {
		t.Errorf("log_dir = %q, want %q", st.GetLogDir(), logDir)
	}
	if st.GetToolsDir() != toolsDir {
		t.Errorf("tools_dir = %q, want %q", st.GetToolsDir(), toolsDir)
	}
}
