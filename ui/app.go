// Command Downex is the desktop window of Downex: a Wails v2 host that embeds the
// Svelte frontend in frontend/ and connects it to downexd.
//
// The frontend calls the exported methods of App as window.go.main.App.<Method> and
// receives the daemon's download events as the Wails event "downex:events" (see
// frontend/src/lib/wails.ts). "downex-ui --confirm <id>" (how downexd asks the user to
// confirm a browser hand-off, docs/DESKTOP-SPEC.md §1) becomes the event
// "downex:confirm" ({id}), also when a window is already open (single instance).
// "downex-ui --progress <id>" runs a progress window of its own instead (DESKTOP-SPEC §7):
// one process per download, whose page learns that from the Startup binding and whose
// events are those of that download only. Arguments and results are JSON shaped like the protojson
// encoding of the downex.v1 messages (proto/downex/v1/downex.proto) with lowerCamelCase
// names: int64 values are decimal strings, enums their names, timestamps RFC 3339, and
// fields at their proto3 default are omitted. A failed call rejects with the Connect
// error text "<code>: <message>".
//
// The window is built with the Wails CLI ("wails build" in this directory), which sets
// the build tags that select main.go; a plain "go build" compiles main_stub.go, which
// only explains that (see README.md).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"downex/internal/platform"
	downexv1 "downex/internal/rpc/downex/v1"
)

// version is logged at start-up; builds set it with -ldflags "-X main.version=<version>".
var version = "dev"

const (
	// eventsName is the Wails event that carries Watch batches to the frontend.
	eventsName = "downex:events"
	// confirmName is the Wails event that asks the frontend to show the confirmation
	// dialog of one download awaiting confirmation; its payload is {"id": "<id>"}.
	confirmName = "downex:confirm"
	// confirmFlag is the command-line option that carries such a request.
	confirmFlag = "confirm"
	// callTimeout bounds one binding's daemon work, including the wait for the start of
	// downexd when the window opens (≤ 3 s).
	callTimeout = 30 * time.Second
	// shutdownWait bounds how long closing the window waits for the events bridge.
	shutdownWait = 3 * time.Second
)

// Host is what App needs from the window runtime (the Wails runtime in production,
// a fake in tests).
type Host interface {
	// Emit sends an event with one JSON payload to the frontend.
	Emit(name string, payload json.RawMessage)
	// Confirm asks a yes/no question in a native dialog and reports a yes.
	Confirm(title, message string) (bool, error)
	// PickFolder shows the native folder picker, starting in dir when it is not empty.
	// It returns "" when the user cancels.
	PickFolder(title, dir string) (string, error)
	// PickFile shows the native file picker for an existing file, starting in dir when it
	// is not empty. It returns "" when the user cancels.
	PickFile(title, dir string) (string, error)
	// Focus shows the window, restores it if it is minimised and brings it to the front.
	Focus()
	// SetTheme applies the app's theme to what the page does not draw: the native title
	// bar (Windows) and the window's appearance (theme.go).
	SetTheme(c themeChoice)
}

// App is bound to the window. Its exported methods are exactly the bindings of the
// frontend's contract; everything else is unexported so that Wails does not bind it.
type App struct {
	daemon Daemon
	shell  Shell
	log    *slog.Logger
	// minBackoff and maxBackoff bound the events bridge's reconnect delay (1 s, 10 s).
	minBackoff, maxBackoff time.Duration

	mu      sync.Mutex
	host    Host
	ctx     context.Context // canceled by shutdown
	cancel  context.CancelFunc
	bridge  sync.WaitGroup
	started bool
	// pageReady: the page has loaded (domReady), so events reach its listeners.
	pageReady bool
	// pending holds confirmation requests that arrived before the page was ready.
	pending []int64

	// view is what this process shows (set before startup; the main window by default).
	view view
	// openWindow starts the progress window of a download (startProgressWindow).
	openWindow func(id int64) error
	// freeSpace reads the bytes free on the volume of an absolute folder (platform.FreeSpace).
	freeSpace func(dir string) (int64, error)
	// themePath is the stored theme choice (themePath(); tests: a temporary file).
	themePath string
	// env reads the environment (os.Getenv): the profile folders that Diagnostics hides.
	env func(string) string
}

