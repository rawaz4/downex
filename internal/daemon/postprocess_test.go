package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"downex/internal/avscan"
	"downex/internal/filemark"
	"downex/internal/model"
)

// postFake records the calls of a PostProcessor's Mark and Scan.
type postFake struct {
	calls    []string
	origin   filemark.Origin
	markErr  error
	markPan  bool
	scanArgs []string
	result   avscan.Result
	scanErr  error
	scanCtx  context.Context
}

func (f *postFake) processor() *PostProcessor {
	return &PostProcessor{
		Mark: func(path string, o filemark.Origin) error {
			f.calls = append(f.calls, "mark "+path)
			f.origin = o
			if f.markPan {
				panic("mark exploded")
			}
			return f.markErr
		},
		Scan: func(ctx context.Context, command, args, file string) (avscan.Result, error) {
			f.calls = append(f.calls, "scan "+file)
			f.scanArgs = []string{command, args, file}
			f.scanCtx = ctx
			return f.result, f.scanErr
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
		Log: slog.New(slog.DiscardHandler),
	}
}

func completedDownload(dir string) *model.Download {
	return &model.Download{
		ID:       7,
		Request:  model.Request{URL: "https://user:pw@example.com/get?id=1", Headers: http.Header{"Referer": {"https://example.com/page"}}},
		FinalURL: "https://cdn.example.com/file.zip?sig=s",
		Dir:      dir,
		Filename: "file.zip",
		State:    model.StateCompleted,
		Size:     4,
	}
}

var scanOn = DesktopSettings{MarkDownloadedFiles: true, AVScanEnabled: true, AVScanCommand: "/opt/scan", AVScanArgs: "-q [File]"}

func TestPostProcessMarksThenScans(t *testing.T) {
	dir := t.TempDir()
	d := completedDownload(dir)
	before := *d
	f := &postFake{}
	res := f.processor().Process(context.Background(), d, scanOn)
	path := filepath.Join(dir, "file.zip")
	if want := []string{"mark " + path, "scan " + path}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %q, want %q", f.calls, want)
	}
	if want := (filemark.Origin{URL: "https://cdn.example.com/file.zip?sig=s", Referrer: "https://example.com/page", Time: time.Unix(1700000000, 0)}); f.origin != want {
		t.Fatalf("origin = %+v, want %+v", f.origin, want)
	}
	if want := []string{"/opt/scan", "-q [File]", path}; !reflect.DeepEqual(f.scanArgs, want) {
		t.Fatalf("scan arguments = %q, want %q", f.scanArgs, want)
	}
	if res != (PostResult{Marked: true, Scan: ScanClean}) || res.Alarming() || res.NotificationText("file.zip") != "" {
		t.Fatalf("result = %+v", res)
	}
	if !reflect.DeepEqual(*d, before) {
		t.Fatalf("Process changed the download: %+v", *d)
	}
}

func TestPostProcessOutcomes(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		result   avscan.Result
		scanErr  error
		want     ScanOutcome
		exit     int
		note     string
		notified string
	}{
		{"clean", context.Background(), avscan.Result{}, nil, ScanClean, 0, "", ""},
		{"problem", context.Background(), avscan.Result{ExitCode: 2, Output: "Threat found"}, nil, ScanProblem, 2,
			"Antivirus scan reported a problem (exit code 2)", "Scanner reported a problem with file.zip"},
		{"killed", context.Background(), avscan.Result{ExitCode: -1}, nil, ScanProblem, -1,
			"Antivirus scan reported a problem (exit code -1)", "Scanner reported a problem with file.zip"},
		{"timeout", context.Background(), avscan.Result{}, avscan.ErrTimeout, ScanFailed, 0,
			"Antivirus scan did not finish within 10m0s", "Could not scan file.zip"},
		{"cannot start", context.Background(), avscan.Result{}, errors.New("exec: permission denied"), ScanFailed, 0,
			"Antivirus scan could not run", "Could not scan file.zip"},
		{"shutdown", canceled, avscan.Result{}, context.Canceled, ScanFailed, 0,
			"Antivirus scan was interrupted", "Could not scan file.zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &postFake{result: tc.result, scanErr: tc.scanErr}
			res := f.processor().Process(tc.ctx, completedDownload(t.TempDir()), scanOn)
			if res.Scan != tc.want || res.ExitCode != tc.exit || res.Note != tc.note {
				t.Fatalf("result = %+v, want scan %d exit %d note %q", res, tc.want, tc.exit, tc.note)
			}
			if got := res.NotificationText("file.zip"); got != tc.notified {
				t.Fatalf("notification %q, want %q", got, tc.notified)
			}
			if res.Alarming() != (tc.notified != "") {
				t.Fatalf("Alarming = %v", res.Alarming())
			}
			if f.scanCtx != tc.ctx {
				t.Fatal("the scan did not get the caller's context")
			}
			if strings.Contains(res.Note, "example.com") || strings.Contains(res.Note, "/opt/scan") {
				t.Fatalf("note %q repeats a URL or path", res.Note)
			}
		})
	}
}

