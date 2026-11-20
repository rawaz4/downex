package filecheck

import (
	"bytes"
	"encoding/binary"
	"unicode/utf8"
)

// HeadBytes is how many bytes from the start of a file the checks read (§9.3): the
// engine's Progress.Head carries exactly these.
const HeadBytes = 64 << 10

// TailBytes is how many bytes from the end of a file the final check reads: the ZIP
// end-of-central-directory record with the longest comment (22 + 65,535 bytes).
const TailBytes = 22 + 65535

// Content is what a file's bytes say it is (§9.2).
type Content struct {
	// ID is the signature's content id ("pe", "zip", "html"; "" = unknown).
	ID string
	// Brand refines some ids: the ISO-BMFF brand family ("audio", "qt", "3gp", "heif",
	// "avif", "craw"; "" = a video), and after the final check the container kind of a
	// ZIP ("ooxml", "odf", "epub", "apk", "jar", "appx", "ipa", "xpi", "nupkg", "vsix",
	// "whl"; "" = a plain archive).
	Brand string
	// Strong: the signature is specific enough to warn on (a weak match never causes
	// CAUTION or DANGER on its own).
	Strong bool
	// DLL: a PE image marked as a dynamic-link library.
	DLL bool
}

func strong(id string) Content { return Content{ID: id, Strong: true} }
func weak(id string) Content   { return Content{ID: id} }

// Sniff identifies head, the first bytes of a file (§9.2). A head shorter than
// HeadBytes is taken to be the whole file.
func Sniff(head []byte) Content {
	return sniff(head, len(head) < HeadBytes)
}

func sniff(b []byte, whole bool) Content {
	if c, ok := sniffDisk(b); ok { // before the boot code of a hybrid image is read as anything else
		return c
	}
	if c, ok := sniffProgram(b); ok {
		return c
	}
	if c, ok := sniffArchive(b); ok {
		return c
	}
	if c, ok := sniffDocument(b); ok {
		return c
	}
	if c, ok := sniffImage(b); ok { // GIF before the transport stream: both start with 0x47
		return c
	}
	if c, ok := sniffMedia(b); ok {
		return c
	}
	if len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF) { // UTF-16 text, not an MPEG frame sync
		return strong("text")
	}
	if c, ok := sniffWeakBinary(b); ok {
		return c
	}
	return sniffText(b, whole)
}

func at(b []byte, off int, sig string) bool {
	return off >= 0 && len(b) >= off+len(sig) && string(b[off:off+len(sig)]) == sig
}

func u16le(b []byte, off int) (uint16, bool) {
	if off < 0 || len(b) < off+2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(b[off:]), true
}

func u32le(b []byte, off int) (uint32, bool) {
	if off < 0 || len(b) < off+4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[off:]), true
}

func u32be(b []byte, off int) (uint32, bool) {
	if off < 0 || len(b) < off+4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[off:]), true
}

func sniffDisk(b []byte) (Content, bool) {
	// UDF (ECMA-167): a volume recognition sequence with BEA01 then NSR02/NSR03.
	if at(b, 32769, "BEA01") {
		for off := 32769 + 2048; off+5 <= len(b) && off < 32769+16*2048; off += 2048 {
			if at(b, off, "NSR02") || at(b, off, "NSR03") {
				return strong("udf"), true
			}
		}
	}
	for _, off := range [...]int{32769, 34817, 36865} { // ECMA-119 volume descriptors
		if at(b, off, "CD001") {
			return strong("iso9660"), true
		}
	}
	if at(b, 0, "vhdxfile") {
		return strong("vhdx"), true
	}
	if at(b, 0, "conectix") { // a dynamic disk's footer copy
		return strong("vhd"), true
	}
	return Content{}, false
}

var (
	lnkCLSID = []byte{0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}
	cfbMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
)

