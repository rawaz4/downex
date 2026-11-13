package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// installTimeout bounds "update install": downexd downloads and verifies the new version
// before it answers.
const installTimeout = time.Hour

const updateSynopsis = `update check [--json]
       downex update install [--json]`

const updateAbout = "Check for a new version of Downex, or install it. downexd reads the signed update\n" +
	"feed of its settings (\"downex settings --update-feed\"); without one, updates are off.\n" +
	"  check    show whether a newer version is available\n" +
	"  install  download, verify and install the newer version; downexd stops for the\n" +
	"           installer (its running downloads are paused and their progress is saved)"

func (a *app) update(ctx context.Context, args []string) error {
	return a.runSub(ctx, "update", updateSynopsis, updateAbout, "check or install", args, "",
		map[string]func(context.Context, []string) error{
			"check":   func(ctx context.Context, args []string) error { return a.updateRun(ctx, args, false) },
			"install": func(ctx context.Context, args []string) error { return a.updateRun(ctx, args, true) },
		})
}

// updateInfoView is the JSON form of UpdateInfo.
type updateInfoView struct {
	CurrentVersion string     `json:"currentVersion"`
	LatestVersion  string     `json:"latestVersion,omitempty"`
	Available      bool       `json:"available"`
	Notes          string     `json:"notes,omitempty"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	CheckedAt      *time.Time `json:"checkedAt,omitempty"`
}

func newUpdateInfoView(u *downexv1.UpdateInfo) updateInfoView {
	v := updateInfoView{
		CurrentVersion: u.GetCurrentVersion(), LatestVersion: u.GetLatestVersion(), Available: u.GetAvailable(),
		Notes: u.GetNotes(),
	}
	if ts := u.GetPublishedAt(); ts != nil {
		t := ts.AsTime()
		v.PublishedAt = &t
	}
	if ts := u.GetCheckedAt(); ts != nil {
		t := ts.AsTime()
		v.CheckedAt = &t
	}
	return v
}

func (a *app) updateRun(ctx context.Context, args []string, install bool) error {
	sub, about := "check", "Show whether a newer version of Downex is available."
	if install {
		sub, about = "install", "Download, verify and install the newer version of Downex; downexd stops for the installer."
	}
	fs := a.flagSet("update "+sub, "update "+sub+" [--json]", about)
	t := a.daemonFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"update", sub + " takes no arguments"}
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	var info *downexv1.UpdateInfo
	if install {
		a.con.printf("Downloading and verifying the update...\n")
		cctx, cancel := context.WithTimeout(ctx, installTimeout)
		defer cancel()
		resp, err := c.Daemon.InstallUpdate(cctx, connect.NewRequest(&downexv1.InstallUpdateRequest{}))
		if err != nil {
			return rpcError("update install", err)
		}
		info = resp.Msg.GetInfo()
	} else {
		cctx, cancel := rpcContext(ctx)
		defer cancel()
		resp, err := c.Daemon.CheckForUpdate(cctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
		if err != nil {
			return rpcError("update check", err)
		}
		info = resp.Msg.GetInfo()
	}
	v := newUpdateInfoView(info)
	if *asJSON {
		return writeJSON(a.stdout, v)
	}
	if install {
		fmt.Fprintf(a.stdout, "Installing Downex %s; downexd stops for the installer.\n", printable(v.LatestVersion))
		return nil
	}
	if !v.Available {
		fmt.Fprintf(a.stdout, "Downex %s is up to date.\n", printable(v.CurrentVersion))
		return nil
	}
	fmt.Fprintf(a.stdout, "Downex %s is available (this is %s)", printable(v.LatestVersion), printable(v.CurrentVersion))
	if v.PublishedAt != nil {
		fmt.Fprintf(a.stdout, ", published %s", v.PublishedAt.Local().Format(time.DateOnly))
	}
	fmt.Fprintln(a.stdout, ".")
	if notes := strings.TrimSpace(v.Notes); notes != "" {
		fmt.Fprintln(a.stdout)
		for line := range strings.SplitSeq(notes, "\n") {
			fmt.Fprintf(a.stdout, "  %s\n", printable(strings.TrimRight(line, "\r")))
		}
		fmt.Fprintln(a.stdout)
	}
	fmt.Fprintf(a.stdout, "Install it with: %s\n", commandLine(t, "update", "install"))
	return nil
}
