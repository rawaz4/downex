package palette

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The pure cases of ui/frontend/src/lib/components/CommandPalette.test.ts.

func cmd(id, label string, opts ...func(*Command)) Command {
	c := Command{ID: id, Label: label, Group: "Downloads"}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func group(g string) func(*Command)    { return func(c *Command) { c.Group = g } }
func desc(d string) func(*Command)     { return func(c *Command) { c.Description = d } }
func keywords(k string) func(*Command) { return func(c *Command) { c.Keywords = k } }
func disabled(why string) func(*Command) {
	return func(c *Command) { c.Disabled, c.DisabledReason = true, why }
}

func ids(cs []Command) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func keys(es []Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Key)
	}
	return out
}

const MiB = 1 << 20

var (
	downloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	completed   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	queued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
)

func item(id int64, filename string, state downexv1.DownloadState, host string) Download {
	d := &downexv1.Download{Id: id, Filename: filename, State: state, Size: 10 * MiB}
	return Download{ID: id, Name: filename, Host: host, Kind: "archives", Download: d}
}

func TestFuzzyScore(t *testing.T) {
	if s := FuzzyScore("", "anything"); s != 0 {
		t.Errorf("empty query = %v", s)
	}
	if s := FuzzyScore("xyz", "Pause all"); s != -1 {
		t.Errorf("no match = %v", s)
	}
	if a, b := FuzzyScore("pa", "Pause all"), FuzzyScore("pa", "Open folder at path"); !(a > b) {
		t.Errorf("word start and run: %v, not above %v", a, b)
	}
	if s := FuzzyScore("ra", "Resume all"); !(s > 0) {
		t.Errorf("ra = %v", s)
	}
	// the numbers of the JavaScript rule: 1 + run×2 + 3 at a word start − min(3, gap)×0.25
	for _, c := range []struct {
		q, text string
		want    float64
	}{
		{"pa", "Pause all", 7},           // p at 0 (word start): 4; a right after it (run 1): 3
		{"pa", "Open folder at path", 4}, // p at 2: 0.75; a starts "at": 3.25
		{"p a", "Pause all", 7},          // the space ends the run; "a" of "all" starts a word
		{"k", "conference-keynote", 3.25},
	} {
		if got := FuzzyScore(c.q, c.text); got != c.want {
			t.Errorf("FuzzyScore(%q, %q) = %v, want %v", c.q, c.text, got, c.want)
		}
	}
}

func TestFilterCommands(t *testing.T) {
	list := []Command{cmd("a", "Open settings"), cmd("b", "Pause all"), cmd("c", "Pause selected")}
	if got := ids(FilterCommands(list, "")); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("empty query = %v", got)
	}
	if got := ids(FilterCommands(list, "pause sel")); !slices.Equal(got, []string{"c"}) {
		t.Errorf("pause sel = %v", got)
	}
	if got := FilterCommands(list, "set"); len(got) == 0 || got[0].ID != "a" {
		t.Errorf("set = %v", ids(got))
	}
}

func TestEntriesEmptyQuery(t *testing.T) {
	list := []Command{cmd("a", "Open settings", group("View")), cmd("b", "Pause all")}
	downloads := []Download{item(1, "one.iso", downloading, "files.example.com"), item(2, "done.zip", completed, "files.example.com")}
	for i := range 6 {
		downloads = append(downloads, item(int64(10+i), "r"+string(rune('0'+i))+".iso", downloading, "files.example.com"))
	}
	rows := Entries(list, downloads, "")
	var shown []Entry
	for _, r := range rows {
		if r.Type == EntryDownload {
			shown = append(shown, r)
		}
	}
	if len(shown) != MaxRunning {
		t.Fatalf("%d downloads shown, want %d", len(shown), MaxRunning)
	}
	for _, r := range shown {
		if r.Group != "Running downloads" {
			t.Errorf("group %q", r.Group)
		}
		if r.Item.ID == 2 {
			t.Error("a completed download is listed before a query")
		}
	}
	if got := keys(rows[MaxRunning:]); !slices.Equal(got, []string{"c-a", "c-b"}) {
		t.Errorf("commands after the downloads = %v", got)
	}
}

