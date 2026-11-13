//go:build darwin && cgo

// [fields] The AppKit side of ui/mac's native text fields (see field_darwin.h). Every
// decision about what the root sees is Go's (field.go); this file holds the views, draws
// them as the kit draws text, and routes the keys. Main thread only.

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#import <objc/message.h>
#include "field_darwin.h"
#include "textct.h"
#include "_cgo_export.h"

// The window's content view (window_darwin.m); only its handle is used here.
@interface DXView : NSView
@property(nonatomic) uintptr_t handle;
@end

static int dxfMods(NSEventModifierFlags f) {
	int m = 0;
	if (f & NSEventModifierFlagShift) m |= 1;
	if (f & NSEventModifierFlagControl) m |= 2;
	if (f & NSEventModifierFlagOption) m |= 4;
	if (f & NSEventModifierFlagCommand) m |= 8;
	return m;
}

static unichar dxfPlainChar(NSEvent *e) {
	NSString *plain = e.charactersIgnoringModifiers;
	return plain.length == 1 ? [plain characterAtIndex:0] : 0;
}

// ---- text across the boundary: never nil, never NULL ----------------------------------

// dxfStr is s (UTF-8 from Go, made valid there: fldUTF8) as an NSString; anything AppKit
// cannot read is the empty string rather than a nil that would raise.
static NSString *dxfStr(const char *s) {
	NSString *t = s ? [NSString stringWithUTF8String:s] : nil;
	return t ? t : @"";
}

// dxfWellFormed is s with each lone surrogate as U+FFFD, as a browser's UTF-8 encoder
// writes it: such a string has no UTF8String.
static NSString *dxfWellFormed(NSString *s) {
	NSUInteger n = s.length;
	NSMutableString *out = nil;
	for (NSUInteger i = 0; i < n; i++) {
		unichar c = [s characterAtIndex:i];
		bool lone = false;
		if (CFStringIsSurrogateHighCharacter(c)) {
			if (i + 1 < n && CFStringIsSurrogateLowCharacter([s characterAtIndex:i + 1])) {
				i++;
				continue;
			}
			lone = true;
		} else if (CFStringIsSurrogateLowCharacter(c)) {
			lone = true;
		}
		if (lone) {
			if (!out) out = [s mutableCopy];
			[out replaceCharactersInRange:NSMakeRange(i, 1) withString:@"�"];
		}
	}
	return out ? out : (s ? s : @"");
}

// dxfUTF8 is s as malloc'd UTF-8 (never NULL).
static char *dxfUTF8(NSString *s) {
	const char *u = dxfWellFormed(s).UTF8String;
	return strdup(u ? u : "");
}

// dxfSnap is r in s with no edge inside a character (a surrogate pair, a combining
// sequence), as the field editor puts a selection: a caret goes to the start of the
// character it falls in, a range grows to the whole characters it touches.
static NSRange dxfSnap(NSString *s, NSRange r) {
	NSUInteger n = s.length;
	if (r.location > n) r = NSMakeRange(n, 0);
	if (NSMaxRange(r) > n) r.length = n - r.location;
	if (r.length == 0) {
		if (r.location < n) r.location = [s rangeOfComposedCharacterSequenceAtIndex:r.location].location;
		return r;
	}
	return [s rangeOfComposedCharacterSequencesForRange:r];
}

// ---- the field's state --------------------------------------------------------------

@class DXFTextView;

@interface DXFState : NSObject <NSTextFieldDelegate, NSTextViewDelegate, NSLayoutManagerDelegate> {
@public
	double fg[4], bg[4], ph[4];
}
@property(nonatomic) uintptr_t handle;
@property(nonatomic) int kind;
@property(nonatomic, weak) NSWindow *window;
@property(nonatomic, strong) NSView *view;        // the NSTextField, or the multi-line NSScrollView
@property(nonatomic, strong) DXFTextView *text;   // a multi-line field's text view
@property(nonatomic, strong) NSTextView *editor;  // a plain field's own field editor
@property(nonatomic, strong) NSUndoManager *undo; // the field's own undo, as an input has
@property(nonatomic) CTFontRef font;
@property(nonatomic) double kern;
@property(nonatomic, copy) NSString *placeholder;
@property(nonatomic, copy) NSString *name;
@property(nonatomic) int align;
@property(nonatomic) bool rtl;
@property(nonatomic) double base;       // the baseline below the view's top (single line)
@property(nonatomic) double lineHeight; // each line's box
@property(nonatomic) double lineBase;   // the baseline below a line box's top
@property(nonatomic) long maxLength;
@property(nonatomic) NSRange saved;     // a single-line field's selection while not edited
@property(nonatomic) bool hadFocus;
@property(nonatomic) bool fixing;   // an edit made again (max length, line breaks)
@property(nonatomic) bool swapping; // SetPassword is replacing the control
@property(nonatomic) bool composed;   // an input method's text is in the field: where, how long
@property(nonatomic) NSUInteger compStart, compLen;
@property(nonatomic) bool trimming;   // dxfTrimCommit's own edit
@property(nonatomic) bool released;
@end

static NSColor *dxfColor(const double *c) {
	return [NSColor colorWithSRGBRed:c[0] green:c[1] blue:c[2] alpha:c[3]];
}

// dxfEditorOf is the text view the user edits in: a multi-line field's, or a single-line
// field's field editor while it is edited (nil otherwise).
static NSTextView *dxfEditorOf(DXFState *st) {
	if (st.kind == DXF_MULTI) return (NSTextView *)st.text;
	NSText *t = [(NSTextField *)st.view currentEditor];
	return [t isKindOfClass:NSTextView.class] ? (NSTextView *)t : nil;
}

static NSTextAlignment dxfNSAlign(int a) {
	switch (a) {
	case 1: return NSTextAlignmentCenter;
	case 2: return NSTextAlignmentRight;
	}
	return NSTextAlignmentLeft;
}

static NSParagraphStyle *dxfParagraph(DXFState *st) {
	NSMutableParagraphStyle *p = [NSMutableParagraphStyle new];
	p.alignment = dxfNSAlign(st.align);
	p.baseWritingDirection = st.rtl ? NSWritingDirectionRightToLeft : NSWritingDirectionLeftToRight;
	if (st.kind == DXF_MULTI && st.lineHeight > 0) {
		p.minimumLineHeight = st.lineHeight;
		p.maximumLineHeight = st.lineHeight;
	}
	return p;
}

static NSDictionary *dxfAttrs(DXFState *st) {
	NSMutableDictionary *a = [NSMutableDictionary dictionary];
	if (st.font) a[NSFontAttributeName] = (__bridge NSFont *)st.font;
	a[NSForegroundColorAttributeName] = dxfColor(st->fg);
	a[NSParagraphStyleAttributeName] = dxfParagraph(st);
	if (st.kern != 0) {
		a[NSKernAttributeName] = @(st.kern);
		a[NSLigatureAttributeName] = @0;
	}
	return a;
}

