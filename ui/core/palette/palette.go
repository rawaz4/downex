// Package palette is the command palette's logic (Ctrl+K, DESIGN-SYSTEM.md §3.16), ported from
// the module script of ui/frontend/src/lib/components/CommandPalette.svelte and the command list
// of App.svelte: fuzzy scoring, the rows for a query, highlighting, and every command of the main
// window with what it does in plain words. Pure; the view draws the rows and runs commands by ID.
package palette

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Command is one action of the palette. The view runs it by ID.
type Command struct {
	ID    string
	Label string
	// Group is the heading it is listed under ("Commands", "View", "Go to").
	Group string
	// Icon is the kit's icon name ("" for none).
	Icon string
	// Shortcut is its keyboard shortcut as shown ("Ctrl+N").
	Shortcut string
	// Keywords are more words it is found by.
	Keywords string
	// Description says what it does in plain words ("Stops the 3 active downloads, keeps their
	// progress").
	Description string
	Disabled    bool
	// DisabledReason says why it is disabled ("nothing selected"); it is read with it.
	DisabledReason string
}

// Download is a download the palette can find (DESIGN-SYSTEM.md §3.16).
type Download struct {
	ID int64
	// Name is its display name (bidi-escaped).
	Name string
	// Host is its server without "www." ("" when unknown).
	Host string
	// Kind is its file tile's kind.
	Kind string
	// Tag is a stream's or site's badge ("HLS", "YouTube"); "" for none.
	Tag      string
	Download *downexv1.Download
}

const (
	// RunningGroup heads the running downloads (empty query).
	RunningGroup = "Running downloads"
	// DownloadsGroup heads the downloads a query found.
	DownloadsGroup = "Downloads"
	// MaxRunning is how many running downloads are listed before a query narrows them.
	MaxRunning = 5
)

// FuzzyScore is how well query matches text as a fuzzy subsequence, ignoring case; -1 when it
// does not. Consecutive letters and letters at word starts count more, so "pa" ranks "Pause all"
// above "Open folder at path". An empty query scores 0.
func FuzzyScore(query, text string) float64 {
	q := jsLower(jsTrim(query))
	if q == "" {
		return 0
	}
	t := units(jsLower(text))
	score := 0.0
	ti, run := 0, 0
	var ch [2]uint16
	for _, r := range q {
		if r == ' ' {
			run = 0
			continue
		}
		sub := encodeRune(&ch, r)
		at := indexFrom(t, sub, ti)
		if at < 0 {
			return -1
		}
		wordStart := at == 0 || wordBoundary(t[at-1])
		if at == ti && ti > 0 {
			run++
		} else {
			run = 0
		}
		bonus := 0.0
		if wordStart {
			bonus = 3
		}
		score += 1 + float64(run*2) + bonus - float64(min(3, at-ti))*0.25
		ti = at + 1
	}
	return score
}

// encodeRune is r in UTF-16 code units, in buf.
func encodeRune(buf *[2]uint16, r rune) []uint16 {
	if r1, r2 := utf16.EncodeRune(r); r1 != unicode.ReplacementChar || r2 != unicode.ReplacementChar {
		buf[0], buf[1] = uint16(r1), uint16(r2)
		return buf[:2]
	}
	buf[0] = uint16(r)
	return buf[:1]
}

// wordBoundary is /[\s:/(.-]/ on one code unit: the letter after it starts a word.
func wordBoundary(c uint16) bool {
	switch c {
	case ':', '/', '(', '.', '-':
		return true
	}
	return isJSSpace(rune(c))
}

// bestScore is the best score of query in any of fields (a download's name and server are
// matched one by one, so that letters of one do not borrow from another).
func bestScore(query string, fields ...string) float64 {
	best := -1.0
	for _, f := range fields {
		if f != "" {
			best = max(best, FuzzyScore(query, f))
		}
	}
	return best
}

