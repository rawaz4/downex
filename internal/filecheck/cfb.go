package filecheck

import (
	"bytes"
	"encoding/binary"
	"io"
)

// Root storage CLSIDs of Windows Installer files in a compound file ([MS-CFB]): the
// package, patch and transform kinds. Any other CLSID is an OLE document.
var (
	clsidMSI = []byte{0x84, 0x10, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}
	clsidMSP = []byte{0x86, 0x10, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}
	clsidMST = []byte{0x82, 0x10, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}
)

// cfbRootOffset returns the offset of the root directory entry's CLSID (§9.2): the
// directory starts at sector (first directory sector + 1) << sector shift.
func cfbRootOffset(header []byte) (int64, bool) {
	if len(header) < 0x34 || !bytes.HasPrefix(header, cfbMagic) {
		return 0, false
	}
	shift := binary.LittleEndian.Uint16(header[0x1E:])
	if shift != 9 && shift != 12 {
		return 0, false
	}
	sect := binary.LittleEndian.Uint32(header[0x30:])
	if sect >= 0xFFFFFFFA { // a special value, not a sector
		return 0, false
	}
	return (int64(sect)+1)<<shift + 0x50, true
}

func cfbKind(clsid []byte) string {
	switch {
	case bytes.Equal(clsid, clsidMSI):
		return "msi"
	case bytes.Equal(clsid, clsidMSP):
		return "msp"
	case bytes.Equal(clsid, clsidMST):
		return "mst"
	}
	return "ole"
}

// cfbKindFromHead tells an installer from an OLE document when the root entry lies
// in head; otherwise it says "ole" and the final check reads the entry (cfbKindAt).
func cfbKindFromHead(head []byte) string {
	off, ok := cfbRootOffset(head)
	if !ok || off+16 > int64(len(head)) {
		return "ole"
	}
	return cfbKind(head[off : off+16])
}

// cfbKindAt reads the root entry's CLSID from r (one directory sector at most).
func cfbKindAt(r io.ReaderAt, size int64, head []byte) string {
	off, ok := cfbRootOffset(head)
	if !ok || off+16 > size {
		return "ole"
	}
	if off+16 <= int64(len(head)) {
		return cfbKind(head[off : off+16])
	}
	clsid := make([]byte, 16)
	if _, err := r.ReadAt(clsid, off); err != nil {
		return "ole"
	}
	return cfbKind(clsid)
}
