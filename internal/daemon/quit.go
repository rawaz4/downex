package daemon

// AnnounceQuit tells every window that the user quit Downex (the tray's Quit, the menu-bar
// icon's Quit, macOS ⌘Q in the window): each Watch stream gets a quit event, also the last
// batch of a stream that Shutdown ends, and the windows close. The caller stops the daemon
// next. Shutdown alone (an update, "downex daemon stop", the system's logout) sends none.
func (m *Manager) AnnounceQuit() {
	m.log.Info("quit: the windows close")
	m.hub.publishQuit()
}
