package sitemedia

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// keyPrefix marks a site track key and the version of its recipe.
const keyPrefix = "s1-"

// Key returns the identity of a format across resolutions of its page
// (docs/SITE-MEDIA-SPEC.md §3.11): "s1-" and the first 32 hex digits of the SHA-256 of
// the extractor key, the video id, the format id, the language, the exact size (""
// when unknown, size <= 0) and the URL's lmt parameter ("" when none), separated by
// zero bytes. URLs and their tokens change with every resolution and are not part of
// it; a re-encoded format (another size or lmt) gets another key, so a resumed track
// never mixes bytes of two encodings.
func Key(extractorKey, videoID, formatID, language string, size int64, lmt string) string {
	sizeText := ""
	if size > 0 {
		sizeText = strconv.FormatInt(size, 10)
	}
	h := sha256.New()
	for i, part := range []string{extractorKey, videoID, formatID, language, sizeText, lmt} {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(part))
	}
	return keyPrefix + hex.EncodeToString(h.Sum(nil))[:32]
}
