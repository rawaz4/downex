package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Settings → About: what this window is built from (Startup's build), diagnostics to
// copy into a report without private data, and the open-source notices.

// buildInfo is this window's build (Startup's "build"). The commit fields are present
// only when the build was stamped from a version-controlled tree.
type buildInfo struct {
	Version      string `json:"version"` // main.version ("dev" for a development build)
	OS           string `json:"os"`      // runtime.GOOS
	Arch         string `json:"arch"`    // runtime.GOARCH
	GoVersion    string `json:"goVersion"`
	WailsVersion string `json:"wailsVersion,omitempty"`
	Commit       string `json:"commit,omitempty"`     // vcs.revision
	CommitTime   string `json:"commitTime,omitempty"` // vcs.time, RFC 3339
	Modified     bool   `json:"modified,omitempty"`   // vcs.modified: built with uncommitted changes
}

// wailsModule is the Wails module whose version the About page shows.
const wailsModule = "github.com/wailsapp/wails/v2"

// currentBuild is this process's build.
func currentBuild() buildInfo { return buildInfoFrom(debug.ReadBuildInfo()) }

// buildInfoFrom reads bi (ok false: no build information, the Go version is then the
// runtime's).
func buildInfoFrom(bi *debug.BuildInfo, ok bool) buildInfo {
	b := buildInfo{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version()}
	if !ok || bi == nil {
		return b
	}
	if bi.GoVersion != "" {
		b.GoVersion = bi.GoVersion
	}
	for _, m := range bi.Deps {
		if m != nil && m.Path == wailsModule {
			if m.Replace != nil && m.Replace.Version != "" {
				b.WailsVersion = m.Replace.Version
			} else {
				b.WailsVersion = m.Version
			}
		}
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.time":
			b.CommitTime = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	return b
}

// ---- diagnostics --------------------------------------------------------------------

// diagnosticsRequest is the argument of Diagnostics: the WebView2 (browser engine)
// version, which only the page can read.
type diagnosticsRequest struct {
	Webview string `json:"webview"`
}

var browserVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// textResult is the result of Diagnostics and ThirdPartyNotices.
type textResult struct {
	Text string `json:"text"`
}

// Diagnostics returns {text}: a plain-text summary to paste into a problem report
// (Settings → About, "Copy diagnostics"): the window's build and system, the WebView2
// version the page names ({webview?}: digits and dots only), downexd's version, state and
// folders, whether updates can be installed, yt-dlp and Deno (state, version, how they
// were checked, error codes), the settings that change behaviour, and how many downloads
// are in each state. It never contains the IPC endpoint, an address, a file name, a
// cookie, a site login, a proxy or its credentials, the update address or the
// antivirus command; folders inside the user's profile are written as %USERPROFILE%,
// %APPDATA% and %LOCALAPPDATA% (elsewhere "~"). What downexd cannot answer is said so.
func (a *App) Diagnostics(req json.RawMessage) (json.RawMessage, error) {
	var in diagnosticsRequest
	if !isEmptyJSON(req) {
		if err := decodeStrict(req, &in); err != nil {
			return nil, apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed request: %w", err)))
		}
	}
	if in.Webview != "" && (len(in.Webview) > 32 || !browserVersion.MatchString(in.Webview)) {
		return nil, apiError(connect.NewError(connect.CodeInvalidArgument, errors.New("webview must be a version such as 141.0.3537.71")))
	}
	ctx, cancel := a.callContext()
	defer cancel()
	b, err := json.Marshal(textResult{Text: a.diagnostics(ctx, in.Webview)})
	if err != nil {
		return nil, apiError(connect.NewError(connect.CodeInternal, err))
	}
	return b, nil
}

func (a *App) diagnostics(ctx context.Context, webview string) string {
	var w strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&w, format+"\n", args...) }
	path := func(p string) string {
		if p == "" {
			return "not known"
		}
		return redactProfile(p, runtime.GOOS, a.env)
	}
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	on := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}

	b := currentBuild()
	line("Downex window %s (%s/%s, Go %s)", b.Version, b.OS, b.Arch, strings.TrimPrefix(b.GoVersion, "go"))
	if b.WailsVersion != "" {
		line("Wails %s", b.WailsVersion)
	}
	if b.Commit != "" {
		modified := ""
		if b.Modified {
			modified = ", modified"
		}
		line("Commit %s (%s%s)", b.Commit, b.CommitTime, modified)
	}
	if webview != "" {
		line("WebView2 %s", webview)
	}
	if a.view.progress > 0 {
		line("View: progress window")
	}

	st, err := a.daemon.Status(ctx)
	if err != nil {
		line("downexd: not reachable (%s)", connect.CodeOf(err))
	} else {
		line("downexd %s, running since %s, %d active, %d queued", st.GetVersion(),
			st.GetStartedAt().AsTime().UTC().Format("2006-01-02 15:04 MST"), st.GetActive(), st.GetQueued())
		line("Updates supported: %s", yes(st.GetUpdatesSupported()))
		line("Data folder: %s", path(st.GetDataDir()))
		line("Log folder: %s", path(st.GetLogDir()))
		line("Tools folder: %s", path(st.GetToolsDir()))
	}

	if s, err := a.daemon.GetSettings(ctx); err == nil && s.GetSettings() != nil {
		set := s.GetSettings()
		line("Settings: %d at once, %d connections, speed limit %s, category folders %s",
			set.GetMaxConcurrentDownloads(), set.GetDefaultMaxConnections(), limitText(set.GetGlobalSpeedLimit()),
			on(set.GetUseCategoryFolders()))
		line("Browser downloads confirmed: %s; progress window %s; complete dialog %s; clipboard %s",
			on(set.GetConfirmBrowserDownloads()), on(set.GetShowProgressWindow()), on(set.GetShowCompleteDialog()),
			on(set.GetClipboardMonitor()))
		line("Launch at sign-in: %s; tray icon %s; mark downloaded files %s", on(set.GetLaunchAtLogin()),
			on(set.GetShowTrayIcon()), on(set.GetMarkDownloadedFiles()))
		line("Antivirus scan: %s", on(set.GetAvScanEnabled()))
		feed := "not set"
		if set.GetUpdateFeedUrl() != "" {
			feed = "set"
		}
		line("Update address: %s; automatic checks %s", feed, on(set.GetAutoCheckUpdates()))
		line("Proxy: %s", strings.ToLower(strings.TrimPrefix(set.GetProxy().GetMode().String(), "PROXY_MODE_")))
		line("Host rules: %d; Site logins: %d", len(set.GetHostRules()), len(set.GetSiteLogins()))
		line("Video sites: %s; tools updated automatically %s; channel %s", on(set.GetSiteMedia()),
			on(set.GetToolsAutoUpdate()), cmp.Or(set.GetToolsChannel(), "nightly"))
	}

	if t, err := a.daemon.GetTools(ctx); err == nil && t.GetStatus() != nil {
		for _, tool := range t.GetStatus().GetTools() {
			state := strings.ToLower(strings.TrimPrefix(tool.GetState().String(), "TOOL_STATE_"))
			text := tool.GetName() + ": " + state
			if tool.GetVersion() != "" {
				text += " " + tool.GetVersion()
			}
			var how []string
			if tool.GetVersion() != "" {
				if tool.GetManaged() {
					how = append(how, "managed")
				} else {
					how = append(how, "found on PATH")
				}
			}
			if tool.GetVerified() != "" {
				how = append(how, tool.GetVerified())
			}
			if tool.GetErrorCode() != "" {
				how = append(how, tool.GetErrorCode())
			}
			if len(how) > 0 {
				text += " (" + strings.Join(how, ", ") + ")"
			}
			line("%s", text)
		}
	}

	if l, err := a.daemon.List(ctx); err == nil {
		counts := map[string]int{}
		media := 0
		for _, d := range l.GetDownloads() {
			counts[stateName(d.GetState())]++
			if d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA {
				media++
			}
		}
		var parts []string
		for state, n := range counts {
			parts = append(parts, fmt.Sprintf("%s: %d", state, n))
		}
		slices.Sort(parts)
		line("Downloads: %d (%d videos or streams); %s", len(l.GetDownloads()), media, strings.Join(parts, ", "))
	}
	return w.String()
}

