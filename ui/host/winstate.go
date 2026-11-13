package host

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
)

// The main window comes back at the size the user left it, maximised if it was, also after
// a restart (an update, the tray's Open after Exit); progress windows fit their content and
// keep nothing (today's ui/window_state.go). The file is
// {"width": 1500, "height": 950, "maximised": true}, client sizes in DIPs. The product
// keeps it beside the theme file (DefaultWindowStatePath: today's ui-window.json, so both
// windows open at the size the user left either); a preview passes a file of its own.

// WindowStateFileName is the product's file, beside ThemeFileName.
const WindowStateFileName = "ui-window.json"

// maxWindowStateFile bounds what is read of the file; anything longer is not one.
const maxWindowStateFile = 4 << 10

// MaxWindowSide bounds a stored side (DIPs); a larger value is not a window size.
const MaxWindowSide = 16384

// WindowState is the stored size of the main window.
type WindowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

// WindowStatePath is the stored window state beside the theme file at themePath; ""
// without one.
func WindowStatePath(themePath string) string {
	if themePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(themePath), WindowStateFileName)
}

// DefaultWindowStatePath is this user's stored main window state (today's file).
func DefaultWindowStatePath() string { return WindowStatePath(DefaultThemePath()) }

// LoadWindowState reads the stored state at path. A missing, unreadable, oversized or
// malformed file, or a size outside 1..MaxWindowSide, reads as none.
func LoadWindowState(path string) (WindowState, bool) {
	if path == "" {
		return WindowState{}, false
	}
	b, ok := readSmallFile(path, maxWindowStateFile)
	if !ok {
		return WindowState{}, false
	}
	var s WindowState
	if err := json.Unmarshal(b, &s); err != nil || !s.Valid() {
		return WindowState{}, false
	}
	return s, true
}

// Valid reports a size of 1..MaxWindowSide on both sides.
func (s WindowState) Valid() bool {
	return s.Width > 0 && s.Height > 0 && s.Width <= MaxWindowSide && s.Height <= MaxWindowSide
}

// SaveWindowState stores s at path atomically (writeFileAtomic).
func SaveWindowState(path string, s WindowState) error {
	if path == "" {
		return errors.New("no folder for the window file")
	}
	if !s.Valid() {
		return errors.New("not a window size")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// StartSize is the size to open at: the stored one, never below the window's minimum.
func (s WindowState) StartSize(minWidth, minHeight int) (int, int) {
	return max(s.Width, minWidth), max(s.Height, minHeight)
}

// ClosingState is what to store when the window closes: its size while normal; while
// maximised or minimised the last normal size (prev when hasPrev, else the default) is
// kept, since the maximised size is the screen's and a minimised one is not a size.
func ClosingState(prev WindowState, hasPrev bool, defWidth, defHeight, width, height int, maximised, minimised bool) WindowState {
	if !hasPrev {
		prev = WindowState{Width: defWidth, Height: defHeight}
	}
	switch {
	case minimised:
		return prev
	case maximised:
		prev.Maximised = true
		return prev
	}
	return WindowState{Width: width, Height: height}
}

// PlacedState is what to store when a window that can read its placement closes
// (GetWindowPlacement's restored rectangle, in DIPs): its restored client size in whole
// DIPs (its size while normal, the last normal one while maximised or minimised), never
// below the minimum, and whether it is maximised (minimised from maximised counts).
func PlacedState(normalWidth, normalHeight float32, maximised bool, minWidth, minHeight int) WindowState {
	w := int(math.Round(float64(normalWidth)))
	h := int(math.Round(float64(normalHeight)))
	return WindowState{Width: max(w, minWidth), Height: max(h, minHeight), Maximised: maximised}
}
