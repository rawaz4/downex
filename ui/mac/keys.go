package mac

import "downex/ui/kit"

// keyOf maps a macOS virtual key code (Carbon's kVK_* values) and the character the key
// types without modifiers in the current layout to a kit key. Letters and digits follow
// the character, so that Command+A is the key that types an A on every Latin layout, as
// the kit's keys mean; every other key follows its position. It is pure: the code and
// table are untagged so that they are tested on every system.
func keyOf(code uint16, char rune) kit.Key {
	switch {
	case char >= 'a' && char <= 'z':
		return kit.KeyA + kit.Key(char-'a')
	case char >= 'A' && char <= 'Z':
		return kit.KeyA + kit.Key(char-'A')
	}
	if k, ok := macKeys[code]; ok {
		return k
	}
	return 0
}

// macKeys are the keys by position (kVK_ANSI_* and kVK_* of Carbon's Events.h).
var macKeys = map[uint16]kit.Key{
	0x00: kit.KeyA, 0x0B: kit.KeyB, 0x08: kit.KeyC, 0x02: kit.KeyD, 0x0E: kit.KeyE, 0x03: kit.KeyF,
	0x05: kit.KeyG, 0x04: kit.KeyH, 0x22: kit.KeyI, 0x26: kit.KeyJ, 0x28: kit.KeyK, 0x25: kit.KeyL,
	0x2E: kit.KeyM, 0x2D: kit.KeyN, 0x1F: kit.KeyO, 0x23: kit.KeyP, 0x0C: kit.KeyQ, 0x0F: kit.KeyR,
	0x01: kit.KeyS, 0x11: kit.KeyT, 0x20: kit.KeyU, 0x09: kit.KeyV, 0x0D: kit.KeyW, 0x07: kit.KeyX,
	0x10: kit.KeyY, 0x06: kit.KeyZ,
	0x1D: kit.Key0, 0x12: kit.Key1, 0x13: kit.Key2, 0x14: kit.Key3, 0x15: kit.Key4, 0x17: kit.Key5,
	0x16: kit.Key6, 0x1A: kit.Key7, 0x1C: kit.Key8, 0x19: kit.Key9,
	0x24: kit.KeyEnter, 0x4C: kit.KeyEnter, // Return, keypad Enter
	0x30: kit.KeyTab, 0x31: kit.KeySpace, 0x33: kit.KeyBackspace, 0x35: kit.KeyEscape,
	0x75: kit.KeyDelete, 0x72: kit.KeyInsert, // forward delete, Help (Insert on PC keyboards)
	0x73: kit.KeyHome, 0x77: kit.KeyEnd, 0x74: kit.KeyPageUp, 0x79: kit.KeyPageDown,
	0x7B: kit.KeyArrowLeft, 0x7C: kit.KeyArrowRight, 0x7D: kit.KeyArrowDown, 0x7E: kit.KeyArrowUp,
	0x38: kit.KeyShift, 0x3C: kit.KeyShift, 0x3B: kit.KeyControl, 0x3E: kit.KeyControl,
	0x3A: kit.KeyAlt, 0x3D: kit.KeyAlt, 0x37: kit.KeyMeta, 0x36: kit.KeyMeta, 0x39: kit.KeyCapsLock,
	0x6E: kit.KeyContextMenu,
	0x29: kit.KeySemicolon, 0x18: kit.KeyEqual, 0x2B: kit.KeyComma, 0x1B: kit.KeyMinus,
	0x2F: kit.KeyPeriod, 0x2C: kit.KeySlash, 0x32: kit.KeyBackquote, 0x21: kit.KeyBracketLeft,
	0x2A: kit.KeyBackslash, 0x1E: kit.KeyBracketRight, 0x27: kit.KeyQuote, 0x0A: kit.KeyIntlBackslash,
	0x52: kit.KeyNumpad0, 0x53: kit.KeyNumpad1, 0x54: kit.KeyNumpad2, 0x55: kit.KeyNumpad3,
	0x56: kit.KeyNumpad4, 0x57: kit.KeyNumpad5, 0x58: kit.KeyNumpad6, 0x59: kit.KeyNumpad7,
	0x5B: kit.KeyNumpad8, 0x5C: kit.KeyNumpad9, 0x43: kit.KeyNumpadMultiply, 0x45: kit.KeyNumpadAdd,
	0x4E: kit.KeyNumpadSubtract, 0x41: kit.KeyNumpadDecimal, 0x4B: kit.KeyNumpadDivide,
	0x47: kit.KeyNumLock, // keypad Clear
	0x7A: kit.KeyF1, 0x78: kit.KeyF2, 0x63: kit.KeyF3, 0x76: kit.KeyF4, 0x60: kit.KeyF5, 0x61: kit.KeyF6,
	0x62: kit.KeyF7, 0x64: kit.KeyF8, 0x65: kit.KeyF9, 0x6D: kit.KeyF10, 0x67: kit.KeyF11, 0x6F: kit.KeyF12,
	0x69: kit.KeyF13, 0x6B: kit.KeyF14, 0x71: kit.KeyF15, 0x6A: kit.KeyF16, 0x40: kit.KeyF17,
	0x4F: kit.KeyF18, 0x50: kit.KeyF19, 0x5A: kit.KeyF20,
}
