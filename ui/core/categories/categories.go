// Package categories holds file kinds (a row's icon, judged from the extension), categories
// (the sidebar's groups: Settings.categories, DESKTOP-SPEC §2, plus "Other") and the status
// filters of the sidebar, ported from ui/frontend/src/lib/categories.ts.
//
// The functions are pure. format.ts cached names per Download object; here a caller that
// sorts or searches many rows keeps the names it derived for a message it has not replaced.
package categories

import (
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/text"
)

// FileKind is the kind of file a name has.
type FileKind string

// The file kinds.
const (
	Video     FileKind = "video"
	Audio     FileKind = "audio"
	Archives  FileKind = "archives"
	Documents FileKind = "documents"
	Programs  FileKind = "programs"
	Other     FileKind = "other"

	// AllKinds is what Index.KindOf gives the All filter; it is no file's kind.
	AllKinds FileKind = "all"
)

// FileKinds lists the kinds in their order.
func FileKinds() []FileKind { return []FileKind{Video, Audio, Archives, Documents, Programs, Other} }

var extensions = [...]struct {
	kind FileKind
	exts []string
}{
	{Video, []string{"mp4", "m4v", "mkv", "webm", "avi", "mov", "wmv", "flv", "mpg", "mpeg", "ts", "m2ts", "3gp", "ogv"}},
	{Audio, []string{"mp3", "flac", "wav", "aac", "ogg", "oga", "opus", "m4a", "wma", "aif", "aiff", "alac", "mka"}},
	{Archives, []string{"zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "tbz2", "xz", "txz", "zst", "lz", "lzma", "cab", "iso", "img"}},
	{Documents, []string{"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "rtf", "txt", "md", "csv", "epub", "djvu"}},
	{Programs, []string{"exe", "msi", "msix", "appx", "deb", "rpm", "apk", "appimage", "flatpak", "snap", "dmg", "pkg", "jar", "bat", "cmd", "sh", "run"}},
}

var byExtension = func() map[string]FileKind {
	m := map[string]FileKind{}
	for _, e := range extensions {
		for _, ext := range e.exts {
			m[ext] = e.kind
		}
	}
	return m
}()

// ExtensionOf is the lower-case extension of the last path component ("" when none). It
// ignores a query or fragment.
func ExtensionOf(name string) string {
	base := name
	if cut := strings.IndexAny(base, "?#"); cut >= 0 {
		base = base[:cut]
	}
	if slash := strings.LastIndexAny(base, `/\`); slash >= 0 {
		base = base[slash+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return ""
	}
	return text.LowerJS(base[dot+1:])
}

// ClassifyName is the kind of file a name has, by its extension (independent of the user's
// categories).
func ClassifyName(name string) FileKind {
	if k, ok := byExtension[ExtensionOf(name)]; ok {
		return k
	}
	return Other
}

// RawName is the name as received: the filename, else the decoded last URL path segment,
// else the URL.
func RawName(d *downexv1.Download) string {
	if f := d.GetFilename(); f != "" {
		return f
	}
	u := d.GetFinalUrl()
	if u == "" {
		u = d.GetUrl()
	}
	return nameFromURL(u)
}

func nameFromURL(rawURL string) string {
	if u, err := text.ParseURL(rawURL); err == nil {
		p := u.Pathname()
		if last, ok := text.DecodeURIComponent(p[strings.LastIndexByte(p, '/')+1:]); ok && last != "" {
			return last
		}
	}
	return rawURL
}

// DisplayName is the name to show (and to sort and search by). Bidi control characters are
// made visible ("⟨U+202E⟩") so a name cannot disguise its extension; actions use the download
// id, never this string.
func DisplayName(d *downexv1.Download) string { return text.EscapeBidi(RawName(d)) }

// KindOf is the file kind of a download, by its raw name.
func KindOf(d *downexv1.Download) FileKind { return ClassifyName(RawName(d)) }

// executable lists the extensions the system may run rather than display when the file is
// opened.
var executable = func() map[string]bool {
	m := map[string]bool{}
	for _, e := range extensions {
		if e.kind == Programs {
			for _, ext := range e.exts {
				m[ext] = true
			}
		}
	}
	for _, ext := range []string{"appxbundle", "com", "scr", "pif", "cpl", "msc", "hta", "js", "jse", "vbs", "vbe",
		"wsf", "wsh", "ps1", "psm1", "lnk", "url", "reg", "command", "app", "desktop"} {
		m[ext] = true
	}
	return m
}()

// IsExecutableName reports whether opening the file could run code (judged from the real,
// unescaped name).
func IsExecutableName(name string) bool { return executable[ExtensionOf(name)] }

// ---- categories ------------------------------------------------------------------

// Filter is the sidebar's category filter: "all", "other" (none of the configured
// categories), or a configured category ("cat:" + its name, which may be anything). A
// download's bucket is "other" or a category's key.
type Filter string

// The filters that are not a category.
const (
	All          Filter = "all"
	OtherFilter  Filter = "other"
	categoryHead        = "cat:"
)

// The labels of All and OtherFilter.
const (
	AllLabel   = "All downloads"
	OtherLabel = "Other"
)

// Key is the filter of a configured category.
func Key(name string) Filter { return Filter(categoryHead + name) }

// Entry is a configured category in filters and counts.
type Entry struct {
	Key  Filter
	Name string
	// Kind decides the icon and colour shown for it.
	Kind FileKind
}

// NormalizeExtension normalises an extension as the daemon stores it: lower case, no
// leading dots.
func NormalizeExtension(ext string) string {
	return strings.TrimLeft(text.LowerJS(text.TrimJS(ext)), ".")
}

// CategoryKind is the kind of a category: its name when that is one of the built-in kinds
// ("Video"), else the kind most of its extensions have, else "other".
func CategoryKind(name string, exts []string) FileKind {
	named := FileKind(text.LowerJS(text.TrimJS(name)))
	for _, k := range FileKinds() {
		if named == k {
			return k
		}
	}
	votes := map[FileKind]int{}
	var order []FileKind
	for _, ext := range exts {
		if k, ok := byExtension[NormalizeExtension(ext)]; ok {
			if votes[k] == 0 {
				order = append(order, k)
			}
			votes[k]++
		}
	}
	best, most := Other, 0
	for _, k := range order {
		if votes[k] > most {
			best, most = k, votes[k]
		}
	}
	return best
}

// Index groups downloads by the configured categories. A download belongs to the category
// the daemon chose for it (Download.category) while that category exists; one from before
// categories, or whose category was renamed or removed, is grouped by its extension like the
// daemon would (the first category listing it wins); the rest is "other".
type Index struct {
	entries     []Entry
	byName      map[string]Entry
	byExtension map[string]Filter
}

// NewIndex indexes the configured categories in their order. The daemon accepts neither an
// empty name nor a duplicate; a duplicate keeps the first entry.
func NewIndex(cats []*downexv1.Category) *Index {
	ix := &Index{byName: map[string]Entry{}, byExtension: map[string]Filter{}}
	for _, c := range cats {
		name := c.GetName()
		if _, dup := ix.byName[name]; name == "" || dup {
			continue
		}
		e := Entry{Key: Key(name), Name: name, Kind: CategoryKind(name, c.GetExtensions())}
		ix.byName[name] = e
		ix.entries = append(ix.entries, e)
		for _, ext := range c.GetExtensions() {
			n := NormalizeExtension(ext)
			if _, taken := ix.byExtension[n]; n != "" && !taken {
				ix.byExtension[n] = e.Key
			}
		}
	}
	return ix
}

// Entries lists the configured categories in order, without duplicates.
func (ix *Index) Entries() []Entry { return append([]Entry(nil), ix.entries...) }

// BucketOf is where a download is counted: its category's key, or OtherFilter.
func (ix *Index) BucketOf(d *downexv1.Download) Filter {
	if c := d.GetCategory(); c != "" {
		if e, ok := ix.byName[c]; ok {
			return e.Key
		}
	}
	if k, ok := ix.byExtension[ExtensionOf(RawName(d))]; ok {
		return k
	}
	return OtherFilter
}

// Has reports whether the filter still names something (a removed category does not).
func (ix *Index) Has(f Filter) bool {
	if f == All || f == OtherFilter {
		return true
	}
	_, ok := ix.byName[f.name()]
	return ok
}

// name is what follows the filter's "cat:" (format.ts's filter.slice(4)).
func (f Filter) name() string {
	if len(f) < len(categoryHead) {
		return ""
	}
	return string(f[len(categoryHead):])
}

// LabelOf is the filter's label: "All downloads", "Other", or the category's name.
func (ix *Index) LabelOf(f Filter) string {
	switch f {
	case All:
		return AllLabel
	case OtherFilter:
		return OtherLabel
	}
	return f.name()
}

// KindOf is the kind shown for a filter: AllKinds for All, else a file kind.
func (ix *Index) KindOf(f Filter) FileKind {
	switch f {
	case All:
		return AllKinds
	case OtherFilter:
		return Other
	}
	if e, ok := ix.byName[f.name()]; ok {
		return e.Kind
	}
	return Other
}

// ---- status filters --------------------------------------------------------------

// Status is a status filter of the sidebar.
type Status string

// The status filters.
const (
	AnyStatus Status = "any"
	Awaiting  Status = "awaiting"
	Active    Status = "active"
	Queued    Status = "queued"
	Paused    Status = "paused"
	Completed Status = "completed"
	Failed    Status = "failed"
)

// StatusFilters lists the filters other than AnyStatus in their order.
func StatusFilters() []Status { return []Status{Awaiting, Active, Queued, Paused, Completed, Failed} }

// StatusLabel is a status filter's label.
func StatusLabel(s Status) string {
	switch s {
	case AnyStatus:
		return "Any status"
	case Awaiting:
		return "To confirm"
	case Active:
		return "Active"
	case Queued:
		return "Queued"
	case Paused:
		return "Paused"
	case Completed:
		return "Completed"
	case Failed:
		return "Failed"
	}
	return ""
}

// StatusBucket is the status filter a state belongs to; ok is false for an unspecified state.
func StatusBucket(state downexv1.DownloadState) (s Status, ok bool) {
	switch state {
	case downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION:
		return Awaiting, true
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return Active, true
	case downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
		return Queued, true
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return Paused, true
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return Completed, true
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return Failed, true
	}
	return "", false
}

// MatchesStatus reports whether a state passes the status filter.
func MatchesStatus(state downexv1.DownloadState, f Status) bool {
	if f == AnyStatus {
		return true
	}
	s, ok := StatusBucket(state)
	return ok && s == f
}
