package validate

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// ---- Settings → Network, Integration, Updates (docs/M6-SPEC.md §2–§4) ----

// HostRuleDraft is a host rule as the Network page edits it.
type HostRuleDraft struct {
	Key     int
	Pattern string
	// Connections as typed; 0 or empty = the default.
	Connections string
	SpeedValue  string
	SpeedUnit   RateUnit
	// SpeedLimitBytes is the limit as loaded (bytes per second): kept exactly while its text is
	// unchanged (format's rate parsing).
	SpeedLimitBytes int64
	// MaxRequest is the longest ranged request in MB, as typed; 0 or empty = no cap.
	MaxRequest string
	// MaxRequestBytes is the cap as loaded: kept exactly while its text is unchanged.
	MaxRequestBytes int64
}

// HostRuleDraftErrors are a host rule row's messages.
type HostRuleDraftErrors struct {
	Pattern, Connections, Speed, MaxRequest string
}

// Empty reports no error.
func (e HostRuleDraftErrors) Empty() bool { return e == HostRuleDraftErrors{} }

// SiteLoginDraft is a site login as the Network page edits it. Its password is never shown: a
// new one waits in Password until the settings are saved (then SetSecret), and ClearPassword
// removes the stored one.
type SiteLoginDraft struct {
	Key int
	// ID is "" until downexd assigned one (a new login).
	ID         string
	URLPattern string
	Username   string
	// HasPassword: a password is stored for it (as loaded).
	HasPassword   bool
	Password      string
	ClearPassword bool
}

// SiteLoginDraftErrors are a site login row's messages.
type SiteLoginDraftErrors struct {
	URLPattern, Username string
}

// Empty reports no error.
func (e SiteLoginDraftErrors) Empty() bool { return e == SiteLoginDraftErrors{} }

// NetworkForm is Settings → Network.
type NetworkForm struct {
	// ProxyMode UNSPECIFIED is shown as (and means) system.
	ProxyMode  downexv1.ProxyMode
	HTTPProxy  string
	HTTPSProxy string
	SocksProxy string
	Bypass     string
	PacURL     string
	ProxyUser  string
	HostRules  []HostRuleDraft
	SiteLogins []SiteLoginDraft
}

// IntegrationForm is Settings → Integration.
type IntegrationForm struct {
	ClipboardMonitor bool
	// CheckDownloadedFiles is Settings field 27, the file check (DESKTOP-SPEC §9.9).
	CheckDownloadedFiles bool
	MarkDownloadedFiles  bool
	AVScanEnabled        bool
	AVScanCommand        string
	AVScanArgs           string
}

// UpdatesForm is Settings → Updates.
type UpdatesForm struct {
	UpdateFeedURL    string
	AutoCheckUpdates bool
}

// VideoForm is Settings → Video sites (SITE-MEDIA-SPEC §5.4).
type VideoForm struct {
	SiteMedia       bool
	ToolsAutoUpdate bool
	// ToolsChannel is "stable" or "nightly".
	ToolsChannel string
}

// mb is 1 MB = 1,000,000 B: the Network page edits request caps in decimal megabytes.
const mb = 1000 * 1000

// RequestCapText is a request cap in MB as the Network page shows it: "10", "10.486" (10 MiB);
// "" for none.
func RequestCapText(bytes int64) string {
	if bytes <= 0 {
		return ""
	}
	return jsNumber(jsRound(float64(bytes)/mb*1000) / 1000)
}

// RequestCapRangeMB is the caps downexd accepts, in MB as the page says them: "1.05"
// (MinRequestBytes, 1 MiB, rounded up) to "1073" (MaxRequestBytes, 1 GiB, rounded down), so
// both ends parse.
var RequestCapRangeMB = struct{ Min, Max string }{
	Min: jsNumber(math.Ceil(float64(MinRequestBytes)/mb*100) / 100),
	Max: jsNumber(math.Floor(float64(MaxRequestBytes) / mb)),
}

