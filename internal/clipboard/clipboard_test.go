package clipboard

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func wantDefault(ext string) bool {
	return slices.Contains([]string{"zip", "exe", "mp4", "pdf", "7z", "tgz"}, ext)
}

func TestExtract(t *testing.T) {
	many := strings.Repeat("https://example.com/a.zip\n", MaxURLs)
	tooMany := many + "https://example.com/b.zip"
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"one URL", "https://example.com/file.zip", []string{"https://example.com/file.zip"}},
		{"surrounding space", "  \thttps://example.com/file.zip \r\n", []string{"https://example.com/file.zip"}},
		{"http", "http://example.com/dl/setup.EXE", []string{"http://example.com/dl/setup.EXE"}},
		{"upper-case scheme", "HTTPS://Example.com/a.pdf", []string{"HTTPS://Example.com/a.pdf"}},
		{"query and fragment", "https://example.com/v.mp4?token=x#t=10", []string{"https://example.com/v.mp4?token=x#t=10"}},
		{"escaped name", "https://example.com/my%20file.7z", []string{"https://example.com/my%20file.7z"}},
		{"port", "https://example.com:8443/a.zip", []string{"https://example.com:8443/a.zip"}},
		// Links with credentials are never offered (they would be stored and shown).
		{"userinfo", "https://u:p@example.com:8443/a.zip", nil},
		{"user only", "https://admin@nas.local/backup.zip", nil},
		{"empty userinfo", "https://@example.com/a.zip", nil},
		{"userinfo among URLs", "https://a.example/1.zip\nhttps://admin:pw@nas.local/b.zip", nil},
		{"@ in the path", "https://example.com/u@x/a.zip", []string{"https://example.com/u@x/a.zip"}},
		{"IPv6 host", "http://[::1]:8080/a.zip", []string{"http://[::1]:8080/a.zip"}},
		{"several lines", "https://a.example/1.zip\r\n\r\nhttps://b.example/2.pdf\n", []string{"https://a.example/1.zip", "https://b.example/2.pdf"}},
		{"duplicates once", "https://a.example/1.zip\nhttps://a.example/1.zip", []string{"https://a.example/1.zip"}},
		{"unwanted extensions dropped", "https://a.example/page.html\nhttps://a.example/1.zip", []string{"https://a.example/1.zip"}},
		{"BOM", "\ufeffhttps://a.example/1.zip", []string{"https://a.example/1.zip"}},
		{"max lines", many, []string{"https://example.com/a.zip"}},
		{"too many lines", tooMany, nil},
		{"empty", "", nil},
		{"blank", " \n\t\n", nil},
		{"no extension", "https://example.com/download", nil},
		{"directory", "https://example.com/a.zip/", nil},
		{"host only", "https://example.com", nil},
		{"dot file", "https://example.com/.zip", nil},
		{"trailing dot", "https://example.com/a.", nil},
		{"extension too long", "https://example.com/a.abcdefghijklmnopq", nil},
		{"extension with symbols", "https://example.com/a.z-p", nil},
		{"extension in the query only", "https://example.com/get?f=a.zip", nil},
		{"prose", "see https://example.com/a.zip", nil},
		{"prose line among URLs", "https://example.com/a.zip\nthanks!", nil},
		{"space inside", "https://example.com/a b.zip", nil},
		{"nbsp inside", "https://example.com/a\u00a0b.zip", nil},
		{"ftp", "ftp://example.com/a.zip", nil},
		{"file", "file:///C:/a.zip", nil},
		{"javascript", "javascript:alert(1)//a.zip", nil},
		{"opaque", "https:example.com/a.zip", nil},
		{"no host", "https:///a.zip", nil},
		{"relative", "//example.com/a.zip", nil},
		{"bad escape", "https://example.com/%zz.zip", nil},
		{"control character", "https://example.com/a\x7f.zip", nil},
		{"too long", "https://example.com/" + strings.Repeat("a", MaxTextBytes) + ".zip", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(tc.text, wantDefault)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Extract(%q) = %q, want %q", truncateForLog(tc.text), got, tc.want)
			}
		})
	}
}

func truncateForLog(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func TestExtractPassesLowerCaseExtensions(t *testing.T) {
	var seen []string
	Extract("https://example.com/A.ZIP\nhttps://example.com/b.Tar.Gz", func(ext string) bool {
		seen = append(seen, ext)
		return true
	})
	if want := []string{"zip", "gz"}; !slices.Equal(seen, want) {
		t.Fatalf("extensions passed = %q, want %q", seen, want)
	}
}

func TestDeduperWindow(t *testing.T) {
	d := NewDeduper(DedupeWindow)
	t0 := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)
	steps := []struct {
		url  string
		at   time.Duration
		want bool
	}{
		{"https://example.com/a.zip", 0, true},
		{"https://example.com/a.zip", time.Minute, false},
		{"HTTPS://EXAMPLE.COM/a.zip#frag", 2 * time.Minute, false}, // same URL
		{"https://example.com/A.zip", 2 * time.Minute, true},       // the path is case-sensitive
		{"https://example.com/a.zip?x=1", 3 * time.Minute, true},   // another query
		{"https://example.com/a.zip", DedupeWindow - time.Nanosecond, false},
		{"https://example.com/a.zip", DedupeWindow, true}, // the window has passed
		{"https://example.com/a.zip", DedupeWindow + time.Minute, false},
		{"https://example.com/a.zip", 2*DedupeWindow + time.Minute, true},
	}
	for i, s := range steps {
		if got := d.Offer(s.url, t0.Add(s.at)); got != s.want {
			t.Fatalf("step %d: Offer(%q, +%v) = %v, want %v", i, s.url, s.at, got, s.want)
		}
	}
}

