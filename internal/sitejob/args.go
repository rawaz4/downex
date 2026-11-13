package sitejob

// argsSpec is what one yt-dlp run is given.
type argsSpec struct {
	// Deno is the absolute path of the Deno executable ("" = none: yt-dlp runs
	// without a JavaScript runtime).
	Deno string
	// CacheDir is yt-dlp's cache folder ("" = no cache).
	CacheDir string
	// Proxy is the route of the page; one with credentials goes through the
	// environment (proxyEnv), never through the arguments.
	Proxy Proxy
	// Cookies is the cookie file ("" = none); Referer the page that embeds the video
	// ("" = none).
	Cookies, Referer string
	// PageURL is the validated page address.
	PageURL string
	// Manifests keeps YouTube's HLS and DASH manifests, which are skipped otherwise:
	// fetching them costs about a second, and direct formats do not need them.
	Manifests bool
}

// Values of yt-dlp's --extractor-args for YouTube (docs/SITE-MEDIA-SPEC.md §4.7):
// translated subtitles are never listed, and the manifests only when a run keeps them.
const (
	skipManifestsArg = "youtube:skip=translated_subs,hls,dash"
	keepManifestsArg = "youtube:skip=translated_subs"
)

// buildArgs returns yt-dlp's arguments for one extraction (docs/SITE-MEDIA-SPEC.md
// §4.7), exactly in this order: no configuration, plugins or update checks, plain
// UTF-8 output, the operating system's certificate roots, Deno as the only JavaScript
// runtime, the cache folder, the proxy ("" = direct, which also keeps yt-dlp from the
// system's proxy settings), the cookie file and Referer when given, one video without
// a playlist, YouTube without translated subtitles (and, unless a.Manifests, without
// its HLS and DASH manifests), and the JSON of the page whose URL follows "--".
func buildArgs(a argsSpec) []string {
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-update", "--color", "no_color", "--encoding", "utf-8",
		"--compat-options", "no-certifi", "--no-js-runtimes"}
	if a.Deno != "" {
		args = append(args, "--js-runtimes", "deno:"+a.Deno)
	}
	if a.CacheDir != "" {
		args = append(args, "--cache-dir", a.CacheDir)
	} else {
		args = append(args, "--no-cache-dir")
	}
	args = append(args, "--socket-timeout", "30")
	switch {
	case a.Proxy.URL == "":
		args = append(args, "--proxy", "")
	case !a.Proxy.hasLogin():
		args = append(args, "--proxy", a.Proxy.URL)
	}
	if a.Cookies != "" {
		args = append(args, "--cookies", a.Cookies)
	}
	if a.Referer != "" {
		args = append(args, "--referer", a.Referer)
	}
	extractor := skipManifestsArg
	if a.Manifests {
		extractor = keepManifestsArg
	}
	return append(args, "--no-playlist", "--flat-playlist", "--ignore-no-formats-error",
		"--extractor-args", extractor, "-J", "--", a.PageURL)
}
