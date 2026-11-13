package settings_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

// A round trip through a real downexd, built from this tree and run as ui/host's integration
// test runs it: on its own --endpoint, --db and --log-dir with --no-tray, its per-user
// folders inside the test's folder, never the user's own downexd. $DOWNEX_SETTINGS_SCRATCH is
// the folder to build and run it in (else the test's temporary folder);
// $DOWNEX_HOST_TEST_DOWNEXD names a built downexd to use instead. -short skips it.

// realDaemon starts downexd and returns the window's daemon for it.
func realDaemon(t *testing.T) host.Daemon {
	t.Helper()
	root := os.Getenv("DOWNEX_SETTINGS_SCRATCH")
	if root == "" {
		root = t.TempDir()
	} else {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		root = filepath.Join(root, "roundtrip-"+hex.EncodeToString(b))
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exe := os.Getenv("DOWNEX_HOST_TEST_DOWNEXD")
	if exe == "" {
		exe = filepath.Join(root, "downexd")
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		gotool, err := exec.LookPath("go")
		if err != nil {
			gotool = filepath.Join(runtime.GOROOT(), "bin", "go")
		}
		cmd := exec.Command(gotool, "build", "-o", exe, "downex/cmd/downexd")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build downexd: %v\n%s", err, out)
		}
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	endpoint := `\\.\pipe\downex-settings-test-` + hex.EncodeToString(b)
	if runtime.GOOS != "windows" {
		dir, err := os.MkdirTemp("", "dxs-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		endpoint = filepath.Join(dir, "rpc.sock")
	}
	user := filepath.Join(root, "user")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "--db", filepath.Join(root, "state", "downex.db"), "--endpoint", endpoint,
		"--log-dir", filepath.Join(root, "logs"), "--no-tray")
	cmd.Dir = root
	over := map[string]string{ipc.EnvEndpoint: endpoint, "DOWNEX_DAEMON": "", "LOCALAPPDATA": user, "APPDATA": user,
		"XDG_STATE_HOME": user, "XDG_CONFIG_HOME": user, "XDG_DATA_HOME": user, "HOME": user}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := over[name]; ok {
			continue
		}
		if runtime.GOOS == "windows" {
			skip := false
			for k := range over {
				skip = skip || strings.EqualFold(k, name)
			}
			if skip {
				continue
			}
		}
		cmd.Env = append(cmd.Env, kv)
	}
	for k, v := range over {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start downexd: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	raw := client.Default().Client(endpoint)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = raw.Daemon.Shutdown(ctx, connect.NewRequest(&downexv1.ShutdownRequest{}))
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Errorf("downexd did not stop after Shutdown; killed")
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		st, err := raw.Daemon.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
		cancel()
		if err == nil {
			if int(st.Msg.GetPid()) != cmd.Process.Pid {
				t.Fatalf("the endpoint is served by pid %d, not the started downexd %d", st.Msg.GetPid(), cmd.Process.Pid)
			}
			break
		}
		select {
		case <-done:
			t.Fatalf("downexd exited: %v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("downexd did not answer: %v\n%s", err, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Setenv(ipc.EnvEndpoint, endpoint)
	env := client.Default()
	env.StartDetached = func(string, ...string) error { return errors.New("the test starts no daemon") }
	env.DaemonPath = func() (string, error) { return "", errors.New("no downexd for this test") }
	return host.NewClientDaemon(env)
}

// olderBuild is a window that does not know a field: GetSettings hands it the field only as
// the bytes downexd sent (unknown fields), as an older build's proto messages hold it.
type olderBuild struct {
	host.Daemon
	hidden protoreflect.Name
}

func (o olderBuild) GetSettings(ctx context.Context) (*downexv1.GetSettingsResponse, error) {
	resp, err := o.Daemon.GetSettings(ctx)
	if err != nil || resp.GetSettings() == nil {
		return resp, err
	}
	s := proto.CloneOf(resp.GetSettings())
	r := s.ProtoReflect()
	fd := r.Descriptor().Fields().ByName(o.hidden)
	one := r.New()
	one.Set(fd, r.Get(fd))
	b, err := proto.Marshal(one.Interface())
	if err != nil {
		return nil, err
	}
	r.Clear(fd)
	r.SetUnknown(append(r.GetUnknown(), b...))
	return &downexv1.GetSettingsResponse{Settings: s}, nil
}

func TestSaveKeepsUnknownFieldsWithRealDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds and runs downexd")
	}
	real := realDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// A value only the newer field carries: the complete dialog on (its default false bytes
	// would be none).
	if _, err := host.EditSettings(ctx, real, func(s *downexv1.Settings) error {
		s.ShowCompleteDialog = true
		s.MaxConcurrentDownloads = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	h := viewstest.New(t, viewstest.Options{})
	h.App.Daemon = olderBuild{Daemon: real, hidden: "show_complete_dialog"}
	var saved []*downexv1.Settings
	d := open(h, views.SettingsRequest{Page: validate.PageDownloads}, settings.Options{OnSaved: func(s *downexv1.Settings) { saved = append(saved, s) }})
	waitFor(t, func() bool { h.Settle(); return !buttonNamed(t, d.Card(), "Save").Disabled })
	in := inputNamed(t, d.Card(), "Simultaneous downloads")
	if in.Text() != "3" {
		t.Fatalf("loaded %q", in.Text())
	}
	typeInto(h, in, "5")
	press(h, buttonNamed(t, d.Card(), "Save"))
	waitFor(t, func() bool { h.Settle(); return len(saved) == 1 })

	got, err := real.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.GetSettings(); s.GetMaxConcurrentDownloads() != 5 || !s.GetShowCompleteDialog() {
		t.Errorf("stored: max %d, complete dialog %v (the field this build does not know was lost)", s.GetMaxConcurrentDownloads(), s.GetShowCompleteDialog())
	}
	// The control: the same message without its unknown fields loses the value, so the
	// check above can fail.
	older, err := olderBuild{Daemon: real, hidden: "show_complete_dialog"}.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lost := proto.CloneOf(older.GetSettings())
	lost.ProtoReflect().SetUnknown(nil)
	if _, err := real.UpdateSettings(ctx, &downexv1.UpdateSettingsRequest{Settings: lost}); err != nil {
		t.Fatal(err)
	}
	if got, _ := real.GetSettings(ctx); got.GetSettings().GetShowCompleteDialog() {
		t.Error("the control kept the value without its unknown fields")
	}
}
