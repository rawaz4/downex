//go:build darwin && cgo

package clipboard

// The macOS clipboard monitor of the downexd-agent (docs/M6-SPEC.md §3): the general
// pasteboard's changeCount is checked every second; on a change its types are read
// first, and the text only when no type marks the content as concealed or transient
// (macExcluded). A change between the two reads is picked up by the next check.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Foundation -framework AppKit -mmacosx-version-min=13.0
#include <stdlib.h>
#include "clipboard_darwin.h"
*/
import "C"

import (
	"log/slog"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// pollInterval is how often the pasteboard's changeCount is checked.
const pollInterval = time.Second

func startPlatform(offer func(string), log *slog.Logger) (func() error, error) {
	stop, done := make(chan struct{}), make(chan struct{})
	last := int64(C.downexClipChangeCount())
	go func() {
		defer close(done)
		t := time.NewTicker(pollInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			if int64(C.downexClipChangeCount()) == last {
				continue
			}
			text, ok, count := readPasteboard()
			last = count
			if ok {
				offer(text)
			}
		}
	}()
	var once sync.Once
	return func() error {
		once.Do(func() {
			close(stop)
			<-done
		})
		return nil
	}, nil
}

// readPasteboard returns the pasteboard's text (ok false: none, too long, excluded,
// or changed while it was read) and the changeCount that was examined.
func readPasteboard() (text string, ok bool, count int64) {
	var c1, c2 C.long
	types := C.downexClipTypes(&c1)
	var list []string
	if types != nil {
		list = strings.Split(C.GoString(types), "\n")
		C.free(unsafe.Pointer(types))
	}
	if macExcluded(list) {
		return "", false, int64(c1)
	}
	s := C.downexClipText(C.long(MaxTextBytes), &c2)
	if s == nil {
		return "", false, int64(c2)
	}
	text = C.GoString(s)
	C.free(unsafe.Pointer(s))
	if c1 != c2 { // changed in between: the next check reads the new content
		return "", false, int64(c1)
	}
	return text, true, int64(c2)
}
