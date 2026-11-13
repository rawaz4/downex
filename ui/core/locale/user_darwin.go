//go:build darwin && cgo

package locale

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework CoreFoundation -lobjc
#include <CoreFoundation/CoreFoundation.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>
#include <objc/message.h>
#include <objc/runtime.h>

// Core Foundation exports these (JavaScriptCore's WTF imports them for its language), but
// today's SDK headers no longer declare them.
Boolean CFBundleGetLocalizationInfoForLocalization(CFStringRef localizationName, SInt32 *languageCode, SInt32 *regionCode, SInt32 *scriptCode, CFStringEncoding *stringEncoding);
CFStringRef CFBundleCopyLocalizationForLocalizationInfo(SInt32 languageCode, SInt32 regionCode, SInt32 scriptCode, CFStringEncoding stringEncoding);
void *objc_autoreleasePoolPush(void);
void objc_autoreleasePoolPop(void *pool);

// minimizedLanguage is the first of +[NSLocale minimizedLanguagesFromLanguages:] for the
// user's preferred languages: the list WebKit gives its web pages (navigator.language,
// Intl's default locale), reduced by the system's InternationalSupport to the languages it
// lets pages see ("en-IQ" → "en-GB", "de-CH" → "de-DE", "ja-JP" → "ja"). It writes UTF-8
// into buf and returns 1, or 0 where the system has no such list.
static int minimizedLanguage(CFArrayRef langs, char *buf, int size) {
	static void *support;
	if (!support) support = dlopen("/System/Library/PrivateFrameworks/InternationalSupport.framework/InternationalSupport", RTLD_LAZY | RTLD_LOCAL);
	Class locale = objc_getClass("NSLocale");
	SEL sel = sel_registerName("minimizedLanguagesFromLanguages:");
	if (!locale || !class_respondsToSelector(object_getClass((id)locale), sel)) return 0;
	int ok = 0;
	void *pool = objc_autoreleasePoolPush();
	CFArrayRef out = (CFArrayRef)((id (*)(id, SEL, id))objc_msgSend)((id)locale, sel, (id)langs);
	if (out && CFGetTypeID(out) == CFArrayGetTypeID() && CFArrayGetCount(out) > 0) {
		CFTypeRef first = CFArrayGetValueAtIndex(out, 0);
		if (CFGetTypeID(first) == CFStringGetTypeID())
			ok = CFStringGetCString((CFStringRef)first, buf, size, kCFStringEncodingUTF8) ? 1 : 0;
	}
	objc_autoreleasePoolPop(pool);
	return ok;
}

// bundleLanguage is WebKit's language where the system cannot minimize (JavaScriptCore's
// httpStyleLanguageCode): the first preferred language through the Script Manager codes of
// CFBundle ("en-IQ" → "en_US").
static int bundleLanguage(CFArrayRef langs, char *buf, int size) {
	if (CFArrayGetCount(langs) == 0) return 0;
	CFStringRef lang = (CFStringRef)CFArrayGetValueAtIndex(langs, 0);
	SInt32 language = 0, region = 0, script = 0;
	CFStringEncoding encoding = 0;
	CFBundleGetLocalizationInfoForLocalization(lang, &language, &region, &script, &encoding);
	CFStringRef reduced = CFBundleCopyLocalizationForLocalizationInfo(language, region, script, encoding);
	int ok = CFStringGetCString(reduced ? reduced : lang, buf, size, kCFStringEncodingUTF8) ? 1 : 0;
	if (reduced) CFRelease(reduced);
	return ok;
}

// webkitLanguage is WebKit's language for the preferred languages langs (UTF-8, separated
// by NUL, n of them), or for the user's when n is 0.
static int webkitLanguage(const char *langs, int n, char *buf, int size) {
	CFArrayRef prefs;
	if (n == 0) {
		prefs = CFLocaleCopyPreferredLanguages();
	} else {
		CFMutableArrayRef list = CFArrayCreateMutable(NULL, n, &kCFTypeArrayCallBacks);
		for (int i = 0; i < n; i++) {
			CFStringRef s = CFStringCreateWithCString(NULL, langs, kCFStringEncodingUTF8);
			if (s) {
				CFArrayAppendValue(list, s);
				CFRelease(s);
			}
			langs += strlen(langs) + 1;
		}
		prefs = list;
	}
	if (!prefs) return 0;
	int ok = minimizedLanguage(prefs, buf, size) || bundleLanguage(prefs, buf, size);
	CFRelease(prefs);
	return ok;
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

// UserLocale is the language the window formats in by default, as WKWebView's
// navigator.language (and Intl's default locale) reports it on this Mac: the user's
// preferred languages as the system minimizes them for web pages ("en-IQ" → "en-GB",
// "de-CH" → "de-DE", "zh-Hans-CN" → "zh-CN"), else "en-US".
func UserLocale() string { return webkitLanguage(nil) }

// webkitLanguage is WebKit's navigator.language for the preferred languages (the user's
// when there are none).
func webkitLanguage(preferred []string) string {
	var list *C.char
	if len(preferred) > 0 {
		list = C.CString(strings.Join(preferred, "\x00") + "\x00")
		defer C.free(unsafe.Pointer(list))
	}
	var buf [128]C.char
	if C.webkitLanguage(list, C.int(len(preferred)), &buf[0], C.int(len(buf))) == 0 {
		return "en-US"
	}
	return webkitTag(C.GoString(&buf[0]))
}

// webkitTag writes a language as WebKit's navigator.language does: "_" read as "-"
// ("en_GB" → "en-GB"); "en-US" when it is not a language tag.
func webkitTag(name string) string {
	tag := strings.ReplaceAll(name, "_", "-")
	if checkTag(tag) != nil {
		return "en-US"
	}
	return tag
}
