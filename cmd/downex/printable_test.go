package main

import (
	"context"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// hostileError is what a TLS failure reports for a server whose certificate names
// carry terminal escapes (Go's verifier repeats the SAN dNSNames unquoted): an OSC
// window-title sequence ended by BEL, an 8-bit CSI and a bidirectional override.
const hostileError = "x509: certificate is valid for \x1b]0;PWNED-TITLE\x07evil.example, \u009b31m, \u202eexe.txt, not download.example"

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		`plain C:\Users\a b.iso`: `plain C:\Users\a b.iso`,
		"caf\u00e9 \u4e2d\u6587": "caf\u00e9 \u4e2d\u6587",
		"\x1b]52;c;SGVsbG8=\x07": `\x1b]52;c;SGVsbG8=\x07`,
		"a\r\nb\tc\x7f":          `a\x0d\x0ab\x09c\x7f`,
		"\u009b2J":               `\x9b2J`,
		"\u202egnp.exe":          `\u202egnp.exe`,
		"bad\xffbyte":            `bad\xffbyte`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDaemonStringsAreNotTerminalCommands (SEC-6): error messages, file names and URLs
// from the daemon (partly from remote servers) never reach the terminal as control
// sequences: not in the watch table, not in "add --wait" and not in ls.
func TestDaemonStringsAreNotTerminalCommands(t *testing.T) {
	failed := active(7, 100, 40)
	failed.State, failed.Error, failed.ErrorCode, failed.SpeedBps = stFailed, hostileError, "tls", 0
	failed.Filename = "name\x1b[2J.bin"
	check := func(what, out string) {
		t.Helper()
		if strings.ContainsAny(out, "\x1b\x07\u009b\u202e") {
			t.Errorf("%s printed control characters: %q", what, out)
		}
		if !strings.Contains(out, `\x1b]0;PWNED-TITLE\x07evil.example`) && !strings.Contains(out, `name\x1b[2J.bin`) {
			t.Errorf("%s lacks the escaped text: %q", what, out)
		}
	}

	f, h := newFakeDaemon(t)
	f.do(func() {
		f.watch = waitScript([]*downexv1.Event{snapshot(active(7, 100, 10))}, []*downexv1.Event{updated(failed)})
	})
	r := h.run(context.Background(), "add", "https://example.test/f.bin", "--wait")
	if r.code != exitError {
		t.Fatalf("add --wait: %v", r)
	}
	check("add --wait", r.stderr)

	f, h = newFakeDaemon(t)
	f.do(func() { f.downloads[7] = failed })
	r = h.run(context.Background(), "ls")
	if r.code != exitOK {
		t.Fatalf("ls: %v", r)
	}
	check("ls", r.stdout)

	if row := tableRow(4, failed); strings.ContainsAny(row, "\x1b\x07\u009b\u202e") {
		t.Errorf("watch row printed control characters: %q", row)
	}
}
