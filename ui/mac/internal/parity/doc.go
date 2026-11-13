// Package parity compares the native Mac window with today's Mac window (the Svelte UI in
// Wails' WebKit window) screen by screen: the same fixture state (scene.Fixtures, the
// demo's ?fixture= tokens and the steps of scripts/measure-dialogs.mjs) rendered natively
// (ui/views/viewstest over ui/mac's offscreen renderer, or the native program's own window
// for the main and progress windows) and by WebKit (testdata/wkplay.swift: a WKWebView on
// the built demo with ?mac=1, the startup view the Mac host gives), at 1200×760 points and
// 2x, light and dark. Each pair is written as reference | native | difference.
//
// [mac webkit] TestParityCheckBoxes compares the native check boxes with WebKit's own
// (testdata/checks.html snapped by testdata/wksnap.swift); TestTabLabelCentredWebKit checks
// the tab labels with the real text engine (it runs always).
//
// It runs only when asked:
//
//	DOWNEX_PARITY_DEMO=http://127.0.0.1:8765/   the demo (ui/frontend built with the mock api)
//	DOWNEX_PARITY_OUT=dir                        where the sheets go (default: a temp folder)
//	DOWNEX_PARITY_STATES=a,b                     only these states ("main", "pw-status", …)
//	DOWNEX_PARITY_THEMES=light                   only these themes
package parity
