// Package validate checks the forms of the Add, Settings and site-login editors, ported from
// ui/frontend/src/lib/validate.ts. It mirrors DAEMON-SPEC §3–§4 so the user gets immediate
// feedback; downexd stays the authority and validates again. Every message is today's, byte for
// byte. A check returns "" when the value is fine.
package validate

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The client-input limits the window checks before downexd does (api.ts LIMITS; DAEMON-SPEC
// §3–§4, DESKTOP-SPEC §2, M6-SPEC §1, SITE-MEDIA-SPEC §3.8).
const (
	MinConnections           = 1
	MaxConnections           = 32
	MinConcurrent            = 1
	MaxConcurrent            = 16
	MaxURLBytes              = 32 * 1024
	MaxFilenameLength        = 255
	MaxCategories            = 32
	MaxCategoryNameLength    = 64
	MaxExtensionsPerCategory = 200
	MaxExtensionLength       = 16
	MaxQueueNameLength       = 64
	MaxQueueConcurrent       = 16
	// MaxRepeatMinutes is a schedule's longest repeat interval: a day.
	MaxRepeatMinutes = 1440
	// MaxScanArgsLength bounds the scanner's arguments (Settings → Integration).
	MaxScanArgsLength = 4096
	// MinRequestBytes and MaxRequestBytes bound a host rule's longest ranged request: 1 MiB to
	// 1 GiB, 0 = no cap.
	MinRequestBytes = 1024 * 1024
	MaxRequestBytes = 1024 * 1024 * 1024
)

const (
	noLimit  = "Enter a number, or leave empty for no limit"
	ellipsis = "\u2026"
)

// CheckURL checks an address to download: at most MaxURLBytes, http or https, with a host.
// reason says why not ("is not a valid URL"); ok when it is fine.
func CheckURL(input string) (reason string, ok bool) {
	if len(input) > MaxURLBytes {
		return "is longer than " + formatCount(MaxURLBytes) + " bytes", false
	}
	u, parsed := parseURL(input)
	switch {
	case !parsed:
		return "is not a valid URL", false
	case u.scheme != "http" && u.scheme != "https":
		return "must start with http:// or https://", false
	case u.host == "":
		return "has no host", false
	}
	return "", true
}

// SplitURLs splits a text area into URLs (one per line or separated by white space),
// de-duplicated in order.
func SplitURLs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, token := range strings.FieldsFunc(text, isJSSpace) {
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

// CheckDir checks a folder: an absolute local path (`X:\…` on Windows, `/…` elsewhere) or
// none. UNC paths are refused.
func CheckDir(dir string) string {
	switch {
	case dir == "":
		return ""
	case len(dir) >= 2 && isSep(dir[0]) && isSep(dir[1]):
		return "Network (UNC) and device paths are not supported"
	case len(dir) >= 3 && isAlpha(dir[0]) && dir[1] == ':' && isSep(dir[2]), strings.HasPrefix(dir, "/"):
		return ""
	}
	return "Use an absolute folder path"
}

func isSep(c byte) bool { return c == '\\' || c == '/' }

// CheckFilename checks a file name: no separators or control characters, not "." or "..", at
// most MaxFilenameLength characters (UTF-16 code units, as today's window counts them).
func CheckFilename(name string) string {
	switch {
	case name == "":
		return ""
	case strings.ContainsAny(name, `\/`):
		return `A file name cannot contain / or \`
	case name == "." || name == "..":
		return "Not a valid file name"
	case hasControl(name):
		return "A file name cannot contain control characters"
	case utf16Len(name) > MaxFilenameLength:
		return "Keep the file name under " + strconv.Itoa(MaxFilenameLength+1) + " characters"
	}
	return ""
}

// CheckIntRange checks a whole number from min to max; label names the field in the message.
func CheckIntRange(text string, min, max int, label string) string {
	t := jsTrim(text)
	if !allDigits(t) {
		return label + " must be a whole number from " + strconv.Itoa(min) + " to " + strconv.Itoa(max)
	}
	if n := number(t); n < float64(min) || n > float64(max) {
		return label + " must be from " + strconv.Itoa(min) + " to " + strconv.Itoa(max)
	}
	return ""
}

// number is Number(text) of a text of digits (and at most one dot).
func number(text string) float64 {
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return math.Inf(1) // only out of range: more digits than a float64 holds
	}
	return n
}

// hasControl reports a C0 control character or DEL.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// AddForm is the Add dialog's form for files.
type AddForm struct {
	URLs        string
	Dir         string
	Filename    string
	Connections string
	SpeedValue  string
	SpeedUnit   RateUnit
	StartPaused bool
	// QueueID is the queue to add to (M6); 0 = the main queue.
	QueueID int64
}

// AddFormErrors are the Add form's messages by field ("" = fine).
type AddFormErrors struct {
	URLs, Dir, Filename, Connections, Speed string
}

// Empty reports no error.
func (e AddFormErrors) Empty() bool { return e == AddFormErrors{} }

// maxListedBadURLs is how many bad addresses the message names.
const maxListedBadURLs = 3

// ValidateAddForm checks the Add form and makes one request per address; with errors, ok is
// false and requests nil.
func ValidateAddForm(form AddForm) (requests []*downexv1.AddRequest, errs AddFormErrors, ok bool) {
	urls := SplitURLs(form.URLs)
	if len(urls) == 0 {
		errs.URLs = "Enter at least one URL"
	} else {
		var listed []string
		bad := 0
		for _, u := range urls {
			reason, fine := CheckURL(u)
			if fine {
				continue
			}
			bad++
			if len(listed) < maxListedBadURLs {
				listed = append(listed, `"`+truncate(u, 60)+`" `+reason)
			}
		}
		if bad > 0 {
			errs.URLs = strings.Join(listed, "; ")
			if bad > maxListedBadURLs {
				errs.URLs += " (+" + strconv.Itoa(bad-maxListedBadURLs) + " more)"
			}
		}
	}
	dir := jsTrim(form.Dir)
	errs.Dir = CheckDir(dir)
	filename := ""
	if len(urls) <= 1 {
		filename = jsTrim(form.Filename)
	}
	errs.Filename = CheckFilename(filename)
	errs.Connections = CheckIntRange(form.Connections, MinConnections, MaxConnections, "Connections")
	speed, speedOK := parseRate(form.SpeedValue, form.SpeedUnit, 0)
	if !speedOK {
		errs.Speed = noLimit
	}
	if !errs.Empty() {
		return nil, errs, false
	}
	maxConnections := int32(number(jsTrim(form.Connections)))
	for _, u := range urls {
		requests = append(requests, &downexv1.AddRequest{
			Url:            u,
			Dir:            dir,
			Filename:       filename,
			MaxConnections: maxConnections,
			SpeedLimit:     speed,
			StartPaused:    form.StartPaused,
			Source:         downexv1.AddSource_ADD_SOURCE_UI,
			QueueId:        max(form.QueueID, 0),
		})
	}
	return requests, errs, true
}

// StreamForm is the Add dialog's "Video or stream" mode: one address, an HLS/DASH manifest
// (MEDIA-SPEC §6) or a web page whose video downexd resolves with yt-dlp (SITE-MEDIA-SPEC §5.4).
type StreamForm struct {
	URL, Dir, Filename string
}

// StreamFormErrors are its messages by field.
type StreamFormErrors struct {
	URL, Dir, Filename string
}

// Empty reports no error.
func (e StreamFormErrors) Empty() bool { return e == StreamFormErrors{} }

// ValidateStreamForm checks the stream form; the form it returns is trimmed.
func ValidateStreamForm(form StreamForm) (StreamForm, StreamFormErrors, bool) {
	var errs StreamFormErrors
	out := StreamForm{URL: jsTrim(form.URL), Dir: jsTrim(form.Dir), Filename: jsTrim(form.Filename)}
	if out.URL == "" {
		errs.URL = "Enter the address of a video page, or of an HLS (.m3u8) or DASH (.mpd) manifest"
	} else if reason, ok := CheckURL(out.URL); !ok {
		errs.URL = "The address " + reason
	}
	errs.Dir = CheckDir(out.Dir)
	errs.Filename = CheckFilename(out.Filename)
	if !errs.Empty() {
		return StreamForm{}, errs, false
	}
	return out, errs, true
}

// CategoryDraft is a category as the Settings dialog edits it.
type CategoryDraft struct {
	// Key identifies the editor row (never sent).
	Key  int
	Name string
	// Extensions as typed: separated by spaces or commas; leading dots and capitals are allowed.
	Extensions string
	Dir        string
}

// CategoryDraftErrors are a category row's messages.
type CategoryDraftErrors struct {
	Name, Extensions, Dir string
}

// Empty reports no error.
func (e CategoryDraftErrors) Empty() bool { return e == CategoryDraftErrors{} }

// ParseExtensions splits typed extensions into downexd's form (lower case, no dots),
// de-duplicated in order, and the tokens that are not extensions.
func ParseExtensions(text string) (extensions, invalid []string) {
	for _, token := range strings.FieldsFunc(text, func(r rune) bool { return isJSSpace(r) || r == ',' || r == ';' }) {
		ext := normalizeExtension(token)
		switch {
		case utf16Len(ext) > MaxExtensionLength || !isExtension(ext):
			invalid = append(invalid, token)
		case !slices.Contains(extensions, ext):
			extensions = append(extensions, ext)
		}
	}
	return extensions, invalid
}

// isExtension is /^[a-z0-9]+$/.
func isExtension(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || isDigit(c)) {
			return false
		}
	}
	return s != ""
}

// FormatExtensions shows extensions in the editor.
func FormatExtensions(extensions []string) string { return strings.Join(extensions, " ") }

// CategoryDrafts are the editor rows of categories, keyed from firstKey.
func CategoryDrafts(categories []*downexv1.Category, firstKey int) []CategoryDraft {
	out := make([]CategoryDraft, len(categories))
	for i, c := range categories {
		out[i] = CategoryDraft{Key: firstKey + i, Name: c.GetName(), Extensions: FormatExtensions(c.GetExtensions()), Dir: c.GetDir()}
	}
	return out
}

// CategoriesResult is the checked list of categories: the categories when OK, else the
// message about the list (List) and the rows' messages by key.
type CategoriesResult struct {
	OK         bool
	Categories []*downexv1.Category
	List       string
	Rows       map[int]CategoryDraftErrors
}

// ValidateCategories checks the categories as DESKTOP-SPEC §2 asks: unique non-empty names of
// at most 64 characters (unique ignoring case too, since their folders would be the same on
// Windows), absolute folders or none, at most 32 categories and 200 extensions each.
func ValidateCategories(drafts []CategoryDraft) CategoriesResult {
	rows := map[int]CategoryDraftErrors{}
	var categories []*downexv1.Category
	seen := map[string]bool{}
	list := ""
	if len(drafts) > MaxCategories {
		list = "Keep at most " + strconv.Itoa(MaxCategories) + " categories"
	}
	for _, draft := range drafts {
		var row CategoryDraftErrors
		name := jsTrim(draft.Name)
		switch {
		case name == "":
			row.Name = "Enter a name"
		case utf8.RuneCountInString(name) > MaxCategoryNameLength:
			row.Name = "Keep the name under " + strconv.Itoa(MaxCategoryNameLength+1) + " characters"
		case hasControl(name):
			row.Name = "A name cannot contain control characters"
		case seen[jsLower(name)]:
			row.Name = "Another category has this name"
		}
		seen[jsLower(name)] = true

		extensions, invalid := ParseExtensions(draft.Extensions)
		if len(invalid) > 0 {
			quoted := make([]string, 0, 3)
			for _, x := range invalid[:min(3, len(invalid))] {
				quoted = append(quoted, `"`+truncate(x, 20)+`"`)
			}
			row.Extensions = "Not an extension: " + strings.Join(quoted, ", ") + " (letters and digits, up to " + strconv.Itoa(MaxExtensionLength) + ")"
		} else if len(extensions) > MaxExtensionsPerCategory {
			row.Extensions = "Keep at most " + strconv.Itoa(MaxExtensionsPerCategory) + " extensions"
		}

		dir := jsTrim(draft.Dir)
		row.Dir = CheckDir(dir)

		if !row.Empty() {
			rows[draft.Key] = row
		}
		if extensions == nil {
			extensions = []string{}
		}
		categories = append(categories, &downexv1.Category{Name: name, Extensions: extensions, Dir: dir})
	}
	if list != "" || len(rows) > 0 {
		return CategoriesResult{List: list, Rows: rows}
	}
	return CategoriesResult{OK: true, Categories: categories}
}

// truncate is s cut to n UTF-16 code units with "…" in the last place.
func truncate(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n-1])) + ellipsis
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
