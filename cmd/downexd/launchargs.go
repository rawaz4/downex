package main

import "strings"

// withoutProcessSerial drops the "-psn_<high>_<low>" process serial number that
// LaunchServices has passed to app bundles it launches on some macOS versions (Downex
// Agent.app opened with open(1), or launched by a click on one of its notifications);
// the flag parser would refuse it. Used by the macOS agent only (mainthread_darwin.go).
func withoutProcessSerial(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	return out
}
