package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Secret kinds of DaemonService.SetSecret/ClearSecret.
const (
	secretProxy     = "proxy"
	secretSiteLogin = "site_login"
)

// settingsSession reads and writes the daemon's settings as a whole (UpdateSettings
// replaces every field, so each change starts from a fresh read).
type settingsSession struct {
	c *client.Client
}

func (s settingsSession) get(ctx context.Context, op string) (*downexv1.Settings, error) {
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := s.c.Daemon.GetSettings(cctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		return nil, rpcError(op, err)
	}
	if st := resp.Msg.GetSettings(); st != nil {
		return st, nil
	}
	return &downexv1.Settings{}, nil
}

func (s settingsSession) put(ctx context.Context, op string, st *downexv1.Settings) (*downexv1.Settings, error) {
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := s.c.Daemon.UpdateSettings(cctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: st}))
	if err != nil {
		return nil, rpcError(op, err)
	}
	if us := resp.Msg.GetSettings(); us != nil {
		return us, nil
	}
	return st, nil
}

func (s settingsSession) setSecret(ctx context.Context, op, kind, id, password string) error {
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := s.c.Daemon.SetSecret(cctx, connect.NewRequest(&downexv1.SetSecretRequest{Kind: kind, Id: id, Password: password}))
	if err != nil {
		return rpcError(op, err)
	}
	return nil
}

// clearSecret removes a stored password; one that does not exist is no error.
func (s settingsSession) clearSecret(ctx context.Context, op, kind, id string) error {
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := s.c.Daemon.ClearSecret(cctx, connect.NewRequest(&downexv1.ClearSecretRequest{Kind: kind, Id: id}))
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return rpcError(op, err)
	}
	return nil
}

// ---- proxy ----

const proxySynopsis = `proxy [show] [--json]
       downex proxy set system|none [--user u] [--json]
       downex proxy set manual [--http host:port] [--https host:port] [--socks url] [--bypass list] [--user u] [--json]
       downex proxy set pac <url> [--user u] [--json]`

const proxyAbout = "Show or change the proxy downexd uses for downloads:\n" +
	"  system  the operating system's proxy settings (the default; on Windows including\n" +
	"          automatic detection and configuration scripts)\n" +
	"  none    connect directly\n" +
	"  manual  --http for http:// downloads, --https for https:// downloads (both host:port),\n" +
	"          --socks socks5://host:port (also socks4://, socks4a://) for both; --bypass lists\n" +
	"          hosts reached directly, separated by ';' or ',' (wildcards such as *.example.com,\n" +
	"          <local> for names without dots)\n" +
	"  pac     a proxy auto-configuration script at an http, https or file URL\n" +
	"Flags not given keep their values. --user sets the proxy's user name; the password is\n" +
	"then asked for without echo (or read as the first line of standard input) and stored in\n" +
	"the system's credential store, never in the settings. --user \"\" removes both."

func (a *app) proxy(ctx context.Context, args []string) error {
	return a.runSub(ctx, "proxy", proxySynopsis, proxyAbout, "show or set", args, "show",
		map[string]func(context.Context, []string) error{"show": a.proxyShow, "set": a.proxySet})
}

func (a *app) proxyShow(ctx context.Context, args []string) error {
	fs := a.flagSet("proxy show", "proxy show [--json]", "Show the proxy downexd uses for downloads.")
	t := a.daemonFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"proxy", "show takes no arguments"}
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	st, err := settingsSession{c}.get(ctx, "proxy")
	if err != nil {
		return err
	}
	return a.printProxy(newProxyView(st.GetProxy()), *asJSON)
}

