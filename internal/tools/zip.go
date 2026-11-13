package tools

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// unzipExe extracts the executable of a Deno release archive (docs/SITE-MEDIA-SPEC.md
// §4.4): the archive must hold exactly one entry, a regular file at its root named
// want ("deno" or "deno.exe"), unpacking to at most limit bytes (checked against both
// the declared and the actual size). Any other entry, a directory, a path separator or
// ".." is refused (errors wrap ErrArchive). The file is written to dst (which must not
// exist; mode 0755 on Unix) and flushed; it returns its size and SHA-256. On failure
// dst is removed.
func unzipExe(archive, want, dst string, limit int64) (size int64, sum [sha256.Size]byte, err error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return 0, sum, fmt.Errorf("%w: %v", ErrArchive, err)
	}
	defer r.Close()
	if len(r.File) != 1 {
		return 0, sum, fmt.Errorf("%w: %d entries, want only %s", ErrArchive, len(r.File), want)
	}
	f := r.File[0]
	switch {
	case f.Name != want || strings.ContainsAny(f.Name, `/\:`) || strings.Contains(f.Name, ".."):
		return 0, sum, fmt.Errorf("%w: unexpected entry %.64q", ErrArchive, f.Name)
	case !f.Mode().IsRegular():
		return 0, sum, fmt.Errorf("%w: %s is not a regular file", ErrArchive, want)
	case f.UncompressedSize64 > uint64(limit):
		return 0, sum, fmt.Errorf("%w: %s unpacks to more than %d bytes", ErrArchive, want, limit)
	}
	return extractFile(f, dst, true, limit)
}

// extractFile writes the content of the archive entry f to the new file dst (which must
// not exist; with exe, mode 0755 on Unix) and flushes it, reading at most limit bytes:
// more, or content that does not match the entry's declared size and CRC-32, is refused
// (ErrArchive). It returns the size and SHA-256 written. On failure dst is removed.
func extractFile(f *zip.File, dst string, exe bool, limit int64) (size int64, sum [sha256.Size]byte, err error) {
	rc, err := f.Open()
	if err != nil {
		return 0, sum, fmt.Errorf("%w: %v", ErrArchive, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, sum, fmt.Errorf("%w: %.64q is there twice", ErrArchive, f.Name)
		}
		return 0, sum, writeError(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = os.Remove(dst)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, limit+1))
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return 0, sum, writeError(err)
		}
		return 0, sum, fmt.Errorf("%w: %v", ErrArchive, err)
	}
	if n > limit {
		return 0, sum, fmt.Errorf("%w: %.64q unpacks to more than %d bytes", ErrArchive, f.Name, limit)
	}
	if exe {
		if err := chmodExe(out); err != nil {
			return 0, sum, writeError(err)
		}
	}
	if err := out.Sync(); err != nil {
		return 0, sum, writeError(err)
	}
	if err := out.Close(); err != nil {
		return 0, sum, writeError(err)
	}
	ok = true
	copy(sum[:], h.Sum(nil))
	return n, sum, nil
}

// Bounds of an onedir archive (yt-dlp's zip: an executable and its _internal/ folder of
// a few hundred files, some 60 MiB unpacked).
const (
	maxYtDlpUnpacked = 512 << 20
	maxTreeEntries   = 20000
	maxEntryName     = 255
)

// treeLimits bound what unzipTree unpacks.
type treeLimits struct {
	bytes   int64 // all files together, declared and actual
	entries int   // entries of the archive, directories included
}

var ytdlpTreeLimits = treeLimits{bytes: maxYtDlpUnpacked, entries: maxTreeEntries}

// unpacked is what unzipTree wrote.
type unpacked struct {
	exeSum  [sha256.Size]byte
	exeSize int64
	files   []manifestFile // every file written, the executable included, sorted by path
}

