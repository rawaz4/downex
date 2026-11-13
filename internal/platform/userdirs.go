package platform

import (
	"path"
	"strings"
)

// userDir returns the directory that data, the contents of an xdg-user-dirs
// "user-dirs.dirs" file, assigns to XDG_<name>_DIR, or "" when it assigns none.
//
// The file is a shell fragment whose lines read XDG_NAME_DIR="$HOME/relative/path" or
// XDG_NAME_DIR="/absolute/path". Values are parsed as one shell word: double quotes,
// single quotes and backslash escapes are honoured, and $HOME or ${HOME} expands to home
// outside single quotes. An optional "export " prefix and a trailing comment are allowed.
// Lines using anything else (other variables, command substitution, several words) and
// values that are not absolute after expansion are ignored. As when the shell sources the
// file, the last valid assignment wins.
//
// It is used on Linux only, but kept free of build constraints so that every platform
// runs its tests. Paths are slash-separated and the result is cleaned.
func userDir(data []byte, name, home string) string {
	key := "XDG_" + name + "_DIR="
	dir := ""
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimLeft(rest, " \t")
		}
		value, ok := strings.CutPrefix(line, key)
		if !ok {
			continue
		}
		v, ok := shellWord(value, home)
		if !ok || !strings.HasPrefix(v, "/") {
			continue
		}
		dir = path.Clean(v)
	}
	return dir
}

// shellWord parses s, the right-hand side of a shell assignment, as one word, expanding
// $HOME and ${HOME} to home outside single quotes. ok is false for unterminated quotes,
// parameter expansions other than HOME (or HOME when home is empty), command
// substitution, and anything but blanks and a comment after the word.
func shellWord(s, home string) (word string, ok bool) {
	const (
		bare = iota
		single
		double
	)
	var b strings.Builder
	state := bare
	for i := 0; i < len(s); {
		c := s[i]
		switch state {
		case single:
			if c == '\'' {
				state = bare
			} else {
				b.WriteByte(c)
			}
			i++
		case double:
			switch c {
			case '"':
				state = bare
				i++
			case '\\':
				// Inside double quotes a backslash only escapes $ ` " and \.
				if i+1 < len(s) && strings.IndexByte("$`\"\\", s[i+1]) >= 0 {
					b.WriteByte(s[i+1])
					i += 2
				} else {
					b.WriteByte(c)
					i++
				}
			case '$':
				n, ok := expandHome(s[i:], home, &b)
				if !ok {
					return "", false
				}
				i += n
			case '`':
				return "", false
			default:
				b.WriteByte(c)
				i++
			}
		default: // bare
			switch c {
			case ' ', '\t':
				rest := strings.TrimLeft(s[i:], " \t")
				if rest != "" && rest[0] != '#' {
					return "", false
				}
				return b.String(), true
			case '\'':
				state = single
				i++
			case '"':
				state = double
				i++
			case '\\':
				if i+1 >= len(s) {
					return "", false
				}
				b.WriteByte(s[i+1])
				i += 2
			case '$':
				n, ok := expandHome(s[i:], home, &b)
				if !ok {
					return "", false
				}
				i += n
			case '`', ';', '&', '|', '<', '>', '(', ')':
				return "", false
			default:
				b.WriteByte(c)
				i++
			}
		}
	}
	if state != bare {
		return "", false
	}
	return b.String(), true
}

// expandHome writes home to b when s starts with the parameter expansion $HOME or
// ${HOME}, and returns the length of that expansion.
func expandHome(s, home string, b *strings.Builder) (n int, ok bool) {
	if home == "" {
		return 0, false
	}
	switch {
	case strings.HasPrefix(s, "${HOME}"):
		n = len("${HOME}")
	case strings.HasPrefix(s, "$HOME") && (len(s) == len("$HOME") || !isNameByte(s[len("$HOME")])):
		n = len("$HOME")
	default:
		return 0, false
	}
	b.WriteString(home)
	return n, true
}

// isNameByte reports whether c can continue a shell variable name.
func isNameByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