// dxfQuiet turns off what an NSTextView would change on its own: a browser's input and
// Windows' EDIT change nothing the user types (URLs, proxy addresses, "--" arguments).
static void dxfQuiet(NSTextView *tv) {
	tv.automaticQuoteSubstitutionEnabled = NO;
	tv.automaticDashSubstitutionEnabled = NO;
	tv.automaticTextReplacementEnabled = NO;
	tv.automaticSpellingCorrectionEnabled = NO;
	tv.continuousSpellCheckingEnabled = NO;
	tv.grammarCheckingEnabled = NO;
	tv.automaticLinkDetectionEnabled = NO;
	tv.automaticDataDetectionEnabled = NO;
	tv.automaticTextCompletionEnabled = NO;
	tv.smartInsertDeleteEnabled = NO;
	tv.enabledTextCheckingTypes = 0;
	tv.allowsUndo = YES;
	tv.richText = NO;
	tv.importsGraphics = NO;
	tv.usesFindBar = NO;
	tv.usesFontPanel = NO;
	tv.usesRuler = NO;
}

// dxfStyleEditor gives a text view the field's font, colours, alignment and direction.
static void dxfStyleEditor(DXFState *st, NSTextView *tv) {
	if (!tv) return;
	dxfQuiet(tv);
	NSDictionary *a = dxfAttrs(st);
	tv.insertionPointColor = dxfColor(st->fg); // WebKit's caret is currentColor
	tv.textContainer.lineFragmentPadding = 0;
	tv.alignment = dxfNSAlign(st.align);
	tv.baseWritingDirection = st.rtl ? NSWritingDirectionRightToLeft : NSWritingDirectionLeftToRight;
	tv.typingAttributes = a;
	if (!tv.hasMarkedText && tv.textStorage.length > 0) {
		[tv.textStorage beginEditing];
		[tv.textStorage setAttributes:a range:NSMakeRange(0, tv.textStorage.length)];
		[tv.textStorage endEditing];
	}
	if (st.kind != DXF_MULTI) {
		tv.drawsBackground = NO; // the control's cell draws the ground (and the placeholder)
		double top = st.base - st.lineBase;
		if (st.editor != tv) {
			// The system's secure field editor lays out by its own line metrics.
			top = st.base - [tv.layoutManager defaultBaselineOffsetForFont:(__bridge NSFont *)st.font];
		}
		tv.textContainerInset = NSMakeSize(0, MAX(top, 0));
	}
}

// ---- drawing as the kit draws -------------------------------------------------------

// dxfDrawLine draws s on one line of width w with its baseline y below the top of the
// flipped context c, in the field's alignment, with the kit's text engine (textct.go).
static void dxfDrawLine(DXFState *st, CGContextRef c, NSString *s, double w, double h, double y, const double *col) {
	if (!st.font || s.length == 0) return;
	NSUInteger n = s.length;
	UniChar *u = malloc(n * sizeof(UniChar));
	[s getCharacters:u range:NSMakeRange(0, n)];
	CFDictionaryRef attrs = dxtAttributes(st.font, st.kern, st.rtl ? 1 : 0);
	CTLineRef l = dxtLineOf(u, (CFIndex)n, attrs);
	free(u);
	double adv = 0, trailing = 0;
	dxtLineBounds(l, &adv, &trailing);
	double room = w - adv, x = 0;
	switch (st.align) {
	case 1: x = room / 2; break;
	case 2: x = room; break;
	}
	if (room < 0) x = st.rtl ? room : 0; // too long: its start shows, as an input's does
	CGContextSaveGState(c);
	CGContextClipToRect(c, CGRectMake(0, 0, w, h));
	dxtTextBegin(c, col[0], col[1], col[2], col[3]);
	dxtDrawLine(c, l, x, y);
	dxtTextEnd(c);
	CGContextRestoreGState(c);
	CFRelease(l);
	CFRelease(attrs);
}

// dxfDrawMarks draws a password field's text as shown: one mark per character, the font's
// mediumsmallblackcircle glyph that AppKit's secure field editor and WebKit's password
// input both draw (no character maps to it), at the font's advance and letter spacing.
static void dxfDrawMarks(DXFState *st, CGContextRef c, NSString *s, double w, double h, double y, const double *col) {
	if (!st.font || s.length == 0) return;
	__block NSUInteger n = 0;
	[s enumerateSubstringsInRange:NSMakeRange(0, s.length) options:NSStringEnumerationByComposedCharacterSequences
		usingBlock:^(NSString *sub, NSRange r, NSRange er, BOOL *stop) { n++; }];
	CGGlyph g = CTFontGetGlyphWithName(st.font, CFSTR("mediumsmallblackcircle"));
	if (g == 0) {
		UniChar u = 0x25CF;
		CTFontGetGlyphsForCharacters(st.font, &u, &g, 1);
	}
	CGSize a;
	CTFontGetAdvancesForGlyphs(st.font, kCTFontOrientationHorizontal, &g, &a, 1);
	double adv = a.width + st.kern;
	double total = adv * (double)n, room = w - total, x = 0;
	switch (st.align) {
	case 1: x = room / 2; break;
	case 2: x = room; break;
	}
	if (room < 0) x = st.rtl ? room : 0;
	CGGlyph *gs = malloc(n * sizeof(CGGlyph));
	CGPoint *ps = malloc(n * sizeof(CGPoint));
	CGContextSaveGState(c);
	CGContextClipToRect(c, CGRectMake(0, 0, w, h));
	dxtTextBegin(c, col[0], col[1], col[2], col[3]);
	CGAffineTransform t = CGContextGetCTM(c);
	if (t.b == 0 && t.c == 0) { // the baseline on a device pixel, as dxtDrawLine puts it
		CGPoint d = CGContextConvertPointToDeviceSpace(c, CGPointMake(0, y));
		d.y = round(d.y);
		y = CGContextConvertPointToUserSpace(c, d).y;
	}
	for (NSUInteger i = 0; i < n; i++) {
		gs[i] = g;
		ps[i] = CGPointMake(x + adv * (double)i, -y); // the text matrix flips y
	}
	CTFontDrawGlyphs(st.font, gs, ps, n, c);
	dxtTextEnd(c);
	CGContextRestoreGState(c);
	free(gs);
	free(ps);
}

// dxfDrawCell draws a single-line field: its ground, then its text or its placeholder
// (only the placeholder while it is edited: the field editor draws the text).
static void dxfDrawCell(DXFState *st, NSRect frame, NSControl *view) {
	CGContextRef c = NSGraphicsContext.currentContext.CGContext;
	if (!c || !st) return;
	CGContextSaveGState(c);
	CGColorRef g = CGColorCreateSRGB(st->bg[0], st->bg[1], st->bg[2], st->bg[3]);
	CGContextSetFillColorWithColor(c, g);
	CGColorRelease(g);
	CGContextFillRect(c, frame);
	CGContextRestoreGState(c);
	NSText *ed = view.currentEditor;
	NSString *s = ed ? ed.string : view.stringValue;
	if (s.length == 0) {
		dxfDrawLine(st, c, st.placeholder, frame.size.width, frame.size.height, st.base, st->ph);
	} else if (!ed) {
		if (st.kind == DXF_PASSWORD) {
			dxfDrawMarks(st, c, s, frame.size.width, frame.size.height, st.base, st->fg);
		} else {
			dxfDrawLine(st, c, s, frame.size.width, frame.size.height, st.base, st->fg);
		}
	}
}

