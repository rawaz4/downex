package progress

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/state"
)

// TestLinkExpiredView: docs/DESKTOP-SPEC.md §10.1, the progress window of a download whose
// link expired: "Failed: Link expired", New link… (primary), Open page (when known), Close.
func TestLinkExpiredView(t *testing.T) {
	expired := func(page string) *downexv1.Download {
		return formattest.Download(7, func(d *downexv1.Download) {
			d.State, d.ErrorCode, d.PageUrl = state.StateFailed, "link_expired", page
			d.Error = "connection 1: bytes 0-9: HTTP 403 Forbidden: engine: link expired"
			d.Size, d.Received = 100*MiB, 40*MiB
		})
	}
	r := show(t, expired("https://pages.example/files"))
	want(t, "state", r.stateLine(), "Failed: Link expired")
	a := r.s().actions
	if a.renew.Style.Hidden || a.page.Style.Hidden || a.close.Style.Hidden || !a.retry.Style.Hidden || !a.restart.Style.Hidden {
		t.Fatalf("buttons: renew %v page %v close %v retry %v", !a.renew.Style.Hidden, !a.page.Style.Hidden,
			!a.close.Style.Hidden, !a.retry.Style.Hidden)
	}
	want(t, "primary", a.renew.Label().Text(), "New link…")
	want(t, "page", a.page.Label().Text(), "Open page")
	var asked []int64
	r.pw.o.Renew = func(id int64) error { asked = append(asked, id); return nil }
	r.click(a.renew)
	r.settle()
	if len(asked) != 1 || asked[0] != 7 {
		t.Fatalf("New link… asked for %v", asked)
	}

	none := show(t, expired(""))
	if !none.s().actions.page.Style.Hidden {
		t.Error("Open page without a page")
	}
}
