package queues

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
)

// ScheduleEditor.svelte's .repeat is text-align: end: the retry minutes and the "Off"
// placeholder sit at the field's end, so its native field must be end-aligned (right in a
// left-to-right window).
func TestRetryFieldIsEndAligned(t *testing.T) {
	r := setup(t, setupOptions{})
	r.click(r.d.qlist.newBtn)
	r.click(r.d.sched.sw)
	f, ok := r.d.sched.repeat.Native().(*hostcaptest.Field)
	if !ok {
		t.Fatal("the retry field has no native field")
	}
	if f.Opts.Align != kit.TextAlignEnd || !f.Opts.AlignsRight() {
		t.Fatalf("retry field: align %d, right %v; want end (right)", f.Opts.Align, f.Opts.AlignsRight())
	}
	if f.Opts.Placeholder != "Off" {
		t.Fatalf("retry placeholder %q", f.Opts.Placeholder)
	}
}
