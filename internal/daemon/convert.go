package daemon

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/filecheck"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// protoState maps a stored state to its API value.
func protoState(s model.State) downexv1.DownloadState {
	switch s {
	case model.StateQueued:
		return downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	case model.StateProbing:
		return downexv1.DownloadState_DOWNLOAD_STATE_PROBING
	case model.StateDownloading:
		return downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	case model.StatePaused:
		return downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	case model.StateCompleted:
		return downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	case model.StateFailed:
		return downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	case model.StateCanceled:
		return downexv1.DownloadState_DOWNLOAD_STATE_CANCELED
	case model.StateAwaitingConfirmation:
		return downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION
	}
	return downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED
}

// protoSource maps a recorded source to its API value.
func protoSource(s model.Source) downexv1.AddSource {
	switch s {
	case model.SourceCLI:
		return downexv1.AddSource_ADD_SOURCE_CLI
	case model.SourceUI:
		return downexv1.AddSource_ADD_SOURCE_UI
	case model.SourceBrowser:
		return downexv1.AddSource_ADD_SOURCE_BROWSER
	case model.SourceClipboard:
		return downexv1.AddSource_ADD_SOURCE_CLIPBOARD
	}
	return downexv1.AddSource_ADD_SOURCE_UNSPECIFIED
}

// protoMode maps a download mode to its API value; an unprobed download is unspecified.
func protoMode(m model.Mode) downexv1.Mode {
	switch m {
	case model.ModeRanged:
		return downexv1.Mode_MODE_RANGED
	case model.ModeSingle:
		return downexv1.Mode_MODE_SINGLE
	}
	return downexv1.Mode_MODE_UNSPECIFIED
}

// protoTime converts t, returning nil for the zero time ("absent").
func protoTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// protoSegments converts the live segment views of a progress event.
func protoSegments(views []engine.SegmentView) []*downexv1.Segment {
	if len(views) == 0 {
		return nil
	}
	out := make([]*downexv1.Segment, len(views))
	for i, v := range views {
		out[i] = &downexv1.Segment{Start: v.Start, End: v.End, Received: v.Received}
	}
	return out
}

// protoConnections converts the live worker views of a progress event (docs/DESKTOP-SPEC.md
// §7) to connection_info, ordered by slot. media keeps each view's fragment index; file
// downloads report -1. Views without a slot are dropped; the detail text is trimmed to
// maxConnectionDetail bytes.
func protoConnections(views []model.WorkerView, media bool) []*downexv1.ConnectionInfo {
	if len(views) == 0 {
		return nil
	}
	out := make([]*downexv1.ConnectionInfo, 0, len(views))
	for _, v := range views {
		if v.Slot <= 0 {
			continue
		}
		frag := int32(-1)
		if media && v.Fragment >= 0 {
			frag = clampInt32(v.Fragment)
		}
		start, end := v.Start, v.End
		if start < 0 {
			start, end = -1, -1
		}
		out = append(out, &downexv1.ConnectionInfo{
			Index:    clampInt32(v.Slot),
			State:    protoConnectionState(v.State),
			Start:    start,
			End:      end,
			Received: max(v.Received, 0),
			SpeedBps: finiteRate(v.SpeedBps),
			Detail:   connectionDetail(v.Detail),
			Fragment: frag,
		})
	}
	slices.SortStableFunc(out, func(a, b *downexv1.ConnectionInfo) int { return cmp.Compare(a.Index, b.Index) })
	if len(out) == 0 {
		return nil
	}
	return out
}

// maxConnectionDetail bounds the detail text of one connection (a short status line).
const maxConnectionDetail = 200

// connectionDetail returns a worker's detail text as valid UTF-8 of at most
// maxConnectionDetail bytes on one line.
func connectionDetail(s string) string { return shortLine(s, maxConnectionDetail) }

