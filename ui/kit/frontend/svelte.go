package frontend

import (
	"fmt"
	"strings"
)

// Node is an element of a component's markup, or a Svelte {#if} block (Tag "#if", whose
// Branches hold the {:else if} and {:else} arms in order).
type Node struct {
	Tag      string
	Attrs    map[string]string // attribute values as written; {expr} values keep their braces
	Children []*Node
	Branches []Branch
	Line     int
}

// Branch is one arm of an {#if} block; Cond is "" for {:else}.
type Branch struct {
	Cond     string
	Children []*Node
}

// Markup parses the markup of a component (outside its <script> and <style> blocks) into
// its top-level nodes. It reads elements with attributes, self-closing elements, HTML
// comments and {#if}/{:else if}/{:else}/{/if}; text between elements must be white space.
func Markup(src string) ([]*Node, error) {
	body := blankBlocks(src, "<script", "</script>")
	body = blankBlocks(body, "<style>", "</style>")
	p := &markupParser{src: body}
	nodes, end, err := p.nodes("")
	if err != nil {
		return nil, err
	}
	if end != "" {
		return nil, p.errf(p.pos, "unexpected %s", end)
	}
	return nodes, nil
}

// blankBlocks blanks every block from open to close (keeping newlines).
func blankBlocks(src, open, close string) string {
	b := []byte(src)
	for from := 0; ; {
		i := strings.Index(string(b[from:]), open)
		if i < 0 {
			return string(b)
		}
		i += from
		j := strings.Index(string(b[i:]), close)
		if j < 0 {
			return string(b)
		}
		j += i + len(close)
		for k := i; k < j; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
		from = j
	}
}

type markupParser struct {
	src string
	pos int
}

func (p *markupParser) errf(off int, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", lineOf(p.src, off), fmt.Sprintf(format, args...))
}

// nodes reads nodes until the end of the source or a closing token, which it returns: a
// closing tag "</g>", "{:else if ...}", "{:else}" or "{/if}".
func (p *markupParser) nodes(_ string) ([]*Node, string, error) {
	var out []*Node
	for {
		for p.pos < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])) {
			p.pos++
		}
		if p.pos >= len(p.src) {
			return out, "", nil
		}
		rest := p.src[p.pos:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest, "-->")
			if end < 0 {
				return nil, "", p.errf(p.pos, "comment never closed")
			}
			p.pos += end + 3
		case strings.HasPrefix(rest, "</"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return nil, "", p.errf(p.pos, "closing tag never ends")
			}
			tok := rest[:end+1]
			p.pos += end + 1
			return out, tok, nil
		case strings.HasPrefix(rest, "{#if "):
			n, err := p.ifBlock()
			if err != nil {
				return nil, "", err
			}
			out = append(out, n)
		case strings.HasPrefix(rest, "{:else") || strings.HasPrefix(rest, "{/if}"):
			end := strings.IndexByte(rest, '}')
			tok := rest[:end+1]
			p.pos += end + 1
			return out, tok, nil
		case strings.HasPrefix(rest, "<"):
			n, err := p.element()
			if err != nil {
				return nil, "", err
			}
			out = append(out, n)
		default:
			return nil, "", p.errf(p.pos, "text in the markup: %q", firstLine(rest))
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (p *markupParser) ifBlock() (*Node, error) {
	start := p.pos
	end := strings.IndexByte(p.src[p.pos:], '}')
	if end < 0 {
		return nil, p.errf(p.pos, "{#if never closed")
	}
	cond := strings.TrimSpace(p.src[p.pos+len("{#if ") : p.pos+end])
	p.pos += end + 1
	n := &Node{Tag: "#if", Line: lineOf(p.src, start)}
	for {
		kids, tok, err := p.nodes("")
		if err != nil {
			return nil, err
		}
		n.Branches = append(n.Branches, Branch{Cond: cond, Children: kids})
		switch {
		case tok == "{/if}":
			return n, nil
		case tok == "{:else}":
			cond = ""
		case strings.HasPrefix(tok, "{:else if "):
			cond = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tok, "{:else if "), "}"))
		default:
			return nil, p.errf(start, "{#if %s} ended by %q", cond, tok)
		}
	}
}

