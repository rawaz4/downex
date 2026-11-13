package daemon

import (
	"fmt"
	"os"
	"testing"

	"downex/internal/engine"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// TestCorruptProgressAsksForRestart (L8): saved progress that does not match the part
// file (truncated externally, or a crash during a restart) can only be recovered by a
// restart, like a changed remote file. It is reported with the code that tells clients
// so (validator_changed), not as "internal", and Resume with restart recovers it.
func TestCorruptProgressAsksForRestart(t *testing.T) {
	corrupt := fmt.Errorf("resume: %w: part file has 0 bytes, expected 9", engine.ErrCorruptCheckpoint)
	if got := ErrorCode(corrupt); got != CodeValidatorChanged {
		t.Errorf("ErrorCode(corrupt checkpoint) = %q, want %q", got, CodeValidatorChanged)
	}
	if got := codeFromMessage(corrupt.Error()); got != CodeValidatorChanged {
		t.Errorf("codeFromMessage(corrupt checkpoint) = %q, want %q", got, CodeValidatorChanged)
	}

	const size, seed = 4 << 20, 71
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"c"`})
	h := newHarness(t, harnessOptions{})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/corrupt.bin", SpeedLimit: 512 << 10})
	h.waitReceived(d.GetId(), 64<<10)
	// The paused download's record is the one the engine stored (a running one's live
	// view may not know the file name yet: Manager.refreshLive reads it asynchronously).
	p := h.pause(d.GetId())
	if p.GetState() != stPaused || p.GetFilename() == "" {
		t.Fatalf("pause = %v", p)
	}
	if err := os.Truncate(engine.PartPath(h.dir, p.GetFilename()), 0); err != nil {
		t.Fatal(err)
	}
	unlimited := int64(0)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), SpeedLimit: &unlimited})
	failed := h.waitState(d.GetId(), stFailed)
	if failed.GetErrorCode() != CodeValidatorChanged {
		t.Fatalf("error code = %q (%s), want %q", failed.GetErrorCode(), failed.GetError(), CodeValidatorChanged)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true})
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
}
