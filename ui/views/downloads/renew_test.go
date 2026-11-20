package downloads

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/state"
	"downex/ui/views"
)

// TestDetailsLinkExpired: docs/DESKTOP-SPEC.md §10.1, the details pane of a download whose
// link expired: the hint, New link…, Open page (when known) and Retry.
func TestDetailsLinkExpired(t *testing.T) {
	p := showPane(t, Download(21, func(d *downexv1.Download) {
		d.State, d.ErrorCode = state.StateFailed, "link_expired"
		d.Error = "bytes 0-9: HTTP 403 Forbidden: engine: link expired"
		d.Received, d.PageUrl = 1000, "https://pages.example/files"
	}), nil)
	if !strings.Contains(p.text(), "The link stopped working: it expired, was revoked or now leads to a web page. The data received so far is kept.") {
		t.Errorf("text %q", p.text())
	}
	for _, name := range []string{"New link…", "Open page", "Retry"} {
		if p.buttonNamed(name) == nil {
			t.Fatalf("no %s", name)
		}
	}
	p.Click(p.buttonNamed("New link…"))
	if c, ok := p.Calls.Last("Renew"); !ok || c.Args[0].(views.RenewRequest).ID != 21 {
		t.Errorf("New link… calls %v", p.Calls.List)
	}
	p.Click(p.buttonNamed("Open page"))
	if c, ok := p.Calls.Last("OpenPage"); !ok || c.Args[0].(int64) != 21 {
		t.Errorf("Open page calls %v", p.Calls.List)
	}

	q := showPane(t, Download(22, func(d *downexv1.Download) {
		d.State, d.ErrorCode, d.Error = state.StateFailed, "link_expired", "probe: HTTP 410 Gone: engine: link expired"
	}), nil)
	if q.buttonNamed("Open page") != nil || q.buttonNamed("New link…") == nil {
		t.Error("buttons without a page")
	}
}
