package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// Polite mode (docs/POLITE-SPEC.md) with real executables: the e2etest build of downexd
// answers its probes from a file instead of the network (DOWNEX_E2E_POLITE_RTT, the round
// trip in whole milliseconds; no file = no reply) and takes the loopback test server as an
// anchor (DOWNEX_E2E_POLITE_LOOPBACK=1). The real controller, monitor, peer table, limiter
// chain, settings, Status and CLI run unchanged.

// Environment of the e2etest build (cmd/downexd/testhooks_e2e.go).
const (
	envHookPoliteRTT      = "DOWNEX_E2E_POLITE_RTT"
	envHookPoliteLoopback = "DOWNEX_E2E_POLITE_LOOPBACK"
)

// politeTimeout bounds each phase: a calibration (≤ 4 s after 5 s untrusted), an engage
// (2 ticks after the window holds the new delay), NO_SIGNAL (10 s unknown) and the rise
// back from the floor (the increase is bounded by 1.5 × the 10 s rate).
const politeTimeout = 90 * time.Second

// setRTT makes the probes answer after ms milliseconds (0: removes the file, no reply).
// The file is replaced atomically: the daemon reads it at every probe.
func setRTT(t *testing.T, path string, ms int) {
	t.Helper()
	if ms <= 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(ms)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// waitPolite polls Status until ok accepts Polite mode's status, and returns it.
func (h *harness) waitPolite(what string, ok func(*downexv1.PoliteStatus) bool) *downexv1.PoliteStatus {
	h.t.Helper()
	deadline := time.Now().Add(politeTimeout)
	var last *downexv1.PoliteStatus
	for {
		st, err := h.status()
		if err == nil {
			last = st.GetPolite()
			if ok(last) {
				return last
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("Polite mode did not reach %s within %v: last status %v (err %v); its log:\n%s",
				what, politeTimeout, last, err, h.politeLog())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// politeLog returns the daemon log's Polite mode lines.
func (h *harness) politeLog() string {
	b, err := os.ReadFile(filepath.Join(h.logDir, "downexd.log"))
	if err != nil {
		return err.Error()
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(strings.ToLower(line), "polite") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// Polite mode on a throttled download: full speed while the line delay stays at its base,
// slowed (and the download really slower) once the probes' round trip grows by 130 ms,
// back up when it falls again, and full speed without a signal once nothing answers.
func TestPoliteMode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const throttle = 2 << 20
	srv := newServer(t, testserver.Options{Size: 512 << 20, Seed: 61, ETag: `"p1"`, Throttle: throttle})
	rtt := filepath.Join(h.root, "polite-rtt")
	setRTT(t, rtt, 20)
	d := h.startHooksDaemon(map[string]string{envHookPoliteRTT: rtt, envHookPoliteLoopback: "1"})

	if st, err := h.status(); err != nil || st.GetPolite().GetState() != downexv1.PoliteState_POLITE_STATE_OFF {
		t.Fatalf("a new installation's Polite mode = %v (err %v), want off", st.GetPolite(), err)
	}
	h.cli("settings", "--polite", "on").ok(t)
	if st, _ := h.status(); st.GetPolite().GetState() != downexv1.PoliteState_POLITE_STATE_IDLE {
		t.Errorf("on with nothing downloading = %v, want idle", st.GetPolite())
	}

	id := h.add(srv.URL+"/dl?id=1", "-o", h.out, "-c", "1")
	h.waitReceived(id, 256<<10)

	// 1. A smooth line: full speed with a known delay (after a calibration when the first
	// sample came under load).
	h.waitPolite("watching with a known delay", func(p *downexv1.PoliteStatus) bool {
		return p.GetState() == downexv1.PoliteState_POLITE_STATE_WATCHING && p.GetDelayMs() >= 0
	})
	if r := h.cli("daemon", "status").ok(t); !strings.Contains(r.stdout, "on, full speed (line delay +") {
		t.Errorf("daemon status while watching: %v", r)
	}

	// 2. The line lags: slowed, down to the floor while the delay stays high, and the
	// download receives far less than the server's 2 MiB/s.
	setRTT(t, rtt, 150)
	h.waitPolite("slowed", func(p *downexv1.PoliteStatus) bool {
		return p.GetState() == downexv1.PoliteState_POLITE_STATE_SLOWED && p.GetRateLimit() > 0
	})
	if r := h.cli("daemon", "status").ok(t); !strings.Contains(r.stdout, "slowed to ") ||
		!strings.Contains(r.stdout, "to keep the line smooth") {
		t.Errorf("daemon status while slowed: %v", r)
	}
	h.waitPolite("a cap under 256 KiB/s", func(p *downexv1.PoliteStatus) bool {
		return p.GetState() == downexv1.PoliteState_POLITE_STATE_SLOWED && p.GetRateLimit() < 256<<10
	})
	before := h.get(id).GetReceived()
	time.Sleep(3 * time.Second)
	if got := h.get(id).GetReceived() - before; got > 2<<20 {
		t.Errorf("received %d bytes in 3 s while slowed under 256 KiB/s (the server sends 6 MiB in that time)", got)
	}

	// 3. The line is quiet again: the cap rises back towards full speed (or is removed).
	setRTT(t, rtt, 20)
	h.waitPolite("a cap of 1 MiB/s or more, or none", func(p *downexv1.PoliteStatus) bool {
		return p.GetState() == downexv1.PoliteState_POLITE_STATE_WATCHING ||
			(p.GetState() == downexv1.PoliteState_POLITE_STATE_SLOWED && p.GetRateLimit() >= 1<<20)
	})

	// 4. Nothing answers: fail-open, full speed.
	setRTT(t, rtt, 0)
	h.waitPolite("no signal", func(p *downexv1.PoliteStatus) bool {
		return p.GetState() == downexv1.PoliteState_POLITE_STATE_NO_SIGNAL && p.GetRateLimit() == 0
	})
	if r := h.cli("daemon", "status").ok(t); !strings.Contains(r.stdout, "on, but this connection can't be measured: full speed") {
		t.Errorf("daemon status without a signal: %v", r)
	}

	// Off removes everything at once.
	h.cli("settings", "--polite", "off").ok(t)
	if st, _ := h.status(); st.GetPolite().GetState() != downexv1.PoliteState_POLITE_STATE_OFF || st.GetPolite().GetRateLimit() != 0 {
		t.Errorf("off = %v", st.GetPolite())
	}
	// The log has the state changes, never an address (rule 18).
	log := h.daemonLog()
	if !strings.Contains(log, "polite mode") {
		t.Errorf("the daemon log lacks Polite mode's state changes:\n%s", log)
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "polite") && strings.Contains(line, "127.0.0.1") {
			t.Errorf("a Polite mode log line names an address: %s", line)
		}
	}
	h.stop(d)
}