func TestDeduperClockStepBack(t *testing.T) {
	d := NewDeduper(DedupeWindow)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if !d.Offer("https://example.com/a.zip", t0) {
		t.Fatal("first offer refused")
	}
	if d.Offer("https://example.com/a.zip", t0.Add(-time.Minute)) {
		t.Fatal("offer a minute before the remembered one accepted")
	}
	if !d.Offer("https://example.com/a.zip", t0.Add(-time.Hour)) {
		t.Fatal("offer after a large backward clock step refused")
	}
}

func TestDeduperBounded(t *testing.T) {
	d := NewDeduper(DedupeWindow)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range maxRemembered + 100 {
		if !d.Offer(fmt.Sprintf("https://example.com/%d.zip", i), t0.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("offer %d refused", i)
		}
	}
	if n := len(d.seen); n > maxRemembered {
		t.Fatalf("remembers %d URLs, want at most %d", n, maxRemembered)
	}
	// The newest are still remembered, the oldest were forgotten.
	if d.Offer(fmt.Sprintf("https://example.com/%d.zip", maxRemembered+99), t0.Add(time.Second)) {
		t.Fatal("newest URL offered again")
	}
	if !d.Offer("https://example.com/0.zip", t0.Add(time.Second)) {
		t.Fatal("oldest URL still remembered")
	}
	// Expired entries are dropped when room is needed.
	later := t0.Add(DedupeWindow + time.Hour)
	d.Offer("https://example.com/new.zip", later)
	for range maxRemembered {
		d.Offer(fmt.Sprintf("https://example.com/x%d.zip", len(d.seen)), later)
	}
	if n := len(d.seen); n > maxRemembered {
		t.Fatalf("remembers %d URLs, want at most %d", n, maxRemembered)
	}
}

func TestWindowsExcluded(t *testing.T) {
	zero, one := uint32(0), uint32(1)
	for _, tc := range []struct {
		name                          string
		exclude, ignore, historyFound bool
		history                       *uint32
		want                          bool
	}{
		{"plain", false, false, false, nil, false},
		{"exclude format", true, false, false, nil, true},
		{"viewer ignore", false, true, false, nil, true},
		{"history 0", false, false, true, &zero, true},
		{"history 1", false, false, true, &one, false},
		{"history unreadable", false, false, true, nil, true},
	} {
		if got := windowsExcluded(tc.exclude, tc.ignore, tc.historyFound, tc.history); got != tc.want {
			t.Errorf("%s: windowsExcluded = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMacExcluded(t *testing.T) {
	for _, tc := range []struct {
		types []string
		want  bool
	}{
		{nil, false},
		{[]string{"public.utf8-plain-text"}, false},
		{[]string{"public.utf8-plain-text", TypeConcealed}, true},
		{[]string{TypeTransient, "public.utf8-plain-text"}, true},
		{[]string{"org.nspasteboard.AutoGeneratedType", "public.utf8-plain-text"}, false},
	} {
		if got := macExcluded(tc.types); got != tc.want {
			t.Errorf("macExcluded(%q) = %v, want %v", tc.types, got, tc.want)
		}
	}
}

// fakePlatform is a platform monitor driven by the test.
type fakePlatform struct {
	mu      sync.Mutex
	offer   func(string)
	stopped int
	stopErr error
}

func (f *fakePlatform) start(offer func(string), _ *slog.Logger) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offer = offer
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped++
		return f.stopErr
	}, nil
}

func (f *fakePlatform) send(text string) {
	f.mu.Lock()
	offer := f.offer
	f.mu.Unlock()
	offer(text)
}

func TestMonitorDelivers(t *testing.T) {
	f := &fakePlatform{stopErr: errors.New("stop failed")}
	got := make(chan string, 8)
	m, err := start(func(s string) { got <- s }, nil, f.start)
	if err != nil {
		t.Fatal(err)
	}
	f.send("one")
	f.send("two")
	for _, want := range []string{"one", "two"} {
		select {
		case s := <-got:
			if s != want {
				t.Fatalf("delivered %q, want %q", s, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q not delivered", want)
		}
	}
	if err := m.Close(); err == nil || err.Error() != "stop failed" {
		t.Fatalf("Close = %v, want the platform's error", err)
	}
	if err := m.Close(); err == nil {
		t.Fatal("second Close lost the error")
	}
	<-m.Done()
	if f.stopped != 1 {
		t.Fatalf("platform stopped %d times, want 1", f.stopped)
	}
	f.send("after close") // must neither block nor be delivered
	select {
	case s := <-got:
		t.Fatalf("delivered %q after Close", s)
	case <-time.After(50 * time.Millisecond):
	}
	var nilMonitor *Monitor
	if err := nilMonitor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorDropsWhileBusy(t *testing.T) {
	f := &fakePlatform{}
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	m, err := start(func(s string) {
		<-release
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	}, nil, f.start)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { // offers never block, however slow the handler is
		defer close(done)
		for i := range 100 {
			f.send(fmt.Sprint(i))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("offer blocked on a busy handler")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = m.Close()
	<-m.Done()
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || len(got) > pendingTexts+1 {
		t.Fatalf("delivered %d texts, want 1..%d", len(got), pendingTexts+1)
	}
}

func TestStartError(t *testing.T) {
	want := errors.New("no clipboard")
	m, err := start(func(string) {}, nil, func(func(string), *slog.Logger) (func() error, error) { return nil, want })
	if m != nil || !errors.Is(err, want) {
		t.Fatalf("start = %v, %v; want nil, %v", m, err, want)
	}
}
