package main

import (
	"slices"
	"testing"
)

func TestWithoutProcessSerial(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{nil, []string{}},
		{[]string{"-psn_0_12345"}, []string{}},
		{[]string{"--background", "-psn_0_1", "--log-level", "debug"}, []string{"--background", "--log-level", "debug"}},
		{[]string{"--db", "-psn.db"}, []string{"--db", "-psn.db"}}, // only the serial number's form
	} {
		if got := withoutProcessSerial(c.in); !slices.Equal(got, c.want) {
			t.Errorf("withoutProcessSerial(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	in := []string{"-psn_0_1", "--foreground"}
	withoutProcessSerial(in)
	if in[0] != "-psn_0_1" {
		t.Error("the input was modified")
	}
}