func sniffProgram(b []byte) (Content, bool) {
	switch {
	case at(b, 0, "MZ"):
		if lfa, ok := u32le(b, 0x3C); ok && lfa >= 0x40 && int64(lfa) <= int64(len(b))-24 && at(b, int(lfa), "PE\x00\x00") {
			c := strong("pe")
			if ch, ok := u16le(b, int(lfa)+22); ok && ch&0x2000 != 0 {
				c.DLL = true
			}
			return c, true
		}
		return weak("mz"), true
	case at(b, 0, "\x7fELF") && len(b) > 6 && (b[4] == 1 || b[4] == 2) && (b[5] == 1 || b[5] == 2) && b[6] == 1:
		return strong("elf"), true
	case at(b, 0, "\xfe\xed\xfa\xce"), at(b, 0, "\xfe\xed\xfa\xcf"), at(b, 0, "\xce\xfa\xed\xfe"), at(b, 0, "\xcf\xfa\xed\xfe"):
		return strong("macho"), true
	case at(b, 0, "\xca\xfe\xba\xbe"), at(b, 0, "\xca\xfe\xba\xbf"):
		n, ok1 := u32be(b, 4)
		cpu, ok2 := u32be(b, 8)
		if ok1 && ok2 && n >= 1 && n <= 30 {
			switch cpu & 0x00FFFFFF {
			case 7, 12, 18: // x86, ARM, PowerPC
				return strong("macho-fat"), true
			}
		}
		if b[3] == 0xbe && len(b) >= 8 && binary.BigEndian.Uint16(b[6:]) >= 45 {
			return strong("java-class"), true
		}
		return Content{}, false
	case at(b, 0, "#!"):
		i := 2
		for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
			i++
		}
		if i < len(b) && b[i] == '/' {
			return strong("script"), true
		}
		return Content{}, false
	case bytes.HasPrefix(b, cfbMagic):
		return strong(cfbKindFromHead(b)), true
	case len(b) >= 20 && binary.LittleEndian.Uint32(b) == 0x4C && bytes.Equal(b[4:20], lnkCLSID):
		return strong("lnk"), true
	case at(b, 0, "xar!"):
		return strong("xar"), true
	case at(b, 0, "!<arch>\n"):
		if at(b, 8, "debian-binary") {
			return strong("deb"), true
		}
		return weak("ar"), true
	case at(b, 0, "\xed\xab\xee\xdb"):
		return strong("rpm"), true
	}
	return Content{}, false
}

func sniffArchive(b []byte) (Content, bool) {
	switch {
	case at(b, 0, "PK\x03\x04"), at(b, 0, "PK\x05\x06"), at(b, 0, "PK\x07\x08"):
		return strong("zip"), true
	case at(b, 0, "Cr24"):
		return strong("crx"), true
	case at(b, 0, "Rar!\x1a\x07\x01\x00"):
		return strong("rar5"), true
	case at(b, 0, "Rar!\x1a\x07\x00"):
		return strong("rar4"), true
	case at(b, 0, "7z\xbc\xaf\x27\x1c"):
		return strong("7z"), true
	case at(b, 0, "\x1f\x8b\x08"):
		return strong("gzip"), true
	case at(b, 0, "\xfd7zXZ\x00"):
		return strong("xz"), true
	case at(b, 0, "BZh") && len(b) >= 10 && b[3] >= '1' && b[3] <= '9' && at(b, 4, "\x31\x41\x59\x26\x53\x59"):
		return strong("bzip2"), true
	case at(b, 0, "\x28\xb5\x2f\xfd"):
		return strong("zstd"), true
	case at(b, 257, "ustar"):
		return strong("tar"), true
	case at(b, 0, "MSCF\x00\x00\x00\x00"):
		return strong("cab"), true
	}
	return Content{}, false
}

func sniffDocument(b []byte) (Content, bool) {
	if at(b, 0, `{\rtf`) {
		return strong("rtf"), true
	}
	if i := bytes.Index(b[:min(len(b), 1023+5)], []byte("%PDF-")); i >= 0 && i <= 1023 {
		return strong("pdf"), true
	}
	return Content{}, false
}

