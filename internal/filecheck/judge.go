package filecheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"downex/internal/model"
)

// Reason codes (§9.9): stable machine codes, [a-z_]{1,64}.
const (
	reasonProgramDisguised       = "program_disguised"
	reasonDoubleExtension        = "double_extension"
	reasonNamePadding            = "name_padding"
	reasonNameHidDirection       = "name_hid_direction"
	reasonNameHadControls        = "name_had_controls"
	reasonServedAsOther          = "served_as_other"
	reasonArchiveWithProgram     = "archive_with_program"
	reasonProgramInsideDocument  = "program_inside_document"
	reasonDisguisedMember        = "disguised_member"
	reasonAppendedArchiveProgram = "appended_archive_with_program"
	reasonAppendedArchive        = "appended_archive"
	reasonDiskImageDisguised     = "disk_image_disguised"
	reasonWebPage                = "web_page"
	reasonTextNotBinary          = "text_not_binary"
	reasonArchiveDisguised       = "archive_disguised"
	reasonArchiveKind            = "archive_kind"
	reasonOtherType              = "other_type"
)

// Reasons lists every reason code (§9.9).
func Reasons() []string {
	return []string{
		reasonProgramDisguised, reasonDoubleExtension, reasonNamePadding, reasonNameHidDirection,
		reasonNameHadControls, reasonServedAsOther, reasonArchiveWithProgram, reasonProgramInsideDocument,
		reasonDisguisedMember, reasonAppendedArchiveProgram, reasonAppendedArchive, reasonDiskImageDisguised,
		reasonWebPage, reasonTextNotBinary, reasonArchiveDisguised, reasonArchiveKind, reasonOtherType,
	}
}

// Timeout bounds a final check (§9.3), checked between reads.
const Timeout = 30 * time.Second

// Rule order for ties between findings of the same level (§9.5 combining).
const (
	orderN1 = iota
	orderN2
	orderN3
	orderN4
	orderR1
	orderR2
	orderR3
	orderR4
	orderR5
	orderR6
	orderR7
	orderA1
	orderA2
	orderA3
)

type finding struct {
	level  model.FileCheckLevel
	order  int
	reason string
	member *listing // the listing whose member and program count the result reports
}

// input is everything one check judges.
type input struct {
	name     string
	controls bool
	served   string // media type, lower case
	stage    model.FileCheckStage
	content  Content
	members  *listing // the archive's own listing (final stage)
	appended *listing // a ZIP found at the end of another file (final stage)
	unlisted bool     // an archive format that is never listed (§9.3)
}

// CheckName is the NAME stage (§9.1): the name rules N1–N4 only. served is the served
// Content-Type, or the requester's MIME hint when nothing was served yet. nil = nothing
// to say.
func CheckName(name string, nameControls bool, served string) *model.FileCheck {
	return judge(input{name: name, controls: nameControls, served: normType(served), stage: model.FileCheckStageName})
}

// CheckHead is the EARLY stage (§9.1): the name rules and the content rules on the
// first bytes of the file (head shorter than HeadBytes = the whole file).
func CheckHead(name string, nameControls bool, served string, head []byte) *model.FileCheck {
	return judge(input{name: name, controls: nameControls, served: normType(served),
		stage: model.FileCheckStageEarly, content: Sniff(head)})
}

// CheckFile is the FINAL stage (§9.1) on the size bytes of r: head, tail and the
// archive listings within the bounds of §9.3. A read error before the head is read
// returns the error and no result; any later error, ctx's end or a bound judges what
// was found so far.
func CheckFile(ctx context.Context, r io.ReaderAt, size int64, name string, nameControls bool, served string) (*model.FileCheck, error) {
	rep, err := InspectFile(ctx, r, size, name, nameControls, served)
	return rep.Result, err
}

// Report is a final check with what it found when there is nothing to say: what
// `downex check` prints.
type Report struct {
	Result       *model.FileCheck // nil = nothing to say
	ContentLabel string           // what the bytes say ("" = unknown)
	ClaimLabel   string           // what the name (else the served type) says ("" = nothing)
}

// InspectFile is CheckFile with the content and claim labels of the file even when the
// check has nothing to say.
func InspectFile(ctx context.Context, r io.ReaderAt, size int64, name string, nameControls bool, served string) (Report, error) {
	in, err := inspect(ctx, r, size, name, nameControls, served)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Result: judge(in)}
	if in.content.Strong {
		rep.ContentLabel = contentLabel(in.content.ID, in.content.Brand).noun
		if in.content.DLL {
			rep.ContentLabel = contentLabel("pe", "dll").noun
		}
	}
	if cl := claims[lastExt(name)]; cl != nil {
		rep.ClaimLabel = cl.label.noun
	} else if cl := servedClaim(in.served); cl != nil {
		rep.ClaimLabel = cl.label.noun
	}
	return rep, nil
}

