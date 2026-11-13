package clipboard

import "slices"

// Clipboard formats (Windows) and pasteboard types (macOS) that mark content which
// clipboard monitors must leave alone (passwords, one-time codes, transient data).
const (
	// FormatExcludeFromMonitoring (Windows): present = ignore the content.
	FormatExcludeFromMonitoring = "ExcludeClipboardContentFromMonitorProcessing"
	// FormatViewerIgnore (Windows): the older convention of password managers for the same.
	FormatViewerIgnore = "Clipboard Viewer Ignore"
	// FormatCanIncludeInHistory (Windows): a DWORD; 0 = keep out of the clipboard history
	// (treated as sensitive here).
	FormatCanIncludeInHistory = "CanIncludeInClipboardHistory"
	// TypeConcealed (macOS): the content is a password or similar.
	TypeConcealed = "org.nspasteboard.ConcealedType"
	// TypeTransient (macOS): the content is put on the pasteboard only briefly.
	TypeTransient = "org.nspasteboard.TransientType"
)

// windowsExcluded reports whether Windows clipboard content is excluded from
// monitoring: either exclusion format is present, or CanIncludeInClipboardHistory is
// present with the value 0 (history is that format's DWORD, nil when absent or
// unreadable; an unreadable value counts as 0, to err on the side of privacy).
func windowsExcluded(excludeFormat, viewerIgnore bool, historyPresent bool, history *uint32) bool {
	if excludeFormat || viewerIgnore {
		return true
	}
	return historyPresent && (history == nil || *history == 0)
}

// macExcluded reports whether pasteboard content with these types is excluded.
func macExcluded(types []string) bool {
	return slices.Contains(types, TypeConcealed) || slices.Contains(types, TypeTransient)
}
