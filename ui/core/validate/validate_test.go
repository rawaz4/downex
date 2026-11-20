package validate

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
)

// The cases of ui/frontend/src/lib/validate.test.ts.

const (
	MiB = 1024 * 1024
	MB  = 1000 * 1000
)

// defaultSettings is api.ts DEFAULT_SETTINGS.
func defaultSettings() *downexv1.Settings {
	return &downexv1.Settings{
		MaxConcurrentDownloads: 3,
		DefaultMaxConnections:  32,
		Categories: []*downexv1.Category{
			{Name: "Video", Extensions: []string{"mp4", "mkv", "avi", "mov", "webm", "wmv", "flv", "m4v", "mpg", "mpeg", "ts", "3gp"}},
			{Name: "Audio", Extensions: []string{"mp3", "flac", "m4a", "aac", "wav", "ogg", "opus", "wma"}},
			{Name: "Archives", Extensions: []string{"zip", "rar", "7z", "gz", "xz", "bz2", "tar", "tgz", "zst", "iso", "img", "dmg"}},
			{Name: "Documents", Extensions: []string{"pdf", "epub", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "txt", "csv", "md"}},
			{Name: "Programs", Extensions: []string{"exe", "msi", "msix", "appx", "apk", "deb", "rpm", "appimage", "bat", "cmd", "ps1", "jar"}},
		},
		UseCategoryFolders:      true,
		ConfirmBrowserDownloads: true,
		NotifyOnComplete:        true,
		NotifyOnFailure:         true,
		ShowTrayIcon:            true,
		ShowProgressWindow:      true,
		ShowCompleteDialog:      true,
		Proxy:                   &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_SYSTEM},
		MarkDownloadedFiles:     true,
		CheckDownloadedFiles:    true,
		AvScanArgs:              "[File]",
		AutoCheckUpdates:        true,
		SiteMedia:               true,
		ToolsAutoUpdate:         true,
		ToolsChannel:            "nightly",
	}
}

func addForm(edit ...func(*AddForm)) AddForm {
	f := AddForm{URLs: "https://downloads.example.com/a.zip", Connections: "8", SpeedUnit: format.MBps}
	for _, e := range edit {
		e(&f)
	}
	return f
}

func TestCheckURL(t *testing.T) {
	for _, good := range []string{"https://example.com/a", "http://127.0.0.1:8080/x?y=1"} {
		if reason, ok := CheckURL(good); !ok {
			t.Errorf("CheckURL(%q) = %q", good, reason)
		}
	}
	if reason, ok := CheckURL("ftp://example.com/a"); ok || !strings.Contains(reason, "http") {
		t.Errorf("ftp = %q, %v", reason, ok)
	}
	if _, ok := CheckURL("not a url"); ok {
		t.Error("not a url passed")
	}
	if reason, _ := CheckURL("https://example.com/" + strings.Repeat("a", 33*1024)); reason != "is longer than 32,768 bytes" {
		t.Errorf("long = %q", reason)
	}
	if reason, _ := CheckURL("not a url"); reason != "is not a valid URL" {
		t.Errorf("garbage = %q", reason)
	}
	if reason, _ := CheckURL("mailto:me@example.com"); reason != "must start with http:// or https://" {
		t.Errorf("mailto = %q", reason)
	}
}

func TestSplitURLs(t *testing.T) {
	got := SplitURLs(" https://a.example/1\n\nhttps://a.example/2  https://a.example/1\r\n")
	if !slices.Equal(got, []string{"https://a.example/1", "https://a.example/2"}) {
		t.Errorf("SplitURLs = %q", got)
	}
}

func TestPathsAndNames(t *testing.T) {
	for _, ok := range []string{"", `C:\Users\you\Downloads`, "d:/data", "/home/you"} {
		if e := CheckDir(ok); e != "" {
			t.Errorf("CheckDir(%q) = %q", ok, e)
		}
	}
	if e := CheckDir("relative/dir"); !strings.Contains(e, "absolute") {
		t.Errorf("relative = %q", e)
	}
	for _, unc := range []string{`\\server\share`, "//server/share"} {
		if e := CheckDir(unc); !strings.Contains(e, "UNC") {
			t.Errorf("CheckDir(%q) = %q", unc, e)
		}
	}
	if e := CheckFilename("ok name.zip"); e != "" {
		t.Errorf("ok name = %q", e)
	}
	for _, bad := range []string{"a/b.zip", `a\b.zip`, "..", "bad\x07.zip", strings.Repeat("x", 256)} {
		if CheckFilename(bad) == "" {
			t.Errorf("CheckFilename(%q) passed", bad)
		}
	}
	if e := CheckFilename(strings.Repeat("x", 256)); e != "Keep the file name under 256 characters" {
		t.Errorf("long name = %q", e)
	}
}

