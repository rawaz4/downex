package tools

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha512"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// This file checks yt-dlp's detached OpenPGP signature (docs/SITE-MEDIA-SPEC.md §4.3)
// with the standard library only. It reads exactly what that needs of RFC 4880/9580:
// ASCII armor, v4 RSA public keys (primary keys and subkeys) and their fingerprints,
// and one v4 signature packet of class 0x00 (binary document) made with RSA and SHA-512.
// Keys are trusted only through their pinned fingerprints; their self-signatures, user
// IDs and other packets are not read. Every input is untrusted and bounded.

// ytdlpKeyFingerprint pins "Simon Sawicki (yt-dlp signing key)", RSA-4096, no expiry,
// which signs the stable and nightly releases.
const ytdlpKeyFingerprint = "AC0CBBE6848D6A873464AF4E57CF65933B5A7581"

// ytdlpKeyASC is upstream's armored public key (github.com/yt-dlp/yt-dlp public.key).
//
//go:embed ytdlp_key.asc
var ytdlpKeyASC []byte

// OpenPGP constants used here.
const (
	pgpTagSignature    = 2
	pgpTagPublicKey    = 6
	pgpTagPublicSubkey = 14

	pgpAlgoRSA       = 1 // RSA (encrypt or sign)
	pgpAlgoRSASign   = 3 // RSA sign-only (deprecated, still readable)
	pgpHashSHA512    = 10
	pgpSigBinaryDoc  = 0x00
	subCreationTime  = 2
	subExpiration    = 3
	subIssuerKeyID   = 16
	subIssuerFprint  = 33
	maxPGPPackets    = 256
	maxArmoredKey    = 256 << 10
	minRSAKeyBits    = 2048
	maxRSAKeyBits    = 8192
	pgpFingerprintV4 = 20
)

// pgpKey is a v4 RSA public key and its fingerprint.
type pgpKey struct {
	fingerprint [pgpFingerprintV4]byte
	pub         *rsa.PublicKey
}

// pgpPacket is one packet: its tag and body.
type pgpPacket struct {
	tag  byte
	body []byte
}

func malformedPGP(what string) error { return fmt.Errorf("%w: %s", ErrSignature, what) }

// readPackets splits data into packets (old and new header formats; partial body
// lengths are refused: keys and signatures never use them).
func readPackets(data []byte) ([]pgpPacket, error) {
	var out []pgpPacket
	for len(data) > 0 {
		if len(out) == maxPGPPackets {
			return nil, malformedPGP("too many packets")
		}
		b := data[0]
		if b&0x80 == 0 {
			return nil, malformedPGP("not an OpenPGP packet")
		}
		var tag byte
		var n, hdr int64
		if b&0x40 != 0 { // new format
			tag = b & 0x3f
			if len(data) < 2 {
				return nil, malformedPGP("truncated packet header")
			}
			switch o1 := int64(data[1]); {
			case o1 < 192:
				n, hdr = o1, 2
			case o1 < 224:
				if len(data) < 3 {
					return nil, malformedPGP("truncated packet header")
				}
				n, hdr = (o1-192)<<8+int64(data[2])+192, 3
			case o1 == 255:
				if len(data) < 6 {
					return nil, malformedPGP("truncated packet header")
				}
				n, hdr = int64(binary.BigEndian.Uint32(data[2:6])), 6
			default:
				return nil, malformedPGP("partial packet lengths are not supported")
			}
		} else { // old format
			tag = (b >> 2) & 0x0f
			switch b & 3 {
			case 0:
				if len(data) < 2 {
					return nil, malformedPGP("truncated packet header")
				}
				n, hdr = int64(data[1]), 2
			case 1:
				if len(data) < 3 {
					return nil, malformedPGP("truncated packet header")
				}
				n, hdr = int64(binary.BigEndian.Uint16(data[1:3])), 3
			case 2:
				if len(data) < 5 {
					return nil, malformedPGP("truncated packet header")
				}
				n, hdr = int64(binary.BigEndian.Uint32(data[1:5])), 5
			default: // indeterminate: the rest of the data
				n, hdr = int64(len(data)-1), 1
			}
		}
		if n > int64(len(data))-hdr {
			return nil, malformedPGP("truncated packet")
		}
		out = append(out, pgpPacket{tag: tag, body: data[hdr : hdr+n]})
		data = data[hdr+n:]
	}
	return out, nil
}

