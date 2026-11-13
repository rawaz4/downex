//go:build windows && !amd64

package uia

// Only the x64 build has the thunks that receive the providers' double arguments (as the
// rest of ui/win runs only there); NewTree refuses elsewhere.
func thunkPCs() (fromPoint, setRange uintptr) { return 0, 0 }

var fromPointTarget, setRangeTarget uintptr

const thunksSupported = false