func TestValidateAddForm(t *testing.T) {
	t.Run("one request per URL with the chosen options", func(t *testing.T) {
		reqs, errs, ok := ValidateAddForm(addForm(func(f *AddForm) {
			f.URLs, f.Dir, f.Filename = "https://a.example/1.iso\nhttps://a.example/2.iso", " /data/isos ", "ignored.iso"
			f.Connections, f.SpeedValue, f.StartPaused = "16", "1.5", true
		}))
		if !ok {
			t.Fatalf("errors %+v", errs)
		}
		want := []*downexv1.AddRequest{
			{Url: "https://a.example/1.iso", Dir: "/data/isos", MaxConnections: 16, SpeedLimit: 1.5 * MB, StartPaused: true, Source: downexv1.AddSource_ADD_SOURCE_UI},
			{Url: "https://a.example/2.iso", Dir: "/data/isos", MaxConnections: 16, SpeedLimit: 1.5 * MB, StartPaused: true, Source: downexv1.AddSource_ADD_SOURCE_UI},
		}
		if len(reqs) != 2 || !proto.Equal(reqs[0], want[0]) || !proto.Equal(reqs[1], want[1]) {
			t.Errorf("requests = %v", reqs)
		}
	})
	t.Run("keeps the file name for a single URL", func(t *testing.T) {
		reqs, _, ok := ValidateAddForm(addForm(func(f *AddForm) { f.Filename = " renamed.zip " }))
		if !ok || reqs[0].GetFilename() != "renamed.zip" {
			t.Errorf("requests = %v", reqs)
		}
	})
	t.Run("reports every invalid field", func(t *testing.T) {
		_, errs, ok := ValidateAddForm(addForm(func(f *AddForm) {
			f.URLs, f.Dir, f.Connections, f.SpeedValue = "ftp://x.example/a https://ok.example/b", "rel", "33", "fast"
		}))
		if ok || !strings.Contains(errs.URLs, "ftp://x.example/a") || errs.Dir == "" || errs.Speed == "" || errs.Filename != "" {
			t.Errorf("errors = %+v", errs)
		}
		if errs.Connections != "Connections must be from 1 to 32" {
			t.Errorf("connections = %q", errs.Connections)
		}
		if errs.URLs != `"ftp://x.example/a" must start with http:// or https://` {
			t.Errorf("urls = %q", errs.URLs)
		}
	})
	t.Run("requires at least one URL and a whole number of connections", func(t *testing.T) {
		_, errs, ok := ValidateAddForm(addForm(func(f *AddForm) { f.URLs, f.Connections = "  \n ", "2.5" }))
		if ok || errs.URLs != "Enter at least one URL" || errs.Connections != "Connections must be a whole number from 1 to 32" {
			t.Errorf("errors = %+v", errs)
		}
	})
	t.Run("summarises long lists of bad URLs", func(t *testing.T) {
		_, errs, _ := ValidateAddForm(addForm(func(f *AddForm) { f.URLs = "a b c d e" }))
		if !strings.HasSuffix(errs.URLs, "(+2 more)") || errs.URLs != `"a" is not a valid URL; "b" is not a valid URL; "c" is not a valid URL (+2 more)` {
			t.Errorf("urls = %q", errs.URLs)
		}
	})
	t.Run("truncates a long bad address to 60 characters", func(t *testing.T) {
		long := "ftp://" + strings.Repeat("x", 80)
		_, errs, _ := ValidateAddForm(addForm(func(f *AddForm) { f.URLs = long }))
		if want := `"` + long[:59] + ellipsis + `" must start with http:// or https://`; errs.URLs != want {
			t.Errorf("urls = %q", errs.URLs)
		}
	})
	t.Run("puts the chosen queue into Add requests", func(t *testing.T) {
		reqs, _, _ := ValidateAddForm(addForm(func(f *AddForm) { f.QueueID = 3 }))
		if reqs[0].GetQueueId() != 3 {
			t.Errorf("queue = %d", reqs[0].GetQueueId())
		}
		reqs, _, _ = ValidateAddForm(addForm())
		if reqs[0].GetQueueId() != 0 {
			t.Errorf("no queue = %d", reqs[0].GetQueueId())
		}
	})
}

func generalForm(base *downexv1.Settings) SettingsForm {
	return SettingsForm{
		MaxConcurrent:           "3",
		SpeedUnit:               format.MBps,
		DefaultDir:              "/home/you/Downloads",
		DefaultConnections:      "32",
		Categories:              CategoryDrafts(base.GetCategories(), 1),
		UseCategoryFolders:      true,
		ConfirmBrowserDownloads: true,
		NotifyOnComplete:        true,
		NotifyOnFailure:         true,
		LaunchAtLogin:           base.GetLaunchAtLogin(),
		ShowTrayIcon:            true,
		ShowProgressWindow:      true,
		ShowCompleteDialog:      true,
	}
}

