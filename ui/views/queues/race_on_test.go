//go:build race

package queues

// raceOn: the race detector is on, which makes an offscreen render about 20 times slower.
const raceOn = true
