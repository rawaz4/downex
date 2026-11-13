//go:build linux || darwin

package platform

import "golang.org/x/sys/unix"

// freeSpace returns the bytes available to unprivileged users on the volume of the
// existing folder dir.
func freeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
