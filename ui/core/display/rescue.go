package display

import (
	"strings"

	"downex/internal/rescuetext"
	"downex/ui/core/format"
	"downex/ui/core/text"
)

// The texts of "Rescue partial file…" (docs/DESKTOP-SPEC.md §11) that more than one screen
// shows: the Add split button's menu and the palette, the dialog, the toast and the details
// pane.
const (
	RescueLabel       = "Rescue partial file…"
	RescueKeywords    = "crdownload part partial browser failed interrupted continue resume finish"
	RescueDescription = "Finishes a download a browser left unfinished (.crdownload, .part)"
)

// RescueErrorText is the explanation of a refused rescue with code on goos (the table of
// docs/BROWSER-SPEC.md §6, R-E7, shared with the extension and the CLI); "" for a code it
// does not word (the dialog then shows downexd's message).
func RescueErrorText(code, goos string) string { return rescuetext.Text(code, goos) }

// RescueFileError reports the codes the dialog shows under the partial file's field; the
// others go under the address. invalid_request is the file's too: the dialog checks the
// address itself, so downexd refuses only the file that way (not a regular file, or
// another download's own file).
func RescueFileError(code string) bool {
	switch code {
	case rescuetext.NotFound, rescuetext.Ambiguous, rescuetext.Locked, rescuetext.NoAccess, rescuetext.Empty,
		"invalid_request":
		return true
	}
	return false
}

// RescuedLabel names the details pane's row of the bytes a rescue kept.
const RescuedLabel = "Rescued"

// RescuedText is the details pane's "Rescued" value: "412.0 MB from a partial file".
func RescuedText(bytes int64) string { return format.Bytes(float64(bytes)) + " from a partial file" }

// RescuedToast is the toast after a rescue of the file named name that kept kept bytes.
func RescuedToast(name string, kept int64) string {
	return "Continuing “" + text.Isolate(name) + "” from " + format.Bytes(float64(kept))
}

// PartialKind names the browser a partial file's name comes from: "Chrome partial file
// (.crdownload)", "Firefox partial file (.part)", else "partial file".
func PartialKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".crdownload"):
		return "Chrome partial file (.crdownload)"
	case strings.HasSuffix(lower, ".part"):
		return "Firefox partial file (.part)"
	}
	return "partial file"
}

// RescueSaveAs is the "Save as" field's first value for a partial file named name: the
// name without .crdownload or .part, or "" for a provisional "Unconfirmed N.crdownload" (the
// server names it).
func RescueSaveAs(name string) string {
	lower := strings.ToLower(name)
	for _, suf := range []string{".crdownload", ".part"} {
		if strings.HasSuffix(lower, suf) {
			name = name[:len(name)-len(suf)]
			break
		}
	}
	if strings.HasPrefix(name, "Unconfirmed ") {
		return ""
	}
	return name
}