func mustSettings(t *testing.T, form SettingsForm, base *downexv1.Settings) *downexv1.Settings {
	t.Helper()
	s, errs, ok := ValidateSettingsForm(form, base)
	if !ok {
		t.Fatalf("errors %+v", errs)
	}
	return s
}

func TestValidateSettingsForm(t *testing.T) {
	base := defaultSettings()
	base.DefaultDir = "/home/you/Downloads"
	base.Categories = []*downexv1.Category{
		{Name: "Video", Extensions: []string{"mp4", "mkv"}},
		{Name: "Books", Extensions: []string{"epub"}, Dir: "/data/books"},
	}
	base.LaunchAtLogin = true
	ok := generalForm(base)

	t.Run("writes back every field, unchanged ones included", func(t *testing.T) {
		if s := mustSettings(t, ok, base); !proto.Equal(s, base) {
			t.Errorf("unchanged = %v", s)
		}
		f := ok
		f.SpeedValue, f.NotifyOnComplete, f.ShowTrayIcon = "2", false, false
		want := proto.CloneOf(base)
		want.GlobalSpeedLimit, want.NotifyOnComplete, want.ShowTrayIcon = 2*MB, false, false
		if s := mustSettings(t, f, base); !proto.Equal(s, want) {
			t.Errorf("changed = %v", s)
		}
		p := ok
		p.ShowProgressWindow, p.ShowCompleteDialog = false, false
		want = proto.CloneOf(base)
		want.ShowProgressWindow, want.ShowCompleteDialog = false, false
		if s := mustSettings(t, p, base); !proto.Equal(s, want) {
			t.Errorf("progress = %v", s)
		}
	})
	t.Run("keeps fields of the loaded settings that the form does not edit", func(t *testing.T) {
		newer := proto.CloneOf(base)
		// a field this build does not know (number 900, a string)
		newer.ProtoReflect().SetUnknown(protowire.AppendString(protowire.AppendTag(nil, 900, protowire.BytesType), "kept"))
		s := mustSettings(t, ok, newer)
		if !proto.Equal(s, newer) || len(s.ProtoReflect().GetUnknown()) == 0 {
			t.Errorf("unknown field lost: %v", s)
		}
	})
	t.Run("edits categories: rename, extensions, folder, add and remove", func(t *testing.T) {
		f := ok
		video := ok.Categories[0]
		video.Name, video.Extensions = " Movies ", ".MP4, mkv  webm mkv"
		f.Categories = []CategoryDraft{video, {Key: 99, Name: "Comics", Extensions: "cbz cbr", Dir: `C:\Comics`}}
		s := mustSettings(t, f, base)
		want := []*downexv1.Category{
			{Name: "Movies", Extensions: []string{"mp4", "mkv", "webm"}},
			{Name: "Comics", Extensions: []string{"cbz", "cbr"}, Dir: `C:\Comics`},
		}
		if len(s.Categories) != 2 || !proto.Equal(s.Categories[0], want[0]) || !proto.Equal(s.Categories[1], want[1]) {
			t.Errorf("categories = %v", s.Categories)
		}
	})
	t.Run("checks categories like the daemon", func(t *testing.T) {
		f := ok
		f.Categories = []CategoryDraft{
			{Key: 1, Name: "Video", Extensions: "mp4"},
			{Key: 2, Name: "video", Extensions: "mp4.x", Dir: "relative"},
			{Key: 3, Name: "  ", Extensions: strings.Repeat("x", 17)},
			{Key: 4, Name: strings.Repeat("n", 65)},
		}
		_, errs, valid := ValidateSettingsForm(f, base)
		if valid {
			t.Fatal("passed")
		}
		if _, has := errs.CategoryRows[1]; has {
			t.Errorf("row 1 = %+v", errs.CategoryRows[1])
		}
		if r := errs.CategoryRows[2]; r.Name != "Another category has this name" || r.Extensions != `Not an extension: "mp4.x" (letters and digits, up to 16)` || r.Dir != "Use an absolute folder path" {
			t.Errorf("row 2 = %+v", r)
		}
		if r := errs.CategoryRows[3]; r.Name != "Enter a name" || r.Extensions == "" {
			t.Errorf("row 3 = %+v", r)
		}
		if r := errs.CategoryRows[4]; r.Name != "Keep the name under 65 characters" {
			t.Errorf("row 4 = %+v", r)
		}
		many := ok
		many.Categories = nil
		for i := range 33 {
			many.Categories = append(many.Categories, CategoryDraft{Key: i, Name: fmt.Sprintf("C%d", i)})
		}
		if _, errs, _ := ValidateSettingsForm(many, base); errs.Categories != "Keep at most 32 categories" {
			t.Errorf("too many = %q", errs.Categories)
		}
		var exts []string
		for i := range 201 {
			exts = append(exts, fmt.Sprintf("e%d", i))
		}
		long := ok
		long.Categories = []CategoryDraft{{Key: 1, Name: "X", Extensions: strings.Join(exts, " ")}}
		if _, errs, _ := ValidateSettingsForm(long, base); errs.CategoryRows[1].Extensions != "Keep at most 200 extensions" {
			t.Errorf("too long = %+v", errs.CategoryRows[1])
		}
	})
	t.Run("enforces the daemon ranges", func(t *testing.T) {
		f := ok
		f.MaxConcurrent, f.DefaultConnections, f.DefaultDir = "17", "0", ""
		_, errs, valid := ValidateSettingsForm(f, base)
		keys := errs.Keys()
		slices.Sort(keys)
		if valid || !slices.Equal(keys, []ErrorKey{ErrDefaultConnections, ErrDefaultDir, ErrMaxConcurrent}) {
			t.Errorf("keys = %v", keys)
		}
		if errs.MaxConcurrent != "Simultaneous downloads must be from 1 to 16" || errs.DefaultDir != "Choose a default folder" || errs.DefaultConnections != "Connections must be from 1 to 32" {
			t.Errorf("errors = %+v", errs)
		}
	})
}

