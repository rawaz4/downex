package sitejob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"downex/internal/sitemedia"
	"downex/internal/tools"
)

// resolved is what one Resolve call returned.
type resolved struct {
	info *sitemedia.Info
	err  error
}

// resolveAsync starts r.Resolve(ctx, req) and returns where its outcome arrives.
func resolveAsync(ctx context.Context, r *Resolver, req Request) <-chan resolved {
	ch := make(chan resolved, 1)
	go func() {
		info, err := r.Resolve(ctx, req)
		ch <- resolved{info, err}
	}()
	return ch
}

// wait returns the outcome on ch, failing the test after 20 s.
func wait(t *testing.T, ch <-chan resolved) resolved {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for Resolve")
		return resolved{}
	}
}

// busy checks that a look-ahead of page was refused at once with ErrBusy, without a run.
func busy(t *testing.T, r *Resolver, f *fakeTools, page string) {
	t.Helper()
	runs := len(f.records(t))
	start := time.Now()
	info, err := r.Resolve(testContext(t), Request{PageURL: page, Prefetch: true})
	if !errors.Is(err, ErrBusy) || !errors.Is(err, sitemedia.ErrSiteResolving) || info != nil {
		t.Errorf("look-ahead of %s = %v, %v; want ErrBusy (site_resolving)", page, info, err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("the look-ahead waited %v", took)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.records(t)); n != runs {
		t.Errorf("a refused look-ahead ran yt-dlp: %d runs, want %d", n, runs)
	}
}

// A look-ahead resolves the page once and fills the cache that a later probe, add or
// run uses: no second run.
func TestPrefetchFillsTheCache(t *testing.T) {
	f := newFakeTools(t, "json")
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	ahead, err := r.Resolve(ctx, Request{PageURL: testPage, Prefetch: true})
	if err != nil || ahead == nil {
		t.Fatalf("look-ahead = %v, %v", ahead, err)
	}
	got, err := r.Resolve(ctx, Request{PageURL: testPage + "#t=12"})
	if err != nil || got != ahead {
		t.Errorf("the request after the look-ahead got %p, %v; want the cached %p", got, err, ahead)
	}
	recs := f.records(t)
	if len(recs) != 1 || !slices.Equal(recs[0].Args, argsOf(f, testPage, false)) {
		t.Errorf("%d runs (%v), want 1 with the arguments of a probe", len(recs), recs)
	}
	// A look-ahead no user's request joined says nothing about the copy to the tools
	// manager (its fallback and rollback are for users' extractions, §4.5), and takes its
	// copy through AcquireReady, which never installs.
	if got := f.outcomes(); len(got) != 0 {
		t.Errorf("reports %v, want none", got)
	}
	if a, ready := f.acquireCalls(), f.readyAcquireCalls(); a != 0 || ready != 1 {
		t.Errorf("Acquire %d, AcquireReady %d; want 0 and 1", a, ready)
	}
}

// A look-ahead joins the running extraction of its page (a user's), and a user's request
// joins a running look-ahead, also after the look-ahead's caller left: one run each.
func TestPrefetchJoinsTheRunningExtraction(t *testing.T) {
	f := newFakeTools(t, "block")
	r := newTestResolver(t, f, ResolverConfig{MaxConcurrent: 1})
	ctx := testContext(t)
	user := resolveAsync(ctx, r, Request{PageURL: testPage})
	waitFor(t, "the user's extraction", func() bool { return len(f.records(t)) == 1 })
	ahead := resolveAsync(ctx, r, Request{PageURL: testPage, Prefetch: true})
	time.Sleep(200 * time.Millisecond)
	select {
	case o := <-ahead:
		t.Fatalf("the look-ahead did not join: %v, %v", o.info, o.err)
	default:
	}
	f.release(t)
	u, a := wait(t, user), wait(t, ahead)
	if u.err != nil || a.err != nil || u.info == nil || u.info != a.info {
		t.Fatalf("user %p, %v; look-ahead %p, %v", u.info, u.err, a.info, a.err)
	}
	if n := len(f.records(t)); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}

	// The other way round; the look-ahead's caller gives up first.
	g := newFakeTools(t, "block")
	r = newTestResolver(t, g, ResolverConfig{MaxConcurrent: 1})
	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := r.Resolve(short, Request{PageURL: testPage, Prefetch: true}); !errors.Is(err, sitemedia.ErrSiteResolving) ||
		errors.Is(err, ErrBusy) {
		t.Fatalf("look-ahead past its deadline = %v, want site_resolving while it goes on", err)
	}
	waitFor(t, "the look-ahead's extraction", func() bool { return len(g.records(t)) == 1 })
	user = resolveAsync(ctx, r, Request{PageURL: testPage})
	time.Sleep(200 * time.Millisecond)
	g.release(t)
	if u := wait(t, user); u.err != nil || u.info == nil {
		t.Fatalf("user = %v, %v", u.info, u.err)
	}
	if n := len(g.records(t)); n != 1 {
		t.Errorf("%d runs, want 1: the user's request did not join the look-ahead", n)
	}
}

