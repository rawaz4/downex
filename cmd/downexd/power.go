package main

import (
	"errors"
	"log/slog"
	"os"

	"downex/internal/daemon"
	"downex/internal/platform"
)

// powerSetup are the post-queue power actions, the wake timer and the keep-awake request
// of a daemon (docs/M6-SPEC.md §1).
type powerSetup struct {
	power daemon.Power     // nil: sleep, hibernate and shutdown are unavailable
	wake  daemon.WakeTimer // nil: schedules never wake the computer
	awake daemon.KeepAwake // nil: queues never keep the computer from idling into sleep
	close func()
}

// systemPower performs the power actions with the platform's functions.
type systemPower struct{}

func (systemPower) Sleep() error       { return platform.Sleep() }
func (systemPower) Hibernate() error   { return platform.Hibernate() }
func (systemPower) Shutdown() error    { return platform.Shutdown() }
func (systemPower) CanHibernate() bool { return platform.CanHibernate() }

// newWakeTimer creates the platform's wake timer; tests replace it.
var newWakeTimer = func() (wakeTimer, error) { return platform.NewWakeTimer() }

type wakeTimer interface {
	daemon.WakeTimer
	Close() error
}

// awakeReason is what Windows lists for downexd's power request ("powercfg /requests").
const awakeReason = "Downex is running a scheduled queue or one that acts when it is done"

// newAwakeRequest creates the platform's keep-awake request; tests replace it.
var newAwakeRequest = func() (awakeRequest, error) { return platform.NewAwakeRequest(awakeReason) }

type awakeRequest interface {
	daemon.KeepAwake
	Close() error
}

// powerHooks returns the power setup for the daemon described by o: only the user's own
// daemon (ownDaemon) may suspend or shut down the computer, arm a wake timer or keep the
// computer awake. A private instance (tests, scripts, other profiles) gets none of them,
// so it never powers anything off or keeps anything on; its queues refuse those
// post-queue actions.
func powerHooks(o options, log *slog.Logger) powerSetup {
	p := powerSetup{close: func() {}}
	if !ownDaemon(o, os.Getenv) {
		return p
	}
	p.power = systemPower{}
	var closers []func()
	w, err := newWakeTimer()
	switch {
	case err == nil:
		p.wake = w
		closers = append(closers, func() {
			if err := w.Close(); err != nil {
				log.Warn("close the wake timer", "err", err)
			}
		})
	case errors.Is(err, errors.ErrUnsupported): // only Windows has one
	default:
		log.Warn("scheduled queues cannot wake the computer", "err", err)
	}
	a, err := newAwakeRequest()
	switch {
	case err == nil:
		p.awake = a
		closers = append(closers, func() {
			if err := a.Close(); err != nil {
				log.Warn("close the keep-awake request", "err", err)
			}
		})
	case errors.Is(err, errors.ErrUnsupported): // Windows and macOS have one
	default:
		log.Warn("queues cannot keep the computer awake", "err", err)
	}
	p.close = func() {
		for _, c := range closers {
			c()
		}
	}
	return p
}
