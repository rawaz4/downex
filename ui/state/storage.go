package state

// Storage is what today's window kept in WebView2's localStorage, per viewer: string
// values by key (prefs.File in the native window). Get answers ok false for a key that is
// not set; an error means the storage cannot be used (the stores then keep the session's
// choice).
type Storage interface {
	Get(key string) (value string, ok bool, err error)
	Set(key, value string) error
	Remove(key string) error
}

// SharedStorage is a Storage that other windows change too (the storage event of today's
// window): Watch calls f with the key that changed, "" when everything may have.
type SharedStorage interface {
	Storage
	Watch(f func(key string)) (cancel func())
}

// WriteStatus is a Storage whose writes reach the disk later, on a goroutine of their own
// (prefs.File): its Set and Remove change the values at once and never wait for the disk,
// so a store's click handler never blocks on a file another program holds. A write that
// fails is retried; WriteErr says why the newest changes are not stored yet (nil when
// everything is), and WatchWrites calls f each time that turns from nil to a failure or
// back.
type WriteStatus interface {
	WriteErr() error
	WatchWrites(f func(err error)) (cancel func())
}

// storageErr is a store's report of its storage: the error of its last synchronous write
// (a plain Storage), else what a WriteStatus says of the writes still pending.
func storageErr(s Storage, last error) error {
	if last != nil {
		return last
	}
	if ws, ok := s.(WriteStatus); ok {
		return ws.WriteErr()
	}
	return nil
}

// watchWrites calls changed each time s's WriteStatus changes (nothing for another Storage).
func watchWrites(s Storage, changed func()) (cancel func()) {
	if ws, ok := s.(WriteStatus); ok {
		return ws.WatchWrites(func(error) { changed() })
	}
	return func() {}
}

// The keys today's window stores (App.svelte, DownloadTable.svelte, RestartDialog.svelte,
// ProgressWindow.svelte, lib/progress.ts and the theme and density stores).
const (
	ThemeKey            = "downex.theme"
	DensityKey          = "downex.density"
	ColumnWidthsKey     = "downex.columnWidths"
	ExistingFileKey     = "downex.existingFile"
	ProgressDetailsKey  = "downex.progress.details"
	DetailsCollapsedKey = "downex.detailsCollapsed"
)

// StorageKeys lists them.
func StorageKeys() []string {
	return []string{ThemeKey, DensityKey, ColumnWidthsKey, ExistingFileKey, ProgressDetailsKey, DetailsCollapsedKey}
}
