//go:build windows || linux || darwin

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// helperToken follows "--" in the arguments of a test binary started as a helper.
const helperToken = "downex-platform-helper"

// helperTimeout bounds how long a helper waits for its go-file, so that a failed test
// never leaves one running.
const helperTimeout = 30 * time.Second

// helperCommand returns the arguments that run this test binary as a helper in mode.
func helperCommand(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestHelperProcess$", "--", helperToken, mode}, args...)
}

// TestHelperProcess is not a test: it is the body of the helper processes started by the
// StartDetached tests, and returns at once in a normal test run.
//
//	detached <go-file> <marker>: wait for go-file, then write marker (facts about itself)
//	spawn <go-file> <marker>:    StartDetached a "detached" helper, then exit at once
//	spawn-when <start-file> <go-file> <marker>: wait for start-file, then act as spawn
//	record <marker> <arg>...:    write marker with one line per arg, then exit
func TestHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || i+4 > len(os.Args) || os.Args[i+1] != helperToken {
		return
	}
	mode, args := os.Args[i+2], os.Args[i+3:]
	var err error
	switch {
	case mode == "record":
		err = writeMarker(args[0], args[1:])
	case mode == "detached" && len(args) == 2:
		err = runDetachedHelper(args[0], args[1])
	case mode == "spawn" && len(args) == 2:
		err = StartDetached(os.Args[0], helperCommand("detached", args...)...)
	case mode == "spawn-when" && len(args) == 3:
		if err = waitFile(args[0]); err == nil {
			err = StartDetached(os.Args[0], helperCommand("detached", args[1:]...)...)
		}
	default:
		err = fmt.Errorf("bad helper arguments %q", os.Args[i+1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// waitFile waits until the file at path exists, for at most helperTimeout.
func waitFile(path string) error {
	deadline := time.Now().Add(helperTimeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runDetachedHelper(goFile, marker string) error {
	if err := waitFile(goFile); err != nil {
		return err
	}
	facts := []string{fmt.Sprintf("pid=%d", os.Getpid())}
	if n, err := os.Stdin.Read(make([]byte, 1)); n == 0 && errors.Is(err, io.EOF) {
		facts = append(facts, "stdin=eof")
	}
	facts = append(facts, detachFacts()...)
	return writeMarker(marker, facts)
}

// writeMarker writes lines to marker, one per line.
func writeMarker(marker string, lines []string) error {
	tmp := marker + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, marker) // the parent never sees a partial marker
}

// awaitMarker waits for the helper's marker and returns its facts.
func awaitMarker(t *testing.T, marker string) []string {
	t.Helper()
	deadline := time.Now().Add(helperTimeout)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			return strings.Fields(string(data))
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read marker: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the detached helper's marker")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// checkFacts verifies the facts a detached helper reported about itself.
func checkFacts(t *testing.T, facts []string) {
	t.Helper()
	for _, want := range append([]string{"stdin=eof"}, wantDetachFacts(t)...) {
		if !slices.Contains(facts, want) {
			t.Errorf("detached helper reported %q, missing %q", facts, want)
		}
	}
}

// helperFiles returns a go-file and marker path; the go-file is created at cleanup in any
// case, so that a helper of a failed test exits promptly.
func helperFiles(t *testing.T) (goFile, marker string) {
	dir := t.TempDir()
	goFile, marker = filepath.Join(dir, "go"), filepath.Join(dir, "marker")
	t.Cleanup(func() {
		_ = os.WriteFile(goFile, nil, 0o644)
		// Give the helper a moment to see it and leave the directory before it is removed.
		time.Sleep(50 * time.Millisecond)
	})
	return goFile, marker
}

// TestStartDetachedReturnsBeforeChildWorks: StartDetached returns while the child is
// still running (it waits for a file the parent creates only afterwards), and the child
// then does its work, detached and with null standard input.
func TestStartDetachedReturnsBeforeChildWorks(t *testing.T) {
	goFile, marker := helperFiles(t)
	returned := make(chan error, 1)
	go func() { returned <- StartDetached(os.Args[0], helperCommand("detached", goFile, marker)...) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("StartDetached: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("StartDetached did not return while the child was running")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker exists before the parent let the child proceed (%v)", err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	checkFacts(t, awaitMarker(t, marker))
}

// TestStartDetachedOutlivesCaller: a process started detached keeps running after the
// process that started it has exited, and holds none of its handles: the starter's
// output pipe reaches EOF as soon as the starter exits.
func TestStartDetachedOutlivesCaller(t *testing.T) {
	goFile, marker := helperFiles(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], helperCommand("spawn", goFile, marker)...)
	cmd.WaitDelay = 5 * time.Second // a pipe inherited by the grandchild would hold Output open
	out, err := cmd.CombinedOutput()
	switch {
	case errors.Is(err, exec.ErrWaitDelay):
		t.Fatalf("the detached process inherited the starter's output pipe: %s", out)
	case err != nil:
		t.Fatalf("starter: %v: %s", err, out)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker exists before the parent let the child proceed (%v)", err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	checkFacts(t, awaitMarker(t, marker))
}

func TestStartDetachedMissingExecutable(t *testing.T) {
	err := StartDetached(filepath.Join(t.TempDir(), "no-such-program"))
	if err == nil {
		t.Fatal("StartDetached of a missing executable succeeded")
	}
}
