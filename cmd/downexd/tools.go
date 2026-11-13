package main

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"

	"downex/internal/daemon"
	"downex/internal/platform"
	"downex/internal/sitejob"
	"downex/internal/tools"
)

// toolsDirName is the tools folder next to the database of a daemon other than the
// user's own (docs/SITE-MEDIA-SPEC.md §3.9).
const toolsDirName = "tools"

// toolsFolder returns the folder of the yt-dlp and Deno copies a daemon uses
// (docs/SITE-MEDIA-SPEC.md §3.9): --tools-dir when given, else platform.ToolsDir() for
// the user's own daemon and <dir of the database>/tools for every other one, so that
// tests and private instances never touch the user's folder.
func toolsFolder(flagValue string, own bool, dbPath string) (string, error) {
	switch {
	case flagValue != "":
		return filepath.Abs(flagValue)
	case own:
		return platform.ToolsDir()
	}
	return filepath.Join(filepath.Dir(dbPath), toolsDirName), nil
}

// siteTools are a daemon's site-media parts (docs/SITE-MEDIA-SPEC.md §2.1): the tools
// manager, which finds, downloads and updates yt-dlp and Deno, and the resolver that
// runs them. Only the user's own daemon (and an e2etest build given a release server)
// downloads tools; every other daemon uses copies on PATH or already in its folder.
type siteTools struct {
	mgr       *tools.Manager
	resolver  *sitejob.Resolver
	downloads bool // the manager downloads and updates tools: the update timer runs
	closeOnce sync.Once
}

// openSiteTools creates the tools manager of the folder dir and its resolver. own is the
// user's own daemon; hooks may let a test daemon download from a local release server.
// Downloads go through the proxy of the network settings (toolsClient).
func openSiteTools(dir string, own bool, network *daemon.NetworkRuntime, hooks testHooks, log *slog.Logger) (*siteTools, error) {
	cfg := tools.Config{Dir: dir, ReleaseBase: hooks.toolsBase, PassEnvPrefix: hooks.passEnv}
	if hooks.ytdlpKey != nil {
		cfg.YtDlpKeys = [][]byte{hooks.ytdlpKey}
	}
	downloads := own || hooks.toolsBase != ""
	if downloads {
		cfg.Client = toolsClient(network, hooks.roots)
	}
	mgr, err := tools.New(cfg) // creates the folder when missing
	if err != nil {
		return nil, err
	}
	res := sitejob.NewResolver(sitejob.ResolverConfig{Tools: mgr, Log: log.With("component", "sitejob")})
	return &siteTools{mgr: mgr, resolver: res, downloads: downloads}, nil
}

// close stops the running extractions (their process trees are killed and their work
// folders removed), then the manager's installs and checks. It runs once; later calls
// do nothing.
func (s *siteTools) close(log *slog.Logger) {
	s.closeOnce.Do(func() {
		_ = s.resolver.Close()
		if err := s.mgr.Close(); err != nil && !errors.Is(err, tools.ErrClosed) {
			log.Warn("stopping the tools manager", "err", err)
		}
	})
}

// toolsClient returns the client of tool downloads (docs/SITE-MEDIA-SPEC.md §4.2):
// https only, through the proxy of the network settings, trusting roots instead of the
// system's root certificates when they are given (the end-to-end tests' build only),
// like update checks.
func toolsClient(network *daemon.NetworkRuntime, roots *x509.CertPool) *http.Client {
	return updateClient(network, roots)
}

// toolsAdapter is the daemon's view of the tools manager (daemon.Tools): the methods
// are the manager's, its status converted field by field.
type toolsAdapter struct{ m *tools.Manager }

var _ daemon.Tools = toolsAdapter{}

func (a toolsAdapter) Configure(enabled, autoUpdate bool, channel string) {
	a.m.Configure(enabled, autoUpdate, channel)
}

func (a toolsAdapter) Status() daemon.ToolsSnapshot { return toolsSnapshot(a.m.Status()) }

func (a toolsAdapter) Ensure(ctx context.Context) error { return a.m.Ensure(ctx) }

func (a toolsAdapter) Install(ctx context.Context, update bool) (daemon.ToolsSnapshot, error) {
	s, err := a.m.Install(ctx, update)
	return toolsSnapshot(s), err
}

func (a toolsAdapter) CheckUpdate(ctx context.Context) error { return a.m.CheckUpdate(ctx) }

func (a toolsAdapter) Subscribe(fn func()) (cancel func()) { return a.m.Subscribe(fn) }

// toolsSnapshot converts the manager's status for the daemon. Info.Signer (the
// Authenticode subject of a managed deno.exe) has no field in the API and stays in
// tools.json.
func toolsSnapshot(s tools.Snapshot) daemon.ToolsSnapshot {
	out := daemon.ToolsSnapshot{Enabled: s.Enabled, Ready: s.Ready, Discovering: s.Discovering}
	if s.Tools != nil {
		out.Tools = make([]daemon.ToolSnapshot, len(s.Tools))
	}
	for i, t := range s.Tools {
		out.Tools[i] = daemon.ToolSnapshot{
			Name: t.Name, State: toolState(t.State), Version: t.Version, Path: t.Path, Managed: t.Managed,
			LatestVersion: t.LatestVersion, CheckedAt: t.CheckedAt, BytesDone: t.BytesDone, BytesTotal: t.BytesTotal,
			Error: t.Error, ErrorCode: t.ErrorCode, Verified: t.Verified,
		}
	}
	return out
}

// toolState maps a tool's state to the daemon's.
func toolState(s tools.State) daemon.ToolState {
	switch s {
	case tools.StateMissing:
		return daemon.ToolStateMissing
	case tools.StateInstalling:
		return daemon.ToolStateInstalling
	case tools.StateReady:
		return daemon.ToolStateReady
	case tools.StateFailed:
		return daemon.ToolStateFailed
	case tools.StateDisabled:
		return daemon.ToolStateDisabled
	}
	return daemon.ToolStateUnknown
}
