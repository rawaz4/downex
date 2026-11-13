package frontend

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Color is an sRGB colour with straight alpha, each part 0 to 1 (kit.Color's layout); Hex
// keeps how the source wrote an opaque colour (0xRRGGBB) for the generated code, -1 when it
// was written another way.
type Color struct {
	R, G, B, A float64
	Bytes      [3]uint8
	Hex        int64
}

// ParseColor reads #rgb, #rrggbb, rgb(r g b / a), rgb(r, g, b), rgba(r, g, b, a) and
// transparent.
func ParseColor(s string) (Color, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "transparent":
		return Color{Hex: -1}, nil
	case strings.HasPrefix(s, "#"):
		h := s[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) != 6 {
			return Color{}, fmt.Errorf("colour %q: want #rgb or #rrggbb", s)
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return Color{}, fmt.Errorf("colour %q: %w", s, err)
		}
		b := [3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)}
		return Color{R: float64(b[0]) / 255, G: float64(b[1]) / 255, B: float64(b[2]) / 255, A: 1, Bytes: b, Hex: int64(v)}, nil
	case strings.HasPrefix(s, "rgb(") || strings.HasPrefix(s, "rgba("):
		in := s[strings.IndexByte(s, '(')+1:]
		if !strings.HasSuffix(in, ")") {
			return Color{}, fmt.Errorf("colour %q: no closing parenthesis", s)
		}
		in = strings.TrimSuffix(in, ")")
		var parts []string
		alpha := "1"
		if strings.Contains(in, ",") {
			parts = splitTop(in, ',')
			if len(parts) == 4 {
				alpha, parts = parts[3], parts[:3]
			}
		} else {
			main, a, slash := strings.Cut(in, "/")
			parts = strings.Fields(main)
			if slash {
				alpha = strings.TrimSpace(a)
			}
		}
		if len(parts) != 3 {
			return Color{}, fmt.Errorf("colour %q: want three channels", s)
		}
		var b [3]uint8
		for i, p := range parts {
			v, err := strconv.ParseFloat(p, 64)
			if err != nil || v < 0 || v > 255 || v != math.Trunc(v) {
				return Color{}, fmt.Errorf("colour %q: channel %q is not 0..255", s, p)
			}
			b[i] = uint8(v)
		}
		a, err := parseAlpha(alpha)
		if err != nil {
			return Color{}, fmt.Errorf("colour %q: %w", s, err)
		}
		return Color{R: float64(b[0]) / 255, G: float64(b[1]) / 255, B: float64(b[2]) / 255, A: a, Bytes: b, Hex: -1}, nil
	}
	return Color{}, fmt.Errorf("%q is not a colour", s)
}

func parseAlpha(s string) (float64, error) {
	pct := strings.HasSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	if err != nil {
		return 0, fmt.Errorf("alpha %q: %w", s, err)
	}
	if pct {
		v /= 100
	}
	if v < 0 || v > 1 {
		return 0, fmt.Errorf("alpha %q out of 0..1", s)
	}
	return v, nil
}

// Length is a CSS length: px, rem, or a unitless 0.
type Length struct {
	V    float64
	Unit string // "px" or "rem"
}

// ParseLength reads Npx, Nrem and 0.
func ParseLength(s string) (Length, error) {
	switch {
	case s == "0":
		return Length{0, "px"}, nil
	case strings.HasSuffix(s, "rem"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "rem"), 64)
		if err != nil {
			return Length{}, fmt.Errorf("length %q: %w", s, err)
		}
		return Length{v, "rem"}, nil
	case strings.HasSuffix(s, "px"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "px"), 64)
		if err != nil {
			return Length{}, fmt.Errorf("length %q: %w", s, err)
		}
		return Length{v, "px"}, nil
	}
	return Length{}, fmt.Errorf("%q is not a px or rem length", s)
}

// Shadow is one CSS box-shadow.
type Shadow struct {
	X, Y, Blur, Spread float64
	Color              Color
	Inset              bool
}

// ParseShadows reads a box-shadow list, or none.
func ParseShadows(s string) ([]Shadow, error) {
	if s == "none" {
		return nil, nil
	}
	var out []Shadow
	for _, one := range splitTop(s, ',') {
		var sh Shadow
		var lens []float64
		colour := ""
		for _, f := range fieldsTop(one) {
			switch {
			case f == "inset":
				sh.Inset = true
			case strings.HasPrefix(f, "rgb") || strings.HasPrefix(f, "#") || f == "transparent":
				colour = f
			default:
				l, err := ParseLength(f)
				if err != nil || l.Unit != "px" {
					return nil, fmt.Errorf("shadow %q: %q is not a px length", one, f)
				}
				lens = append(lens, l.V)
			}
		}
		if len(lens) < 2 || len(lens) > 4 || colour == "" {
			return nil, fmt.Errorf("shadow %q: want 2 to 4 lengths and a colour", one)
		}
		c, err := ParseColor(colour)
		if err != nil {
			return nil, err
		}
		sh.X, sh.Y, sh.Color = lens[0], lens[1], c
		if len(lens) > 2 {
			sh.Blur = lens[2]
		}
		if len(lens) > 3 {
			sh.Spread = lens[3]
		}
		out = append(out, sh)
	}
	return out, nil
}

// fieldsTop splits s at spaces outside parentheses.
func fieldsTop(s string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || (s[i] == ' ' && depth == 0) {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	return out
}

// ParseDuration reads Nms and Ns as milliseconds.
func ParseDuration(s string) (float64, error) {
	switch {
	case strings.HasSuffix(s, "ms"):
		return strconv.ParseFloat(strings.TrimSuffix(s, "ms"), 64)
	case strings.HasSuffix(s, "s"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
		return v * 1000, err
	}
	return 0, fmt.Errorf("%q is not a duration", s)
}

// ParseCubicBezier reads cubic-bezier(x1, y1, x2, y2).
func ParseCubicBezier(s string) ([4]float64, error) {
	var out [4]float64
	in, ok := strings.CutPrefix(s, "cubic-bezier(")
	if !ok || !strings.HasSuffix(in, ")") {
		return out, fmt.Errorf("%q is not a cubic-bezier()", s)
	}
	parts := splitTop(strings.TrimSuffix(in, ")"), ',')
	if len(parts) != 4 {
		return out, fmt.Errorf("%q: want four numbers", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return out, fmt.Errorf("%q: %w", s, err)
		}
		out[i] = v
	}
	return out, nil
}

// VarRef reads var(--name) and var(--name, fallback).
func VarRef(s string) (name, fallback string, ok bool) {
	in, ok := strings.CutPrefix(s, "var(")
	if !ok || !strings.HasSuffix(in, ")") {
		return "", "", false
	}
	in = strings.TrimSuffix(in, ")")
	name, fallback, _ = strings.Cut(in, ",")
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "--") || strings.ContainsAny(name, " ()") {
		return "", "", false
	}
	return name, strings.TrimSpace(fallback), true
}

// GoName turns a CSS name (without its dashes) into a Go identifier: line-strong →
// LineStrong, surface-2 → Surface2, font-ui → FontUI.
func GoName(css string) string {
	var b strings.Builder
	for _, part := range strings.Split(strings.TrimPrefix(css, "--"), "-") {
		if part == "" {
			continue
		}
		switch part {
		case "ui", "id", "url":
			b.WriteString(strings.ToUpper(part))
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}
