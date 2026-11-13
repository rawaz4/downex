package sitejob

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"downex/internal/sitemedia"
	"downex/internal/tools"
	"downex/internal/update"
)

// TestRealSite (DOWNEX_REAL_SITE_URL=<page>) reaches the network and runs only when
// asked for (docs/SITE-MEDIA-SPEC.md §8.3); it is never part of the default test run.
// It sets up yt-dlp and Deno (a copy on PATH, or downloaded from GitHub into
// DOWNEX_REAL_TOOLS_DIR, else a temporary folder), checks that the yt-dlp in use
// accepts --compat-options no-certifi, resolves the page and logs the offer.
func TestRealSite(t *testing.T) {
	page := os.Getenv("DOWNEX_REAL_SITE_URL")
	if page == "" {
		t.Skip("set DOWNEX_REAL_SITE_URL=<page URL> to resolve a real page with yt-dlp")
	}
	dir := os.Getenv("DOWNEX_REAL_TOOLS_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	mgr, err := tools.New(tools.Config{Dir: dir, Client: update.NewHTTPClient(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	mgr.Configure(true, false, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	lease, err := mgr.Acquire(ctx)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	t.Logf("yt-dlp %s (managed %v) at %s; Deno %q", lease.YtDlpVersion, lease.YtDlpManaged, lease.YtDlp, lease.Deno)
	run, err := tools.NewRunDir(lease.WorkRoot)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tools.Exec(ctx, tools.ExecSpec{
		Path: lease.YtDlp, Args: []string{"--ignore-config", "--compat-options", "no-certifi", "--version"},
		Env: mgr.Env(envSpec(lease, run, Proxy{})), Dir: run, Timeout: time.Minute,
	})
	_ = os.RemoveAll(run)
	lease.Release()
	if err != nil || res.ExitCode != 0 {
		t.Errorf("yt-dlp does not accept --compat-options no-certifi: exit %d, %v: %s", res.ExitCode, err, res.StderrTail)
	}

	r := NewResolver(ResolverConfig{Tools: mgr})
	defer r.Close()
	start := time.Now()
	info, err := r.Resolve(ctx, Request{PageURL: page})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Logf("resolved in %v: %s %q (%s), %d formats, %d protected", time.Since(start).Round(time.Millisecond),
		info.Site, info.Title, info.Duration, len(info.Formats), info.Protected)
	u, _ := url.Parse(page)
	off, err := sitemedia.Offers(info, u.Host)
	if err != nil {
		t.Fatalf("Offers: %v", err)
	}
	t.Logf("format %s, container %s, default %s + %s", off.Format, off.Container, off.DefaultVideo, off.DefaultAudio)
	for _, tr := range off.Tracks {
		t.Logf("  %-12s %-6s %-12s %-40s size %d approx %v %s %s", tr.ID, tr.Kind, tr.Name, tr.Codecs, tr.Size, tr.SizeApprox,
			tr.Ext, tr.Language)
	}
}