func inspect(ctx context.Context, r io.ReaderAt, size int64, name string, nameControls bool, served string) (input, error) {
	in := input{name: name, controls: nameControls, served: normType(served), stage: model.FileCheckStageFinal}
	head := make([]byte, min(max(size, 0), HeadBytes))
	if len(head) > 0 {
		if err := ctx.Err(); err != nil {
			return in, err
		}
		if n, err := r.ReadAt(head, 0); n < len(head) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return in, fmt.Errorf("filecheck: read head: %w", err)
		}
	}
	whole := size <= HeadBytes
	in.content = sniff(head, whole)
	tail := head
	if !whole && ctx.Err() == nil {
		t := make([]byte, min(size, TailBytes))
		if readFull(r, t, size-int64(len(t))) {
			tail = t
		} else {
			tail = nil
		}
	}
	if (in.content.ID == "" || !in.content.Strong) && sniffTail(tail) == "vhd" {
		in.content = strong("vhd")
	}
	switch in.content.ID {
	case "ole":
		in.content.ID = cfbKindAt(r, size, head)
	case "zip":
		if d, ok := findZipDir(ctx, r, size, tail); ok {
			in.members = listZip(ctx, r, d)
			in.content.Brand = in.members.kind()
			if in.content.Brand == "epub" {
				in.members.bookScripts()
			}
		} else {
			in.members = newListing()
			in.members.incomplete = true
		}
	case "rar5":
		in.members = listRAR5(ctx, r, size)
	case "cab":
		in.members = listCAB(ctx, r, size, head)
	case "rar4", "7z", "gzip", "xz", "bzip2", "zstd", "tar":
		in.unlisted = true
	case "pdf", "jpeg", "png", "gif", "isobmff":
		if d, ok := findZipDir(ctx, r, size, tail); ok && d.entries > 0 {
			in.appended = listZip(ctx, r, d)
		}
	}
	return in, nil
}

// CheckPath opens path read-only and runs CheckFile on it, within Timeout.
func CheckPath(ctx context.Context, path, name string, nameControls bool, served string) (*model.FileCheck, error) {
	rep, err := InspectPath(ctx, path, name, nameControls, served)
	return rep.Result, err
}

// InspectPath opens path read-only and runs InspectFile on it, within Timeout.
func InspectPath(ctx context.Context, path, name string, nameControls bool, served string) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	f, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Report{}, err
	}
	if !fi.Mode().IsRegular() {
		return Report{}, errors.New("filecheck: not a regular file") // no name: logs never carry one (§9.1 F1)
	}
	return InspectFile(ctx, f, fi.Size(), name, nameControls, served)
}

// Shown is the result clients show for download d (§9.1): a completed download's
// stored final result only; otherwise early (the current run's EARLY result), else the
// NAME stage of its name with the served type (or the requester's MIME hint). Media
// downloads, whose file Downex assembles itself, get the NAME stage only. nil = nothing
// to say. Callers show nothing while check_downloaded_files is off.
func Shown(d *model.Download, early *model.FileCheck) *model.FileCheck {
	if d.State == model.StateCompleted {
		return d.FileCheck
	}
	if early != nil && d.Kind == model.KindFile {
		return early
	}
	served := d.ServedType
	if served == "" {
		served = d.MIME
	}
	return CheckName(d.Filename, d.NameControls, served)
}

