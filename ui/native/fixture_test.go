//go:build windows

package main

import (
	"context"
	"testing"

	"downex/ui/core/about"
	"downex/ui/state/scene"
)

// A fixture run's downexd is the window's own version: Settings → About showed "window 0.9.2
// but downexd 0.0.0-mock" in every fixture run (and scrolled at 1190×752), a mismatch only
// the simulation made. The simulation's other answers are its own.
func TestFixtureDaemonIsTheWindowsVersion(t *testing.T) {
	mock, err := scene.New(scene.Options{Scene: scene.Studio})
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	raw, err := mock.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d := fixtureDaemon{Daemon: mock, version: "0.9.2"}
	st, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.GetVersion() != "0.9.2" || about.VersionsDiffer("0.9.2", st.GetVersion()) {
		t.Errorf("fixture downexd %q, want the window's 0.9.2", st.GetVersion())
	}
	if st.GetPid() != raw.GetPid() || st.GetEndpoint() != raw.GetEndpoint() || st.GetActive() != raw.GetActive() {
		t.Errorf("the rest of the status changed: %v, was %v", st, raw)
	}
	if again, _ := mock.Status(context.Background()); again.GetVersion() == "0.9.2" {
		t.Error("the simulation's own answer was changed")
	}
}
