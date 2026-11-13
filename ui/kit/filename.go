package kit

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/bidi"
)

// File names as the user reads them (today's FileName.svelte and text.ts splitExtension).
//
// In a name with right-to-left letters the trailing Latin extension is a left-to-right
// run of its own, so a Kurdish or Arabic name ends in ".mp4" instead of "mp4." (the dot
// otherwise takes the right-to-left direction around it). The stem stays plain text, so
// the paragraph still takes its direction from the name's first strong letter (HTML's
// dir=auto): a cut name keeps its start in view and ends in the ellipsis. Other names are
// shown as they are. Bidi controls in the name itself are the caller's to make visible
// first (today's displayName); a label's Text, tooltip and accessible name are always the
// name as given.
//
// Today's markup uses isolates (<bdi>). The text here uses embeddings (U+202A to U+202C)
// instead, because DirectWrite's bidi analysis ignores the isolates of Unicode 6.3: they
// are drawn as if they were not there. An isolate's initiator stays at the level around
// it, which keeps the extension apart from a run of the same level before it (Arabic
// digits, Latin words, or a right-to-left word in a left-to-right name); a mark of the
// name's direction (U+200E or U+200F) before the embedding does the same. For a name that
// is the whole paragraph, or the whole content of its isolate, this orders it as the
// isolates do: its first strong letter comes before the extension, and the neutrals next
// to the extension resolve the same way.

// The explicit embeddings and marks of UAX #9 that file names use.
const (
	lre = "\u202A" // LEFT-TO-RIGHT EMBEDDING: the extension (<bdi dir="ltr">)
	rle = "\u202B" // RIGHT-TO-LEFT EMBEDDING: a right-to-left name in a set direction (<bdi>)
	pdf = "\u202C" // POP DIRECTIONAL FORMATTING
	lrm = "\u200E" // LEFT-TO-RIGHT MARK
	rlm = "\u200F" // RIGHT-TO-LEFT MARK
)

// SplitExtension splits a file name into its stem and a trailing Latin extension of 1 to 10
// ASCII letters and digits after the last dot (".mp4", ".iso"), as text.ts's splitExtension
// does (/^(.+)(\.[A-Za-z0-9]{1,10})$/s). Without such an extension, or when the dot starts
// the name (".gitignore"), the whole name is the stem and ext is "". stem + ext is always
// the name.
func SplitExtension(name string) (stem, ext string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || len(name)-i-1 < 1 || len(name)-i-1 > 10 {
		return name, ""
	}
	for j := i + 1; j < len(name); j++ {
		c := name[j]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			return name, ""
		}
	}
	return name[:i], name[i:]
}

// HasRightToLeft reports whether s has letters of a right-to-left script: Hebrew, Arabic
// (Kurdish and Persian letters included), Syriac, Thaana, N'Ko and the Hebrew and Arabic
// presentation forms (FileName.svelte's /[\u0590-\u08FF\uFB1D-\uFDFF\uFE70-\uFEFF]/).
func HasRightToLeft(s string) bool {
	for _, r := range s {
		if 0x0590 <= r && r <= 0x08FF || 0xFB1D <= r && r <= 0xFDFF || 0xFE70 <= r && r <= 0xFEFF {
			return true
		}
	}
	return false
}

// FileNameText is the text that shows name as FileName.svelte does: a name with
// right-to-left letters and a Latin extension is its stem, a mark of the name's direction
// (its first strong letter's) and the extension in a left-to-right embedding (U+202A …
// U+202C); any other name is itself.
func FileNameText(name string) string {
	if !HasRightToLeft(name) {
		return name
	}
	stem, ext := SplitExtension(name)
	if ext == "" || !endsRightToLeft(stem) {
		return name
	}
	mark := lrm
	if FirstStrong(stem) == DirRTL {
		mark = rlm
	}
	return stem + mark + lre + ext + pdf
}

// endsRightToLeft reports whether the last letter of s is a right-to-left one, digits and
// punctuation skipped (FileName.svelte's endsRightToLeft). Only such a stem needs the
// extension's own run: after a Latin word ("… IMAX.mp4") the extension already joins that
// word's run, and a separate one would move it to the far left (".mp4IMAX").
func endsRightToLeft(s string) bool {
	rs := []rune(s)
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if HasRightToLeft(string(r)) {
			return true
		}
		if unicode.IsLetter(r) {
			return false
		}
	}
	return false
}

// FirstStrong is the direction of s's first strong letter (UAX #9 rules P2 and P3, which
// HTML's dir=auto and <bdi> follow: letters of type L, R or AL, skipping what isolates
// enclose), left to right when it has none, or when the paragraph ends first.
func FirstStrong(s string) Direction {
	depth := 0
	for i := 0; i < len(s); {
		p, size := bidi.LookupString(s[i:])
		if size == 0 {
			size = 1
		}
		i += size
		switch p.Class() {
		case bidi.LRI, bidi.RLI, bidi.FSI:
			depth++
		case bidi.PDI:
			if depth > 0 {
				depth--
			}
		case bidi.B:
			if depth == 0 {
				return DirLTR
			}
		case bidi.L:
			if depth == 0 {
				return DirLTR
			}
		case bidi.R, bidi.AL:
			if depth == 0 {
				return DirRTL
			}
		}
	}
	return DirLTR
}

// NewFileName makes a label that shows a file name by FileNameText's rule. The direction in
// opts says where the name sits:
//
//   - DirAuto: the name is the paragraph and takes the direction of its first strong letter
//     (the list's <span dir="auto">): a right-to-left name starts at the right, and when it
//     is cut its start stays in view and the ellipsis ends the line on the left.
//   - DirLTR or DirRTL: a paragraph in that direction holds the name in an embedding of
//     its first strong letter's direction (the details title's and the progress window's
//     <bdi>): the name keeps its own order and the line its direction and alignment.
//
// The label's Text, its tooltip and its accessible name are name itself.
func NewFileName(name string, font Font, opts TextOptions) *Label {
	l := NewLabel(name, font, opts)
	l.fileName = true
	l.shown = l.fileNameText()
	return l
}

// fileNameText is what a file-name label lays out for its text and direction.
func (l *Label) fileNameText() string { return FileNameLayoutText(l.text, l.opts.Direction) }

// FileNameLayoutText is the text a file-name label (NewFileName) lays out for name in a
// paragraph of direction dir: FileNameText's for DirAuto, else that text in an embedding of
// the name's first strong letter's direction.
func FileNameLayoutText(name string, dir Direction) string {
	s := FileNameText(name)
	if dir == DirAuto {
		return s
	}
	if FirstStrong(name) == DirRTL {
		return rle + s + pdf
	}
	return lre + s + pdf
}