// ---- edits --------------------------------------------------------------------------

// dxfShouldChange applies the field's rules to an edit: a single-line field takes no line
// breaks, no field takes NULs, and the max length (UTF-16 units, never splitting a
// character) holds for what the user types and pastes, not while an input method composes.
// An edit the rules change is made again, changed, as one undoable step.
static BOOL dxfShouldChange(DXFState *st, NSTextView *tv, NSRange r, NSString *s) {
	if (!s || st.fixing) return YES;
	NSString *t = s;
	if ([t rangeOfString:@"\0"].location != NSNotFound) t = [t stringByReplacingOccurrencesOfString:@"\0" withString:@""];
	if (st.kind != DXF_MULTI && [t rangeOfCharacterFromSet:[NSCharacterSet characterSetWithCharactersInString:@"\r\n"]].location != NSNotFound) {
		t = [[t stringByReplacingOccurrencesOfString:@"\r" withString:@""] stringByReplacingOccurrencesOfString:@"\n" withString:@""];
	}
	if (tv.hasMarkedText) { // the composition, or its commit: dxfTrimCommit holds the max length
		st.composed = YES;
		st.compStart = r.location;
		st.compLen = t.length;
	}
	if (st.maxLength > 0 && !tv.hasMarkedText) {
		long room = st.maxLength - ((long)tv.string.length - (long)r.length);
		if (room < (long)t.length) {
			if (room <= 0) {
				t = @"";
			} else {
				NSRange cr = [t rangeOfComposedCharacterSequenceAtIndex:(NSUInteger)room];
				if ((long)cr.location < room) room = (long)cr.location;
				t = [t substringToIndex:(NSUInteger)room];
			}
		}
	}
	if ([t isEqualToString:s]) return YES;
	if (t.length > 0 || r.length > 0) {
		st.fixing = YES;
		[tv insertText:t replacementRange:r];
		st.fixing = NO;
	}
	return NO;
}

// dxfCommand decides the editing commands the field must not do itself: Enter, Escape
// (AppKit's completion list), Tab (the key-view loop) are the root's; a single-line field
// never ends its editing on them, beeps or selects everything. noop: (a Command key nobody
// used) does not beep, as a browser's input does not.
static BOOL dxfCommand(DXFState *st, SEL sel) {
	if (sel == @selector(cancelOperation:) || sel == @selector(complete:) || sel == @selector(insertTab:) ||
		sel == @selector(insertBacktab:) || sel == @selector(insertTabIgnoringFieldEditor:) || sel == @selector(noop:)) {
		return YES;
	}
	if (st.kind != DXF_MULTI && (sel == @selector(insertNewline:) || sel == @selector(insertLineBreak:) ||
		sel == @selector(insertNewlineIgnoringFieldEditor:) || sel == @selector(insertParagraphSeparator:))) {
		return YES;
	}
	return NO;
}

// dxfTrimCommit holds the max length once a composition ends, as WebKit cuts an input's
// committed composition: the end of the committed text goes (whole characters), as one more
// undoable step; the caret follows what is left.
static void dxfTrimCommit(DXFState *st) {
	if (!st.composed) return;
	NSTextView *tv = dxfEditorOf(st);
	if (!tv || tv.hasMarkedText) return;
	st.composed = NO;
	NSString *s = tv.string;
	long excess = (long)s.length - st.maxLength;
	if (st.maxLength <= 0 || excess <= 0) return;
	NSUInteger a = MIN(st.compStart, s.length), b = MIN(st.compStart + st.compLen, s.length);
	if (b <= a) return;
	NSUInteger cut = b - MIN((NSUInteger)excess, b - a);
	if (cut < b) cut = [s rangeOfComposedCharacterSequenceAtIndex:cut].location;
	if (cut < a) cut = a;
	NSRange del = NSMakeRange(cut, b - cut);
	st.trimming = YES;
	st.fixing = YES;
	if ([tv shouldChangeTextInRange:del replacementString:@""]) {
		[tv replaceCharactersInRange:del withString:@""];
		[tv didChangeText];
	}
	st.fixing = NO;
	st.trimming = NO;
	tv.selectedRange = NSMakeRange(cut, 0);
}

static void dxfChanged(DXFState *st) {
	if (st.released || st.trimming) return; // a trim: the change that made it notifies
	dxfTrimCommit(st);
	[st.view setNeedsDisplay:YES]; // the placeholder goes or comes back
	if (st.text) [(NSView *)st.text setNeedsDisplay:YES];
	goFldChanged(st.handle);
}

@implementation DXFState
- (BOOL)control:(NSControl *)c textView:(NSTextView *)tv doCommandBySelector:(SEL)sel {
	return dxfCommand(self, sel);
}
- (void)controlTextDidChange:(NSNotification *)n { dxfChanged(self); }
- (void)controlTextDidBeginEditing:(NSNotification *)n { [self.view setNeedsDisplay:YES]; }
- (void)controlTextDidEndEditing:(NSNotification *)n {
	NSTextView *ed = n.userInfo[@"NSFieldEditor"];
	if ([ed isKindOfClass:NSTextView.class]) self.saved = ed.selectedRange;
	[self.view setNeedsDisplay:YES];
}
// The multi-line text view's delegate.
- (BOOL)textView:(NSTextView *)tv doCommandBySelector:(SEL)sel { return dxfCommand(self, sel); }
- (BOOL)textView:(NSTextView *)tv shouldChangeTextInRange:(NSRange)r replacementString:(NSString *)s {
	return dxfShouldChange(self, tv, r, s);
}
- (void)textDidChange:(NSNotification *)n { dxfChanged(self); }
- (NSUndoManager *)undoManagerForTextView:(NSTextView *)tv { return self.undo; }
// The line boxes of the field's own text views: the kit's line height, the baseline where
// WebKit puts it (half-leading).
- (BOOL)layoutManager:(NSLayoutManager *)lm shouldSetLineFragmentRect:(inout NSRect *)rect
	lineFragmentUsedRect:(inout NSRect *)used baselineOffset:(inout CGFloat *)base
	inTextContainer:(NSTextContainer *)tc forGlyphRange:(NSRange)gr {
	if (self.lineHeight <= 0) return NO;
	rect->size.height = self.lineHeight;
	used->size.height = self.lineHeight;
	*base = self.lineBase;
	return YES;
}
- (void)dealloc {
	if (_font) CFRelease(_font);
}
@end

// ---- the views ----------------------------------------------------------------------

// dxfMoved hands a pointer move over a field to the window's root view, so that the root
// hears it (the box's hover): AppKit sends moves to the first responder, which is the
// field's text view while it has the focus, and the root view's tracking area does not
// hear them then.
static void dxfMoved(NSView *v, NSEvent *e) {
	NSView *root = v.window.contentView;
	if (root && root != v) [root mouseMoved:e];
}

// DXFEditor is a plain single-line field's own field editor, a TextKit 1 view whose line box
// the state sets; it draws glyphs as WebKit draws them (no font smoothing).
@interface DXFEditor : NSTextView
@end

