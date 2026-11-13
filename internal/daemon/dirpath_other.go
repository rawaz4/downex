//go:build !windows

package daemon

// checkLocalDir accepts every clean absolute path: unlike Windows, opening a path never
// reaches a remote host by itself (network file systems are mounted by the user).
func checkLocalDir(string) error { return nil }

// samePath reports whether two clean absolute paths are equal.
func samePath(a, b string) bool { return a == b }