func TestParseExtensions(t *testing.T) {
	ext, invalid := ParseExtensions(" .Tar, gz;;TGZ tar ")
	if !slices.Equal(ext, []string{"tar", "gz", "tgz"}) || len(invalid) != 0 {
		t.Errorf("extensions = %q, %q", ext, invalid)
	}
	if _, invalid := ParseExtensions("ok b-ad ü"); !slices.Equal(invalid, []string{"b-ad", "ü"}) {
		t.Errorf("invalid = %q", invalid)
	}
	if ext, invalid := ParseExtensions(""); len(ext) != 0 || len(invalid) != 0 {
		t.Errorf("empty = %q, %q", ext, invalid)
	}
}

func TestProxyAndPatternChecks(t *testing.T) {
	// host:port as net.SplitHostPort reads it: "_" and a trailing dot are fine, http:// and https:// too.
	for _, good := range []string{"proxy.example.com:8080", "[::1]:3128", "", "corp_proxy:3128", "proxy.example.com.:8080", "http://proxy:80", "HTTPS://proxy:443/", "10.0.0.1:3128"} {
		if e := CheckHostPort(good); e != "" {
			t.Errorf("CheckHostPort(%q) = %q", good, e)
		}
	}
	for _, bad := range []string{"proxy.example.com", "proxy:0", "proxy:65536", "a b:80", "socks5://proxy:1080", "ftp://proxy:21", "me:pw@proxy:8080", "proxy:8080/path", "a:b:80", ":8080"} {
		if CheckHostPort(bad) == "" {
			t.Errorf("CheckHostPort(%q) passed", bad)
		}
	}
	for in, want := range map[string]string{
		"socks5://proxy:1080": "Enter a SOCKS proxy in the SOCKS field",
		"me:pw@proxy:8080":    "Enter the user name below, not in the address",
		"proxy:0":             "Enter host:port, for example proxy.example.com:8080",
	} {
		if got := CheckHostPort(in); got != want {
			t.Errorf("CheckHostPort(%q) = %q, want %q", in, got, want)
		}
	}
	for _, good := range []string{"socks5://127.0.0.1:1080", "SOCKS4a://proxy:1080", "socks4://p:1080", "socks5h://corp_socks:1080", "socks://p.example.com.:1080"} {
		if e := CheckSocks(good); e != "" {
			t.Errorf("CheckSocks(%q) = %q", good, e)
		}
	}
	for _, bad := range []string{"socks6://proxy:1080", "proxy:1080", "socks5://me:pw@proxy:1080", "socks5://proxy", "socks5://proxy:1080/x"} {
		if CheckSocks(bad) == "" {
			t.Errorf("CheckSocks(%q) passed", bad)
		}
	}
	if got := CheckSocks("proxy:1080"); got != "Enter socks5://host:port (socks4:// and socks4a:// work too)" {
		t.Errorf("socks message = %q", got)
	}
	// Host rule patterns as downexd's: a host, *.suffix, * and ? globs.
	for _, good := range []string{"*.example.com", "example.com", "*example.com", "*", "cdn*.example.com", "a?c.example.com", "my_host", "Example.COM"} {
		if e := CheckHostPattern(good); e != "" {
			t.Errorf("CheckHostPattern(%q) = %q", good, e)
		}
	}
	for _, bad := range []string{"", "example.com:80", "https://example.com", "a/b", "a b", "me@example.com", "ex%41mple.com"} {
		if CheckHostPattern(bad) == "" {
			t.Errorf("CheckHostPattern(%q) passed", bad)
		}
	}
	// Site login patterns as downexd's: an http(s)/* address glob, or a host pattern.
	for _, good := range []string{"https://example.com/*", "*://files.example.com/private/*", "*.example.com", "example.com"} {
		if e := CheckSitePattern(good); e != "" {
			t.Errorf("CheckSitePattern(%q) = %q", good, e)
		}
	}
	for _, bad := range []string{"", "ftp://example.com/*", "https://", "example.com/path", "https://example.com/a b"} {
		if CheckSitePattern(bad) == "" {
			t.Errorf("CheckSitePattern(%q) passed", bad)
		}
	}
	for _, c := range [][2]string{
		{`C:\Tools\scan.exe`, ""},
		{"/usr/bin/clamscan", ""},
		{"scan.exe", "Use the absolute path of the program"},
		{`\\server\share\scan.exe`, "Network (UNC) paths are not supported"},
	} {
		if got := CheckProgramPath(c[0]); got != c[1] {
			t.Errorf("CheckProgramPath(%q) = %q", c[0], got)
		}
	}
}