@implementation DXFEditor
- (void)drawRect:(NSRect)r {
	CGContextRef c = NSGraphicsContext.currentContext.CGContext;
	CGContextSetShouldSmoothFonts(c, false);
	CGContextSetAllowsFontSmoothing(c, false);
	[super drawRect:r];
}
- (void)mouseMoved:(NSEvent *)e { dxfMoved(self, e); }
@end

static NSTextView *dxfMakeTextView(DXFState *st, Class cls, bool fieldEditor) {
	NSTextStorage *ts = [NSTextStorage new];
	NSLayoutManager *lm = [NSLayoutManager new];
	lm.delegate = st;
	lm.usesFontLeading = NO;
	[ts addLayoutManager:lm];
	NSTextContainer *tc = [[NSTextContainer alloc] initWithSize:NSMakeSize(CGFLOAT_MAX, CGFLOAT_MAX)];
	tc.lineFragmentPadding = 0;
	[lm addTextContainer:tc];
	NSTextView *tv = [[cls alloc] initWithFrame:NSMakeRect(0, 0, 100, 20) textContainer:tc];
	tv.fieldEditor = fieldEditor;
	dxfQuiet(tv);
	return tv;
}

#define DXF_CELL_METHODS \
	- (NSRect)titleRectForBounds:(NSRect)b { return b; } \
	- (NSRect)drawingRectForBounds:(NSRect)b { return b; } \
	- (void)drawWithFrame:(NSRect)f inView:(NSView *)v { dxfDrawCell(self.st, f, (NSControl *)v); } \
	- (void)drawInteriorWithFrame:(NSRect)f inView:(NSView *)v {} \
	- (void)drawFocusRingMaskWithFrame:(NSRect)f inView:(NSView *)v {} \
	- (NSRect)focusRingMaskBoundsForFrame:(NSRect)f inView:(NSView *)v { return NSZeroRect; } \
	- (NSText *)setUpFieldEditorAttributes:(NSText *)t { \
		t = [super setUpFieldEditorAttributes:t]; \
		if ([t isKindOfClass:NSTextView.class]) dxfStyleEditor(self.st, (NSTextView *)t); \
		return t; \
	} \
	- (void)editWithFrame:(NSRect)f inView:(NSView *)v editor:(NSText *)t delegate:(id)d event:(NSEvent *)e { \
		[super editWithFrame:v.bounds inView:v editor:t delegate:d event:e]; \
		[v setNeedsDisplay:YES]; /* the cell stops drawing the text: the editor does */ \
		if ([t isKindOfClass:NSTextView.class]) dxfStyleEditor(self.st, (NSTextView *)t); \
	} \
	- (void)selectWithFrame:(NSRect)f inView:(NSView *)v editor:(NSText *)t delegate:(id)d start:(NSInteger)s length:(NSInteger)l { \
		[super selectWithFrame:v.bounds inView:v editor:t delegate:d start:s length:l]; \
		[v setNeedsDisplay:YES]; \
		if ([t isKindOfClass:NSTextView.class]) dxfStyleEditor(self.st, (NSTextView *)t); \
	}

@interface DXFCell : NSTextFieldCell
@property(nonatomic, weak) DXFState *st;
@end
@implementation DXFCell
DXF_CELL_METHODS
- (NSTextView *)fieldEditorForView:(NSView *)v {
	DXFState *st = self.st;
	if (!st) return nil;
	if (!st.editor) st.editor = dxfMakeTextView(st, DXFEditor.class, true);
	return st.editor;
}
@end

@interface DXFSecureCell : NSSecureTextFieldCell
@property(nonatomic, weak) DXFState *st;
@end
@implementation DXFSecureCell
DXF_CELL_METHODS
@end

// The controls: flipped as the kit's coordinates are, the state's delegate, the moves
// handed to the root view, and the key ups and modifier changes left to the key monitor
// (the responder chain would hand the root each of them a second time).
#define DXF_CONTROL_METHODS(BASE) \
	- (BOOL)isFlipped { return YES; } \
	- (void)mouseMoved:(NSEvent *)e { dxfMoved(self, e); } \
	- (void)keyUp:(NSEvent *)e {} \
	- (void)flagsChanged:(NSEvent *)e {} \
	- (DXFState *)dxfState { return self.st; } \
	- (NSUndoManager *)undoManagerForTextView:(NSTextView *)tv { return self.st.undo; } \
	- (BOOL)textView:(NSTextView *)tv shouldChangeTextInRange:(NSRange)r replacementString:(NSString *)s { \
		if (!dxfShouldChange(self.st, tv, r, s)) return NO; \
		if (![BASE instancesRespondToSelector:_cmd]) return YES; \
		struct objc_super su = {self, BASE.class}; \
		return ((BOOL(*)(struct objc_super *, SEL, id, NSRange, id))objc_msgSendSuper)(&su, _cmd, tv, r, s); \
	}

@interface DXFField : NSTextField
@property(nonatomic, weak) DXFState *st;
@end
@implementation DXFField
+ (Class)cellClass { return DXFCell.class; }
DXF_CONTROL_METHODS(NSTextField)
@end

@interface DXFSecure : NSSecureTextField
@property(nonatomic, weak) DXFState *st;
@end
@implementation DXFSecure
+ (Class)cellClass { return DXFSecureCell.class; }
DXF_CONTROL_METHODS(NSSecureTextField)
@end

// DXFScroll holds a multi-line field's text view; like the controls, it ends the responder
// chain for what the root hears from the monitor and its tracking area already.
@interface DXFScroll : NSScrollView
@end
@implementation DXFScroll
- (void)mouseMoved:(NSEvent *)e { dxfMoved(self, e); }
- (void)keyUp:(NSEvent *)e {}
- (void)flagsChanged:(NSEvent *)e {}
@end

// DXFTextView is a multi-line field's text view: its placeholder drawn as the kit draws text.
@interface DXFTextView : NSTextView
@property(nonatomic, weak) DXFState *st;
@end

@implementation DXFTextView
- (DXFState *)dxfState { return self.st; }
- (void)drawRect:(NSRect)r {
	CGContextRef c = NSGraphicsContext.currentContext.CGContext;
	CGContextSetShouldSmoothFonts(c, false);
	CGContextSetAllowsFontSmoothing(c, false);
	[super drawRect:r];
	DXFState *st = self.st;
	if (st && self.string.length == 0 && !self.hasMarkedText && st.placeholder.length > 0) {
		NSPoint o = self.textContainerOrigin;
		CGContextSaveGState(c);
		CGContextTranslateCTM(c, o.x, o.y);
		double w = self.textContainer.size.width;
		if (w > 1e6) w = self.bounds.size.width;
		dxfDrawLine(st, c, st.placeholder, w, st.lineHeight > 0 ? st.lineHeight : self.bounds.size.height,
			st.lineBase, st->ph);
		CGContextRestoreGState(c);
	}
}
- (void)mouseMoved:(NSEvent *)e { dxfMoved(self, e); }
@end

// ---- focus: which field has the window's first responder ----------------------------