func sniffImage(b []byte) (Content, bool) {
	switch {
	case at(b, 0, "\x89PNG\r\n\x1a\n"):
		return strong("png"), true
	case at(b, 0, "\xff\xd8\xff"):
		return strong("jpeg"), true
	case at(b, 0, "GIF87a"), at(b, 0, "GIF89a"):
		return strong("gif"), true
	case at(b, 0, "II\x2a\x00"), at(b, 0, "MM\x00\x2a"):
		return strong("tiff"), true
	case at(b, 0, "8BPS"):
		return strong("psd"), true
	case at(b, 0, "\x00\x00\x00\x0cjP  \r\n\x87\n"):
		return strong("jp2"), true
	}
	return Content{}, false
}

var bmffTypes = map[string]bool{
	"ftyp": true, "styp": true, "moov": true, "moof": true, "mdat": true, "free": true,
	"skip": true, "wide": true, "sidx": true, "pnot": true, "uuid": true,
}

func sniffMedia(b []byte) (Content, bool) {
	if size, ok := u32be(b, 0); ok && (size >= 8 || size == 1) && len(b) >= 8 && bmffTypes[string(b[4:8])] {
		c := strong("isobmff")
		if string(b[4:8]) == "ftyp" && len(b) >= 12 {
			c.Brand = bmffBrand(string(b[8:12]))
		}
		return c, true
	}
	switch {
	case at(b, 0, "\x1a\x45\xdf\xa3"):
		if ebmlDocType(b[:min(len(b), 64)]) == "webm" {
			return strong("webm"), true
		}
		return strong("matroska"), true
	case at(b, 0, "RIFF") && len(b) >= 12:
		switch string(b[8:12]) {
		case "WAVE":
			return strong("riff-wave"), true
		case "AVI ":
			return strong("riff-avi"), true
		case "WEBP":
			return strong("riff-webp"), true
		case "ACON":
			return strong("riff-acon"), true
		}
		return Content{}, false
	case at(b, 0, "OggS"):
		return strong("ogg"), true
	case at(b, 0, "fLaC"):
		return strong("flac"), true
	case at(b, 0, "\x30\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c"):
		return strong("asf"), true
	case at(b, 0, "FLV\x01"):
		return strong("flv"), true
	case at(b, 0, "\x00\x00\x01\xba"):
		return strong("mpeg-ps"), true
	case len(b) > 376 && b[0] == 0x47 && b[188] == 0x47 && b[376] == 0x47:
		return strong("mpegts"), true
	case len(b) > 388 && b[4] == 0x47 && b[196] == 0x47 && b[388] == 0x47:
		return strong("m2ts"), true
	case at(b, 0, "ID3") && len(b) > 3 && b[3] >= 2 && b[3] <= 4:
		return strong("id3"), true
	}
	return Content{}, false
}

// bmffBrand maps an ftyp major brand to its family (§9.2).
func bmffBrand(brand string) string {
	switch brand {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return "heif"
	case "avif", "avis":
		return "avif"
	case "M4A ", "M4B ", "M4P ", "F4A ":
		return "audio"
	case "qt  ":
		return "qt"
	case "crx ":
		return "craw"
	}
	if len(brand) == 4 && (brand[:3] == "3gp" || brand[:3] == "3g2") {
		return "3gp"
	}
	return ""
}

// ebmlDocType returns the DocType string of an EBML header within b ("" = none).
func ebmlDocType(b []byte) string {
	i := bytes.Index(b, []byte{0x42, 0x82})
	if i < 0 || i+2 >= len(b) {
		return ""
	}
	j := i + 2
	first := b[j]
	n := 1
	for mask := byte(0x80); n <= 8 && first&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 || j+n > len(b) {
		return ""
	}
	size := int(first & (0xFF >> n))
	for k := 1; k < n; k++ {
		size = size<<8 | int(b[j+k])
	}
	j += n
	if size < 0 || j+size > len(b) {
		return ""
	}
	return string(b[j : j+size])
}

