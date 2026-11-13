// Package icons draws the "Studio" design's icons on a kit canvas: the line icons of
// Icon.svelte, the file tiles of FileIcon.svelte and the status marks of StatusMark.svelte,
// whose path data and paints are generated from those components (icons_gen.go; go generate
// writes it, and the drift test fails when a component and it disagree), and the Downex logo
// of BrandMark.svelte and internal/brand, laid out on the pixel grid of its size (brand.go).
// It is platform-free.
package icons

//go:generate go run ../internal/gen -icons icons_gen.go

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// Name is a line icon of Icon.svelte.
type Name uint8

type lineIcon struct {
	name   string
	d      string
	stroke float32 // in grid units
	filled bool    // filled as well as stroked (resume, resume-all, play)
}

// String is the icon's name in Icon.svelte ("win-close").
func (n Name) String() string {
	if n >= numIcons {
		return "?"
	}
	return lineIcons[n].name
}

// ByName is the icon Icon.svelte names name, and whether there is one.
func ByName(name string) (Name, bool) {
	for i := range lineIcons {
		if lineIcons[i].name == name {
			return Name(i), true
		}
	}
	return 0, false
}

// TileKind is a file kind of FileIcon.svelte.
type TileKind uint8

// String is the kind's name in FileIcon.svelte.
func (k TileKind) String() string {
	names := [...]string{"video", "audio", "archives", "documents", "programs", "images", "other"}
	if int(k) < len(names) {
		return names[k]
	}
	return "other"
}

type paint uint8

const (
	paintNone paint = iota
	paintHue
	paintDeep
	paintGlyph
)

// shape is one shape of a tile or a mark, on its component's grid.
type shape struct {
	d                   string
	fill, stroke        paint
	width               float32 // stroke width in grid units
	roundCap, roundJoin bool
	fillOpacity         float32 // 0: opaque
	opacity             float32 // 0: opaque
	rotate              [3]float32
}

type tile struct {
	hue, deep func(*tokens.Theme) kit.Color
	shapes    []shape
	small     []shape // at smallTile pixels and below; nil: the same shapes
}

// MarkKind is a status mark of StatusMark.svelte.
type MarkKind uint8

type mark struct {
	color  func(*tokens.Theme) kit.Color
	shapes []markShape
}

type markShape struct {
	shape
	fill, stroke func(*tokens.Theme) kit.Color
	// onTone marks the glyph drawn on the tone (StatusMark's .on): Canvas in forced colours,
	// where the rest is CanvasText.
	onTone bool
}
