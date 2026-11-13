//go:build race

package add

// raceOn: the race detector is on, which allocates in code that otherwise does not.
const raceOn = true
