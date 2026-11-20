package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// TestClientDaemonRescue: the window's Rescue (docs/DESKTOP-SPEC.md §11) against a real
// downexd: a partial file is adopted and completes byte-exact; a refusal keeps its code in
// the Downex-Error-Code metadata the dialog reads.
func TestClientDaemonRescue(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds and runs downexd")
	}
	root := t.TempDir()
	daemon := startTestDaemon(t, root)
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ipc.EnvEndpoint, daemon.endpoint)
	env := client.Default()
	env.StartDetached = func(string, ...string) error { return errors.New("the test starts no daemon") }
	env.DaemonPath = func() (string, error) { return "", errors.New("no downexd for this test") }
	d := NewClientDaemon(env)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	o := testserver.Options{Size: 3 << 20, Seed: 91, ETag: `"h"`}
	srv := testserver.New(o)
	defer srv.Close()
	const kept = 1<<20 + 3
	b := make([]byte, kept)
	testserver.ContentAt(o, b, 0)
	partial := filepath.Join(out, "kept.bin.part")
	if err := os.WriteFile(partial, b, 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := d.Rescue(ctx, &downexv1.RescueRequest{Url: srv.URL + "/kept.bin", PartialPath: partial,
		Source: downexv1.AddSource_ADD_SOURCE_UI})
	if err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	id := resp.GetDownload().GetId()
	if resp.GetRescuedBytes() != kept || resp.GetDownload().GetFilename() != "kept.bin" {
		t.Fatalf("rescued %v", resp)
	}
	for {
		got, err := d.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			break
		}
		if got.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED || ctx.Err() != nil {
			t.Fatalf("download %v", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(out, "kept.bin"))
	want := testserver.Content(o)
	if err != nil || string(data) != string(want) {
		t.Fatalf("the rescued file differs (%d bytes, %v)", len(data), err)
	}

	// Another file: refused with its code.
	other := filepath.Join(out, "other.bin.part")
	b2 := make([]byte, kept)
	testserver.ContentAt(testserver.Options{Size: 3 << 20, Seed: 92}, b2, 0)
	if err := os.WriteFile(other, b2, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = d.Rescue(ctx, &downexv1.RescueRequest{Url: srv.URL + "/kept.bin", PartialPath: other,
		Source: downexv1.AddSource_ADD_SOURCE_UI})
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Meta().Get("Downex-Error-Code") != "rescue_changed" || ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("Rescue of another file = %v", err)
	}
}