func (a *app) proxySet(ctx context.Context, args []string) error {
	fs := a.flagSet("proxy set", "proxy set system|none|manual|pac [url] [flags]", proxyAbout)
	t := a.daemonFlags(fs)
	var httpP, httpsP, socks, bypass, user string
	fs.StringVar(&httpP, "http", "", "manual: proxy `host:port` for http:// downloads (\"\" = none)")
	fs.StringVar(&httpsP, "https", "", "manual: proxy `host:port` for https:// downloads (\"\" = none)")
	fs.StringVar(&socks, "socks", "", "manual: SOCKS proxy `url` socks5://host:port, socks4:// or socks4a:// (\"\" = none)")
	fs.StringVar(&bypass, "bypass", "", "manual: `hosts` reached without the proxy, separated by ';' or ','")
	fs.StringVar(&user, "user", "", "proxy user `name`; the password is asked for (\"\" = no authentication)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) == 0 {
		return usageError{"proxy", "set expects a mode: system, none, manual or pac"}
	}
	set := visited(fs)
	mode, err := parseProxyMode(pos[0])
	if err != nil {
		return usageError{"proxy", err.Error()}
	}
	var pacURL string
	switch {
	case mode == downexv1.ProxyMode_PROXY_MODE_PAC && len(pos) > 2,
		mode != downexv1.ProxyMode_PROXY_MODE_PAC && len(pos) > 1:
		return usageError{"proxy", "too many arguments"}
	case mode == downexv1.ProxyMode_PROXY_MODE_PAC && len(pos) == 2:
		if pacURL, err = checkPACURL(pos[1]); err != nil {
			return usageError{"proxy", err.Error()}
		}
	}
	if mode != downexv1.ProxyMode_PROXY_MODE_MANUAL {
		for _, f := range []string{"http", "https", "socks", "bypass"} {
			if set[f] {
				return usageError{"proxy", "--" + f + " applies only to \"proxy set manual\""}
			}
		}
	}
	if set["user"] && mode == downexv1.ProxyMode_PROXY_MODE_NONE && user != "" {
		return usageError{"proxy", "--user does not apply to \"proxy set none\""}
	}
	if set["http"] {
		if httpP, err = checkHostPort(httpP); err != nil {
			return usageError{"proxy", "--http: " + err.Error()}
		}
	}
	if set["https"] {
		if httpsP, err = checkHostPort(httpsP); err != nil {
			return usageError{"proxy", "--https: " + err.Error()}
		}
	}
	if set["socks"] {
		if socks, err = checkSOCKSURL(socks); err != nil {
			return usageError{"proxy", "--socks: " + err.Error()}
		}
	}
	if set["bypass"] {
		if bypass, err = normalizeBypass(bypass); err != nil {
			return usageError{"proxy", "--bypass: " + err.Error()}
		}
	}
	user = strings.TrimSpace(user)
	if set["user"] && strings.IndexFunc(user, needsEscape) >= 0 {
		return usageError{"proxy", "--user must not contain control characters"}
	}
	// The password is read before anything changes, so that an interrupted prompt
	// leaves the settings as they were.
	var password string
	if set["user"] && user != "" {
		if password, err = a.sys.password(ctx, a.stderr, fmt.Sprintf("Proxy password for %s: ", printable(user))); err != nil {
			return err
		}
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	ss := settingsSession{c}
	st, err := ss.get(ctx, "proxy")
	if err != nil {
		return err
	}
	p := st.GetProxy() // st is this command's own fresh copy
	if p == nil {
		p = &downexv1.ProxySettings{}
	}
	p.Mode = mode
	if set["http"] {
		p.HttpProxy = httpP
	}
	if set["https"] {
		p.HttpsProxy = httpsP
	}
	if set["socks"] {
		p.SocksProxy = socks
	}
	if set["bypass"] {
		p.Bypass = bypass
	}
	if pacURL != "" {
		p.PacUrl = pacURL
	}
	if set["user"] {
		p.Username = user
	}
	switch {
	case mode == downexv1.ProxyMode_PROXY_MODE_MANUAL && p.GetHttpProxy() == "" && p.GetHttpsProxy() == "" && p.GetSocksProxy() == "":
		return usageError{"proxy", "manual needs --http, --https or --socks"}
	case mode == downexv1.ProxyMode_PROXY_MODE_PAC && p.GetPacUrl() == "":
		return usageError{"proxy", "pac needs the URL of the configuration script"}
	}
	st.Proxy = p
	if st, err = ss.put(ctx, "proxy", st); err != nil {
		return err
	}
	if set["user"] {
		if user == "" {
			err = ss.clearSecret(ctx, "proxy", secretProxy, "")
		} else {
			err = ss.setSecret(ctx, "proxy", secretProxy, "", password)
		}
		if err != nil {
			return err
		}
		if st, err = ss.get(ctx, "proxy"); err != nil { // has_password changed
			return err
		}
	}
	return a.printProxy(newProxyView(st.GetProxy()), *asJSON)
}

// parseProxyMode parses a proxy mode name.
func parseProxyMode(s string) (downexv1.ProxyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "system", "auto":
		return downexv1.ProxyMode_PROXY_MODE_SYSTEM, nil
	case "none", "direct", "off":
		return downexv1.ProxyMode_PROXY_MODE_NONE, nil
	case "manual":
		return downexv1.ProxyMode_PROXY_MODE_MANUAL, nil
	case "pac":
		return downexv1.ProxyMode_PROXY_MODE_PAC, nil
	}
	return 0, fmt.Errorf("unknown proxy mode %q (want system, none, manual or pac)", s)
}

