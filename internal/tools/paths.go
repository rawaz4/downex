package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tool names (ToolInfo.name).
const (
	ToolYtDlp = "yt-dlp"
	ToolDeno  = "deno"
)

// Names inside the tools folder (docs/SITE-MEDIA-SPEC.md §3.9):
//
//	tools.json                     current and previous versions, hashes, verification, bad versions
//	.lock                          exclusive file lock while installing, switching or cleaning
//	yt-dlp/<version>/yt-dlp(.exe)  one folder per installed version; an onedir copy also
//	                 _internal/    holds the rest of the unpacked zip and
//	                 downex-files.json, the list of its files with their sizes and SHA-256
//	deno/<version>/deno(.exe)
//	cache/yt-dlp/, cache/deno/     yt-dlp's --cache-dir and DENO_DIR
//	tmp/                           partial downloads; emptied at start
//	run/<16 hex>/                  one per child run: tmp/ (its TEMP), cookie file; removed after it
const (
	stateName = "tools.json"
	lockName  = ".lock"
	cacheName = "cache"
	tmpName   = "tmp"
	runName   = "run"
	// manifestName is the file list of an onedir copy, inside its version folder.
	manifestName = "downex-files.json"
	// newInfix marks a version folder being filled: <tool>/<version>.new-<random>.
	newInfix = ".new-"
	// oldInfix marks a version folder set aside while a new copy of the same version
	// is tried: <tool>/<version>.old-<random>.
	oldInfix = ".old-"
)

// layout locates the files of one tools folder for system goos.
type layout struct {
	dir  string // absolute
	goos string
}

// exeName is the file name of tool's executable on goos.
func exeName(tool, goos string) string {
	if goos == "windows" {
		return tool + ".exe"
	}
	return tool
}

func (l layout) toolDir(tool string) string { return filepath.Join(l.dir, tool) }

// versionDir is the folder of an installed version. version must have passed the tool's
// version check (validVersion): paths are built only from validated strings.
func (l layout) versionDir(tool, version string) string {
	return filepath.Join(l.dir, tool, version)
}

// exe is the executable of an installed version.
func (l layout) exe(tool, version string) string {
	return filepath.Join(l.versionDir(tool, version), exeName(tool, l.goos))
}

// rel is the path of an installed version's executable relative to the tools folder, as
// tools.json records it (slash-separated).
func (l layout) rel(tool, version string) string {
	return tool + "/" + version + "/" + exeName(tool, l.goos)
}

func (l layout) state() string      { return filepath.Join(l.dir, stateName) }
func (l layout) lock() string       { return filepath.Join(l.dir, lockName) }
func (l layout) tmp() string        { return filepath.Join(l.dir, tmpName) }
func (l layout) run() string        { return filepath.Join(l.dir, runName) }
func (l layout) ytdlpCache() string { return filepath.Join(l.dir, cacheName, ToolYtDlp) }
func (l layout) denoCache() string  { return filepath.Join(l.dir, cacheName, ToolDeno) }

// prepare creates the folders of the layout (mode 0700; on Windows they inherit the
// parent's ACL).
func (l layout) prepare() error {
	for _, d := range []string{l.dir, l.toolDir(ToolYtDlp), l.toolDir(ToolDeno), l.ytdlpCache(), l.denoCache(), l.tmp(), l.run()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("tools: create %s: %w", d, err)
		}
	}
	return nil
}

// clearDir removes everything inside dir, keeping dir itself; entries that cannot be
// removed (a file a running process still holds) are left.
func clearDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// randomHex returns n random bytes as 2n lowercase hex digits.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewRunDir creates a private work folder for one child run below root (a Lease's
// WorkRoot): root/<16 hex>/ with an empty tmp/ inside, both mode 0700, and returns its
// path. The caller removes it (os.RemoveAll) when the child has exited; leftovers are
// removed when the manager starts.
func NewRunDir(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("tools: work root %q is not absolute", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("tools: create the work root: %w", err)
	}
	for range 4 {
		dir := filepath.Join(root, randomHex(8))
		if err := os.Mkdir(dir, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("tools: create a work folder: %w", err)
		}
		if err := os.Mkdir(filepath.Join(dir, tmpName), 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("tools: create a work folder: %w", err)
		}
		return dir, nil
	}
	return "", fmt.Errorf("tools: create a work folder: name collisions")
}

// isTempDir reports whether name is a version folder being filled
// (<version>.new-<hex>) or set aside (<version>.old-<hex>).
func isTempDir(name string) bool {
	return strings.Contains(name, newInfix) || strings.Contains(name, oldInfix)
}
