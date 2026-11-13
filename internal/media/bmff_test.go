package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// box builds an ISO-BMFF box with a 32-bit size.
func box(typ string, payload ...[]byte) []byte {
	p := cat(payload...)
	return cat(u32(uint32(8+len(p))), []byte(typ), p)
}

func fullBox(typ string, version byte, payload ...[]byte) []byte {
	return box(typ, append([]byte{version, 0, 0, 0}, cat(payload...)...))
}

func hdlr(handler string) []byte {
	return fullBox("hdlr", 0, u32(0), []byte(handler), make([]byte, 12), []byte("handler\x00"))
}

func stsd(version byte, entries ...[]byte) []byte {
	return fullBox("stsd", version, u32(uint32(len(entries))), cat(entries...))
}

// visualEntry is a VisualSampleEntry: 8 bytes of SampleEntry and 70 of video fields.
func visualEntry(typ string, children ...[]byte) []byte {
	return box(typ, make([]byte, 78), cat(children...))
}

// audioEntry is an AudioSampleEntry whose version field is set (0 for ISO layout).
func audioEntry(typ string, version uint16, children ...[]byte) []byte {
	fields := make([]byte, 28)
	binary.BigEndian.PutUint16(fields[8:], version)
	switch version {
	case 1:
		fields = append(fields, make([]byte, 16)...)
	case 2:
		fields = append(fields, make([]byte, 36)...)
	}
	return box(typ, fields, cat(children...))
}

func ftyp() []byte { return box("ftyp", []byte("iso6"), u32(0), []byte("iso6cmfc")) }

// initSegment builds ftyp + moov with one track of the given handler and sample entry.
func initSegment(handler string, stsdVersion byte, entry []byte, moovExtra ...[]byte) []byte {
	trak := box("trak",
		fullBox("tkhd", 0, make([]byte, 80)),
		box("mdia",
			fullBox("mdhd", 0, make([]byte, 20)),
			hdlr(handler),
			box("minf",
				fullBox("vmhd", 0, make([]byte, 8)),
				box("stbl", stsd(stsdVersion, entry), fullBox("stts", 0, u32(0))))))
	return cat(ftyp(), box("moov", fullBox("mvhd", 0, make([]byte, 96)), trak, cat(moovExtra...)))
}

func avcC() []byte { return box("avcC", []byte{1, 0x64, 0, 0x1f, 0xff, 0xe0, 0}) }

func protection(scheme string) []byte {
	return box("sinf", box("frma", []byte("avc1")), fullBox("schm", 0, []byte(scheme), u32(0x10000)),
		box("schi", fullBox("tenc", 0, make([]byte, 20))))
}

