package views

import (
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/text"
	"downex/ui/host"
	"downex/ui/state"
)

// App.svelte's small rules that several screens read (api.ts's isAwaiting and needsRestart,
// which ui/state does not carry).

// IsAwaiting reports a download awaiting confirmation (api.ts isAwaiting).
func IsAwaiting(s downexv1.DownloadState) bool { return s == state.StateAwaiting }

// NeedsRestart reports a download downexd cannot resume: it failed because the file changed on
// the server (api.ts needsRestart).
func NeedsRestart(d *downexv1.Download) bool {
	return d.GetState() == state.StateFailed && d.GetErrorCode() == "validator_changed"
}

// HasProgress reports a download that has a progress to show: anything not finished or
// waiting for confirmation (the menu's "Show progress").
func HasProgress(d *downexv1.Download) bool {
	return d.GetState() != state.StateCompleted && !IsAwaiting(d.GetState())
}

// StartsNow reports that downexd started a download (or is about to) rather than keeping it
// paused or queueing it behind max_concurrent_downloads (DESKTOP-SPEC §7: those open no
// progress window).
func StartsNow(d *downexv1.Download) bool {
	st := d.GetState()
	if st == state.StateQueued && d.GetQueuePosition() > 0 {
		return false
	}
	return st != state.StatePaused && !IsAwaiting(st) && st != state.StateCompleted
}

// Restartable reports a download the Restart dialog takes: anything that has started (a
// completed one is downloaded again; ENGINE-SPEC §7.1).
func Restartable(d *downexv1.Download) bool { return !IsAwaiting(d.GetState()) }

// LoginPatternFor is the site login pattern for the server that asked for credentials: its
// origin, any path ("" when the address cannot be read).
func LoginPatternFor(d *downexv1.Download) string {
	raw := d.GetFinalUrl()
	if raw == "" {
		raw = d.GetUrl()
	}
	u, err := text.ParseURL(raw)
	if err != nil {
		return ""
	}
	return u.Scheme() + "://" + u.Host() + "/*"
}

// SelectedIDs are the ids of downloads, in their order (store.selected.map(d => d.id)).
func SelectedIDs(list []*downexv1.Download) []int64 {
	ids := make([]int64, len(list))
	for i, d := range list {
		ids[i] = d.GetId()
	}
	return ids
}

// ReportText is the toast App.svelte's report() shows for a bulk action's failures ("" when
// every one worked): "Could not <verb> <name>: <error>" for one, "Could not <verb> N
// downloads: <first error>" for several, names and messages bidi-isolated.
func ReportText(outcomes []state.Outcome, verb string, nameOf func(id int64) string) string {
	var failed []state.Outcome
	for _, o := range outcomes {
		if !o.OK() {
			failed = append(failed, o)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	first := failed[0]
	if len(failed) == 1 {
		return "Could not " + verb + " " + text.Isolate(nameOf(first.ID)) + ": " + text.Isolate(first.Err.Message)
	}
	return "Could not " + verb + " " + strconv.Itoa(len(failed)) + " downloads: " + text.Isolate(first.Err.Message)
}

// ErrorMessage is a call error's message as the toasts quote it (toApiError(err).message).
func ErrorMessage(err error) string { return state.Message(err) }

// PageTitle is the page header's title: "All downloads", or the names of the filters shown
// joined by " · " ("Video · Paused", "Main queue"): App.svelte's pageTitle.
func PageTitle(category categories.Filter, label func(categories.Filter) string, queue int64, queueName func(int64) string, status categories.Status) string {
	var parts []string
	if category != categories.All {
		parts = append(parts, text.EscapeBidi(label(category)))
	}
	if queue != 0 {
		parts = append(parts, text.EscapeBidi(queueName(queue)))
	}
	if status != categories.AnyStatus {
		parts = append(parts, categories.StatusLabel(status))
	}
	if len(parts) == 0 {
		return categories.AllLabel
	}
	return strings.Join(parts, " · ")
}

// NeedBytes is what is left to download: downloading, queued and paused downloads of known
// size (the status bar's free-space warning).
func NeedBytes(list []*downexv1.Download) int64 {
	var sum int64
	for _, d := range list {
		switch d.GetState() {
		case state.StateDownloading, state.StateQueued, state.StatePaused:
			if d.GetSize() > 0 {
				sum += max(0, d.GetSize()-d.GetReceived())
			}
		}
	}
	return sum
}

// CallErrorText is "<code>: <message>" as today's window shows an error it quotes whole.
func CallErrorText(err error) string {
	if e := host.ErrorOf(err); e != nil {
		return e.Error()
	}
	return ""
}