static DXFState *dxfStateOf(NSResponder *r) {
	if (!r) return nil;
	if ([r isKindOfClass:NSTextView.class]) {
		NSTextView *tv = (NSTextView *)r;
		if ([tv respondsToSelector:@selector(dxfState)]) return [(id)tv dxfState];
		if (!tv.isFieldEditor) return nil;
		if ([(id)tv.delegate respondsToSelector:@selector(dxfState)]) return [(id)tv.delegate dxfState];
		// A field editor that becomes the first responder has its delegate set only later:
		// it is inside its field's view already.
		for (NSView *v = tv.superview; v; v = v.superview) {
			if ([v respondsToSelector:@selector(dxfState)]) return [(id)v dxfState];
		}
		return nil;
	}
	if ([r respondsToSelector:@selector(dxfState)]) return [(id)r dxfState];
	return nil;
}

// DXFWindowFields follows one window's first responder (KVO) and tells Go when a field
// gets or loses it, and marks the multi-line field that had it when the window resigned
// key (Reclaim).
@interface DXFWindowFields : NSObject
@property(nonatomic, weak) NSWindow *window;
@property(nonatomic, weak) DXFState *current;
@property(nonatomic, strong) NSHashTable<DXFState *> *states;
@end

@implementation DXFWindowFields
- (void)observeValueForKeyPath:(NSString *)path ofObject:(id)obj change:(NSDictionary *)change context:(void *)ctx {
	[self follow];
}
- (void)follow {
	DXFState *now = dxfStateOf(self.window.firstResponder);
	DXFState *old = self.current;
	if (now == old) return;
	self.current = now;
	if (old.swapping || now.swapping) return;
	if (old && !old.released) goFldFocus(old.handle, false);
	if (now && !now.released) goFldFocus(now.handle, true);
}
- (void)resignedKey:(NSNotification *)n {
	DXFState *cur = dxfStateOf(self.window.firstResponder);
	for (DXFState *st in self.states) {
		if (st.kind == DXF_MULTI) st.hadFocus = st == cur;
	}
}
@end

static NSMapTable<NSWindow *, DXFWindowFields *> *dxfWindows;
static id dxfMonitor;
static void dxfInstallMonitor(void);

static DXFWindowFields *dxfFieldsOf(NSWindow *w, bool make) {
	if (!dxfWindows) dxfWindows = [NSMapTable weakToStrongObjectsMapTable];
	DXFWindowFields *f = [dxfWindows objectForKey:w];
	if (!f && make) {
		f = [DXFWindowFields new];
		f.window = w;
		f.states = [NSHashTable weakObjectsHashTable];
		[w addObserver:f forKeyPath:@"firstResponder" options:0 context:NULL];
		[[NSNotificationCenter defaultCenter] addObserver:f selector:@selector(resignedKey:)
			name:NSWindowDidResignKeyNotification object:w];
		[dxfWindows setObject:f forKey:w];
		dxfInstallMonitor();
	}
	return f;
}

void dxfWindowClosing(void *p) {
	NSWindow *w = (__bridge NSWindow *)p;
	DXFWindowFields *f = [dxfWindows objectForKey:w];
	if (!f) return;
	[w removeObserver:f forKeyPath:@"firstResponder"];
	[[NSNotificationCenter defaultCenter] removeObserver:f];
	for (DXFState *st in f.states) st.released = true;
	[dxfWindows removeObjectForKey:w];
}

// ---- keys: the root first -----------------------------------------------------------

// dxfMenuHas reports whether menu (or a submenu) has an item for key with Command.
static bool dxfMenuHas(NSMenu *menu, NSString *key, NSEventModifierFlags mods) {
	for (NSMenuItem *it in menu.itemArray) {
		if (it.hasSubmenu && dxfMenuHas(it.submenu, key, mods)) return true;
		if (it.keyEquivalent.length == 0) continue;
		NSEventModifierFlags m = it.keyEquivalentModifierMask & (NSEventModifierFlagCommand | NSEventModifierFlagShift | NSEventModifierFlagOption | NSEventModifierFlagControl);
		NSString *k = it.keyEquivalent;
		if (![k isEqualToString:k.lowercaseString]) { // an upper-case key equivalent means Shift
			m |= NSEventModifierFlagShift;
			k = k.lowercaseString;
		}
		if ([k isEqualToString:key] && m == mods) return true;
	}
	return false;
}

// dxfEditFallback does what the Edit menu's items do in a field (Cut, Copy, Paste, Select
// All, Undo, Redo) when the application's menu has no item for the key: a field must take
// them either way, as a browser's input does. Like the item, it acts only when the text view
// validates the action (a password is never cut or copied); the key is the field's either way.
static bool dxfEditFallback(DXFState *st, NSEvent *e, NSTextView *tv) {
	NSEventModifierFlags m = e.modifierFlags & (NSEventModifierFlagCommand | NSEventModifierFlagShift | NSEventModifierFlagOption | NSEventModifierFlagControl);
	if (!(m & NSEventModifierFlagCommand) || (m & (NSEventModifierFlagOption | NSEventModifierFlagControl))) return false;
	NSString *k = e.charactersIgnoringModifiers.lowercaseString;
	if (k.length != 1) return false;
	SEL sel = NULL;
	bool shift = m & NSEventModifierFlagShift;
	unichar c = [k characterAtIndex:0];
	if (!shift) {
		switch (c) {
		case 'x': sel = @selector(cut:); break;
		case 'c': sel = @selector(copy:); break;
		case 'v': sel = @selector(paste:); break;
		case 'a': sel = @selector(selectAll:); break;
		case 'z': sel = @selector(undo:); break;
		}
	} else if (c == 'z') {
		sel = @selector(redo:);
	}
	if (!sel) return false;
	if (NSApp.mainMenu && dxfMenuHas(NSApp.mainMenu, k, m)) return false;
	if (sel == @selector(undo:) || sel == @selector(redo:)) {
		NSUndoManager *u = tv.undoManager;
		if (sel == @selector(undo:) && u.canUndo) [u undo];
		if (sel == @selector(redo:) && u.canRedo) [u redo];
		return true;
	}
	if (st.kind == DXF_PASSWORD && (sel == @selector(cut:) || sel == @selector(copy:))) return true;
	NSMenuItem *probe = [[NSMenuItem alloc] initWithTitle:@"" action:sel keyEquivalent:@""];
	if ([tv respondsToSelector:@selector(validateUserInterfaceItem:)]) {
		if (![(id<NSUserInterfaceValidations>)tv validateUserInterfaceItem:probe]) return true;
	} else if ([tv respondsToSelector:@selector(validateMenuItem:)] && ![tv validateMenuItem:probe]) {
		return true;
	}
	[NSApp sendAction:sel to:tv from:nil];
	return true;
}

