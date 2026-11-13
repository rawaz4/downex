//go:build darwin && cgo

package glue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/mac"
)

var lockSeq atomic.Int32

// child is a helper process: this test binary with $GLUE_HELPER.
type child struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string
	done  chan error
}

type helperEnv struct{ lock, dir, locks string }

func newHelperEnv(t *testing.T) helperEnv {
	return helperEnv{
		lock:  fmt.Sprintf("downex-glue-mactest-%d-%d", os.Getpid(), lockSeq.Add(1)),
		dir:   t.TempDir(),
		locks: shortTemp(t),
	}
}

func startHelper(t *testing.T, part string, e helperEnv, args ...string) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	a, _ := json.Marshal(args)
	cmd.Env = append(os.Environ(), envHelper+"="+part, envLock+"="+e.lock, envDir+"="+e.dir, envLocks+"="+e.locks,
		envArgs+"="+string(a))
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, in: in, lines: make(chan string, 32), done: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
		c.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		in.Close()
		select {
		case err := <-c.done:
			c.done <- err
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill() // this test's own child only
			t.Error("a helper process did not end")
		}
	})
	return c
}

func (c *child) line(t *testing.T, d time.Duration) string {
	t.Helper()
	select {
	case l, ok := <-c.lines:
		if !ok {
			t.Fatal("the helper ended without its line")
		}
		return l
	case <-time.After(d):
		t.Fatalf("no line from the helper within %v", d)
	}
	return ""
}

// ask sends a command and returns its answer.
func (c *child) ask(t *testing.T, cmd string) string {
	t.Helper()
	if _, err := fmt.Fprintln(c.in, cmd); err != nil {
		t.Fatal(err)
	}
	return c.line(t, 20*time.Second)
}

// exitCode waits at most d for the child to end: its exit code, -1 when it did not end.
func (c *child) exitCode(d time.Duration) int {
	select {
	case err := <-c.done:
		c.done <- err
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return -2
	case <-time.After(d):
		return -1
	}
}

