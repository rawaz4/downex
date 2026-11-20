package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

const testTicket = "0123456789abcdef0123456789abcdef"

// addVideo forwards browserFetch, and added carries the download's fetch ticket
// (docs/SITE-MEDIA-SPEC.md §2.8); without one there is none.
func TestAddVideoFetchTicket(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}, ticket: testTicket}
	s := startSession(t, be)
	s.send(map[string]any{"id": "a", "type": "addVideo", "pageUrl": "https://www.tiktok.example/@u/video/1", "browserFetch": true})
	m := s.recv()
	if m["ok"] != true || m["fetchTicket"] != testTicket {
		t.Fatalf("added = %v", m)
	}
	if !be.videoAdds[0].GetBrowserFetch() {
		t.Fatal("browserFetch did not reach the daemon")
	}
	be.ticket = ""
	s.send(map[string]any{"id": "b", "type": "addVideo", "pageUrl": "https://www.tiktok.example/@u/video/1"})
	m = s.recv()
	if _, ok := m["fetchTicket"]; ok || be.videoAdds[1].GetBrowserFetch() {
		t.Fatalf("no browserFetch: added = %v, forwarded %v", m, be.videoAdds[1].GetBrowserFetch())
	}
	s.send(map[string]any{"id": "c", "type": "addVideo", "pageUrl": "https://www.tiktok.example/@u/video/1", "browserFetch": "yes"})
	if m = s.recv(); m["ok"] != false {
		t.Fatalf("a browserFetch that is not a boolean: %v", m)
	}
}

// A videoInfo of a page whose video servers answer only browsers says so; others do not.
func TestVideoInfoBrowserFetch(t *testing.T) {
	info := siteInfo(1)
	info.BrowserFetch = true
	fields, err := videoInfo(info)
	if err != nil || fields["browserFetch"] != true {
		t.Fatalf("videoInfo = %v, %v", fields, err)
	}
	info.BrowserFetch = false
	if fields, _ = videoInfo(info); fields["browserFetch"] != nil {
		t.Fatalf("browserFetch on an ordinary page: %v", fields)
	}
}