func m6Base() *downexv1.Settings {
	s := defaultSettings()
	s.DefaultDir = "/home/you/Downloads"
	return s
}

func TestM6Settings(t *testing.T) {
	base := m6Base()
	general := generalForm(base)

	t.Run("keeps the loaded M6 settings when a form has no M6 pages", func(t *testing.T) {
		loaded := proto.CloneOf(base)
		loaded.ClipboardMonitor, loaded.UpdateFeedUrl = true, "https://u.example/f.json"
		if s := mustSettings(t, general, loaded); !proto.Equal(s, loaded) {
			t.Errorf("settings = %v", s)
		}
	})

	t.Run("builds the network settings and finds the page of each error", func(t *testing.T) {
		network := NewNetworkForm(base, 100)
		network.ProxyMode, network.HTTPProxy = downexv1.ProxyMode_PROXY_MODE_MANUAL, " proxy:8080 "
		network.HostRules = []HostRuleDraft{{Key: 1, Pattern: "Example.com", SpeedValue: "1", SpeedUnit: format.MBps}}
		network.SiteLogins = []SiteLoginDraft{{Key: 2, URLPattern: "https://example.com/*", Username: "me", Password: "x"}}
		f := general
		f.Network = &network
		s := mustSettings(t, f, base)
		if p := s.GetProxy(); p.GetMode() != downexv1.ProxyMode_PROXY_MODE_MANUAL || p.GetHttpProxy() != "proxy:8080" || p.GetHasPassword() {
			t.Errorf("proxy = %v", p)
		}
		if len(s.HostRules) != 1 || !proto.Equal(s.HostRules[0], &downexv1.HostRule{Pattern: "example.com", SpeedLimit: MB}) {
			t.Errorf("host rules = %v", s.HostRules)
		}
		if len(s.SiteLogins) != 1 || !proto.Equal(s.SiteLogins[0], &downexv1.SiteLogin{UrlPattern: "https://example.com/*", Username: "me"}) {
			t.Errorf("site logins = %v", s.SiteLogins)
		}
		// The password never becomes part of the settings.
		if strings.Contains(protojson.Format(s), `"x"`) {
			t.Error("the password is in the settings")
		}

		bad := general
		badNet := network
		badNet.HTTPProxy = ""
		badNet.HostRules = append(slices.Clone(network.HostRules), network.HostRules[0])
		badNet.HostRules[1].Key = 3
		bad.Network = &badNet
		integration := NewIntegrationForm(base)
		integration.AVScanEnabled = true
		bad.Integration = &integration
		bad.Updates = &UpdatesForm{UpdateFeedURL: "ftp://u.example/f", AutoCheckUpdates: true}
		_, errs, valid := ValidateSettingsForm(bad, base)
		if valid {
			t.Fatal("passed")
		}
		if !strings.Contains(errs.Proxy, "at least one proxy") || errs.HostRuleRows[3].Pattern != "Another rule has this host" ||
			errs.AVScanCommand != "Choose the scanner program" || errs.UpdateFeedURL != "The update address must start with https://" {
			t.Errorf("errors = %+v", errs)
		}
		for _, c := range []struct {
			errs SettingsFormErrors
			want SettingsPage
			ok   bool
		}{
			{errs, PageNetwork, true},
			{SettingsFormErrors{AVScanArgs: "x"}, PageIntegration, true},
			{SettingsFormErrors{UpdateFeedURL: "x"}, PageUpdates, true},
			// The download fields are on Downloads (General has none that can be wrong).
			{SettingsFormErrors{DefaultDir: "x", UpdateFeedURL: "x"}, PageDownloads, true},
			{SettingsFormErrors{}, "", false},
		} {
			if got, ok := FirstErrorPage(c.errs); got != c.want || ok != c.ok {
				t.Errorf("FirstErrorPage = %q, %v; want %q", got, ok, c.want)
			}
		}
		// PAC needs its address.
		pac := general
		pacNet := NewNetworkForm(base, 1)
		pacNet.ProxyMode = downexv1.ProxyMode_PROXY_MODE_PAC
		pac.Network = &pacNet
		if _, errs, valid := ValidateSettingsForm(pac, base); valid || errs.PacURL != "Enter the address of the proxy configuration script" {
			t.Errorf("pac = %+v", errs)
		}
	})

	t.Run("checks only the proxy fields of the chosen mode, and keeps the others as they are", func(t *testing.T) {
		// As "downex proxy set system" leaves them: manual values (one of them odd) and a PAC address.
		stored := proto.CloneOf(base)
		stored.Proxy = &downexv1.ProxySettings{
			Mode:       downexv1.ProxyMode_PROXY_MODE_SYSTEM,
			HttpProxy:  "corp proxy",
			HttpsProxy: "corp_proxy:3128",
			SocksProxy: "proxy:1080",
			Bypass:     "a\tb",
			PacUrl:     "ftp://wpad.example/proxy.pac",
			Username:   "me",
		}
		for _, mode := range []downexv1.ProxyMode{downexv1.ProxyMode_PROXY_MODE_SYSTEM, downexv1.ProxyMode_PROXY_MODE_UNSPECIFIED, downexv1.ProxyMode_PROXY_MODE_NONE} {
			f := general
			n := NewNetworkForm(stored, 1)
			n.ProxyMode = mode
			f.Network = &n
			s := mustSettings(t, f, stored)
			p := s.GetProxy()
			if p.GetMode() != mode || p.GetHttpProxy() != "corp proxy" || p.GetHttpsProxy() != "corp_proxy:3128" || p.GetSocksProxy() != "proxy:1080" || p.GetPacUrl() != "ftp://wpad.example/proxy.pac" {
				t.Errorf("%v: proxy = %v", mode, p)
			}
		}
		// A user name with a control character is only checked where it is shown (not with no proxy).
		user := NewNetworkForm(stored, 1)
		user.ProxyUser = "m\x07e"
		none, system := user, user
		none.ProxyMode, system.ProxyMode = downexv1.ProxyMode_PROXY_MODE_NONE, downexv1.ProxyMode_PROXY_MODE_SYSTEM
		f := general
		f.Network = &none
		if _, errs, valid := ValidateSettingsForm(f, stored); !valid {
			t.Errorf("no proxy = %+v", errs)
		}
		f.Network = &system
		if _, errs, valid := ValidateSettingsForm(f, stored); valid || errs.ProxyUser != "A user name cannot contain control characters" {
			t.Errorf("system = %+v", errs)
		}

		manual := NewNetworkForm(stored, 1)
		manual.ProxyMode = downexv1.ProxyMode_PROXY_MODE_MANUAL
		f.Network = &manual
		_, errs, valid := ValidateSettingsForm(f, stored)
		keys := errs.Keys()
		slices.Sort(keys)
		if valid || !slices.Equal(keys, []ErrorKey{ErrBypass, ErrHTTPProxy, ErrSocksProxy}) {
			t.Errorf("manual keys = %v", keys)
		}
		pac := NewNetworkForm(stored, 1)
		pac.ProxyMode = downexv1.ProxyMode_PROXY_MODE_PAC
		f.Network = &pac
		_, errs, valid = ValidateSettingsForm(f, stored)
		if valid || !slices.Equal(errs.Keys(), []ErrorKey{ErrPacURL}) || errs.PacURL != "The script address must start with http:// or https:// or file://" {
			t.Errorf("pac = %+v", errs)
		}

		// Host rules other clients may write.
		rules := NewNetworkForm(stored, 1)
		rules.HostRules = nil
		for i, pattern := range []string{"*", "cdn*.example.com", "my_host"} {
			rules.HostRules = append(rules.HostRules, HostRuleDraft{Key: i + 1, Pattern: pattern, SpeedUnit: format.MBps})
		}
		f.Network = &rules
		s := mustSettings(t, f, stored)
		var patterns []string
		for _, r := range s.HostRules {
			patterns = append(patterns, r.GetPattern())
		}
		if !slices.Equal(patterns, []string{"*", "cdn*.example.com", "my_host"}) {
			t.Errorf("patterns = %q", patterns)
		}
	})
}

