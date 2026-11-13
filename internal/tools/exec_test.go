package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// standin returns the test binary placed as standin(.exe) in a temporary folder.
func standin(t *testing.T) string {
	t.Helper()
	return standInAt(t, t.TempDir(), "standin")
}

func TestExecOutputAndExitCode(t *testing.T) {
	exe := standin(t)
	ctx := context.Background()
	// Arguments reach the child verbatim: there is no shell.
	args := []string{"a b", `"quoted"`, "&calc", "|more", "$(id)", "%PATH%", `back\slash\`, "", "ünïcödé"}
	res, err := Exec(ctx, ExecSpec{Path: exe, Args: append([]string{"echo"}, args...), Timeout: 30 * time.Second})
	if err != nil || res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("echo: %+v, %v", res, err)
	}
	got := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(res.Stdout), "\r\n", "\n"), "\n"), "\n")
	if !slices.Equal(got, args) {
		t.Errorf("arguments %q, want %q", got, args)
	}
	res, err = Exec(ctx, ExecSpec{Path: exe, Args: []string{"exit", "2"}, Timeout: 30 * time.Second})
	if err != nil || res.ExitCode != 2 || !bytes.Contains(res.StderrTail, []byte("exiting with 2")) {
		t.Errorf("exit 2: %+v, %v", res, err)
	}
	if _, err := Exec(ctx, ExecSpec{Path: "standin", Args: []string{"exit", "0"}}); !errors.Is(err, ErrExec) {
		t.Errorf("relative path: %v, want ErrExec", err)
	}
	if _, err := Exec(ctx, ExecSpec{Path: filepath.Join(t.TempDir(), "missing.exe")}); !errors.Is(err, ErrExec) {
		t.Errorf("missing program: %v, want ErrExec", err)
	}
	bad := filepath.Join(t.TempDir(), "notaprogram.exe")
	_ = os.WriteFile(bad, []byte("not a program"), 0o755)
	if _, err := Exec(ctx, ExecSpec{Path: bad}); !errors.Is(err, ErrExec) {
		t.Errorf("not a program: %v, want ErrExec", err)
	}
	dir := t.TempDir()
	res, err = Exec(ctx, ExecSpec{Path: exe, Args: []string{"env"}, Dir: dir, Timeout: 30 * time.Second})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("env with Dir: %+v, %v", res, err)
	}
}

func TestExecOutputLimits(t *testing.T) {
	exe := standin(t)
	ctx := context.Background()
	res, err := Exec(ctx, ExecSpec{Path: exe, Args: []string{"flood", "1000"}, MaxStdout: 1000, Timeout: 30 * time.Second})
	if err != nil || res.StdoutTruncated || len(res.Stdout) != 1000 || res.ExitCode != 0 {
		t.Errorf("exactly at the limit: truncated=%v len=%d exit=%d, %v", res.StdoutTruncated, len(res.Stdout), res.ExitCode, err)
	}
	res, err = Exec(ctx, ExecSpec{Path: exe, Args: []string{"flood", strconv.Itoa(8 << 20)}, MaxStdout: 64 << 10, Timeout: 30 * time.Second})
	if err != nil || !res.StdoutTruncated || len(res.Stdout) != 64<<10 || res.ExitCode != -1 {
		t.Errorf("over the limit: truncated=%v len=%d exit=%d, %v", res.StdoutTruncated, len(res.Stdout), res.ExitCode, err)
	}
	res, err = Exec(ctx, ExecSpec{Path: exe, Args: []string{"stderr", "10000"}, MaxStderr: 1000, Timeout: 30 * time.Second})
	tail := strings.ReplaceAll(string(res.StderrTail), "\r\n", "\n")
	if err != nil || len(res.StderrTail) != 1000 || !strings.HasSuffix(tail, "line 009999\n") || res.ExitCode != 1 {
		t.Errorf("stderr tail: len=%d exit=%d end=%q, %v", len(res.StderrTail), res.ExitCode, tail[max(0, len(tail)-20):], err)
	}
}

func TestExecTimeoutKillsTree(t *testing.T) {
	exe := standin(t)
	start := time.Now()
	res, err := Exec(context.Background(), ExecSpec{Path: exe, Args: []string{"spawn"}, Timeout: 3 * time.Second, MemoryLimit: DefaultMemoryLimit})
	if err != nil || !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("spawn: %+v, %v", res, err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Exec returned after %v", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", res.Stdout, err)
	}
	waitFor(t, "the grandchild to die", func() bool { return !processAlive(pid) })
}

func TestExecKillsLeftoversAfterExit(t *testing.T) {
	exe := standin(t)
	start := time.Now()
	res, err := Exec(context.Background(), ExecSpec{Path: exe, Args: []string{"spawn", "exit"}, Timeout: 60 * time.Second})
	if err != nil || res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("spawn exit: %+v, %v", res, err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("Exec waited %v for the grandchild", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", res.Stdout, err)
	}
	waitFor(t, "the grandchild to die", func() bool { return !processAlive(pid) })
}

func TestExecCancel(t *testing.T) {
	exe := standin(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	res, err := Exec(ctx, ExecSpec{Path: exe, Args: []string{"sleep", "60s"}})
	if !errors.Is(err, context.Canceled) || res.ExitCode != -1 || res.TimedOut {
		t.Errorf("cancel: %+v, %v", res, err)
	}
	if _, err := Exec(ctx, ExecSpec{Path: exe, Args: []string{"exit", "0"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled before start: %v", err)
	}
}

func TestExecEnvironmentAllowList(t *testing.T) {
	exe := standin(t)
	t.Setenv("DOWNEX_TOOLSTEST_SECRET", "leak")
	t.Setenv("DOWNEX_TOOLSTEST_PASS", "ok")
	tmp := t.TempDir()
	env := Environment(runtime.GOOS, os.Getenv, "DOWNEX_TOOLSTEST_P", os.Environ(), EnvSpec{TempDir: tmp, DenoCache: tmp})
	res, err := Exec(context.Background(), ExecSpec{Path: exe, Args: []string{"env"}, Env: env, Timeout: 30 * time.Second})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("env: %+v, %v", res, err)
	}
	allowed := map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		allowed[strings.ToUpper(name)] = true
	}
	if runtime.GOOS == "windows" {
		allowed["SYSTEMROOT"] = true // os/exec adds it when missing
	}
	got := strings.Split(strings.ReplaceAll(string(res.Stdout), "\r\n", "\n"), "\n")
	seen := map[string]bool{}
	for _, kv := range got {
		if kv == "" || strings.HasPrefix(kv, "=") { // Windows' per-drive "=C:=C:\..." entries
			continue
		}
		name, _, _ := strings.Cut(kv, "=")
		if !allowed[strings.ToUpper(name)] {
			t.Errorf("child got %q, which the allow-list does not hold", kv)
		}
		seen[strings.ToUpper(name)] = true
	}
	for _, name := range []string{"DOWNEX_TOOLSTEST_PASS", "TEMP", "PATH", "PYTHONUTF8", "DENO_DIR"} {
		if !seen[name] {
			t.Errorf("child lacks %s", name)
		}
	}
	if seen["DOWNEX_TOOLSTEST_SECRET"] {
		t.Error("a variable outside the prefix reached the child")
	}
	// A nil environment is empty, never inherited.
	res, err = Exec(context.Background(), ExecSpec{Path: exe, Args: []string{"env"}, Timeout: 30 * time.Second})
	if err != nil || bytes.Contains(res.Stdout, []byte("DOWNEX_TOOLSTEST_")) {
		t.Errorf("nil environment inherited: %q, %v", res.Stdout, err)
	}
}

func TestNewRunDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "run")
	a, err := NewRunDir(root)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewRunDir(root)
	if a == b || filepath.Dir(a) != root || len(filepath.Base(a)) != 16 {
		t.Errorf("run dirs %s, %s", a, b)
	}
	if fi, err := os.Stat(filepath.Join(a, "tmp")); err != nil || !fi.IsDir() {
		t.Errorf("no tmp inside: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(a); fi.Mode().Perm() != 0o700 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	if _, err := NewRunDir("relative"); err == nil {
		t.Error("relative root accepted")
	}
}
