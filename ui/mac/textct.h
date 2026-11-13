// The text engine's Core Text helpers (textct.go defines them).
#include <CoreText/CoreText.h>
#include <CoreGraphics/CoreGraphics.h>

typedef struct {
	double ascent, descent, leading, capHeight, xHeight, underlinePos, underlineThick;
	int adjustAscent; // Times, Helvetica or Courier: WebKit adds 15% to the ascent
} dxtFontMetrics;

int dxtFamilyInstalled(const UniChar *u, CFIndex n);
CTFontRef dxtSystemFont(double size, double weight, int italic, double opsz, int tabular);
CTFontRef dxtMonoFont(double size, double weight, int italic, double opsz, int tabular);
CTFontRef dxtNamedFont(const UniChar *u, CFIndex n, double size, double weight, int italic, double opsz, int tabular);
void dxtMetrics(CTFontRef f, dxtFontMetrics *m);
CFDictionaryRef dxtAttributes(CTFontRef f, double kern, int rtl);
CFAttributedStringRef dxtAttrString(const UniChar *u, CFIndex n, CFDictionaryRef attrs);
CTLineRef dxtLineOf(const UniChar *u, CFIndex n, CFDictionaryRef attrs);
void dxtLineBounds(CTLineRef l, double *adv, double *trailing);
void dxtIndexAdvances(CTLineRef l, CFIndex base, double *w, CFIndex n);
void dxtClusters(CFStringRef s, unsigned char *f, CFIndex n);
void dxtBreaks(CFStringRef s, CFIndex start, CFIndex end, unsigned char *f);
double dxtWidthOf(const UniChar *u, CFIndex n, CFDictionaryRef attrs);
CFIndex dxtIndexAt(CTLineRef l, double x);
double dxtOffsetAt(CTLineRef l, CFIndex i);
int dxtRangeRuns(CTLineRef l, CFIndex a, CFIndex b, double *out, int max);
void dxtTextBegin(CGContextRef c, double r, double g, double b, double a);
void dxtDrawLine(CGContextRef c, CTLineRef l, double x, double y);
void dxtTextEnd(CGContextRef c);
void dxtRelease(CFTypeRef r);
