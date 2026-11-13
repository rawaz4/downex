//go:build windows

package win

import (
	"fmt"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
)

// parseSVGPath reads SVG path data into absolute kit.PathOps appended to ops (the shared
// parser, gfx.ParseSVGPath). Malformed data fails with an error that wraps kit.ErrInvalid
// and gives the byte offset of the fault.
func parseSVGPath(d string, ops []kit.PathOp) ([]kit.PathOp, error) {
	ops, err := gfx.ParseSVGPath(d, ops)
	if err != nil {
		return ops, fmt.Errorf("win: %w", err)
	}
	return ops, nil
}
