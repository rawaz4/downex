package views

import (
	"reflect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/validate"
	"downex/ui/kit"
)

// The requests the openers take: App.svelte's DialogState and the arguments of the functions
// that set it (openAdd, openSettings, openQueues, requestRemove, requestRestart,
// requestConfirm, the open confirmation of openDownload, the menu's move-queue).

// AddMode is which way the Add dialog opens (the Add split button's menu).
type AddMode uint8

// The add modes.
const (
	// AddDefault: as the addresses say (a single manifest address opens as a stream).
	AddDefault AddMode = iota
	// AddFiles is 'files', AddStream 'stream' ("Add video or stream…").
	AddFiles
	AddStream
)

// AddRequest opens the Add dialog (openAdd(urls, mode)).
type AddRequest struct {
	// URLs prefill the links ("\n"-separated: a paste, a drop).
	URLs string
	Mode AddMode
}

// SettingsRequest opens the Settings dialog (openSettings(opts)).
type SettingsRequest struct {
	// Page is the page shown first ("" = General; Downloads with FocusField).
	Page validate.SettingsPage
	// AddLogin opens Network with the editor of a new site login for this address pattern
	// (auth_required: loginPatternFor).
	AddLogin string
	// FocusField is the field that takes the focus (the status bar's "Custom…" limit).
	FocusField validate.SettingsField
}

// QueuesRequest opens the Queues dialog (openQueues(id)).
type QueuesRequest struct {
	// Queue is the queue selected first; 0 is App.svelte's null (the main queue).
	Queue int64
}

// MoveRequest opens Move to queue (the menu's move-queue).
type MoveRequest struct {
	IDs []int64
}

// RemoveRequest asks to remove downloads (requestRemove(ids, deleteFile)).
type RemoveRequest struct {
	IDs []int64
	// DeleteFile preselects "Delete the files too" (Shift+Delete, "Remove and delete file…").
	DeleteFile bool
}

// RestartReason is why the Restart dialog asks (RestartDialog's reason).
type RestartReason uint8

// The restart reasons.
const (
	// RestartManual: the user asked to restart.
	RestartManual RestartReason = iota
	// RestartValidatorChanged: downexd cannot resume (the file changed on the server).
	RestartValidatorChanged
	// RestartCompleted: every one is complete (Download again).
	RestartCompleted
)

func (r RestartReason) String() string {
	switch r {
	case RestartValidatorChanged:
		return "validator_changed"
	case RestartCompleted:
		return "completed"
	}
	return "manual"
}

// AgainFile is a completed download that is downloaded again (RestartDialog's AgainFile).
type AgainFile struct {
	// Name is its display name.
	Name string
	// Exists: its file is still there (a host that cannot tell answers true).
	Exists bool
	// Site: a web page's video.
	Site bool
}

// RestartRequest asks to restart downloads (requestRestart(ids, reason)). Screens give IDs and
// Reason (RestartManual or RestartValidatorChanged); the composition keeps the restartable
// ones, asks for the completed ones' files and fills Again (and Reason RestartCompleted when
// every one is complete) before the dialog shows.
type RestartRequest struct {
	IDs    []int64
	Reason RestartReason
	Again  []AgainFile
}

// OpenRequest asks before opening a completed download whose file can run code, or whose name
// hides its type with bidi controls (OpenDialog).
type OpenRequest struct {
	ID int64
	// WithApp: Open with… (the user chooses the app next).
	WithApp bool
}

// ConfirmRequest asks for the confirmation dialog of a download awaiting confirmation
// (requestConfirm(id, explicit)).
type ConfirmRequest struct {
	ID int64
	// Requested: the user asked for it (the list, the details pane, a notice's Review…); it
	// comes before older ones and takes the focus. Otherwise it opened by itself.
	Requested bool
}

// RescueRequest opens the Rescue partial file dialog (docs/DESKTOP-SPEC.md §11); Partial,
// when set, is the partial file already chosen.
type RescueRequest struct {
	Partial string
}

// RenewRequest opens the New link dialog of a download (docs/DESKTOP-SPEC.md §10).
type RenewRequest struct {
	ID int64
}

