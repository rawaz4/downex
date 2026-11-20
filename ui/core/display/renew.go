package display

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/text"
)

// The texts of expired links (docs/DESKTOP-SPEC.md §10) that more than one screen shows:
// the list's and the details pane's buttons, the progress window's, the confirmation's
// offer card and the toasts. The native window has them; today's WebView window does not.
const (
	NewLinkLabel  = "New link…"
	OpenPageLabel = "Open page"
	// OfferTitle and OfferButton are the confirmation's offer card.
	OfferTitle  = "Same file as a stopped download"
	OfferButton = "Continue that download"
	// OpenPageFailed is the toast of an "Open page" the browser did not take.
	OpenPageFailed = "Downex could not open the page."
)

// OfferText is the offer card's text for offer o of an add whose proposed name is newName.
func OfferText(o *downexv1.RenewOffer, newName string) string {
	s := "“" + text.Isolate(o.GetFilename()) + "” stopped at " + format.Bytes(float64(o.GetReceived())) + " of " +
		format.Bytes(float64(o.GetSize())) + " because its link expired. "
	if o.GetNameDiffers() && newName != "" {
		return s + "This link serves the same data under the name “" + text.Isolate(newName) + "”."
	}
	return s + "This link serves the same file."
}

// RenewedToast is the toast after a renewal of the download named name: it continues, or
// (restarted) it downloads again from the start.
func RenewedToast(name string, restarted bool) string {
	if restarted {
		return "“" + text.Isolate(name) + "” downloads again from the start with the new link."
	}
	return "“" + text.Isolate(name) + "” continues with the new link."
}
