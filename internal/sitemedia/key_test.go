package sitemedia

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

// The key is "s1-" and 32 hex digits of the SHA-256 of the fields joined by zero
// bytes; an unknown size and lmt are empty fields.
func TestKey(t *testing.T) {
	sum := sha256.Sum256([]byte("Youtube\x00aBcDeFgHiJk\x00137\x00en\x00117000000\x001790000000001080"))
	if got, want := Key("Youtube", "aBcDeFgHiJk", "137", "en", 117000000, "1790000000001080"), "s1-"+hex.EncodeToString(sum[:])[:32]; got != want {
		t.Errorf("Key = %s, want %s", got, want)
	}
	sum = sha256.Sum256([]byte("Generic\x00x\x00f\x00\x00\x00"))
	if got, want := Key("Generic", "x", "f", "", 0, ""), "s1-"+hex.EncodeToString(sum[:])[:32]; got != want {
		t.Errorf("Key without size and lmt = %s, want %s", got, want)
	}
	if Key("Generic", "x", "f", "", -5, "") != Key("Generic", "x", "f", "", 0, "") {
		t.Error("a negative size is not unknown")
	}
	if !regexp.MustCompile(`^s1-[0-9a-f]{32}$`).MatchString(Key("a", "b", "c", "d", 1, "2")) {
		t.Error("key format")
	}
}

// Every field takes part, and the separators keep shifted fields apart.
func TestKeyFieldsDistinct(t *testing.T) {
	keys := map[string]string{}
	for name, k := range map[string]string{
		"base":      Key("Youtube", "id", "137", "en", 100, "5"),
		"extractor": Key("Vimeo", "id", "137", "en", 100, "5"),
		"video":     Key("Youtube", "id2", "137", "en", 100, "5"),
		"format":    Key("Youtube", "id", "136", "en", 100, "5"),
		"language":  Key("Youtube", "id", "137", "de", 100, "5"),
		"size":      Key("Youtube", "id", "137", "en", 101, "5"),
		"no size":   Key("Youtube", "id", "137", "en", 0, "5"),
		"lmt":       Key("Youtube", "id", "137", "en", 100, "6"),
		"shifted":   Key("Youtube", "id1", "37", "en", 100, "5"),
		"moved":     Key("Youtube", "id", "137en", "", 100, "5"),
	} {
		if other, dup := keys[k]; dup {
			t.Errorf("%s and %s have the same key", name, other)
		}
		keys[k] = name
	}
}
