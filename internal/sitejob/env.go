package sitejob

import (
	"net/url"
	"path/filepath"

	"downex/internal/tools"
)

// runTmp is the temporary folder inside a run's work folder (tools.NewRunDir).
const runTmp = "tmp"

// envSpec is what Downex sets in the environment of a yt-dlp run with lease l in the
// work folder run (docs/SITE-MEDIA-SPEC.md §4.7): Deno's folder first on PATH, the
// run's own temporary folder, Deno's cache and, for a proxy with credentials, the
// proxy variables (proxyEnv). Everything else comes from the tools manager's
// allow-list.
func envSpec(l *tools.Lease, run string, p Proxy) tools.EnvSpec {
	s := tools.EnvSpec{TempDir: filepath.Join(run, runTmp), DenoCache: l.DenoCache, Set: proxyEnv(p)}
	if l.Deno != "" {
		s.DenoDir = filepath.Dir(l.Deno)
	}
	return s
}

// proxyEnv returns HTTP_PROXY, HTTPS_PROXY and ALL_PROXY with the proxy's credentials
// in its URL when the proxy has a login, so that they never appear in yt-dlp's
// arguments (which other users of the machine can read) or in logs; nil otherwise
// (buildArgs passes the proxy).
func proxyEnv(p Proxy) []string {
	if p.URL == "" || !p.hasLogin() {
		return nil
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil
	}
	u.User = url.UserPassword(p.Username, p.Password)
	s := u.String()
	return []string{"HTTP_PROXY=" + s, "HTTPS_PROXY=" + s, "ALL_PROXY=" + s}
}