// unzipTree unpacks an onedir release archive into the empty folder dir: the program's
// executable at the archive's root, named exe (or, if there is none of that name, the
// only regular file at the root whose name starts with "yt-dlp", ending in ".exe" when
// exe does), is written as exeAs (mode 0755 on Unix), every other entry at its path.
// Each entry name is checked by archivePath; directories are created, regular files
// written (0755 when the archive marks them executable) and flushed, anything else
// (symbolic links, devices) is refused, as are duplicates (ignoring case), a file where
// another entry needs a folder, a root entry named exeAs (other than the executable) or
// manifestName, more than lim.entries entries and more than lim.bytes bytes, declared or
// actual. Errors wrap ErrArchive, or are write errors; what was written stays in dir,
// which the caller removes.
func unzipTree(archive, dir, exe, exeAs string, lim treeLimits) (unpacked, error) {
	var u unpacked
	r, err := zip.OpenReader(archive)
	if err != nil {
		return u, fmt.Errorf("%w: %v", ErrArchive, err)
	}
	defer r.Close()
	if len(r.File) == 0 || len(r.File) > lim.entries {
		return u, fmt.Errorf("%w: %d entries", ErrArchive, len(r.File))
	}
	type item struct {
		f    *zip.File
		path string // slash-separated, "" = the root
		dir  bool
	}
	items := make([]item, 0, len(r.File))
	files := map[string]bool{} // lower-cased paths of files
	dirs := map[string]bool{}  // lower-cased paths of folders, explicit or implied
	var declared uint64
	for _, f := range r.File {
		p, isDir, err := archivePath(f.Name)
		if err != nil {
			return u, err
		}
		mode := f.Mode()
		switch {
		case isDir || mode.IsDir():
			if mode.Type() != fs.ModeDir && mode.Type() != 0 {
				return u, fmt.Errorf("%w: %.64q is not a folder", ErrArchive, f.Name)
			}
			isDir = true
		case p == "":
			return u, fmt.Errorf("%w: an entry without a name", ErrArchive)
		case !mode.IsRegular():
			return u, fmt.Errorf("%w: %.64q is not a regular file", ErrArchive, f.Name)
		}
		key := strings.ToLower(p)
		if !isDir {
			if files[key] || dirs[key] {
				return u, fmt.Errorf("%w: %.64q is there twice", ErrArchive, f.Name)
			}
			files[key] = true
			declared += f.UncompressedSize64
			if f.UncompressedSize64 > uint64(lim.bytes) || declared > uint64(lim.bytes) {
				return u, fmt.Errorf("%w: unpacks to more than %d bytes", ErrArchive, lim.bytes)
			}
		} else if files[key] {
			return u, fmt.Errorf("%w: %.64q is a file and a folder", ErrArchive, f.Name)
		}
		// Record the folders above the entry (and a folder entry itself).
		d := key
		if !isDir {
			d = parentPath(key)
		}
		for d != "" && !dirs[d] {
			if files[d] {
				return u, fmt.Errorf("%w: %.64q is a file and a folder", ErrArchive, d)
			}
			dirs[d] = true
			d = parentPath(d)
		}
		items = append(items, item{f: f, path: p, dir: isDir})
	}
	// The executable: exe at the root, else the only root file that looks like it.
	exeKey := strings.ToLower(exe)
	if !files[exeKey] {
		var found []string
		for _, it := range items {
			k := strings.ToLower(it.path)
			if !it.dir && !strings.Contains(k, "/") && strings.HasPrefix(k, "yt-dlp") &&
				(!strings.HasSuffix(strings.ToLower(exe), ".exe") || strings.HasSuffix(k, ".exe")) {
				found = append(found, k)
			}
		}
		if len(found) != 1 {
			return u, fmt.Errorf("%w: no single executable %s at the root", ErrArchive, exe)
		}
		exeKey = found[0]
	}
	for _, name := range []string{exeAs, manifestName} {
		k := strings.ToLower(name)
		if k != exeKey && (files[k] || dirs[k]) {
			return u, fmt.Errorf("%w: unexpected entry %.64q", ErrArchive, name)
		}
	}
	// Folders first, then the files, each into a folder that exists.
	for _, it := range items {
		if it.dir && it.path != "" {
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(it.path)), 0o700); err != nil {
				return u, writeError(err)
			}
		}
	}
	remaining := lim.bytes
	for _, it := range items {
		if it.dir {
			continue
		}
		rel, isExe := it.path, strings.ToLower(it.path) == exeKey
		if isExe {
			rel = exeAs
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if parent := parentPath(rel); parent != "" {
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(parent)), 0o700); err != nil {
				return u, writeError(err)
			}
		}
		n, sum, err := extractFile(it.f, dst, isExe || it.f.Mode()&0o111 != 0, remaining)
		if err != nil {
			return u, err
		}
		remaining -= n
		if isExe {
			u.exeSum, u.exeSize = sum, n
		}
		u.files = append(u.files, manifestFile{Path: rel, Size: n, SHA256: hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(u.files, func(a, b manifestFile) int { return strings.Compare(a.Path, b.Path) })
	return u, nil
}

// archivePath checks the name of an archive entry and returns its path, slash-separated
// and without "." elements ("" for the root itself); dir reports a trailing separator.
// Backslashes count as separators (archives made by Windows tools). Refused (ErrArchive):
// names that are empty, longer than maxEntryName bytes or not UTF-8; absolute paths,
// drive letters and ".."; empty elements; control characters and the characters Windows
// does not allow in names (<>:"|?*); elements ending in a dot or a space; Windows device
// names (CON, NUL, COM1, ...).
func archivePath(name string) (p string, dir bool, err error) {
	bad := func(why string) (string, bool, error) {
		return "", false, fmt.Errorf("%w: entry %.64q %s", ErrArchive, name, why)
	}
	if name == "" || len(name) > maxEntryName || !utf8.ValidString(name) {
		return bad("has an unusable name")
	}
	n := strings.ReplaceAll(name, `\`, "/")
	dir = strings.HasSuffix(n, "/")
	n = strings.TrimSuffix(n, "/")
	if strings.HasPrefix(n, "/") {
		return bad("is an absolute path")
	}
	var parts []string
	for _, c := range strings.Split(n, "/") {
		switch {
		case c == ".":
			continue
		case c == "" || c == "..":
			return bad("leaves its folder")
		case strings.ContainsAny(c, `<>:"|?*`) || strings.ContainsFunc(c, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return bad("has a character names cannot have")
		case strings.HasSuffix(c, ".") || strings.HasSuffix(c, " "):
			return bad("ends in a dot or a space")
		case isDeviceName(c):
			return bad("is a device name")
		}
		parts = append(parts, c)
	}
	p = strings.Join(parts, "/")
	if p != "" && !filepath.IsLocal(filepath.FromSlash(p)) {
		return bad("leaves its folder")
	}
	return p, dir, nil
}

// parentPath is the folder of a slash-separated path ("" at the root).
func parentPath(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// isDeviceName reports a Windows device name (CON, PRN, AUX, NUL, COM0-9, LPT0-9,
// CONIN$, CONOUT$), with or without an extension, in any case.
func isDeviceName(c string) bool {
	base, _, _ := strings.Cut(c, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '0' && base[3] <= '9'
}
