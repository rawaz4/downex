package settings

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// skin is how the Settings window is drawn on an OS (owner decision 2026-11-10: Windows 11's
// Settings on Windows, macOS's System Settings on the Mac). The pages, their words and their
// behaviour are the same under both; only sizes, grounds and the navigation differ.
type skin uint8

const (
	// skinFluent is Windows 11's Settings: a page title, the content filling the pane (at most
	// fluentMaxWidth wide, centred, with large text), every row its own card, Windows 11 switches, a pill marker in the navigation.
	skinFluent skin = iota
	// skinMac is macOS's System Settings: a smaller dialog, grouped rows under quiet grey
	// headings, macOS switches, coloured icon tiles and a filled selection in the navigation.
	skinMac
	// skinPlain is the navigation's plain look: a narrow dialog's row of tabs, forced colours.
	skinPlain
)

// skinFor is the skin of a window's environment.
func skinFor(env kit.Environment) skin {
	if env.WebKit { // only the macOS host sets it
		return skinMac
	}
	return skinFluent
}

// curSkin is the skin the page builders use. The dialog sets it before it builds a page (on the
// UI thread, which builds every page), so the builders need no dialog.
var curSkin = skinFluent

// fluentMaxWidth is the widest a Windows page's content gets: in the 60rem dialog it fills the
// pane; with larger text it stops here, centred.
const fluentMaxWidth kit.DIP = 760

// The sizes of the Settings dialog per skin.
func (s skin) dialogSize() (w, h kit.Length) {
	return kit.Rem(s.widthRem()), kit.Rem(s.heightRem())
}

func (s skin) widthRem() float32 {
	if s == skinMac {
		return 52
	}
	return 60 // the owner (2026-11-11): 70rem left too much empty room around the pages
}

// heightRem is the dialog's least height.
func (s skin) heightRem() float32 {
	if s == skinMac {
		return 40
	}
	return 46
}

// navWidth is the navigation's width.
func (s skin) navWidth(sc kit.Scale) kit.DIP {
	if s == skinMac {
		return sc.Rem(12)
	}
	return sc.Rem(14)
}

// switchStyle is the switches' style.
func (s skin) switchStyle() widgets.SwitchStyle {
	if s == skinMac {
		return widgets.SwitchMac
	}
	return widgets.SwitchFluent
}

var (
	// fontPageTitle is a Windows page's title: 600 26 px display.
	fontPageTitle = displayFont(kit.Px(26), 600, 1.25)
	// fontMacPageTitle is a Mac page's title: 650 20 px display.
	fontMacPageTitle = displayFont(kit.Px(20), 650, 1.25)
	// fontNavTitle is "Settings" above the navigation: the dialog title's 650 16 px display.
	fontNavTitle = displayFont(tokens.TypeDialog, 650, 1.25)
	// fontFluentHead is a Windows section's heading: 600 var(--type-body).
	fontFluentHead = uiFont(tokens.TypeBody, 600, 1.4)
	// fontMacHead is a macOS group's heading: 600 12.5 px in --muted.
	fontMacHead = uiFont(tokens.TypeCell, 600, 1.35)
)

// pageTitle is a Windows page's title (an h2 under the dialog's).
func pageTitle(s string) *kit.Element {
	f, pad := fontPageTitle, kit.Px(18)
	if curSkin == skinMac {
		f, pad = fontMacPageTitle, kit.Px(14)
	}
	t := heading(s, f, 2)
	// the end keeps clear of the close button over the pane's corner
	return el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Padding: kit.Edges{Bottom: pad, End: kit.Px(40)}}, t)
}

// macTileColors are the navigation's icon tiles on the Mac, one per page (in pages' order).
var macTileColors = []kit.Color{
	kit.RGB(0x6b7a73), // General
	kit.RGB(0x2e9b6a), // Downloads
	kit.RGB(0x3a7bd5), // Network
	kit.RGB(0x8a63d2), // Integration
	kit.RGB(0xd1495b), // Video sites
	kit.RGB(0xd98a2b), // Appearance
	kit.RGB(0x3a9fb5), // Updates
	kit.RGB(0x5d6a64), // About
}

// inkWhite is the icon on a macOS tile.
var inkWhite views.Ink = func(*tokens.Theme) kit.Color { return kit.RGB(0xffffff) }