// ParseRequestCap reads a host rule's request cap as typed (MB): 0 or empty = no cap, else
// 1.05–1073 MB (1 MiB to 1 GiB, what downexd accepts). The loaded value is kept exactly while
// its text is unchanged. ok is false when it is not one.
func ParseRequestCap(text string, loadedBytes int64) (bytes int64, ok bool) {
	t := jsTrim(text)
	if t == "" || zeros(t) {
		return 0, true
	}
	if loadedBytes > 0 && t == RequestCapText(loadedBytes) {
		return loadedBytes, true
	}
	if !decimal(t) {
		return 0, false
	}
	b := jsRound(number(t) * mb)
	if b < MinRequestBytes || b > MaxRequestBytes {
		return 0, false
	}
	return int64(b), true
}

// zeros is /^0+(\.0*)?$/.
func zeros(t string) bool {
	whole, frac, dot := strings.Cut(t, ".")
	return whole != "" && strings.Trim(whole, "0") == "" && (!dot || strings.Trim(frac, "0") == "")
}

// decimal is /^\d+(\.\d+)?$/.
func decimal(t string) bool {
	whole, frac, dot := strings.Cut(t, ".")
	return allDigits(whole) && (!dot || allDigits(frac))
}

// HostRuleDrafts are the editor rows of host rules, keyed from firstKey.
func HostRuleDrafts(rules []*downexv1.HostRule, firstKey int) []HostRuleDraft {
	out := make([]HostRuleDraft, len(rules))
	for i, r := range rules {
		value, unit := splitRate(r.GetSpeedLimit())
		connections := ""
		if r.GetMaxConnections() > 0 {
			connections = strconv.Itoa(int(r.GetMaxConnections()))
		}
		out[i] = HostRuleDraft{
			Key:             firstKey + i,
			Pattern:         r.GetPattern(),
			Connections:     connections,
			SpeedValue:      value,
			SpeedUnit:       unit,
			SpeedLimitBytes: r.GetSpeedLimit(),
			MaxRequest:      RequestCapText(r.GetMaxRequestBytes()),
			MaxRequestBytes: r.GetMaxRequestBytes(),
		}
	}
	return out
}

// SiteLoginDrafts are the editor rows of site logins, keyed from firstKey.
func SiteLoginDrafts(logins []*downexv1.SiteLogin, firstKey int) []SiteLoginDraft {
	out := make([]SiteLoginDraft, len(logins))
	for i, l := range logins {
		out[i] = SiteLoginDraft{Key: firstKey + i, ID: l.GetId(), URLPattern: l.GetUrlPattern(), Username: l.GetUsername(), HasPassword: l.GetHasPassword()}
	}
	return out
}

// SiteLoginEditor is the Network page's editor of one site login: a new one (IsNew) or a login
// of the list. It belongs to the Settings dialog, so that it outlives the page (switching pages)
// and Save can apply what it holds.
type SiteLoginEditor struct {
	// Key is the login's key in the list; unused when IsNew.
	Key           int
	IsNew         bool
	URLPattern    string
	Username      string
	Password      string
	ClearPassword bool
	// OpenedWith is the address pattern it opened with (a new login's may be proposed).
	OpenedWith string
}

// SiteLoginEditorErrors are the editor's messages.
type SiteLoginEditorErrors struct {
	URLPattern, Username string
}

// NewLoginEditor is the editor of a new login for an address pattern (auth_required proposes
// one).
func NewLoginEditor(urlPattern string) SiteLoginEditor {
	return SiteLoginEditor{IsNew: true, URLPattern: urlPattern, OpenedWith: urlPattern}
}

// LoginEditorOf is the editor of a login of the list.
func LoginEditorOf(l SiteLoginDraft) SiteLoginEditor {
	return SiteLoginEditor{Key: l.Key, URLPattern: l.URLPattern, Username: l.Username, ClearPassword: l.ClearPassword, OpenedWith: l.URLPattern}
}

