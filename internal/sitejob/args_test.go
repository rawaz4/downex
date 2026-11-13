package sitejob

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"downex/internal/tools"
)

// The arguments are exactly those of docs/SITE-MEDIA-SPEC.md §4.7, in order, for a
// direct run, an HTTP proxy, a proxy with credentials and a run with cookies: YouTube's
// HLS and DASH manifests are skipped unless the run asks for them.
func TestBuildArgs(t *testing.T) {
	const (
		page  = "https://www.youtube.com/watch?v=aaaaaaaaaaa"
		deno  = "/tools/deno/2.5.0/deno"
		cache = "/tools/cache/yt-dlp"
	)
	head := []string{"--ignore-config", "--no-plugin-dirs", "--no-update", "--color", "no_color", "--encoding", "utf-8",
		"--compat-options", "no-certifi", "--no-js-runtimes", "--js-runtimes", "deno:" + deno,
		"--cache-dir", cache, "--socket-timeout", "30"}
	tail := []string{"--no-playlist", "--flat-playlist", "--ignore-no-formats-error",
		"--extractor-args", "youtube:skip=translated_subs,hls,dash", "-J", "--", page}
	withManifests := []string{"--no-playlist", "--flat-playlist", "--ignore-no-formats-error",
		"--extractor-args", "youtube:skip=translated_subs", "-J", "--", page}
	join := func(parts ...[]string) []string { return slices.Concat(parts...) }
	for _, c := range []struct {
		name string
		spec argsSpec
		want []string
	}{
		{"direct", argsSpec{Deno: deno, CacheDir: cache, PageURL: page},
			join(head, []string{"--proxy", ""}, tail)},
		{"http proxy", argsSpec{Deno: deno, CacheDir: cache, Proxy: Proxy{URL: "http://proxy.lan:3128"}, PageURL: page},
			join(head, []string{"--proxy", "http://proxy.lan:3128"}, tail)},
		{"socks proxy", argsSpec{Deno: deno, CacheDir: cache, Proxy: Proxy{URL: "socks5://127.0.0.1:1080"}, PageURL: page},
			join(head, []string{"--proxy", "socks5://127.0.0.1:1080"}, tail)},
		{"socks proxy resolving names", argsSpec{Deno: deno, CacheDir: cache, Proxy: Proxy{URL: "socks5h://127.0.0.1:9050"}, PageURL: page},
			join(head, []string{"--proxy", "socks5h://127.0.0.1:9050"}, tail)},
		{"proxy with credentials", argsSpec{Deno: deno, CacheDir: cache,
			Proxy: Proxy{URL: "http://proxy.lan:3128", Username: "me", Password: "s3cret"}, PageURL: page},
			join(head, tail)},
		{"cookies and referer", argsSpec{Deno: deno, CacheDir: cache, Cookies: "/run/1/cookies.txt",
			Referer: "https://embedder.example/post", PageURL: page},
			join(head, []string{"--proxy", "", "--cookies", "/run/1/cookies.txt", "--referer", "https://embedder.example/post"}, tail)},
		{"without deno or cache", argsSpec{PageURL: page},
			join([]string{"--ignore-config", "--no-plugin-dirs", "--no-update", "--color", "no_color", "--encoding", "utf-8",
				"--compat-options", "no-certifi", "--no-js-runtimes", "--no-cache-dir", "--socket-timeout", "30", "--proxy", ""}, tail)},
		{"with the manifests", argsSpec{Deno: deno, CacheDir: cache, PageURL: page, Manifests: true},
			join(head, []string{"--proxy", ""}, withManifests)},
	} {
		got := buildArgs(c.spec)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if strings.Contains(strings.Join(got, " "), "s3cret") {
			t.Errorf("%s: the proxy password is in the arguments", c.name)
		}
	}
}

// A proxy with credentials reaches yt-dlp only through the environment, with them in
// its URL; one without credentials, and a direct route, set no proxy variables.
func TestProxyEnv(t *testing.T) {
	got := proxyEnv(Proxy{URL: "socks5://proxy.lan:1080", Username: "me@corp", Password: "p:w/d"})
	want := "socks5://me%40corp:p%3Aw%2Fd@proxy.lan:1080"
	if !reflect.DeepEqual(got, []string{"HTTP_PROXY=" + want, "HTTPS_PROXY=" + want, "ALL_PROXY=" + want}) {
		t.Errorf("proxyEnv = %q", got)
	}
	for _, p := range []Proxy{{}, {URL: "http://proxy.lan:3128"}} {
		if got := proxyEnv(p); got != nil {
			t.Errorf("proxyEnv(%+v) = %q", p, got)
		}
	}
	l := &tools.Lease{Deno: filepath.Join("tools", "deno", "2.5.0", "deno"), DenoCache: "dc"}
	s := envSpec(l, "run1", Proxy{URL: "http://proxy.lan:3128", Password: "x"})
	if s.DenoDir != filepath.Join("tools", "deno", "2.5.0") || s.TempDir != filepath.Join("run1", "tmp") ||
		s.DenoCache != "dc" || len(s.Set) != 3 {
		t.Errorf("envSpec = %+v", s)
	}
	if s := envSpec(&tools.Lease{}, "run1", Proxy{}); s.DenoDir != "" || s.Set != nil {
		t.Errorf("envSpec without Deno = %+v", s)
	}
}