func (p *markupParser) element() (*Node, error) {
	start := p.pos
	p.pos++ // '<'
	i := p.pos
	for i < len(p.src) && !strings.ContainsRune(" \t\r\n/>", rune(p.src[i])) {
		i++
	}
	n := &Node{Tag: p.src[p.pos:i], Attrs: map[string]string{}, Line: lineOf(p.src, start)}
	p.pos = i
	for {
		for p.pos < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])) {
			p.pos++
		}
		if p.pos >= len(p.src) {
			return nil, p.errf(start, "<%s never closed", n.Tag)
		}
		switch {
		case strings.HasPrefix(p.src[p.pos:], "/>"):
			p.pos += 2
			return n, nil
		case p.src[p.pos] == '>':
			p.pos++
			kids, tok, err := p.nodes("")
			if err != nil {
				return nil, err
			}
			if tok != "</"+n.Tag+">" {
				return nil, p.errf(start, "<%s> closed by %q", n.Tag, tok)
			}
			n.Children = kids
			return n, nil
		}
		// an attribute: name, name="value", name={expr}, {shorthand}
		j := p.pos
		if p.src[j] == '{' {
			end := matchBrace(p.src, j)
			if end < 0 {
				return nil, p.errf(j, "attribute expression never closed")
			}
			n.Attrs[p.src[j:end+1]] = p.src[j : end+1]
			p.pos = end + 1
			continue
		}
		for j < len(p.src) && !strings.ContainsRune(" \t\r\n=/>", rune(p.src[j])) {
			j++
		}
		name := p.src[p.pos:j]
		p.pos = j
		if p.pos < len(p.src) && p.src[p.pos] == '=' {
			p.pos++
			switch c := p.src[p.pos]; c {
			case '"', '\'':
				end := strings.IndexByte(p.src[p.pos+1:], c)
				if end < 0 {
					return nil, p.errf(p.pos, "attribute %s never closed", name)
				}
				n.Attrs[name] = p.src[p.pos+1 : p.pos+1+end]
				p.pos += end + 2
			case '{':
				end := matchBrace(p.src, p.pos)
				if end < 0 {
					return nil, p.errf(p.pos, "attribute %s never closed", name)
				}
				n.Attrs[name] = p.src[p.pos : end+1]
				p.pos = end + 1
			default:
				return nil, p.errf(p.pos, "unquoted attribute %s", name)
			}
		} else {
			n.Attrs[name] = ""
		}
	}
}

// matchBrace is the index of the '}' that closes the '{' at i, or -1.
func matchBrace(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		case '\'', '"', '`':
			q := s[i]
			for i++; i < len(s) && s[i] != q; i++ {
			}
		}
	}
	return -1
}

// ---- the module script --------------------------------------------------------------------

// tsToken is a token of the TypeScript subset the module scripts are read with.
type tsToken struct {
	kind byte   // 'i' identifier, 's' string, 't' template, 'n' number, 'p' punctuation
	text string // the identifier, the string's value, the template's raw body, the number
	at   int
}

// tsTokens splits a script into tokens, skipping comments.
func tsTokens(src string) ([]tsToken, error) {
	var out []tsToken
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("line %d: comment never closed", lineOf(src, i))
			}
			i += end + 4
		case c == '\'' || c == '"':
			j := i + 1
			var b strings.Builder
			for ; j < len(src) && src[j] != c; j++ {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				}
				b.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, fmt.Errorf("line %d: string never closed", lineOf(src, i))
			}
			out = append(out, tsToken{'s', b.String(), i})
			i = j + 1
		case c == '`':
			end := strings.IndexByte(src[i+1:], '`')
			if end < 0 {
				return nil, fmt.Errorf("line %d: template never closed", lineOf(src, i))
			}
			out = append(out, tsToken{'t', src[i+1 : i+1+end], i})
			i += end + 2
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '$' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			out = append(out, tsToken{'i', src[i:j], i})
			i = j
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			out = append(out, tsToken{'n', src[i:j], i})
			i = j
		default:
			out = append(out, tsToken{'p', string(c), i})
			i++
		}
	}
	return out, nil
}

// moduleScript is the body of a component's <script ... module> block.
func moduleScript(src string) (string, error) {
	i := strings.Index(src, "<script lang=\"ts\" module>")
	if i < 0 {
		return "", fmt.Errorf("no <script lang=\"ts\" module> block")
	}
	i += len("<script lang=\"ts\" module>")
	j := strings.Index(src[i:], "</script>")
	if j < 0 {
		return "", fmt.Errorf("the module script never ends")
	}
	return src[i : i+j], nil
}

// functionSource is the source of `function name(...` up to its closing brace, white space
// collapsed, so that a drift test can tell when a function the generator ports changed.
func functionSource(script, name string) (string, error) {
	i := strings.Index(script, "function "+name+"(")
	if i < 0 {
		return "", fmt.Errorf("function %s not found", name)
	}
	open := strings.IndexByte(script[i:], '{')
	if open < 0 {
		return "", fmt.Errorf("function %s has no body", name)
	}
	end := matchBrace(script, i+open)
	if end < 0 {
		return "", fmt.Errorf("function %s never ends", name)
	}
	return normalizeSpace(script[i : end+1]), nil
}

// FunctionSources is the source of each named function of a component's module script,
// white space collapsed, joined by "|": what a drift test compares with the copy a hand
// port keeps.
func FunctionSources(component string, names ...string) (string, error) {
	script, err := moduleScript(component)
	if err != nil {
		return "", err
	}
	var out []string
	for _, n := range names {
		s, err := functionSource(script, n)
		if err != nil {
			return "", err
		}
		out = append(out, s)
	}
	return strings.Join(out, "|"), nil
}
