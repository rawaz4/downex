package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

// TestGetAndInstallTools: GetTools renders the tools' status; InstallTools starts or
// joins the install, answers before the caller's deadline while it goes on, and maps its
// failures to tools_unavailable and site_media_off.
func TestGetAndInstallTools(t *testing.T) {
	tools := newFakeTools()
	tools.snap.Tools[1].Error = strings.Repeat("é", 150) + "\nsecond line"
	tools.snap.Tools[1].ErrorCode = "network"
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: newFakeSite(1), tools: tools})
	res, err := h.dm.GetTools(ctxT(t), connect.NewRequest(&downexv1.GetToolsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Msg.GetStatus()
	if !st.GetEnabled() || !st.GetReady() || len(st.GetTools()) != 2 {
		t.Fatalf("status = %v", st)
	}
	y, d := st.GetTools()[0], st.GetTools()[1]
	if y.GetName() != "yt-dlp" || y.GetState() != downexv1.ToolState_TOOL_STATE_READY || y.GetVersion() != "2026.10.10.233141" ||
		!y.GetManaged() || y.GetPath() == "" || y.GetLatestVersion() != "2026.10.10.233141" || y.GetVerified() != "openpgp+sha256" ||
		!y.GetCheckedAt().AsTime().Equal(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)) || y.GetBytesTotal() != -1 {
		t.Errorf("yt-dlp = %v", y)
	}
	if e := d.GetError(); len(e) > maxToolError || strings.ContainsAny(e, "\n") || !strings.HasPrefix(e, "éé") ||
		d.GetErrorCode() != "network" || d.GetCheckedAt() != nil {
		t.Errorf("deno = %v (error %d bytes)", d, len(d.GetError()))
	}

	install := func(update bool, timeout time.Duration) (*downexv1.ToolsStatus, time.Duration, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		start := time.Now()
		res, err := h.dm.InstallTools(ctx, connect.NewRequest(&downexv1.InstallToolsRequest{Update: update}))
		if err != nil {
			return nil, time.Since(start), err
		}
		return res.Msg.GetStatus(), time.Since(start), nil
	}
	if st, _, err := install(true, waitTimeout); err != nil || len(st.GetTools()) != 2 {
		t.Fatalf("install = %v, %v", st, err)
	}
	// At the caller's deadline (less the margin) the status so far is the answer.
	tools.mu.Lock()
	tools.installFor = make(chan struct{})
	tools.mu.Unlock()
	st, took, err := install(false, 3*time.Second)
	if err != nil || len(st.GetTools()) != 2 || took > 2500*time.Millisecond {
		t.Errorf("install at the deadline = %v after %v, %v", st, took, err)
	}
	if _, installs, _ := tools.recorded(); len(installs) != 2 || !installs[0] || installs[1] {
		t.Errorf("installs = %v", installs)
	}
	tools.mu.Lock()
	tools.installFor = nil
	tools.mu.Unlock()

	for _, c := range []struct {
		err  error
		text string
	}{
		{fmt.Errorf("%w: yt-dlp failed its signature check", sitemedia.ErrToolsUnavailable), "yt-dlp failed its signature check"},
		{errors.New("disk full"), "yt-dlp and Deno are not set up: disk full"},
	} {
		tools.mu.Lock()
		tools.installErr = c.err
		tools.mu.Unlock()
		_, _, err := install(false, waitTimeout)
		if mediaCode(t, err) != CodeToolsUnavailable || connect.CodeOf(err) != connect.CodeFailedPrecondition ||
			!strings.Contains(err.Error(), c.text) {
			t.Errorf("failed install: %v", err)
		}
	}

	s := protoSettings(h.m.Settings())
	s.SiteMedia = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	_, _, err = install(false, waitTimeout)
	if mediaCode(t, err) != CodeSiteMediaOff || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("install with site media off: %v", err)
	}
	// The status stays readable: the settings page shows the tools disabled.
	if _, err := h.dm.GetTools(ctxT(t), connect.NewRequest(&downexv1.GetToolsRequest{})); err != nil {
		t.Errorf("GetTools with site media off: %v", err)
	}
}

