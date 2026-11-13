//go:build !windows && !(darwin && cgo)

package locale

import (
	"os"
	"strings"
	"sync/atomic"
)

// [program] preferred is the process's own language (SetUserLocale), "" for none.
var preferred atomic.Value

// SetUserLocale makes tag ("de-DE"; "pt_BR" is read as "pt-BR") the language UserLocale
// reports in this process, ahead of the environment, without changing the environment: the
// processes the window starts keep theirs. macOS's window sets NSLocale's first preferred
// language (WebKit's navigator.language); "" or a malformed tag clears it.
func SetUserLocale(tag string) {
	tag = strings.ReplaceAll(tag, "_", "-")
	if checkTag(tag) != nil {
		tag = ""
	}
	preferred.Store(tag)
}

// [program] end

// UserLocale is the language the window formats in by default: SetUserLocale's, else the first of $LC_ALL,
// $LC_MESSAGES and $LANG that names one ("de_DE.UTF-8" → "de-DE"), else "en-US".
func UserLocale() string {
	if tag, _ := preferred.Load().(string); tag != "" { // [program]
		return tag
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if tag := posixTag(os.Getenv(name)); tag != "" {
			return tag
		}
	}
	return "en-US"
}

// posixTag turns a POSIX locale name ("de_DE.UTF-8@euro") into a BCP 47 tag; "" for none,
// "C" and "POSIX".
func posixTag(v string) string {
	v, _, _ = strings.Cut(v, ".")
	v, _, _ = strings.Cut(v, "@")
	if v == "" || v == "C" || v == "POSIX" {
		return ""
	}
	tag := strings.ReplaceAll(v, "_", "-")
	if checkTag(tag) != nil {
		return ""
	}
	return tag
}
