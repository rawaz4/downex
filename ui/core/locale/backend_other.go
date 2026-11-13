//go:build !(windows && (amd64 || 386)) && !(darwin && cgo)

package locale

// newSystem has no system formatter to offer here: windows/arm64 cannot pass ICU's double
// arguments through Go's system calls, macOS has its backend only with cgo (icu_darwin.go),
// and Linux has none yet.
func newSystem(tag, timeZone string) (Formatter, error) { return nil, ErrUnsupported }

func icuLocale(tag string) (string, error) { return "", ErrUnsupported }

func systemLanguageName(id string) (string, bool) { return "", false }
