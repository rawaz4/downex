package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"downex/internal/avscan"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/transport"
)

func TestErrorCode(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("segment 3: %w", err) }
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{wrap(engine.ErrValidatorChanged), CodeValidatorChanged},
		{wrap(fmt.Errorf("write: %w: %w", engine.ErrDiskFull, syscall.ENOSPC)), CodeDiskFull},
		{wrap(fmt.Errorf("%w: x509: unknown authority", engine.ErrTLS)), CodeTLS},
		{wrap(transport.ErrInsecureRedirect), CodeTLS},
		{wrap(&engine.StatusError{Code: 404}), CodeHTTPStatus},
		{wrap(engine.ErrRangeIgnored), CodeHTTPStatus},
		{wrap(transport.ErrRedirects), CodeHTTPStatus},
		{wrap(transport.ErrTooLarge), CodeHTTPStatus},
		{wrap(engine.ErrStalled), CodeStalled},
		{wrap(engine.ErrInvalidRequest), CodeInvalidRequest},
		{wrap(transport.ErrInvalidRequest), CodeInvalidRequest},
		{wrap(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), CodeNetwork},
		{wrap(&net.DNSError{Err: "no such host", Name: "x.invalid"}), CodeNetwork},
		{wrap(io.ErrUnexpectedEOF), CodeNetwork},
		{wrap(errors.New("http2: server sent GOAWAY")), CodeNetwork},
		{wrap(&os.PathError{Op: "open", Path: "x", Err: os.ErrPermission}), CodeInternal},
		// A file error carrying an errno (which satisfies net.Error) is not a network error.
		{wrap(&os.PathError{Op: "write", Path: "x", Err: syscall.Errno(112)}), CodeInternal},
		{wrap(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.Errno(5)}), CodeInternal},
		{wrap(syscall.Errno(5)), CodeInternal},
		{wrap(store.ErrClosed), CodeInternal},
		{errors.New("something else"), CodeInternal},
	}
	for _, c := range cases {
		if got := ErrorCode(c.err); got != c.want {
			t.Errorf("ErrorCode(%v) = %q, want %q", c.err, got, c.want)
		}
		if c.err == nil {
			continue
		}
		// The persisted message maps the same way, except for network errors that only
		// a type (not a sentinel text) identifies.
		if got := codeFromMessage(c.err.Error()); got != c.want && c.want != CodeNetwork {
			t.Errorf("codeFromMessage(%q) = %q, want %q", c.err.Error(), got, c.want)
		}
	}
	for msg, want := range map[string]string{
		"":                               "",
		"dial tcp: connection refused":   CodeNetwork,
		"read: connection reset by peer": CodeNetwork,
		"lookup x.invalid: no such host": CodeNetwork,
		"unexpected EOF":                 CodeNetwork,
		"unexpected HTTP status 503 Service Unavailable": CodeHTTPStatus,
		"save checkpoint: database is locked":            CodeInternal,
	} {
		if got := codeFromMessage(msg); got != want {
			t.Errorf("codeFromMessage(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestProtoConversion(t *testing.T) {
	states := map[model.State]downexv1.DownloadState{
		model.StateQueued:      downexv1.DownloadState_DOWNLOAD_STATE_QUEUED,
		model.StateProbing:     downexv1.DownloadState_DOWNLOAD_STATE_PROBING,
		model.StateDownloading: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
		model.StatePaused:      downexv1.DownloadState_DOWNLOAD_STATE_PAUSED,
		model.StateCompleted:   downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED,
		model.StateFailed:      downexv1.DownloadState_DOWNLOAD_STATE_FAILED,
		model.StateCanceled:    downexv1.DownloadState_DOWNLOAD_STATE_CANCELED,
		model.State(99):        downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED,
	}
	for in, want := range states {
		if got := protoState(in); got != want {
			t.Errorf("protoState(%d) = %v", in, got)
		}
	}
	if protoMode(model.ModeUnknown) != downexv1.Mode_MODE_UNSPECIFIED || protoMode(model.ModeRanged) != downexv1.Mode_MODE_RANGED ||
		protoMode(model.ModeSingle) != downexv1.Mode_MODE_SINGLE {
		t.Error("protoMode")
	}
	if protoTime(time.Time{}) != nil {
		t.Error("zero time is not absent")
	}
	created := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	v := downloadView{
		rec: model.Download{
			ID: 4, Request: model.Request{URL: "https://h/x"}, FinalURL: "https://h/y", Dir: "/d", Filename: "f",
			Size: 100, Mode: model.ModeRanged, State: model.StateFailed, MaxConnections: 4, SpeedLimit: 9,
			Error: "engine: remote file changed", CreatedAt: created, UpdatedAt: created,
		},
		received: 40, queuePosition: 0,
	}
	p := v.toProto()
	if p.GetId() != 4 || p.GetUrl() != "https://h/x" || p.GetFinalUrl() != "https://h/y" || p.GetReceived() != 40 ||
		p.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_FAILED || p.GetErrorCode() != CodeValidatorChanged ||
		!p.GetCreatedAt().AsTime().Equal(created) || p.GetCompletedAt() != nil || p.GetSpeedBps() != 0 {
		t.Fatalf("failed proto = %v", p)
	}
	v.rec.State, v.rec.Error = model.StatePaused, "stale"
	if p := v.toProto(); p.GetError() != "" || p.GetErrorCode() != "" {
		t.Fatalf("error shown for a paused download: %v", p)
	}
	v.running = true
	v.progress = &engine.Progress{Mode: model.ModeRanged, Size: 100, Received: 70, SpeedBps: 5, Connections: 2,
		Segments: []engine.SegmentView{{Start: 0, End: 49, Received: 40}, {Start: 50, End: 99, Received: 30}}}
	if p := v.toProto(); p.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING || p.GetReceived() != 70 ||
		p.GetConnections() != 2 || len(p.GetSegments()) != 2 || p.GetSegments()[1].GetReceived() != 30 {
		t.Fatalf("live proto = %v", p)
	}
	s := DefaultSettings()
	s.MaxConcurrentDownloads, s.GlobalSpeedLimit, s.DefaultDir, s.DefaultMaxConnections = 2, 3, "/x", 4
	s.Categories[1].Dir, s.LaunchAtLogin, s.NotifyOnFailure = "/music", true, false
	if got := settingsFromProto(protoSettings(s)); !got.Equal(s) {
		t.Fatalf("settings round trip = %+v", got)
	}
}

func TestSettingsFile(t *testing.T) {
	dir := t.TempDir()
	downloads := func() (string, error) { return dir, nil }
	path := filepath.Join(dir, "sub", SettingsFileName)

	s, err := loadSettings(path)
	if err != nil || !s.Equal(DefaultSettings()) {
		t.Fatalf("missing file = %+v, %v", s, err)
	}
	want := Settings{MaxConcurrentDownloads: 5, GlobalSpeedLimit: 1000, DefaultDir: dir, DefaultMaxConnections: 16,
		Categories: []Category{{Name: "Books", Extensions: []string{"epub"}, Dir: dir}}, ShowTrayIcon: true}
	if err := saveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	if s, err := loadSettings(path); err != nil || !s.Equal(want) {
		t.Fatalf("round trip = %+v, %v", s, err)
	}
	// No categories and false booleans are kept as such, not replaced by the defaults.
	none := Settings{MaxConcurrentDownloads: 1}
	if err := saveSettings(path, none); err != nil {
		t.Fatal(err)
	}
	if s, err := loadSettings(path); err != nil || !s.Equal(none) || s.Categories == nil {
		t.Fatalf("round trip without categories = %+v, %v", s, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "sub", ".settings-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
	// A corrupt file does not stop the daemon: it starts with the defaults.
	tf, err := transport.NewHTTPFactory(transport.HTTPOptions{Proxy: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), Config{Store: store.NewMemory(), Transport: tf, SettingsPath: path, DownloadsDir: downloads})
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultSettings()
	defaults.MaxConcurrentDownloads, defaults.DefaultDir, defaults.DefaultMaxConnections = 3, dir, DefaultMaxConnections
	if got := m.Settings(); !got.Equal(defaults) {
		t.Fatalf("settings after a corrupt file = %+v", got)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, problems := Settings{MaxConcurrentDownloads: 99, GlobalSpeedLimit: -1, DefaultDir: "relative",
		DefaultMaxConnections: 77}.repair(downloads)
	if !got.Equal(Settings{MaxConcurrentDownloads: 3, DefaultDir: dir, DefaultMaxConnections: DefaultMaxConnections,
		Desktop: DesktopSettings{AVScanArgs: avscan.DefaultArgs}}) || len(problems) != 4 {
		t.Fatalf("repair = %+v, %q", got, problems)
	}
	noDownloads := func() (string, error) { return "", errors.New("unknown") }
	got, problems = Settings{}.repair(noDownloads)
	if got.DefaultDir != "" || got.MaxConcurrentDownloads != 3 || len(problems) != 1 {
		t.Fatalf("repair without a Downloads folder = %+v, %q", got, problems)
	}
}

func TestRunIntentPriority(t *testing.T) {
	for _, c := range []struct {
		seq  []intent
		want intent
	}{
		{[]intent{intentRequeue, intentShutdown}, intentShutdown},
		{[]intent{intentShutdown, intentRequeue}, intentShutdown},
		{[]intent{intentPause, intentShutdown}, intentPause},
		{[]intent{intentShutdown, intentPause}, intentPause},
		{[]intent{intentPause, intentRestart}, intentRestart},
		{[]intent{intentRestart, intentPause}, intentPause},
		{[]intent{intentRemove, intentPause, intentRestart, intentShutdown}, intentRemove},
		{[]intent{intentRequeue, intentRemove}, intentRemove},
		{[]intent{intentPause, intentResume}, intentResume},
		{[]intent{intentResume, intentShutdown}, intentResume},
		{[]intent{intentRemove, intentResume}, intentRemove},
	} {
		r := &run{}
		for _, i := range c.seq {
			r.setIntent(i)
		}
		if r.intent != c.want {
			t.Errorf("%v → %v, want %v", c.seq, r.intent, c.want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:pw@host.example:8443/a/b?token=secret#frag": "https://host.example:8443/a/b",
		"http://h/":   "http://h/",
		"http://[::1": "(unparsable URL)",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
