package widgets_test

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// An Input's Align reaches its native field with the window's direction (Settings' .s-num and
// .s-rate and the Queues' .repeat are text-align: end).
func TestInputAlignReachesTheNativeField(t *testing.T) {
	end := widgets.NewInput("10", widgets.InputOptions{Name: "Connections per download", Align: kit.TextAlignEnd}, nil)
	start := widgets.NewInput("x", widgets.InputOptions{Name: "Name"}, nil)
	r := newRig(t, kit.Size{W: 500, H: 300}, end, start)
	if len(r.host.Fields) != 2 {
		t.Fatalf("%d native fields, want 2", len(r.host.Fields))
	}
	byName := map[string]kit.TextFieldOptions{}
	for _, f := range r.host.Fields {
		byName[f.Opts.Name] = f.Opts
	}
	if o := byName["Connections per download"]; o.Align != kit.TextAlignEnd || !o.AlignsRight() {
		t.Errorf("the end-aligned input's field: align %d, right %v", o.Align, o.AlignsRight())
	}
	if o := byName["Name"]; o.Align != kit.TextAlignStart || o.AlignsRight() {
		t.Errorf("a plain input's field: align %d, right %v", o.Align, o.AlignsRight())
	}
}