func findLogin(logins []SiteLoginDraft, e SiteLoginEditor) int {
	if e.IsNew {
		return -1
	}
	return slices.IndexFunc(logins, func(l SiteLoginDraft) bool { return l.Key == e.Key })
}

// LoginEditorChanged reports that the editor holds something the user entered or changed
// (closing it would lose it).
func LoginEditorChanged(e SiteLoginEditor, logins []SiteLoginDraft) bool {
	if e.Password != "" {
		return true
	}
	i := findLogin(logins, e)
	if i < 0 {
		return jsTrim(e.Username) != "" || jsTrim(e.URLPattern) != jsTrim(e.OpenedWith)
	}
	l := logins[i]
	return jsTrim(e.URLPattern) != jsTrim(l.URLPattern) || jsTrim(e.Username) != jsTrim(l.Username) || e.ClearPassword != l.ClearPassword
}

// ApplyLoginEditor applies the editor to the list: a new login is added (with a key from
// newKey), an edited one replaced; its password waits in the draft until the settings are
// saved. key is the login's key in the list.
func ApplyLoginEditor(logins []SiteLoginDraft, e SiteLoginEditor, newKey func() int) (siteLogins []SiteLoginDraft, key int, errs SiteLoginEditorErrors, ok bool) {
	urlPattern := jsTrim(e.URLPattern)
	username := jsTrim(e.Username)
	if pe := CheckSitePattern(urlPattern); pe != "" {
		errs.URLPattern = pe
		if urlPattern == "" {
			errs.URLPattern = "Enter the address it is for"
		}
	}
	if username == "" {
		errs.Username = "Enter the user name"
	} else if hasControl(username) {
		errs.Username = "A user name cannot contain control characters"
	}
	if errs != (SiteLoginEditorErrors{}) {
		return nil, 0, errs, false
	}
	i := findLogin(logins, e)
	if i < 0 {
		key := newKey()
		out := append(slices.Clone(logins), SiteLoginDraft{Key: key, URLPattern: urlPattern, Username: username, Password: e.Password})
		return out, key, errs, true
	}
	out := slices.Clone(logins)
	l := &out[i]
	l.URLPattern, l.Username = urlPattern, username
	if e.Password != "" {
		l.Password, l.ClearPassword = e.Password, false
	} else {
		l.ClearPassword = e.ClearPassword
	}
	return out, l.Key, errs, true
}

// NewNetworkForm is the Network page's form for settings; host rules are keyed from firstKey,
// site logins after them.
func NewNetworkForm(s *downexv1.Settings, firstKey int) NetworkForm {
	p := s.GetProxy()
	return NetworkForm{
		ProxyMode:  p.GetMode(),
		HTTPProxy:  p.GetHttpProxy(),
		HTTPSProxy: p.GetHttpsProxy(),
		SocksProxy: p.GetSocksProxy(),
		Bypass:     p.GetBypass(),
		PacURL:     p.GetPacUrl(),
		ProxyUser:  p.GetUsername(),
		HostRules:  HostRuleDrafts(s.GetHostRules(), firstKey),
		SiteLogins: SiteLoginDrafts(s.GetSiteLogins(), firstKey+len(s.GetHostRules())),
	}
}

// NewIntegrationForm is the Integration page's form for settings.
func NewIntegrationForm(s *downexv1.Settings) IntegrationForm {
	return IntegrationForm{
		ClipboardMonitor:     s.GetClipboardMonitor(),
		CheckDownloadedFiles: s.GetCheckDownloadedFiles(),
		MarkDownloadedFiles:  s.GetMarkDownloadedFiles(),
		AVScanEnabled:        s.GetAvScanEnabled(),
		AVScanCommand:        s.GetAvScanCommand(),
		AVScanArgs:           s.GetAvScanArgs(),
	}
}