func TestEntriesRanking(t *testing.T) {
	pause := cmd("pause", "Pause all")
	rows := Entries([]Command{pause}, []Download{item(7, "Pause all", completed, "")}, "pause all")
	if got := keys(rows); !slices.Equal(got, []string{"c-pause", "d-7"}) {
		t.Errorf("tie = %v", got)
	}
	if rows[1].Group != "Downloads" {
		t.Errorf("group = %q", rows[1].Group)
	}
	// a download that matches better comes first
	better := Entries([]Command{cmd("x", "Show the archive list")}, []Download{item(3, "keynote.mp4", queued, "files.example.com")}, "keynote")
	if got := keys(better); !slices.Equal(got, []string{"d-3"}) {
		t.Errorf("better = %v", got)
	}
	// a group is never split: its entries stay together, groups ordered by their best match
	goTo := cmd("go", "Status: Paused", group("Go to"))
	pauseSel := cmd("pause-sel", "Pause selected", group("Commands"))
	when := cmd("when", "When done…", group("Commands"), desc("Keep running, sleep, shut down, or quit Downex"), keywords("pause"))
	grouped := Entries([]Command{pauseSel, goTo, when}, nil, "pause")
	var groups []string
	for _, r := range grouped {
		groups = append(groups, r.Group)
	}
	if !slices.Equal(groups, []string{"Commands", "Commands", "Go to"}) {
		t.Errorf("groups = %v", groups)
	}
	if grouped[0].Key != "c-pause-sel" {
		t.Errorf("first = %s", grouped[0].Key)
	}
	// downloads are found by their server too
	if got := keys(Entries(nil, []Download{item(4, "file-4.bin", queued, "mirror.example.org")}, "mirror")); !slices.Equal(got, []string{"d-4"}) {
		t.Errorf("by server = %v", got)
	}
}

func TestUnavailableNeverFirst(t *testing.T) {
	sel := cmd("pause-sel", "Pause selected", disabled("nothing selected"))
	all := cmd("pause-all", "Pause all")
	if got := keys(Entries([]Command{sel, all}, nil, "pause")); !slices.Equal(got, []string{"c-pause-all", "c-pause-sel"}) {
		t.Errorf("entries = %v", got)
	}
	if got := ids(FilterCommands([]Command{sel, all}, "pause")); !slices.Equal(got, []string{"pause-all", "pause-sel"}) {
		t.Errorf("filter = %v", got)
	}
	// available, it keeps its place
	on := cmd("pause-sel", "Pause selected")
	if got := keys(Entries([]Command{on, all}, nil, "pause")); !slices.Equal(got, []string{"c-pause-sel", "c-pause-all"}) {
		t.Errorf("available = %v", got)
	}
}

func TestDescriptionWholeWords(t *testing.T) {
	when := cmd("when", "When done…", desc("Keep running, sleep, shut down, or quit Downex"))
	remove := cmd("remove", "Remove selected…", desc("Asks first; the files stay unless you choose to delete them"))
	both := []Command{when, remove}
	if got := ids(FilterCommands(both, "shut")); !slices.Equal(got, []string{"when"}) {
		t.Errorf("shut = %v", got)
	}
	if got := ids(FilterCommands(both, "sleep shut")); !slices.Equal(got, []string{"when"}) {
		t.Errorf("sleep shut = %v", got)
	}
	// a loose subsequence of a sentence is no match
	if got := FilterCommands(both, "key"); len(got) != 0 {
		t.Errorf("key = %v", ids(got))
	}
}

func TestHighlightParts(t *testing.T) {
	for _, c := range []struct {
		q, text string
		want    []Part
	}{
		{"key", "conference-keynote.mp4", []Part{{"conference-", false}, {"key", true}, {"note.mp4", false}}},
		{"zz", "abc", []Part{{"abc", false}}},
		{"", "abc", []Part{{"abc", false}}},
		// lower case changes the length (İ → i + U+0307): one plain run
		{"i", "İstanbul", []Part{{"İstanbul", false}}},
		// UTF-16 positions: a letter outside the BMP is two units, kept whole
		{"𝒜b", "x𝒜b", []Part{{"x", false}, {"𝒜b", true}}},
	} {
		if got := HighlightParts(c.q, c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("HighlightParts(%q, %q) = %v, want %v", c.q, c.text, got, c.want)
		}
	}
}

func TestKeys(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"Ctrl+Shift+P", []string{"Ctrl", "Shift", "P"}},
		{"+", []string{"+"}},
		{"Ctrl+,", []string{"Ctrl", ","}},
		{"Del", []string{"Del"}},
	} {
		if got := Keys(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Keys(%q) = %v", c.in, got)
		}
	}
}

func TestJSLower(t *testing.T) {
	for _, c := range [][2]string{
		{"Pause ALL", "pause all"},
		{"İ", "i" + string(rune(0x307))},
		{"ΟΔΟΣ", "οδος"},
		{"AΣ.", "aς."},
		{"AΣ.B", "aσ.b"},
		{"Σ", "σ"},
		{"ΣA", "σa"},
	} {
		if got := jsLower(c[0]); got != c[1] {
			t.Errorf("jsLower(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	if !strings.EqualFold(jsTrim("\u00a0 x \ufeff"), "x") {
		t.Error("trim")
	}
}
