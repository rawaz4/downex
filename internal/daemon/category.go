package daemon

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"downex/internal/transport"
)

// Category limits (docs/DESKTOP-SPEC.md §2).
const (
	MaxCategories          = 32  // categories in the settings
	MaxCategoryExtensions  = 200 // extensions of one category
	MaxCategoryNameRunes   = 64  // characters of a category name
	maxCategoryExtensionLn = 16  // bytes of one extension
)

// Category maps file extensions to a save folder.
type Category struct {
	Name       string   `json:"name"`       // unique (case-insensitively); also the folder name below default_dir
	Extensions []string `json:"extensions"` // lower-case, without dots
	Dir        string   `json:"dir"`        // absolute; "" = <default_dir>/<Name>
}

func (c Category) equal(o Category) bool {
	return c.Name == o.Name && c.Dir == o.Dir && slices.Equal(c.Extensions, o.Extensions)
}

// Extensions of the default categories (docs/DESKTOP-SPEC.md §2).
const (
	videoExtensions    = "mp4 mkv avi mov webm wmv flv m4v mpg mpeg ts 3gp"
	audioExtensions    = "mp3 flac m4a aac wav ogg opus wma"
	archiveExtensions  = "zip rar 7z gz xz bz2 tar tgz zst iso img dmg"
	documentExtensions = "pdf epub doc docx xls xlsx ppt pptx odt ods txt csv md"
	programExtensions  = "exe msi msix appx apk deb rpm appimage bat cmd ps1 jar"
)

// DefaultCategories returns the default categories, a new slice on every call.
func DefaultCategories() []Category {
	return []Category{
		{Name: "Video", Extensions: strings.Fields(videoExtensions)},
		{Name: "Audio", Extensions: strings.Fields(audioExtensions)},
		{Name: "Archives", Extensions: strings.Fields(archiveExtensions)},
		{Name: "Documents", Extensions: strings.Fields(documentExtensions)},
		{Name: "Programs", Extensions: strings.Fields(programExtensions)},
	}
}

func cloneCategories(cs []Category) []Category {
	if cs == nil {
		return nil
	}
	out := make([]Category, len(cs))
	for i, c := range cs {
		c.Extensions = slices.Clone(c.Extensions)
		if c.Extensions == nil {
			c.Extensions = []string{}
		}
		out[i] = c
	}
	return out
}

// normalizeCategories validates categories from a client or the settings file and
// returns them normalised in a new slice (never nil): extensions are trimmed,
// lower-cased, stripped of a leading dot and de-duplicated, and dirs cleaned. Names
// must be unique ignoring case, at most MaxCategoryNameRunes characters and usable as
// a folder name as they are; dirs must be absolute local directories as for Add
// (docs/DAEMON-SPEC.md §4; defaultDir is trusted); extensions are [a-z0-9]{1,16}.
func normalizeCategories(cs []Category, defaultDir string) ([]Category, error) {
	if len(cs) > MaxCategories {
		return nil, fmt.Errorf("at most %d categories are allowed", MaxCategories)
	}
	out := make([]Category, 0, len(cs))
	seen := make(map[string]bool, len(cs))
	for i, c := range cs {
		if err := checkCategoryName(c.Name); err != nil {
			return nil, fmt.Errorf("categories[%d]: %w", i, err)
		}
		key := strings.ToLower(c.Name)
		if seen[key] {
			return nil, fmt.Errorf("categories[%d]: the name %q is used twice", i, c.Name)
		}
		seen[key] = true
		if len(c.Extensions) > MaxCategoryExtensions {
			return nil, fmt.Errorf("category %q: at most %d extensions are allowed", c.Name, MaxCategoryExtensions)
		}
		n := Category{Name: c.Name, Extensions: make([]string, 0, len(c.Extensions))}
		for _, e := range c.Extensions {
			ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
			if !validExtension(ext) {
				return nil, fmt.Errorf("category %q: extension %q must be 1-%d letters or digits", c.Name,
					truncate(e, 32), maxCategoryExtensionLn)
			}
			if !slices.Contains(n.Extensions, ext) {
				n.Extensions = append(n.Extensions, ext)
			}
		}
		if c.Dir != "" {
			dir, err := checkDir(c.Dir, defaultDir)
			if err != nil {
				return nil, fmt.Errorf("category %q: %w", c.Name, err)
			}
			n.Dir = dir
		}
		out = append(out, n)
	}
	return out, nil
}

// checkCategoryName accepts names that are valid folder names on Windows and Linux as
// they are (transport.SanitizeFilename leaves them unchanged): never empty, "." or "..",
// without separators, reserved device names or trailing dots and spaces.
func checkCategoryName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a category name is empty")
	case utf8.RuneCountInString(name) > MaxCategoryNameRunes:
		return fmt.Errorf("the category name %q is longer than %d characters", truncate(name, 80), MaxCategoryNameRunes)
	case !utf8.ValidString(name) || transport.SanitizeFilename(name) != name:
		return fmt.Errorf("the category name %q cannot be used as a folder name", truncate(name, 80))
	}
	return nil
}

