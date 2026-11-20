package dialogs_test

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/viewstest"
)

// expiredFile is a download whose link expired at 40 MB of 100 MB.
func expiredFile(id int64, name string) *downexv1.Download {
	d := file(id, name, state.StateFailed)
	d.ErrorCode, d.Error = "link_expired", "bytes 0-9: HTTP 403 Forbidden: engine: link expired"
	d.Size, d.Received, d.PageUrl = 100_000_000, 40_000_000, "https://pages.example/files"
	return d
}

func lastToast(h *viewstest.Harness) string {
	items := h.App.Toasts.Items()
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1].Message
}

// TestRenewDialog: docs/DESKTOP-SPEC.md §10, the New link dialog.
func TestRenewDialog(t *testing.T) {
	t.Run("texts, the page link and a renewal", func(t *testing.T) {
		h, d := harness(t, expiredFile(1, "data.zip"))
		n := dialogs.NewRenew(h.App, views.RenewRequest{ID: 1}, nil)
		show(h, n)
		if s := n.Card().Semantics.Name; s != "New link" {
			t.Errorf("title %q", s)
		}
		if s := n.Card().Semantics.Description; s != "Downex continues “⁨data.zip⁩” from 40.0 MB of 100 MB after checking that the new link serves the same file." {
			t.Errorf("description %q", s)
		}
		if n.PageButton() == nil || n.PageButton().Label().Text() != "Open the download page" {
			t.Fatal("no page link")
		}
		if f := n.Field(); f.InputOptions().Name != "New link" || f.InputOptions().Placeholder != "https://…" {
			t.Errorf("field %+v", f.InputOptions())
		}
		cancel, submit := n.Buttons()
		if cancel.Label().Text() != "Cancel" || submit.Label().Text() != "Continue download" {
			t.Errorf("buttons %q %q", cancel.Label().Text(), submit.Label().Text())
		}
		n.Field().OnChange("https://cdn.example/data.zip?sig=2")
		n.Submit()
		h.Settle()
		if n.Shown() {
			t.Fatal("the dialog stayed open")
		}
		if got := lastToast(h); got != "“⁨data.zip⁩” continues with the new link." {
			t.Errorf("toast %q", got)
		}
		if n := d.Count("renewLink"); n != 1 {
			t.Fatalf("%d renewals", n)
		}
	})
	t.Run("a different file is refused under the field", func(t *testing.T) {
		h, _ := harness(t, expiredFile(1, "data.zip"))
		n := dialogs.NewRenew(h.App, views.RenewRequest{ID: 1}, nil)
		show(h, n)
		n.Field().OnChange("https://other.example/mismatch.zip")
		n.Submit()
		h.Settle()
		if !n.Shown() || n.NoteText() != "This link serves a different file: its bytes differ from what was downloaded." {
			t.Fatalf("shown %v note %q", n.Shown(), n.NoteText())
		}
		n.Field().OnChange("ftp://x")
		if n.NoteText() != "" {
			t.Errorf("an edit kept the note %q", n.NoteText())
		}
		n.Submit()
		if n.NoteText() != "Enter an http or https link." {
			t.Errorf("ftp note %q", n.NoteText())
		}
	})
	t.Run("another name: Continue anyway", func(t *testing.T) {
		h, d := harness(t, expiredFile(1, "data.zip"))
		n := dialogs.NewRenew(h.App, views.RenewRequest{ID: 1}, nil)
		show(h, n)
		n.Field().OnChange("https://cdn.example/name.zip")
		n.Submit()
		h.Settle()
		_, submit := n.Buttons()
		if !strings.Contains(n.NoteText(), "Downex keeps the name") || submit.Label().Text() != "Continue anyway" {
			t.Fatalf("note %q button %q", n.NoteText(), submit.Label().Text())
		}
		n.Submit()
		h.Settle()
		args := d.Args("renewLink")
		if n.Shown() || d.Count("renewLink") != 2 || len(args) != 2 || !args[1].(*downexv1.RenewLinkRequest).GetAllowNameChange() {
			t.Fatalf("shown %v args %v", n.Shown(), args)
		}
	})
	t.Run("texts of every refusal", func(t *testing.T) {
		for code, want := range map[string]string{
			"renew_unproven":  "Too little of the file can be compared to be sure this is the same file.",
			"renew_size":      "This link's file is 9.0 MB; this download is 4.0 MB.",
			"renew_changed":   "The server reports another version of the file. Restart the download to get the new version.",
			"renew_no_ranges": "This link's server sends only whole files, so the download cannot continue where it stopped. Restart the download to use it.",
			"link_expired":    "This link does not work either: it expired or leads to a web page.",
			"auth_required":   "The server asks for a user name and password. Add a site login for it, then try again.",
		} {
			msg := "9000000 bytes, expected 4000000: x"
			h, _ := harness(t, expiredFile(1, "data.zip"))
			n := dialogs.NewRenew(h.App, views.RenewRequest{ID: 1}, func(*downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
				return nil, statetest.RenewErr(code, msg)
			})
			show(h, n)
			n.Field().OnChange("https://cdn.example/a.zip")
			n.Submit()
			h.Settle()
			if n.NoteText() != want {
				t.Errorf("%s: note %q", code, n.NoteText())
			}
		}
	})
	t.Run("nothing received yet", func(t *testing.T) {
		d := expiredFile(1, "data.zip")
		d.Received, d.PageUrl = 0, ""
		h, _ := harness(t, d)
		n := dialogs.NewRenew(h.App, views.RenewRequest{ID: 1}, nil)
		show(h, n)
		if s := n.Card().Semantics.Description; s != "Downex downloads “⁨data.zip⁩” from the new link." || n.PageButton() != nil {
			t.Errorf("description %q page %v", s, n.PageButton())
		}
	})
}
