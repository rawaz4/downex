package tools

import (
	"path/filepath"
	"testing"
)

// LookAheadEnv exposes a test manager (testEnv) to the tests of package tools_test,
// which run it under the real resolver (internal/sitejob imports this package, so they
// cannot live in it).
type LookAheadEnv struct{ e *testEnv }

// NewLookAheadEnv returns a manager wired to a release server and an empty PATH.
func NewLookAheadEnv(t *testing.T) *LookAheadEnv { return &LookAheadEnv{e: newTestEnv(t)} }

// Manager is the manager under test.
func (x *LookAheadEnv) Manager() *Manager { return x.e.m }

// Publish publishes yt-dlp (nightly) tag yt and Deno version deno on the release server.
func (x *LookAheadEnv) Publish(t *testing.T, yt, deno string) { x.e.publishBoth(t, yt, deno) }

// PathCopies puts stand-in copies of yt-dlp yt and Deno deno on PATH. A yt-dlp given a
// stderr text prints it for every extraction and exits 1.
func (x *LookAheadEnv) PathCopies(t *testing.T, yt, deno, stderr string) {
	t.Helper()
	bin := t.TempDir()
	p := standInAt(t, filepath.Join(bin, yt), ToolYtDlp)
	if stderr != "" {
		writeStandInStderr(t, filepath.Dir(p), stderr)
	}
	x.e.setPath(ToolYtDlp, p)
	x.e.setPath(ToolDeno, standInAt(t, filepath.Join(bin, deno), ToolDeno))
}

// Requests is the number of requests the release server answered.
func (x *LookAheadEnv) Requests() int { return x.e.rs.total() }

// Trial reports a pending fallback or rollback trial (§4.5).
func (x *LookAheadEnv) Trial() bool {
	x.e.m.mu.Lock()
	defer x.e.m.mu.Unlock()
	return x.e.m.trial != nil
}

// Unsuitable is the number of PATH copies of yt-dlp marked unsuitable.
func (x *LookAheadEnv) Unsuitable() int {
	x.e.m.mu.Lock()
	defer x.e.m.mu.Unlock()
	return len(x.e.m.st.YtDlp.Unsuitable)
}
