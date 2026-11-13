package state

import (
	"context"
	"math"
	"strings"
	"sync"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/media"
)

// ToolsStatus is a downex.v1.ToolsStatus.
type ToolsStatus = downexv1.ToolsStatus

// ToolName is "yt-dlp" or "Deno"; another name as it is.
func ToolName(name string) string {
	switch name {
	case "yt-dlp":
		return "yt-dlp"
	case "deno":
		return "Deno"
	}
	return name
}

const toolInstalling = downexv1.ToolState_TOOL_STATE_INSTALLING

// IsInstalling reports that some tool is being downloaded or updated.
func IsInstalling(s *ToolsStatus) bool {
	for _, t := range s.GetTools() {
		if t.GetState() == toolInstalling {
			return true
		}
	}
	return false
}

// IsFirstInstall reports the first-use install: downloading while nothing can resolve
// pages yet. An update of a ready copy, which keeps working meanwhile, is not one (it
// shows no toast).
func IsFirstInstall(s *ToolsStatus) bool {
	return s != nil && s.GetEnabled() && !s.GetReady() && IsInstalling(s)
}

// FailedTools are the tools that failed (their last install or check).
func FailedTools(s *ToolsStatus) []*downexv1.ToolInfo {
	var out []*downexv1.ToolInfo
	for _, t := range s.GetTools() {
		if t.GetState() == downexv1.ToolState_TOOL_STATE_FAILED {
			out = append(out, t)
		}
	}
	return out
}

// InstallFraction is how far the running install is, 0..1 over the tools being
// downloaded; NaN while any of them has no known total (or none is being downloaded).
func InstallFraction(s *ToolsStatus) float64 {
	var total, done float64
	n := 0
	for _, t := range s.GetTools() {
		if t.GetState() != toolInstalling {
			continue
		}
		n++
		if !(t.GetBytesTotal() > 0) {
			return math.NaN()
		}
		total += float64(t.GetBytesTotal())
		done += float64(min(t.GetBytesDone(), t.GetBytesTotal()))
	}
	if n == 0 {
		return math.NaN()
	}
	return math.Min(1, math.Max(0, done/total))
}

// InstallText is "Downloading yt-dlp and Deno: 34%" (the tools being downloaded), with
// fallback as the fraction when the totals do not give one; withPercent false names only
// what downloads (beside a progress bar, which shows the percentage itself).
func InstallText(s *ToolsStatus, fallback float64, withPercent bool) string {
	var names []string
	for _, t := range s.GetTools() {
		if t.GetState() == toolInstalling {
			names = append(names, ToolName(t.GetName()))
		}
	}
	fraction := InstallFraction(s)
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		fraction = fallback
	}
	what := "Downloading yt-dlp and Deno"
	if len(names) > 0 {
		what = "Downloading " + strings.Join(names, " and ")
	}
	if withPercent && !math.IsNaN(fraction) && !math.IsInf(fraction, 0) {
		return what + ": " + format.Percent(fraction)
	}
	return what + "…"
}

// ToolsDaemon is what the ToolsStore calls (a host.Daemon has it).
type ToolsDaemon interface {
	GetTools(ctx context.Context) (*downexv1.GetToolsResponse, error)
	InstallTools(ctx context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error)
}

// ToolsOptions are what a ToolsStore works with.
type ToolsOptions struct {
	Daemon ToolsDaemon
	Events Events
	Call   CallContext
	// Go runs the read after a failed install in the background (nil: a goroutine).
	Go func(func())
}

// InstallKind is the install the user started.
type InstallKind uint8

const (
	// NotBusy: none runs.
	NotBusy InstallKind = iota
	// Installing: an install of what is missing.
	Installing
	// Updating: an install that also looks for newer releases.
	Updating
)

// ToolsStore is yt-dlp and Deno as the window knows them (stores/tools.svelte.ts,
// SITE-MEDIA-SPEC §4, §5.4): their state comes from GetTools and from the tools entries
// of Watch; Settings → Video sites installs or updates them (InstallTools answers when
// the install ends while its progress arrives as events).
type ToolsStore struct {
	Changes Signal

	daemon ToolsDaemon
	events Events
	call   CallContext
	goFn   func(func())

	mu           sync.Mutex
	unsub        func()
	started      bool
	loading      chan struct{}
	status       *ToolsStatus
	supported    bool
	loadError    string
	busy         InstallKind
	installError string
}

