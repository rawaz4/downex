// Package settings is the native window's Settings dialog: SettingsDialog.svelte and its pages
// (SettingsDownloads, SettingsNetwork, SettingsIntegration, SettingsVideo, SettingsAppearance,
// SettingsUpdates, SettingsAbout) rebuilt on ui/kit and its widgets, with today's layout,
// sizes, colours, fonts, icons, wording, motion and keyboard.
//
// The composition opens it from App.Open.Settings:
//
//	app.Open.Settings = func(r views.SettingsRequest) { app.Dialogs.Show(settings.New(app, r, settings.Options{})) }
//
// The settings are edited on the message GetSettings returned and saved with host.SaveSettings,
// so the fields this build does not know survive; passwords go to downexd only through
// SetSecret after the save, and are never shown, logged or exposed to assistive technology.
package settings
