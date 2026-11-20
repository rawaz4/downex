package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// rescueTarget is an absolute path on every OS.
var rescueTarget = filepath.Join(string(filepath.Separator)+"home", "u", "Downloads", "big.iso")

func init() {
	if v := filepath.VolumeName(rescueTarget); v == "" && filepath.Separator == '\\' {
		rescueTarget = `C:\Users\u\Downloads\big.iso`
	}
}

func rescueMsg(kv ...any) map[string]any {
	m := map[string]any{"id": "r", "type": "rescue", "url": "https://example.test/big.iso", "target": rescueTarget,
		"bytesReceived": 1000, "startPaused": true}
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] == nil {
			delete(m, kv[i].(string))
		} else {
			m[kv[i].(string)] = kv[i+1]
		}
	}
	return m
}

// TestRescueValidation: the fields of rescue and resume (docs/BROWSER-SPEC.md §2).
func TestRescueValidation(t *testing.T) {
	for name, msg := range map[string]map[string]any{
		"no url":             rescueMsg("url", nil),
		"ftp url":            rescueMsg("url", "ftp://example.test/a"),
		"no target":          rescueMsg("target", nil),
		"relative target":    rescueMsg("target", "big.iso"),
		"target with NUL":    rescueMsg("target", rescueTarget+"\x00"),
		"long target":        rescueMsg("target", rescueTarget+strings.Repeat("a", maxRescuePathBytes)),
		"no count":           rescueMsg("bytesReceived", nil),
		"negative count":     rescueMsg("bytesReceived", -1),
		"fractional count":   rescueMsg("bytesReceived", 1.5),
		"huge count":         rescueMsg("bytesReceived", float64(1<<60)),
		"total below -1":     rescueMsg("totalBytes", -2),
		"negative start":     rescueMsg("startTime", -5),
		"etag with newline":  rescueMsg("etag", "\"a\nb\""),
		"long date":          rescueMsg("lastModified", strings.Repeat("x", maxRescueValidatorBytes+1)),
		"startPaused string": rescueMsg("startPaused", "yes"),
		"no startPaused":     rescueMsg("startPaused", nil),
		"bad header":         rescueMsg("headers", [][]string{{"Cookie", "a\r\nb"}}),
		"user in page":       rescueMsg("pageUrl", "https://u:p@example.test/"),
		"zero deadline":      rescueMsg("deadlineMs", 0),
		"resume without id":  {"id": "r", "type": "resume"},
		"resume id zero":     {"id": "r", "type": "resume", "downloadId": 0},
		"resume id string":   {"id": "r", "type": "resume", "downloadId": "7"},
	} {
		t.Run(name, func(t *testing.T) {
			be := &fakeBackend{}
			s := startSession(t, be)
			s.send(msg)
			if m := s.recv(); errorCode(m) != codeInvalidRequest || m["id"] != "r" {
				t.Fatalf("response %v", m)
			}
			if len(be.rescues) != 0 || len(be.resumes) != 0 {
				t.Fatal("an invalid request reached the daemon")
			}
		})
	}
}

// TestRescueForwarded: a rescue becomes RescueRequest (source BROWSER) within
// rescueTimeout and the extension's deadline; rescued and resumed answers.
func TestRescueForwarded(t *testing.T) {
	be := &fakeBackend{}
	s := startSessionFor(t, be, rescueTimeout+10*time.Second) // outlives the 30 s budget checked below
	s.send(rescueMsg("totalBytes", 5000, "startTime", 1_700_000_000_000, "etag", `W/"e"`,
		"lastModified", "Mon, 02 Jan 2026 10:00:00 GMT", "filename", "big.iso", "deadlineMs", 30000,
		"headers", [][]string{{"Cookie", "sid=1"}}, "pageUrl", "https://example.test/page"))
	m := s.recv()
	if m["type"] != "rescued" || m["downloadId"] != float64(1) || m["keptBytes"] != float64(500) {
		t.Fatalf("response %v", m)
	}
	be.mu.Lock()
	req, dl := be.rescues[0], be.deadlines[0]
	be.mu.Unlock()
	if req.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || req.GetTargetPath() != rescueTarget ||
		req.GetPartialPath() != "" || req.GetBytesReceived() != 1000 || req.GetExpectedSize() != 5000 ||
		req.GetStartedAtMs() != 1_700_000_000_000 || req.GetEtag() != `W/"e"` || !req.GetStartPaused() ||
		req.GetFilename() != "big.iso" || len(req.GetHeaders()) != 1 || req.GetPageUrl() != "https://example.test/page" {
		t.Fatalf("forwarded %+v", req)
	}
	if left := time.Until(dl); left > 30*time.Second-responseMargin || left < 20*time.Second {
		t.Fatalf("deadline %v ahead, want just under 30 s", left)
	}
	s.send(map[string]any{"id": "s", "type": "resume", "downloadId": 1})
	if m := s.recv(); m["type"] != "resumed" || m["downloadId"] != float64(1) || m["id"] != "s" {
		t.Fatalf("resume response %v", m)
	}
	if be.resumes[0] != 1 {
		t.Fatalf("resumes %v", be.resumes)
	}
}

// TestRescueRefusalReason: a refusal is `rejected` with the daemon's code as reason.
func TestRescueRefusalReason(t *testing.T) {
	ce := connect.NewError(connect.CodeFailedPrecondition, errors.New("rescue_changed: the server's bytes 1-2 differ"))
	ce.Meta().Set(errorCodeMeta, "rescue_changed")
	s := startSession(t, &fakeBackend{rescueErr: ce})
	s.send(rescueMsg())
	m := s.recv()
	e, _ := m["error"].(map[string]any)
	if errorCode(m) != codeRejected || e["reason"] != "rescue_changed" {
		t.Fatalf("response %v", m)
	}
	s.send(map[string]any{"id": "s", "type": "resume", "downloadId": 4})
	if m := s.recv(); errorCode(m) != codeRejected {
		t.Fatalf("resume response %v", m)
	}
}

// TestRescueLogRedacted: the log names the target's base name only and never a header
// value or the query.
func TestRescueLogRedacted(t *testing.T) {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	r := &relay{be: &fakeBackend{}, log: newTestJSONLogger(w)}
	in := bytes.NewBuffer(nil)
	in.Write(jsonFrame(t, rescueMsg("url", "https://example.test/big.iso?sig=query-secret",
		"headers", [][]string{{"Cookie", "sid=cookie-secret"}})))
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	log := w.buf.String()
	w.mu.Unlock()
	for _, secret := range []string{"cookie-secret", "query-secret", filepath.Dir(rescueTarget)} {
		if strings.Contains(log, strings.ReplaceAll(secret, `\`, `\\`)) {
			t.Errorf("%q in the log: %s", secret, log)
		}
	}
	if !strings.Contains(log, `"target":"big.iso"`) || !strings.Contains(log, `"kept_bytes":500`) {
		t.Errorf("log lacks the target or the kept bytes: %s", log)
	}
}
