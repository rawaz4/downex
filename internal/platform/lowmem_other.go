//go:build !windows

package platform

// TrimWorkingSet does nothing here: macOS and Linux have no portable call that empties a
// process's resident pages, and they reclaim an idle process's clean pages themselves
// when memory is short.
func TrimWorkingSet() error { return nil }
