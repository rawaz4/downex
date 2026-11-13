package settings

import (
	"sync"

	"downex/ui/core/categories"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/views"
)

// The file tiles (FileIcon.svelte: Downloads' categories, Appearance's preview) and the logo
// (BrandMark.svelte: About), drawn with kit/icons. The widgets package keeps its icon sets to
// itself, so this package keeps the sets it draws tiles and the logo with, one per kit
// resources (ReleaseIcons frees them).

var iconSets struct {
	sync.Mutex
	m map[kit.Resources]*icons.Set
}

func iconsFor(res kit.Resources) *icons.Set {
	iconSets.Lock()
	defer iconSets.Unlock()
	if iconSets.m == nil {
		iconSets.m = map[kit.Resources]*icons.Set{}
	}
	s := iconSets.m[res]
	if s == nil {
		s = icons.NewSet(res)
		iconSets.m[res] = s
	}
	return s
}

// ReleaseIcons frees the paths this package drew with res (call it with
// widgets.ReleaseIcons, before res closes).
func ReleaseIcons(res kit.Resources) {
	iconSets.Lock()
	defer iconSets.Unlock()
	if s := iconSets.m[res]; s != nil {
		s.Release()
		delete(iconSets.m, res)
	}
}

// tileKindOf is a category's tile.
func tileKindOf(k categories.FileKind) icons.TileKind {
	switch k {
	case categories.Video:
		return icons.TileVideo
	case categories.Audio:
		return icons.TileAudio
	case categories.Archives:
		return icons.TileArchives
	case categories.Documents:
		return icons.TileDocuments
	case categories.Programs:
		return icons.TilePrograms
	}
	return icons.TileOther
}

// tile is a decorative file tile of a size.
type tile struct {
	kit.Element
	kind icons.TileKind
	size kit.DIP
}

func newTile(kind icons.TileKind, size kit.DIP) *tile {
	t := &tile{kind: kind, size: size}
	t.Semantics.Hidden = true
	t.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	return t
}

func (t *tile) set(kind icons.TileKind, size kit.DIP) {
	if kind == t.kind && size == t.size {
		return
	}
	resized := size != t.size
	t.kind, t.size = kind, size
	if resized {
		t.Style.Width, t.Style.Height = kit.Px(size), kit.Px(size)
		t.Relayout()
	}
	t.Invalidate()
}

// Paint is kit.Painter's.
func (t *tile) Paint(p *kit.PaintContext) {
	w := t.Window()
	if w == nil || w.Host() == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	iconsFor(w.Host().Resources()).DrawTile(p.Canvas, t.kind, at, t.size, views.TokensOf(&t.Element))
}

// brandMark is BrandMark.svelte: the logo of a size.
type brandMark struct {
	kit.Element
	size kit.DIP
}

func newBrandMark(size kit.DIP) *brandMark {
	b := &brandMark{size: size}
	b.Semantics.Hidden = true
	b.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	return b
}

// Paint is kit.Painter's.
func (b *brandMark) Paint(p *kit.PaintContext) {
	w := b.Window()
	if w == nil || w.Host() == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	iconsFor(w.Host().Resources()).DrawBrand(p.Canvas, at, float64(b.size), views.TokensOf(&b.Element))
}
