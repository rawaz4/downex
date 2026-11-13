//go:build race

package settings_test

// raceEnabled: the race detector allocates on its own, so allocation counts are not checked.
const raceEnabled = true