// readMPI reads a multiprecision integer: a two-octet bit count and its big-endian bytes.
func readMPI(b []byte) (mpi, rest []byte, err error) {
	if len(b) < 2 {
		return nil, nil, malformedPGP("truncated integer")
	}
	n := (int(binary.BigEndian.Uint16(b)) + 7) / 8
	if len(b)-2 < n {
		return nil, nil, malformedPGP("truncated integer")
	}
	return b[2 : 2+n], b[2+n:], nil
}

// crc24 is the armor checksum of RFC 4880 §6.1.
func crc24(b []byte) uint32 {
	crc := uint32(0xB704CE)
	for _, c := range b {
		crc ^= uint32(c) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	return crc & 0xFFFFFF
}

// dearmor decodes the first ASCII-armored block of kind ("PGP PUBLIC KEY BLOCK",
// "PGP SIGNATURE") in data, checking its CRC-24 when present.
func dearmor(data []byte, kind string) ([]byte, error) {
	text := string(data)
	begin, end := "-----BEGIN "+kind+"-----", "-----END "+kind+"-----"
	i := strings.Index(text, begin)
	if i < 0 {
		return nil, malformedPGP("no " + kind)
	}
	text = text[i+len(begin):]
	j := strings.Index(text, end)
	if j < 0 {
		return nil, malformedPGP("unterminated " + kind)
	}
	var b64 strings.Builder
	var sum string
	headers := true
	for k, l := range lines(text[:j]) {
		l = strings.TrimSpace(l)
		if k == 0 {
			if l != "" {
				return nil, malformedPGP("malformed armor")
			}
			continue
		}
		if headers {
			if l == "" {
				headers = false
				continue
			}
			if strings.Contains(l, ": ") {
				continue
			}
			headers = false // a block without headers or blank line
		}
		switch {
		case l == "":
		case strings.HasPrefix(l, "="):
			sum = l[1:]
		default:
			if sum != "" {
				return nil, malformedPGP("data after the armor checksum")
			}
			b64.WriteString(l)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil || len(raw) == 0 {
		return nil, malformedPGP("malformed armor")
	}
	if sum != "" {
		c, err := base64.StdEncoding.DecodeString(sum)
		if err != nil || len(c) != 3 || uint32(c[0])<<16|uint32(c[1])<<8|uint32(c[2]) != crc24(raw) {
			return nil, malformedPGP("armor checksum mismatch")
		}
	}
	return raw, nil
}

// parseKeys returns the v4 RSA keys (primary keys and subkeys) of an armored or binary
// key block. Other key versions and algorithms are skipped.
func parseKeys(data []byte) ([]pgpKey, error) {
	if len(data) > maxArmoredKey {
		return nil, malformedPGP("key block too large")
	}
	if bytes.Contains(data, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		raw, err := dearmor(data, "PGP PUBLIC KEY BLOCK")
		if err != nil {
			return nil, err
		}
		data = raw
	}
	pkts, err := readPackets(data)
	if err != nil {
		return nil, err
	}
	var keys []pgpKey
	for _, p := range pkts {
		if p.tag != pgpTagPublicKey && p.tag != pgpTagPublicSubkey {
			continue
		}
		k, ok, err := parsePublicKey(p.body)
		if err != nil {
			return nil, err
		}
		if ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, malformedPGP("no v4 RSA key")
	}
	return keys, nil
}

// parsePublicKey reads a public key packet body; ok is false for keys that are not v4
// RSA keys.
func parsePublicKey(body []byte) (k pgpKey, ok bool, err error) {
	if len(body) < 6 || len(body) > 0xffff {
		return pgpKey{}, false, malformedPGP("malformed public key")
	}
	if body[0] != 4 || (body[5] != pgpAlgoRSA && body[5] != pgpAlgoRSASign) {
		return pgpKey{}, false, nil
	}
	n, rest, err := readMPI(body[6:])
	if err != nil {
		return pgpKey{}, false, err
	}
	e, rest, err := readMPI(rest)
	if err != nil {
		return pgpKey{}, false, err
	}
	if len(rest) != 0 {
		return pgpKey{}, false, malformedPGP("malformed RSA key")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n)}
	if bits := pub.N.BitLen(); bits < minRSAKeyBits || bits > maxRSAKeyBits {
		return pgpKey{}, false, malformedPGP(fmt.Sprintf("unsupported RSA key size %d", bits))
	}
	ev := new(big.Int).SetBytes(e)
	if len(e) > 4 || !ev.IsInt64() || ev.Int64() < 3 || ev.Int64() > 1<<31-1 || ev.Bit(0) == 0 {
		return pgpKey{}, false, malformedPGP("unsupported RSA exponent")
	}
	pub.E = int(ev.Int64())
	h := sha1.New()
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
	h.Write(body)
	copy(k.fingerprint[:], h.Sum(nil))
	k.pub = pub
	return k, true, nil
}

// pgpSignature is a parsed v4 signature packet.
type pgpSignature struct {
	hashed   []byte // the body's first 6 + hashed-length bytes: what the digest covers
	left16   [2]byte
	mpi      []byte
	created  time.Time
	expires  time.Duration // 0 = never
	issuerFP []byte        // 20 bytes, from the issuer fingerprint subpacket
}

// parseSignature reads a detached signature (binary, or armored "PGP SIGNATURE"): exactly
// one v4 signature packet of class 0x00, RSA and SHA-512, with a hashed creation time
// and an issuer fingerprint.
func parseSignature(data []byte) (*pgpSignature, error) {
	if len(data) > maxSigFile {
		return nil, malformedPGP("signature too large")
	}
	if bytes.Contains(data, []byte("-----BEGIN PGP SIGNATURE-----")) {
		raw, err := dearmor(data, "PGP SIGNATURE")
		if err != nil {
			return nil, err
		}
		data = raw
	}
	pkts, err := readPackets(data)
	if err != nil {
		return nil, err
	}
	if len(pkts) != 1 || pkts[0].tag != pgpTagSignature {
		return nil, malformedPGP("not a single signature packet")
	}
	b := pkts[0].body
	if len(b) < 6 {
		return nil, malformedPGP("truncated signature")
	}
	switch {
	case b[0] != 4:
		return nil, malformedPGP("not a version 4 signature")
	case b[1] != pgpSigBinaryDoc:
		return nil, malformedPGP("not a binary document signature")
	case b[2] != pgpAlgoRSA && b[2] != pgpAlgoRSASign:
		return nil, malformedPGP("not an RSA signature")
	case b[3] != pgpHashSHA512:
		return nil, malformedPGP("not a SHA-512 signature")
	}
	hl := int(binary.BigEndian.Uint16(b[4:6]))
	if len(b) < 6+hl+2 {
		return nil, malformedPGP("truncated signature")
	}
	sig := &pgpSignature{hashed: b[:6+hl]}
	pos := 6 + hl
	ul := int(binary.BigEndian.Uint16(b[pos : pos+2]))
	pos += 2
	if len(b) < pos+ul+2 {
		return nil, malformedPGP("truncated signature")
	}
	if err := sig.subpackets(b[6:6+hl], true); err != nil {
		return nil, err
	}
	if err := sig.subpackets(b[pos:pos+ul], false); err != nil {
		return nil, err
	}
	pos += ul
	copy(sig.left16[:], b[pos:pos+2])
	mpi, rest, err := readMPI(b[pos+2:])
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 || len(mpi) == 0 {
		return nil, malformedPGP("malformed signature value")
	}
	sig.mpi = mpi
	if sig.created.IsZero() {
		return nil, malformedPGP("no signature creation time")
	}
	if len(sig.issuerFP) != pgpFingerprintV4 {
		return nil, malformedPGP("no issuer fingerprint")
	}
	return sig, nil
}

// subpackets reads a signature's hashed or unhashed subpacket area. Only the hashed area
// may set the creation and expiration times; an unknown critical subpacket in it makes
// the signature unusable (RFC 4880 §5.2.3.1).
func (s *pgpSignature) subpackets(b []byte, hashed bool) error {
	for len(b) > 0 {
		var n, hdr int
		switch o1 := int(b[0]); {
		case o1 < 192:
			n, hdr = o1, 1
		case o1 < 255:
			if len(b) < 2 {
				return malformedPGP("truncated subpacket")
			}
			n, hdr = (o1-192)<<8+int(b[1])+192, 2
		default:
			if len(b) < 5 {
				return malformedPGP("truncated subpacket")
			}
			v := binary.BigEndian.Uint32(b[1:5])
			if v > uint32(len(b)) {
				return malformedPGP("truncated subpacket")
			}
			n, hdr = int(v), 5
		}
		if n < 1 || n > len(b)-hdr {
			return malformedPGP("truncated subpacket")
		}
		typ, critical := b[hdr]&0x7f, b[hdr]&0x80 != 0
		val := b[hdr+1 : hdr+n]
		switch typ {
		case subCreationTime:
			if len(val) != 4 {
				return malformedPGP("malformed creation time")
			}
			if hashed {
				s.created = time.Unix(int64(binary.BigEndian.Uint32(val)), 0).UTC()
			}
		case subExpiration:
			if len(val) != 4 {
				return malformedPGP("malformed expiration time")
			}
			if hashed {
				s.expires = time.Duration(binary.BigEndian.Uint32(val)) * time.Second
			}
		case subIssuerFprint:
			if len(val) == 1+pgpFingerprintV4 && val[0] == 4 && (hashed || s.issuerFP == nil) {
				s.issuerFP = val[1:]
			}
		case subIssuerKeyID:
		default:
			if critical && hashed {
				return malformedPGP(fmt.Sprintf("unknown critical subpacket %d", typ))
			}
		}
		b = b[hdr+n:]
	}
	return nil
}

// verifyDetached checks that sigData is a valid signature over data by one of keys and
// returns its creation time. now rejects an expired signature. Errors wrap ErrSignature.
func verifyDetached(keys []pgpKey, data, sigData []byte, now time.Time) (time.Time, error) {
	sig, err := parseSignature(sigData)
	if err != nil {
		return time.Time{}, err
	}
	var key *pgpKey
	for i := range keys {
		if bytes.Equal(keys[i].fingerprint[:], sig.issuerFP) {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return time.Time{}, fmt.Errorf("%w: signed by key %s, which is not pinned", ErrSignature, strings.ToUpper(hex.EncodeToString(sig.issuerFP)))
	}
	if sig.expires > 0 && now.After(sig.created.Add(sig.expires)) {
		return time.Time{}, fmt.Errorf("%w: the signature has expired", ErrSignature)
	}
	h := sha512.New()
	h.Write(data)
	h.Write(sig.hashed)
	var trailer [6]byte
	trailer[0], trailer[1] = 4, 0xff
	binary.BigEndian.PutUint32(trailer[2:], uint32(len(sig.hashed)))
	h.Write(trailer[:])
	digest := h.Sum(nil)
	if digest[0] != sig.left16[0] || digest[1] != sig.left16[1] {
		return time.Time{}, fmt.Errorf("%w: the digest does not match", ErrSignature)
	}
	size := (key.pub.N.BitLen() + 7) / 8
	if len(sig.mpi) > size {
		return time.Time{}, fmt.Errorf("%w: malformed signature value", ErrSignature)
	}
	s := make([]byte, size)
	copy(s[size-len(sig.mpi):], sig.mpi)
	if err := rsa.VerifyPKCS1v15(key.pub, crypto.SHA512, digest, s); err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrSignature, err)
	}
	return sig.created, nil
}

// errEmbeddedKey: the embedded key file does not hold the pinned yt-dlp key.
var errEmbeddedKey = fmt.Errorf("%w: the embedded yt-dlp signing key is missing or wrong", ErrSignature)

// pinnedKeys returns the keys yt-dlp's signatures must verify with: every v4 RSA key of
// the armored blocks given, or, when none are given (nil), the embedded key whose
// fingerprint is ytdlpKeyFingerprint (errEmbeddedKey when it is missing).
func pinnedKeys(armored [][]byte) ([]pgpKey, error) {
	if armored == nil {
		keys, err := parseKeys(ytdlpKeyASC)
		if err != nil {
			return nil, fmt.Errorf("%w (%v)", errEmbeddedKey, err)
		}
		want, _ := hex.DecodeString(ytdlpKeyFingerprint)
		for _, k := range keys {
			if bytes.Equal(k.fingerprint[:], want) {
				return []pgpKey{k}, nil
			}
		}
		return nil, errEmbeddedKey
	}
	var all []pgpKey
	for _, a := range armored {
		keys, err := parseKeys(a)
		if err != nil {
			return nil, fmt.Errorf("tools: yt-dlp key: %w", err)
		}
		all = append(all, keys...)
	}
	if len(all) == 0 {
		return nil, errors.New("tools: no yt-dlp keys given")
	}
	return all, nil
}
