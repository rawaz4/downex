package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/internal/ipc"
)

// TestCheckElevation (SEC-2): an elevated daemon is refused unless explicitly allowed,
// and so is one whose elevation cannot be determined.
func TestCheckElevation(t *testing.T) {
	yes := func() (bool, error) { return true, nil }
	no := func() (bool, error) { return false, nil }
	broken := func() (bool, error) { return false, errors.New("token query failed") }
	if err := checkElevation(yes, false); !errors.Is(err, errElevated) {
		t.Errorf("elevated: %v, want errElevated", err)
	}
	if err := checkElevation(yes, true); err != nil {
		t.Errorf("elevated with --allow-elevated: %v", err)
	}
	if err := checkElevation(no, false); err != nil {
		t.Errorf("not elevated: %v", err)
	}
	if err := checkElevation(broken, false); err == nil || !strings.Contains(err.Error(), "--allow-elevated") {
		t.Errorf("unknown elevation: %v, want a refusal naming --allow-elevated", err)
	}
	// The flag is accepted.
	if _, ok, code := parseFlags([]string{"--allow-elevated"}, &bytes.Buffer{}, &bytes.Buffer{}); !ok || code != exitOK {
		t.Errorf("--allow-elevated: ok %v code %d", ok, code)
	}
}

// TestStartsOnceExitingDaemonReleasesLock (L6): a daemon started while the previous
// one is still exiting (it holds the instance lock, its endpoint is already gone) waits
// for the lock and starts, instead of failing because nothing answers.
func TestStartsOnceExitingDaemonReleasesLock(t *testing.T) {
	endpoint := testEndpoint(t)
	release, err := ipc.AcquireInstanceLock(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	args := []string{"--db", filepath.Join(dir, "downex.db"), "--endpoint", endpoint, "--log-dir", filepath.Join(dir, "logs")}
	d := startDaemon(t, args)
	time.Sleep(500 * time.Millisecond) // the old daemon is closing its database
	release()
	c := dial(t, endpoint)
	st := waitReady(t, c, d)
	if st.GetEndpoint() != endpoint {
		t.Fatalf("status = %v", st)
	}
	if strings.Contains(d.stdout.String(), "already running") {
		t.Fatalf("the new daemon reported a running one: %s", d.stdout.String())
	}
}

// TestOneDaemonPerDatabase (L10): a second daemon on another endpoint but the same
// database refuses to start; it would schedule the first one's downloads too.
func TestOneDaemonPerDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "downex.db")
	first := testEndpoint(t)
	d1 := startDaemon(t, []string{"--db", db, "--endpoint", first, "--log-dir", filepath.Join(dir, "logs1")})
	waitReady(t, dial(t, first), d1)

	second := testEndpoint(t)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"--db", db, "--endpoint", second, "--log-dir", filepath.Join(dir, "logs2")}, &out, &errOut)
	if code != exitError || !strings.Contains(errOut.String(), "in use by another downexd") {
		t.Fatalf("second daemon on the same database: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}
