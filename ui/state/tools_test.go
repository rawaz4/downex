package state_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

const (
	toolInstalling = downexv1.ToolState_TOOL_STATE_INSTALLING
	toolReady      = downexv1.ToolState_TOOL_STATE_READY
	toolFailed     = downexv1.ToolState_TOOL_STATE_FAILED
)

func withTools(patch func(t *downexv1.ToolInfo), status func(s *downexv1.ToolsStatus)) *downexv1.ToolsStatus {
	return statetest.Tools(func(s *downexv1.ToolsStatus) {
		for _, t := range s.Tools {
			patch(t)
		}
		if status != nil {
			status(s)
		}
	})
}

func notReady(s *downexv1.ToolsStatus) { s.Ready = false }

func TestToolsMeasuresTheInstall(t *testing.T) {
	installing := withTools(func(x *downexv1.ToolInfo) {
		x.State, x.Version = toolInstalling, ""
		if x.Name == "deno" {
			x.BytesDone, x.BytesTotal = 30*MiB, 45*MiB
		} else {
			x.BytesDone, x.BytesTotal = 6*MiB, 18*MiB
		}
	}, notReady)
	if !state.IsInstalling(installing) || math.Abs(state.InstallFraction(installing)-36.0/63) > 1e-9 {
		t.Fatal(state.InstallFraction(installing))
	}
	if got := state.InstallText(installing, math.NaN(), true); got != "Downloading yt-dlp and Deno: 57%" {
		t.Fatal(got)
	}
	// a total not known yet: no percentage (unless the daemon's message gave one)
	unknown := withTools(func(x *downexv1.ToolInfo) {
		x.State = toolReady
		if x.Name == "deno" {
			x.State = toolInstalling
		}
		x.BytesTotal = -1
	}, notReady)
	if !math.IsNaN(state.InstallFraction(unknown)) {
		t.Fatal(state.InstallFraction(unknown))
	}
	for _, c := range []struct{ got, want string }{
		{state.InstallText(unknown, math.NaN(), true), "Downloading Deno…"},
		{state.InstallText(unknown, 0.4, true), "Downloading Deno: 40%"},
		{state.InstallText(nil, math.NaN(), true), "Downloading yt-dlp and Deno…"},
		// beside a progress bar the percentage is the bar's alone
		{state.InstallText(installing, math.NaN(), false), "Downloading yt-dlp and Deno…"},
		{state.ToolName("deno"), "Deno"},
		{state.ToolName("yt-dlp"), "yt-dlp"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
	if !math.IsNaN(state.InstallFraction(statetest.Tools(nil))) {
		t.Fatal("fraction of ready tools")
	}
}

func TestToolsTellsTheFirstInstallFromAnUpdate(t *testing.T) {
	first := withTools(func(x *downexv1.ToolInfo) { x.State = toolInstalling }, notReady)
	if !state.IsFirstInstall(first) {
		t.Fatal("first")
	}
	ready := proto.CloneOf(first)
	ready.Ready = true // a ready copy keeps working
	off := proto.CloneOf(first)
	off.Enabled = false
	if state.IsFirstInstall(ready) || state.IsFirstInstall(off) || state.IsFirstInstall(nil) {
		t.Fatal("not first")
	}
	failed := state.FailedTools(withTools(func(x *downexv1.ToolInfo) {
		if x.Name == "deno" {
			x.State = toolFailed
		}
	}, nil))
	if len(failed) != 1 || failed[0].GetName() != "deno" || len(state.FailedTools(nil)) != 0 {
		t.Fatal(failed)
	}
}

// fakeTools answers GetTools and InstallTools as a test says.
type fakeTools struct {
	mu       sync.Mutex
	gets     int
	installs int
	get      func() (*downexv1.ToolsStatus, error)
	install  func(update bool) (*downexv1.ToolsStatus, error)
}

func (f *fakeTools) GetTools(context.Context) (*downexv1.GetToolsResponse, error) {
	f.mu.Lock()
	f.gets++
	get := f.get
	f.mu.Unlock()
	if get == nil {
		return &downexv1.GetToolsResponse{Status: statetest.Tools(nil)}, nil
	}
	s, err := get()
	return &downexv1.GetToolsResponse{Status: s}, err
}

func (f *fakeTools) InstallTools(_ context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error) {
	f.mu.Lock()
	f.installs++
	install := f.install
	f.mu.Unlock()
	if install == nil {
		return &downexv1.InstallToolsResponse{Status: statetest.Tools(nil)}, nil
	}
	s, err := install(req.GetUpdate())
	return &downexv1.InstallToolsResponse{Status: s}, err
}

func newTools(t *testing.T, f *fakeTools) (*state.ToolsStore, *state.Hub) {
	t.Helper()
	hub := &state.Hub{}
	s := state.NewTools(state.ToolsOptions{Daemon: f, Events: hub, Go: statetest.Sync})
	t.Cleanup(s.Dispose)
	return s, hub
}

func TestToolsStoreFollowsEventsAndReads(t *testing.T) {
	f := &fakeTools{}
	s, hub := newTools(t, f)
	s.Start()
	if s.Status() != nil {
		t.Fatal(s.Status())
	}
	installing := withTools(func(x *downexv1.ToolInfo) { x.State, x.BytesDone, x.BytesTotal = toolInstalling, 1, 4 }, notReady)
	hub.Publish(state.Batch{statetest.Removed(3), statetest.ToolsEvent(installing)})
	if !proto.Equal(s.Status(), installing) || !s.Installing() || s.Fraction() != 0.25 {
		t.Fatal(s.Status())
	}
	s.Load()
	if f.gets != 1 || !proto.Equal(s.Status(), statetest.Tools(nil)) || s.Installing() {
		t.Fatal(s.Status())
	}
	s.Dispose()
	hub.Publish(state.Batch{statetest.ToolsEvent(installing)})
	if !proto.Equal(s.Status(), statetest.Tools(nil)) {
		t.Fatal("followed after Dispose")
	}
}

func TestToolsStoreKnowsADownexdWithoutSiteMedia(t *testing.T) {
	s, _ := newTools(t, &fakeTools{get: func() (*downexv1.ToolsStatus, error) {
		return nil, errors.New("unimplemented: downex.v1.DaemonService.GetTools is not implemented")
	}})
	s.Load()
	if s.Supported() || s.LoadError() != "" {
		t.Fatal(s.Supported(), s.LoadError())
	}
}

func TestToolsStoreReportsAReadThatFailedOtherwise(t *testing.T) {
	s, _ := newTools(t, &fakeTools{get: func() (*downexv1.ToolsStatus, error) {
		return nil, errors.New("unavailable: downexd is not running")
	}})
	s.Load()
	if !s.Supported() || s.LoadError() != "downexd is not running" {
		t.Fatal(s.Supported(), s.LoadError())
	}
}

func TestToolsStoreInstallsOnceAtATime(t *testing.T) {
	finish := make(chan *downexv1.ToolsStatus)
	entered := make(chan struct{})
	f := &fakeTools{install: func(bool) (*downexv1.ToolsStatus, error) { close(entered); return <-finish, nil }}
	s, _ := newTools(t, f)
	first := make(chan bool)
	go func() { first <- s.Install(ctx, false) }()
	<-entered
	if s.Busy() != state.Installing {
		t.Fatal(s.Busy())
	}
	if s.Install(ctx, true) { // joined: nothing more is asked
		t.Fatal("a second install")
	}
	if f.installs != 1 {
		t.Fatal(f.installs)
	}
	finish <- statetest.Tools(nil)
	if !<-first || s.Busy() != state.NotBusy || !s.Status().GetReady() {
		t.Fatal(s.Busy(), s.Status())
	}

	f.mu.Lock()
	f.install = func(bool) (*downexv1.ToolsStatus, error) {
		return nil, errors.New("failed_precondition: tools_unavailable: yt-dlp and Deno are not set up: yt-dlp failed its signature check")
	}
	f.mu.Unlock()
	if s.Install(ctx, true) {
		t.Fatal("succeeded")
	}
	if got := s.InstallError(); got != "Downex could not set up yt-dlp/Deno: yt-dlp failed its signature check." {
		t.Fatal(got)
	}
	if f.gets == 0 { // what the failure left is read again
		t.Fatal("not read again")
	}
}
