package add

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/views/viewstest"
)

// offered is a hand-off whose link serves the same file as stopped download 9.
func offered(differs bool) *downexv1.Download {
	return fileDownload(func(d *downexv1.Download) {
		d.Filename = "dataset (1).tar.zst"
		d.RenewOffer = &downexv1.RenewOffer{DownloadId: 9, Filename: "dataset.tar.zst", Size: 4_200_000_000,
			Received: 1_700_000_000, NameDiffers: differs}
	})
}

// TestConfirmOfferCard: docs/DESKTOP-SPEC.md §10.3.
func TestConfirmOfferCard(t *testing.T) {
	r := setupConfirm(t, confirmSetup{d: offered(false)})
	if r.c.Offer == nil || r.c.Offer.Style.Hidden {
		t.Fatal("no offer card")
	}
	if got := dialogText(r.c); !containsAll(got, "Same file as a stopped download",
		"“⁨dataset.tar.zst⁩” stopped at 1.7 GB of 4.2 GB because its link expired. This link serves the same file.") {
		t.Errorf("text %q", got)
	}
	if r.c.OfferButton.Label().Text() != "Continue that download" || r.c.OfferButton.Variant != 1 {
		t.Errorf("button %q", r.c.OfferButton.Label().Text())
	}
	// The card comes before the address, and the footer is unchanged.
	if !(r.c.Offer.WindowBounds().Max.Y <= r.c.address.WindowBounds().Min.Y) {
		t.Error("the card is not above the address")
	}
	if r.c.start.Label().Text() != "Start download" {
		t.Error("the footer changed")
	}

	var sent *downexv1.RenewLinkRequest
	r.c.o.Renew = func(_ context.Context, req *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
		sent = req
		return &downexv1.RenewLinkResponse{Download: &downexv1.Download{Id: 9}}, nil
	}
	r.h.Click(&r.c.OfferButton.Element)
	r.h.Settle()
	if sent == nil || sent.GetId() != 9 || sent.GetFromDownload() != 4 || !sent.GetStart() || !sent.GetAllowNameChange() {
		t.Fatalf("request %v", sent)
	}
	if !r.c.gone {
		t.Error("the dialog stayed")
	}
	if items := r.h.App.Toasts.Items(); len(items) == 0 || items[len(items)-1].Message != "“⁨dataset.tar.zst⁩” continues with the new link." {
		t.Errorf("toasts %+v", items)
	}

	other := setupConfirm(t, confirmSetup{d: offered(true)})
	if got := dialogText(other.c); !containsAll(got, "This link serves the same data under the name “⁨dataset (1).tar.zst⁩”.") {
		t.Errorf("another name: %q", got)
	}
	other.c.o.Renew = func(context.Context, *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("renew_mismatch: bytes 0-65535 differ"))
	}
	other.h.Click(&other.c.OfferButton.Element)
	other.h.Settle()
	if other.c.gone || other.c.failure == "" {
		t.Errorf("a refusal: gone %v failure %q", other.c.gone, other.c.failure)
	}

	if plain := setupConfirm(t, confirmSetup{d: fileDownload(nil)}); plain.c.Offer != nil {
		t.Error("a download without an offer shows a card")
	}
}

// TestConfirmOfferCardFits: the card keeps the 44rem file Confirm inside the window at the
// reference sizes and text sizes (the fit rule).
func TestConfirmOfferCardFits(t *testing.T) {
	for _, sz := range []struct{ w, h, font int }{{1200, 760, 16}, {1190, 752, 16}, {960, 608, 16}, {1200, 760, 20}, {1190, 752, 20}} {
		t.Run(fmt.Sprintf("%dx%d@%d", sz.w, sz.h, sz.font), func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{NoStart: true, Width: sz.w, Height: sz.h, Font: sz.font})
			c := NewConfirm(h.App, offered(true), ConfirmOptions{Settings: confirmSettings(), Requested: true})
			h.App.Dialogs.Show(c)
			h.Settle()
			if p := fitProblems(h, c, "confirm-renew", sz.w, sz.h, 1, sz.font); len(p) > 0 {
				t.Errorf("fit: %v", p)
			}
		})
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
