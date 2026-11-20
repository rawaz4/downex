package media_test

import (
	"errors"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/validate"
)

// The relay's bounds check (internal/validate.ClipMs) and the daemon's (media.Clip)
// refuse with the same texts (docs/MEDIA-SPEC.md §8 C2).
func TestClipReasonsMatchValidate(t *testing.T) {
	for _, c := range [][2]int64{{-1, 1000}, {0, validate.MaxClipMs + 1}, {5000, 5000}, {5000, 5999}, {80_000, 165_000}} {
		verr := validate.ClipMs(c[0], c[1])
		merr := media.Clip{Start: time.Duration(c[0]) * time.Millisecond, End: time.Duration(c[1]) * time.Millisecond}.Validate(0)
		var ce *media.ClipError
		switch {
		case verr == nil && merr == nil:
		case verr == nil || !errors.As(merr, &ce) || ce.Reason != verr.Error():
			t.Errorf("%v: validate %v, media %v", c, verr, merr)
		}
	}
	if validate.MaxClipMs != media.MaxClip.Milliseconds() || validate.MinClipMs != media.MinClipLen.Milliseconds() {
		t.Error("the bounds differ")
	}
}
