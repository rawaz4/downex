//go:build windows

package win

import (
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows/registry"
)

// readEnvironment is the window's scale at dpi and the system settings that change the
// look: dark apps, a contrast theme and its colours, animations, and the text size.
func readEnvironment(dpi uint32) kit.Environment {
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: float32(dpi) / 96, Text: textScale()}}
	env.Dark = appsDark()
	hc := w32.HIGHCONTRAST{}
	hc.Size = uint32(unsafe.Sizeof(hc))
	if w32.SystemParametersInfo(w32.SPI_GETHIGHCONTRAST, hc.Size, unsafe.Pointer(&hc)) {
		env.HighContrast = hc.Flags&w32.HCF_HIGHCONTRASTON != 0
	}
	env.SystemColors = kit.SystemColors{
		Canvas:        sysColor(w32.COLOR_WINDOW),
		CanvasText:    sysColor(w32.COLOR_WINDOWTEXT),
		LinkText:      sysColor(w32.COLOR_HOTLIGHT),
		GrayText:      sysColor(w32.COLOR_GRAYTEXT),
		Highlight:     sysColor(w32.COLOR_HIGHLIGHT),
		HighlightText: sysColor(w32.COLOR_HIGHLIGHTTEXT),
		ButtonFace:    sysColor(w32.COLOR_BTNFACE),
		ButtonText:    sysColor(w32.COLOR_BTNTEXT),
	}
	var anim int32 = 1
	if w32.SystemParametersInfo(w32.SPI_GETCLIENTAREAANIMATION, 0, unsafe.Pointer(&anim)) {
		env.ReducedMotion = anim == 0
	}
	env.ReducedTransparency = !transparencyOn()
	var hover uint32
	if w32.SystemParametersInfo(w32.SPI_GETMOUSEHOVERTIME, 0, unsafe.Pointer(&hover)) && hover > 0 {
		env.HoverTime = time.Duration(hover) * time.Millisecond
	}
	return env
}

func sysColor(index int32) kit.Color {
	c := w32.GetSysColor(index)
	return kit.RGBA(uint8(c), uint8(c>>8), uint8(c>>16), 1)
}

// appsDark reads Settings › Personalisation › Colours › app mode.
func appsDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// transparencyOn reads Settings › Personalisation › Colours › Transparency effects (on when
// the value is missing), which Chromium's prefers-reduced-transparency follows.
func transparencyOn() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("EnableTransparency")
	return err != nil || v != 0
}

// textScale reads Settings › Accessibility › Text size: 1 to 2.25.
func textScale() float32 {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Accessibility`, registry.QUERY_VALUE)
	if err != nil {
		return 1
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("TextScaleFactor")
	if err != nil || v < 100 || v > 225 {
		return 1
	}
	return float32(v) / 100
}

// readSettings reads the mouse settings the windows' input needs.
func (a *App) readSettings() {
	a.wheelLines, a.wheelChars = 3, 3
	var v uint32
	if w32.SystemParametersInfo(w32.SPI_GETWHEELSCROLLLINES, 0, unsafe.Pointer(&v)) {
		a.wheelLines = v
	}
	if w32.SystemParametersInfo(w32.SPI_GETWHEELSCROLLCHARS, 0, unsafe.Pointer(&v)) {
		a.wheelChars = v
	}
}