// waitState asks for the host's state until it contains want (at most d).
func (c *child) waitState(t *testing.T, want string, d time.Duration) string {
	t.Helper()
	end := time.Now().Add(d)
	for {
		s := c.ask(t, "state")
		if strings.Contains(s, want) || time.Now().After(end) {
			return s
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestTheSystemQuitEndsTheWindowsFirst: the quit Apple event (logout, restart, shut down, the
// Dock's Quit) reaches applicationShouldTerminate:, which postpones the termination
// (NSTerminateLater) while the windows' sessions end and their prefs are written, then lets it
// go on: the process exits 0 with the prefs on disk.
func TestTheSystemQuitEndsTheWindowsFirst(t *testing.T) {
	// The quit Apple event (handed to NSApplication's handler), and terminate: from inside a
	// main-queue block (the reply once waited behind it for ever).
	for _, how := range []string{"quit-event", "terminate"} {
		t.Run(how, func(t *testing.T) { systemQuit(t, how) })
	}
}

func systemQuit(t *testing.T, how string) {
	e := newHelperEnv(t)
	c := startHelper(t, "host", e)
	if l := c.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("host: %q", l)
	}
	if a := c.ask(t, "set native.test.quit yes"); a != "ok" {
		t.Fatalf("set: %s", a)
	}
	start := time.Now()
	if a := c.ask(t, how); a != "sent=true" && a != "ok" {
		t.Fatalf("%s: %s", how, a)
	}
	if code := c.exitCode(10 * time.Second); code != 0 {
		t.Fatalf("after %s the process exited %d", how, code)
	}
	t.Logf("ended %v after %s", time.Since(start).Round(time.Millisecond), how)
	files := FilesOf(IsPreview(e.lock), e.dir, e.dir)
	b, err := os.ReadFile(files.Prefs)
	if err != nil || !strings.Contains(string(b), `"native.test.quit"`) {
		t.Errorf("prefs after the quit: %q %v", b, err)
	}
	logb, _ := os.ReadFile(filepath.Join(files.LogDir, files.LogName))
	if !strings.Contains(string(logb), "quit requested by the system") {
		t.Errorf("ui.log has no quit request:\n%s", logb)
	}
}

// TestALaterStartBringsTheWindowBack: a second start (this build's, over the socket, or an
// older Wails build's distributed notification) shows the minimised main window again and
// asks for the activation.
func TestALaterStartBringsTheWindowBack(t *testing.T) {
	e := newHelperEnv(t)
	c := startHelper(t, "host", e)
	if l := c.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("host: %q", l)
	}
	c.ask(t, "minimize")
	if s := c.waitState(t, "mini=true", 5*time.Second); !strings.Contains(s, "mini=true") {
		t.Fatalf("not minimised: %s", s)
	}
	second := startHelper(t, "second", e)
	if l := second.line(t, 20*time.Second); l != "handed=true err=<nil>" {
		t.Fatalf("second start: %s", l)
	}
	if code := second.exitCode(10 * time.Second); code != 0 {
		t.Fatalf("second start exited %d", code)
	}
	s := c.waitState(t, "mini=false visible=", 5*time.Second)
	if !strings.Contains(s, "visible=true mini=false") {
		t.Errorf("after a second start: %s", s)
	}
	t.Logf("after a second start: %s", s)

	// An older build's start (Wails v2.17) posts its hand-off to the lock's name.
	c.ask(t, "minimize")
	c.waitState(t, "mini=true", 5*time.Second)
	payload, _ := handoffJSON([]string{}, "/Applications/Downex.app/Contents/MacOS")
	mac.PostDistributed(e.lock, string(payload))
	s = c.waitState(t, "visible=true mini=false", 5*time.Second)
	if !strings.Contains(s, "visible=true mini=false") {
		t.Errorf("after an older build's start: %s", s)
	}
	t.Logf("after an older build's start: %s", s)
}

// TestAStartHandsOverToAnOlderBuild: when one of today's Wails windows holds the lock and no
// native host answers, a start posts Wails' own hand-off (the JSON as the notification's
// object) to the lock's name and exits.
func TestAStartHandsOverToAnOlderBuild(t *testing.T) {
	e := newHelperEnv(t)
	wails := startHelper(t, "wails-standin", e)
	if l := wails.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("stand-in: %q", l)
	}
	second := startHelper(t, "second", e, "--confirm", "5")
	if l := second.line(t, 20*time.Second); l != "handed=true err=<nil>" {
		t.Fatalf("start: %s", l)
	}
	got := wails.line(t, 10*time.Second)
	var h host.Handoff
	if err := json.Unmarshal([]byte(got), &h); err != nil || strings.Join(h.Args, " ") != "--confirm 5" {
		t.Errorf("Wails received %q", got)
	}
}

// TestThemeFollowsAnotherProcess: another process's ui-theme.json (today's window, or the
// other bundle's) reaches the open window.
func TestThemeFollowsAnotherProcess(t *testing.T) {
	e := newHelperEnv(t)
	c := startHelper(t, "host", e)
	if l := c.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("host: %q", l)
	}
	for _, choice := range []host.ThemeChoice{host.ThemeDark, host.ThemeLight, host.ThemeSystem} {
		at := time.Now()
		if err := host.SaveThemeChoice(FilesOf(IsPreview(e.lock), e.dir, e.dir).Theme, choice); err != nil {
			t.Fatal(err)
		}
		s := c.waitState(t, "theme="+string(choice), 3*time.Second)
		if !strings.Contains(s, "theme="+string(choice)) {
			t.Errorf("%s not followed: %s", choice, s)
			continue
		}
		t.Logf("%s followed within %v", choice, time.Since(at).Round(10*time.Millisecond))
	}
}

// TestTheFolderPickerIsASheet: Settings' Browse… (the views' Pickers) shows the open panel as
// a sheet on the window, with no message line (Wails sets only the title, which a sheet does
// not show), and Cancel answers "".
func TestTheFolderPickerIsASheet(t *testing.T) {
	e := newHelperEnv(t)
	c := startHelper(t, "host", e)
	if l := c.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("host: %q", l)
	}
	a := c.ask(t, "pick "+e.dir)
	t.Log(a)
	if !strings.HasPrefix(a, "sheet NSOpenPanel title=Choose a folder message= ") || !strings.HasSuffix(a, `Cancel true: path="" err=<nil>`) {
		t.Errorf("picker: %s", a)
	}
}