static void dxfInstallMonitor(void) {
	if (dxfMonitor) return;
	NSEventMask mask = NSEventMaskKeyDown | NSEventMaskKeyUp | NSEventMaskFlagsChanged;
	dxfMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:mask handler:^NSEvent *(NSEvent *e) {
		NSWindow *w = e.window;
		if (!w || ![dxfWindows objectForKey:w]) return e;
		NSResponder *r = w.firstResponder;
		DXFState *st = dxfStateOf(r);
		if (!st || st.released || ![r isKindOfClass:NSTextView.class]) return e;
		NSTextView *tv = (NSTextView *)r;
		if (e.type == NSEventTypeFlagsChanged) {
			goFldFlags(st.handle, e.keyCode, dxfMods(e.modifierFlags));
			return e;
		}
		bool down = e.type == NSEventTypeKeyDown;
		// While an input method composes, its key downs are its own and never reach the root;
		// the key ups do (as WebKit sends a page the keyup of a key its composition took) and
		// still reach the input method. The root's own input does the same (dxfRootComposing).
		bool marked = tv.hasMarkedText;
		if (marked && down) return e;
		bool used = goFldKey(st.handle, down, e.keyCode, dxfPlainChar(e), dxfMods(e.modifierFlags), down && e.isARepeat);
		if (marked) return e;
		if (used) return nil; // the root used it: the field never sees it
		if (down && dxfEditFallback(st, e, tv)) return nil;
		return e;
	}];
}

// ---- the fields ---------------------------------------------------------------------

static NSTextField *dxfMakeControl(DXFState *st, bool secure) {
	NSTextField *f = secure ? [DXFSecure new] : [DXFField new];
	((DXFField *)f).st = st; // both classes have the property
	((DXFCell *)f.cell).st = st;
	f.bezeled = NO;
	f.bordered = NO;
	f.drawsBackground = NO;
	f.focusRingType = NSFocusRingTypeNone;
	f.editable = YES;
	f.selectable = YES;
	f.usesSingleLineMode = YES;
	f.cell.scrollable = YES;
	f.cell.wraps = NO;
	f.lineBreakMode = NSLineBreakByClipping;
	f.allowsEditingTextAttributes = NO;
	f.importsGraphics = NO;
	f.automaticTextCompletionEnabled = NO;
	f.allowsCharacterPickerTouchBarItem = NO;
	f.delegate = st;
	return f;
}

static void dxfApplyLook(DXFState *st) {
	if (st.kind == DXF_MULTI) {
		NSScrollView *sv = (NSScrollView *)st.view;
		sv.backgroundColor = dxfColor(st->bg);
		st.text.backgroundColor = dxfColor(st->bg);
		dxfStyleEditor(st, st.text);
		st.text.defaultParagraphStyle = dxfParagraph(st);
		st.text.accessibilityLabel = st.name;
		[st.text setNeedsDisplay:YES];
		return;
	}
	NSTextField *f = (NSTextField *)st.view;
	if (st.font) f.font = (__bridge NSFont *)st.font;
	f.textColor = dxfColor(st->fg);
	f.alignment = dxfNSAlign(st.align);
	f.baseWritingDirection = st.rtl ? NSWritingDirectionRightToLeft : NSWritingDirectionLeftToRight;
	f.accessibilityLabel = st.name;
	f.placeholderString = nil; // the cell draws it, as the kit does
	dxfStyleEditor(st, dxfEditorOf(st));
	[f setNeedsDisplay:YES];
}

void *dxfNew(void *pw, uintptr_t handle, int kind) {
	NSWindow *w = (__bridge NSWindow *)pw;
	DXFState *st = [DXFState new];
	st.handle = handle;
	st.kind = kind;
	st.window = w;
	st.undo = [NSUndoManager new];
	st->fg[3] = 1;
	if (kind == DXF_MULTI) {
		NSScrollView *sv = [[DXFScroll alloc] initWithFrame:NSMakeRect(0, 0, 100, 40)];
		sv.hasVerticalScroller = YES;
		sv.hasHorizontalScroller = NO;
		sv.autohidesScrollers = YES; // a bar only while the lines overflow (overflow: auto)
		sv.borderType = NSNoBorder;
		sv.drawsBackground = YES;
		DXFTextView *tv = (DXFTextView *)dxfMakeTextView(st, DXFTextView.class, false);
		tv.st = st;
		tv.delegate = st;
		tv.minSize = NSMakeSize(0, 0);
		tv.maxSize = NSMakeSize(CGFLOAT_MAX, CGFLOAT_MAX);
		tv.verticallyResizable = YES;
		tv.horizontallyResizable = NO;
		tv.autoresizingMask = NSViewWidthSizable;
		tv.textContainer.widthTracksTextView = YES;
		tv.textContainerInset = NSZeroSize;
		tv.drawsBackground = YES;
		sv.documentView = tv;
		st.text = tv;
		st.view = sv;
	} else {
		st.view = dxfMakeControl(st, kind == DXF_PASSWORD);
	}
	st.view.hidden = YES;
	[w.contentView addSubview:st.view];
	[dxfFieldsOf(w, true).states addObject:st];
	return (__bridge_retained void *)st;
}

void dxfSetStyle(void *p, CTFontRef font, double kern, const double *fg, const double *bg, const double *ph,
	const char *placeholder, const char *name, int align, bool rtl, double lineHeight, double lineBase) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released) return;
	if (font) CFRetain(font);
	if (st.font) CFRelease(st.font);
	st.font = font;
	st.kern = kern;
	for (int i = 0; i < 4; i++) {
		st->fg[i] = fg[i];
		st->bg[i] = bg[i];
		st->ph[i] = ph[i];
	}
	st.placeholder = dxfStr(placeholder);
	st.name = dxfStr(name);
	st.align = align;
	st.rtl = rtl;
	st.lineHeight = lineHeight;
	st.lineBase = lineBase;
	dxfApplyLook(st);
}

void dxfSetAlign(void *p, int align, bool rtl) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released || (st.align == align && st.rtl == rtl)) return;
	st.align = align;
	st.rtl = rtl;
	dxfApplyLook(st);
}

static bool dxfHasFocus(DXFState *st) {
	NSWindow *w = st.view.window;
	return w && dxfStateOf(w.firstResponder) == st;
}

void dxfSetFrame(void *p, double x, double y, double w, double h, double base, bool hide) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released) return;
	NSView *v = st.view;
	if (hide) {
		if (dxfHasFocus(st)) {
			// Hiding the first responder would move it: the field goes out of sight instead and
			// keeps the focus, which is the kit's to move.
			[v setFrameOrigin:NSMakePoint(-100000, -100000)];
		} else {
			v.hidden = YES;
		}
		return;
	}
	bool moved = !NSEqualRects(v.frame, NSMakeRect(x, y, w, h)) || st.base != base;
	st.base = base;
	v.hidden = NO;
	if (!moved) return;
	v.frame = NSMakeRect(x, y, w, h);
	if (st.kind != DXF_MULTI) {
		NSTextView *ed = dxfEditorOf(st);
		if (ed) {
			// The field editor's clip view follows the cell's frame only when editing starts.
			NSView *clip = ed.superview;
			if ([clip isKindOfClass:NSClipView.class]) clip.frame = v.bounds;
			dxfStyleEditor(st, ed);
		}
	}
	[v setNeedsDisplay:YES];
	[v displayIfNeeded]; // no blank field while a live resize moves it
}

bool dxfFrame(void *p, double *x, double *y, double *w, double *h) {
	DXFState *st = (__bridge DXFState *)p;
	NSRect f = st.view.frame;
	*x = f.origin.x;
	*y = f.origin.y;
	*w = f.size.width;
	*h = f.size.height;
	return st.view.hidden;
}