func TestSiteMediaSettings(t *testing.T) {
	base := defaultSettings()
	base.DefaultDir, base.Categories = "/home/you/Downloads", nil
	general := generalForm(base)
	general.Categories = nil

	t.Run("reads a host rule request cap in MB, keeping a loaded value exactly", func(t *testing.T) {
		for _, c := range []struct {
			bytes int64
			want  string
		}{{0, ""}, {10 * MB, "10"}, {10 * MiB, "10.486"}, {1_500_000, "1.5"}} {
			if got := RequestCapText(c.bytes); got != c.want {
				t.Errorf("RequestCapText(%d) = %q", c.bytes, got)
			}
		}
		for _, c := range []struct {
			text   string
			loaded int64
			want   int64
			ok     bool
		}{
			{"10.486", 10 * MiB, 10 * MiB, true},
			{"10.486", 0, 10_486_000, true},
			{"", 0, 0, true},
			{" 0 ", 0, 0, true},
			{"10", 0, 10 * MB, true},
			{"1.5", 0, 1.5 * MB, true},
			{"1024", 0, 1024 * MB, true},
			{"1.05", 0, 1_050_000, true},
			{"1073", 0, 1_073_000_000, true},
			{"1", 0, 0, false},
			{"1074", 0, 0, false},
			{"1.5", 1_500_000, 1_500_000, true}, // unchanged: as loaded
			{"0.5", 0, 0, false},
			{"1100", 0, 0, false},
			{"ten", 0, 0, false},
			{"-1", 0, 0, false},
			{"0.000", 0, 0, true},
		} {
			if got, ok := ParseRequestCap(c.text, c.loaded); got != c.want || ok != c.ok {
				t.Errorf("ParseRequestCap(%q, %d) = %d, %v", c.text, c.loaded, got, ok)
			}
		}
		// what downexd accepts: 1 MiB to 1 GiB, said as 1.05 to 1073 MB
		if RequestCapRangeMB.Min != "1.05" || RequestCapRangeMB.Max != "1073" {
			t.Errorf("range = %+v", RequestCapRangeMB)
		}
	})

	t.Run("writes the Video sites page into the settings; the channel is nightly or stable", func(t *testing.T) {
		if got := NewVideoForm(defaultSettings()); got != (VideoForm{SiteMedia: true, ToolsAutoUpdate: true, ToolsChannel: "nightly"}) {
			t.Errorf("video form = %+v", got)
		}
		f := general
		f.Video = &VideoForm{ToolsChannel: "stable"}
		if s := mustSettings(t, f, base); s.GetSiteMedia() || s.GetToolsAutoUpdate() || s.GetToolsChannel() != "stable" {
			t.Errorf("video = %v", s)
		}
		f.Video = &VideoForm{SiteMedia: true, ToolsAutoUpdate: true, ToolsChannel: "beta"}
		if s := mustSettings(t, f, base); s.GetToolsChannel() != "nightly" {
			t.Errorf("odd channel = %q", s.GetToolsChannel())
		}
		// without the page, the loaded values stay
		loaded := proto.CloneOf(base)
		loaded.SiteMedia, loaded.ToolsChannel = false, "stable"
		if s := mustSettings(t, general, loaded); s.GetSiteMedia() || s.GetToolsChannel() != "stable" {
			t.Errorf("kept = %v", s)
		}
	})

	t.Run("marks a request cap out of range on the Network page", func(t *testing.T) {
		network := NewNetworkForm(base, 1)
		network.HostRules = []HostRuleDraft{{Key: 4, Pattern: "cdn.example.com", SpeedUnit: format.MBps, MaxRequest: "0.25"}}
		f := general
		f.Network = &network
		_, errs, valid := ValidateSettingsForm(f, base)
		if valid || !reflect.DeepEqual(errs.HostRuleRows, map[int]HostRuleDraftErrors{4: {MaxRequest: "Enter 1.05 to 1073 MB, or leave empty for no cap"}}) {
			t.Errorf("rows = %+v", errs.HostRuleRows)
		}
		if page, _ := FirstErrorPage(errs); page != PageNetwork {
			t.Errorf("page = %q", page)
		}
	})

	t.Run("asks for a video page or a manifest in the Add dialog", func(t *testing.T) {
		if _, errs, ok := ValidateStreamForm(StreamForm{URL: " "}); ok || errs != (StreamFormErrors{URL: "Enter the address of a video page, or of an HLS (.m3u8) or DASH (.mpd) manifest"}) {
			t.Errorf("empty = %+v", errs)
		}
		got, _, ok := ValidateStreamForm(StreamForm{URL: " https://www.youtube.com/watch?v=aqz-KE-bpKQ "})
		if !ok || got != (StreamForm{URL: "https://www.youtube.com/watch?v=aqz-KE-bpKQ"}) {
			t.Errorf("page = %+v", got)
		}
		if _, errs, _ := ValidateStreamForm(StreamForm{URL: "ftp://x/a.m3u8"}); errs.URL != "The address must start with http:// or https://" {
			t.Errorf("ftp = %q", errs.URL)
		}
	})
}