func validExtension(ext string) bool {
	if ext == "" || len(ext) > maxCategoryExtensionLn {
		return false
	}
	for i := 0; i < len(ext); i++ {
		if c := ext[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// truncate shortens s to at most n bytes on a rune boundary, for messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// fileExtension returns the lower-case extension of a file name ("" when it has none,
// or one that no category could list).
func fileExtension(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	ext := strings.ToLower(name[i+1:])
	if !validExtension(ext) {
		return ""
	}
	return ext
}

// urlFileName returns the last non-empty segment of rawURL's path, decoded ("" when
// there is none).
func urlFileName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	return p[strings.LastIndexByte(p, '/')+1:]
}

// categoryForExtension returns the first category listing ext, or nil.
func categoryForExtension(cs []Category, ext string) *Category {
	if ext == "" {
		return nil
	}
	for i := range cs {
		if slices.Contains(cs[i].Extensions, ext) {
			return &cs[i]
		}
	}
	return nil
}

// chooseCategory picks the category of a new download (docs/DESKTOP-SPEC.md §2): by the
// extension of the requested filename, else of the URL path's last segment, else by
// the MIME hint. The first matching category wins; "" means none.
func chooseCategory(cs []Category, filename, rawURL, mime string) string {
	name := filename
	if fileExtension(name) == "" {
		name = urlFileName(rawURL)
	}
	if c := categoryForExtension(cs, fileExtension(name)); c != nil {
		return c.Name
	}
	if exts := mimeExtensions(mime); len(exts) > 0 {
		for i := range cs {
			for _, e := range exts {
				if slices.Contains(cs[i].Extensions, e) {
					return cs[i].Name
				}
			}
		}
	}
	return ""
}

// mimeTypeExtensions maps common MIME types of downloads to the extension they stand for.
var mimeTypeExtensions = map[string]string{
	"application/zip":               "zip",
	"application/x-zip-compressed":  "zip",
	"application/vnd.rar":           "rar",
	"application/x-rar-compressed":  "rar",
	"application/x-7z-compressed":   "7z",
	"application/gzip":              "gz",
	"application/x-gzip":            "gz",
	"application/x-xz":              "xz",
	"application/x-bzip2":           "bz2",
	"application/x-tar":             "tar",
	"application/zstd":              "zst",
	"application/x-iso9660-image":   "iso",
	"application/x-apple-diskimage": "dmg",

	"application/pdf":               "pdf",
	"application/epub+zip":          "epub",
	"application/msword":            "doc",
	"application/vnd.ms-excel":      "xls",
	"application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.oasis.opendocument.text":                                   "odt",
	"application/vnd.oasis.opendocument.spreadsheet":                            "ods",
	"text/plain":    "txt",
	"text/csv":      "csv",
	"text/markdown": "md",

	"application/x-msdownload":                      "exe",
	"application/x-msdos-program":                   "exe",
	"application/x-dosexec":                         "exe",
	"application/vnd.microsoft.portable-executable": "exe",
	"application/x-msi":                             "msi",
	"application/x-ms-installer":                    "msi",
	"application/msix":                              "msix",
	"application/vnd.ms-appx":                       "appx",
	"application/vnd.android.package-archive":       "apk",
	"application/vnd.debian.binary-package":         "deb",
	"application/x-debian-package":                  "deb",
	"application/x-rpm":                             "rpm",
	"application/x-redhat-package-manager":          "rpm",
	"application/java-archive":                      "jar",
	"application/x-java-archive":                    "jar",

	"video/mp4":        "mp4",
	"video/x-matroska": "mkv",
	"video/webm":       "webm",
	"video/quicktime":  "mov",
	"video/x-msvideo":  "avi",
	"video/x-ms-wmv":   "wmv",
	"video/x-flv":      "flv",
	"video/mpeg":       "mpg",
	"video/mp2t":       "ts",
	"video/3gpp":       "3gp",
	"audio/mpeg":       "mp3",
	"audio/flac":       "flac",
	"audio/mp4":        "m4a",
	"audio/aac":        "aac",
	"audio/wav":        "wav",
	"audio/x-wav":      "wav",
	"audio/ogg":        "ogg",
	"audio/opus":       "opus",
	"audio/x-ms-wma":   "wma",
}

// mimeExtensions returns the extensions a MIME hint (normalised, see validate.MIME)
// stands for: the specific type's, then for any video/* or audio/* type the extensions
// of the default Video or Audio category, so that unlisted subtypes still find them.
func mimeExtensions(mime string) []string {
	var out []string
	if ext, ok := mimeTypeExtensions[mime]; ok {
		out = append(out, ext)
	}
	switch {
	case strings.HasPrefix(mime, "video/"):
		out = append(out, strings.Fields(videoExtensions)...)
	case strings.HasPrefix(mime, "audio/"):
		out = append(out, strings.Fields(audioExtensions)...)
	}
	return out
}

// categoryFolder returns the folder of the category named name: its dir, else
// <default_dir>/<name>. ok is false when no category has that name or no default
// directory is known for it.
func (s *Settings) categoryFolder(name string) (dir string, ok bool) {
	if name == "" {
		return "", false
	}
	for _, c := range s.Categories {
		if c.Name != name {
			continue
		}
		if c.Dir != "" {
			return c.Dir, true
		}
		if s.DefaultDir == "" {
			return "", false
		}
		return filepath.Join(s.DefaultDir, c.Name), true
	}
	return "", false
}

// categoryOfFolder returns the first category whose folder is dir ("" = none).
func (s *Settings) categoryOfFolder(dir string) string {
	for _, c := range s.Categories {
		if f, ok := s.categoryFolder(c.Name); ok && samePath(filepath.Clean(f), filepath.Clean(dir)) {
			return c.Name
		}
	}
	return ""
}