// newApp returns an App that serves the window from d. log may be nil.
func newApp(d Daemon, sh Shell, log *slog.Logger) *App {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		daemon: d, shell: sh, log: log,
		minBackoff: time.Second, maxBackoff: 10 * time.Second,
		ctx: ctx, cancel: cancel,
		openWindow: startProgressWindow,
		freeSpace:  platform.FreeSpace,
		themePath:  themePath(),
		env:        os.Getenv,
	}
}

// startup attaches the window runtime and starts forwarding daemon events to it (a
// progress window's: those of its download only). Its first Watch (or the page's first
// call) is the window's first contact with downexd, which starts it when it is not
// running; nothing starts it later (see Daemon).
func (a *App) startup(host Host) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started || a.ctx.Err() != nil {
		return
	}
	a.started = true
	a.host = host
	var ids []int64
	if a.view.progress > 0 {
		ids = []int64{a.view.progress}
	}
	b := &eventBridge{
		daemon: a.daemon,
		ids:    ids,
		emit:   func(p json.RawMessage) { host.Emit(eventsName, p) },
		log:    a.log,
		min:    a.minBackoff,
		max:    a.maxBackoff,
	}
	a.bridge.Add(1)
	go func() {
		defer a.bridge.Done()
		b.run(a.ctx)
	}()
	if a.view.progress > 0 {
		a.log.Info("progress window started", "version", version, "download", a.view.progress)
	} else {
		a.log.Info("window started", "version", version)
	}
}

// shutdown stops the events bridge and cancels calls in progress. It waits for the
// bridge for at most shutdownWait; no event is emitted once it has returned in time.
func (a *App) shutdown() {
	a.cancel()
	done := make(chan struct{})
	go func() {
		a.bridge.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
		a.log.Warn("the events bridge did not stop in time")
	}
}

// secondInstance brings the window to the front when Downex is started again, and
// forwards the confirmation requests of that start's arguments (args without the
// program name) to the page.
func (a *App) secondInstance(args []string) {
	if h := a.runtimeHost(); h != nil {
		h.Focus()
	}
	a.requestConfirmations(confirmArgs(args))
}

// domReady is called when the page has loaded: confirmation requests that arrived
// before it (the window's own command line) are sent now.
func (a *App) domReady() {
	a.mu.Lock()
	a.pageReady = true
	a.mu.Unlock()
	a.requestConfirmations(nil)
}

// requestConfirmations asks the page to show the confirmation dialogs of ids, in order.
// Before the page is ready they wait; the page also shows every download awaiting
// confirmation that it lists, so a request lost to a reload costs nothing. A progress
// window shows no confirmation dialogs: it ignores them.
func (a *App) requestConfirmations(ids []int64) {
	if a.view.progress > 0 {
		return
	}
	a.mu.Lock()
	a.pending = append(a.pending, ids...)
	h := a.host
	if !a.pageReady || h == nil || len(a.pending) == 0 {
		a.mu.Unlock()
		return
	}
	send := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, id := range send {
		payload, err := json.Marshal(confirmEvent{ID: strconv.FormatInt(id, 10)})
		if err != nil {
			continue
		}
		h.Emit(confirmName, payload)
	}
	a.log.Info("confirmation requested", "downloads", len(send))
}

// confirmEvent is the payload of confirmName; the id is a decimal string, as int64
// values are in protojson.
type confirmEvent struct {
	ID string `json:"id"`
}

// confirmArgs returns the download ids of the "--confirm <id>" and "--confirm=<id>"
// options (one dash works too) in args, in order and without repeats. Other
// arguments, and ids that are not positive decimal integers, are ignored.
func confirmArgs(args []string) []int64 {
	return idArgs(args, confirmFlag)
}

func (a *App) runtimeHost() Host {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.host
}

// callContext bounds one binding's daemon work; it ends early when the window closes.
func (a *App) callContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, callTimeout)
}

// ---- bindings ----------------------------------------------------------------------

// List returns DownloadService.List: {downloads: [...]}.
func (a *App) List() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.List(ctx))
}

// Get forwards a GetRequest ({id}) and returns the GetResponse: {download: {...}}.
func (a *App) Get(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.GetRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	d, err := a.daemon.Get(ctx, in.GetId())
	return reply(&downexv1.GetResponse{Download: d}, err)
}

// Add forwards an AddRequest and returns the AddResponse: {download: {...}}.
func (a *App) Add(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.AddRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.Add(ctx, in))
}

