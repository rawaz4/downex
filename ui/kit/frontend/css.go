// Package frontend reads what the native window mirrors from the WebView window's sources
// (ui/frontend/src): app.css's tokens, the Svelte components' styles and the SVG of their
// icons. The generators of ui/kit/tokens and ui/kit/icons use it, and so do the drift tests
// of those packages and of the views, which read the sources again and compare them with
// the Go code. It is a build and test tool: no product code imports it (TestNotInProduct).
//
// The parsers read the subset of CSS, Svelte and TypeScript those files use and fail with
// the file and line of anything else, so a change the native side cannot follow stops the
// generator instead of being dropped.
package frontend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir is the frontend's source folder, ui/frontend/src, found from the working directory
// (a package folder under ui/ when go generate and go test run).
func Dir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; {
		if fi, err := os.Stat(filepath.Join(d, "ui", "frontend", "src", "app.css")); err == nil && !fi.IsDir() {
			return filepath.Join(d, "ui", "frontend", "src"), nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("frontend: ui/frontend/src/app.css not found above %s", wd)
		}
		d = parent
	}
}

// Decl is one CSS declaration.
type Decl struct {
	Prop, Value string
	Line        int
}

// Rule is one CSS rule with the @media query it sits in ("" for none).
type Rule struct {
	Media     string
	Selectors []string
	Decls     []Decl
	Line      int
}

// Get is the value of the last declaration of prop in r, and whether it has one.
func (r Rule) Get(prop string) (string, bool) {
	for i := len(r.Decls) - 1; i >= 0; i-- {
		if r.Decls[i].Prop == prop {
			return r.Decls[i].Value, true
		}
	}
	return "", false
}

// HasSelector reports whether sel is one of r's selectors.
func (r Rule) HasSelector(sel string) bool {
	for _, s := range r.Selectors {
		if s == sel {
			return true
		}
	}
	return false
}

// StripComments replaces CSS comments with spaces, keeping the newlines (and so the line
// numbers). Comments do not start inside strings in the files read here; a quote inside a
// comment does not matter.
func StripComments(src string) (string, error) {
	b := []byte(src)
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\'' || b[i] == '"' {
			q := b[i]
			for i++; i < len(b) && b[i] != q && b[i] != '\n'; i++ {
			}
			continue
		}
		if b[i] != '/' || b[i+1] != '*' {
			continue
		}
		end := strings.Index(string(b[i+2:]), "*/")
		if end < 0 {
			return "", fmt.Errorf("line %d: comment never closed", lineOf(src, i))
		}
		for j := i; j < i+2+end+2; j++ {
			if b[j] != '\n' {
				b[j] = ' '
			}
		}
		i += end + 3
	}
	return string(b), nil
}

func lineOf(src string, off int) int { return 1 + strings.Count(src[:off], "\n") }

// ParseCSS reads a style sheet: rules, and @media blocks of rules (nested @media and other
// at-rules other than @keyframes, which are skipped, fail). Comments must be stripped.
func ParseCSS(src string) ([]Rule, error) {
	p := &cssParser{src: src}
	return p.rules("", len(src))
}

type cssParser struct {
	src string
	pos int
}

func (p *cssParser) errf(off int, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", lineOf(p.src, off), fmt.Sprintf(format, args...))
}

// block finds the '{' that opens the next block and its matching '}' before end.
func (p *cssParser) block(end int) (prelude string, at, open, close int, err error) {
	at = p.pos
	open = -1
	for i := p.pos; i < end; i++ {
		switch p.src[i] {
		case '{':
			open = i
		case '}':
			return "", at, -1, -1, p.errf(i, "unexpected '}'")
		case '\'', '"':
			q := p.src[i]
			for i++; i < end && p.src[i] != q; i++ {
			}
		}
		if open >= 0 {
			break
		}
	}
	if open < 0 {
		if strings.TrimSpace(p.src[p.pos:end]) != "" {
			return "", at, -1, -1, p.errf(p.pos, "text outside a rule: %q", strings.TrimSpace(p.src[p.pos:end]))
		}
		return "", at, -1, -1, nil
	}
	depth := 0
	for i := open; i < end; i++ {
		switch p.src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(p.src[p.pos:open]), at, open, i, nil
			}
		case '\'', '"':
			q := p.src[i]
			for i++; i < end && p.src[i] != q; i++ {
			}
		}
	}
	return "", at, -1, -1, p.errf(open, "block never closed")
}

func (p *cssParser) rules(media string, end int) ([]Rule, error) {
	var out []Rule
	for {
		prelude, at, open, close, err := p.block(end)
		if err != nil {
			return nil, err
		}
		if open < 0 {
			p.pos = end
			return out, nil
		}
		at += len(p.src[at:open]) - len(strings.TrimLeft(p.src[at:open], " \t\r\n"))
		switch {
		case strings.HasPrefix(prelude, "@media"):
			if media != "" {
				return nil, p.errf(at, "@media inside @media")
			}
			q := normalizeSpace(strings.TrimPrefix(prelude, "@media"))
			p.pos = open + 1
			inner, err := p.rules(q, close)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		case strings.HasPrefix(prelude, "@keyframes"):
			// motion: the kit animates in Go
		case strings.HasPrefix(prelude, "@"):
			return nil, p.errf(at, "unsupported at-rule %q", prelude)
		default:
			decls, err := p.decls(open+1, close)
			if err != nil {
				return nil, err
			}
			out = append(out, Rule{Media: media, Selectors: splitTop(prelude, ','), Decls: decls, Line: lineOf(p.src, at)})
		}
		p.pos = close + 1
	}
}

// decls reads the declarations between start and end.
func (p *cssParser) decls(start, end int) ([]Decl, error) {
	var out []Decl
	body := p.src[start:end]
	off := start
	for _, part := range splitTopKeep(body, ';') {
		text := strings.TrimSpace(part.s)
		if text == "" {
			continue
		}
		at := off + part.at + strings.Index(part.s, text)
		colon := strings.IndexByte(text, ':')
		if colon <= 0 {
			return nil, p.errf(at, "malformed declaration %q", text)
		}
		prop := strings.TrimSpace(text[:colon])
		val := normalizeSpace(text[colon+1:])
		out = append(out, Decl{Prop: prop, Value: val, Line: lineOf(p.src, at)})
	}
	return out, nil
}

type piece struct {
	s  string
	at int
}

// splitTopKeep splits s at sep outside parentheses and quotes, keeping each piece's offset.
func splitTopKeep(s string, sep byte) []piece {
	var out []piece
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == '\'' || c == '"':
			for i++; i < len(s) && s[i] != c; i++ {
			}
		case c == sep && depth == 0:
			out = append(out, piece{s[start:i], start})
			start = i + 1
		}
	}
	return append(out, piece{s[start:], start})
}

// splitTop splits s at sep outside parentheses and quotes, trimming each piece.
func splitTop(s string, sep byte) []string {
	var out []string
	for _, p := range splitTopKeep(s, sep) {
		if t := normalizeSpace(p.s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizeSpace collapses runs of white space into one space and trims the ends.
func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// SvelteStyle is the CSS of a component's <style> block, comments stripped, with the
// offset of its first line so that errors name the component's line.
func SvelteStyle(src string) (css string, firstLine int, err error) {
	open := strings.Index(src, "<style>")
	close := strings.LastIndex(src, "</style>")
	if open < 0 || close < open {
		return "", 0, fmt.Errorf("no <style> block")
	}
	body := src[open+len("<style>") : close]
	css, err = StripComments(body)
	return css, lineOf(src, open), err
}
