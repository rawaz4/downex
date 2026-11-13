package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func freeSpaceCall(t *testing.T, a *App, req any) (json.RawMessage, error) {
	t.Helper()
	raw, ok := req.(string)
	if !ok {
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	return a.FreeSpace(json.RawMessage(raw))
}

func TestFreeSpaceReadsTheVolume(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	dir := t.TempDir()
	raw, err := freeSpaceCall(t, a, map[string]string{"dir": dir})
	if err != nil {
		t.Fatalf("FreeSpace(temp dir) = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != 1 {
		t.Fatalf("FreeSpace = %s, %v; want one field", raw, err)
	}
	s, ok := got["freeBytes"].(string)
	if !ok {
		t.Fatalf("freeBytes = %#v, want a decimal string", got["freeBytes"])
	}
	if n, err := strconv.ParseInt(s, 10, 64); err != nil || n <= 0 {
		t.Errorf("freeBytes = %q, want a positive int64", s)
	}

	// A folder that does not exist yet is measured at its nearest existing parent.
	if _, err := freeSpaceCall(t, a, map[string]string{"dir": filepath.Join(dir, "not", "yet")}); err != nil {
		t.Errorf("FreeSpace(missing folder) = %v", err)
	}
}

func TestFreeSpaceAnswersInt64AsADecimalString(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	var asked string
	a.freeSpace = func(dir string) (int64, error) {
		asked = dir
		return 229_780_750_336_123, nil
	}
	dir := t.TempDir()
	raw, err := freeSpaceCall(t, a, map[string]string{"dir": dir})
	if want := `{"freeBytes":"229780750336123"}`; err != nil || string(raw) != want {
		t.Errorf("FreeSpace = %s, %v; want %s", raw, err, want)
	}
	if asked != dir {
		t.Errorf("read %q, want the folder asked for", asked)
	}
}

func TestFreeSpaceRefusesWithoutEchoingThePath(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	reads := 0
	a.freeSpace = func(string) (int64, error) {
		reads++
		return 1, nil
	}
	secret := "Users/someone/secret-folder"
	for _, req := range []string{
		`{"dir": ""}`,
		`{}`,
		``,
		`null`,
		`{"dir": "` + secret + `"}`,
		`{"dir": "./` + secret + `"}`,
		`{"dir": "relative"}`,
	} {
		raw, err := freeSpaceCall(t, a, req)
		if err == nil {
			t.Errorf("FreeSpace(%s) = %s, want a refusal", req, raw)
			continue
		}
		if want := "invalid_argument: dir must be an absolute path"; err.Error() != want {
			t.Errorf("FreeSpace(%s) = %q, want %q", req, err, want)
		}
	}
	if runtime.GOOS == "windows" {
		// Rooted but without a drive: relative to the current drive.
		if _, err := freeSpaceCall(t, a, `{"dir": "\\`+strings.ReplaceAll(secret, "/", `\\`)+`"}`); err == nil {
			t.Error("FreeSpace(\\Users\\…) is accepted; want a refusal")
		}
	}
	for _, req := range []string{`{"dir": 5}`, `{"dir": "/x", "other": 1}`, `[`, `"` + secret + `"`} {
		_, err := freeSpaceCall(t, a, req)
		if err == nil || err.Error() != "invalid_argument: malformed request" {
			t.Errorf("FreeSpace(%s) = %v, want invalid_argument: malformed request", req, err)
		}
	}
	if reads != 0 {
		t.Errorf("refused requests read the free space %d times", reads)
	}
}

func TestFreeSpaceUnavailableHidesTheError(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	dir := filepath.Join(t.TempDir(), "private-name")
	for _, fail := range []func(string) (int64, error){
		func(d string) (int64, error) {
			return 0, errors.New("platform: free space of " + d + ": access is denied")
		},
		func(string) (int64, error) { return -1, nil },
	} {
		a.freeSpace = fail
		raw, err := freeSpaceCall(t, a, map[string]string{"dir": dir})
		if err == nil {
			t.Fatalf("FreeSpace = %s, want unavailable", raw)
		}
		if want := "unavailable: could not read the free space"; err.Error() != want {
			t.Errorf("FreeSpace = %q, want %q", err, want)
		}
		if strings.Contains(err.Error(), "private-name") {
			t.Errorf("the error repeats the folder: %q", err)
		}
	}
}
