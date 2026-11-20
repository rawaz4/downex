package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"downex/internal/filecheck"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
)

// The file check of docs/DESKTOP-SPEC.md §9 in the CLI: the "Contents" and "File check"
// rows of info, the "check file" status of ls, `downex check` and the warning after a
// local download.

// fileCheckView is the JSON form of a file check ("fileCheck" in ls/info --json and
// check --json).
type fileCheckView struct {
	Level             string `json:"level"` // "danger", "caution" or "note"
	Stage             string `json:"stage"` // "name", "early" or "final"
	Reason            string `json:"reason"`
	Content           string `json:"content,omitempty"`
	ContentLabel      string `json:"contentLabel,omitempty"`
	ClaimLabel        string `json:"claimLabel,omitempty"`
	ServedType        string `json:"servedType,omitempty"`
	Member            string `json:"member,omitempty"`
	ProgramMembers    int    `json:"programMembers,omitempty"`
	ListingIncomplete bool   `json:"listingIncomplete,omitempty"`
	Summary           string `json:"summary"`
	Detail            string `json:"detail,omitempty"`
}

func levelName(l model.FileCheckLevel) string {
	switch l {
	case model.FileCheckDanger:
		return "danger"
	case model.FileCheckCaution:
		return "caution"
	case model.FileCheckNote:
		return "note"
	}
	return ""
}

func stageName(s model.FileCheckStage) string {
	switch s {
	case model.FileCheckStageEarly:
		return "early"
	case model.FileCheckStageFinal:
		return "final"
	}
	return "name"
}

// newFileCheckView renders fc for the file named name (nil for none), as downexd does.
func newFileCheckView(fc *model.FileCheck, name string) *fileCheckView {
	if fc == nil || fc.Level == model.FileCheckNone {
		return nil
	}
	return &fileCheckView{
		Level: levelName(fc.Level), Stage: stageName(fc.Stage), Reason: fc.Reason, Content: fc.Content,
		ContentLabel: filecheck.ContentLabel(fc), ClaimLabel: filecheck.ClaimLabel(fc), ServedType: fc.Served,
		Member: fc.Member, ProgramMembers: fc.ProgramMembers, ListingIncomplete: fc.ListingIncomplete,
		Summary: filecheck.Summary(fc, name), Detail: filecheck.Detail(fc),
	}
}

// rpcFileCheckView is the view of a file check downexd reports.
func rpcFileCheckView(p *downexv1.FileCheck) *fileCheckView {
	if p == nil || p.GetLevel() == downexv1.FileCheckLevel_FILE_CHECK_LEVEL_UNSPECIFIED {
		return nil
	}
	stage := model.FileCheckStageName
	switch p.GetStage() {
	case downexv1.FileCheckStage_FILE_CHECK_STAGE_EARLY:
		stage = model.FileCheckStageEarly
	case downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL:
		stage = model.FileCheckStageFinal
	}
	return &fileCheckView{
		Level: levelName(model.FileCheckLevel(p.GetLevel())), Stage: stageName(stage), Reason: p.GetReason(),
		Content: p.GetContent(), ContentLabel: p.GetContentLabel(), ClaimLabel: p.GetClaimLabel(),
		ServedType: p.GetServedType(), Member: p.GetMember(), ProgramMembers: int(p.GetProgramMembers()),
		ListingIncomplete: p.GetListingIncomplete(), Summary: p.GetSummary(), Detail: p.GetDetail(),
	}
}

// warns reports a level the user is warned about (DANGER or CAUTION).
func (v *fileCheckView) warns() bool {
	return v != nil && (v.Level == "danger" || v.Level == "caution")
}

// levelWord is the level as info and check print it.
func (v *fileCheckView) levelWord() string {
	switch v.Level {
	case "danger":
		return filecheck.LevelWord(model.FileCheckDanger)
	case "caution":
		return filecheck.LevelWord(model.FileCheckCaution)
	}
	return filecheck.LevelWord(model.FileCheckNote)
}

// contentsText is the "Contents" row: the content's label, and what the name claims
// when that differs ("" when the content is unknown).
func (v *fileCheckView) contentsText() string {
	if v == nil || v.ContentLabel == "" {
		return ""
	}
	if v.ClaimLabel != "" && v.ClaimLabel != v.ContentLabel {
		return v.ContentLabel + " (named as " + v.ClaimLabel + ")"
	}
	return v.ContentLabel
}

// resultText is the "File check" row: "Danger: <summary>"; the early stage says it
// was seen while downloading.
func (v *fileCheckView) resultText() string {
	word := v.levelWord()
	if v.Stage == "early" {
		word += " (while downloading)"
	}
	return word + ": " + v.Summary
}

