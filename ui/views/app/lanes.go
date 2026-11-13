package app

import (
	"downex/ui/views"
	"downex/ui/views/add"
	"downex/ui/views/dialogs"
	"downex/ui/views/palette"
	"downex/ui/views/queues"
	"downex/ui/views/settings"
)

// laneScreens are the lanes' dialogs as App.svelte's {#if dialog?.kind === …} blocks make them,
// each handed what App.svelte hands it. A lane's own default is App.svelte's function wherever
// the lane has one (the Remove, Restart, Open and Move dialogs' confirm, the confirmation's
// Cancel download and its queue move, Settings' onsaved); the composition passes only what
// differs: the progress window after an add or a confirmation (DESKTOP-SPEC §7) and Settings →
// Video sites.
func (m *Main) laneScreens() Screens {
	return Screens{
		Add: func(a *views.App, r views.AddRequest) views.Modal {
			return add.New(a, r, add.Options{
				Submit:         m.AddDownloads,
				SubmitMedia:    m.AddMedia,
				OnOpenSettings: m.openVideoSettings,
				DefaultQueue:   a.Main.Downloads.QueueFilter(),
			})
		},
		Confirm: func(a *views.App, r views.ConfirmRequest) views.Modal {
			d, ok := a.Main.Downloads.Get(r.ID)
			if !ok {
				return nil // {#if d}: gone before its dialog could show
			}
			s, _ := a.Settings()
			return add.NewConfirm(a, d, add.ConfirmOptions{
				Settings:  s,
				Waiting:   len(a.Main.Downloads.Awaiting()),
				Requested: r.Requested,
				Confirm:   m.ConfirmDownload,
			})
		},
		Remove: func(a *views.App, r views.RemoveRequest) views.Modal {
			return dialogs.NewRemove(a, r, nil)
		},
		Restart: func(a *views.App, r views.RestartRequest) views.Modal {
			return dialogs.NewRestart(a, r, nil)
		},
		Open: func(a *views.App, r views.OpenRequest) views.Modal {
			return dialogs.NewOpen(a, r, nil)
		},
		Move: func(a *views.App, r views.MoveRequest) views.Modal {
			return dialogs.NewMove(a, r, nil)
		},
		Settings: func(a *views.App, r views.SettingsRequest) views.Modal {
			return settings.New(a, r, settings.Options{})
		},
		Queues: func(a *views.App, r views.QueuesRequest) views.Modal {
			return queues.New(a, r)
		},
		Palette: func(a *views.App) views.Modal {
			return palette.New(a, palette.Options{})
		},
	}
}

// withLanes fills the screens s leaves nil with the lanes' (a test passes stand-ins).
func (m *Main) withLanes(s Screens) Screens {
	l := m.laneScreens()
	if s.Add == nil {
		s.Add = l.Add
	}
	if s.Confirm == nil {
		s.Confirm = l.Confirm
	}
	if s.Remove == nil {
		s.Remove = l.Remove
	}
	if s.Restart == nil {
		s.Restart = l.Restart
	}
	if s.Open == nil {
		s.Open = l.Open
	}
	if s.Move == nil {
		s.Move = l.Move
	}
	if s.Settings == nil {
		s.Settings = l.Settings
	}
	if s.Queues == nil {
		s.Queues = l.Queues
	}
	if s.Palette == nil {
		s.Palette = l.Palette
	}
	return s
}
