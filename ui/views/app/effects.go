package app

import (
	"context"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/confirm"
	"downex/ui/core/media"
	"downex/ui/core/text"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/shell"
)

type contextT = context.Context

func isSite(d *downexv1.Download) bool { return media.IsSite(d.GetMedia()) }

// ---- the confirm queue (DESKTOP-SPEC §1) ------------------------------------------------------

// confirmKey is what the confirm queue's effect depends on (App.svelte's nextConfirm, dialog
// and menu): a change starts a new attempt, the earlier one is dropped.
type confirmKey struct {
	id     int64
	ok     bool
	dialog bool
	menu   bool
}

// requestConfirm asks for the dialog of id: again if it was closed, and first when the user
// asked (explicit).
func (m *Main) requestConfirm(id int64, explicit bool) {
	delete(m.dismissed, id)
	if explicit {
		m.preferred = id
	}
}

// closeConfirm: closing the dialog of id (if it is still the one shown) leaves the download
// waiting, its dialog closed until it is asked for again.
func (m *Main) closeConfirm(id int64) {
	m.dismissed[id] = struct{}{}
	if m.preferred == id {
		m.preferred = 0
	}
}

// SetConfirmElsewhere tells the window whether the standalone Confirm download window is open
// (docs/DESKTOP-SPEC.md §1): while it is, the confirm queue opens no dialog by itself (one the
// user asks for still opens). Call ConfirmElsewhereChanged when the answer changes.
func (m *Main) SetConfirmElsewhere(open func() bool) { m.elsewhere = open }

// ConfirmElsewhereChanged: the standalone Confirm download window opened or closed (UI thread).
func (m *Main) ConfirmElsewhereChanged() { m.refresh() }

// ConfirmShownElsewhere: the standalone Confirm download window shows the confirmation of id
// (UI thread). This window's dialog of it goes, and does not open again by itself: the user
// can still ask for it (the list's Review).
func (m *Main) ConfirmShownElsewhere(id int64) {
	m.closeConfirm(id)
	if on := m.confirmOn; on != nil && m.confirmID == id {
		m.confirmOn = nil
		if m.App.Dialogs.Dialog() == views.Modal(on) {
			on.removed = true
			on.Modal.DismissModal()
		}
	}
	m.refresh()
}

// confirmEffect shows the next confirmation dialog, oldest first, once nothing else is open,
// with the settings read just before (a failed read waits for the next one); and takes away a
// dialog whose download was confirmed or removed elsewhere.
func (m *Main) confirmEffect() {
	a := m.App
	ds := a.Main.Downloads
	if m.confirmOn != nil {
		if d, ok := ds.Get(m.confirmID); !ok || !views.IsAwaiting(d.GetState()) {
			on := m.confirmOn
			m.confirmOn = nil
			if a.Dialogs.Dialog() == views.Modal(on) {
				on.removed = true
				on.Modal.DismissModal() // App.svelte's `dialog = null`: it leaves without its onclose
			}
		}
	}
	_, loaded := a.Settings()
	var key confirmKey
	if ds.Ready() && !ds.Stale() && loaded {
		key.id, key.ok = confirm.Next(ds.All(), m.dismissed, m.preferred)
		// The standalone window confirms the browser's downloads: none opens here by itself.
		if key.ok && key.id != m.preferred && m.elsewhere != nil && m.elsewhere() {
			key.id, key.ok = 0, false
		}
	}
	key.dialog, key.menu = a.Dialogs.DialogOpen(), a.Dialogs.MenuOpen()
	if key == m.confirmKey {
		return
	}
	m.confirmKey = key
	m.confirmGen++
	if !key.ok || key.dialog || key.menu {
		return
	}
	gen, id := m.confirmGen, key.id
	a.Go(func() {
		ok := a.Main.LoadSettings()
		a.UI(func() {
			if gen != m.confirmGen || !ok || a.Dialogs.DialogOpen() || a.Dialogs.MenuOpen() || m.closed {
				return
			}
			requested := m.preferred == id
			if requested {
				m.preferred = 0
			}
			m.showConfirm(id, requested)
		})
	})
}

