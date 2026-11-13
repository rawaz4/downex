//go:build linux

package filemark

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// markPlatform sets the freedesktop origin attributes of path (never of a symbolic
// link's target). A file system without user attributes fails with ENOTSUP.
func markPlatform(path string, o Origin) error {
	if err := checkRegular(path); err != nil {
		return err
	}
	var errs []error
	for _, a := range []struct{ name, value string }{
		{XattrOriginURL, o.URL},
		{XattrReferrerURL, o.Referrer},
	} {
		if a.value == "" {
			continue
		}
		if err := unix.Lsetxattr(path, a.name, []byte(a.value), 0); err != nil {
			errs = append(errs, fmt.Errorf("filemark: set %s: %w", a.name, err))
		}
	}
	return errors.Join(errs...)
}