// Pause forwards a PauseRequest ({id}) and returns the PauseResponse.
func (a *App) Pause(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.PauseRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.Pause(ctx, in))
}

// Resume forwards a ResumeRequest ({id, restart, maxConnections?, speedLimit?}).
func (a *App) Resume(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.ResumeRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.Resume(ctx, in))
}

// Remove forwards a RemoveRequest ({id, deleteFile}) and returns {}.
func (a *App) Remove(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.RemoveRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.Remove(ctx, in))
}

// Confirm forwards a ConfirmRequest ({id, dir?, filename?, start, maxConnections?,
// speedLimit?, videoTrack?, audioTrack?}) for a download awaiting confirmation and
// returns the ConfirmResponse: {download: {...}}.
func (a *App) Confirm(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.ConfirmRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.Confirm(ctx, in))
}

// AddMedia forwards an AddMediaRequest ({manifestUrl, headers?, dir?, filename?, source?,
// videoTrack?, audioTrack?, pageUrl?, title?, startPaused?}) for an HLS or DASH manifest
// and returns the AddMediaResponse: {download: {...}}, a download of kind
// DOWNLOAD_KIND_MEDIA with its media info. A stream the daemon refuses (encrypted, live)
// rejects with invalid_argument and the daemon's message.
func (a *App) AddMedia(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.AddMediaRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.AddMedia(ctx, in))
}

// ProbeMedia forwards a ProbeMediaRequest ({manifestUrl, headers?}) and returns the
// ProbeMediaResponse: {media: {...}} with the stream's tracks and the daemon's default
// pick; nothing is added.
func (a *App) ProbeMedia(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.ProbeMediaRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.ProbeMedia(ctx, in))
}

// SetDownloadOptions forwards a SetDownloadOptionsRequest ({id, speedLimit?,
// maxConnections?, completionDialog?}: an absent field stays as it is, speedLimit 0 is
// unlimited and maxConnections 0 the daemon's default; a progress window sends
// completionDialog true once, DESKTOP-SPEC §8) and returns the SetDownloadOptionsResponse:
// {download: {...}}. A running download applies the speed limit at once.
func (a *App) SetDownloadOptions(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.SetDownloadOptionsRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.SetDownloadOptions(ctx, in))
}

// PauseAll returns DownloadService.PauseAll: {paused}.
func (a *App) PauseAll() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.PauseAll(ctx))
}

// ResumeAll returns DownloadService.ResumeAll: {resumed}.
func (a *App) ResumeAll() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.ResumeAll(ctx))
}

// GetSettings returns DaemonService.GetSettings: {settings: {...}}.
func (a *App) GetSettings() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.GetSettings(ctx))
}

// UpdateSettings forwards an UpdateSettingsRequest ({settings: {...}}, a full
// replacement validated by the daemon) and returns the stored settings.
//
// Settings fields that a newer downexd has and this window does not know never reach
// the page (protojson leaves them out), so the page cannot write them back: they are
// copied from the daemon's current settings, and the replacement keeps them (also those
// of nested messages: the proxy settings, and site logins matched by id).
func (a *App) UpdateSettings(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.UpdateSettingsRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	if next := in.GetSettings(); next != nil {
		cur, err := a.daemon.GetSettings(ctx)
		if err != nil {
			return nil, apiError(err)
		}
		if s := cur.GetSettings(); s != nil {
			keepUnknown(next.ProtoReflect(), s.ProtoReflect())
		}
	}
	return reply(a.daemon.UpdateSettings(ctx, in))
}

// keepUnknown copies the fields of cur that this build does not know into next: its own,
// those of its singular message fields that both have, and those of the entries of its
// repeated message fields whose string field "id" matches (Settings.site_logins).
func keepUnknown(next, cur protoreflect.Message) {
	if u := cur.GetUnknown(); len(u) > 0 {
		next.SetUnknown(slices.Clone(u))
	}
	fields := cur.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind || fd.IsMap() {
			continue
		}
		if !cur.Has(fd) || !next.Has(fd) {
			continue
		}
		if !fd.IsList() {
			keepUnknown(next.Mutable(fd).Message(), cur.Get(fd).Message())
			continue
		}
		id := fd.Message().Fields().ByName("id")
		if id == nil || id.Kind() != protoreflect.StringKind || id.IsList() {
			continue
		}
		byID := map[string]protoreflect.Message{}
		curList := cur.Get(fd).List()
		for j := range curList.Len() {
			m := curList.Get(j).Message()
			if k := m.Get(id).String(); k != "" {
				byID[k] = m
			}
		}
		nextList := next.Mutable(fd).List()
		for j := range nextList.Len() {
			m := nextList.Get(j).Message()
			if old, ok := byID[m.Get(id).String()]; ok {
				keepUnknown(m, old)
			}
		}
	}
}

