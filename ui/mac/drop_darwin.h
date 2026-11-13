// [popups] The AppKit side of drag and drop onto a window (drop.go): the window's content
// view becomes an NSDraggingDestination for links, text and files.
#ifndef DOWNEX_UI_MAC_DROP_H
#define DOWNEX_UI_MAC_DROP_H

#include <stdint.h>
#include <stdbool.h>

// dxpDropEnable registers win's content view for dragged links, text and files; its drags
// go to goDrop* with handle. dxpDropDisable undoes it.
void dxpDropEnable(void *win, uintptr_t handle);
void dxpDropDisable(void *win);

// dxpDropData is what a drag carries, read from its pasteboard (UTF-8, malloc'd; NULL for
// a type that is not there).
typedef struct {
	char *url;      // public.url that is not a file URL
	char *text;     // public.utf8-plain-text
	char *html;     // public.html
	char *file;     // the first file's path
	char *webloc;   // the URL of the first file when it is a .webloc
	bool files;     // file URLs or promised files
	bool hasText, hasHTML, hasURL;
} dxpDropData;

// dxpDropInsert puts text into the editable text field or view at (x, y) of win's content
// (points, y down), at the character under the point; it reports whether there was one.
bool dxpDropInsert(void *win, double x, double y, const char *text);
// dxpDropFieldAt reports an editable text field or view at (x, y).
bool dxpDropFieldAt(void *win, double x, double y);

#endif
