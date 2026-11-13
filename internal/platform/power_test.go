package platform

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// The power functions themselves are never called by tests: they would really suspend
// or shut down the machine. Only their pure parts are checked here.

func TestMacPowerCommand(t *testing.T) {
	exe, args, ok := macPowerCommand("sleep")
	if !ok || exe != "/usr/bin/pmset" || !slices.Equal(args, []string{"sleepnow"}) {
		t.Fatalf("sleep = %q %q %v", exe, args, ok)
	}
	exe, args, ok = macPowerCommand("shutdown")
	if !ok || exe != "/usr/bin/osascript" || !slices.Equal(args, []string{"-e", `tell application "System Events" to shut down`}) {
		t.Fatalf("shutdown = %q %q %v", exe, args, ok)
	}
	if _, _, ok := macPowerCommand("hibernate"); ok {
		t.Fatal("hibernate has a macOS command")
	}
}

func TestMacCaffeinateCommand(t *testing.T) {
	exe, args := macCaffeinateCommand(4242)
	if exe != "/usr/bin/caffeinate" || !slices.Equal(args, []string{"-i", "-w", "4242"}) {
		t.Fatalf("caffeinate = %q %q", exe, args)
	}
}

func TestFileTimeOf(t *testing.T) {
	// 1970-01-01 is 116444736000000000 in FILETIME units; one second is 10^7.
	if got := fileTimeOf(time.Unix(0, 0)); got != 116444736000000000 {
		t.Fatalf("epoch = %d", got)
	}
	if got := fileTimeOf(time.Unix(1, 500)); got != 116444736000000000+10_000_000+5 {
		t.Fatalf("epoch + 1 s = %d", got)
	}
}

func TestErrPowerUnsupported(t *testing.T) {
	if !errors.Is(ErrPowerUnsupported, errors.ErrUnsupported) {
		t.Fatal("ErrPowerUnsupported does not wrap errors.ErrUnsupported")
	}
	_ = CanHibernate() // safe: only asks
}
