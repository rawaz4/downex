package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"downex/internal/dlock"
	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// renewTimeout bounds a renewal: the proof sends at most 8 requests of at most 10 s each
// (docs/ENGINE-SPEC.md §7.3).
const renewTimeout = 2 * time.Minute

// The refusal codes of RenewLink the CLI words itself (docs/DAEMON-SPEC.md §6).
const (
	codeRenewName     = "renew_name"
	codeRenewUnproven = "renew_unproven"
	codeLinkExpired   = "link_expired"
)

// renew gives a stopped download a new link (docs/DESKTOP-SPEC.md §10, CLI).
func (a *app) renew(ctx context.Context, args []string) error {
	fs := a.flagSet("renew", "renew <id> <url> [flags]",
		"Give a stopped download a new link, for example after its link expired. The new link\n"+
			"must serve the same file: downexd first compares the bytes the new server sends with\n"+
			"those already downloaded (and its size and version), then continues the download from\n"+
			"where it stopped. Nothing changes when they differ.\n\n"+
			"Without -H the stored headers are sent (without cookies and logins when the new link is\n"+
			"on another site). With --local the download is renewed and run in this process, as with\n"+
			"\"resume --local\" (only while downexd is not running).")
	t := a.daemonFlags(fs)
	var (
		headers headerFlag
		netf    netFlags
		nameOK  bool
		restart bool
		later   bool
		wait    bool
		local   bool
	)
	netf.register(fs)
	fs.Var(&headers, "H", "request header \"Name: value\" to send with the new link (repeatable)")
	fs.BoolVar(&nameOK, "name-ok", false, "continue although the new link names the file differently")
	fs.BoolVar(&restart, "restart-if-needed", false, "when too little can be compared, download it again from the start")
	fs.BoolVar(&later, "later", false, "keep the download paused after the renewal instead of queueing it")
	fs.BoolVar(&wait, "wait", false, "follow the download's progress until it completes")
	fs.BoolVar(&local, "local", false, "renew and run the download in this process without downexd")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 2 {
		return usageError{"renew", "expects a download id and the new link"}
	}
	id, err := parseID("renew", pos[:1])
	if err != nil {
		return err
	}
	rawURL := pos[1]
	if err := validate.URL(rawURL); err != nil {
		return usageError{"renew", "the new link: " + err.Error()}
	}
	if later && wait {
		return usageError{"renew", "--later and --wait cannot be combined"}
	}
	if local {
		if wait {
			return usageError{"renew", "--wait cannot be combined with --local, which always runs in the foreground"}
		}
		if err := a.refuseWhileDaemonRuns(ctx, t, "renew", id); err != nil {
			return err
		}
		return a.renewLocal(ctx, id, rawURL, headers.h, netf, t.db, renewFlags{nameOK, restart, later})
	}
	if visited(fs)["proxy"] || visited(fs)["v"] {
		return usageError{"renew", "--proxy and -v apply only with --local (downexd uses its own settings)"}
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, renewTimeout)
	defer cancel()
	resp, err := c.Downloads.RenewLink(cctx, connect.NewRequest(&downexv1.RenewLinkRequest{
		Id: int64(id), Link: &downexv1.RenewLinkRequest_Url{Url: rawURL}, Headers: rpcHeaders(headers.h),
		AllowNameChange: nameOK, RestartIfUnproven: restart, Start: !later,
	}))
	if err != nil {
		return a.renewRefused(id, err)
	}
	d := resp.Msg.GetDownload()
	a.printRenewed(id, resp.Msg.GetRestarted(), rpcSize(d), d.GetReceived())
	if later {
		fmt.Fprintf(a.stdout, "It stays paused. Resume it with: %s\n", commandLine(t, "resume", idArg(int64(id))))
	}
	if !wait {
		return nil
	}
	return a.follow(ctx, c, t, d)
}

// renewFlags are renew's choices.
type renewFlags struct{ nameOK, restart, later bool }

// printRenewed says how download id continues after a renewal.
func (a *app) printRenewed(id model.DownloadID, restarted bool, size, kept int64) {
	if restarted {
		fmt.Fprintf(a.stdout, "Download %d downloads again from the start with the new link.\n", id)
		return
	}
	fmt.Fprintf(a.stdout, "Download %d continues with the new link (%s kept).\n", id, keptText(size, kept))
}