func TestPostProcessSettings(t *testing.T) {
	path := func(dir string) string { return filepath.Join(dir, "file.zip") }
	for _, tc := range []struct {
		name string
		s    DesktopSettings
		want func(dir string) []string
	}{
		{"defaults: mark only", DefaultDesktopSettings(), func(dir string) []string { return []string{"mark " + path(dir)} }},
		{"nothing", DesktopSettings{AVScanArgs: "[File]"}, func(string) []string { return nil }},
		{"scan only", DesktopSettings{AVScanEnabled: true, AVScanCommand: "/s", AVScanArgs: "[File]"},
			func(dir string) []string { return []string{"scan " + path(dir)} }},
		{"scan enabled without a command", DesktopSettings{AVScanEnabled: true, AVScanArgs: "[File]"}, func(string) []string { return nil }},
		{"command without enabling", DesktopSettings{AVScanCommand: "/s", AVScanArgs: "[File]"}, func(string) []string { return nil }},
	} {
		dir := t.TempDir()
		f := &postFake{}
		res := f.processor().Process(context.Background(), completedDownload(dir), tc.s)
		if want := tc.want(dir); !reflect.DeepEqual(f.calls, want) {
			t.Errorf("%s: calls = %q, want %q", tc.name, f.calls, want)
		}
		if res.Alarming() || res.Note != "" {
			t.Errorf("%s: result = %+v", tc.name, res)
		}
	}
}

func TestPostProcessNeverFails(t *testing.T) {
	dir := t.TempDir()
	// A failed mark is only logged: the scan still runs.
	f := &postFake{markErr: errors.New("no streams on FAT32")}
	res := f.processor().Process(context.Background(), completedDownload(dir), scanOn)
	if res.Marked || res.Scan != ScanClean || res.Note != "" || len(f.calls) != 2 {
		t.Fatalf("after a failed mark: result %+v, calls %q", res, f.calls)
	}
	// A panic is contained.
	f = &postFake{markPan: true}
	res = f.processor().Process(context.Background(), completedDownload(dir), scanOn)
	if res.Scan != ScanFailed || res.Note == "" {
		t.Fatalf("after a panic: result %+v", res)
	}
	f = &postFake{markPan: true}
	res = f.processor().Process(context.Background(), completedDownload(dir), DefaultDesktopSettings())
	if res.Alarming() || res.Note != "" {
		t.Fatalf("after a panic without scanning: result %+v", res)
	}
	// No file name: nothing to do.
	f = &postFake{}
	d := completedDownload(dir)
	d.Filename = ""
	if res := f.processor().Process(context.Background(), d, scanOn); res != (PostResult{}) || len(f.calls) != 0 {
		t.Fatalf("without a file name: result %+v, calls %q", res, f.calls)
	}
	// Missing hooks are skipped.
	p := &PostProcessor{}
	if res := p.Process(context.Background(), completedDownload(dir), scanOn); res != (PostResult{}) {
		t.Fatalf("without hooks: result %+v", res)
	}
}

func TestDownloadOrigin(t *testing.T) {
	now := time.Unix(1, 0)
	d := &model.Download{Request: model.Request{URL: "https://a.example/x"}}
	if o := downloadOrigin(d, now); o != (filemark.Origin{URL: "https://a.example/x", Time: now}) {
		t.Fatalf("plain origin = %+v", o)
	}
	d.Kind = model.KindMedia
	d.Media = &model.Media{PageURL: "https://a.example/watch"}
	if o := downloadOrigin(d, now); o.Referrer != "https://a.example/watch" {
		t.Fatalf("media origin = %+v", o)
	}
	d.Request.Headers = http.Header{"Referer": {"https://ref.example/"}}
	if o := downloadOrigin(d, now); o.Referrer != "https://ref.example/" {
		t.Fatalf("media origin with a Referer = %+v", o)
	}
}

// TestPostProcessRealMark runs the real marking on a temporary file; the scan is
// replaced. Where the file system cannot be marked, the result says so and the
// download is still fine.
func TestPostProcessRealMark(t *testing.T) {
	dir := t.TempDir()
	d := completedDownload(dir)
	if err := os.WriteFile(filepath.Join(dir, d.Filename), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPostProcessor(testLogger(t))
	p.Scan = nil
	res := p.Process(context.Background(), d, DefaultDesktopSettings())
	if res.Alarming() || res.Note != "" {
		t.Fatalf("result = %+v", res)
	}
	if runtime.GOOS == "windows" && res.Marked {
		data, err := os.ReadFile(filepath.Join(dir, d.Filename) + ":" + filemark.ZoneStream)
		if err != nil {
			t.Fatal(err)
		}
		if s := string(data); !strings.Contains(s, "ZoneId=3") || !strings.Contains(s, "HostUrl=https://cdn.example.com/file.zip?sig=s") ||
			strings.Contains(s, "pw@") {
			t.Fatalf("Zone.Identifier = %q", s)
		}
	}
	t.Logf("marked: %v", res.Marked)
}
