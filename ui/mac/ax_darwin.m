//go:build darwin && cgo

// [ax] The AppKit side of the native window's accessibility (see ax_darwin.h). Everything
// runs on the main thread; a request that arrives elsewhere is refused and counted.

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#import <objc/message.h>
#include "ax_darwin.h"
#include "ax_view.h"
#include "_cgo_export.h"

// The legacy attribute API is what the system's accessibility server calls and what the
// in-process client below uses; WebKit's ARIA attributes exist only there.
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

// DXView is window_darwin.m's content view; this file adds its accessibility (a category).
@interface DXView : NSView
@property(nonatomic) uintptr_t handle;
@end

static NSUInteger dxaxGen = 1;     // bumped whenever the tree may have changed
static long dxaxRefusedCount;      // requests off the main thread
static long dxaxPostedCount[DXAX_N_COUNT + 1];
static NSString *dxaxLastText;
static int dxaxLastPriority;
static void *dxaxLastTarget;
static char dxaxKeyTag;
static CFRunLoopObserverRef dxaxObserver;

void dxaxBump(void) { dxaxGen++; }
long dxaxRefused(void) { return dxaxRefusedCount; }
long dxaxPosted(int n) { return n >= 0 && n <= DXAX_N_COUNT ? dxaxPostedCount[n] : 0; }

// dxaxStrdup is s as malloc'd UTF-8 ("" for a string that has none: lone surrogates).
static char *dxaxStrdup(NSString *s) { return strdup(s.UTF8String ?: ""); }

char *dxaxLastAnnouncement(int *priority, void **target) {
	*priority = dxaxLastPriority;
	*target = dxaxLastTarget;
	return dxaxLastText ? dxaxStrdup(dxaxLastText) : NULL;
}

// dxaxWatch bumps the generation at every turn of the main run loop: what an element read
// in one request is reused only within it (a request is served in one turn; the root's
// calls, which change the tree, run in turns of their own).
static void dxaxWatch(void) {
	if (dxaxObserver) return;
	dxaxObserver = CFRunLoopObserverCreateWithHandler(NULL,
		kCFRunLoopBeforeTimers | kCFRunLoopBeforeSources | kCFRunLoopAfterWaiting, true, 0,
		^(CFRunLoopObserverRef o, CFRunLoopActivity a) { dxaxGen++; });
	CFRunLoopAddObserver(CFRunLoopGetMain(), dxaxObserver, kCFRunLoopCommonModes);
}

static NSString *dxaxString(char *s) {
	if (!s) return nil;
	NSString *r = [NSString stringWithUTF8String:s];
	free(s);
	return r;
}

static bool dxaxMain(void) {
	if ([NSThread isMainThread]) return true;
	dxaxRefusedCount++;
	return false;
}

// dxaxFieldElement is a native field's accessibility element: an NSTextField's cell (the
// view itself is ignored), a multi-line field's text view (AXTextArea, as WebKit shows a
// textarea: its NSScrollView would add a scroll area around it), an NSTextView itself.
static id dxaxFieldElement(NSView *field) {
	if ([field isKindOfClass:[NSTextField class]]) return NSAccessibilityUnignoredDescendant(field) ?: field;
	if ([field isKindOfClass:[NSScrollView class]]) {
		NSView *doc = ((NSScrollView *)field).documentView;
		if ([doc isKindOfClass:[NSTextView class]]) return doc;
	}
	return field;
}

// dxaxUnignored is what a client sees of obj: the content view stands for its window, a
// native field for its element.
static id dxaxUnignored(id obj, NSView *view) {
	if (obj && obj == view) return NSAccessibilityUnignoredAncestor(view);
	if ([obj isKindOfClass:[NSView class]]) return dxaxFieldElement(obj); // a native field
	return obj;
}

static NSArray *dxaxArray(void *p, int n, NSView *view) {
	if (n <= 0 || !p) {
		if (p) free(p);
		return @[];
	}
	void **refs = (void **)p;
	NSMutableArray *a = [NSMutableArray arrayWithCapacity:n];
	for (int i = 0; i < n; i++) {
		id o = dxaxUnignored((__bridge id)refs[i], view);
		if (o) [a addObject:o];
	}
	free(p);
	return a;
}

static NSRect dxaxToScreen(NSView *v, NSRect r) {
	if (!v || !v.window || r.size.width <= 0 || r.size.height <= 0) return NSZeroRect;
	return [v.window convertRectToScreen:[v convertRect:r toView:nil]];
}

static uintptr_t dxaxKeyOf(NSView *v) {
	if (!v) return 0;
	NSNumber *k = objc_getAssociatedObject(v, &dxaxKeyTag);
	if (k) return (uintptr_t)k.unsignedLongLongValue;
	if ([v isKindOfClass:[DXView class]]) return (uintptr_t)goAXViewKey(((DXView *)v).handle);
	return 0;
}

