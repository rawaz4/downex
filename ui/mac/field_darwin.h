// [fields] The AppKit side of ui/mac's native text fields (field.go): NSTextField and
// NSSecureTextField with a cell that draws like the kit, a multi-line NSTextView in an
// NSScrollView, the root-first key monitor, and the root view's own text input
// (NSTextInputClient). Each field is known to Objective-C by a state object (void *, +1,
// owned by Go until dxfRelease) and to Go by a runtime/cgo handle.
#ifndef DOWNEX_UI_MAC_FIELD_H
#define DOWNEX_UI_MAC_FIELD_H

#include <stdint.h>
#include <stdbool.h>
#include <CoreText/CoreText.h>

// Field kinds: as kit.FieldKind.
enum { DXF_TEXT = 0, DXF_PASSWORD = 1, DXF_MULTI = 2 };

// dxfNew makes a hidden field of kind in the window's content view.
void *dxfNew(void *win, uintptr_t handle, int kind);
// dxfSetStyle gives the field its look: font (retained), letter spacing, the text, ground
// and placeholder colours (sRGB, 4 each), placeholder and accessible name (UTF-8), the
// alignment (0 left, 1 centre, 2 right) and right-to-left reading order, and for a
// multi-line field its line box height and the baseline's place in it.
void dxfSetStyle(void *st, CTFontRef font, double kern, const double *fg, const double *bg,
	const double *ph, const char *placeholder, const char *name, int align, bool rtl,
	double lineHeight, double lineBaseline);
// dxfSetAlign changes only the alignment and reading order (dir=auto).
void dxfSetAlign(void *st, int align, bool rtl);
// dxfSetFrame places the field on (x, y, w, h), content-view points (y down); base is a
// single-line field's baseline below y. hide hides it instead.
void dxfSetFrame(void *st, double x, double y, double w, double h, double base, bool hide);
// dxfFrame reports where the field's view is and whether it is hidden (for the tests).
bool dxfFrame(void *st, double *x, double *y, double *w, double *h);
// dxfText is the field's text, malloc'd UTF-8 (a lone surrogate as U+FFFD; never NULL).
char *dxfText(void *st);
// dxfSetText replaces the text without a change notification, clearing the field's undo.
void dxfSetText(void *st, const char *s);
void dxfFocus(void *st);
void dxfBlur(void *st);
bool dxfFocused(void *st);
void dxfSelectAll(void *st);
// Selections are UTF-16 offsets.
void dxfSelection(void *st, long *start, long *end);
void dxfSetSelection(void *st, long start, long end);
void dxfReplaceSelection(void *st, const char *s);
void dxfSetMaxLength(void *st, long n);
void dxfSetPassword(void *st, bool on);
void dxfReclaim(void *st);
void dxfRelease(void *st);
// dxfWindowClosing forgets the window's field bookkeeping (its window is closing).
void dxfWindowClosing(void *win);

// The root view's text input (kit.Host.SetTextInput): while active, the keys the root
// does not use go through the input methods and come back as text; caret in content
// points (y down).
void dxfSetRootTextInput(void *win, bool active, double x, double y, double w, double h);
// dxfRootText is the root view's keyDown hook (window_darwin.m): it hands a key the root
// did not use to the input methods while the root's text input is active, and reports
// whether it did.
bool dxfRootText(void *view, void *event);
// dxfRootComposing is the root view's hook before it hands a key down to the root: while an
// input method composes for the root's text input, the key is the input method's (it never
// reaches the root, as in a field) and it reports true.
bool dxfRootComposing(void *view, void *event);
// dxfRepeatMove is the root view's mouseMoved: hook: AppKit can deliver one move both to the
// root (its tracking area) and to a focused field's text view, which hands it to the root
// (dxfMoved); it reports true for the second delivery of the move the view last heard.
bool dxfRepeatMove(void *view, void *event);

#endif
