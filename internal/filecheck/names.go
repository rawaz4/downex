package filecheck

import (
	"strings"
	"unicode/utf8"
)

// isFiller reports the characters that pad a name so its real extension is pushed out
// of view (§9.5 N2/N3).
func isFiller(r rune) bool {
	switch {
	case r == 0x20, r == 0xA0, r >= 0x2000 && r <= 0x200A, r == 0x202F, r == 0x205F,
		r == 0x3000, r == 0x3164, r == 0x2800:
		return true
	}
	return false
}

// HasHiddenControls reports the characters rule N1 is about in name (§9.5): bidi
// embeddings, overrides and isolates, which sanitising removes from a file name.
func HasHiddenControls(name string) bool { return hasBidi(name) }

// hasBidi reports bidirectional embeddings, overrides and isolates (U+202A–U+202E,
// U+2066–U+2069); the marks U+200E, U+200F and U+061C do not count (§9.5 N1).
func hasBidi(s string) bool {
	for _, r := range s {
		if r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 {
			return true
		}
	}
	return false
}

// doubleExt splits name into its last extension (outer) and the token before it
// (inner), the inner one with trailing whitespace and filler trimmed; padded reports
// that filler was trimmed. ok is false when the name has no two extensions.
func doubleExt(name string) (outer, inner string, padded, ok bool) {
	n := strings.TrimRight(name, ". ")
	i := strings.LastIndexByte(n, '.')
	if i <= 0 {
		return "", "", false, false
	}
	outer = strings.ToLower(n[i+1:])
	stem := n[:i]
	trimmed := strings.TrimRightFunc(stem, isFiller)
	padded = len(trimmed) != len(stem)
	j := strings.LastIndexByte(trimmed, '.')
	if j < 0 || j == len(trimmed)-1 {
		return outer, "", padded, false
	}
	return outer, strings.ToLower(trimmed[j+1:]), padded, true
}

