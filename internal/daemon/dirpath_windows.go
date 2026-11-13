//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// checkLocalDir refuses directories that are not plain paths on a local drive letter
// (docs/DAEMON-SPEC.md §4: dir comes from untrusted clients). It requires the clean
// absolute dir to be X:\..., which rules out
//   - UNC paths (\\host\share, //host/share, \\host@SSL\DavWWWRoot): touching them makes
//     Windows authenticate to that host with the user's credentials (SMB or WebDAV);
//   - device and long-path forms (\\?\, \\.\, \??\, \\?\GLOBALROOT\...), which skip
//     Win32 normalisation and reach raw devices or create undeletable names,
//
// and refuses components that name alternate data streams (':'), reserved device names
// (CON, NUL, COM1, ...), end in a dot or a space, or hold characters Windows forbids in
// names.
func checkLocalDir(dir string) error {
	if len(dir) < 3 || dir[1] != ':' || !isASCIILetter(dir[0]) || !os.IsPathSeparator(dir[2]) {
		return errors.New("dir must be a path on a local drive (X:\\...); network shares and device paths are not accepted")
	}
	for _, c := range strings.FieldsFunc(dir[3:], func(r rune) bool { return r == '\\' || r == '/' }) {
		switch {
		case strings.ContainsAny(c, `:<>"|?*`) || strings.ContainsFunc(c, func(r rune) bool { return r < 0x20 }):
			return fmt.Errorf("dir component %q contains a character Windows does not allow in names", c)
		case strings.HasSuffix(c, ".") || strings.HasSuffix(c, " "):
			return fmt.Errorf("dir component %q ends in a dot or a space", c)
		case reservedDeviceName(c):
			return fmt.Errorf("dir component %q is a reserved device name", c)
		}
	}
	return nil
}

// samePath reports whether two clean absolute paths name the same location (Windows
// paths are case-insensitive).
func samePath(a, b string) bool { return strings.EqualFold(a, b) }

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// reservedDeviceName reports names that Windows maps to a device whatever directory
// they are in, with or without an extension: CON, PRN, AUX, NUL, COM0-9, LPT0-9 (also
// with superscript digits), CONIN$ and CONOUT$.
func reservedDeviceName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) >= 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		switch d := base[3:]; d {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
			return true
		}
	}
	return false
}
