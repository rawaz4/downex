//go:build ignore

// genicon writes the Downex icons, drawn by internal/brand (the "D" of download
// segments in mint on a dim dark tile), each size laid out at its own resolution so
// that small sizes stay sharp. It writes appicon.png (1024 px on the macOS icon grid,
// read by "wails build" and by the macOS iconset in scripts/build-macos.sh),
// appicon-light.png (the light variant), windows/icon.ico (16 to 256 px, the
// executables' and the installer's icon), windows/tray.ico (the notification-area
// glyph at 16, 20, 24 and 32 px, which the tray also draws at run time at the size
// Windows asks for) and the browser extension's icons, ext/public/icon/<size>.png.
//
//	go run ./ui/build/genicon.go [-out ui/build] [-ext ext/public/icon]
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"

	"downex/internal/brand"
)

func main() {
	out := flag.String("out", filepath.Join("ui", "build"), "build directory of the Wails project")
	ext := flag.String("ext", filepath.Join("ext", "public", "icon"), "icon directory of the browser extension")
	flag.Parse()
	if err := run(*out, *ext); err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
}

func run(dir, ext string) error {
	files := map[string][]byte{
		filepath.Join(dir, "appicon.png"):       brand.PNG(brand.Mac(1024, brand.Dark)),
		filepath.Join(dir, "appicon-light.png"): brand.PNG(brand.Mac(1024, brand.Light)),
	}
	app, err := ico(brand.App, 16, 20, 24, 32, 40, 48, 64, 128, 256)
	if err != nil {
		return err
	}
	files[filepath.Join(dir, "windows", "icon.ico")] = app
	tray, err := ico(func(s int, _ brand.Variant) *image.NRGBA { return brand.Tray(s) }, 16, 20, 24, 32)
	if err != nil {
		return err
	}
	files[filepath.Join(dir, "windows", "tray.ico")] = tray
	for _, s := range []int{16, 32, 48, 128} {
		files[filepath.Join(ext, strconv.Itoa(s)+".png")] = brand.PNG(brand.App(s, brand.Dark))
	}
	for name, b := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(name, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func ico(draw func(int, brand.Variant) *image.NRGBA, sizes ...int) ([]byte, error) {
	imgs := make([]*image.NRGBA, len(sizes))
	for i, s := range sizes {
		imgs[i] = draw(s, brand.Dark)
	}
	return brand.ICO(imgs)
}