// keptText is "4.2 MB of 9.9 MB": the bytes a download keeps of its size.
func keptText(size, kept int64) string {
	if size < 0 {
		return formatBytes(kept)
	}
	return formatBytes(kept) + " of " + formatBytes(size)
}

// renewRefused words a refused renewal of download id.
func (a *app) renewRefused(id model.DownloadID, err error) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return downloadRPCError("renew", int64(id), err)
	}
	code := ce.Meta().Get("Downex-Error-Code")
	msg := strings.TrimPrefix(ce.Message(), code+": ")
	switch code {
	case codeRenewName:
		a.con.printf("downex: error: %s. Run again with --name-ok to continue anyway.\n", msg)
		return silentError{err}
	case codeRenewUnproven:
		a.con.printf("downex: error: too little of download %d can be compared with the new link. Run again with "+
			"--restart-if-needed to download it from the start.\n", id)
		return silentError{err}
	case "":
		return downloadRPCError("renew", int64(id), err)
	}
	return &daemonError{msg: msg, err: err}
}

// renewLocal renews download id in the database of dbFlag without downexd and runs it
// (unless later), as "resume --local" does.
func (a *app) renewLocal(ctx context.Context, id model.DownloadID, rawURL string, h http.Header, netf netFlags,
	dbFlag string, f renewFlags) (err error) {
	st, dbPath, err := openStore(dbFlag)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()
	eng, err := a.newEngine(st, netf)
	if err != nil {
		return err
	}
	lock, err := dlock.Acquire(dlock.Dir(dbPath), id)
	if err != nil {
		return err
	}
	d, err := st.GetDownload(ctx, id)
	if err != nil {
		lock.Release()
		return lookupError(id, err)
	}
	if h == nil {
		h = engine.RenewHeaders(d.Request, rawURL)
	}
	pctx, cancel := context.WithTimeout(ctx, renewTimeout)
	defer cancel()
	p, err := eng.ProveLink(pctx, id, model.Request{URL: rawURL, Method: http.MethodGet, Headers: h},
		engine.ProveOptions{RestartIfUnproven: f.restart})
	if err == nil && p.NameDiffers && !f.nameOK {
		err = &engine.NameError{ID: id, New: p.RemoteName, Old: d.Filename}
	}
	if err == nil {
		err = eng.Renew(pctx, id, p, false)
	}
	if err != nil {
		lock.Release()
		return a.localRenewRefused(id, err)
	}
	segs, _ := st.LoadSegments(ctx, id)
	rd, _ := st.GetDownload(ctx, id)
	if rd != nil {
		d = rd
	}
	a.printRenewed(id, p.Fresh, d.Size, dlview.Received(d, segs))
	lock.Release() // download takes it again
	if f.later {
		fmt.Fprintf(a.stdout, "It stays paused. Resume it with: %s\n", resumeCommand(id, dbFlag, "--local"))
		return nil
	}
	return a.download(ctx, st, eng, id, runOptions{dbPath: dbPath, dbFlag: dbFlag})
}

// localRenewRefused words a refused local renewal like renewRefused.
func (a *app) localRenewRefused(id model.DownloadID, err error) error {
	var name *engine.NameError
	switch {
	case errors.As(err, &name):
		a.con.printf("downex: error: %s. Run again with --name-ok to continue anyway.\n", name.Error())
		return silentError{err}
	case errors.Is(err, engine.ErrLinkUnproven):
		a.con.printf("downex: error: too little of download %d can be compared with the new link. Run again with "+
			"--restart-if-needed to download it from the start.\n", id)
		return silentError{err}
	}
	return err
}

// linkExpired reports a failed download whose link expired (docs/ENGINE-SPEC.md §7.2):
// its error code, else (a record read without downexd) its stored message.
func linkExpired(state, code, msg string) bool {
	if state != stateName(model.StateFailed) {
		return false
	}
	return code == codeLinkExpired || (code == "" && strings.Contains(msg, engine.ErrLinkExpired.Error()))
}
