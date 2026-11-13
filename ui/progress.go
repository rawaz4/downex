package main

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"downex/internal/client"
	"downex/internal/platform"
)

// singleInstanceID names the per-user single-instance lock of the main window: starting
// Downex while its window is open brings that window to the front instead of opening
// another one.
const singleInstanceID = "downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904"

// progressFlag is the command-line option of a progress window (docs/DESKTOP-SPEC.md §7):
// "downex-ui --progress <id>" shows only the progress of download <id>. Wails v2 has one
// window per process, so every progress window is a process of its own.
const progressFlag = "progress"

// The progress window's sizes (DESKTOP-SPEC §7). The page grows it to its expanded height
// when the user shows the details, and shrinks it back.
const (
	progressWidth     = 520
	progressHeight    = 300 // collapsed; 560 with the details
	progressMinWidth  = 480
	progressMinHeight = 260
)

// view is what one UI process shows: the main window (progress 0), or the progress
// window of one download.
type view struct {
	progress int64
}

// viewOf returns the view that the command-line arguments args (without the program
// name) ask for: the progress window of the first valid "--progress <id>", else the
// main window.
func viewOf(args []string) view {
	if ids := idArgs(args, progressFlag); len(ids) > 0 {
		return view{progress: ids[0]}
	}
	return view{}
}

// rgb is a window's background colour before its page paints.
type rgb struct{ r, g, b uint8 }

// The page's grounds (docs/DESIGN-SYSTEM.md §2.1), so that a window opens in the colour its
// page then paints: the main window's --chrome (title bar, sidebar, status bar) and the
// progress window's --surface, each in the light and the dark theme.
var (
	chromeLight  = rgb{0xee, 0xf2, 0xf0}
	chromeDark   = rgb{0x0a, 0x0e, 0x0c}
	surfaceLight = rgb{0xff, 0xff, 0xff}
	surfaceDark  = rgb{0x17, 0x1e, 0x1b}
)

// windowSpec is the window of a view, and its single-instance lock.
type windowSpec struct {
	title               string
	width, height       int
	minWidth, minHeight int
	instanceID          string
	// frameless: the window has no system title bar, and the page draws its own with the
	// window controls (startupInfo.CustomTitleBar; docs/DESIGN-SYSTEM.md §4.2, §6.2). Only
	// the main window on Windows: progress windows and macOS keep the system frame. The
	// Windows 11 frame decorations (shadow, rounded corners, resize borders) stay.
	frameless bool
	// light and dark are the background in the system's light and dark theme.
	light, dark rgb
}

// window returns the window of v on this system.
func (v view) window() windowSpec { return v.windowOn(runtime.GOOS) }

// windowOn returns the window of v on system goos. Each progress window has its own
// single-instance id, derived from the main one and the download id: opening the progress
// of a download whose window is open brings that window to the front, and the main window
// and the progress windows of other downloads stay independent of it.
func (v view) windowOn(goos string) windowSpec {
	if v.progress > 0 {
		return windowSpec{
			title: "Downex",
			width: progressWidth, height: progressHeight,
			minWidth: progressMinWidth, minHeight: progressMinHeight,
			instanceID: progressInstanceID(v.progress),
			light:      surfaceLight, dark: surfaceDark,
		}
	}
	return windowSpec{
		title: "Downex",
		width: 1200, height: 760,
		minWidth: 900, minHeight: 600,
		instanceID: singleInstanceID,
		frameless:  goos == "windows",
		light:      chromeLight, dark: chromeDark,
	}
}

// background is the window's colour before its page paints, in the system's theme.
func (s windowSpec) background(dark bool) rgb {
	if dark {
		return s.dark
	}
	return s.light
}

// progressInstanceID is the single-instance id of the progress window of download id.
func progressInstanceID(id int64) string {
	return singleInstanceID + "-progress-" + strconv.FormatInt(id, 10)
}

// startupInfo is the result of the Startup binding: which view this process shows, and
// what this system's window can do.
type startupInfo struct {
	View string `json:"view"`         // "main" | "progress"
	ID   string `json:"id,omitempty"` // the download of a progress window (int64 as a decimal string)
	// OpenWith: the OpenWith binding can show the system's "Open with" dialog (always sent;
	// false: the page does not offer Open with…).
	OpenWith bool `json:"openWith"`
	// Hibernate: the system can hibernate after a queue (docs/M6-SPEC.md §1; macOS cannot,
	// and downexd refuses it there): the queue editor offers it only then. Always sent.
	Hibernate bool `json:"hibernate"`
	// WakeTimers: a queue's schedule can wake the computer for its start (Windows waitable
	// timers): the schedule editor offers "Wake the computer" only then. Always sent.
	WakeTimers bool `json:"wakeTimers"`
	// CustomTitleBar: this window is frameless (windowSpec.frameless), so the page draws the
	// title bar with the minimise, maximise and close buttons and its drag region. Always
	// sent.
	CustomTitleBar bool `json:"customTitleBar"`
	// Build is what this window is built from (Settings → About): its version, system and
	// architecture, Go and Wails versions, and the commit when the build has one.
	Build buildInfo `json:"build"`
}

func (v view) info() startupInfo {
	info := startupInfo{
		View:           "main",
		OpenWith:       canOpenWith,
		Hibernate:      canHibernate(runtime.GOOS),
		WakeTimers:     hasWakeTimers(runtime.GOOS),
		CustomTitleBar: v.window().frameless,
		Build:          currentBuild(),
	}
	if v.progress > 0 {
		info.View, info.ID = "progress", strconv.FormatInt(v.progress, 10)
	}
	return info
}

// canHibernate reports whether downexd can hibernate the system goos after a queue.
func canHibernate(goos string) bool { return goos != "darwin" }

// hasWakeTimers reports whether a queue's schedule can wake system goos from sleep.
func hasWakeTimers(goos string) bool { return goos == "windows" }

// idArgs returns the download ids of the "--<flag> <id>" and "--<flag>=<id>" options
// (one dash works too) in args, in order and without repeats. Other arguments, and ids
// that are not positive decimal integers, are ignored.
func idArgs(args []string, flag string) []int64 {
	var ids []int64
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(args[i], "-"), "-"), "=")
		if !strings.HasPrefix(args[i], "-") || name != flag {
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				break
			}
			i++
			value = args[i]
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || slices.Contains(ids, id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// startProgressWindow starts "<this program> --progress <id>" detached, with its window
// shown (platform.StartApp). This process is in the foreground (the user just asked for
// the window), and it lets the new one take the foreground too: when the window is
// open already, the new process hands over to it, which then comes to the front. On
// macOS, inside Downex.app, the program is the same one in Downex Progress.app, whose
// windows have no Dock icon of their own (progressProgram; StartApp opens that bundle).
func startProgressWindow(id int64) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this program: %w", err)
	}
	if l, err := client.FindLayout(func() (string, error) { return exe, nil }); err == nil {
		exe = progressProgram(l, exe, regularFile)
	}
	allowForegroundHandoff()
	return platform.StartApp(exe, "--"+progressFlag, strconv.FormatInt(id, 10))
}

// progressProgram returns the program that shows the progress windows of this one (exe,
// installed as l says): inside the macOS app bundle the progress app's executable
// (client.Layout.Progress, docs/MACOS-SPEC.md §1) when isFile finds it, which is the
// progress app itself there; otherwise exe (Windows, Linux, a development build, and a
// bundle without the progress app, whose windows then show in the Dock).
func progressProgram(l client.Layout, exe string, isFile func(string) bool) string {
	if l.Bundle != "" && isFile(l.Progress) {
		return l.Progress
	}
	return exe
}

func regularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
