//go:build !race

package state_test

// raceSlowdown is 1 outside the race detector: the performance bounds are today's.
const raceSlowdown = 1