// proxyModeName names a proxy mode as "proxy set" takes it.
func proxyModeName(m downexv1.ProxyMode) string {
	switch m {
	case downexv1.ProxyMode_PROXY_MODE_UNSPECIFIED, downexv1.ProxyMode_PROXY_MODE_SYSTEM:
		return "system"
	case downexv1.ProxyMode_PROXY_MODE_NONE:
		return "none"
	case downexv1.ProxyMode_PROXY_MODE_MANUAL:
		return "manual"
	case downexv1.ProxyMode_PROXY_MODE_PAC:
		return "pac"
	}
	return fmt.Sprintf("mode(%d)", int32(m))
}

// checkHostPort validates a "host:port" proxy address ("" = none).
func checkHostPort(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(s)
	n, perr := strconv.Atoi(port)
	if err != nil || host == "" || perr != nil || n < 1 || n > 65535 || strings.ContainsAny(host, "/@ ") {
		return "", fmt.Errorf("want host:port, e.g. proxy.example.com:8080, got %q", s)
	}
	return s, nil
}

// checkSOCKSURL validates a SOCKS proxy URL ("" = none).
func checkSOCKSURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("want socks5://host:port, socks4://host:port or socks4a://host:port, got %q", s)
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks4", "socks4a":
	default:
		return "", fmt.Errorf("want socks5://host:port, socks4://host:port or socks4a://host:port, got %q", s)
	}
	if u.User != nil {
		return "", errors.New("put the user name in --user, not in the URL (the password is asked for)")
	}
	if _, err := checkHostPort(u.Host); err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return "", fmt.Errorf("want socks5://host:port, socks4://host:port or socks4a://host:port, got %q", s)
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host, nil
}

// checkPACURL validates the URL of a proxy auto-configuration script.
func checkPACURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err == nil {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			if u.Host != "" {
				return s, nil
			}
		case "file":
			if u.Path != "" || u.Opaque != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("want an http, https or file URL of the configuration script, got %q", s)
}

// normalizeBypass returns a bypass list separated by ';'.
func normalizeBypass(s string) (string, error) {
	var out []string
	for part := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == ';' || r == ',' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.ContainsAny(part, " \t") || strings.IndexFunc(part, needsEscape) >= 0 {
			return "", fmt.Errorf("invalid host pattern %q", part)
		}
		out = append(out, part)
	}
	return strings.Join(out, ";"), nil
}