// Status returns DaemonService.Status.
func (a *App) Status() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.Status(ctx))
}

// OpenFile opens a completed download ({id}) with the system's default application;
// programs and scripts are confirmed first (see openFile).
func (a *App) OpenFile(req json.RawMessage) error {
	in := &downexv1.GetRequest{}
	if err := decode(req, in); err != nil {
		return err
	}
	return apiError(a.openFile(in.GetId()))
}

// OpenWith lets the user choose the application that opens a completed download ({id}),
// DESKTOP-SPEC §8 (Windows: the system's "Open with" dialog), after the checks and the
// confirmation of OpenFile. Closing the dialog without a choice rejects with canceled; a
// system without such a dialog rejects with unimplemented.
func (a *App) OpenWith(req json.RawMessage) error {
	in := &downexv1.GetRequest{}
	if err := decode(req, in); err != nil {
		return err
	}
	return apiError(a.openWith(in.GetId()))
}

// OpenFolder shows a download ({id}) in the system's file manager: its file selected
// when it exists, else its folder.
func (a *App) OpenFolder(req json.RawMessage) error {
	in := &downexv1.GetRequest{}
	if err := decode(req, in); err != nil {
		return err
	}
	return apiError(a.openFolder(in.GetId()))
}

// OpenProgressWindow opens the progress window of a download ({id}), DESKTOP-SPEC §7:
// it starts "<this program> --progress <id>" detached, a process of its own. When that
// download's window is open already, the new process brings it to the front and exits.
func (a *App) OpenProgressWindow(req json.RawMessage) error {
	in := &downexv1.GetRequest{}
	if err := decode(req, in); err != nil {
		return err
	}
	id := in.GetId()
	if id <= 0 {
		return apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no such download: %d", id)))
	}
	if err := a.openWindow(id); err != nil {
		a.log.Warn("cannot open a progress window", "download", id, "error", err)
		return apiError(connect.NewError(connect.CodeInternal, fmt.Errorf("cannot open the progress window: %w", err)))
	}
	a.log.Info("progress window requested", "download", id)
	return nil
}

// Startup returns what this window shows: {view: "main"}, or {view: "progress", id}
// for the progress window of download id ("downex-ui --progress <id>"), and what this
// system's window can do: openWith (true on Windows; false where OpenWith rejects with
// unimplemented, such as macOS, where the page does not offer Open with…).
func (a *App) Startup() (json.RawMessage, error) {
	b, err := json.Marshal(a.view.info())
	if err != nil {
		return nil, apiError(connect.NewError(connect.CodeInternal, err))
	}
	return b, nil
}

// pickFolderRequest is the argument of PickFolder.
type pickFolderRequest struct {
	InitialDir string `json:"initialDir"`
}

// PickFolder shows the native folder picker ({initialDir}) and returns the chosen
// absolute folder, or "" when the user cancels. A starting folder that does not exist
// is replaced by its nearest existing parent.
func (a *App) PickFolder(req json.RawMessage) (string, error) {
	var in pickFolderRequest
	if !isEmptyJSON(req) {
		dec := json.NewDecoder(bytes.NewReader(req))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return "", apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed request: %w", err)))
		}
	}
	h := a.runtimeHost()
	if h == nil {
		return "", apiError(errNoWindow())
	}
	dir, err := h.PickFolder("Choose a folder", existingDir(in.InitialDir))
	if err != nil {
		return "", apiError(connect.NewError(connect.CodeInternal, fmt.Errorf("folder picker: %w", err)))
	}
	return dir, nil
}

func errNoWindow() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("the window is not ready"))
}

