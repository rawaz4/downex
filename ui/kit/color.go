package kit

// Color is an sRGB colour with straight (not premultiplied) alpha, each part from 0 to 1,
// as CSS rgb() and the tokens of ui/frontend/src/app.css give them. The zero value is
// transparent.
type Color struct {
	R, G, B, A float32
}

// RGB is the opaque colour written 0xRRGGBB (CSS #rrggbb).
func RGB(hex uint32) Color {
	return Color{float32(hex>>16&0xff) / 255, float32(hex>>8&0xff) / 255, float32(hex&0xff) / 255, 1}
}

// RGBA is CSS rgb(r g b / a).
func RGBA(r, g, b uint8, a float32) Color {
	return Color{float32(r) / 255, float32(g) / 255, float32(b) / 255, a}
}

// WithAlpha is c with alpha a.
func (c Color) WithAlpha(a float32) Color {
	c.A = a
	return c
}

// Mix is CSS color-mix(in srgb, a p, b) with p from 0 to 1 (0.88 for 88%): p of a and the
// rest of b, mixed with premultiplied alpha as CSS Color 5 mixes, so that mixing with
// transparent keeps the hue: Mix(hue, 0.11, Color{}) is the hue at alpha 0.11 (app.css
// .tag), and Mix(danger, 0.88, ink) darkens the danger button on hover.
func Mix(a Color, p float32, b Color) Color {
	p = min(max(p, 0), 1)
	q := 1 - p
	alpha := a.A*p + b.A*q
	if alpha <= 0 {
		return Color{}
	}
	return Color{
		(a.R*a.A*p + b.R*b.A*q) / alpha,
		(a.G*a.A*p + b.G*b.A*q) / alpha,
		(a.B*a.A*p + b.B*b.A*q) / alpha,
		alpha,
	}
}