func TestToolsProtoStates(t *testing.T) {
	for s, want := range map[ToolState]downexv1.ToolState{
		ToolStateUnknown:    downexv1.ToolState_TOOL_STATE_UNSPECIFIED,
		ToolStateMissing:    downexv1.ToolState_TOOL_STATE_MISSING,
		ToolStateInstalling: downexv1.ToolState_TOOL_STATE_INSTALLING,
		ToolStateReady:      downexv1.ToolState_TOOL_STATE_READY,
		ToolStateFailed:     downexv1.ToolState_TOOL_STATE_FAILED,
		ToolStateDisabled:   downexv1.ToolState_TOOL_STATE_DISABLED,
		ToolState(99):       downexv1.ToolState_TOOL_STATE_UNSPECIFIED,
	} {
		if got := protoToolState(s); got != want {
			t.Errorf("protoToolState(%d) = %v, want %v", s, got, want)
		}
	}
	st := protoToolsStatus(ToolsSnapshot{Tools: []ToolSnapshot{{Name: "yt-dlp", State: ToolStateInstalling, BytesDone: -5,
		BytesTotal: -9}}})
	if tt := st.GetTools()[0]; tt.GetBytesDone() != 0 || tt.GetBytesTotal() != -1 || st.GetEnabled() || st.GetReady() {
		t.Errorf("status = %v", st)
	}
	if st := protoToolsStatus(ToolsSnapshot{}); st.GetTools() != nil {
		t.Errorf("no tools = %v", st)
	}
}

// TestToolsAutoCheck: StartTools checks for updates 5 minutes after the start and then
// daily, in timers of at most an hour, only while site media and automatic updates are
// on; turning them on schedules a check 5 minutes later.
func TestToolsAutoCheck(t *testing.T) {
	tools := newFakeTools()
	tools.checkErr = errors.New("offline")
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: newFakeSite(1), tools: tools})
	timers := newFakeTimers()
	stop := StartTools(h.m, ToolsServiceConfig{
		now: timers.clock, afterFunc: timers.afterFunc, jitter: func(time.Duration) time.Duration { return 0 },
	})
	defer stop()
	checks := func() int { _, _, n := tools.recorded(); return n }

	timers.wantArmed(t, 5*time.Minute)
	timers.advance(4 * time.Minute)
	timers.fire() // early: only re-armed
	timers.wantArmed(t, time.Minute)
	if checks() != 0 {
		t.Fatal("checked before the first delay")
	}
	timers.advance(time.Minute)
	timers.fire()
	timers.wantArmed(t, time.Hour) // 24 h, in timers of at most an hour
	if checks() != 1 {
		t.Fatalf("%d checks after the first delay", checks())
	}
	timers.advance(24 * time.Hour)
	timers.fire()
	timers.wantArmed(t, time.Hour)
	if checks() != 2 {
		t.Fatalf("%d checks after a day", checks())
	}

	// Automatic updates off: the timer runs, nothing is checked.
	s := protoSettings(h.m.Settings())
	s.ToolsAutoUpdate = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	timers.advance(24 * time.Hour)
	timers.fire()
	timers.wantArmed(t, time.Hour)
	if checks() != 2 {
		t.Error("checked with tools_auto_update off")
	}
	// On again: the next check is 5 minutes away.
	s.ToolsAutoUpdate = true
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	timers.wantArmed(t, 5*time.Minute)

	// The service ends with the manager.
	if err := h.m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("the tools service did not end with the manager")
	}
}

func TestStartToolsJitter(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	StartTools(h.m, ToolsServiceConfig{})() // without Config.Tools: nothing to stop
	tools := newFakeTools()
	h2 := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: newFakeSite(1), tools: tools})
	timers := newFakeTimers()
	stop := StartTools(h2.m, ToolsServiceConfig{now: timers.clock, afterFunc: timers.afterFunc,
		jitter: func(limit time.Duration) time.Duration { return limit / 2 }})
	defer stop()
	timers.wantArmed(t, 5*time.Minute+30*time.Second)
}
