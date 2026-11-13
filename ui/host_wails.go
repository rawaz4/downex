//go:build desktop || dev || bindings

package main

import (
	"context"
	"encoding/json"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// wailsHost is the Host of the Wails window whose start-up context is ctx.
type wailsHost struct{ ctx context.Context }

func (h wailsHost) Emit(name string, payload json.RawMessage) {
	wruntime.EventsEmit(h.ctx, name, payload)
}

// Confirm asks with a native Yes/No message box whose default button is No.
func (h wailsHost) Confirm(title, message string) (bool, error) {
	answer, err := wruntime.MessageDialog(h.ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.QuestionDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{"Yes", "No"},
		DefaultButton: "No",
		CancelButton:  "No",
	})
	if err != nil {
		return false, err
	}
	return strings.EqualFold(answer, "Yes"), nil
}

func (h wailsHost) PickFolder(title, dir string) (string, error) {
	return wruntime.OpenDirectoryDialog(h.ctx, wruntime.OpenDialogOptions{
		Title:                title,
		DefaultDirectory:     dir,
		CanCreateDirectories: true,
	})
}

func (h wailsHost) PickFile(title, dir string) (string, error) {
	return wruntime.OpenFileDialog(h.ctx, wruntime.OpenDialogOptions{
		Title:            title,
		DefaultDirectory: dir,
	})
}

func (h wailsHost) Focus() {
	wruntime.WindowUnminimise(h.ctx)
	wruntime.WindowShow(h.ctx)
}

// SetTheme switches the window's native title bar to c at once (Windows: the DWM dark
// mode and, for a window with the system frame, the caption colours of windowsOptions;
// System keeps following Windows' changes). Wails ignores it where it has no theme.
func (h wailsHost) SetTheme(c themeChoice) {
	switch c {
	case themeLight:
		wruntime.WindowSetLightTheme(h.ctx)
	case themeDark:
		wruntime.WindowSetDarkTheme(h.ctx)
	default:
		wruntime.WindowSetSystemDefaultTheme(h.ctx)
	}
}
