//go:build windows

package main

import (
	"os"
	"testing"
)

func TestTerminalHelpersOnFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, _, ok := termSize(f); ok {
		t.Error("a file has a console size")
	}
	if _, err := enableVT(f); err == nil {
		t.Error("enableVT succeeded on a file")
	}
	if _, _, ok := liveTerminal(f); ok {
		t.Error("a file is treated as a live terminal")
	}
}
