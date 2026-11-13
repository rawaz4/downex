package settings

import (
	"fmt"
	"strings"

	"downex/ui/kit"
)

// InlinePieces describes an inline paragraph's pieces (a debugging aid).
func InlinePieces(e *kit.Element) string {
	in, ok := e.Widget().(*inline)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, p := range in.pieces {
		fmt.Fprintf(&b, "[%d %q line %d x %.2f w %.2f adv %.2f first %v last %v] ", p.span, p.text, p.line, p.x, p.w, p.adv, p.first, p.last)
	}
	return b.String()
}
