package daemon

import (
	"context"
	"slices"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The Manager drives Config.Network, the live network configuration (docs/M6-SPEC.md
// §2): it hands it the network settings at start and after every change, retries an
// update that failed (an unreachable PAC script or system discovery) with a growing
// delay, and refreshes a system or PAC configuration on Reevaluate (resume from sleep,
// clock changes) and every networkRefresh, so that a changed system proxy is followed.

// Network update timings.
const (
	// networkStartTimeout bounds the update at start: downloads wait for it.
	networkStartTimeout = 3 * time.Second
	// networkChangeTimeout bounds the update that UpdateSettings waits for.
	networkChangeTimeout = 5 * time.Second
	// networkUpdateTimeout bounds a background update.
	networkUpdateTimeout = 30 * time.Second
	// networkRetryMin and networkRetryMax bound the delay before a failed update is
	// tried again (it doubles in between).
	networkRetryMin = 30 * time.Second
	networkRetryMax = 10 * time.Minute
	// networkRefresh is how often a system or PAC proxy configuration is read again.
	networkRefresh = 30 * time.Minute
)

// refreshNetwork hands the current network settings to Config.Network, bounded by
// timeout, and returns its error (nil without Config.Network). The update is numbered
// before the settings are read, so of concurrent updates the last one (which read the
// latest settings) wins, and a slow one never overwrites a newer one.
func (m *Manager) refreshNetwork(timeout time.Duration) error {
	rt := m.cfg.Network
	if rt == nil {
		return nil
	}
	seq := rt.beginUpdate()
	ctx, cancel := context.WithTimeout(m.base, timeout)
	defer cancel()
	err := rt.update(ctx, m.Settings().Network, seq)
	if err != nil && m.base.Err() == nil {
		m.log.Warn("the proxy configuration is not available; connecting directly until it is", "err", err)
	}
	return err
}

// kickNetwork asks the network goroutine for an update soon; it never blocks.
func (m *Manager) kickNetwork() {
	select {
	case m.netKick <- struct{}{}:
	default:
	}
}

// runNetwork is the goroutine that updates Config.Network in the background (failed
// started it with a failed first update); it ends at Shutdown.
func (m *Manager) runNetwork(failed bool) {
	defer m.wg.Done()
	delay := networkRetryMin
	t := time.NewTimer(networkRefresh)
	defer t.Stop()
	arm := func() {
		mode := m.Settings().Network.Proxy.Mode
		switch {
		case failed:
			t.Reset(delay)
			delay = min(2*delay, networkRetryMax)
		case mode == ProxyModeSystem || mode == ProxyModePAC:
			delay = networkRetryMin
			t.Reset(networkRefresh)
		default:
			delay = networkRetryMin
			t.Stop()
		}
	}
	arm()
	for {
		select {
		case <-t.C:
		case <-m.netKick:
		case <-m.schedDone:
			return
		}
		failed = m.refreshNetwork(networkUpdateTimeout) != nil
		arm()
	}
}

// networkChanged applies new network settings (UpdateSettings, after they were stored):
// the update runs at once, bounded by networkChangeTimeout, and is retried in the
// background when it fails. The passwords of site logins that next no longer has are
// removed from the keyring.
func (m *Manager) networkChanged(prev, next NetworkSettings) {
	rt := m.cfg.Network
	if rt == nil {
		return
	}
	if err := m.refreshNetwork(networkChangeTimeout); err != nil {
		m.kickNetwork()
	}
	if rt.keyring == nil {
		return
	}
	for _, l := range prev.SiteLogins {
		if slices.ContainsFunc(next.SiteLogins, func(n SiteLogin) bool { return n.ID == l.ID }) {
			continue
		}
		if err := rt.keyring.Delete(secretName(SecretKindSiteLogin, l.ID)); err != nil {
			m.log.Warn("cannot remove the password of a removed site login", "login", l.ID, "err", err)
		}
	}
}

// protoSettings renders s for clients with the has_password flags of the proxy and the
// site logins (never the passwords).
func (m *Manager) protoSettings(s Settings) *downexv1.Settings {
	var has func(kind, id string) bool
	if rt := m.cfg.Network; rt != nil {
		has = rt.HasSecret
	}
	return protoSettingsWith(s, has)
}