char *dxfText(void *p) {
	DXFState *st = (__bridge DXFState *)p;
	NSString *s;
	if (st.kind == DXF_MULTI) {
		s = st.text.string;
	} else {
		NSText *ed = [(NSTextField *)st.view currentEditor];
		s = ed ? ed.string : ((NSTextField *)st.view).stringValue;
	}
	return dxfUTF8(s);
}

void dxfSetText(void *p, const char *cs) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released) return;
	NSString *s = dxfStr(cs);
	NSRange end = NSMakeRange(s.length, 0);
	st.composed = NO;
	bool focused = dxfHasFocus(st);
	if (st.kind == DXF_MULTI) {
		NSTextView *tv = st.text;
		[tv.textStorage beginEditing];
		[tv.textStorage replaceCharactersInRange:NSMakeRange(0, tv.textStorage.length)
			withAttributedString:[[NSAttributedString alloc] initWithString:s attributes:dxfAttrs(st)]];
		[tv.textStorage endEditing];
		tv.selectedRange = focused ? end : NSMakeRange(0, 0);
		[tv scrollRangeToVisible:tv.selectedRange];
	} else {
		NSTextField *f = (NSTextField *)st.view;
		NSTextView *ed = dxfEditorOf(st);
		if (ed) {
			[ed.textStorage beginEditing];
			[ed.textStorage replaceCharactersInRange:NSMakeRange(0, ed.textStorage.length)
				withAttributedString:[[NSAttributedString alloc] initWithString:s attributes:dxfAttrs(st)]];
			[ed.textStorage endEditing];
			ed.selectedRange = end; // while the user edits, the caret goes to the end
			[ed scrollRangeToVisible:end];
		}
		f.stringValue = s;
		// Unfocused, the value shows from its start (the cell draws it so) and the caret
		// waits at the end, where a browser puts it when a script sets an input's value.
		st.saved = end;
	}
	[st.undo removeAllActions];
	if (st.editor) [st.editor.undoManager removeAllActions];
	[st.view setNeedsDisplay:YES];
	if (st.text) [(NSView *)st.text setNeedsDisplay:YES];
}

void dxfFocus(void *p) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released || dxfHasFocus(st)) return;
	NSWindow *w = st.view.window;
	if (!w) return;
	if (st.view.hidden) {
		// A field the kit hid (or never placed) takes the focus out of sight, as dxfSetFrame
		// hides a focused field: AppKit gives no hidden view the first responder. The kit's
		// next SetBounds puts it in place (its frame differs then).
		[st.view setFrameOrigin:NSMakePoint(-100000, -100000)];
		st.view.hidden = NO;
	}
	if (st.kind == DXF_MULTI) {
		[w makeFirstResponder:st.text];
		return;
	}
	NSRange keep = st.saved;
	if (![w makeFirstResponder:st.view]) return;
	NSTextView *ed = dxfEditorOf(st);
	if (ed) {
		NSUInteger n = ed.string.length;
		NSUInteger a = MIN(keep.location, n);
		ed.selectedRange = NSMakeRange(a, MIN(keep.length, n - a));
		[ed scrollRangeToVisible:ed.selectedRange];
	}
}

void dxfBlur(void *p) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released || !dxfHasFocus(st)) return;
	NSWindow *w = st.view.window;
	[w makeFirstResponder:w.contentView];
}

bool dxfFocused(void *p) {
	DXFState *st = (__bridge DXFState *)p;
	return !st.released && dxfHasFocus(st);
}

void dxfSelectAll(void *p) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released) return;
	NSTextView *ed = dxfEditorOf(st);
	if (ed) {
		[ed selectAll:nil];
	} else {
		st.saved = NSMakeRange(0, ((NSTextField *)st.view).stringValue.length);
	}
}

void dxfSelection(void *p, long *start, long *end) {
	DXFState *st = (__bridge DXFState *)p;
	*start = *end = 0;
	if (st.released) return;
	NSTextView *ed = dxfEditorOf(st);
	NSRange r = ed ? ed.selectedRange : st.saved;
	*start = (long)r.location;
	*end = (long)(r.location + r.length);
}

void dxfSetSelection(void *p, long a, long b) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released) return;
	NSTextView *ed = dxfEditorOf(st);
	NSString *s = ed ? ed.string : ((NSTextField *)st.view).stringValue;
	long n = (long)s.length;
	a = MIN(MAX(a, 0), n);
	b = MIN(MAX(b, 0), n);
	if (b < a) b = a;
	// Never inside a character, edited or not: an edit there would split a surrogate pair.
	NSRange r = dxfSnap(s, NSMakeRange((NSUInteger)a, (NSUInteger)(b - a)));
	if (ed) {
		ed.selectedRange = r;
		[ed scrollRangeToVisible:r];
	} else {
		st.saved = r;
	}
}

void dxfReplaceSelection(void *p, const char *cs) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released) return;
	NSString *s = dxfStr(cs);
	NSTextView *ed = dxfEditorOf(st);
	if (ed) {
		// As typing: through the edit rules, one undoable step, and a change notification.
		[ed insertText:s replacementRange:ed.selectedRange];
		return;
	}
	NSTextField *f = (NSTextField *)st.view;
	NSString *v = f.stringValue;
	NSRange r = dxfSnap(v, st.saved);
	f.stringValue = [v stringByReplacingCharactersInRange:r withString:s];
	st.saved = NSMakeRange(r.location + s.length, 0);
	dxfChanged(st);
}

void dxfSetMaxLength(void *p, long n) {
	((__bridge DXFState *)p).maxLength = MAX(n, 0);
}

void dxfSetPassword(void *p, bool on) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released || st.kind == DXF_MULTI || on == (st.kind == DXF_PASSWORD)) return;
	NSTextField *old = (NSTextField *)st.view;
	bool focused = dxfHasFocus(st);
	long a = 0, b = 0;
	dxfSelection(p, &a, &b);
	NSString *text = [(dxfEditorOf(st) ? dxfEditorOf(st).string : old.stringValue) copy]; // an editor's string changes with it
	st.swapping = YES;
	if (focused) [old.window makeFirstResponder:old.window.contentView];
	NSTextField *nf = dxfMakeControl(st, on);
	nf.frame = old.frame;
	nf.hidden = old.hidden;
	nf.stringValue = text;
	st.kind = on ? DXF_PASSWORD : DXF_TEXT;
	[old.superview replaceSubview:old with:nf];
	st.view = nf;
	dxfApplyLook(st);
	st.saved = NSMakeRange((NSUInteger)a, (NSUInteger)(b - a));
	if (focused) dxfFocus(p);
	st.swapping = NO;
	DXFWindowFields *wf = [dxfWindows objectForKey:nf.window];
	if (wf) wf.current = dxfStateOf(nf.window.firstResponder);
}

void dxfReclaim(void *p) {
	DXFState *st = (__bridge DXFState *)p;
	if (st.released || !st.hadFocus) return;
	NSWindow *w = st.view.window;
	if (!w.isKeyWindow) return;
	st.hadFocus = NO;
	dxfFocus(p);
}