func TestSettingsPages(t *testing.T) {
	if !slices.Equal(SettingsPages, []SettingsPage{"general", "downloads", "network", "integration", "video", "appearance", "updates", "about"}) {
		t.Errorf("pages = %v", SettingsPages)
	}
	for _, k := range []ErrorKey{ErrMaxConcurrent, ErrSpeed, ErrDefaultDir, ErrDefaultConnections, ErrCategories, ErrCategoryRows} {
		if PageOfError(k) != PageDownloads {
			t.Errorf("%s is not on Downloads", k)
		}
	}
	for _, k := range []ErrorKey{ErrProxy, ErrHTTPProxy, ErrHTTPSProxy, ErrSocksProxy, ErrBypass, ErrPacURL, ErrProxyUser, ErrHostRuleRows, ErrSiteLoginRows} {
		if PageOfError(k) != PageNetwork {
			t.Errorf("%s is not on Network", k)
		}
	}
	if PageOfError(ErrAVScanCommand) != PageIntegration || PageOfError(ErrAVScanArgs) != PageIntegration || PageOfError(ErrUpdateFeedURL) != PageUpdates {
		t.Error("integration and updates pages")
	}
	for _, c := range []struct {
		errs SettingsFormErrors
		want SettingsPage
	}{
		{SettingsFormErrors{UpdateFeedURL: "x", AVScanArgs: "x"}, PageIntegration},
		{SettingsFormErrors{UpdateFeedURL: "x", Proxy: "x", CategoryRows: map[int]CategoryDraftErrors{1: {Name: "x"}}}, PageDownloads},
		{SettingsFormErrors{Speed: "x"}, PageDownloads},
	} {
		if got, _ := FirstErrorPage(c.errs); got != c.want {
			t.Errorf("FirstErrorPage(%+v) = %q", c.errs, got)
		}
	}
	// An absent value is no error.
	if _, ok := FirstErrorPage(SettingsFormErrors{Speed: ""}); ok {
		t.Error("an empty message is an error")
	}
	errs := SettingsFormErrors{Speed: "a", HTTPProxy: "b", AVScanArgs: "c", UpdateFeedURL: "d"}
	for page, want := range map[SettingsPage]SettingsFormErrors{
		PageDownloads:   {Speed: "a"},
		PageNetwork:     {HTTPProxy: "b"},
		PageIntegration: {AVScanArgs: "c"},
		PageUpdates:     {UpdateFeedURL: "d"},
		PageGeneral:     {},
		PageAppearance:  {},
	} {
		if got := ErrorsOnPage(errs, page); !reflect.DeepEqual(got, want) {
			t.Errorf("ErrorsOnPage(%s) = %+v", page, got)
		}
	}
}

