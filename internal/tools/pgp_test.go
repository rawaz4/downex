package tools

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyDetached(t *testing.T) {
	s := newTestSigner(t, "release")
	other := newTestSigner(t, "other")
	keys, err := parseKeys(s.armoredPublic())
	if err != nil || len(keys) != 1 || keys[0].fingerprint != s.fp {
		t.Fatalf("parseKeys = %v, %v", keys, err)
	}
	sums := []byte(hexSum("a") + "  yt-dlp.exe\n")
	created := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	now := created.Add(time.Hour)
	good := s.sign(sums, created, sigOptions{creationCrit: true})
	got, err := verifyDetached(keys, sums, good, now)
	if err != nil || !got.Equal(created) {
		t.Fatalf("valid signature: %v, %v", got, err)
	}
	// Armored signatures are read too.
	if _, err := verifyDetached(keys, sums, armor("PGP SIGNATURE", good), now); err != nil {
		t.Errorf("armored signature: %v", err)
	}
	flipped := bytes.Clone(good)
	flipped[len(flipped)-5] ^= 0x40
	tamperedSums := bytes.Replace(sums, []byte("yt-dlp.exe"), []byte("yt-dlp.exf"), 1)
	cases := map[string]struct {
		data, sig []byte
		keys      []pgpKey
	}{
		"tampered SUMS":        {tamperedSums, good, keys},
		"trailing byte":        {append(bytes.Clone(sums), '\n'), good, keys},
		"tampered signature":   {sums, flipped, keys},
		"other signer":         {sums, other.sign(sums, created, sigOptions{}), keys},
		"no keys":              {sums, good, nil},
		"text signature":       {sums, s.sign(sums, created, sigOptions{sigType: 0x01}), keys},
		"SHA-256":              {sums, s.sign(sums, created, sigOptions{hashAlgo: 8}), keys},
		"no issuer":            {sums, s.sign(sums, created, sigOptions{noIssuerFP: true}), keys},
		"unknown critical":     {sums, s.sign(sums, created, sigOptions{critical: 99}), keys},
		"expired":              {sums, s.sign(sums, created, sigOptions{expires: time.Minute}), keys},
		"empty":                {sums, nil, keys},
		"two packets":          {sums, append(bytes.Clone(good), good...), keys},
		"truncated":            {sums, good[:len(good)/2], keys},
		"key packet":           {sums, packet(pgpTagPublicKey, s.body), keys},
		"garbage":              {sums, []byte("not a signature"), keys},
		"armor without packet": {sums, []byte("-----BEGIN PGP SIGNATURE-----\n\n=AAAA\n-----END PGP SIGNATURE-----\n"), keys},
	}
	for name, c := range cases {
		if _, err := verifyDetached(c.keys, c.data, c.sig, now); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
		}
	}
	// Keys of several blocks are all pinned.
	both, err := pinnedKeys([][]byte{other.armoredPublic(), s.armoredPublic()})
	if err != nil || len(both) != 2 {
		t.Fatalf("pinnedKeys = %d keys, %v", len(both), err)
	}
	if _, err := verifyDetached(both, sums, good, now); err != nil {
		t.Errorf("second pinned key: %v", err)
	}
}

func TestParseKeysArmor(t *testing.T) {
	s := newTestSigner(t, "release")
	arm := s.armoredPublic()
	if _, err := parseKeys(arm); err != nil {
		t.Fatal(err)
	}
	// Binary key blocks are read too.
	if keys, err := parseKeys(packet(pgpTagPublicKey, s.body)); err != nil || len(keys) != 1 {
		t.Errorf("binary key: %v, %v", keys, err)
	}
	// An old-format packet header (tag 6, two-octet length) gives the same key.
	old := append([]byte{0x80 | pgpTagPublicKey<<2 | 1, byte(len(s.body) >> 8), byte(len(s.body))}, s.body...)
	if keys, err := parseKeys(old); err != nil || len(keys) != 1 || keys[0].fingerprint != s.fp {
		t.Errorf("old-format key: %v, %v", keys, err)
	}
	lines := strings.Split(string(arm), "\n")
	var crcLine int
	for i, l := range lines {
		if strings.HasPrefix(l, "=") {
			crcLine = i
		}
	}
	badCRC := append([]string(nil), lines...)
	badCRC[crcLine] = "=AAAA"
	noCRC := append(append([]string(nil), lines[:crcLine]...), lines[crcLine+1:]...)
	if _, err := parseKeys([]byte(strings.Join(noCRC, "\n"))); err != nil {
		t.Errorf("armor without checksum: %v", err)
	}
	small := newSmallKeyBody(t)
	for name, data := range map[string][]byte{
		"bad checksum": []byte(strings.Join(badCRC, "\n")),
		"unterminated": arm[:len(arm)-30],
		"no key":       armor("PGP PUBLIC KEY BLOCK", packet(13, []byte("user only"))),
		"bad base64":   []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n!!!!\n-----END PGP PUBLIC KEY BLOCK-----\n"),
		"small key":    packet(pgpTagPublicKey, small),
		"empty":        nil,
		"huge":         bytes.Repeat([]byte{'a'}, maxArmoredKey+1),
		"partial len":  {0xc6, 0xe1, 0x00},
		"not a packet": {0x06, 0x01},
	} {
		if _, err := parseKeys(data); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
		}
	}
	// A v3 key or a non-RSA key in the block is skipped, not fatal.
	mixed := append(packet(pgpTagPublicKey, []byte{4, 0, 0, 0, 0, 22, 0, 8, 0xff}), packet(pgpTagPublicSubkey, s.body)...)
	if keys, err := parseKeys(mixed); err != nil || len(keys) != 1 {
		t.Errorf("mixed block: %v, %v", keys, err)
	}
}

