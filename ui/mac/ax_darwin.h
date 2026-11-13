// [ax] The Objective-C side of the native window's accessibility (ax.go, ax_darwin.m):
// DXAXElement, the NSAccessibilityElement of one semantic node, the content view's
// accessibility (a category on DXView), notifications and announcements, and the
// decoration of the native text fields that stand in for their nodes. Every decision is
// Go's (package ui/mac/ax); an element knows its window's bridge by a key into a Go table
// (never a Go pointer) and its node by id.
#ifndef DOWNEX_UI_MAC_AX_H
#define DOWNEX_UI_MAC_AX_H

#include <stdint.h>
#include <stdbool.h>

// dxaxInfo is what NSAccessibility shows of a node (ax.Info). Strings are malloc'd by Go
// (NULL when empty) and freed by dxaxInfoFree.
typedef struct {
	char *role, *subrole, *roleDesc, *title, *label, *help, *ident, *text, *valueDesc, *shortcut, *mark;
	int valueKind; // 0 none, 1 text, 2 number
	double number;
	int hasRange;
	double min, max;
	int flags; // DXAX_F_*
	int orientation, sort;
	int rows, cols, index, rowLoc, rowLen, colLoc, colLen, posInSet, setSize, level;
	int actions, settable; // ax.Act*, ax.Set*
	double x, y, w, h; // window DIPs (content view points, y down)
} dxaxInfo;

enum {
	DXAX_F_ENABLED = 1 << 0,
	DXAX_F_FOCUSED = 1 << 1,
	DXAX_F_SELECTED = 1 << 2,
	DXAX_F_EXPANDABLE = 1 << 3,
	DXAX_F_EXPANDED = 1 << 4,
	DXAX_F_REQUIRED = 1 << 5,
	DXAX_F_PROTECTED = 1 << 6,
	DXAX_F_INVALID = 1 << 7,
	DXAX_F_TABLE = 1 << 8,
	DXAX_F_ROW = 1 << 9,
	DXAX_F_CELL = 1 << 10,
	DXAX_F_BUSY = 1 << 11,
	DXAX_F_MODAL = 1 << 12,
	DXAX_F_MULTI = 1 << 13,
	DXAX_F_CONTROL = 1 << 14,   // AXInvalid even while valid (WebKit's form controls)
	DXAX_F_CHECKABLE = 1 << 15, // a checkable menu item: AXMenuItemMarkChar
};

// Lists an element asks Go for (goAXList's kind).
enum { DXAX_CHILDREN, DXAX_ROWS, DXAX_VISIBLE_ROWS, DXAX_SELECTION, DXAX_COLUMN_HEADERS };

// Actions (goAXAction's act) and settable attributes (goAXSet*'s what).
enum { DXAX_PRESS = 1, DXAX_INCREMENT = 2, DXAX_DECREMENT = 4, DXAX_SHOWMENU = 8, DXAX_SCROLL = 16 };
enum { DXAX_SET_FOCUSED = 2, DXAX_SET_SELECTED = 4, DXAX_SET_EXPANDED = 8 };

// Notifications (ax.Notification's values).
enum {
	DXAX_N_FOCUS = 1,
	DXAX_N_VALUE,
	DXAX_N_TITLE,
	DXAX_N_SELECTED_CHILDREN,
	DXAX_N_SELECTED_ROWS,
	DXAX_N_ROW_COUNT,
	DXAX_N_LAYOUT,
	DXAX_N_EXPANDED,
	DXAX_N_COUNT
};

// dxaxElementNew makes the element of node id of the bridge key over view (+1, owned by Go
// until dxaxElementDestroy).
void *dxaxElementNew(uintptr_t key, uint64_t id, void *view);
// dxaxElementDestroy marks the element dead, posts NSAccessibilityUIElementDestroyed for
// it and drops Go's reference.
void dxaxElementDestroy(void *e);
// dxaxPost posts notification n (DXAX_N_*) about obj (an element, a field, or the content
// view, which stands for its window).
void dxaxPost(void *obj, int n);
// dxaxAnnounce has VoiceOver read text (priority: 1 medium, 2 high), posted about the
// window of view (the content view of the host that announced), else the application.
void dxaxAnnounce(void *view, const char *text, int priority);
// dxaxFieldIn is the native text field (NSTextField, NSTextView or the NSScrollView of
// one) shown in view whose box lies in the rectangle (view points, y down) with slack, or
// NULL. A client sees an NSTextField as its cell and an NSScrollView as its text view.
void *dxaxFieldIn(void *view, double x0, double y0, double x1, double y1, double slack);
// dxaxFieldDecorate gives a native field its node's accessibility.
void dxaxFieldDecorate(void *field, void *parent, void *view, const char *label, const char *help,
	const char *ident, bool required);
// dxaxSetViewKey ties a content view to its bridge key (0 unties it).
void dxaxSetViewKey(void *view, uintptr_t key);
// dxaxContentView is a DXWindow's content view (unretained).
void *dxaxContentView(void *win);
// dxaxBump tells cached element information that the tree may have changed.
void dxaxBump(void);
// dxaxRefused is the number of accessibility requests refused because they came off the
// main thread.
long dxaxRefused(void);
// dxaxPosted is how many notifications of kind n were posted (DXAX_N_*; 0: destroyed,
// DXAX_N_COUNT: announcements).
long dxaxPosted(int n);
// dxaxLastAnnouncement is the last announcement's text and priority (malloc'd, or NULL),
// and the object it was posted about (unretained: compare only).
char *dxaxLastAnnouncement(int *priority, void **target);

// ---- an in-process client (ax_inspect.go): AppKit's legacy NSAccessibility API, which
// is what the system's accessibility server calls for VoiceOver ----

typedef struct {
	int kind; // 0 nil, 1 string, 2 number, 3 element, 4 elements, 5 rect, 6 point, 7 size, 8 range, 9 other
	char *s;
	double n;
	void *e;
	void **es;
	int count;
	double x, y, w, h;
} dxaxVal;

// dxaxAttr reads attribute name of obj (param: an optional parameter as up to 2 ints,
// nparam of them; nparam -1: through accessibilityAttributeValue: itself). Strings and
// arrays are malloc'd, elements retained.
void dxaxAttr(void *obj, const char *name, int nparam, int p0, int p1, dxaxVal *out);
// dxaxNames lists obj's attribute (what 0), action (1) or parameterized attribute (2)
// names, or accessibilityAttributeNames itself (3), joined by newlines (malloc'd).
char *dxaxNames(void *obj, int what);
bool dxaxSettable(void *obj, const char *name);
// dxaxSet sets attribute name (kind: 1 string s, 2 number n, 3 bool n, 4 the elements es).
void dxaxSet(void *obj, const char *name, int kind, const char *s, double n, void **es, int count);
void dxaxPerform(void *obj, const char *action);
// dxaxHit is the element at a screen point (Cocoa's, y up) of obj (+1, or NULL).
void *dxaxHit(void *obj, double x, double y);
// dxaxWindowElement is win itself as an accessibility element (+1).
void *dxaxWindowElement(void *win);
void dxaxRetain(void *obj);
void dxaxRelease(void *obj);
// dxaxClassName is obj's class (malloc'd).
char *dxaxClassName(void *obj);

#endif