// NewUpdatesForm is the Updates page's form for settings.
func NewUpdatesForm(s *downexv1.Settings) UpdatesForm {
	return UpdatesForm{UpdateFeedURL: s.GetUpdateFeedUrl(), AutoCheckUpdates: s.GetAutoCheckUpdates()}
}

// NewVideoForm is the Video sites page's form for settings.
func NewVideoForm(s *downexv1.Settings) VideoForm {
	return VideoForm{SiteMedia: s.GetSiteMedia(), ToolsAutoUpdate: s.GetToolsAutoUpdate(), ToolsChannel: s.GetToolsChannel()}
}

// hostNameByte is a byte a proxy host may have: a name or address ("_" and a trailing dot are
// fine), and any non-ASCII letter.
func hostNameByte(c byte) bool {
	return isAlpha(c) || isDigit(c) || c >= 0x80 || strings.IndexByte("-._~!$&'()*+,;=", c) >= 0
}

// hostPortProblem reads "host:port" as net.SplitHostPort does (as `downex proxy set manual
// --http` checks it): a bracketed IPv6 address or a host without ':', '/', '@' or spaces, and a
// port from 1 to 65535. "" when it is one; else "no port", "login", "bad host" or "bad port".
func hostPortProblem(text string) string {
	var host, port string
	if strings.HasPrefix(text, "[") && strings.Contains(text, "]:") {
		end := strings.IndexByte(text, ']')
		if end+1 < len(text) && text[end+1] == ':' {
			host, port = text[1:end], text[end+2:]
			if host == "" || strings.Trim(host, "0123456789abcdefABCDEF:.") != "" {
				return "bad host"
			}
			return portProblem(port)
		}
	}
	i := strings.LastIndexByte(text, ':')
	if i < 0 {
		return "no port"
	}
	host, port = text[:i], text[i+1:]
	if strings.Contains(host, "@") {
		return "login"
	}
	if host == "" {
		return "bad host"
	}
	for j := 0; j < len(host); j++ {
		if !hostNameByte(host[j]) {
			return "bad host"
		}
	}
	return portProblem(port)
}

func portProblem(port string) string {
	if port == "" || len(port) > 5 || !allDigits(port) {
		return "no port"
	}
	if n, _ := strconv.Atoi(port); n < 1 || n > 65535 {
		return "bad port"
	}
	return ""
}

// schemePrefix is /^([A-Za-z][A-Za-z0-9+.-]*):\/\//: the scheme and the length of "scheme://".
func schemePrefix(t string) (scheme string, n int) {
	if t == "" || !isAlpha(t[0]) {
		return "", 0
	}
	i := 1
	for i < len(t) && (isAlpha(t[i]) || isDigit(t[i]) || t[i] == '+' || t[i] == '.' || t[i] == '-') {
		i++
	}
	if !strings.HasPrefix(t[i:], "://") {
		return "", 0
	}
	return t[:i], i + 3
}

const hostPortHint = "Enter host:port, for example proxy.example.com:8080"

// CheckHostPort checks the address of an HTTP proxy as downexd and the CLI take it: host:port,
// optionally as an http:// or https:// (a proxy reached over TLS) address.
func CheckHostPort(text string) string {
	t := jsTrim(text)
	if t == "" {
		return ""
	}
	scheme, n := schemePrefix(t)
	kind := strings.ToLower(scheme)
	if n > 0 && strings.HasPrefix(kind, "socks") {
		return "Enter a SOCKS proxy in the SOCKS field"
	}
	if n > 0 && kind != "http" && kind != "https" {
		return hostPortHint
	}
	rest := t
	if n > 0 {
		rest = strings.TrimSuffix(t[n:], "/")
	}
	switch hostPortProblem(rest) {
	case "":
		return ""
	case "login":
		return "Enter the user name below, not in the address"
	}
	return hostPortHint
}

