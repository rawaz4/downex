//go:build linux

package main

import (
	"os"
	"testing"
)

func TestTerminalHelpersOnPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, _, ok := termSize(w); ok {
		t.Error("a pipe has a terminal size")
	}
	restore, err := enableVT(w)
	if err != nil || restore == nil {
		t.Fatalf("enableVT: %v", err)
	}
	restore()
	if _, _, ok := liveTerminal(w); ok {
		t.Error("a pipe is treated as a live terminal")
	}
}