// checkFilesOn reports the check_downloaded_files setting of the settings file next to
// the database at dbPath (docs/DESKTOP-SPEC.md §9.9): on when the file or the setting is
// missing or unreadable, as downexd reads it.
func checkFilesOn(dbPath string) bool {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(dbPath), settingsFileName))
	if err != nil {
		return true
	}
	var f struct {
		Desktop *struct {
			CheckFiles *bool `json:"check_files"`
		} `json:"desktop"`
	}
	if json.Unmarshal(data, &f) != nil || f.Desktop == nil || f.Desktop.CheckFiles == nil {
		return true
	}
	return *f.Desktop.CheckFiles
}

// settingsFileName is downexd's settings file next to the database (daemon.SettingsFileName).
const settingsFileName = "settings.json"

// checkReport is the JSON form of `downex check`.
type checkReport struct {
	Path      string         `json:"path"`
	ID        int64          `json:"id,omitempty"`
	Contents  string         `json:"contents"`             // "" = unknown
	NamedAs   string         `json:"namedAs,omitempty"`    // what the name claims
	Served    string         `json:"serverSaid,omitempty"` // the served type (a download only)
	FileCheck *fileCheckView `json:"fileCheck"`            // null = no warning
}

func (a *app) check(ctx context.Context, args []string) (err error) {
	flags := a.flagSet("check", "check <file|id> [--json]",
		"Check whether a file is what its name says (docs/DESKTOP-SPEC.md §9): a program named like\n"+
			"a video, a web page instead of the file, an archive that holds a disguised program. With a\n"+
			"download id, the completed download's file is checked with what its server said. It reads\n"+
			"only, never starts downexd and never changes or stores anything.")
	db := a.dbFlag(flags)
	asJSON := flags.Bool("json", false, "print JSON instead of text")
	pos, err := parseArgs(flags, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		return usageError{"check", "expects exactly one file or download id"}
	}
	rep := checkReport{Path: pos[0]}
	name, controls, served := filepath.Base(pos[0]), false, ""
	if id, perr := strconv.ParseInt(pos[0], 10, 64); perr == nil && id > 0 && !fileExists(pos[0]) {
		d, err := a.completedDownload(ctx, *db, model.DownloadID(id))
		if err != nil {
			return err
		}
		rep.ID, rep.Path = id, filepath.Join(d.Dir, d.Filename)
		name, controls, served = d.Filename, d.NameControls, d.ServedType
		rep.Served = d.ServedType
	} else if abs, err := filepath.Abs(pos[0]); err == nil {
		rep.Path = abs
	}
	r, err := filecheck.InspectPath(ctx, rep.Path, name, controls, served)
	if err != nil {
		return fmt.Errorf("check %s: %w", printable(rep.Path), err)
	}
	rep.Contents, rep.NamedAs = r.ContentLabel, r.ClaimLabel
	rep.FileCheck = newFileCheckView(r.Result, name)
	if *asJSON {
		return writeJSON(a.stdout, rep)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s:\t%s\n", k, printable(v)) }
	row("File", rep.Path)
	row("Contents", orText(rep.Contents, "unknown"))
	row("Named as", orText(rep.NamedAs, "nothing in particular"))
	if rep.ID != 0 {
		row("Server said", orText(rep.Served, "nothing in particular"))
	}
	if v := rep.FileCheck; v != nil {
		row("Result", v.levelWord()+": "+v.Summary)
		if v.Detail != "" {
			row("", v.Detail)
		}
	} else {
		row("Result", "no warning")
	}
	return tw.Flush()
}

// completedDownload reads download id from the database (never through downexd) and
// requires it to be a completed file download.
func (a *app) completedDownload(ctx context.Context, db string, id model.DownloadID) (d *model.Download, err error) {
	st, _, err := openStore(db)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeStore(st)) }()
	if d, err = st.GetDownload(ctx, id); err != nil {
		return nil, lookupError(id, err)
	}
	if d.State != model.StateCompleted {
		return nil, fmt.Errorf("download %d is not completed (%s); check its file once it is", id, stateName(d.State))
	}
	return d, nil
}

// checkCompleted runs the final file check on download d, which this process has just
// completed, stores it and prints a warning for DANGER and CAUTION (docs/DESKTOP-SPEC.md
// §9.7). It never fails the download: problems are warnings.
func (a *app) checkCompleted(ctx context.Context, st store.Store, d *model.Download, dbPath string) {
	if d.Kind != model.KindFile || !checkFilesOn(dbPath) {
		return
	}
	fc, err := filecheck.CheckPath(ctx, filepath.Join(d.Dir, d.Filename), d.Filename, d.NameControls, d.ServedType)
	if err != nil {
		a.con.printf("warning: cannot check the downloaded file: %v\n", err)
		fc = nil
	}
	if fc != nil {
		fc.CheckedAt = time.Now()
	}
	if err := st.SetFileCheck(ctx, d.ID, fc); err != nil {
		a.con.printf("warning: cannot store the file check: %v\n", err)
	}
	if v := newFileCheckView(fc, d.Filename); v.warns() {
		a.con.printf("Warning: %s\n", printable(v.Summary))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