// CheckSocks checks "socks5://host:port" (socks5h, socks4, socks4a too, as downexd takes
// them).
func CheckSocks(text string) string {
	t := jsTrim(text)
	if t == "" {
		return ""
	}
	problem := "scheme"
	if rest, ok := socksRest(t); ok {
		problem = hostPortProblem(strings.TrimSuffix(rest, "/"))
	}
	switch problem {
	case "":
		return ""
	case "login":
		return "Enter the user name below, not in the address"
	}
	return "Enter socks5://host:port (socks4:// and socks4a:// work too)"
}

// socksRest is /^socks(?:5h?|4a?)?:\/\/(.*)$/i: what follows the scheme, without line ends.
func socksRest(t string) (string, bool) {
	lower := strings.ToLower(t)
	for _, scheme := range []string{"socks5h://", "socks5://", "socks4a://", "socks4://", "socks://"} {
		if strings.HasPrefix(lower, scheme) {
			rest := t[len(scheme):]
			if strings.ContainsAny(rest, "\n\r\u2028\u2029") {
				return "", false
			}
			return rest, true
		}
	}
	return "", false
}

// CheckHostPattern checks a host rule pattern as downexd takes it: a host, "*.example.com", "*"
// or a glob with * and ? (no scheme, port, path or spaces); letters of any case (it is stored in
// lower case).
func CheckHostPattern(text string) string {
	t := jsLower(jsTrim(text))
	if t == "" {
		return "Enter a host, such as example.com or *.example.com"
	}
	for i := 0; i < len(t); i++ {
		if c := t[i]; !(c >= 'a' && c <= 'z' || isDigit(c) || strings.IndexByte(".-_*?", c) >= 0) {
			return "Use a host such as example.com; * and ? match any characters"
		}
	}
	return ""
}

// CheckSitePattern checks a site login pattern as downexd takes it: an http, https or * address
// glob such as "https://example.com/*", or a host pattern such as "*.example.com".
func CheckSitePattern(text string) string {
	t := jsTrim(text)
	if t == "" {
		return "Enter the address it is for, such as https://example.com/*"
	}
	if strings.ContainsFunc(t, isJSSpace) || hasControl(t) {
		return "An address pattern cannot contain spaces"
	}
	if i := strings.Index(t, "://"); i >= 0 {
		scheme := jsLower(t[:i])
		if (scheme == "http" || scheme == "https" || scheme == "*") && len(t) > i+3 {
			return ""
		}
	} else if CheckHostPattern(t) == "" {
		return ""
	}
	return "Use an address such as https://example.com/*, or a host such as *.example.com"
}

// CheckProgramPath checks the absolute path of a program (Windows "C:\…" or "/…").
func CheckProgramPath(text string) string {
	t := jsTrim(text)
	switch {
	case t == "":
		return ""
	case len(t) >= 2 && isSep(t[0]) && isSep(t[1]):
		return "Network (UNC) paths are not supported"
	case len(t) >= 4 && isAlpha(t[0]) && t[1] == ':' && isSep(t[2]) && notLineEnd(t[3:]),
		len(t) >= 2 && t[0] == '/' && notLineEnd(t[1:]):
		return ""
	}
	return "Use the absolute path of the program"
}

// notLineEnd is a regular expression's "." on the first character of s: anything but a line
// terminator.
func notLineEnd(s string) bool {
	for _, r := range s {
		return r != '\n' && r != '\r' && r != 0x2028 && r != 0x2029
	}
	return false
}

// checkAddress checks an address with one of schemes ("https"); what names it in the message.
func checkAddress(text string, schemes []string, what string) string {
	u, ok := parseURL(text)
	if !ok {
		return what + " is not a valid address"
	}
	if !slices.Contains(schemes, u.scheme) {
		quoted := make([]string, len(schemes))
		for i, s := range schemes {
			quoted[i] = s + "://"
		}
		return what + " must start with " + strings.Join(quoted, " or ")
	}
	if u.scheme != "file" && u.host == "" {
		return what + " has no host"
	}
	return ""
}

// network is what the Network page writes into the settings.
type network struct {
	proxy      *downexv1.ProxySettings
	hostRules  []*downexv1.HostRule
	siteLogins []*downexv1.SiteLogin
}

