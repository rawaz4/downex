package add

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/locale"
	"downex/ui/core/media"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

func TestMain(m *testing.M) {
	// the window's start-up gives audio tracks ICU's language names (Intl.DisplayNames)
	media.SetLanguageNamer(locale.LanguageName)
	os.Exit(m.Run())
}

type syncMutex = sync.Mutex

// MiB is testing.ts's MiB.
const MiB = 1 << 20

// apiError is api.ts's ApiError(code, message): a Connect error.
func apiError(code connect.Code, msg string) error {
	return connect.NewError(code, errString(msg))
}

type errString string

func (e errString) Error() string { return string(e) }

// textError is a thrown Error('<code>: <message>') (toApiError reads the code from the text).
func textError(s string) error { return errString(s) }

// newHarness is a headless window over the Studio scene (the component tests' render()).
func newHarness(t *testing.T) *viewstest.Harness {
	t.Helper()
	return viewstest.New(t, viewstest.Options{NoStart: true})
}

// calls records calls with their arguments, safe for the background goroutines.
type calls[T any] struct {
	mu   sync.Mutex
	args []T
}

func (c *calls[T]) add(v T) {
	c.mu.Lock()
	c.args = append(c.args, v)
	c.mu.Unlock()
}

func (c *calls[T]) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.args)
}

func (c *calls[T]) last() T {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	if len(c.args) == 0 {
		return zero
	}
	return c.args[len(c.args)-1]
}

func (c *calls[T]) all() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]T(nil), c.args...)
}

// focused is the widget with the window's focus.
func focused(h *viewstest.Harness) kit.Widget {
	if f := h.Window.Focused(); f != nil {
		return f.Widget()
	}
	return nil
}

// isFocused reports whether w has the focus.
func isFocused(h *viewstest.Harness, w kit.Widget) bool {
	f := h.Window.Focused()
	return f != nil && f == w.Base()
}

// choose picks a Select's option as a click on it does (chooseOption).
func choose(t *testing.T, h *viewstest.Harness, s *widgets.Select, value string) {
	t.Helper()
	found := false
	for _, o := range s.Options {
		if o.Value == value {
			found = true
		}
	}
	if !found {
		t.Fatalf("no option %q", value)
	}
	s.Open(widgets.FocusChecked)
	h.Settle()
	if s.Value() != value {
		s.SetValue(value)
		if s.OnChange != nil {
			s.OnChange(value)
		}
	}
	s.Close()
	h.Settle()
}

// optionValues and optionLabels are a Select's options.
func optionValues(s *widgets.Select) []string {
	var out []string
	for _, o := range s.Options {
		out = append(out, o.Value)
	}
	return out
}

func optionLabels(s *widgets.Select) []string {
	var out []string
	for _, o := range s.Options {
		out = append(out, o.Label)
	}
	return out
}

func optionDetails(s *widgets.Select) []string {
	var out []string
	for _, o := range s.Options {
		out = append(out, o.Detail)
	}
	return out
}

// press presses a button (a click).
func press(h *viewstest.Harness, b interface{ Press() }) {
	b.Press()
	h.Settle()
}

// key sends a key to the focused element.
func key(h *viewstest.Harness, k kit.Key, mods kit.Modifiers) bool { return h.Key(k, mods) }

// blockingClock is a media.Clock over the stores' clock (statetest): After fires when the test
// advances it.
func retryClock(h *viewstest.Harness) media.Clock { return appClock{h.StoreClock} }

// sleepOn blocks a fake daemon's call until the stores' clock has moved d on (setTimeout in a
// mocked call).
func sleepOn(h *viewstest.Harness, d time.Duration) {
	ch := make(chan struct{})
	h.StoreClock.AfterFunc(d, func() { close(ch) })
	<-ch
}

// addOutcomes is the default submit's answer: every address added.
func okOutcomes(reqs []*downexv1.AddRequest) []state.AddOutcome {
	out := make([]state.AddOutcome, len(reqs))
	for i, r := range reqs {
		out[i] = state.AddOutcome{URL: r.GetUrl(), Download: &downexv1.Download{Id: int64(i + 1), Url: r.GetUrl()}}
	}
	return out
}

var (
	_ = context.Background
	_ = views.ModalAdd
)
