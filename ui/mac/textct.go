//go:build darwin && cgo

package mac

// The Core Text calls of the text engine: small C helpers that return plain numbers, so
// that the Go side keeps every rule (text.go, textlayout.go).

/*
#include <math.h>
#include <CoreText/CoreText.h>
#include <CoreGraphics/CoreGraphics.h>
#import <AppKit/AppKit.h>
#include "textct.h"


CFStringRef dxtString(const UniChar *u, CFIndex n) {
	return CFStringCreateWithCharacters(NULL, u, n);
}

// dxtFamilyFaces is the faces of the installed family name (NULL when it has none). Names
// that start with a dot are the system's private ones, which pages cannot name.
CFArrayRef dxtFamilyFaces(const UniChar *u, CFIndex n) {
	if (n == 0 || u[0] == '.') {
		return NULL;
	}
	CFStringRef fam = dxtString(u, n);
	NSDictionary *attrs = @{(__bridge id)kCTFontFamilyNameAttribute: (__bridge id)fam};
	CTFontDescriptorRef d = CTFontDescriptorCreateWithAttributes((__bridge CFDictionaryRef)attrs);
	NSSet *mand = [NSSet setWithObject:(__bridge id)kCTFontFamilyNameAttribute];
	CFArrayRef m = CTFontDescriptorCreateMatchingFontDescriptors(d, (__bridge CFSetRef)mand);
	CFRelease(d);
	CFRelease(fam);
	if (m != NULL && CFArrayGetCount(m) == 0) {
		CFRelease(m);
		m = NULL;
	}
	return m;
}

int dxtFamilyInstalled(const UniChar *u, CFIndex n) {
	@autoreleasepool {
		CFArrayRef m = dxtFamilyFaces(u, n);
		if (m == NULL) {
			return 0;
		}
		CFRelease(m);
		return 1;
	}
}

int dxtHasAxis(CTFontRef f, uint32_t tag, double *lo, double *hi) {
	CFArrayRef axes = CTFontCopyVariationAxes(f);
	if (axes == NULL) {
		return 0;
	}
	int found = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(axes); i++) {
		NSDictionary *a = (__bridge NSDictionary *)CFArrayGetValueAtIndex(axes, i);
		if ([a[(__bridge id)kCTFontVariationAxisIdentifierKey] unsignedIntValue] == tag) {
			*lo = [a[(__bridge id)kCTFontVariationAxisMinimumValueKey] doubleValue];
			*hi = [a[(__bridge id)kCTFontVariationAxisMaximumValueKey] doubleValue];
			found = 1;
		}
	}
	CFRelease(axes);
	return found;
}

#define dxtTagWght 0x77676874
#define dxtTagOpsz 0x6f70737a

// dxtFinish is f (consumed) with the CSS weight on its 'wght' axis, the optical size (when
// above 0) on its 'opsz' axis, and tabular figures when asked: a +1 font.
CTFontRef dxtFinish(CTFontRef f, double weight, double opsz, int tabular) {
	NSMutableDictionary *var = [NSMutableDictionary dictionary];
	double lo, hi;
	if (dxtHasAxis(f, dxtTagWght, &lo, &hi)) {
		var[@(dxtTagWght)] = @(fmin(fmax(weight, lo), hi));
	}
	if (opsz > 0 && dxtHasAxis(f, dxtTagOpsz, &lo, &hi)) {
		var[@(dxtTagOpsz)] = @(fmin(fmax(opsz, lo), hi));
	}
	NSMutableDictionary *attrs = [NSMutableDictionary dictionary];
	if (var.count > 0) {
		attrs[(__bridge id)kCTFontVariationAttribute] = var;
	}
	if (tabular) {
		attrs[(__bridge id)kCTFontFeatureSettingsAttribute] = @[@{
			(__bridge id)kCTFontOpenTypeFeatureTag: @"tnum",
			(__bridge id)kCTFontOpenTypeFeatureValue: @1,
		}];
	}
	if (attrs.count == 0) {
		return f;
	}
	CTFontDescriptorRef d = CTFontDescriptorCreateWithAttributes((__bridge CFDictionaryRef)attrs);
	CTFontRef out = CTFontCreateCopyWithAttributes(f, 0, NULL, d);
	CFRelease(d);
	CFRelease(f);
	return out;
}

// dxtOblique is f (consumed) slanted by WebKit's synthetic oblique angle (14 degrees).
CTFontRef dxtOblique(CTFontRef f) {
	CGAffineTransform m = CGAffineTransformMake(1, 0, tan(14 * M_PI / 180), 1, 0, 0);
	CTFontRef out = CTFontCreateCopyWithAttributes(f, 0, &m, NULL);
	CFRelease(f);
	return out;
}

// dxtItalic is f (consumed) in its family's italic, else slanted.
CTFontRef dxtItalic(CTFontRef f) {
	CTFontRef it = CTFontCreateCopyWithSymbolicTraits(f, 0, NULL, kCTFontItalicTrait, kCTFontItalicTrait);
	if (it != NULL && (CTFontGetSymbolicTraits(it) & kCTFontItalicTrait)) {
		CFRelease(f);
		return it;
	}
	if (it != NULL) {
		CFRelease(it);
	}
	return dxtOblique(f);
}

NSFontWeight dxtNSWeight(double w) {
	if (w < 150) return NSFontWeightUltraLight;
	if (w < 250) return NSFontWeightThin;
	if (w < 350) return NSFontWeightLight;
	if (w < 450) return NSFontWeightRegular;
	if (w < 550) return NSFontWeightMedium;
	if (w < 650) return NSFontWeightSemibold;
	if (w < 750) return NSFontWeightBold;
	if (w < 850) return NSFontWeightHeavy;
	return NSFontWeightBlack;
}

// dxtSystemFont is the system UI font (SF Pro) as WebKit makes system-ui: the UI font
// type, which keeps the automatic optical size and tracking, with the CSS weight on its
// 'wght' axis.
CTFontRef dxtSystemFont(double size, double weight, int italic, double opsz, int tabular) {
	@autoreleasepool {
		CTFontRef f = CTFontCreateUIFontForLanguage(kCTFontUIFontSystem, size, NULL);
		if (f == NULL) {
			return NULL;
		}
		if (italic) {
			f = dxtItalic(f);
		}
		return dxtFinish(f, weight, opsz, tabular);
	}
}

// dxtMonoFont is the system's monospaced font (SF Mono), WebKit's ui-monospace.
CTFontRef dxtMonoFont(double size, double weight, int italic, double opsz, int tabular) {
	@autoreleasepool {
		NSFont *ns = [NSFont monospacedSystemFontOfSize:size weight:dxtNSWeight(weight)];
		if (ns == nil) {
			return NULL;
		}
		CTFontRef f = (CTFontRef)CFBridgingRetain(ns);
		if (italic) {
			f = dxtItalic(f);
		}
		return dxtFinish(f, weight, opsz, tabular);
	}
}

// dxtFaceWeight is a face's CSS weight: its OS/2 usWeightClass, else its weight trait.
double dxtFaceWeight(CTFontRef f) {
	CFDataRef os2 = CTFontCopyTable(f, kCTFontTableOS2, kCTFontTableOptionNoOptions);
	if (os2 != NULL) {
		double w = 0;
		if (CFDataGetLength(os2) >= 6) {
			const UInt8 *b = CFDataGetBytePtr(os2);
			w = (double)((b[4] << 8) | b[5]);
		}
		CFRelease(os2);
		if (w >= 1 && w <= 1000) {
			return w;
		}
	}
	NSDictionary *traits = CFBridgingRelease(CTFontCopyTraits(f));
	double t = [traits[(__bridge id)kCTFontWeightTrait] doubleValue];
	// Core Text's -1..1 weight trait to CSS: 0 is 400, 0.23 500, 0.3 600, 0.4 700.
	if (t <= 0) return fmax(100, 400 + t * 500);
	if (t < 0.23) return 400 + t / 0.23 * 100;
	if (t < 0.3) return 500 + (t - 0.23) / 0.07 * 100;
	if (t < 0.4) return 600 + (t - 0.3) / 0.1 * 100;
	return fmin(900, 700 + (t - 0.4) / 0.2 * 200);
}

// dxtCSSWeightDistance orders faces as CSS Fonts 4's font matching does for weight: a
// smaller value is a better face for the desired weight w.
double dxtCSSWeightDistance(double w, double lo, double hi) {
	if (w >= lo && w <= hi) return 0;
	double fw = w < lo ? lo : hi; // the face's nearest weight
	if (w >= 400 && w <= 500) {
		if (fw > w && fw <= 500) return fw - w;
		if (fw < w) return 1000 + (w - fw);
		return 2000 + (fw - w);
	}
	if (w < 400) {
		if (fw < w) return w - fw;
		return 1000 + (fw - w);
	}
	if (fw > w) return fw - w;
	return 1000 + (w - fw);
}

// dxtNamedFont is the face of the installed family (faces from dxtFamilyFaces) that CSS
// font matching picks for the weight and style, at size.
CTFontRef dxtNamedFont(const UniChar *u, CFIndex n, double size, double weight, int italic, double opsz, int tabular) {
	@autoreleasepool {
		CFArrayRef faces = dxtFamilyFaces(u, n);
		if (faces == NULL) {
			return NULL;
		}
		CTFontRef best = NULL;
		double bestScore = INFINITY;
		for (CFIndex i = 0; i < CFArrayGetCount(faces); i++) {
			CTFontDescriptorRef d = CFArrayGetValueAtIndex(faces, i);
			CTFontRef f = CTFontCreateWithFontDescriptor(d, size, NULL);
			if (f == NULL) {
				continue;
			}
			double lo, hi;
			if (!dxtHasAxis(f, dxtTagWght, &lo, &hi)) {
				lo = hi = dxtFaceWeight(f);
			}
			int isItalic = (CTFontGetSymbolicTraits(f) & kCTFontItalicTrait) != 0;
			double score = dxtCSSWeightDistance(weight, lo, hi) + (isItalic == (italic != 0) ? 0 : 10000);
			if (score < bestScore) {
				if (best != NULL) {
					CFRelease(best);
				}
				best = f;
				bestScore = score;
			} else {
				CFRelease(f);
			}
		}
		CFRelease(faces);
		if (best == NULL) {
			return NULL;
		}
		if (italic && !(CTFontGetSymbolicTraits(best) & kCTFontItalicTrait)) {
			best = dxtOblique(best);
		}
		return dxtFinish(best, weight, opsz, tabular);
	}
}

void dxtMetrics(CTFontRef f, dxtFontMetrics *m) {
	@autoreleasepool {
		m->ascent = CTFontGetAscent(f);
		m->descent = CTFontGetDescent(f);
		m->leading = CTFontGetLeading(f);
		m->capHeight = CTFontGetCapHeight(f);
		m->xHeight = CTFontGetXHeight(f);
		m->underlinePos = CTFontGetUnderlinePosition(f);
		m->underlineThick = CTFontGetUnderlineThickness(f);
		NSString *fam = CFBridgingRelease(CTFontCopyFamilyName(f));
		m->adjustAscent = [fam isEqualToString:@"Times"] || [fam isEqualToString:@"Helvetica"] || [fam isEqualToString:@"Courier"];
	}
}

// dxtAttributes is what every character of a text in font f gets: the font, letter
// spacing (kern) when not 0 with only the ligatures the script needs (WebKit turns the
// common ones off then), the base direction, and the colour of the context it is drawn
// in.
CFDictionaryRef dxtAttributes(CTFontRef f, double kern, int rtl) {
	@autoreleasepool {
		CTWritingDirection dir = rtl ? kCTWritingDirectionRightToLeft : kCTWritingDirectionLeftToRight;
		CTParagraphStyleSetting set[] = {{kCTParagraphStyleSpecifierBaseWritingDirection, sizeof dir, &dir}};
		CTParagraphStyleRef ps = CTParagraphStyleCreate(set, 1);
		NSMutableDictionary *a = [NSMutableDictionary dictionary];
		a[(__bridge id)kCTFontAttributeName] = (__bridge id)f;
		a[(__bridge id)kCTParagraphStyleAttributeName] = (__bridge id)ps;
		a[(__bridge id)kCTForegroundColorFromContextAttributeName] = @YES;
		if (kern != 0) {
			a[(__bridge id)kCTKernAttributeName] = @(kern);
			a[(__bridge id)kCTLigatureAttributeName] = @0;
		}
		CFRelease(ps);
		return CFBridgingRetain(a);
	}
}

CFAttributedStringRef dxtAttrString(const UniChar *u, CFIndex n, CFDictionaryRef attrs) {
	@autoreleasepool {
		CFStringRef s = dxtString(u, n);
		CFAttributedStringRef a = CFAttributedStringCreate(NULL, s, attrs);
		CFRelease(s);
		// [mac webkit] WebKit shapes a bidi isolate as a run of its own, so no kerning pair
		// spans U+2066…U+2069: a zero kern on each control stops Core Text's pair across it
		// (without letter spacing, which the kern attribute carries otherwise).
		if (CFDictionaryGetValue(attrs, kCTKernAttributeName) == NULL) {
			CFMutableAttributedStringRef m = NULL;
			for (CFIndex i = 0; i < n; i++) {
				if (u[i] >= 0x2066 && u[i] <= 0x2069) {
					if (m == NULL) {
						m = CFAttributedStringCreateMutableCopy(NULL, 0, a);
					}
					double zero = 0;
					CFNumberRef z = CFNumberCreate(NULL, kCFNumberDoubleType, &zero);
					CFAttributedStringSetAttribute(m, CFRangeMake(i, 1), kCTKernAttributeName, z);
					CFRelease(z);
				}
			}
			if (m != NULL) {
				CFRelease(a);
				return m;
			}
		}
		return a;
	}
}

CTLineRef dxtLineOf(const UniChar *u, CFIndex n, CFDictionaryRef attrs) {
	@autoreleasepool {
		CFAttributedStringRef a = dxtAttrString(u, n, attrs);
		CTLineRef l = CTLineCreateWithAttributedString(a);
		CFRelease(a);
		return l;
	}
}

void dxtLineBounds(CTLineRef l, double *adv, double *trailing) {
	@autoreleasepool {
		*adv = CTLineGetTypographicBounds(l, NULL, NULL, NULL);
		*trailing = CTLineGetTrailingWhitespaceWidth(l);
	}
}

// dxtIndexAdvances adds each glyph's advance of line l to w[i - base], i its string index
// in [base, base+n).
void dxtIndexAdvances(CTLineRef l, CFIndex base, double *w, CFIndex n) {
	@autoreleasepool {
		CFArrayRef runs = CTLineGetGlyphRuns(l);
		for (CFIndex r = 0; r < CFArrayGetCount(runs); r++) {
			CTRunRef run = CFArrayGetValueAtIndex(runs, r);
			CFIndex g = CTRunGetGlyphCount(run);
			if (g == 0) {
				continue;
			}
			CGSize adv[64];
			CFIndex idx[64];
			for (CFIndex at = 0; at < g; at += 64) {
				CFIndex k = g - at < 64 ? g - at : 64;
				CTRunGetAdvances(run, CFRangeMake(at, k), adv);
				CTRunGetStringIndices(run, CFRangeMake(at, k), idx);
				for (CFIndex j = 0; j < k; j++) {
					CFIndex i = idx[j] - base;
					if (i >= 0 && i < n) {
						w[i] += adv[j].width;
					}
				}
			}
		}
	}
}

// dxtClusters sets bit 1 of f[i] where i starts a composed character sequence of s.
void dxtClusters(CFStringRef s, unsigned char *f, CFIndex n) {
	@autoreleasepool {
		for (CFIndex i = 0; i < n;) {
			CFRange r = CFStringGetRangeOfComposedCharactersAtIndex(s, i);
			f[i] |= 1;
			i = r.location + r.length > i ? r.location + r.length : i + 1;
		}
	}
}

// dxtBreaks sets bit 4 of f[i] where the system's line breaking (ICU's, which WebKit asks
// for text that is not ASCII) allows a break before i, i in (start, end).
void dxtBreaks(CFStringRef s, CFIndex start, CFIndex end, unsigned char *f) {
	@autoreleasepool {
		CFStringTokenizerRef t = CFStringTokenizerCreate(NULL, s, CFRangeMake(start, end - start), kCFStringTokenizerUnitLineBreak, NULL);
		if (t == NULL) {
			return;
		}
		while (CFStringTokenizerAdvanceToNextToken(t) != kCFStringTokenizerTokenNone) {
			CFRange r = CFStringTokenizerGetCurrentTokenRange(t);
			if (r.location > start && r.location < end) {
				f[r.location] |= 4;
			}
		}
		CFRelease(t);
	}
}

// dxtWidthOf is the advance of the n units u set alone in attrs.
double dxtWidthOf(const UniChar *u, CFIndex n, CFDictionaryRef attrs) {
	@autoreleasepool {
		CTLineRef l = dxtLineOf(u, n, attrs);
		if (l == NULL) {
			return 0;
		}
		double w = CTLineGetTypographicBounds(l, NULL, NULL, NULL);
		CFRelease(l);
		return w;
	}
}

CFIndex dxtIndexAt(CTLineRef l, double x) {
	@autoreleasepool {
		return CTLineGetStringIndexForPosition(l, CGPointMake(x, 0));
	}
}

double dxtOffsetAt(CTLineRef l, CFIndex i) {
	@autoreleasepool {
		return CTLineGetOffsetForStringIndex(l, i, NULL);
	}
}

// dxtRangeRuns writes, for each glyph run of l that holds string indices in [a, b), the x
// extent of those glyphs and whether the run is right to left: (x0, x1, rtl) triples in
// out, at most max of them, in visual order. It returns their number.
int dxtRangeRuns(CTLineRef l, CFIndex a, CFIndex b, double *out, int max) {
	@autoreleasepool {
		CFArrayRef runs = CTLineGetGlyphRuns(l);
		int k = 0;
		for (CFIndex r = 0; r < CFArrayGetCount(runs) && k < max; r++) {
			CTRunRef run = CFArrayGetValueAtIndex(runs, r);
			CFRange sr = CTRunGetStringRange(run);
			if (sr.location >= b || sr.location + sr.length <= a) {
				continue;
			}
			CFIndex g = CTRunGetGlyphCount(run);
			double x0 = INFINITY, x1 = -INFINITY;
			CGPoint pos[64];
			CGSize adv[64];
			CFIndex idx[64];
			for (CFIndex at = 0; at < g; at += 64) {
				CFIndex n = g - at < 64 ? g - at : 64;
				CTRunGetPositions(run, CFRangeMake(at, n), pos);
				CTRunGetAdvances(run, CFRangeMake(at, n), adv);
				CTRunGetStringIndices(run, CFRangeMake(at, n), idx);
				for (CFIndex j = 0; j < n; j++) {
					if (idx[j] >= a && idx[j] < b) {
						x0 = fmin(x0, pos[j].x);
						x1 = fmax(x1, pos[j].x + adv[j].width);
					}
				}
			}
			if (x0 <= x1) {
				out[3 * k] = x0;
				out[3 * k + 1] = x1;
				out[3 * k + 2] = (CTRunGetStatus(run) & kCTRunStatusRightToLeft) ? 1 : 0;
				k++;
			}
		}
		return k;
	}
}

// dxtTextBegin readies c for text as WebKit draws it under -webkit-font-smoothing:
// antialiased: grayscale antialiasing, no font smoothing (no stem darkening), glyphs at
// subpixel positions; the fill is the sRGB colour (r, g, b, a).
void dxtTextBegin(CGContextRef c, double r, double g, double b, double a) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextSetShouldAntialias(c, true);
		CGContextSetAllowsAntialiasing(c, true);
		CGContextSetShouldSmoothFonts(c, false);
		CGContextSetAllowsFontSmoothing(c, false);
		CGContextSetShouldSubpixelPositionFonts(c, true);
		CGContextSetAllowsFontSubpixelPositioning(c, true);
		CGContextSetShouldSubpixelQuantizeFonts(c, true);
		CGContextSetAllowsFontSubpixelQuantization(c, true);
		CGColorRef col = CGColorCreateSRGB(r, g, b, a);
		CGContextSetFillColorWithColor(c, col);
		CGColorRelease(col);
		CGContextSetTextMatrix(c, CGAffineTransformMakeScale(1, -1));
	}
}

// dxtDrawLine draws l with its origin at (x, y), y moved to the nearest device pixel as
// WebKit puts a baseline when the transform does not rotate.
void dxtDrawLine(CGContextRef c, CTLineRef l, double x, double y) {
	@autoreleasepool {
		CGAffineTransform t = CGContextGetCTM(c);
		if (t.b == 0 && t.c == 0) {
			CGPoint p = CGContextConvertPointToDeviceSpace(c, CGPointMake(x, y));
			p.y = round(p.y);
			y = CGContextConvertPointToUserSpace(c, p).y;
		}
		// CTLineDraw gives the same pixels as WebKit's CTFontDrawGlyphs per run (measured).
		CGContextSetTextPosition(c, x, y);
		CTLineDraw(l, c);
	}
}

void dxtTextEnd(CGContextRef c) {
	@autoreleasepool {
		CGContextRestoreGState(c);
	}
}

void dxtRelease(CFTypeRef r) {
	@autoreleasepool {
		if (r != NULL) {
			CFRelease(r);
		}
	}
}
*/
import "C"