func validateNetwork(form *NetworkForm, errs *SettingsFormErrors) *network {
	before := errs.count()
	mode := form.ProxyMode
	http := jsTrim(form.HTTPProxy)
	https := jsTrim(form.HTTPSProxy)
	socks := jsTrim(form.SocksProxy)
	pac := jsTrim(form.PacURL)
	// Only the fields of the chosen mode are shown and used (as downexd checks them); the others
	// are kept as they are, like `downex proxy set system` keeps them.
	if mode == downexv1.ProxyMode_PROXY_MODE_MANUAL {
		errs.HTTPProxy = CheckHostPort(http)
		errs.HTTPSProxy = CheckHostPort(https)
		errs.SocksProxy = CheckSocks(socks)
		if http == "" && https == "" && socks == "" {
			errs.Proxy = "Enter at least one proxy, or choose another option"
		}
		if hasControl(form.Bypass) {
			errs.Bypass = "The list cannot contain control characters"
		}
	}
	if mode == downexv1.ProxyMode_PROXY_MODE_PAC {
		if pac == "" {
			errs.PacURL = "Enter the address of the proxy configuration script"
		} else {
			errs.PacURL = checkAddress(pac, []string{"http", "https", "file"}, "The script address")
		}
	}
	if mode != downexv1.ProxyMode_PROXY_MODE_NONE && hasControl(form.ProxyUser) {
		errs.ProxyUser = "A user name cannot contain control characters"
	}

	ruleRows := map[int]HostRuleDraftErrors{}
	var hostRules []*downexv1.HostRule
	patterns := map[string]bool{}
	for _, r := range form.HostRules {
		var row HostRuleDraftErrors
		pattern := jsLower(jsTrim(r.Pattern))
		if pe := CheckHostPattern(pattern); pe != "" {
			row.Pattern = pe
		} else if patterns[pattern] {
			row.Pattern = "Another rule has this host"
		}
		patterns[pattern] = true
		conn := jsTrim(r.Connections)
		if conn != "" {
			row.Connections = CheckIntRange(conn, 0, MaxConnections, "Connections")
		}
		speed, speedOK := parseRate(r.SpeedValue, r.SpeedUnit, r.SpeedLimitBytes)
		if !speedOK {
			row.Speed = noLimit
		}
		capBytes, capOK := ParseRequestCap(r.MaxRequest, r.MaxRequestBytes)
		if !capOK {
			row.MaxRequest = "Enter " + RequestCapRangeMB.Min + " to " + RequestCapRangeMB.Max + " MB, or leave empty for no cap"
		}
		if !row.Empty() {
			ruleRows[r.Key] = row
		}
		maxConnections := int32(0)
		if conn != "" {
			maxConnections = int32(number(conn))
		}
		hostRules = append(hostRules, &downexv1.HostRule{Pattern: pattern, MaxConnections: maxConnections, SpeedLimit: speed, MaxRequestBytes: capBytes})
	}
	if len(ruleRows) > 0 {
		errs.HostRuleRows = ruleRows
	}

	loginRows := map[int]SiteLoginDraftErrors{}
	var siteLogins []*downexv1.SiteLogin
	seen := map[string]bool{}
	for _, l := range form.SiteLogins {
		var row SiteLoginDraftErrors
		urlPattern := jsTrim(l.URLPattern)
		username := jsTrim(l.Username)
		row.URLPattern = CheckSitePattern(urlPattern)
		// The editor asks for a user name; one stored without (by another client) is kept.
		if hasControl(username) {
			row.Username = "A user name cannot contain control characters"
		}
		key := jsLower(urlPattern) + "\n" + username
		if row.Empty() && seen[key] {
			row.URLPattern = "Another login has this address and user name"
		}
		seen[key] = true
		if !row.Empty() {
			loginRows[l.Key] = row
		}
		siteLogins = append(siteLogins, &downexv1.SiteLogin{Id: l.ID, UrlPattern: urlPattern, Username: username, HasPassword: l.HasPassword})
	}
	if len(loginRows) > 0 {
		errs.SiteLoginRows = loginRows
	}
	if errs.count() > before {
		return nil
	}
	return &network{
		proxy: &downexv1.ProxySettings{
			Mode:       mode,
			HttpProxy:  http,
			HttpsProxy: https,
			SocksProxy: socks,
			Bypass:     jsTrim(form.Bypass),
			PacUrl:     pac,
			Username:   jsTrim(form.ProxyUser),
		},
		hostRules:  hostRules,
		siteLogins: siteLogins,
	}
}