// newSmallKeyBody is a v4 RSA key body with a 1024-bit modulus (refused).
func newSmallKeyBody(t *testing.T) []byte {
	n := append([]byte{0x04, 0x00}, bytes.Repeat([]byte{0xff}, 128)...)
	return append(append([]byte{4, 0, 0, 0, 0, pgpAlgoRSA}, n...), 0x00, 0x11, 0x01, 0x00, 0x01)
}

// TestEmbeddedYtDlpKey checks the embedded key's pinned fingerprint. The file must hold
// upstream's public.key (docs/SITE-MEDIA-SPEC.md §4.3).
func TestEmbeddedYtDlpKey(t *testing.T) {
	if !bytes.Contains(ytdlpKeyASC, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		if _, err := pinnedKeys(nil); !errors.Is(err, errEmbeddedKey) {
			t.Errorf("placeholder key: %v, want errEmbeddedKey", err)
		}
		t.Skip("ytdlp_key.asc is still a placeholder: put yt-dlp's public.key there (fingerprint " + ytdlpKeyFingerprint + ")")
	}
	keys, err := pinnedKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(keys[0].fingerprint[:])); got != ytdlpKeyFingerprint {
		t.Errorf("fingerprint %s, want %s", got, ytdlpKeyFingerprint)
	}
	if bits := keys[0].pub.N.BitLen(); bits != 4096 {
		t.Errorf("key size %d, want 4096", bits)
	}
}

// TestRealYtDlpSignature verifies the real signature of yt-dlp 2026.08.19 (stable) kept
// in testdata: SHA2-256SUMS, SHA2-256SUMS.sig and public.key from that release.
func TestRealYtDlpSignature(t *testing.T) {
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", "ytdlp-2026.08.19", name))
		if err != nil {
			t.Skipf("testdata of the real 2026.08.19 release missing (%v)", err)
		}
		return data
	}
	sums, sig, key := read("SHA2-256SUMS"), read("SHA2-256SUMS.sig"), read("public.key")
	keys, err := pinnedKeys([][]byte{key})
	if err != nil {
		t.Fatal(err)
	}
	created, err := verifyDetached(keys, sums, sig, time.Now())
	if err != nil {
		t.Fatalf("real signature: %v", err)
	}
	if created.Year() != 2026 {
		t.Errorf("creation time %v", created)
	}
	if _, err := sumFor(sums, "yt-dlp.exe"); err != nil {
		t.Errorf("SUMS names yt-dlp.exe: %v", err)
	}
	if _, err := verifyDetached(keys, append(bytes.Clone(sums), ' '), sig, time.Now()); !errors.Is(err, ErrSignature) {
		t.Errorf("tampered real SUMS: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(key), bytes.TrimSpace(ytdlpKeyASC)) {
		t.Error("testdata public.key differs from the embedded ytdlp_key.asc")
	}
}

func FuzzParseSignature(f *testing.F) {
	s := newTestSigner(f, "release")
	f.Add(s.sign([]byte("data"), time.Unix(1_700_000_000, 0), sigOptions{creationCrit: true}))
	f.Add(armor("PGP SIGNATURE", s.sign([]byte("x"), time.Unix(1, 0), sigOptions{expires: time.Hour})))
	f.Add([]byte{0x88, 0x02, 0x04, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := parseSignature(data); err != nil && !errors.Is(err, ErrSignature) {
			t.Fatalf("error not wrapping ErrSignature: %v", err)
		}
	})
}

func FuzzParseKeys(f *testing.F) {
	s := newTestSigner(f, "release")
	f.Add(s.armoredPublic())
	f.Add(packet(pgpTagPublicKey, s.body))
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := parseKeys(data); err != nil && !errors.Is(err, ErrSignature) {
			t.Fatalf("error not wrapping ErrSignature: %v", err)
		}
	})
}
