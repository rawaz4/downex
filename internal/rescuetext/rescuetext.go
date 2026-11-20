// Package rescuetext holds the texts that explain why a rescue of a browser's partial
// file failed (docs/BROWSER-SPEC.md §6, table R-E7). The desktop window's dialog and the
// CLI use them; the extension repeats them in ext/src/lib/rescue.ts, which
// TestExtensionRepeatsTexts holds to this table.
package rescuetext

// Error codes the table words (docs/DAEMON-SPEC.md §6).
const (
	NoRanges    = "rescue_no_ranges"
	SizeUnknown = "rescue_size_unknown"
	Changed     = "rescue_changed"
	Unproven    = "rescue_unproven"
	NotFound    = "rescue_not_found"
	Ambiguous   = "rescue_ambiguous"
	Locked      = "rescue_locked"
	NoAccess    = "rescue_no_access"
	Empty       = "rescue_empty"
	LinkExpired = "link_expired"
	HTTPStatus  = "http_status"
	Auth        = "auth_required"
	Network     = "network"
)

// texts is the table R-E7.
var texts = map[string]string{
	NoRanges:    "This server cannot continue a download from the middle, so the saved part cannot be used.",
	SizeUnknown: "The server does not say how big the file is, so Downex cannot check the saved part.",
	Changed:     "The file on the server is not the one the browser started, so the saved part cannot be used.",
	Unproven:    "The saved part holds too little that tells files apart, so Downex cannot check it.",
	NotFound:    "The browser's partial file is gone. Browsers delete it when a download is cancelled.",
	Ambiguous:   "More than one unfinished browser file could be this one. In Downex, use Rescue partial file… and choose it.",
	Locked:      "The browser is still using the partial file. Try again in a few seconds.",
	NoAccess:    "Downex is not allowed to open the partial file.",
	Empty:       "The partial file is empty: there is nothing to finish.",
	LinkExpired: "The address has expired. Start the download again on the web page.",
	HTTPStatus:  "The server refused the address. It may have expired: start the download again on the web page.",
	Auth:        "The server asks for a sign-in. Add a site login in Downex, then try again.",
	Network:     "Downex could not reach the server.",
}

// MacNoAccess is added to the rescue_no_access text on macOS.
const MacNoAccess = " Allow Downex Agent in System Settings > Privacy & Security > Files and Folders."

// Text is the explanation of code on goos ("" for a code the table does not word: the
// caller shows the daemon's message).
func Text(code, goos string) string {
	t := texts[code]
	if code == NoAccess && goos == "darwin" {
		t += MacNoAccess
	}
	return t
}

// FromStart reports the codes after which the download can only start over: the saved
// part is unusable but the address works (the offer "Download from the start").
func FromStart(code string) bool {
	return code == NoRanges || code == SizeUnknown || code == Changed || code == Unproven
}

// Retryable reports the codes after which trying again may succeed.
func Retryable(code string) bool {
	return code == Locked || code == Network
}

// Codes lists the codes the table words.
func Codes() []string {
	return []string{NoRanges, SizeUnknown, Changed, Unproven, NotFound, Ambiguous, Locked, NoAccess, Empty,
		LinkExpired, HTTPStatus, Auth, Network}
}