// fetchTake and fetchPut reach the daemon as they were sent (data decoded), and their
// answers come back; invalid ones are refused before.
func TestFetchTakeAndPut(t *testing.T) {
	be := &fakeBackend{takeResp: &downexv1.TakeBrowserFetchResponse{
		Fetch:        &downexv1.BrowserFetch{Ticket: testTicket, FetchId: "7", Url: "https://media.example/v.mp4?sig=x", Start: 0, End: 1023, Total: 4096},
		EndedTickets: []string{strings.Repeat("f", 32)},
	}, putStop: true}
	s := startSession(t, be)
	s.send(map[string]any{"id": "t", "type": "fetchTake", "tickets": []string{testTicket, strings.Repeat("f", 32)}})
	m := s.recv()
	f, _ := m["fetch"].(map[string]any)
	if m["type"] != "fetchTask" || f["url"] != "https://media.example/v.mp4?sig=x" || f["fetchId"] != "7" || f["total"] != float64(4096) ||
		len(m["ended"].([]any)) != 1 {
		t.Fatalf("fetchTask = %v", m)
	}
	if got := be.takes[0]; got.GetWaitMs() != defFetchWaitMs || len(got.GetTickets()) != 2 {
		t.Fatalf("take forwarded = %v", got)
	}
	data := bytes.Repeat([]byte{0, 1, 2, 255}, 1000)
	s.send(map[string]any{"id": "p", "type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 0,
		"head": map[string]any{"status": 206, "contentType": "video/mp4", "contentRange": "bytes 0-3999/4096", "contentLength": 4000},
		"data": base64.StdEncoding.EncodeToString(data), "end": true})
	m = s.recv()
	if m["type"] != "fetchAck" || m["stop"] != true {
		t.Fatalf("fetchAck = %v", m)
	}
	p := be.puts[0]
	if !bytes.Equal(p.GetData(), data) || !p.GetEnd() || p.GetHead().GetStatus() != 206 || p.GetHead().GetContentLength() != 4000 ||
		p.GetHead().GetContentRange() != "bytes 0-3999/4096" {
		t.Fatalf("put forwarded = %v", p)
	}
	s.send(map[string]any{"id": "q", "type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 1, "error": "no_tab"})
	s.recv()
	if p := be.puts[1]; p.GetError() != "no_tab" || p.GetHead() != nil || p.GetData() != nil {
		t.Fatalf("error put forwarded = %v", p)
	}

	for name, msg := range map[string]map[string]any{
		"no tickets":      {"type": "fetchTake", "tickets": []string{}},
		"bad ticket":      {"type": "fetchTake", "tickets": []string{"XYZ"}},
		"too many":        {"type": "fetchTake", "tickets": make([]string, 65)},
		"long wait":       {"type": "fetchTake", "tickets": []string{testTicket}, "waitMs": 25001},
		"put no ticket":   {"type": "fetchPut", "fetchId": "7", "seq": 0},
		"put no fetch":    {"type": "fetchPut", "ticket": testTicket, "seq": 0},
		"put no seq":      {"type": "fetchPut", "ticket": testTicket, "fetchId": "7"},
		"negative seq":    {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": -1},
		"bad status":      {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 0, "head": map[string]any{"status": 99}},
		"bad length":      {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 0, "head": map[string]any{"status": 200, "contentLength": -2}},
		"not base64":      {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 1, "data": "%%%"},
		"too much data":   {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 1, "data": base64.StdEncoding.EncodeToString(make([]byte, maxFetchPiece+1))},
		"long error":      {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 1, "error": strings.Repeat("e", 257)},
		"control in type": {"type": "fetchPut", "ticket": testTicket, "fetchId": "7", "seq": 0, "head": map[string]any{"status": 200, "contentType": "video/mp4\n"}},
	} {
		msg["id"] = "x"
		s.send(msg)
		if m := s.recv(); m["ok"] != false || m["error"].(map[string]any)["code"] != codeInvalidRequest {
			t.Errorf("%s: %v", name, m)
		}
	}
	if len(be.takes) != 1 || len(be.puts) != 2 {
		t.Errorf("invalid requests reached the daemon: %d takes, %d puts", len(be.takes), len(be.puts))
	}
}

// A fetch never starts downexd: with none running, it is daemon_unavailable; once one
// runs, its answers come through the real daemon (a ticket it never made is ended).
func TestFetchNeverStartsDaemon(t *testing.T) {
	d := newDaemonServer(t)
	ld := &lateDaemon{t: t, delay: 30 * time.Millisecond}
	s := startSession(t, &daemonBackend{env: d.env(ld.isUp, ld.start)})
	s.send(map[string]any{"id": "t", "type": "fetchTake", "tickets": []string{testTicket}, "waitMs": 0})
	if m := s.recv(); errorCode(m) != codeDaemonUnavailable {
		t.Fatalf("take without downexd: %v", m)
	}
	s.send(map[string]any{"id": "p", "type": "fetchPut", "ticket": testTicket, "fetchId": "1", "seq": 0, "end": true})
	if m := s.recv(); errorCode(m) != codeDaemonUnavailable {
		t.Fatalf("put without downexd: %v", m)
	}
	if n := ld.startCount(); n != 0 {
		t.Fatalf("a fetch started downexd %d times", n)
	}
	s.send(map[string]any{"id": "h", "type": "hello", "protocol": 1, "browser": "chrome"})
	s.recv()
	for i := 0; i < 100 && !ld.isUp(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	s.send(map[string]any{"id": "t2", "type": "fetchTake", "tickets": []string{testTicket}, "waitMs": 0})
	m := s.recv()
	if ended, _ := m["ended"].([]any); m["type"] != "fetchTask" || len(ended) != 1 || ended[0] != testTicket || m["fetch"] != nil {
		t.Fatalf("take of an unknown ticket: %v", m)
	}
	s.send(map[string]any{"id": "p2", "type": "fetchPut", "ticket": testTicket, "fetchId": "1", "seq": 0, "end": true})
	if m := s.recv(); m["type"] != "fetchAck" || m["stop"] != true {
		t.Fatalf("put of an unknown fetch: %v", m)
	}
}