// shortLine returns s as valid UTF-8 of at most limit bytes on one line (control
// characters become spaces).
func shortLine(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// finiteRate returns a speed for the API: NaN, infinite and negative rates become 0.
func finiteRate(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

// protoConnectionState maps a worker state to its API value.
func protoConnectionState(s model.WorkerState) downexv1.ConnectionState {
	switch s {
	case model.WorkerConnecting:
		return downexv1.ConnectionState_CONNECTION_STATE_CONNECTING
	case model.WorkerRequesting:
		return downexv1.ConnectionState_CONNECTION_STATE_REQUESTING
	case model.WorkerReceiving:
		return downexv1.ConnectionState_CONNECTION_STATE_RECEIVING
	case model.WorkerRetrying:
		return downexv1.ConnectionState_CONNECTION_STATE_RETRYING
	case model.WorkerIdle:
		return downexv1.ConnectionState_CONNECTION_STATE_IDLE
	case model.WorkerDone:
		return downexv1.ConnectionState_CONNECTION_STATE_DONE
	case model.WorkerError:
		return downexv1.ConnectionState_CONNECTION_STATE_ERROR
	}
	return downexv1.ConnectionState_CONNECTION_STATE_UNSPECIFIED
}

// downloadView is everything needed to render one download for clients.
type downloadView struct {
	rec           model.Download
	received      int64 // durable bytes while stopped
	queue         model.QueueID
	errCode       string
	running       bool
	progress      *engine.Progress     // latest live progress, nil before the first event
	mprogress     *MediaProgress       // latest live progress of a media download
	queuePosition int                  // 1-based among queued downloads, 0 otherwise
	check         *model.FileCheck     // the file check shown (filecheck.Shown); nil = none or off
	renewCheck    bool                 // a held add whose link is being checked (renew.go)
	renewOffer    *downexv1.RenewOffer // an awaiting add's offer to continue a stopped download
}

// protoFileCheck renders a file check of the file named name with its texts
// (docs/DESKTOP-SPEC.md §9.6); nil and NONE are omitted.
func protoFileCheck(fc *model.FileCheck, name string) *downexv1.FileCheck {
	if fc == nil || fc.Level == model.FileCheckNone {
		return nil
	}
	return &downexv1.FileCheck{
		Level:             downexv1.FileCheckLevel(fc.Level),
		Stage:             downexv1.FileCheckStage(fc.Stage + 1),
		Reason:            fc.Reason,
		Content:           fc.Content,
		ContentLabel:      filecheck.ContentLabel(fc),
		ClaimLabel:        filecheck.ClaimLabel(fc),
		ServedType:        fc.Served,
		Member:            fc.Member,
		ProgramMembers:    clampInt32(fc.ProgramMembers),
		ListingIncomplete: fc.ListingIncomplete,
		Summary:           filecheck.Summary(fc, name),
		Detail:            filecheck.Detail(fc),
	}
}

// toProto renders v. Live fields are only set while the download runs; error and
// error_code only for a failed download.
func (v downloadView) toProto() *downexv1.Download {
	d := &v.rec
	// The stored size, which a completed media download takes from its file and an
	// unfinished one derives (dlview.Size); a running job's progress replaces it below.
	size, approx := dlview.Size(d)
	pd := &downexv1.Download{
		Id:             int64(d.ID),
		Url:            d.Request.URL,
		FinalUrl:       d.FinalURL,
		Dir:            d.Dir,
		Filename:       d.Filename,
		Size:           size,
		SizeApprox:     approx,
		Received:       v.received,
		State:          protoState(d.State),
		Mode:           protoMode(d.Mode),
		MaxConnections: int32(d.MaxConnections),
		SpeedLimit:     d.SpeedLimit,
		CreatedAt:      protoTime(d.CreatedAt),
		UpdatedAt:      protoTime(d.UpdatedAt),
		CompletedAt:    protoTime(d.CompletedAt),
		QueuePosition:  int32(v.queuePosition),
		Source:         protoSource(d.Source),
		ExpectedSize:   d.ExpectedSize,
		Mime:           d.MIME,
		Category:       d.Category,
		Kind:           protoKind(d.Kind),
		// The download complete dialog (docs/DESKTOP-SPEC.md §8); protoLocked adds the
		// running time of the current run.
		CompletionDialog: d.CompletionDialog,
		ActiveMs:         max(d.ActiveMS, 0),
		StartedAt:        protoTime(d.StartedAt),
		QueueId:          int64(v.queue),
		FileCheck:        protoFileCheck(v.check, d.Filename),
		RenewCheck:       v.renewCheck,
		RenewOffer:       v.renewOffer,
		RescuedBytes:     d.RescuedBytes,
	}
	pd.PageUrl = dlview.PageURL(d)
	if pd.QueueId == 0 {
		pd.QueueId = int64(model.MainQueueID)
	}
	if d.Kind == model.KindMedia && d.Media != nil {
		pd.Media = protoMedia(d.Request.URL, d.Media)
	}
	switch {
	case d.State == model.StateCompleted:
		if d.Size >= 0 {
			pd.Received = d.Size
		}
		// A note of the post-processing (docs/M6-SPEC.md §3: the antivirus scan reported
		// a problem or did not finish); the download stays completed, without a code.
		pd.Error = d.Error
	case d.State == model.StateFailed:
		pd.Error = d.Error
		pd.ErrorCode = v.errCode
		if pd.ErrorCode == "" {
			pd.ErrorCode = recordErrorCode(d)
		}
	}
	if v.running && v.progress != nil {
		p := v.progress
		pd.State = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
		pd.Mode = protoMode(p.Mode)
		pd.Size = p.Size
		pd.Received = p.Received
		pd.SpeedBps = p.SpeedBps
		pd.Connections = int32(p.Connections)
		pd.Segments = protoSegments(p.Segments)
		pd.ConnectionInfo = protoConnections(p.Workers, false)
	}
	if v.running && v.mprogress != nil && d.Kind == model.KindMedia && d.State != model.StateCompleted {
		// A media download's size is exact once it is assembled; while its job runs, the
		// job's byte total or an estimate (liveMediaSize).
		pd.Size, pd.SizeApprox = liveMediaSize(d, v.mprogress)
	}
	if v.running && v.mprogress != nil {
		// A media download's progress is counted in fragments (and bytes received).
		p := v.mprogress
		pd.State = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
		pd.Received = max(p.BytesDone, 0)
		pd.SpeedBps = p.SpeedBps
		pd.Connections = int32(p.Connections)
		pd.ConnectionInfo = protoConnections(p.Workers, true)
		if pd.Media != nil {
			if p.FragmentsTotal > 0 {
				pd.Media.FragmentsTotal = clampInt32(p.FragmentsTotal)
			}
			pd.Media.FragmentsDone = clampInt32(p.FragmentsDone)
		}
	}
	return pd
}

// protoKind maps a download kind to its API value.
func protoKind(k model.Kind) downexv1.DownloadKind {
	switch k {
	case model.KindFile:
		return downexv1.DownloadKind_DOWNLOAD_KIND_FILE
	case model.KindMedia:
		return downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
	}
	return downexv1.DownloadKind_DOWNLOAD_KIND_UNSPECIFIED
}

// protoSettings converts settings to their API form, without the has_password flags of
// the proxy and the site logins (see Manager.protoSettings).
func protoSettings(s Settings) *downexv1.Settings {
	return protoSettingsWith(s, nil)
}

// protoSettingsWith converts settings to their API form; hasSecret reports which
// passwords are stored (nil: none), never the passwords themselves.
func protoSettingsWith(s Settings, hasSecret func(kind, id string) bool) *downexv1.Settings {
	cats := make([]*downexv1.Category, len(s.Categories))
	for i, c := range s.Categories {
		cats[i] = &downexv1.Category{Name: c.Name, Extensions: slices.Clone(c.Extensions), Dir: c.Dir}
	}
	p := &downexv1.Settings{
		MaxConcurrentDownloads:  int32(s.MaxConcurrentDownloads),
		GlobalSpeedLimit:        s.GlobalSpeedLimit,
		DefaultDir:              s.DefaultDir,
		DefaultMaxConnections:   int32(s.DefaultMaxConnections),
		Categories:              cats,
		UseCategoryFolders:      s.UseCategoryFolders,
		ConfirmBrowserDownloads: s.ConfirmBrowserDownloads,
		NotifyOnComplete:        s.NotifyOnComplete,
		NotifyOnFailure:         s.NotifyOnFailure,
		LaunchAtLogin:           s.LaunchAtLogin,
		ShowTrayIcon:            s.ShowTrayIcon,
		ShowProgressWindow:      s.ShowProgressWindow,
		ShowCompleteDialog:      s.ShowCompleteDialog,
	}
	networkToProto(p, s.Network, hasSecret)
	s.Desktop.toProto(p)
	s.Update.setProto(p)
	s.Media.toProto(p)
	s.Polite.toProto(p)
	return p
}

// settingsFromProto converts API settings (a full replacement); validation happens in
// normalize. The has_password flags are ignored.
func settingsFromProto(p *downexv1.Settings) Settings {
	cats := make([]Category, len(p.GetCategories()))
	for i, c := range p.GetCategories() {
		cats[i] = Category{Name: c.GetName(), Extensions: slices.Clone(c.GetExtensions()), Dir: c.GetDir()}
	}
	return Settings{
		MaxConcurrentDownloads:  int(p.GetMaxConcurrentDownloads()),
		GlobalSpeedLimit:        p.GetGlobalSpeedLimit(),
		DefaultDir:              p.GetDefaultDir(),
		DefaultMaxConnections:   int(p.GetDefaultMaxConnections()),
		Categories:              cats,
		UseCategoryFolders:      p.GetUseCategoryFolders(),
		ConfirmBrowserDownloads: p.GetConfirmBrowserDownloads(),
		NotifyOnComplete:        p.GetNotifyOnComplete(),
		NotifyOnFailure:         p.GetNotifyOnFailure(),
		LaunchAtLogin:           p.GetLaunchAtLogin(),
		ShowTrayIcon:            p.GetShowTrayIcon(),
		ShowProgressWindow:      p.GetShowProgressWindow(),
		ShowCompleteDialog:      p.GetShowCompleteDialog(),
		Network:                 networkFromProto(p),
		Desktop:                 desktopFromProto(p),
		Update:                  updateSettingsFromProto(p),
		Media:                   mediaSettingsFromProto(p),
		Polite:                  politeSettingsFromProto(p),
	}
}

// protoPostAction maps a post-queue action to its API value.
func protoPostAction(a model.PostAction) downexv1.PostAction {
	switch a {
	case model.PostActionQuit:
		return downexv1.PostAction_POST_ACTION_QUIT_DOWNEX
	case model.PostActionSleep:
		return downexv1.PostAction_POST_ACTION_SLEEP
	case model.PostActionHibernate:
		return downexv1.PostAction_POST_ACTION_HIBERNATE
	case model.PostActionShutdown:
		return downexv1.PostAction_POST_ACTION_SHUTDOWN
	}
	return downexv1.PostAction_POST_ACTION_UNSPECIFIED
}

// postActionFromProto maps an API post-queue action; ok is false for values this build
// does not know.
func postActionFromProto(a downexv1.PostAction) (model.PostAction, bool) {
	switch a {
	case downexv1.PostAction_POST_ACTION_UNSPECIFIED:
		return model.PostActionNone, true
	case downexv1.PostAction_POST_ACTION_QUIT_DOWNEX:
		return model.PostActionQuit, true
	case downexv1.PostAction_POST_ACTION_SLEEP:
		return model.PostActionSleep, true
	case downexv1.PostAction_POST_ACTION_HIBERNATE:
		return model.PostActionHibernate, true
	case downexv1.PostAction_POST_ACTION_SHUTDOWN:
		return model.PostActionShutdown, true
	}
	return model.PostActionNone, false
}

// protoQueue renders queue q with its downloads ids in queue order.
func protoQueue(q model.Queue, ids []int64) *downexv1.Queue {
	s := q.Schedule
	return &downexv1.Queue{
		Id:            int64(q.ID),
		Name:          q.Name,
		Running:       q.Running,
		MaxConcurrent: int32(q.MaxConcurrent),
		SpeedLimit:    q.SpeedLimit,
		Schedule: &downexv1.Schedule{
			Enabled: s.Enabled, StartMinute: int32(s.StartMinute), StopEnabled: s.StopEnabled,
			StopMinute: int32(s.StopMinute), Weekdays: int32(s.Weekdays), OnceDate: s.OnceDate,
			WakeComputer: s.WakeComputer, RepeatMinutes: int32(s.RepeatMinutes),
		},
		PostAction:  protoPostAction(q.PostAction),
		IsMain:      q.IsMain(),
		DownloadIds: ids,
	}
}