// A look-ahead never waits behind or delays a user's work: while any other extraction
// runs or waits it is refused at once (ErrBusy, not cached), at most one runs, and a
// running one holds none of the users' turns.
func TestPrefetchNeverQueues(t *testing.T) {
	f := newFakeTools(t, "block")
	r := newTestResolver(t, f, ResolverConfig{MaxConcurrent: 1})
	ctx := testContext(t)
	other := testPage + "&n=2"

	// Behind a user's extraction.
	user := resolveAsync(ctx, r, Request{PageURL: testPage})
	waitFor(t, "the user's extraction", func() bool { return len(f.records(t)) == 1 })
	busy(t, r, f, other)
	f.release(t)
	if u := wait(t, user); u.err != nil {
		t.Fatal(u.err)
	}
	// Refusals are not cached: once idle, the same look-ahead runs.
	if _, err := r.Resolve(ctx, Request{PageURL: other, Prefetch: true}); err != nil {
		t.Fatalf("look-ahead once idle: %v", err)
	}
	if n := len(f.records(t)); n != 2 {
		t.Fatalf("%d runs, want 2", n)
	}

	// Its own cap of one; a user's request starts at once beside it, although the users
	// have a single turn.
	g := newFakeTools(t, "block")
	r = newTestResolver(t, g, ResolverConfig{MaxConcurrent: 1})
	ahead := resolveAsync(ctx, r, Request{PageURL: testPage, Prefetch: true})
	waitFor(t, "the look-ahead's extraction", func() bool { return len(g.records(t)) == 1 })
	busy(t, r, g, other)
	user = resolveAsync(ctx, r, Request{PageURL: other})
	waitFor(t, "the user's extraction beside the look-ahead", func() bool { return len(g.records(t)) == 2 })
	busy(t, r, g, testPage+"&n=3")
	g.release(t)
	if a, u := wait(t, ahead), wait(t, user); a.err != nil || u.err != nil {
		t.Fatalf("look-ahead %v, user %v", a.err, u.err)
	}
}