func TestCheckInitClear(t *testing.T) {
	for name, data := range map[string][]byte{
		"video":           initSegment("vide", 0, visualEntry("avc1", avcC(), box("btrt", make([]byte, 12)))),
		"audio":           initSegment("soun", 0, audioEntry("mp4a", 0, fullBox("esds", 0, make([]byte, 20)))),
		"audio v1":        initSegment("soun", 0, audioEntry("mp4a", 1, fullBox("esds", 0, make([]byte, 20)))),
		"audio v2":        initSegment("soun", 0, audioEntry("lpcm", 2)),
		"text":            initSegment("text", 0, box("wvtt", make([]byte, 8), box("vttC", []byte("WEBVTT")))),
		"unknown handler": initSegment("zzzz", 0, box("abcd", make([]byte, 3))),
		"with mvex":       initSegment("vide", 0, visualEntry("avc1", avcC()), box("mvex", fullBox("trex", 0, make([]byte, 20)))),
		// A protection type inside a box that is not walked is just bytes.
		"opaque payload": initSegment("vide", 0, visualEntry("avc1"), box("udta", box("pssh", make([]byte, 4)))),
		// size 1 with a 64-bit largesize.
		"largesize": cat(ftyp(), box("moov"), u32(1), []byte("free"), binary.BigEndian.AppendUint64(nil, 24), make([]byte, 8)),
		// size 0: the last box extends to the end of the data.
		"size zero": cat(ftyp(), box("moov"), u32(0), []byte("mdat"), make([]byte, 100)),
		// A file prefix may end inside a trailing box that is not walked.
		"prefix":           cat(ftyp(), box("moov"), u32(1000), []byte("mdat"), make([]byte, 10)),
		"prefix largesize": cat(ftyp(), box("moov"), u32(1), []byte("mdat"), binary.BigEndian.AppendUint64(nil, 1<<40)),
		"prefix in header": cat(ftyp(), box("moov"), []byte{0, 0, 1}),
		"prefix in large":  cat(ftyp(), box("moov"), u32(1), []byte("mdat"), []byte{0, 0}),
		"uuid":             cat(ftyp(), box("moov"), box("uuid", make([]byte, 16), []byte("x"))),
		"data handler ref": initSegment("vide", 0, visualEntry("avc1"), box("minf", fullBox("hdlr", 0, []byte("dhlr"), []byte("alis")))),
		"moof clear":       cat(initSegment("vide", 0, visualEntry("avc1")), box("moof", fullBox("mfhd", 0, u32(1)), box("traf", fullBox("tfhd", 0, u32(1))))),
	} {
		if err := CheckInit(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCheckInitEncrypted(t *testing.T) {
	for name, data := range map[string][]byte{
		"pssh in moov":         initSegment("vide", 0, visualEntry("avc1"), fullBox("pssh", 0, make([]byte, 20), u32(0))),
		"pssh at top level":    cat(initSegment("vide", 0, visualEntry("avc1")), fullBox("pssh", 0, make([]byte, 20))),
		"encv":                 initSegment("vide", 0, visualEntry("encv", avcC(), protection("cenc"))),
		"enca":                 initSegment("soun", 0, audioEntry("enca", 0, protection("cbcs"))),
		"enct":                 initSegment("text", 0, box("enct", make([]byte, 8))),
		"encs":                 initSegment("meta", 0, box("encs", make([]byte, 8))),
		"encv unknown handler": initSegment("zzzz", 0, box("encv", make([]byte, 78))),
		"sinf in avc1":         initSegment("vide", 0, visualEntry("avc1", avcC(), protection("cens"))),
		"sinf in mp4a":         initSegment("soun", 0, audioEntry("mp4a", 0, protection("cenc"))),
		"sinf in QT v1 audio":  initSegment("soun", 0, audioEntry("mp4a", 1, protection("cenc"))),
		"sinf in QT v2 audio":  initSegment("soun", 0, audioEntry("mp4a", 2, protection("cenc"))),
		"tenc in moov":         initSegment("vide", 0, visualEntry("avc1"), fullBox("tenc", 0, make([]byte, 20))),
		"sinf in trak":         cat(ftyp(), box("moov", box("trak", protection("cenc")))),
		"senc in moof":         cat(initSegment("vide", 0, visualEntry("avc1")), box("moof", box("traf", fullBox("senc", 0, u32(0))))),
		"truncated pssh":       cat(initSegment("vide", 0, visualEntry("avc1")), u32(1000), []byte("pssh")),
		"largesize pssh":       cat(ftyp(), box("moov", u32(1), []byte("pssh"), binary.BigEndian.AppendUint64(nil, 16))),
	} {
		if err := CheckInit(data); !errors.Is(err, ErrEncrypted) {
			t.Errorf("%s: %v, want ErrEncrypted", name, err)
		}
	}
}

func TestCheckInitMalformed(t *testing.T) {
	nested := box("moov")
	for i := 0; i < MaxBoxDepth+2; i++ {
		nested = box("moov", nested)
	}
	for name, c := range map[string]struct {
		data []byte
		want error
	}{
		"empty":            {nil, ErrMalformed},
		"no moov":          {cat(ftyp(), box("free")), ErrMalformed},
		"zero bytes":       {make([]byte, 64), ErrMalformed},
		"ts packets":       {append([]byte{0x47, 0x40, 0x00, 0x10}, make([]byte, 184)...), ErrMalformed},
		"short header":     {[]byte{0, 0, 0}, ErrMalformed},
		"size below 8":     {cat(u32(4), []byte("moov")), ErrMalformed},
		"largesize < 16":   {cat(u32(1), []byte("moov"), binary.BigEndian.AppendUint64(nil, 12)), ErrMalformed},
		"largesize cut":    {cat(u32(1), []byte("moov"), []byte{0, 0}), ErrMalformed},
		"truncated moov":   {cat(ftyp(), u32(100), []byte("moov"), box("mvhd")), ErrMalformed},
		"truncated child":  {cat(ftyp(), box("moov", u32(64), []byte("trak"))), ErrMalformed},
		"child header cut": {cat(ftyp(), box("moov", []byte{0, 0, 0, 9})), ErrMalformed},
		"short stsd":       {cat(ftyp(), box("moov", box("trak", box("mdia", box("minf", box("stbl", box("stsd", []byte{0, 0}))))))), ErrMalformed},
		"stsd entry cut":   {cat(ftyp(), box("moov", box("stbl", stsd(0, cat(u32(50), []byte("avc1")))))), ErrMalformed},
		"small uuid":       {cat(ftyp(), box("moov"), box("uuid", make([]byte, 4))), ErrMalformed},
		"first box cut":    {cat(u32(100), []byte("ftyp"), make([]byte, 4)), ErrMalformed},
		"deep nesting":     {cat(ftyp(), nested), ErrTooLarge},
	} {
		if err := CheckInit(c.data); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// Structure inside sample entries is best effort: garbage there is not an error.
	garbled := initSegment("vide", 0, visualEntry("avc1", []byte{0, 0, 0, 3, 'x'}))
	if err := CheckInit(garbled); err != nil {
		t.Errorf("garbled sample entry children: %v", err)
	}
}

func TestSniff(t *testing.T) {
	ts := make([]byte, 376)
	ts[0], ts[188] = 0x47, 0x47
	for name, c := range map[string]struct {
		data []byte
		want Container
	}{
		"ts":         {ts, ContainerTS},
		"ts short":   {[]byte{0x47, 1, 2}, ContainerTS},
		"ts no sync": {append([]byte{0x47}, make([]byte, 200)...), ContainerUnknown},
		"fmp4 init":  {ftyp(), ContainerFMP4},
		"fmp4 media": {box("moof"), ContainerFMP4},
		"styp":       {box("styp"), ContainerFMP4},
		"webm":       {[]byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F}, ContainerWebM},
		"empty":      {nil, ContainerUnknown},
		"text":       {[]byte("WEBVTT\n"), ContainerUnknown},
	} {
		if got := Sniff(c.data); got != c.want {
			t.Errorf("%s: Sniff = %q, want %q", name, got, c.want)
		}
	}
}
