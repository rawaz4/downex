package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// An onedir yt-dlp (docs/SITE-MEDIA-SPEC.md §4.4) is its release zip unpacked into the
// version folder: the executable, written as yt-dlp(.exe) like a onefile copy, and its
// _internal/ folder. The zip is what the signed checksum list vouches for and is checked
// before anything is unpacked (unzipTree); what was unpacked is recorded in the folder's
// file list (manifestName: every file with its size and SHA-256), whose own SHA-256
// tools.json keeps. The copy runs only while every listed file still matches, just as a
// onefile copy runs only while its executable matches (verifyFolder: at discovery,
// before a rollback trial and before a folder is reused). Files the list does not name
// are not looked at.

const (
	manifestVersion = 1
	// maxManifestFile bounds the file list read back (maxTreeEntries short lines).
	maxManifestFile = 16 << 20
)

// fileManifest is the content of manifestName.
type fileManifest struct {
	Version int            `json:"version"`
	Files   []manifestFile `json:"files"`
}

// manifestFile is one unpacked file.
type manifestFile struct {
	Path   string `json:"path"` // slash-separated, relative to the version folder
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // lowercase hex
}

// writeManifest writes the file list of the copy unpacked into dir (a new file,
// flushed) and returns its SHA-256.
func writeManifest(dir string, files []manifestFile) (sum [sha256.Size]byte, err error) {
	data, err := json.Marshal(fileManifest{Version: manifestVersion, Files: files})
	if err != nil {
		return sum, err
	}
	f, err := os.OpenFile(filepath.Join(dir, manifestName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return sum, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return sum, err
	}
	return sha256.Sum256(data), nil
}

// verifyFolder reports whether the version folder dir holds the copy e describes: the
// executable exe with e's size and SHA-256 and, for an onedir copy, the file list with
// e's SHA-256 and every file it names with its size and SHA-256.
func verifyFolder(dir, exe string, e entry) bool {
	sum, size, err := fileSum(filepath.Join(dir, exe))
	if err != nil || size != e.Size || hex.EncodeToString(sum[:]) != e.SHA256 {
		return false
	}
	if e.Layout != layoutOnedir {
		return true
	}
	f, err := os.Open(filepath.Join(dir, manifestName))
	if err != nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxManifestFile+1))
	f.Close()
	if err != nil || len(data) > maxManifestFile {
		return false
	}
	if s := sha256.Sum256(data); hex.EncodeToString(s[:]) != e.Manifest {
		return false
	}
	var m fileManifest
	if json.Unmarshal(data, &m) != nil || m.Version != manifestVersion || len(m.Files) > maxTreeEntries {
		return false
	}
	hasExe := false
	for _, mf := range m.Files {
		p, isDir, err := archivePath(mf.Path)
		if err != nil || isDir || p != mf.Path || !validSHA256(mf.SHA256) {
			return false
		}
		if p == exe { // hashed above
			hasExe = mf.Size == e.Size && mf.SHA256 == e.SHA256
			continue
		}
		sum, size, err := fileSum(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || size != mf.Size || hex.EncodeToString(sum[:]) != mf.SHA256 {
			return false
		}
	}
	return hasExe
}
