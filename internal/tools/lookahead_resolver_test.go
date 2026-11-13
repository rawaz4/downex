package tools_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"downex/internal/sitejob"
	"downex/internal/sitemedia"
	"downex/internal/tools"
)

// TestLookAheadsNeverInstall runs look-aheads (docs/SITE-MEDIA-SPEC.md §5.6) through the
// real resolver and manager: PATH copies whose yt-dlp fails every page must not make the
// manager download, install or try its managed copy, nor change its fallback state, and
// the user's request that follows still gets that fallback.
func TestLookAheadsNeverInstall(t *testing.T) {
	t.Parallel()
	x := tools.NewLookAheadEnv(t)
	x.Publish(t, "2026.10.20", "2.5.0")
	x.PathCopies(t, "2026.10.15", "2.5.0", "ERROR: [youtube] aaaaaaaaaaa: nsig extraction failed: some code changed\n")
	r := sitejob.NewResolver(sitejob.ResolverConfig{Tools: x.Manager()})
	t.Cleanup(func() { _ = r.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "aaaaaaaaaaa"} {
		_, err := r.Resolve(ctx, sitejob.Request{PageURL: "https://www.youtube.com/watch?v=" + id, Prefetch: true})
		if !errors.Is(err, sitemedia.ErrSiteError) {
			t.Fatalf("look-ahead of %s = %v, want site_error", id, err)
		}
	}
	if n := x.Requests(); n != 0 {
		t.Errorf("look-aheads made %d release requests", n)
	}
	if x.Trial() || x.Unsuitable() != 0 {
		t.Errorf("look-aheads changed the fallback state: trial %v, %d unsuitable", x.Trial(), x.Unsuitable())
	}
	if in := x.Manager().Status().Tools[0]; in.Managed || in.State != tools.StateReady || in.Version != "2026.10.15" {
		t.Errorf("yt-dlp after the look-aheads: %+v", in)
	}
	// The user's request is not answered from a look-ahead's failure: it runs the PATH
	// copy itself and, when that fails, the managed copy, which is installed for it.
	if _, err := r.Resolve(ctx, sitejob.Request{PageURL: "https://www.youtube.com/watch?v=aaaaaaaaaaa"}); err == nil {
		t.Fatal("the user's request succeeded with failing copies")
	}
	if x.Requests() == 0 {
		t.Error("the user's request got no fallback to the managed copy")
	}
}
