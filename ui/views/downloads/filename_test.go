package downloads

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/kit/kittest"
	"downex/ui/state/statetest"
	"downex/ui/views/viewstest"
)

// nameIn is FileName.svelte as a row shows it: the row's name label for a download named name,
// and the text it lays out.
func nameIn(t *testing.T, name string) (*kit.Label, string) {
	t.Helper()
	d := Download(1, func(d *downexv1.Download) { d.Filename = name })
	r := newRig(t, viewstest.Options{Daemon: statetest.NewDaemon(d)}, gridBox, 0)
	rw := r.row(1)
	if rw == nil {
		t.Fatal("no row")
	}
	lay, ok := rw.name.TextLayout().(*kittest.Layout)
	if !ok {
		t.Fatalf("layout %T", rw.name.TextLayout())
	}
	return rw.name, lay.Text
}

// ---- FileName.test.ts ------------------------------------------------------------------------------

func TestFileName(t *testing.T) {
	t.Run("gives the extension of a right-to-left name its own left-to-right isolate", func(t *testing.T) {
		const name = "وزە بۆ کردنی کارێک ؟.mp4"
		l, shown := nameIn(t, name)
		// <bdi dir="ltr">.mp4</bdi>: the extension in a left-to-right embedding (U+202A … U+202C)
		i := strings.Index(shown, "‪")
		if i < 0 || !strings.HasSuffix(shown, "‪.mp4‬") {
			t.Fatalf("shown %q", shown)
		}
		// The stem stays plain text, so the row's dir="auto" still finds its first strong letter:
		// the stem first, then the mark of its direction.
		stem := strings.TrimSuffix(shown[:i], "‏")
		if stem != "وزە بۆ کردنی کارێک ؟" || !strings.HasSuffix(shown[:i], "‏") {
			t.Errorf("stem %q", shown[:i])
		}
		if l.Text() != name || l.Options().Direction != kit.DirAuto {
			t.Errorf("text %q, direction %v", l.Text(), l.Options().Direction)
		}
	})

	t.Run("leaves other names as one plain text node", func(t *testing.T) {
		for _, name := range []string{"Win.10.Aio.19045.7807.Multi.iso", "ڕاپۆرت", "README"} {
			l, shown := nameIn(t, name)
			if shown != name || l.Text() != name {
				t.Errorf("%q shown as %q", name, shown)
			}
		}
	})
}