void dxfRelease(void *p) {
	DXFState *st = (__bridge_transfer DXFState *)p;
	NSWindow *w = st.window;
	DXFWindowFields *wf = w ? [dxfWindows objectForKey:w] : nil;
	if (wf) {
		[wf.states removeObject:st];
		if (wf.current == st) wf.current = nil;
	}
	st.released = true;
	if (st.text) st.text.delegate = nil;
	if ([st.view isKindOfClass:NSTextField.class]) {
		NSTextField *f = (NSTextField *)st.view;
		[f abortEditing];
		f.delegate = nil;
	}
	[st.view removeFromSuperview];
	st = nil;
}

// ---- the root view's own text input -------------------------------------------------

static char dxfInputKey;

@interface DXFRootInput : NSObject
@property(nonatomic) bool active;
@property(nonatomic) NSRect caret;
@property(nonatomic, copy) NSAttributedString *marked;
@property(nonatomic) NSRange markedSel;
@end
@implementation DXFRootInput
@end

static DXFRootInput *dxfInputOf(NSView *v, bool make) {
	DXFRootInput *in = objc_getAssociatedObject(v, &dxfInputKey);
	if (!in && make) {
		in = [DXFRootInput new];
		objc_setAssociatedObject(v, &dxfInputKey, in, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	}
	return in;
}

void dxfSetRootTextInput(void *pw, bool active, double x, double y, double w, double h) {
	NSWindow *win = (__bridge NSWindow *)pw;
	NSView *v = win.contentView;
	DXFRootInput *in = dxfInputOf(v, active);
	if (!in) return;
	bool was = in.active;
	in.active = active;
	in.caret = NSMakeRect(x, y, w, h);
	if (was && !active) {
		in.marked = nil;
		[v.inputContext discardMarkedText];
	}
	if (active) [v.inputContext invalidateCharacterCoordinates];
}

bool dxfRootText(void *pv, void *pe) {
	NSView *v = (__bridge NSView *)pv;
	NSEvent *e = (__bridge NSEvent *)pe;
	DXFRootInput *in = dxfInputOf(v, false);
	if (!in.active) return false;
	if (e.modifierFlags & (NSEventModifierFlagCommand | NSEventModifierFlagControl)) return false;
	[v interpretKeyEvents:@[ e ]];
	return true;
}

bool dxfRootComposing(void *pv, void *pe) {
	NSView *v = (__bridge NSView *)pv;
	NSEvent *e = (__bridge NSEvent *)pe;
	DXFRootInput *in = dxfInputOf(v, false);
	if (!in.active || !in.marked || e.type != NSEventTypeKeyDown) return false;
	if (e.modifierFlags & (NSEventModifierFlagCommand | NSEventModifierFlagControl)) return false;
	[v interpretKeyEvents:@[ e ]];
	return true;
}

// ---- pointer moves: each once -------------------------------------------------------

@interface DXFLastMove : NSObject
@property(nonatomic) NSTimeInterval time;
@property(nonatomic) NSInteger window;
@end
@implementation DXFLastMove
@end

static char dxfMoveKey;

bool dxfRepeatMove(void *pv, void *pe) {
	NSView *v = (__bridge NSView *)pv;
	NSEvent *e = (__bridge NSEvent *)pe;
	DXFLastMove *last = objc_getAssociatedObject(v, &dxfMoveKey);
	if (!last) {
		last = [DXFLastMove new];
		objc_setAssociatedObject(v, &dxfMoveKey, last, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	} else if (fabs(last.time - e.timestamp) < 1e-6 && last.window == e.windowNumber) {
		// The same move again: two moves never share a timestamp, while AppKit's copy of an
		// event can differ in its timestamp's last bits and its location by a fraction (a
		// window on a fractional origin).
		return true;
	}
	last.time = e.timestamp;
	last.window = e.windowNumber;
	return false;
}

// The root view takes the input methods' text (NSTextInputClient) while its text input is
// active; the text reaches the root as TextEvents, a composition stays the input method's.
@interface DXView (DXFTextInput) <NSTextInputClient>
@end

@implementation DXView (DXFTextInput)
- (NSTextInputContext *)inputContext {
	DXFRootInput *in = dxfInputOf(self, false);
	return in.active ? [super inputContext] : nil;
}
- (void)insertText:(id)s replacementRange:(NSRange)r {
	NSString *t = [s isKindOfClass:NSAttributedString.class] ? [(NSAttributedString *)s string] : s;
	DXFRootInput *in = dxfInputOf(self, false);
	in.marked = nil;
	if (t.length == 0) return;
	char *u = dxfUTF8(t);
	goWindowText(self.handle, u);
	free(u);
}
- (void)doCommandBySelector:(SEL)sel {
	// While the root's input is active, the root saw the key as a KeyEvent already (or the
	// input method composed with it); otherwise NSResponder's own (beeps, as an unhandled key).
	if (dxfInputOf(self, false).active) return;
	[super doCommandBySelector:sel];
}
- (void)setMarkedText:(id)s selectedRange:(NSRange)sel replacementRange:(NSRange)r {
	DXFRootInput *in = dxfInputOf(self, true);
	NSAttributedString *a = [s isKindOfClass:NSAttributedString.class] ? s : [[NSAttributedString alloc] initWithString:s];
	in.marked = a.length ? a : nil;
	in.markedSel = sel;
}
- (void)unmarkText {
	DXFRootInput *in = dxfInputOf(self, false);
	NSString *t = in.marked.string;
	in.marked = nil;
	if (t.length) {
		char *u = dxfUTF8(t);
		goWindowText(self.handle, u);
		free(u);
	}
}
- (NSRange)selectedRange {
	DXFRootInput *in = dxfInputOf(self, false);
	return in.marked ? NSMakeRange(in.markedSel.location, in.markedSel.length) : NSMakeRange(0, 0);
}
- (NSRange)markedRange {
	DXFRootInput *in = dxfInputOf(self, false);
	return in.marked ? NSMakeRange(0, in.marked.length) : NSMakeRange(NSNotFound, 0);
}
- (BOOL)hasMarkedText { return dxfInputOf(self, false).marked != nil; }
- (NSAttributedString *)attributedSubstringForProposedRange:(NSRange)r actualRange:(NSRangePointer)actual {
	NSAttributedString *m = dxfInputOf(self, false).marked;
	if (!m || r.location >= m.length) return nil;
	NSRange c = NSMakeRange(r.location, MIN(r.length, m.length - r.location));
	if (actual) *actual = c;
	return [m attributedSubstringFromRange:c];
}
- (NSArray<NSAttributedStringKey> *)validAttributesForMarkedText { return @[]; }
- (NSRect)firstRectForCharacterRange:(NSRange)r actualRange:(NSRangePointer)actual {
	DXFRootInput *in = dxfInputOf(self, false);
	NSRect c = in ? in.caret : NSZeroRect;
	if (actual) *actual = r;
	NSRect wr = [self convertRect:c toView:nil];
	return self.window ? [self.window convertRectToScreen:wr] : wr;
}
- (NSUInteger)characterIndexForPoint:(NSPoint)p { return NSNotFound; }
@end
