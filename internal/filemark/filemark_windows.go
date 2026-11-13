//go:build windows

package filemark

import (
	"fmt"
	"os"
)

// markPlatform writes the Zone.Identifier stream of path, replacing an existing one.
func markPlatform(path string, o Origin) error {
	if err := checkRegular(path); err != nil {
		return err
	}
	// The stream is opened by name ("<file>:Zone.Identifier"); a file system without
	// named streams (FAT32, exFAT, some shares) refuses the name.
	if err := os.WriteFile(path+":"+ZoneStream, ZoneIdentifier(o), 0o644); err != nil {
		return fmt.Errorf("filemark: write %s: %w", ZoneStream, err)
	}
	return nil
}