func (m *Main) showConfirm(id int64, requested bool) {
	inner := call(m.screens.Confirm, m.App, views.ConfirmRequest{ID: id, Requested: requested})
	if inner == nil {
		return
	}
	c := &confirmModal{Modal: inner, m: m, id: id}
	m.confirmOn, m.confirmID = c, id
	m.App.Dialogs.Show(c)
}

// confirmModal wraps a lane's confirmation dialog: its closing by the user leaves the download
// waiting with its dialog closed (closeConfirm); one replaced by another dialog or taken away
// because its download is no longer waiting does not.
type confirmModal struct {
	views.Modal
	m        *Main
	id       int64
	replaced bool
	removed  bool
}

// Unwrap is the lane's dialog inside (its card, for measures).
func (c *confirmModal) Unwrap() views.Modal { return c.Modal }

// ShowModal is views.Modal's.
func (c *confirmModal) ShowModal(w *kit.Window, done func()) kit.Widget {
	return c.Modal.ShowModal(w, func() {
		if !c.replaced && !c.removed {
			c.m.closeConfirm(c.id)
		}
		if c.m.confirmOn == c {
			c.m.confirmOn = nil
		}
		done()
	})
}

// DismissModal is views.Modal's.
func (c *confirmModal) DismissModal() {
	c.replaced = true
	c.Modal.DismissModal()
}

// Confirming is the download whose confirmation dialog shows (ok false: none).
func (m *Main) Confirming() (id int64, ok bool) {
	if m.confirmOn == nil || m.App.Dialogs.Dialog() != views.Modal(m.confirmOn) {
		return 0, false
	}
	return m.confirmID, true
}

// Dismissed reports the downloads whose dialog was closed (tests).
func (m *Main) Dismissed(id int64) bool { _, ok := m.dismissed[id]; return ok }

// ---- arrivals ---------------------------------------------------------------------------------

// arrivals tracks which ids appear between readings of the list (motion.svelte.ts's
// Arrivals): the first reading once the list is ready only records what is there.
type arrivals struct {
	known  map[int64]struct{}
	seeded bool
}

// update takes a reading and returns the ids added since the last one.
func (r *arrivals) update(list []*downexv1.Download, ready bool) []int64 {
	if !ready {
		return nil
	}
	var added []int64
	now := make(map[int64]struct{}, len(list))
	for _, d := range list {
		id := d.GetId()
		now[id] = struct{}{}
		if r.seeded {
			if _, ok := r.known[id]; !ok {
				added = append(added, id)
			}
		}
	}
	r.known, r.seeded = now, true
	return added
}

// arrivalsEffect: downloads handed over by the browser (or seen on the clipboard) say so in a
// notice: one awaiting confirmation offers its dialog now or later when a dialog, the menu or
// the palette is in the way of the one that opens by itself; one already added offers to show
// it. A download confirmed or removed takes its notice along.
func (m *Main) arrivalsEffect() {
	a := m.App
	ds := a.Main.Downloads
	added := m.arrivals.update(ds.All(), ds.Ready())
	for _, id := range added {
		d, ok := ds.Get(id)
		if !ok {
			continue
		}
		src := d.GetSource()
		if src != downexv1.AddSource_ADD_SOURCE_BROWSER && src != downexv1.AddSource_ADD_SOURCE_CLIPBOARD {
			continue
		}
		from := "from the clipboard"
		if src == downexv1.AddSource_ADD_SOURCE_BROWSER {
			from = "from the browser"
		}
		name := text.EscapeBidi(ds.NameOf(id))
		key := "dl-" + strconv.FormatInt(id, 10)
		id := id
		if views.IsAwaiting(d.GetState()) {
			if !a.Dialogs.DialogOpen() && !a.Dialogs.MenuOpen() && !a.Dialogs.PaletteOpen() {
				continue
			}
			a.Notices.Show(widgets.Notice{Key: key, Title: "Download " + from + " to confirm", Detail: name, Actions: []widgets.NoticeAction{
				{Label: "Later", Run: func() { m.closeConfirm(id); m.refresh() }},
				{Label: "Review…", Primary: true, Run: func() { m.requestConfirm(id, true); m.refresh() }},
			}})
		} else {
			a.Notices.Show(widgets.Notice{Key: key, Title: "Download added " + from, Detail: name, Actions: []widgets.NoticeAction{
				{Label: "Show", Primary: true, Run: func() { m.showDownload(id) }},
			}})
		}
	}
	for _, n := range a.Notices.Items() {
		if !strings.HasPrefix(n.Key, "dl-") {
			continue
		}
		id, err := strconv.ParseInt(n.Key[3:], 10, 64)
		if err != nil {
			continue
		}
		d, ok := ds.Get(id)
		if !ok || (len(n.Actions) > 1 && !views.IsAwaiting(d.GetState())) {
			a.Notices.Dismiss(n.Key)
		}
	}
}

