// Package gfx holds the platform-free drawing rules both native backends (ui/win and
// ui/mac) share, so that their pixels follow one definition: SVG path data, the CSS
// box-shadow mask, the SVG arc rules and the device-pixel rounding of a frame's area.
package gfx

import (
	"fmt"
	"math"
	"strconv"

	"downex/ui/kit"
)

// ParseSVGPath reads SVG path data (SVG 2's grammar: M L H V C S Q T A Z, absolute and
// relative, repeated without their letter, numbers and arc flags written without
// separators) into absolute kit.PathOps appended to ops. Malformed data fails with an error
// that wraps kit.ErrInvalid and gives the byte offset of the fault.
func ParseSVGPath(d string, ops []kit.PathOp) ([]kit.PathOp, error) {
	p := svgParser{s: d}
	var cur, start, lastCtrl kit.Point
	var lastCmd byte
	first := true
	for {
		p.space()
		if p.i >= len(p.s) {
			return ops, nil
		}
		at := p.i
		c := p.s[p.i]
		if isCmd(c) {
			p.i++
		} else if lastCmd == 0 {
			return ops, p.fail("path data must start with a command")
		} else if !p.number() {
			return ops, p.fail(fmt.Sprintf("unexpected %q", c))
		} else {
			// A repeated command without its letter; a moveto repeats as a lineto.
			switch c = lastCmd; c {
			case 'M':
				c = 'L'
			case 'm':
				c = 'l'
			case 'Z', 'z':
				return ops, p.fail("numbers after closepath")
			}
		}
		if first && c != 'M' && c != 'm' {
			p.i = at
			return ops, p.fail("path data must start with a moveto")
		}
		first = false
		rel := c >= 'a'
		var o kit.Point
		if rel {
			o = cur
		}
		pt := func() (kit.Point, error) {
			x, err := p.num()
			if err != nil {
				return kit.Point{}, err
			}
			y, err := p.num()
			if err != nil {
				return kit.Point{}, err
			}
			return kit.Pt(x+o.X, y+o.Y), nil
		}
		ctrl := cur // the reflected control point for S and T, by default the current point
		switch c | 0x20 {
		case 'm':
			q, err := pt()
			if err != nil {
				return ops, err
			}
			ops = append(ops, kit.PathOp{Verb: kit.PathMove, P: [3]kit.Point{q}})
			cur, start = q, q
		case 'l':
			q, err := pt()
			if err != nil {
				return ops, err
			}
			ops = append(ops, kit.PathOp{Verb: kit.PathLine, P: [3]kit.Point{q}})
			cur = q
		case 'h':
			x, err := p.num()
			if err != nil {
				return ops, err
			}
			cur = kit.Pt(x+o.X, cur.Y)
			ops = append(ops, kit.PathOp{Verb: kit.PathLine, P: [3]kit.Point{cur}})
		case 'v':
			y, err := p.num()
			if err != nil {
				return ops, err
			}
			cur = kit.Pt(cur.X, y+o.Y)
			ops = append(ops, kit.PathOp{Verb: kit.PathLine, P: [3]kit.Point{cur}})
		case 'c', 's':
			var c1 kit.Point
			if c|0x20 == 'c' {
				var err error
				if c1, err = pt(); err != nil {
					return ops, err
				}
			} else {
				c1 = cur
				if l := lastCmd | 0x20; l == 'c' || l == 's' {
					c1 = kit.Pt(2*cur.X-lastCtrl.X, 2*cur.Y-lastCtrl.Y)
				}
			}
			c2, err := pt()
			if err != nil {
				return ops, err
			}
			q, err := pt()
			if err != nil {
				return ops, err
			}
			ops = append(ops, kit.PathOp{Verb: kit.PathCubic, P: [3]kit.Point{c1, c2, q}})
			ctrl, cur = c2, q
		case 'q', 't':
			var c1 kit.Point
			if c|0x20 == 'q' {
				var err error
				if c1, err = pt(); err != nil {
					return ops, err
				}
			} else {
				c1 = cur
				if l := lastCmd | 0x20; l == 'q' || l == 't' {
					c1 = kit.Pt(2*cur.X-lastCtrl.X, 2*cur.Y-lastCtrl.Y)
				}
			}
			q, err := pt()
			if err != nil {
				return ops, err
			}
			ops = append(ops, kit.PathOp{Verb: kit.PathQuad, P: [3]kit.Point{c1, q}})
			ctrl, cur = c1, q
		case 'a':
			rx, err := p.num()
			if err != nil {
				return ops, err
			}
			ry, err := p.num()
			if err != nil {
				return ops, err
			}
			rot, err := p.num()
			if err != nil {
				return ops, err
			}
			large, err := p.flag()
			if err != nil {
				return ops, err
			}
			sweep, err := p.flag()
			if err != nil {
				return ops, err
			}
			q, err := pt()
			if err != nil {
				return ops, err
			}
			ops = append(ops, kit.PathOp{Verb: kit.PathArc, P: [3]kit.Point{q}, Radii: kit.Size{W: rx, H: ry},
				Rotation: rot, Large: large, Sweep: sweep})
			cur = q
		case 'z':
			ops = append(ops, kit.PathOp{Verb: kit.PathClose})
			cur = start
		default:
			return ops, p.fail(fmt.Sprintf("unknown command %q", c))
		}
		lastCmd, lastCtrl = c, ctrl
	}
}

func isCmd(c byte) bool {
	switch c | 0x20 {
	case 'm', 'l', 'h', 'v', 'c', 's', 'q', 't', 'a', 'z':
		return true
	}
	return false
}

type svgParser struct {
	s string
	i int
}

func (p *svgParser) fail(msg string) error {
	return fmt.Errorf("SVG path data at offset %d: %s: %w", p.i, msg, kit.ErrInvalid)
}

// space skips white space and at most one comma between numbers.
func (p *svgParser) space() {
	comma := false
	for p.i < len(p.s) {
		switch c := p.s[p.i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			p.i++
		case c == ',' && !comma:
			comma = true
			p.i++
		default:
			return
		}
	}
}

// number reports whether a number starts at the current position.
func (p *svgParser) number() bool {
	if p.i >= len(p.s) {
		return false
	}
	c := p.s[p.i]
	return c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9')
}

// num reads one number: sign, digits, a fraction and an exponent, ending where the next
// number or command starts ("1-2", ".75.75", "1e-3").
func (p *svgParser) num() (float32, error) {
	p.space()
	start := p.i
	if p.i < len(p.s) && (p.s[p.i] == '-' || p.s[p.i] == '+') {
		p.i++
	}
	digits := 0
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
		digits++
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			digits++
		}
	}
	if digits == 0 {
		p.i = start
		return 0, p.fail("a number is missing")
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		j := p.i + 1
		if j < len(p.s) && (p.s[j] == '-' || p.s[j] == '+') {
			j++
		}
		if j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
			for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
				j++
			}
			p.i = j
		}
	}
	v, err := strconv.ParseFloat(p.s[start:p.i], 32)
	if err != nil || math.IsInf(v, 0) {
		text := p.s[start:p.i]
		p.i = start // the error points at the number's start
		return 0, p.fail(fmt.Sprintf("bad number %q", text))
	}
	return float32(v), nil
}

// flag reads an arc flag, one '0' or '1' that needs no separator after it.
func (p *svgParser) flag() (bool, error) {
	p.space()
	if p.i < len(p.s) {
		switch p.s[p.i] {
		case '0':
			p.i++
			return false, nil
		case '1':
			p.i++
			return true, nil
		}
	}
	return false, p.fail("an arc flag (0 or 1) is missing")
}