func validateIntegration(form *IntegrationForm, errs *SettingsFormErrors) *IntegrationForm {
	before := errs.count()
	command := jsTrim(form.AVScanCommand)
	if ce := CheckProgramPath(command); ce != "" {
		errs.AVScanCommand = ce
	} else if form.AVScanEnabled && command == "" {
		errs.AVScanCommand = "Choose the scanner program"
	}
	if hasControl(form.AVScanArgs) {
		errs.AVScanArgs = "The arguments cannot contain control characters"
	} else if utf16Len(form.AVScanArgs) > MaxScanArgsLength {
		errs.AVScanArgs = "The arguments are too long"
	}
	if errs.count() > before {
		return nil
	}
	out := *form
	out.AVScanCommand, out.AVScanArgs = command, jsTrim(form.AVScanArgs)
	return &out
}

func validateUpdates(form *UpdatesForm, errs *SettingsFormErrors) *UpdatesForm {
	url := jsTrim(form.UpdateFeedURL)
	if url != "" {
		if e := checkAddress(url, []string{"https"}, "The update address"); e != "" {
			errs.UpdateFeedURL = e
			return nil
		}
	}
	return &UpdatesForm{UpdateFeedURL: url, AutoCheckUpdates: form.AutoCheckUpdates}
}

// SettingsForm is the Settings dialog's form.
type SettingsForm struct {
	MaxConcurrent string
	SpeedValue    string
	SpeedUnit     RateUnit
	// SpeedLimitBytes is the limit as loaded: kept exactly while its text is unchanged.
	SpeedLimitBytes         int64
	DefaultDir              string
	DefaultConnections      string
	Categories              []CategoryDraft
	UseCategoryFolders      bool
	ConfirmBrowserDownloads bool
	NotifyOnComplete        bool
	NotifyOnFailure         bool
	LaunchAtLogin           bool
	ShowTrayIcon            bool
	ShowProgressWindow      bool
	ShowCompleteDialog      bool
	// PoliteMode is Polite mode's switch (docs/POLITE-SPEC.md §8); polite_target_ms is not in
	// the window and keeps its loaded value.
	PoliteMode bool
	// Network is Settings → Network (M6); nil = the loaded values are kept. So are
	// Integration, Updates and Video.
	Network     *NetworkForm
	Integration *IntegrationForm
	Updates     *UpdatesForm
	// Video is Settings → Video sites (SITE-MEDIA-SPEC §5.4).
	Video *VideoForm
	// RenewLinks is Settings → Downloads' "Expired links" (DESKTOP-SPEC §10, Settings field
	// 28); UNSPECIFIED keeps the loaded value.
	RenewLinks downexv1.RenewLinks
}

