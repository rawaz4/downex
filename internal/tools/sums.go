package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf16"
)

// sumEntry is one line of a checksum list.
type sumEntry struct {
	sum  [sha256.Size]byte
	name string // as written: a plain name, or a path on the machine that made the list
}

// parseGNULine parses one line of GNU sha256sum output: 64 hex digits, a space, a space
// or "*" (binary mode), and the file name.
func parseGNULine(line string) (sumEntry, bool) {
	if len(line) < 64+2+1 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
		return sumEntry{}, false
	}
	var e sumEntry
	if n, err := hex.Decode(e.sum[:], []byte(line[:64])); err != nil || n != sha256.Size {
		return sumEntry{}, false
	}
	e.name = line[66:]
	return e, true
}

// lines splits data at "\n", dropping a trailing "\r" from each line.
func lines(data string) []string {
	ls := strings.Split(data, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimSuffix(l, "\r")
	}
	return ls
}

// sumFor returns the SHA-256 that a GNU-format checksum list (yt-dlp's SHA2-256SUMS)
// gives for the file name: exactly one line must name it exactly. Lines in other formats
// are ignored; errors wrap ErrChecksum.
func sumFor(list []byte, name string) ([sha256.Size]byte, error) {
	found := namedSums(list, name)
	switch len(found) {
	case 0:
		return [sha256.Size]byte{}, fmt.Errorf("%w: the checksum list does not name %s", ErrChecksum, name)
	case 1:
		return found[0].sum, nil
	}
	return [sha256.Size]byte{}, fmt.Errorf("%w: the checksum list names %s more than once", ErrChecksum, name)
}

// namedSums returns the GNU-format lines of a checksum list that name exactly name.
func namedSums(list []byte, name string) []sumEntry {
	var found []sumEntry
	for _, l := range lines(string(list)) {
		if e, ok := parseGNULine(l); ok && e.name == name {
			found = append(found, e)
		}
	}
	return found
}

// parseSHA256File returns the SHA-256 that a Deno ".sha256sum" file gives for the file
// name. Two formats are published: GNU sha256sum output (Unix assets) and PowerShell's
// "Get-FileHash | Format-List" (Windows assets: "Hash : <UPPERHEX>" with "Algorithm"
// and "Path" lines, CRLF, possibly UTF-16 with a byte order mark). A file with one entry
// is accepted whatever path it records (the build machine's); with several, the entry
// whose base name is name is taken. Errors wrap ErrChecksum.
func parseSHA256File(data []byte, name string) ([sha256.Size]byte, error) {
	text, err := decodeText(data)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var entries []sumEntry
	ps := -1 // the PowerShell record whose Path line may follow
	for _, l := range lines(text) {
		if e, ok := parseGNULine(strings.TrimSpace(l)); ok {
			entries = append(entries, e)
			continue
		}
		key, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "algorithm":
			if !strings.EqualFold(value, "SHA256") {
				return [sha256.Size]byte{}, fmt.Errorf("%w: the checksum file uses %.16q", ErrChecksum, value)
			}
		case "hash":
			var e sumEntry
			if len(value) != 2*sha256.Size {
				return [sha256.Size]byte{}, fmt.Errorf("%w: malformed hash in the checksum file", ErrChecksum)
			}
			if n, err := hex.Decode(e.sum[:], []byte(value)); err != nil || n != sha256.Size {
				return [sha256.Size]byte{}, fmt.Errorf("%w: malformed hash in the checksum file", ErrChecksum)
			}
			entries = append(entries, e)
			ps = len(entries) - 1
		case "path":
			if ps >= 0 {
				entries[ps].name = value
				ps = -1
			}
		}
	}
	switch {
	case len(entries) == 1:
		return entries[0].sum, nil
	case len(entries) == 0:
		return [sha256.Size]byte{}, fmt.Errorf("%w: no hash in the checksum file", ErrChecksum)
	}
	var found []sumEntry
	for _, e := range entries {
		if baseName(e.name) == name {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		return [sha256.Size]byte{}, fmt.Errorf("%w: the checksum file does not name %s exactly once", ErrChecksum, name)
	}
	return found[0].sum, nil
}

// baseName is the last element of a slash or backslash separated path, without a
// leading "*" (binary mode marker).
func baseName(p string) string {
	p = strings.TrimPrefix(p, "*")
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// decodeText returns data as text: UTF-8 (a byte order mark is dropped), or UTF-16
// with a byte order mark (PowerShell 5's redirection writes UTF-16LE).
func decodeText(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		return string(data[3:]), nil
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		if len(data)%2 != 0 {
			return "", fmt.Errorf("%w: malformed UTF-16 checksum file", ErrChecksum)
		}
		le := data[0] == 0xff
		u := make([]uint16, 0, len(data)/2-1)
		for i := 2; i+1 < len(data); i += 2 {
			if le {
				u = append(u, uint16(data[i])|uint16(data[i+1])<<8)
			} else {
				u = append(u, uint16(data[i])<<8|uint16(data[i+1]))
			}
		}
		return string(utf16.Decode(u)), nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%w: unexpected NUL bytes in the checksum file", ErrChecksum)
	}
	return string(data), nil
}
