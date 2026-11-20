package filecheck

import (
	"bytes"
	"testing"
)

func TestSniffSignatures(t *testing.T) {
	pad := func(b []byte) []byte { return append(b, make([]byte, 600)...) }
	fat := pad([]byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 2, 0x01, 0, 0, 7})
	java := pad([]byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 52})
	tar := make([]byte, 600)
	copy(tar[257:], "ustar")
	pdfLate := append(bytes.Repeat([]byte{' '}, 1023), "%PDF-1.7"...)
	pdfTooLate := append(bytes.Repeat([]byte{0}, 1025), "%PDF-1.7"...)
	m2ts := make([]byte, 400)
	m2ts[4], m2ts[196], m2ts[388] = 0x47, 0x47, 0x47
	vhdx := pad([]byte("vhdxfile"))
	tests := []struct {
		name   string
		in     []byte
		id     string
		strong bool
		brand  string
	}{
		{"pe", peBytes(false), "pe", true, ""},
		{"mz without PE", pad([]byte("MZ")), "mz", false, ""},
		{"elf", pad([]byte("\x7fELF\x02\x01\x01")), "elf", true, ""},
		{"elf bad class", pad([]byte("\x7fELF\x03\x01\x01")), "", false, ""},
		{"macho", pad([]byte("\xcf\xfa\xed\xfe")), "macho", true, ""},
		{"macho fat", fat, "macho-fat", true, ""},
		{"java class", java, "java-class", true, ""},
		{"script", pad([]byte("#! /bin/sh\n")), "script", true, ""},
		{"msi", cfbBytes(9, clsidMSI), "msi", true, ""},
		{"msp", cfbBytes(9, clsidMSP), "msp", true, ""},
		{"mst", cfbBytes(9, clsidMST), "mst", true, ""},
		{"ole document", cfbBytes(9, make([]byte, 16)), "ole", true, ""},
		{"msi 4096-byte sectors", cfbBytes(12, clsidMSI), "msi", true, ""},
		{"lnk", lnkBytes(), "lnk", true, ""},
		{"xar", pad([]byte("xar!")), "xar", true, ""},
		{"deb", pad([]byte("!<arch>\ndebian-binary")), "deb", true, ""},
		{"ar", pad([]byte("!<arch>\nfoo")), "ar", false, ""},
		{"rpm", pad([]byte{0xED, 0xAB, 0xEE, 0xDB}), "rpm", true, ""},
		{"zip", pad([]byte("PK\x03\x04")), "zip", true, ""},
		{"crx", pad([]byte("Cr24")), "crx", true, ""},
		{"rar4", pad([]byte("Rar!\x1a\x07\x00")), "rar4", true, ""},
		{"rar5", pad([]byte("Rar!\x1a\x07\x01\x00")), "rar5", true, ""},
		{"7z", pad([]byte("7z\xbc\xaf\x27\x1c")), "7z", true, ""},
		{"gzip", pad([]byte("\x1f\x8b\x08")), "gzip", true, ""},
		{"xz", pad([]byte("\xfd7zXZ\x00")), "xz", true, ""},
		{"bzip2", pad([]byte("BZh9\x31\x41\x59\x26\x53\x59")), "bzip2", true, ""},
		{"zstd", pad([]byte{0x28, 0xB5, 0x2F, 0xFD}), "zstd", true, ""},
		{"tar", tar, "tar", true, ""},
		{"cab", pad([]byte("MSCF\x00\x00\x00\x00")), "cab", true, ""},
		{"iso hybrid", isoBytes(), "iso9660", true, ""},
		{"udf", udfBytes(), "udf", true, ""},
		{"vhdx", vhdx, "vhdx", true, ""},
		{"vhd dynamic", pad([]byte("conectix")), "vhd", true, ""},
		{"pdf", pad([]byte("%PDF-1.4")), "pdf", true, ""},
		{"pdf at 1023", pdfLate, "pdf", true, ""},
		{"pdf at 1025", pdfTooLate, "", false, ""},
		{"rtf", pad([]byte(`{\rtf1`)), "rtf", true, ""},
		{"mp4", pad([]byte("\x00\x00\x00\x20ftypisom")), "isobmff", true, ""},
		{"m4a", pad([]byte("\x00\x00\x00\x20ftypM4A ")), "isobmff", true, "audio"},
		{"heic", pad([]byte("\x00\x00\x00\x20ftypheic")), "isobmff", true, "heif"},
		{"avif", pad([]byte("\x00\x00\x00\x20ftypavif")), "isobmff", true, "avif"},
		{"mov", pad([]byte("\x00\x00\x00\x20ftypqt  ")), "isobmff", true, "qt"},
		{"3gp", pad([]byte("\x00\x00\x00\x20ftyp3gp5")), "isobmff", true, "3gp"},
		{"moov first", pad([]byte("\x00\x00\x01\x00moov")), "isobmff", true, ""},
		{"jp2", pad([]byte("\x00\x00\x00\x0cjP  \r\n\x87\n")), "jp2", true, ""},
		{"webm", pad([]byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x84webm")), "webm", true, ""},
		{"matroska", pad([]byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x88matroska")), "matroska", true, ""},
		{"wav", pad([]byte("RIFF\x00\x00\x00\x00WAVE")), "riff-wave", true, ""},
		{"avi", pad([]byte("RIFF\x00\x00\x00\x00AVI ")), "riff-avi", true, ""},
		{"webp", pad([]byte("RIFF\x00\x00\x00\x00WEBP")), "riff-webp", true, ""},
		{"ani", pad([]byte("RIFF\x00\x00\x00\x00ACON")), "riff-acon", true, ""},
		{"ogg", pad([]byte("OggS")), "ogg", true, ""},
		{"flac", pad([]byte("fLaC")), "flac", true, ""},
		{"asf", pad([]byte("\x30\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c")), "asf", true, ""},
		{"flv", pad([]byte("FLV\x01")), "flv", true, ""},
		{"mpeg-ps", pad([]byte{0, 0, 1, 0xBA}), "mpeg-ps", true, ""},
		{"ts", tsBytes(), "mpegts", true, ""},
		{"m2ts", m2ts, "m2ts", true, ""},
		{"id3", pad([]byte("ID3\x04")), "id3", true, ""},
		{"mpeg audio", pad([]byte{0xFF, 0xFB, 0x90, 0x00}), "mpeg-audio", false, ""},
		{"adts", pad([]byte{0xFF, 0xF1, 0x50, 0x80}), "adts", false, ""},
		{"png", pad([]byte("\x89PNG\r\n\x1a\n")), "png", true, ""},
		{"jpeg", pad([]byte{0xFF, 0xD8, 0xFF, 0xE0}), "jpeg", true, ""},
		{"gif", pad([]byte("GIF89a")), "gif", true, ""},
		{"gif is not ts", append([]byte("GIF89a"), tsBytes()[6:]...), "gif", true, ""},
		{"tiff", pad([]byte("II\x2a\x00")), "tiff", true, ""},
		{"psd", pad([]byte("8BPS")), "psd", true, ""},
		{"bmp", pad([]byte("BM\x00\x00\x00\x00\x00\x00\x00\x00")), "bmp", false, ""},
		{"ico", pad([]byte{0, 0, 1, 0, 1, 0}), "ico", false, ""},
		{"html", []byte("\xef\xbb\xbf\n  <!DOCTYPE html>\n<html><body>Sign in</body></html>"), "html", true, ""},
		{"html <a ", []byte("<a href=x>x</a>"), "html", true, ""},
		{"<a without terminator", []byte("<abbr>not a pattern of the table</abbr>"), "text", true, ""},
		{"xml with html", []byte("<?xml version=\"1.0\"?>\n<html xmlns=\"x\"></html>"), "html", true, ""},
		{"svg", []byte("<?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), "svg", true, ""},
		{"xml", []byte("<?xml version=\"1.0\"?><feed/>"), "xml", true, ""},
		{"svg after a comment", []byte("<!-- drawn by hand -->\n<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), "svg", true, ""},
		{"page with a comment", []byte("<!-- page -->\n<html><body><svg></svg></body></html>"), "html", true, ""},
		{"utf-16 text", []byte("\xff\xfeh\x00i\x00"), "text", true, ""},
		{"long text", repeatText("hello world ", 10), "text", true, ""},
		{"short whole text", []byte("Not found"), "text", true, ""},
		{"text cut mid-rune", append(repeatText("é", 40), 0xC3), "text", true, ""},
		{"binary junk", []byte{0x00, 0x01, 0x02, 0x03, 0x04}, "", false, ""},
		{"empty", nil, "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Sniff(tc.in)
			if c.ID != tc.id || c.Strong != tc.strong || c.Brand != tc.brand {
				t.Fatalf("Sniff = %+v, want id %q strong %v brand %q", c, tc.id, tc.strong, tc.brand)
			}
		})
	}
}

func TestSniffWeakTextInHead(t *testing.T) {
	head := repeatText("a", 20)
	if c := sniff(head, false); c.ID != "text" || c.Strong {
		t.Fatalf("20 bytes of a longer file = %+v, want weak text", c)
	}
	if c := sniff(repeatText("a", 10), false); c.ID != "" {
		t.Fatalf("10 bytes of a longer file = %+v, want nothing", c)
	}
}

func TestSniffDLL(t *testing.T) {
	if c := Sniff(peBytes(true)); c.ID != "pe" || !c.DLL {
		t.Fatalf("DLL = %+v", c)
	}
}

func TestSniffTail(t *testing.T) {
	tail := make([]byte, 1024)
	copy(tail[512:], "conectix")
	if got := sniffTail(tail); got != "vhd" {
		t.Fatalf("sniffTail = %q", got)
	}
}

func TestCFBRootBeyondHead(t *testing.T) {
	b := cfbBytes(12, clsidMSI)
	head := b[:512] // the root entry is in the second 4096-byte sector
	if got := cfbKindFromHead(head); got != "ole" {
		t.Fatalf("from a short head = %q, want ole until the entry is read", got)
	}
	if got := cfbKindAt(bytes.NewReader(b), int64(len(b)), head); got != "msi" {
		t.Fatalf("cfbKindAt = %q", got)
	}
}

func FuzzSniff(f *testing.F) {
	for _, s := range [][]byte{peBytes(false), cfbBytes(9, clsidMSI), lnkBytes(), tsBytes(), []byte("<html>"), []byte("\x1a\x45\xdf\xa3\x42\x82\xff")} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c := Sniff(b)
		if c.ID != "" && contentLabel(c.ID, c.Brand).noun == "" {
			t.Fatalf("id %q has no label", c.ID)
		}
	})
}