// ValidateSettingsForm checks the Settings dialog. The result starts from base, the settings as
// they were loaded, so that fields the dialog does not show (unknown ones included) are written
// back unchanged.
func ValidateSettingsForm(form SettingsForm, base *downexv1.Settings) (*downexv1.Settings, SettingsFormErrors, bool) {
	var errs SettingsFormErrors
	errs.MaxConcurrent = CheckIntRange(form.MaxConcurrent, MinConcurrent, MaxConcurrent, "Simultaneous downloads")
	speed, speedOK := parseRate(form.SpeedValue, form.SpeedUnit, form.SpeedLimitBytes)
	if !speedOK {
		errs.Speed = noLimit
	}
	dir := jsTrim(form.DefaultDir)
	if dir == "" {
		errs.DefaultDir = "Choose a default folder"
	} else {
		errs.DefaultDir = CheckDir(dir)
	}
	errs.DefaultConnections = CheckIntRange(form.DefaultConnections, MinConnections, MaxConnections, "Connections")
	categories := ValidateCategories(form.Categories)
	if !categories.OK {
		errs.Categories = categories.List
		if len(categories.Rows) > 0 {
			errs.CategoryRows = categories.Rows
		}
	}
	var net *network
	if form.Network != nil {
		net = validateNetwork(form.Network, &errs)
	}
	var integration *IntegrationForm
	if form.Integration != nil {
		integration = validateIntegration(form.Integration, &errs)
	}
	var updates *UpdatesForm
	if form.Updates != nil {
		updates = validateUpdates(form.Updates, &errs)
	}
	if errs.count() > 0 || !categories.OK {
		return nil, errs, false
	}

	s := proto.CloneOf(base)
	if s == nil {
		s = &downexv1.Settings{}
	}
	if net != nil {
		// A mode the user did not change stays as stored (UNSPECIFIED = system); the stored
		// password stays (a new one goes through SetSecret).
		p := proto.CloneOf(base.GetProxy())
		if p == nil {
			p = &downexv1.ProxySettings{}
		}
		p.Mode, p.HttpProxy, p.HttpsProxy, p.SocksProxy = net.proxy.Mode, net.proxy.HttpProxy, net.proxy.HttpsProxy, net.proxy.SocksProxy
		p.Bypass, p.PacUrl, p.Username = net.proxy.Bypass, net.proxy.PacUrl, net.proxy.Username
		s.Proxy, s.HostRules, s.SiteLogins = p, net.hostRules, net.siteLogins
	}
	if integration != nil {
		s.ClipboardMonitor = integration.ClipboardMonitor
		s.CheckDownloadedFiles = integration.CheckDownloadedFiles
		s.MarkDownloadedFiles = integration.MarkDownloadedFiles
		s.AvScanEnabled = integration.AVScanEnabled
		s.AvScanCommand = integration.AVScanCommand
		s.AvScanArgs = integration.AVScanArgs
	}
	if updates != nil {
		s.UpdateFeedUrl, s.AutoCheckUpdates = updates.UpdateFeedURL, updates.AutoCheckUpdates
	}
	if v := form.Video; v != nil {
		// The channel comes from a select: anything else is the default, as downexd reads it.
		channel := "nightly"
		if v.ToolsChannel == "stable" {
			channel = "stable"
		}
		s.SiteMedia, s.ToolsAutoUpdate, s.ToolsChannel = v.SiteMedia, v.ToolsAutoUpdate, channel
	}
	s.MaxConcurrentDownloads = int32(number(jsTrim(form.MaxConcurrent)))
	s.GlobalSpeedLimit = speed
	s.DefaultDir = dir
	s.DefaultMaxConnections = int32(number(jsTrim(form.DefaultConnections)))
	s.Categories = categories.Categories
	s.UseCategoryFolders = form.UseCategoryFolders
	s.ConfirmBrowserDownloads = form.ConfirmBrowserDownloads
	s.NotifyOnComplete = form.NotifyOnComplete
	s.NotifyOnFailure = form.NotifyOnFailure
	s.LaunchAtLogin = form.LaunchAtLogin
	s.ShowTrayIcon = form.ShowTrayIcon
	s.ShowProgressWindow = form.ShowProgressWindow
	s.ShowCompleteDialog = form.ShowCompleteDialog
	if form.RenewLinks != downexv1.RenewLinks_RENEW_LINKS_UNSPECIFIED {
		s.RenewLinks = form.RenewLinks
	}
	s.PoliteMode = form.PoliteMode
	return s, errs, true
}