// proxyView is the JSON form of the proxy settings (never with a password).
type proxyView struct {
	Mode        string `json:"mode"` // system, none, manual or pac
	HTTPProxy   string `json:"httpProxy,omitempty"`
	HTTPSProxy  string `json:"httpsProxy,omitempty"`
	SOCKSProxy  string `json:"socksProxy,omitempty"`
	Bypass      string `json:"bypass,omitempty"`
	PACURL      string `json:"pacUrl,omitempty"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

func newProxyView(p *downexv1.ProxySettings) proxyView {
	return proxyView{
		Mode: proxyModeName(p.GetMode()), HTTPProxy: p.GetHttpProxy(), HTTPSProxy: p.GetHttpsProxy(),
		SOCKSProxy: p.GetSocksProxy(), Bypass: p.GetBypass(), PACURL: p.GetPacUrl(),
		Username: p.GetUsername(), HasPassword: p.GetHasPassword(),
	}
}

// describeProxy summarises the proxy settings on one line.
func describeProxy(v proxyView) string {
	var out string
	switch v.Mode {
	case "system":
		out = "system settings"
	case "none":
		out = "none (direct connections)"
	case "manual":
		var parts []string
		if v.HTTPProxy != "" {
			parts = append(parts, "http "+v.HTTPProxy)
		}
		if v.HTTPSProxy != "" {
			parts = append(parts, "https "+v.HTTPSProxy)
		}
		if v.SOCKSProxy != "" {
			parts = append(parts, v.SOCKSProxy)
		}
		out = "manual (" + strings.Join(parts, ", ") + ")"
	case "pac":
		out = "configuration script " + v.PACURL
	default:
		out = v.Mode
	}
	if v.Username != "" && v.Mode != "none" {
		out += ", user " + v.Username
	}
	return printable(out)
}

// printProxy prints the proxy settings: the fields of the current mode (JSON: all of them,
// so that stored manual servers stay visible while another mode is chosen).
func (a *app) printProxy(v proxyView, asJSON bool) error {
	if asJSON {
		return writeJSON(a.stdout, v)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Mode:\t%s\n", v.Mode)
	row := func(k, val string) {
		if val != "" {
			fmt.Fprintf(tw, "%s:\t%s\n", k, printable(val))
		}
	}
	switch v.Mode {
	case "manual":
		row("HTTP proxy", v.HTTPProxy)
		row("HTTPS proxy", v.HTTPSProxy)
		row("SOCKS proxy", v.SOCKSProxy)
		row("Bypass", v.Bypass)
	case "pac":
		row("Configuration script", v.PACURL)
	}
	if v.Username != "" && v.Mode != "none" {
		pw := "not set"
		if v.HasPassword {
			pw = "stored"
		}
		fmt.Fprintf(tw, "User:\t%s (password %s)\n", printable(v.Username), pw)
	}
	return tw.Flush()
}

// ---- site logins ----

const loginSynopsis = `login [ls] [--json]
       downex login add <url-pattern> --user name [--json]
       downex login rm <id>`

const loginAbout = "Manage the site logins downexd uses when a server asks for authentication (Basic,\n" +
	"Digest, NTLM/Negotiate). A pattern is a URL with wildcards, such as\n" +
	"https://example.com/files/* or *.example.com. add asks for the password without echo (or\n" +
	"reads the first line of standard input) and stores it in the system's credential store,\n" +
	"never in the settings; adding a pattern and user that exist replaces their password."

func (a *app) login(ctx context.Context, args []string) error {
	return a.runSub(ctx, "login", loginSynopsis, loginAbout, "ls, add or rm", args, "ls",
		map[string]func(context.Context, []string) error{
			"ls": a.loginLs, "list": a.loginLs, "add": a.loginAdd, "rm": a.loginRm, "remove": a.loginRm,
		})
}

// siteLoginView is the JSON form of a site login (never with its password).
type siteLoginView struct {
	ID          string `json:"id"`
	URLPattern  string `json:"urlPattern"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
}

func newSiteLoginViews(ls []*downexv1.SiteLogin) []siteLoginView {
	out := make([]siteLoginView, 0, len(ls))
	for _, l := range ls {
		out = append(out, siteLoginView{ID: l.GetId(), URLPattern: l.GetUrlPattern(), Username: l.GetUsername(), HasPassword: l.GetHasPassword()})
	}
	return out
}

func (a *app) loginLs(ctx context.Context, args []string) error {
	fs := a.flagSet("login ls", "login ls [--json]", "List the site logins (their passwords are never shown).")
	t := a.daemonFlags(fs)
	asJSON := fs.Bool("json", false, "print a JSON array")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"login", "ls takes no arguments"}
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	st, err := settingsSession{c}.get(ctx, "login")
	if err != nil {
		return err
	}
	views := newSiteLoginViews(st.GetSiteLogins())
	if *asJSON {
		return writeJSON(a.stdout, views)
	}
	return writeSiteLogins(a.stdout, views)
}