// A look-ahead alone never tries another copy of yt-dlp after a failure and reports
// nothing (the tools manager would install the other copy for that), and a failure that
// may be the copy's is not cached: the user's request that follows resolves the page
// itself, with the fallback of a user's extraction.
func TestPrefetchTriesNoOtherCopy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alt := filepath.Join(t.TempDir(), "yt-dlp-alt"+filepath.Ext(exe))
	copyFile(t, exe, alt)
	f := newFakeTools(t, "stderr", "json")
	f.exes = []string{exe, alt}
	f.setStderr(t, "ERROR: [youtube] x: Something broke\n", 1)
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	if _, err := r.Resolve(ctx, Request{PageURL: testPage, Prefetch: true}); !errors.Is(err, sitemedia.ErrSiteError) {
		t.Fatalf("look-ahead = %v, want site_error", err)
	}
	if n, a, ready := len(f.records(t)), f.acquireCalls(), f.readyAcquireCalls(); n != 1 || a != 0 || ready != 1 {
		t.Errorf("%d runs, %d Acquire and %d AcquireReady calls; want 1, 0 and 1", n, a, ready)
	}
	if got := f.outcomes(); len(got) != 0 {
		t.Errorf("reports %v, want none", got)
	}
	info, err := r.Resolve(ctx, Request{PageURL: testPage})
	if err != nil || info == nil {
		t.Fatalf("the user's request after the failed look-ahead = %v, %v; want its own extraction", info, err)
	}
	if recs := f.records(t); len(recs) != 2 || recs[1].Exe != alt {
		t.Errorf("%d runs (%v), want the user's own with the other copy", len(recs), recs)
	}

	// Once a user's request joined the look-ahead, its failure gets the other copy as a
	// user's extraction does.
	g := newFakeTools(t, "block-stderr", "json")
	g.exes = []string{exe, alt}
	g.setStderr(t, "ERROR: [youtube] x: Something broke\n", 1)
	r = newTestResolver(t, g, ResolverConfig{})
	ahead := resolveAsync(ctx, r, Request{PageURL: testPage, Prefetch: true})
	waitFor(t, "the look-ahead's extraction", func() bool { return len(g.records(t)) == 1 })
	user := resolveAsync(ctx, r, Request{PageURL: testPage})
	time.Sleep(200 * time.Millisecond)
	g.release(t)
	a, u := wait(t, ahead), wait(t, user)
	if a.err != nil || u.err != nil || u.info == nil || u.info != a.info {
		t.Fatalf("look-ahead %v, %v; user %v, %v", a.info, a.err, u.info, u.err)
	}
	if recs := g.records(t); len(recs) != 2 || recs[1].Exe != alt {
		t.Errorf("%d runs (%v), want the second with the other copy", len(recs), recs)
	}
	if got := g.outcomes(); !slices.Equal(got, []tools.Outcome{tools.SiteError, tools.OK}) {
		t.Errorf("reports %v, want [SiteError OK]", got)
	}
	if a, ready := g.acquireCalls(), g.readyAcquireCalls(); a != 1 || ready != 1 {
		t.Errorf("Acquire %d, AcquireReady %d; want 1 (the other copy) and 1 (the look-ahead)", a, ready)
	}
}

// A look-ahead's answer about the page (a refusal yt-dlp gave, not a failure of the
// copy) is cached like a probe's; the tools' state never is.
func TestPrefetchCachesPageRefusals(t *testing.T) {
	f := newFakeTools(t, "stderr", "json")
	f.setStderr(t, "ERROR: [youtube] aaaaaaaaaaa: Private video. Sign in if you've been granted access to this video\n", 1)
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	if _, err := r.Resolve(ctx, Request{PageURL: testPage, Prefetch: true}); !errors.Is(err, sitemedia.ErrSiteUnavailable) {
		t.Fatalf("look-ahead = %v, want site_unavailable", err)
	}
	if _, err := r.Resolve(ctx, Request{PageURL: testPage}); !errors.Is(err, sitemedia.ErrSiteUnavailable) {
		t.Fatalf("the user's request = %v, want the cached site_unavailable", err)
	}
	if n := len(f.records(t)); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}

	g := newFakeTools(t, "json")
	g.acquireErr = fmt.Errorf("%w: yt-dlp or Deno is missing", sitemedia.ErrToolsUnavailable)
	r = newTestResolver(t, g, ResolverConfig{})
	if _, err := r.Resolve(ctx, Request{PageURL: testPage, Prefetch: true}); !errors.Is(err, sitemedia.ErrToolsUnavailable) {
		t.Fatalf("look-ahead without tools = %v", err)
	}
	if a, ready := g.acquireCalls(), g.readyAcquireCalls(); a != 0 || ready != 1 {
		t.Errorf("Acquire %d, AcquireReady %d; want 0 and 1", a, ready)
	}
	g.mu.Lock()
	g.acquireErr = nil
	g.mu.Unlock()
	if info, err := r.Resolve(ctx, Request{PageURL: testPage}); err != nil || info == nil {
		t.Errorf("the user's request after tools_unavailable = %v, %v; want its own extraction", info, err)
	}
}
