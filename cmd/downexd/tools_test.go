package main

import (
	"path/filepath"
	"testing"
	"time"

	"downex/internal/daemon"
	"downex/internal/platform"
	"downex/internal/tools"
)

// TestToolsFolder: --tools-dir wins; otherwise the user's own daemon uses the per-user
// tools folder and every other daemon a folder next to its database, so that tests and
// private instances never touch the user's copies (docs/SITE-MEDIA-SPEC.md §3.9).
func TestToolsFolder(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state", "downex.db")
	flag := filepath.Join(t.TempDir(), "my tools")
	if got, err := toolsFolder(flag, true, db); err != nil || got != flag {
		t.Errorf("--tools-dir: %q, %v; want %q", got, err, flag)
	}
	if got, err := toolsFolder("", false, db); err != nil || got != filepath.Join(filepath.Dir(db), "tools") {
		t.Errorf("private daemon: %q, %v", got, err)
	}
	want, werr := platform.ToolsDir()
	if got, err := toolsFolder("", true, db); got != want || (err == nil) != (werr == nil) {
		t.Errorf("own daemon: %q, %v; want %q, %v", got, err, want, werr)
	}
	rel, err := toolsFolder("relative", false, db)
	if err != nil || !filepath.IsAbs(rel) {
		t.Errorf("relative --tools-dir: %q, %v; want an absolute path", rel, err)
	}
}

// TestToolsSnapshot: the manager's status reaches the daemon field by field, every state
// as the daemon's state of the same name.
func TestToolsSnapshot(t *testing.T) {
	states := map[tools.State]daemon.ToolState{
		tools.StateUnspecified: daemon.ToolStateUnknown,
		tools.StateMissing:     daemon.ToolStateMissing,
		tools.StateInstalling:  daemon.ToolStateInstalling,
		tools.StateReady:       daemon.ToolStateReady,
		tools.StateFailed:      daemon.ToolStateFailed,
		tools.StateDisabled:    daemon.ToolStateDisabled,
		tools.State(99):        daemon.ToolStateUnknown,
	}
	for in, want := range states {
		if got := toolState(in); got != want {
			t.Errorf("toolState(%d) = %d, want %d", in, got, want)
		}
	}
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	in := tools.Snapshot{Enabled: true, Discovering: true, Tools: []tools.Info{
		{Name: tools.ToolYtDlp, State: tools.StateInstalling, Version: "2026.10.10.233141", Path: `C:\t\yt-dlp.exe`,
			Managed: true, LatestVersion: "2026.10.21", CheckedAt: at, BytesDone: 5, BytesTotal: -1, Error: "e",
			ErrorCode: tools.CodeNetwork, Verified: "openpgp+sha256", Signer: "ignored"},
		{Name: tools.ToolDeno, State: tools.StateDisabled},
	}}
	got := toolsSnapshot(in)
	want := daemon.ToolsSnapshot{Enabled: true, Discovering: true, Tools: []daemon.ToolSnapshot{
		{Name: "yt-dlp", State: daemon.ToolStateInstalling, Version: "2026.10.10.233141", Path: `C:\t\yt-dlp.exe`,
			Managed: true, LatestVersion: "2026.10.21", CheckedAt: at, BytesDone: 5, BytesTotal: -1, Error: "e",
			ErrorCode: "network", Verified: "openpgp+sha256"},
		{Name: "deno", State: daemon.ToolStateDisabled},
	}}
	if got.Enabled != want.Enabled || got.Ready != want.Ready || got.Discovering != want.Discovering ||
		len(got.Tools) != len(want.Tools) {
		t.Fatalf("toolsSnapshot = %+v, want %+v", got, want)
	}
	for i := range want.Tools {
		if got.Tools[i] != want.Tools[i] {
			t.Errorf("tool %d = %+v, want %+v", i, got.Tools[i], want.Tools[i])
		}
	}
	if got := toolsSnapshot(tools.Snapshot{}); got.Tools != nil {
		t.Errorf("an empty status gained tools: %+v", got)
	}
}