// describedScore matches a description only when every word of the query starts one of its
// words ("shut" finds "…sleep, shut down…"): a loose subsequence of a sentence would match
// almost anything.
func describedScore(query, description string) float64 {
	if description == "" {
		return -1
	}
	words := strings.FieldsFunc(jsLower(description), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, q := range strings.FieldsFunc(jsLower(jsTrim(query)), isJSSpace) {
		if !slices.ContainsFunc(words, func(w string) bool { return strings.HasPrefix(w, q) }) {
			return -1
		}
	}
	return FuzzyScore(query, description)
}

func commandScore(query string, c *Command) float64 {
	return max(FuzzyScore(query, c.Label+" "+c.Keywords), describedScore(query, c.Description))
}

func cmpScore(a, b float64) int {
	switch {
	case b > a:
		return 1
	case b < a:
		return -1
	}
	return 0
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FilterCommands returns the commands matching query, best first; all of them in their own order
// when the query is empty. An unavailable command never comes first among equal matches.
func FilterCommands(commands []Command, query string) []Command {
	if jsTrim(query) == "" {
		return slices.Clone(commands)
	}
	type scored struct {
		c Command
		i int
		s float64
	}
	var ranked []scored
	for i := range commands {
		if s := commandScore(query, &commands[i]); s >= 0 {
			ranked = append(ranked, scored{commands[i], i, s})
		}
	}
	slices.SortStableFunc(ranked, func(a, b scored) int {
		if c := cmpScore(a.s, b.s); c != 0 {
			return c
		}
		if c := boolInt(a.c.Disabled) - boolInt(b.c.Disabled); c != 0 {
			return c
		}
		return a.i - b.i
	})
	out := make([]Command, len(ranked))
	for i, r := range ranked {
		out[i] = r.c
	}
	return out
}

// EntryType tells a command row from a download row.
type EntryType uint8

const (
	EntryCommand EntryType = iota
	EntryDownload
)

// Entry is a row of the palette: a command, or a download.
type Entry struct {
	Type EntryType
	// Key is "c-<command id>" or "d-<download id>".
	Key   string
	Group string
	// Command is set for EntryCommand, Item for EntryDownload.
	Command *Command
	Item    *Download
}

// IsRunning reports a running download: its row shows its progress, and it is listed with an
// empty query.
func IsRunning(d *downexv1.Download) bool {
	switch d.GetState() {
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return true
	}
	return false
}

func commandEntry(c *Command) Entry {
	return Entry{Type: EntryCommand, Key: "c-" + c.ID, Group: c.Group, Command: c}
}

func downloadEntry(d *Download, group string) Entry {
	return Entry{Type: EntryDownload, Key: "d-" + strconv.FormatInt(d.ID, 10), Group: group, Item: d}
}

// Entries are the rows for query. Empty: the running downloads (at most MaxRunning), then the
// commands in their order. Otherwise commands (label, keywords, description) and downloads
// (name, server) ranked together by their score; on a tie, commands come first. The entries of
// one group then stay together under one heading, the groups in the order of their best match
// (so the best match is still the first row). The entries point into commands and downloads.
func Entries(commands []Command, downloads []Download, query string) []Entry {
	if jsTrim(query) == "" {
		var out []Entry
		for i := range downloads {
			if len(out) == MaxRunning {
				break
			}
			if IsRunning(downloads[i].Download) {
				out = append(out, downloadEntry(&downloads[i], RunningGroup))
			}
		}
		for i := range commands {
			out = append(out, commandEntry(&commands[i]))
		}
		return out
	}
	type scored struct {
		e   Entry
		s   float64
		tie int
		i   int
	}
	var ranked []scored
	for i := range commands {
		if s := commandScore(query, &commands[i]); s >= 0 {
			ranked = append(ranked, scored{commandEntry(&commands[i]), s, 0, i})
		}
	}
	for i := range downloads {
		if s := bestScore(query, downloads[i].Name, downloads[i].Host); s >= 0 {
			ranked = append(ranked, scored{downloadEntry(&downloads[i], DownloadsGroup), s, 1, i})
		}
	}
	// an unavailable command never comes first among equals: Enter on the best match does something
	off := func(x scored) int { return boolInt(x.e.Type == EntryCommand && x.e.Command.Disabled) }
	slices.SortStableFunc(ranked, func(a, b scored) int {
		if c := cmpScore(a.s, b.s); c != 0 {
			return c
		}
		if c := off(a) - off(b); c != 0 {
			return c
		}
		if c := a.tie - b.tie; c != 0 {
			return c
		}
		return a.i - b.i
	})
	rank := map[string]int{}
	for _, r := range ranked {
		if _, ok := rank[r.e.Group]; !ok {
			rank[r.e.Group] = len(rank)
		}
	}
	out := make([]Entry, len(ranked))
	for i, r := range ranked {
		out[i] = r.e
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return rank[a.Group] - rank[b.Group] })
	return out
}

// Part is a run of text that a query matches (Hit) or does not.
type Part struct {
	Text string
	Hit  bool
}

// HighlightParts splits text into runs of letters that a fuzzy query matches and letters it does
// not, for highlighting. No match, or a text whose lower case changes its length: one plain run.
func HighlightParts(query, text string) []Part {
	q := jsLower(jsTrim(query))
	src := units(text)
	t := units(jsLower(text))
	plain := []Part{{Text: text}}
	if q == "" || len(t) != len(src) {
		return plain
	}
	hit := make([]bool, len(src))
	ti := 0
	var ch [2]uint16
	for _, r := range q {
		if r == ' ' {
			continue
		}
		sub := encodeRune(&ch, r)
		at := indexFrom(t, sub, ti)
		if at < 0 {
			return plain
		}
		for k := range sub {
			hit[at+k] = true
		}
		ti = at + len(sub)
	}
	var parts []Part
	start := 0
	for i := 1; i <= len(src); i++ {
		if i == len(src) || hit[i] != hit[start] {
			parts = append(parts, Part{Text: string(utf16.Decode(src[start:i])), Hit: hit[start]})
			start = i
		}
	}
	return parts
}

// Keys is a command's keyboard shortcut as keys: "Ctrl+Shift+P" → Ctrl, Shift, P.
func Keys(shortcut string) []string {
	if shortcut == "+" {
		return []string{"+"}
	}
	var out []string
	for _, k := range strings.Split(shortcut, "+") {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}
