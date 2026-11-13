//go:build race

package e2e

// raceEnabled reports whether the tests run with the race detector; the executables
// under test are then built with it as well.
const raceEnabled = true
