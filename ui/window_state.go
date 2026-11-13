package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// The main window comes back at the size the user left it, maximised if it was, also
// after a restart (an update, the tray's Open after Exit). Without it every start opened
// at the default 1200×760, which narrows the Name column (the one that takes the rest of
// the width). Progress windows fit their content and keep nothing.

// windowStateFileName sits next to ui-theme.json:
// {"width": 1500, "height": 950, "maximised": true}, in the DIPs Wails sizes windows in.
const windowStateFileName = "ui-window.json"

// maxWindowStateFile bounds what is read of the file; anything longer is not one.
const maxWindowStateFile = 4 << 10

// maxWindowSide bounds a stored side (DIPs); a larger value is not a window size.
const maxWindowSide = 16384

type windowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

// windowStatePath is the stored window state beside the theme file; "" without one.
func windowStatePath(themePath string) string {
	if themePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(themePath), windowStateFileName)
}

// loadWindowState reads the stored state at path. A missing, unreadable, oversized or
// malformed file, or a size outside 1..maxWindowSide, reads as none.
func loadWindowState(path string) (windowState, bool) {
	if path == "" {
		return windowState{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return windowState{}, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxWindowStateFile+1))
	if err != nil || len(b) > maxWindowStateFile {
		return windowState{}, false
	}
	var s windowState
	if err := json.Unmarshal(b, &s); err != nil || !s.valid() {
		return windowState{}, false
	}
	return s, true
}

func (s windowState) valid() bool {
	return s.Width > 0 && s.Height > 0 && s.Width <= maxWindowSide && s.Height <= maxWindowSide
}

// saveWindowState stores s at path atomically (writeFileAtomic).
func saveWindowState(path string, s windowState) error {
	if path == "" {
		return errors.New("no folder for the window file")
	}
	if !s.valid() {
		return errors.New("not a window size")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// startSize is the size to open at: the stored one, never below the window's minimum.
func (s windowState) startSize(minWidth, minHeight int) (int, int) {
	return max(s.Width, minWidth), max(s.Height, minHeight)
}

// closingState is what to store when the window closes: its size while normal; while
// maximised or minimised the last normal size (prev, or the default) is kept, since the
// maximised size is the screen's and a minimised one is not a size.
func closingState(prev windowState, hasPrev bool, defWidth, defHeight, width, height int, maximised, minimised bool) windowState {
	if !hasPrev {
		prev = windowState{Width: defWidth, Height: defHeight}
	}
	switch {
	case minimised:
		return prev
	case maximised:
		prev.Maximised = true
		return prev
	}
	return windowState{Width: width, Height: height}
}