// ---- the tools toast (SITE-MEDIA-SPEC §5.4) ---------------------------------------------------

// toolsEffect: a progress toast while downexd downloads yt-dlp and Deno the first time (not for
// updates of working copies), and a notice that offers the settings when that failed.
func (m *Main) toolsEffect(stale bool) {
	a := m.App
	status := a.Main.Tools.Status()
	first := state.IsFirstInstall(status)
	m.Shell.ShowTools(!stale && first)
	if !stale && first {
		m.Shell.ToolsToast().Update(status, a.Main.Tools.Fraction())
	}
	if first {
		m.toolsWere = true
		return
	}
	if !m.toolsWere {
		return
	}
	m.toolsWere = false
	failed := state.FailedTools(status)
	if status == nil || status.GetReady() || len(failed) == 0 {
		return
	}
	detail := failed[0].GetError()
	if detail == "" {
		detail = "Video downloads from web pages need them."
	}
	a.Notices.Show(widgets.Notice{Key: "tools", Title: "Downex could not set up yt-dlp/Deno", Detail: text.EscapeBidi(detail), Actions: []widgets.NoticeAction{
		{Label: "Open settings", Primary: true, Run: m.openVideoSettings},
	}})
}

// openVideoSettings is Settings → Video sites (from the Add dialog or a notice).
func (m *Main) openVideoSettings() {
	if m.App.Stale() {
		m.App.Unavailable()
		return
	}
	m.openSettings(views.SettingsRequest{Page: "video"})
}

// ---- the update pop-up (docs/M6-SPEC.md §4) ---------------------------------------------------

// updateEffect offers the update that downexd's automatic check found in a pop-up once nothing
// else is in the way (a dialog, the menu, a confirmation about to show) while downexd is
// reachable, and takes the pop-up away when a later check offers something else or nothing,
// or when a download comes to wait for its confirmation (not while it installs): the pop-up
// opened by itself and must not hold back a download the browser handed over. It shows again
// once the confirmation has closed.
func (m *Main) updateEffect(stale bool) {
	a := m.App
	st := a.Main.Update
	info, ok := st.Offer()
	if on := m.updateOn; on != nil {
		switch {
		case a.Dialogs.Dialog() != views.Modal(on): // closed, or replaced by another dialog
			m.updateOn = nil
		case on.Busy() || st.Installed() != nil:
		case !ok || state.UpdateOfferKey(info) != state.UpdateOfferKey(on.Info()) || m.confirmKey.ok:
			m.updateOn = nil
			on.DismissModal() // not put off: it, or a newer offer, shows once it has gone
			return
		default:
			return
		}
		if m.updateOn != nil {
			return
		}
	}
	if !ok || stale || a.Dialogs.DialogOpen() || a.Dialogs.MenuOpen() || m.confirmKey.ok || m.closed {
		return
	}
	d := dialogs.NewUpdate(a, st, info)
	m.updateOn = d
	a.Dialogs.Show(d)
}