func TestLoginEditor(t *testing.T) {
	logins := []SiteLoginDraft{{Key: 5, ID: "a1", URLPattern: "https://example.com/*", Username: "me", HasPassword: true}}
	e := NewLoginEditor("https://proposed.example/*")
	if LoginEditorChanged(e, logins) {
		t.Error("an untouched new editor changed")
	}
	e.Username = "you"
	if !LoginEditorChanged(e, logins) {
		t.Error("a typed user name is no change")
	}
	next := 9
	out, key, errs, ok := ApplyLoginEditor(logins, e, func() int { next++; return next })
	if !ok || key != 10 || len(out) != 2 || out[1] != (SiteLoginDraft{Key: 10, URLPattern: "https://proposed.example/*", Username: "you"}) {
		t.Errorf("new = %+v, %d, %+v", out, key, errs)
	}
	edit := LoginEditorOf(logins[0])
	if LoginEditorChanged(edit, logins) {
		t.Error("an untouched editor changed")
	}
	edit.Password = "secret"
	out, key, _, ok = ApplyLoginEditor(logins, edit, nil)
	if !ok || key != 5 || out[0].Password != "secret" || out[0].ClearPassword || logins[0].Password != "" {
		t.Errorf("edit = %+v", out)
	}
	if _, _, errs, ok := ApplyLoginEditor(logins, NewLoginEditor(""), nil); ok || errs != (SiteLoginEditorErrors{URLPattern: "Enter the address it is for", Username: "Enter the user name"}) {
		t.Errorf("empty = %+v", errs)
	}
}
