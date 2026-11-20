package filecheck

import (
	"fmt"
	"strings"

	"downex/internal/model"
)

// The texts of docs/DESKTOP-SPEC.md §9.6, quoted exactly there.
const (
	headingDanger  = "This file is not what its name says"
	headingCaution = "This may not be the file you wanted"
	detailDanger   = "Files that pretend to be another type are a common way to spread malware. Don’t rename or run it unless you trust where it came from."
	detailEarly    = "Downex checks it again when the download completes."
	detailListing  = "Downex could not list every file inside it."
)

// Heading is the heading of a result's note: DANGER and CAUTION have one, NOTE none.
func Heading(level model.FileCheckLevel) string {
	switch level {
	case model.FileCheckDanger:
		return headingDanger
	case model.FileCheckCaution:
		return headingCaution
	}
	return ""
}

// LevelWord names a level in the CLI ("Danger", "Caution", "Note"; "" for none).
func LevelWord(level model.FileCheckLevel) string {
	switch level {
	case model.FileCheckDanger:
		return "Danger"
	case model.FileCheckCaution:
		return "Caution"
	case model.FileCheckNote:
		return "Note"
	}
	return ""
}

// ContentLabel names the content of a result without its article ("Windows program";
// "" when unknown).
func ContentLabel(fc *model.FileCheck) string {
	if fc == nil {
		return ""
	}
	return contentLabel(fc.Content, fc.Brand).noun
}

// ClaimLabel names what the name (or the served type) claims, without its article
// ("MP4 video"; "" for no claim).
func ClaimLabel(fc *model.FileCheck) string {
	if fc == nil {
		return ""
	}
	return claimLabel(fc).noun
}

func claimLabel(fc *model.FileCheck) label {
	if fc.Claim != "" {
		return extLabel(fc.Claim)
	}
	if c := servedClaim(fc.Served); c != nil {
		return c.label
	}
	return label{}
}

// Summary is the one or two sentences that say what a result found (§9.6). name is
// the file's name; member and file names are quoted and their bidi controls escaped.
func Summary(fc *model.FileCheck, name string) string {
	if fc == nil {
		return ""
	}
	q := func(s string) string { return "“" + EscapeBidi(s) + "”" }
	content := contentLabel(fc.Content, fc.Brand).full()
	cl := claimLabel(fc)
	claim := cl.full()
	outerExt, innerExt, _, _ := doubleExt(name)
	if outerExt == "" {
		outerExt = lastExt(name)
	}
	outer := extLabel(outerExt).full()
	if outer == "" {
		outer = "a program"
	}
	more := func(s string) string { // " and {n−1} more" before the final period
		if fc.ProgramMembers > 1 {
			return fmt.Sprintf("%s and %d more.", s, fc.ProgramMembers-1)
		}
		return s + "."
	}
	holder := cl.noun
	if holder == "" {
		holder = contentLabel(fc.Content, fc.Brand).noun
	}
	if holder == "" {
		holder = "file"
	}
	switch fc.Reason {
	case reasonProgramDisguised, reasonArchiveDisguised:
		return fmt.Sprintf("This file is %s, not %s.", content, claim)
	case reasonDoubleExtension:
		return fmt.Sprintf("%s is %s whose name imitates %s.", q(name), outer, extLabel(innerExt).full())
	case reasonNamePadding:
		return fmt.Sprintf("%s is %s whose real extension is hidden behind spaces.", q(name), outer)
	case reasonNameHidDirection:
		return fmt.Sprintf("The name this file came with used hidden characters that reverse text, a trick to make %s look like another type of file. Downex removed them; the real name is %s.", outer, q(name))
	case reasonNameHadControls:
		return "The name this file came with contained hidden text-direction characters; Downex removed them."
	case reasonServedAsOther:
		is := content
		if is == "" {
			is = outer
		}
		return fmt.Sprintf("The server said this was %s (%s), but it is %s.", servedLabel(fc.Served, a("file")).full(), fc.Served, is)
	case reasonArchiveWithProgram:
		return more(fmt.Sprintf("This file is %s, not %s, and it holds a program: %s", content, claim, q(fc.Member)))
	case reasonProgramInsideDocument:
		return more(fmt.Sprintf("This %s holds a program: %s", holder, q(fc.Member)))
	case reasonDisguisedMember:
		imitates := "another type of file"
		if _, _, inner := nameReason(memberBase(fc.Member)); inner != "" {
			if l := extLabel(inner).full(); l != "" {
				imitates = l
			}
		}
		return fmt.Sprintf("This archive holds %s, a program whose name imitates %s.", q(fc.Member), imitates)
	case reasonAppendedArchiveProgram:
		return more(fmt.Sprintf("This %s also carries a ZIP archive with a program inside: %s", holder, q(fc.Member)))
	case reasonDiskImageDisguised:
		return fmt.Sprintf("This file is %s, not %s. A disc image can hold programs.", content, claim)
	case reasonWebPage:
		return fmt.Sprintf("This file is a web page, not %s. The server may have sent an error or sign-in page instead of the file; check the address or sign in on the site, then download it again.", claim)
	case reasonTextNotBinary:
		return fmt.Sprintf("This file is plain text, not %s. The server may have sent a message instead of the file.", claim)
	case reasonOtherType, reasonArchiveKind:
		return fmt.Sprintf("This file is %s, though its name says %s.", content, claim)
	case reasonAppendedArchive:
		return fmt.Sprintf("This %s also carries a ZIP archive.", holder)
	}
	return ""
}

// Detail is the line under a summary (§9.6): the DANGER advice, the early hint and the
// incomplete-listing note, as they apply.
func Detail(fc *model.FileCheck) string {
	if fc == nil {
		return ""
	}
	var parts []string
	if fc.Level == model.FileCheckDanger {
		parts = append(parts, detailDanger)
	}
	if fc.ListingIncomplete {
		parts = append(parts, detailListing)
	}
	if fc.Stage == model.FileCheckStageEarly && fc.Level >= model.FileCheckCaution {
		parts = append(parts, detailEarly)
	}
	return strings.Join(parts, " ")
}

// NotificationText is the tray text of a final result (§9.7), "" when it has none.
func NotificationText(fc *model.FileCheck, name string) string {
	if fc == nil {
		return ""
	}
	switch fc.Level {
	case model.FileCheckDanger:
		return EscapeBidi(name) + " is not what its name says."
	case model.FileCheckCaution:
		return EscapeBidi(name) + " may not be the file you wanted."
	}
	return ""
}

// NotificationTitle is the tray title of a final result (§9.7).
const NotificationTitle = "Check this download"

// EscapeBidi replaces each bidi control (and the LRM, RLM and ALM marks) with a
// visible "⟨U+202E⟩" marker, so a name cannot reorder the text around it.
func EscapeBidi(s string) string {
	if strings.IndexFunc(s, bidiMark) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if bidiMark(r) {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func bidiMark(r rune) bool {
	return r == 0x200E || r == 0x200F || r == 0x061C || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}
