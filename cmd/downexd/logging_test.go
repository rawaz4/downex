package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

func TestRotatingWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	w, err := openRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := range 12 {
		line := fmt.Sprintf("line %02d %s\n", i, strings.Repeat("x", 20)) // 30 bytes
		lines = append(lines, line)
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late")); err == nil {
		t.Fatal("write after close succeeded")
	}
	var got []string
	for _, p := range []string{path + ".2", path + ".1", path} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 100 {
			t.Errorf("%s has %d bytes", filepath.Base(p), len(data))
		}
		got = append(got, string(data))
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("more backups than configured")
	}
	// The newest 9 lines survive, in order, across the files.
	if all := strings.Join(got, ""); all != strings.Join(lines[3:], "") {
		t.Fatalf("logs = %q", all)
	}

	// Reopening appends and counts the existing size.
	w, err = openRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if w.size != int64(len(got[2])) {
		t.Fatalf("size after reopen = %d", w.size)
	}
	_ = w.Close()
}

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: redactAttr}))
	log.Info("request",
		"Cookie", "session=TOPSECRET1",
		"authorization", "Bearer TOPSECRET2",
		"headers", http.Header{"Proxy-Authorization": {"Basic TOPSECRET3"}, "Referer": {"https://ref.example/"}},
		slog.Group("req", "Set-Cookie", "TOPSECRET4", "body", []byte("TOPSECRET5")),
		"url", "https://host.example/file")
	out := buf.String()
	if strings.Contains(out, "TOPSECRET") {
		t.Fatalf("credential logged: %s", out)
	}
	for _, want := range []string{redacted, "https://ref.example/", "https://host.example/file"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "": slog.LevelInfo, "warn": slog.LevelWarn,
		"warning": slog.LevelWarn, "error": slog.LevelError,
	} {
		if got, err := parseLevel(in); err != nil || got != want {
			t.Errorf("parseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseLevel("loud"); err == nil {
		t.Error("unknown level accepted")
	}
}

func TestFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(t.Context(), []string{"--version"}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), version) {
		t.Fatalf("--version: %d %q", code, out.String())
	}
	for _, args := range [][]string{{"--bogus"}, {"extra"}, {"--log-level", "loud"}} {
		if code := run(t.Context(), args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	errOut.Reset()
	if code := run(t.Context(), []string{"-h"}, &out, &errOut); code != exitOK || !strings.Contains(errOut.String(), "-endpoint path") {
		t.Fatalf("-h: %d %q", code, errOut.String())
	}
}

// TestURLCredentialsRedacted (SEC-4): URLs carry credentials in their user information
// (sent as Authorization: Basic) and in signed queries. Wherever a URL is logged (an
// attribute, a stored error message, the text of a *url.Error, the message itself) only
// its scheme, host and path remain.
func TestURLCredentialsRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: redactAttr}))
	signed := "https://bucket.example/obj.bin?X-Amz-Signature=SIGSECRET&X-Amz-Credential=CREDSECRET#FRAGSECRET"
	log.Warn("server does not support byte ranges", "url", "https://alice:PASSSECRET@files.example/big.iso")
	log.Warn("probe failed", "err", fmt.Errorf("probe: %w", &url.Error{Op: "Get", URL: signed, Err: errors.New("connection refused")}))
	log.Warn("download failed", "err", `Get "`+signed+`": dial tcp: lookup bucket.example: no such host`)
	log.Info("redirected to http://u:PASSSECRET2@other.example/x?token=TOKSECRET now", "final_url", must(url.Parse(signed)))
	out := buf.String()
	for _, secret := range []string{"PASSSECRET", "SIGSECRET", "CREDSECRET", "FRAGSECRET", "TOKSECRET"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %s:\n%s", secret, out)
		}
	}
	for _, want := range []string{"https://[redacted]@files.example/big.iso", "https://bucket.example/obj.bin?[redacted]",
		"no such host", "connection refused", "http://[redacted]@other.example/x?[redacted] now"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestDaemonLogRedactsEngineURLs (SEC-4): the engine logs the request URL when a server
// ignores ranges; through the daemon's logger the credentials in it never reach the
// log file.
func TestDaemonLogRedactsEngineURLs(t *testing.T) {
	ts := testserver.New(testserver.Options{Size: 64 << 10, Seed: 3, NoRanges: true})
	t.Cleanup(ts.Close)
	endpoint := testEndpoint(t)
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	d := startDaemon(t, []string{"--db", filepath.Join(dir, "downex.db"), "--endpoint", endpoint, "--log-dir", logDir})
	c := dial(t, endpoint)
	waitReady(t, c, d)
	u := strings.Replace(ts.URL, "http://", "http://alice:PASSSECRET@", 1) + "/f.bin?sig=SIGSECRET"
	res, err := c.dl.Add(t.Context(), connect.NewRequest(&downexv1.AddRequest{Url: u, Dir: filepath.Join(dir, "out")}))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		g, err := c.dl.Get(t.Context(), connect.NewRequest(&downexv1.GetRequest{Id: res.Msg.GetDownload().GetId()}))
		if err == nil && g.Msg.GetDownload().GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not complete: %v %v", g, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.cancel()
	d.wait(t)
	logs, err := os.ReadFile(filepath.Join(logDir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logs, []byte("byte ranges")) {
		t.Fatalf("the engine's warning is missing:\n%s", logs)
	}
	for _, secret := range []string{"PASSSECRET", "SIGSECRET"} {
		if bytes.Contains(logs, []byte(secret)) {
			t.Errorf("log contains %s:\n%s", secret, logs)
		}
	}
}
