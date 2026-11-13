//go:build race

package state_test

// raceSlowdown scales the wall-clock bounds of the performance tests under the race
// detector, which makes code 2 to 20 times slower (the Go race detector's documentation),
// more when the native packages' tests run beside it: a bound there would measure the
// detector, not this code. The bounds hold unscaled in every other build.
const raceSlowdown = 5
