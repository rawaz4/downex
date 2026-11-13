//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// TestAddRefusesRemoteAndDevicePaths (SEC-3): a client (the browser relay forwards web
// data) cannot make the daemon touch a network share, which would authenticate to that
// host with the user's credentials, nor a device or long-path name that skips Win32
// normalisation. Nothing is created and no connection is made: parseAdd refuses first.
func TestAddRefusesRemoteAndDevicePaths(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)
	base := t.TempDir()
	for _, dir := range []string{
		`\\127.0.0.1\downexreviewnoshare\x`,
		`//127.0.0.1/downexreviewnoshare/x`,
		`\\attacker.example@SSL@443\DavWWWRoot\x`,
		`\\?\UNC\127.0.0.1\downexreviewnoshare\x`,
		`\\?\` + filepath.Join(base, "long"),
		`\\.\` + filepath.Join(base, "dev"),
		`\\?\GLOBALROOT\Device\HarddiskVolume1\x`,
		`\??\` + filepath.Join(base, "nt"),
		filepath.Join(base, "NUL"),
		filepath.Join(base, "con.txt"),
		filepath.Join(base, "COM1"),
		filepath.Join(base, "lpt¹"),
		filepath.Join(base, "trailing."),
		filepath.Join(base, "trailing "),
		filepath.Join(base, "x:stream"),
		filepath.Join(base, "a|b"),
	} {
		_, err := h.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{Url: "http://127.0.0.1:9/f", Dir: dir}))
		if connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("dir %q: %v, want InvalidArgument", dir, err)
		}
		if _, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{
			Settings: &downexv1.Settings{DefaultDir: dir},
		})); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("default_dir %q: %v, want InvalidArgument", dir, err)
		}
	}
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Errorf("refused directories were created: %v %v", entries, err)
	}
	for _, dir := range []string{
		filepath.Join(base, "plain"),
		strings.ReplaceAll(filepath.Join(base, "slashes"), `\`, "/"),
		filepath.Join(base, "New folder (2)"),
		filepath.Join(base, "console"), // only the exact device names are reserved
	} {
		if _, err := h.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{
			Url: "http://127.0.0.1:9/f", Dir: dir, StartPaused: true,
		})); err != nil {
			t.Errorf("dir %q: %v", dir, err)
		}
	}
}

// TestRedirectedDownloadsFolderIsTrusted: the system's Downloads folder may be
// redirected to a network share; it stays usable as the default directory (and when a
// client names it explicitly), while other shares are refused.
func TestRedirectedDownloadsFolderIsTrusted(t *testing.T) {
	const share = `\\127.0.0.1\downex-test-share\alice\Downloads` // fails fast if touched
	downloads := func() (string, error) { return share, nil }
	s, err := Settings{}.normalize(downloads)
	if err != nil || s.DefaultDir != share {
		t.Fatalf("default settings = %+v, %v; want the redirected Downloads folder", s, err)
	}
	if s, err := (Settings{DefaultDir: strings.ToUpper(share)}).normalize(downloads); err != nil || !strings.EqualFold(s.DefaultDir, share) {
		t.Fatalf("explicit Downloads folder = %+v, %v", s, err)
	}
	if _, err := (Settings{DefaultDir: `\\fileserver\other`}).normalize(downloads); err == nil {
		t.Fatal("another share was accepted as default_dir")
	}
	in, err := parseAdd(&downexv1.AddRequest{Url: "http://example.com/f"}, share)
	if err != nil || in.dir != share {
		t.Fatalf("add with the default dir = %q, %v", in.dir, err)
	}
	if _, err := parseAdd(&downexv1.AddRequest{Url: "http://example.com/f", Dir: share}, share); err != nil {
		t.Fatalf("add naming the default dir: %v", err)
	}
	if _, err := parseAdd(&downexv1.AddRequest{Url: "http://example.com/f", Dir: `\\evil.example\share`}, share); err == nil {
		t.Fatal("add accepted another share")
	}
	// A persisted share that is not the Downloads folder is replaced by the default.
	r, problems := Settings{DefaultDir: `\\evil.example\share`}.repair(downloads)
	if r.DefaultDir != share || len(problems) != 1 {
		t.Fatalf("repair = %+v, %q", r, problems)
	}
}