// UpdatePopup is the update pop-up shown (nil: none).
func (m *Main) UpdatePopup() *dialogs.Update {
	if m.updateOn == nil || m.App.Dialogs.Dialog() != views.Modal(m.updateOn) {
		return nil
	}
	return m.updateOn
}

// ---- the post-queue countdown -----------------------------------------------------------------

// postActionEffect shows the countdown of a post-queue action while downexd can still be asked
// to cancel it (App.svelte's countdown, keyed by queue and deadline).
func (m *Main) postActionEffect(stale bool) {
	a := m.App
	var p *downexv1.PostActionPending
	if !stale {
		p = a.Main.Queues.NextAction()
	}
	key := ""
	if p != nil {
		key = strconv.FormatInt(p.GetQueueId(), 10) + ":" + strconv.FormatInt(shell.DeadlineMs(p), 10)
	}
	if key == m.postKey {
		return
	}
	m.postKey = key
	if p == nil {
		m.Shell.ShowPostAction(nil, false)
		return
	}
	q := p.GetQueueId()
	b := shell.NewPostActionBanner(p, a.Main.Queues.NameOf(q), a.Clock.Now, func(done func(error)) {
		m.cancelPostAction(q, done)
	})
	m.Shell.ShowPostAction(b, !a.Dialogs.MenuOpen())
}

// ---- free space -------------------------------------------------------------------------------

// freeSpaceEffects reads the free space of the download folder's drive at start, when the
// settings change, and after each completion.
func (m *Main) freeSpaceEffects(s *downexv1.Settings, loaded bool, all []*downexv1.Download) {
	dir := s.GetDefaultDir()
	if dir != m.freeDir || loaded != m.freeLoaded || m.freeReads == 0 {
		m.freeDir, m.freeLoaded = dir, loaded
		m.readFreeSpace()
	}
	n := 0
	for _, d := range all {
		if d.GetState() == state.StateCompleted {
			n++
		}
	}
	grew := m.lastCompleted >= 0 && n > m.lastCompleted
	m.lastCompleted = n
	if grew {
		m.readFreeSpace()
	}
}

// readFreeSpace asks the host (hidden when it cannot tell: an older host answers
// unimplemented, and is not asked again).
func (m *Main) readFreeSpace() {
	a := m.App
	s, loaded := a.Settings()
	dir := s.GetDefaultDir()
	m.freeReads++
	n := m.freeReads
	if !loaded || dir == "" {
		if m.freeKnown {
			m.freeKnown = false
			m.refreshSoon()
		}
		return
	}
	if m.freeUnsupported || a.Stale() || a.Platform == nil {
		return
	}
	a.Go(func() {
		bytes, err := a.Platform.FreeSpace(dir)
		a.UI(func() {
			if n != m.freeReads {
				return
			}
			if err != nil {
				m.freeKnown = false
				if e := host.ErrorOf(err); e != nil && e.Code == connect.CodeUnimplemented {
					m.freeUnsupported = true
				}
			} else {
				m.freeBytes, m.freeKnown = bytes, true
			}
			m.refresh()
		})
	})
}

func (m *Main) refreshSoon() { m.App.UI(m.refresh) }

// scheduleFreeSpace reads the free space every minute.
func (m *Main) scheduleFreeSpace() {
	every := m.opts.FreeSpaceEvery
	if every <= 0 {
		every = FreeSpaceInterval
	}
	a := m.App
	m.freeTimer = a.Clock.AfterFunc(every, func() {
		a.UI(func() {
			if m.closed {
				return
			}
			m.readFreeSpace()
			m.scheduleFreeSpace()
		})
	})
}

// FreeSpace is the free space shown (known false: hidden).
func (m *Main) FreeSpace() (bytes int64, known bool) { return m.freeBytes, m.freeKnown }
