package macos

import (
	"path"
	"testing"

	"downex/internal/client"
)

// TestLayoutMatchesClient ties the bundle that the templates and scripts/build-macos.sh
// make to the paths the programs look for at run time (client.Layout: the daemon's
// window, progress app and login item, the clients' autostart): every executable of the
// bundle finds the same installation, and the executables it names are the ones the
// Info.plist templates declare (bundles, checked against the templates by
// TestBundleInfoPlists).
func TestLayoutMatchesClient(t *testing.T) {
	const app = "/Applications/Downex.app"
	exe := func(b bundleSpec) string {
		return path.Join(app, b.dir, "Contents", "MacOS", b.executable)
	}
	window, progress, agent := exe(bundles[0]), exe(bundles[1]), exe(bundles[2])
	helpers := path.Join(app, "Contents", "Helpers")
	for _, from := range []string{window, progress, agent, helpers + "/downexd", helpers + "/downex", helpers + "/downex-nmhost"} {
		l := client.LayoutOf("darwin", from)
		if l.Bundle != app || l.Dir != helpers || l.Window != window || l.Progress != progress || l.Agent != agent ||
			l.Daemon() != helpers+"/downexd" {
			t.Errorf("from %s: %+v, daemon %s", from, l, l.Daemon())
		}
	}
	if bundles[0].executable != client.MacWindowName || bundles[1].executable != client.MacWindowName ||
		path.Base(bundles[1].dir) != client.MacProgressApp || path.Base(bundles[2].dir) != client.MacAgentApp ||
		bundles[2].executable != client.MacAgentName {
		t.Errorf("the bundle table %+v does not use client's names", bundles)
	}
}