func sniffWeakBinary(b []byte) (Content, bool) {
	switch {
	case len(b) >= 4 && b[0] == 0xFF && (b[1] == 0xF1 || b[1] == 0xF9):
		return weak("adts"), true
	case len(b) >= 4 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 && (b[1]>>1)&3 != 0 && b[2]>>4 != 15 && (b[2]>>2)&3 != 3:
		return weak("mpeg-audio"), true
	case at(b, 0, "BM") && at(b, 6, "\x00\x00\x00\x00"):
		return weak("bmp"), true
	case at(b, 0, "\x00\x00\x01\x00") && len(b) >= 6 && b[4] >= 1 && b[5] == 0:
		return weak("ico"), true
	}
	return Content{}, false
}

// htmlPatterns are the HTML patterns of WHATWG MIME Sniffing, each followed by a space
// or ">" (§9.2).
var htmlPatterns = []string{
	"<!DOCTYPE HTML", "<HTML", "<HEAD", "<SCRIPT", "<IFRAME", "<H1", "<DIV", "<FONT",
	"<TABLE", "<A", "<STYLE", "<TITLE", "<B", "<BODY", "<BR", "<P", "<!--",
}

func sniffText(b []byte, whole bool) Content {
	if !plainText(b) {
		return Content{}
	}
	s := bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	s = bytes.TrimLeft(s, "\t\n\f\r ")
	if afterComments(s) { // a drawing that starts with comments (a licence), not a page
		return strong("svg")
	}
	for _, p := range htmlPatterns {
		if len(s) > len(p) && bytes.EqualFold(s[:len(p)], []byte(p)) && (s[len(p)] == ' ' || s[len(p)] == '>') {
			return strong("html")
		}
	}
	lower := bytes.ToLower(s[:min(len(s), 1024)])
	switch {
	case bytes.HasPrefix(lower, []byte("<?xml")):
		switch {
		case bytes.Contains(lower, []byte("<html")):
			return strong("html")
		case bytes.Contains(lower, []byte("<svg")):
			return strong("svg")
		}
		return strong("xml")
	case bytes.HasPrefix(lower, []byte("<svg")), bytes.HasPrefix(lower, []byte("<!doctype svg")):
		return strong("svg")
	}
	switch {
	case len(b) >= 64, whole && len(b) > 0:
		return strong("text")
	case len(b) >= 16:
		return weak("text")
	}
	return Content{}
}

// afterComments reports text that starts with one or more complete "<!-- … -->"
// comments (within the head) followed, after whitespace, by "<svg" or "<!DOCTYPE svg".
func afterComments(s []byte) bool {
	if !bytes.HasPrefix(s, []byte("<!--")) {
		return false
	}
	for bytes.HasPrefix(s, []byte("<!--")) {
		end := bytes.Index(s[4:], []byte("-->"))
		if end < 0 {
			return false
		}
		s = bytes.TrimLeft(s[4+end+3:], "\t\n\f\r ")
	}
	lower := bytes.ToLower(s[:min(len(s), 14)])
	return bytes.HasPrefix(lower, []byte("<svg")) || bytes.HasPrefix(lower, []byte("<!doctype svg"))
}

// plainText: valid UTF-8 (a last rune cut by the head allowed) without NUL and without
// C0 controls other than tab, line feed, form feed, carriage return and escape.
func plainText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\f' && c != '\r' && c != 0x1B {
			return false
		}
	}
	if utf8.Valid(b) {
		return true
	}
	i := len(b) - 1
	for i > 0 && len(b)-i < utf8.UTFMax && !utf8.RuneStart(b[i]) {
		i--
	}
	return utf8.Valid(b[:i]) && !utf8.FullRune(b[i:])
}

// sniffTail identifies content from a file's last bytes: the fixed VHD footer.
func sniffTail(tail []byte) string {
	if len(tail) >= 512 && at(tail, len(tail)-512, "conectix") {
		return "vhd"
	}
	return ""
}
