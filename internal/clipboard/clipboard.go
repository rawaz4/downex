// Package clipboard watches the system clipboard for copied download links
// (docs/M6-SPEC.md §3, clipboard_monitor). The platform monitors only report new
// clipboard texts that are not marked as excluded or sensitive:
//
//   - Windows: AddClipboardFormatListener on a message-only window that runs on a
//     locked OS thread of its own (clipboard_windows.go). Content carrying
//     ExcludeClipboardContentFromMonitorProcessing, "Clipboard Viewer Ignore" or
//     CanIncludeInClipboardHistory = 0 is ignored.
//   - macOS: NSPasteboard's changeCount is checked every second (clipboard_darwin.go,
//     darwin && cgo: the downexd-agent); org.nspasteboard.ConcealedType and
//     TransientType content is ignored. A darwin build without cgo has no monitor.
//   - Other systems: none (Start returns ErrNotSupported).
//
// What a text means is decided by the OS-independent rules of this file: Extract
// accepts a text that consists only of http(s) URLs (one per line, at most MaxURLs)
// and keeps those whose path ends with a wanted extension; Deduper offers the same
// URL at most once per DedupeWindow.
package clipboard

import (
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Limits of the clipboard rules.
const (
	// MaxTextBytes bounds the clipboard texts that are looked at; longer ones are not
	// read at all (they are documents, not links).
	MaxTextBytes = 64 << 10
	// MaxURLs is the most lines of URLs one clipboard text may hold.
	MaxURLs = 10
	// DedupeWindow is how long an offered URL is not offered again.
	DedupeWindow = 10 * time.Minute
	// maxExtensionLen matches the longest extension a category may list.
	maxExtensionLen = 16
	// maxRemembered bounds the URLs a Deduper remembers.
	maxRemembered = 4096
	// pendingTexts bounds the texts waiting for the handler; newer ones are dropped.
	pendingTexts = 4
	// byteOrderMark may lead a copied text.
	byteOrderMark = string(rune(0xfeff))
)

// ErrNotSupported is returned by Start where the clipboard cannot be monitored.
var ErrNotSupported = errors.New("clipboard: monitoring is not supported on this system")

// Monitor watches the clipboard until Close. Create it with Start.
type Monitor struct {
	texts    chan string
	stop     chan struct{}
	done     chan struct{}
	platform func() error // stops the platform monitor
	once     sync.Once
	err      error
}

// Start watches the clipboard and calls onText with every new clipboard text that is
// not marked as excluded or sensitive and holds at most MaxTextBytes. onText runs on a
// goroutine of the monitor, one call at a time; texts that arrive while it is busy
// are queued (a few) or dropped. log may be nil.
func Start(onText func(text string), log *slog.Logger) (*Monitor, error) {
	return start(onText, log, startPlatform)
}

// platformStarter starts a platform monitor that calls offer with every new,
// not excluded clipboard text (never blocking) and returns the function that stops it.
type platformStarter func(offer func(string), log *slog.Logger) (stop func() error, err error)

func start(onText func(string), log *slog.Logger, platform platformStarter) (*Monitor, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := &Monitor{texts: make(chan string, pendingTexts), stop: make(chan struct{}), done: make(chan struct{})}
	stop, err := platform(m.offer, log)
	if err != nil {
		return nil, err
	}
	m.platform = stop
	go m.deliver(onText)
	return m, nil
}

// offer hands a new clipboard text to the handler without blocking the platform side.
func (m *Monitor) offer(text string) {
	select {
	case <-m.stop:
	case m.texts <- text:
	default: // the handler is not keeping up: the newest text is dropped
	}
}

func (m *Monitor) deliver(onText func(string)) {
	defer close(m.done)
	for {
		select {
		case <-m.stop:
			return
		case t := <-m.texts:
			select {
			case <-m.stop:
				return
			default:
			}
			onText(t)
		}
	}
}

// Close stops the monitor: no onText call starts afterwards, while one already running
// may still be in progress (Done tells when it has returned). It may be called more
// than once, also from onText; a nil Monitor is ignored.
func (m *Monitor) Close() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() {
		m.err = m.platform()
		close(m.stop)
	})
	return m.err
}

