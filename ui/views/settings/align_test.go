package settings_test

import (
	"testing"

	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

// TestNumberFieldsAlignToTheEnd follows SettingsDownloads.svelte's .s-num and .s-rate
// (text-align: end): the counts and the global limit (with its "No limit" placeholder)
// sit at the end of their fields; every other Downloads field stays at the start.
func TestNumberFieldsAlignToTheEnd(t *testing.T) {
	h := viewstest.New(t, viewstest.Options{})
	d := open(h, views.SettingsRequest{Page: validate.PageDownloads}, settings.Options{})
	h.Settle()
	for name, want := range map[string]kit.TextAlign{
		"Simultaneous downloads":   kit.TextAlignEnd,
		"Connections per download": kit.TextAlignEnd,
		"Global speed limit":       kit.TextAlignEnd,
		"Default folder":           kit.TextAlignStart,
	} {
		in := inputNamed(t, d.Card(), name)
		if got := in.InputOptions().Align; got != want {
			t.Errorf("%s: InputOptions.Align = %v, want %v", name, got, want)
		}
		f, ok := in.Native().(*hostcaptest.Field)
		if !ok {
			t.Fatalf("%s: native field %T", name, in.Native())
		}
		if f.Opts.Align != want {
			t.Errorf("%s: host field Align = %v, want %v", name, f.Opts.Align, want)
		}
	}
}
