//go:build darwin && cgo

// The Objective-C side of the macOS clipboard monitor (clipboard_darwin.go).

#ifndef DOWNEX_CLIPBOARD_DARWIN_H
#define DOWNEX_CLIPBOARD_DARWIN_H

// downexClipChangeCount returns the general pasteboard's changeCount.
long downexClipChangeCount(void);

// downexClipTypes returns the general pasteboard's types, one per line, as a string
// to be released with free (NULL when there are none), and stores the changeCount
// they belong to in *changeCount.
char *downexClipTypes(long *changeCount);

// downexClipText returns the general pasteboard's plain text as UTF-8, to be
// released with free; NULL when there is none or it is longer than maxBytes. The
// changeCount it belongs to is stored in *changeCount.
char *downexClipText(long maxBytes, long *changeCount);

#endif
