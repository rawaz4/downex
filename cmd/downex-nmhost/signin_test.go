package main

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The user's sign-in (docs/SITE-MEDIA-SPEC.md §6.3): probeVideo and addVideo forward the
// Cookie header and cookieDomain; a cookieDomain that is no lowercase DNS name is refused
// before the daemon is asked.
func TestVideoSignInDomain(t *testing.T) {
	be := &videoBackend{fakeBackend: &fakeBackend{}}
	s := startSession(t, be)
	page := "https://www.instagram.com/reel/AbCdEf/"
	cookie := [][]string{{"Cookie", "sessionid=s1"}}
	s.send(map[string]any{"id": "p", "type": "probeVideo", "pageUrl": page, "headers": cookie, "cookieDomain": "instagram.com"})
	if m := s.recv(); m["type"] != "videoInfo" {
		t.Fatalf("probeVideo: %v", m)
	}
	s.send(map[string]any{"id": "a", "type": "addVideo", "pageUrl": page, "headers": cookie, "cookieDomain": "instagram.com"})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("addVideo: %v", m)
	}
	s.send(map[string]any{"id": "n", "type": "probeVideo", "pageUrl": page})
	if m := s.recv(); m["type"] != "videoInfo" {
		t.Fatalf("probeVideo without a sign-in: %v", m)
	}
	be.mu.Lock()
	probes, adds := be.probes, be.videoAdds
	be.mu.Unlock()
	if len(probes) != 2 || len(adds) != 1 {
		t.Fatalf("%d probes, %d adds", len(probes), len(adds))
	}
	cookieOf := func(hs []*downexv1.Header) string {
		for _, h := range hs {
			if h.GetName() == "Cookie" {
				return h.GetValue()
			}
		}
		return ""
	}
	if probes[0].GetCookieDomain() != "instagram.com" || cookieOf(probes[0].GetHeaders()) != "sessionid=s1" ||
		adds[0].GetCookieDomain() != "instagram.com" || cookieOf(adds[0].GetHeaders()) != "sessionid=s1" {
		t.Errorf("forwarded: probe %v, add %v", probes[0], adds[0])
	}
	if probes[1].GetCookieDomain() != "" || cookieOf(probes[1].GetHeaders()) != "" {
		t.Errorf("a probe without a sign-in got one: %v", probes[1])
	}

	for name, domain := range map[string]any{
		"upper case":  "Instagram.com",
		"leading dot": ".instagram.com",
		"ip":          "127.0.0.1",
		"too long":    strings.Repeat("a.", 127) + "com",
		"space":       "insta gram.com",
		"wrong type":  7,
	} {
		s.send(map[string]any{"id": name, "type": "addVideo", "pageUrl": page, "headers": cookie, "cookieDomain": domain})
		if m := s.recv(); errorCode(m) != codeInvalidRequest {
			t.Errorf("%s: %v", name, m)
		} else if msg, _ := m["error"].(map[string]any)["message"].(string); strings.Contains(msg, "sessionid") {
			t.Errorf("%s: the error repeats the cookie: %v", name, m)
		}
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.videoAdds) != 1 {
		t.Errorf("refused requests reached the daemon: %d adds", len(be.videoAdds))
	}
}