// TestTheRiskyFileAlertIsTodays: opening an installer asks first in today's alert (Yes, No):
// No is the window's default button (Return), no button takes Escape, as in Wails, and an
// Escape sent to the alert leaves it open; No opens nothing. (Keys reach an inactive test
// app's alert only through the window; Return's press itself is not simulated.)
func TestTheRiskyFileAlertIsTodays(t *testing.T) {
	e := newHelperEnv(t)
	c := startHelper(t, "host", e)
	if l := c.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("host: %q", l)
	}
	a := c.ask(t, "risky")
	t.Log(a)
	parts := strings.Split(a, " => ")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "modal ") || !strings.Contains(parts[0], "Run this program?|setup.pkg") ||
		!strings.Contains(parts[0], "Yes[") || !strings.Contains(parts[0], "No[") || !strings.Contains(parts[0], " default=No") ||
		strings.Contains(parts[0], `[\e]`) {
		t.Fatalf("alert: %s", a)
	}
	if !strings.HasPrefix(parts[1], "after Escape: modal ") {
		t.Errorf("Escape closed the alert: %s", parts[1])
	}
	if parts[2] != "No: err=<nil>" {
		t.Errorf("No: %s", parts[2])
	}
}

// TestYieldActivationDoesNotHoldTheWindow: the main window yields the activation to the
// progress app before it starts it (host.AllowForegroundHandoff), from a goroutine, while
// the window runs; it returns at once.
func TestYieldActivationDoesNotHoldTheWindow(t *testing.T) {
	e := newHelperEnv(t)
	c := startHelper(t, "host", e)
	if l := c.line(t, 20*time.Second); l != "ready" {
		t.Fatalf("host: %q", l)
	}
	for _, b := range []string{"/System/Applications/Calculator.app", "/nonexistent/Downex Progress.app"} {
		a := c.ask(t, "yield "+strings.ReplaceAll(b, " ", "_"))
		var ms int
		if _, err := fmt.Sscanf(a, "took=%d", &ms); err != nil || ms > 250 {
			t.Errorf("yield to %s: %s", b, a)
		}
	}
}

// TestQuitWithEveryWindowMinimised: ⌘Q and Close Window end the main window's process with
// no key, main or visible window (every one minimised), as today's Wails ⌘Q did.
func TestQuitWithEveryWindowMinimised(t *testing.T) {
	for _, key := range []string{"q", "w"} {
		t.Run(key, func(t *testing.T) {
			e := newHelperEnv(t)
			c := startHelper(t, "host", e)
			if l := c.line(t, 20*time.Second); l != "ready" {
				t.Fatalf("host: %q", l)
			}
			c.ask(t, "minimize")
			if s := c.waitState(t, "mini=true", 5*time.Second); !strings.Contains(s, "key=false visible=false mini=true") {
				t.Fatalf("not minimised: %s", s)
			}
			if a := c.ask(t, "menukey "+key); a != "took=true" {
				t.Fatalf("⌘%s: %s", key, a)
			}
			if code := c.exitCode(10 * time.Second); code != 0 {
				t.Fatalf("⌘%s with every window minimised: exit %d", key, code)
			}
		})
	}
}

// TestAPanicInAttachClosesTheWindow: a window whose root panics in Attach closes and its
// open fails, as on Windows: the host knows the window before Attach (windowMade). Before,
// the panic could not find it, so the broken window was shown and the process kept it.
func TestAPanicInAttachClosesTheWindow(t *testing.T) {
	e := newHelperEnv(t)
	c := startHelper(t, "attach-panic", e)
	if l := c.line(t, 20*time.Second); l != "run failed=true visible=0 windows=0" {
		t.Errorf("helper: %q", l)
	}
	if code := c.exitCode(10 * time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
}