// SetTheme applies the app's theme ({theme: "system" | "light" | "dark"}, Settings →
// Appearance) to this window's native parts (Host.SetTheme: the title bar of a window
// with the system frame) and stores it for the windows opened later, which read it
// before their window exists (theme.go). The page calls it at start and on every change,
// in every window. Anything but one of the three values (also an unknown field) rejects
// with invalid_argument; a choice that cannot be stored is still applied, and rejects
// with internal.
func (a *App) SetTheme(req json.RawMessage) error {
	var in themeRequest
	if err := decodeStrict(req, &in); err != nil {
		return apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed request: %w", err)))
	}
	c, ok := parseThemeChoice(in.Theme)
	if !ok {
		return apiError(connect.NewError(connect.CodeInvalidArgument, errors.New(`theme must be "system", "light" or "dark"`)))
	}
	if h := a.runtimeHost(); h != nil {
		h.SetTheme(c)
	}
	if err := saveThemeChoice(a.themePath, c); err != nil {
		a.log.Warn("cannot store the theme", "error", err)
		return apiError(connect.NewError(connect.CodeInternal, fmt.Errorf("cannot store the theme: %w", err)))
	}
	return nil
}

// OpenAppFolder shows one of Downex's folders in the file manager ({kind: "data" | "logs" |
// "tools" | "downloads"}, Settings → About). The page names the folder, never a path: the
// host resolves it itself (appFolder: downexd's own folders from its Status, the default
// download folder from its settings). An unknown kind or field rejects with
// invalid_argument, a folder that does not exist (yet) with failed_precondition.
func (a *App) OpenAppFolder(req json.RawMessage) error {
	var in appFolderRequest
	if err := decodeStrict(req, &in); err != nil {
		return apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed request: %w", err)))
	}
	kind, ok := parseFolderKind(in.Kind)
	if !ok {
		return apiError(connect.NewError(connect.CodeInvalidArgument, errors.New(`kind must be "data", "logs", "tools" or "downloads"`)))
	}
	return apiError(a.openAppFolder(kind))
}

// ---- wire format -------------------------------------------------------------------

// Results use protojson's lowerCamelCase names (UseProtoNames false) and omit fields
// at their proto3 default, which the frontend reads as that default; a size of -1
// (unknown) is not a default and is always sent.
var (
	marshalOptions   = protojson.MarshalOptions{UseProtoNames: false}
	unmarshalOptions = protojson.UnmarshalOptions{}
)

// isEmptyJSON reports an absent argument (no bytes, or null).
func isEmptyJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// decode parses a binding's argument into m (protojson accepts lowerCamelCase and
// proto names, int64 as number or string, enums as name or number). An absent
// argument leaves m empty.
func decode(raw json.RawMessage, m proto.Message) error {
	if isEmptyJSON(raw) {
		return nil
	}
	if err := unmarshalOptions.Unmarshal(raw, m); err != nil {
		return apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed request: %w", err)))
	}
	return nil
}

// encode renders m for the frontend.
func encode(m proto.Message) (json.RawMessage, error) {
	b, err := marshalOptions.Marshal(m)
	if err != nil {
		return nil, apiError(connect.NewError(connect.CodeInternal, fmt.Errorf("encode %s: %w", m.ProtoReflect().Descriptor().Name(), err)))
	}
	return b, nil
}

// reply encodes a daemon response, or maps its error.
func reply[M proto.Message](m M, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, apiError(err)
	}
	return encode(m)
}

// callError is what a binding rejects with: its text is "<code>: <message>", the
// format of connect.Error, which the frontend parses into ApiError.code.
type callError struct {
	code connect.Code
	msg  string
}

func (e *callError) Error() string { return e.code.String() + ": " + e.msg }

// apiError maps err to a callError. Connect errors keep their code and message; a
// context that ended maps to canceled or deadline_exceeded; anything else is unknown.
func apiError(err error) error {
	if err == nil {
		return nil
	}
	var ce *callError
	if errors.As(err, &ce) {
		return ce
	}
	code := connect.CodeOf(err)
	msg := err.Error()
	var cerr *connect.Error
	if errors.As(err, &cerr) && cerr == err {
		msg = cerr.Message()
	}
	if code == connect.CodeUnknown {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			code, msg = connect.CodeDeadlineExceeded, "downexd did not answer in time"
		case errors.Is(err, context.Canceled):
			code, msg = connect.CodeCanceled, "the window is closing"
		}
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "no details"
	}
	return &callError{code: code, msg: msg}
}