// limitText is a speed limit for the diagnostics.
func limitText(bps int64) string {
	if bps <= 0 {
		return "none"
	}
	return fmt.Sprintf("%d B/s", bps)
}

// redactProfile writes a path inside the user's profile with the variable that names its
// folder: on Windows %LOCALAPPDATA%, %APPDATA% or %USERPROFILE% (the longest that
// matches, compared without case), elsewhere "~" for $HOME. Only whole folder names
// match ("C:\Users\Al" is not a prefix of "C:\Users\Alice").
func redactProfile(p, goos string, env func(string) string) string {
	type prefix struct{ dir, name string }
	var prefixes []prefix
	sep := "/"
	if goos == "windows" {
		sep = `\`
		for _, v := range []string{"LOCALAPPDATA", "APPDATA", "USERPROFILE"} {
			if d := env(v); d != "" {
				prefixes = append(prefixes, prefix{strings.TrimRight(d, `\/`), "%" + v + "%"})
			}
		}
	} else if d := env("HOME"); d != "" {
		prefixes = append(prefixes, prefix{strings.TrimRight(d, "/"), "~"})
	}
	slices.SortFunc(prefixes, func(a, b prefix) int { return len(b.dir) - len(a.dir) })
	for _, pre := range prefixes {
		if pre.dir == "" || len(p) < len(pre.dir) {
			continue
		}
		head, rest := p[:len(pre.dir)], p[len(pre.dir):]
		same := head == pre.dir
		if goos == "windows" {
			same = strings.EqualFold(head, pre.dir)
		}
		if same && (rest == "" || strings.HasPrefix(rest, sep) || goos == "windows" && strings.HasPrefix(rest, "/")) {
			return pre.name + rest
		}
	}
	return p
}

// ---- open-source notices ------------------------------------------------------------

// thirdPartyNotices is THIRD-PARTY-NOTICES.txt, generated by scripts/third-party-notices.mjs (the
// licences of the Go modules linked into Downex's programs and of the frontend's
// bundled npm packages, and the programs Downex downloads separately).
//
//go:embed THIRD-PARTY-NOTICES.txt
var thirdPartyNotices string

// ThirdPartyNotices returns {text}: the open-source notices of Downex's programs
// (Settings → About).
func (a *App) ThirdPartyNotices() (json.RawMessage, error) {
	b, err := json.Marshal(textResult{Text: thirdPartyNotices})
	if err != nil {
		return nil, apiError(connect.NewError(connect.CodeInternal, err))
	}
	return b, nil
}
