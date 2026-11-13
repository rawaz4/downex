package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"downex/internal/avscan"
	downexv1 "downex/internal/rpc/downex/v1"
)

func TestDefaultDesktopSettings(t *testing.T) {
	d := DefaultDesktopSettings()
	want := DesktopSettings{MarkDownloadedFiles: true, AVScanArgs: "[File]"}
	if d != want {
		t.Fatalf("DefaultDesktopSettings = %+v, want %+v", d, want)
	}
}

func TestDesktopSettingsJSON(t *testing.T) {
	for _, tc := range []struct {
		name, json string
		want       DesktopSettings
	}{
		{"empty object", `{}`, DefaultDesktopSettings()},
		{"marking off", `{"mark_downloaded_files": false}`, DesktopSettings{AVScanArgs: "[File]"}},
		{"clipboard on", `{"clipboard_monitor": true}`, DesktopSettings{ClipboardMonitor: true, MarkDownloadedFiles: true, AVScanArgs: "[File]"}},
		{"everything", `{"clipboard_monitor":true,"mark_downloaded_files":false,"av_scan_enabled":true,"av_scan_command":"/s","av_scan_args":"-q [File]","unknown":1}`,
			DesktopSettings{ClipboardMonitor: true, AVScanEnabled: true, AVScanCommand: "/s", AVScanArgs: "-q [File]"}},
		{"explicit empty args", `{"av_scan_args": ""}`, DesktopSettings{MarkDownloadedFiles: true}},
	} {
		var d DesktopSettings
		if err := json.Unmarshal([]byte(tc.json), &d); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if d != tc.want {
			t.Errorf("%s: decoded %+v, want %+v", tc.name, d, tc.want)
		}
	}
	var d DesktopSettings
	if err := json.Unmarshal([]byte(`{"clipboard_monitor": "yes"}`), &d); err == nil {
		t.Error("a malformed desktop object decoded")
	}
	// What is written is read back unchanged.
	in := DesktopSettings{ClipboardMonitor: true, AVScanEnabled: true, AVScanCommand: "/x", AVScanArgs: "a [File]"}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out DesktopSettings
	if err := json.Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("round trip = %+v, %v; want %+v", out, err, in)
	}
	for _, key := range []string{`"clipboard_monitor"`, `"mark_downloaded_files"`, `"av_scan_enabled"`, `"av_scan_command"`, `"av_scan_args"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("encoded settings lack %s: %s", key, data)
		}
	}
}

// scannerPath returns an existing program usable as a scanner path.
func scannerPath(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestDesktopSettingsNormalize(t *testing.T) {
	exe := scannerPath(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.exe")
	ok := []struct {
		name string
		in   DesktopSettings
		want DesktopSettings
	}{
		{"defaults", DefaultDesktopSettings(), DefaultDesktopSettings()},
		{"empty args become the default", DesktopSettings{AVScanArgs: "  "}, DesktopSettings{AVScanArgs: avscan.DefaultArgs}},
		{"enabled scanner", DesktopSettings{AVScanEnabled: true, AVScanCommand: " " + exe + " ", AVScanArgs: "-q [File]"},
			DesktopSettings{AVScanEnabled: true, AVScanCommand: filepath.Clean(exe), AVScanArgs: "-q [File]"}},
		{"disabled with a missing scanner", DesktopSettings{AVScanCommand: missing, AVScanArgs: "[File]"},
			DesktopSettings{AVScanCommand: missing, AVScanArgs: "[File]"}},
		{"booleans kept", DesktopSettings{ClipboardMonitor: true, AVScanArgs: "[File]"}, DesktopSettings{ClipboardMonitor: true, AVScanArgs: "[File]"}},
	}
	for _, tc := range ok {
		got, err := tc.in.normalize()
		if err != nil {
			t.Errorf("%s: normalize = %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: normalize = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	bad := []struct {
		name string
		in   DesktopSettings
		want string
	}{
		{"enabled without command", DesktopSettings{AVScanEnabled: true}, "av_scan_command is required"},
		{"enabled relative", DesktopSettings{AVScanEnabled: true, AVScanCommand: "scan.exe"}, "absolute"},
		{"enabled missing", DesktopSettings{AVScanEnabled: true, AVScanCommand: missing}, "does not exist"},
		{"enabled directory", DesktopSettings{AVScanEnabled: true, AVScanCommand: dir}, "not a file"},
		{"disabled relative", DesktopSettings{AVScanCommand: "scan.exe"}, "absolute"},
		{"NUL in command", DesktopSettings{AVScanCommand: exe + "\x00"}, "av_scan_command"},
		{"NUL in args", DesktopSettings{AVScanArgs: "[File]\x00"}, "av_scan_args"},
		{"long args", DesktopSettings{AVScanArgs: strings.Repeat("a", avscan.MaxArgsBytes+1)}, "av_scan_args"},
	}
	if runtime.GOOS != "windows" {
		bad = append(bad, struct {
			name string
			in   DesktopSettings
			want string
		}{"unterminated quote", DesktopSettings{AVScanArgs: `"[File]`}, "av_scan_args"})
	} else {
		script := filepath.Join(dir, "scan.bat")
		if err := os.WriteFile(script, []byte("@echo off"), 0o600); err != nil {
			t.Fatal(err)
		}
		bad = append(bad, struct {
			name string
			in   DesktopSettings
			want string
		}{"batch file", DesktopSettings{AVScanEnabled: true, AVScanCommand: script}, "program"})
	}
	for _, tc := range bad {
		_, err := tc.in.normalize()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: normalize = %v, want an error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestDesktopSettingsRepair(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "gone", "scan.exe") // absolute, not existing: kept
	for _, tc := range []struct {
		name     string
		in, want DesktopSettings
		problems int
	}{
		{"valid", DesktopSettings{AVScanEnabled: true, AVScanCommand: abs, AVScanArgs: "[File]"},
			DesktopSettings{AVScanEnabled: true, AVScanCommand: abs, AVScanArgs: "[File]"}, 0},
		{"empty args", DesktopSettings{}, DesktopSettings{AVScanArgs: "[File]"}, 0},
		{"bad args", DesktopSettings{AVScanArgs: "a\x00"}, DesktopSettings{AVScanArgs: "[File]"}, 1},
		{"relative command", DesktopSettings{AVScanEnabled: true, AVScanCommand: "scan", AVScanArgs: "[File]"},
			DesktopSettings{AVScanArgs: "[File]"}, 1},
		{"enabled without command", DesktopSettings{AVScanEnabled: true, AVScanArgs: "[File]"},
			DesktopSettings{AVScanArgs: "[File]"}, 1},
		{"other fields kept", DesktopSettings{ClipboardMonitor: true, MarkDownloadedFiles: false, AVScanArgs: "[File]"},
			DesktopSettings{ClipboardMonitor: true, AVScanArgs: "[File]"}, 0},
	} {
		got, problems := tc.in.repair()
		if got != tc.want || len(problems) != tc.problems {
			t.Errorf("%s: repair = %+v %q, want %+v with %d problems", tc.name, got, problems, tc.want, tc.problems)
		}
	}
}

func TestDesktopSettingsProto(t *testing.T) {
	in := DesktopSettings{ClipboardMonitor: true, MarkDownloadedFiles: false, AVScanEnabled: true, AVScanCommand: "/opt/scan", AVScanArgs: "-x [File]"}
	p := &downexv1.Settings{MaxConcurrentDownloads: 3}
	in.toProto(p)
	if !p.GetClipboardMonitor() || p.GetMarkDownloadedFiles() || !p.GetAvScanEnabled() ||
		p.GetAvScanCommand() != "/opt/scan" || p.GetAvScanArgs() != "-x [File]" || p.GetMaxConcurrentDownloads() != 3 {
		t.Fatalf("toProto = %v", p)
	}
	if out := desktopFromProto(p); out != in {
		t.Fatalf("desktopFromProto = %+v, want %+v", out, in)
	}
	if out := desktopFromProto(nil); out != (DesktopSettings{}) {
		t.Fatalf("desktopFromProto(nil) = %+v", out)
	}
}
