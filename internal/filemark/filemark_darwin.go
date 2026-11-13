//go:build darwin

package filemark

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// markPlatform sets the quarantine and where-from attributes of path (never of a
// symbolic link's target).
func markPlatform(path string, o Origin) error {
	if err := checkRegular(path); err != nil {
		return err
	}
	var errs []error
	q := QuarantineValue(o.Time, newEventID())
	if err := unix.Lsetxattr(path, XattrQuarantine, []byte(q), 0); err != nil {
		errs = append(errs, fmt.Errorf("filemark: set %s: %w", XattrQuarantine, err))
	}
	if froms := whereFroms(o); len(froms) > 0 {
		if err := unix.Lsetxattr(path, XattrWhereFroms, StringArrayPlist(froms), 0); err != nil {
			errs = append(errs, fmt.Errorf("filemark: set %s: %w", XattrWhereFroms, err))
		}
	}
	return errors.Join(errs...)
}
