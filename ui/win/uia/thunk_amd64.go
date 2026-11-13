//go:build windows

package uia

// The assembly thunks of thunk_amd64.s. They are never called from Go: their addresses go
// into the vtables.
func fromPointThunk()
func setRangeThunk()

// thunkPCs returns the thunks' addresses.
func thunkPCs() (fromPoint, setRange uintptr)

// The Go callbacks the thunks jump to (vtable.go sets them).
var fromPointTarget, setRangeTarget uintptr

const thunksSupported = true
