//go:build windows

package locale

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// UserLocale is the language the window formats in by default, as WebView2's
// navigator.language reports it: the user's first preferred display language ("en-GB"), else
// the user's locale, else "en-US".
func UserLocale() string {
	if langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil {
		for _, l := range langs {
			if checkTag(l) == nil {
				return l
			}
		}
	}
	var buf [85]uint16 // LOCALE_NAME_MAX_LENGTH
	if n, err := getUserDefaultLocaleName(&buf[0], int32(len(buf))); err == nil && n > 1 {
		if l := windows.UTF16ToString(buf[:n-1]); checkTag(l) == nil {
			return l
		}
	}
	return "en-US"
}

var procGetUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

func getUserDefaultLocaleName(buf *uint16, size int32) (int32, error) {
	if err := procGetUserDefaultLocaleName.Find(); err != nil {
		return 0, err
	}
	r, _, err := procGetUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(buf)), uintptr(size))
	if int32(r) == 0 {
		return 0, err
	}
	return int32(r), nil
}
