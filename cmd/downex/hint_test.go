package main

import (
	"context"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// TestCorruptProgressHint (L8): a download whose saved progress does not match its part
// file is reported with the restart hint (plain resume would fail the same way again),
// and with the right explanation.
func TestCorruptProgressHint(t *testing.T) {
	failed := active(7, 100, 40)
	failed.State, failed.ErrorCode, failed.SpeedBps = stFailed, "validator_changed", 0
	failed.Error = "resume: engine: stored progress is inconsistent: part file has 0 bytes, expected 100"
	f, h := newFakeDaemon(t)
	f.do(func() { f.watch = waitScript([]*downexv1.Event{updated(failed)}) })
	r := h.run(context.Background(), "add", "https://example.test/f.bin", "--wait")
	if r.code != exitError {
		t.Fatalf("add --wait: %v", r)
	}
	for _, want := range []string{"does not match the partial file", "downex resume 7 --restart"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}
	if strings.Contains(r.stderr, "server changed") || strings.Contains(r.stderr, "Try again with: downex resume 7\n") {
		t.Errorf("wrong hint:\n%s", r.stderr)
	}
}