// judge applies the verdict rules of §9.5 and combines them.
func judge(in input) *model.FileCheck {
	var fs []finding
	add := func(level model.FileCheckLevel, order int, reason string, l *listing) {
		fs = append(fs, finding{level: level, order: order, reason: reason, member: l})
	}
	outer := lastExt(in.name)
	key := contentKey(in.content.ID, in.content.Brand)
	cfam := contentFamily(in.content.ID, in.content.Brand)
	known := in.content.ID != "" && in.content.Strong

	cl := claims[outer]
	if cl == nil {
		cl = servedClaim(in.served)
	}
	accepted := cl != nil && known && cl.accepted(key)

	// Name rules (all stages).
	if in.controls {
		if runExts[outer] {
			add(model.FileCheckDanger, orderN1, reasonNameHidDirection, nil)
		} else {
			add(model.FileCheckNote, orderN1, reasonNameHadControls, nil)
		}
	}
	switch r, _, _ := nameReason(in.name); r {
	case reasonDoubleExtension:
		add(model.FileCheckDanger, orderN2, r, nil)
	case reasonNamePadding:
		order := orderN3
		if _, _, padded, ok := doubleExt(in.name); ok && padded {
			order = orderN2
		}
		add(model.FileCheckDanger, order, r, nil)
	}
	if af := alarmFamily(in.served); runExts[outer] && af != famNone {
		// An .exe that really is an MP4 is not served as something else (R7 says so).
		if in.stage == model.FileCheckStageName || !known || cfam != af {
			add(model.FileCheckDanger, orderN4, reasonServedAsOther, nil)
		}
	}

	// Content rules (early and final), when the claim does not accept the content.
	if cl != nil && known && !accepted {
		cf := cl.fam
		switch {
		case cfam == famProgram && cf != famProgram:
			add(model.FileCheckDanger, orderR1, reasonProgramDisguised, nil)
		case cfam == famScript && in1(cf, famMedia, famImage, famDocument, famArchive, famDisk):
			add(model.FileCheckDanger, orderR2, reasonProgramDisguised, nil)
		case cfam == famDisk && in1(cf, famMedia, famImage, famDocument, famText, famWeb):
			add(model.FileCheckDanger, orderR3, reasonDiskImageDisguised, nil)
		case cfam == famDisk && cf == famArchive:
			add(model.FileCheckCaution, orderR3, reasonArchiveDisguised, nil)
		case cfam == famArchive && in1(cf, famMedia, famImage, famDocument):
			if in.members != nil && in.members.programs > 0 {
				add(model.FileCheckDanger, orderR4, reasonArchiveWithProgram, in.members)
			} else {
				add(model.FileCheckCaution, orderR4, reasonArchiveDisguised, in.members)
			}
		case cfam == famArchive && in1(cf, famText, famWeb):
			add(model.FileCheckNote, orderR4, reasonOtherType, nil)
		case cfam == famArchive:
			add(model.FileCheckNote, orderR4, reasonArchiveKind, nil)
		case cfam == famWeb && in1(cf, famMedia, famImage, famDocument, famArchive, famDisk, famProgram):
			add(model.FileCheckCaution, orderR5, reasonWebPage, nil)
		case cfam == famText && (in1(cf, famMedia, famImage, famDocument, famArchive, famDisk) || cl.binProg):
			add(model.FileCheckCaution, orderR6, reasonTextNotBinary, nil)
		default:
			add(model.FileCheckNote, orderR7, reasonOtherType, nil)
		}
	}

	// Archive rules (final).
	if accepted && in.members != nil {
		if cl.zipDoc && in.members.programs > 0 {
			add(model.FileCheckDanger, orderA1, reasonProgramInsideDocument, in.members)
		}
		if cl.plain && in.members.firstDisguised != "" {
			add(model.FileCheckDanger, orderA2, reasonDisguisedMember, in.members)
		}
	}
	if in.appended != nil {
		if in.appended.programs > 0 {
			add(model.FileCheckDanger, orderA3, reasonAppendedArchiveProgram, in.appended)
		} else {
			add(model.FileCheckNote, orderA3, reasonAppendedArchive, in.appended)
		}
	}

	if len(fs) == 0 {
		return nil
	}
	best := fs[0]
	for _, f := range fs[1:] {
		if f.level > best.level || f.level == best.level && f.order < best.order {
			best = f
		}
	}
	fc := &model.FileCheck{
		Level: best.level, Stage: in.stage, Reason: best.reason,
		Content: in.content.ID, Brand: in.content.Brand, Served: in.served,
	}
	if !known {
		fc.Content, fc.Brand = "", ""
	}
	if in.content.DLL {
		fc.Brand = "dll"
	}
	if cl != nil {
		fc.Claim = cl.ext
	}
	if l := best.member; l != nil {
		fc.Member = l.member()
		fc.ProgramMembers = l.programs
		fc.ListingIncomplete = l.incomplete
	}
	if best.reason == reasonArchiveDisguised && (in.unlisted || in.members != nil && in.members.incomplete) {
		fc.ListingIncomplete = true
	}
	return fc
}

func in1(f family, set ...family) bool {
	for _, s := range set {
		if f == s {
			return true
		}
	}
	return false
}