// fillerRun reports a run of at least three filler characters.
func fillerRun(name string) bool {
	run := 0
	for _, r := range name {
		if isFiller(r) {
			if run++; run >= 3 {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// nameReason is the strongest name-only rule a name breaks (N2/N3), "" for none. It
// judges archive members too.
func nameReason(name string) (reason string, outer, inner string) {
	o, in, padded, ok := doubleExt(name)
	if !runExts[o] {
		return "", o, ""
	}
	if ok && decoyExts[in] {
		if padded {
			return reasonNamePadding, o, in
		}
		return reasonDoubleExtension, o, in
	}
	if fillerRun(name) {
		return reasonNamePadding, o, ""
	}
	return "", o, ""
}

// memberBase is the last component of an archive member's path.
func memberBase(path string) string {
	path = strings.TrimRight(path, "/")
	return path[strings.LastIndexByte(path, '/')+1:]
}

// memberProgram reports an archive member the system runs when it is opened: its
// extension is a run extension, or it is inside a Mac app bundle's MacOS folder (§9.3).
func memberProgram(path string) bool {
	if runExts[lastExt(memberBase(path))] {
		return true
	}
	lower := strings.ToLower(path)
	i := strings.Index(lower, ".app/contents/macos/")
	return i > 0 && i+len(".app/contents/macos/") < len(lower)
}

// memberDisguised: a program member whose name hides what it is (§9.3).
func memberDisguised(path string) bool {
	base := memberBase(path)
	if r, _, _ := nameReason(base); r != "" {
		return true
	}
	return hasBidi(path)
}

// maxMemberBytes bounds the member name kept in a result (§9.3).
const maxMemberBytes = 255

func clipName(s string) string {
	if len(s) <= maxMemberBytes {
		return s
	}
	n := maxMemberBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// listing is what an archive's member names say (§9.3).
type listing struct {
	entries        int
	programs       int
	firstProgram   string
	firstDisguised string
	incomplete     bool
	marks          map[string]bool
	// webScripts counts the program members that are plain ".js" files (not disguised, not
	// in an app bundle) and firstOther is the first program member that is not one: an
	// e-book's reader runs its own scripts, the system never does (see bookScripts).
	webScripts int
	firstOther string
}

func newListing() *listing { return &listing{marks: map[string]bool{}} }

// add records one member name (UTF-8, "/" separated).
func (l *listing) add(name string) {
	name = strings.ReplaceAll(name, `\`, "/")
	l.entries++
	l.mark(name)
	if strings.HasSuffix(name, "/") || !memberProgram(name) {
		return
	}
	l.programs++
	if l.firstProgram == "" {
		l.firstProgram = clipName(name)
	}
	disguised := memberDisguised(name)
	if l.firstDisguised == "" && disguised {
		l.firstDisguised = clipName(name)
	}
	if !disguised && lastExt(memberBase(name)) == "js" && !strings.Contains(strings.ToLower(name), ".app/contents/macos/") {
		l.webScripts++
	} else if l.firstOther == "" {
		l.firstOther = clipName(name)
	}
}

// bookScripts drops the plain ".js" members from the programs of an EPUB e-book (§9.3):
// EPUB 3 content documents may carry JavaScript that the reading app runs, which the
// system never opens as a program.
func (l *listing) bookScripts() {
	if l.webScripts == 0 {
		return
	}
	l.programs -= l.webScripts
	l.firstProgram = l.firstOther
	l.webScripts = 0
}

func (l *listing) mark(name string) {
	lower := strings.ToLower(strings.TrimPrefix(name, "/"))
	root := !strings.Contains(strings.TrimSuffix(lower, "/"), "/")
	switch {
	case lower == "[content_types].xml":
		l.marks["ooxml"] = true
	case lower == "content.xml":
		l.marks["odf-content"] = true
	case lower == "meta-inf/manifest.xml":
		l.marks["odf-manifest"] = true
	case lower == "meta-inf/container.xml":
		l.marks["epub"] = true
	case lower == "androidmanifest.xml":
		l.marks["apk"] = true
	case lower == "meta-inf/manifest.mf":
		l.marks["jar"] = true
	case lower == "appxmanifest.xml", strings.HasPrefix(lower, "appxmetadata/"):
		l.marks["appx"] = true
	case lower == "manifest.json":
		l.marks["xpi"] = true
	case lower == "extension.vsixmanifest":
		l.marks["vsix"] = true
	case root && strings.HasSuffix(lower, ".nuspec"):
		l.marks["nupkg"] = true
	}
	if first, rest, ok := strings.Cut(lower, "/"); ok {
		if strings.HasSuffix(first, ".dist-info") {
			l.marks["whl"] = true
		}
		if first == "payload" {
			if app, _, ok := strings.Cut(rest, "/"); ok && strings.HasSuffix(app, ".app") {
				l.marks["ipa"] = true
			}
		}
	}
}

// kind is the container kind of a ZIP (§9.3); "" = a plain archive. Program
// containers are checked first: several of them are Open Packaging files that also
// carry "[Content_Types].xml".
func (l *listing) kind() string {
	m := l.marks
	switch {
	case m["apk"]:
		return "apk"
	case m["appx"]:
		return "appx"
	case m["vsix"]:
		return "vsix"
	case m["nupkg"]:
		return "nupkg"
	case m["ipa"]:
		return "ipa"
	case m["whl"]:
		return "whl"
	case m["jar"]:
		return "jar"
	case m["xpi"]:
		return "xpi"
	case m["ooxml"]:
		return "ooxml"
	case m["odf-content"] && m["odf-manifest"]:
		return "odf"
	case m["epub"]:
		return "epub"
	}
	return ""
}

// member is the member a result names: the first disguised one, else the first program.
func (l *listing) member() string {
	if l.firstDisguised != "" {
		return l.firstDisguised
	}
	return l.firstProgram
}
