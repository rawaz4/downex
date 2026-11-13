package tools

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// tools.json (docs/SITE-MEDIA-SPEC.md §3.9) records the managed copies:
//
//	{"version":1,
//	 "ytdlp":{"version":"2026.10.10.233141","channel":"nightly","file":"yt-dlp/2026.10.10.233141/yt-dlp.exe",
//	          "sha256":"…","size":18350000,"layout":"onedir","manifest":"…","sigCreated":"…",
//	          "verified":"openpgp+sha256","installedAt":"…","previous":{…},"bad":["…"],
//	          "unsuitable":[{"path":"…","version":"…"}],"latest":"…","checkedAt":"…"},
//	 "deno":{…}}
//
// It is written atomically and read with bounds: an unknown "version", or a file that
// does not parse, reads as empty (the tools are then installed again), and an entry
// that fails its checks is dropped.
const (
	stateVersion  = 1
	maxStateFile  = 64 << 10
	maxBad        = 16
	maxUnsuitable = 8
	maxPathLen    = 4096
)

// stateFile is the content of tools.json.
type stateFile struct {
	Version int        `json:"version"`
	YtDlp   toolRecord `json:"ytdlp"`
	Deno    toolRecord `json:"deno"`
}

// toolRecord is what tools.json keeps about one tool: the current managed copy (its
// fields inline; Version "" = none) and the rest.
type toolRecord struct {
	entry
	// Previous is the copy switched away from, kept for a rollback.
	Previous *entry `json:"previous,omitempty"`
	// Bad lists versions that failed after a switch and were rolled back: never
	// installed again.
	Bad []string `json:"bad,omitempty"`
	// Unsuitable lists PATH copies (path and --version) that are not used (§4.5).
	Unsuitable []pathVersion `json:"unsuitable,omitempty"`
	// Latest and CheckedAt are the last release seen and when.
	Latest    string    `json:"latest,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
}

// entry is one installed version.
type entry struct {
	Version string `json:"version,omitempty"`
	Channel string `json:"channel,omitempty"` // yt-dlp: the release channel
	File    string `json:"file,omitempty"`    // relative to the tools folder, slash-separated
	SHA256  string `json:"sha256,omitempty"`  // of the executable, lowercase hex
	Size    int64  `json:"size,omitempty"`
	// Layout is yt-dlp's: layoutOnefile or layoutOnedir; "" is a onefile copy installed
	// before layouts were recorded, which the next update check replaces by the onedir
	// build of its release where there is one. Deno's is "".
	Layout string `json:"layout,omitempty"`
	// Manifest is the SHA-256 (lowercase hex) of an onedir copy's file list
	// (manifestName in its version folder), which names every unpacked file with its
	// size and SHA-256; "" for other layouts.
	Manifest    string    `json:"manifest,omitempty"`
	SigCreated  time.Time `json:"sigCreated,omitzero"` // yt-dlp: the signature's creation time
	Verified    string    `json:"verified,omitempty"`  // ToolInfo.verified
	Signer      string    `json:"signer,omitempty"`    // Windows Deno: the Authenticode signer
	InstalledAt time.Time `json:"installedAt,omitzero"`
}

// pathVersion is a PATH copy marked unsuitable.
type pathVersion struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

func (s *stateFile) record(tool string) *toolRecord {
	if tool == ToolDeno {
		return &s.Deno
	}
	return &s.YtDlp
}

// clone returns a deep copy of s.
func (s stateFile) clone() stateFile {
	for _, r := range []*toolRecord{&s.YtDlp, &s.Deno} {
		if r.Previous != nil {
			p := *r.Previous
			r.Previous = &p
		}
		r.Bad = append([]string(nil), r.Bad...)
		r.Unsuitable = append([]pathVersion(nil), r.Unsuitable...)
	}
	return s
}

// loadState reads tools.json of l; a missing, oversized or unreadable file, or one of
// another version, reads as empty.
func loadState(l layout) stateFile {
	f, err := os.Open(l.state())
	for i := 1; err != nil && !errors.Is(err, fs.ErrNotExist) && runtime.GOOS == "windows" && i <= 10; i++ {
		time.Sleep(time.Duration(i) * 10 * time.Millisecond) // a scanner or a rename holds it
		f, err = os.Open(l.state())
	}
	if err != nil {
		return stateFile{Version: stateVersion}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStateFile+1))
	var s stateFile
	if err != nil || len(data) > maxStateFile || json.Unmarshal(data, &s) != nil || s.Version != stateVersion {
		return stateFile{Version: stateVersion}
	}
	s.YtDlp.sanitize(l, ToolYtDlp)
	s.Deno.sanitize(l, ToolDeno)
	return s
}

// sanitize drops what fails its checks.
func (r *toolRecord) sanitize(l layout, tool string) {
	if !r.entry.valid(l, tool) {
		r.entry = entry{}
	}
	if r.Previous != nil && (!r.Previous.valid(l, tool) || r.Previous.Version == r.Version) {
		r.Previous = nil
	}
	var bad []string
	for _, v := range r.Bad {
		if validVersion(tool, v) && len(bad) < maxBad {
			bad = append(bad, v)
		}
	}
	r.Bad = bad
	var un []pathVersion
	for _, p := range r.Unsuitable {
		if filepath.IsAbs(p.Path) && len(p.Path) <= maxPathLen && validVersion(tool, p.Version) && len(un) < maxUnsuitable {
			un = append(un, p)
		}
	}
	r.Unsuitable = un
	if r.Latest != "" && !validVersion(tool, r.Latest) {
		r.Latest = ""
	}
}

// valid reports a usable entry: a valid version, the expected file path, a SHA-256 and
// a size within the tool's limit.
func (e entry) valid(l layout, tool string) bool {
	if !validVersion(tool, e.Version) || e.File != l.rel(tool, e.Version) || e.Size <= 0 {
		return false
	}
	if tool == ToolYtDlp && e.Size > maxYtDlpAsset || tool == ToolDeno && e.Size > maxDenoUnpacked {
		return false
	}
	switch {
	case tool == ToolDeno && (e.Layout != "" || e.Manifest != ""):
		return false
	case e.Layout == layoutOnedir:
		if !validSHA256(e.Manifest) {
			return false
		}
	case e.Layout != "" && e.Layout != layoutOnefile || e.Manifest != "":
		return false
	}
	return validSHA256(e.SHA256)
}

// validSHA256 reports a SHA-256 written as 64 lowercase hex digits.
func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

// saveState writes s to tools.json of l atomically (temporary file, flush, rename).
func saveState(l layout, s stateFile) error {
	s.Version = stateVersion
	for _, r := range []*toolRecord{&s.YtDlp, &s.Deno} {
		if len(r.Bad) > maxBad {
			r.Bad = r.Bad[len(r.Bad)-maxBad:]
		}
		if len(r.Unsuitable) > maxUnsuitable {
			r.Unsuitable = r.Unsuitable[len(r.Unsuitable)-maxUnsuitable:]
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxStateFile {
		return errors.New("tools: tools.json would be too large")
	}
	return writeFileAtomic(l.state(), data, 0o600)
}

// writeFileAtomic replaces path with data: a temporary file in the same folder, given
// mode perm, flushed, then renamed over path. The temporary file is removed on failure.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tools-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameRetry(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// renameRetry renames, retrying for a moment on Windows, where a reader that has the
// target open (an antivirus scanner) makes the rename fail.
func renameRetry(from, to string) error {
	err := os.Rename(from, to)
	for i := 1; err != nil && runtime.GOOS == "windows" && i <= 10; i++ {
		time.Sleep(time.Duration(i) * 10 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}
