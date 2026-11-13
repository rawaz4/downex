package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestYtDlpVersionOrder(t *testing.T) {
	order := []string{"2025.12.31", "2026.01.02", "2026.08.19", "2026.08.19.1", "2026.08.19.2", "2026.10.10.000001", "2026.10.10.233141", "2026.10.11"}
	for i := range order {
		for j := range order {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := compareYtDlp(order[i], order[j]); got != want {
				t.Errorf("compareYtDlp(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, bad := range []string{"", "2026.8.19", "2026.13.01", "2026.02.30", "26.08.19", "2026.08.19.", "2026.08.19.1234567890", "v2026.08.19", "2026.08.19 ", "2026-08-19"} {
		if validVersion(ToolYtDlp, bad) {
			t.Errorf("yt-dlp version %q accepted", bad)
		}
	}
	if v, ok := parseYtDlpVersion("2026.10.10.233141"); !ok || v.build != 233141 || v.date.Format("2006-01-02") != "2026-10-10" {
		t.Errorf("parse nightly = %+v, %v", v, ok)
	}
}

func TestDenoVersionOrder(t *testing.T) {
	order := []string{"1.46.3", "2.2.9", "2.3.0", "2.3.10", "2.10.0", "10.0.0"}
	for i := range order {
		for j := range order {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := compareDeno(order[i], order[j]); got != want {
				t.Errorf("compareDeno(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, bad := range []string{"", "2.3", "v2.3.0", "2.3.0-rc.1", "2.3.0.1", "a.b.c", "1234567.0.0"} {
		if validVersion(ToolDeno, bad) {
			t.Errorf("Deno version %q accepted", bad)
		}
	}
}

func TestVersionOfTag(t *testing.T) {
	cases := []struct {
		tool, tag, want string
		ok              bool
	}{
		{ToolYtDlp, "2026.08.19", "2026.08.19", true},
		{ToolYtDlp, "2026.10.10.233141", "2026.10.10.233141", true},
		{ToolYtDlp, "2026.10.10.2331", "", false}, // tags carry six digits of time
		{ToolYtDlp, "2026.13.10", "", false},
		{ToolYtDlp, "v2026.08.19", "", false},
		{ToolYtDlp, "../2026.08.19", "", false},
		{ToolDeno, "v2.5.4", "2.5.4", true},
		{ToolDeno, "2.5.4", "", false},
		{ToolDeno, "v2.5.4-rc.1", "", false},
		{ToolDeno, "v2.5", "", false},
	}
	for _, c := range cases {
		got, ok := versionOfTag(c.tool, c.tag)
		if got != c.want || ok != c.ok {
			t.Errorf("versionOfTag(%s, %q) = %q, %v, want %q, %v", c.tool, c.tag, got, ok, c.want, c.ok)
		}
	}
}

func TestLatestTag(t *testing.T) {
	var location string
	status := http.StatusFound
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/yt-dlp/yt-dlp/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	f := &fetcher{client: secureClient(srv.Client()), headerTimeout: 5 * time.Second, stallWindow: 5 * time.Second}
	ctx := context.Background()

	location = srv.URL + "/yt-dlp/yt-dlp/releases/tag/2026.08.19"
	if tag, err := f.latestTag(ctx, srv.URL, repoYtDlpStable); err != nil || tag != "2026.08.19" {
		t.Errorf("absolute redirect: %q, %v", tag, err)
	}
	location = "/YT-DLP/yt-dlp/releases/tag/2026.10.10.233141" // relative, other case
	if tag, err := f.latestTag(ctx, srv.URL, repoYtDlpStable); err != nil || tag != "2026.10.10.233141" {
		t.Errorf("relative redirect: %q, %v", tag, err)
	}
	for name, c := range map[string]struct {
		location string
		status   int
		code     string
	}{
		"other host":      {"https://evil.example/yt-dlp/yt-dlp/releases/tag/2026.08.19", http.StatusFound, CodeHTTPStatus},
		"plain http":      {"http://" + srv.Listener.Addr().String() + "/yt-dlp/yt-dlp/releases/tag/2026.08.19", http.StatusFound, CodeNetwork},
		"other repo":      {"/other/repo/releases/tag/2026.08.19", http.StatusFound, CodeHTTPStatus},
		"tag with slash":  {"/yt-dlp/yt-dlp/releases/tag/2026.08.19/x", http.StatusFound, CodeHTTPStatus},
		"no redirect":     {"", http.StatusOK, CodeHTTPStatus},
		"not found":       {"", http.StatusNotFound, CodeHTTPStatus},
		"empty tag":       {"/yt-dlp/yt-dlp/releases/tag/", http.StatusFound, CodeHTTPStatus},
		"encoded slashes": {"/yt-dlp/yt-dlp/releases/tag/..%2F..%2Fx", http.StatusFound, CodeHTTPStatus},
	} {
		location, status = c.location, c.status
		tag, err := f.latestTag(ctx, srv.URL, repoYtDlpStable)
		if err == nil || ErrorCode(err) != c.code {
			t.Errorf("%s: %q, %v (code %q), want code %s", name, tag, err, ErrorCode(err), c.code)
		}
	}
}

func TestReleaseURL(t *testing.T) {
	got := releaseURL("https://github.com", repoYtDlpNightly, "2026.10.10.233141", "SHA2-256SUMS.sig")
	if want := "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/download/2026.10.10.233141/SHA2-256SUMS.sig"; got != want {
		t.Errorf("releaseURL = %s, want %s", got, want)
	}
}