void dxaxSetViewKey(void *p, uintptr_t key) {
	NSView *v = (__bridge NSView *)p;
	objc_setAssociatedObject(v, &dxaxKeyTag, @((unsigned long long)key), OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

void *dxaxContentView(void *win) { return (__bridge void *)((__bridge NSWindow *)win).contentView; }

// ---- the element of one node ----------------------------------------------------------

@interface DXAXElement : NSAccessibilityElement {
@public
	uintptr_t key;
	uint64_t node;
	BOOL dead;
	BOOL have;
	NSUInteger gen;
	dxaxInfo info;
	NSString *role, *subrole, *roleDesc, *title, *label, *help, *ident, *text, *valueDesc, *shortcut, *mark;
	NSMutableArray *columns; // a table's AXColumn elements, made when a client asks for them
}
@property(nonatomic, weak) NSView *view;
@end

// DXAXColumn is one column of a table, as WebKit gives every table and grid AXColumn
// elements (AXColumns, and after the rows in its AXChildren): its index, its header, and
// the cell at its index of every row laid out. It is no node of the kit's: its table
// element makes it when a client first asks for the columns and destroys it with itself.
@interface DXAXColumn : NSAccessibilityElement {
@public
	NSInteger index;
	BOOL dead;
}
@property(nonatomic, weak) DXAXElement *table;
@end

@implementation DXAXElement

// load reads the node's information when it is not read in this turn; NO for a node gone.
- (BOOL)load {
	if (dead || !dxaxMain()) return NO;
	if (have && gen == dxaxGen) return YES;
	dxaxInfo in;
	memset(&in, 0, sizeof in);
	if (!goAXInfo(key, node, &in)) {
		have = NO;
		return NO;
	}
	role = dxaxString(in.role);
	subrole = dxaxString(in.subrole);
	roleDesc = dxaxString(in.roleDesc);
	title = dxaxString(in.title);
	label = dxaxString(in.label);
	help = dxaxString(in.help);
	ident = dxaxString(in.ident);
	text = dxaxString(in.text);
	valueDesc = dxaxString(in.valueDesc);
	shortcut = dxaxString(in.shortcut);
	mark = dxaxString(in.mark);
	in.role = in.subrole = in.roleDesc = in.title = in.label = in.help = in.ident = in.text = in.valueDesc = in.shortcut = in.mark = NULL;
	info = in;
	have = YES;
	gen = dxaxGen;
	return YES;
}

- (BOOL)flag:(int)f { return [self load] && (info.flags & f) != 0; }

- (BOOL)isAccessibilityElement { return !dead; }
- (NSAccessibilityRole)accessibilityRole { return [self load] ? role : nil; }
- (NSAccessibilitySubrole)accessibilitySubrole { return [self load] ? subrole : nil; }
- (NSString *)accessibilityRoleDescription {
	if (![self load]) return nil;
	if (roleDesc) return roleDesc;
	return NSAccessibilityRoleDescription(role, subrole);
}
- (NSString *)accessibilityTitle { return [self load] ? title : nil; }
- (NSString *)accessibilityLabel { return [self load] ? label : nil; }
- (NSString *)accessibilityHelp { return [self load] ? help : nil; }
- (NSString *)accessibilityIdentifier { return [self load] ? (ident ?: @"") : nil; }
- (id)accessibilityValue {
	if (![self load]) return nil;
	switch (info.valueKind) {
	case 1: return text ?: @"";
	case 2: return @(info.number);
	}
	return nil;
}
- (NSString *)accessibilityValueDescription { return [self load] ? valueDesc : nil; }
- (id)accessibilityMinValue { return [self load] && info.hasRange ? @(info.min) : nil; }
- (id)accessibilityMaxValue { return [self load] && info.hasRange ? @(info.max) : nil; }
- (BOOL)isAccessibilityEnabled { return [self flag:DXAX_F_ENABLED]; }
- (BOOL)isAccessibilityFocused { return [self flag:DXAX_F_FOCUSED]; }
- (BOOL)isAccessibilitySelected { return [self flag:DXAX_F_SELECTED]; }
- (BOOL)isAccessibilityExpanded { return [self flag:DXAX_F_EXPANDED]; }
- (BOOL)isAccessibilityRequired { return [self flag:DXAX_F_REQUIRED]; }
- (NSAccessibilityOrientation)accessibilityOrientation {
	if (![self load]) return NSAccessibilityOrientationUnknown;
	switch (info.orientation) {
	case 1: return NSAccessibilityOrientationHorizontal;
	case 2: return NSAccessibilityOrientationVertical;
	}
	return NSAccessibilityOrientationUnknown;
}
- (NSAccessibilitySortDirection)accessibilitySortDirection {
	if (![self load]) return NSAccessibilitySortDirectionUnknown;
	switch (info.sort) {
	case 1: return NSAccessibilitySortDirectionAscending;
	case 2: return NSAccessibilitySortDirectionDescending;
	}
	return NSAccessibilitySortDirectionUnknown;
}
- (NSRect)accessibilityFrame {
	if (![self load]) return NSZeroRect;
	return dxaxToScreen(self.view, NSMakeRect(info.x, info.y, info.w, info.h));
}
- (id)accessibilityParent {
	if (dead || !dxaxMain()) return nil;
	return dxaxUnignored((__bridge id)goAXParent(key, node), self.view);
}
- (NSArray *)accessibilityChildren {
	NSArray *a = [self list:DXAX_CHILDREN];
	// A table lists its columns after its rows, as WebKit's does.
	if (a && [self flag:DXAX_F_TABLE]) a = [a arrayByAddingObjectsFromArray:[self columnList]];
	return a;
}
- (NSArray *)accessibilityChildrenInNavigationOrder { return [self accessibilityChildren]; }
- (id)accessibilityWindow { return self.view.window; }
- (id)accessibilityTopLevelUIElement { return self.view.window; }
- (id)accessibilityHitTest:(NSPoint)p {
	NSView *v = self.view;
	return v ? dxaxViewHitTest(v, p) : nil;
}
- (id)accessibilityFocusedUIElement { return self; }

- (NSArray *)list:(int)kind {
	if (dead || !dxaxMain()) return nil;
	int n = 0;
	void *p = goAXList(key, node, kind, &n);
	return dxaxArray(p, n, self.view);
}

// ---- tables, rows and cells ----

- (NSArray *)accessibilityRows { return [self list:DXAX_ROWS]; }
- (NSArray *)accessibilityVisibleRows { return [self list:DXAX_VISIBLE_ROWS]; }
- (NSArray *)accessibilitySelectedRows { return [self list:DXAX_SELECTION]; }
- (NSArray *)accessibilitySelectedChildren { return [self list:DXAX_SELECTION]; }
- (NSArray *)accessibilityColumnHeaderUIElements { return [self list:DXAX_COLUMN_HEADERS]; }
- (NSArray *)accessibilityColumns { return [self columnList]; }
- (NSArray *)accessibilityVisibleColumns { return [self columnList]; }

// columnList is the table's columns, made or dropped to its column count now.
- (NSArray *)columnList {
	if (dead || !dxaxMain() || ![self flag:DXAX_F_TABLE]) return @[];
	NSUInteger n = (NSUInteger)MAX(goAXColumnCount(key, node), 0);
	if (!columns) columns = [NSMutableArray arrayWithCapacity:n];
	while (columns.count > n) {
		DXAXColumn *c = columns.lastObject;
		c->dead = YES;
		NSAccessibilityPostNotification(c, NSAccessibilityUIElementDestroyedNotification);
		[columns removeLastObject];
	}
	while (columns.count < n) {
		DXAXColumn *c = [DXAXColumn new];
		c->index = (NSInteger)columns.count;
		c.table = self;
		[columns addObject:c];
	}
	return [columns copy];
}
- (NSInteger)accessibilityRowCount { return [self load] ? info.rows : 0; }
- (NSInteger)accessibilityColumnCount { return [self load] ? info.cols : 0; }
- (NSInteger)accessibilityIndex { return [self load] ? info.index : 0; }
- (NSRange)accessibilityRowIndexRange {
	return [self load] ? NSMakeRange(info.rowLoc, info.rowLen) : NSMakeRange(0, 0);
}
- (NSRange)accessibilityColumnIndexRange {
	return [self load] ? NSMakeRange(info.colLoc, info.colLen) : NSMakeRange(0, 0);
}
- (id)accessibilityCellForColumn:(NSInteger)col row:(NSInteger)row {
	if (dead || !dxaxMain() || col < 0 || row < 0 || col > INT_MAX || row > INT_MAX) return nil;
	return (__bridge id)goAXCell(key, node, (int)col, (int)row);
}

// ---- actions and setters ----

- (BOOL)act:(int)a {
	if (dead || !dxaxMain()) return NO;
	return goAXAction(key, node, a);
}
- (BOOL)accessibilityPerformPress { return [self act:DXAX_PRESS]; }
- (BOOL)accessibilityPerformIncrement { return [self act:DXAX_INCREMENT]; }
- (BOOL)accessibilityPerformDecrement { return [self act:DXAX_DECREMENT]; }
- (BOOL)accessibilityPerformShowMenu { return [self act:DXAX_SHOWMENU]; }

- (NSArray *)accessibilityActionNames {
	if (![self load]) return @[];
	NSMutableArray *a = [NSMutableArray array];
	if (info.actions & DXAX_PRESS) [a addObject:NSAccessibilityPressAction];
	if (info.actions & DXAX_INCREMENT) [a addObject:NSAccessibilityIncrementAction];
	if (info.actions & DXAX_DECREMENT) [a addObject:NSAccessibilityDecrementAction];
	if (info.actions & DXAX_SHOWMENU) [a addObject:NSAccessibilityShowMenuAction];
	if (info.actions & DXAX_SCROLL) [a addObject:@"AXScrollToVisible"];
	return a;
}
- (NSString *)accessibilityActionDescription:(NSString *)action {
	if ([action isEqualToString:@"AXScrollToVisible"]) return @"scroll to visible";
	return NSAccessibilityActionDescription(action);
}
- (void)accessibilityPerformAction:(NSString *)action {
	if ([action isEqualToString:NSAccessibilityPressAction]) [self act:DXAX_PRESS];
	else if ([action isEqualToString:NSAccessibilityIncrementAction]) [self act:DXAX_INCREMENT];
	else if ([action isEqualToString:NSAccessibilityDecrementAction]) [self act:DXAX_DECREMENT];
	else if ([action isEqualToString:NSAccessibilityShowMenuAction]) [self act:DXAX_SHOWMENU];
	else if ([action isEqualToString:@"AXScrollToVisible"]) [self act:DXAX_SCROLL];
}

- (void)setAccessibilityValue:(id)v {
	if (dead || !dxaxMain() || ![self load]) return;
	if ([v isKindOfClass:[NSString class]]) {
		goAXSetText(key, node, (char *)[(NSString *)v UTF8String]);
	} else if ([v isKindOfClass:[NSNumber class]] && info.hasRange) {
		goAXSetNumber(key, node, [(NSNumber *)v doubleValue]);
	}
}
- (void)setAccessibilityFocused:(BOOL)on {
	if (!dead && dxaxMain()) goAXSetBool(key, node, DXAX_SET_FOCUSED, on);
}
- (void)setAccessibilitySelected:(BOOL)on {
	if (!dead && dxaxMain()) goAXSetBool(key, node, DXAX_SET_SELECTED, on);
}
- (void)setAccessibilityExpanded:(BOOL)on {
	if (!dead && dxaxMain()) goAXSetBool(key, node, DXAX_SET_EXPANDED, on);
}
- (void)setSelection:(NSArray *)items {
	if (dead || !dxaxMain()) return;
	NSUInteger n = 0;
	uint64_t *ids = malloc(sizeof(uint64_t) * (items.count + 1));
	for (id o in items) {
		if ([o isKindOfClass:[DXAXElement class]] && ((DXAXElement *)o)->key == key) ids[n++] = ((DXAXElement *)o)->node;
	}
	goAXSetSelection(key, node, ids, (int)n);
	free(ids);
}
- (void)setAccessibilitySelectedRows:(NSArray *)rows { [self setSelection:rows]; }
- (void)setAccessibilitySelectedChildren:(NSArray *)items { [self setSelection:items]; }

// isAccessibilitySelectorAllowed: decides which attributes and actions the element has,
// and which of its attributes are settable, from the node.
- (BOOL)isAccessibilitySelectorAllowed:(SEL)s {
	if (![self load]) return NO;
	int f = info.flags, set = info.settable, act = info.actions;
	if (s == @selector(accessibilityRows) || s == @selector(accessibilityVisibleRows) ||
		s == @selector(accessibilityColumnHeaderUIElements) || s == @selector(accessibilityRowCount) ||
		s == @selector(accessibilityColumnCount) || s == @selector(accessibilityCellForColumn:row:) ||
		s == @selector(accessibilityColumns) || s == @selector(accessibilityVisibleColumns))
		return (f & DXAX_F_TABLE) != 0;
	if (s == @selector(accessibilitySelectedRows)) return (f & DXAX_F_TABLE) != 0;
	if (s == @selector(setAccessibilitySelectedRows:)) return (f & DXAX_F_TABLE) && (set & 16);
	if (s == @selector(accessibilitySelectedChildren)) return (set & 16) && !(f & DXAX_F_TABLE);
	if (s == @selector(setAccessibilitySelectedChildren:)) return (set & 16) && !(f & DXAX_F_TABLE);
	if (s == @selector(accessibilityIndex)) return (f & DXAX_F_ROW) != 0;
	if (s == @selector(accessibilityRowIndexRange) || s == @selector(accessibilityColumnIndexRange))
		return (f & DXAX_F_CELL) != 0;
	if (s == @selector(accessibilityMinValue) || s == @selector(accessibilityMaxValue)) return info.hasRange != 0;
	if (s == @selector(accessibilityValueDescription)) return valueDesc != nil;
	if (s == @selector(accessibilityValue)) return info.valueKind != 0;
	if (s == @selector(setAccessibilityValue:)) return (set & 1) != 0;
	if (s == @selector(isAccessibilityExpanded)) return (f & DXAX_F_EXPANDABLE) != 0;
	if (s == @selector(setAccessibilityExpanded:)) return (set & 8) != 0;
	if (s == @selector(setAccessibilityFocused:)) return (set & 2) != 0;
	if (s == @selector(setAccessibilitySelected:)) return (set & 4) != 0;
	if (s == @selector(isAccessibilitySelected)) return (set & 4) || (f & (DXAX_F_SELECTED | DXAX_F_ROW));
	if (s == @selector(accessibilityOrientation)) return info.orientation != 0;
	if (s == @selector(accessibilitySortDirection)) return info.sort != 0;
	if (s == @selector(isAccessibilityRequired)) return (f & DXAX_F_REQUIRED) != 0;
	if (s == @selector(accessibilityHelp)) return help != nil;
	if (s == @selector(accessibilityTitle)) return title != nil;
	if (s == @selector(accessibilityPerformPress)) return (act & DXAX_PRESS) != 0;
	if (s == @selector(accessibilityPerformIncrement)) return (act & DXAX_INCREMENT) != 0;
	if (s == @selector(accessibilityPerformDecrement)) return (act & DXAX_DECREMENT) != 0;
	if (s == @selector(accessibilityPerformShowMenu)) return (act & DXAX_SHOWMENU) != 0;
	return [super isAccessibilitySelectorAllowed:s];
}

// ---- WebKit's attributes the NSAccessibility protocol has no method for ----
//
// The system's accessibility server reads a method-based element through the legacy API
// too: NSObject's accessibilityAttributeNames and accessibilityAttributeValue: answer the
// protocol's methods, and these overrides add WebKit's ARIA attributes to them (checked
// with a real AXUIElement client: ui/mac/internal/axcheck TestSystemClient).

- (NSArray *)extraNames {
	if (![self load]) return @[];
	NSMutableArray *a = [NSMutableArray arrayWithObject:@"AXElementBusy"];
	if ([role isEqualToString:NSAccessibilityMenuItemRole]) [a addObject:@"AXMenuItemMarkChar"];
	if (info.flags & (DXAX_F_CONTROL | DXAX_F_INVALID)) [a addObject:@"AXInvalid"];
	if (info.posInSet > 0) [a addObject:@"AXARIAPosInSet"];
	if (info.setSize > 0) [a addObject:@"AXARIASetSize"];
	if (shortcut.length) [a addObject:@"AXKeyShortcutsValue"];
	return a;
}

- (id)extraValue:(NSString *)n {
	if (![self load]) return nil;
	if ([n isEqualToString:@"AXElementBusy"]) return @((info.flags & DXAX_F_BUSY) != 0);
	if ([n isEqualToString:@"AXMenuItemMarkChar"]) return mark;
	if ([n isEqualToString:@"AXInvalid"]) return (info.flags & DXAX_F_INVALID) ? @"true" : @"false";
	if ([n isEqualToString:@"AXARIAPosInSet"]) return info.posInSet > 0 ? @(info.posInSet) : nil;
	if ([n isEqualToString:@"AXARIASetSize"]) return info.setSize > 0 ? @(info.setSize) : nil;
	if ([n isEqualToString:@"AXKeyShortcutsValue"]) return shortcut.length ? shortcut : nil;
	return nil;
}

- (BOOL)isExtra:(NSString *)n { return [[self extraNames] containsObject:n]; }

- (NSArray *)accessibilityAttributeNames {
	NSArray *a = [super accessibilityAttributeNames];
	if (dead || !dxaxMain()) return a;
	return [a arrayByAddingObjectsFromArray:[self extraNames]];
}

- (id)accessibilityAttributeValue:(NSString *)n {
	if (!dead && dxaxMain() && [self isExtra:n]) return [self extraValue:n];
	return [super accessibilityAttributeValue:n];
}

@end

@implementation DXAXColumn
- (BOOL)isAccessibilityElement { return !dead; }
- (NSAccessibilityRole)accessibilityRole { return NSAccessibilityColumnRole; }
- (NSString *)accessibilityRoleDescription { return NSAccessibilityRoleDescription(NSAccessibilityColumnRole, nil); }
- (NSInteger)accessibilityIndex { return index; }
- (id)accessibilityParent { return dead ? nil : self.table; }
- (id)accessibilityWindow { return self.table.view.window; }
- (id)accessibilityTopLevelUIElement { return self.table.view.window; }
- (NSArray *)accessibilityChildren {
	DXAXElement *t = self.table;
	if (dead || !t || t->dead || !dxaxMain()) return nil;
	int n = 0;
	void *p = goAXColumnCells(t->key, t->node, (int)index, &n);
	return dxaxArray(p, n, t.view);
}
- (NSArray *)accessibilityChildrenInNavigationOrder { return [self accessibilityChildren]; }
- (id)accessibilityHeader {
	DXAXElement *t = self.table;
	if (dead || !t || t->dead || !dxaxMain()) return nil;
	return dxaxUnignored((__bridge id)goAXColumnHeader(t->key, t->node, (int)index), t.view);
}
- (NSArray *)accessibilityRows { return dead ? nil : [self.table accessibilityRows]; }
- (NSArray *)accessibilityVisibleRows { return dead ? nil : [self.table accessibilityVisibleRows]; }
// Its frame is its cells' (WebKit's column spans its cells too).
- (NSRect)accessibilityFrame {
	NSRect r = NSZeroRect;
	for (id c in [self accessibilityChildren]) {
		NSRect f = [c accessibilityFrame];
		if (f.size.width > 0 && f.size.height > 0) r = NSIsEmptyRect(r) ? f : NSUnionRect(r, f);
	}
	return r;
}
- (id)accessibilityHitTest:(NSPoint)p { return [self.table accessibilityHitTest:p]; }
@end

void *dxaxElementNew(uintptr_t key, uint64_t id, void *view) {
	dxaxWatch();
	DXAXElement *e = [DXAXElement new];
	e->key = key;
	e->node = id;
	e.view = (__bridge NSView *)view;
	return (__bridge_retained void *)e;
}

void dxaxElementDestroy(void *p) {
	DXAXElement *e = (__bridge_transfer DXAXElement *)p;
	e->dead = YES;
	e->have = NO;
	for (DXAXColumn *c in e->columns) {
		c->dead = YES;
		NSAccessibilityPostNotification(c, NSAccessibilityUIElementDestroyedNotification);
	}
	e->columns = nil;
	dxaxPostedCount[0]++;
	NSAccessibilityPostNotification(e, NSAccessibilityUIElementDestroyedNotification);
	// Go's reference goes after the current request (which may be the element's own).
	dispatch_async(dispatch_get_main_queue(), ^{ (void)e; });
}

static NSString *dxaxNotificationName(int n) {
	switch (n) {
	case DXAX_N_FOCUS: return NSAccessibilityFocusedUIElementChangedNotification;
	case DXAX_N_VALUE: return NSAccessibilityValueChangedNotification;
	case DXAX_N_TITLE: return NSAccessibilityTitleChangedNotification;
	case DXAX_N_SELECTED_CHILDREN: return NSAccessibilitySelectedChildrenChangedNotification;
	case DXAX_N_SELECTED_ROWS: return NSAccessibilitySelectedRowsChangedNotification;
	case DXAX_N_ROW_COUNT: return NSAccessibilityRowCountChangedNotification;
	case DXAX_N_LAYOUT: return NSAccessibilityLayoutChangedNotification;
	case DXAX_N_EXPANDED: return @"AXExpandedChanged";
	}
	return nil;
}

void dxaxPost(void *p, int n) {
	id obj = (__bridge id)p;
	NSString *name = dxaxNotificationName(n);
	if (!obj || !name) return;
	if ([obj isKindOfClass:[DXAXElement class]] && ((DXAXElement *)obj)->dead) return;
	if ([obj isKindOfClass:[NSView class]] && ![obj isAccessibilityElement]) {
		obj = NSAccessibilityUnignoredAncestor(obj); // the content view: its window
		if (!obj) return;
	}
	if (n > 0 && n < DXAX_N_COUNT) dxaxPostedCount[n]++;
	NSAccessibilityPostNotification(obj, name);
}

void dxaxAnnounce(void *view, const char *text, int priority) {
	NSString *s = text ? [NSString stringWithUTF8String:text] : nil;
	if (s.length == 0) return;
	dxaxPostedCount[DXAX_N_COUNT]++;
	dxaxLastText = s;
	dxaxLastPriority = priority;
	NSDictionary *ui = @{
		NSAccessibilityAnnouncementKey : s,
		NSAccessibilityPriorityKey : @(priority >= 2 ? NSAccessibilityPriorityHigh : NSAccessibilityPriorityMedium),
	};
	// About the window whose content announced (a background progress window's own), as
	// WebKit posts about its own web area's window.
	id target = ((__bridge NSView *)view).window ?: (id)NSApp;
	dxaxLastTarget = (__bridge void *)target;
	NSAccessibilityPostNotificationWithUserInfo(target, NSAccessibilityAnnouncementRequestedNotification, ui);
}

// ---- native fields ------------------------------------------------------------------

static BOOL dxaxIsField(NSView *s) {
	return [s isKindOfClass:[NSTextField class]] || [s isKindOfClass:[NSTextView class]] ||
		([s isKindOfClass:[NSScrollView class]] && [((NSScrollView *)s).documentView isKindOfClass:[NSTextView class]]);
}

static NSView *dxaxFind(NSView *root, NSView *v, NSRect box, double slack, int depth) {
	for (NSView *s in v.subviews) {
		if (s.isHidden || s.alphaValue <= 0) continue;
		if (dxaxIsField(s)) {
			NSRect r = [s convertRect:s.bounds toView:root];
			if (r.size.width > 0 && r.size.height > 0 && NSMinX(r) >= NSMinX(box) - slack && NSMinY(r) >= NSMinY(box) - slack &&
				NSMaxX(r) <= NSMaxX(box) + slack && NSMaxY(r) <= NSMaxY(box) + slack)
				return s;
			continue; // never inside a field (its field editor)
		}
		if (depth < 3) {
			NSView *f = dxaxFind(root, s, box, slack, depth + 1);
			if (f) return f;
		}
	}
	return nil;
}

void *dxaxFieldIn(void *view, double x0, double y0, double x1, double y1, double slack) {
	NSView *v = (__bridge NSView *)view;
	if (!v || x1 <= x0 || y1 <= y0) return NULL;
	return (__bridge void *)dxaxFind(v, v, NSMakeRect(x0, y0, x1 - x0, y1 - y0), slack, 0);
}

// DXAXFieldMark is what dxaxFieldDecorate last gave a field's element (an associated
// object of the element).
//
// Decoration runs inside a client's request (the bridge places a field when it lists its
// parent's children), and AppKit records an error on the request in progress whenever an
// accessibility call it serves through the legacy API fails: reading an NSCell's unset
// accessibilityLabel (unsupported attribute), or setting an NSTextView's label or
// identifier (not settable there). The server then fails that whole request: the window's
// AXChildren came back as kAXErrorAttributeUnsupported (or kAXErrorFailure) whenever a
// native field was shown, so VoiceOver saw an empty window. So decoration never reads the
// element's accessibility back (it compares with what it set itself), sets a cell's
// attributes through the cell's own setters (which never fail), and gives a text view
// override values (accessibilitySetOverrideValue:forAttribute:, which never fail either
// and which the legacy API, the server's, answers first: also its AXParent, which the
// text view's setter does not change).
@interface DXAXFieldMark : NSObject {
@public
	BOOL required, hidScroll;
}
@property(nonatomic, weak) id parent;
@property(nonatomic, copy) NSString *label, *help, *ident;
@end
@implementation DXAXFieldMark
@end

static char dxaxMarkTag;

static BOOL dxaxSame(NSString *a, NSString *b) { return a == b || [a isEqualToString:b]; }

// dxaxOverride gives a field's view element (a text view) attribute name's value v (nil
// removes the override).
static void dxaxOverride(id el, NSString *name, id v) { [el accessibilitySetOverrideValue:v forAttribute:name]; }

void dxaxFieldDecorate(void *f, void *parent, void *view, const char *label, const char *help, const char *ident, bool required) {
	NSView *field = (__bridge NSView *)f;
	id el = dxaxFieldElement(field);
	id p = dxaxUnignored((__bridge id)parent, (__bridge NSView *)view);
	BOOL cell = [el isKindOfClass:[NSCell class]];
	DXAXFieldMark *m = objc_getAssociatedObject(el, &dxaxMarkTag);
	BOOL fresh = m == nil;
	if (fresh) {
		m = [DXAXFieldMark new];
		objc_setAssociatedObject(el, &dxaxMarkTag, m, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	}
	if (p && m.parent != p) {
		if (cell) [el setAccessibilityParent:p];
		else dxaxOverride(el, NSAccessibilityParentAttribute, p);
		m.parent = p;
	}
	// A multi-line field's scroll view is left out (WebKit's textarea has no scroll area).
	if (el != field && [field isKindOfClass:[NSScrollView class]] && !m->hidScroll) {
		field.accessibilityElement = NO;
		m->hidScroll = YES;
	}
	// The label goes once, where VoiceOver reads the text: the cell, or a multi-line
	// field's text view (never its scroll view too).
	NSString *l = label ? [NSString stringWithUTF8String:label] : nil;
	NSString *h = help ? [NSString stringWithUTF8String:help] : nil;
	NSString *i = ident ? [NSString stringWithUTF8String:ident] : nil;
	id<NSAccessibility> t = el;
	if (fresh || !dxaxSame(m.label, l)) {
		if (cell) t.accessibilityLabel = l;
		else dxaxOverride(el, NSAccessibilityDescriptionAttribute, l);
		m.label = l;
	}
	if (fresh || !dxaxSame(m.help, h)) {
		if (cell) t.accessibilityHelp = h;
		else dxaxOverride(el, NSAccessibilityHelpAttribute, h);
		m.help = h;
	}
	if (i && !dxaxSame(m.ident, i)) {
		if (cell) t.accessibilityIdentifier = i;
		else dxaxOverride(el, NSAccessibilityIdentifierAttribute, i);
		m.ident = i;
	}
	if (fresh || m->required != required) {
		if (cell) t.accessibilityRequired = required;
		else dxaxOverride(el, NSAccessibilityRequiredAttribute, required ? @YES : nil);
		m->required = required;
	}
}

// ---- the content view ---------------------------------------------------------------

NSArray *dxaxViewChildren(NSView *view) {
	if (!dxaxMain()) return @[];
	uintptr_t key = dxaxKeyOf(view);
	if (!key) return nil;
	dxaxWatch();
	int n = -1;
	void *p = goAXRootChildren(key, &n);
	if (n < 0) return nil; // no tree: AppKit's own children
	return dxaxArray(p, n, view);
}

id dxaxViewHitTest(NSView *view, NSPoint screen) {
	if (!dxaxMain() || !view.window) return nil;
	uintptr_t key = dxaxKeyOf(view);
	if (!key) return nil;
	NSPoint wp = [view.window convertPointFromScreen:screen];
	NSPoint vp = [view convertPoint:wp fromView:nil];
	id o = (__bridge id)goAXHitTest(key, vp.x, vp.y);
	if (!o) return nil;
	if (o == view) return NSAccessibilityUnignoredAncestor(view);
	if ([o isKindOfClass:[NSView class]]) return [o accessibilityHitTest:screen]; // a native field
	return o;
}

id dxaxViewFocused(NSView *view) {
	if (!dxaxMain()) return nil;
	uintptr_t key = dxaxKeyOf(view);
	if (!key) return nil;
	id o = (__bridge id)goAXFocused(key);
	if (!o) return NSAccessibilityUnignoredAncestor(view);
	if ([o isKindOfClass:[NSView class]]) return [o accessibilityFocusedUIElement]; // a native field
	return o;
}

@implementation DXView (DXAX)
- (NSArray *)accessibilityChildren {
	NSArray *a = dxaxViewChildren(self);
	return a ?: [super accessibilityChildren];
}
- (id)accessibilityHitTest:(NSPoint)p {
	id o = dxaxViewHitTest(self, p);
	return o ?: [super accessibilityHitTest:p];
}
- (id)accessibilityFocusedUIElement {
	id o = dxaxViewFocused(self);
	return o ?: [super accessibilityFocusedUIElement];
}
@end

// ---- the in-process client ------------------------------------------------------------

static void dxaxValue(id v, dxaxVal *out) {
	memset(out, 0, sizeof *out);
	if (!v) return;
	if ([v isKindOfClass:[NSString class]]) {
		out->kind = 1;
		out->s = dxaxStrdup((NSString *)v);
	} else if ([v isKindOfClass:[NSAttributedString class]]) {
		out->kind = 1;
		out->s = dxaxStrdup([(NSAttributedString *)v string]);
	} else if ([v isKindOfClass:[NSNumber class]]) {
		out->kind = 2;
		out->n = [(NSNumber *)v doubleValue];
	} else if ([v isKindOfClass:[NSArray class]]) {
		NSArray *a = v;
		out->kind = 4;
		out->count = (int)a.count;
		out->es = malloc(sizeof(void *) * (a.count + 1));
		for (NSUInteger i = 0; i < a.count; i++) out->es[i] = (void *)CFBridgingRetain(a[i]);
	} else if ([v isKindOfClass:[NSValue class]]) {
		const char *t = [(NSValue *)v objCType];
		if (strcmp(t, @encode(NSRect)) == 0) {
			NSRect r = [(NSValue *)v rectValue];
			out->kind = 5;
			out->x = r.origin.x, out->y = r.origin.y, out->w = r.size.width, out->h = r.size.height;
		} else if (strcmp(t, @encode(NSPoint)) == 0) {
			NSPoint p = [(NSValue *)v pointValue];
			out->kind = 6;
			out->x = p.x, out->y = p.y;
		} else if (strcmp(t, @encode(NSSize)) == 0) {
			NSSize s = [(NSValue *)v sizeValue];
			out->kind = 7;
			out->w = s.width, out->h = s.height;
		} else if (strcmp(t, @encode(NSRange)) == 0) {
			NSRange r = [(NSValue *)v rangeValue];
			out->kind = 8;
			out->x = r.location, out->w = r.length;
		} else {
			out->kind = 9;
		}
	} else {
		out->kind = 3;
		out->e = (void *)CFBridgingRetain(v);
	}
}

// The server reads a method-based element (an NSAccessibilityElement) through the
// NSAccessibility protocol's methods, allowed by isAccessibilitySelectorAllowed:; AppKit's
// own objects (the window, views, fields) answer the legacy API themselves.
static BOOL dxaxModern(id o) { return [o isKindOfClass:[NSAccessibilityElement class]]; }

static BOOL dxaxAllowed(id o, SEL s) {
	return [o respondsToSelector:s] && [o isAccessibilitySelectorAllowed:s];
}

typedef struct {
	const char *name;
	const char *get;
	char type; // o object, b BOOL, i NSInteger, r NSRect, g NSRange, O orientation/sort (NSInteger)
	const char *set;
} dxaxAttrDef;

static dxaxAttrDef dxaxAttrs[] = {
	{"AXRole", "accessibilityRole", 'o', NULL},
	{"AXSubrole", "accessibilitySubrole", 'o', NULL},
	{"AXRoleDescription", "accessibilityRoleDescription", 'o', NULL},
	{"AXTitle", "accessibilityTitle", 'o', NULL},
	{"AXDescription", "accessibilityLabel", 'o', NULL},
	{"AXHelp", "accessibilityHelp", 'o', NULL},
	{"AXIdentifier", "accessibilityIdentifier", 'o', NULL},
	{"AXValue", "accessibilityValue", 'o', "setAccessibilityValue:"},
	{"AXValueDescription", "accessibilityValueDescription", 'o', NULL},
	{"AXMinValue", "accessibilityMinValue", 'o', NULL},
	{"AXMaxValue", "accessibilityMaxValue", 'o', NULL},
	{"AXEnabled", "isAccessibilityEnabled", 'b', NULL},
	{"AXFocused", "isAccessibilityFocused", 'b', "setAccessibilityFocused:"},
	{"AXSelected", "isAccessibilitySelected", 'b', "setAccessibilitySelected:"},
	{"AXExpanded", "isAccessibilityExpanded", 'b', "setAccessibilityExpanded:"},
	{"AXRequired", "isAccessibilityRequired", 'b', NULL},
	{"AXOrientation", "accessibilityOrientation", 'O', NULL},
	{"AXSortDirection", "accessibilitySortDirection", 'O', NULL},
	{"AXFrame", "accessibilityFrame", 'r', NULL},
	{"AXParent", "accessibilityParent", 'o', NULL},
	{"AXChildren", "accessibilityChildren", 'o', NULL},
	{"AXWindow", "accessibilityWindow", 'o', NULL},
	{"AXTopLevelUIElement", "accessibilityTopLevelUIElement", 'o', NULL},
	{"AXRows", "accessibilityRows", 'o', NULL},
	{"AXVisibleRows", "accessibilityVisibleRows", 'o', NULL},
	{"AXSelectedRows", "accessibilitySelectedRows", 'o', "setAccessibilitySelectedRows:"},
	{"AXSelectedChildren", "accessibilitySelectedChildren", 'o', "setAccessibilitySelectedChildren:"},
	{"AXColumns", "accessibilityColumns", 'o', NULL},
	{"AXVisibleColumns", "accessibilityVisibleColumns", 'o', NULL},
	{"AXHeader", "accessibilityHeader", 'o', NULL},
	{"AXColumnHeaderUIElements", "accessibilityColumnHeaderUIElements", 'o', NULL},
	{"AXRowCount", "accessibilityRowCount", 'i', NULL},
	{"AXColumnCount", "accessibilityColumnCount", 'i', NULL},
	{"AXIndex", "accessibilityIndex", 'i', NULL},
	{"AXRowIndexRange", "accessibilityRowIndexRange", 'g', NULL},
	{"AXColumnIndexRange", "accessibilityColumnIndexRange", 'g', NULL},
	{"AXFocusedUIElement", "accessibilityFocusedUIElement", 'o', NULL},
};

static dxaxAttrDef *dxaxAttrNamed(const char *name) {
	for (size_t i = 0; i < sizeof dxaxAttrs / sizeof dxaxAttrs[0]; i++) {
		if (strcmp(dxaxAttrs[i].name, name) == 0) return &dxaxAttrs[i];
	}
	return NULL;
}

static id dxaxModernValue(id o, dxaxAttrDef *d) {
	SEL g = sel_registerName(d->get);
	if (!dxaxAllowed(o, g)) return nil;
	switch (d->type) {
	case 'o': return ((id(*)(id, SEL))objc_msgSend)(o, g);
	case 'b': return @(((BOOL(*)(id, SEL))objc_msgSend)(o, g));
	case 'i':
	case 'O': return @(((NSInteger(*)(id, SEL))objc_msgSend)(o, g));
	// Structures by direct sends: the compiler picks objc_msgSend_stret where the ABI
	// needs it (an NSRect on x86_64).
	case 'r': return [NSValue valueWithRect:[(id<NSAccessibility>)o accessibilityFrame]];
	case 'g':
		if (g == @selector(accessibilityRowIndexRange)) return [NSValue valueWithRange:[o accessibilityRowIndexRange]];
		return [NSValue valueWithRange:[o accessibilityColumnIndexRange]];
	}
	return nil;
}

// dxaxServerValue is attribute n of AppKit's object o as the server reads it for a client:
// through the request entry point AppKit serves AXUIElement requests with, which answers
// override values (accessibilitySetOverrideValue:forAttribute:, how dxaxFieldDecorate
// labels and places a text view) before the object's own. accessibilityAttributeValue:
// itself does not; it is the fallback where the entry point is missing. Inspector only:
// nothing in the window's accessibility calls this.
static id dxaxServerValue(id o, NSString *n) {
	SEL s = NSSelectorFromString(@"_accessibilityValueForAttribute:clientError:");
	if (![o respondsToSelector:s]) return [o accessibilityAttributeValue:n];
	int err = 0;
	id v = ((id(*)(id, SEL, NSString *, int *))objc_msgSend)(o, s, n, &err);
	return err ? nil : v;
}

void dxaxAttr(void *obj, const char *name, int nparam, int p0, int p1, dxaxVal *out) {
	id o = (__bridge id)obj;
	NSString *n = [NSString stringWithUTF8String:name];
	id v = nil;
	if (nparam < 0) {
		v = [o accessibilityAttributeValue:n]; // the legacy API itself, whatever the object
	} else if (dxaxModern(o)) {
		if (nparam == 2 && strcmp(name, "AXCellForColumnAndRow") == 0) {
			if (dxaxAllowed(o, @selector(accessibilityCellForColumn:row:))) v = [o accessibilityCellForColumn:p0 row:p1];
		} else if (nparam == 0) {
			dxaxAttrDef *d = dxaxAttrNamed(name);
			if (d) v = dxaxModernValue(o, d);
			else if ([o isKindOfClass:[DXAXElement class]] && [(DXAXElement *)o isExtra:n])
				v = [(DXAXElement *)o extraValue:n];
		}
	} else if (nparam == 0) {
		v = dxaxServerValue(o, n);
		// AppKit's objects answer some attributes only through the protocol (a window's
		// frame and focused element).
		dxaxAttrDef *d = dxaxAttrNamed(name);
		if (!v && d && [o respondsToSelector:sel_registerName(d->get)] && d->type == 'o') {
			v = ((id(*)(id, SEL))objc_msgSend)(o, sel_registerName(d->get));
		} else if (!v && d && d->type == 'r' && [o respondsToSelector:@selector(accessibilityFrame)]) {
			v = [NSValue valueWithRect:[(id<NSAccessibility>)o accessibilityFrame]];
		}
	} else {
		id param = nparam == 1 ? (id)@(p0) : (id)@[ @(p0), @(p1) ];
		v = [o accessibilityAttributeValue:n forParameter:param];
	}
	dxaxValue(v, out);
}

static NSArray *dxaxModernActions(id o) {
	if ([o respondsToSelector:@selector(accessibilityActionNames)]) return [o accessibilityActionNames];
	NSMutableArray *a = [NSMutableArray array];
	if (dxaxAllowed(o, @selector(accessibilityPerformPress))) [a addObject:NSAccessibilityPressAction];
	if (dxaxAllowed(o, @selector(accessibilityPerformIncrement))) [a addObject:NSAccessibilityIncrementAction];
	if (dxaxAllowed(o, @selector(accessibilityPerformDecrement))) [a addObject:NSAccessibilityDecrementAction];
	if (dxaxAllowed(o, @selector(accessibilityPerformShowMenu))) [a addObject:NSAccessibilityShowMenuAction];
	return a;
}

char *dxaxNames(void *obj, int what) {
	id o = (__bridge id)obj;
	NSArray *a;
	if (what == 3) {
		a = [o accessibilityAttributeNames]; // the legacy API itself, whatever the object
	} else if (dxaxModern(o)) {
		NSMutableArray *m = [NSMutableArray array];
		if (what == 0) {
			for (size_t i = 0; i < sizeof dxaxAttrs / sizeof dxaxAttrs[0]; i++) {
				if (dxaxAllowed(o, sel_registerName(dxaxAttrs[i].get))) [m addObject:@(dxaxAttrs[i].name)];
			}
			if ([o isKindOfClass:[DXAXElement class]]) [m addObjectsFromArray:[(DXAXElement *)o extraNames]];
		} else if (what == 1) {
			[m addObjectsFromArray:dxaxModernActions(o)];
		} else if (dxaxAllowed(o, @selector(accessibilityCellForColumn:row:))) {
			[m addObject:@"AXCellForColumnAndRow"];
		}
		a = m;
	} else {
		a = what == 0 ? [o accessibilityAttributeNames]
			: what == 1 ? [o accessibilityActionNames] : [o accessibilityParameterizedAttributeNames];
	}
	return dxaxStrdup([a componentsJoinedByString:@"\n"]);
}

bool dxaxSettable(void *obj, const char *name) {
	id o = (__bridge id)obj;
	if (dxaxModern(o)) {
		dxaxAttrDef *d = dxaxAttrNamed(name);
		return d && d->set && dxaxAllowed(o, sel_registerName(d->set));
	}
	return [o accessibilityIsAttributeSettable:[NSString stringWithUTF8String:name]];
}

void dxaxSet(void *obj, const char *name, int kind, const char *s, double n, void **es, int count) {
	id o = (__bridge id)obj;
	id v = nil;
	switch (kind) {
	case 1: v = [NSString stringWithUTF8String:s]; break;
	case 2: v = @(n); break;
	case 3: v = @(n != 0); break;
	case 4: {
		NSMutableArray *a = [NSMutableArray array];
		for (int i = 0; i < count; i++) [a addObject:(__bridge id)es[i]];
		v = a;
		break;
	}
	}
	if (dxaxModern(o)) {
		dxaxAttrDef *d = dxaxAttrNamed(name);
		if (!d || !d->set || !dxaxAllowed(o, sel_registerName(d->set))) return;
		SEL st = sel_registerName(d->set);
		if (d->type == 'b') ((void (*)(id, SEL, BOOL))objc_msgSend)(o, st, [v boolValue]);
		else ((void (*)(id, SEL, id))objc_msgSend)(o, st, v);
		return;
	}
	[o accessibilitySetValue:v forAttribute:[NSString stringWithUTF8String:name]];
}

void dxaxPerform(void *obj, const char *action) {
	id o = (__bridge id)obj;
	NSString *a = [NSString stringWithUTF8String:action];
	if (dxaxModern(o) && ![o respondsToSelector:@selector(accessibilityPerformAction:)]) {
		if ([a isEqualToString:NSAccessibilityPressAction] && dxaxAllowed(o, @selector(accessibilityPerformPress)))
			[o accessibilityPerformPress];
		else if ([a isEqualToString:NSAccessibilityIncrementAction] && dxaxAllowed(o, @selector(accessibilityPerformIncrement)))
			[o accessibilityPerformIncrement];
		else if ([a isEqualToString:NSAccessibilityDecrementAction] && dxaxAllowed(o, @selector(accessibilityPerformDecrement)))
			[o accessibilityPerformDecrement];
		else if ([a isEqualToString:NSAccessibilityShowMenuAction] && dxaxAllowed(o, @selector(accessibilityPerformShowMenu)))
			[o accessibilityPerformShowMenu];
		return;
	}
	[o accessibilityPerformAction:a];
}

void *dxaxHit(void *obj, double x, double y) {
	id r = [(__bridge id)obj accessibilityHitTest:NSMakePoint(x, y)];
	return r ? (void *)CFBridgingRetain(r) : NULL;
}

void *dxaxWindowElement(void *win) { return (void *)CFBridgingRetain((__bridge id)win); }
void dxaxRetain(void *obj) { CFRetain(obj); }
void dxaxRelease(void *obj) { CFRelease(obj); }
char *dxaxClassName(void *obj) { return strdup(class_getName([(__bridge id)obj class]) ?: ""); }
