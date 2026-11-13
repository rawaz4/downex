package mac

import (
	"testing"

	"downex/ui/kit"
)

func TestKeyOf(t *testing.T) {
	for _, c := range []struct {
		code uint16
		char rune
		want kit.Key
	}{
		{0x00, 'a', kit.KeyA},
		{0x00, 'q', kit.KeyQ}, // the A position on an AZERTY layout types q: it is Q
		{0x0C, 'a', kit.KeyA},
		{0x06, 'Z', kit.KeyZ},
		{0x12, '&', kit.Key1}, // a digit key by its position when it types no digit
		{0x24, '\r', kit.KeyEnter},
		{0x4C, 3, kit.KeyEnter},
		{0x35, 0x1b, kit.KeyEscape},
		{0x33, 0x7f, kit.KeyBackspace},
		{0x75, 0xF728, kit.KeyDelete},
		{0x7E, 0xF700, kit.KeyArrowUp},
		{0x2B, ',', kit.KeyComma}, // Command+, opens Settings
		{0x37, 0, kit.KeyMeta},
		{0x7A, 0xF704, kit.KeyF1},
		{0x3F, 0, 0}, // fn: no kit key
	} {
		if got := keyOf(c.code, c.char); got != c.want {
			t.Errorf("keyOf(%#x, %q) = %#x, want %#x", c.code, c.char, got, c.want)
		}
	}
}
