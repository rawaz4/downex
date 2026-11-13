//go:build e2etest

package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"downex/internal/engine"
)

// The hooks of an e2etest build (internal/e2e builds it; never shipped):
//
//	DOWNEX_E2E_CLOCK=<RFC 3339 time>,<speed>  the schedule clock starts at that time and runs
//	                                           speed times faster than the real one
//	DOWNEX_E2E_POWER_LOG=<file>                power actions append "sleep", "hibernate" or
//	                                           "shutdown" (one line each) instead of happening
//	DOWNEX_E2E_ROOT_CA=<PEM file>              trusted instead of the system roots
//	DOWNEX_E2E_UPDATE_KEY=<base64 key>         the update signing key
//	DOWNEX_E2E_TOOLS_BASE=<https base>         replaces https://github.com for yt-dlp and Deno
//	                                           downloads, which this daemon then makes although
//	                                           it is not the user's own
//	DOWNEX_E2E_YTDLP_KEY=<armored key | file>  the OpenPGP key of yt-dlp's checksum signatures
//
// Every DOWNEX_E2E_* variable is passed on to the yt-dlp and Deno children
// (docs/SITE-MEDIA-SPEC.md §8.3): the stand-in tools read DOWNEX_E2E_FAKE_YTDLP and
// DOWNEX_E2E_FAKE_LOG.
const (
	envE2EClock     = "DOWNEX_E2E_CLOCK"
	envE2EPowerLog  = "DOWNEX_E2E_POWER_LOG"
	envE2ERootCA    = "DOWNEX_E2E_ROOT_CA"
	envE2EUpdateKey = "DOWNEX_E2E_UPDATE_KEY"
	envE2EToolsBase = "DOWNEX_E2E_TOOLS_BASE"
	envE2EYtDlpKey  = "DOWNEX_E2E_YTDLP_KEY"
	envE2EPrefix    = "DOWNEX_E2E_"
)

func loadTestHooks() (testHooks, error) {
	var h testHooks
	if v := os.Getenv(envE2EClock); v != "" {
		at, speedText, ok := strings.Cut(v, ",")
		start, err := time.Parse(time.RFC3339, at)
		speed, serr := strconv.ParseFloat(speedText, 64)
		if !ok || err != nil || serr != nil || speed <= 0 {
			return h, fmt.Errorf("%s=%q: want <RFC 3339 time>,<speed>", envE2EClock, v)
		}
		h.clock = &scaledClock{base: start, start: time.Now(), speed: speed}
	}
	if p := os.Getenv(envE2EPowerLog); p != "" {
		h.power = &powerLog{path: p}
	}
	if p := os.Getenv(envE2ERootCA); p != "" {
		pem, err := os.ReadFile(p)
		if err != nil {
			return h, fmt.Errorf("%s: %w", envE2ERootCA, err)
		}
		h.roots = x509.NewCertPool()
		if !h.roots.AppendCertsFromPEM(pem) {
			return h, fmt.Errorf("%s: no certificate in %s", envE2ERootCA, p)
		}
	}
	h.updateKey = os.Getenv(envE2EUpdateKey)
	h.toolsBase = os.Getenv(envE2EToolsBase)
	if v := os.Getenv(envE2EYtDlpKey); v != "" {
		// The armored key itself, or the file that holds it.
		h.ytdlpKey = []byte(v)
		if !strings.Contains(v, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
			key, err := os.ReadFile(v)
			if err != nil {
				return h, fmt.Errorf("%s: %w", envE2EYtDlpKey, err)
			}
			h.ytdlpKey = key
		}
	}
	h.passEnv = envE2EPrefix
	return h, nil
}

// scaledClock is a clock that starts at base and runs speed times faster than real time.
type scaledClock struct {
	base, start time.Time
	speed       float64
}

func (c *scaledClock) Now() time.Time {
	return c.base.Add(time.Duration(float64(time.Since(c.start)) * c.speed))
}

func (c *scaledClock) real(d time.Duration) time.Duration { return time.Duration(float64(d) / c.speed) }

func (c *scaledClock) NewTimer(d time.Duration) engine.Timer {
	return &scaledTimer{c: c, t: time.NewTimer(c.real(d))}
}

func (c *scaledClock) NewTicker(d time.Duration) engine.Ticker {
	return scaledTicker{time.NewTicker(max(c.real(d), time.Millisecond))}
}

type scaledTimer struct {
	c *scaledClock
	t *time.Timer
}

func (t *scaledTimer) C() <-chan time.Time        { return t.t.C }
func (t *scaledTimer) Stop() bool                 { return t.t.Stop() }
func (t *scaledTimer) Reset(d time.Duration) bool { return t.t.Reset(t.c.real(d)) }

type scaledTicker struct{ t *time.Ticker }

func (t scaledTicker) C() <-chan time.Time { return t.t.C }
func (t scaledTicker) Stop()               { t.t.Stop() }

// powerLog records power actions in a file.
type powerLog struct {
	mu   sync.Mutex
	path string
}

func (p *powerLog) record(action string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(action + "\n")
	return errors.Join(werr, f.Close())
}

func (p *powerLog) Sleep() error       { return p.record("sleep") }
func (p *powerLog) Hibernate() error   { return p.record("hibernate") }
func (p *powerLog) Shutdown() error    { return p.record("shutdown") }
func (p *powerLog) CanHibernate() bool { return true }