func writeSiteLogins(w io.Writer, views []siteLoginView) error {
	if len(views) == 0 {
		fmt.Fprintln(w, "No site logins.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tURL PATTERN\tUSER\tPASSWORD")
	for _, v := range views {
		pw := "not set"
		if v.HasPassword {
			pw = "stored"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", printable(v.ID), printable(v.URLPattern), printable(v.Username), pw)
	}
	return tw.Flush()
}

func (a *app) loginAdd(ctx context.Context, args []string) error {
	fs := a.flagSet("login add", "login add <url-pattern> --user name [--json]", loginAbout)
	t := a.daemonFlags(fs)
	user := fs.String("user", "", "the user `name` for the site")
	asJSON := fs.Bool("json", false, "print the login as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		return usageError{"login", "add expects exactly one URL pattern"}
	}
	pattern := strings.TrimSpace(pos[0])
	name := strings.TrimSpace(*user)
	switch {
	case pattern == "":
		return usageError{"login", "the URL pattern must not be empty"}
	case strings.ContainsAny(pattern, " \t") || strings.IndexFunc(pattern, needsEscape) >= 0:
		return usageError{"login", fmt.Sprintf("invalid URL pattern %q", pattern)}
	case name == "":
		return usageError{"login", "add needs --user name"}
	case strings.IndexFunc(name, needsEscape) >= 0:
		return usageError{"login", "--user must not contain control characters"}
	}
	if u, err := url.Parse(pattern); err == nil && u.User != nil {
		return usageError{"login", "put the user name in --user, not in the URL pattern"}
	}
	// The password is read before anything changes.
	password, err := a.sys.password(ctx, a.stderr, fmt.Sprintf("Password for %s at %s: ", printable(name), printable(pattern)))
	if err != nil {
		return err
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	ss := settingsSession{c}
	st, err := ss.get(ctx, "login add")
	if err != nil {
		return err
	}
	login := findSiteLogin(st.GetSiteLogins(), func(l *downexv1.SiteLogin) bool {
		return l.GetUrlPattern() == pattern && l.GetUsername() == name
	})
	added := login == nil
	if added {
		known := make(map[string]bool, len(st.GetSiteLogins()))
		for _, l := range st.GetSiteLogins() {
			known[l.GetId()] = true
		}
		st.SiteLogins = append(st.SiteLogins, &downexv1.SiteLogin{UrlPattern: pattern, Username: name})
		if st, err = ss.put(ctx, "login add", st); err != nil {
			return err
		}
		// The daemon assigns the new login's id.
		login = findSiteLogin(st.GetSiteLogins(), func(l *downexv1.SiteLogin) bool {
			return !known[l.GetId()] && l.GetUrlPattern() == pattern && l.GetUsername() == name
		})
		if login == nil || login.GetId() == "" {
			return errors.New("login add: downexd did not keep the new site login")
		}
	}
	if err := ss.setSecret(ctx, "login add", secretSiteLogin, login.GetId(), password); err != nil {
		if added {
			// A login without its password would only fail; take it out again.
			if rerr := a.removeSiteLogin(ctx, ss, login.GetId()); rerr != nil {
				a.con.printf("warning: could not remove the new site login %s again: %v\n", login.GetId(), rerr)
			}
		}
		return err
	}
	if st, err = ss.get(ctx, "login add"); err == nil {
		if l := findSiteLogin(st.GetSiteLogins(), func(l *downexv1.SiteLogin) bool { return l.GetId() == login.GetId() }); l != nil {
			login = l
		}
	}
	if *asJSON {
		return writeJSON(a.stdout, newSiteLoginViews([]*downexv1.SiteLogin{login})[0])
	}
	verb := "Added"
	if !added {
		verb = "Replaced the password of"
	}
	fmt.Fprintf(a.stdout, "%s site login %s: %s as %s.\n", verb, printable(login.GetId()), printable(pattern), printable(name))
	return nil
}

func findSiteLogin(ls []*downexv1.SiteLogin, match func(*downexv1.SiteLogin) bool) *downexv1.SiteLogin {
	for _, l := range ls {
		if match(l) {
			return l
		}
	}
	return nil
}

// removeSiteLogin drops login id from the settings (read, modify, write).
func (a *app) removeSiteLogin(ctx context.Context, ss settingsSession, id string) error {
	st, err := ss.get(ctx, "login rm")
	if err != nil {
		return err
	}
	kept := st.SiteLogins[:0:0]
	for _, l := range st.GetSiteLogins() {
		if l.GetId() != id {
			kept = append(kept, l)
		}
	}
	st.SiteLogins = kept
	_, err = ss.put(ctx, "login rm", st)
	return err
}

func (a *app) loginRm(ctx context.Context, args []string) error {
	fs := a.flagSet("login rm", "login rm <id>", "Remove a site login and its stored password (ids: \"downex login ls\").")
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return usageError{"login", "rm expects exactly one site login id"}
	}
	id := strings.TrimSpace(pos[0])
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	ss := settingsSession{c}
	st, err := ss.get(ctx, "login rm")
	if err != nil {
		return err
	}
	login := findSiteLogin(st.GetSiteLogins(), func(l *downexv1.SiteLogin) bool { return l.GetId() == id })
	if login == nil {
		return fmt.Errorf("no site login with id %q (see \"downex login ls\")", id)
	}
	// The password first: should that fail, the login stays listed and can be removed again.
	if err := ss.clearSecret(ctx, "login rm", secretSiteLogin, id); err != nil {
		return err
	}
	if err := a.removeSiteLogin(ctx, ss, id); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Removed site login %s (%s as %s).\n", printable(id), printable(login.GetUrlPattern()), printable(login.GetUsername()))
	return nil
}
