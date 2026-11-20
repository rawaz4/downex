package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// playedMsg is a probePlayed or addPlayed message with two files.
func playedMsg(id, typ string) map[string]any {
	return map[string]any{"id": id, "type": typ, "pageUrl": "https://www.social.example/feed?x=1",
		"played": map[string]any{"durationMs": 19_000, "videoWidth": 720, "videoHeight": 1280, "hasAudio": 1,
			"otherDurationsMs": []any{6243, 46_320},
			"resources": []any{
				map[string]any{"id": "r1", "url": "https://cdn.example/v/a.mp4?sig=secret", "size": 3_000_000, "kind": "video",
					"head": base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x10ftypisom\x00\x00\x00\x00")), "browser": true},
				map[string]any{"id": "r2", "url": "https://cdn2.example/v/b.mp4", "head": base64.StdEncoding.EncodeToString([]byte("head"))},
			}}}
}

// probePlayed and addPlayed reach the daemon with origin PLAYED and the decoded files, and
// answer videoInfo (origin played) and added with the fetch ticket; the log keeps counts,
// never an address or a byte.
func TestProbeAndAddPlayed(t *testing.T) {
	w := &testWriter{t: t}
	t.Cleanup(w.stop)
	be := &videoBackend{fakeBackend: &fakeBackend{}, ticket: testTicket}
	be.probe = func(req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
		m := siteInfo(1)
		m.Origin = downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED
		return m, nil
	}
	r := &relay{be: be, log: newTestJSONLogger(w)}
	in := bytes.NewBuffer(nil)
	in.Write(jsonFrame(t, playedMsg("p", "probePlayed")))
	add := playedMsg("a", "addPlayed")
	add["videoTrack"], add["title"] = "r1", "someone - a caption"
	in.Write(jsonFrame(t, add))
	var out bytes.Buffer
	if err := r.serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	var answers []map[string]any
	for rd := bytes.NewReader(out.Bytes()); rd.Len() > 0; {
		b, err := readFrame(rd)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		answers = append(answers, m)
	}
	// The video pool answers in any order.
	if len(answers) == 2 && answers[0]["id"] == "a" {
		answers[0], answers[1] = answers[1], answers[0]
	}
	if len(answers) != 2 || answers[0]["type"] != "videoInfo" || answers[0]["origin"] != "played" ||
		answers[1]["ok"] != true || answers[1]["fetchTicket"] != testTicket {
		t.Fatalf("answers %v", answers)
	}
	p := be.probes[0]
	if p.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED || len(p.GetPlayed().GetResources()) != 2 ||
		!bytes.Contains(p.GetPlayed().GetResources()[0].GetHead(), []byte("ftypisom")) ||
		!p.GetPlayed().GetResources()[0].GetBrowser() || p.GetPlayed().GetDurationMs() != 19_000 || p.GetPlayed().GetHasAudio() != 1 ||
		len(p.GetPlayed().GetOtherDurationsMs()) != 2 || p.GetPlayed().GetOtherDurationsMs()[0] != 6243 {
		t.Fatalf("forwarded %v", p)
	}
	a := be.videoAdds[0]
	if a.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED || a.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER ||
		a.GetVideoTrack() != "r1" || a.GetTitle() != "someone - a caption" || len(a.GetPlayed().GetResources()) != 2 {
		t.Fatalf("forwarded add %v", a)
	}
	w.mu.Lock()
	logs := w.buf.String()
	w.mu.Unlock()
	if strings.Contains(logs, "cdn.example") || strings.Contains(logs, "secret") || !strings.Contains(logs, `"head_bytes"`) ||
		!strings.Contains(logs, `"files":2`) {
		t.Fatalf("log: %s", logs)
	}
}

func TestPlayedValidation(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	for name, edit := range map[string]func(m map[string]any){
		"no page":   func(m map[string]any) { delete(m, "pageUrl") },
		"no source": func(m map[string]any) { delete(m, "played") },
		"no files":  func(m map[string]any) { m["played"].(map[string]any)["resources"] = []any{} },
		"bad head":  func(m map[string]any) { file(m)["head"] = "%%%" },
		"no head":   func(m map[string]any) { delete(file(m), "head") },
		"big head": func(m map[string]any) {
			file(m)["head"] = base64.StdEncoding.EncodeToString(make([]byte, 300<<10))
		},
		"bad url":   func(m map[string]any) { file(m)["url"] = "ftp://cdn.example/a" },
		"bad kind":  func(m map[string]any) { file(m)["kind"] = "muxed" },
		"bad size":  func(m map[string]any) { file(m)["size"] = -1 },
		"bad audio": func(m map[string]any) { m["played"].(map[string]any)["hasAudio"] = 2 },
		"bad ms":    func(m map[string]any) { m["played"].(map[string]any)["durationMs"] = -5 },
		"bad other": func(m map[string]any) { m["played"].(map[string]any)["otherDurationsMs"] = []any{0} },
		"many others": func(m map[string]any) {
			m["played"].(map[string]any)["otherDurationsMs"] = []any{1, 2, 3, 4, 5, 6, 7, 8, 9}
		},
		"wide": func(m map[string]any) { m["played"].(map[string]any)["videoWidth"] = 20000 },
		"too many": func(m map[string]any) {
			var many []any
			for range 17 {
				many = append(many, map[string]any{"id": "r1", "url": "https://cdn.example/a.mp4", "head": "aGVhZA=="})
			}
			m["played"].(map[string]any)["resources"] = many
		},
		"too much head": func(m map[string]any) {
			var many []any
			for range 5 {
				many = append(many, map[string]any{"id": "r1", "url": "https://cdn.example/a.mp4",
					"head": base64.StdEncoding.EncodeToString(make([]byte, 250<<10))})
			}
			m["played"].(map[string]any)["resources"] = many
		},
	} {
		msg := playedMsg("v", "probePlayed")
		edit(msg)
		s.send(msg)
		if m := s.recv(); m["ok"] != false {
			t.Errorf("%s: %v", name, m)
		}
	}
	if len(be.probes) != 0 {
		t.Fatalf("an invalid request reached the daemon: %d", len(be.probes))
	}
}

func file(m map[string]any) map[string]any {
	return m["played"].(map[string]any)["resources"].([]any)[0].(map[string]any)
}