// Openers open the window's dialogs and screens. The composition (views/app) fills every
// field with App.svelte's logic (the stale check and its toast, the confirm queue, the files'
// check before Download again) and the lanes' constructors; screens call them and never
// import one another. They run on the UI thread.
type Openers struct {
	// Add is openAdd: the Add dialog (unavailable while stale).
	Add func(AddRequest)
	// Confirm is requestConfirm: the confirmation dialog of an awaiting download, now when
	// nothing is in the way, else when the dialog or menu closes.
	Confirm func(ConfirmRequest)
	// Remove is requestRemove: the Remove dialog (nothing for no ids; unavailable while
	// stale).
	Remove func(RemoveRequest)
	// Restart is requestRestart: the Restart / Download again dialog.
	Restart func(RestartRequest)
	// Renew is the New link dialog of a download whose link expired (or any paused or
	// failed file download; unavailable while stale).
	Renew func(RenewRequest)
	// Rescue is the Rescue partial file dialog (unavailable while stale).
	Rescue func(RescueRequest)
	// Open is the open confirmation of a program or a name with hidden controls.
	Open func(OpenRequest)
	// MoveToQueue is the Move to queue dialog for the ids (the menu's move-queue; Commands
	// checks staleness first).
	MoveToQueue func(MoveRequest)
	// Settings is openSettings: the Settings dialog on a page.
	Settings func(SettingsRequest)
	// Queues is openQueues: the Queues dialog with a queue selected.
	Queues func(QueuesRequest)
	// Palette is openPalette: the command palette (Ctrl+K).
	Palette func()
	// About is Settings → About (the macOS application menu's About, the tray's).
	About func()
}

// LimitChoice is a choice of the status bar's speed-limit menu (StatusBar's LimitChoice): a
// limit in bytes per second (0 = unlimited) or "Custom…".
type LimitChoice struct {
	Custom         bool
	BytesPerSecond int64
}

// WhenDoneChoice is a choice of the status bar's When done menu: an action for the main queue,
// or "Other queues…" (Other).
type WhenDoneChoice struct {
	Other  bool
	Action downexv1.PostAction
}

// Actions are the window's actions that more than one screen reaches (the toolbar, the rows,
// the context menu, the details pane, the palette, the shortcuts, the status bar). The
// composition fills every field with App.svelte's function of the same name; ids nil means the
// selection. They run on the UI thread and report through toasts.
type Actions struct {
	Pause, Resume func(ids []int64)
	// TogglePauseResume pauses the selection when it can, else resumes it (Space).
	TogglePauseResume   func()
	PauseAll, ResumeAll func()
	// OpenDownload is a row's Enter or double-click: opens a completed download (asking
	// first for programs), shows an unfinished one's progress window, or the dialog of one
	// awaiting confirmation.
	OpenDownload func(id int64)
	OpenFolder   func(id int64)
	// OpenProgress opens a download's progress window (unavailable while stale).
	OpenProgress func(id int64)
	CopyURLs     func(ids []int64)
	// ShowDownload shows a download in the list: every filter cleared, selected, the list
	// focused (a notice's Show).
	ShowDownload func(id int64)
	// ClearFilters shows every download again (the list's empty state).
	ClearFilters func()
	// SelectAll selects every download the list shows and focuses the list (Ctrl+A).
	SelectAll func()
	// FocusSearch focuses the title bar's search and selects its text (Ctrl+F).
	FocusSearch func()
	// FocusList focuses the download grid.
	FocusList func()
	// ToggleDetails collapses or expands the details pane.
	ToggleDetails func()
	// SetGlobalLimit is the status bar's speed-limit menu (Custom… opens Settings).
	SetGlobalLimit func(LimitChoice)
	// SetWhenDone is the status bar's When done menu (Other queues… opens Queues).
	SetWhenDone func(WhenDoneChoice)
	// SetPolite turns Polite mode on or off (the limit menu's checkbox, the palette;
	// docs/POLITE-SPEC.md §8).
	SetPolite func(on bool)
	// OpenLimitMenu and OpenWhenDoneMenu open the status bar's menus (the palette's
	// "Set global speed limit…" and "When done…").
	OpenLimitMenu, OpenWhenDoneMenu func()
	// SetDownloadLimit is the details pane's speed limit of one download.
	SetDownloadLimit func(id int64, bytesPerSecond int64)
	// AddSiteLogin opens Settings → Network with a login for the download's server.
	AddSiteLogin func(id int64)
	// OpenPage opens the page a download came from in the browser ("Open page"; the user
	// always clicks for it); a failure is the toast "Downex could not open the page.".
	OpenPage func(id int64)
	// CancelPostAction cancels a queue's post-queue countdown.
	CancelPostAction func(queueID int64)
	// OpenMenu opens the download context menu at a point (window DIPs) for the selection.
	OpenMenu func(at kit.Point)
	// CloseMenu closes it and focuses the list.
	CloseMenu func()
}

// Missing names the fields of an Openers or Actions value (or a pointer to one) left nil: the
// composition's test checks it fills every one.
func Missing(v any) []string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := range rv.NumField() {
		f := rv.Field(i)
		if f.Kind() == reflect.Func && f.IsNil() {
			out = append(out, rv.Type().Field(i).Name)
		}
	}
	return out
}
