// Command gen writes ui/kit's generated Go files from the WebView window's sources in
// ui/frontend/src, so that both windows draw with the same values. go generate runs it:
//
//	go run ../internal/gen -tokens tokens_gen.go  (in ui/kit/tokens: app.css's tokens)
//	go run ../internal/gen -icons icons_gen.go    (in ui/kit/icons: Icon, FileIcon, StatusMark)
//
// The packages' drift tests generate the same files in memory and fail when they differ
// from the checked-in ones, so a change made to one UI and not the other is caught.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"downex/ui/kit/frontend"
)

func main() {
	tokensOut := flag.String("tokens", "", "write the tokens file here")
	iconsOut := flag.String("icons", "", "write the icons file here")
	flag.Parse()
	if (*tokensOut == "") == (*iconsOut == "") {
		fmt.Fprintln(os.Stderr, "gen: give one of -tokens FILE or -icons FILE")
		os.Exit(2)
	}
	src, err := frontend.Dir()
	if err != nil {
		fail(err)
	}
	var out []byte
	path := *tokensOut
	if path != "" {
		out, err = frontend.GenerateTokens(src)
	} else {
		path = *iconsOut
		out, err = frontend.GenerateIcons(src)
	}
	if err != nil {
		fail(err)
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, out) {
		return // unchanged: keep the file's time
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen:", err)
	os.Exit(1)
}
