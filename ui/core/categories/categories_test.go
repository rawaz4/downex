package categories_test

import (
	"slices"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	. "downex/ui/core/format/formattest"
)

// The tables of ui/frontend/src/lib/categories.test.ts and the categories cases of
// text.test.ts, case for case.

// defaultCategories is lib/api.ts's DEFAULT_CATEGORIES.
func defaultCategories() []*downexv1.Category {
	return []*downexv1.Category{
		{Name: "Video", Extensions: []string{"mp4", "mkv", "avi", "mov", "webm", "wmv", "flv", "m4v", "mpg", "mpeg", "ts", "3gp"}},
		{Name: "Audio", Extensions: []string{"mp3", "flac", "m4a", "aac", "wav", "ogg", "opus", "wma"}},
		{Name: "Archives", Extensions: []string{"zip", "rar", "7z", "gz", "xz", "bz2", "tar", "tgz", "zst", "iso", "img", "dmg"}},
		{Name: "Documents", Extensions: []string{"pdf", "epub", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "txt", "csv", "md"}},
		{Name: "Programs", Extensions: []string{"exe", "msi", "msix", "appx", "apk", "deb", "rpm", "appimage", "bat", "cmd", "ps1", "jar"}},
	}
}

func TestExtensionOf(t *testing.T) {
	for _, c := range [][2]string{
		{"movie.MP4", "mp4"},
		{"archive.tar.gz", "gz"},
		{".bashrc", ""},
		{"file.", ""},
		{"noext", ""},
		{"https://example.com/a/b.pdf?x=1.zip#frag.exe", "pdf"},
		{`C:\Users\you\setup.EXE`, "exe"},
		{"/home/you/dir.v2/readme", ""},
		{"Show #37 (1h41m01s-1h57m22s).mp4", "mp4"},
		{"game#1.exe", "exe"},
		{"what?.pdf", "pdf"},
		{`C:\Users\you\Video\Show #37.mp4`, "mp4"},
	} {
		if got := categories.ExtensionOf(c[0]); got != c[1] {
			t.Errorf("ExtensionOf(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestClassifyName(t *testing.T) {
	for _, c := range []struct {
		name string
		kind categories.FileKind
	}{
		{"clip.mkv", categories.Video},
		{"song.FLAC", categories.Audio},
		{"backup.7z", categories.Archives},
		{"dataset.tar.xz", categories.Archives},
		{"report.pdf", categories.Documents},
		{"sheet.xlsx", categories.Documents},
		{"setup.msi", categories.Programs},
		{"tool_1.0_amd64.deb", categories.Programs},
		{"editor.AppImage", categories.Programs},
		{"firmware.bin", categories.Other},
		{"README", categories.Other},
	} {
		if got := categories.ClassifyName(c.name); got != c.kind {
			t.Errorf("ClassifyName(%q) = %q, want %q", c.name, got, c.kind)
		}
	}
}

func TestDisplayNamePrefersTheFilename(t *testing.T) {
	d := Download(1, func(d *downexv1.Download) { d.Filename = "talk.webm"; d.Url = "https://example.com/x.zip" })
	if got := categories.DisplayName(d); got != "talk.webm" {
		t.Errorf("DisplayName = %q", got)
	}
	if got := categories.KindOf(d); got != categories.Video {
		t.Errorf("KindOf = %q", got)
	}
}

func TestDisplayNameFallsBackToTheURL(t *testing.T) {
	d := Download(2, func(d *downexv1.Download) {
		d.Filename = ""
		d.Url = "https://example.com/get?id=1"
		d.FinalUrl = "https://cdn.example.com/files/my%20notes.pdf"
	})
	if got := categories.DisplayName(d); got != "my notes.pdf" {
		t.Errorf("DisplayName = %q", got)
	}
	if got := categories.KindOf(d); got != categories.Documents {
		t.Errorf("KindOf = %q", got)
	}
	bare := Download(3, func(d *downexv1.Download) { d.Filename = ""; d.Url = "https://example.com/"; d.FinalUrl = "" })
	if got := categories.DisplayName(bare); got != "https://example.com/" {
		t.Errorf("DisplayName = %q", got)
	}
	if got := categories.KindOf(bare); got != categories.Other {
		t.Errorf("KindOf = %q", got)
	}
}

const rlo = "\u202e"

func TestBidiControlsInNames(t *testing.T) {
	// shows the escaped name but keeps the raw one for classification and actions (text.test.ts)
	d := Download(1, func(d *downexv1.Download) { d.Filename = ""; d.Url = "https://example.org/pub/invoice%E2%80%AEfdp.exe" })
	if got := categories.RawName(d); got != "invoice"+rlo+"fdp.exe" {
		t.Errorf("RawName = %q", got)
	}
	if got := categories.DisplayName(d); got != "invoice⟨U+202E⟩fdp.exe" {
		t.Errorf("DisplayName = %q", got)
	}
	if got := categories.KindOf(d); got != categories.Programs {
		t.Errorf("KindOf = %q", got)
	}
	if !categories.IsExecutableName(categories.RawName(d)) {
		t.Error("IsExecutableName(raw) = false")
	}
	named := Download(2, func(d *downexv1.Download) { d.Filename = "photo" + rlo + "gpj.scr" })
	if got := categories.DisplayName(named); got != "photo⟨U+202E⟩gpj.scr" {
		t.Errorf("DisplayName = %q", got)
	}
	if !categories.IsExecutableName(named.Filename) {
		t.Error("IsExecutableName(scr) = false")
	}
	if categories.IsExecutableName("report.pdf") {
		t.Error("IsExecutableName(report.pdf) = true")
	}
}

func TestStatusBuckets(t *testing.T) {
	type sc = struct {
		state downexv1.DownloadState
		want  categories.Status
		ok    bool
	}
	for _, c := range []sc{
		{downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION, categories.Awaiting, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, categories.Active, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_PROBING, categories.Active, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, categories.Queued, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, categories.Paused, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_CANCELED, categories.Paused, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED, categories.Completed, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_FAILED, categories.Failed, true},
		{downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED, "", false},
	} {
		if got, ok := categories.StatusBucket(c.state); got != c.want || ok != c.ok {
			t.Errorf("StatusBucket(%v) = %q %v, want %q %v", c.state, got, ok, c.want, c.ok)
		}
	}
}

func TestMatchesStatus(t *testing.T) {
	if !categories.MatchesStatus(downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED, categories.AnyStatus) {
		t.Error("unspecified does not match any")
	}
	if categories.MatchesStatus(downexv1.DownloadState_DOWNLOAD_STATE_FAILED, categories.Active) {
		t.Error("failed matches active")
	}
	if !categories.MatchesStatus(downexv1.DownloadState_DOWNLOAD_STATE_PROBING, categories.Active) {
		t.Error("probing does not match active")
	}
}

func newIndex() *categories.Index {
	return categories.NewIndex(append(defaultCategories(),
		&downexv1.Category{Name: "Books", Extensions: []string{"epub", "mobi"}},
		&downexv1.Category{Name: "Video", Extensions: []string{"dup"}}, // a duplicate keeps the first
		&downexv1.Category{Name: "Comics", Extensions: []string{"CBZ", ".cbr"}},
	))
}

func TestIndexListsTheCategoriesInOrder(t *testing.T) {
	ix := newIndex()
	var got [][2]string
	for _, e := range ix.Entries() {
		got = append(got, [2]string{string(e.Key), string(e.Kind)})
	}
	want := [][2]string{
		{"cat:Video", "video"}, {"cat:Audio", "audio"}, {"cat:Archives", "archives"}, {"cat:Documents", "documents"},
		{"cat:Programs", "programs"}, {"cat:Books", "documents"}, {"cat:Comics", "other"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Entries = %v, want %v", got, want)
	}
	if !ix.Has("cat:Books") || ix.Has("cat:Gone") || !(ix.Has(categories.All) && ix.Has(categories.OtherFilter)) {
		t.Error("Has is wrong")
	}
	if got := ix.LabelOf("cat:Books"); got != "Books" {
		t.Errorf("LabelOf(cat:Books) = %q", got)
	}
	if got := ix.LabelOf(categories.OtherFilter); got != "Other" {
		t.Errorf("LabelOf(other) = %q", got)
	}
}

func TestIndexBuckets(t *testing.T) {
	ix := newIndex()
	for _, c := range []struct {
		d    *downexv1.Download
		want categories.Filter
	}{
		{Download(1, func(d *downexv1.Download) { d.Filename = "a.zip"; d.Category = "Books" }), "cat:Books"},
		// from before categories, or a renamed category: grouped by extension (first category wins)
		{Download(2, func(d *downexv1.Download) { d.Filename = "a.mobi"; d.Category = "" }), "cat:Books"},
		{Download(3, func(d *downexv1.Download) { d.Filename = "a.epub"; d.Category = "Ebooks" }), "cat:Documents"},
		{Download(4, func(d *downexv1.Download) { d.Filename = "x.cbr" }), "cat:Comics"},
		{Download(5, func(d *downexv1.Download) { d.Filename = "x.dup" }), "other"},
		{Download(6, func(d *downexv1.Download) { d.Filename = ""; d.Url = "https://example.com/v/clip.MKV?x=1" }), "cat:Video"},
	} {
		if got := ix.BucketOf(c.d); got != c.want {
			t.Errorf("BucketOf(%s) = %q, want %q", categories.RawName(c.d), got, c.want)
		}
	}
	empty := categories.NewIndex(nil)
	if got := empty.BucketOf(Download(7, func(d *downexv1.Download) { d.Filename = "a.mp4"; d.Category = "Video" })); got != "other" {
		t.Errorf("empty index bucket = %q", got)
	}
}

func TestCategoryKind(t *testing.T) {
	for _, c := range []struct {
		name string
		exts []string
		want categories.FileKind
	}{
		{"Films", []string{"mp4", "mkv", "pdf"}, categories.Video},
		{"audio", nil, categories.Audio},
		{"Misc", []string{"xyz"}, categories.Other},
	} {
		if got := categories.CategoryKind(c.name, c.exts); got != c.want {
			t.Errorf("CategoryKind(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