// NewTools makes the store.
func NewTools(o ToolsOptions) *ToolsStore {
	t := &ToolsStore{daemon: o.Daemon, events: o.Events, call: callOr(o.Call), goFn: o.Go, supported: true}
	if t.goFn == nil {
		t.goFn = func(f func()) { go f() }
	}
	return t
}

// Status is the last state downexd reported (nil until it reported one).
func (t *ToolsStore) Status() *ToolsStatus { t.mu.Lock(); defer t.mu.Unlock(); return t.status }

// Supported reports that downexd has the tools (an older one answers unimplemented:
// "Update Downex").
func (t *ToolsStore) Supported() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.supported }

// LoadError is why the last read failed ("" when it did not).
func (t *ToolsStore) LoadError() string { t.mu.Lock(); defer t.mu.Unlock(); return t.loadError }

// Busy is the install the user started that runs.
func (t *ToolsStore) Busy() InstallKind { t.mu.Lock(); defer t.mu.Unlock(); return t.busy }

// InstallError is why the user's install failed (the daemon's tools_unavailable text).
func (t *ToolsStore) InstallError() string { t.mu.Lock(); defer t.mu.Unlock(); return t.installError }

// Installing reports that some tool is being downloaded.
func (t *ToolsStore) Installing() bool { return IsInstalling(t.Status()) }

// Fraction is InstallFraction of Status.
func (t *ToolsStore) Fraction() float64 { return InstallFraction(t.Status()) }

// Start subscribes to the tools events.
func (t *ToolsStore) Start() {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.mu.Unlock()
	if t.events != nil {
		unsub := t.events.Subscribe(t.Apply)
		t.mu.Lock()
		t.unsub = unsub
		t.mu.Unlock()
	}
}

// Dispose unsubscribes.
func (t *ToolsStore) Dispose() {
	t.mu.Lock()
	unsub := t.unsub
	t.unsub = nil
	t.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// Apply takes one batch of events.
func (t *ToolsStore) Apply(batch Batch) {
	changed := false
	t.mu.Lock()
	for _, ev := range batch {
		if k, ok := ev.GetKind().(*downexv1.Event_Tools); ok {
			t.status = k.Tools
			t.supported = true
			changed = true
		}
	}
	t.mu.Unlock()
	if changed {
		t.Changes.Notify()
	}
}

// Load reads the state (concurrent calls share one read).
func (t *ToolsStore) Load() {
	t.mu.Lock()
	if t.loading != nil {
		ch := t.loading
		t.mu.Unlock()
		<-ch
		return
	}
	ch := make(chan struct{})
	t.loading = ch
	t.mu.Unlock()
	resp, err := callWith(t.call, t.daemon.GetTools)
	t.mu.Lock()
	switch {
	case err == nil:
		t.status, t.supported, t.loadError = resp.GetStatus(), true, ""
	case codeOf(err) == connect.CodeUnimplemented:
		t.supported = false
	default:
		t.loadError = Message(err)
	}
	t.loading = nil
	t.mu.Unlock()
	close(ch)
	t.Changes.Notify()
}

// Install installs the tools (update: also newer releases) unless an install the user
// started runs, and reports whether it ended well. Its progress arrives as events
// meanwhile; ctx bounds the call (host.ToolsInstallTimeout in the window).
func (t *ToolsStore) Install(ctx context.Context, update bool) bool {
	t.mu.Lock()
	if t.busy != NotBusy {
		t.mu.Unlock()
		return false
	}
	t.busy = Installing
	if update {
		t.busy = Updating
	}
	t.installError = ""
	t.mu.Unlock()
	t.Changes.Notify()

	resp, err := t.daemon.InstallTools(ctx, &downexv1.InstallToolsRequest{Update: update})
	t.mu.Lock()
	t.busy = NotBusy
	if err == nil {
		t.status, t.supported = resp.GetStatus(), true
		t.mu.Unlock()
		t.Changes.Notify()
		return true
	}
	if codeOf(err) == connect.CodeUnimplemented {
		t.supported = false
	}
	t.installError = media.SiteErrorText(ErrorOf(err))
	t.mu.Unlock()
	t.Changes.Notify()
	// What the failure left (each tool's error) is shown from a fresh read.
	t.goFn(t.Load)
	return false
}
