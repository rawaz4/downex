package host

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"downex/internal/client"
)

func TestViewOf(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int64
	}{
		{nil, 0},
		{[]string{"--progress", "42"}, 42},
		{[]string{"--progress=7"}, 7},
		{[]string{"-progress", "8", "--progress", "9"}, 8},
		{[]string{"--confirm", "5", "--progress", "6"}, 6},
		{[]string{"--progress", "0", "--progress", "-1", "--progress", "x", "--progress=9223372036854775808"}, 0},
		{[]string{"progress", "5", "--progressive", "6", "--progress"}, 0},
		{[]string{"--progress", "abc", "--progress", "12"}, 12},
	} {
		got := ViewOf(tc.args)
		if got.Progress != tc.want || got.IsProgress() != (tc.want > 0) {
			t.Errorf("ViewOf(%q) = %+v, want %d", tc.args, got, tc.want)
		}
	}
	// The progress flag does not count as a confirmation request, nor the reverse.
	if got := ConfirmArgs([]string{"--progress", "5"}); got != nil {
		t.Errorf("ConfirmArgs(--progress 5) = %v", got)
	}
}

// "--confirm <id> --raise" (downexd's browser add) is the standalone Confirm download window;
// "--confirm <id>" alone is the main window's dialog, as before.
func TestViewOfConfirm(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want View
	}{
		{[]string{"--confirm", "5", "--raise"}, View{Confirm: 5}},
		{[]string{"--raise", "--confirm=9", "--confirm", "4"}, View{Confirm: 9}},
		{[]string{"--confirm", "5"}, View{}},
		{[]string{"--raise"}, View{}},
		{[]string{"--confirm", "5", "--progress", "6", "--raise"}, View{Progress: 6}},
	} {
		got := ViewOf(tc.args)
		if got != tc.want || got.IsConfirm() != (tc.want.Confirm > 0) || got.IsMain() != (tc.want == View{}) {
			t.Errorf("ViewOf(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
	s := View{Confirm: 5}.WindowOn("windows")
	if s.Title != "Confirm download" || s.Frameless || s.Width != ConfirmWidth || s.InstanceID == MainInstanceID {
		t.Errorf("the confirm window's spec %+v", s)
	}
}

func TestConfirmArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []int64
	}{
		{nil, nil},
		{[]string{"--confirm", "42"}, []int64{42}},
		{[]string{"--confirm=7", "-confirm", "8", "--confirm", "7"}, []int64{7, 8}},
		{[]string{"x", "--other", "--confirm", "0", "--confirm", "-3", "--confirm", "abc", "--confirm=9223372036854775808"}, nil},
		{[]string{"confirm", "5", "--confirmed", "6", "--confirm"}, nil},
		{[]string{"--confirm", "12", "--confirm"}, []int64{12}},
	} {
		if got := ConfirmArgs(tc.args); !slices.Equal(got, tc.want) {
			t.Errorf("ConfirmArgs(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestWindowSpecs(t *testing.T) {
	main := View{}.Window()
	if main.Width != 1200 || main.Height != 760 || main.MinWidth != 900 || main.MinHeight != 600 || main.InstanceID != MainInstanceID {
		t.Errorf("main window = %+v", main)
	}
	if MainInstanceID != "downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904" {
		t.Errorf("the main window's lock is %q: a running window would not hand over", MainInstanceID)
	}
	if main.Frameless != (runtime.GOOS == "windows") {
		t.Errorf("main window frameless = %v on %s", main.Frameless, runtime.GOOS)
	}
	p := View{Progress: 5}.Window()
	want := WindowSpec{
		Title: "Downex", Width: 520, Height: 300, MinWidth: 480, MinHeight: 260, InstanceID: MainInstanceID + "-progress-5",
		Light: RGB{0xff, 0xff, 0xff}, Dark: RGB{0x17, 0x1e, 0x1b},
	}
	if p != want {
		t.Errorf("progress window = %+v, want %+v", p, want)
	}
	// One lock per download, none shared with the main window.
	ids := []string{main.InstanceID, p.InstanceID, View{Progress: 6}.Window().InstanceID, View{Progress: 55}.Window().InstanceID}
	for i := range ids {
		for j := range i {
			if ids[i] == ids[j] {
				t.Errorf("windows %d and %d share the single-instance id %q", i, j, ids[i])
			}
		}
	}
	if (View{Progress: 5}).Window().InstanceID != p.InstanceID || (View{Progress: 5}).InstanceID() != p.InstanceID {
		t.Error("the single-instance id of a download's window is not stable")
	}
	if (View{}).InstanceID() != MainInstanceID {
		t.Error("the main view's instance id")
	}
}

func TestWindowFrameAndBackground(t *testing.T) {
	// Only the Windows main window is frameless (it draws its title bar); progress windows
	// and the other systems keep the system frame.
	for _, goos := range []string{"windows", "darwin", "linux"} {
		if got := (View{}).WindowOn(goos).Frameless; got != (goos == "windows") {
			t.Errorf("main window on %s: frameless = %v", goos, got)
		}
		if (View{Progress: 3}).WindowOn(goos).Frameless {
			t.Errorf("progress window on %s is frameless", goos)
		}
	}
	// The window opens in its page's ground: --chrome for the main window, --surface for a
	// progress window, --raised for the Confirm download window (docs/DESIGN-SYSTEM.md §2.1).
	for _, tc := range []struct {
		v           View
		dark        bool
		want        RGB
		description string
	}{
		{View{}, false, RGB{0xee, 0xf2, 0xf0}, "main, light"},
		{View{}, true, RGB{0x0a, 0x0e, 0x0c}, "main, dark"},
		{View{Progress: 9}, false, RGB{0xff, 0xff, 0xff}, "progress, light"},
		{View{Progress: 9}, true, RGB{0x17, 0x1e, 0x1b}, "progress, dark"},
		{View{Confirm: 9}, false, RGB{0xff, 0xff, 0xff}, "confirm, light"},
		{View{Confirm: 9}, true, RGB{0x21, 0x2a, 0x26}, "confirm, dark"},
	} {
		if got := tc.v.Window().Background(tc.dark); got != tc.want {
			t.Errorf("%s: background = %+v, want %+v", tc.description, got, tc.want)
		}
	}
	if got := (RGB{0x12, 0x34, 0x56}).COLORREF(); got != 0x563412 {
		t.Errorf("COLORREF = %#x, want 0x563412", got)
	}
}

// What today's Startup binding sends besides the view and the build.
func TestCapabilities(t *testing.T) {
	if CanOpenWith != (runtime.GOOS == "windows") {
		t.Errorf("CanOpenWith = %v on %s", CanOpenWith, runtime.GOOS)
	}
	want := Capabilities{
		OpenWith: CanOpenWith, Hibernate: runtime.GOOS != "darwin", WakeTimers: runtime.GOOS == "windows",
		CustomTitleBar: runtime.GOOS == "windows",
	}
	if got := (View{}).Capabilities(); got != want {
		t.Errorf("main window: %+v, want %+v", got, want)
	}
	want.CustomTitleBar = false
	if got := (View{Progress: 42}).Capabilities(); got != want {
		t.Errorf("progress window: %+v, want %+v", got, want)
	}
}

func TestPlatformCapabilities(t *testing.T) {
	for _, tc := range []struct {
		goos             string
		hibernate, timer bool
	}{
		{"windows", true, true},
		{"darwin", false, false},
		{"linux", true, false},
	} {
		if got := CanHibernate(tc.goos); got != tc.hibernate {
			t.Errorf("CanHibernate(%s) = %v", tc.goos, got)
		}
		if got := HasWakeTimers(tc.goos); got != tc.timer {
			t.Errorf("HasWakeTimers(%s) = %v", tc.goos, got)
		}
	}
}

func TestOpenProgressWindow(t *testing.T) {
	var mu sync.Mutex
	var opened []int64
	var fail error
	start := func(id int64) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, id)
		return fail
	}
	for _, id := range []int64{5, 6} {
		if err := openProgressWindow(id, start, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []int64{0, -3} {
		if err := openProgressWindow(bad, start, nil); !hasPrefix(err, "invalid_argument: no such download: ") {
			t.Errorf("openProgressWindow(%d) = %v", bad, err)
		}
	}
	mu.Lock()
	fail = errors.New("no such file")
	mu.Unlock()
	if err := openProgressWindow(7, start, nil); errText(err) != "internal: cannot open the progress window: no such file" {
		t.Errorf("a failed start = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(opened, []int64{5, 6, 7}) {
		t.Errorf("opened %v", opened)
	}

	// The session's opener is the one it was given.
	var asked []int64
	s := NewSession(SessionOptions{
		Daemon: newFakeDaemon(), Shell: &fakeShell{}, ThemePath: filepath.Join(t.TempDir(), ThemeFileName),
		OpenWindow: func(id int64) error { asked = append(asked, id); return nil },
	})
	defer s.Close()
	if err := s.OpenProgressWindow(9); err != nil || !slices.Equal(asked, []int64{9}) {
		t.Errorf("Session.OpenProgressWindow = %v, opened %v", err, asked)
	}
}

// TestProgressProgram: inside Downex.app the progress windows are the progress app's
// (docs/MACOS-SPEC.md §1, client.Layout), when it is installed; otherwise, and on every
// other system, this program shows them.
func TestProgressProgram(t *testing.T) {
	const app = "/Applications/Downex.app"
	const helper = app + "/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex"
	installed := func(p string) bool { return p == helper }
	missing := func(string) bool { return false }
	for _, tc := range []struct {
		exe    string
		isFile func(string) bool
		want   string
	}{
		{app + "/Contents/MacOS/Downex", installed, helper},
		{app + "/Contents/MacOS/Downex", missing, app + "/Contents/MacOS/Downex"},
		{helper, installed, helper}, // the progress app is its own progress program
		{"/src/ui/build/bin/Downex.app/Contents/MacOS/Downex", installed, "/src/ui/build/bin/Downex.app/Contents/MacOS/Downex"},
		{"/src/ui/build/bin/Downex", installed, "/src/ui/build/bin/Downex"},
	} {
		if got := ProgressProgram(client.LayoutOf("darwin", tc.exe), tc.exe, tc.isFile); got != tc.want {
			t.Errorf("ProgressProgram(%q) = %q, want %q", tc.exe, got, tc.want)
		}
	}
	exe := filepath.Join(t.TempDir(), "downex-ui.exe")
	if got := ProgressProgram(client.LayoutOf("windows", exe), exe, func(string) bool { return true }); got != exe {
		t.Errorf("windows: ProgressProgram = %q, want %q", got, exe)
	}
}

// The names of the single-instance lock are Wails v2.17's, so that a running old window
// and a new one hand over to each other.
func TestHandoffNames(t *testing.T) {
	got := HandoffNamesOf(MainInstanceID)
	want := HandoffNames{
		Mutex:  "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904sim",
		Class:  "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-sic",
		Window: "wails-app-downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904-siw",
	}
	if got != want {
		t.Errorf("HandoffNamesOf(main) = %+v, want %+v", got, want)
	}
	if p := HandoffNamesOf(ProgressInstanceID(42)); p.Mutex != "wails-app-"+MainInstanceID+"-progress-42sim" {
		t.Errorf("progress window's mutex = %q", p.Mutex)
	}
	if HandoffDataKind != 1542 {
		t.Errorf("HandoffDataKind = %d, want 1542", HandoffDataKind)
	}
}

// wailsPayload is what Wails v2.17's sender puts in WM_COPYDATA for json: the string as
// UTF-16 with its NUL, and cbData one byte longer than that (the byte after the NUL is
// whatever follows in memory).
func wailsPayload(json string) []byte {
	units := utf16.Encode([]rune(json))
	units = append(units, 0)
	b := make([]byte, 0, 2*len(units)+1)
	for _, u := range units {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0x5a)
}

func TestHandoffWireFormat(t *testing.T) {
	h := Handoff{Args: []string{"--confirm", "7", `C:\Users\Ünïcode\file name.txt`, "emoji 😀"}, WorkingDirectory: `C:\Program Files\Downex`}
	b, err := EncodeHandoff(h)
	if err != nil {
		t.Fatal(err)
	}
	// UTF-16 JSON ending with one NUL unit, nothing after it.
	if len(b)%2 != 0 || b[len(b)-2] != 0 || b[len(b)-1] != 0 {
		t.Fatalf("payload does not end with a NUL unit: % x", b[len(b)-4:])
	}
	units := make([]uint16, len(b)/2-1)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	if s := string(utf16.Decode(units)); !strings.HasPrefix(s, `{"Args":["--confirm","7",`) || !strings.HasSuffix(s, `"WorkingDirectory":"C:\\Program Files\\Downex"}`) {
		t.Errorf("payload JSON = %s", s)
	}
	got, err := DecodeHandoff(b)
	if err != nil || !reflect.DeepEqual(got, h) {
		t.Errorf("round trip = %+v, %v; want %+v", got, err, h)
	}

	// What an old Wails window sends reads the same.
	got, err = DecodeHandoff(wailsPayload(`{"Args":["--progress","42"],"WorkingDirectory":"C:\\x"}`))
	if err != nil || !reflect.DeepEqual(got, Handoff{Args: []string{"--progress", "42"}, WorkingDirectory: `C:\x`}) {
		t.Errorf("Wails payload = %+v, %v", got, err)
	}
	// Without a NUL the payload ends at cbData; unknown fields and case are as encoding/json reads them.
	got, err = DecodeHandoff(wailsPayload(`{"args":["a"],"Extra":1}`)[:2*len(`{"args":["a"],"Extra":1}`)])
	if err != nil || !reflect.DeepEqual(got, Handoff{Args: []string{"a"}}) {
		t.Errorf("payload without a NUL = %+v, %v", got, err)
	}
	for name, bad := range map[string][]byte{
		"empty":    nil,
		"not JSON": wailsPayload("--confirm 7"),
		"too long": make([]byte, MaxHandoff+2),
	} {
		if _, err := DecodeHandoff(bad); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	if _, err := EncodeHandoff(Handoff{Args: []string{strings.Repeat("x", MaxHandoff)}}); err == nil {
		t.Error("an oversized hand-off was encoded")
	}
}

// The window's own "--confirm" waits until the window is ready; a second start hands its
// arguments over and brings the window forward; nothing is sent twice.
func TestConfirmRequestsReachTheWindow(t *testing.T) {
	s, _ := newTestSession(t, newFakeDaemon(), "--confirm", "5")
	r := &recorder{}
	if got := r.shownIDs(); len(got) != 0 {
		t.Fatalf("sent before the window was ready: %v", got)
	}
	s.Confirmations().Ready(r.show)
	if got := r.shownIDs(); !slices.Equal(got, []int64{5}) {
		t.Fatalf("after Ready: %v", got)
	}
	s.SecondInstance([]string{"--confirm", "6", "--confirm=7"}, r.focus)
	if got := r.shownIDs(); !slices.Equal(got, []int64{5, 6, 7}) {
		t.Errorf("after the second instance: %v", got)
	}
	if n := r.focusCount(); n != 1 {
		t.Errorf("focus called %d times, want 1", n)
	}
	// A second Ready sends nothing twice.
	s.Confirmations().Ready(r.show)
	if got := r.shownIDs(); len(got) != 3 {
		t.Errorf("after a second Ready: %v", got)
	}
	// Requests that arrive before Ready wait for it, in order.
	c := NewConfirmations(View{}, nil)
	c.Request([]int64{3})
	c.SecondInstance([]string{"--confirm", "4"}, nil)
	r2 := &recorder{}
	c.Ready(r2.show)
	if got := r2.shownIDs(); !slices.Equal(got, []int64{3, 4}) {
		t.Errorf("waiting requests = %v, want [3 4]", got)
	}
}

func TestSecondInstanceFocusesTheWindow(t *testing.T) {
	s, _ := newTestSession(t, newFakeDaemon())
	r := &recorder{}
	s.Confirmations().Ready(r.show)
	s.SecondInstance(nil, r.focus)
	if n := r.focusCount(); n != 1 {
		t.Errorf("focus called %d times, want 1", n)
	}
	if got := r.shownIDs(); len(got) != 0 {
		t.Errorf("confirmation requests %v without --confirm", got)
	}
}

// "--raise" marks the window of a download the user just started in the browser; other
// arguments, a "--raise" value included, do not.
func TestRaiseArg(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--progress", "5", "--raise"}, true},
		{[]string{"-raise"}, true},
		{[]string{"--progress", "5"}, false},
		{[]string{"--raise=1"}, false},
		{[]string{"raise"}, false},
		{nil, false},
	} {
		if got := RaiseArg(c.args); got != c.want {
			t.Errorf("RaiseArg(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