// Done is closed once the handler goroutine has ended (after Close).
func (m *Monitor) Done() <-chan struct{} { return m.done }

// Extract returns the URLs of a clipboard text worth offering as downloads: the text
// (at most MaxTextBytes) must consist only of absolute http or https URLs without user
// information, one per line (blank lines and surrounding spaces are ignored), at most
// MaxURLs of them; anything else yields none. Of those, the URLs whose path ends with a file extension
// for which wanted reports true are returned (each once, as written). Extensions are
// passed lower-case, without the dot.
func Extract(text string, wanted func(ext string) bool) []string {
	if len(text) > MaxTextBytes {
		return nil
	}
	text = strings.TrimPrefix(text, byteOrderMark)
	var urls []string
	lines := 0
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if lines++; lines > MaxURLs {
			return nil
		}
		u, ok := parseHTTPURL(line)
		if !ok {
			return nil
		}
		if ext := pathExtension(u.Path); ext != "" && wanted(ext) && !slices.Contains(urls, line) {
			urls = append(urls, line)
		}
	}
	return urls
}

// parseHTTPURL parses s as an absolute http(s) URL with a host and without spaces.
// A URL with user information (even an empty one: "https://@host/") is not a download
// link: its credentials would be stored and shown without anyone having chosen to add
// it.
func parseHTTPURL(s string) (*url.URL, bool) {
	if strings.ContainsFunc(s, isSpaceOrControl) {
		return nil, false
	}
	u, err := url.Parse(s)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.Hostname() == "" {
		return nil, false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u, true
	}
	return nil, false
}

func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f || r == 0x85 || r == 0xa0 || r == 0x2028 || r == 0x2029 || r == 0xfeff
}

// pathExtension is the lower-case extension of the last segment of a decoded URL
// path, "" when the path ends with a slash or the segment has no plausible extension
// (letters and digits, at most 16).
func pathExtension(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return ""
	}
	name := p[strings.LastIndexByte(p, '/')+1:]
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 || len(name)-i-1 > maxExtensionLen {
		return ""
	}
	ext := strings.ToLower(name[i+1:])
	for j := 0; j < len(ext); j++ {
		if c := ext[j]; (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ""
		}
	}
	return ext
}

// Deduper remembers offered URLs so that the same URL is offered at most once per
// window. URLs that differ only in the case of the scheme or host, or in the fragment,
// are the same. It is safe for concurrent use; the zero Deduper is not usable.
type Deduper struct {
	window time.Duration
	mu     sync.Mutex
	seen   map[string]time.Time
}

// NewDeduper returns a Deduper with the given window (DedupeWindow in production).
func NewDeduper(window time.Duration) *Deduper {
	return &Deduper{window: window, seen: make(map[string]time.Time)}
}

// Offer reports whether rawURL may be offered at now: it was not offered within the
// window before now. A URL that may be offered is remembered as offered at now.
func (d *Deduper) Offer(rawURL string, now time.Time) bool {
	key := dedupeKey(rawURL)
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[key]; ok && now.Sub(t) < d.window && now.Sub(t) > -d.window {
		return false
	}
	if len(d.seen) >= maxRemembered {
		d.pruneLocked(now)
	}
	d.seen[key] = now
	return true
}

// pruneLocked forgets the URLs whose window has passed and, should that not free any
// room, the one offered first.
func (d *Deduper) pruneLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, t := range d.seen {
		if now.Sub(t) >= d.window {
			delete(d.seen, k)
		} else if oldestKey == "" || t.Before(oldest) {
			oldestKey, oldest = k, t
		}
	}
	if len(d.seen) >= maxRemembered {
		delete(d.seen, oldestKey)
	}
}

// dedupeKey identifies a URL for Deduper: scheme and host lower-cased, fragment
// dropped; an unparsable URL is its own key.
func dedupeKey(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return rawURL
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}
