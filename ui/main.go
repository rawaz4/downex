//go:build desktop || dev || bindings

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"downex/internal/client"
	"downex/internal/platform"
)

const (
	logName    = "ui.log"
	logMaxSize = 1 << 20 // rotated to ui.log.1 at start-up beyond this
)

func main() {
	platform.TuneMemory()
	// A window process holds no downloads: what it freed goes back to the system each minute.
	go platform.ReleaseMemoryWhileIdle(context.Background(), time.Minute, func() bool { return false })
	allowForegroundHandoff()
	logger, closeLog := openLog()
	defer closeLog()

	assets, err := frontendAssets()
	if err != nil {
		logger.Error("frontend assets", "error", err)
		fmt.Fprintln(os.Stderr, "Downex:", err)
		os.Exit(1)
	}
	app := newApp(newClientDaemon(client.Default()), newShell(), logger)
	// "--progress <id>": this process is the progress window of that download
	// (docs/DESKTOP-SPEC.md §7). Otherwise it is the main window, and "--confirm <id>"
	// from downexd is shown once the page has loaded. When the window is already open,
	// this process only hands its arguments over (SingleInstanceLock).
	app.view = viewOf(os.Args[1:])
	app.requestConfirmations(confirmArgs(os.Args[1:]))
	spec := app.view.window()

	// The app's theme (Settings → Appearance, stored by SetTheme; theme.go) from the first
	// frame: the page's ground until it paints, the native title bar and macOS's
	// appearance. System follows the system's (DESIGN-SYSTEM.md §6.2).
	theme := loadThemeChoice(app.themePath)
	bg := spec.background(theme.isDark(prefersDark()))

	// The main window opens as the user left it (window_state.go); progress windows fit
	// their content.
	width, height := spec.width, spec.height
	startState := options.Normal
	statePath := ""
	var lastState windowState
	var hasLastState bool
	if app.view.progress == 0 {
		statePath = windowStatePath(app.themePath)
		if lastState, hasLastState = loadWindowState(statePath); hasLastState {
			width, height = lastState.startSize(spec.minWidth, spec.minHeight)
			if lastState.Maximised {
				startState = options.Maximised
			}
		}
	}
	err = wails.Run(&options.App{
		Title:            spec.title,
		Width:            width,
		Height:           height,
		MinWidth:         spec.minWidth,
		MinHeight:        spec.minHeight,
		WindowStartState: startState,
		BackgroundColour: options.NewRGB(bg.r, bg.g, bg.b),
		// The Windows main window draws its own title bar (the page's TitleBar: drag region,
		// minimise, maximise and close); windowsOptions keeps the Windows 11 frame
		// decorations, so its shadow, rounded corners, resize borders and snap stay.
		Frameless:   spec.frameless,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup: func(ctx context.Context) {
			app.startup(wailsHost{ctx: ctx})
			startMemoryTrim(ctx, logger) // webview_windows.go
			if app.view.progress > 0 {
				hideFromDock() // macOS: no Dock icon per progress window; elsewhere nothing
			}
		},
		OnDomReady: func(context.Context) {
			if app.view.progress > 0 {
				hideFromDock() // in case the start-up request came too early (it is idempotent)
			}
			app.domReady()
		},
		// Every close (the title bar's Close, Alt+F4, WM_CLOSE from the installer) passes
		// here first: the window is still there to measure.
		OnBeforeClose: func(ctx context.Context) bool {
			if statePath != "" {
				w, h := wruntime.WindowGetSize(ctx)
				s := closingState(lastState, hasLastState, spec.width, spec.height, w, h,
					wruntime.WindowIsMaximised(ctx), wruntime.WindowIsMinimised(ctx))
				if err := saveWindowState(statePath, s); err != nil {
					logger.Warn("window size not kept", "error", err)
				}
			}
			return false
		},
		OnShutdown: func(context.Context) { app.shutdown() },
		Bind:       []any{app},
		// macOS only (docs/MACOS-SPEC.md §5): the app menu (About, Settings…, Quit), the
		// Edit and Window menus, and the standard title bar following the system's
		// appearance. Other systems have no menu bar and ignore the Mac options.
		Menu: appMenu(app),
		Mac:  macOptions(theme),
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               spec.instanceID,
			OnSecondInstanceLaunch: func(d options.SecondInstanceData) { app.secondInstance(d.Args) },
		},
		// Production builds show only the page's own context menus (and the editing
		// menu of text fields); development builds keep the browser's menu and devtools.
		EnableDefaultContextMenu: false,
		Windows:                  windowsOptions(theme, spec),
		Linux:                    &linux.Options{ProgramName: "downex"},
	})
	if err != nil {
		logger.Error("window failed", "error", err)
		fmt.Fprintln(os.Stderr, "Downex:", err)
		closeLog()
		os.Exit(1)
	}
}

// openLog opens ui.log in downexd's log directory (JSON lines, never URLs or file
// contents). Without it, messages are discarded.
func openLog() (*slog.Logger, func()) {
	dir, err := platform.LogDir()
	if err != nil {
		return slog.New(slog.DiscardHandler), func() {}
	}
	path := filepath.Join(dir, logName)
	if info, err := os.Stat(path); err == nil && info.Size() > logMaxSize {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return slog.New(slog.DiscardHandler), func() {}
	}
	return slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})), func() { _ = f.Close() }
}
