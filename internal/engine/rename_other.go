//go:build !windows && !linux

package engine

// renameNoReplace renames src to dst, failing with an error that matches fs.ErrExist
// when dst exists.
func renameNoReplace(src, dst string) error { return linkRename(src, dst) }
